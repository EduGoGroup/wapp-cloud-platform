// Porta internal/publicapi/sessions.go @ 115a4ba (listSessionsHandler, sessionDTO, isoFormat) y
// el registro de D2, D3 y D4 (internal/publicapi/publicapi.go @ 115a4ba, líneas 498-550).
//
// sessions.go — LAS SESIONES (TELÉFONOS VINCULADOS) DEL TENANT (mapa §2.4): D2 las lista, con su
// salud derivada (health.go); D3 y D4 fijan su perfil y su estado con los handlers de
// sessionadmin.go, a los que este fichero pone la cadena W. El puerto SessionLister, que la cara
// vieja compartía entre el envío y este listado, vive en messages.go.
//
// En el rojo solo existían SessionsDeps y MountSessions; el handler de D2 y su fila
// (sessionDTO) nacieron con el verde.

package apipublica

import (
	"net/http"
	"time"

	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// SessionsDeps es lo que D2, D3 y D4 necesitan. Cada ruta se monta según SU dependencia (ver
// MountSessions). En la cara vieja era publicapi.SessionDeps más los campos Health, Alerter y
// DBTimeout de publicapi.Deps.
type SessionsDeps struct {
	// Sessions es la flota: lista las sesiones del tenant (D2). nil ⇒ D2 no se monta.
	Sessions SessionLister
	// Health deriva el campo "health" de cada fila de D2. Su valor cero vale (5 min / 2 min).
	Health HealthRules
	// Alerter es el seam del alerting push de D2 (ADR-0023). nil ⇒ NoopAlerter.
	Alerter Alerter
	// DBTimeout es el plazo del listado de D2, cableado desde config.PublicAPIDBTimeout. <= 0 ⇒
	// 1,5 s.
	DBTimeout time.Duration
	// SessionProfiles persiste el perfil de una sesión (D3). nil ⇒ D3 no se monta.
	SessionProfiles SessionProfileStore
	// ProfilePush empuja el perfil recién persistido a la sesión viva (D3). nil NO desmonta D3:
	// es un no-op (el empuje al conectar reconcilia).
	ProfilePush ProfilePusher
	// SessionStatus persiste el estado de una sesión (D4). nil ⇒ D4 no se monta.
	SessionStatus SessionStatusStore
}

// MountSessions registra en c, cada una SOLO si su dependencia no es nil (sin ella la ruta no
// existe: 404 de ruta inexistente):
//
//   - D2 "GET /api/v1/sessions" si d.Sessions != nil — cadena R, permiso "sessions.read", sin
//     auditoría;
//   - D3 "POST /api/v1/sessions/{id}/profile" si d.SessionProfiles != nil — cadena W, permiso
//     "sessions.write", recurso de auditoría "session"; sirve
//     SetSessionProfileHandler(d.SessionProfiles, d.ProfilePush, k.Log). d.ProfilePush nil NO la
//     desmonta;
//   - D4 "POST /api/v1/sessions/{id}/status" si d.SessionStatus != nil — cadena W, permiso
//     "sessions.write", recurso "session"; sirve SetSessionStatusHandler(d.SessionStatus).
//
// Las cadenas son las de Common: 401 sin token, 403 sin el permiso o con un token sin empresa, y
// en D3 y D4 EXACTAMENTE un registro de auditoría por petición que pasa el permiso ("success" o
// "failure" según el código); en D2, ninguno. Lo que D3 y D4 responden pasada la cadena lo dicen
// sus handlers (sessionadmin.go): sus errores son TEXTO PLANO, no el JSON {"error"} de D2.
//
// D2 lista las sesiones del tenant DEL TOKEN (INV-8: el tenant no viaja en la petición, y
// d.Sessions.List recibe ese tenant y ningún otro, así que una sesión ajena NUNCA aparece). Solo
// lectura. Responde 200 con un arreglo JSON —`[]`, nunca `null`, si el tenant no tiene
// sesiones— de una fila por sesión, en el orden de List, con estos campos EN ESTE ORDEN:
//
//	session_id, edge_id, state, self_pn*, last_connected_at*, last_seen_at*, profile, health*,
//	whatsapp_state*, degraded_reason*, degraded_since*, last_health_at*, last_event_age_s*,
//	outbox_depth*, binary_version*, uptime_s*, dek_load_duration_ms*, intent_circuit*,
//	worker_taskset*, intent_p50_ms*, intent_omitted_by_reason*, stuck_heads*, stuck_head_polls*,
//	failed_seal_dispatch*, failed_seal_budget*
//
// Los marcados con * se OMITEN si no se conocen (cadena vacía, instante cero, entero 0, puntero o
// mapa nil). Reglas que el cuerpo cumple:
//
//   - "state" es el del registro del stream CloudLink (online|offline|loggedout), SEPARADO de
//     "whatsapp_state", que es la verdad del socket;
//   - "profile" (active|passive) va SIEMPRE, sin omitirse: la columna es NOT NULL, así que
//     siempre se sabe, y un campo ausente diría «no lo sé». El campo `role` no existe (0064);
//   - los cuatro instantes van en RFC 3339, en UTC ("2006-01-02T15:04:05Z");
//   - "health" es d.Health aplicado a la sesión (ver HealthRules): "degraded", "stale" u omitido;
//   - el bloque del worker (worker_taskset … failed_seal_budget) se copia TAL CUAL: ningún
//     default ni suma. intent_p50_ms, stuck_heads, stuck_head_polls, failed_seal_dispatch y
//     failed_seal_budget son punteros: nil se omite y un 0 MEDIDO viaja como 0 (ausencia ≠ cero);
//     intent_omitted_by_reason viaja clave a clave, sin agregarse en un total;
//   - solo metadatos de operación: CERO credenciales y ninguna PII más allá de self_pn.
//
// Por cada sesión con "health" no vacío D2 llama UNA vez a d.Alerter.Alert(tenant del token,
// session_id, health) —d.Alerter nil ⇒ NoopAlerter—. Es best-effort: un error del Alerter no
// altera la respuesta y deja una línea Debug "alerting de salud falló (best-effort)" con
// "session_id", "estado" y "error". Una sesión sin salud derivada no lo invoca.
//
// PLAZO. List corre acotado por d.DBTimeout (<= 0 ⇒ 1,5 s): es la ruta que la consola consulta
// cada pocos segundos, y una base lenta debe ser un 504 legible, no una pantalla colgada. Vencido
// el plazo ⇒ 504 {"error":"el listado de sesiones no respondió a tiempo, reintenta"} y una línea
// Warn "lectura a BD vencida: se responde 504" con "op"="sessions.list" y "tenant_id"; otro fallo
// de List ⇒ 500 {"error":"no se pudieron listar las sesiones"}, sin esa línea.
//
// k.Log nil ⇒ mismas respuestas, sin líneas. Defensa que los tokens de sharedjwt no alcanzan
// (RequirePermission corta antes): una identidad sin empresa que llegara al handler de D2
// recibiría 401 {"error":"autenticación requerida"}.
//
// Fallo de cableado: k.MW nil hace panic AL MONTAR (ver Common), se monte alguna ruta o ninguna.
func MountSessions(c *Cara, k Common, d SessionsDeps) {
	mustHaveMW(k, "MountSessions")

	// Listar las sesiones/teléfonos vinculados del tenant (Plan 021 · T0, R-A1).
	// Lectura sin auditoría (idempotente), acotada al tenant del token (INV-8):
	// fleet.List filtra por tenant, así que una sesión ajena NUNCA aparece. Reusa
	// el MISMO SessionLister que ya alimenta el aislamiento del envío (sin nueva
	// dependencia). Solo expone metadatos de operación (CERO credenciales/PII).
	if d.Sessions != nil {
		alerter := d.Alerter
		if alerter == nil {
			alerter = NoopAlerter{} // ADR-0023: seam del alerting push, no-op por defecto.
		}
		c.Handle("GET /api/v1/sessions", protectRead(k, "sessions.read",
			listSessionsHandler(d.Sessions, d.Health, alerter, d.DBTimeout, k.Log)))
	}

	// PERFIL de sesión active|passive (Plan 046 · T1.2, ADR-0027): sucede a /role con
	// el vocabulario del dueño. MISMO scope (sessions.write), MISMA auditoría y MISMO
	// aislamiento al tenant del token (INV-8); reusa el MISMO handler que
	// /admin/sessions/{id}/profile. El push al Edge es best-effort (d.ProfilePush nil ⇒
	// no-op).
	if d.SessionProfiles != nil {
		c.Handle("POST /api/v1/sessions/{id}/profile", protect(k, "sessions.write", "session",
			SetSessionProfileHandler(d.SessionProfiles, d.ProfilePush, k.Log)))
	}

	// Estatus de sesión (Plan 020 · T3): retirar/limpiar un zombie (loggedout) o
	// dejar offline. Escritura auditada (sessions.write), acotada al tenant del token
	// (INV-8); reusa el MISMO handler que /admin/sessions/{id}/status.
	if d.SessionStatus != nil {
		c.Handle("POST /api/v1/sessions/{id}/status", protect(k, "sessions.write", "session",
			SetSessionStatusHandler(d.SessionStatus)))
	}
}

// isoFormat es el layout de los timestamps del DTO (RFC3339 en UTC).
const isoFormat = "2006-01-02T15:04:05Z07:00"

// sessionDTO es una fila del listado GET /api/v1/sessions. Expone SOLO metadatos
// de operación de la sesión (REQ-A2/A4): jamás credenciales ni PII más allá del
// número propio (self_pn), que ya se persiste en fleet_sessions (Plan 020 · T2).
// Los campos opcionales (self_pn, timestamps, salud) se omiten si no se conocen.
//
// Plan 031 · T4 (ADR-0023): suma la salud REAL del socket (whatsapp_state y su
// snapshot) SEPARADA de State (registro del stream CloudLink), más el estado
// DERIVADO health ("degraded"|"stale"|omitido) calculado al servir. Todo son
// metadatos de salud: CERO credenciales/llaves.
type sessionDTO struct {
	SessionID       string `json:"session_id"`
	EdgeID          string `json:"edge_id"`
	State           string `json:"state"`
	SelfPn          string `json:"self_pn,omitempty"`
	LastConnectedAt string `json:"last_connected_at,omitempty"`
	LastSeenAt      string `json:"last_seen_at,omitempty"`

	// Profile es el PERFIL de negocio de la sesión (active|passive, Plan 046 · T1.2,
	// ADR-0027), con el vocabulario del dueño («activa / pasiva», D-046.6). Va SIN
	// omitempty a propósito: la columna es NOT NULL con DEFAULT 'passive', así que
	// siempre lo sabemos, y un campo ausente le diría al cliente «no lo sé» cuando sí
	// lo sabemos.
	//
	// 📌 El campo `role` que acompañaba a este DESAPARECIÓ del DTO con la 0064: era el
	// alias legado, no lo consumía nadie fuera de esta plataforma y su ciclo de
	// deprecación no protegía a ningún cliente real (D-046.1 revisada).
	Profile string `json:"profile"`

	// Salud (Plan 031 · T4). Health es el estado derivado; el resto es el snapshot.
	Health            string `json:"health,omitempty"`
	WhatsappState     string `json:"whatsapp_state,omitempty"`
	DegradedReason    string `json:"degraded_reason,omitempty"`
	DegradedSince     string `json:"degraded_since,omitempty"`
	LastHealthAt      string `json:"last_health_at,omitempty"`
	LastEventAgeS     int64  `json:"last_event_age_s,omitempty"`
	OutboxDepth       int64  `json:"outbox_depth,omitempty"`
	BinaryVersion     string `json:"binary_version,omitempty"`
	UptimeS           int64  `json:"uptime_s,omitempty"`
	DekLoadDurationMs int64  `json:"dek_load_duration_ms,omitempty"`

	// IntentCircuit es el breaker del clasificador: "closed"|"open"|"half_open".
	// ⚠️ Hasta cloudlink v0.12.0 SIEMPRE viajaba vacío; desde el 051 · T4.3 llega
	// lleno, así que este campo EMPIEZA A APARECER en la respuesta. Ausente sigue
	// significando «el Edge no lo sabe», NUNCA "closed" (decisión 4 de la Ola 4).
	// Es la mitad "breaker abierto" del criterio de T4.3.
	IntentCircuit string `json:"intent_circuit,omitempty"`

	// --- Salud del WORKER del cajero de intents (Plan 051 · T4.3). 🔴 Los campos
	// medibles van en PUNTERO/mapa: se OMITEN cuando el Edge no los sabe, y un 0
	// presente es un 0 medido. Un consumidor NO puede pintar la ausencia como
	// "disjunta" ni como "0 ms": la ausencia se pinta como DESCONOCIDO. ---

	// WorkerTaskset: "disjunta"|"solapada"|"cajero_sin_confinar". Ausente = el Edge
	// no lo sabe (no es Linux, o el parte del worker está rancio). Es la mitad
	// "taskset" del criterio de T4.3.
	WorkerTaskset string `json:"worker_taskset,omitempty"`
	// IntentP50Ms es el p50 de la INFERENCIA en ms. Ausente = no medible; NO es
	// "0 ms". No confundir con el p50 del handler de whatsmeow (otra población).
	IntentP50Ms *int64 `json:"intent_p50_ms,omitempty"`
	// IntentOmittedByReason: motivo→conteo de los despachos sin intent. 🔴 NUNCA se
	// agrega en un total ("fastlane" es el camino SANO; "presupuesto"/"breaker" son
	// FALLOS). Solo trae claves con valor distinto de cero: clave ausente ≠ cero
	// medido, y ausencia del objeto entero = no reportado.
	IntentOmittedByReason map[string]int64 `json:"intent_omitted_by_reason,omitempty"`
	// StuckHeads/StuckHeadPolls: cabeza de cola atascada (T3.12). Sin ellos, cero
	// despachos se lee igual "no hay trabajo" que "el trabajo no avanza".
	StuckHeads     *int64 `json:"stuck_heads,omitempty"`
	StuckHeadPolls *int64 `json:"stuck_head_polls,omitempty"`
	// FailedSealDispatch/FailedSealBudget: 🔴 SEPARADOS a propósito — solo el
	// primero implica mensajes DUPLICADOS. Agregarlos deshace T3.12.
	FailedSealDispatch *int64 `json:"failed_seal_dispatch,omitempty"`
	FailedSealBudget   *int64 `json:"failed_seal_budget,omitempty"`
}

// listSessionsHandler devuelve el handler de GET /api/v1/sessions: lista las
// sesiones/teléfonos vinculados del tenant del token (INV-8), cada una con su
// estado de link (online|offline|loggedout), perfil (active|passive), número propio si se
// conoce y la salud real del socket con su estado derivado (Plan 031 · T4). Solo
// lectura. 200 con el arreglo (vacío si el tenant no tiene sesiones); 401 sin
// identidad; 500 ante fallo del listador. fleet.List ya filtra por tenant: una
// sesión de otro tenant NUNCA aparece (aislamiento por tenant, INV-8).
//
// rules deriva health al servir; alerter es el punto de extensión del alerting push
// (ADR-0023): hoy no-op, se invoca best-effort por cada sesión con salud derivada
// para dejar el seam vivo (nada se empuja todavía).
//
// dbTimeout acota el listado (Plan 050 · Ola 3 · T3.3, ver dbCtx en deadlines.go):
// esta ruta es la que consulta la consola cada pocos segundos, así que una base lenta
// se traduce en pantallas colgadas en vez de en un 504 legible. <=0 cae al suelo de
// dbCtx. Con el plazo vencido responde 504 (transitorio), no 500.
func listSessionsHandler(sessions SessionLister, rules HealthRules, alerter Alerter, dbTimeout time.Duration, log sharedlogger.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}
		ctx, cancel := dbCtx(r.Context(), dbTimeout)
		defer cancel()
		list, err := sessions.List(ctx, id.TenantID)
		if err != nil {
			if dbTimedOut504(w, log, err, "el listado de sesiones no respondió a tiempo, reintenta",
				"op", "sessions.list", "tenant_id", id.TenantID) {
				return
			}
			writeError(w, http.StatusInternalServerError, "no se pudieron listar las sesiones")
			return
		}
		out := make([]sessionDTO, 0, len(list))
		for _, s := range list {
			dto := sessionDTO{
				SessionID:         s.SessionID,
				EdgeID:            s.EdgeID,
				State:             string(s.State),
				Profile:           string(s.Profile),
				SelfPn:            s.SelfPn,
				Health:            rules.derive(s),
				WhatsappState:     s.WhatsappState,
				DegradedReason:    s.DegradedReason,
				LastEventAgeS:     s.LastEventAgeS,
				OutboxDepth:       s.OutboxDepth,
				BinaryVersion:     s.BinaryVersion,
				UptimeS:           s.UptimeS,
				DekLoadDurationMs: s.DekLoadDurationMs,
				IntentCircuit:     s.IntentCircuit,

				// Bloque del worker (Plan 051 · T4.3): se copia TAL CUAL, punteros y
				// mapa incluidos. Ningún COALESCE, ninguna suma, ningún default: nil
				// viaja como campo ausente y eso es lo que significa «no lo sé».
				WorkerTaskset:         s.WorkerTaskset,
				IntentP50Ms:           s.IntentP50Ms,
				IntentOmittedByReason: s.IntentOmittedByReason,
				StuckHeads:            s.StuckHeads,
				StuckHeadPolls:        s.StuckHeadPolls,
				FailedSealDispatch:    s.FailedSealDispatch,
				FailedSealBudget:      s.FailedSealBudget,
			}
			if !s.LastConnectedAt.IsZero() {
				dto.LastConnectedAt = s.LastConnectedAt.UTC().Format(isoFormat)
			}
			if !s.LastSeenAt.IsZero() {
				dto.LastSeenAt = s.LastSeenAt.UTC().Format(isoFormat)
			}
			if !s.DegradedSince.IsZero() {
				dto.DegradedSince = s.DegradedSince.UTC().Format(isoFormat)
			}
			if !s.LastHealthAt.IsZero() {
				dto.LastHealthAt = s.LastHealthAt.UTC().Format(isoFormat)
			}
			// Seam del alerting push (ADR-0023): best-effort, no-op hoy. Un error del
			// Alerter no afecta la lectura (la salud ya está en la respuesta): se
			// registra a Debug si hay logger.
			if dto.Health != "" && alerter != nil {
				if aerr := alerter.Alert(r.Context(), id.TenantID, s.SessionID, dto.Health); aerr != nil && log != nil {
					log.Debug("alerting de salud falló (best-effort)",
						"session_id", s.SessionID, "estado", dto.Health, "error", aerr)
				}
			}
			out = append(out, dto)
		}
		writeJSON(w, http.StatusOK, out)
	})
}
