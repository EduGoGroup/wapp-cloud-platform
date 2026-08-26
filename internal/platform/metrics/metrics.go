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
	"sync"
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
	cartMatch          *prometheus.CounterVec
	llmDegradacion     *prometheus.CounterVec
	autoreplyStreak    prometheus.Histogram

	// Fuente del gauge wapp_flow_autoreply_streak_max (Plan 049 · Opción A).
	// Aquí la dependencia va AL REVÉS que en el resto del paquete: los demás
	// colectores los EMPUJA el runtime por callback (Receipt,
	// FlowReactiveBlocked…), pero un gauge se TIRA en el scrape, así que
	// metrics tiene que poder preguntarle al runtime. Se resuelve con una
	// función inyectada, no con un import: el motor sigue sin conocer
	// prometheus.
	//
	// Va bajo RWMutex y NO bajo atomic.Pointer[func() int] a propósito: el
	// atómico obligaría a guardar un puntero A una variable de tipo función
	// (&fn) y a desreferenciarlo en cada scrape para poder distinguir el "aún
	// no inyectada", lo que es más ruidoso de leer sin ganar nada medible aquí
	// — hay UNA escritura en todo el arranque y una lectura cada intervalo de
	// scrape (segundos), o sea contención nula. El mutex sí hace falta: New()
	// corre en el arranque y el scrape corre concurrentemente, así que
	// escribir el campo sin sincronizar sería una carrera de datos real (la
	// caza `go test -race`).
	streakMaxMu     sync.RWMutex
	streakMaxSource func() int
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
			Help: "Entrantes que NO entraron al motor reactivo, por motivo (passive|self_loop|rate_limit|saturation). Los tres primeros son cortes DELIBERADOS (política: rol, números propios, cupo de auto-respuestas) y no son un error; saturation es una PÉRDIDA (el mensaje debía entrar y se descartó sin cupo en el pool) ⇒ es el único motivo que señala degradación del servicio y el único sobre el que alarmar.",
		}, []string{"reason"}),
		webhookDeliveries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "wapp_webhook_deliveries_total",
			Help: "Entregas del worker del puente CRM (Plan 042 · T3.4), por resultado (delivered|failed|dead|claim_lost).",
		}, []string{"status"}),
		flowEventLifecycle: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "wapp_flow_event_lifecycle_total",
			Help: "Efectos de ciclo de vida del evento conversacional leídos del outbox flow_events (Plan 043 · T6.5, MD-043.17), por nombre de efecto, tipo de evento (event_kind = payload->>'kind', NUNCA la columna kind) y causa (reason = payload->>'reason', solo poblado en event_escaped: owner_flow_finished|orphan_menu|client_escape; vacío en el resto y en las filas anteriores al Plan 053 · T4.1).",
		}, []string{"name", "event_kind", "reason"}),
		cartMatch: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "wapp_cart_match_total",
			Help: "Resoluciones de la cascada DETERMINISTA del carrito (Plan 044 · Ola 3.5 · T3.5-1), por escalón que lo resolvió —o que lo perdió— (escalon = exact|fuzzy|ninguno|troceo|troceo_perdido) y nivel de la sub-máquina en que se resolvió (nivel = una de las constantes Level* del módulo cart). Mide QUÉ ESCALÓN RESUELVE —y, en el troceado, qué se quedó fuera—, que es el dato con el que se decide si hace falta el turno LLM: `exact` es el cliente escribiendo la etiqueta tal cual, `fuzzy` es la errata rescatada por la distancia de edición con umbral 0,85 (D-044.45), y `ninguno` es la cascada corriendo y NO resolviendo — o sea el material del turno LLM (T3.5-2), no un error. T3.5-3 añadió los DOS ESCALONES DEL TROCEADO, que no son un peldaño más de la cascada sino su desenlace cuando el turno traía VARIOS productos en una sola frase y se partió en Go: `troceo` es un trozo que acabó ENTRANDO en el pedido —lo casara la cascada o lo resolviera una llamada chica al modelo— y `troceo_perdido` es un trozo que NO entró, por cualquiera de estas tres razones y sin distinguirlas: el turno se quedó sin llamadas (turnoacotado.MaxLlamadasPorTurno = 3) o sin presupuesto de tiempo (PresupuestoTroceado), el modelo devolvió un código vacío, ilegible o fuera de la lista de candidatos, o el artículo TIENE VARIANTES y elegir una habría sido inventar un precio. Los dos se cuentan POR TROZO y no por turno, porque la pregunta que hay que poder responder desde fuera es «¿cuánto del pedido de la gente se está perdiendo?» y esa se responde con la PROPORCIÓN entre ambos, no con cuántos turnos hubo. Y los dos salen SOLO con nivel=categories: el troceado no aterriza en ningún otro nivel (T3.5-3), así que verlos en otro nivel sería un bug, no un dato. ⚠️ LO QUE ESTE CONTADOR NO CUENTA, y sin esto el volumen se lee al revés: (a) el mensaje resuelto por CÓDIGO EXACTO —el cliente teclea «2», que es el camino mayoritario— NO aparece, porque la cascada ni llega a correr; (b) tampoco aparecen los niveles EXCLUIDOS (quantity, item_note, order_note, buyer_data y los terminales), por la misma razón; (c) una entrada de más de 16 tokens tampoco, porque es prosa y se descarta antes —mismo techo para la cascada y para el troceado—; (d) el turno troceado en el que NO ENTRÓ NI UN producto no deja ni un solo `troceo_perdido`, porque la observación cuelga del camino en que SÍ se recompone el pedido, de modo que la pérdida publicada es una COTA INFERIOR de la real. Así que la suma de las cinco etiquetas NO es el número de mensajes del carrito —y desde T3.5-3 ni siquiera es un número de mensajes: los tres primeros escalones cuentan MENSAJES y los dos del troceado cuentan TROZOS, así que un solo turno multi-producto suma varias veces—. 🔴 CERO TEXTO LIBRE: ni el mensaje del cliente ni textmatch.Result.Evidence —que es texto legible por humanos— pueden entrar en una etiqueta; las dos que hay son de cardinalidad fija (5 escalones × los niveles del carrito). El tenant tampoco se etiqueta: misma regla dura del paquete.",
		}, []string{"escalon", "nivel"}),
		llmDegradacion: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "wapp_llm_degradacion_total",
			Help: "Caídas a NIVEL A de una inferencia LLM (Plan 044 · Ola 3.5 · T3.5-2, ADR-0044 §5): una vez por fallo de la vía que SÍ tiene motivo, o sea exactamente las mismas veces que se escribe un aviso al dueño en owner_degradation_notices — este contador y esa tabla cuentan lo mismo visto desde dos sitios. 📊 ES EL DATO DE CAMPO QUE DESBLOQUEA EL DESALOJO (D-044.41): internal/intake/pipeline/plaza.go dice por escrito que el desalojo «NO se construye hasta que exista la Ola 3.5 y un dato de campo», y el dato es la serie {origen=\"turno\"}. Cómo se lee, porque sin esto el número no significa nada: `origen` dice QUÉ ENTRADA del selector se estaba sirviendo — `turno` es el TURNO ACOTADO del Nivel B (alguien esperando en WhatsApp mientras el carrito no entiende lo que escribió), `pipeline` son las cinco etapas P1–P5 del presupuesto, y `seleccion` es el fallo al CONSTRUIR el adaptador (credencial ilegible, vía imposible), que no llegó a tocar el cable. Así que rate(wapp_llm_degradacion_total{origen=\"turno\",reason=\"timeout\"}) es «turnos interactivos que se quedaron sin interpretación porque el Ollama del cliente no contestó en su plazo», y con K=1 por plaza (ADR-0046 · Mecanismo 1) el sospechoso número uno de ese timeout es una cadena de LOTE ocupando la única plaza — que es la pregunta que D-044.41 hace. ⚠️ UNIDAD, y desde T3.5-3 importa: {origen=\"turno\"} cuenta LLAMADAS, no turnos —el troceado hace una llamada chica por trozo sin casar, hasta turnoacotado.MaxLlamadasPorTurno = 3—, así que un mismo turno interactivo puede sumar aquí más de una vez. Su hermano `reason=\"edge_sin_capacidad\"` es el mismo fenómeno dicho por el Edge en vez de deducido del reloj, y SÍ TIENE PRODUCTOR, medido en campo: la noche del 2026-08-26, en UAT, {origen=\"turno\",reason=\"edge_sin_capacidad\",via=\"local\"} llegó a 2 —el Edge del VPS rechazando turnos interactivos por semáforo lleno mientras servía el pipeline—, que es exactamente el dato que D-044.41 esperaba (degradation.go). ⚠️ LO QUE ESTE CONTADOR NO CUENTA, y es la mitad de la historia del turno acotado: la degradación por CALIDAD. Un modelo que contesta a tiempo pero elige mal, dice `usable:false` o devuelve un número fuera de la lista NO aparece aquí — no es un fallo de la vía, el equipo del cliente está perfectamente, y por eso ni escribe aviso al dueño (llmvia/notify.go, primera rama de motivoDe) ni suma aquí. Eso se ve por el desenlace de la consulta del motor de flujos (engine.ObservadorConsulta), que hoy va al log. Tampoco cuenta el calentamiento fallido, por la misma razón y a propósito. 🔴 CERO PII Y CERO ALTA CARDINALIDAD: ni tenant, ni edge, ni sesión, ni el texto del cliente — la regla dura del paquete (INV-5), custodiada además por un test.",
		}, []string{"origen", "via", "reason"}),
		autoreplyStreak: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "wapp_flow_autoreply_streak",
			Help: "Distribución de la LONGITUD de las rachas de auto-respuestas consecutivas por conversación (Plan 049 · Opción A, OBSERVAR): se observa UNA vez por episodio CERRADO, y el episodio se cierra cuando muere el estado de la conversación (flujo terminado, escape del cliente o TTL) o cuando pasan 30 min sin auto-respuestas — sin ese matiz el número no se interpreta bien. ⚠️ EL CIERRE POR INACTIVIDAD LO MATERIALIZA ESTE MISMO SCRAPE: como no hay proceso de fondo (ADR-0003), el runtime barre las rachas vencidas cuando alguien raspa /metrics (al calcular wapp_flow_autoreply_streak_max, que ya recorre el mapa), así que una racha abandonada aparece aquí en el primer scrape POSTERIOR a sus 30 min de silencio, no en el instante en que venció — y si nadie raspa, no se cierra. ⚠️ Y PUEDE TARDAR UN SCRAPE MÁS: el registry colecta este histograma y el gauge hermano EN PARALELO, así que si el histograma serializa su estado antes de que corra la fuente del gauge —que es quien barre—, lo barrido en el scrape N se publica en el N+1. Medido, no supuesto (TestAutoreplyStreak_ObservarDuranteElScrapeNoSeBloquea): no se pierde ni se duplica ninguna observación, solo llega tarde. Consecuencia práctica al VERIFICARLO A MANO: hay que raspar DOS veces; con un solo scrape se puede ver _count sin moverse y concluir en falso que el cableado no funciona. Al leer el histograma: los episodios abandonados llegan con retraso (hasta un intervalo de scrape) y, tras un reinicio del proceso, las rachas vivas en memoria se pierden sin observarse. ⚠️ UNIDAD: lo que se cuenta es UNA EMISIÓN del motor (una llamada a send), NO un turno conversacional. Un mismo entrante puede producir más de una emisión (p. ej. el resumen del rescate y, acto seguido, la pantalla del flujo), así que la racha es una COTA SUPERIOR del número de turnos. Consecuencia práctica para quien lea el p99: la estimación de «20-30 auto-respuestas legítimas» del §5 del plan está en TURNOS, así que en esta métrica ese mismo recorrido puede leerse algo más alto — no se debe traducir un percentil de esta métrica a un umbral de turnos sin ese ajuste. Y en el otro extremo: también cuentan los avisos de error del sistema (el aviso de fallo del sink durable) y cada escape acaba dejando un 1 en el histograma (tras el cierre, el propio aviso de escape abre una racha nueva de 1, que se observa cuando esa racha de 1 vence por inactividad y la barre el scrape siguiente — no en el instante del escape), de modo que la cola baja está sesgada hacia abajo por esas dos causas y la mediana NO se debe leer como «longitud típica de una conversación». ⚠️ QUÉ NO CUENTA (la métrica SUBCUENTA, y es sabido): solo se cuentan los envíos que pasan por el motor de flujos (internal/flujos/runtime, función send). Las NOTIFICACIONES DE CAMBIO DE ESTADO DEL PEDIDO (internal/intakes/notifier.go) van directas al Sender sin pasar por ahí y NO suman a la racha, aunque son auto-respuestas de pleno derecho: el sistema hablándole al MISMO contacto de la conversación sin que nadie haya tecleado nada. Así que en las conversaciones con pedido vivo la racha real es más larga que la publicada. Cablear el contador en el Notifier quedó fuera del alcance de la Opción A. (Los otros dos puntos de envío fuera del motor —internal/publicapi/messages.go y internal/platform/httpapi/admin.go— quedan fuera CON RAZÓN: son envíos humanos, no auto-respuestas.) ⚠️ NO ES UNA ALARMA Y NO SE DEBE MONTAR UNA ALERTA SOBRE ELLA: igual que en wapp_flow_reactive_blocked_total se distingue el corte DELIBERADO de política (que no es un error) de la PÉRDIDA real (saturation, el único motivo sobre el que alarmar), aquí una racha larga es OBSERVACIÓN DE POLÍTICA, no degradación — una conversación larga es el motor funcionando exactamente como se diseñó (un catálogo que pagina de 5 en 5 hasta 500 artículos produce 20-30 auto-respuestas perfectamente legítimas). No hay umbral y no se corta a nadie: se cuenta y se publica. Su único propósito es medir la distribución de las rachas LEGÍTIMAS durante 2-4 semanas para poder decidir DESPUÉS, con datos (el p99), si procede un corte — la Opción B del Plan 049, hoy APLAZADA.",
			// Escala tipo Fibonacci de 1 a 987: fina donde se toma la decisión y
			// abierta hacia la cola. El §5 del plan estima el recorrido legítimo
			// más largo (catálogo paginado de 5 en 5 hasta 500 artículos) en 20-30
			// auto-respuestas, y el §9 quiere leer el p99 de las rachas legítimas
			// para poder ELEGIR un umbral después. Por eso la resolución fina está
			// en 13-55 (13, 21, 34, 55): es la franja donde caerá ese p99, y un
			// bucket ancho ahí lo dejaría en un intervalo demasiado gordo para
			// justificar un corte. Por debajo (1, 2, 3, 5, 8) se separan las
			// conversaciones triviales de las de menú; por encima (89…987) los
			// buckets se ensanchan a propósito: ahí ya no se afina nada, solo
			// interesa VER si existe cola larga (un bucle con terceros). Sin
			// etiquetas: tenant y session están PROHIBIDOS por la regla dura del
			// paquete, así que el histograma es plano y no un HistogramVec.
			Buckets: []float64{1, 2, 3, 5, 8, 13, 21, 34, 55, 89, 144, 233, 377, 610, 987},
		}),
	}
	// El GaugeFunc se construye DESPUÉS del literal porque su closure captura m
	// (la fuente vive en el struct y se inyecta más tarde, ver
	// SetFlowAutoreplyStreakMaxSource). Se registra ya, con la fuente todavía
	// nil: durante esa ventana el scrape devuelve 0 en vez de entrar en pánico.
	autoreplyStreakMax := prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "wapp_flow_autoreply_streak_max",
		Help: "Longitud de la racha de auto-respuestas VIVA más larga en este instante (Plan 049 · Opción A), muestreada en el momento del scrape. ⚠️ UNIDAD: la racha cuenta EMISIONES del motor (llamadas a send), NO turnos conversacionales — un mismo entrante puede producir más de una emisión (p. ej. el resumen del rescate y, acto seguido, la pantalla del flujo) y también cuentan los avisos de error del sistema (el aviso de fallo del sink durable), así que este valor es una COTA SUPERIOR del número de turnos: la estimación de «20-30 auto-respuestas legítimas» del §5 del plan está en TURNOS y aquí ese mismo recorrido puede verse algo más alto, de modo que no se debe traducir este número a un umbral de turnos sin ese ajuste. Por el mismo motivo un valor bajo tampoco dice gran cosa: tras un escape el propio aviso abre inmediatamente una racha nueva de 1. Existe porque wapp_flow_autoreply_streak solo observa episodios CERRADOS, y un bucle desbocado contra un autorespondedor de terceros NO cierra nunca mientras dura: sería invisible en el histograma justo mientras está ocurriendo. Este gauge lo hace visible EN VIVO y responde a la pregunta 3 del §9 del plan («¿ocurre siquiera un bucle con terceros?»). ⚠️ VIVA QUIERE DECIR VIVA: el propio scrape BARRE antes de medir las rachas que llevan más de 30 min sin una auto-respuesta (y las manda al histograma), precisamente para que este número no se quede clavado en una racha fosilizada de una conversación que el cliente abandonó hace horas — sin ese barrido, un catálogo legítimo de 30 abandonado dejaría el gauge en 30 para siempre y no se distinguiría de un bucle en curso. Efecto lateral que conviene conocer: leer /metrics tiene consecuencias sobre el histograma hermano, y el intervalo de scrape es la resolución con la que se cierran los episodios inactivos. ⚠️ SUBCUENTA IGUAL QUE EL HISTOGRAMA: las notificaciones de estado del pedido (internal/intakes/notifier.go) no pasan por el motor de flujos y no suman a la racha. ⚠️ TAMPOCO ES UNA ALARMA: no hay umbral, no se corta a nadie y un valor alto puede ser una conversación larga legítima (el episodio se cierra por fin de conversación o por 30 min sin auto-respuestas); se publica para OBSERVAR, no para alertar. Vale 0 cuando no hay ninguna racha viva Y TAMBIÉN mientras no se haya inyectado la fuente (la ventana de arranque entre New() y el cableado del runtime), así que un cero no distingue «sin tráfico» de «sin cablear».",
	}, func() float64 {
		m.streakMaxMu.RLock()
		fn := m.streakMaxSource
		m.streakMaxMu.RUnlock()
		if fn == nil {
			return 0
		}
		return float64(fn())
	})
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.httpRequests, m.httpDuration, m.rateLimitHits, m.receipts, m.reactiveBlocks, m.webhookDeliveries,
		m.flowEventLifecycle, m.cartMatch, m.llmDegradacion, m.autoreplyStreak, autoreplyStreakMax,
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

