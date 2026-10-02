//go:build integracion

package procesos

import (
	"net/http"
	"slices"
	"strings"
	"testing"
)

// El subtest «metricas» de P0: las métricas que /metrics debe declarar sin tráfico de negocio, la
// que ya no debe aparecer y las muestras cuya familia no está declarada.
// Sale de p0_arranque_test.go (D-F9-11: solo se movieron declaraciones).

// p0Metrica es un nombre de métrica que /metrics debe declarar y el tipo con el que lo declara.
type p0Metrica struct{ nombre, tipo string }

// p0MetricasSinTrafico son las métricas wapp_* que un servidor recién arrancado, SIN tráfico de
// negocio, ya publica en /metrics. Es la lista MEDIDA (una sola vez, contra el binario viejo y
// el nuevo, a los ~100 ms de arrancar), no la de contratos.md §8:
//
//   - 6 de wapp_db_*: son GaugeFunc/CounterFunc del pool (metrics.go:481-502) y se evalúan en el
//     scrape, así que existen siempre.
//   - wapp_flow_autoreply_streak y wapp_flow_autoreply_streak_max: un Histogram y un GaugeFunc
//     SIN etiquetas (metrics.go:108,131); un metric sin etiquetas existe desde que se registra.
//   - wapp_edge_inference_reporting_edges: el único de los cinco descriptores dinámicos de
//     inferstats.go que se emite sin Edges (el gauge de Edges conocidos, vale 0; inferstats.go:123).
//
// Son 8 de los 17 nombres estáticos y 1 de los 5 dinámicos de contratos.md §8. Los otros 13 NO se exigen
// (trampa T-10 de reglas.md, un vector no existe hasta su primer incremento) y quedan FUERA a
// propósito, cada uno por su motivo:
//
//   - CounterVec sin ningún incremento todavía: wapp_ratelimit_hits_total, wapp_receipts_total,
//     wapp_webhook_deliveries_total, wapp_cart_match_total, wapp_flow_event_lifecycle_total,
//     wapp_flow_reactive_blocked_total, wapp_llm_degradacion_total (metrics.go:80-107). Los exigen
//     los procesos que provocan el evento (P3, P4, P6).
//   - Descriptores dinámicos que solo se emiten por clave presente, es decir, con un Edge que haya
//     reportado: wapp_edge_inference_by_regime_total, wapp_edge_inference_by_class_total,
//     wapp_edge_intent_omitted_total, wapp_edge_inference_samples_total (inferstats.go:113-141,
//     «un mapa vacío no emite nada»).
//   - wapp_http_requests_total y wapp_http_request_duration_seconds (un CounterVec y un
//     HistogramVec) tampoco existen sin tráfico: en la corrida medida aparecieron solo porque el
//     sondeo de disponibilidad del arnés ya había hecho GET /healthz. Por eso no están aquí sino en
//     p0MetricasHTTP, que exige además haber hecho esa petición antes de raspar.
var p0MetricasSinTrafico = []p0Metrica{
	{"wapp_db_idle", "gauge"},
	{"wapp_db_in_use", "gauge"},
	{"wapp_db_max_idle_closed", "counter"},
	{"wapp_db_max_open", "gauge"},
	{"wapp_db_wait_count", "counter"},
	{"wapp_db_wait_duration_seconds", "counter"},
	{"wapp_edge_inference_reporting_edges", "gauge"},
	{"wapp_flow_autoreply_streak", "histogram"},
	{"wapp_flow_autoreply_streak_max", "gauge"},
}

// p0MetricasHTTP son las dos métricas de la instrumentación HTTP del listener admin, que existen
// una vez que el listener ha atendido una petición. P0 las provoca con un GET /healthz propio
// antes de raspar (además del sondeo del arnés).
var p0MetricasHTTP = []p0Metrica{
	{"wapp_http_requests_total", "counter"},
	{"wapp_http_request_duration_seconds", "histogram"},
}

// p0MetricaRetirada es la métrica que NO debe aparecer: se retiró con el login (metrics.go:201) y
// internal/platform/metrics/metrics_test.go:48-49 falla si su nombre sale en el cuerpo.
const p0MetricaRetirada = "wapp_auth_logins_total"

