package publicapi

import (
	"net/http"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// sessionDTO es una fila del listado GET /api/v1/sessions. Expone SOLO metadatos
// de operación de la sesión (REQ-A2/A4): jamás credenciales ni PII más allá del
// número propio (self_pn), que ya se persiste en fleet_sessions (Plan 020 · T2).
// Los campos opcionales (self_pn, timestamps) se omiten si no se conocen todavía.
type sessionDTO struct {
	SessionID       string `json:"session_id"`
	EdgeID          string `json:"edge_id"`
	State           string `json:"state"`
	Role            string `json:"role"`
	SelfPn          string `json:"self_pn,omitempty"`
	LastConnectedAt string `json:"last_connected_at,omitempty"`
	LastSeenAt      string `json:"last_seen_at,omitempty"`
}

// listSessionsHandler devuelve el handler de GET /api/v1/sessions: lista las
// sesiones/teléfonos vinculados del tenant del token (INV-8), cada una con su
// estado (online|offline|loggedout), rol (bot|passive) y número propio si se
// conoce. Solo lectura. 200 con el arreglo (vacío si el tenant no tiene sesiones);
// 401 sin identidad; 500 ante fallo del listador. fleet.List ya filtra por tenant:
// una sesión de otro tenant NUNCA aparece (aislamiento por tenant, INV-8).
func listSessionsHandler(sessions SessionLister) http.Handler {
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
				SessionID: s.SessionID,
				EdgeID:    s.EdgeID,
				State:     string(s.State),
				Role:      string(s.Role),
				SelfPn:    s.SelfPn,
			}
			if !s.LastConnectedAt.IsZero() {
				dto.LastConnectedAt = s.LastConnectedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
			}
			if !s.LastSeenAt.IsZero() {
				dto.LastSeenAt = s.LastSeenAt.UTC().Format("2006-01-02T15:04:05Z07:00")
			}
			out = append(out, dto)
		}
		writeJSON(w, http.StatusOK, out)
	})
}
