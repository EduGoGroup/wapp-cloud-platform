// Porta internal/publicapi/catalogtabular.go @ 724f3035 (286 líneas, entero) y el registro de
// I15 (internal/publicapi/publicapi.go @ 724f3035, registerCatalogImport, líneas 1106-1113).
//
// catalogtabular.go — LA PUERTA DE LA PLANILLA DEL IMPORT DE CATÁLOGO (Plan 041 · Ola 3 · T3.4,
// D-041.9; mapa §2.9, I15): POST /api/v1/catalog/import/tabular. Es el otro extremo de
// export.go: allí las filas se convierten en un CSV o un XLSX que se descarga; aquí un CSV o un
// XLSX que se sube vuelve a ser filas. Las dos mitades viven en la capa que sabe de bytes y de
// transporte, para que el contrato tabular de catalogimport siga sin saber en qué formato viajó.
//
// En el rojo solo existía MountCatalogTabular; el handler, la lectura del multipart y los
// lectores de CSV y XLSX nacieron con el verde (05 E-4, P6). Los puertos y las dependencias son
// los de catalogimport.go, y el tramo común (diff, respuesta, apply), el de
// catalogimport_apply.go.

package apipublica

import (
	"bytes"
	"encoding/csv"
	"errors"
	"net/http"
	"strings"

	"github.com/xuri/excelize/v2"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo/catalogimport"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
)

