//go:build pendiente

package stages_test

// Trozo de match_cascade_test.go (E-13): las dos promesas del PREFILTRO del barrido
// (stages.LengthMargin) — que nunca descarta un match, contra un ORÁCULO ingenuo, y que
// descarta de verdad, contando comparaciones en vez de midiendo el reloj.

import (
	"context"
	"fmt"
	"testing"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/textmatch"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/stages"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo/indice"
)

// differentialCatalog es una carta de 40 artículos SIN tags y SIN variantes, para que
// los escalones por clave que no son la etiqueta no puedan intervenir: así el oráculo
// lineal es exacto y una discrepancia solo puede venir del prefiltro.
func differentialCatalog() catalogo.Catalog {
	bases := []string{
		"Pan", "Café", "Torta", "Tarta", "Pizza", "Empanada", "Tequeños",
		"Alfajor de maicena", "Torta de chocolate húmeda", "Jugo natural de naranja",
	}
	sizes := []string{"clásico", "grande", "familiar", "premium"}
	items := make([]catalogo.Article, 0, len(bases)*len(sizes))
	for i, base := range bases {
		for j, size := range sizes {
			items = append(items, catalogo.Article{
				Code:  fmt.Sprintf("%d", i*4+j+1),
				SKU:   fmt.Sprintf("ART-%02d-%d", i, j),
				Label: base + " " + size,
				Price: float64(100 * (i*4 + j + 1)),
			})
		}
	}
	return oneCategory(items...)
}

// differentialProbes son los textos con los que se interroga al catálogo: exactos, con
// errata en palabra larga, con errata en palabra corta (que NO debe casar) y ajenos.
var differentialProbes = []string{
	"pan clásico", "pan clasico", "pon clásico",
	"tequeños grande", "tequenos grande", "tequeñoss grande",
	"torta de chocolate húmeda familiar", "torta de chocolate humeda familiar",
	"tarta clásico", "torta clásico",
	"alfajor de maicena premium", "alfajor de maisena premium",
	// 🔴 EL PAR DEL BORDE: 23 runas contra las 26 de «Alfajor de maicena premium»,
	// distancia 3 y tope 3 ⇒ 0,8846, casa POR LOS PELOS. Está para que el diferencial
	// note un prefiltro que se pase de estricto POR UNO.
	"alfajor de maicena prem",
	"jugo natural de naranja grande", "jugo natural de naraja grande",
	"bicicleta de montaña", "algo que no existe en ninguna carta",
	"pizza", "empanada familiar", "café premium", "cafe premium",
}

// naiveOracle es la implementación INGENUA: recorre TODAS las etiquetas con la cascada
// por defecto, sin saltarse ninguna, y devuelve la mejor (empate ⇒ la primera del
// documento). Es deliberadamente lenta y obvia.
func naiveOracle(t *testing.T, cat catalogo.Catalog, text string) (sku string, confidence float64, strategy string) {
	t.Helper()
	cascade := stages.DefaultCascade()
	for _, category := range cat.Categories {
		for _, a := range category.Items {
			r, err := cascade.Compare(context.Background(), text, a.Label)
			if err != nil {
				t.Fatalf("la cascada por defecto falló con %q: %v", a.Label, err)
			}
			if r.Outcome == textmatch.OutcomeMatch && r.Confidence > confidence {
				sku, confidence, strategy = a.SKU, r.Confidence, r.Strategy
			}
		}
	}
	return sku, confidence, strategy
}

