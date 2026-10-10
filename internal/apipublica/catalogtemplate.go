// Porta internal/publicapi/catalogtemplate.go @ 724f3035 (144 líneas, entero) y el registro de
// I16 e I17 (internal/publicapi/publicapi.go @ 724f3035, registerCatalogImport, líneas
// 1115-1123).
//
// catalogtemplate.go — LA PLANTILLA Y EL PROMPT DEL IMPORT DE CATÁLOGO (Plan 041 · Ola 3 · T3.2;
// mapa §2.9, I16 e I17): GET /api/v1/catalog/import/template y GET /api/v1/catalog/import/prompt.
// Son el contrato dicho de dos maneras, y las sirve el BACKEND —no una copia pegada en la
// consola— porque salen de los MISMOS structs y del mismo texto versionado del módulo catálogo:
// no pueden describir un contrato que este servidor ya no acepta.
//
// En el rojo solo existía MountCatalogTemplate; los dos handlers y sus auxiliares nacieron con
// el verde (05 E-4, P6). Las dependencias son las de catalogimport.go.

package apipublica

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo/catalogimport"
)

// MountCatalogTemplate registra en c las rutas I16, "GET /api/v1/catalog/import/template", e
// I17, "GET /api/v1/catalog/import/prompt", con la MISMA condición que I14: solo si d.Content,
// d.ContentVersions y d.Entitlements son los TRES distintos de nil. 🔴 Ninguna de las dos toca
// un almacén y aun así exigen los dos puertos: aparecen y desaparecen con el POST porque son la
// primera mitad del mismo acto (repartir la plantilla de un import que no está montado mandaría
// al operador a llenarla para encontrarse un 404 al subirla). Si falta cualquiera de las tres,
// NINGUNA de las dos existe (404; ningún patrón; T-11). No monta I14 ni I15.
//
// Las dos llevan cadena R de Common con permiso "content.read" (401 sin token; 403
// {"error":"permiso denegado"} sin el permiso o con un token sin empresa; NINGÚN registro de
// auditoría, tampoco en el camino feliz) y, POR DENTRO de ella, el gate de la feature
// "catalog_import" (entitlements.RequireFeature, fail-closed): con el permiso y sin la feature
// ⇒ 403 con EXACTAMENTE {"error":"feature_not_enabled","feature":"catalog_import"}; sin el
// permiso Y sin la feature ⇒ el 403 es el del permiso. Van gateadas igual que el resto del
// import porque son parte de la MISMA capacidad vendida: repartir la plantilla a quien no puede
// importar sería enseñar la puerta y no dar la llave.
//
// Ninguna lleva nada del tenant: la respuesta es idéntica para todos, y ni d.Content ni
// d.ContentVersions se consultan JAMÁS.
//
// I16, LA PLANTILLA: `GET …/catalog/import/template?format=json|csv|xlsx`. TRES FORMATOS, UN
// SOLO CATÁLOGO: el JSON es el contrato (y es lo que se pega en un LLM junto al prompt); el CSV
// y el XLSX son la MISMA información en la planilla canónica (D-041.9). Las filas de las dos
// salen del mismo documento, no de un literal paralelo.
//
//   - "format": si falta o viene vacío ⇒ json. Solo valen los literales "json", "csv" y "xlsx"
//     (ni mayúsculas ni espacios). Otro ⇒ 400 {"error":"format inválido: usa json, csv o xlsx"}
//     —no se adivina: «xls» tecleado a las prisas no puede degradar en silencio a otra cosa—,
//     sin Content-Disposition;
//   - 200 con el archivo ENTERO, generado en memoria antes de escribir la primera cabecera.
//     Cabeceras: Content-Disposition `attachment; filename="catalogo-plantilla.<format>"` —SIN
//     fecha, al revés que el export: la plantilla nueva debe pisar a la vieja en la carpeta de
//     descargas—; Content-Length = los bytes del cuerpo; y Content-Type
//     "application/json; charset=utf-8", "text/csv; charset=utf-8" o
//     "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet";
//   - json: catalogimport.BuildTemplate() serializado INDENTADO a dos espacios y con UN salto de
//     línea final (lo abre y lo edita a mano el dueño; una sola línea de 900 caracteres es
//     ilegible en un editor y en la caja de un LLM). Es un documento que el propio validador
//     acepta sin un defecto: subido tal cual a I14 da 200;
//   - csv: BOM UTF-8, fin de línea CRLF y comillas RFC 4180 (el mismo escritor del export de
//     solicitudes); la primera fila es catalogimport.TabularColumns() y las demás
//     catalogimport.TemplateSheetRows(), una por artículo. El precio va PELADO (18000): sin
//     símbolo ni separador de miles;
//   - xlsx: un libro con la hoja catalogimport.TabularSheetName ("catalogo"), la fila 1 con las
//     columnas y las mismas filas. El precio se escribe como NÚMERO y el texto como CADENA;
//   - las tres descargas, subidas a su puerta (el JSON a I14; el CSV y el XLSX a I15), producen
//     el MISMO catálogo;
//   - un fallo al generar el archivo daría 500 {"error":"no se pudo generar la plantilla"}; con
//     la plantilla del módulo no se alcanza.
//
// I17, EL PROMPT: `GET …/catalog/import/prompt`. El texto que el dueño del negocio pega en SU
// LLM junto con la plantilla y su lista de productos. Existe como ENDPOINT y no como texto en el
// BFF porque el prompt está versionado junto al contrato: copiado en otro repo sería una segunda
// fuente.
//
//   - 200, Content-Type application/json, con {"format","version","prompt"} en ese orden:
//     catalogimport.ImportFormat ("wapp.catalog_import"), catalogimport.ImportVersion (número) y
//     catalogimport.ImportPrompt() TAL CUAL. Viaja con el format y la VERSIÓN para que una
//     pantalla que cachee el texto pueda notar que el contrato cambió;
//   - no lee ningún parámetro: la query se ignora.
//
// 🟡 El texto del prompt es el del design §6 y NO se ha probado contra un LLM externo real
// (deuda visible de T3.2, decidida el 2026-08-06). Se sirve literal: no se «mejora».
//
// Fallo de cableado: k.MW nil con las tres dependencias presentes hace panic AL MONTAR, con un
// mensaje que nombra MountCatalogTemplate (ver Common). Si falta una dependencia no se monta
// nada y k ni se mira.
func MountCatalogTemplate(c *Cara, k Common, d CatalogImportDeps) {
	if !catalogImportMountable(d) {
		return
	}
	mustHaveMW(k, "MountCatalogTemplate")

	// La plantilla y el prompt son LECTURA (content.read) y no tocan la BD: son el
	// contrato dicho de dos maneras. Cuelgan de la misma condición que el POST —y por
	// tanto aparecen y desaparecen con él— porque son la primera mitad del mismo
	// acto: repartir la plantilla de un import que no está montado mandaría al
	// operador a llenarla para encontrarse un 404 al subirla (T3.2).
	canImport := entitlements.RequireFeature(d.Entitlements, entitlements.FeatureCatalogImport)
	c.Handle("GET /api/v1/catalog/import/template", protectRead(k, "content.read", canImport(catalogTemplateHandler())))
	c.Handle("GET /api/v1/catalog/import/prompt", protectRead(k, "content.read", canImport(catalogTemplatePromptHandler())))
}