// MountCatalogTabular registra en c la ruta I15, "POST /api/v1/catalog/import/tabular", con la
// MISMA condición que I14: solo si d.Content, d.ContentVersions y d.Entitlements son los TRES
// distintos de nil. Si falta cualquiera, la ruta NO existe (404; ningún patrón; T-11). No monta
// I14, I16 ni I17.
//
// UN SOLO CAMINO, DOS PUERTAS. Es el MISMO import de MountCatalogImport subiendo la planilla que
// el dueño llenó en su hoja de cálculo en vez del JSON del contrato. Lo único que hace distinto
// es traducir el archivo a filas y las filas al documento (catalogimport.ParseTabular); a partir
// de ahí es el mismo código: el mismo diff, el mismo versionado y la misma respuesta. Por eso un
// catálogo subido de las dos maneras produce el MISMO blob, byte a byte.
//
// LOS GUARDIAS son los de I14, idénticos: cadena W de Common, permiso "content.write" (también
// en validate; "content.read" no basta), recurso de auditoría "catalog_import" —el MISMO: lo que
// se hace es lo mismo, y por dónde entró queda anotado en la procedencia de la versión— y, por
// dentro de la auditoría, el gate de la feature "catalog_import". 401 sin token; 403
// {"error":"permiso denegado"} sin el permiso; 403
// {"error":"feature_not_enabled","feature":"catalog_import"} con el permiso y sin la feature
// (ningún puerto se consulta; UN registro "failure" con Meta {"status":403}); UN registro por
// petición que pasa el permiso.
//
// LA PETICIÓN. `POST …/catalog/import/tabular?mode=validate|apply&ref=<ref>` con un formulario
// multipart/form-data y el archivo en el campo "file" —uno solo y con nombre fijo: aceptar
// «cualquier parte que parezca un archivo» subiría en silencio el equivocado—.
//
//   - "mode" y "ref": las mismas reglas de I14 (defecto validate y "catalogo"; modo desconocido
//     ⇒ 400 {"error":"mode debe ser validate o apply"}, con la forma SIMPLE y decidido antes de
//     leer el archivo). El tenant es el del token (INV-8);
//   - el formato NO se declara: se reconoce por el CONTENIDO. Un archivo que empieza por la
//     firma de un ZIP (`PK\x03\x04`) es un XLSX; cualquier otro se lee como CSV. Ni la extensión
//     ni el nombre del archivo ni su Content-Type cuentan;
//   - XLSX: la hoja de datos se busca POR NOMBRE (catalogimport.TabularSheetName = "catalogo"),
//     sin distinguir mayúsculas y sin los espacios de alrededor, esté en la posición que esté
//     (quien recibe el libro puede dejar hojas suyas delante). Las celdas se leen CRUDAS: un
//     precio con formato de moneda llega como su número, no como «$18.000,00». El libro no puede
//     ocupar más de 32 MiB DESCOMPRIMIDO (un .xlsx es un ZIP y el techo de bytes del archivo no
//     dice nada de lo que ocupa al abrirlo);
//   - CSV, tres tolerancias del TRANSPORTE (el contrato —columnas, celdas, precio— sigue
//     estricto): el BOM UTF-8 inicial se descarta; el separador se reconoce en la PRIMERA línea
//     entre coma, punto y coma y tabulador —gana el que más veces aparece y, en empate, la coma—
//     porque Excel en español guarda con «;»; las filas pueden tener distinto número de campos; y
//     una comilla suelta en un campo sin comillar se lee tal cual (LazyQuotes).
//
// DOS TECHOS DE BYTES, y hacen falta los dos. Con T = el techo efectivo (d.ContentMaxBytes, o
// 1048576 si es <= 0):
//
//   - el del CUERPO entero: T + 8 KiB (el margen es sitio para los delimitadores y cabeceras del
//     multipart; sin él un archivo exactamente en el límite se rechazaría por el peso del sobre);
//   - el del ARCHIVO: T, el MISMO número que aplica el import JSON. Un archivo de exactamente T
//     bytes pasa;
//   - pasarse de cualquiera de los dos ⇒ 413 y el mensaje nombra SIEMPRE el techo del archivo
//     (T), nunca T + 8 KiB.
//
// LOS FALLOS. 🔴 TODO fallo del archivo o de la planilla —también el 413— sale con la MISMA
// forma que los defectos del import JSON, {"error":"validation_failed","errors":[…]}: la
// pantalla que los pinta es la misma y no tiene por qué saber por qué puerta entró el documento.
// Los de LECTURA llevan UNA entrada, {"field":"archivo","reason":<motivo>} y nada más (sin fila
// ni índices: el problema no está en ninguna fila), con estos motivos literales:
//
//   - 413, por tamaño: «el archivo excede el tamaño máximo de T bytes» (única excepción de forma
//     del 413 de la API: no lleva "max_bytes"; ver limits.go);
//   - 400, sin archivo (cuerpo que no es multipart, formulario vacío o campo con otro nombre):
//     «no llegó ningún archivo: súbelo en el campo «file» de un formulario multipart/form-data.»;
//   - 400, firma de ZIP pero no es un libro: «no se pudo abrir el archivo de Excel: comprueba
//     que sea el .xlsx que descargaste de la plantilla.»;
//   - 400, libro sin la hoja: «el libro no tiene ninguna hoja llamada «catalogo»: renómbrala así
//     o parte de la plantilla que se descarga desde aquí.»;
//   - y tres DEFENSAS, también 400, que con un archivo ya recibido entero y las tolerancias de
//     arriba no se sabe alcanzar desde fuera: «no se pudo leer el archivo subido.» (fallo al
//     leer la parte del formulario), «no se pudo leer la hoja «catalogo» del libro.» (la hoja
//     existe y no se deja recorrer) y «no se pudo leer el archivo como planilla: sube el CSV o
//     el XLSX que descargaste de la plantilla.» (el lector de CSV se rinde).
//
// Los de CONTENIDO son los de catalogimport.ParseTabular tal cual ⇒ 400 con su lista, ubicados
// por FILA ("row", el número que la hoja enseña en su margen; la cabecera es la 1) y no por
// índices de categoría y artículo: es lo que tiene delante quien llenó la planilla.
//
// Con un 400 o un 413 no se lee el catálogo vigente ni se escribe nada, tampoco en apply, y la
// respuesta NO lleva "document": uno a medias sería peor que ninguno.
//
// LA RESPUESTA. 200 con el mismo objeto de I14 más UN campo, al final:
// {"mode","ref","applied","items","diff","archived_version"?,"document"}.
//
//   - "document" es el documento LEÍDO Y NORMALIZADO, listo para enviarse tal cual a
//     POST /api/v1/catalog/import. 🔴 Existe para que la confirmación en dos pasos siga siendo
//     fiel: la pantalla enseña el diff del validate y manda ESE documento al import JSON para
//     aplicarlo (un .xlsx es binario y no cabe en un campo oculto; volver a pedirlo rompería la
//     garantía de que se aplica exactamente lo que se enseñó). Aplicarlo por I14 escribe el mismo
//     blob que aplicar la planilla por aquí;
//   - viaja TAMBIÉN en apply: la respuesta es un solo objeto con "applied" como única diferencia;
//   - el diff, el catálogo vigente (primer import, vigente ilegible con su aviso, almacén caído
//     ⇒ 500 {"error":"no se pudo leer el catálogo vigente"}), "applied", "archived_version" y el
//     500 {"error":"no se pudo aplicar el catálogo"} son los de MountCatalogImport.
//
// En apply, d.ContentVersions.ReplaceTenantContentVersioned se llama UNA vez con la procedencia
// store.VersionSourceImportTabular = "import_tabular": la fija el camino, no el llamante.
//
// Defensa que los tokens de sharedjwt no alcanzan: una identidad sin empresa que llegara al
// handler ⇒ 401 {"error":"autenticación requerida"}.
//
// Fallo de cableado: k.MW nil con las tres dependencias presentes hace panic AL MONTAR, con un
// mensaje que nombra MountCatalogTabular (ver Common). Si falta una dependencia no se monta nada
// y k ni se mira.
func MountCatalogTabular(c *Cara, k Common, d CatalogImportDeps) {
	if !catalogImportMountable(d) {
		return
	}
	mustHaveMW(k, "MountCatalogTabular")

	// La planilla (T3.4, D-041.9) es la MISMA operación por otra puerta: sube el
	// CSV/XLSX que el dueño llenó en su hoja en vez del JSON del contrato. Va con
	// los mismos tres guardias y con el MISMO recurso de auditoría —lo que se hace
	// es lo mismo, y por dónde entró queda anotado en `source` de la versión, que es
	// donde de verdad significa algo.
	canImport := entitlements.RequireFeature(d.Entitlements, entitlements.FeatureCatalogImport)
	c.Handle("POST /api/v1/catalog/import/tabular", protect(k, "content.write", "catalog_import",
		canImport(catalogTabularHandler(d.Content, d.ContentVersions, catalogImportLimits(d)))))
}

