// Package cart es el módulo del carrito conversacional (Plan 016): una
// sub-máquina de navegación por un catálogo de dos niveles (categorías →
// artículos) que acumula líneas de pedido. Los tipos de este archivo son
// PUROS (sin I/O ni dependencias externas más allá del contrato model): el
// árbol de catálogo del módulo y su parser desde model.Content.Raw.
//
// El contrato genérico model.Content (Plan 015) NO conoce el concepto de
// "categoría": se mantiene agnóstico del dominio. El árbol del carrito viaja en
// el blob crudo model.Content.Raw (map[string]any, poblado por el adapter json)
// y este módulo define sus propios tipos que lo deserializan (design.md §3.1).
//
// CATÁLOGO v2 (Plan 041 · D-041.2/D-041.3): el blob admite además
// subcategorías, etiquetas, atributos, variantes y componentes de combo. TODO lo
// nuevo es OPCIONAL —un blob v1 es un v2 válido y se parsea exactamente igual—
// y el puerto ContentSource no cambia (INV-02): los campos nuevos ya viajaban
// por Raw desde el Plan 016.
package cart

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
)

// SystemSKUPrefix es el prefijo RESERVADO del sistema en los skus (D-041.2). Las
// líneas que wApp añade por su cuenta —p. ej. "_shipping", el envío del Plan 041
// Ola 4— viven bajo este prefijo, así que un artículo de catálogo que lo use
// haría indistinguible lo que pidió el cliente de lo que puso el sistema. En
// runtime tales artículos se DESCARTAN con aviso (el resto del catálogo sigue
// vendiendo); en el import el validador estricto lo rechaza de plano.
const SystemSKUPrefix = "_"

// Nombres de los campos del contrato v2, tal como aparecen en el aviso cuando se
// descartan. Se usan como valor de CatalogWarning.Field para que el log diga
// exactamente qué parte del blob quedó fuera.
const (
	fieldSubcategories = "subcategories"
	fieldSubcategory   = "subcategory"
	fieldTags          = "tags"
	fieldAttributes    = "attributes"
	fieldVariants      = "variants"
	fieldComponents    = "components"
	fieldSKU           = "sku"
)

// maxCatalogWarnings acota cuántos avisos se acumulan en un parseo. Un catálogo
// roto de miles de artículos no debe llenar la memoria ni el log con la misma
// queja repetida; a partir del tope solo se cuenta cuántos quedaron fuera.
const maxCatalogWarnings = 50

// Catalog es el árbol de catálogo del carrito: una lista ordenada de categorías,
// cada una con sus artículos. Tipo PROPIO del módulo (design.md §3.1).
//
// Las etiquetas json de los campos v2 (aquí y en Category/Article) llevan
// `omitempty` a propósito: el golden de no-regresión serializa el árbol entero y
// compara con el resultado congelado ANTES del v2, de modo que un blob v1 solo
// coincide si NINGÚN campo nuevo se pobló.
type Catalog struct {
	Categories []Category
	// Warnings son los campos del contrato v2 que se descartaron por venir mal
	// formados. El parseo de runtime es TOLERANTE (design.md §D-041.3): un
	// catálogo a medias tiene que seguir vendiendo, así que lo ilegible se ignora
	// y se avisa. El validador ESTRICTO —el que rechaza el documento entero para
	// que el dueño lo arregle— es el del import (Ola 3), no este.
	Warnings []CatalogWarning `json:",omitempty"`
}

// CatalogWarning describe un campo v2 descartado durante el parseo tolerante.
// Category es el código de la categoría afectada; SKU el del artículo (vacío si
// el aviso es de la categoría); Field el campo del contrato v2; Reason el motivo
// en lenguaje llano.
type CatalogWarning struct {
	Category string
	SKU      string
	Field    string
	Reason   string
}

