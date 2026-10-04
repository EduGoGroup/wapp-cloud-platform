// Porta internal/publicapi/audit.go @ 9a77307 (listAuditHandler, toAuditDTOs) y
// el registro de C1 (internal/publicapi/publicapi.go @ 9a77307, línea 604).
//
// audit.go — LA BITÁCORA DE LA EMPRESA DEL TOKEN (mapa §2.3, C1). parseIntQuery, que en la cara
// vieja vivía en este fichero, nació en response.go: la usan también áreas de F4, F6 y F8 (T-15).

package apipublica

import (
	"context"
	"net/http"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// maxAuditPageSize acota el tamaño de página del listado de auditoría.
const maxAuditPageSize = 500

// AuditReader es el puerto de lectura de la bitácora que consume C1: el subconjunto de
// in.Auditor (módulo acceso nuevo) que hace falta aquí. En el arranque es el MISMO servicio que
// graba las escrituras (Common.Auditor).
type AuditReader interface {
	// ListAudit devuelve los eventos de tenantID, los más recientes primero, paginados.
	ListAudit(ctx context.Context, tenantID string, limit, offset int) ([]domain.AuditEvent, error)
}

// AuditDeps es lo que C1 necesita.
type AuditDeps struct {
	// Audit lee la bitácora. nil = C1 no se monta.
	Audit AuditReader
}

// MountAudit registra en c "GET /api/v1/audit" (C1), solo si d.Audit no es nil (sin él, 404 de
// ruta inexistente), con cadena R y permiso "audit.read" (ver Common: 401 sin token, 403 sin el
// permiso o con un token sin empresa, y ningún registro de auditoría).
//
// La respuesta:
//
//   - el tenant que recibe ListAudit es SIEMPRE el del token (INV-8); un tenant en la query no
//     cuenta;
//   - paginación por ?limit y ?offset: enteros no negativos; si faltan, no son enteros o son
//     negativos valen 100 y 0; un limit por encima de 500 se recorta a 500; un 0 explícito es 0;
//   - 200 {"events":[…]}, en el orden que da el puerto, y [] (nunca null) si no hay eventos.
//     Cada evento: "id", "actor", "action", "resource", "result", "meta" (se omite si está
//     vacío) y "at" (UTC, RFC 3339 con segundos: "2006-01-02T15:04:05Z"). El tenant NO viaja
//     (es el del token). Todo es opaco: CERO PII (INV-5);
//   - un fallo de ListAudit ⇒ 500 {"error":"no se pudo listar la auditoría"}.
//
// Defensa que los tokens de sharedjwt no alcanzan (RequirePermission corta antes): una identidad
// sin empresa que llegara al handler recibiría 401 {"error":"autenticación requerida"}.
//
// Fallo de cableado: k.MW nil con d.Audit no nil hace panic AL MONTAR (ver Common).
func MountAudit(c *Cara, k Common, d AuditDeps) {
	// Lectura de la bitácora de auditoría (Plan 018 · T10, R11). Paginada, acotada
	// al tenant del token (INV-8); scope audit.read (o *.read del rol viewer).
	// Lectura sin auditoría (no tiene efecto). Los eventos ya son OPACOS (CERO PII).
	if d.Audit == nil {
		return
	}
	mustHaveMW(k, "MountAudit")
	c.Handle("GET /api/v1/audit", protectRead(k, "audit.read", listAuditHandler(d.Audit)))
}

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
		if !ok || id.TenantID == "" {
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
