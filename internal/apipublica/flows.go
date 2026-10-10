// Porta internal/publicapi/flows.go @ 5ba7f419 (248 líneas, entero) y el registro de I1–I4 e
// I11–I13 (internal/publicapi/publicapi.go @ 5ba7f419, Register, líneas 446-461 y 484-496), con
// el puerto FlowStore y los campos Flows, Modules, Starter, Triggers y TriggersDurableFlow de
// publicapi.Deps (líneas 53-61, 70-75 y 150-158).
//
// flows.go — LOS FLUJOS Y SUS REGLAS DE DISPARO POR LA API PÚBLICA (mapa §2.9, filas I1–I4 e
// I11–I13): publicar una definición, listarlas, leer una, arrancar una conversación, y el CRUD de
// las reglas que deciden qué flujo arranca con qué palabra.
//
// De las siete rutas, CUATRO no tienen handler propio: I1 e I11–I13 sirven TAL CUAL los handlers
// de internal/modulos/conversacion/admin, los mismos que /admin/flows y /admin/triggers en :8100.
// Aquí solo se les pone delante la cadena de la cara. Las otras tres (I2, I3, I4) son de este
// fichero.
//
// 🔴 I4 NO reusa admin.StartHandler aunque haga casi lo mismo: toma el flow_id de la RUTA (no del
// cuerpo), su 400 de campos habla solo de session_id y no mira el método. Comparte con él el
// motor (admin.Starter) y la tabla de errores de arranque, texto a texto.
//
// I5–I10 (media y contenido de tenant) e I14–I19 (catálogo y eventos de conversación) viven en
// media.go, tenantcontent.go, catalog*.go y conversationevent*.go.
//
// En el rojo solo existen el puerto, FlowsDeps y MountFlows; los handlers de I2–I4, sus cuerpos
// y la tabla de errores de arranque nacen con el verde (05 E-4, P6).

package apipublica

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/admin"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// FlowsStore (FlowStore en la cara vieja) agrupa las operaciones sobre definiciones de flujo que
// consume la API pública. Todas van acotadas al tenant que reciben. Lo satisfacen
// *store.PostgresRepository y *store.MemoryRepository del módulo conversación NUEVO.
//
// InsertDefinition encaja además con admin.DefinitionStore: por eso el mismo valor sirve a I1
// (admin.DefinitionHandler) sin adaptador.
type FlowsStore interface {
	// InsertDefinition guarda f como versión nueva del flujo en tenantID y devuelve la versión
	// ASIGNADA. Solo lo llama I1, a través de admin.DefinitionHandler.
	InsertDefinition(ctx context.Context, tenantID string, f model.Flow) (version int, err error)
	// LatestDefinition devuelve la definición vigente (última versión) de flowID en tenantID, o
	// store.ErrDefinitionNotFound si ese tenant no tiene ese flujo. Lo usa I3.
	LatestDefinition(ctx context.Context, tenantID, flowID string) (model.Flow, error)
	// ListDefinitions devuelve los flujos de tenantID, cada uno con su última versión. Lo usa I2.
	ListDefinitions(ctx context.Context, tenantID string) ([]store.FlowSummary, error)
}

// FlowsDeps es lo que I1–I4 e I11–I13 necesitan. En la cara vieja eran publicapi.FlowDeps
// (Flows, Modules, Starter) y los campos Triggers y TriggersDurableFlow de publicapi.Deps.
type FlowsDeps struct {
	// Flows es el store de definiciones (I1, I2, I3). NO se comprueba al montar.
	Flows FlowsStore
	// Modules da los tipos de nodo de los módulos enchufables, para la validación del alta (I1).
	// nil es válido: solo tipos core.
	Modules admin.ModuleTypeSource
	// Starter es el motor de flujos (I4): el mismo que sirve /admin/flows/start. NO se comprueba
	// al montar.
	Starter admin.Starter
	// Triggers es el store de reglas de disparo. nil ⇒ I11–I13 NO se montan.
	Triggers admin.TriggerStore
	// TriggersDurableFlow contesta «¿el flujo de esta regla tiene contenido durable?» a los tres
	// handlers del CRUD de reglas (Plan 054 · D-054.6/D-054.8). nil es válido (ningún flujo es
	// durable, fail-open) y NO condiciona el montaje.
	TriggersDurableFlow admin.DurableFlowChecker
}

