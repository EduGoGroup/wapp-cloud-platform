package indice_test

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/EduGoGroup/wapp-shared/textmatch"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo/indice"
)

// helpers_test.go — el ORÁCULO y los corpus compartidos por los tests del índice.
// Los normalizadores que NO cumplen el contrato viven en normalizador_test.go, y
// las fuentes falsas de la caché en cache_test.go, cada uno con quien lo usa.

// ---------------------------------------------------------------------------
// EL ORÁCULO: LA BÚSQUEDA LINEAL INGENUA
// ---------------------------------------------------------------------------
//
// Escrito como lo escribiría cualquiera que no tuviera índice: recorrer el catálogo
// entero y comparar. NO consulta ninguna estructura del índice, y por eso sirve de
// oráculo: si los dos coinciden en todas las consultas, el índice no ha inventado
// ni ha perdido nada. Normaliza con `textmatch.Normalize`, el de producción (T-5).

func match(c catalogo.Category, a catalogo.Article) indice.Coincidencia {
	return indice.Coincidencia{Categoria: c.Code, CategoriaLabel: c.Label, Articulo: a}
}

func linearBySKU(cat catalogo.Catalog, sku string) (indice.Coincidencia, bool) {
	for _, c := range cat.Categories {
		for _, a := range c.Items {
			if a.SKU == sku {
				return match(c, a), true
			}
		}
	}
	return indice.Coincidencia{}, false
}

func linearByLabel(cat catalogo.Catalog, text string) []indice.Coincidencia {
	q := textmatch.Normalize(text)
	var out []indice.Coincidencia
	for _, c := range cat.Categories {
		for _, a := range c.Items {
			if textmatch.Normalize(a.Label) == q {
				out = append(out, match(c, a))
			}
		}
	}
	return out
}

func linearByTag(cat catalogo.Catalog, text string) []indice.Coincidencia {
	q := textmatch.Normalize(text)
	var out []indice.Coincidencia
	for _, c := range cat.Categories {
		for _, a := range c.Items {
			for _, tag := range a.Tags {
				if textmatch.Normalize(tag) == q {
					// Un artículo con dos tags que normalizan igual sale UNA vez: el
					// resultado es el artículo, no el tag.
					out = append(out, match(c, a))
					break
				}
			}
		}
	}
	return out
}

func linearByVariant(cat catalogo.Catalog, text string) []indice.Coincidencia {
	q := textmatch.Normalize(text)
	var out []indice.Coincidencia
	for _, c := range cat.Categories {
		for _, a := range c.Items {
			for _, v := range a.Variants {
				if textmatch.Normalize(v.Label) == q {
					// Aquí NO hay break: el resultado es el par (artículo, variante) y
					// dos variantes que normalizan igual son dos resultados.
					m := match(c, a)
					m.Variante = v
					m.HayVariante = true
					out = append(out, m)
				}
			}
		}
	}
	return out
}