// catalogTabularFormField (tabularFormField en la cara vieja) es el campo del formulario
// multipart donde viaja el archivo. Uno solo y con nombre fijo: aceptar «cualquier parte que
// parezca un archivo» haría que un formulario mal armado subiera en silencio el archivo
// equivocado.
const catalogTabularFormField = "file"

// catalogTabularEnvelopeSlack (multipartEnvelopeSlack en la cara vieja) es el margen que se le
// da al sobre multipart por encima del techo del archivo: los delimitadores y las cabeceras de
// las partes ocupan unos cientos de bytes, y sin margen un archivo EXACTAMENTE en el límite se
// rechazaría por el peso del sobre y no por el suyo.
const catalogTabularEnvelopeSlack = 8 << 10

// catalogTabularMaxUnzipBytes (maxTabularUnzipBytes en la cara vieja) acota lo que puede llegar
// a ocupar el XLSX una vez DESCOMPRIMIDO, y es el techo que de verdad importa en este endpoint:
// un .xlsx es un ZIP, y el techo de bytes del archivo subido no dice nada de lo que ocupa al
// abrirlo — un megabyte de ZIP bien construido se expande a gigabytes (una «zip
// bomb»), así que sin este límite el techo del multipart protegería solo la red y no
// la memoria del proceso.
//
// 32 MiB es holgado para lo que este import acepta —una planilla de 500 artículos
// ronda unos pocos MB de XML— y sigue siendo un orden de magnitud menos de lo que
// haría daño. excelize deja el límite por defecto en 16 GiB, que es como no tener
// ninguno.
const catalogTabularMaxUnzipBytes = 32 << 20