// MountFlows registra en c las rutas de flujos y de reglas de disparo:
//
//	I1   "POST /api/v1/flows"             W  flows.create     recurso "flow"     SIEMPRE
//	I2   "GET /api/v1/flows"              R  flows.read                          SIEMPRE
//	I3   "GET /api/v1/flows/{id}"         R  flows.read                          SIEMPRE
//	I4   "POST /api/v1/flows/{id}/start"  W  flows.start      recurso "flow"     SIEMPRE
//	I11  "POST /api/v1/triggers"          W  triggers.create  recurso "trigger"  si d.Triggers != nil
//	I12  "GET /api/v1/triggers"           R  triggers.read                       si d.Triggers != nil
//	I13  "DELETE /api/v1/triggers/{id}"   W  triggers.delete  recurso "trigger"  si d.Triggers != nil
//
// CONDICIÓN DE MONTAJE. I1–I4 no tienen ninguna: con d vacío las cuatro existen igual, como en la
// cara vieja. I11–I13 van las tres o ninguna, según d.Triggers: sin el store NO se registra
// ningún patrón de reglas y sus caminos dan el 404 de ruta inexistente (trampa T-11), no un 500.
// d.TriggersDurableFlow no entra en la condición.
//
// CADENAS, las de Common (W y R), sin gate de feature en ninguna de las siete: 401
// {"error":"autenticación requerida"} sin token; 403 {"error":"permiso denegado"} sin el permiso
// o con un token sin empresa; ningún registro de auditoría en esos dos. Las W dejan EXACTAMENTE
// un registro por petición que pasa el permiso (Action = el permiso, Resource = el recurso,
// "success" con estado < 400 y "failure" con Meta {"status":<código>} si no); las R, ninguno.
// Cada permiso es el suyo: flows.read no abre I1 ni I4. Otro método sobre un camino montado ⇒ el
// 405 del mux.
//
// EL TENANT ES SIEMPRE EL DEL TOKEN (INV-8) en las siete: uno en la query o en el cuerpo no
// cuenta, y ninguna respuesta lo lleva.
//
// I1 · PUBLICAR UNA DEFINICIÓN. El handler es admin.DefinitionHandler(d.Flows, d.Modules) tal
// cual, y su contrato manda. Lo que se ve desde aquí: cuerpo {"definition": <flujo>}; 201
// {"flow_id","version"} con la versión que ASIGNÓ el store; 400 {"error":"cuerpo JSON inválido"},
// 400 {"error":"definition es requerida"}, 400 con "definición de flujo inválida: …" y 500
// {"error":"no se pudo persistir la definición"}. 🔴 Los tipos de d.Modules se leen UNA vez, AL
// MONTAR, no en cada petición (un módulo registrado después no se ve; se porta como está).
//
// I2 · LISTAR. d.Flows.ListDefinitions(tenant del token), UNA vez y con el contexto de la
// petición:
//
//   - falla ⇒ 500 {"error":"no se pudieron listar los flujos"}, sin repetir el error;
//   - 200 con un arreglo JSON, en el orden que dio el store, de {"flow_id","version","created_at"}.
//     Sin flujos es `[]`, nunca `null`. "created_at" va en RFC3339 UTC con precisión de segundos
//     (un instante en otra zona se normaliza a UTC) y se OMITE si el instante es el cero.
//
// I3 · LEER UNA. d.Flows.LatestDefinition(tenant del token, {id} de la ruta tal cual), UNA vez:
//
//   - store.ErrDefinitionNotFound (errors.Is, también envuelto) ⇒ 404 {"error":"flujo no
//     encontrado"}. Es también lo que recibe quien pide el flujo de OTRO tenant: el store filtra
//     por tenant y no se distingue «no existe» de «no es tuyo»;
//   - otro error ⇒ 500 {"error":"no se pudo leer el flujo"};
//   - 200 con la definición vigente, serializada tal cual es model.Flow
//     ({"flow_id","version","initial","nodes"}).
//
// I4 · ARRANCAR UNA CONVERSACIÓN del flujo {id} para un contacto y enviar su menú inicial. El
// flow_id va en la RUTA; un `flow_id` (o un `tenant_id`) en el cuerpo se ignora. Cuerpo JSON sin
// techo propio: {"session_id","contact_ref":{"kind","value"},"contact"}; solo se lee el PRIMER
// valor JSON. La identidad del contacto es contact_ref si trae `value` no vacío; con contact_ref
// ausente o de value vacío vale `contact`, una cadena plana que equivale a {kind: phone_e164,
// value: contact} (alias de compatibilidad, Plan 010). Comprobaciones, en orden; la primera que
// falla responde y d.Starter NO se llama:
//
//  1. cuerpo que no se decodifica (vacío, JSON roto, un tipo que no casa) ⇒ 400 {"error":"cuerpo
//     JSON inválido"};
//  2. session_id vacío (también con `{}` y `null`) ⇒ 400 {"error":"session_id es requerido"}. Va
//     ANTES que la identidad del contacto;
//  3. sin ninguna identidad ⇒ 400 {"error":"se requiere contact_ref {kind,value} o contact
//     (alias phone_e164)"};
//  4. contact.NewRef la rechaza ⇒ 400 con "contact_ref inválida: " + el texto de ese error, que
//     ya empieza por "contact_ref inválida": el prefijo sale DOS veces (rareza portada).
//
// Después llama UNA vez a d.Starter.Start con el contexto de la petición (sin plazo propio), el
// tenant del token, el {id} de la ruta y el session_id TAL CUAL llegaron, y la contact.Ref ya
// normalizada. Sin error ⇒ 200 {"acked_command_id","ok","error"} tomados del Ack ("error" se
// omite si está vacío). Un Ack con ok=false sigue siendo un 200, y un Ack nil da
// {"acked_command_id":"","ok":false}.
//
// Si Start falla, el error se traduce así (errors.Is / errors.As, también envuelto); gana el
// PRIMER caso que case, en este orden, y es la misma tabla que admin.StartHandler:
//
//  1. runtime.ErrConversationExists ⇒ 409 {"error":"ya existe una conversación viva para la
//     clave"};
//  2. runtime.ErrDurableFlowNeedsEvent ⇒ 409 con un texto DISTINTO (Plan 054 · T2.5), que es la
//     única superficie para distinguir los dos 409 (no hay campo `code`): "el flujo tiene
//     contenido durable (cart/survey): su evento nace en la conversación, no por esta API.
//     Configura una regla event_start para este flujo (POST /api/v1/triggers) para que el cliente
//     lo arranque escribiendo su palabra clave; no reintentes esta llamada, seguirá devolviendo
//     409";
//  3. session.ErrSessionOffline de internal/modulos/edge/session ⇒ 502 {"error":"sesión offline:
//     no hay stream vivo para el Edge"}. 🔴 Se decide por IDENTIDAD del centinela (mapa §4.4,
//     trampa T-9 de reglas.md): es la MISMA variable que httpapi.ErrSessionOffline de
//     internal/platform, así que un error que envuelva cualquiera de los dos nombres casa. Gana
//     al stream caído: offline significa «no salió»;
//  4. un error con `StreamCaido() bool` que devuelva true (errors.As, duck-typing, sin importar
//     el gateway; Plan 050 · T2.4) ⇒ 504 con "el stream del Edge se cerró antes del ack: la
//     conversación YA quedó abierta y el comando de su primer mensaje viajó al Edge, así que no
//     se sabe si el cliente llegó a recibirlo. NO reintentes este arranque —devolverá 409—:
//     comprueba la conversación y, si el primer mensaje no salió, continúala sobre la que ya
//     existe". No es el texto del 504 de POST /api/v1/messages, a propósito. Con StreamCaido()
//     false el error sigue a los casos de abajo según su causa;
//  5. context.DeadlineExceeded o context.Canceled ⇒ 504 {"error":"timeout esperando el ack del
//     Edge"};
//  6. cualquier otro ⇒ 500 {"error":"no se pudo iniciar la conversación"}, sin repetir el error.
//
// I11–I13 · REGLAS DE DISPARO. Los handlers son admin.CreateTriggerHandler,
// admin.ListTriggersHandler y admin.DeleteTriggerHandler, los tres construidos con (d.Triggers,
// d.TriggersDurableFlow) tal cual, y su contrato manda. Lo que se ve desde aquí:
//
//   - I11: 201 con la regla creada; 400 {"error":"cuerpo JSON inválido"} y los 400 de
//     validación; 422 si la regla (keyword o fallback) apunta a un flujo durable y el tenant no
//     tiene ninguna event_start habilitada; 500 {"error":"no se pudo crear la regla de disparo"};
//   - I12: 200 con el arreglo de reglas del tenant (`[]` si no hay), con las marcas derivadas
//     `shadowed_by_event_list` y `flow_needs_event`;
//   - I13: 204 sin cuerpo; 404 {"error":"regla de disparo no encontrada"} si el {id} no existe o
//     es de otro tenant; 422 si es la última event_start habilitada y dejaría sin respuesta una
//     regla hacia un flujo durable.
//
// Defensa que los tokens de sharedjwt no alcanzan (RequirePermission corta antes): una identidad
// sin empresa que llegara a un handler recibiría 401 {"error":"autenticación requerida"} sin
// tocar ningún puerto. Otra, que el mux no deja alcanzar: un {id} vacío en I3 o I4 sería 400
// {"error":"flow id requerido en la ruta"}.
//
// Fallo de cableado: k.MW nil hace panic AL MONTAR (ver Common), con un mensaje que nombra
// MountFlows, también con d vacío (I1–I4 se montan siempre). d.Flows y d.Starter NO se comprueban
// al montar, igual que en la cara vieja: el arranque los cablea siempre.
func MountFlows(c *Cara, k Common, d FlowsDeps) {
	panic(pendiente.Implementar("apipublica.MountFlows"))
}
