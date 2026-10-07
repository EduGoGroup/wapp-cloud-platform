// Porta internal/catalogimport/validator.go @ 3c74b80 (el cuerpo del documento: categorías y subcategorías, líneas 321-491; partido de validator.go por E-13)

package catalogimport

import (
	"encoding/json"
	"strconv"
	"strings"
)

// ============================ catálogo ============================

// validateCatalog valida el cuerpo: categorías, artículos y las referencias que
// los cruzan.
func (v *validation) validateCatalog(raw json.RawMessage) ImportBody {
	if isAbsent(raw) {
		v.c.at(header(), "catalog", "el documento no trae catálogo: falta el bloque \"catalog\" con sus categorías.")
		return ImportBody{}
	}
	var body rawBody
	if err := json.Unmarshal(raw, &body); err != nil {
		v.c.at(header(), "catalog", "el bloque \"catalog\" debe ser un objeto con la lista de categorías dentro.")
		return ImportBody{}
	}
	cats, ok := v.decodeCategories(body.Categories)
	if !ok {
		return ImportBody{}
	}
	// La preparación resuelve el nombre de cada categoría y su lista de artículos:
	// hace falta ANTES de validar para poder contar los artículos del documento
	// entero y para poder nombrar cada categoría en los mensajes.
	for i := range cats {
		v.prepareCategory(i, &cats[i])
	}
	if !v.withinItemLimit(cats) {
		return ImportBody{}
	}

	out := ImportBody{Categories: make([]ImportCategory, 0, len(cats))}
	for i := range cats {
		out.Categories = append(out.Categories, v.validateCategory(i, &cats[i]))
	}
	v.resolveComponentRefs()
	return out
}

// decodeCategories saca la lista de categorías. Un catálogo sin categorías no es
// un catálogo a medias: es una conversación que no puede empezar.
func (v *validation) decodeCategories(raw json.RawMessage) ([]rawCategory, bool) {
	if !isAbsent(raw) {
		var cats []rawCategory
		if err := json.Unmarshal(raw, &cats); err != nil {
			v.c.at(header(), "categories", "\"categories\" debe ser una lista de categorías, cada una con su código, su nombre y sus artículos.")
			return nil, false
		}
		if len(cats) > 0 {
			return cats, true
		}
	}
	v.c.at(header(), "categories", "el catálogo no trae ninguna categoría: agrega al menos una con sus artículos.")
	return nil, false
}

// prepareCategory resuelve código, nombre y lista de artículos de una categoría, y
// de paso anota los defectos de esos tres campos.
func (v *validation) prepareCategory(i int, rc *rawCategory) {
	l := atCategory(i)
	positional := "la categoría " + strconv.Itoa(i+1)
	label, _ := v.optionalText(l, positional, "label", "el nombre", rc.Label)
	code, _ := v.optionalText(l, positional, "code", "el código", rc.Code)
	rc.label, rc.code = strings.TrimSpace(label), strings.TrimSpace(code)
	rc.subject = categorySubject(i, rc.label, rc.code)

	if rc.label == "" {
		v.c.at(l, "label", rc.subject+" no tiene nombre: es lo que se le muestra al cliente al elegir.")
	}
	if rc.code == "" {
		v.c.at(l, "code", rc.subject+" no tiene código: es lo que teclea el cliente para entrar en ella.")
	}
	rc.items = v.decodeItems(l, rc.subject, rc.Items)
}

// decodeItems saca los artículos de una categoría. Una categoría sin artículos se
// rechaza: en la conversación es una puerta que el cliente abre para encontrar la
// nada.
func (v *validation) decodeItems(l loc, subject string, raw json.RawMessage) []rawItem {
	if !isAbsent(raw) {
		var items []rawItem
		if err := json.Unmarshal(raw, &items); err != nil {
			v.c.at(l, "items", subject+": \"items\" debe ser una lista de artículos.")
			return nil
		}
		if len(items) > 0 {
			return items
		}
	}
	v.c.at(l, "items", subject+" no tiene artículos: una categoría vacía deja al cliente en un callejón sin salida.")
	return nil
}

// withinItemLimit comprueba el tope de artículos del documento entero. Si se pasa,
// la validación se detiene ahí a propósito: seguir produciría miles de errores
// sobre un archivo que no se va a aceptar de todos modos.
func (v *validation) withinItemLimit(cats []rawCategory) bool {
	total := 0
	for i := range cats {
		total += len(cats[i].items)
	}
	if total > v.limits.MaxItems {
		v.c.at(header(), "catalog", "el catálogo trae "+strconv.Itoa(total)+" artículos y el máximo por importación es "+strconv.Itoa(v.limits.MaxItems)+": divide la carga en varios archivos.")
		return false
	}
	return true
}

// validateCategory valida una categoría ya preparada y sus artículos.
func (v *validation) validateCategory(i int, rc *rawCategory) ImportCategory {
	l := atCategory(i)
	if rc.code != "" {
		if prev, dup := v.catCodes[rc.code]; dup {
			v.c.at(l, "code", rc.subject+": el código "+strconv.Quote(rc.code)+" ya lo usa la categoría "+strconv.Itoa(prev+1)+"; el cliente teclea ese número y no se sabría a cuál quiere entrar.")
		} else {
			v.catCodes[rc.code] = i
		}
	}

	out := ImportCategory{
		Code:          rc.code,
		Label:         rc.label,
		Subcategories: v.validateSubcategories(l, rc),
	}
	cc := &categoryCtx{
		index:   i,
		subject: rc.subject,
		subs:    make(map[string]bool, len(out.Subcategories)),
		codes:   make(map[string]int, len(rc.items)),
	}
	for _, s := range out.Subcategories {
		cc.subs[s.Code] = true
	}
	out.Items = make([]ImportItem, 0, len(rc.items))
	for j := range rc.items {
		out.Items = append(out.Items, v.validateItem(cc, j, rc.items[j]))
	}
	return out
}

// validateSubcategories valida el segundo filtro OPCIONAL. Si viene, viene bien:
// una subcategoría sin código no se puede referenciar y una repetida hace ambigua
// la referencia.
func (v *validation) validateSubcategories(l loc, rc *rawCategory) []ImportSubcategory {
	if isAbsent(rc.Subcategories) {
		return nil
	}
	var subs []rawSubcategory
	if err := json.Unmarshal(rc.Subcategories, &subs); err != nil {
		v.c.at(l, "subcategories", rc.subject+": \"subcategories\" debe ser una lista de {code, label}.")
		return nil
	}
	out := make([]ImportSubcategory, 0, len(subs))
	seen := make(map[string]bool, len(subs))
	for k, rs := range subs {
		subject := rc.subject + ", subcategoría " + strconv.Itoa(k+1)
		field := "subcategories[" + strconv.Itoa(k) + "]"
		code := v.requiredText(l, subject, field+".code", "el código", rs.Code)
		label := v.requiredText(l, subject, field+".label", "el nombre", rs.Label)
		if code == "" {
			continue
		}
		if seen[code] {
			v.c.at(l, field+".code", subject+": el código "+strconv.Quote(code)+" ya lo usa otra subcategoría de la misma categoría.")
			continue
		}
		seen[code] = true
		out = append(out, ImportSubcategory{Code: code, Label: label})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
