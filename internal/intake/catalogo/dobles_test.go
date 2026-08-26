package catalogo_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/cart"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake/catalogo"
)

// dobles_test.go — los dobles y el ORÁCULO compartidos por los tests de T3.7.

// ---------------------------------------------------------------------------
// EL NORMALIZADOR DE TEST
// ---------------------------------------------------------------------------

// pliegueDoble pliega los pocos diacríticos que aparecen en los fixtures. La «ñ»
// NO está, que es justo el punto.
var pliegueDoble = strings.NewReplacer(
	"á", "a", "é", "e", "í", "i", "ó", "o", "ú", "u", "ü", "u", "à", "a", "ç", "c",
)

// normalizadorDoble es un DOBLE DE TEST, no una copia del normalizador de
// producción. El de producción es `wapp-shared/textmatch.Normalize` y todavía no se
// puede importar (ver el bloque de normalizador.go); esto es lo mínimo que cumple
// `catalogo.VerificarNormalizador` para poder ejercitar el índice: minúsculas,
// recomposición de la ñ descompuesta, plegado de LOS DIACRÍTICOS DE LOS FIXTURES —
// no de la tabla latina entera— y colapso de espacios.
//
// Que sea un doble y no una copia se nota en que no serviría en producción: no
// conoce «ł», ni «ø», ni las marcas combinantes sueltas. Lo que sí hace es cumplir
// el contrato, y por eso el índice lo acepta.
func normalizadorDoble(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "n\u0303", "\u00f1") // la ñ descompuesta, ANTES de plegar nada
	s = pliegueDoble.Replace(s)
	return strings.Join(strings.Fields(s), " ")
}

// normalizadorTramposo pasa por minúsculas y pliega la ñ como si fuera una n con
// tilde. Es exactamente el error que `VerificarNormalizador` existe para cazar.
func normalizadorTramposo(s string) string {
	return strings.ReplaceAll(normalizadorDoble(s), "ñ", "n")
}

// ---------------------------------------------------------------------------
// LA FUENTE FALSA — LA QUE CUENTA LAS LECTURAS (criterio (a))
// ---------------------------------------------------------------------------

// fuenteFalsa sirve un documento en memoria y CUENTA cuántas veces se lo piden. Es
// el contador con el que se demuestra que las búsquedas por ítem no leen.
type fuenteFalsa struct {
	mu       sync.Mutex
	docs     map[string][]byte
	lecturas int
	err      error
}

func nuevaFuenteFalsa() *fuenteFalsa {
	return &fuenteFalsa{docs: map[string][]byte{}}
}

// publicar simula una escritura del documento del tenant. Es la MISMA operación
// para los dos caminos del criterio (b) —el import y el `PUT` a mano— y eso no es
// una simplificación del test: es lo que la caché ve. Ninguno de los dos deja marca
// de versión en `tenant_content` (no hay columna donde dejarla), así que desde aquí
// abajo son indistinguibles por construcción.
func (f *fuenteFalsa) publicar(tenantID, doc string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.docs[tenantID] = []byte(doc)
}

func (f *fuenteFalsa) LeerCatalogo(_ context.Context, tenantID string) (catalogo.Documento, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lecturas++
	if f.err != nil {
		return catalogo.Documento{}, f.err
	}
	doc, ok := f.docs[tenantID]
	if !ok {
		return catalogo.Documento{}, errors.New("fuenteFalsa: sin documento para " + tenantID)
	}
	return catalogo.Documento{Raw: doc}, nil
}

func (f *fuenteFalsa) leidas() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lecturas
}

// indiceSatisfaceFuente dice si `*catalogo.Indice` implementa `catalogo.Fuente`.
// Tiene que ser FALSE: el objeto que se consulta una vez por ítem no debe tener
// forma de volver a Postgres. Lo comprueba TestFrontera_ElIndiceNoPuedeLeer.
var _, indiceSatisfaceFuente = any((*catalogo.Indice)(nil)).(catalogo.Fuente)

// ---------------------------------------------------------------------------
// EL ORÁCULO: LA BÚSQUEDA LINEAL INGENUA (criterio (c))
// ---------------------------------------------------------------------------
//
// Escrito como lo escribiría cualquiera que no tuviera índice: recorrer el catálogo
// entero y comparar. NO consulta ninguna estructura del índice, y por eso sirve de
// oráculo: si los dos coinciden en todas las consultas, el índice no ha inventado
// ni ha perdido nada.

func oraculoSKU(cat cart.Catalog, sku string) (catalogo.Coincidencia, bool) {
	for _, c := range cat.Categories {
		for _, a := range c.Items {
			if a.SKU == sku {
				return coincidencia(c, a), true
			}
		}
	}
	return catalogo.Coincidencia{}, false
}

func oraculoEtiqueta(cat cart.Catalog, texto string) []catalogo.Coincidencia {
	q := normalizadorDoble(texto)
	var out []catalogo.Coincidencia
	for _, c := range cat.Categories {
		for _, a := range c.Items {
			if normalizadorDoble(a.Label) == q {
				out = append(out, coincidencia(c, a))
			}
		}
	}
	return out
}

