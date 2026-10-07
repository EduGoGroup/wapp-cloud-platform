// Porta internal/catalogimport/diff.go @ 3c74b80

package catalogimport

import (
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// Diff es la respuesta a la única pregunta que el dueño se hace antes de aplicar
// un import: «¿qué le va a pasar a mi catálogo?» (D-041.7). Se calcula por SKU
// —no por posición ni por nombre— porque el sku es lo que viaja a las líneas del
// pedido: mover un artículo de categoría o corregirle una tilde no es un cambio
// de catálogo, cambiarle el precio sí.
//
// EL LADO VIEJO SE COMPARA PARSEADO, no como JSON crudo. Es lo que hace que el
// diff sobreviva a lo cosmético: reindentar el archivo, reordenar las claves o
// alternar entre 2 y 2.0 no produce ni un renglón. El precio de esa decisión es
// el hueco #6 de la ola y se paga con CurrentWarnings (ver ahí).
//
// Las listas NO son excluyentes entre sí: un artículo al que le cambian el precio
// Y las variantes aparece en PriceChanges y en ChangedDetails. Excluyente lo es
// solo Unchanged, que cuenta los que no salen en ninguna.
type Diff struct {
	// PriceChanges son los artículos que siguen existiendo y valen otra cosa. La
	// etiqueta que se muestra es la NUEVA (la que va a quedar tras aplicar): la
	// pantalla enseña el catálogo que viene, no el que se va.
	PriceChanges []PriceChange `json:"price_changes"`
	// Added son los skus que el documento trae y el catálogo vigente no tiene.
	Added []ItemRef `json:"added"`
	// Removed son los skus del catálogo vigente que el documento NO trae: dejan de
	// venderse en cuanto se aplique. Es la lista que hay que mirar dos veces.
	Removed []ItemRef `json:"removed"`
	// ChangedDetails son los skus a los que les cambió algo que NO es el precio
	// (variantes, tags, atributos, componentes, etiqueta, descripción, código o
	// subcategoría). Es un agregado a propósito: la v1 del diff dice QUE cambió,
	// no QUÉ campo (D-041.7 lo declara como mejora futura).
	ChangedDetails []string `json:"changed_details"`
	// Unchanged es cuántos artículos quedan exactamente igual. Sirve para que el
	// operador reconozca su catálogo: «3 cambios sobre 120» tranquiliza, «3
	// cambios sobre 4» avisa de que subió el archivo equivocado.
	Unchanged int `json:"unchanged"`
	// CurrentWarnings es lo que el catálogo VIGENTE ya tenía mal y el motor
	// ignoraba en silencio (hueco #6 de la ola). Importa porque esos artículos y
	// campos NO están en el lado viejo de la comparación: no aparecerán en
	// Removed aunque desaparezcan de verdad. Sin esta lista, un artículo con sku
	// reservado se esfumaría sin que nada lo dijera; con ella, el operador ve el
	// aviso antes de confirmar. Vacía cuando el catálogo vigente está impecable.
	CurrentWarnings []string `json:"current_warnings,omitempty"`
}

// PriceChange es un artículo que cambia de precio: el sku por el que se le
// reconoce, la etiqueta NUEVA y los dos precios.
type PriceChange struct {
	SKU      string  `json:"sku"`
	Label    string  `json:"label"`
	OldPrice float64 `json:"old_price"`
	NewPrice float64 `json:"new_price"`
}

// ItemRef identifica un artículo en las listas de altas y bajas. La etiqueta va
// junto al sku porque un sku suelto no le dice nada al dueño del negocio.
type ItemRef struct {
	SKU   string `json:"sku"`
	Label string `json:"label"`
}

// Empty indica que aplicar el documento no cambiaría el catálogo: ni altas, ni
// bajas, ni precios, ni detalles. Los avisos del catálogo vigente NO cuentan como
// cambio (describen lo que ya pasaba antes de este import), y Unchanged tampoco.
func (d Diff) Empty() bool {
	panic(pendiente.Implementar("catalogimport.Diff.Empty"))
}

// DiffCatalog compara el documento validado contra el catálogo vigente ya
// PARSEADO y devuelve el resumen de lo que cambiaría al aplicarlo. Es PURO: no
// lee BD ni toca el documento.
//
// Para un primer import —o para un blob vigente que ni siquiera se pudo parsear—
// se pasa el cero de catalogo.Catalog: sin lado viejo, todo el documento es Added
// (hueco #7 de la ola). Quien llama es el único que sabe distinguir «no había
// catálogo» de «lo había pero está roto», y por eso ese matiz viaja como aviso
// añadido a CurrentWarnings, no como un error de esta función.
//
// Promete:
//   - los artículos se casan por SKU, comparado tal cual (sin recortar): cambiar de
//     categoría o de posición no es un cambio;
//   - sku solo en el documento ⇒ Added, con la etiqueta nueva; sku solo en el
//     catálogo vigente ⇒ Removed, con la etiqueta VIEJA (el documento ya no la trae);
//   - mismo sku y otro precio ⇒ PriceChanges, con la etiqueta NUEVA y los dos
//     precios; el precio de una variante no es el precio del artículo;
//   - mismo sku y otra etiqueta, código, descripción, subcategoría, tags, atributos,
//     variantes o componentes ⇒ ChangedDetails. El orden de tags y de variantes SÍ
//     cuenta (es el orden en que se le enseñan al cliente); unos atributos nil y un
//     mapa vacío son lo mismo; la qty ausente de un componente y la qty 1 son la
//     misma;
//   - un artículo puede salir en PriceChanges y en ChangedDetails a la vez;
//     Unchanged cuenta los que no salen en ninguna de las dos;
//   - un sku repetido en el catálogo vigente se queda con su PRIMERA aparición, que
//     es la que el runtime encuentra al buscar;
//   - las cuatro listas salen ordenadas por sku (orden de bytes, no el del
//     documento) y nunca son nil: sin elementos se serializan como [];
//   - CurrentWarnings trae una línea por aviso del parseo tolerante del catálogo
//     vigente, en su orden: «catálogo vigente», más « · categoría "c"», « · artículo
//     "s"» y « · campo "f"» cuando el aviso los trae, más «: », el motivo y
//     « (el motor ya lo ignoraba, así que no entra en la comparación de arriba)».
//     Sin avisos es nil y no viaja en el JSON.
func DiffCatalog(current catalogo.Catalog, next ImportBody) Diff {
	panic(pendiente.Implementar("catalogimport.DiffCatalog"))
}
