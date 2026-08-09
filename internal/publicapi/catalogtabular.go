package publicapi

import (
	"bytes"
	"encoding/csv"
	"errors"
	"net/http"
	"strings"

	"github.com/xuri/excelize/v2"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/catalogimport"
	flowstore "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

// Este fichero es el otro extremo de export.go: allí las filas se convierten en un
// CSV o un XLSX que se descarga; aquí un CSV o un XLSX que se sube vuelve a ser
// filas. Las dos mitades viven en la misma capa a propósito —la que sabe de bytes y
// de transporte— para que el contrato tabular de catalogimport siga sin saber en qué
// formato viajó: él solo ve filas.

// tabularFormField es el campo del formulario multipart donde viaja el archivo. Uno
// solo y con nombre fijo: aceptar «cualquier parte que parezca un archivo» haría que
// un formulario mal armado subiera en silencio el archivo equivocado.
const tabularFormField = "file"

// multipartEnvelopeSlack es el margen que se le da al sobre multipart por encima del
// techo del archivo: los delimitadores y las cabeceras de las partes ocupan unos
// cientos de bytes, y sin margen un archivo EXACTAMENTE en el límite se rechazaría
// por el peso del sobre y no por el suyo.
const multipartEnvelopeSlack = 8 << 10

// maxTabularUnzipBytes acota lo que puede llegar a ocupar el XLSX una vez
// DESCOMPRIMIDO, y es el techo que de verdad importa en este endpoint: un .xlsx es
// un ZIP, y el techo de bytes del archivo subido no dice nada de lo que ocupa al
// abrirlo — un megabyte de ZIP bien construido se expande a gigabytes (una «zip
// bomb»), así que sin este límite el techo del multipart protegería solo la red y no
// la memoria del proceso.
//
// 32 MiB es holgado para lo que este import acepta —una planilla de 500 artículos
// ronda unos pocos MB de XML— y sigue siendo un orden de magnitud menos de lo que
// haría daño. excelize deja el límite por defecto en 16 GiB, que es como no tener
// ninguno.
const maxTabularUnzipBytes = 32 << 20

// catalogImportTabularHandler devuelve el handler de
// POST /api/v1/catalog/import/tabular?mode=validate|apply&ref=<ref> (D-041.9): el
// MISMO import de catálogo, pero subiendo la planilla que el dueño del negocio llenó
// en su hoja de cálculo (CSV o XLSX) en vez del JSON del contrato.
//
// UN SOLO CAMINO, DOS PUERTAS. Lo único que este handler hace distinto es traducir
// el archivo a filas y las filas al documento del contrato; a partir de ahí es
// literalmente el mismo código que el import JSON: el mismo validador, el mismo
// diff, el mismo versionado y la misma respuesta. Por eso un catálogo subido de las
// dos maneras produce el mismo blob, y por eso no hay dos definiciones de «catálogo
// correcto» que puedan separarse con el tiempo.
//
// LO QUE SÍ CAMBIA ES DÓNDE ESTÁ EL DEFECTO: los errores salen ubicados por FILA
// («la fila 4»), no por índices de categoría y artículo, porque es lo que la persona
// que llenó la planilla tiene delante.
//
// El formato NO se declara: se reconoce por el contenido (un XLSX empieza por la
// firma de un ZIP). Fiarse de la extensión sería fiarse de algo que el usuario puede
// cambiar sin cambiar el archivo.
//
// LA RESPUESTA TRAE ADEMÁS EL DOCUMENTO NORMALIZADO (`document`), que es lo que
// permite confirmar en dos pasos sin volver a pedir el archivo: la pantalla enseña el
// diff del validate y manda ESE documento al import JSON de siempre para aplicarlo.
// Un .xlsx no cabe en un campo oculto —es binario— y volver a pedirlo rompería la
// garantía de que se aplica exactamente lo que se enseñó (ver
// catalogImportResponse.Document).
//
// Respuestas: 200 con {mode, ref, applied, items, diff, archived_version?, document}
// —el mismo objeto que el import JSON, con `document` como único añadido—; 400 con
// {error:"validation_failed", errors:[…]} si la planilla no se puede leer o no valida
// (y entonces NO hay documento: uno a medias sería peor que ninguno); 400 si el modo
// es desconocido o no viene el archivo; 401 sin identidad; 403 sin la feature
// catalog_import (lo corta el gate); 413 si el archivo excede el techo de bytes; 500
// en fallo del store.
func catalogImportTabularHandler(cs TenantContentStore, vw CatalogVersionWriter, limits catalogimport.Limits) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target, ok := catalogImportTargetFrom(w, r, cs, vw, flowstore.VersionSourceImportTabular)
		if !ok {
			return
		}
		// La planilla devuelve el documento normalizado; el JSON no lo necesita.
		target.echoDocument = true

		doc, code, errBody := decodeTabularBody(w, r, limits)
		if errBody != nil {
			writeJSON(w, code, errBody)
			return
		}
		finishCatalogImport(w, r, cs, vw, target, doc)
	})
}

