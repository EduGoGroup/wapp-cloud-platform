// Porta internal/flujos/admin/handlers.go @ 724f3035
//
// No se porta `Register` (D-F8-2): solo lo llamaban tests (deuda D-7); las dos rutas
// las monta el arranque. El comentario de paquete vive en doc.go.

package admin

import (
	"context"
	"net/http"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// DefinitionStore persiste una definición de flujo como versión nueva y devuelve la
// versión asignada. Lo satisfacen *store.PostgresRepository y *store.MemoryRepository
// (su método InsertDefinition encaja).
type DefinitionStore interface {
	InsertDefinition(ctx context.Context, tenantID string, f model.Flow) (version int, err error)
}

// ModuleTypeSource expone los tipos de nodo registrados por los módulos enchufables.
// La validación de esquema del alta (model.ParseAndValidate) los acepta de forma LAXA,
// de modo que un flujo con un nodo de módulo (p. ej. "cart") pasa el alta sin acoplar
// `model` a los módulos concretos. Lo satisface *modules.Registry.
type ModuleTypeSource interface {
	Types() []string
}

// Starter abre una conversación por API (crea el estado en el nodo inicial y envía el
// menú) y devuelve el Ack del último texto emitido. Lo satisface *runtime.Runtime (su
// método Start encaja). Recibe la identidad del contacto como contact.Ref ya validada
// y normalizada (Plan 010, design.md §7).
type Starter interface {
	Start(ctx context.Context, tenantID, flowID, sessionID string, ref contact.Ref) (*cloudlinkv1.Ack, error)
}

// DefinitionHandler devuelve el handler de POST /admin/flows: publica una definición
// de flujo, validada, como versión nueva del tenant del token.
//
// mods aporta los tipos de nodo de los módulos enchufables y puede ser nil (solo tipos
// core). Sus Types() se leen UNA vez, al construir el handler, no en cada petición: un
// módulo registrado después no se ve (se porta como está).
//
// El cuerpo es {"definition": <objeto-flujo>}; cualquier otro campo se ignora, y en
// particular un `tenant_id` (INV-8). Las comprobaciones van en este orden y la primera
// que falla responde:
//
//  1. 405 "método no permitido (usar POST)" si el método no es POST (antes que la
//     autenticación).
//  2. 401 "autenticación requerida" si no hay Identity en el contexto o su TenantID
//     está vacío.
//  3. 400 "cuerpo JSON inválido" si el cuerpo no decodifica.
//  4. 400 "definition es requerida" si falta `definition`.
//  5. 400 "definición de flujo inválida: " + el texto del error de
//     model.ParseAndValidate(definition, tipos de módulo...). Ese error ya empieza por
//     el texto de model.ErrInvalidFlow, así que el prefijo sale DOS veces (se porta).
//  6. 400 "definición de flujo inválida: " + el error de mods.ValidateModuleNodes(flujo),
//     SOLO si mods tiene ese método (capacidad opcional, Plan 027 · T6, cierra H11:
//     un cart sin catálogo se rechaza en el alta). Una fuente sin él no valida la
//     estructura de los nodos de módulo.
//  7. 500 "no se pudo persistir la definición" si InsertDefinition falla.
//
// Nada se persiste si falla un paso anterior al 7. InsertDefinition recibe el contexto
// de la petición, el tenant de la Identity y el flujo ya validado.
//
// Éxito: 201 con {"flow_id": <el de la definición>, "version": <la que devolvió el
// store>} —la versión es la ASIGNADA, no la que traía el cuerpo—.
func DefinitionHandler(store DefinitionStore, mods ModuleTypeSource) http.Handler {
	panic(pendiente.Implementar("admin.DefinitionHandler"))
}

// StartHandler devuelve el handler de POST /admin/flows/start: abre una conversación
// por API (decisión C) para el tenant del token y refleja el Ack del envío del menú.
//
// El cuerpo es {flow_id, session_id, contact_ref{kind,value}} o, por compatibilidad
// (design.md §10.F), `contact` como cadena plana, que equivale a {kind: phone_e164,
// value: contact}. Manda contact_ref si trae `value` no vacío; con contact_ref ausente
// o de value vacío se usa el alias. Un `tenant_id` del cuerpo se ignora (INV-8).
//
// Comprobaciones, en orden; la primera que falla responde y Start NO se llama:
//
//  1. 405 "método no permitido (usar POST)".
//  2. 401 "autenticación requerida" (sin Identity o con TenantID vacío).
//  3. 400 "cuerpo JSON inválido".
//  4. 400 "flow_id y session_id son requeridos" si falta cualquiera de los dos.
//  5. 400 "se requiere contact_ref {kind,value} o contact (alias phone_e164)" si no
//     viene ninguna identidad.
//  6. 400 "contact_ref inválida: " + el texto del error de contact.NewRef (que ya
//     empieza por "contact_ref inválida": el prefijo sale dos veces, se porta).
//
// Después llama a Start con el contexto de la petición, el tenant de la Identity, el
// flow_id y el session_id TAL CUAL llegaron y la Ref normalizada. Si Start no falla:
// 200 con {"acked_command_id", "ok", "error"} tomados del Ack; `error` se omite si
// está vacío. Un Ack con ok=false sigue siendo un 200, y un Ack nil da
// {"acked_command_id":"","ok":false}.
//
// Si Start falla, el error se traduce así (errors.Is/As, también envuelto; gana el
// primer caso que case, en este orden):
//
//   - runtime.ErrConversationExists → 409 "ya existe una conversación viva para la
//     clave".
//   - runtime.ErrDurableFlowNeedsEvent → 409 con un texto DISTINTO (Plan 054 · T2.5):
//     "el flujo tiene contenido durable (cart/survey): su evento nace en la
//     conversación, no por esta API. Configura una regla event_start para este flujo
//     (POST /api/v1/triggers) para que el cliente lo arranque escribiendo su palabra
//     clave; no reintentes esta llamada, seguirá devolviendo 409". El texto es la
//     única superficie para distinguir los dos 409.
//   - session.ErrSessionOffline (internal/modulos/edge/session; por IDENTIDAD, es la
//     misma variable que httpapi.ErrSessionOffline, trampa T-8) → 502 "sesión offline:
//     no hay stream vivo para el Edge".
//   - un error que, por errors.As, tenga `StreamCaido() bool` y devuelva true (Plan
//     050 · T2.4; duck-typing, sin importar el Gateway) → 504 "el stream del Edge se
//     cerró antes del ack: la conversación YA quedó abierta y el comando de su primer
//     mensaje viajó al Edge, así que no se sabe si el cliente llegó a recibirlo. NO
//     reintentes este arranque —devolverá 409—: comprueba la conversación y, si el
//     primer mensaje no salió, continúala sobre la que ya existe". Con StreamCaido()
//     false el error sigue a los casos de abajo según su causa.
//   - context.DeadlineExceeded o context.Canceled → 504 "timeout esperando el ack del
//     Edge".
//   - cualquier otro → 500 "no se pudo iniciar la conversación".
func StartHandler(starter Starter) http.Handler {
	panic(pendiente.Implementar("admin.StartHandler"))
}
