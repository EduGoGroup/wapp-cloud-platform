// Porta internal/publicapi/tenantcontent.go @ 724f3035 (204 líneas, entero) y el registro de
// I6–I10 (internal/publicapi/publicapi.go @ 724f3035, Register, líneas 470-483; los campos
// Content y ContentMaxBytes de MediaDeps, líneas 112-118).
//
// tenantcontent.go — EL CRUD DE CONTENIDO DINÁMICO POR-TENANT (Plan 018 · T6, R7; mapa §2.9,
// I6–I10): los blobs JSONB de public.tenant_content que alimentan el adapter content.JSON del
// Motor (source:json,ref) o una ref de media por-tenant. Todo acotado al tenant del token
// (INV-8): el aislamiento lo garantiza el store (PK/WHERE tenant_id), NUNCA el cuerpo.
//
// En el rojo solo existen el puerto, TenantContentDeps y MountTenantContent; los cuatro handlers,
// la fila del listado y el techo efectivo (tenantContentBytes en la cara vieja) son no exportados
// y nacen con el verde (05 E-4, P6). Sus promesas viven en el comentario de MountTenantContent.

package apipublica

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// TenantContentStore es el puerto MÍNIMO del CRUD de contenido dinámico por-tenant que la cara
// consume. Lo satisfacen *store.PostgresRepository y *store.MemoryRepository del módulo
// conversación NUEVO. TODAS las operaciones van acotadas al tenant: una ref de otro tenant no
// existe para este puerto.
type TenantContentStore interface {
	// UpsertTenantContent registra o reemplaza el blob de (tenantID, ref).
	UpsertTenantContent(ctx context.Context, tenantID, ref string, blob []byte) error
	// GetTenantContent devuelve el blob de (tenantID, ref), o store.ErrTenantContentNotFound si
	// ese tenant no tiene esa ref.
	GetTenantContent(ctx context.Context, tenantID, ref string) ([]byte, error)
	// ListTenantContent devuelve las cabeceras (ref y marcas de tiempo, sin blob) del tenant.
	ListTenantContent(ctx context.Context, tenantID string) ([]store.TenantContentSummary, error)
	// DeleteTenantContent borra el blob de (tenantID, ref), o devuelve
	// store.ErrTenantContentNotFound si no existía.
	DeleteTenantContent(ctx context.Context, tenantID, ref string) error
}

// TenantContentDeps es lo que I6–I10 necesitan. En la cara vieja eran los campos Content y
// ContentMaxBytes de publicapi.MediaDeps.
type TenantContentDeps struct {
	// Content es el store de los blobs. nil NO desmonta las rutas: ver MountTenantContent.
	Content TenantContentStore
	// MaxBytes (ContentMaxBytes en la cara vieja) es el techo del blob de I6/I7, en bytes. <= 0 ⇒
	// catalogimport.DefaultMaxJSONBytes (1 MiB, el valor que este endpoint tenía fijo). Se cablea
	// desde config.TenantContentConfig (WAPP_TENANT_CONTENT_MAX_BYTES).
	//
	// UNA SOLA FUENTE, A PROPÓSITO: este PUT y el import de catálogo (Plan 041 · Ola 3) escriben
	// en la MISMA tabla, así que dos techos independientes abren una trampa concreta: subir el
	// del import, importar bien, y que después este PUT rechace ESE MISMO blob. El arranque le
	// da el mismo número a los dos.
	MaxBytes int64
}

