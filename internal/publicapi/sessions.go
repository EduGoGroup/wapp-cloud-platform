package publicapi

import (
	"net/http"
	"time"

	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

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
	Role            string `json:"role"`
	SelfPn          string `json:"self_pn,omitempty"`
	LastConnectedAt string `json:"last_connected_at,omitempty"`
	LastSeenAt      string `json:"last_seen_at,omitempty"`

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
// estado de link (online|offline|loggedout), rol (bot|passive), número propio si se
// conoce y la salud real del socket con su estado derivado (Plan 031 · T4). Solo
// lectura. 200 con el arreglo (vacío si el tenant no tiene sesiones); 401 sin
// identidad; 500 ante fallo del listador. fleet.List ya filtra por tenant: una
// sesión de otro tenant NUNCA aparece (aislamiento por tenant, INV-8).
//
// rules deriva health al servir; alerter es el punto de extensión del alerting push
// (ADR-0023): hoy no-op, se invoca best-effort por cada sesión con salud derivada
// para dejar el seam vivo (nada se empuja todavía).
//
// dbTimeout acota el listado (Plan 050 · Ola 3 · T3.3, ver dbCtx en publicapi.go):
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
				Role:              string(s.Role),
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
