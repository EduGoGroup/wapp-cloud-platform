// Porta internal/flujos/modules/cart/catalog.go @ 3c74b80

// Package catalogo es el modelo del catálogo de un tenant, sacado del módulo cart (Plan 016)
// porque también lo leen el índice de captación y el importador: el árbol de dos niveles
// (categorías → artículos) y su parser desde model.Content.Raw. Los tipos de este archivo son
// PUROS (sin I/O ni dependencias externas más allá del contrato model).
//
// El contrato genérico model.Content (Plan 015) NO conoce el concepto de
// "categoría": se mantiene agnóstico del dominio. El árbol del catálogo viaja en
// el blob crudo model.Content.Raw (map[string]any, poblado por el adapter json)
// y este paquete define sus propios tipos que lo deserializan (design.md §3.1).
//
// CATÁLOGO v2 (Plan 041 · D-041.2/D-041.3): el blob admite además
// subcategorías, etiquetas, atributos, variantes y componentes de combo. TODO lo
// nuevo es OPCIONAL —un blob v1 es un v2 válido y se parsea exactamente igual—
// y el puerto ContentSource no cambia (INV-02): los campos nuevos ya viajaban
// por Raw desde el Plan 016. loadCatalog (el snapshot de Vars) NO vive aquí: es del carrito.
package catalogo

import (
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// SystemSKUPrefix es el prefijo RESERVADO del sistema en los skus (D-041.2). Las
// líneas que wApp añade por su cuenta —p. ej. "_shipping", el envío del Plan 041
// Ola 4— viven bajo este prefijo, así que un artículo de catálogo que lo use
// haría indistinguible lo que pidió el cliente de lo que puso el sistema. En
// runtime tales artículos se DESCARTAN con aviso (el resto del catálogo sigue
// vendiendo); en el import el validador estricto lo rechaza de plano.
const SystemSKUPrefix = "_"

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
func (a Article) HasVariants() bool {
	panic(pendiente.Implementar("catalogo.Article.HasVariants"))
}

// IsCombo indica si el artículo es un combo (se vende como UNA línea a su propio
// precio, con los componentes como información del dueño).
func (a Article) IsCombo() bool {
	panic(pendiente.Implementar("catalogo.Article.IsCombo"))
}

// ParseCatalog deserializa model.Content.Raw (el blob crudo que puebla el adapter json) al árbol
// tipado del catálogo, v1 o v2 indistintamente. Es PURO: sin I/O.
//
// Tolera el round-trip JSONB de forma natural: Raw es un map[string]any (números como float64,
// claves como string) que se re-serializa a JSON y se decodifica sobre los tipos del blob. Devuelve
// el Catalog cero y un error envuelto sobre model.ErrInvalidFlow (inspeccionable con errors.Is)
// cuando: Raw es nil («el carrito exige content.raw con el árbol de catálogo, pero llegó vacío»);
// Raw no se puede re-serializar («no se pudo re-serializar content.raw del catálogo: <error>»); el
// blob no cuadra con la forma del v1, que es ESTRICTA —un price no numérico tumba el parseo—
// («blob de catálogo mal formado: <error>»); o no hay categorías («el catálogo no tiene categorías»).
//
// Un campo del v2 mal formado NUNCA produce error: se descarta y queda anotado en
// Catalog.Warnings (Category; SKU, vacío si el aviso es de la categoría; Field, el campo; Reason,
// el motivo literal), en orden de documento y, dentro de un artículo, subcategory → tags →
// attributes → variants → components. Sin nada que avisar, Warnings es nil; un campo v2 que se
// queda sin nada utilizable es nil, no vacío (Items, en cambio, nunca es nil). Nada se recorta ni
// se normaliza: dos codes que solo difieren en un espacio son distintos. Reglas y motivos:
//   - subcategories: «no es una lista de {code,label}: se ignoran las subcategorías de la
//     categoría»; «subcategoría sin code: se descarta»; «subcategoría con code repetido %q: se
//     descarta» (gana la primera);
//   - subcategory: «no es un texto: se ignora»; si no es de SU categoría, «referencia %q, que no
//     es una subcategoría de su categoría: se ignora»;
//   - tags: «no es una lista de textos: se ignoran las etiquetas del artículo»; la vacía o de solo
//     blancos, «etiqueta vacía: se descarta»; la que queda no se recorta;
//   - attributes: «no es un objeto de pares clave→valor: se ignoran los atributos del artículo»;
//     texto, número (sin exponente) y booleano pasan a texto; «atributo con clave vacía: se
//     descarta»; objeto, lista o null, «el atributo %q no es un valor simple: se descarta»; las
//     claves se recorren ORDENADAS;
//   - variants: «no es una lista de {code,label,price}: el artículo se vende sin variantes»; por
//     variante y en este orden, «variante sin code: se descarta», «variante con code repetido %q:
//     se descarta», «la variante %q no tiene label con el que ofrecerla: se descarta», «la
//     variante %q no trae price (obligatorio con variantes): se descarta» (ausente ≠ 0, que sí
//     vale), «la variante %q tiene price negativo: se descarta»; si había alguna y no queda
//     ninguna, además «no queda ninguna variante utilizable: el artículo se vende sin variantes»;
//   - components: «no es una lista de {sku,qty}: se ignoran los componentes del combo»;
//     «componente sin sku: se descarta»; qty ausente vale 1, con decimales se trunca, y menor que
//     1 vale 1 con «el componente %q trae una qty menor que 1: se toma 1»;
//   - variants Y components: quedan las variantes y, en components, «el artículo declara variants
//     y components a la vez: se ignoran los components (el import lo rechazará)»;
//   - sku que EMPIEZA por SystemSKUPrefix (un espacio delante lo salva): el ARTÍCULO se cae sin
//     mirar sus campos v2, con Field «sku» y «el prefijo "_" está reservado para las líneas del
//     sistema: el artículo se descarta»;
//   - tope de 50 avisos; si hubo más, uno final sin categoría ni sku, con Field «(varios)» y «se
//     omitieron N avisos más del mismo parseo».
func ParseCatalog(c model.Content) (Catalog, error) {
	panic(pendiente.Implementar("catalogo.ParseCatalog"))
}
