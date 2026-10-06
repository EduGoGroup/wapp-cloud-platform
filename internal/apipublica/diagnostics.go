// Porta internal/publicapi/diagnostics.go @ 115a4ba (DiagnosticsRequester, DiagnosticsStore,
// preflightDiagnostics, resolveScope, requestDiagnosticsHandler, getDiagnosticsHandler y sus
// textos) y el registro de D5 y D6 (internal/publicapi/publicapi.go @ 115a4ba, líneas 523-541).
//
// diagnostics.go — EL DIAGNÓSTICO REMOTO BAJO DEMANDA (Plan 031 · T5, ADR-0023 capa 3; mapa
// §2.4): D5 pide a una sesión del Edge su bundle de diagnóstico y D6 lo descarga. El bundle es
// material OPERATIVO saneado en origen por el Edge: aquí viaja opaco, sin llaves ni credenciales.
//
// En el rojo solo existían los puertos, DiagnosticsDeps y MountDiagnostics; los dos handlers y
// sus auxiliares nacieron con el verde. Reutiliza de messages.go el puerto SessionLister, la
// guarda sessionBelongsToTenant y la traducción writeSendError, y de deadlines.go los plazos.

package apipublica

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/diagnostics"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// DiagnosticsRequester emite un DiagnosticsRequest por el stream CloudLink a una sesión. Lo
// satisface *grpc.Server del módulo edge NUEVO (internal/modulos/edge/grpc). El command_id lo
// genera y persiste D5 ANTES de llamar (correlación sin carrera), y la llamada NO espera el
// bundle. De su error D5 espera lo mismo que D1 del de SendText (ver MessageSender).
type DiagnosticsRequester interface {
	RequestDiagnostics(ctx context.Context, sessionID, commandID, scope string) error
}

// DiagnosticsStore persiste las solicitudes y sus bundles, y resuelve el consentimiento por
// tenant. Lo satisfacen *diagnostics.Postgres del módulo edge NUEVO y, en los tests,
// *diagnosticshelpertest.Memoria. Toda operación va acotada al tenant del token (INV-8). Es el
// subconjunto de diagnostics.Store que la cara consume (la recepción del bundle es del gateway).
type DiagnosticsStore interface {
	// ConsentEnabled: default ON (opt-out); false solo si el tenant lo desactivó.
	ConsentEnabled(ctx context.Context, tenantID string) (bool, error)
	// CreateRequest registra la solicitud pendiente que el bundle correlacionará.
	CreateRequest(ctx context.Context, tenantID, sessionID, commandID, requestedBy string, expiresAt time.Time) error
	// DeleteRequest borra la solicitud del tenant: el rollback de D5.
	DeleteRequest(ctx context.Context, tenantID, commandID string) error
	// GetBundle devuelve el bundle listo, o diagnostics.ErrNotFound, ErrExpired o ErrPending.
	GetBundle(ctx context.Context, tenantID, commandID string) (diagnostics.Record, error)
}

// DiagnosticsDeps es lo que D5 y D6 necesitan. En la cara vieja era publicapi.DiagDeps más los
// campos Sessions y DBTimeout de publicapi.Deps.
type DiagnosticsDeps struct {
	// Diagnostics es el almacén de solicitudes y bundles. nil ⇒ no se monta ninguna de las dos.
	Diagnostics DiagnosticsStore
	// DiagnosticsRequester es el gateway CloudLink. nil ⇒ no se monta ninguna de las dos.
	DiagnosticsRequester DiagnosticsRequester
	// Sessions es la flota, para la guarda de aislamiento de D5. nil ⇒ no se monta ninguna.
	Sessions SessionLister
	// BundleTTL (DiagnosticsBundleTTL en la cara vieja) es la retención de la solicitud y de su
	// bundle, cableada desde config. <= 0 ⇒ 30 min.
	BundleTTL time.Duration
	// DBTimeout es el plazo de CADA lectura a BD (el consentimiento y la guarda de D5, el bundle
	// de D6), cableado desde config.PublicAPIDBTimeout. <= 0 ⇒ 1,5 s.
	DBTimeout time.Duration
}