// CartMatch registra UNA resolución de la cascada determinista del carrito (Plan
// 044 · Ola 3.5 · T3.5-1): escalon es "exact", "fuzzy" o "ninguno" —y, desde el
// troceado (T3.5-3), también "troceo" o "troceo_perdido", que se cuentan por TROZO
// y solo en el nivel categories—, y nivel es el
// nivel de la sub-máquina (categories|articles|article|variant|continue|summary|
// item_note_scope). Los dos son de cardinalidad FIJA y NINGUNO de los dos puede
// portar texto del cliente — ver el Help del colector, donde está la regla y lo
// que este contador deliberadamente NO cuenta.
//
// Se pasa como callback al módulo cart (que NO importa este paquete: mismo
// desacoplo que Receipt/FlowReactiveBlocked/WebhookDelivery).
func (m *Metrics) CartMatch(escalon, nivel string) {
	if m == nil {
		return
	}
	m.cartMatch.WithLabelValues(escalon, nivel).Inc()
}

// LLMDegradacion registra UNA caída a Nivel A de la vía LLM de un tenant (Plan
// 044 · Ola 3.5 · T3.5-2). Los tres argumentos son de vocabulario CERRADO y
// contado, y ninguno puede portar texto del cliente ni identificar a nadie:
//
//	origen  →  3 valores: seleccion | pipeline | turno (llmvia.Origen*).
//	via     →  2 valores: local | api (el CHECK de tenant_llm).
//	reason  →  8 valores: los de degradation.Reason, que es el mismo vocabulario
//	           cerrado que el CHECK de la 0075 y el que habla el transporte.
//
// CARDINALIDAD, contada: 3 × 2 × 8 = 48 series en el peor caso teórico, y en la
// práctica un puñado —una vía por tenant y tres o cuatro motivos que ocurren de
// verdad—. Es del mismo orden que wapp_flow_event_lifecycle_total y muy por debajo
// de lo que costaría UNA etiqueta por tenant, que es la que está prohibida.
//
// Se pasa como CALLBACK al selector de vía (llmvia.WithDegradacionObservada), que
// NO importa este paquete: mismo desacoplo que Receipt, FlowReactiveBlocked,
// WebhookDelivery y CartMatch. Nil-safe como todo el paquete.
func (m *Metrics) LLMDegradacion(origen, via, reason string) {
	if m == nil {
		return
	}
	m.llmDegradacion.WithLabelValues(origen, via, reason).Inc()
}

