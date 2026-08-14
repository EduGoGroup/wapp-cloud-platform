package platformadmin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	iamdomain "github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/out"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
	"github.com/google/uuid"
)

// AccessRequestItem representa una solicitud en la bandeja de acceso.
type AccessRequestItem struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	Email     string    `json:"email"`
	Origin    string    `json:"origin"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

// ListAccessRequestsResponse es el cuerpo JSON para GET /admin/access-requests.
type ListAccessRequestsResponse struct {
	Items []AccessRequestItem `json:"items"`
}

// ApproveAccessRequestRequest es el cuerpo JSON para POST /admin/access-requests/{id}/approve.
type ApproveAccessRequestRequest struct {
	TenantID string   `json:"tenant_id"`
	Role     string   `json:"role"`
	Systems  []string `json:"systems"`
}

// RejectAccessRequestRequest es el cuerpo JSON para POST /admin/access-requests/{id}/reject.
type RejectAccessRequestRequest struct {
	Reason string `json:"reason"`
}

// ListAccessRequests consulta las solicitudes de acceso filtradas por estado.
func (r *Repository) ListAccessRequests(ctx context.Context, status string) ([]AccessRequestItem, error) {
	if status == "" {
		status = "pending"
	}

	query := `
		SELECT id::text, user_id::text, email, origin, status, created_at
		FROM public.access_requests
		WHERE status = $1
		ORDER BY created_at ASC
	`
	rows, err := r.db.QueryContext(ctx, query, status)
	if err != nil {
		return nil, fmt.Errorf("platformadmin: list access requests: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			_ = cerr
		}
	}()

	var items []AccessRequestItem
	for rows.Next() {
		var it AccessRequestItem
		if err := rows.Scan(&it.ID, &it.UserID, &it.Email, &it.Origin, &it.Status, &it.CreatedAt); err != nil {
			return nil, fmt.Errorf("platformadmin: scan access request: %w", err)
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("platformadmin: iterate access requests: %w", err)
	}
	if items == nil {
		items = []AccessRequestItem{}
	}
	return items, nil
}

// CreateAccessRequest siembra una solicitud de acceso en estado pending.
func (r *Repository) CreateAccessRequest(ctx context.Context, userID, email, origin string) error {
	if userID == "" || email == "" || (origin != "bff" && origin != "edge") {
		return ErrInvalidInput
	}

	query := `
		INSERT INTO public.access_requests (user_id, email, origin, status)
		VALUES ($1, $2, $3, 'pending')
		ON CONFLICT (user_id) WHERE status = 'pending' DO NOTHING
	`
	_, err := r.db.ExecContext(ctx, query, userID, email, origin)
	if err != nil {
		return fmt.Errorf("platformadmin: create access request: %w", err)
	}
	return nil
}

// checkPendingUserMembership verifica la solicitud y la pertenencia a otro tenant.
func (r *Repository) checkPendingUserMembership(ctx context.Context, requestID, tenantID string) (string, error) {
	var (
		userID string
		status string
	)
	err := r.db.QueryRowContext(ctx, `
		SELECT user_id::text, status
		FROM public.access_requests
		WHERE id = $1
	`, requestID).Scan(&userID, &status)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", ErrNotFound
	case err != nil:
		return "", fmt.Errorf("platformadmin: read access request: %w", err)
	}

	if status != "pending" {
		return "", ErrConflict
	}

	var existingTenantID string
	err = r.db.QueryRowContext(ctx, `
		SELECT tenant_id::text
		FROM public.tenant_members
		WHERE user_id = $1
	`, userID).Scan(&existingTenantID)
	if err == nil && existingTenantID != "" && existingTenantID != tenantID {
		return "", ErrConflict
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("platformadmin: check existing membership: %w", err)
	}

	return userID, nil
}

// resolveRoleID encuentra el id canónico del rol por nombre o id.
func (r *Repository) resolveRoleID(ctx context.Context, role string) (string, error) {
	var roleID string
	err := r.db.QueryRowContext(ctx, `
		SELECT id::text
		FROM public.iam_roles
		WHERE name = $1 OR id::text = $1
	`, role).Scan(&roleID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrInvalidInput
	} else if err != nil {
		return "", fmt.Errorf("platformadmin: resolve role: %w", err)
	}
	return roleID, nil
}

// executeApprovalTx ejecuta la transacción local de aprobación.
func (r *Repository) executeApprovalTx(ctx context.Context, requestID, tenantID, userID, roleID, operatorID string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("platformadmin: begin tx: %w", err)
	}
	defer func() {
		if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
			_ = rerr
		}
	}()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO public.tenant_members (user_id, tenant_id)
		VALUES ($1, $2)
		ON CONFLICT DO NOTHING
	`, userID, tenantID); err != nil {
		return fmt.Errorf("platformadmin: insert tenant_member: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO public.iam_user_roles (user_id, role_id, tenant_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id, role_id, tenant_id) DO NOTHING
	`, userID, roleID, tenantID); err != nil {
		return fmt.Errorf("platformadmin: insert iam_user_role: %w", err)
	}

	var opArg any
	if opUUID, parseErr := uuid.Parse(operatorID); parseErr == nil {
		opArg = opUUID
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE public.access_requests
		SET status = 'approved', decided_by = $1, decided_at = now()
		WHERE id = $2 AND status = 'pending'
	`, opArg, requestID)
	if err != nil {
		return fmt.Errorf("platformadmin: update access request status: %w", err)
	}
	rowsAff, err := res.RowsAffected()
	if err != nil || rowsAff == 0 {
		return ErrConflict
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("platformadmin: commit tx: %w", err)
	}
	return nil
}

