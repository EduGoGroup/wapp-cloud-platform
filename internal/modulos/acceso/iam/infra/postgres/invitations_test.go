//go:build pendiente

package iampostgres

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
)

// testInvitationID es una invitación cualquiera.
const testInvitationID = "5e4d3c2b-1a09-4f8e-8d7c-6b5a4f3e2d1c"

var _ func(*sql.DB) *InvitationRepo = NewInvitationRepo

// TestNewInvitationRepo_DoesNotTouchPool: construir no consulta nada.
func TestNewInvitationRepo_DoesNotTouchPool(t *testing.T) {
	if NewInvitationRepo(downPool(t)) == nil {
		t.Fatal("NewInvitationRepo devolvió nil")
	}
}

// TestInvitationRepo_InfraErrors: con la base caída cada método propaga la causa envuelta con su
// texto. Ni Create la disfraza de digest repetido, ni ListByTenant devuelve lista, ni Revoke la
// toma por «no existe» o por «ya estaba revocada» (nil).
func TestInvitationRepo_InfraErrors(t *testing.T) {
	cases := []struct {
		name   string
		prefix string
		call   func(r *InvitationRepo) error
	}{
		{"create_is_not_conflict", "iam: emitir invitación: ", func(r *InvitationRepo) error {
			_, err := r.Create(t.Context(), domain.Invitation{
				TenantID:  testTenantID,
				TokenHash: domain.HashInvitationToken("token-de-prueba"),
				ExpiresAt: time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC),
				CreatedBy: testUserID,
			})
			if errors.Is(err, domain.ErrConflict) {
				t.Errorf("Create con la base caída = %v; no es un conflicto", err)
			}
			return err
		}},
		{"list_returns_nil", "iam: listar invitaciones: ", func(r *InvitationRepo) error {
			invitations, err := r.ListByTenant(t.Context(), testTenantID)
			if invitations != nil {
				t.Errorf("ListByTenant con error devolvió %d invitaciones; quiere nil", len(invitations))
			}
			return err
		}},
		{"revoke_is_not_not_found", "iam: revocar invitación: ", func(r *InvitationRepo) error {
			err := r.Revoke(t.Context(), testInvitationID, testTenantID)
			if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrConflict) {
				t.Errorf("Revoke con la base caída = %v; no es ausencia ni conflicto", err)
			}
			return err
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wantInfraError(t, c.name, c.call(NewInvitationRepo(downPool(t))), c.prefix)
		})
	}
}
