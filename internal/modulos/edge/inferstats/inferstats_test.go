//go:build pendiente

package inferstats

import (
	"maps"
	"sync"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/metrics/inferencia"
)

func ptr(v int64) *int64 { return &v }

// Aggregate es ALIAS de inferencia.Agregado, no un tipo definido: solo con un alias un
// *Aggregate ES un *inferencia.Agregado. Y (*Store).Aggregated encaja en la firma que pide
// platform/metrics (RegisterInferenceStats recibe un `func() inferencia.Agregado`).
var (
	_ *inferencia.Agregado             = new(Aggregate)
	_ func(*Store) inferencia.Agregado = (*Store).Aggregated
	_ func(*Store, Key, Report)        = (*Store).Observe
	_ func() *Store                    = New
)

// New: almacén vacío, con los tres mapas utilizables y nada medido.
func TestNewIsEmpty(t *testing.T) {
	t.Parallel()
	ag := New().Aggregated()
	if ag.Edges != 0 {
		t.Fatalf("Edges = %d, quiero 0", ag.Edges)
	}
	if ag.PorRegimen == nil || ag.PorClase == nil || ag.OmitidasPorMotivo == nil {
		t.Fatalf("los tres mapas de un agregado vacío tienen que ser no-nil: %+v", ag)
	}
	if len(ag.PorRegimen)+len(ag.PorClase)+len(ag.OmitidasPorMotivo) != 0 {
		t.Fatalf("los mapas de un almacén vacío traen claves: %+v", ag)
	}
	if ag.MuestrasPrefill != nil || ag.MuestrasGeneracion != nil {
		t.Fatal("sin ningún Edge, las muestras tienen que ser nil (no medible)")
	}
}

// T-12 · EL test del paquete: los contadores son del PROCESO del Edge pero viajan en el latido
// de CADA sesión suya. Tres teléfonos = tres latidos con los MISMOS totales, que no se suman.
func TestThreePhonesOfOneEdgeDoNotTriple(t *testing.T) {
	t.Parallel()
	st := New()
	k := Key{TenantID: "t-1", EdgeID: "edge-1"}
	for range 3 {
		st.Observe(k, Report{
			ByRegime:        map[string]int64{"caliente": 50},
			ByClass:         map[string]int64{"lote": 20},
			SkippedByReason: map[string]int64{"sin_cupo": 4},
			PrefillSamples:  ptr(9),
		})
	}
	ag := st.Aggregated()
	if got := ag.PorRegimen["caliente"]; got != 50 {
		t.Fatalf("inferencias en caliente = %d, quiero 50: el parte es del PROCESO del Edge y "+
			"llega repetido en el latido de cada sesión suya", got)
	}
	if ag.PorClase["lote"] != 20 || ag.OmitidasPorMotivo["sin_cupo"] != 4 {
		t.Fatalf("PorClase/OmitidasPorMotivo se multiplicaron: %v %v", ag.PorClase, ag.OmitidasPorMotivo)
	}
	if ag.MuestrasPrefill == nil || *ag.MuestrasPrefill != 9 {
		t.Fatalf("MuestrasPrefill = %v, quiero 9", ag.MuestrasPrefill)
	}
	if ag.Edges != 1 {
		t.Fatalf("Edges reportando = %d, quiero 1", ag.Edges)
	}
}

// El último parte SUSTITUYE: lo que llega es el acumulado de la vida del proceso, no el delta.
// Y sustituir es entero: lo que el parte nuevo no trae deja de estar.
func TestTheLastReportReplaces(t *testing.T) {
	t.Parallel()
	st := New()
	k := Key{TenantID: "t-1", EdgeID: "edge-1"}
	st.Observe(k, Report{ByRegime: map[string]int64{"frio": 1, "templado": 8}, PrefillSamples: ptr(5), GenerationSamples: ptr(6)})
	st.Observe(k, Report{ByRegime: map[string]int64{"frio": 2}})
	st.Observe(k, Report{ByRegime: map[string]int64{"frio": 3}})

	ag := st.Aggregated()
	if got := ag.PorRegimen["frio"]; got != 3 {
		t.Fatalf("frio = %d, quiero 3 (el último parte manda, no la suma de los tres)", got)
	}
	if _, ok := ag.PorRegimen["templado"]; ok {
		t.Fatalf("templado sigue en el agregado (%v): el parte nuevo no lo trae y tenía que desaparecer", ag.PorRegimen)
	}
	if ag.MuestrasPrefill != nil || ag.MuestrasGeneracion != nil {
		t.Fatal("las muestras del primer parte sobrevivieron a la sustitución")
	}
}

