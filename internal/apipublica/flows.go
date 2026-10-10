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
// En el rojo solo existían el puerto, FlowsDeps y MountFlows; los handlers de I2–I4, sus cuerpos
// y la tabla de errores de arranque nacieron con el verde (05 E-4, P6).

package apipublica

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/admin"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/session"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
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
// {"flow_id","version"} con la versión que ASIGNÓ el store; 400 "cuerpo JSON inválido", 400
// "definition es requerida", 400 con "definición de flujo inválida: …" y 500 "no se pudo
// persistir la definición". 🔴 Los tipos de d.Modules se leen UNA vez, AL MONTAR, no en cada
// petición (un módulo registrado después no se ve; se porta como está).
//
// 🔴 LOS ERRORES DE LOS HANDLERS DE admin (I1, I11–I13) SON TEXTO PLANO, no el {"error":…} del
// resto de la cara: salen por http.Error, así que el cuerpo es el texto seguido de un salto de
// línea, con Content-Type text/plain. Rareza portada: es lo que ya servía la cara vieja. El 401
// y el 403 de la cadena sí son JSON, también en esas cuatro rutas.
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
//     no hay stream vivo para el Edge"}. 🔴 Se decide por IDENTIDAD del centinela (F8
//     `reglas.md` T-8, mapa §4.4): es la MISMA variable que httpapi.ErrSessionOffline de
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
//   - I11: 201 con la regla creada; 400 "cuerpo JSON inválido" y los 400 de validación; 422 si
//     la regla (keyword o fallback) apunta a un flujo durable y el tenant no tiene ninguna
//     event_start habilitada; 500 "no se pudo crear la regla de disparo";
//   - I12: 200 con el arreglo de reglas del tenant (`[]` si no hay), con las marcas derivadas
//     `shadowed_by_event_list` y `flow_needs_event`;
//   - I13: 204 sin cuerpo; 404 "regla de disparo no encontrada" si el {id} no existe o es de
//     otro tenant; 422 si es la última event_start habilitada y dejaría sin respuesta una
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
	mustHaveMW(k, "MountFlows")

	// Publicar definición de flujo (escritura auditada). Reusa TAL CUAL el handler
	// de /admin/flows: ya toma el tenant del token y valida el esquema.
	c.Handle("POST /api/v1/flows", protect(k, "flows.create", "flow",
		admin.DefinitionHandler(d.Flows, d.Modules)))

	// Listar / leer definiciones (lecturas, sin auditoría).
	c.Handle("GET /api/v1/flows", protectRead(k, "flows.read", flowsListHandler(d.Flows)))
	c.Handle("GET /api/v1/flows/{id}", protectRead(k, "flows.read", flowsGetHandler(d.Flows)))

	// Arrancar una conversación de un flujo (escritura auditada). flow_id va en la
	// ruta (design.md §8); el resto (session_id, contacto) en el cuerpo. Reusa el
	// motor de flujos (Starter) que también sirve /admin/flows/start.
	c.Handle("POST /api/v1/flows/{id}/start", protect(k, "flows.start", "flow",
		flowsStartHandler(d.Starter)))

	// CRUD de reglas de disparo (Plan 019 · T5): keyword/fallback/escape por-tenant
	// que alimentan al ConfigResolver del Motor. Escrituras auditadas
	// (triggers.create/delete); lectura sin auditoría (triggers.read). Todo acotado
	// al tenant del token (INV-8); reusa los MISMOS handlers que /admin/triggers.
	if d.Triggers != nil {
		c.Handle("POST /api/v1/triggers", protect(k, "triggers.create", "trigger",
			admin.CreateTriggerHandler(d.Triggers, d.TriggersDurableFlow)))
		c.Handle("GET /api/v1/triggers", protectRead(k, "triggers.read",
			admin.ListTriggersHandler(d.Triggers, d.TriggersDurableFlow)))
		c.Handle("DELETE /api/v1/triggers/{id}", protect(k, "triggers.delete", "trigger",
			admin.DeleteTriggerHandler(d.Triggers, d.TriggersDurableFlow)))
	}
}