// linearWalk es el catálogo aplanado en orden de documento: lo que Etiqueta y En
// tienen que devolver posición a posición.
func linearWalk(cat catalogo.Catalog) []indice.Coincidencia {
	var out []indice.Coincidencia
	for _, c := range cat.Categories {
		for _, a := range c.Items {
			out = append(out, match(c, a))
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// EL CATÁLOGO TRAMPOSO
// ---------------------------------------------------------------------------

// trickyCatalog es el fixture del test diferencial: todo lo que puede hacer
// divergir un índice de una búsqueda lineal está aquí dentro. Las dos primeras
// categorías son el corpus del test viejo:
//
//	· tres labels que normalizan IGUAL ("Café", "cafe", "  CAFÉ  ")
//	· «Piña» vs «Pina»: dos artículos que un plegado ingenuo colapsaría
//	· un sku REPETIDO en dos categorías (gana el primero en orden de documento)
//	· un artículo con dos tags que normalizan igual (sale una vez, no dos)
//	· un tag compartido por varios artículos (salen todos, en orden)
//	· dos variantes del mismo artículo con el mismo label normalizado (salen las dos)
//	· un artículo con label VACÍO
//
// La tercera es el corpus ADVERSARIO (reglas.md §5):
//
//	· espacios Unicode en el sku (NBSP al final, espacio delante): el sku es opaco
//	· espacios Unicode en etiquetas y tags (NBSP, EM SPACE, ideográfico), que el
//	  normalizador colapsa, frente al de ancho cero (U+200B), que NO es un espacio
//	· separadores repetidos ("a@@b", "sin;;gluten", "sin||tacc") frente al simple
//	· dígitos no ASCII (arábigo-índicos y de ancho completo), que no se pliegan
//	· sku vacío y label de solo espacios
func trickyCatalog() catalogo.Catalog {
	return catalogo.Catalog{Categories: []catalogo.Category{
		{Code: "1", Label: "Bebidas", Items: []catalogo.Article{
			{Code: "1", SKU: "CAFE", Label: "Café", Price: 2.5, Tags: []string{"Caliente", "CALIENTE", "clásico"}},
			{Code: "2", SKU: "CAFE-2", Label: "cafe", Price: 2.6, Tags: []string{"caliente"}},
			{Code: "3", SKU: "TE", Label: "  CAFÉ  ", Price: 2.0},
			{Code: "4", SKU: "PINA", Label: "Piña colada", Price: 5.0, Tags: []string{"frío"}},
			{Code: "5", SKU: "PINA-SIN", Label: "Pina colada", Price: 4.0},
		}},
		{Code: "2", Label: "Postres", Items: []catalogo.Article{
			{Code: "1", SKU: "CAFE", Label: "Café en grano", Price: 9.0, Tags: []string{"clásico"}},
			{Code: "2", SKU: "TORTA", Label: "Torta de chocolate", Price: 20, Variants: []catalogo.Variant{
				{Code: "V1", Label: "Grande", Price: 25},
				{Code: "V2", Label: "grande", Price: 26},
				{Code: "V3", Label: "12 porciones", Price: 30},
			}},
			{Code: "3", SKU: "SINLABEL", Label: "", Price: 1},
			{Code: "4", SKU: "ANO", Label: "Torta de año nuevo", Price: 40, Tags: []string{"año"}},
		}},
		{Code: "3", Label: "Adversarios", Items: []catalogo.Article{
			{Code: "1", SKU: "CAFE\u00a0", Label: "Té\u00a0verde", Price: 3, Tags: []string{"sin\u2003gluten", "sin gluten"}},
			{Code: "2", SKU: " TORTA", Label: "Té\u200bverde", Price: 3.1},
			{Code: "3", SKU: "A@@B", Label: "a@@b", Price: 3.2, Tags: []string{"sin;;gluten", "sin||tacc"}},
			{Code: "4", SKU: "A@B", Label: "a@b", Price: 3.3, Tags: []string{"sin;gluten"}},
			{Code: "5", SKU: "PACK", Label: "Pack ١٢", Price: 3.4, Variants: []catalogo.Variant{
				{Code: "V1", Label: "１２ porciones", Price: 31},
				{Code: "V2", Label: "12\u3000porciones", Price: 32},
			}},
			{Code: "6", SKU: "", Label: " \u00a0 ", Price: 3.5},
		}},
	}}
}

// queries es el corpus con el que se interroga a los dos —índice y oráculo—. Va
// deliberadamente más allá de lo que hay en el catálogo: fallos, cadena vacía, el
// par «año»/«ano» que separa un normalizador correcto de uno que pliega la ñ, y
// las formas adversarias de cada clave.
func queries() []string {
	return []string{
		"Café", "cafe", "CAFÉ", "  café  ", "café en grano",
		"Piña colada", "pina colada", "PIÑA COLADA",
		"Torta de chocolate", "torta   de   chocolate",
		"Grande", "grande", "GRANDE", "12 porciones",
		"Caliente", "caliente", "clásico", "clasico", "frío", "frio",
		"año", "ano", "Torta de año nuevo",
		"", " ", "no existe", "cafeteria", "caf",
		// Adversarias.
		"té verde", "Té\u00a0verde", "té\u200bverde", "te\u2003verde",
		"sin gluten", "sin\u2003gluten", "sin;;gluten", "sin;gluten", "sin||tacc", "sin|tacc",
		"a@@b", "a@b", "A@@B", "a@@@b",
		"pack 12", "pack ١٢", "１２ porciones", "12\u3000porciones", "١٢ porciones",
		"\u00a0", "\u200b", "\u3000",
	}
}

func skuQueries() []string {
	return []string{
		"CAFE", "CAFE-2", "TE", "PINA", "PINA-SIN", "TORTA", "SINLABEL", "ANO", "", "cafe", "NO-EXISTE",
		// Adversarias: el sku no se normaliza, así que cada una es una clave distinta.
		"CAFE\u00a0", "CAFE ", " TORTA", "TORTA ", "A@@B", "A@B", "a@@b", "ＣＡＦＥ", "PACK", " ", "\u00a0",
	}
}

// trickyIndex construye el índice del catálogo tramposo, con el normalizador de
// producción, o falla el test.
func trickyIndex(t *testing.T) *indice.Indice {
	t.Helper()
	idx, err := indice.Construir(trickyCatalog(), textmatch.Normalize)
	if err != nil {
		t.Fatalf("Construir(trickyCatalog) = error %v; se esperaba un índice", err)
	}
	return idx
}

// skus resume una lista de coincidencias por el sku de sus artículos, en orden.
func skus(ms []indice.Coincidencia) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Articulo.SKU)
	}
	return out
}

