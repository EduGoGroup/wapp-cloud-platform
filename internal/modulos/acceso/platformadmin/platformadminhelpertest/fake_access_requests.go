package platformadminhelpertest

// Parte de fake.go, partido por tamaño (F2-03, a petición de Jhoan; solo se movieron
// declaraciones): AccessRequestStore del doble.

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/platformadmin"
)

// ── AccessRequestStore ───────────────────────────────────────────────────────────────────────

// ListAccessRequests implementa platformadmin.AccessRequestStore.
func (f *Fake) ListAccessRequests(_ context.Context, status string) ([]platformadmin.AccessRequestItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failures["ListAccessRequests"]; err != nil {
		return nil, err
	}
	if status == "" {
		status = "pending"
	}
	items := []platformadmin.AccessRequestItem{}
	for _, r := range f.requests { // f.requests va en orden de alta: created_at ascendente
		if r.Status != status {
			continue
		}
		items = append(items, platformadmin.AccessRequestItem{
			ID: f.requestID[r], UserID: r.UserID, Email: r.Email, Origin: r.Origin, Status: r.Status,
			CreatedAt: r.CreatedAt, Systems: []string{}, SystemsKnown: false,
		})
	}
	return items, nil
}

// CreateAccessRequest implementa platformadmin.AccessRequestStore.
func (f *Fake) CreateAccessRequest(_ context.Context, userID, email, origin string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failures["CreateAccessRequest"]; err != nil {
		return err
	}
	if userID == "" || email == "" || (origin != "bff" && origin != "edge") {
		return platformadmin.ErrInvalidInput
	}
	for _, r := range f.requests {
		if r.UserID == userID && r.Status == "pending" {
			return nil // ON CONFLICT (user_id) WHERE status = 'pending' DO NOTHING
		}
	}
	r := &RequestRow{UserID: userID, Email: email, Origin: origin, Status: "pending", CreatedAt: f.now()}
	f.requests = append(f.requests, r)
	f.requestID[r] = uuid.NewString()
	return nil
}

// RejectAccessRequest implementa platformadmin.AccessRequestStore.
func (f *Fake) RejectAccessRequest(_ context.Context, requestID, reason, operatorID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failures["RejectAccessRequest"]; err != nil {
		return err
	}
	if requestID == "" || strings.TrimSpace(reason) == "" {
		return platformadmin.ErrInvalidInput
	}
	r := f.findRequestLocked(requestID)
	switch {
	case r == nil:
		return platformadmin.ErrNotFound
	case r.Status != "pending":
		return platformadmin.ErrConflict
	}
	r.Status = "rejected"
	r.Reason = &reason
	f.decideLocked(r, operatorID)
	return nil
}

// LookupAccessRequestStatus implementa platformadmin.AccessRequestStore.
func (f *Fake) LookupAccessRequestStatus(_ context.Context, requestID string) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failures["LookupAccessRequestStatus"]; err != nil {
		return "", "", err
	}
	r := f.findRequestLocked(requestID)
	if r == nil {
		return "", "", platformadmin.ErrNotFound
	}
	return r.UserID, r.Status, nil
}

// ResolveRoleID implementa platformadmin.AccessRequestStore.
func (f *Fake) ResolveRoleID(_ context.Context, role string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failures["ResolveRoleID"]; err != nil {
		return "", err
	}
	for _, r := range f.roles {
		if r.Name == role || r.ID == role {
			return r.ID, nil
		}
	}
	return "", platformadmin.ErrInvalidInput
}

// CheckRetryApproved implementa platformadmin.AccessRequestStore.
func (f *Fake) CheckRetryApproved(_ context.Context, userID, tenantID, roleID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failures["CheckRetryApproved"]; err != nil {
		return err
	}
	k := accessKey{userID, tenantID}
	if !f.members[k] {
		return platformadmin.ErrConflict
	}
	if !slices.Contains(f.userRoles[k], roleID) {
		return platformadmin.ErrRetryRoleMismatch
	}
	return nil
}

// ExecuteApprovalTx implementa platformadmin.AccessRequestStore: comprueba TODO antes de
// escribir nada, que es como el Fake da el «o todo o nada» de la transacción de Postgres.
func (f *Fake) ExecuteApprovalTx(_ context.Context, requestID, tenantID, userID, roleID, operatorID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failures["ExecuteApprovalTx"]; err != nil {
		return err
	}
	// Lo que en Postgres es una clave foránea: la empresa y el rol existen.
	if _, ok := f.tenants[tenantID]; !ok {
		return fmt.Errorf("platformadmin fake: la empresa %s no existe (tenant_members.tenant_id)", tenantID)
	}
	if !slices.ContainsFunc(f.roles, func(r Role) bool { return r.ID == roleID }) {
		return fmt.Errorf("platformadmin fake: el rol %s no existe (iam_user_roles.role_id)", roleID)
	}
	// Una sola empresa salvo multi_empresa en la de destino (GrantTenantAccess).
	for k, member := range f.members {
		if member && k.user == userID && k.tenant != tenantID && !f.multi[tenantID] {
			return platformadmin.ErrConflict
		}
	}
	r := f.findRequestLocked(requestID)
	if r == nil || r.Status != "pending" {
		return platformadmin.ErrConflict
	}
	k := accessKey{userID, tenantID}
	f.members[k] = true
	if !slices.Contains(f.userRoles[k], roleID) {
		f.userRoles[k] = append(f.userRoles[k], roleID)
		slices.Sort(f.userRoles[k])
	}
	r.Status = "approved"
	f.decideLocked(r, operatorID)
	return nil
}

// findRequestLocked busca la solicitud por id. Con f.mu tomado.
func (f *Fake) findRequestLocked(requestID string) *RequestRow {
	for _, r := range f.requests {
		if f.requestID[r] == requestID {
			return r
		}
	}
	return nil
}

// decideLocked anota quién y cuándo resolvió: decided_by solo si el operador es un UUID (si no,
// NULL), como el adaptador Postgres. Con f.mu tomado.
func (f *Fake) decideLocked(r *RequestRow, operatorID string) {
	r.DecidedBy = nil
	if op, err := uuid.Parse(operatorID); err == nil {
		s := op.String()
		r.DecidedBy = &s
	}
	at := f.now()
	r.DecidedAt = &at
}