// FlowEventLifecycle registra el DELTA de una vuelta del colector incremental de
// flow_events (Plan 043 · T6.5, MD-043.17: primer consumidor de PRODUCCIÓN del
// outbox append-only, no un assert de test). delta es el count(*) que devolvió
// esa vuelta para (name, event_kind, reason) — NUNCA un total absoluto — así que
// se ACUMULA (Add), igual que el resto de contadores del paquete.
//
// event_kind es SIEMPRE payload->>'kind' (menu|cart|survey|media|…), JAMÁS la
// columna `kind` de la fila ("persist"|"event"): la firma del método fuerza el
// vocabulario correcto por construcción — quien la llama no tiene forma de
// pasar la columna equivocada sin nombrar mal la variable a propósito. Ver la
// COLISIÓN DE VOCABULARIO documentada en
// internal/flujos/runtime/event_effects.go.
//
// `reason` es la TERCERA etiqueta desde el Plan 053 · Ola 4 · T4.1 y solo la
// puebla `event_escaped`: sus tres emisores mezclaban «el flujo terminó solo»,
// «el menú no entendió el texto» y «el cliente dijo salir» bajo una serie única.
// El resto de efectos la traen vacía, y eso es un HECHO —no tienen causas que
// distinguir—, no un dato que se perdiera por el camino. ⚠️ Añadirla es una
// MIGRACIÓN DE CARDINALIDAD: las series de dos etiquetas dejan de alimentarse y
// no vuelven, así que un panel o una alerta que las nombre exactamente hay que
// reescribirlo (una suma sin `by (...)` sigue dando el mismo total). El detalle
// está en la cabecera de eventTelemetryQuery, en internal/platform/metrics/flowlifecycle.
//
// Cardinalidad: `name` son los efectos de ciclo de vida del evento (hoy siete,
// más los que el motor añada — este método no los enumera, así que un octavo
// efecto nuevo no necesita tocar este paquete) × `event_kind` (cuatro tipos de
// evento) × `reason` (tres causas + el vacío, y SOLO sobre event_escaped: el
// producto real no es 7×4×4, porque las combinaciones imposibles nunca se
// instancian — Prometheus solo crea la serie que de verdad se incrementa). NO
// cardinalidad por tenant (misma regla dura del paquete, INV-5). Se pasa como
// callback al Colector (que NO importa este paquete: mismo desacoplo que
// Receipt/FlowReactiveBlocked/WebhookDelivery — ver
// internal/platform/metrics/flowlifecycle).
func (m *Metrics) FlowEventLifecycle(name, eventKind, reason string, delta float64) {
	if m == nil {
		return
	}
	m.flowEventLifecycle.WithLabelValues(name, eventKind, reason).Add(delta)
}