// ---------------------------------------------------------------------------
// FIXTURES DE TALLA
// ---------------------------------------------------------------------------

// sizedCatalog genera un catálogo de n artículos repartidos en 20 categorías, cada
// uno con sku propio, tres tags y dos variantes. No hay ningún fixture grande en el
// repo, así que el catálogo de la cota y el del rendimiento se generan aquí.
func sizedCatalog(n int) catalogo.Catalog {
	const categories = 20
	cat := catalogo.Catalog{Categories: make([]catalogo.Category, categories)}
	for c := range categories {
		cat.Categories[c] = catalogo.Category{Code: strconv.Itoa(c + 1), Label: "Categoría " + strconv.Itoa(c+1)}
	}
	for i := range n {
		c := i % categories
		s := strconv.Itoa(i)
		cat.Categories[c].Items = append(cat.Categories[c].Items, catalogo.Article{
			Code:  s,
			SKU:   "SKU-" + s,
			Label: "Artículo número " + s + " de piña y café",
			Price: float64(i) + 0.5,
			Tags:  []string{"tag-" + strconv.Itoa(i%37), "clásico", "año-" + strconv.Itoa(i%11)},
			Variants: []catalogo.Variant{
				{Code: "V1", Label: "Presentación grande " + s, Price: float64(i) + 1},
				{Code: "V2", Label: "Presentación pequeña " + s, Price: float64(i)},
			},
		})
	}
	return cat
}

// sizedDocument serializa sizedCatalog a la forma JSON que guarda
// `public.tenant_content` (las claves en minúscula del blob del carrito). Es lo que
// consume el control del test de rendimiento.
func sizedDocument(t *testing.T, n int) []byte {
	t.Helper()
	type variantJSON struct {
		Code  string  `json:"code"`
		Label string  `json:"label"`
		Price float64 `json:"price"`
	}
	type articleJSON struct {
		Code     string        `json:"code"`
		SKU      string        `json:"sku"`
		Label    string        `json:"label"`
		Price    float64       `json:"price"`
		Tags     []string      `json:"tags"`
		Variants []variantJSON `json:"variants"`
	}
	type categoryJSON struct {
		Code  string        `json:"code"`
		Label string        `json:"label"`
		Items []articleJSON `json:"items"`
	}

	cat := sizedCatalog(n)
	doc := struct {
		Categories []categoryJSON `json:"categories"`
	}{Categories: make([]categoryJSON, 0, len(cat.Categories))}

	for _, c := range cat.Categories {
		cj := categoryJSON{Code: c.Code, Label: c.Label, Items: make([]articleJSON, 0, len(c.Items))}
		for _, a := range c.Items {
			aj := articleJSON{Code: a.Code, SKU: a.SKU, Label: a.Label, Price: a.Price, Tags: a.Tags}
			for _, v := range a.Variants {
				aj.Variants = append(aj.Variants, variantJSON{Code: v.Code, Label: v.Label, Price: v.Price})
			}
			cj.Items = append(cj.Items, aj)
		}
		doc.Categories = append(doc.Categories, cj)
	}

	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("serializando el documento de talla %d: %v", n, err)
	}
	return raw
}

// countArticles cuenta los artículos de un catálogo, sobre todas sus categorías.
func countArticles(cat catalogo.Catalog) int {
	n := 0
	for _, c := range cat.Categories {
		n += len(c.Items)
	}
	return n
}
