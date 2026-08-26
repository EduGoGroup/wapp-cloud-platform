package stages_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/logger"
	"github.com/EduGoGroup/wapp-shared/textmatch"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/cart"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake/catalogo"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake/stages"
)

// match_dobles_test.go — el CATÁLOGO de los tests de T3.2 y los dos dobles que
// hacen medibles sus criterios: el que cuenta las llamadas al escalón caro y el que
// espía QUÉ textos llegan al comparador determinista.

// ---------------------------------------------------------------------------
// EL CATÁLOGO DEL FIXTURE
// ---------------------------------------------------------------------------

// catalogoAmbar es la carta con la que se replaya el caso Ambar, y cada artículo
// está puesto para ejercitar UN escalón distinto de la cascada:
//
//   - TEQ-30 «Tequeños congelados» ⇒ el ítem lo dice IGUAL: escalón exacto.
//     También es el que se busca escrito sin ñ («tequenos»), que son 19 runas y una
//     edición: 0,947, el escalón FUZZY.
//   - TORTA-CHOC «Torta chocolate húmedo + crema choc.» ⇒ el cliente dice «torta de
//     chocolate»: ni igual ni parecido en distancia de edición (18 runas contra 36).
//     Es el caso de la ZONA GRIS, y el que tiene variantes.
//   - HAMB «Hamburguesa» ⇒ el de las personalizaciones y los añadidos.
//   - SAL «Sal» y SALSA «Salsa» ⇒ 🔴 LA TRAMPA del criterio (a): están para que
//     «sin sal» tenga contra qué casar por error. Que no case es lo que se mide.
//   - QUESO «Queso» ⇒ el añadido que SÍ es artículo (criterio (b)).
//   - DECO-INF «Decoración infantil» ⇒ el añadido facturable del caso Ambar.
func catalogoAmbar() cart.Catalog {
	return cart.Catalog{Categories: []cart.Category{
		{Code: "1", Label: "Tortas", Items: []cart.Article{
			{Code: "1", SKU: "TORTA-CHOC", Label: "Torta chocolate húmedo + crema choc.", Variants: []cart.Variant{
				{Code: "10", Label: "10 porciones", Price: 2100},
				{Code: "12", Label: "12 porciones", Price: 2400},
				{Code: "25", Label: "25 porciones", Price: 3900},
			}},
		}},
		{Code: "2", Label: "Congelados", Items: []cart.Article{
			{Code: "1", SKU: "TEQ-30", Label: "Tequeños congelados", Price: 490, Tags: []string{"congelados"}},
		}},
		{Code: "3", Label: "Sandwiches", Items: []cart.Article{
			{Code: "1", SKU: "HAMB", Label: "Hamburguesa", Price: 3000},
		}},
		{Code: "4", Label: "Extras", Items: []cart.Article{
			{Code: "1", SKU: "DECO-INF", Label: "Decoración infantil", Price: 800},
			{Code: "2", SKU: "QUESO", Label: "Queso", Price: 150},
			{Code: "3", SKU: "SAL", Label: "Sal", Price: 50},
			{Code: "4", SKU: "SALSA", Label: "Salsa", Price: 60},
		}},
	}}
}

// sinArticulo devuelve el mismo catálogo sin el artículo del sku dado. Es lo que
// hace comparables los criterios (b) y (c): el MISMO texto del cliente contra dos
// cartas que solo se diferencian en si «queso» existe.
func sinArticulo(cat cart.Catalog, sku string) cart.Catalog {
	out := cart.Catalog{}
	for _, c := range cat.Categories {
		nueva := cart.Category{Code: c.Code, Label: c.Label}
		for _, a := range c.Items {
			if a.SKU != sku {
				nueva.Items = append(nueva.Items, a)
			}
		}
		out.Categories = append(out.Categories, nueva)
	}
	return out
}

// indiceDe construye el índice con el normalizador DE PRODUCCIÓN
// (`textmatch.Normalize`), no con un doble: el match y el índice tienen que opinar
// lo mismo sobre la ñ, y probarlo con dos normalizadores distintos no demostraría
// nada del sistema.
func indiceDe(t *testing.T, cat cart.Catalog) *catalogo.Indice {
	t.Helper()
	idx, err := catalogo.Construir(cat, textmatch.Normalize)
	require.NoError(t, err)
	return idx
}

// ---------------------------------------------------------------------------
// LOS DOS DOBLES QUE MIDEN
// ---------------------------------------------------------------------------

