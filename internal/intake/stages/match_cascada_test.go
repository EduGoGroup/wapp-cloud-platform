package stages_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/textmatch"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/cart"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake/stages"
)

// match_cascada_test.go — LA CASCADA: el umbral que NO se baja (D-044.45) y el
// prefiltro por longitud, contra un ORÁCULO ingenuo.

// ---------------------------------------------------------------------------
// D-044.45 — EL UMBRAL SE QUEDA EN 0,85
// ---------------------------------------------------------------------------

// TestCascada_ElUmbralEs085YLosDosHechosQuedanFIJADOS.
//
// 🔴 FIJA LOS DOS HECHOS A LA VEZ, que es lo que T3.1 aprendió a la mala: que a 0,85
// «ñoquis»/«noquis» NO casa, y que a 0,80 sí. Así ninguno de los dos se puede mover
// en silencio para que un criterio salga verde — que es exactamente el patrón que en
// la Ola 2 dio «9 de 9 de P3» midiendo la laxitud del validador.
func TestCascada_ElUmbralEs085YLosDosHechosQuedanFIJADOS(t *testing.T) {
	require.Equal(t, 0.85, textmatch.DefaultFuzzyThreshold, "el umbral canónico de D-044.45")
	require.InDelta(t, 0.15, stages.MargenLongitud, 1e-9,
		"el prefiltro del barrido está DERIVADO del umbral: si uno se mueve, el otro también")

	c := stages.CascadaPorDefecto()
	require.False(t, c.HasGrayZone(),
		"🔴 el comparador del bucle es determinista POR CONSTRUCCIÓN: el escalón caro no está cableado en él")

	// 6 runas, 1 edición ⇒ 1 − 1/6 = 0,8333 < 0,85: NO casa, y la rescata la zona gris.
	r, err := c.Compare(context.Background(), "ñoquis", "noquis")
	require.NoError(t, err)
	require.Equal(t, textmatch.OutcomeNoMatch, r.Outcome)
	require.InDelta(t, 0.8333, r.Confidence, 0.0001)

	// El MISMO par con el umbral que se descartó (0,80) sí casaría.
	r80, err := textmatch.NewFuzzy(0.80).Compare(context.Background(), "ñoquis", "noquis")
	require.NoError(t, err)
	require.Equal(t, textmatch.OutcomeMatch, r80.Outcome,
		"bajar a 0,80 rescataría la palabra corta — y por eso NO se hizo: casaría también torta/tarta")

	// 19 runas y 1 edición ⇒ 0,947: la palabra larga sí la rescata el determinista.
	rLargo, err := c.Compare(context.Background(), "tequenos congelados", "Tequeños congelados")
	require.NoError(t, err)
	require.Equal(t, textmatch.OutcomeMatch, rLargo.Outcome)
	require.InDelta(t, 0.9473, rLargo.Confidence, 0.0001)
}

// TestCascada_TortaYTartaNOCasan es el riesgo concreto que justifica no bajar el
// umbral: dos artículos vecinos de una pastelería a UNA edición de distancia.
func TestCascada_TortaYTartaNOCasan(t *testing.T) {
	r, err := stages.CascadaPorDefecto().Compare(context.Background(), "torta", "tarta")
	require.NoError(t, err)
	require.Equal(t, textmatch.OutcomeNoMatch, r.Outcome,
		"🔴 con 0,80 esto casaría y el presupuesto llevaría el artículo equivocado")
	require.InDelta(t, 0.80, r.Confidence, 0.0001)
}

// ---------------------------------------------------------------------------
// EL PREFILTRO POR LONGITUD, CONTRA UN ORÁCULO
// ---------------------------------------------------------------------------

