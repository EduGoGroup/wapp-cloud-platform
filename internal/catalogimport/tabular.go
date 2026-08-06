package catalogimport

import (
	"cmp"
	"encoding/json"
	"math"
	"slices"
	"strconv"
	"strings"
)

// ============================ lectura de la planilla (D-041.9) ============================

// Este fichero es la mitad que LEE del contrato tabular: template.go emite las
// filas de la planilla canónica y ParseTabular las devuelve al documento del
// contrato. El nombre de las columnas y los separadores de las celdas múltiples
// están declarados UNA vez —las constantes de aquí, la lista de template.go— porque
// dos copias del literal se desincronizan al primer retoque y el síntoma sería una
// planilla que el propio wApp no sabe releer.
//
// LA PLANILLA NO TIENE VALIDADOR PROPIO, y esa es la decisión que gobierna todo lo
// que sigue: este parser traduce filas a CatalogImport y se lo entrega al MISMO
// Validate del JSON. Si la planilla juzgara por su cuenta habría dos definiciones de
// «catálogo correcto» y una de las dos envejecería sin que nadie se enterase.
//
// LO ÚNICO QUE AÑADE EL CAMINO TABULAR ES DÓNDE ESTÁ EL DEFECTO. En una hoja de
// cálculo se busca por FILA: «la categoría 2, artículo 3» es una coordenada del JSON
// que quien llenó la planilla no ha visto nunca. Por eso los defectos que devuelve
// el validador se re-ubican en la fila que produjo cada elemento antes de salir
// (localize), y por eso ImportFieldError tiene Row.

// Nombres de las columnas de la planilla canónica. Los compone tabularColumns
// (template.go), que es donde está fijado su ORDEN —contrato, porque las columnas
// son posicionales para quien las llena—; aquí solo viven los nombres, que es lo que
// el parser compara al leer la cabecera.
const (
	colCategoria    = "categoria"
	colSubcategoria = "subcategoria"
	colCodigo       = "codigo"
	colSKU          = "sku"
	colNombre       = "nombre"
	colPrecio       = "precio"
	colDescripcion  = "descripcion"
	colTags         = "tags"
	colAtributos    = "atributos"
	colVariantes    = "variantes"
	colComponentes  = "componentes"
)

// tabularRequired son las columnas SIN LAS QUE NO HAY ARTÍCULO. Las demás pueden
// faltar en la hoja: quien no vende nada con variantes borra esa columna, y
// rechazarle la planilla por eso sería rechazársela por algo que no es del negocio.
// Que la lectura vaya por NOMBRE y no por posición es lo que permite esta holgura
// sin ambigüedad: una columna ausente es una columna vacía, no un corrimiento.
var tabularRequired = []string{colCategoria, colCodigo, colSKU, colNombre, colPrecio}

// tabularColumnOf traduce el campo del contrato (inglés, como en el JSON) a la
// columna de la planilla, para que un defecto que el validador ubica en "price"
// señale la columna «precio» que el dueño tiene delante. Lo que no está aquí no
// tiene columna (format, version, catalog) y se devuelve tal cual.
var tabularColumnOf = map[string]string{
	"code":        colCodigo,
	"sku":         colSKU,
	"label":       colNombre,
	"price":       colPrecio,
	"description": colDescripcion,
	"subcategory": colSubcategoria,
	"tags":        colTags,
	"attributes":  colAtributos,
	"variants":    colVariantes,
	"components":  colComponentes,
}

// tabularSourceKind es la procedencia que se anota en el documento leído de una
// planilla. Es informativa (el validador no la interpreta) y no viaja al blob: sirve
// para que después se sepa que ese catálogo entró por una hoja de cálculo.
const tabularSourceKind = "planilla"

