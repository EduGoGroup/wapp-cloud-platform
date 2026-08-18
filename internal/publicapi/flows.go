package publicapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	flowadmin "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/admin"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// flowSummaryDTO es una fila del listado GET /api/v1/flows.
type flowSummaryDTO struct {
	FlowID    string `json:"flow_id"`
	Version   int    `json:"version"`
	CreatedAt string `json:"created_at,omitempty"`
}

// listFlowsHandler devuelve el handler de GET /api/v1/flows: lista los flujos del
// tenant del token (INV-8), cada uno con su última versión. 200 con el arreglo
// (vacío si no hay flujos); 401 sin identidad; 500 ante fallo del store.
func listFlowsHandler(flows FlowStore) http.Handler {
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
		out := make([]flowSummaryDTO, 0, len(summaries))
		for _, s := range summaries {
			dto := flowSummaryDTO{FlowID: s.FlowID, Version: s.Version}
			if !s.CreatedAt.IsZero() {
				dto.CreatedAt = s.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
			}
			out = append(out, dto)
		}
		writeJSON(w, http.StatusOK, out)
	})
}

// getFlowHandler devuelve el handler de GET /api/v1/flows/{id}: la definición
// vigente (última versión) del flujo {id} para el tenant del token (INV-8). 200 con
// la definición; 404 si el tenant no tiene ese flujo (o es de otro tenant: el store
// filtra por tenant, así que un flow_id ajeno da 404); 401 sin identidad; 500 en
// otro fallo.
func getFlowHandler(flows FlowStore) http.Handler {
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

// contactRefBody es la identidad flexible del contacto en el cuerpo (Plan 010).
type contactRefBody struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// startFlowRequest es el cuerpo JSON de POST /api/v1/flows/{id}/start. flow_id va en
// la RUTA (no en el cuerpo). La identidad del contacto se aporta como contact_ref
// {kind,value} o, por compat, un `contact` plano interpretado como phone_e164. El
// tenant_id NO viaja aquí (INV-8): sale del token.
type startFlowRequest struct {
	SessionID  string          `json:"session_id"`
	ContactRef *contactRefBody `json:"contact_ref"`
	Contact    string          `json:"contact"` // alias compat = phone_e164
}

// ref deriva la contact.Ref validada del cuerpo (prioriza contact_ref; si falta usa
// `contact` como phone_e164). ok=false si no se aportó ninguna identidad.
func (req startFlowRequest) ref() (r contact.Ref, ok bool, err error) {
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

// startResponse refleja el Ack del envío del menú inicial (mismo contrato que el
// arranque admin).
type startResponse struct {
	AckedCommandID string `json:"acked_command_id"`
	OK             bool   `json:"ok"`
	Error          string `json:"error,omitempty"`
}

// startFlowHandler devuelve el handler de POST /api/v1/flows/{id}/start: abre una
// conversación del flujo {id} para el contacto indicado y envía el menú inicial.
// Reusa el motor de flujos (Starter, el mismo de /admin/flows/start); toma el tenant
// del token (INV-8) y el flow_id de la ruta. Respuestas:
//
//   - 200 con {acked_command_id, ok, error} al recibir el Ack.
//   - 409 si ya hay una conversación viva para la clave (ErrConversationExists), o
//     si el flujo tiene contenido durable y no trae evento padre
//     (ErrDurableFlowNeedsEvent, Plan 054 · T2.5) — dos 409 con texto distinto.
//   - 502 si la sesión está offline; 504 si expira el ack; 500 en otro fallo.
//   - 401 sin identidad; 400 si falta flow_id/session_id/contacto o el JSON es inválido.
func startFlowHandler(starter flowadmin.Starter) http.Handler {
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

		var req startFlowRequest
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

// msgStreamCaidoStart es el cuerpo del 504 «se cayó» al ARRANCAR una conversación.
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
// streamCaidoFrom no se redefine aquí: vive en messages.go, mismo paquete.
const msgStreamCaidoStart = "el stream del Edge se cerró antes del ack: la conversación YA quedó abierta y el " +
	"comando de su primer mensaje viajó al Edge, así que no se sabe si el cliente llegó a recibirlo. " +
	"NO reintentes este arranque —devolverá 409—: comprueba la conversación y, si el primer mensaje " +
	"no salió, continúala sobre la que ya existe"

// writeStartError traduce el error de Start a un código HTTP: conversación existente
// -> 409, flujo con contenido durable sin evento -> 409 (texto DISTINTO del
// anterior, Plan 054 · T2.5), sesión offline -> 502, stream caído esperando el Ack ->
// 504, timeout/cancelación -> 504, resto -> 500 (mismo criterio que flujos/admin).
//
// Que el error de ENVÍO llegue hasta aquí no es teoría: el arranque termina en
// rt.send (runtime/start.go), que envuelve con %w el error del Gateway. Los casos de
// ErrSessionOffline y DeadlineExceeded que ya había son la prueba de que ese error
// cruza; el del stream caído cruza por el mismo sitio.
func writeStartError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, runtime.ErrConversationExists):
		writeError(w, http.StatusConflict, "ya existe una conversación viva para la clave")
	case errors.Is(err, runtime.ErrDurableFlowNeedsEvent):
		// MD-054.3 (design.md §6, confirmado por grep contra errorBody/writeError):
		// publicapi NO tiene campo `code` estructurado en su JSON de error — solo
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
		writeError(w, http.StatusBadGateway, "sesión offline: no hay stream vivo para el Edge")
	case streamCaidoFrom(err):
		// Plan 050 · Ola 2 · T2.4. Antes de esto el stream caído caía al default y
		// salía un 500 «no se pudo iniciar la conversación»: código equivocado (no
		// falló el servidor), causa oculta, y encima incoherente con el 504 que el
		// MISMO fallo devuelve por /api/v1/messages en el mismo despliegue.
		writeError(w, http.StatusGatewayTimeout, msgStreamCaidoStart)
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		writeError(w, http.StatusGatewayTimeout, "timeout esperando el ack del Edge")
	default:
		writeError(w, http.StatusInternalServerError, "no se pudo iniciar la conversación")
	}
}
