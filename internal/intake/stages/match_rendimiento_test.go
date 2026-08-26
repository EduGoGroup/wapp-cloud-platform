package stages_test

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/textmatch"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/cart"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake/stages"
)

// match_rendimiento_test.go — EL CRITERIO DE LATENCIA DE D-044.44 APLICADO AL MATCH:
// ≤ 5 ms p99 POR ÍTEM con un catálogo de 2.000 artículos, que es el techo declarado.
//
// T3.7 midió lo que cuestan las cuatro búsquedas por clave (µs). Esto mide la etapa
// ENTERA, que es lo que el criterio dice: el barrido del escalón fuzzy incluido, que
// es O(artículos) y es el único trabajo de esta tarea que crece con el catálogo.

const (
	// plazoPorItemMatch es D-044.44 escrito como número.
	plazoPorItemMatch = 5 * time.Millisecond
	// articulosDelTecho es la cota de MaxArticulos (D-044.44 · 2).
	articulosDelTecho = 2000
	// itemsPorPedido es el tope de ítems de un pedido (D-044.39). Se mide con el
	// pedido MÁS GRANDE que el sistema admite, no con uno cómodo.
	itemsPorPedido = 10
	// corridas son las muestras del p99. Con 200, el p99 es la segunda peor: es un
	// número comparable entre corridas, no un máximo disfrazado.
	corridas = 200
)

// TestRendimiento_ElMatchPorItemCabeEnElPlazo.
//
// # QUÉ SE MIDE
//
// Un `Run` COMPLETO —normalizar el catálogo para el barrido, cruzar los 10 ítems y
// serializar el artefacto— dividido entre los 10 ítems. Incluir en el «por ítem» el
// coste que en realidad es POR JOB (la preparación del barrido) es a propósito: hace
// el número CONSERVADOR, así que si pasa así, en producción sobra más margen.
//
// Los ítems son el PEOR CASO: ninguno casa por clave, así que los diez pagan el
// barrido entero del catálogo. Un fixture que casara por etiqueta mediría un hash.
//
// # 🔴 EL CONTROL, PORQUE UN «≤ 5 ms» SIN CONTROL PUEDE SER UNA TAUTOLOGÍA
//
// Al lado se mide la MISMA búsqueda sin el prefiltro por longitud —la implementación
// ingenua, la que el oráculo del test diferencial usa—. Ese número dice de qué
// protege el criterio; se IMPRIME y no se assertea, porque depende de la máquina.
func TestRendimiento_ElMatchPorItemCabeEnElPlazo(t *testing.T) {
	cat := catalogoDeTalla(articulosDelTecho)
	idx := indiceDe(t, cat)
	require.Equal(t, articulosDelTecho, idx.Articulos())

	items := make([]llm.NormalizedItem, itemsPorPedido)
	for i := range items {
		items[i] = llm.NormalizedItem{Product: sondaSinMatch(i), Qty: 1, Evidence: "x"}
	}
	m, _ := matchDe(t)
	entrada := stages.EntradaMatch{Cantidades: p4De(items...), Indice: idx}

	// Una corrida de calentamiento: la primera paga los mapas del runtime y no es
	// representativa de las 24 horas siguientes.
	primera, err := m.Run(context.Background(), jobDeAmbar(), entrada)
	require.NoError(t, err)
	require.Len(t, primera.Lines, itemsPorPedido+1)
	for _, l := range primera.Lines[:itemsPorPedido] {
		require.Equal(t, stages.KindUnmatched, l.Kind, "el fixture tiene que ser el PEOR caso: ningún ítem puede casar")
	}

	muestras := make([]time.Duration, corridas)
	for i := range corridas {
		t0 := time.Now()
		art, err := m.Run(context.Background(), jobDeAmbar(), entrada)
		muestras[i] = time.Since(t0) / itemsPorPedido
		require.NoError(t, err)
		require.Len(t, art.Lines, itemsPorPedido+1)
	}

	// El control: el mismo barrido SIN prefiltro, hecho aquí a mano.
	control := make([]time.Duration, 20)
	c := stages.CascadaPorDefecto()
	etiquetas := etiquetasDe(cat)
	for i := range control {
		t0 := time.Now()
		for _, it := range items {
			objetivo := textmatch.Normalize(it.Product)
			for _, e := range etiquetas {
				if _, err := c.Compare(context.Background(), objetivo, e); err != nil {
					require.NoError(t, err)
				}
			}
		}
		control[i] = time.Since(t0) / itemsPorPedido
	}

	p50, p99 := percentilDur(muestras, 50), percentilDur(muestras, 99)
	t.Logf("catálogo de %d artículos · pedido de %d ítems, ninguno casa por clave (peor caso)", articulosDelTecho, itemsPorPedido)
	t.Logf("POR ÍTEM con prefiltro (n=%d): p50=%v  p99=%v   ← criterio ≤ %v", corridas, p50, p99, plazoPorItemMatch)
	t.Logf("POR ÍTEM sin prefiltro (n=%d): p50=%v  p99=%v   ← el barrido ingenuo que esto sustituye",
		len(control), percentilDur(control, 50), percentilDur(control, 99))

	require.LessOrEqualf(t, p99, plazoPorItemMatch,
		"el p99 por ítem con %d artículos es %v y el criterio de D-044.44 es %v", articulosDelTecho, p99, plazoPorItemMatch)
}

// catalogoDeTalla genera un catálogo de n artículos con etiquetas de longitudes
// VARIADAS: si todas midieran lo mismo, el prefiltro descartaría todo o nada y la
// medición no se parecería a una carta real.
func catalogoDeTalla(n int) cart.Catalog {
	familias := []string{
		"Torta", "Tarta", "Alfajor", "Empanada de carne", "Pizza napolitana grande",
		"Jugo natural de naranja exprimido", "Café", "Pan de masa madre", "Tequeños congelados",
		"Cheesecake de frutos rojos del bosque",
	}
	presentaciones := []string{"clásico", "grande", "familiar", "premium", "individual"}
	cat := cart.Catalog{Categories: []cart.Category{{Code: "1", Label: "Carta"}}}
	items := make([]cart.Article, 0, n)
	for i := range n {
		items = append(items, cart.Article{
			Code:  fmt.Sprintf("%d", i+1),
			SKU:   fmt.Sprintf("SKU-%04d", i),
			Label: fmt.Sprintf("%s %s %d", familias[i%len(familias)], presentaciones[i%len(presentaciones)], i),
			Price: float64(100 + i),
		})
	}
	cat.Categories[0].Items = items
	return cat
}

// sondaSinMatch devuelve un texto que NO puede casar ninguna etiqueta de
// catalogoDeTalla: familia inexistente y longitud que se parece a las de la carta,
// para que el prefiltro tenga que trabajar en vez de descartarlo todo por tamaño.
func sondaSinMatch(i int) string {
	return fmt.Sprintf("bicicleta de montaña rodado %d con canasto", 20+i)
}

// etiquetasDe extrae las etiquetas del catálogo para el control.
func etiquetasDe(cat cart.Catalog) []string {
	var out []string
	for _, c := range cat.Categories {
		for _, a := range c.Items {
			out = append(out, a.Label)
		}
	}
	return out
}

// percentilDur devuelve el elemento que ocupa el percentil p (sin interpolar).
func percentilDur(muestras []time.Duration, p int) time.Duration {
	orden := slices.Clone(muestras)
	slices.Sort(orden)
	i := (len(orden)*p)/100 - 1
	if i < 0 {
		i = 0
	}
	if i >= len(orden) {
		i = len(orden) - 1
	}
	return orden[i]
}