// ParseTabular traduce las filas de la planilla canónica al documento del contrato y
// lo valida con el MISMO validador del JSON. La primera fila es la CABECERA (los
// nombres de las columnas) y cada fila siguiente es UN artículo con la cabecera de su
// categoría repetida, que es como se filtra y se ordena en una hoja sin cruzar
// pestañas.
//
// Los defectos salen ubicados por FILA, en el número que la hoja enseña en su margen
// izquierdo: la cabecera es la 1 y el primer artículo, la 2.
//
// LEER Y JUZGAR SON DOS PASADAS Y LA SEGUNDA NO SE HACE SI LA PRIMERA ENCUENTRA
// ALGO: no se puede opinar sobre lo que dice un documento que no se ha podido leer, y
// hacerlo produciría defectos inventados —un combo cuyo integrante «no existe» solo
// porque la fila que lo declaraba tenía la celda de categoría rota—. Dentro de cada
// pasada sí se acumula TODO (el mismo criterio de Validate): quien llena una planilla
// no puede permitirse veinte viajes arreglando un defecto por vez.
func ParseTabular(rows [][]string, limits Limits) (CatalogImport, *ImportValidationError) {
	p := &tabular{
		c:      &collector{},
		limits: limits.normalized(),
		byCode: make(map[string]*tabularCategory),
	}
	if !p.readHeader(rows) || !p.withinItemLimit(rows) {
		return CatalogImport{}, p.c.result()
	}
	for i, row := range rows[1:] {
		p.readRow(i+2, row) // +2: la cabecera es la fila 1 y este bucle empieza en la 2
	}
	if p.c.any() {
		return CatalogImport{}, p.c.result()
	}

	raw, err := json.Marshal(p.document())
	if err != nil {
		p.c.atRow(0, "planilla", "no se pudo interpretar la planilla; vuelve a intentarlo partiendo de la plantilla descargada.")
		return CatalogImport{}, p.c.result()
	}
	doc, verr := Validate(raw, p.limits)
	if verr != nil {
		return CatalogImport{}, p.localize(verr)
	}
	return doc, nil
}

// tabular es el estado de UNA lectura de planilla: los defectos acumulados, los
// topes, dónde cayó cada columna en la cabecera y las categorías que se van armando.
type tabular struct {
	c      *collector
	limits Limits
	cols   map[string]int // nombre de columna → posición en la fila
	order  []*tabularCategory
	byCode map[string]*tabularCategory
}

// tabularCategory acumula lo que una categoría reúne a lo largo de las filas que la
// nombran: su nombre, sus subcategorías —que en la planilla se declaran EN LAS
// CELDAS, no en una tabla aparte— y sus artículos.
//
// Guarda la FILA de la que salió cada cosa, y no es contabilidad ociosa: es lo único
// que permite devolver a su sitio un defecto que el validador ubica en «la categoría
// 2, artículo 3».
type tabularCategory struct {
	code     string
	label    string
	row      int
	subs     map[string]tabularSub
	subOrder []string
	items    []ImportItem
	itemRows []int
}

// tabularSub es una subcategoría declarada en una celda, con la fila que la declaró
// primero (para poder señalar las dos cuando dos filas la llaman distinto).
type tabularSub struct {
	label string
	row   int
}

// ============================ cabecera y topes ============================

// readHeader localiza las columnas por su NOMBRE. Por nombre y no por posición
// porque la hoja la abre una persona en Excel: mover una columna de sitio no puede
// romper el import, y en cambio una columna que falta sí tiene que decirse.
func (p *tabular) readHeader(rows [][]string) bool {
	if len(rows) == 0 {
		p.c.atRow(0, "planilla", "la planilla está vacía: descarga la plantilla, llénala y vuelve a subirla.")
		return false
	}
	p.cols = make(map[string]int, len(tabularColumns))
	for i, cell := range rows[0] {
		name := normalizeHeader(cell)
		if _, seen := p.cols[name]; name == "" || seen {
			continue // columna sin nombre, o repetida: manda la primera que apareció
		}
		p.cols[name] = i
	}

	missing := make([]string, 0, len(tabularRequired))
	for _, want := range tabularRequired {
		if _, ok := p.cols[want]; !ok {
			missing = append(missing, want)
		}
	}
	if len(missing) > 0 {
		p.c.atRow(1, "cabecera", "a la primera fila le faltan columnas ("+strings.Join(missing, ", ")+"): "+
			"la planilla se lee por el NOMBRE de sus columnas y sin esas no hay artículo que valga. "+
			"Descarga la plantilla y parte de ella.")
		return false
	}
	if len(rows) == 1 {
		p.c.atRow(1, "planilla", "la planilla solo trae la cabecera: escribe debajo un artículo por fila.")
		return false
	}
	return true
}

