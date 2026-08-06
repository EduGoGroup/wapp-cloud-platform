package catalogimport

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/cart"
)

// ErrDocumentTooLarge es el rechazo por tamaño: el documento supera
// Limits.MaxJSONBytes. Se decide leyendo, SIN deserializar (ver ReadLimited), así
// que un archivo de 50 MiB nunca se materializa en memoria.
var ErrDocumentTooLarge = errors.New("el documento de import excede el tamaño máximo permitido")

// maxImportErrors acota cuántos defectos se acumulan en una validación. Un
// documento roto de 500 artículos produciría miles de entradas: una respuesta
// inmanejable para la consola y una lista que nadie lee. Pasado el tope solo se
// dice cuántos quedaron sin detallar (mismo criterio que los avisos del parseo
// tolerante en cart).
const maxImportErrors = 200

// ReadLimited lee el documento crudo aplicando el techo de bytes ANTES de que
// nada se deserialice, que es el punto entero del requisito: si el límite se
// comprobara sobre el resultado del parseo, el documento absurdo ya estaría en
// memoria y el daño hecho.
//
// Lee como mucho MaxJSONBytes+1 bytes —el byte de más es lo que permite
// distinguir "justo en el límite" de "se pasó"— y devuelve ErrDocumentTooLarge en
// cuanto los supera, sin tocar encoding/json. El resto del cuerpo NO se consume:
// da igual que el cliente esté enviando gigabytes.
func ReadLimited(r io.Reader, limits Limits) ([]byte, error) {
	l := limits.normalized()
	body, err := io.ReadAll(io.LimitReader(r, l.MaxJSONBytes+1))
	if err != nil {
		return nil, fmt.Errorf("no se pudo leer el documento de import: %w", err)
	}
	if int64(len(body)) > l.MaxJSONBytes {
		return nil, fmt.Errorf("%w (máximo %d bytes)", ErrDocumentTooLarge, l.MaxJSONBytes)
	}
	return body, nil
}

// Validate deserializa y valida el documento en UNA sola pasada y devuelve TODOS
// sus defectos, no el primero: quien importa un catálogo desde una hoja de cálculo
// o desde un LLM no puede permitirse veinte viajes de ida y vuelta arreglando un
// error por vez.
//
// Devuelve el documento ya tipado y nil cuando es válido; el cero de CatalogImport
// y la lista completa de errores cuando no lo es.
func Validate(raw []byte, limits Limits) (CatalogImport, *ImportValidationError) {
	v := &validation{
		c:        &collector{},
		limits:   limits.normalized(),
		skus:     make(map[string]string),
		catCodes: make(map[string]int),
	}

	if len(bytes.TrimSpace(raw)) == 0 {
		v.c.at(header(), "documento", "el documento está vacío: pega el JSON del catálogo o sube el archivo.")
		return CatalogImport{}, v.c.result()
	}

	var d rawDocument
	if err := json.Unmarshal(raw, &d); err != nil {
		v.c.at(header(), "documento", malformedReason(raw, err))
		return CatalogImport{}, v.c.result()
	}

	doc := CatalogImport{
		Format:  v.validateFormat(d.Format),
		Version: v.validateVersion(d.Version),
	}
	// Contrato desconocido ⇒ no se sigue. Aplicar las reglas de la v1 a un
	// documento que dice ser otra cosa produciría una lista de errores inventados
	// sobre campos que en SU contrato quizá ni existen.
	if v.c.any() {
		return CatalogImport{}, v.c.result()
	}

	doc.Source = v.validateSource(d.Source)
	doc.Catalog = v.validateCatalog(d.Catalog)
	if err := v.c.result(); err != nil {
		return CatalogImport{}, err
	}
	return doc, nil
}

// ============================ acumulación y ubicación ============================

// loc es la ubicación de un defecto dentro del documento. Los dos punteros nil
// son la cabecera (format, version, límites).
type loc struct {
	category *int
	item     *int
}

// header ubica un defecto en la cabecera del documento.
func header() loc { return loc{} }

// atCategory ubica un defecto en una categoría (i es su posición, base 0).
func atCategory(i int) loc { return loc{category: &i} }