// catalogoDiferencial es una carta de 40 artículos SIN tags y SIN variantes, para
// que los escalones por clave que no son la etiqueta no puedan intervenir: así el
// oráculo lineal es exacto y una discrepancia solo puede venir del prefiltro.
func catalogoDiferencial() cart.Catalog {
	cat := cart.Catalog{Categories: []cart.Category{{Code: "1", Label: "Carta"}}}
	bases := []string{
		"Pan", "Café", "Torta", "Tarta", "Pizza", "Empanada", "Tequeños",
		"Alfajor de maicena", "Torta de chocolate húmeda", "Jugo natural de naranja",
	}
	for i, b := range bases {
		for j := range 4 {
			cat.Categories[0].Items = append(cat.Categories[0].Items, cart.Article{
				Code:  fmt.Sprintf("%d", i*4+j+1),
				SKU:   fmt.Sprintf("ART-%02d-%d", i, j),
				Label: fmt.Sprintf("%s %s", b, []string{"clásico", "grande", "familiar", "premium"}[j]),
				Price: float64(100 * (i*4 + j + 1)),
			})
		}
	}
	return cat
}

// sondasDiferenciales son los textos con los que se interroga al catálogo: exactos,
// con errata en palabra larga, con errata en palabra corta (que NO debe casar) y
// completamente ajenos.
var sondasDiferenciales = []string{
	"pan clásico", "pan clasico", "pon clásico",
	"tequeños grande", "tequenos grande", "tequeñoss grande",
	"torta de chocolate húmeda familiar", "torta de chocolate humeda familiar",
	"tarta clásico", "torta clásico",
	"alfajor de maicena premium", "alfajor de maisena premium",
	// 🔴 EL PAR DEL BORDE: 23 runas contra las 26 de «Alfajor de maicena premium»,
	// distancia 3 y tope 3 ⇒ sim 0,8846, casa POR LOS PELOS. Está aquí para que el
	// diferencial note un prefiltro que se pase de estricto POR UNO — sin este par,
	// cambiar el «>» del descarte por un «>=» no pondría rojo a nadie desde fuera.
	"alfajor de maicena prem",
	"jugo natural de naranja grande", "jugo natural de naraja grande",
	"bicicleta de montaña", "algo que no existe en ninguna carta",
	"pizza", "empanada familiar", "café premium", "cafe premium",
}

// TestDiferencial_ElPrefiltroNoDESCARTANingunMatch compara, sonda a sonda, lo que
// produce la etapa contra un ORÁCULO ingenuo: recorrer TODAS las etiquetas con la
// misma cascada, sin prefiltro, y quedarse con la mejor.
//
// 🔴 ES EL TEST QUE AUTORIZA EL PREFILTRO. Saltarse candidatos sin compararlos es
// una optimización que, mal hecha, se come matches legítimos en silencio: la línea
// saldría `unmatched` y nadie ataría el renglón sin precio con una desigualdad de
// longitudes escrita meses antes. Aquí la aritmética se comprueba, no se argumenta.
func TestDiferencial_ElPrefiltroNoDESCARTANingunMatch(t *testing.T) {
	cat := catalogoDiferencial()
	idx := indiceDe(t, cat)
	m, _ := matchDe(t) // SIN zona gris: aquí solo se mide lo determinista

	casados, sinCasar, porFuzzy := 0, 0, 0
	for _, sonda := range sondasDiferenciales {
		t.Run(sonda, func(t *testing.T) {
			art, err := m.Run(context.Background(), jobDeAmbar(), stages.EntradaMatch{
				Cantidades: p4De(llm.NormalizedItem{Product: sonda, Qty: 1, Evidence: sonda}),
				Indice:     idx,
			})
			require.NoError(t, err)
			linea := art.Lines[0]

			skuOraculo, confOraculo, estrategiaOraculo := oraculoLineal(t, cat, sonda)
			if skuOraculo == "" {
				require.Equal(t, stages.KindUnmatched, linea.Kind,
					"el oráculo no encuentra nada para %q y la etapa sí: el prefiltro NO puede añadir matches", sonda)
				sinCasar++
				return
			}
			require.Equalf(t, stages.KindMatched, linea.Kind,
				"🔴 el oráculo casa %q con %s (%.4f) y la etapa lo dejó SIN MATCH: el prefiltro se comió un match",
				sonda, skuOraculo, confOraculo)
			require.Equal(t, skuOraculo, linea.SKU)
			require.InDelta(t, confOraculo, linea.Match.Confidence, 1e-9)
			casados++
			if estrategiaOraculo == "fuzzy" {
				porFuzzy++
			}
		})
	}

	// 🔴 META-TEST: sin esto el diferencial podría salir verde sin haber ejercitado
	// nada —todas las sondas sin casar, o todas casando por igualdad exacta, que es
	// el único camino que el prefiltro no puede romper—.
	require.Greater(t, casados, 5, "el corpus tiene que CASAR de verdad")
	require.Greater(t, sinCasar, 1, "y tiene que traer textos que NO casan")
	require.Greaterf(t, porFuzzy, 3,
		"y sobre todo tiene que ejercitar el FUZZY (%d casos): es el único escalón que el prefiltro puede romper", porFuzzy)
}