// normalizeHeader deja el nombre de una columna en la forma en la que se compara: sin
// espacios, sin BOM, en minúsculas y sin tildes.
//
// Excel autocapitaliza («Categoria») y una persona escribe «descripción» con su
// tilde. Rechazar la planilla por eso sería rechazarla por algo que no es del
// negocio; lo que NO se adivina es una columna que se llama de otra manera.
func normalizeHeader(cell string) string {
	s := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(cell, "\ufeff")))
	return headerAccents.Replace(s)
}

// headerAccents quita las tildes de la cabecera (solo de la cabecera: en los datos
// las tildes son del negocio y se respetan tal cual).
var headerAccents = strings.NewReplacer(
	"á", "a", "é", "e", "í", "i", "ó", "o", "ú", "u", "ü", "u", "ñ", "n",
)

// withinItemLimit comprueba el tope de artículos ANTES de armar nada. Adelantarlo a
// la lectura no es duplicar el que ya hace el validador: es no construir en memoria
// un documento de cien mil filas para acabar rechazándolo.
func (p *tabular) withinItemLimit(rows [][]string) bool {
	n := 0
	for _, row := range rows[1:] {
		if !blankRow(row) {
			n++
		}
	}
	if n > p.limits.MaxItems {
		p.c.atRow(0, "planilla", "la planilla trae "+strconv.Itoa(n)+" artículos y el máximo por importación es "+
			strconv.Itoa(p.limits.MaxItems)+": divide la carga en varias planillas.")
		return false
	}
	return true
}

// ============================ filas ============================

// readRow convierte UNA fila en un artículo de su categoría.
func (p *tabular) readRow(n int, row []string) {
	if blankRow(row) {
		return // una hoja de cálculo está llena de filas vacías; no son un defecto
	}
	label := p.cell(row, colNombre)
	subject := rowSubject(n, label)
	cat := p.category(n, row)
	if cat == nil {
		return // sin categoría legible no hay dónde colgar el artículo
	}
	cat.items = append(cat.items, ImportItem{
		Code:        p.cell(row, colCodigo),
		SKU:         p.cell(row, colSKU),
		Label:       label,
		Price:       p.price(n, colPrecio, subject, p.cell(row, colPrecio)),
		Description: p.cell(row, colDescripcion),
		Subcategory: p.subcategory(n, cat, row),
		Tags:        p.tags(n, subject, p.cell(row, colTags)),
		Attributes:  p.attributes(n, subject, p.cell(row, colAtributos)),
		Variants:    p.variants(n, subject, p.cell(row, colVariantes)),
		Components:  p.components(n, subject, p.cell(row, colComponentes)),
	})
	cat.itemRows = append(cat.itemRows, n)
}

// cell devuelve el contenido de una columna de la fila, sin espacios alrededor. Una
// columna que no está en la cabecera —o una fila que se acaba antes, que es lo que
// pasa cuando alguien borra las celdas vacías del final— vale cadena vacía.
func (p *tabular) cell(row []string, column string) string {
	i, ok := p.cols[column]
	if !ok || i >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[i])
}

// category resuelve a qué categoría pertenece la fila, creándola la primera vez que
// se la nombra. Devuelve nil si la celda no se puede leer.
func (p *tabular) category(n int, row []string) *tabularCategory {
	code, label, ok := p.codeLabel(n, colCategoria, "categoría", p.cell(row, colCategoria))
	if !ok {
		return nil
	}
	cat, seen := p.byCode[code]
	if !seen {
		cat = &tabularCategory{code: code, label: label, row: n, subs: make(map[string]tabularSub)}
		p.byCode[code] = cat
		p.order = append(p.order, cat)
		return cat
	}
	if cat.label != label {
		p.c.atRow(n, colCategoria, "la fila "+strconv.Itoa(n)+" llama "+strconv.Quote(label)+" a la categoría "+
			strconv.Quote(code)+" y la fila "+strconv.Itoa(cat.row)+" la llamó "+strconv.Quote(cat.label)+
			": una categoría tiene un solo nombre, escríbelo igual en todas sus filas.")
	}
	return cat
}