// atItem ubica un defecto en un artículo (j es su posición dentro de la categoría
// i, base 0).
func atItem(i, j int) loc { return loc{category: &i, item: &j} }

// collector acumula los defectos con tope.
type collector struct {
	errs    []ImportFieldError
	dropped int
}

// at anota un defecto en una ubicación.
func (c *collector) at(l loc, field, reason string) {
	if len(c.errs) >= maxImportErrors {
		c.dropped++
		return
	}
	c.errs = append(c.errs, ImportFieldError{
		CategoryIndex: l.category,
		ItemIndex:     l.item,
		Field:         field,
		Reason:        reason,
	})
}

// any indica si se acumuló algún defecto.
func (c *collector) any() bool { return len(c.errs) > 0 || c.dropped > 0 }

// result cierra la acumulación: ordena los defectos por su posición en el
// documento (cabecera primero, luego categoría y artículo) y devuelve nil si no
// hubo ninguno. El orden es estable dentro de cada elemento, así que los campos de
// un mismo artículo salen siempre en el mismo orden: una lista que baila entre
// corridas no se puede comparar en un test ni leer con confianza en pantalla.
func (c *collector) result() *ImportValidationError {
	if !c.any() {
		return nil
	}
	slices.SortStableFunc(c.errs, func(a, b ImportFieldError) int {
		if n := cmp.Compare(position(a.CategoryIndex), position(b.CategoryIndex)); n != 0 {
			return n
		}
		return cmp.Compare(position(a.ItemIndex), position(b.ItemIndex))
	})
	if c.dropped > 0 {
		c.errs = append(c.errs, ImportFieldError{
			Field:  "(varios)",
			Reason: "se omitieron " + strconv.Itoa(c.dropped) + " problemas más: arregla los de arriba y vuelve a subir el archivo para ver el resto.",
		})
	}
	return &ImportValidationError{Errors: c.errs}
}

// position convierte un índice opcional en un entero ordenable: la cabecera (nil)
// va antes que cualquier posición.
func position(p *int) int {
	if p == nil {
		return -1
	}
	return *p
}

// ============================ forma cruda del documento ============================

// El documento se decodifica sobre json.RawMessage campo a campo, no sobre los
// structs del contrato, y no es un rodeo: con tipos directos, UN solo campo con el
// tipo equivocado —un `"price": "18000"`, la salida más típica de un LLM— aborta
// el Unmarshal del documento ENTERO y el usuario recibe un único error de
// encoding/json, en inglés y sin decir en qué artículo está. Decodificando campo a
// campo, ese mismo defecto se convierte en un error localizado más de la lista y
// la validación sigue con el resto.
type rawDocument struct {
	Format  json.RawMessage `json:"format"`
	Version json.RawMessage `json:"version"`
	Source  json.RawMessage `json:"source"`
	Catalog json.RawMessage `json:"catalog"`
}

type rawBody struct {
	Categories json.RawMessage `json:"categories"`
}

type rawCategory struct {
	Code          json.RawMessage `json:"code"`
	Label         json.RawMessage `json:"label"`
	Subcategories json.RawMessage `json:"subcategories"`
	Items         json.RawMessage `json:"items"`

	// Campos resueltos en la pasada de preparación (no vienen del JSON: los
	// minúsculos los ignora encoding/json).
	code    string
	label   string
	subject string
	items   []rawItem
}

type rawItem struct {
	Code        json.RawMessage `json:"code"`
	SKU         json.RawMessage `json:"sku"`
	Label       json.RawMessage `json:"label"`
	Price       json.RawMessage `json:"price"`
	Description json.RawMessage `json:"description"`
	Subcategory json.RawMessage `json:"subcategory"`
	Tags        json.RawMessage `json:"tags"`
	Attributes  json.RawMessage `json:"attributes"`
	Variants    json.RawMessage `json:"variants"`
	Components  json.RawMessage `json:"components"`
}

type rawSubcategory struct {
	Code  json.RawMessage `json:"code"`
	Label json.RawMessage `json:"label"`
}

type rawVariant struct {
	Code  json.RawMessage `json:"code"`
	Label json.RawMessage `json:"label"`
	Price json.RawMessage `json:"price"`
}

