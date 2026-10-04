package iampostgres

import (
	"database/sql"
	"errors"
	"fmt"
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

// optTime describe un *time.Time para un mensaje: <nil> o el instante en UTC.
func optTime(p *time.Time) string {
	if p == nil {
		return "<nil>"
	}
	return p.UTC().Format(time.RFC3339Nano)
}

// describeInvitation describe una invitación campo a campo, con los punteros desreferenciados.
func describeInvitation(inv domain.Invitation) string {
	return fmt.Sprintf("{id %q tenant %q digest %x role %s expires %s by %q redeemedBy %s redeemedAt %s revokedAt %s created %s}",
		inv.ID, inv.TenantID, inv.TokenHash, optString(inv.RoleID), inv.ExpiresAt.UTC().Format(time.RFC3339Nano),
		inv.CreatedBy, optString(inv.RedeemedBy), optTime(inv.RedeemedAt), optTime(inv.RevokedAt),
		inv.CreatedAt.UTC().Format(time.RFC3339Nano))
}

// TestScanInvitation: la fila de tenant_invitations (orden de invitationCols) llega entera a la
// entidad. Las cuatro NULLables (role_id, redeemed_by, redeemed_at, revoked_at) son nil si son
// NULL y apuntan a su valor si no: perder revoked_at dejaría una invitación revocada diciendo
// «pendiente». Un error de Scan se devuelve tal cual, con la invitación vacía.
func TestScanInvitation(t *testing.T) {
	created := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	expires, redeemed, revoked := created.Add(72*time.Hour), created.Add(time.Hour), created.Add(2*time.Hour)
	digest := domain.HashInvitationToken("token-de-prueba")
	roleID, redeemer := companyRoleID, "9f8e7d6c-5b4a-4392-8170-6f5e4d3c2b1a"
	base := domain.Invitation{
		ID: testInvitationID, TenantID: testTenantID, TokenHash: digest,
		ExpiresAt: expires, CreatedBy: testUserID, CreatedAt: created,
	}
	all := base
	all.RoleID, all.RedeemedBy, all.RedeemedAt, all.RevokedAt = &roleID, &redeemer, &redeemed, &revoked

	cases := []struct {
		name    string
		row     fakeRow
		want    domain.Invitation
		wantErr error
	}{
		{"pending_all_nullables_null", fakeRow{values: []any{
			testInvitationID, testTenantID, digest, nil, expires, testUserID, nil, nil, nil, created,
		}}, base, nil},
		{"all_nullables_set", fakeRow{values: []any{
			testInvitationID, testTenantID, digest, roleID, expires, testUserID, redeemer, redeemed, revoked, created,
		}}, all, nil},
		{"scan_error_returned_as_is", fakeRow{err: sql.ErrNoRows}, domain.Invitation{}, sql.ErrNoRows},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := scanInvitation(c.row)
			if !errors.Is(err, c.wantErr) || (c.wantErr == nil && err != nil) {
				t.Fatalf("scanInvitation error = %v; quiere %v", err, c.wantErr)
			}
			if describeInvitation(got) != describeInvitation(c.want) {
				t.Errorf("scanInvitation = %s;\nquiere          %s", describeInvitation(got), describeInvitation(c.want))
			}
		})
	}
}
