// Porta internal/flujos/admin/sessions.go @ 115a4ba (SessionProfileStore, ProfilePusher,
// SessionStatusStore, SetSessionProfileHandler, SetSessionStatusHandler, pushProfileBestEffort y
// sus textos), sobre el fleet del módulo edge NUEVO. El writeJSON de internal/flujos/admin
// (handlers.go @ 115a4ba) es, byte a byte, el writeJSON de response.go: se reutiliza ese.
//
// sessionadmin.go — EL PERFIL Y EL ESTADO DE UNA SESIÓN, POR ADMINISTRACIÓN (mapa §2.4, D3 y D4;
// §3, J16 y J17). Decisión D-FX-2: los dos handlers se portan aquí desde flujos/admin y se
// EXPORTAN, porque los sirven dos listeners: la cara pública (:8103), donde MountSessions
// (sessions.go) les pone la cadena W de Common, y la de administración (:8100), donde el arranque
// los envuelve con SU cadena. Por eso ningún handler de este fichero lleva cadena dentro: solo
// espera encontrar la Identity en el contexto.
//
// 🔴 Sus errores son `http.Error` en TEXTO PLANO ("<mensaje>\n", text/plain), NO el JSON
// {"error": …} del resto de la cara: es lo observable del handler viejo y se conserva.
//
// En el rojo solo existían los puertos y las dos constructoras; el empuje best-effort
// (pushProfileBestEffort) y los cuerpos de petición y respuesta nacieron con el verde.

package apipublica

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/fleet"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// SessionProfileStore es el subconjunto de fleet.Repository que consume el handler de PERFIL de
// sesión (Plan 046 · T1.2). Lo satisfacen fleet.Repository y *fleet.PostgresRepository del módulo
// edge NUEVO. La operación se acota al tenant del token (INV-8): found=false si la sesión no
// existe bajo ese tenant; fleet.ErrInvalidProfile si profile no es active|passive.
type SessionProfileStore interface {
	SetProfile(ctx context.Context, tenantID, sessionID string, profile fleet.Profile) (found bool, err error)
}

// ProfilePusher empuja a la sesión viva el cambio de perfil recién persistido, para que el Edge
// reconfigure su filtro de entrantes sin esperar a reconectar (ADR-0027, Ola 2). En producción lo
// cumple *filtercfg.Pusher del módulo edge NUEVO (traduce a un ConfigUpdate de kind `filters`).
//
// Es **best-effort**: un fallo de empuje NO invalida la escritura (el perfil ya quedó persistido
// y el empuje al conectar reconcilia) y por tanto **no cambia el código de respuesta**; el error
// se registra y ahí muere. nil es un no-op válido.
type ProfilePusher interface {
	PushProfile(ctx context.Context, tenantID, sessionID string, profile fleet.Profile) error
}

// profilePushTimeout acota el push de perfil desenganchado del ctx de la petición.
//
// El valor sale de lo que el push hace de verdad: una lectura a Postgres
// (ProfilesByTenant) más un fan-out concurrente a las sesiones vivas del tenant, y ese
// fan-out YA está acotado por dentro (WAPP_GRPC_PUSH_TIMEOUT por sesión). Cinco
// segundos es techo holgado para las dos cosas y a la vez un tope duro: sin él, un
// Neon colgado dejaría la goroutine del handler esperando para siempre por un push
// que a nadie le importa ya.
const profilePushTimeout = 5 * time.Second

