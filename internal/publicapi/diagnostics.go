package publicapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/diagnostics"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// DiagnosticsRequester emite un DiagnosticsRequest por el stream CloudLink a una
// sesión (Plan 031 · T5, ADR-0023). Lo satisface *gatewaygrpc.Server. El command_id
// lo genera y persiste el handler ANTES de llamar (correlación sin carrera).
type DiagnosticsRequester interface {
	RequestDiagnostics(ctx context.Context, sessionID, commandID, scope string) error
}

// DiagnosticsStore persiste las solicitudes/bundles y resuelve el consentimiento por
// tenant. Lo satisface *diagnostics.Postgres (y *diagnostics.MemoryStore en tests).
// Toda operación va acotada al tenant del token (INV-8).
type DiagnosticsStore interface {
	ConsentEnabled(ctx context.Context, tenantID string) (bool, error)
	CreateRequest(ctx context.Context, tenantID, sessionID, commandID, requestedBy string, expiresAt time.Time) error
	DeleteRequest(ctx context.Context, tenantID, commandID string) error
	GetBundle(ctx context.Context, tenantID, commandID string) (diagnostics.Record, error)
}

// diagnosticsRequestBody es el cuerpo JSON (OPCIONAL) de POST .../diagnostics: solo
// el scope. El tenant y el session_id NO viajan aquí (INV-8 / ruta). Cuerpo vacío ⇒
// scope "full".
type diagnosticsRequestBody struct {
	Scope string `json:"scope"`
}

// diagnosticsRequestResponse confirma la solicitud emitida: el command_id con el que
// se descargará el bundle cuando el Edge responda.
type diagnosticsRequestResponse struct {
	CommandID string `json:"command_id"`
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
	ExpiresAt string `json:"expires_at"`
}

// diagnosticsBundleResponse es el bundle ya recibido, para la descarga.
type diagnosticsBundleResponse struct {
	CommandID      string `json:"command_id"`
	SessionID      string `json:"session_id"`
	RequestedBy    string `json:"requested_by"`
	RequestedAt    string `json:"requested_at"`
	ReceivedAt     string `json:"received_at"`
	LogTail        string `json:"log_tail"`
	GoroutineDump  string `json:"goroutine_dump"`
	SubsystemsJSON string `json:"subsystems_json"`
}

// preflightDiagnostics valida identidad + ruta + consentimiento (default ON, opt-out)
// + aislamiento session→tenant (INV-8). Escribe el error apropiado y devuelve ok=false
// para cortar; con ok=true devuelve la Identity y el session_id ya validados.
func preflightDiagnostics(w http.ResponseWriter, r *http.Request, store DiagnosticsStore, sessions SessionLister) (httpapi.Identity, string, bool) {
	id, ok := httpapi.IdentityFromContext(r.Context())
	if !ok || id.TenantID == "" {
		writeError(w, http.StatusUnauthorized, "autenticación requerida")
		return id, "", false
	}
	sessionID := r.PathValue("id")
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "session id requerido en la ruta")
		return id, "", false
	}
	// Gate de CONSENTIMIENTO (ADR-0023): opt-out ⇒ 403. Un fallo del checker NO abre la
	// capacidad (se trata como no verificable ⇒ 500, no como consentido).
	consented, err := store.ConsentEnabled(r.Context(), id.TenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "no se pudo verificar el consentimiento")
		return id, "", false
	}
	if !consented {
		writeError(w, http.StatusForbidden, "el tenant desactivó el diagnóstico remoto (opt-out)")
		return id, "", false
	}
	// Aislamiento por tenant (INV-8): la sesión debe ser del tenant del token.
	belongs, err := sessionBelongsToTenant(r.Context(), sessions, id.TenantID, sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "no se pudo verificar la sesión")
		return id, "", false
	}
	if !belongs {
		writeError(w, http.StatusNotFound, "sesión no encontrada para el tenant")
		return id, "", false
	}
	return id, sessionID, true
}

// resolveScope lee el scope OPCIONAL del cuerpo (cuerpo vacío ⇒ "full"). ok=false si
// el cuerpo es un JSON inválido (ya escribió 400).
func resolveScope(w http.ResponseWriter, r *http.Request) (string, bool) {
	scope := "full"
	if r.Body == nil {
		return scope, true
	}
	var body diagnosticsRequestBody
	if derr := json.NewDecoder(r.Body).Decode(&body); derr != nil && !errors.Is(derr, io.EOF) {
		writeError(w, http.StatusBadRequest, "cuerpo JSON inválido")
		return "", false
	}
	if s := strings.TrimSpace(body.Scope); s != "" {
		scope = s
	}
	return scope, true
}