// Entre Edges DISTINTOS sí se suma, en los tres mapas y en las dos muestras. 🔴 Dos Edges del
// MISMO tenant: es el fixture que distingue la clave (tenant, edge) de una clave por tenant.
func TestSeveralEdgesAreSummed(t *testing.T) {
	t.Parallel()
	st := New()
	st.Observe(Key{TenantID: "t-1", EdgeID: "e-1"}, Report{
		ByRegime:          map[string]int64{"frio": 2},
		ByClass:           map[string]int64{"lote": 10, "interactivo": 1},
		SkippedByReason:   map[string]int64{"sin_cupo": 3},
		PrefillSamples:    ptr(40),
		GenerationSamples: ptr(7),
	})
	st.Observe(Key{TenantID: "t-1", EdgeID: "e-2"}, Report{
		ByRegime:          map[string]int64{"frio": 5, "caliente": 1},
		ByClass:           map[string]int64{"lote": 5},
		SkippedByReason:   map[string]int64{"sin_cupo": 1, "apagado": 2},
		PrefillSamples:    ptr(2),
		GenerationSamples: ptr(3),
	})

	ag := st.Aggregated()
	if want := map[string]int64{"lote": 15, "interactivo": 1}; !maps.Equal(ag.PorClase, want) {
		t.Fatalf("PorClase = %v, quiero %v: un tenant con dos instalaciones tiene dos Ollamas y "+
			"dos juegos de contadores", ag.PorClase, want)
	}
	if want := map[string]int64{"frio": 7, "caliente": 1}; !maps.Equal(ag.PorRegimen, want) {
		t.Fatalf("PorRegimen = %v, quiero %v", ag.PorRegimen, want)
	}
	if want := map[string]int64{"sin_cupo": 4, "apagado": 2}; !maps.Equal(ag.OmitidasPorMotivo, want) {
		t.Fatalf("OmitidasPorMotivo = %v, quiero %v", ag.OmitidasPorMotivo, want)
	}
	if ag.MuestrasPrefill == nil || *ag.MuestrasPrefill != 42 {
		t.Fatalf("MuestrasPrefill = %v, quiero 42", ag.MuestrasPrefill)
	}
	if ag.MuestrasGeneracion == nil || *ag.MuestrasGeneracion != 10 {
		t.Fatalf("MuestrasGeneracion = %v, quiero 10", ag.MuestrasGeneracion)
	}
	if ag.Edges != 2 {
		t.Fatalf("Edges = %d, quiero 2", ag.Edges)
	}

	// El mismo edge_id en OTRO tenant es otro proceso: el tenant también es parte de la clave.
	st.Observe(Key{TenantID: "t-2", EdgeID: "e-1"}, Report{ByClass: map[string]int64{"lote": 100}})
	if ag := st.Aggregated(); ag.PorClase["lote"] != 115 || ag.Edges != 3 {
		t.Fatalf("con un tercer Edge de otro tenant: lote=%d Edges=%d, quiero 115 y 3", ag.PorClase["lote"], ag.Edges)
	}
}

// El almacén NO olvida: un Edge que se va deja su serie PLANA, no un escalón hacia abajo (que
// Prometheus leería como un reinicio de contador). Se ejerce por lo único observable: dejar de
// recibir partes de un Edge no cambia nada.
func TestAnEdgeThatLeavesDoesNotLowerTheSum(t *testing.T) {
	t.Parallel()
	st := New()
	st.Observe(Key{TenantID: "t", EdgeID: "e-1"}, Report{ByRegime: map[string]int64{"frio": 7}})
	st.Observe(Key{TenantID: "t", EdgeID: "e-2"}, Report{ByRegime: map[string]int64{"frio": 3}})
	before := st.Aggregated()

	for range 5 {
		st.Observe(Key{TenantID: "t", EdgeID: "e-1"}, Report{ByRegime: map[string]int64{"frio": 7}})
	}
	after := st.Aggregated()
	if before.PorRegimen["frio"] != 10 || after.PorRegimen["frio"] != 10 {
		t.Fatalf("la suma pasó de %d a %d al irse un Edge (quiero 10 y 10): Prometheus lo leería "+
			"como un reinicio de contador y rate() perdería el tramo", before.PorRegimen["frio"], after.PorRegimen["frio"])
	}
	if after.Edges != 2 {
		t.Fatalf("Edges = %d, quiero 2: el Edge que se fue sigue contando", after.Edges)
	}
}

