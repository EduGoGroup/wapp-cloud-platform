package metrics

import (
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/inferstats"
)

func ptr(v int64) *int64 { return &v }

// TestInferenceStats_ElCriterioDeT179 es el criterio de la tarea, ejercido: «¿qué
// proporción de las inferencias de la última hora pagó arranque en frío?» tiene que
// poder responderse SIN ABRIR UN LOG.
//
// Lo que hace falta para eso, y es lo que se afirma aquí, es que la serie exista con
// su etiqueta de régimen y que sea un CONTADOR: sin `_total`/tipo counter, `rate()`
// sobre ella no significa nada y la pregunta —que es sobre una ventana de tiempo— no
// se puede formular.
//
// 🔬 MUTACIÓN: cambiar `prometheus.CounterValue` por `GaugeValue` en emitirPorClave ⇒
// rojo (el `# TYPE` deja de decir counter).
func TestInferenceStats_ElCriterioDeT179(t *testing.T) {
	m := New()
	st := inferstats.New()
	if err := m.RegisterInferenceStats(st.Agrega); err != nil {
		t.Fatalf("RegisterInferenceStats: %v", err)
	}

	st.Observa(inferstats.Clave{TenantID: "t-1", EdgeID: "edge-1"}, inferstats.Parte{
		PorRegimen: map[string]int64{"frio": 3, "templado": 2, "caliente": 45},
	})

	cuerpo := scrape(t, m)
	for _, quiero := range []string{
		"# TYPE wapp_edge_inference_by_regime_total counter",
		`wapp_edge_inference_by_regime_total{regimen="frio"} 3`,
		`wapp_edge_inference_by_regime_total{regimen="templado"} 2`,
		`wapp_edge_inference_by_regime_total{regimen="caliente"} 45`,
	} {
		if !strings.Contains(cuerpo, quiero) {
			t.Errorf("/metrics no trae %q.\nSin esta serie, la proporción de inferencias en frío "+
				"solo se puede sacar abriendo el log del Edge, que es lo que T1.7-9 viene a quitar.\n"+
				"--- cuerpo ---\n%s", quiero, cuerpo)
		}
	}
}

// TestInferenceStats_AusenteNoEsCero: cuando el Edge no mide una fase, la serie NO se
// emite. Un 0 publicado se leería como «cero observaciones midieron el prefill», que
// es una afirmación; la verdad es «este Edge no lo reporta».
//
// Las dos mitades importan: la fase que SÍ llega tiene que salir, o el test pasaría
// con un colector que no publica nada.
//
// 🔬 MUTACIÓN: quitar la guarda `if n == nil { return }` de emitirMuestras ⇒ rojo (la
// generación aparece valiendo 0).
func TestInferenceStats_AusenteNoEsCero(t *testing.T) {
	m := New()
	st := inferstats.New()
	if err := m.RegisterInferenceStats(st.Agrega); err != nil {
		t.Fatalf("RegisterInferenceStats: %v", err)
	}
	st.Observa(inferstats.Clave{TenantID: "t-1", EdgeID: "edge-1"}, inferstats.Parte{
		MuestrasPrefill: ptr(91), // la generación NO llega
	})

	cuerpo := scrape(t, m)
	if !strings.Contains(cuerpo, `wapp_edge_inference_samples_total{fase="prefill"} 91`) {
		t.Errorf("no salió el `n` del prefill:\n%s", cuerpo)
	}
	if strings.Contains(cuerpo, `fase="generacion"`) {
		t.Errorf("se publicó la fase que el Edge NO mide: un 0 ahí se lee como «cero "+
			"observaciones», que es una afirmación distinta de «no lo sé».\n%s", cuerpo)
	}
}

// TestInferenceStats_ElCuantilNoSube deja fijado por test lo que hoy es una decisión
// escrita: a /metrics sube el `n`, NO el p50.
//
// Un p50 no se agrega entre Edges —sumarlo, promediarlo o tomar su máximo produce un
// número con buena pinta y sin significado—, y el que llega es además monótono y sin
// reset, así que tampoco serviría para comparar dos periodos. Si alguien lo publica en
// el futuro tendrá que borrar este test, y al hacerlo leerá el porqué.
func TestInferenceStats_ElCuantilNoSube(t *testing.T) {
	m := New()
	st := inferstats.New()
	if err := m.RegisterInferenceStats(st.Agrega); err != nil {
		t.Fatalf("RegisterInferenceStats: %v", err)
	}
	st.Observa(inferstats.Clave{TenantID: "t-1", EdgeID: "e-1"}, inferstats.Parte{MuestrasPrefill: ptr(10)})

	// Se miran las líneas de MUESTRA, no el cuerpo entero: el `# HELP` de la serie de
	// muestras explica por qué el cuantil no sube, así que un Contains sobre todo el
	// texto se dispararía con su propia explicación. Lo custodia el nombre de la serie,
	// que es lo que un panel puede consultar.
	for _, linea := range strings.Split(scrape(t, m), "\n") {
		if strings.HasPrefix(linea, "#") || !strings.HasPrefix(linea, "wapp_edge_inference") {
			continue
		}
		if strings.Contains(linea, "p50") {
			t.Fatalf("subió un p50 a /metrics (%q). No se agrega entre Edges y el que llega es "+
				"monótono sin reset: publicarlo invita a usarlo para un A/B, que es exactamente "+
				"el error que esta ola estuvo a punto de cometer.", linea)
		}
	}
}