// requestDiagnosticsHandler devuelve POST /api/v1/sessions/{id}/diagnostics: emite un
// DiagnosticsRequest a la sesión {id} del tenant del token (Plan 031 · T5, ADR-0023).
// Orden: gate de CONSENTIMIENTO por tenant (default ON; opt-out ⇒ 403) → aislamiento
// session→tenant (INV-8 ⇒ 404) → genera command_id → persiste la solicitud pendiente →
// empuja el request por el stream (rollback de la fila si el push falla). El grant
// diagnostics.request lo exige el middleware (protect); la auditoría durable la deja
// AuditMiddleware, y aquí se añade un log estructurado con command_id/session_id/subject.
// Respuestas:
//
//   - 202 con {command_id, session_id, status:"pending", expires_at} al emitir.
//   - 400 si falta el id en la ruta o el cuerpo JSON es inválido.
//   - 401 sin identidad; 403 si el tenant desactivó el diagnóstico (opt-out).
//   - 404 si la sesión no es del tenant; 502 si la sesión está offline; 500 en fallo.
func requestDiagnosticsHandler(gw DiagnosticsRequester, store DiagnosticsStore, sessions SessionLister, ttl time.Duration, log sharedlogger.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if gw == nil || store == nil {
			writeError(w, http.StatusInternalServerError, "diagnóstico remoto no configurado")
			return
		}
		// Identidad + consentimiento (default ON, opt-out) + aislamiento session→tenant.
		id, sessionID, ok := preflightDiagnostics(w, r, store, sessions)
		if !ok {
			return
		}
		scope, ok := resolveScope(w, r)
		if !ok {
			return
		}

		commandID, err := diagnostics.NewCommandID()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "no se pudo generar el command_id")
			return
		}
		expiresAt := time.Now().Add(ttl)
		if err := store.CreateRequest(r.Context(), id.TenantID, sessionID, commandID, id.Subject, expiresAt); err != nil {
			writeError(w, http.StatusInternalServerError, "no se pudo registrar la solicitud")
			return
		}

		// Push del DiagnosticsRequest por el stream. Si falla (sesión offline), se hace
		// rollback de la fila pendiente para no dejar una solicitud que nunca recibirá
		// bundle, y se traduce el error (offline ⇒ 502).
		if err := gw.RequestDiagnostics(r.Context(), sessionID, commandID, scope); err != nil {
			if derr := store.DeleteRequest(r.Context(), id.TenantID, commandID); derr != nil && log != nil {
				log.Warn("diagnóstico: rollback de solicitud tras push fallido falló",
					"tenant_id", id.TenantID, "command_id", commandID, "error", derr)
			}
			writeSendError(w, err)
			return
		}

		// Rastro de auditoría OPERATIVO (además del audit_events de AuditMiddleware):
		// quién (subject del JWT), qué sesión, qué command_id. CERO PII.
		if log != nil {
			log.Info("diagnóstico remoto solicitado",
				"tenant_id", id.TenantID, "subject", id.Subject,
				"session_id", sessionID, "command_id", commandID, "scope", scope)
		}

		writeJSON(w, http.StatusAccepted, diagnosticsRequestResponse{
			CommandID: commandID,
			SessionID: sessionID,
			Status:    "pending",
			ExpiresAt: expiresAt.UTC().Format(time.RFC3339),
		})
	})
}

// getDiagnosticsHandler devuelve GET /api/v1/diagnostics/{command_id}: descarga el
// bundle almacenado del tenant del token (Plan 031 · T5). Mismo grant que el request
// (diagnostics.request) y también auditado (protect). Respuestas:
//
//   - 200 con el bundle si está listo (ready).
//   - 202 si sigue pendiente (el Edge aún no respondió).
//   - 400 si falta el command_id en la ruta; 401 sin identidad.
//   - 404 si no existe para el tenant; 410 si expiró; 500 en fallo del store.
func getDiagnosticsHandler(store DiagnosticsStore, log sharedlogger.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}
		if store == nil {
			writeError(w, http.StatusInternalServerError, "diagnóstico remoto no configurado")
			return
		}
		commandID := r.PathValue("command_id")
		if commandID == "" {
			writeError(w, http.StatusBadRequest, "command_id requerido en la ruta")
			return
		}

		rec, err := store.GetBundle(r.Context(), id.TenantID, commandID)
		switch {
		case errors.Is(err, diagnostics.ErrNotFound):
			writeError(w, http.StatusNotFound, "diagnóstico no encontrado")
			return
		case errors.Is(err, diagnostics.ErrExpired):
			writeError(w, http.StatusGone, "diagnóstico expirado")
			return
		case errors.Is(err, diagnostics.ErrPending):
			writeJSON(w, http.StatusAccepted, map[string]string{
				"command_id": commandID,
				"status":     "pending",
				"message":    "el Edge aún no respondió; reintentar la descarga",
			})
			return
		case err != nil:
			writeError(w, http.StatusInternalServerError, "no se pudo leer el diagnóstico")
			return
		}

		// Rastro de auditoría de la DESCARGA (además del audit_events de AuditMiddleware).
		if log != nil {
			log.Info("diagnóstico remoto descargado",
				"tenant_id", id.TenantID, "subject", id.Subject,
				"session_id", rec.SessionID, "command_id", commandID)
		}

		writeJSON(w, http.StatusOK, diagnosticsBundleResponse{
			CommandID:      rec.CommandID,
			SessionID:      rec.SessionID,
			RequestedBy:    rec.RequestedBy,
			RequestedAt:    rec.RequestedAt.UTC().Format(time.RFC3339),
			ReceivedAt:     rec.ReceivedAt.UTC().Format(time.RFC3339),
			LogTail:        rec.Bundle.LogTail,
			GoroutineDump:  rec.Bundle.GoroutineDump,
			SubsystemsJSON: rec.Bundle.SubsystemsJSON,
		})
	})
}
