package catalogimport

import (
	"slices"
	"strconv"
	"strings"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/cart"
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
// cambio (describen lo que ya pasaba antes de este import).
func (d Diff) Empty() bool {
	return len(d.PriceChanges) == 0 && len(d.Added) == 0 && len(d.Removed) == 0 && len(d.ChangedDetails) == 0
}

// DiffCatalog compara el documento validado contra el catálogo vigente ya
// PARSEADO y devuelve el resumen de lo que cambiaría al aplicarlo. Es PURO: no
// lee BD ni toca el documento.
//
// Para un primer import —o para un blob vigente que ni siquiera se pudo parsear—
// se pasa el cero de cart.Catalog: sin lado viejo, todo el documento es Added
// (hueco #7 de la ola). Quien llama es el único que sabe distinguir «no había
// catálogo» de «lo había pero está roto», y por eso ese matiz viaja como aviso
// añadido a CurrentWarnings, no como un error de esta función.
func DiffCatalog(current cart.Catalog, next ImportBody) Diff {
	oldItems := flattenCurrent(current)
	newItems := flattenNext(next)

	d := Diff{
		PriceChanges:    make([]PriceChange, 0),
		Added:           make([]ItemRef, 0),
		Removed:         make([]ItemRef, 0),
		ChangedDetails:  make([]string, 0),
		CurrentWarnings: describeWarnings(current.Warnings),
	}

	for _, sku := range sortedSKUs(newItems) {
		item := newItems[sku]
		before, existed := oldItems[sku]
		if !existed {
			d.Added = append(d.Added, ItemRef{SKU: sku, Label: item.label})
			continue
		}
		priceChanged := before.price != item.price
		if priceChanged {
			d.PriceChanges = append(d.PriceChanges, PriceChange{
				SKU: sku, Label: item.label, OldPrice: before.price, NewPrice: item.price,
			})
		}
		detailsChanged := !sameDetails(before, item)
		if detailsChanged {
			d.ChangedDetails = append(d.ChangedDetails, sku)
		}
		if !priceChanged && !detailsChanged {
			d.Unchanged++
		}
	}

	for _, sku := range sortedSKUs(oldItems) {
		if _, stays := newItems[sku]; !stays {
			d.Removed = append(d.Removed, ItemRef{SKU: sku, Label: oldItems[sku].label})
		}
	}
	return d
}

// diffItem es la forma NORMALIZADA de un artículo, común a los dos lados de la
// comparación. Existe para que el catálogo vigente (tipos de cart, campos
// exportados sin etiquetas json) y el documento de import (tipos del contrato) se
// comparen con UNA sola función y no con dos que puedan divergir.
type diffItem struct {
	label       string
	code        string
	description string
	subcategory string
	price       float64
	tags        []string
	attributes  map[string]string
	variants    []ImportVariant
	components  []ImportComponent
}

// flattenCurrent aplana el catálogo vigente parseado a sku → artículo.
//
// Un sku REPETIDO en el catálogo vigente se queda con su PRIMERA aparición: el
// validador del import prohíbe los duplicados (D-041.5), pero el parseo de
// runtime es tolerante y un blob viejo puede traerlos. Quedarse con el primero
// —en vez de con el último— es la misma preferencia que aplica el runtime al
// buscar, así que el diff describe el artículo que el cliente veía.
func flattenCurrent(cat cart.Catalog) map[string]diffItem {
	out := make(map[string]diffItem)
	for _, c := range cat.Categories {
		for _, a := range c.Items {
			if _, dup := out[a.SKU]; dup {
				continue
			}
			out[a.SKU] = diffItem{
				label:       a.Label,
				code:        a.Code,
				description: a.Description,
				subcategory: a.Subcategory,
				price:       a.Price,
				tags:        a.Tags,
				attributes:  a.Attributes,
				variants:    toImportVariants(a.Variants),
				components:  toImportComponents(a.Components),
			}
		}
	}
	return out
}

// flattenNext aplana el documento de import a sku → artículo. Los duplicados no
// se contemplan: el documento llega YA validado y el sku único en todo el
// catálogo es una de sus reglas.
func flattenNext(body ImportBody) map[string]diffItem {
	out := make(map[string]diffItem)
	for _, c := range body.Categories {
		for _, it := range c.Items {
			out[it.SKU] = diffItem{
				label:       it.Label,
				code:        it.Code,
				description: it.Description,
				subcategory: it.Subcategory,
				price:       it.Price,
				tags:        it.Tags,
				attributes:  it.Attributes,
				variants:    it.Variants,
				components:  normalizeComponents(it.Components),
			}
		}
	}
	return out
}

// toImportVariants traduce las variantes del runtime a la forma del contrato.
func toImportVariants(in []cart.Variant) []ImportVariant {
	if len(in) == 0 {
		return nil
	}
	out := make([]ImportVariant, 0, len(in))
	for _, v := range in {
		out = append(out, ImportVariant{Code: v.Code, Label: v.Label, Price: v.Price})
	}
	return out
}

// toImportComponents traduce los componentes del runtime a la forma del contrato.
// El runtime ya materializó la qty ausente como 1 (parseComponents), igual que
// hace normalizeComponents con el lado nuevo: los dos lados llegan comparables.
func toImportComponents(in []cart.Component) []ImportComponent {
	if len(in) == 0 {
		return nil
	}
	out := make([]ImportComponent, 0, len(in))
	for _, c := range in {
		out = append(out, ImportComponent{SKU: c.SKU, Qty: c.Qty})
	}
	return out
}

// normalizeComponents materializa la qty ausente como 1 en el lado nuevo. Sin
// esto, un combo cuyo componente pasa de `qty` omitido a `"qty": 1` —el MISMO
// combo— aparecería como cambio de detalle.
func normalizeComponents(in []ImportComponent) []ImportComponent {
	if len(in) == 0 {
		return nil
	}
	out := make([]ImportComponent, 0, len(in))
	for _, c := range in {
		if c.Qty == 0 {
			c.Qty = 1
		}
		out = append(out, c)
	}
	return out
}

// sameDetails compara todo lo que NO es el precio del artículo. El orden de tags
// y variantes SÍ cuenta: es el orden en que se le enseñan al cliente, así que
// reordenarlas cambia lo que ve.
func sameDetails(a, b diffItem) bool {
	if a.label != b.label || a.code != b.code || a.description != b.description || a.subcategory != b.subcategory {
		return false
	}
	if !slices.Equal(a.tags, b.tags) {
		return false
	}
	if !sameAttributes(a.attributes, b.attributes) {
		return false
	}
	if !slices.Equal(a.variants, b.variants) {
		return false
	}
	return slices.Equal(a.components, b.components)
}

// sameAttributes compara dos mapas de atributos tratando nil y vacío como lo
// mismo (el JSON los produce indistintamente).
func sameAttributes(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		if vb, ok := b[k]; !ok || va != vb {
			return false
		}
	}
	return true
}

