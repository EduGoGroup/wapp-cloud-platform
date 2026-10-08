// Porta internal/catalogimport/tabular.go @ 3c74b80

package catalogimport

import (
	"encoding/json"
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
//
// El tabular.go viejo mide 609 líneas y aquí está partido por tema (05 E-13), solo
// moviendo declaraciones: tabular.go (la entrada, la cabecera, los topes y las
// filas), tabular_cells.go (la mini-sintaxis de las celdas múltiples y el precio) y
// tabular_locate.go (el documento que se arma y la vuelta de cada defecto a su
// fila). Los dos trozos solo llevan auxiliares no exportados: nacieron en el verde
// (05 E-4), junto a los gemelos de test que ya existían en rojo.

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
//
// LO QUE DEVUELVE. El documento ya validado —el mismo catálogo que daría su
// equivalente JSON, con Source.Kind "planilla"— o el cero de CatalogImport y la lista
// de defectos. Cada defecto lleva Row (0 = de la planilla entera), SIN índices de
// categoría ni de artículo, y en Field el nombre de la COLUMNA de la planilla, no el
// campo del JSON ("precio", no "price"; "variantes", no "variants[1].price"). Los
// que juzga el validador conservan su motivo —hablan de «el artículo 2 de la
// categoría …»— y solo cambian de coordenada. Salen en orden de fila. El tope de 200
// defectos y su resumen son los de Validate.
//
// LA CABECERA (fila 1). Las columnas se localizan por NOMBRE, no por posición: sin
// espacios alrededor, sin BOM delante, sin distinguir mayúsculas y sin tildes
// («Descripción» es descripcion). Una columna repetida la gana la primera; una que
// no es del contrato se ignora. Son obligatorias categoria, codigo, sku, nombre y
// precio: si falta alguna, un único defecto (fila 1, campo "cabecera") las NOMBRA y
// no se lee nada más. Las demás pueden faltar: una columna ausente es una columna
// vacía. Sin ninguna fila, o con solo la cabecera, un único defecto de campo
// "planilla".
//
// LAS FILAS. Una fila sin nada escrito (o solo espacios, también Unicode) ni cuenta
// ni estorba; una fila más corta que la cabecera tiene vacías las celdas que le
// faltan. Toda celda se lee sin espacios alrededor. El tope Limits.MaxItems cuenta
// las filas NO vacías y se comprueba antes de leer ninguna: pasado, un único defecto
// de fila 0 y campo "planilla".
//
// LAS CELDAS:
//   - categoria y subcategoria se escriben «codigo|nombre», los dos obligatorios; la
//     subcategoría es opcional y queda declarada en su categoría por el hecho de
//     escribirla. Dos filas que le dan nombres distintos al mismo código son un
//     defecto de la segunda, que cita la fila de la primera;
//   - tags: entradas separadas por «;»; una entrada con «|» es un defecto;
//   - atributos: «clave|valor» por entrada; una clave repetida se queda con el
//     último valor;
//   - variantes: «codigo|nombre|precio» por entrada;
//   - componentes: «sku|cantidad» por entrada, con la cantidad SIEMPRE escrita: un
//     entero de 1 o más (lo que acepta strconv.Atoi: «+2» y «007» valen);
//   - en toda celda múltiple las entradas vacías se descartan (un «;» de más, o
//     repetido, es un desliz y no un defecto) y los campos de cada entrada se leen
//     sin espacios alrededor; un «|» de más o repetido sí rompe la entrada;
//   - precio (el del artículo y el de cada variante): el ÚNICO campo que la lectura
//     juzga por su cuenta, porque una celda ilegible llegaría al validador como 0,
//     que es un precio válido, y el artículo se importaría regalado. Vacío es un
//     defecto; lo demás tiene que leerlo strconv.ParseFloat y no ser NaN ni
//     infinito. Los dígitos no ASCII no son un número; «1e3», «0x1p4» y «1_000» sí
//     lo son para ParseFloat y pasan. Que sea negativo lo juzga el validador.
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