// subcategory resuelve el segundo filtro, que es OPCIONAL. La subcategoría queda
// declarada por el hecho de escribirla en una celda: en la planilla no hay una tabla
// aparte donde declararlas, y exigir una la convertiría en un enigma.
func (p *tabular) subcategory(n int, cat *tabularCategory, row []string) string {
	cell := p.cell(row, colSubcategoria)
	if cell == "" {
		return ""
	}
	code, label, ok := p.codeLabel(n, colSubcategoria, "subcategoría", cell)
	if !ok {
		return ""
	}
	prev, seen := cat.subs[code]
	if !seen {
		cat.subs[code] = tabularSub{label: label, row: n}
		cat.subOrder = append(cat.subOrder, code)
		return code
	}
	if prev.label != label {
		p.c.atRow(n, colSubcategoria, "la fila "+strconv.Itoa(n)+" llama "+strconv.Quote(label)+" a la subcategoría "+
			strconv.Quote(code)+" y la fila "+strconv.Itoa(prev.row)+" la llamó "+strconv.Quote(prev.label)+
			": una subcategoría tiene un solo nombre, escríbelo igual en todas sus filas.")
	}
	return code
}

// codeLabel deshace una celda `codigo|nombre` (categoría o subcategoría).
//
// EL CÓDIGO VA EXPLÍCITO EN LA CELDA y no se deduce del orden de las filas, que es
// la misma decisión que toma el emisor (template.go, codeLabelCell) y por el mismo
// motivo: el código de una categoría es LO QUE EL CLIENTE TECLEA en WhatsApp para
// entrar en ella, y numerarlas por posición se los cambiaría cada vez que el dueño
// reordena su hoja.
func (p *tabular) codeLabel(n int, column, human, cell string) (code, label string, ok bool) {
	if cell == "" {
		p.c.atRow(n, column, "la fila "+strconv.Itoa(n)+" no dice a qué "+human+" pertenece: escribe su código y su "+
			"nombre separados por «|», por ejemplo «1|Tortas».")
		return "", "", false
	}
	fields := splitFields(cell)
	if len(fields) != 2 || fields[0] == "" || fields[1] == "" {
		p.c.atRow(n, column, "la fila "+strconv.Itoa(n)+": la "+human+" "+strconv.Quote(cell)+
			" se escribe «codigo|nombre», por ejemplo «1|Tortas».")
		return "", "", false
	}
	return fields[0], fields[1], true
}

// ============================ celdas múltiples ============================