// Category es un grupo de artículos. Code es lo que teclea el usuario para
// elegir la categoría ("1", "2", …); Label es su etiqueta visible.
type Category struct {
	Code  string
	Label string
	Items []Article
	// Subcategories es el segundo filtro OPCIONAL del v2 (D-041.2). Categoría
	// como primer filtro + subcategoría opcional, y ahí se para.
	Subcategories []Subcategory `json:",omitempty"`
}

// Subcategory es un sub-grupo dentro de una categoría. Los artículos la
// referencian por Code (Article.Subcategory).
type Subcategory struct {
	Code  string
	Label string
}

// Article es un artículo del catálogo. Code es lo que teclea el usuario dentro
// de la categoría; SKU es el identificador de negocio (viaja en los efectos);
// Price es el precio unitario; Description es el detalle mostrado bajo "ver
// descripción" (opcional).
type Article struct {
	Code        string
	SKU         string
	Label       string
	Price       float64
	Description string
	// --- contrato v2 (D-041.2), todo OPCIONAL --------------------------------
	// Subcategory referencia un Code de las Subcategories de SU categoría; una
	// referencia colgante se descarta con aviso.
	Subcategory string `json:",omitempty"`
	// Tags y Attributes son informativos (búsqueda/ficha); el runtime no navega
	// por ellos.
	Tags       []string          `json:",omitempty"`
	Attributes map[string]string `json:",omitempty"`
	// Variants convierte el artículo en un sub-nivel de elección: al agregarlo se
	// pide la variante y la línea toma SU precio y SU etiqueta (D-041.4). Price
	// queda como referencia/base.
	Variants []Variant `json:",omitempty"`
	// Components describe un COMBO: se vende como UNA línea al Price del
	// artículo; los componentes son para el dueño y el puente, no para el
	// cliente. Excluyente con Variants.
	Components []Component `json:",omitempty"`
}

// Variant es una presentación concreta de un artículo (tamaño, porciones, …) con
// su propio precio. Code es el identificador estable que se pega al sku de la
// línea ("TORTA-CHOC#V2"); Label es lo que ve el cliente.
type Variant struct {
	Code  string
	Label string
	Price float64
}

// Component es un integrante de un combo. SKU referencia otro artículo del
// catálogo (integridad que verifica el import, no el runtime) y Qty cuántas
// unidades suyas entran en el combo.
type Component struct {
	SKU string
	Qty int
}

// HasVariants indica si el artículo se vende por variantes (y por tanto la
// sub-máquina pide elegir una antes de la cantidad).
func (a Article) HasVariants() bool { return len(a.Variants) > 0 }

// IsCombo indica si el artículo es un combo (se vende como UNA línea a su propio
// precio, con los componentes como información del dueño).
func (a Article) IsCombo() bool { return len(a.Components) > 0 }

// catalogBlob es la forma JSON esperada del blob de catálogo por-tenant (tabla
// tenant_content del Plan 015, resuelto por el adapter json). El seed del e2e
// del Plan 016 depende de esta forma exacta:
//
//	{
//	  "categories": [
//	    { "code": "1", "label": "Bebidas", "items": [
//	      { "code": "1", "sku": "CAFE", "label": "Café", "price": 2.5, "description": "Espresso doble" },
//	      { "code": "2", "sku": "TE",   "label": "Té",   "price": 2.0, "description": "Verde o negro" }
//	    ] },
//	    { "code": "2", "label": "Postres", "items": [
//	      { "code": "1", "sku": "FLAN", "label": "Flan", "price": 3.0, "description": "Casero" }
//	    ] }
//	  ]
//	}
//
// El campo "description" de cada artículo es OPCIONAL.
//
// Los campos del v2 se declaran json.RawMessage y se decodifican UNO A UNO
// (parseSubcategories, parseTags, …). No es un rodeo: si fueran campos tipados,
// un "tags": "decoracion" (cadena donde va una lista) haría fallar el Unmarshal
// del blob ENTERO y el tenant se quedaría sin catálogo por una etiqueta mal
// escrita. Decodificados aparte, ese campo se descarta con aviso y el catálogo
// sigue vendiendo. Los campos del v1 siguen siendo ESTRICTOS a propósito: un
// price no numérico sí tumba el parseo, como antes del v2.
type catalogBlob struct {
	Categories []categoryBlob `json:"categories"`
}

