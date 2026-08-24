package inferstats_test

import (
	"sync"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/inferstats"
)

func ptr(v int64) *int64 { return &v }

// TestTresTelefonosDeUnEdgeNoTriplicanLasInferencias es EL test de este paquete.
//
// 🔴 LA TRAMPA, dicha entera: los contadores del parte los lleva el PROCESO del Edge
// —un cajero, un Ollama— pero viajan en el latido de CADA sesión suya. Un Edge con
// tres teléfonos manda TRES latidos con LOS MISMOS totales. Si el almacén se indexara
// por sesión y sumara, la flota reportaría el triple de inferencias con una serie
// perfectamente creíble: sube, es monótona, `rate()` funciona, y miente por un factor
// igual al número de teléfonos.
//
// 🔬 MUTACIÓN: añadir un `SessionID` a la Clave (o acumular con `+=` en Observa) ⇒
// rojo con 150 en vez de 50.
func TestTresTelefonosDeUnEdgeNoTriplicanLasInferencias(t *testing.T) {
	t.Parallel()
	st := inferstats.New()
	k := inferstats.Clave{TenantID: "t-1", EdgeID: "edge-1"}
	// El mismo Edge late tres veces (una por teléfono) con el MISMO acumulado.
	for range 3 {
		st.Observa(k, inferstats.Parte{PorRegimen: map[string]int64{"caliente": 50}})
	}
	if got := st.Agrega().PorRegimen["caliente"]; got != 50 {
		t.Fatalf("inferencias en caliente = %d, quiero 50: el parte es del PROCESO del Edge y "+
			"llega repetido en el latido de cada sesión suya", got)
	}
	if edges := st.Agrega().Edges; edges != 1 {
		t.Fatalf("Edges reportando = %d, quiero 1", edges)
	}
}

// TestElUltimoParteSUSTITUYE: lo que llega es el acumulado de la vida del proceso, no
// el delta desde el último latido. Sustituir es lo correcto; acumular contaría cada
// inferencia una vez por latido, y con un latido cada pocos segundos eso es un factor
// de cien.
func TestElUltimoParteSUSTITUYE(t *testing.T) {
	t.Parallel()
	st := inferstats.New()
	k := inferstats.Clave{TenantID: "t-1", EdgeID: "edge-1"}
	st.Observa(k, inferstats.Parte{PorRegimen: map[string]int64{"frio": 1}})
	st.Observa(k, inferstats.Parte{PorRegimen: map[string]int64{"frio": 2}})
	st.Observa(k, inferstats.Parte{PorRegimen: map[string]int64{"frio": 3}})
	if got := st.Agrega().PorRegimen["frio"]; got != 3 {
		t.Fatalf("frio = %d, quiero 3 (el último parte manda, no la suma de los tres)", got)
	}
}

// TestVariosEdgesSeSuman: entre Edges DISTINTOS sí se suma — son procesos distintos y
// cada uno cuenta sus propias inferencias.
//
// 🔴 DOS EDGES DEL MISMO TENANT, y ese detalle del fixture es la mitad del test. Un
// tenant con DOS instalaciones es un caso real de esta casa (es literalmente el
// escenario que T1.7-8 vino a arreglar), y es el único que distingue una clave por
// (tenant, edge) de una clave por tenant a secas. Con un fixture de dos tenants, una
// clave que ignorase el EdgeID pasaría el test sin decir nada — se comprobó dejándolo
// así primero.
//
// 🔬 MUTACIÓN: quitar el EdgeID de la clave de Observa ⇒ rojo (lote=5, un solo Edge).
func TestVariosEdgesSeSuman(t *testing.T) {
	t.Parallel()
	st := inferstats.New()
	st.Observa(inferstats.Clave{TenantID: "t-1", EdgeID: "e-1"},
		inferstats.Parte{PorClase: map[string]int64{"lote": 10, "interactivo": 1}})
	st.Observa(inferstats.Clave{TenantID: "t-1", EdgeID: "e-2"},
		inferstats.Parte{PorClase: map[string]int64{"lote": 5}})

	ag := st.Agrega()
	if ag.PorClase["lote"] != 15 || ag.PorClase["interactivo"] != 1 {
		t.Fatalf("suma entre los dos Edges del MISMO tenant = %v, quiero lote=15 interactivo=1: "+
			"un tenant con dos instalaciones tiene dos Ollamas y dos juegos de contadores", ag.PorClase)
	}
	if ag.Edges != 2 {
		t.Fatalf("Edges = %d, quiero 2", ag.Edges)
	}

	// Y entre tenants distintos, obviamente, también.
	st.Observa(inferstats.Clave{TenantID: "t-2", EdgeID: "e-1"},
		inferstats.Parte{PorClase: map[string]int64{"lote": 100}})
	if ag := st.Agrega(); ag.PorClase["lote"] != 115 || ag.Edges != 3 {
		t.Fatalf("con un tercer Edge de otro tenant: lote=%d Edges=%d, quiero 115 y 3",
			ag.PorClase["lote"], ag.Edges)
	}
}