// pushProfileBestEffort invoca al pusher tras persistir, con el contrato de
// ConfigPusher: pusher nil es no-op, y un fallo se loguea SIN tocar la respuesta
// (que ya se escribió, o se escribe a continuación con 200 igualmente).
//
// 🔴 EL CONTEXTO NO ES EL DE LA PETICIÓN (corrección del code review 2026-08-21).
// Antes se pasaba r.Context(), y PushConfig hace wg.Wait() sobre él: si el cliente
// abortaba el POST —cerrar la pestaña, un timeout del proxy, un Ctrl-C en el curl—
// el ctx moría, el push SE PERDÍA y quedaba el peor estado posible: el perfil
// PERSISTIDO, la sesión viva sin enterarse hasta reconectar, y nadie a quien avisar
// porque el 200 ya se había escrito (o se escribía igual a continuación).
//
// context.WithoutCancel conserva los VALORES del ctx de la petición —el request-id
// que arrastra el logger, la identidad— y suelta solo su cancelación, que es
// exactamente lo que sobra aquí. El timeout propio garantiza que la goroutine no se
// queda colgada: desenganchar del ctx del cliente no puede significar «sin límite».
//
// Esto NO cambia el best-effort (criterio (e) de T2.1): el push sigue sin poder
// alterar el código de respuesta. Lo que cambia es que ahora se INTENTA de verdad.
func pushProfileBestEffort(r *http.Request, pusher ProfilePusher, log sharedlogger.Logger,
	tenantID, sessionID string, profile fleet.Profile,
) {
	if pusher == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), profilePushTimeout)
	defer cancel()
	if perr := pusher.PushProfile(ctx, tenantID, sessionID, profile); perr != nil && log != nil {
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

// SessionStatusStore es el subconjunto de fleet.Repository que consume el handler de estado de
// sesión (Plan 020 · T3). Lo satisfacen fleet.Repository y *fleet.PostgresRepository del módulo
// edge NUEVO. La operación se acota al tenant del token (INV-8) y sirve para RETIRAR/limpiar una
// sesión zombie (loggedout) o dejarla offline: found=false si la sesión no existe bajo ese
// tenant; fleet.ErrInvalidState si state no es offline|loggedout.
type SessionStatusStore interface {
	SetState(ctx context.Context, tenantID, sessionID string, state fleet.State) (found bool, err error)
}

// SetSessionProfileHandler devuelve el handler de POST …/sessions/{id}/profile (D3 y J16): fija
// el PERFIL (active|passive) de la sesión {id} del tenant de la Identity del contexto (Plan 046 ·
// T1.2, D-046.5). El tenant y el session_id NO viajan en el cuerpo (INV-8): salen del contexto y
// de la ruta (r.PathValue("id")). El cuerpo es {"profile": "active"|"passive"} (se le recortan
// los espacios de los extremos). NO lleva cadena dentro: la pone quien lo monta.
//
// Respuestas, comprobadas en este orden; los errores son TEXTO PLANO ("<mensaje>\n"):
//
//   - 401 "autenticación requerida" sin Identity en el contexto o con una sin tenant;
//   - 400 "session id requerido en la ruta" si la ruta no trae {id};
//   - 400 "cuerpo JSON inválido" si el cuerpo no es JSON;
//   - 400 "profile inválido (usar active|passive)" si el perfil no es active|passive (⚠️ `bot` es
//     400: es vocabulario de la ruta VIEJA /role, retirada con la 0064). En ninguno de los 400
//     ni en el 401 se llama al store;
//   - store.SetProfile(tenant de la Identity, id, perfil), con el contexto de la petición:
//     fleet.ErrInvalidProfile ⇒ el mismo 400 de perfil inválido; otro error ⇒ 500 "no se pudo
//     fijar el perfil de la sesión"; found=false ⇒ 404 "sesión no encontrada" (404 y no 403: no
//     se revela si existe en OTRO tenant);
//   - 200 con cuerpo JSON {"session_id","profile"} y Content-Type application/json.
//
// EMPUJE. Solo tras persistir (nunca en un 4xx/5xx), y ANTES de escribir el 200, llama UNA vez a
// pusher.PushProfile(tenant, id, perfil). El contexto que le pasa NO es el de la petición: es
// context.WithoutCancel del de la petición —conserva sus valores (la Identity, el request-id) y
// suelta su cancelación: el aborto del cliente no puede llevarse por delante el empuje de una
// escritura que ya ocurrió— con un plazo propio de 5 s. Un error del empuje no cambia el 200:
// deja una línea Warn "sessions: push de perfil best-effort falló (persistido; reconcilia al
// conectar)" con "tenant_id", "session_id", "profile" y "error".
//
// pusher nil ⇒ no se empuja nada; log nil ⇒ mismas respuestas, sin línea.
//
// ⚠️ El entitlement `passive_profiles` NO gatea esta ruta en v1 (decisión del plan): existe
// declarado para cuando se cobre. Ninguna línea de aquí lo consulta.
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

// SetSessionStatusHandler devuelve el handler de POST …/sessions/{id}/status (D4 y J17): fija el
// estado de la sesión {id} del tenant de la Identity del contexto a uno del conjunto
// admin-admitido (offline|loggedout), p. ej. para retirar/limpiar un zombie (Plan 020 · T3).
// 'online' NO se admite: es DERIVADO del stream vivo. El tenant y el session_id NO viajan en el
// cuerpo (INV-8). El cuerpo es {"state": "offline"|"loggedout"} (se le recortan los espacios de
// los extremos). NO lleva cadena dentro: la pone quien lo monta.
//
// Respuestas, comprobadas en este orden; los errores son TEXTO PLANO ("<mensaje>\n"):
//
//   - 401 "autenticación requerida" sin Identity en el contexto o con una sin tenant;
//   - 400 "session id requerido en la ruta" si la ruta no trae {id};
//   - 400 "cuerpo JSON inválido" si el cuerpo no es JSON;
//   - 400 "state inválido (usar offline|loggedout)" si el estado no es offline|loggedout. En
//     ninguno de los 400 ni en el 401 se llama al store;
//   - store.SetState(tenant de la Identity, id, estado), con el contexto de la petición:
//     fleet.ErrInvalidState ⇒ el mismo 400 de estado inválido; otro error ⇒ 500 "no se pudo fijar
//     el estado de la sesión"; found=false ⇒ 404 "sesión no encontrada" (no se revela si existe
//     en OTRO tenant);
//   - 200 con cuerpo JSON {"session_id","state"} y Content-Type application/json.
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
