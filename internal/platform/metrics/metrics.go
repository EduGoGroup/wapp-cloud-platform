// Package metrics expone la observabilidad Prometheus de la Plataforma Cloud
// (Plan 018 · T10, R11): contadores/latencia de las peticiones HTTP (admin y API
// pública), acuses de login ok/fallido, hits de rate-limit y acuses persistidos.
//
// REGLA DURA (INV-5, zero-knowledge): CERO PII en las etiquetas. La ruta se
// etiqueta con el PATRÓN del ServeMux (p. ej. "POST /api/v1/flows/{id}/start"),
// NUNCA con el valor real del path (que podría portar ids); el tenant NO se
// etiqueta (alta cardinalidad + aislamiento). Las métricas viven en un registry
// PROPIO inyectado (no el default global) para que cada arranque/test sea
// independiente y no haya doble-registro.
package metrics

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics agrupa el registry y los colectores de la plataforma. Sus métodos son
// NIL-SAFE: un *Metrics nil no registra nada (simplifica los tests que no montan
// observabilidad). Se construye una vez en el arranque y se comparte entre los
// dos listeners HTTP y el sink de acuses.
type Metrics struct {
	reg                *prometheus.Registry
	httpRequests       *prometheus.CounterVec
	httpDuration       *prometheus.HistogramVec
	rateLimitHits      *prometheus.CounterVec
	receipts           *prometheus.CounterVec
	reactiveBlocks     *prometheus.CounterVec
	webhookDeliveries  *prometheus.CounterVec
	flowEventLifecycle *prometheus.CounterVec
}

// New construye el registry propio y registra los colectores. Incluye los
// colectores estándar (Go runtime + proceso) para dar visibilidad de recursos.
func New() *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{
		reg: reg,
		httpRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "wapp_http_requests_total",
			Help: "Total de peticiones HTTP por listener, ruta (patrón), método y código.",
		}, []string{"listener", "route", "method", "status"}),
		httpDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "wapp_http_request_duration_seconds",
			Help:    "Latencia de las peticiones HTTP por listener, ruta (patrón) y método.",
			Buckets: prometheus.DefBuckets,
		}, []string{"listener", "route", "method"}),
		rateLimitHits: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "wapp_ratelimit_hits_total",
			Help: "Total de peticiones rechazadas por rate-limit, por ámbito (public).",
		}, []string{"scope"}),
		receipts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "wapp_receipts_total",
			Help: "Total de acuses persistidos por estado (delivered|read).",
		}, []string{"status"}),
		reactiveBlocks: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "wapp_flow_reactive_blocked_total",
			Help: "Entrantes que NO entraron al motor reactivo, por motivo (passive|self_loop|rate_limit).",
		}, []string{"reason"}),
		webhookDeliveries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "wapp_webhook_deliveries_total",
			Help: "Entregas del worker del puente CRM (Plan 042 · T3.4), por resultado (delivered|failed|dead|claim_lost).",
		}, []string{"status"}),
		flowEventLifecycle: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "wapp_flow_event_lifecycle_total",
			Help: "Efectos de ciclo de vida del evento conversacional leídos del outbox flow_events (Plan 043 · T6.5, MD-043.17), por nombre de efecto y tipo de evento (event_kind = payload->>'kind', NUNCA la columna kind).",
		}, []string{"name", "event_kind"}),
	}
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.httpRequests, m.httpDuration, m.rateLimitHits, m.receipts, m.reactiveBlocks, m.webhookDeliveries,
		m.flowEventLifecycle,
	)
	return m
}

// PromHandler devuelve el handler de /metrics sobre el registry propio.
func (m *Metrics) PromHandler() http.Handler {
	if m == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		})
	}
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{Registry: m.reg})
}

