// Porta internal/publicapi/media.go @ 724f3035 (126 líneas, entero) y el registro de I5
// (internal/publicapi/publicapi.go @ 724f3035, Register, líneas 463-468; el campo Media de
// MediaDeps, líneas 109-111).
//
// media.go — MEDIA POR API (Plan 018 · T6, R7; mapa §2.9, I5): POST /api/v1/media/upload-url
// presigna una URL PUT de corta vida para subir a R2 un archivo (PDF o imagen) que después se
// referencia en un flujo (nodo media) o en un blob de tenant_content.
//
// Zero-knowledge (ADR-0007/0009): la plataforma solo entrega una URL firmada; NUNCA expone las
// credenciales de R2 al cliente, y quien sube o descarga lo hace sin llaves.
//
// En el rojo solo existen el puerto, MediaDeps y MountMedia; el handler, el cuerpo de la petición
// y la fábrica de la key (mediaObjectKey y sanitizeFilename en la cara vieja) son no exportados y
// nacen con el verde (05 E-4, P6). Sus promesas viven en el comentario de MountMedia.

package apipublica

import (
	"context"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// MediaPresignUploader (PresignUploader en la cara vieja) es el puerto MÍNIMO que la cara consume
// para presignar una subida a R2: el subconjunto de objectstore.PresignClient (Plan 017) que hace
// falta aquí. Lo satisface el cliente de presign real de internal/platform/storage/objectstore.
//
// UN método, a propósito: desde esta puerta no se puede firmar una descarga ni borrar un objeto.
type MediaPresignUploader interface {
	// GenerateUploadURL devuelve una URL PUT prefirmada de corta vida para el objeto key y el
	// instante en que deja de valer. Un error es que la firma no se pudo hacer.
	GenerateUploadURL(ctx context.Context, key string) (url string, expiresAt time.Time, err error)
}

// MediaDeps es lo que I5 necesita. En la cara vieja era el campo Media de publicapi.MediaDeps
// (que agrupaba además el contenido de tenant y el import de catálogo: aquí cada área lleva sus
// propias deps, en tenantcontent.go y catalogimport.go).
type MediaDeps struct {
	// Uploader presigna la subida (Media en la cara vieja). nil NO desmonta la ruta: ver
	// MountMedia.
	Uploader MediaPresignUploader
}

// MountMedia registra en c la ruta I5, "POST /api/v1/media/upload-url", SIEMPRE: no tiene
// condición de montaje, igual que en la cara vieja. Con d vacío la ruta existe y responde 500
// (abajo), no el 404 de una ruta inexistente.
//
// Cadena W de Common, permiso "media.upload", recurso de auditoría "media": 401 sin token; 403
// {"error":"permiso denegado"} sin el permiso o con un token sin empresa; ningún registro en
// esos dos; y UN registro por petición que pasa la cadena —"success" con el 200, "failure" con
// Meta {"status":<código>} en cualquier 4xx o 5xx del handler—. Sin gate de feature. CERO PII en
// la auditoría: action y resource, nunca el nombre del archivo.
//
// LA PETICIÓN. Cuerpo JSON {"filename","mime"}: el nombre visible del archivo y su tipo de
// contenido. El tenant NO viaja en él (INV-8): sale del token, y un `tenant_id` en el cuerpo o
// en la query se ignora, como cualquier otro campo que el contrato no publica.
//
// LOS DESENLACES, en este orden (gana el primero que case):
//
//  1. d.Uploader nil ⇒ 500 {"error":"almacén de objetos no configurado"}. 🔴 Va ANTES de leer el
//     cuerpo: sin almacén, un cuerpo ilegible también es 500 y no 400;
//  2. cuerpo que no se decodifica (vacío, JSON roto, un tipo que no casa, o un valor que no es
//     un objeto) ⇒ 400 {"error":"cuerpo JSON inválido"}. Solo se lee el PRIMER valor JSON: lo
//     que venga detrás no se mira (`{…} basura` es válido). El cuerpo no tiene techo propio;
//  3. `filename` o `mime` vacíos tras recortarles los espacios de los bordes (también `null` y
//     `{}`) ⇒ 400 {"error":"filename y mime son requeridos"}. En ningún 400 se firma nada;
//  4. d.Uploader.GenerateUploadURL falla ⇒ 502 {"error":"no se pudo presignar la subida"}, SIN
//     repetir el error;
//  5. 200 {"url","key","expires_at"}, en ese orden y con Content-Type application/json: la URL
//     tal cual la dio el puerto, la key firmada y el instante de expiración en RFC3339 UTC con
//     precisión de segundos (un instante en otra zona se normaliza a UTC).
//
// El puerto se llama UNA vez, con el contexto de la petición (sin plazo propio) y con la MISMA
// key que viaja en la respuesta.
//
// LA KEY. "wapp/media/<tenant del token>/<uuid>-<nombre saneado>":
//
//   - el prefijo "wapp/media" es el namespace de wApp en el bucket compartido (Plan 017) y va
//     compilado, no configurado;
//   - el segmento del tenant AÍSLA el objeto (INV-8): dos tenants que suben el mismo nombre
//     reciben keys bajo prefijos distintos;
//   - el uuid (aleatorio, forma canónica de 36 caracteres) la hace no adivinable y evita
//     colisiones: dos peticiones idénticas dan dos keys distintas;
//   - es EXACTAMENTE la que el autor coloca en `content.key` de un nodo media, y la que el runtime
//     presigna VERBATIM para descargar: su forma no se transforma en ningún otro sitio.
//
// EL NOMBRE SANEADO evita separadores y traversal en la key. Del `filename` ya recortado:
//
//   - cada `\` cuenta como `/`, y se toma solo el último segmento del camino
//     (`../../etc/passwd` ⇒ `passwd`; `C:\docs\lista.pdf` ⇒ `lista.pdf`; `dir/` ⇒ `dir`);
//   - a ese segmento se le recortan los espacios de los bordes; si queda vacío, `.` o `..` ⇒
//     `file`;
//   - cada carácter que no sea letra ASCII, dígito ASCII, `.`, `-` o `_` se sustituye por UN `_`
//     (`lista precios.pdf` ⇒ `lista_precios.pdf`; `menú.pdf` ⇒ `men_.pdf`: un `_` por carácter,
//     no por byte). El `mime` no entra en la key ni se valida: solo tiene que venir.
//
// Rareza portada tal cual: un `filename` que es solo `/` no cae en `file` sino en `_` (su último
// segmento es el propio separador, que luego se sustituye).
//
// Defensa que los tokens de sharedjwt no alcanzan (RequirePermission corta antes): una identidad
// sin empresa que llegara al handler recibiría 401 {"error":"autenticación requerida"} sin
// firmar nada.
//
// Fallo de cableado: k.MW nil hace panic AL MONTAR (ver Common), con un mensaje que nombra
// MountMedia. d.Uploader NO se comprueba al montar.
func MountMedia(c *Cara, k Common, d MediaDeps) {
	panic(pendiente.Implementar("apipublica.MountMedia"))
}