// decodeTabularBody lee el archivo subido, lo convierte en filas y las traduce al
// documento del contrato. Devuelve el documento y, si algo falla, el status más el
// cuerpo de error ya armado (nil = todo bien).
//
// TODOS los fallos que no son de tamaño salen con la MISMA forma que los del import
// JSON —validation_failed con su lista— aunque el problema sea del archivo y no del
// catálogo: la pantalla que los pinta es la misma, y darle dos formas distintas la
// obligaría a saber por qué puerta entró el documento para poder enseñar el error.
func decodeTabularBody(w http.ResponseWriter, r *http.Request, limits catalogimport.Limits) (catalogimport.CatalogImport, int, any) {
	raw, code, msg := readUploadedFile(w, r, limits)
	if msg != "" {
		return catalogimport.CatalogImport{}, code, tabularFailure(msg)
	}
	rows, msg := tabularRows(raw)
	if msg != "" {
		return catalogimport.CatalogImport{}, http.StatusBadRequest, tabularFailure(msg)
	}
	doc, verr := catalogimport.ParseTabular(rows, limits)
	if verr != nil {
		return catalogimport.CatalogImport{}, http.StatusBadRequest,
			catalogImportErrors{Error: "validation_failed", Errors: verr.Errors}
	}
	return doc, 0, nil
}

// tabularFailure envuelve un fallo de LECTURA del archivo en la misma forma que los
// defectos del catálogo. Sin fila: el problema no está en ninguna, está en el archivo.
func tabularFailure(reason string) catalogImportErrors {
	return catalogImportErrors{
		Error:  "validation_failed",
		Errors: []catalogimport.ImportFieldError{{Field: "archivo", Reason: reason}},
	}
}

// readUploadedFile saca del multipart el archivo subido con DOS techos, y hacen
// falta los dos:
//
//   - El del CUERPO entero (MaxBytesReader), que corta la conexión sin acumular
//     nada. Es el que impide que alguien empuje gigabytes por este endpoint.
//   - El del ARCHIVO (ReadLimited, el mismo que aplica el import JSON), que es el
//     que de verdad expresa el requisito: un catálogo que pasa el import tiene que
//     caber en tenant_content, y por eso es el MISMO número de bytes.
//
// El techo de bytes NO acota lo que el archivo ocupa al abrirlo, que en un XLSX es
// otra cosa (ver maxTabularUnzipBytes).
func readUploadedFile(w http.ResponseWriter, r *http.Request, limits catalogimport.Limits) ([]byte, int, string) {
	maxBytes := limits.MaxJSONBytes
	if maxBytes <= 0 {
		maxBytes = catalogimport.DefaultMaxJSONBytes
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes+multipartEnvelopeSlack)

	file, _, err := r.FormFile(tabularFormField)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			// Se nombra el techo del ARCHIVO, no el del sobre (maxBytes + slack):
			// el slack es sitio para las cabeceras del multipart, no presupuesto
			// que quien sube pueda gastar. Decirle el número mayor le haría
			// reintentar con un archivo que el segundo techo rechazaría igual.
			return nil, http.StatusRequestEntityTooLarge, tooLargeMessage("el archivo", maxBytes)
		}
		return nil, http.StatusBadRequest, "no llegó ningún archivo: súbelo en el campo «" + tabularFormField +
			"» de un formulario multipart/form-data."
	}
	defer func() {
		if cerr := file.Close(); cerr != nil {
			_ = cerr // ya se leyó entero; cerrarlo mal no cambia la respuesta
		}
	}()

	raw, err := catalogimport.ReadLimited(file, limits)
	if err != nil {
		if errors.Is(err, catalogimport.ErrDocumentTooLarge) {
			return nil, http.StatusRequestEntityTooLarge, tooLargeMessage("el archivo", maxBytes)
		}
		return nil, http.StatusBadRequest, "no se pudo leer el archivo subido."
	}
	return raw, 0, ""
}

// xlsxSignature es la firma de un ZIP, que es lo que un .xlsx es por dentro.
var xlsxSignature = []byte{'P', 'K', 0x03, 0x04}