// catalogTemplateFormatJSON (formatJSON en la cara vieja) es el formato por defecto de la
// plantilla: el contrato mismo. Los otros dos (exportFormatCSV/exportFormatXLSX) los declara el
// export de solicitudes y se reusan tal cual: dos juegos de nombres para "csv" en la misma API
// sería una trampa para quien la consume.
const catalogTemplateFormatJSON = "json"

// catalogTemplateFilename (templateFilename en la cara vieja) es el nombre con el que se
// descarga la plantilla. Sin fecha, a diferencia del export: dos exports distintos no deben
// pisarse en la carpeta de descargas, pero dos plantillas SÍ —la nueva sustituye a la vieja, y
// tener «catalogo-plantilla-20260806.json» y otras cuatro al lado es justo cómo alguien
// acaba llenando la caducada.
const catalogTemplateFilename = "catalogo-plantilla"

// catalogTemplateHandler sirve
// GET /api/v1/catalog/import/template?format=json|csv|xlsx: la plantilla de
// ejemplo que el dueño del negocio descarga para partir de ella (patrón
// buildImportTemplate de EduGo 038, design §1).
//
// LA SIRVE EL BACKEND, no una copia pegada en la consola, y ese es el punto entero
// del patrón: la plantilla sale de los MISMOS structs del contrato
// (catalogimport.BuildTemplate), así que no puede describir un contrato que este
// servidor ya no acepta. Una plantilla estática en el front se queda vieja el día
// que sube ImportVersion y reparte documentos que el import rechaza en bloque.
//
// Las respuestas, una a una, están en el contrato de MountCatalogTemplate.
func catalogTemplateHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		format := r.URL.Query().Get("format")
		if format == "" {
			format = catalogTemplateFormatJSON
		}

		var buf bytes.Buffer
		var err error
		switch format {
		case catalogTemplateFormatJSON:
			err = catalogTemplateWriteJSON(&buf)
		case exportFormatCSV:
			err = exportWriteCSV(&buf, catalogimport.TabularColumns(), catalogimport.TemplateSheetRows())
		case exportFormatXLSX:
			err = exportWriteXLSX(&buf, catalogimport.TabularSheetName,
				catalogimport.TabularColumns(), catalogimport.TemplateSheetRows())
		default:
			writeError(w, http.StatusBadRequest, "format inválido: usa json, csv o xlsx")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "no se pudo generar la plantilla")
			return
		}

		w.Header().Set("Content-Type", catalogTemplateContentType(format))
		w.Header().Set("Content-Disposition",
			fmt.Sprintf("attachment; filename=%q", catalogTemplateFilename+"."+format))
		w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
		w.WriteHeader(http.StatusOK)
		if _, werr := w.Write(buf.Bytes()); werr != nil {
			return
		}
	})
}