type categoryBlob struct {
	Code          string          `json:"code"`
	Label         string          `json:"label"`
	Items         []articleBlob   `json:"items"`
	Subcategories json.RawMessage `json:"subcategories"`
}

type articleBlob struct {
	Code        string          `json:"code"`
	SKU         string          `json:"sku"`
	Label       string          `json:"label"`
	Price       float64         `json:"price"`
	Description string          `json:"description"`
	Subcategory json.RawMessage `json:"subcategory"`
	Tags        json.RawMessage `json:"tags"`
	Attributes  json.RawMessage `json:"attributes"`
	Variants    json.RawMessage `json:"variants"`
	Components  json.RawMessage `json:"components"`
}

type subcategoryBlob struct {
	Code  string `json:"code"`
	Label string `json:"label"`
}

type variantBlob struct {
	Code  string   `json:"code"`
	Label string   `json:"label"`
	Price *float64 `json:"price"` // puntero: distingue "ausente" de 0
}

type componentBlob struct {
	SKU string   `json:"sku"`
	Qty *float64 `json:"qty"` // puntero: distingue "ausente" de 0
}

// warnBag acumula los avisos del parseo tolerante con tope (maxCatalogWarnings).
type warnBag struct {
	list    []CatalogWarning
	dropped int
}

func (w *warnBag) add(category, sku, field, reason string) {
	if len(w.list) >= maxCatalogWarnings {
		w.dropped++
		return
	}
	w.list = append(w.list, CatalogWarning{Category: category, SKU: sku, Field: field, Reason: reason})
}

// result cierra la bolsa: si hubo avisos por encima del tope, añade uno final que
// dice cuántos quedaron sin detallar (para que el log no mienta por omisión).
func (w *warnBag) result() []CatalogWarning {
	if w.dropped > 0 {
		w.list = append(w.list, CatalogWarning{
			Field:  "(varios)",
			Reason: "se omitieron " + strconv.Itoa(w.dropped) + " avisos más del mismo parseo",
		})
	}
	return w.list
}

// ParseCatalog deserializa model.Content.Raw (el blob crudo que puebla el
// adapter json) al árbol tipado del catálogo, v1 o v2 indistintamente. Es PURO:
// sin I/O.
//
// Tolera el round-trip JSONB de forma natural: Raw es un map[string]any (números
// como float64, claves como string) que se re-serializa a JSON y se decodifica
// sobre los tipos del blob. Devuelve un error envuelto sobre model.ErrInvalidFlow
// (inspeccionable con errors.Is) cuando:
//   - Raw es nil/ausente (el nodo no resolvió contenido con árbol de catálogo);
//   - el blob no cuadra con la forma esperada del v1 (categorías, items, price…);
//   - el catálogo no tiene ninguna categoría.
//
// Un campo del v2 mal formado NUNCA produce error: se descarta y queda anotado en
// Catalog.Warnings (el módulo los registra por el logger al resolver el catálogo).
func ParseCatalog(c model.Content) (Catalog, error) {
	if c.Raw == nil {
		return Catalog{}, fmt.Errorf("%w: el carrito exige content.raw con el árbol de catálogo, pero llegó vacío", model.ErrInvalidFlow)
	}

	// Re-serializar el map[string]any y decodificarlo sobre los tipos del blob:
	// absorbe el round-trip JSONB (float64/string) sin conversiones manuales.
	raw, err := json.Marshal(c.Raw)
	if err != nil {
		return Catalog{}, fmt.Errorf("%w: no se pudo re-serializar content.raw del catálogo: %w", model.ErrInvalidFlow, err)
	}

	var blob catalogBlob
	if err := json.Unmarshal(raw, &blob); err != nil {
		return Catalog{}, fmt.Errorf("%w: blob de catálogo mal formado: %w", model.ErrInvalidFlow, err)
	}

	if len(blob.Categories) == 0 {
		return Catalog{}, fmt.Errorf("%w: el catálogo no tiene categorías", model.ErrInvalidFlow)
	}

	w := &warnBag{}
	cat := Catalog{Categories: make([]Category, 0, len(blob.Categories))}
	for _, cb := range blob.Categories {
		cat.Categories = append(cat.Categories, parseCategory(cb, w))
	}
	cat.Warnings = w.result()
	return cat, nil
}