func oraculoTag(cat cart.Catalog, texto string) []catalogo.Coincidencia {
	q := normalizadorDoble(texto)
	var out []catalogo.Coincidencia
	for _, c := range cat.Categories {
		for _, a := range c.Items {
			for _, t := range a.Tags {
				if normalizadorDoble(t) == q {
					// Un artículo con dos tags que normalizan igual sale UNA vez: el
					// resultado es el artículo, no el tag.
					out = append(out, coincidencia(c, a))
					break
				}
			}
		}
	}
	return out
}

func oraculoVariante(cat cart.Catalog, texto string) []catalogo.Coincidencia {
	q := normalizadorDoble(texto)
	var out []catalogo.Coincidencia
	for _, c := range cat.Categories {
		for _, a := range c.Items {
			for _, v := range a.Variants {
				if normalizadorDoble(v.Label) == q {
					// Aquí NO hay break: el resultado es el par (artículo, variante) y
					// dos variantes que normalizan igual son dos resultados.
					co := coincidencia(c, a)
					co.Variante = v
					co.HayVariante = true
					out = append(out, co)
				}
			}
		}
	}
	return out
}

func coincidencia(c cart.Category, a cart.Article) catalogo.Coincidencia {
	return catalogo.Coincidencia{Categoria: c.Code, CategoriaLabel: c.Label, Articulo: a}
}

// ---------------------------------------------------------------------------
// EL CATÁLOGO TRAMPOSO
// ---------------------------------------------------------------------------

// catalogoTramposo es el fixture del test diferencial: todo lo que puede hacer
// divergir un índice de una búsqueda lineal está aquí dentro.
//
//	· tres labels que normalizan IGUAL ("Café", "cafe", "  CAFÉ  ")
//	· «Piña» vs «Pina»: dos artículos que un plegado ingenuo colapsaría
//	· un sku REPETIDO en dos categorías (gana el primero en orden de documento)
//	· un artículo con dos tags que normalizan igual (sale una vez, no dos)
//	· un tag compartido por varios artículos (salen todos, en orden)
//	· dos variantes del mismo artículo con el mismo label normalizado (salen las dos)
//	· un artículo con label VACÍO
func catalogoTramposo() cart.Catalog {
	return cart.Catalog{Categories: []cart.Category{
		{Code: "1", Label: "Bebidas", Items: []cart.Article{
			{Code: "1", SKU: "CAFE", Label: "Café", Price: 2.5, Tags: []string{"Caliente", "CALIENTE", "clásico"}},
			{Code: "2", SKU: "CAFE-2", Label: "cafe", Price: 2.6, Tags: []string{"caliente"}},
			{Code: "3", SKU: "TE", Label: "  CAFÉ  ", Price: 2.0},
			{Code: "4", SKU: "PINA", Label: "Piña colada", Price: 5.0, Tags: []string{"frío"}},
			{Code: "5", SKU: "PINA-SIN", Label: "Pina colada", Price: 4.0},
		}},
		{Code: "2", Label: "Postres", Items: []cart.Article{
			{Code: "1", SKU: "CAFE", Label: "Café en grano", Price: 9.0, Tags: []string{"clásico"}},
			{Code: "2", SKU: "TORTA", Label: "Torta de chocolate", Price: 20, Variants: []cart.Variant{
				{Code: "V1", Label: "Grande", Price: 25},
				{Code: "V2", Label: "grande", Price: 26},
				{Code: "V3", Label: "12 porciones", Price: 30},
			}},
			{Code: "3", SKU: "SINLABEL", Label: "", Price: 1},
			{Code: "4", SKU: "ANO", Label: "Torta de año nuevo", Price: 40, Tags: []string{"año"}},
		}},
	}}
}

// consultas es el corpus con el que se interroga a los dos —índice y oráculo—. Va
// deliberadamente más allá de lo que hay en el catálogo: fallos, cadena vacía, y el
// par «año»/«ano» que separa un normalizador correcto de uno que pliega la ñ.
func consultas() []string {
	return []string{
		"Café", "cafe", "CAFÉ", "  café  ", "café en grano",
		"Piña colada", "pina colada", "PIÑA COLADA",
		"Torta de chocolate", "torta   de   chocolate",
		"Grande", "grande", "GRANDE", "12 porciones",
		"Caliente", "caliente", "clásico", "clasico", "frío", "frio",
		"año", "ano", "Torta de año nuevo",
		"", " ", "no existe", "cafeteria", "caf",
	}
}

func skusDeConsulta() []string {
	return []string{"CAFE", "CAFE-2", "TE", "PINA", "PINA-SIN", "TORTA", "SINLABEL", "ANO", "", "cafe", "NO-EXISTE"}
}

// indiceTramposo construye el índice del catálogo tramposo o falla el test.
func indiceTramposo(t *testing.T) *catalogo.Indice {
	t.Helper()
	idx, err := catalogo.Construir(catalogoTramposo(), normalizadorDoble)
	if err != nil {
		t.Fatalf("construir el índice del catálogo tramposo: %v", err)
	}
	return idx
}