// zonaGrisFalsa es el escalón caro. Guarda CADA consulta —el esperado y los
// candidatos que se le ofrecieron— porque el criterio no es solo «cuántas veces se
// llamó»: un contador que solo cuenta no distingue haber preguntado por el ítem
// correcto de haber preguntado por una personalización.
type zonaGrisFalsa struct {
	// respuestas mapea el texto del esperado al índice que debe devolver. Lo que no
	// esté en el mapa se contesta con «ninguno corresponde» (-1).
	respuestas map[string]int
	// err, si no es nil, hace fallar TODAS las llamadas.
	err error

	pedidos    []string
	candidatos [][]string
}

func (z *zonaGrisFalsa) Name() string { return "zona_gris_falsa" }

func (z *zonaGrisFalsa) Resolve(_ context.Context, expected string, candidates []string) (textmatch.GrayZoneDecision, error) {
	z.pedidos = append(z.pedidos, expected)
	z.candidatos = append(z.candidatos, append([]string(nil), candidates...))
	if z.err != nil {
		return textmatch.GrayZoneDecision{}, z.err
	}
	idx, ok := z.respuestas[expected]
	if !ok {
		return textmatch.GrayZoneDecision{Index: -1, Evidence: "ninguno corresponde"}, nil
	}
	return textmatch.GrayZoneDecision{Index: idx, Confidence: 0.91, Evidence: "el doble lo decidió"}, nil
}

// comparadorEspia envuelve la cascada REAL y anota qué esperados pasan por el bucle.
//
// 🔴 ENVUELVE, NO SUSTITUYE. Un doble que contestara por su cuenta mediría al doble:
// lo que aquí se comprueba es que un texto —«sin sal»— NUNCA LLEGA al comparador de
// producción, así que el comparador tiene que ser el de producción.
type comparadorEspia struct {
	real       textmatch.Comparator
	esperados  []string
	llamadas   int
	fallarCon  error
	fallarDesd int
}

func (c *comparadorEspia) Compare(ctx context.Context, expected, candidate string) (textmatch.Result, error) {
	c.llamadas++
	if len(c.esperados) == 0 || c.esperados[len(c.esperados)-1] != expected {
		c.esperados = append(c.esperados, expected)
	}
	if c.fallarCon != nil && c.llamadas >= c.fallarDesd {
		return textmatch.Result{}, c.fallarCon
	}
	return c.real.Compare(ctx, expected, candidate)
}

// vioAlgunoQueContenga responde si alguno de los textos que llegaron al comparador
// contiene el fragmento. Se pregunta por CONTENIDO y no por igualdad porque lo que
// no puede pasar es que la personalización llegue de ninguna forma.
func (c *comparadorEspia) vioAlgunoQueContenga(frag string) bool {
	for _, e := range c.esperados {
		if strings.Contains(e, frag) {
			return true
		}
	}
	return false
}

// errZonaGris es el fallo del escalón caro (timeout, Edge sin capacidad…).
var errZonaGris = errors.New("el modelo no contestó")

// ---------------------------------------------------------------------------
// ATREZO
// ---------------------------------------------------------------------------

// matchDe construye la etapa con un log que no ensucia la salida del test.
func matchDe(t *testing.T, opts ...stages.OpciónMatch) (*stages.Match, *storeFake) {
	t.Helper()
	store := &storeFake{}
	m, err := stages.NewMatch(logger.New(logger.WithWriter(&bytes.Buffer{})), store, opts...)
	require.NoError(t, err)
	return m, store
}

// p4De arma el artefacto de P4 con los ítems dados.
func p4De(items ...llm.NormalizedItem) *llm.Quantities {
	return &llm.Quantities{Version: llm.ArtifactVersion, DeliveryDate: "2026-07-22", Items: items}
}

// lineaConSKU busca la línea de un sku. Devuelve nil si no está, para que el test
// pueda afirmar tanto la presencia como la AUSENCIA (que es la mitad del criterio
// (a): ninguna línea de «Sal»).
func lineaConSKU(art *stages.ArtefactoMatch, sku string) *stages.Linea {
	for i := range art.Lines {
		if art.Lines[i].SKU == sku {
			return &art.Lines[i]
		}
	}
	return nil
}

// precio desempaqueta el precio de una línea, fallando si estaba vacío. Existe para
// que un test que espera precio no pase por un nil de casualidad.
func precio(t *testing.T, l stages.Linea) float64 {
	t.Helper()
	require.NotNilf(t, l.UnitPrice, "la línea %q tenía que traer precio y viene vacía", l.Label)
	return *l.UnitPrice
}