// flowsSummaryDTO (flowSummaryDTO en la cara vieja) es una fila del listado GET /api/v1/flows.
type flowsSummaryDTO struct {
	FlowID    string `json:"flow_id"`
	Version   int    `json:"version"`
	CreatedAt string `json:"created_at,omitempty"`
}

// flowsListHandler (listFlowsHandler en la cara vieja) devuelve el handler de GET /api/v1/flows:
// lista los flujos del tenant del token (INV-8), cada uno con su última versión. 200 con el
// arreglo (vacío si no hay flujos); 401 sin identidad; 500 ante fallo del store.
func flowsListHandler(flows FlowsStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}
		summaries, err := flows.ListDefinitions(r.Context(), id.TenantID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "no se pudieron listar los flujos")
			return
		}
		out := make([]flowsSummaryDTO, 0, len(summaries))
		for _, s := range summaries {
			// formatInstant ya da "" para el instante cero (que `omitempty` quita) y RFC3339 en
			// UTC para el resto: lo mismo que hacía la cara vieja a mano.
			out = append(out, flowsSummaryDTO{FlowID: s.FlowID, Version: s.Version, CreatedAt: formatInstant(s.CreatedAt)})
		}
		writeJSON(w, http.StatusOK, out)
	})
}

// flowsGetHandler (getFlowHandler en la cara vieja) devuelve el handler de GET
// /api/v1/flows/{id}: la definición vigente (última versión) del flujo {id} para el tenant del
// token (INV-8). 200 con la definición; 404 si el tenant no tiene ese flujo (o es de otro
// tenant: el store filtra por tenant, así que un flow_id ajeno da 404); 401 sin identidad; 500
// en otro fallo.
func flowsGetHandler(flows FlowsStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}
		flowID := r.PathValue("id")
		if flowID == "" {
			writeError(w, http.StatusBadRequest, "flow id requerido en la ruta")
			return
		}
		flow, err := flows.LatestDefinition(r.Context(), id.TenantID, flowID)
		if err != nil {
			if errors.Is(err, store.ErrDefinitionNotFound) {
				writeError(w, http.StatusNotFound, "flujo no encontrado")
				return
			}
			writeError(w, http.StatusInternalServerError, "no se pudo leer el flujo")
			return
		}
		writeJSON(w, http.StatusOK, flow)
	})
}

// flowsContactRefBody (contactRefBody en la cara vieja) es la identidad flexible del contacto en
// el cuerpo (Plan 010).
type flowsContactRefBody struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// flowsStartRequest (startFlowRequest en la cara vieja) es el cuerpo JSON de POST
// /api/v1/flows/{id}/start. flow_id va en la RUTA (no en el cuerpo). La identidad del contacto
// se aporta como contact_ref {kind,value} o, por compat, un `contact` plano interpretado como
// phone_e164. El tenant_id NO viaja aquí (INV-8): sale del token.
type flowsStartRequest struct {
	SessionID  string               `json:"session_id"`
	ContactRef *flowsContactRefBody `json:"contact_ref"`
	Contact    string               `json:"contact"` // alias compat = phone_e164
}

// ref deriva la contact.Ref validada del cuerpo (prioriza contact_ref; si falta usa `contact`
// como phone_e164). ok=false si no se aportó ninguna identidad.
func (req flowsStartRequest) ref() (r contact.Ref, ok bool, err error) {
	switch {
	case req.ContactRef != nil && req.ContactRef.Value != "":
		ref, rerr := contact.NewRef(req.ContactRef.Kind, req.ContactRef.Value)
		return ref, true, rerr
	case req.Contact != "":
		ref, rerr := contact.NewRef(contact.KindPhoneE164, req.Contact)
		return ref, true, rerr
	default:
		return contact.Ref{}, false, nil
	}
}

