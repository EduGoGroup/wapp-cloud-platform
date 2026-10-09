//go:build pendiente

package stages_test

// match_doubles_test.go — el CATÁLOGO de los tests del match y los dos dobles que hacen
// medibles sus promesas: el que anota cada consulta al escalón caro y el que espía QUÉ
// textos llegan al comparador determinista. El store, el job y el log son los comunes
// de doubles_test.go.

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/textmatch"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/stages"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo/indice"
)

// ---------------------------------------------------------------------------
// EL CATÁLOGO DEL FIXTURE
// ---------------------------------------------------------------------------

// ambarCatalog es la carta con la que se replaya el caso Ambar; cada artículo ejercita
// UN escalón distinto de la cascada:
//
//   - TEQ-30 «Tequeños congelados» ⇒ el ítem lo dice IGUAL: escalón exacto.
//   - TORTA-CHOC «Torta chocolate húmedo + crema choc.» ⇒ el cliente dice «torta de
//     chocolate»: ni igual ni parecido en distancia de edición. Es el caso de la ZONA
//     GRIS, y el que tiene variantes.
//   - HAMB «Hamburguesa» ⇒ el de las personalizaciones y los añadidos.
//   - SAL «Sal» y SALSA «Salsa» ⇒ 🔴 LA TRAMPA: están para que «sin sal» tenga contra
//     qué casar por error. Que no case es lo que se mide.
//   - QUESO «Queso» ⇒ el añadido que SÍ es artículo.
//   - DECO-INF «Decoración infantil» ⇒ el añadido facturable del caso Ambar.
func ambarCatalog() catalogo.Catalog {
	return catalogo.Catalog{Categories: []catalogo.Category{
		{Code: "1", Label: "Tortas", Items: []catalogo.Article{
			{Code: "1", SKU: "TORTA-CHOC", Label: "Torta chocolate húmedo + crema choc.", Variants: []catalogo.Variant{
				{Code: "10", Label: "10 porciones", Price: 2100},
				{Code: "12", Label: "12 porciones", Price: 2400},
				{Code: "25", Label: "25 porciones", Price: 3900},
			}},
		}},
		{Code: "2", Label: "Congelados", Items: []catalogo.Article{
			{Code: "1", SKU: "TEQ-30", Label: "Tequeños congelados", Price: 490, Tags: []string{"congelados"}},
		}},
		{Code: "3", Label: "Sandwiches", Items: []catalogo.Article{
			{Code: "1", SKU: "HAMB", Label: "Hamburguesa", Price: 3000},
		}},
		{Code: "4", Label: "Extras", Items: []catalogo.Article{
			{Code: "1", SKU: "DECO-INF", Label: "Decoración infantil", Price: 800},
			{Code: "2", SKU: "QUESO", Label: "Queso", Price: 150},
			{Code: "3", SKU: "SAL", Label: "Sal", Price: 50},
			{Code: "4", SKU: "SALSA", Label: "Salsa", Price: 60},
		}},
	}}
}

// oneCategory arma una carta de una sola categoría con los artículos dados, en orden.
func oneCategory(items ...catalogo.Article) catalogo.Catalog {
	return catalogo.Catalog{Categories: []catalogo.Category{{Code: "1", Label: "Carta", Items: items}}}
}

// withoutArticle devuelve el mismo catálogo sin el artículo del sku dado: el MISMO
// texto del cliente contra dos cartas que solo se diferencian en si «queso» existe.
func withoutArticle(cat catalogo.Catalog, sku string) catalogo.Catalog {
	out := catalogo.Catalog{}
	for _, c := range cat.Categories {
		kept := catalogo.Category{Code: c.Code, Label: c.Label}
		for _, a := range c.Items {
			if a.SKU != sku {
				kept.Items = append(kept.Items, a)
			}
		}
		out.Categories = append(out.Categories, kept)
	}
	return out
}

// indexOf construye el índice con el normalizador DE PRODUCCIÓN (`textmatch.Normalize`):
// el match y el índice tienen que opinar lo mismo sobre la ñ (R-05), y probarlo con dos
// normalizadores distintos no demostraría nada del sistema.
func indexOf(t *testing.T, cat catalogo.Catalog) *indice.Indice {
	t.Helper()
	idx, err := indice.Construir(cat, textmatch.Normalize)
	if err != nil {
		t.Fatalf("construir el índice del fixture: %v", err)
	}
	return idx
}