// catalogTabularHandler (catalogImportTabularHandler en la cara vieja) devuelve el handler de
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
// Las respuestas, una a una, están en el contrato de MountCatalogTabular.
func catalogTabularHandler(cs CatalogImportContentReader, vw CatalogImportVersionWriter, limits catalogimport.Limits) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target, ok := catalogImportTargetFrom(w, r, store.VersionSourceImportTabular)
		if !ok {
			return
		}
		// La planilla devuelve el documento normalizado; el JSON no lo necesita.
		target.echoDocument = true

		doc, code, errBody := catalogTabularDecodeBody(w, r, limits)
		if errBody != nil {
			writeJSON(w, code, errBody)
			return
		}
		catalogImportFinish(w, r, cs, vw, target, doc)
	})
}

// catalogTabularDecodeBody (decodeTabularBody en la cara vieja) lee el archivo subido, lo
// convierte en filas y las traduce al documento del contrato. Devuelve el documento y, si algo
// falla, el status más el cuerpo de error ya armado (nil = todo bien).
//
// TODOS los fallos que no son de tamaño salen con la MISMA forma que los del import
// JSON —validation_failed con su lista— aunque el problema sea del archivo y no del
// catálogo: la pantalla que los pinta es la misma, y darle dos formas distintas la
// obligaría a saber por qué puerta entró el documento para poder enseñar el error.
func catalogTabularDecodeBody(w http.ResponseWriter, r *http.Request, limits catalogimport.Limits) (catalogimport.CatalogImport, int, any) {
	raw, code, msg := catalogTabularReadUpload(w, r, limits)
	if msg != "" {
		return catalogimport.CatalogImport{}, code, catalogTabularFailure(msg)
	}
	rows, msg := catalogTabularRows(raw)
	if msg != "" {
		return catalogimport.CatalogImport{}, http.StatusBadRequest, catalogTabularFailure(msg)
	}
	doc, verr := catalogimport.ParseTabular(rows, limits)
	if verr != nil {
		return catalogimport.CatalogImport{}, http.StatusBadRequest,
			catalogImportErrors{Error: "validation_failed", Errors: verr.Errors}
	}
	return doc, 0, nil
}

// catalogTabularFailure (tabularFailure en la cara vieja) envuelve un fallo de LECTURA del
// archivo en la misma forma que los defectos del catálogo. Sin fila: el problema no está en
// ninguna, está en el archivo.
func catalogTabularFailure(reason string) catalogImportErrors {
	return catalogImportErrors{
		Error:  "validation_failed",
		Errors: []catalogimport.ImportFieldError{{Field: "archivo", Reason: reason}},
	}
}

// catalogTabularReadUpload (readUploadedFile en la cara vieja) saca del multipart el archivo
// subido con DOS techos, y hacen falta los dos:
//
//   - El del CUERPO entero (MaxBytesReader), que corta la conexión sin acumular
//     nada. Es el que impide que alguien empuje gigabytes por este endpoint.
//   - El del ARCHIVO (ReadLimited, el mismo que aplica el import JSON), que es el
//     que de verdad expresa el requisito: un catálogo que pasa el import tiene que
//     caber en tenant_content, y por eso es el MISMO número de bytes.
//
// El techo de bytes NO acota lo que el archivo ocupa al abrirlo, que en un XLSX es
// otra cosa (ver catalogTabularMaxUnzipBytes).
func catalogTabularReadUpload(w http.ResponseWriter, r *http.Request, limits catalogimport.Limits) ([]byte, int, string) {
	maxBytes := limits.MaxJSONBytes
	if maxBytes <= 0 {
		maxBytes = catalogimport.DefaultMaxJSONBytes
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes+catalogTabularEnvelopeSlack)

	file, _, err := r.FormFile(catalogTabularFormField)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			// Se nombra el techo del ARCHIVO, no el del sobre (maxBytes + slack):
			// el slack es sitio para las cabeceras del multipart, no presupuesto
			// que quien sube pueda gastar. Decirle el número mayor le haría
			// reintentar con un archivo que el segundo techo rechazaría igual.
			return nil, http.StatusRequestEntityTooLarge, tooLargeMessage("el archivo", maxBytes)
		}
		return nil, http.StatusBadRequest, "no llegó ningún archivo: súbelo en el campo «" + catalogTabularFormField +
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