// oraculoLineal es la implementación INGENUA: recorre todas las etiquetas del
// catálogo con la cascada por defecto, sin saltarse ninguna, y devuelve la mejor
// (empate ⇒ la primera del documento). Es deliberadamente lenta y obvia.
func oraculoLineal(t *testing.T, cat cart.Catalog, texto string) (sku string, conf float64, estrategia string) {
	t.Helper()
	c := stages.CascadaPorDefecto()
	for _, categoria := range cat.Categories {
		for _, a := range categoria.Items {
			r, err := c.Compare(context.Background(), texto, a.Label)
			require.NoError(t, err)
			if r.Outcome == textmatch.OutcomeMatch && r.Confidence > conf {
				sku, conf, estrategia = a.SKU, r.Confidence, r.Strategy
			}
		}
	}
	return sku, conf, estrategia
}

// ---------------------------------------------------------------------------
// EL ORDEN DE LOS ESCALONES
// ---------------------------------------------------------------------------

// TestCascada_ElBarridoSeQuedaConLaMEJOR_NoConLaPrimera: con dos artículos que
// pasan el umbral, gana el más parecido aunque esté después en el documento.
func TestCascada_ElBarridoSeQuedaConLaMEJOR_NoConLaPrimera(t *testing.T) {
	cat := cart.Catalog{Categories: []cart.Category{{Code: "1", Label: "Carta", Items: []cart.Article{
		{Code: "1", SKU: "LEJOS", Label: "Empanada de carne", Price: 100},
		{Code: "2", SKU: "CERCA", Label: "Empanada de carna", Price: 200},
	}}}}
	m, _ := matchDe(t)

	art, err := m.Run(context.Background(), jobDeAmbar(), stages.EntradaMatch{
		Cantidades: p4De(llm.NormalizedItem{Product: "empanada de carnas", Qty: 1, Evidence: "x"}),
		Indice:     indiceDe(t, cat),
	})
	require.NoError(t, err)
	require.Equal(t, "CERCA", art.Lines[0].SKU,
		"«carnas» está a 1 edición de «carna» y a 2 de «carne»: gana la mejor, no la primera")
}

// TestCascada_ElSKUNoSeNormaliza: el sku es opaco. Normalizarlo colapsaría
// «TORTA-CHOC» con «torta choc» y haría ambiguos dos identificadores que el dueño
// escribió distintos.
func TestCascada_ElSKUNoSeNormaliza(t *testing.T) {
	m, _ := matchDe(t)
	idx := indiceDe(t, catalogoAmbar())

	exacto, err := m.Run(context.Background(), jobDeAmbar(), stages.EntradaMatch{
		Cantidades: p4De(llm.NormalizedItem{Product: "TEQ-30", Qty: 1, Evidence: "x"}),
		Indice:     idx,
	})
	require.NoError(t, err)
	require.Equal(t, "TEQ-30", exacto.Lines[0].SKU)
	require.Equal(t, "sku", exacto.Lines[0].Match.Strategy)

	minusculas, err := m.Run(context.Background(), jobDeAmbar(), stages.EntradaMatch{
		Cantidades: p4De(llm.NormalizedItem{Product: "teq 30", Qty: 1, Evidence: "x"}),
		Indice:     idx,
	})
	require.NoError(t, err)
	require.Equal(t, stages.KindUnmatched, minusculas.Lines[0].Kind,
		"«teq 30» NO es el sku «TEQ-30»: el identificador de negocio se compara literal")
}

