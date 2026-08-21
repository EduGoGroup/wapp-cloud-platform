package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// SessionProfileStore es el subconjunto de fleet.Repository que consume el handler
// de PERFIL de sesión (Plan 046 · T1.2). Lo satisface *fleet.PostgresRepository y
// *fleet.MemoryRepository. La operación se acota al tenant del token (INV-8).
//
// Es el eje que SOBREVIVE al DROP de `role`: SetProfile escribe las dos columnas
// mientras el alias legado viva, pero el puerto ya solo habla de perfil.
type SessionProfileStore interface {
	SetProfile(ctx context.Context, tenantID, sessionID string, profile fleet.Profile) (found bool, err error)
}

// ProfilePusher empuja a la sesión viva el cambio de perfil recién persistido, para
// que el Edge reconfigure su filtro de entrantes sin esperar a reconectar (ADR-0027,
// Ola 2). Es el MISMO molde que publicapi.ConfigPusher (ADR-0021) y con el mismo
// contrato: es **best-effort**, un fallo de push NO invalida la escritura (el perfil
// ya quedó persistido y el push al conectar reconcilia) y por tanto **no cambia el
// código de respuesta**; el error se registra y ahí muere.
//
// 🔴 En el Plan 046 · T1.2 nace APAGADO: los dos cableados (publicapi y bootstrap)
// pasan nil, que es un no-op explícito. Quien lo enchufa de verdad —el adaptador que
// traduce este puerto a un ConfigUpdate de kind `filters`— es **T2.1**, y hasta
// entonces cambiar el perfil NO llega al Edge hasta su siguiente conexión.
type ProfilePusher interface {
	PushProfile(ctx context.Context, tenantID, sessionID string, profile fleet.Profile) error
}

// pushProfileBestEffort invoca al pusher tras persistir, con el contrato de
// ConfigPusher: pusher nil es no-op, y un fallo se loguea SIN tocar la respuesta
// (que ya se escribió, o se escribe a continuación con 200 igualmente).
func pushProfileBestEffort(r *http.Request, pusher ProfilePusher, log sharedlogger.Logger,
	tenantID, sessionID string, profile fleet.Profile,
) {
	if pusher == nil {
		return
	}
	if perr := pusher.PushProfile(r.Context(), tenantID, sessionID, profile); perr != nil && log != nil {
		log.Warn("sessions: push de perfil best-effort falló (persistido; reconcilia al conectar)",
			"tenant_id", tenantID, "session_id", sessionID, "profile", string(profile), "error", perr)
	}
}

// sessionProfileRequest es el cuerpo JSON de POST .../sessions/{id}/profile. El
// tenant y el session_id NO viajan aquí (INV-8 / ruta): salen del token y del path.
type sessionProfileRequest struct {
	Profile string `json:"profile"`
}

// sessionProfileDTO es la respuesta de éxito: el session_id y el perfil ya fijado.
type sessionProfileDTO struct {
	SessionID string `json:"session_id"`
	Profile   string `json:"profile"`
}

// SetSessionProfileHandler devuelve el handler de POST .../sessions/{id}/profile:
// fija el PERFIL (active|passive) de la sesión {id} del tenant del token (Plan 046 ·
// T1.2, D-046.5). Sucedió al handler de /role —retirado con la 0064— con su MISMO scope
// (`sessions.write`), el MISMO aislamiento por tenant (el tenant sale del token,
// INV-8 del Plan 018: no se puede tocar la sesión de otro tenant) y las MISMAS cinco
// respuestas:
//
//   - 200 con {session_id, profile} al fijar.
//   - 400 si el JSON es inválido, falta el id en la ruta o el perfil no es
//     active|passive (⚠️ `bot` es 400: es vocabulario de la ruta VIEJA).
//   - 401 sin Identity en el contexto.
//   - 404 si la sesión no existe o pertenece a otro tenant (404 y no 403: no se
//     filtra existencia).
//   - 500 ante fallo de persistencia.
//
// ⚠️ El entitlement `passive_profiles` NO gatea esta ruta en v1 (decisión del plan):
// existe declarado para cuando se cobre. Ninguna línea de aquí lo consulta.
//
// pusher/log admiten nil: ver ProfilePusher (en T1.2 se cablean a nil).
func SetSessionProfileHandler(store SessionProfileStore, pusher ProfilePusher, log sharedlogger.Logger) http.Handler {
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

		var req sessionProfileRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "cuerpo JSON inválido", http.StatusBadRequest)
			return
		}

		profile := fleet.Profile(strings.TrimSpace(req.Profile))
		if !fleet.ValidProfile(profile) {
			http.Error(w, "profile inválido (usar active|passive)", http.StatusBadRequest)
			return
		}

		found, err := store.SetProfile(r.Context(), id.TenantID, sessionID, profile)
		switch {
		case errors.Is(err, fleet.ErrInvalidProfile):
			http.Error(w, "profile inválido (usar active|passive)", http.StatusBadRequest)
		case err != nil:
			http.Error(w, "no se pudo fijar el perfil de la sesión", http.StatusInternalServerError)
		case !found:
			http.Error(w, "sesión no encontrada", http.StatusNotFound)
		default:
			pushProfileBestEffort(r, pusher, log, id.TenantID, sessionID, profile)
			writeJSON(w, http.StatusOK, sessionProfileDTO{SessionID: sessionID, Profile: string(profile)})
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
