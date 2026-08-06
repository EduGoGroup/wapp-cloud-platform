// Package catalogimport es el contrato del import de catálogo (Plan 041 · Ola 3,
// D-041.5) y su validador ESTRICTO. Es PURO: sin I/O, sin BD y sin HTTP.
//
// DOS RIGORES DISTINTOS, A PROPÓSITO. En RUNTIME el catálogo se parsea con
// tolerancia (cart.ParseCatalog): un campo v2 mal escrito se descarta con aviso y
// el negocio sigue vendiendo, porque dejar sin catálogo a un local abierto por una
// etiqueta mal puesta es peor que ignorar la etiqueta. En el IMPORT el MISMO
// documento se rechaza ENTERO, con todos sus defectos listados, para que el dueño
// lo arregle antes de que llegue a producción.
//
// De ahí la regla que ata los dos rigores y que fija un test de este paquete: el
// validador estricto ACEPTA exactamente lo que el runtime CONSERVA. Todo lo que
// cart.ParseCatalog descartaría con un aviso, aquí es un error. Un documento que
// valida produce un blob que el runtime parsea SIN un solo aviso.
//
// Por qué un modelo propio y no los tipos de cart: los tipos del carrito no
// llevan etiquetas json (su serialización alimenta un golden de no-regresión) y
// son el árbol del RUNTIME, no el documento que teclea o genera un LLM. Estos
// structs, en cambio, son el contrato publicado: de ellos sale la plantilla que se
// descarga (T3.2, patrón buildImportTemplate de EduGo) y a ellos vuelve lo que se
// sube. Los dos modelos no se contradicen: el test de coherencia los amarra.
package catalogimport

import "strconv"

// ImportFormat es la marca del documento: identifica el contrato y evita que se
// suba por error un JSON de otra cosa (un export de pedidos, una plantilla de
// EduGo). Viaja en el campo "format".
const ImportFormat = "wapp.catalog_import"

// ImportVersion es la versión del contrato que esta consola entiende. Vive JUNTO
// al validador a propósito (patrón EduGo D-038.6): cuando el contrato cambie,
// subir esta constante hace que las plantillas viejas se rechacen con un mensaje
// claro en vez de importarse a medias.
const ImportVersion = 1

// DefaultMaxJSONBytes es el techo por defecto del documento crudo: 1 MiB, el mismo
// que ya aplica PUT /api/v1/tenant-content al blob de catálogo, así que un
// documento que pasa el import cabe siempre en su destino. Se aplica ANTES de
// deserializar (ver ReadLimited).
const DefaultMaxJSONBytes int64 = 1 << 20

// DefaultMaxItems es el techo por defecto de artículos por importación (total del
// documento, sumando todas las categorías). No es una cota de catálogo: es una
// cota de CARGA, para que un documento absurdo no se convierta en miles de errores
// ni en un blob que el motor tenga que releer en cada mensaje.
const DefaultMaxItems = 500

// Limits son los topes anti-abuso del import. Los sirve la configuración
// (WAPP_TENANT_CONTENT_MAX_BYTES / WAPP_IMPORT_MAX_ITEMS — el de bytes gobierna la
// TABLA, no el import, y por eso no lleva su nombre); este paquete no lee el
// entorno, lo recibe. Un valor <= 0 cae al default: igual que en el resto del
// repo, la configuración nunca DESACTIVA un tope por accidente.
type Limits struct {
	// MaxJSONBytes es el tamaño máximo del documento crudo, en bytes.
	MaxJSONBytes int64
	// MaxItems es el número máximo de artículos del documento entero.
	MaxItems int
}

// DefaultLimits devuelve los topes por defecto del import.
func DefaultLimits() Limits {
	return Limits{MaxJSONBytes: DefaultMaxJSONBytes, MaxItems: DefaultMaxItems}
}

// normalized devuelve los límites con los valores no positivos sustituidos por su
// default.
func (l Limits) normalized() Limits {
	if l.MaxJSONBytes <= 0 {
		l.MaxJSONBytes = DefaultMaxJSONBytes
	}
	if l.MaxItems <= 0 {
		l.MaxItems = DefaultMaxItems
	}
	return l
}

// CatalogImport es el documento de import completo. El JSON es PORTÁTIL (INV-05):
// no lleva tenant ni ref: eso viaja por el token y el query param (D-041.6), de
// modo que el mismo archivo sirve para cualquier negocio.
type CatalogImport struct {
	Format  string        `json:"format"`
	Version int           `json:"version"`
	Source  *ImportSource `json:"source,omitempty"`
	Catalog ImportBody    `json:"catalog"`
}

// ImportSource es la procedencia declarada del documento: de qué herramienta
// salió. Es informativa —el validador no la interpreta— y sirve para que el dueño
// sepa después de dónde vino un catálogo.
type ImportSource struct {
	// Kind es el origen ("llm", "manual", "planilla", …).
	Kind string `json:"kind,omitempty"`
	// Model es el modelo concreto si el origen fue un LLM ("claude-opus", …).
	Model string `json:"model,omitempty"`
	// Hint es una nota libre del autor.
	Hint string `json:"hint,omitempty"`
}

// ImportBody es el catálogo propiamente dicho: la MISMA forma que el blob
// v2 que consume el motor (D-041.2), de modo que aplicar el import es escribir
// este objeto tal cual en tenant_content, sin traducción intermedia.
type ImportBody struct {
	Categories []ImportCategory `json:"categories"`
}