// ---------------------------------------------------------------------------
// LOS DOS DOBLES QUE MIDEN
// ---------------------------------------------------------------------------

// grayZoneName es el `Name()` del doble: lo que tiene que aparecer como `strategy`.
const grayZoneName = "zona_gris_falsa"

// fakeGrayZone es el escalón caro. Guarda CADA consulta —el esperado y los candidatos
// que se le ofrecieron—: un contador que solo cuenta no distingue haber preguntado por
// el ítem correcto de haber preguntado por una personalización.
type fakeGrayZone struct {
	// answers mapea el texto del esperado al índice que debe devolver. Lo que no esté
	// se contesta con «ninguno corresponde» (-1).
	answers map[string]int
	// err, si no es nil, hace fallar TODAS las llamadas.
	err error

	asked   []string
	offered [][]string
}

func (z *fakeGrayZone) Name() string { return grayZoneName }

func (z *fakeGrayZone) Resolve(_ context.Context, expected string, candidates []string) (textmatch.GrayZoneDecision, error) {
	z.asked = append(z.asked, expected)
	z.offered = append(z.offered, append([]string(nil), candidates...))
	if z.err != nil {
		return textmatch.GrayZoneDecision{}, z.err
	}
	idx, ok := z.answers[expected]
	if !ok {
		return textmatch.GrayZoneDecision{Index: -1, Evidence: "ninguno corresponde"}, nil
	}
	return textmatch.GrayZoneDecision{Index: idx, Confidence: 0.91, Evidence: "el doble lo decidió"}, nil
}

// spyComparator envuelve un comparador REAL y anota qué esperados pasan por el barrido.
//
// 🔴 ENVUELVE, NO SUSTITUYE. Un doble que contestara por su cuenta mediría al doble: lo
// que se comprueba es que un texto NUNCA LLEGA al comparador de producción.
type spyComparator struct {
	real     textmatch.Comparator
	expected []string
	calls    int
	failWith error
}

func (c *spyComparator) Compare(ctx context.Context, expected, candidate string) (textmatch.Result, error) {
	c.calls++
	if len(c.expected) == 0 || c.expected[len(c.expected)-1] != expected {
		c.expected = append(c.expected, expected)
	}
	if c.failWith != nil {
		return textmatch.Result{}, c.failWith
	}
	return c.real.Compare(ctx, expected, candidate)
}

// sawAnyContaining responde si alguno de los textos que llegaron al comparador contiene
// el fragmento. Por CONTENIDO y no por igualdad: lo que no puede pasar es que la
// personalización llegue de ninguna forma.
func (c *spyComparator) sawAnyContaining(fragment string) bool {
	for _, e := range c.expected {
		if strings.Contains(e, fragment) {
			return true
		}
	}
	return false
}

// newSpy espía la cascada de producción.
func newSpy() *spyComparator { return &spyComparator{real: stages.DefaultCascade()} }

// errGrayZone es el fallo del escalón caro (timeout, Edge sin capacidad…).
var errGrayZone = errors.New("el modelo no contestó")

// ---------------------------------------------------------------------------
// ATREZO
// ---------------------------------------------------------------------------

// matchBench son la etapa y lo que deja ver: lo persistido y el log.
type matchBench struct {
	stage *stages.Match
	store *fakeStore
	log   bytes.Buffer
}

// newMatchBench construye la etapa con el store y el log de los dobles comunes.
func newMatchBench(t *testing.T, opts ...stages.MatchOption) *matchBench {
	t.Helper()
	b := &matchBench{store: &fakeStore{}}
	stage, err := stages.NewMatch(captureLog(&b.log), b.store, opts...)
	if err != nil {
		t.Fatalf("NewMatch: %v", err)
	}
	b.stage = stage
	return b
}

// run corre la etapa sobre el job del caso y exige que no falle.
func (b *matchBench) run(t *testing.T, in stages.MatchInput) *stages.MatchArtifact {
	t.Helper()
	art, err := b.stage.Run(context.Background(), ambarJob(), in)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if art == nil {
		t.Fatal("Run devolvió artefacto nil sin error")
	}
	return art
}

// runItems es el caso común: unos ítems de P4 contra un catálogo, sin zonas ni nota.
func (b *matchBench) runItems(t *testing.T, cat catalogo.Catalog, items ...llm.NormalizedItem) *stages.MatchArtifact {
	t.Helper()
	return b.run(t, stages.MatchInput{Quantities: p4Of(items...), Index: indexOf(t, cat)})
}

