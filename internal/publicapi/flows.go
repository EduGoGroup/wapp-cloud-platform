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
//   - 409 si ya hay una conversación viva para la clave (ErrConversationExists).
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

// writeStartError traduce el error de Start a un código HTTP: conversación existente
// -> 409, sesión offline -> 502, timeout/cancelación -> 504, resto -> 500 (mismo
// criterio que flujos/admin).
func writeStartError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, runtime.ErrConversationExists):
		writeError(w, http.StatusConflict, "ya existe una conversación viva para la clave")
	case errors.Is(err, session.ErrSessionOffline):
		writeError(w, http.StatusBadGateway, "sesión offline: no hay stream vivo para el Edge")
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		writeError(w, http.StatusGatewayTimeout, "timeout esperando el ack del Edge")
	default:
		writeError(w, http.StatusInternalServerError, "no se pudo iniciar la conversación")
	}
}
