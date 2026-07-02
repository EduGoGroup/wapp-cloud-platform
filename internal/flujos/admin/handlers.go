// Package admin contiene los handlers HTTP del motor de flujos:
// POST /admin/flows (publicar una definición, validada y versionada) y
// POST /admin/flows/start (iniciar una conversación + enviar el menú, decisión C).
//
// Modelan internal/platform/httpapi/admin.go (decode JSON, validación, mapeo de
// errores a códigos) y se registran con Register desde cmd/server/main.go (T5).
//
// SEGURIDAD — auth DIFERIDA a la fase IAM: estos endpoints son INTERNOS y NO
// están autenticados. El tenant_id se aporta en el cuerpo (decisión A, no hay
// IAM aún). Deben exponerse solo en la red de administración (mismo http.Server
// de /healthz, no público) hasta que IAM añada autenticación/RBAC.
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
)

// DefinitionStore persiste una definición de flujo como versión nueva y devuelve
// la versión asignada. Lo satisface *store.PostgresRepository y
// *store.MemoryRepository (su método InsertDefinition encaja).
type DefinitionStore interface {
	InsertDefinition(ctx context.Context, tenantID string, f model.Flow) (version int, err error)
}

// Starter abre una conversación por API (crea el estado en el nodo inicial y
// envía el menú) y devuelve el Ack del último texto emitido. Lo satisface
// *runtime.Runtime (su método Start encaja). Recibe la identidad del contacto
// como contact.Ref ya validada/normalizada (Plan 010, design.md §7).
type Starter interface {
	Start(ctx context.Context, tenantID, flowID, sessionID string, ref contact.Ref) (*cloudlinkv1.Ack, error)
}

// definitionRequest es el cuerpo JSON de POST /admin/flows. El tenant_id viaja
// en el cuerpo (decisión A: aún no hay IAM); definition es el objeto-flujo crudo
// (mismo esquema que model.Flow), que se valida con model.ParseAndValidate.
type definitionRequest struct {
	TenantID   string          `json:"tenant_id"`
	Definition json.RawMessage `json:"definition"`
}

// definitionResponse es la respuesta de POST /admin/flows: el flow_id publicado
// y la versión que el repositorio asignó (versionado, design.md §4).
type definitionResponse struct {
	FlowID  string `json:"flow_id"`
	Version int    `json:"version"`
}

// DefinitionHandler devuelve el handler de POST /admin/flows: decodifica el
// cuerpo {tenant_id, definition}, valida la definición (model.ParseAndValidate)
// y la persiste como versión nueva. Respuestas:
//
//   - 201 con {flow_id, version} al publicar.
//   - 400 si el cuerpo JSON es inválido, falta tenant_id/definition o la
//     definición no cumple el esquema (ErrInvalidFlow).
//   - 405 si el método no es POST.
//   - 500 ante un fallo de persistencia.
func DefinitionHandler(store DefinitionStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "método no permitido (usar POST)", http.StatusMethodNotAllowed)
			return
		}

		var req definitionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "cuerpo JSON inválido", http.StatusBadRequest)
			return
		}
		if req.TenantID == "" {
			http.Error(w, "tenant_id es requerido", http.StatusBadRequest)
			return
		}
		if len(req.Definition) == 0 {
			http.Error(w, "definition es requerida", http.StatusBadRequest)
			return
		}

		flow, err := model.ParseAndValidate(req.Definition)
		if err != nil {
			http.Error(w, "definición de flujo inválida: "+err.Error(), http.StatusBadRequest)
			return
		}

		version, err := store.InsertDefinition(r.Context(), req.TenantID, flow)
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
// interpreta como {kind: phone_e164, value: contact}.
type startRequest struct {
	TenantID   string          `json:"tenant_id"`
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

// StartHandler devuelve el handler de POST /admin/flows/start: decodifica el
// cuerpo {tenant_id, flow_id, session_id, contact_ref{kind,value}} (o el alias
// compat `contact` como phone_e164, §10.F), abre la conversación por API (crea el
// estado en el nodo inicial fijando la versión vigente y envía el menú).
// Respuestas:
//
//   - 200 con {acked_command_id, ok, error} cuando se recibió el Ack del envío.
//   - 409 si ya hay una conversación viva para la clave (ErrConversationExists).
//   - 502 si la sesión está offline (no hay stream vivo para el Edge).
//   - 504 si se agota el contexto/timeout esperando el Ack del Edge.
//   - 500 ante cualquier otro error.
//   - 400 si el cuerpo JSON es inválido o falta algún campo; 405 si no es POST.
func StartHandler(starter Starter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "método no permitido (usar POST)", http.StatusMethodNotAllowed)
			return
		}

		var req startRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "cuerpo JSON inválido", http.StatusBadRequest)
			return
		}
		if req.TenantID == "" || req.FlowID == "" || req.SessionID == "" {
			http.Error(w, "tenant_id, flow_id y session_id son requeridos", http.StatusBadRequest)
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

		ack, err := starter.Start(r.Context(), req.TenantID, req.FlowID, req.SessionID, ref)
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

// writeStartError traduce el error de Start a un código HTTP: conversación ya
// existente -> 409, sesión offline -> 502, timeout/cancelación esperando el Ack
// -> 504, resto -> 500.
func writeStartError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, runtime.ErrConversationExists):
		http.Error(w, "ya existe una conversación viva para la clave", http.StatusConflict)
	case errors.Is(err, session.ErrSessionOffline):
		http.Error(w, "sesión offline: no hay stream vivo para el Edge", http.StatusBadGateway)
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		http.Error(w, "timeout esperando el ack del Edge", http.StatusGatewayTimeout)
	default:
		http.Error(w, "no se pudo iniciar la conversación", http.StatusInternalServerError)
	}
}

// Register monta ambos endpoints admin del motor de flujos en el mux indicado.
// Lo invoca cmd/server/main.go (T5); aquí no se decide dónde se expone el mux
// (red de administración, no público; ver nota de seguridad del paquete).
func Register(mux *http.ServeMux, store DefinitionStore, starter Starter) {
	mux.Handle("/admin/flows", DefinitionHandler(store))
	mux.Handle("/admin/flows/start", StartHandler(starter))
}

// writeJSON serializa v como JSON y responde con el código indicado. Si la
// serialización falla, responde 500 (mismo patrón que httpapi/admin.go).
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