// TestMatchRun_PrefilterNeverDropsAMatch compara, sonda a sonda, lo que produce la etapa
// contra el oráculo ingenuo.
//
// 🔴 ES EL TEST QUE AUTORIZA EL PREFILTRO. Saltarse candidatos sin compararlos es una
// optimización que, mal hecha, se come matches legítimos en silencio: la línea saldría
// `unmatched` y nadie ataría el renglón sin precio con una desigualdad escrita meses
// antes. Aquí la aritmética se comprueba, no se argumenta.
func TestMatchRun_PrefilterNeverDropsAMatch(t *testing.T) {
	cat := differentialCatalog()
	idx := indexOf(t, cat)
	b := newMatchBench(t) // SIN zona gris: aquí solo se mide lo determinista

	matched, unmatched, byFuzzy := 0, 0, 0
	for _, probe := range differentialProbes {
		t.Run(probe, func(t *testing.T) {
			art := b.run(t, stages.MatchInput{Quantities: p4Of(product(probe)), Index: idx})
			line := art.Lines[0]

			wantSKU, wantConfidence, wantStrategy := naiveOracle(t, cat, probe)
			if wantSKU == "" {
				if line.Kind != stages.KindUnmatched {
					t.Fatalf("el oráculo no encuentra nada para %q y la etapa casó %s: el prefiltro NO puede añadir matches", probe, line.SKU)
				}
				unmatched++
				return
			}
			if line.Kind != stages.KindMatched {
				t.Fatalf("el oráculo casa %q con %s (%.4f) y la etapa lo dejó SIN MATCH: el prefiltro se comió un match",
					probe, wantSKU, wantConfidence)
			}
			if line.SKU != wantSKU {
				t.Fatalf("la etapa casó %q con %s y el oráculo con %s", probe, line.SKU, wantSKU)
			}
			if diff := line.Match.Confidence - wantConfidence; diff > 1e-9 || diff < -1e-9 {
				t.Fatalf("confianza de %q = %v, la del oráculo es %v", probe, line.Match.Confidence, wantConfidence)
			}
			matched++
			if wantStrategy == "fuzzy" {
				byFuzzy++
			}
		})
	}

	// 🔴 META-TEST: sin esto el diferencial podría salir verde sin haber ejercitado nada
	// —todas las sondas sin casar, o todas casando por igualdad exacta, que es el único
	// camino que el prefiltro no puede romper—.
	if matched <= 5 || unmatched <= 1 || byFuzzy <= 3 {
		t.Fatalf("el corpus no ejercita el prefiltro: %d casan (%d por fuzzy) y %d no; hacen falta >5, >3 y >1",
			matched, byFuzzy, unmatched)
	}
}

// TestMatchRun_BorderPairStillMatches fija el par del borde con LITERALES, sin oráculo:
// distancia 3 con tope 3.
func TestMatchRun_BorderPairStillMatches(t *testing.T) {
	b := newMatchBench(t)
	art := b.runItems(t, differentialCatalog(), product("alfajor de maicena prem"))
	assertMatched(t, art.Lines[0], "ART-07-3", "fuzzy")
	if diff := art.Lines[0].Match.Confidence - (1 - 3.0/26.0); diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("confianza = %v, se esperaba 1 − 3/26", art.Lines[0].Match.Confidence)
	}
}

// Las cifras del peor caso que el sistema admite.
const (
	// ceilingArticles es el techo del índice (D-044.44): `indice.MaxArticulos`.
	ceilingArticles = 2000
	// ceilingItems es el tope de ítems de un pedido (D-044.39).
	ceilingItems = 10
	// maxSweepComparisons es lo que SÍ se afirma del prefiltro: la DÉCIMA parte de las
	// 20.000 comparaciones del barrido ingenuo (2.000 artículos × 10 ítems). Medido
	// contra el paquete viejo @ 4cd9cfb con este mismo fixture: 888, y como mucho 200 en
	// un ítem. Una regresión que desactivara el prefiltro lo llevaría a 20.000, y una
	// que dejara solo la cota de longitud seguiría cazándose.
	maxSweepComparisons = ceilingArticles * ceilingItems / 10
)

// Las familias y las presentaciones de catalogOfSize. Hay 10 y 5, así que la familia `i`
// solo existe con la presentación `i % 5`: es lo que deja fabricar sondas que casi casan.
var (
	sweepFamilies = []string{
		"Torta", "Tarta", "Alfajor", "Empanada de carne", "Pizza napolitana grande",
		"Jugo natural de naranja exprimido", "Café", "Pan de masa madre", "Tequeños congelados",
		"Cheesecake de frutos rojos del bosque",
	}
	sweepSizes = []string{"clásico", "grande", "familiar", "premium", "individual"}
)