type rawComponent struct {
	SKU json.RawMessage `json:"sku"`
	Qty json.RawMessage `json:"qty"`
}

// validation es el estado de UNA validación: los defectos acumulados, los topes,
// los skus ya vistos (unicidad global) y las referencias de combo pendientes de
// resolver contra el catálogo completo.
type validation struct {
	c        *collector
	limits   Limits
	skus     map[string]string // sku → prosa del artículo que lo usó primero
	catCodes map[string]int    // code de categoría → posición donde se vio primero
	pending  []componentRef
}

// componentRef es una referencia de combo a la espera de que se conozca el
// catálogo entero: un componente puede apuntar a un artículo declarado más
// adelante, así que la integridad no se puede decidir en el momento de leerlo.
type componentRef struct {
	l       loc
	field   string
	subject string
	sku     string
}

// categoryCtx es lo que una categoría le presta a sus artículos mientras se
// validan: cómo llamarla en los mensajes, qué subcategorías declaró y qué códigos
// de artículo lleva usados.
type categoryCtx struct {
	index   int
	subject string
	subs    map[string]bool
	codes   map[string]int
}

// ============================ cabecera ============================

// validateFormat exige la marca del contrato. Sin ella no hay forma de distinguir
// un catálogo de cualquier otro JSON que alguien pegue por error.
func (v *validation) validateFormat(raw json.RawMessage) string {
	if isAbsent(raw) {
		v.c.at(header(), "format", "el archivo no dice qué es: le falta la línea \"format\": "+strconv.Quote(ImportFormat)+". Descarga la plantilla y parte de ella.")
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		v.c.at(header(), "format", "el campo \"format\" debe ser un texto entre comillas: "+strconv.Quote(ImportFormat)+".")
		return ""
	}
	if s != ImportFormat {
		v.c.at(header(), "format", "el archivo dice ser "+strconv.Quote(s)+" y aquí solo se importan catálogos ("+strconv.Quote(ImportFormat)+"): comprueba que no te hayas equivocado de archivo.")
	}
	return s
}