// TestInferenceStats_NoSeEtiquetaPorTenantNiPorEdge custodia la regla dura del
// paquete (INV-5) sobre las series nuevas: nada de cardinalidad ni de acoplamiento por
// tenant. Es fácil de romper sin querer —añadir `edge_id` haría que el p50 sí se
// pudiera agregar en PromQL, y es tentador— así que se afirma en vez de confiarse.
func TestInferenceStats_NoSeEtiquetaPorTenantNiPorEdge(t *testing.T) {
	m := New()
	st := inferstats.New()
	if err := m.RegisterInferenceStats(st.Agrega); err != nil {
		t.Fatalf("RegisterInferenceStats: %v", err)
	}
	st.Observa(inferstats.Clave{TenantID: "tenant-secreto", EdgeID: "edge-secreto"},
		inferstats.Parte{PorClase: map[string]int64{"lote": 1}})

	cuerpo := scrape(t, m)
	for _, prohibido := range []string{"tenant-secreto", "edge-secreto", "tenant_id=", "edge_id=", "session_id="} {
		if strings.Contains(cuerpo, prohibido) {
			t.Errorf("/metrics contiene %q: las series de inferencia no se etiquetan por tenant, "+
				"Edge ni sesión (cardinalidad y aislamiento, regla dura del paquete)", prohibido)
		}
	}
}

// TestInferenceStats_ElNumeroDeEdgesEsGauge: los `samples` son acumulados (contador) y
// los Edges que reportan son un nivel (gauge). Van con tipos distintos a propósito —
// mezclarlos, por ejemplo dividiendo uno por otro, da un número absurdo con buena
// pinta.
func TestInferenceStats_ElNumeroDeEdgesEsGauge(t *testing.T) {
	m := New()
	st := inferstats.New()
	if err := m.RegisterInferenceStats(st.Agrega); err != nil {
		t.Fatalf("RegisterInferenceStats: %v", err)
	}
	st.Observa(inferstats.Clave{TenantID: "t", EdgeID: "e-1"}, inferstats.Parte{MuestrasPrefill: ptr(1)})
	st.Observa(inferstats.Clave{TenantID: "t", EdgeID: "e-2"}, inferstats.Parte{MuestrasPrefill: ptr(1)})

	cuerpo := scrape(t, m)
	if !strings.Contains(cuerpo, "# TYPE wapp_edge_inference_reporting_edges gauge") {
		t.Errorf("el número de Edges no es un gauge:\n%s", cuerpo)
	}
	if !strings.Contains(cuerpo, "wapp_edge_inference_reporting_edges 2") {
		t.Errorf("quiero 2 Edges reportando:\n%s", cuerpo)
	}
	if !strings.Contains(cuerpo, "# TYPE wapp_edge_inference_samples_total counter") {
		t.Errorf("las muestras no son un contador:\n%s", cuerpo)
	}
}

// TestRegisterInferenceStats_NilSafeEIdempotente: el resto del paquete es nil-safe y
// esto también. La doble llamada no tumba el arranque, mismo criterio que
// RegisterDBStats: un cable duplicado por descuido no puede costar la plataforma
// entera por una métrica.
func TestRegisterInferenceStats_NilSafeEIdempotente(t *testing.T) {
	var nada *Metrics
	if err := nada.RegisterInferenceStats(inferstats.New().Agrega); err != nil {
		t.Fatalf("sobre un *Metrics nil: %v", err)
	}
	m := New()
	if err := m.RegisterInferenceStats(nil); err != nil {
		t.Fatalf("sin fuente: %v", err)
	}
	st := inferstats.New()
	if err := m.RegisterInferenceStats(st.Agrega); err != nil {
		t.Fatalf("primera: %v", err)
	}
	if err := m.RegisterInferenceStats(st.Agrega); err != nil {
		t.Fatalf("segunda llamada (tiene que ser idempotente): %v", err)
	}
}