// tags lee las etiquetas: entradas de un solo campo separadas por «;».
func (p *tabular) tags(n int, subject, cell string) []string {
	entries := splitEntries(cell)
	if len(entries) == 0 {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.Contains(e, TabularFieldSeparator) {
			p.c.atRow(n, colTags, subject+": la etiqueta "+strconv.Quote(e)+" lleva «|»; las etiquetas se escriben "+
				"una detrás de otra separadas por «;», por ejemplo «decorada; sin_lactosa».")
			continue
		}
		out = append(out, e)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// attributes lee los pares clave|valor de la ficha.
func (p *tabular) attributes(n int, subject, cell string) map[string]string {
	entries := splitEntries(cell)
	if len(entries) == 0 {
		return nil
	}
	out := make(map[string]string, len(entries))
	for _, e := range entries {
		fields := splitFields(e)
		if len(fields) != 2 || fields[0] == "" || fields[1] == "" {
			p.c.atRow(n, colAtributos, subject+": el atributo "+strconv.Quote(e)+
				" se escribe «clave|valor», por ejemplo «porciones|10-12».")
			continue
		}
		out[fields[0]] = fields[1]
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// variants lee las presentaciones: `codigo|nombre|precio` por entrada.
func (p *tabular) variants(n int, subject, cell string) []ImportVariant {
	entries := splitEntries(cell)
	if len(entries) == 0 {
		return nil
	}
	out := make([]ImportVariant, 0, len(entries))
	for _, e := range entries {
		fields := splitFields(e)
		if len(fields) != 3 || fields[0] == "" || fields[1] == "" {
			p.c.atRow(n, colVariantes, subject+": la variante "+strconv.Quote(e)+
				" se escribe «codigo|nombre|precio», por ejemplo «V1|10-12 porciones|18000».")
			continue
		}
		out = append(out, ImportVariant{
			Code:  fields[0],
			Label: fields[1],
			Price: p.price(n, colVariantes, subject+", variante "+strconv.Quote(fields[0]), fields[2]),
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// components lee los integrantes del combo: `sku|cantidad` por entrada.
//
// LA CANTIDAD SE EXIGE, también cuando vale 1. En el JSON es omitible porque el
// contrato dice que ausente vale 1, pero la planilla la escribe SIEMPRE (template.go,
// componentsCell) y aceptar además el sku a secas sería tener dos gramáticas para lo
// mismo: la que emitimos y otra que solo conoce el parser.
func (p *tabular) components(n int, subject, cell string) []ImportComponent {
	entries := splitEntries(cell)
	if len(entries) == 0 {
		return nil
	}
	out := make([]ImportComponent, 0, len(entries))
	for _, e := range entries {
		fields := splitFields(e)
		if len(fields) != 2 || fields[0] == "" {
			p.c.atRow(n, colComponentes, subject+": el componente "+strconv.Quote(e)+
				" se escribe «sku|cantidad», por ejemplo «TEQUENOS-15|1».")
			continue
		}
		qty, err := strconv.Atoi(fields[1])
		if err != nil || qty < 1 {
			p.c.atRow(n, colComponentes, subject+", componente "+strconv.Quote(fields[0])+": la cantidad "+
				strconv.Quote(fields[1])+" debe ser un número entero de 1 o más.")
			continue
		}
		out = append(out, ImportComponent{SKU: fields[0], Qty: qty})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// price lee un importe de una celda. Es el ÚNICO campo que la lectura tabular juzga
// por su cuenta antes del validador, y no por gusto: en el documento el precio es un
// número, así que una celda ilegible no tiene forma de llegar al validador como
// «precio malo» —llegaría como 0, que es un precio válido— y el artículo se
// importaría regalado.
func (p *tabular) price(n int, column, subject, cell string) float64 {
	if cell == "" {
		p.c.atRow(n, column, subject+" no tiene el precio: es obligatorio.")
		return 0
	}
	f, err := strconv.ParseFloat(cell, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		p.c.atRow(n, column, subject+": el precio "+strconv.Quote(cell)+" no es un número; escríbelo sin símbolo "+
			"de moneda y sin separadores de miles (18000, no \"$18.000\").")
		return 0
	}
	return f
}

// ============================ documento y ubicación ============================

// document arma el documento del contrato con lo leído. Va con su format y su
// version puestos por nosotros: quien llena una planilla no tiene dónde escribirlos y
// no se le va a pedir que los sepa.
func (p *tabular) document() CatalogImport {
	cats := make([]ImportCategory, 0, len(p.order))
	for _, c := range p.order {
		subs := make([]ImportSubcategory, 0, len(c.subOrder))
		for _, code := range c.subOrder {
			subs = append(subs, ImportSubcategory{Code: code, Label: c.subs[code].label})
		}
		cats = append(cats, ImportCategory{
			Code:          c.code,
			Label:         c.label,
			Subcategories: subs,
			Items:         c.items,
		})
	}
	return CatalogImport{
		Format:  ImportFormat,
		Version: ImportVersion,
		Source:  &ImportSource{Kind: tabularSourceKind},
		Catalog: ImportBody{Categories: cats},
	}
}

// localize devuelve a su fila los defectos que el validador ubicó por índices, y
// borra los índices: en el camino tabular son una coordenada de un documento que el
// usuario no ha visto, y dejar las dos ubicaciones sería hablarle en dos idiomas de
// los que solo entiende uno.
//
// Ordena por fila porque es el orden en el que la persona va a recorrer su hoja; el
// del validador (categoría, artículo) no coincide con el de las filas en cuanto
// alguien intercala en su planilla una fila de otra categoría.
func (p *tabular) localize(verr *ImportValidationError) *ImportValidationError {
	for i := range verr.Errors {
		e := &verr.Errors[i]
		e.Row = p.rowOf(e.CategoryIndex, e.ItemIndex)
		e.Field = tabularColumn(*e)
		e.CategoryIndex, e.ItemIndex = nil, nil
	}
	slices.SortStableFunc(verr.Errors, func(a, b ImportFieldError) int {
		return cmp.Compare(a.Row, b.Row)
	})
	return verr
}

// rowOf traduce (categoría, artículo) a la fila que lo produjo. Un defecto de la
// categoría entera se atribuye a la PRIMERA fila que la nombró, que es donde el dueño
// va a ir a mirar.
func (p *tabular) rowOf(category, item *int) int {
	if category == nil || *category >= len(p.order) {
		return 0
	}
	cat := p.order[*category]
	if item == nil || *item >= len(cat.itemRows) {
		return cat.row
	}
	return cat.itemRows[*item]
}

// tabularColumn traduce el campo del contrato a la columna de la planilla que lo
// contiene.
//
// Un defecto de la CATEGORÍA (sin artículo) señala siempre la columna `categoria`
// aunque el validador lo llame "label" o "code": en la planilla el nombre y el código
// de la categoría viven los dos en esa celda, mientras que las columnas `nombre` y
// `codigo` son las del artículo. Traducirlo por el diccionario mandaría al dueño a
// mirar la celda equivocada.
func tabularColumn(e ImportFieldError) string {
	if e.CategoryIndex != nil && e.ItemIndex == nil {
		if strings.HasPrefix(e.Field, "subcategories") {
			return colSubcategoria
		}
		return colCategoria
	}
	base := e.Field
	if i := strings.IndexAny(base, "[."); i >= 0 {
		base = base[:i] // "variants[1].price" → "variants": la celda es una sola
	}
	if col, ok := tabularColumnOf[base]; ok {
		return col
	}
	return e.Field
}

// ============================ utilidades ============================

// atRow anota un defecto en una FILA de la planilla. Usa el MISMO collector que el
// validador JSON —mismo tope, mismo cierre— porque los dos caminos desembocan en la
// misma lista de la respuesta y lo único que cambia es la coordenada. row 0 = el
// defecto no es de ninguna fila en concreto, sino de la planilla entera.
func (c *collector) atRow(row int, field, reason string) {
	if len(c.errs) >= maxImportErrors {
		c.dropped++
		return
	}
	c.errs = append(c.errs, ImportFieldError{Row: row, Field: field, Reason: reason})
}

// rowSubject nombra una fila en los mensajes: por su número —que es lo que la hoja
// enseña en el margen y lo único que no puede faltar— y con el nombre del artículo al
// lado cuando lo tiene. Espeja itemSubject del validador.
func rowSubject(n int, label string) string {
	s := "la fila " + strconv.Itoa(n)
	if label != "" {
		s += " (" + strconv.Quote(label) + ")"
	}
	return s
}

// blankRow indica si la fila no tiene nada escrito.
func blankRow(row []string) bool {
	for _, cell := range row {
		if strings.TrimSpace(cell) != "" {
			return false
		}
	}
	return true
}

// splitEntries deshace una celda múltiple en sus entradas y descarta las vacías: un
// «;» de más al final de la celda es un desliz de quien escribe, no un defecto del
// catálogo.
func splitEntries(cell string) []string {
	if strings.TrimSpace(cell) == "" {
		return nil
	}
	parts := strings.Split(cell, TabularEntrySeparator)
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if e := strings.TrimSpace(part); e != "" {
			out = append(out, e)
		}
	}
	return out
}

// splitFields deshace UNA entrada en sus campos. Un campo no puede contener «|»: es
// el separador, y admitirlo dentro obligaría a inventar un escape que nadie va a
// teclear en una celda. Un nombre con «|» se importa por JSON.
func splitFields(entry string) []string {
	parts := strings.Split(entry, TabularFieldSeparator)
	for i, part := range parts {
		parts[i] = strings.TrimSpace(part)
	}
	return parts
}