// MountDiagnostics registra en c las DOS rutas o NINGUNA: solo si d.Diagnostics,
// d.DiagnosticsRequester y d.Sessions son los tres distintos de nil (si falta cualquiera, ninguna
// existe: 404 de ruta inexistente). Las dos llevan cadena W y el permiso "diagnostics.request":
//
//   - D5 "POST /api/v1/sessions/{id}/diagnostics", recurso de auditoría "session";
//   - D6 "GET /api/v1/diagnostics/{command_id}", recurso "diagnostics". Es una LECTURA auditada
//     a propósito: descargar un bundle es sensible.
//
// La cadena es la de Common: 401 sin token, 403 sin el permiso o con un token sin empresa, y
// EXACTAMENTE un registro de auditoría por petición que pasa el permiso ("success" o "failure"
// según el código), también en D6.
//
// D5 pide el diagnóstico de la sesión {id} del tenant DEL TOKEN (INV-8: el tenant no viaja en
// la petición). El cuerpo es OPCIONAL: {"scope"}; vacío, sin scope o con uno en blanco ⇒ "full"
// (el scope viaja recortado). En orden:
//
//   - CONSENTIMIENTO: d.Diagnostics.ConsentEnabled(tenant), acotado por d.DBTimeout. Vencido el
//     plazo ⇒ 504 {"error":"la verificación del consentimiento no respondió a tiempo,
//     reintenta"} y una línea Warn "lectura a BD vencida: se responde 504" con
//     "op"="diagnostics.consent", "tenant_id" y "session_id"; otro fallo ⇒ 500 {"error":"no se
//     pudo verificar el consentimiento"} (un fallo NO abre la capacidad); opt-out ⇒ 403
//     {"error":"el tenant desactivó el diagnóstico remoto (opt-out)"};
//   - GUARDA DE TENANT: d.Sessions.List(tenant), acotada por d.DBTimeout. Vencida ⇒ 504
//     {"error":"la verificación de la sesión no respondió a tiempo, reintenta"} y la misma línea
//     Warn con "op"="diagnostics.guarda_tenant"; otro fallo ⇒ 500 {"error":"no se pudo verificar
//     la sesión"}; la sesión no es del tenant ⇒ 404 {"error":"sesión no encontrada para el
//     tenant"} (404 y no 403: no se revela si existe en OTRO tenant);
//   - cuerpo que no es JSON ⇒ 400 {"error":"cuerpo JSON inválido"}. Va DESPUÉS de las dos
//     consultas, como en la cara vieja;
//   - genera el command_id (diagnostics.NewCommandID) y registra la solicitud con
//     CreateRequest(tenant, {id}, command_id, subject del token, ahora + d.BundleTTL —<= 0 ⇒
//     30 min—). Si falla ⇒ 500 {"error":"no se pudo registrar la solicitud"} y no se emite nada;
//   - d.DiagnosticsRequester.RequestDiagnostics({id}, command_id, scope). Si falla, ROLLBACK:
//     DeleteRequest(tenant, command_id), para no dejar una solicitud que nunca recibirá bundle
//     —si el rollback falla, una línea Warn "diagnóstico: rollback de solicitud tras push
//     fallido falló" con "tenant_id", "command_id" y "error", sin cambiar la respuesta—, y el
//     error se traduce como el de un envío (ver MountMessages: sesión offline ⇒ 502, los 504 del
//     empuje, resto ⇒ 500 "no se pudo enviar el texto", con su línea Error);
//   - 202 {"command_id","session_id","status":"pending","expires_at"} —expires_at en RFC 3339
//     UTC, el mismo instante que recibió CreateRequest— y una línea Info "diagnóstico remoto
//     solicitado" con "tenant_id", "subject", "session_id", "command_id" y "scope". En el camino
//     feliz NO hay DeleteRequest.
//
// Ninguno de los desenlaces anteriores al empuje emite nada al Edge, así que sus 504 sí dicen
// «reintenta». CreateRequest y RequestDiagnostics reciben el contexto de la petición SIN plazo
// propio (d.DBTimeout acota las lecturas).
//
// 🔴 El ROLLBACK no: DeleteRequest va DESENGANCHADO de la cancelación de la petición y con plazo
// propio, d.DBTimeout (<= 0 ⇒ 1,5 s). Aquí la cara nueva SE APARTA de la vieja a propósito
// (D-F3-11, 2026-10-06): la vieja lo llamaba con r.Context(), y si el cliente se iba mientras se
// empujaba al Edge —justo cuando el empuje suele fallar— el DELETE moría con el contexto
// cancelado y la solicitud quedaba pendiente hasta su TTL.
//
// D6 descarga el bundle {command_id} del tenant DEL TOKEN: GetBundle(tenant, command_id),
// acotado por d.DBTimeout. El plazo vencido se mira ANTES que los centinelas del store:
//
//   - plazo vencido ⇒ 504 {"error":"la lectura del diagnóstico no respondió a tiempo, reintenta
//     la descarga"} y la línea Warn con "op"="diagnostics.bundle", "tenant_id" y "command_id";
//   - diagnostics.ErrNotFound ⇒ 404 {"error":"diagnóstico no encontrado"}: no existe, o es de
//     OTRO tenant (404 opaco, INV-8);
//   - diagnostics.ErrExpired ⇒ 410 {"error":"diagnóstico expirado"};
//   - diagnostics.ErrPending ⇒ 202 {"command_id","message":"el Edge aún no respondió; reintentar
//     la descarga","status":"pending"};
//   - otro fallo ⇒ 500 {"error":"no se pudo leer el diagnóstico"};
//   - listo ⇒ 200 {"command_id","session_id","requested_by","requested_at","received_at",
//     "log_tail","goroutine_dump","subsystems_json"}, en ese orden, sin omitir ninguno, con los
//     dos instantes en RFC 3339 UTC, y una línea Info "diagnóstico remoto descargado" con
//     "tenant_id", "subject", "session_id" y "command_id".
//
// CERO PII y cero contenido del bundle en los logs. k.Log nil ⇒ mismas respuestas, sin líneas.
//
// Defensas que el mux y los tokens de sharedjwt no dejan alcanzar: una identidad sin empresa
// recibiría 401 {"error":"autenticación requerida"}; un {id} vacío, 400 {"error":"session id
// requerido en la ruta"}; un {command_id} vacío, 400 {"error":"command_id requerido en la ruta"}.
//
// Fallo de cableado: k.MW nil hace panic AL MONTAR (ver Common), se monten las rutas o no.
func MountDiagnostics(c *Cara, k Common, d DiagnosticsDeps) {
	mustHaveMW(k, "MountDiagnostics")

	// Diagnóstico remoto bajo demanda (Plan 031 · T5, ADR-0023 capa 3). POST emite un
	// DiagnosticsRequest a la sesión {id} (gate de consentimiento por tenant default
	// ON ⇒ 403 si opt-out; aislamiento session→tenant ⇒ 404); GET descarga el bundle
	// por command_id (202 pendiente / 200 listo / 410 expirado / 404 no encontrado).
	// AMBAS rutas exigen el grant diagnostics.request y se AUDITAN (protect: la descarga
	// se audita a propósito, es una lectura sensible). Solo se montan con el store, el
	// emisor y el listador de sesiones cableados.
	if d.Diagnostics == nil || d.DiagnosticsRequester == nil || d.Sessions == nil {
		return
	}
	ttl := d.BundleTTL
	if ttl <= 0 {
		ttl = defaultDiagnosticsTTL
	}
	c.Handle("POST /api/v1/sessions/{id}/diagnostics", protect(k, "diagnostics.request", "session",
		requestDiagnosticsHandler(d.DiagnosticsRequester, d.Diagnostics, d.Sessions, ttl, d.DBTimeout, k.Log)))
	c.Handle("GET /api/v1/diagnostics/{command_id}", protect(k, "diagnostics.request", "diagnostics",
		getDiagnosticsHandler(d.Diagnostics, d.DBTimeout, k.Log)))
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
//
// Sus DOS consultas a BD —el consentimiento y la guarda de tenant— van acotadas con
// el mismo presupuesto (dbTimeout, ver dbCtx en deadlines.go; Plan 050 · Ola 3 ·
// T3.3). Las dos ocurren ANTES de emitir nada al Edge, así que un plazo vencido
// responde 504 y reintentar es seguro. <=0 cae al suelo de dbCtx.
func preflightDiagnostics(w http.ResponseWriter, r *http.Request, store DiagnosticsStore, sessions SessionLister, dbTimeout time.Duration, log sharedlogger.Logger) (httpapi.Identity, string, bool) {
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
	ctx, cancel := dbCtx(r.Context(), dbTimeout)
	consented, err := store.ConsentEnabled(ctx, id.TenantID)
	cancel()
	if err != nil {
		if dbTimedOut504(w, log, err, "la verificación del consentimiento no respondió a tiempo, reintenta",
			"op", "diagnostics.consent", "tenant_id", id.TenantID, "session_id", sessionID) {
			return id, "", false
		}
		writeError(w, http.StatusInternalServerError, "no se pudo verificar el consentimiento")
		return id, "", false
	}
	if !consented {
		writeError(w, http.StatusForbidden, "el tenant desactivó el diagnóstico remoto (opt-out)")
		return id, "", false
	}
	// Aislamiento por tenant (INV-8): la sesión debe ser del tenant del token.
	belongs, err := sessionBelongsToTenant(r.Context(), sessions, id.TenantID, sessionID, dbTimeout)
	if err != nil {
		if dbTimedOut504(w, log, err, "la verificación de la sesión no respondió a tiempo, reintenta",
			"op", "diagnostics.guarda_tenant", "tenant_id", id.TenantID, "session_id", sessionID) {
			return id, "", false
		}
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
// Las respuestas, una a una, están en el contrato de MountDiagnostics.
//
// El rollback va por rollbackRequest (D-F3-11).
//
// gw y store nunca son nil: MountDiagnostics no monta la ruta sin ellos (la cara vieja
// repetía aquí esa guarda con un 500 «diagnóstico remoto no configurado» inalcanzable).
func requestDiagnosticsHandler(gw DiagnosticsRequester, store DiagnosticsStore, sessions SessionLister, ttl, dbTimeout time.Duration, log sharedlogger.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Identidad + consentimiento (default ON, opt-out) + aislamiento session→tenant.
		id, sessionID, ok := preflightDiagnostics(w, r, store, sessions, dbTimeout, log)
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
			if derr := rollbackRequest(r.Context(), store, id.TenantID, commandID, dbTimeout); derr != nil && log != nil {
				log.Warn("diagnóstico: rollback de solicitud tras push fallido falló",
					"tenant_id", id.TenantID, "command_id", commandID, "error", derr)
			}
			writeSendError(w, err, log, sessionID)
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

// rollbackRequest borra la solicitud que D5 acaba de registrar y no pudo emitir. Lo hace con un
// contexto que NO hereda la cancelación de la petición (context.WithoutCancel) y acotado por el
// plazo de las consultas a BD (dbCtx; <= 0 ⇒ defaultDBTimeout): el borrado tiene que ocurrir
// también cuando el cliente ya se fue, que es el caso que lo necesita, y no puede esperar sin fin
// a una base lenta (D-F3-11). Mismo criterio que el empuje de perfil de sessionadmin.go.
func rollbackRequest(ctx context.Context, store DiagnosticsStore, tenantID, commandID string, dbTimeout time.Duration) error {
	rctx, cancel := dbCtx(context.WithoutCancel(ctx), dbTimeout)
	defer cancel()
	return store.DeleteRequest(rctx, tenantID, commandID)
}

// getDiagnosticsHandler devuelve GET /api/v1/diagnostics/{command_id}: descarga el
// bundle almacenado del tenant del token (Plan 031 · T5). Mismo grant que el request
// (diagnostics.request) y también auditado (protect). Las respuestas, una a una, están
// en el contrato de MountDiagnostics.
//
// dbTimeout acota la lectura del bundle (Plan 050 · Ola 3 · T3.3, ver dbCtx en
// deadlines.go). <=0 cae al suelo de dbCtx. store nunca es nil (ver
// requestDiagnosticsHandler).
func getDiagnosticsHandler(store DiagnosticsStore, dbTimeout time.Duration, log sharedlogger.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}
		commandID := r.PathValue("command_id")
		if commandID == "" {
			writeError(w, http.StatusBadRequest, "command_id requerido en la ruta")
			return
		}

		ctx, cancel := dbCtx(r.Context(), dbTimeout)
		defer cancel()
		rec, err := store.GetBundle(ctx, id.TenantID, commandID)
		// El plazo vencido va ANTES del switch a propósito: los errores tipados del
		// store (ErrNotFound/ErrExpired/ErrPending) responden sobre lo que se LEYÓ, y
		// aquí no se llegó a leer nada. Colarlo en el `case err != nil` lo convertiría
		// en un 500 indistinguible de una base rota.
		if dbTimedOut504(w, log, err, "la lectura del diagnóstico no respondió a tiempo, reintenta la descarga",
			"op", "diagnostics.bundle", "tenant_id", id.TenantID, "command_id", commandID) {
			return
		}
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