// parseCategory arma una categoría con sus subcategorías (v2) y sus artículos.
func parseCategory(cb categoryBlob, w *warnBag) Category {
	category := Category{
		Code:          cb.Code,
		Label:         cb.Label,
		Items:         make([]Article, 0, len(cb.Items)),
		Subcategories: parseSubcategories(cb, w),
	}
	for _, ab := range cb.Items {
		if a, ok := parseArticle(ab, category, w); ok {
			category.Items = append(category.Items, a)
		}
	}
	return category
}

// parseSubcategories decodifica "subcategories" de una categoría. Una lista mal
// formada se ignora entera; las entradas sin code o con code repetido se
// descartan (un code ambiguo no serviría para referenciarlas).
func parseSubcategories(cb categoryBlob, w *warnBag) []Subcategory {
	if len(cb.Subcategories) == 0 {
		return nil
	}
	var raw []subcategoryBlob
	if err := json.Unmarshal(cb.Subcategories, &raw); err != nil {
		w.add(cb.Code, "", fieldSubcategories, "no es una lista de {code,label}: se ignoran las subcategorías de la categoría")
		return nil
	}
	out := make([]Subcategory, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for _, s := range raw {
		switch {
		case s.Code == "":
			w.add(cb.Code, "", fieldSubcategories, "subcategoría sin code: se descarta")
		case seen[s.Code]:
			w.add(cb.Code, "", fieldSubcategories, "subcategoría con code repetido "+strconv.Quote(s.Code)+": se descarta")
		default:
			seen[s.Code] = true
			// subcategoryBlob y Subcategory tienen los mismos campos y tipos (solo
			// difieren en los tags json): la conversión es válida.
			out = append(out, Subcategory(s))
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// parseArticle arma un artículo con sus campos v1 (estrictos, ya decodificados) y
// sus campos v2 (tolerantes). Devuelve ok=false cuando el artículo entero se
// descarta: hoy, solo por usar el prefijo de sku reservado del sistema.
func parseArticle(ab articleBlob, category Category, w *warnBag) (Article, bool) {
	if strings.HasPrefix(ab.SKU, SystemSKUPrefix) {
		w.add(category.Code, ab.SKU, fieldSKU,
			"el prefijo "+strconv.Quote(SystemSKUPrefix)+" está reservado para las líneas del sistema: el artículo se descarta")
		return Article{}, false
	}
	a := Article{
		Code:        ab.Code,
		SKU:         ab.SKU,
		Label:       ab.Label,
		Price:       ab.Price,
		Description: ab.Description,
		Subcategory: parseSubcategoryRef(ab, category, w),
		Tags:        parseTags(ab, category, w),
		Attributes:  parseAttributes(ab, category, w),
		Variants:    parseVariants(ab, category, w),
		Components:  parseComponents(ab, category, w),
	}
	// variants XOR components (D-041.2): con los dos, se conservan las variantes
	// —que sí cambian lo que se cobra— y se ignoran los componentes, que son
	// informativos. El import rechaza el documento; el runtime sigue vendiendo.
	if a.HasVariants() && a.IsCombo() {
		w.add(category.Code, a.SKU, fieldComponents,
			"el artículo declara variants y components a la vez: se ignoran los components (el import lo rechazará)")
		a.Components = nil
	}
	return a, true
}

// parseSubcategoryRef decodifica "subcategory" y comprueba que apunte a una
// subcategoría declarada por SU categoría. Una referencia colgante se descarta:
// dejarla pasar prometería un filtro que no existe.
func parseSubcategoryRef(ab articleBlob, category Category, w *warnBag) string {
	if len(ab.Subcategory) == 0 {
		return ""
	}
	var ref string
	if err := json.Unmarshal(ab.Subcategory, &ref); err != nil {
		w.add(category.Code, ab.SKU, fieldSubcategory, "no es un texto: se ignora")
		return ""
	}
	if ref == "" {
		return ""
	}
	for _, s := range category.Subcategories {
		if s.Code == ref {
			return ref
		}
	}
	w.add(category.Code, ab.SKU, fieldSubcategory,
		"referencia "+strconv.Quote(ref)+", que no es una subcategoría de su categoría: se ignora")
	return ""
}

// parseTags decodifica "tags". Una lista mal formada se ignora entera; las
// etiquetas vacías se descartan.
func parseTags(ab articleBlob, category Category, w *warnBag) []string {
	if len(ab.Tags) == 0 {
		return nil
	}
	var raw []string
	if err := json.Unmarshal(ab.Tags, &raw); err != nil {
		w.add(category.Code, ab.SKU, fieldTags, "no es una lista de textos: se ignoran las etiquetas del artículo")
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, tag := range raw {
		if strings.TrimSpace(tag) == "" {
			w.add(category.Code, ab.SKU, fieldTags, "etiqueta vacía: se descarta")
			continue
		}
		out = append(out, tag)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// parseAttributes decodifica "attributes" como pares texto→texto. Se decodifica
// a map[string]any y se convierten los escalares (número/booleano) a texto: un
// {"porciones": 12} es un atributo perfectamente legible y tumbarlo entero por el
// tipo sería el rigor del import, no el del runtime. Lo que no es escalar
// (objetos, listas) sí se descarta.
func parseAttributes(ab articleBlob, category Category, w *warnBag) map[string]string {
	if len(ab.Attributes) == 0 {
		return nil
	}
	var raw map[string]any
	if err := json.Unmarshal(ab.Attributes, &raw); err != nil {
		w.add(category.Code, ab.SKU, fieldAttributes, "no es un objeto de pares clave→valor: se ignoran los atributos del artículo")
		return nil
	}
	out := make(map[string]string, len(raw))
	for _, k := range sortedKeys(raw) { // orden estable: el log no debe bailar entre corridas
		if k == "" {
			w.add(category.Code, ab.SKU, fieldAttributes, "atributo con clave vacía: se descarta")
			continue
		}
		v, ok := scalarToString(raw[k])
		if !ok {
			w.add(category.Code, ab.SKU, fieldAttributes, "el atributo "+strconv.Quote(k)+" no es un valor simple: se descarta")
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// parseVariants decodifica "variants". Una lista mal formada se ignora entera.
// Cada variante exige code, label y price (D-041.2: el price de la variante es
// obligatorio cuando hay variantes); a la que le falte alguno se descarta, porque
// una variante sin precio se vendería al precio de referencia del artículo, que
// es exactamente lo que el contrato prohíbe.
func parseVariants(ab articleBlob, category Category, w *warnBag) []Variant {
	if len(ab.Variants) == 0 {
		return nil
	}
	var raw []variantBlob
	if err := json.Unmarshal(ab.Variants, &raw); err != nil {
		w.add(category.Code, ab.SKU, fieldVariants, "no es una lista de {code,label,price}: el artículo se vende sin variantes")
		return nil
	}
	out := make([]Variant, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for _, v := range raw {
		if reason, ok := variantRejection(v, seen); ok {
			w.add(category.Code, ab.SKU, fieldVariants, reason)
			continue
		}
		seen[v.Code] = true
		out = append(out, Variant{Code: v.Code, Label: v.Label, Price: *v.Price})
	}
	if len(out) == 0 {
		if len(raw) > 0 {
			w.add(category.Code, ab.SKU, fieldVariants, "no queda ninguna variante utilizable: el artículo se vende sin variantes")
		}
		return nil
	}
	return out
}

// variantRejection devuelve el motivo por el que una variante se descarta, u
// ok=false si es utilizable.
func variantRejection(v variantBlob, seen map[string]bool) (string, bool) {
	switch {
	case v.Code == "":
		return "variante sin code: se descarta", true
	case seen[v.Code]:
		return "variante con code repetido " + strconv.Quote(v.Code) + ": se descarta", true
	case v.Label == "":
		return "la variante " + strconv.Quote(v.Code) + " no tiene label con el que ofrecerla: se descarta", true
	case v.Price == nil:
		return "la variante " + strconv.Quote(v.Code) + " no trae price (obligatorio con variantes): se descarta", true
	case *v.Price < 0:
		return "la variante " + strconv.Quote(v.Code) + " tiene price negativo: se descarta", true
	default:
		return "", false
	}
}

// parseComponents decodifica "components" (los integrantes de un combo). Una
// lista mal formada se ignora entera; un componente sin sku se descarta. La qty
// ausente vale 1, y una qty menor que 1 se corrige a 1 con aviso: el combo se
// cobra a su propio precio, así que la cantidad del componente es informativa y
// no justifica perder el combo. Que cada sku exista en el catálogo lo comprueba
// el validador del import, no el runtime.
func parseComponents(ab articleBlob, category Category, w *warnBag) []Component {
	if len(ab.Components) == 0 {
		return nil
	}
	var raw []componentBlob
	if err := json.Unmarshal(ab.Components, &raw); err != nil {
		w.add(category.Code, ab.SKU, fieldComponents, "no es una lista de {sku,qty}: se ignoran los componentes del combo")
		return nil
	}
	out := make([]Component, 0, len(raw))
	for _, c := range raw {
		if c.SKU == "" {
			w.add(category.Code, ab.SKU, fieldComponents, "componente sin sku: se descarta")
			continue
		}
		qty := 1
		if c.Qty != nil {
			if qty = int(*c.Qty); qty < 1 {
				w.add(category.Code, ab.SKU, fieldComponents,
					"el componente "+strconv.Quote(c.SKU)+" trae una qty menor que 1: se toma 1")
				qty = 1
			}
		}
		out = append(out, Component{SKU: c.SKU, Qty: qty})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// scalarToString convierte a texto un valor escalar de JSON (texto, número o
// booleano). Devuelve ok=false para objetos, listas y null.
func scalarToString(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(t), true
	default:
		return "", false
	}
}

// sortedKeys devuelve las claves de un mapa en orden estable (el recorrido de un
// map en Go es aleatorio y los avisos deben salir siempre igual).
func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// loadCatalog reconstruye el catálogo desde el snapshot que vive en
// Vars["cart_catalog"] (misma forma que model.Content.Raw). Es la vía por la que
// Step —que NO recibe el content resuelto, a diferencia de Render— accede al
// catálogo sin hacer I/O (design.md §4.1, nota de ejecución). Lo siembra el
// runtime. Un snapshot ausente/mal formado deriva en el mismo error que
// ParseCatalog (envuelto sobre model.ErrInvalidFlow).
func loadCatalog(vars map[string]any) (Catalog, error) {
	raw, ok := vars[catalogVarKey].(map[string]any)
	if !ok {
		// Ausente o de otro tipo: se trata igual que Raw nil (ParseCatalog
		// devuelve el error de "catálogo ausente").
		raw = nil
	}
	return ParseCatalog(model.Content{Raw: raw})
}
