// Porta internal/flujos/admin/handlers.go @ 724f3035
//
// No se porta `Register` (D-F8-2): solo lo llamaban tests (deuda D-7); las dos rutas
// las monta el arranque. El comentario de paquete vive en doc.go.

package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/session"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
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

// definitionRequest es el cuerpo JSON de POST /admin/flows. definition es el
// objeto-flujo crudo (mismo esquema que model.Flow), que se valida con
// model.ParseAndValidate. El tenant_id NO viaja aquí (INV-8): sale del token.
type definitionRequest struct {
	Definition json.RawMessage `json:"definition"`
}

// definitionResponse es la respuesta de POST /admin/flows: el flow_id publicado
// y la versión que el repositorio asignó (versionado, design.md §4).
type definitionResponse struct {
	FlowID  string `json:"flow_id"`
	Version int    `json:"version"`
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
	var moduleTypes []string
	if mods != nil {
		moduleTypes = mods.Types()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "método no permitido (usar POST)", http.StatusMethodNotAllowed)
			return
		}

		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			http.Error(w, "autenticación requerida", http.StatusUnauthorized)
			return
		}

		var req definitionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "cuerpo JSON inválido", http.StatusBadRequest)
			return
		}
		if len(req.Definition) == 0 {
			http.Error(w, "definition es requerida", http.StatusBadRequest)
			return
		}

		flow, err := model.ParseAndValidate(req.Definition, moduleTypes...)
		if err != nil {
			http.Error(w, "definición de flujo inválida: "+err.Error(), http.StatusBadRequest)
			return
		}

		// Validación estructural de los nodos de MÓDULO (Plan 027 · Ola 1 · T6, cierra
		// H11): model.ParseAndValidate acepta los tipos de módulo de forma laxa; aquí
		// cada módulo que expone la capacidad valida la estructura de SUS nodos (p. ej.
		// un cart sin catálogo se rechaza en el alta, no degrada en runtime). Se consulta
		// por aserción de capacidad: una fuente que no la implemente conserva el
		// comportamiento previo (sin validación estructural de módulo).
		if v, ok := mods.(interface {
			ValidateModuleNodes(model.Flow) error
		}); ok {
			if verr := v.ValidateModuleNodes(flow); verr != nil {
				http.Error(w, "definición de flujo inválida: "+verr.Error(), http.StatusBadRequest)
				return
			}
		}

		version, err := store.InsertDefinition(r.Context(), id.TenantID, flow)
		if err != nil {
			http.Error(w, "no se pudo persistir la definición", http.StatusInternalServerError)
			return
		}

		writeJSON(w, http.StatusCreated, definitionResponse{
			FlowID:  flow.FlowID,
			Version: version,
		})
	})
}

// contactRefBody es la identidad FLEXIBLE del contacto en el cuerpo JSON
// (Plan 010, design.md §7): {kind, value}, con kind ∈ {phone_e164, wa_lid,
// wa_username}.
type contactRefBody struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// startRequest es el cuerpo JSON de POST /admin/flows/start (decisión C).
// La identidad del contacto se aporta como contact_ref {kind,value}; por
// COMPAT (design.md §10.F) se acepta además un `contact` string plano, que se
// interpreta como {kind: phone_e164, value: contact}. El tenant_id NO viaja aquí
// (INV-8): sale del token.
type startRequest struct {
	FlowID     string          `json:"flow_id"`
	SessionID  string          `json:"session_id"`
	ContactRef *contactRefBody `json:"contact_ref"`
	Contact    string          `json:"contact"` // alias compat = phone_e164
}

// ref deriva la contact.Ref (validada y normalizada) del cuerpo: prioriza
// contact_ref y, si no viene, usa el alias `contact` como phone_e164 (§10.F).
// Devuelve ok=false si no se aportó ninguna identidad.
func (req startRequest) ref() (ref contact.Ref, ok bool, err error) {
	switch {
	case req.ContactRef != nil && req.ContactRef.Value != "":
		r, rerr := contact.NewRef(req.ContactRef.Kind, req.ContactRef.Value)
		return r, true, rerr
	case req.Contact != "":
		r, rerr := contact.NewRef(contact.KindPhoneE164, req.Contact)
		return r, true, rerr
	default:
		return contact.Ref{}, false, nil
	}
}

// startResponse refleja el Ack del envío del menú (acked_command_id, ok) y, si
// lo hubo, el error reportado por el Edge al ejecutar el SendText (Ack.ok=false).
// Mismo contrato que el /admin/messages/send del Gateway.
type startResponse struct {
	AckedCommandID string `json:"acked_command_id"`
	OK             bool   `json:"ok"`
	Error          string `json:"error,omitempty"`
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
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "método no permitido (usar POST)", http.StatusMethodNotAllowed)
			return
		}

		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			http.Error(w, "autenticación requerida", http.StatusUnauthorized)
			return
		}

		var req startRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "cuerpo JSON inválido", http.StatusBadRequest)
			return
		}
		if req.FlowID == "" || req.SessionID == "" {
			http.Error(w, "flow_id y session_id son requeridos", http.StatusBadRequest)
			return
		}
		ref, ok, err := req.ref()
		if !ok {
			http.Error(w, "se requiere contact_ref {kind,value} o contact (alias phone_e164)", http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, "contact_ref inválida: "+err.Error(), http.StatusBadRequest)
			return
		}

		ack, err := starter.Start(r.Context(), id.TenantID, req.FlowID, req.SessionID, ref)
		if err != nil {
			writeStartError(w, err)
			return
		}

		writeJSON(w, http.StatusOK, startResponse{
			AckedCommandID: ack.GetAckedCommandId(),
			OK:             ack.GetOk(),
			Error:          ack.GetError(),
		})
	})
}