// flowsStartResponse (startResponse en la cara vieja) refleja el Ack del envío del menú inicial
// (mismo contrato que el arranque admin).
type flowsStartResponse struct {
	AckedCommandID string `json:"acked_command_id"`
	OK             bool   `json:"ok"`
	Error          string `json:"error,omitempty"`
}

// flowsStartHandler (startFlowHandler en la cara vieja) devuelve el handler de POST
// /api/v1/flows/{id}/start: abre una conversación del flujo {id} para el contacto indicado y
// envía el menú inicial. Reusa el motor de flujos (Starter, el mismo de /admin/flows/start);
// toma el tenant del token (INV-8) y el flow_id de la ruta. Respuestas:
//
//   - 200 con {acked_command_id, ok, error} al recibir el Ack.
//   - 409 si ya hay una conversación viva para la clave (ErrConversationExists), o
//     si el flujo tiene contenido durable y no trae evento padre
//     (ErrDurableFlowNeedsEvent, Plan 054 · T2.5) — dos 409 con texto distinto.
//   - 502 si la sesión está offline; 504 si expira el ack; 500 en otro fallo.
//   - 401 sin identidad; 400 si falta flow_id/session_id/contacto o el JSON es inválido.
func flowsStartHandler(starter admin.Starter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}
		flowID := r.PathValue("id")
		if flowID == "" {
			writeError(w, http.StatusBadRequest, "flow id requerido en la ruta")
			return
		}

		var req flowsStartRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "cuerpo JSON inválido")
			return
		}
		if req.SessionID == "" {
			writeError(w, http.StatusBadRequest, "session_id es requerido")
			return
		}
		ref, ok, err := req.ref()
		if !ok {
			writeError(w, http.StatusBadRequest, "se requiere contact_ref {kind,value} o contact (alias phone_e164)")
			return
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, "contact_ref inválida: "+err.Error())
			return
		}

		ack, err := starter.Start(r.Context(), id.TenantID, flowID, req.SessionID, ref)
		if err != nil {
			flowsWriteStartError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, flowsStartResponse{
			AckedCommandID: ack.GetAckedCommandId(),
			OK:             ack.GetOk(),
			Error:          ack.GetError(),
		})
	})
}

// flowsMsgStreamClosedStart (msgStreamCaidoStart en la cara vieja) es el cuerpo del 504 «se
// cayó» al ARRANCAR una conversación. No es el texto de messages.go y no debe serlo: allí se
// pierde el rastro de un mensaje suelto, aquí queda una conversación viva a medio saludar, y lo
// que el llamante tiene que hacer es distinto.
//
// Las tres afirmaciones están verificadas contra el runtime, no supuestas:
//
//  1. «YA quedó abierta» —no «pudo haber arrancado»—: rt.store.Save del estado
//     inicial ocurre ANTES del envío (runtime/start.go, orden Save-antes-de-SendText),
//     así que cuando el ack se pierde el flow_state ya está persistido. Decir «pudo»
//     mandaría a comprobar algo que es seguro.
//  2. «no se sabe si el cliente llegó a recibirlo»: el comando viajó al Edge y pudo
//     salir a WhatsApp. Por eso esto es un 504 y no el 502 que pedía el enunciado de
//     T2.4 —el 502 de este repo significa «no salió»— ni el 500 al que caía antes.
//  3. «devolverá 409»: reintentar NO duplica el arranque. rt.store.Exists ya da true
//     y restartableOnStart no encuentra ResumePolicy para ningún nodo alcanzable por
//     esta vía (la única registrada es la del carrito, y un flujo durable ni siquiera
//     llega aquí: lo corta antes ErrDurableFlowNeedsEvent), así que sale
//     ErrConversationExists. Avisar de un doble arranque sería asustar con algo que
//     el código impide; lo útil es decir que el reintento no sirve de nada.
//
// streamClosedFrom no se redefine aquí: vive en messages.go, mismo paquete.
const flowsMsgStreamClosedStart = "el stream del Edge se cerró antes del ack: la conversación YA quedó abierta y el " +
	"comando de su primer mensaje viajó al Edge, así que no se sabe si el cliente llegó a recibirlo. " +
	"NO reintentes este arranque —devolverá 409—: comprueba la conversación y, si el primer mensaje " +
	"no salió, continúala sobre la que ya existe"

