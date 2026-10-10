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
// En el rojo solo existe MountCatalogTemplate; los dos handlers y sus auxiliares nacen con el
// verde (05 E-4, P6). Las dependencias son las de catalogimport.go.

package apipublica

import (
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
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
	panic(pendiente.Implementar("apipublica.MountCatalogTemplate"))
}