// validateVersion exige la versión del contrato. Que viva junto al validador es lo
// que permite rechazar una plantilla vieja con un mensaje claro en vez de
// importarla a medias.
func (v *validation) validateVersion(raw json.RawMessage) int {
	if isAbsent(raw) {
		v.c.at(header(), "version", "el archivo no dice de qué versión es: le falta la línea \"version\": "+strconv.Itoa(ImportVersion)+".")
		return 0
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil {
		v.c.at(header(), "version", "el campo \"version\" debe ser un número: "+strconv.Itoa(ImportVersion)+".")
		return 0
	}
	n := int(f)
	if f != math.Trunc(f) || n != ImportVersion {
		v.c.at(header(), "version", "el archivo es de la versión "+trimNumber(f)+" y esta consola entiende la versión "+strconv.Itoa(ImportVersion)+": vuelve a descargar la plantilla y genera el catálogo con ella.")
	}
	return n
}

// validateSource acepta la procedencia declarada. Es informativa: no se
// interpreta, solo se exige que tenga la forma de un objeto para que no se cuele
// un texto suelto donde va un bloque.
func (v *validation) validateSource(raw json.RawMessage) *ImportSource {
	if isAbsent(raw) {
		return nil
	}
	var s ImportSource
	if err := json.Unmarshal(raw, &s); err != nil {
		v.c.at(header(), "source", "el bloque \"source\" debe ser un objeto con kind, model y hint (textos), o no venir.")
		return nil
	}
	return &s
}

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
	if strings.HasPrefix(sku, cart.SystemSKUPrefix) {
		v.c.at(l, "sku", subject+": el sku "+strconv.Quote(sku)+" empieza por "+strconv.Quote(cart.SystemSKUPrefix)+", que está reservado para las líneas que pone wApp (el envío, por ejemplo). Ponle otro.")
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

// ============================ decodificadores de campo ============================

// requiredText decodifica un campo textual obligatorio.
func (v *validation) requiredText(l loc, subject, field, human string, raw json.RawMessage) string {
	s, ok := v.optionalText(l, subject, field, human, raw)
	if !ok {
		return ""
	}
	if strings.TrimSpace(s) == "" {
		v.c.at(l, field, subject+" no tiene "+human+": es obligatorio.")
		return ""
	}
	return s
}

// optionalText decodifica un campo textual opcional. ok=false significa que el
// campo vino con otro tipo (el defecto ya quedó anotado); ausente devuelve la
// cadena vacía con ok=true.
func (v *validation) optionalText(l loc, subject, field, human string, raw json.RawMessage) (string, bool) {
	if isAbsent(raw) {
		return "", true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		v.c.at(l, field, subject+": "+human+" debe ir entre comillas (es un texto, no un número ni una lista).")
		return "", false
	}
	return s, true
}

// plainText decodifica un campo textual opcional del que solo importa el valor.
func (v *validation) plainText(l loc, subject, field, human string, raw json.RawMessage) string {
	s, _ := v.optionalText(l, subject, field, human, raw)
	return s
}

// requiredPrice decodifica un importe obligatorio. El mensaje del tipo equivocado
// nombra el error real que se ve en la práctica: el precio escrito como texto, con
// símbolo de moneda o separador de miles, que es la salida típica de un LLM y de
// una hoja de cálculo.
func (v *validation) requiredPrice(l loc, subject, field, human string, raw json.RawMessage) float64 {
	if isAbsent(raw) {
		v.c.at(l, field, subject+" no tiene "+human+": es obligatorio.")
		return 0
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil {
		v.c.at(l, field, subject+": "+human+" debe ser un número, sin comillas, sin símbolo de moneda y sin separadores de miles (18000, no \"$18.000\").")
		return 0
	}
	if f < 0 {
		v.c.at(l, field, subject+": "+human+" no puede ser negativo.")
		return 0
	}
	return f
}

// ============================ utilidades ============================

// isAbsent indica si un campo no vino en el JSON (o vino como null, que para el
// contrato es lo mismo: no hay valor).
func isAbsent(raw json.RawMessage) bool {
	return len(raw) == 0 || string(raw) == "null"
}

// categorySubject nombra una categoría en los mensajes: por su nombre si lo tiene,
// por su código si no, y por su posición si le falta todo.
func categorySubject(index int, label, code string) string {
	switch {
	case label != "":
		return "la categoría " + strconv.Quote(label)
	case code != "":
		return "la categoría con código " + strconv.Quote(code)
	default:
		return "la categoría " + strconv.Itoa(index+1)
	}
}

// itemSubject nombra un artículo en los mensajes. La posición va SIEMPRE —es lo
// único que no puede faltar y lo que ata la frase con los índices de la
// respuesta— y el nombre se añade cuando existe.
func itemSubject(index int, label, categorySubj string) string {
	s := "el artículo " + strconv.Itoa(index+1)
	if label != "" {
		s += " (" + strconv.Quote(label) + ")"
	}
	return s + " de " + categorySubj
}

// scalarToText convierte a texto un valor simple de JSON. Es la MISMA conversión
// que hace el parseo del runtime al poblar los atributos: si divergieran, un
// documento válido produciría un catálogo distinto del que el motor lee.
func scalarToText(value any) (string, bool) {
	switch t := value.(type) {
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

// trimNumber escribe un número como lo escribiría una persona (sin ceros de
// relleno) para citarlo en un mensaje.
func trimNumber(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// malformedReason traduce el fallo de encoding/json a algo que un dueño de negocio
// pueda accionar: dónde mirar, no qué token esperaba el parser.
func malformedReason(raw []byte, err error) string {
	var se *json.SyntaxError
	if errors.As(err, &se) {
		return "el archivo no es un JSON válido: hay un error de escritura hacia la línea " + strconv.Itoa(lineAt(raw, se.Offset)) + ". Revisa que no falte una coma, una comilla o una llave."
	}
	return "el archivo debe ser un objeto JSON con \"format\", \"version\" y \"catalog\": lo que llegó no tiene esa forma."
}

// lineAt devuelve la línea (base 1) en la que cae un desplazamiento en bytes.
func lineAt(raw []byte, offset int64) int {
	if offset < 0 {
		offset = 0
	}
	if offset > int64(len(raw)) {
		offset = int64(len(raw))
	}
	return 1 + bytes.Count(raw[:offset], []byte{'\n'})
}