// flowsWriteStartError (writeStartError en la cara vieja) traduce el error de Start a un código
// HTTP: conversación existente -> 409, flujo con contenido durable sin evento -> 409 (texto
// DISTINTO del anterior, Plan 054 · T2.5), sesión offline -> 502, stream caído esperando el Ack
// -> 504, timeout/cancelación -> 504, resto -> 500 (mismo criterio que conversacion/admin).
//
// Que el error de ENVÍO llegue hasta aquí no es teoría: el arranque termina en
// rt.send (runtime/start.go), que envuelve con %w el error del Gateway. Los casos de
// ErrSessionOffline y DeadlineExceeded que ya había son la prueba de que ese error
// cruza; el del stream caído cruza por el mismo sitio.
func flowsWriteStartError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, runtime.ErrConversationExists):
		writeError(w, http.StatusConflict, "ya existe una conversación viva para la clave")
	case errors.Is(err, runtime.ErrDurableFlowNeedsEvent):
		// MD-054.3 (design.md §6, confirmado por grep contra errorBody/writeError):
		// la cara NO tiene campo `code` estructurado en su JSON de error — solo
		// {"error": "<texto>"}. No se inventa esa superficie aquí (instrucción
		// explícita del plan); el TEXTO es la única forma de distinguir este 409 del
		// de ErrConversationExists, y le dice al operador qué hacer, no solo que
		// falló (el cliente de WhatsApp NUNCA ve este rechazo: T2.4 lo degrada a la
		// oferta del despachador antes de llegar aquí).
		//
		// Retirada de capacidad, dicha clara (Plan 054 · F2b, D-B — decisión de
		// Jhoan 2026-08-12, tras review): verificado que NINGÚN endpoint de /admin
		// ni de /api/v1 para un evento — las tres puertas del Plan 043 son de
		// WhatsApp. El texto viejo aconsejaba «arráncalo desde una conversación que
		// ya tenga un evento activo», y eso NO se puede hacer por API. Ya no se
		// ofrece esa vía: la única accionable es configurar la regla que SÍ pare el
		// evento desde la conversación.
		writeError(w, http.StatusConflict, "el flujo tiene contenido durable (cart/survey): su evento nace en la conversación, no por "+
			"esta API. Configura una regla event_start para este flujo (POST /api/v1/triggers) para que el "+
			"cliente lo arranque escribiendo su palabra clave; no reintentes esta llamada, seguirá devolviendo 409")
	case errors.Is(err, session.ErrSessionOffline):
		// Identidad del centinela (F8 `reglas.md` T-8, mapa §4.4): session.ErrSessionOffline
		// del módulo edge ES httpapi.ErrSessionOffline, así que casa el que envuelva el gateway.
		writeError(w, http.StatusBadGateway, "sesión offline: no hay stream vivo para el Edge")
	case streamClosedFrom(err):
		// Plan 050 · Ola 2 · T2.4. Antes de esto el stream caído caía al default y
		// salía un 500 «no se pudo iniciar la conversación»: código equivocado (no
		// falló el servidor), causa oculta, y encima incoherente con el 504 que el
		// MISMO fallo devuelve por /api/v1/messages en el mismo despliegue.
		writeError(w, http.StatusGatewayTimeout, flowsMsgStreamClosedStart)
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		writeError(w, http.StatusGatewayTimeout, "timeout esperando el ack del Edge")
	default:
		writeError(w, http.StatusInternalServerError, "no se pudo iniciar la conversación")
	}
}