// msgStreamDownStart (`msgStreamCaidoStart` en el viejo) es el cuerpo del 504 «se cayó» al ARRANCAR una conversación.
// No es el texto de messages.go y no debe serlo: allí se pierde el rastro de un
// mensaje suelto, aquí queda una conversación viva a medio saludar, y lo que el
// llamante tiene que hacer es distinto.
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
// Duplicada a propósito en publicapi/flows.go, con el mismo texto: es el mismo suceso
// visto por los dos APIs y un operador debe reconocerlo igual en ambos.
const msgStreamDownStart = "el stream del Edge se cerró antes del ack: la conversación YA quedó abierta y el " +
	"comando de su primer mensaje viajó al Edge, así que no se sabe si el cliente llegó a recibirlo. " +
	"NO reintentes este arranque —devolverá 409—: comprueba la conversación y, si el primer mensaje " +
	"no salió, continúala sobre la que ya existe"

// streamDownFrom (`streamCaidoFrom` en el viejo) indica si el error viene de un stream que murió esperando el ack,
// por duck-typing (`interface{ StreamCaido() bool }`) y sin importar el paquete del
// Gateway. Es la TERCERA copia del patrón —las otras dos están en publicapi y en
// platform/httpapi— y esa duplicación es la convención de la casa, no un descuido:
// el mismo trato tiene commandIDOf/commandIDCarrier (intakes/notifier.go). El
// contrato es la interfaz anónima, no un tipo compartido, y es lo que mantiene al
// Gateway fuera de los imports de los handlers; un errors.Is contra
// gatewaygrpc.ErrStreamClosed obligaría a importarlo y rompería el desacople.
//
// Falso NO significa «el envío fue bien»: significa que, si falló, fue por otra cosa.
func streamDownFrom(err error) bool {
	var down interface{ StreamCaido() bool }
	return errors.As(err, &down) && down.StreamCaido()
}

// writeStartError traduce el error de Start a un código HTTP: conversación ya
// existente -> 409, flujo con contenido durable sin evento -> 409 (texto DISTINTO
// del anterior, Plan 054 · T2.5, D-054.3(b)/D-054.6), sesión offline -> 502, stream
// caído esperando el Ack -> 504, timeout/cancelación esperando el Ack -> 504,
// resto -> 500.
//
// Que el error de ENVÍO llegue hasta aquí no es teoría: el arranque termina en
// rt.send (runtime/start.go), que envuelve con %w el error del Gateway. Los casos de
// ErrSessionOffline y DeadlineExceeded que ya había son la prueba de que ese error
// cruza; el del stream caído cruza por el mismo sitio. Su gemelo público
// (publicapi/flows.go) hace lo mismo con el mismo texto: si aquí cambia el criterio
// y allí no, la mitad del API queda mintiendo sobre el mismo suceso.
func writeStartError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, runtime.ErrConversationExists):
		http.Error(w, "ya existe una conversación viva para la clave", http.StatusConflict)
	case errors.Is(err, runtime.ErrDurableFlowNeedsEvent):
		// Aquí SÍ se le muestra el 409 al operador (a diferencia del cliente de
		// WhatsApp, que nunca ve este rechazo: T2.4 lo degrada a la oferta del
		// despachador). El texto dice qué hacer, no solo que falló: publicapi
		// (MD-054.3) no tiene campo `code` en su JSON de error, así que el TEXTO es
		// la única superficie para distinguir este 409 del de ErrConversationExists.
		//
		// Retirada de capacidad, dicha clara (Plan 054 · F2b, D-B — decisión de Jhoan
		// 2026-08-12, tras review): verificado que NINGÚN endpoint de /admin ni de
		// /api/v1 para un evento — las tres puertas del Plan 043 son de WhatsApp. El
		// texto viejo aconsejaba «arráncalo desde una conversación que ya tenga un
		// evento activo», y eso NO se puede hacer por API. Ya no se ofrece esa vía:
		// la única accionable es configurar la regla que SÍ pare el evento desde la
		// conversación.
		http.Error(w, "el flujo tiene contenido durable (cart/survey): su evento nace en la conversación, no por "+
			"esta API. Configura una regla event_start para este flujo (POST /api/v1/triggers) para que el "+
			"cliente lo arranque escribiendo su palabra clave; no reintentes esta llamada, seguirá devolviendo 409",
			http.StatusConflict)
	case errors.Is(err, session.ErrSessionOffline):
		http.Error(w, "sesión offline: no hay stream vivo para el Edge", http.StatusBadGateway)
	case streamDownFrom(err):
		// Plan 050 · Ola 2 · T2.4. Antes de esto el stream caído caía al default y
		// salía un 500 «no se pudo iniciar la conversación»: código equivocado (no
		// falló el servidor), causa oculta, y encima incoherente con el 504 que el
		// MISMO fallo devuelve por /admin/messages/send en el mismo despliegue.
		http.Error(w, msgStreamDownStart, http.StatusGatewayTimeout)
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		http.Error(w, "timeout esperando el ack del Edge", http.StatusGatewayTimeout)
	default:
		http.Error(w, "no se pudo iniciar la conversación", http.StatusInternalServerError)
	}
}

// writeJSON serializa v como JSON y responde con el código indicado. Si la
// serialización falla, responde 500 (mismo patrón que httpapi/admin.go). Si falla la
// ESCRITURA del cuerpo, el error se descarta: es la deuda D-16, que se porta como está
// (D-F8-6). Lo usan también los handlers de triggers.go.
func writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "codificando respuesta", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, werr := w.Write(body); werr != nil {
		return
	}
}