// sortedSKUs devuelve los skus de un lado en orden alfabético. Las listas del
// diff se ordenan SIEMPRE por sku, no por el orden del documento: el orden del
// archivo no significa nada para quien lee «qué cambia» y sí haría que dos
// corridas del mismo import se vieran distintas.
func sortedSKUs(items map[string]diffItem) []string {
	out := make([]string, 0, len(items))
	for sku := range items {
		out = append(out, sku)
	}
	slices.Sort(out)
	return out
}

// describeWarnings pasa a español llano los avisos del parseo tolerante del
// catálogo VIGENTE. Cada línea dice dónde estaba el defecto y termina recordando
// lo que de verdad importa: eso ya no está en la comparación.
func describeWarnings(ws []cart.CatalogWarning) []string {
	if len(ws) == 0 {
		return nil
	}
	out := make([]string, 0, len(ws))
	for _, w := range ws {
		var b strings.Builder
		b.WriteString("catálogo vigente")
		if w.Category != "" {
			b.WriteString(" · categoría " + strconv.Quote(w.Category))
		}
		if w.SKU != "" {
			b.WriteString(" · artículo " + strconv.Quote(w.SKU))
		}
		if w.Field != "" {
			b.WriteString(" · campo " + strconv.Quote(w.Field))
		}
		b.WriteString(": " + w.Reason + " (el motor ya lo ignoraba, así que no entra en la comparación de arriba)")
		out = append(out, b.String())
	}
	return out
}