// Las muestras conservan el «no medible»: nil + nil = nil y nil + n = n, en el orden que sea
// (el mapa se recorre en orden aleatorio) y para las dos fases.
func TestSamplesKeepTheNotMeasurable(t *testing.T) {
	t.Parallel()
	st := New()
	st.Observe(Key{TenantID: "t", EdgeID: "old-1"}, Report{})
	st.Observe(Key{TenantID: "t", EdgeID: "old-2"}, Report{})
	if ag := st.Aggregated(); ag.MuestrasPrefill != nil || ag.MuestrasGeneracion != nil {
		t.Fatal("con ningún Edge midiendo, las muestras tienen que ser nil (no medible), no 0")
	}

	st.Observe(Key{TenantID: "t", EdgeID: "new"}, Report{PrefillSamples: ptr(91)})
	// Varias pasadas: el orden de recorrido del mapa cambia y el resultado no puede depender de él.
	for range 20 {
		ag := st.Aggregated()
		if ag.MuestrasPrefill == nil || *ag.MuestrasPrefill != 91 {
			t.Fatalf("MuestrasPrefill = %v, quiero 91: el Edge que no mide no debe restar ni anular", ag.MuestrasPrefill)
		}
		if ag.MuestrasGeneracion != nil {
			t.Fatalf("MuestrasGeneracion = %d; nadie la mide: tiene que seguir nil", *ag.MuestrasGeneracion)
		}
	}

	// Un cero REPORTADO no es «no medible»: se conserva como 0.
	zero := New()
	zero.Observe(Key{TenantID: "t", EdgeID: "e"}, Report{GenerationSamples: ptr(0)})
	if n := zero.Aggregated().MuestrasGeneracion; n == nil || *n != 0 {
		t.Fatalf("MuestrasGeneracion = %v, quiero un 0 medido (no nil)", n)
	}
}

// Observe COPIA el parte: ni los mapas ni los punteros comparten respaldo con el llamante.
func TestObserveCopiesTheReport(t *testing.T) {
	t.Parallel()
	st := New()
	regime := map[string]int64{"frio": 1}
	class := map[string]int64{"lote": 2}
	skipped := map[string]int64{"sin_cupo": 3}
	prefill, generation := ptr(4), ptr(5)
	st.Observe(Key{TenantID: "t", EdgeID: "e"}, Report{
		ByRegime: regime, ByClass: class, SkippedByReason: skipped,
		PrefillSamples: prefill, GenerationSamples: generation,
	})

	regime["frio"], class["lote"], skipped["sin_cupo"] = 999, 999, 999
	regime["nuevo"] = 1
	*prefill, *generation = 999, 999

	ag := st.Aggregated()
	if ag.PorRegimen["frio"] != 1 || len(ag.PorRegimen) != 1 || ag.PorClase["lote"] != 2 || ag.OmitidasPorMotivo["sin_cupo"] != 3 {
		t.Fatalf("el almacén comparte mapas con el llamante: %v %v %v", ag.PorRegimen, ag.PorClase, ag.OmitidasPorMotivo)
	}
	if *ag.MuestrasPrefill != 4 || *ag.MuestrasGeneracion != 5 {
		t.Fatalf("el almacén comparte punteros con el llamante: prefill=%d generation=%d", *ag.MuestrasPrefill, *ag.MuestrasGeneracion)
	}
}