// --- Rachas de auto-respuestas (Plan 049 · Opción A: OBSERVAR) --------------

// FlowAutoreplyStreak observa la longitud de UNA racha de auto-respuestas
// consecutivas en una conversación, y se llama UNA sola vez por episodio, al
// CERRARSE (flujo terminado, escape, TTL del estado, o 30 min sin
// auto-respuestas). Llamarla en cada auto-respuesta en vez de al cierre
// convertiría el histograma en la distribución de los prefijos de cada racha
// —1, 2, 3, … para una racha de 3— y el p99 saldría hundido hacia 1.
//
// ⚠️ Esto MIDE, no corta: la Opción A del Plan 049 no fija umbral ni frena a
// nadie. Un valor alto no es un incidente (ver el Help de la métrica). El corte
// es la Opción B, aplazada hasta tener 2-4 semanas de esta distribución.
//
// SIN etiquetas, por la regla dura del paquete: ni tenant ni sesión (aislamiento
// + cardinalidad). Para saber QUÉ conversación tuvo la racha larga se va al log
// del runtime, no a /metrics — mismo criterio que FlowReactiveBlocked.
//
// Se pasa como callback al runtime de flujos (que NO importa este paquete:
// mismo desacoplo que Receipt/FlowReactiveBlocked/WebhookDelivery).
func (m *Metrics) FlowAutoreplyStreak(racha int) {
	if m == nil {
		return
	}
	m.autoreplyStreak.Observe(float64(racha))
}