// MountTenantContent registra en c las CINCO rutas del contenido de tenant, SIEMPRE: no tienen
// condición de montaje, igual que en la cara vieja. Las cinco van juntas (trampa T-2: un camino
// con varios métodos no se parte):
//
//	I6  PUT    /api/v1/tenant-content/{ref}   W "content.write" (recurso "tenant_content")
//	I7  POST   /api/v1/tenant-content/{ref}   W "content.write" (recurso "tenant_content")
//	I8  DELETE /api/v1/tenant-content/{ref}   W "content.write" (recurso "tenant_content")
//	I9  GET    /api/v1/tenant-content         R "content.read"
//	I10 GET    /api/v1/tenant-content/{ref}   R "content.read"
//
// Cadenas W y R de Common: 401 sin token; 403 {"error":"permiso denegado"} sin el permiso o con
// un token sin empresa; ningún registro en esos dos. Las W dejan UN registro por petición que
// pasa la cadena —"success" con el 2xx, "failure" con Meta {"status":<código>} en cualquier 4xx
// o 5xx del handler—; las R, ninguno. Sin gate de feature. AUDITORÍA CERO PII: action y
// resource, NUNCA la ref ni el contenido del blob.
//
// COMÚN A LAS CINCO. El tenant es SIEMPRE el del token (INV-8): uno en la query o en el cuerpo
// no cuenta. La {ref} llega al store tal cual la entrega el mux (ya sin el escapado de URL:
// `men%C3%BA` ⇒ `menú`), sin recortar ni validar su forma. Con d.Content nil las cinco existen y
// responden 500 {"error":"store de contenido no configurado"} ANTES de mirar la ref o el cuerpo.
// Ningún 500 repite el error del store. Cada petición llama al store UNA vez como mucho, con el
// contexto de la petición.
//
// I6 e I7 — UPSERT. Son el MISMO handler (POST es un alias de PUT): registran el cuerpo CRUDO
// como blob de (tenant, ref). El cuerpo ES el blob que luego lee el Motor. En orden:
//
//  1. cuerpo de más de `techo` bytes (d.MaxBytes, o 1 MiB si no es positivo) ⇒ 413
//     {"error":"el contenido excede el tamaño máximo de <techo> bytes","max_bytes":<techo>}. El
//     techo se aplica LEYENDO, antes de que encoding/json vea nada (catalogimport.ReadLimited:
//     el mismo mecanismo y el mismo número que el import): un cuerpo que se pasa es 413 aunque
//     no sea JSON, y uno de exactamente `techo` bytes entra;
//  2. otro fallo al leer el cuerpo ⇒ 400 {"error":"no se pudo leer el cuerpo"};
//  3. cuerpo vacío o que no es JSON válido ⇒ 400 {"error":"el cuerpo debe ser un JSON válido (el
//     blob de contenido)"}. Vale CUALQUIER valor JSON (objeto, lista, número, cadena, `null`):
//     la forma del blob es del Motor, no de esta puerta. En ningún 4xx se toca el store;
//  4. el store falla ⇒ 500 {"error":"no se pudo registrar el contenido"};
//  5. 200 {"ref":<ref>}. Al store llegan los bytes del cuerpo TAL CUAL (con sus espacios y su
//     orden de claves): no se re-serializa.
//
// I9 — LISTADO. 200 con un ARREGLO de {"ref","created_at","updated_at"} en el orden que dio el
// store; `[]` (nunca `null`) si el tenant no tiene contenido. Los instantes van en RFC3339 UTC
// con precisión de segundos, y un instante cero OMITE su clave. No lleva el blob (se pide con
// I10). El store falla ⇒ 500 {"error":"no se pudo listar el contenido"}.
//
// I10 — LECTURA. 200 con el blob como application/json. errors.Is(err,
// store.ErrTenantContentNotFound) ⇒ 404 {"error":"contenido no encontrado"} —también cuando la
// ref es de OTRO tenant: el store filtra y para este tenant no existe—; otro fallo ⇒ 500
// {"error":"no se pudo leer el contenido"}.
//
// Rareza portada tal cual: el blob NO sale byte a byte. Se emite con json.Marshal de un
// json.RawMessage, que lo COMPACTA (quita los espacios entre tokens) y escapa `<`, `>` y `&`
// como <, > y &: es el mismo valor JSON, no los mismos bytes que entraron por
// I6. Y un blob almacenado que no fuera JSON (I6 no lo deja entrar) responde el 500 en texto
// plano «codificando respuesta».
//
// I8 — BORRADO. 204 sin cuerpo al borrar; errors.Is(err, store.ErrTenantContentNotFound) ⇒ 404
// {"error":"contenido no encontrado"} (no existía, o es de otro tenant); otro fallo ⇒ 500
// {"error":"no se pudo borrar el contenido"}.
//
// Defensas que no se alcanzan desde fuera: una identidad sin empresa que llegara a un handler
// recibiría 401 {"error":"autenticación requerida"} (RequirePermission corta antes), y una {ref}
// vacía en I6, I7, I8 o I10, 400 {"error":"ref requerida en la ruta"} (el mux no casa un
// comodín con un segmento vacío).
//
// Fallo de cableado: k.MW nil hace panic AL MONTAR (ver Common), con un mensaje que nombra
// MountTenantContent. d.Content NO se comprueba al montar.
func MountTenantContent(c *Cara, k Common, d TenantContentDeps) {
	panic(pendiente.Implementar("apipublica.MountTenantContent"))
}