// catalogOfSize genera un catálogo de n artículos con etiquetas de longitudes VARIADAS:
// si todas midieran lo mismo, el prefiltro descartaría todo o nada y la medición no se
// parecería a una carta real.
func catalogOfSize(n int) catalogo.Catalog {
	items := make([]catalogo.Article, 0, n)
	for i := range n {
		items = append(items, catalogo.Article{
			Code:  fmt.Sprintf("%d", i+1),
			SKU:   fmt.Sprintf("SKU-%04d", i),
			Label: fmt.Sprintf("%s %s %d", sweepFamilies[i%len(sweepFamilies)], sweepSizes[i%len(sweepSizes)], i),
			Price: float64(100 + i),
		})
	}
	return oneCategory(items...)
}

// nearMissProbe devuelve un texto que NO casa ninguna etiqueta de catalogOfSize pero se
// PARECE a muchas: una familia que existe, con la presentación de al lado —que para esa
// familia no existe— y un número fuera de la carta.
//
// 🔴 QUE SE PAREZCA ES LO QUE HACE VÁLIDA LA MEDIDA. Una sonda ajena («bicicleta de
// montaña…», la del test viejo de rendimiento) la descarta ENTERA la cota de
// composición: el comparador no se llama ni una vez y el test mediría un bucle vacío.
func nearMissProbe(i int) string {
	return fmt.Sprintf("%s %s %d", sweepFamilies[i%len(sweepFamilies)], sweepSizes[(i+1)%len(sweepSizes)], 5000+i)
}

// TestMatchRun_PrefilterSkipsMostOfTheCeilingCatalog es el criterio de latencia de
// D-044.44 dicho de forma DETERMINISTA: con el catálogo y el pedido en su techo, y
// ningún ítem casando (los diez pagan el barrido), el comparador se llama una fracción
// de las 2.000 veces por ítem que costaría el barrido ingenuo.
//
// 🔴 NO MIDE EL RELOJ A PROPÓSITO. El test viejo afirmaba un p99 absoluto y estuvo rojo
// en contenedor sin que nadie lo viera; luego afirmó un cociente de tiempos. Contar
// comparaciones caza la MISMA regresión —que el barrido vuelva a ser O(catálogo) sin
// filtro— en cualquier máquina y con `-race`. El absoluto (≤ 5 ms p99 por ítem) se
// acredita midiendo en el hardware de producción, no aquí.
func TestMatchRun_PrefilterSkipsMostOfTheCeilingCatalog(t *testing.T) {
	if indice.MaxArticulos != ceilingArticles {
		t.Fatalf("el techo del índice es %d y este test mide con %d", indice.MaxArticulos, ceilingArticles)
	}
	idx := indexOf(t, catalogOfSize(ceilingArticles))
	if idx.Articulos() != ceilingArticles {
		t.Fatalf("el índice tiene %d artículos, se esperaban %d", idx.Articulos(), ceilingArticles)
	}
	items := make([]llm.NormalizedItem, ceilingItems)
	for i := range items {
		items[i] = product(nearMissProbe(i))
	}

	spy := newSpy()
	b := newMatchBench(t, stages.WithComparator(spy))
	art := b.run(t, stages.MatchInput{Quantities: p4Of(items...), Index: idx})

	assertLineCount(t, art, ceilingItems+1, "los diez ítems y el envío")
	for i, l := range art.Lines[:ceilingItems] {
		if l.Kind != stages.KindUnmatched {
			t.Fatalf("el ítem %d casó %s: en este fixture ninguno puede casar", i, l.SKU)
		}
	}
	if spy.calls == 0 {
		t.Fatal("el comparador no se llamó ni una vez: el fixture se descarta entero y no mide el prefiltro")
	}
	if spy.calls > maxSweepComparisons {
		t.Fatalf("%d comparaciones para %d ítems contra un catálogo de %d: el máximo es %d — o se rompió el prefiltro, "+
			"o el barrido dejó de saltarse lo que no puede casar", spy.calls, ceilingItems, ceilingArticles, maxSweepComparisons)
	}
}