// statusRecorder captura el código de estado escrito por el handler (para la
// etiqueta status), sin leer el cuerpo.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// InstrumentHTTP envuelve un mux ENTERO (no cada ruta) y registra petición +
// latencia usando el PATRÓN del ServeMux, que Go fija en r.Pattern DURANTE el
// ruteo (accesible tras next.ServeHTTP). El patrón es de baja cardinalidad y no
// porta PII (los {id} van como plantilla). listener distingue "admin" de
// "public".
//
// El contador wapp_auth_logins_total se retiró con el login (identity Plan 003 ·
// Ola 5): wApp ya no valida credenciales, así que una métrica de logins aquí
// marcaría cero para siempre — y un cero que parece un dato es peor que la
// ausencia del dato. Quien mida logins los mide en identity-core.
func (m *Metrics) InstrumentHTTP(listener string, next http.Handler) http.Handler {
	if m == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sr := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(sr, r)
		elapsed := time.Since(start).Seconds()
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		status := strconv.Itoa(sr.status)
		m.httpRequests.WithLabelValues(listener, route, r.Method, status).Inc()
		m.httpDuration.WithLabelValues(listener, route, r.Method).Observe(elapsed)
	})
}

// RateLimitHit registra un rechazo por rate-limit en el ámbito dado (public).
func (m *Metrics) RateLimitHit(scope string) {
	if m == nil {
		return
	}
	m.rateLimitHits.WithLabelValues(scope).Inc()
}

// Receipt registra un acuse persistido por estado (delivered|read). Se pasa como
// callback al sink de acuses (que NO importa este paquete: queda desacoplado).
func (m *Metrics) Receipt(status string) {
	if m == nil {
		return
	}
	m.receipts.WithLabelValues(status).Inc()
}

// FlowReactiveBlocked registra un entrante que NO llegó al motor reactivo, por
// motivo (passive|self_loop|rate_limit). Responde la pregunta que el operador hace
// de verdad —«¿por qué no contesta?»— sin depender del nivel debug: los tres
// cortes son estados/decisiones, no incidencias, y a INFO inundarían el log (en el
// e2e del 2026-08-06 el corte por passive saltaba ~2.000 veces/hora).
//
// El motivo es de cardinalidad FIJA (tres valores). NO se etiqueta la sesión ni el
// tenant: misma regla dura del paquete (cero PII, cardinalidad acotada). Para saber
// QUÉ sesión, el runtime deja una línea a INFO la primera vez que corta cada una.
//
// Se pasa como callback al runtime de flujos (que NO importa este paquete: queda
// desacoplado, igual que el sink de acuses).
func (m *Metrics) FlowReactiveBlocked(reason string) {
	if m == nil {
		return
	}
	m.reactiveBlocks.WithLabelValues(reason).Inc()
}

// WebhookDelivery registra el resultado de UN intento de entrega del worker del
// puente CRM (Plan 042 · T3.4, visibilidad de dead): status es "delivered"
// (2xx), "failed" (reintentará), "dead" (reintentos agotados, terminal) o
// "claim_lost" (Plan 042 · Ola 3.1: el lease del claim expiró antes de que este
// worker cerrara la fila, y la resolverá quien la reclamó después — un valor
// sostenidamente > 0 dice que ClaimLease se quedó corto para la carga real).
// Cardinalidad FIJA (cuatro valores); el tenant NO se etiqueta (misma regla dura
// del paquete). Se pasa como callback al Worker (que NO importa este paquete:
// mismo desacoplo que Receipt/FlowReactiveBlocked).
func (m *Metrics) WebhookDelivery(status string) {
	if m == nil {
		return
	}
	m.webhookDeliveries.WithLabelValues(status).Inc()
}

