package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// SessionRoleStore es el subconjunto de fleet.Repository que consume el handler de
// rol de sesión (Plan 020 · T1). Lo satisface *fleet.PostgresRepository y
// *fleet.MemoryRepository. La operación se acota al tenant del token (INV-8).
type SessionRoleStore interface {
	SetRole(ctx context.Context, tenantID, sessionID string, role fleet.Role) (found bool, err error)
}

// sessionRoleRequest es el cuerpo JSON de POST .../sessions/{id}/role. El tenant y
// el session_id NO viajan aquí (INV-8 / ruta): salen del token y del path.
type sessionRoleRequest struct {
	Role string `json:"role"`
}

// sessionRoleDTO es la respuesta de éxito: el session_id y el rol ya fijado.
type sessionRoleDTO struct {
	SessionID string `json:"session_id"`
	Role      string `json:"role"`
}

// SetSessionRoleHandler devuelve el handler de POST .../sessions/{id}/role: fija el
// rol (bot|passive) de la sesión {id} del tenant del token (Plan 020 · T1). El
// tenant sale del token (INV-8) y la mutación se acota a él (aislamiento estricto:
// no se puede tocar la sesión de otro tenant). Respuestas:
//
//   - 200 con {session_id, role} al fijar.
//   - 400 si el JSON es inválido, falta el id en la ruta o el rol no es bot|passive.
//   - 401 sin Identity en el contexto.
//   - 404 si la sesión no existe o pertenece a otro tenant (no se filtra existencia).
//   - 500 ante fallo de persistencia.
func SetSessionRoleHandler(store SessionRoleStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			http.Error(w, "autenticación requerida", http.StatusUnauthorized)
			return
		}

		sessionID := r.PathValue("id")
		if sessionID == "" {
			http.Error(w, "session id requerido en la ruta", http.StatusBadRequest)
			return
		}

		var req sessionRoleRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "cuerpo JSON inválido", http.StatusBadRequest)
			return
		}

		role := fleet.Role(strings.TrimSpace(req.Role))
		if !fleet.ValidRole(role) {
			http.Error(w, "role inválido (usar bot|passive)", http.StatusBadRequest)
			return
		}

		found, err := store.SetRole(r.Context(), id.TenantID, sessionID, role)
		switch {
		case errors.Is(err, fleet.ErrInvalidRole):
			http.Error(w, "role inválido (usar bot|passive)", http.StatusBadRequest)
		case err != nil:
			http.Error(w, "no se pudo fijar el rol de la sesión", http.StatusInternalServerError)
		case !found:
			http.Error(w, "sesión no encontrada", http.StatusNotFound)
		default:
			writeJSON(w, http.StatusOK, sessionRoleDTO{SessionID: sessionID, Role: string(role)})
		}
	})
}

// SessionStatusStore es el subconjunto de fleet.Repository que consume el handler de
// estatus de sesión (Plan 020 · T3). Lo satisface *fleet.PostgresRepository y
// *fleet.MemoryRepository. La operación se acota al tenant del token (INV-8) y sirve
// para RETIRAR/limpiar una sesión zombie (loggedout) o dejarla offline.
type SessionStatusStore interface {
	SetState(ctx context.Context, tenantID, sessionID string, state fleet.State) (found bool, err error)
}

// sessionStatusRequest es el cuerpo JSON de POST .../sessions/{id}/status. El tenant
// y el session_id NO viajan aquí (INV-8 / ruta): salen del token y del path.
type sessionStatusRequest struct {
	State string `json:"state"`
}

// sessionStatusDTO es la respuesta de éxito: el session_id y el estado ya fijado.
type sessionStatusDTO struct {
	SessionID string `json:"session_id"`
	State     string `json:"state"`
}

// SetSessionStatusHandler devuelve el handler de POST .../sessions/{id}/status: fija
// el estado de la sesión {id} del tenant del token a uno del conjunto admin-admitido
// (offline|loggedout), p. ej. para retirar/limpiar un zombie (Plan 020 · T3). El
// tenant sale del token (INV-8) y la mutación se acota a él (aislamiento estricto:
// no se puede tocar la sesión de otro tenant). 'online' NO se admite: es DERIVADO del
// stream vivo. Respuestas:
//
//   - 200 con {session_id, state} al fijar.
//   - 400 si el JSON es inválido, falta el id en la ruta o el estado no es offline|loggedout.
//   - 401 sin Identity en el contexto.
//   - 404 si la sesión no existe o pertenece a otro tenant (no se filtra existencia).
//   - 500 ante fallo de persistencia.
func SetSessionStatusHandler(store SessionStatusStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			http.Error(w, "autenticación requerida", http.StatusUnauthorized)
			return
		}

		sessionID := r.PathValue("id")
		if sessionID == "" {
			http.Error(w, "session id requerido en la ruta", http.StatusBadRequest)
			return
		}

		var req sessionStatusRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "cuerpo JSON inválido", http.StatusBadRequest)
			return
		}

		state := fleet.State(strings.TrimSpace(req.State))
		if !fleet.ValidAdminState(state) {
			http.Error(w, "state inválido (usar offline|loggedout)", http.StatusBadRequest)
			return
		}

		found, err := store.SetState(r.Context(), id.TenantID, sessionID, state)
		switch {
		case errors.Is(err, fleet.ErrInvalidState):
			http.Error(w, "state inválido (usar offline|loggedout)", http.StatusBadRequest)
		case err != nil:
			http.Error(w, "no se pudo fijar el estado de la sesión", http.StatusInternalServerError)
		case !found:
			http.Error(w, "sesión no encontrada", http.StatusNotFound)
		default:
			writeJSON(w, http.StatusOK, sessionStatusDTO{SessionID: sessionID, State: string(state)})
		}
	})
}
