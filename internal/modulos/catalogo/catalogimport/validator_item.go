// Porta internal/catalogimport/validator.go @ 3c74b80 (el artículo y sus campos v2, líneas 493-744; partido de validator.go por E-13)

package catalogimport

import (
	"encoding/json"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo"
)

// ============================ artículo ============================

// validateItem valida un artículo entero. El orden de los campos es el orden en el
// que salen sus errores (Go evalúa los operandos de un literal de izquierda a
// derecha), y ese orden es lo que hace la lista comparable entre corridas.
func (v *validation) validateItem(cc *categoryCtx, j int, ri rawItem) ImportItem {
	l := atItem(cc.index, j)
	label, _ := v.optionalText(l, "el artículo "+strconv.Itoa(j+1)+" de "+cc.subject, "label", "el nombre", ri.Label)
	label = strings.TrimSpace(label)
	subject := itemSubject(j, label, cc.subject)
	if label == "" {
		v.c.at(l, "label", subject+" no tiene nombre: es lo que ve el cliente en la lista.")
	}

	out := ImportItem{
		Label:       label,
		Code:        v.itemCode(l, cc, j, subject, ri.Code),
		SKU:         v.itemSKU(l, subject, ri.SKU),
		Price:       v.requiredPrice(l, subject, "price", "el precio", ri.Price),
		Description: v.plainText(l, subject, "description", "la descripción", ri.Description),
		Subcategory: v.itemSubcategory(l, cc, subject, ri.Subcategory),
		Tags:        v.itemTags(l, subject, ri.Tags),
		Attributes:  v.itemAttributes(l, subject, ri.Attributes),
		Variants:    v.itemVariants(l, subject, ri.Variants),
		Components:  v.itemComponents(l, subject, ri.Components),
	}
	// variants XOR components (D-041.2). Se mira la PRESENCIA de los dos campos, no
	// el resultado de parsearlos: si uno de los dos venía mal formado ya tiene su
	// propio error, pero la incompatibilidad se declaró igual.
	if !isAbsent(ri.Variants) && !isAbsent(ri.Components) {
		v.c.at(l, "variants", subject+" declara variantes y componentes a la vez: o se vende en presentaciones (variants) o es un combo (components), no las dos cosas.")
	}
	return out
}

// itemCode valida el código con el que el cliente pide el artículo dentro de su
// categoría. Repetirlo no es un detalle cosmético: dos artículos con el mismo
// número hacen imposible saber cuál pidió.
func (v *validation) itemCode(l loc, cc *categoryCtx, j int, subject string, raw json.RawMessage) string {
	code := v.requiredText(l, subject, "code", "el código", raw)
	if code == "" {
		return ""
	}
	if prev, dup := cc.codes[code]; dup {
		v.c.at(l, "code", subject+": el código "+strconv.Quote(code)+" ya lo usa el artículo "+strconv.Itoa(prev+1)+" de la misma categoría; el cliente teclea ese número y no se sabría cuál de los dos pidió.")
		return code
	}
	cc.codes[code] = j
	return code
}

// itemSKU valida el identificador de negocio: obligatorio, único en TODO el
// catálogo y fuera del prefijo reservado del sistema.
//
// El alcance de la unicidad es GLOBAL, no por categoría, y el mensaje lo dice: el
// design se contradice (la regla de D-041.5 dice «único en el catálogo» y su
// ejemplo de motivo dice «repetido en la categoría %q»), y manda la regla. Por eso
// el motivo cita al artículo que se lo llevó primero CON SU categoría: si el
// choque es entre dos categorías distintas, mandar a buscar dentro de una sola es
// mandar a buscar donde no está.
func (v *validation) itemSKU(l loc, subject string, raw json.RawMessage) string {
	sku := v.requiredText(l, subject, "sku", "el sku", raw)
	if sku == "" {
		return ""
	}
	if strings.HasPrefix(sku, catalogo.SystemSKUPrefix) {
		v.c.at(l, "sku", subject+": el sku "+strconv.Quote(sku)+" empieza por "+strconv.Quote(catalogo.SystemSKUPrefix)+", que está reservado para las líneas que pone wApp (el envío, por ejemplo). Ponle otro.")
		return ""
	}
	if prev, dup := v.skus[sku]; dup {
		v.c.at(l, "sku", subject+": el sku "+strconv.Quote(sku)+" ya lo usa "+prev+"; el sku identifica al artículo en el pedido y tiene que ser único en TODO el catálogo, no solo dentro de su categoría.")
		return sku
	}
	v.skus[sku] = subject
	return sku
}

// itemSubcategory valida la referencia al segundo filtro: si apunta a algo que su
// categoría no declaró, promete un filtro que no existe.
func (v *validation) itemSubcategory(l loc, cc *categoryCtx, subject string, raw json.RawMessage) string {
	ref, ok := v.optionalText(l, subject, "subcategory", "la subcategoría", raw)
	ref = strings.TrimSpace(ref)
	if !ok || ref == "" {
		return ""
	}
	if !cc.subs[ref] {
		v.c.at(l, "subcategory", subject+": la subcategoría "+strconv.Quote(ref)+" no está declarada en su categoría; decláralas en \"subcategories\" o quita la referencia.")
		return ""
	}
	return ref
}

