// Porta internal/catalogimport/validator.go @ 3c74b80

package catalogimport

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
)

// El validador viejo mide 876 líneas y aquí está partido por tema (05 E-13), solo
// moviendo declaraciones: validator.go (la entrada, la acumulación de defectos, la
// forma cruda del documento y la cabecera), validator_catalog.go (cuerpo, categorías
// y subcategorías), validator_item.go (el artículo y sus campos v2) y
// validator_fields.go (los decodificadores de campo y las utilidades de los
// mensajes). Los tres trozos solo llevan auxiliares no exportados: nacieron en el
// verde (05 E-4), junto a los gemelos de test que ya existían en rojo.

// ErrDocumentTooLarge es el rechazo por tamaño: el documento supera
// Limits.MaxJSONBytes. Se decide leyendo, SIN deserializar (ver ReadLimited), así
// que un archivo de 50 MiB nunca se materializa en memoria.
var ErrDocumentTooLarge = errors.New("el documento de import excede el tamaño máximo permitido")

// maxImportErrors acota cuántos defectos se acumulan en una validación. Un
// documento roto de 500 artículos produciría miles de entradas: una respuesta
// inmanejable para la consola y una lista que nadie lee. Pasado el tope solo se
// dice cuántos quedaron sin detallar (mismo criterio que los avisos del parseo
// tolerante en catalogo).
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
//
// Promete:
//   - un cuerpo de exactamente MaxJSONBytes se devuelve entero y sin error;
//   - con un byte más devuelve (nil, error) y errors.Is(err, ErrDocumentTooLarge);
//     el texto es el del centinela más « (máximo N bytes)»;
//   - del lector no se piden más de MaxJSONBytes+1 bytes;
//   - un MaxJSONBytes <= 0 cae a DefaultMaxJSONBytes (nunca desactiva el techo);
//   - un fallo del lector se devuelve envuelto: «no se pudo leer el documento de
//     import: » más el error original (errors.Is lo encuentra).
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
//
// EL RIGOR. Acepta exactamente lo que el runtime conserva: el Catalog de un
// documento válido, serializado, lo parsea catalogo.ParseCatalog SIN un solo aviso.
// Los campos que el contrato no conoce se ignoran sin defecto.
//
// LOS DEFECTOS. Cada uno lleva su ubicación (CategoryIndex e ItemIndex, base 0; los
// dos nil = cabecera o documento entero), el campo del contrato y un motivo en
// español escrito para el dueño del negocio. Los motivos son LITERALES (viajan en el
// 400 que ve la dueña) y nombran el elemento como lo nombra una persona: la
// categoría por su nombre —o «con código "x"», o por su posición si le falta todo—
// y el artículo por su posición (base 1) con su nombre entre paréntesis. Salen
// ordenados por posición en el documento (cabecera, categoría, artículo) y, dentro
// de un mismo elemento, en el orden en que se validan sus campos: la lista no baila
// entre corridas. A partir de 200 defectos no se detalla más: una entrada final,
// con Field "(varios)", dice cuántos se omitieron.
//
// LO QUE CORTA LA VALIDACIÓN (un solo defecto de cabecera y nada más):
//   - el documento vacío o solo espacios, lo que no es JSON (el motivo dice hacia
//     qué línea está el error) y el JSON que no es un objeto: Field "documento";
//   - un "format" o una "version" ausentes, de otro tipo o que no son los de este
//     contrato: el cuerpo NO se interpreta (se acumulan los dos si fallan los dos);
//   - más artículos que Limits.MaxItems en el documento entero (MaxItems <= 0 cae a
//     DefaultMaxItems): no se valida ningún artículo.
//
// LAS REGLAS DEL CUERPO:
//   - "source" es informativa: si viene, tiene que ser un objeto;
//   - hace falta "catalog" con al menos una categoría;
//   - categoría: nombre y código obligatorios (se guardan sin espacios alrededor),
//     código único entre categorías, al menos un artículo; "subcategories" es
//     opcional y, si viene, cada una lleva código y nombre y su código no se repite;
//   - artículo: nombre (sin espacios alrededor), código (único dentro de su
//     categoría), sku y precio obligatorios; el precio es un número JSON no negativo
//     (0 vale; un texto, aunque sean dígitos, no);
//   - el sku es único en TODO el catálogo, no solo en su categoría, y no puede
//     empezar por catalogo.SystemSKUPrefix. Se compara TAL CUAL llega, sin recortar:
//     un sku con un espacio delante del prefijo, o que solo se distingue de otro por
//     un espacio final, pasa (es lo que hace el runtime con el mismo blob);
//   - "subcategory" referencia (sin espacios alrededor) una subcategoría declarada
//     en SU categoría; "tags" es una lista de textos no vacíos; "attributes" es un
//     objeto de escalares (texto, número o booleano, que se guardan como texto);
//   - "variants": lista no vacía de {code, label, price}, código único dentro del
//     artículo; "components": lista no vacía de {sku, qty}, qty entero >= 1 (ausente
//     vale 1) y el sku tiene que existir en el catálogo, aunque se declare más abajo;
//   - "variants" y "components" a la vez se rechaza: se mira la PRESENCIA de los dos
//     campos, no el resultado de parsearlos.
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