// Lo que devuelve Aggregated es del llamante: mutarlo no toca el almacén.
func TestAggregatedReturnsItsOwnCopy(t *testing.T) {
	t.Parallel()
	st := New()
	st.Observe(Key{TenantID: "t", EdgeID: "e"}, Report{
		ByRegime: map[string]int64{"frio": 1}, ByClass: map[string]int64{"lote": 2},
		SkippedByReason: map[string]int64{"sin_cupo": 3}, PrefillSamples: ptr(4), GenerationSamples: ptr(5),
	})

	first := st.Aggregated()
	first.PorRegimen["frio"], first.PorClase["lote"], first.OmitidasPorMotivo["sin_cupo"] = 999, 999, 999
	*first.MuestrasPrefill, *first.MuestrasGeneracion = 999, 999

	second := st.Aggregated()
	if second.PorRegimen["frio"] != 1 || second.PorClase["lote"] != 2 || second.OmitidasPorMotivo["sin_cupo"] != 3 {
		t.Fatalf("mutar un agregado cambió el almacén: %v %v %v", second.PorRegimen, second.PorClase, second.OmitidasPorMotivo)
	}
	if *second.MuestrasPrefill != 4 || *second.MuestrasGeneracion != 5 {
		t.Fatalf("mutar las muestras de un agregado cambió el almacén: prefill=%d generation=%d",
			*second.MuestrasPrefill, *second.MuestrasGeneracion)
	}
}

// Un parte sin EdgeID se ignora; el TenantID vacío no se filtra.
func TestObserveIgnoresAnEmptyEdgeID(t *testing.T) {
	t.Parallel()
	st := New()
	st.Observe(Key{TenantID: "t", EdgeID: ""}, Report{ByRegime: map[string]int64{"frio": 5}})
	if ag := st.Aggregated(); ag.Edges != 0 || len(ag.PorRegimen) != 0 {
		t.Fatalf("un parte sin EdgeID entró en el almacén: %+v", ag)
	}

	st.Observe(Key{TenantID: "", EdgeID: "e"}, Report{ByRegime: map[string]int64{"frio": 5}})
	if ag := st.Aggregated(); ag.Edges != 1 || ag.PorRegimen["frio"] != 5 {
		t.Fatalf("un parte con EdgeID y sin TenantID tenía que guardarse: %+v", ag)
	}
}

// Nil-safe: un arranque sin observabilidad no obliga a poner guardas en el camino del latido.
func TestNilStoreIsSafe(t *testing.T) {
	t.Parallel()
	var none *Store
	none.Observe(Key{TenantID: "t", EdgeID: "e"}, Report{ByRegime: map[string]int64{"frio": 1}})
	ag := none.Aggregated()
	if ag.Edges != 0 || ag.MuestrasPrefill != nil || ag.MuestrasGeneracion != nil {
		t.Fatalf("un *Store nil tiene que devolver un agregado vacío: %+v", ag)
	}
	if ag.PorRegimen == nil || ag.PorClase == nil || ag.OmitidasPorMotivo == nil {
		t.Fatal("un *Store nil tiene que devolver un agregado utilizable (mapas no-nil)")
	}
}

// Concurrente: lo escribe el carril de cada stream y lo lee el scrape de /metrics, en
// goroutines distintas. Sin candado es una carrera de datos real, que -race caza.
func TestConcurrentObserveAndAggregated(t *testing.T) {
	t.Parallel()
	st := New()
	edges := []string{"e-1", "e-2", "e-3", "e-4", "e-5", "e-6", "e-7", "e-8"}
	var wg sync.WaitGroup
	for _, edge := range edges {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				st.Observe(Key{TenantID: "t", EdgeID: edge}, Report{ByRegime: map[string]int64{"frio": 1}, PrefillSamples: ptr(1)})
				ag := st.Aggregated()
				ag.PorRegimen["frio"]++ // el agregado es del llamante: escribirlo no es una carrera.
			}
		}()
	}
	wg.Wait()

	ag := st.Aggregated()
	if got := ag.PorRegimen["frio"]; got != 8 {
		t.Fatalf("frio = %d, quiero 8 (uno por Edge, sustituyendo)", got)
	}
	if ag.Edges != 8 || ag.MuestrasPrefill == nil || *ag.MuestrasPrefill != 8 {
		t.Fatalf("Edges=%d MuestrasPrefill=%v, quiero 8 y 8", ag.Edges, ag.MuestrasPrefill)
	}
}
