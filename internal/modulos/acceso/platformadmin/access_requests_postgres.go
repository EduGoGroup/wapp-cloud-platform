// Porta internal/platformadmin/access_requests.go @ 9a77307, líneas 135-336 y 481-526: el SQL de
// la bandeja de solicitudes de acceso, separado de sus reglas (D-F2-3). Es la mitad Postgres del
// puerto AccessRequestStore (ports.go); las reglas que lo orquestan viven en access_requests.go.

package platformadmin

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// ListAccessRequests implementa AccessRequestStore.ListAccessRequests: un SELECT por status
// ("" ⇒ 'pending') con ORDER BY created_at ASC. Un fallo de la consulta, del escaneo o de la
// iteración se devuelve envuelto ("platformadmin: list|scan|iterate access request(s): …").
func (r *Repository) ListAccessRequests(ctx context.Context, status string) ([]AccessRequestItem, error) {
	panic(pendiente.Implementar("platformadmin.Repository.ListAccessRequests"))
}

// CreateAccessRequest implementa AccessRequestStore.CreateAccessRequest. Valida ANTES de tocar la
// base (ErrInvalidInput sin consulta) y escribe con INSERT … ON CONFLICT (user_id) WHERE status =
// 'pending' DO NOTHING: la idempotencia la da el índice único parcial access_requests_one_pending.
// Un fallo de la sentencia se devuelve envuelto ("platformadmin: create access request: …").
func (r *Repository) CreateAccessRequest(ctx context.Context, userID, email, origin string) error {
	panic(pendiente.Implementar("platformadmin.Repository.CreateAccessRequest"))
}

// RejectAccessRequest implementa AccessRequestStore.RejectAccessRequest. Valida ANTES de tocar la
// base (requestID vacío o motivo en blanco ⇒ ErrInvalidInput sin consulta). Escribe con un UPDATE
// … WHERE id = $3 AND status = 'pending'; si no toca ninguna fila, una segunda consulta distingue
// (M-06) «no existe» (ErrNotFound) de «ya resuelta» (ErrConflict), y un fallo de ESA consulta se
// devuelve envuelto ("platformadmin: check access request existence: …"), NUNCA como
// ErrNotFound: un corte de conexión no es «otro ya la resolvió». decided_by es el operador si es
// un UUID y NULL si no.
func (r *Repository) RejectAccessRequest(ctx context.Context, requestID, reason, operatorID string) error {
	panic(pendiente.Implementar("platformadmin.Repository.RejectAccessRequest"))
}

// LookupAccessRequestStatus implementa AccessRequestStore.LookupAccessRequestStatus: un SELECT
// de user_id y status por id. Sin fila ⇒ ErrNotFound; otro fallo, envuelto ("platformadmin: read
// access request: …").
//
// ⚠️ NO comprueba la membresía cruzada con otra empresa (M-04): esa comprobación vive DENTRO de
// la transacción de ExecuteApprovalTx (GrantTenantAccess), contable.
func (r *Repository) LookupAccessRequestStatus(ctx context.Context, requestID string) (string, string, error) {
	panic(pendiente.Implementar("platformadmin.Repository.LookupAccessRequestStatus"))
}

// ResolveRoleID implementa AccessRequestStore.ResolveRoleID: SELECT id FROM iam_roles WHERE name
// = $1 OR id::text = $1. Sin fila ⇒ ErrInvalidInput; otro fallo, envuelto ("platformadmin:
// resolve role: …").
func (r *Repository) ResolveRoleID(ctx context.Context, role string) (string, error) {
	panic(pendiente.Implementar("platformadmin.Repository.ResolveRoleID"))
}

// CheckRetryApproved implementa AccessRequestStore.CheckRetryApproved con dos SELECT EXISTS, EN
// ESTE ORDEN: la membresía en tenant_members (no ⇒ ErrConflict, sin mirar el rol) y el rol en
// iam_user_roles de ESA empresa (no ⇒ ErrRetryRoleMismatch). Un fallo de cualquiera de las dos se
// devuelve envuelto ("platformadmin: check retry membership|role: …").
func (r *Repository) CheckRetryApproved(ctx context.Context, userID, tenantID, roleID string) error {
	panic(pendiente.Implementar("platformadmin.Repository.CheckRetryApproved"))
}

// ExecuteApprovalTx implementa AccessRequestStore.ExecuteApprovalTx en UNA transacción, en este
// orden:
//  1. iampostgres.GrantTenantAccess(ctx, tx, features, userID, tenantID, &roleID): cerrojo de la
//     persona, guarda de una sola empresa con multi_empresa (features del constructor), alta en
//     tenant_members y rol en iam_user_roles. Recibe la TRANSACCIÓN y no el pool: si commiteara
//     por su cuenta, una aprobación podría dar el acceso y dejar la solicitud en 'pending'. Su
//     rechazo de «una sola empresa» (domain.ErrConflict de iam) sale como ErrConflict de este
//     paquete; cualquier otro error, tal cual.
//  2. UPDATE access_requests SET status = 'approved', decided_by, decided_at = now() WHERE id = $2
//     AND status = 'pending'. Un fallo ⇒ envuelto ("platformadmin: update access request status:
//     …"); 0 filas (o no poder contarlas) ⇒ ErrConflict.
//  3. COMMIT; un fallo ⇒ envuelto ("platformadmin: commit tx: …").
//
// Cualquier error antes del COMMIT deshace TODO (ROLLBACK): ni membresía, ni rol, ni status (R-A7).
// No poder abrir la transacción ⇒ envuelto ("platformadmin: begin tx: …"). decided_by es el
// operador si es un UUID y NULL si no.
func (r *Repository) ExecuteApprovalTx(ctx context.Context, requestID, tenantID, userID, roleID, operatorID string) error {
	panic(pendiente.Implementar("platformadmin.Repository.ExecuteApprovalTx"))
}