// TestUnEdgeQueSeVaNoHACEBAJAR_LaSuma: el almacén NO olvida.
//
// 🔴 NO ES UNA FUGA, ES LA CONDUCTA. Si al desconectarse un Edge se borrara su parte,
// la suma de la flota BAJARÍA, Prometheus lo leería como un reinicio de contador y
// `rate()` perdería el tramo. Un Edge que se va tiene que dejar su serie PLANA —dejó
// de haber inferencias—, no un escalón hacia abajo.
//
// El test lo ejerce por lo único observable desde fuera: dejar de recibir partes de un
// Edge no cambia nada.
func TestUnEdgeQueSeVaNoHACEBAJAR_LaSuma(t *testing.T) {
	t.Parallel()
	st := inferstats.New()
	st.Observa(inferstats.Clave{TenantID: "t", EdgeID: "e-1"}, inferstats.Parte{PorRegimen: map[string]int64{"frio": 7}})
	st.Observa(inferstats.Clave{TenantID: "t", EdgeID: "e-2"}, inferstats.Parte{PorRegimen: map[string]int64{"frio": 3}})
	antes := st.Agrega().PorRegimen["frio"]

	// e-2 se desconecta: solo late e-1, con su mismo acumulado.
	for range 5 {
		st.Observa(inferstats.Clave{TenantID: "t", EdgeID: "e-1"}, inferstats.Parte{PorRegimen: map[string]int64{"frio": 7}})
	}
	if despues := st.Agrega().PorRegimen["frio"]; despues != antes {
		t.Fatalf("la suma bajó de %d a %d al irse un Edge: Prometheus lo leería como un reinicio "+
			"de contador y rate() perdería el tramo", antes, despues)
	}
}

// TestLasMuestrasConservanElNoMedible: nil + nil sigue siendo nil.
//
// Un Edge que NO mide una fase no puede arrastrar la suma a 0 y convertirla en «cero
// muestras», que es una afirmación distinta de «no lo sé» — y la que se publicaría
// como una serie con valor.
//
// 🔬 MUTACIÓN: que sumarInt devuelva `&total` también cuando v es nil ⇒ rojo.
func TestLasMuestrasConservanElNoMedible(t *testing.T) {
	t.Parallel()
	st := inferstats.New()
	st.Observa(inferstats.Clave{TenantID: "t", EdgeID: "viejo"}, inferstats.Parte{})
	if n := st.Agrega().MuestrasPrefill; n != nil {
		t.Fatalf("con ningún Edge midiendo, MuestrasPrefill = %d; tiene que ser nil (no medible)", *n)
	}

	st.Observa(inferstats.Clave{TenantID: "t", EdgeID: "nuevo"}, inferstats.Parte{MuestrasPrefill: ptr(91)})
	n := st.Agrega().MuestrasPrefill
	if n == nil || *n != 91 {
		t.Fatalf("MuestrasPrefill = %v, quiero 91: el Edge que no mide no debe restar", n)
	}
}

// TestObservaCopiaLosMapas: el mapa llega del proto (o de un test) y el almacén no
// puede quedarse con el mismo respaldo que el llamante — una mutación posterior
// cambiaría el parte «ya guardado». Mismo criterio que cloneReasons en fleet.
func TestObservaCopiaLosMapas(t *testing.T) {
	t.Parallel()
	st := inferstats.New()
	m := map[string]int64{"frio": 1}
	st.Observa(inferstats.Clave{TenantID: "t", EdgeID: "e"}, inferstats.Parte{PorRegimen: m})
	m["frio"] = 999
	if got := st.Agrega().PorRegimen["frio"]; got != 1 {
		t.Fatalf("frio = %d: el almacén comparte respaldo con el llamante", got)
	}
}

// TestNilSafeYConcurrente: el almacén lo escribe el carril de cada stream y lo lee el
// scrape de /metrics, que corren en goroutines distintas — y `Gather` colecta EN
// PARALELO. Sin candado esto sería una carrera de datos real, que `-race` caza.
func TestNilSafeYConcurrente(t *testing.T) {
	t.Parallel()
	var nada *inferstats.Store
	nada.Observa(inferstats.Clave{EdgeID: "e"}, inferstats.Parte{})
	if ag := nada.Agrega(); ag.Edges != 0 || ag.PorRegimen == nil {
		t.Fatal("un *Store nil tiene que devolver un agregado vacío y utilizable")
	}

	st := inferstats.New()
	// Ocho Edges con id fijo: `string(rune('a'+i))` haría lo mismo pero dispara el G115
	// de gosec (conversión int→rune), y un lint apagado por un fixture es peor negocio
	// que una lista escrita.
	edges := []string{"e-1", "e-2", "e-3", "e-4", "e-5", "e-6", "e-7", "e-8"}
	var wg sync.WaitGroup
	for _, edge := range edges {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				st.Observa(inferstats.Clave{TenantID: "t", EdgeID: edge},
					inferstats.Parte{PorRegimen: map[string]int64{"frio": 1}})
				_ = st.Agrega()
			}
		}()
	}
	wg.Wait()
	if got := st.Agrega().PorRegimen["frio"]; got != 8 {
		t.Fatalf("frio = %d, quiero 8 (uno por Edge, sustituyendo)", got)
	}
}