// p4Of arma el artefacto de P4 con los ítems dados.
func p4Of(items ...llm.NormalizedItem) *llm.Quantities {
	return &llm.Quantities{Version: llm.ArtifactVersion, DeliveryDate: "2026-07-22", Items: items}
}

// lineWithSKU busca la línea de un sku. Devuelve nil si no está, para poder afirmar
// tanto la presencia como la AUSENCIA (ninguna línea de «Sal»).
func lineWithSKU(art *stages.MatchArtifact, sku string) *stages.Line {
	for i := range art.Lines {
		if art.Lines[i].SKU == sku {
			return &art.Lines[i]
		}
	}
	return nil
}

// assertLineCount fija cuántas líneas trae el artefacto, envío incluido.
func assertLineCount(t *testing.T, art *stages.MatchArtifact, want int, why string) {
	t.Helper()
	if len(art.Lines) != want {
		t.Fatalf("líneas = %d, se esperaban %d (%s): %+v", len(art.Lines), want, why, art.Lines)
	}
}

// assertPrice exige que la línea traiga precio y que sea ése: un test que espera precio
// no puede pasar por un nil de casualidad.
func assertPrice(t *testing.T, l stages.Line, want float64) {
	t.Helper()
	if l.UnitPrice == nil {
		t.Fatalf("la línea %q (%s) tenía que traer precio %v y viene vacía", l.Label, l.SKU, want)
	}
	if *l.UnitPrice != want {
		t.Fatalf("precio de %q (%s) = %v, se esperaba %v", l.Label, l.SKU, *l.UnitPrice, want)
	}
}

// assertMatched exige una línea `matched` con ese sku y esa estrategia.
func assertMatched(t *testing.T, l stages.Line, sku, strategy string) {
	t.Helper()
	if l.Kind != stages.KindMatched || l.SKU != sku {
		t.Fatalf("línea = kind %q, sku %q; se esperaba matched con %q", l.Kind, l.SKU, sku)
	}
	if l.Match == nil || l.Match.Strategy != strategy {
		t.Fatalf("procedencia de %q = %+v; se esperaba la estrategia %q", sku, l.Match, strategy)
	}
}

// assertUnmatched exige el renglón que el dueño precifica a mano: sin sku, sin precio,
// sin opciones y sin procedencia, con lo que dijo el cliente como etiqueta.
func assertUnmatched(t *testing.T, l stages.Line, label string) {
	t.Helper()
	if l.Kind != stages.KindUnmatched {
		t.Fatalf("kind = %q (sku %q), se esperaba unmatched", l.Kind, l.SKU)
	}
	if l.SKU != "" || l.UnitPrice != nil || len(l.VariantOptions) != 0 || l.Match != nil {
		t.Fatalf("un unmatched no copia NADA del catálogo: %+v", l)
	}
	if l.Label != label {
		t.Fatalf("etiqueta del unmatched = %q, se esperaba lo que dijo el cliente, %q", l.Label, label)
	}
}

// assertWarnings compara los avisos con los esperados, en orden. `nil` y vacío son lo
// mismo aquí: lo que importa es que no haya ninguno.
func assertWarnings(t *testing.T, art *stages.MatchArtifact, want ...stages.Warning) {
	t.Helper()
	if len(art.Warnings) != len(want) {
		t.Fatalf("avisos = %+v, se esperaban %+v", art.Warnings, want)
	}
	for i := range want {
		if art.Warnings[i] != want[i] {
			t.Fatalf("aviso %d = %+v, se esperaba %+v", i, art.Warnings[i], want[i])
		}
	}
}

// assertTotal fija el total parcial y las líneas pendientes contra LITERALES.
func assertTotal(t *testing.T, art *stages.MatchArtifact, total float64, pending int, why string) {
	t.Helper()
	gotTotal, gotPending := art.PartialTotal()
	if gotTotal != total || gotPending != pending {
		t.Fatalf("total parcial = %v con %d pendientes; se esperaba %v con %d (%s)", gotTotal, gotPending, total, pending, why)
	}
}

// offeredSKUs son los sku de las `variant_options` de una línea, en su orden; nil si no
// ofrece ninguna.
func offeredSKUs(l stages.Line) []string {
	if len(l.VariantOptions) == 0 {
		return nil
	}
	out := make([]string, 0, len(l.VariantOptions))
	for _, o := range l.VariantOptions {
		out = append(out, o.SKU)
	}
	return out
}