// itemTags valida las etiquetas informativas.
func (v *validation) itemTags(l loc, subject string, raw json.RawMessage) []string {
	if isAbsent(raw) {
		return nil
	}
	var tags []string
	if err := json.Unmarshal(raw, &tags); err != nil {
		v.c.at(l, "tags", subject+": las etiquetas deben ser una lista de textos, por ejemplo [\"sin_lactosa\", \"vegano\"].")
		return nil
	}
	out := make([]string, 0, len(tags))
	for k, tag := range tags {
		if strings.TrimSpace(tag) == "" {
			v.c.at(l, "tags["+strconv.Itoa(k)+"]", subject+": la etiqueta "+strconv.Itoa(k+1)+" está vacía.")
			continue
		}
		out = append(out, tag)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// itemAttributes valida los pares clave→valor de la ficha. Se aceptan números y
// booleanos y se guardan como texto: es exactamente lo que hace el runtime, y el
// validador no puede ser más estricto que el motor sin rechazar catálogos que
// funcionarían.
func (v *validation) itemAttributes(l loc, subject string, raw json.RawMessage) map[string]string {
	if isAbsent(raw) {
		return nil
	}
	var attrs map[string]any
	if err := json.Unmarshal(raw, &attrs); err != nil {
		v.c.at(l, "attributes", subject+": los atributos deben ser un objeto de pares clave→valor, por ejemplo {\"porciones\": \"10-12\"}.")
		return nil
	}
	out := make(map[string]string, len(attrs))
	for _, k := range slices.Sorted(maps.Keys(attrs)) { // orden estable: la lista de errores no debe bailar
		field := "attributes[" + strconv.Quote(k) + "]"
		if strings.TrimSpace(k) == "" {
			v.c.at(l, "attributes", subject+": hay un atributo sin nombre.")
			continue
		}
		s, ok := scalarToText(attrs[k])
		if !ok {
			v.c.at(l, field, subject+": el atributo "+strconv.Quote(k)+" debe ser un texto o un número, no una lista ni otro objeto.")
			continue
		}
		out[k] = s
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// itemVariants valida las presentaciones del artículo. El precio de cada una es
// obligatorio: una variante sin precio se vendería al de referencia del artículo,
// que es justo lo que el contrato prohíbe.
func (v *validation) itemVariants(l loc, subject string, raw json.RawMessage) []ImportVariant {
	if isAbsent(raw) {
		return nil
	}
	var vars []rawVariant
	if err := json.Unmarshal(raw, &vars); err != nil {
		v.c.at(l, "variants", subject+": las variantes deben ser una lista de {code, label, price}.")
		return nil
	}
	if len(vars) == 0 {
		v.c.at(l, "variants", subject+" trae la lista de variantes vacía: quítala o declara al menos una presentación.")
		return nil
	}
	out := make([]ImportVariant, 0, len(vars))
	seen := make(map[string]bool, len(vars))
	for k, rv := range vars {
		field := "variants[" + strconv.Itoa(k) + "]"
		vsubject := subject + ", variante " + strconv.Itoa(k+1)
		code := v.requiredText(l, vsubject, field+".code", "el código", rv.Code)
		label := v.requiredText(l, vsubject, field+".label", "el nombre", rv.Label)
		price := v.requiredPrice(l, vsubject, field+".price", "el precio", rv.Price)
		if code == "" {
			continue
		}
		if seen[code] {
			v.c.at(l, field+".code", vsubject+": el código "+strconv.Quote(code)+" ya lo usa otra variante del mismo artículo.")
			continue
		}
		seen[code] = true
		out = append(out, ImportVariant{Code: code, Label: label, Price: price})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// itemComponents valida los integrantes de un combo. Las referencias a otros skus
// se apuntan y se resuelven al final, cuando ya se conoce el catálogo entero: un
// componente puede nombrar un artículo declarado más abajo.
func (v *validation) itemComponents(l loc, subject string, raw json.RawMessage) []ImportComponent {
	if isAbsent(raw) {
		return nil
	}
	var comps []rawComponent
	if err := json.Unmarshal(raw, &comps); err != nil {
		v.c.at(l, "components", subject+": los componentes deben ser una lista de {sku, qty}.")
		return nil
	}
	if len(comps) == 0 {
		v.c.at(l, "components", subject+" trae la lista de componentes vacía: quítala o declara de qué se compone el combo.")
		return nil
	}
	out := make([]ImportComponent, 0, len(comps))
	for k, rc := range comps {
		field := "components[" + strconv.Itoa(k) + "]"
		csubject := subject + ", componente " + strconv.Itoa(k+1)
		sku := v.requiredText(l, csubject, field+".sku", "el sku", rc.SKU)
		qty := v.componentQty(l, csubject, field+".qty", rc.Qty)
		if sku == "" {
			continue
		}
		v.pending = append(v.pending, componentRef{l: l, field: field + ".sku", subject: csubject, sku: sku})
		out = append(out, ImportComponent{SKU: sku, Qty: qty})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// componentQty valida las unidades de un componente. Ausente vale 1 (lo mismo que
// asume el runtime); media unidad de un integrante de combo no significa nada.
func (v *validation) componentQty(l loc, subject, field string, raw json.RawMessage) int {
	if isAbsent(raw) {
		return 1
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil {
		v.c.at(l, field, subject+": la cantidad debe ser un número entero de 1 o más.")
		return 0
	}
	if f != math.Trunc(f) || f < 1 {
		v.c.at(l, field, subject+": la cantidad es "+trimNumber(f)+" y debe ser un número entero de 1 o más.")
		return 0
	}
	return int(f)
}

// resolveComponentRefs cierra la integridad de los combos contra el catálogo
// completo. Un componente que apunta a un sku inexistente no rompe la venta —el
// combo se cobra a su propio precio— pero sí rompe lo que el dueño y el puente
// leen del pedido, así que el import lo rechaza.
func (v *validation) resolveComponentRefs() {
	for _, ref := range v.pending {
		if _, ok := v.skus[ref.sku]; !ok {
			v.c.at(ref.l, ref.field, ref.subject+": el sku "+strconv.Quote(ref.sku)+" no existe en el catálogo; los componentes de un combo tienen que ser artículos declarados.")
		}
	}
}