// SetFlowAutoreplyStreakMaxSource fija la fuente del gauge
// wapp_flow_autoreply_streak_max: una función que devuelve la racha VIVA más
// larga en este instante, que el colector invoca EN CADA SCRAPE.
//
// POR QUÉ UNA FUENTE INYECTADA Y NO UN CALLBACK como el resto del paquete: los
// demás colectores los empuja el runtime cuando pasa algo (push), pero un gauge
// se tira en el scrape (pull), así que la dependencia va al revés y metrics
// necesita poder PREGUNTAR. Inyectar la función mantiene la regla de la casa —
// el motor sigue sin importar prometheus, igual que con WithReactiveBlockedHook.
//
// POR QUÉ SE PUEDE LLAMAR DESPUÉS DE New(): el gauge se registra en New(), pero
// el runtime que sabe contestar no existe todavía en ese momento. Mientras la
// fuente sea nil el scrape devuelve 0 y NO entra en pánico; esa ventana es
// esperada, no un fallo. (Un 0 en esa ventana es indistinguible de «no hay
// ninguna racha viva» — asumido: la alternativa, no registrar el gauge hasta el
// cableado, deja /metrics cambiando de forma a mitad del arranque.)
//
// Nil-safe respecto al receptor, como el resto del paquete. La escritura va bajo
// el mismo RWMutex que lee el colector: New() y el scrape son concurrentes.
// Llamarla dos veces es legal: gana la última.
func (m *Metrics) SetFlowAutoreplyStreakMaxSource(fn func() int) {
	if m == nil {
		return
	}
	m.streakMaxMu.Lock()
	m.streakMaxSource = fn
	m.streakMaxMu.Unlock()
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
