package publicapi

import (
	"net/http"

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
	IntentCircuit     string `json:"intent_circuit,omitempty"`
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
func listSessionsHandler(sessions SessionLister, rules HealthRules, alerter Alerter, log sharedlogger.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}
		list, err := sessions.List(r.Context(), id.TenantID)
		if err != nil {
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