// tabularRows convierte el archivo subido en filas, reconociendo el formato por su
// CONTENIDO. Devuelve el motivo (en español) cuando el archivo no se deja leer.
func tabularRows(raw []byte) ([][]string, string) {
	if bytes.HasPrefix(raw, xlsxSignature) {
		return xlsxRows(raw)
	}
	return csvRows(raw)
}

// xlsxRows lee la hoja de datos del libro.
//
// LA HOJA SE BUSCA POR NOMBRE, no por posición: quien recibe el libro puede añadir
// hojas suyas —cuentas, notas— y dejarlas delante sin sospechar que con eso rompe el
// import. Se compara sin distinguir mayúsculas porque renombrar una hoja es teclear,
// y «Catalogo» es la misma hoja que «catalogo».
//
// RawCellValue es lo que hace que el precio sea un número: sin él, excelize devuelve
// la celda YA FORMATEADA, así que una columna de precios con formato de moneda
// llegaría aquí como «$18.000,00» y el import rechazaría una planilla impecable por
// algo que solo era el aspecto de la celda.
func xlsxRows(raw []byte) ([][]string, string) {
	opts := excelize.Options{RawCellValue: true, UnzipSizeLimit: maxTabularUnzipBytes}
	f, err := excelize.OpenReader(bytes.NewReader(raw), opts)
	if err != nil {
		return nil, "no se pudo abrir el archivo de Excel: comprueba que sea el .xlsx que descargaste de la plantilla."
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			_ = cerr // solo se leyó; no hay nada que confirmar al cerrar
		}
	}()

	sheet, ok := sheetByName(f, catalogimport.TabularSheetName)
	if !ok {
		return nil, "el libro no tiene ninguna hoja llamada «" + catalogimport.TabularSheetName +
			"»: renómbrala así o parte de la plantilla que se descarga desde aquí."
	}
	rows, err := f.GetRows(sheet, opts)
	if err != nil {
		return nil, "no se pudo leer la hoja «" + catalogimport.TabularSheetName + "» del libro."
	}
	return rows, ""
}

// sheetByName busca una hoja por su nombre sin distinguir mayúsculas.
func sheetByName(f *excelize.File, want string) (string, bool) {
	for _, name := range f.GetSheetList() {
		if strings.EqualFold(strings.TrimSpace(name), want) {
			return name, true
		}
	}
	return "", false
}

// csvRows lee el CSV subido.
//
// Tres tolerancias, y ninguna afecta al CONTRATO —el nombre de las columnas, la
// gramática de las celdas y el precio siguen siendo estrictos—: son del transporte,
// donde quien manda es Excel y no nosotros.
//
//   - El BOM se descarta: lo escribe nuestra propia plantilla (para que Excel en
//     Windows no destroce las tildes) y volvería en la primera celda de la cabecera.
//   - Las filas pueden tener distinto número de campos: quien borra las celdas
//     vacías del final de una fila deja una fila más corta, y eso no es un defecto.
//   - LazyQuotes: una comilla suelta en un campo sin comillar aborta la lectura del
//     archivo ENTERO con un mensaje que nadie puede accionar. Aquí se lee tal cual y,
//     si de verdad estropea el dato, lo dirá el contrato con la fila señalada.
func csvRows(raw []byte) ([][]string, string) {
	body := bytes.TrimPrefix(raw, []byte(csvBOM))
	reader := csv.NewReader(bytes.NewReader(body))
	reader.Comma = csvDelimiter(body)
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true

	rows, err := reader.ReadAll()
	if err != nil {
		return nil, "no se pudo leer el archivo como planilla: sube el CSV o el XLSX que descargaste de la plantilla."
	}
	return rows, ""
}

// csvDelimiter reconoce con qué separa las columnas este CSV mirando su primera
// línea.
//
// No es un lujo: Excel configurado en español guarda los CSV con «;» en vez de «,»,
// así que la planilla que nosotros emitimos con comas vuelve con puntos y comas en
// cuanto el dueño la abre y la guarda. Sin esto, el archivo llega entero y aun así
// «no tiene ninguna columna», que es de los errores más desesperantes que se le
// pueden devolver a alguien.
//
// La cabecera decide y no hay ambigüedad posible: lleva once nombres y diez
// separadores, así que el candidato correcto aparece diez veces y el otro ninguna.
func csvDelimiter(body []byte) rune {
	header := body
	if i := bytes.IndexAny(header, "\r\n"); i >= 0 {
		header = header[:i]
	}
	best, bestCount := ',', bytes.Count(header, []byte{','})
	for _, candidate := range []rune{';', '\t'} {
		if n := bytes.Count(header, []byte(string(candidate))); n > bestCount {
			best, bestCount = candidate, n
		}
	}
	return best
}