// catalogTabularXLSXSignature (xlsxSignature en la cara vieja) es la firma de un ZIP, que es lo
// que un .xlsx es por dentro.
var catalogTabularXLSXSignature = []byte{'P', 'K', 0x03, 0x04}

// catalogTabularRows (tabularRows en la cara vieja) convierte el archivo subido en filas,
// reconociendo el formato por su CONTENIDO. Devuelve el motivo (en español) cuando el archivo no
// se deja leer.
//
// El formato NO se declara: fiarse de la extensión sería fiarse de algo que el usuario puede
// cambiar sin cambiar el archivo.
func catalogTabularRows(raw []byte) ([][]string, string) {
	if bytes.HasPrefix(raw, catalogTabularXLSXSignature) {
		return catalogTabularXLSXRows(raw)
	}
	return catalogTabularCSVRows(raw)
}

// catalogTabularXLSXRows (xlsxRows en la cara vieja) lee la hoja de datos del libro.
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
func catalogTabularXLSXRows(raw []byte) ([][]string, string) {
	opts := excelize.Options{RawCellValue: true, UnzipSizeLimit: catalogTabularMaxUnzipBytes}
	f, err := excelize.OpenReader(bytes.NewReader(raw), opts)
	if err != nil {
		return nil, "no se pudo abrir el archivo de Excel: comprueba que sea el .xlsx que descargaste de la plantilla."
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			_ = cerr // solo se leyó; no hay nada que confirmar al cerrar
		}
	}()

	sheet, ok := catalogTabularSheetByName(f, catalogimport.TabularSheetName)
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

// catalogTabularSheetByName (sheetByName en la cara vieja) busca una hoja por su nombre sin
// distinguir mayúsculas.
func catalogTabularSheetByName(f *excelize.File, want string) (string, bool) {
	for _, name := range f.GetSheetList() {
		if strings.EqualFold(strings.TrimSpace(name), want) {
			return name, true
		}
	}
	return "", false
}

// catalogTabularCSVRows (csvRows en la cara vieja) lee el CSV subido.
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
func catalogTabularCSVRows(raw []byte) ([][]string, string) {
	body := bytes.TrimPrefix(raw, []byte(exportCSVBOM))
	reader := csv.NewReader(bytes.NewReader(body))
	reader.Comma = catalogTabularCSVDelimiter(body)
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true

	rows, err := reader.ReadAll()
	if err != nil {
		return nil, "no se pudo leer el archivo como planilla: sube el CSV o el XLSX que descargaste de la plantilla."
	}
	return rows, ""
}

// catalogTabularCSVDelimiter (csvDelimiter en la cara vieja) reconoce con qué separa las
// columnas este CSV mirando su primera línea.
//
// No es un lujo: Excel configurado en español guarda los CSV con «;» en vez de «,»,
// así que la planilla que nosotros emitimos con comas vuelve con puntos y comas en
// cuanto el dueño la abre y la guarda. Sin esto, el archivo llega entero y aun así
// «no tiene ninguna columna», que es de los errores más desesperantes que se le
// pueden devolver a alguien.
//
// La cabecera decide y no hay ambigüedad posible: lleva once nombres y diez
// separadores, así que el candidato correcto aparece diez veces y el otro ninguna.
func catalogTabularCSVDelimiter(body []byte) rune {
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
