// Porta internal/publicapi/catalogtabular.go @ 724f3035 (286 líneas, entero) y el registro de
// I15 (internal/publicapi/publicapi.go @ 724f3035, registerCatalogImport, líneas 1106-1113).
//
// catalogtabular.go — LA PUERTA DE LA PLANILLA DEL IMPORT DE CATÁLOGO (Plan 041 · Ola 3 · T3.4,
// D-041.9; mapa §2.9, I15): POST /api/v1/catalog/import/tabular. Es el otro extremo de
// export.go: allí las filas se convierten en un CSV o un XLSX que se descarga; aquí un CSV o un
// XLSX que se sube vuelve a ser filas. Las dos mitades viven en la capa que sabe de bytes y de
// transporte, para que el contrato tabular de catalogimport siga sin saber en qué formato viajó.
//
// En el rojo solo existe MountCatalogTabular; el handler, la lectura del multipart y los
// lectores de CSV y XLSX nacen con el verde (05 E-4, P6). Los puertos y las dependencias son los
// de catalogimport.go.

package apipublica

import (
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
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
	panic(pendiente.Implementar("apipublica.MountCatalogTabular"))
}