// ApproveAccessRequest aprueba una solicitud asignando empresa, rol y actualizando sistemas en identity.
func (r *Repository) ApproveAccessRequest(ctx context.Context, requestID, tenantID, role, operatorID string, systems []string, m2m out.IdentityM2MClient) error {
	if requestID == "" || tenantID == "" || role == "" {
		return ErrInvalidInput
	}

	userID, err := r.checkPendingUserMembership(ctx, requestID, tenantID)
	if err != nil {
		return err
	}

	roleID, err := r.resolveRoleID(ctx, role)
	if err != nil {
		return err
	}

	if err := r.executeApprovalTx(ctx, requestID, tenantID, userID, roleID, operatorID); err != nil {
		return err
	}

	if m2m != nil && len(systems) > 0 {
		if _, err := m2m.ReplaceUserSystems(ctx, userID, systems); err != nil {
			return fmt.Errorf("platformadmin: sync user systems: %w", err)
		}
	}

	return nil
}

// RejectAccessRequest rechaza una solicitud guardando el motivo y operador.
func (r *Repository) RejectAccessRequest(ctx context.Context, requestID, reason, operatorID string) error {
	if requestID == "" {
		return ErrInvalidInput
	}

	var opArg any
	if opUUID, parseErr := uuid.Parse(operatorID); parseErr == nil {
		opArg = opUUID
	}

	res, err := r.db.ExecContext(ctx, `
		UPDATE public.access_requests
		SET status = 'rejected', reason = $1, decided_by = $2, decided_at = now()
		WHERE id = $3 AND status = 'pending'
	`, reason, opArg, requestID)
	if err != nil {
		return fmt.Errorf("platformadmin: reject access request: %w", err)
	}
	rowsAff, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("platformadmin: rows affected: %w", err)
	}
	if rowsAff == 0 {
		var exists bool
		if qErr := r.db.QueryRowContext(ctx, `SELECT true FROM public.access_requests WHERE id = $1`, requestID).Scan(&exists); qErr != nil && !exists {
			return ErrNotFound
		}
		return ErrConflict
	}
	return nil
}

// ListAccessRequestsHandler devuelve el handler para GET /admin/access-requests.
func ListAccessRequestsHandler(repo *Repository, platformTenantID string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !httpapi.EnforcePlatformCaller(w, r, platformTenantID) {
			return
		}

		status := r.URL.Query().Get("status")
		if status == "" {
			status = "pending"
		}

		items, err := repo.ListAccessRequests(r.Context(), status)
		if err != nil {
			http.Error(w, "error al listar solicitudes de acceso", http.StatusInternalServerError)
			return
		}

		writeJSON(w, http.StatusOK, ListAccessRequestsResponse{
			Items: items,
		})
	})
}

// ApproveAccessRequestHandler devuelve el handler para POST /admin/access-requests/{id}/approve.
func ApproveAccessRequestHandler(repo *Repository, m2m out.IdentityM2MClient, platformTenantID string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !httpapi.EnforcePlatformCaller(w, r, platformTenantID) {
			return
		}

		requestID := r.PathValue("id")
		if requestID == "" {
			http.Error(w, "id de solicitud requerido", http.StatusBadRequest)
			return
		}

		var req ApproveAccessRequestRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "cuerpo JSON inválido", http.StatusBadRequest)
			return
		}
		if req.TenantID == "" || req.Role == "" {
			http.Error(w, "tenant_id y role son requeridos", http.StatusBadRequest)
			return
		}

		httpapi.SetAuditTargetTenant(r.Context(), req.TenantID)

		var operatorID string
		if id, ok := httpapi.IdentityFromContext(r.Context()); ok {
			operatorID = id.Subject
		}

		err := repo.ApproveAccessRequest(r.Context(), requestID, req.TenantID, req.Role, operatorID, req.Systems, m2m)
		switch {
		case errors.Is(err, ErrNotFound):
			http.Error(w, "solicitud no encontrada", http.StatusNotFound)
			return
		case errors.Is(err, ErrConflict):
			http.Error(w, "la solicitud ya fue resuelta o la persona ya pertenece a otra empresa", http.StatusConflict)
			return
		case errors.Is(err, ErrInvalidInput):
			http.Error(w, "datos de solicitud o rol inválidos", http.StatusBadRequest)
			return
		case errors.Is(err, iamdomain.ErrIdentityUnavailable):
			http.Error(w, "identity-api no disponible al actualizar aplicaciones", http.StatusBadGateway)
			return
		case err != nil:
			http.Error(w, "error al aprobar solicitud", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	})
}

// RejectAccessRequestHandler devuelve el handler para POST /admin/access-requests/{id}/reject.
func RejectAccessRequestHandler(repo *Repository, platformTenantID string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !httpapi.EnforcePlatformCaller(w, r, platformTenantID) {
			return
		}

		requestID := r.PathValue("id")
		if requestID == "" {
			http.Error(w, "id de solicitud requerido", http.StatusBadRequest)
			return
		}

		var req RejectAccessRequestRequest
		if r.Body != nil && r.ContentLength > 0 {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "cuerpo JSON inválido", http.StatusBadRequest)
				return
			}
		}

		var operatorID string
		if id, ok := httpapi.IdentityFromContext(r.Context()); ok {
			operatorID = id.Subject
		}

		err := repo.RejectAccessRequest(r.Context(), requestID, req.Reason, operatorID)
		switch {
		case errors.Is(err, ErrNotFound):
			http.Error(w, "solicitud no encontrada", http.StatusNotFound)
			return
		case errors.Is(err, ErrConflict):
			http.Error(w, "la solicitud ya fue resuelta", http.StatusConflict)
			return
		case errors.Is(err, ErrInvalidInput):
			http.Error(w, "entrada inválida", http.StatusBadRequest)
			return
		case err != nil:
			http.Error(w, "error al rechazar solicitud", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	})
}
