package publicapi

import (
	"net/http"
	"strconv"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// maxAuditPageSize acota el tamaño de página del listado de auditoría.
const maxAuditPageSize = 500

// auditEventDTO es la proyección al wire de un evento de auditoría. Todos los
// campos son OPACOS (CERO PII, INV-5): actor/resource son ids, meta es contexto
// no sensible. tenant_id se OMITE (siempre es el del token) para no repetir.
type auditEventDTO struct {
	ID       int64          `json:"id"`
	Actor    string         `json:"actor"`
	Action   string         `json:"action"`
	Resource string         `json:"resource"`
	Result   string         `json:"result"`
	Meta     map[string]any `json:"meta,omitempty"`
	At       string         `json:"at"`
}

// listAuditHandler sirve GET /api/v1/audit: la bitácora del tenant del token,
// más recientes primero, paginada por ?limit&offset. El tenant SIEMPRE sale de
// la Identity (INV-8), NUNCA del cuerpo/query.
func listAuditHandler(reader AuditReader) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "autenticación requerida"})
			return
		}
		limit := parseIntQuery(r, "limit", 100)
		if limit > maxAuditPageSize {
			limit = maxAuditPageSize
		}
		offset := parseIntQuery(r, "offset", 0)

		events, err := reader.ListAudit(r.Context(), id.TenantID, limit, offset)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "no se pudo listar la auditoría"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": toAuditDTOs(events)})
	})
}

// toAuditDTOs proyecta los eventos del dominio al wire (lista no-nil aunque vacía).
func toAuditDTOs(events []domain.AuditEvent) []auditEventDTO {
	out := make([]auditEventDTO, 0, len(events))
	for _, e := range events {
		out = append(out, auditEventDTO{
			ID:       e.ID,
			Actor:    e.Actor,
			Action:   e.Action,
			Resource: e.Resource,
			Result:   e.Result,
			Meta:     e.Meta,
			At:       e.At.UTC().Format("2006-01-02T15:04:05Z07:00"),
		})
	}
	return out
}

// parseIntQuery lee un entero no negativo de la query; def si falta o es inválido.
func parseIntQuery(r *http.Request, key string, def int) int {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 0 {
		return def
	}
	return v
}