// TestCascada_LaAmbiguedadNoSeRESUELVE_seOfreceAlEscalonSiguiente: un tag o una
// variante que casan VARIOS artículos no deciden nada.
func TestCascada_LaAmbiguedadNoSeRESUELVE_seOfreceAlEscalonSiguiente(t *testing.T) {
	cat := cart.Catalog{Categories: []cart.Category{{Code: "1", Label: "Carta", Items: []cart.Article{
		{Code: "1", SKU: "A", Label: "Alfajor", Price: 100, Tags: []string{"vegano"}},
		{Code: "2", SKU: "B", Label: "Brownie", Price: 200, Tags: []string{"vegano"}},
		{Code: "3", SKU: "C", Label: "Cheesecake", Price: 300, Tags: []string{"sin gluten"}},
	}}}}
	m, _ := matchDe(t)

	varios, err := m.Run(context.Background(), jobDeAmbar(), stages.EntradaMatch{
		Cantidades: p4De(llm.NormalizedItem{Product: "vegano", Qty: 1, Evidence: "x"}),
		Indice:     indiceDe(t, cat),
	})
	require.NoError(t, err)
	require.Equal(t, stages.KindUnmatched, varios.Lines[0].Kind,
		"«vegano» es el tag de DOS artículos: elegir uno sería inventar cuál pidió el cliente")

	uno, err := m.Run(context.Background(), jobDeAmbar(), stages.EntradaMatch{
		Cantidades: p4De(llm.NormalizedItem{Product: "sin gluten", Qty: 1, Evidence: "x"}),
		Indice:     indiceDe(t, cat),
	})
	require.NoError(t, err)
	require.Equal(t, "C", uno.Lines[0].SKU, "con UN solo artículo el tag sí resuelve")
	require.Equal(t, "tag", uno.Lines[0].Match.Strategy)
}

// TestCascada_SinZonaGrisElBorradorSALEIGUAL: el tercer escalón es opcional, y su
// ausencia se paga en renglones para el dueño, no en errores.
func TestCascada_SinZonaGrisElBorradorSALEIGUAL(t *testing.T) {
	m, _ := matchDe(t) // sin ConZonaGris

	art, err := m.Run(context.Background(), jobDeAmbar(), stages.EntradaMatch{
		Cantidades: p4De(itemsDeAmbar()...),
		Indice:     indiceDe(t, catalogoAmbar()),
	})
	require.NoError(t, err)
	require.Zero(t, art.GrayZoneCalls)
	require.Equal(t, stages.KindUnmatched, art.Lines[0].Kind, "la torta de chocolate se queda sin resolver…")
	require.Equal(t, 490.0, precio(t, *lineaConSKU(art, "TEQ-30")), "…pero lo determinista sigue funcionando")
	require.Equal(t, 800.0, precio(t, *lineaConSKU(art, "DECO-INF")))
}

// TestCascada_ElComparadorQueREVIENTA_noPasaPorUnUnmatched: un comparador que falla
// es un defecto nuestro. La línea sale sin match —no se puede hacer otra cosa— pero
// queda en el log a nivel Error, no disfrazada de «el catálogo no lo tiene».
func TestCascada_ElComparadorQueREVIENTA_noPasaPorUnUnmatched(t *testing.T) {
	espia := &comparadorEspia{real: stages.CascadaPorDefecto(), fallarCon: errZonaGris, fallarDesd: 1}
	m, _ := matchDe(t, stages.ConComparador(espia))

	art, err := m.Run(context.Background(), jobDeAmbar(), stages.EntradaMatch{
		Cantidades: p4De(llm.NormalizedItem{Product: "hamburgueza", Qty: 1, Evidence: "x"}),
		Indice:     indiceDe(t, catalogoAmbar()),
	})
	require.NoError(t, err)
	require.Equal(t, stages.KindUnmatched, art.Lines[0].Kind)
	require.Positive(t, espia.llamadas)
}