// FlowEventLifecycle registra el DELTA de una vuelta del colector incremental de
// flow_events (Plan 043 · T6.5, MD-043.17: primer consumidor de PRODUCCIÓN del
// outbox append-only, no un assert de test). delta es el count(*) que devolvió
// esa vuelta para (name, event_kind) — NUNCA un total absoluto — así que se
// ACUMULA (Add), igual que el resto de contadores del paquete.
//
// event_kind es SIEMPRE payload->>'kind' (menu|cart|survey|media|…), JAMÁS la
// columna `kind` de la fila ("persist"|"event"): la firma del método fuerza el
// vocabulario correcto por construcción — quien la llama no tiene forma de
// pasar la columna equivocada sin nombrar mal la variable a propósito. Ver la
// COLISIÓN DE VOCABULARIO documentada en
// internal/flujos/runtime/event_effects.go.
//
// Cardinalidad: `name` son los efectos de ciclo de vida del evento (hoy seis,
// más los que el motor añada — este método no los enumera, así que un séptimo
// efecto nuevo no necesita tocar este paquete) × `event_kind` (cuatro tipos de
// evento). NO cardinalidad por tenant (misma regla dura del paquete, INV-5). Se
// pasa como callback al Colector (que NO importa este paquete: mismo desacoplo
// que Receipt/FlowReactiveBlocked/WebhookDelivery — ver
// internal/platform/metrics/flowlifecycle).
func (m *Metrics) FlowEventLifecycle(name, eventKind string, delta float64) {
	if m == nil {
		return
	}
	m.flowEventLifecycle.WithLabelValues(name, eventKind).Add(delta)
}

// --- Pool de conexiones a PostgreSQL (Plan 050 · Ola 4 · T4.3) --------------

// RegisterDBStats publica las seis series del pool `database/sql` (sql.DBStats)
// sobre el registry propio. Devuelve error solo si el registry lo rechaza por
// algo distinto de "ya estaba" (ver más abajo), para que el arranque decida.
//
// POR QUÉ ES UN MÉTODO APARTE y no un parámetro de New(): es orden de arranque,
// no estética. New() se construye antes de que exista el *sql.DB (bootstrap abre
// la base después, porque el logger y las métricas tienen que estar vivos para
// poder contar un fallo de conexión), así que el pool no puede inyectarse en el
// constructor. Es la PRIMERA vez en este repo que algo se registra DESPUÉS de
// New(), y es seguro: prometheus.Registry protege su estado con un mutex propio
// (Register/Gather son concurrent-safe por contrato), de modo que registrar
// mientras un scrape está en vuelo es legal. La alternativa —retrasar New()
// hasta después de la base— dejaría el fallo de conexión sin instrumentar.
//
// POR QUÉ GaugeFunc/CounterFunc y NO un ticker de refresco (el molde de
// metrics/flowlifecycle): lo que T5.5 va a buscar es el PICO de InUse y de
// WaitCount bajo carga, y un valor refrescado cada N segundos se lo pierde entre
// muestra y muestra. Estas funciones leen db.Stats() EN EL MOMENTO DEL SCRAPE;
// db.Stats() es una copia de struct tomada bajo el mutex del pool: barata y
// segura de leer en caliente.
//
// POR QUÉ NO collectors.NewDBStatsCollector, que ya existe en client_golang:
// prefija sus series con "go_sql_" sin ninguna opción de cambiarlo, y aquí el
// prefijo de la casa es "wapp_" (el criterio de T4.3 se verifica con
// `grep wapp_db_`).
//
// Nil-safe como el resto del paquete: con m == nil o db == nil no registra nada
// y el scrape sigue sirviendo las demás series (un despliegue sin base no debe
// quedarse sin /metrics).
//
// DOBLE LLAMADA: el llamante previsto es UNO SOLO (internal/bootstrap, justo
// después de abrir la base). Aun así, una segunda llamada NO revienta: el
// AlreadyRegisteredError del registry se trata como "ya estaba" y se ignora, de
// forma que un cable duplicado por descuido no tumba el arranque de la
// plataforma por una métrica. Cualquier otro error sí sube al llamante.
//
// CERO datos de negocio (INV-5 del paquete, y además /metrics se sirve SIN
// autenticar en :8100 sobre IP pública — deuda conocida, dueño Plan 026): las
// seis series son números del pool, sin etiquetas de ningún tipo. No hay tenant,
// ni sesión, ni DSN, ni nombre de base.
func (m *Metrics) RegisterDBStats(db *sql.DB) error {
	if m == nil || db == nil {
		return nil
	}
	for _, c := range dbStatsCollectors(db) {
		if err := m.reg.Register(c); err != nil {
			var already prometheus.AlreadyRegisteredError
			if errors.As(err, &already) {
				continue // segunda llamada: idempotente, no tumba el arranque.
			}
			return fmt.Errorf("metrics: registrar las series del pool de BD: %w", err)
		}
	}
	return nil
}