// ImportCategory es un grupo de artículos: el primer filtro de la conversación.
type ImportCategory struct {
	// Code es lo que teclea el cliente para elegir la categoría ("1", "2", …).
	Code string `json:"code"`
	// Label es el nombre visible de la categoría.
	Label string `json:"label"`
	// Subcategories es el segundo filtro, OPCIONAL. Los artículos la referencian
	// por Code.
	Subcategories []ImportSubcategory `json:"subcategories,omitempty"`
	// Items son los artículos de la categoría.
	Items []ImportItem `json:"items"`
}

// ImportSubcategory es un sub-grupo dentro de una categoría.
type ImportSubcategory struct {
	Code  string `json:"code"`
	Label string `json:"label"`
}

// ImportItem es un artículo del catálogo.
type ImportItem struct {
	// Code es lo que teclea el cliente dentro de la categoría.
	Code string `json:"code"`
	// SKU es el identificador de negocio: viaja en los efectos y en el pedido, y es
	// la clave con la que el diff decide qué cambió (D-041.7).
	SKU string `json:"sku"`
	// Label es el nombre visible del artículo.
	Label string `json:"label"`
	// Price es el precio unitario. Con Variants queda como precio de referencia: lo
	// que se cobra es el de la variante elegida.
	Price float64 `json:"price"`
	// Description es el detalle que se muestra en la ficha del artículo.
	Description string `json:"description,omitempty"`
	// Subcategory referencia un Code de las Subcategories de SU categoría.
	Subcategory string `json:"subcategory,omitempty"`
	// Tags son etiquetas informativas (búsqueda/ficha); el runtime no navega por
	// ellas.
	Tags []string `json:"tags,omitempty"`
	// Attributes son pares clave→valor informativos ("porciones": "10-12").
	Attributes map[string]string `json:"attributes,omitempty"`
	// Variants convierte el artículo en una elección de presentación: al agregarlo
	// se pide la variante y la línea toma SU precio. Excluyente con Components.
	Variants []ImportVariant `json:"variants,omitempty"`
	// Components describe un COMBO: se vende como UNA línea al Price del artículo y
	// sus integrantes son información del dueño. Excluyente con Variants.
	Components []ImportComponent `json:"components,omitempty"`
}

// ImportVariant es una presentación concreta de un artículo (tamaño, porciones…)
// con su propio precio.
type ImportVariant struct {
	// Code identifica la variante y se pega al sku de la línea ("TORTA#V2").
	Code string `json:"code"`
	// Label es lo que ve el cliente al elegir.
	Label string `json:"label"`
	// Price es lo que se cobra por esta presentación. Obligatorio: una variante sin
	// precio se vendería al de referencia del artículo, que es justo lo que el
	// contrato prohíbe.
	Price float64 `json:"price"`
}

// ImportComponent es un integrante de un combo. No se cobra por separado: el combo
// se vende a su propio precio.
type ImportComponent struct {
	// SKU referencia otro artículo del MISMO catálogo.
	SKU string `json:"sku"`
	// Qty son las unidades que entran en el combo. Ausente equivale a 1.
	Qty int `json:"qty,omitempty"`
}

// ImportFieldError localiza UN defecto del documento (adaptación del
// ImportFieldError de EduGo a la forma bidimensional del catálogo).
//
// Los índices son POSICIONES DE LISTA, base 0, para que la consola sepa a qué
// elemento saltar. La prosa de Reason, en cambio, cuenta como cuenta una persona
// («el artículo 3 de la categoría …» es el TERCERO): son dos lenguajes distintos
// para dos lectores distintos, y por eso no coinciden.
type ImportFieldError struct {
	// Row es la fila de la PLANILLA que produjo el defecto, en el número que la hoja
	// enseña en su margen izquierdo (la cabecera es la 1). Solo la rellena el camino
	// tabular (ParseTabular): quien sube un JSON no ha visto ninguna fila. Cero = el
	// defecto no es de una fila concreta —o el documento vino en JSON— y entonces
	// mandan los índices.
	//
	// Es int y no *int a propósito, al revés que los índices: una fila 0 no existe,
	// así que el cero-valor ya significa «ninguna» sin necesidad de un puntero.
	Row int `json:"row,omitempty"`
	// CategoryIndex es la posición de la categoría afectada; nil = error de cabecera
	// (format, version, límites) o del documento entero.
	CategoryIndex *int `json:"category_index,omitempty"`
	// ItemIndex es la posición del artículo DENTRO de su categoría; nil = el error es
	// de la categoría, no de un artículo suyo.
	ItemIndex *int `json:"item_index,omitempty"`
	// Field es el campo del contrato al que apunta el defecto ("price",
	// "variants[1].price", "categories"…).
	Field string `json:"field"`
	// Reason es la explicación EN ESPAÑOL y accionable, escrita para el dueño del
	// negocio y no para quien programó esto: dice qué elemento falla, qué le pasa y
	// qué hacer.
	Reason string `json:"reason"`
}

// ImportValidationError agrupa TODOS los defectos encontrados en una sola pasada.
// Que sea un error y no una lista suelta es deliberado: el llamante lo propaga con
// errors.As y lo serializa tal cual al cliente.
type ImportValidationError struct {
	Errors []ImportFieldError `json:"errors"`
}

// Error implementa error. El texto es un resumen; el detalle accionable está en
// cada entrada de Errors, que es lo que se le muestra al usuario.
func (e *ImportValidationError) Error() string {
	if e == nil || len(e.Errors) == 0 {
		return "el documento de import no es válido"
	}
	if len(e.Errors) == 1 {
		return "el documento de import tiene 1 problema: " + e.Errors[0].Reason
	}
	return "el documento de import tiene " + strconv.Itoa(len(e.Errors)) + " problemas; el primero: " + e.Errors[0].Reason
}
