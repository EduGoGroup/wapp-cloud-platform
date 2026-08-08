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
	reg               *prometheus.Registry
	httpRequests      *prometheus.CounterVec
	httpDuration      *prometheus.HistogramVec
	rateLimitHits     *prometheus.CounterVec
	receipts          *prometheus.CounterVec
	reactiveBlocks    *prometheus.CounterVec
	webhookDeliveries *prometheus.CounterVec
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
			Help: "Entregas del worker del puente CRM (Plan 042 · T3.4), por resultado (delivered|failed|dead).",
		}, []string{"status"}),
	}
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.httpRequests, m.httpDuration, m.rateLimitHits, m.receipts, m.reactiveBlocks, m.webhookDeliveries,
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
// (2xx), "failed" (reintentará) o "dead" (reintentos agotados, terminal).
// Cardinalidad FIJA (tres valores); el tenant NO se etiqueta (misma regla dura
// del paquete). Se pasa como callback al Worker (que NO importa este paquete:
// mismo desacoplo que Receipt/FlowReactiveBlocked).
func (m *Metrics) WebhookDelivery(status string) {
	if m == nil {
		return
	}
	m.webhookDeliveries.WithLabelValues(status).Inc()
}