// dbStatsCollectors arma las seis series de sql.DBStats. Está aparte de
// RegisterDBStats para que el conjunto se lea de un vistazo (nombre, tipo,
// campo) y para que añadir una séptima no toque la lógica de registro.
//
// Los tres primeros son MONÓTONOS (nunca decrecen mientras viva el proceso), así
// que son contadores aunque el criterio del plan los liste sin sufijo _total:
// declararlos gauge haría que rate() mintiera. Los tres últimos suben y bajan
// con el tráfico: gauges.
func dbStatsCollectors(db *sql.DB) []prometheus.Collector {
	return []prometheus.Collector{
		prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: "wapp_db_wait_count",
			Help: "Veces que una goroutine tuvo que ESPERAR a que se liberara una conexión del pool porque las MaxOpenConns estaban ocupadas. Junto con wapp_db_wait_duration_seconds es la PRUEBA DIRECTA de que el pool se quedó corto — no una inferencia por latencia. Nadie la había medido en este proyecto hasta el Plan 050 · T4.3; la lee T5.5 para decidir si DEUDA-050.2 (el cuello mudado del head-of-line al pool) exige subir MaxOpenConns en T4.6. Un cero sostenido bajo carga dice que el cuello NO está aquí.",
		}, func() float64 { return float64(db.Stats().WaitCount) }),
		prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: "wapp_db_wait_duration_seconds",
			Help: "Tiempo TOTAL acumulado esperando por una conexión del pool. Es la otra mitad de la prueba de T5.5: wapp_db_wait_count dice cuántas veces se esperó y esta cuánto dolió (el cociente da la espera media, y su rate() la fracción de tiempo de proceso que se va en el pool). En segundos por la convención de unidades base de Prometheus, igual que wapp_http_request_duration_seconds.",
		}, func() float64 { return db.Stats().WaitDuration.Seconds() }),
		prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: "wapp_db_max_idle_closed",
			Help: "Conexiones cerradas por haber excedido MaxIdleConns. Distingue dos diagnósticos que se confunden: si sube a la vez que wapp_db_wait_count, el pool está ABRIENDO y CERRANDO conexiones en bucle (MaxIdleConns demasiado bajo para el tráfico), y eso se arregla tocando el idle, no el max_open.",
		}, func() float64 { return float64(db.Stats().MaxIdleClosed) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "wapp_db_in_use",
			Help: "Conexiones del pool en uso AHORA MISMO (en el instante del scrape, sin refresco diferido: un ticker se perdería el pico, que es justo lo que T5.5 busca). Comparada con wapp_db_max_open dice cuánta cabecera queda antes de que empiece la espera.",
		}, func() float64 { return float64(db.Stats().InUse) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "wapp_db_idle",
			Help: "Conexiones abiertas y ociosas en el instante del scrape. in_use + idle es el total de conexiones vivas contra PostgreSQL, el número que le importa al límite del proveedor (Neon).",
		}, func() float64 { return float64(db.Stats().Idle) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "wapp_db_max_open",
			Help: "Techo configurado de conexiones simultáneas (SetMaxOpenConns). Es un gauge y no una constante a propósito: se publica para que el dato del pool y su límite se lean del MISMO scrape, sin tener que ir a buscar la config del despliegue para interpretar in_use.",
		}, func() float64 { return float64(db.Stats().MaxOpenConnections) }),
	}
}