// catalogTemplateWriteJSON (writeTemplateJSON en la cara vieja) serializa la plantilla
// INDENTADA y con salto final. No es cosmética: este archivo lo abre y lo edita a mano el dueño
// del negocio, y una sola línea de 900 caracteres es ilegible en cualquier editor y en la caja
// de texto de un LLM.
func catalogTemplateWriteJSON(w io.Writer) error {
	body, err := json.MarshalIndent(catalogimport.BuildTemplate(), "", "  ")
	if err != nil {
		return fmt.Errorf("plantilla: serializar el documento: %w", err)
	}
	if _, werr := w.Write(append(body, '\n')); werr != nil {
		return fmt.Errorf("plantilla: escribir el documento: %w", werr)
	}
	return nil
}

// catalogTemplateContentType (templateContentType en la cara vieja) es el MIME de cada formato
// de la plantilla.
func catalogTemplateContentType(format string) string {
	if format == catalogTemplateFormatJSON {
		return "application/json; charset=utf-8"
	}
	return exportContentType(format)
}

// catalogTemplatePromptResponse (catalogPromptResponse en la cara vieja) es el prompt-plantilla
// servido a la consola.
//
// Viaja con el format y la VERSIÓN del contrato al que corresponde, y no por
// simetría: el BFF puede cachear este texto y una pantalla que muestre el prompt de
// la versión 1 cuando el servidor ya valida la 2 mandaría al dueño a generar
// documentos que se rechazan. Con la versión al lado, la pantalla puede notarlo.
type catalogTemplatePromptResponse struct {
	Format  string `json:"format"`
	Version int    `json:"version"`
	Prompt  string `json:"prompt"`
}

// catalogTemplatePromptHandler (catalogPromptHandler en la cara vieja) sirve
// GET /api/v1/catalog/import/prompt: el texto que el dueño del negocio pega en SU LLM (design
// §6) junto con la plantilla y su lista de productos.
//
// Existe como ENDPOINT y no como texto en la plantilla del BFF porque el prompt
// está versionado junto al contrato (vive en el módulo catálogo, con un test que lo ata a
// ImportVersion). Copiado en un HTML de otro repo sería una
// segunda fuente, y envejecería sin que nadie se enterase: el BFF lo pide y lo
// muestra como texto copiable.
func catalogTemplatePromptHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, catalogTemplatePromptResponse{
			Format:  catalogimport.ImportFormat,
			Version: catalogimport.ImportVersion,
			Prompt:  catalogimport.ImportPrompt(),
		})
	})
}