// p0Metricas comprueba GET :8100/metrics: 200, cuerpo en formato de exposición de Prometheus (cada
// muestra pertenece a una familia con su «# TYPE»), las métricas de p0MetricasSinTrafico y las de
// p0MetricasHTTP con su tipo, una muestra de wapp_http_requests_total del listener admin para
// /healthz con valor ≥ 1, y ninguna mención a wapp_auth_logins_total. Hace antes un GET /healthz
// propio para que la instrumentación HTTP tenga algo que mostrar. Falla con t.Fatalf si no hay
// respuesta 200 o el cuerpo no se puede interpretar, y con t.Errorf por cada incumplimiento.
func p0Metricas(t *testing.T, s *servidor) {
	t.Helper()
	if r := s.Admin("").Get(t, "/healthz", nil); r.Codigo != http.StatusOK {
		t.Fatalf("GET /healthz previo al raspado = %d, quería 200", r.Codigo)
	}
	r := s.Admin("").Get(t, "/metrics", nil)
	if r.Codigo != http.StatusOK {
		t.Fatalf("GET :8100/metrics = %d, quería 200\ncuerpo: %s", r.Codigo, recortar(r.Cuerpo))
	}
	exp, err := p0ParsearExposicion(string(r.Cuerpo))
	if err != nil {
		t.Fatalf("/metrics no está en el formato de exposición de Prometheus: %v", err)
	}
	if huerfanas := p0Huerfanas(exp); len(huerfanas) > 0 {
		t.Errorf("muestras de familias sin «# TYPE»: %q", huerfanas)
	}
	for _, m := range slices.Concat(p0MetricasSinTrafico, p0MetricasHTTP) {
		if tipo, hay := exp.Tipos[m.nombre]; !hay || tipo != m.tipo {
			t.Errorf("%s: tipo %q (declarada: %v), quería %q", m.nombre, tipo, hay, m.tipo)
		}
	}
	if !p0HayPeticionAdmin(exp) {
		t.Errorf("no hay muestra de wapp_http_requests_total{listener=admin, route=/healthz, status=200} ≥ 1")
	}
	if strings.Contains(string(r.Cuerpo), p0MetricaRetirada) {
		t.Errorf("%s sigue publicándose en /metrics: se retiró con el login", p0MetricaRetirada)
	}
}

// p0HayPeticionAdmin dice si la exposición tiene una muestra de wapp_http_requests_total del
// listener admin, ruta /healthz, método GET y código 200, con valor ≥ 1. No falla.
func p0HayPeticionAdmin(exp p0Exposicion) bool {
	return slices.ContainsFunc(exp.Muestras, func(m p0Muestra) bool {
		return m.Nombre == "wapp_http_requests_total" && m.Valor >= 1 &&
			m.Etiquetas["listener"] == "admin" && m.Etiquetas["route"] == "/healthz" &&
			m.Etiquetas["method"] == http.MethodGet && m.Etiquetas["status"] == "200"
	})
}

// p0Huerfanas recibe la exposición y devuelve los nombres de las muestras cuya familia no tiene
// línea «# TYPE». Una muestra «x_bucket», «x_sum» o «x_count» pertenece a la familia «x» si esta es
// un histogram o un summary. Devuelve nil si todas están declaradas. No falla.
func p0Huerfanas(exp p0Exposicion) []string {
	var huerfanas []string
	for _, m := range exp.Muestras {
		if _, hay := exp.Tipos[m.Nombre]; hay {
			continue
		}
		declarada := false
		for _, sufijo := range []string{"_bucket", "_sum", "_count"} {
			base, tieneSufijo := strings.CutSuffix(m.Nombre, sufijo)
			if tipo := exp.Tipos[base]; tieneSufijo && (tipo == "histogram" || tipo == "summary") {
				declarada = true
			}
		}
		if !declarada && !slices.Contains(huerfanas, m.Nombre) {
			huerfanas = append(huerfanas, m.Nombre)
		}
	}
	return huerfanas
}
