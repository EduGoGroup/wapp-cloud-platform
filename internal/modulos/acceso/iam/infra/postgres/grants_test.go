package iampostgres

import (
	"database/sql"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
)

var _ func(*sql.DB) *GrantRepo = NewGrantRepo

// TestNewGrantRepo_DoesNotTouchPool: construir no consulta nada.
func TestNewGrantRepo_DoesNotTouchPool(t *testing.T) {
	if NewGrantRepo(downPool(t)) == nil {
		t.Fatal("NewGrantRepo devolvió nil")
	}
}

// TestGrantRepo_InfraErrors: con la base caída cada método propaga la causa envuelta con su
// texto; GrantsOfUser no devuelve una lista (ni vacía) que el token pudiera tomar por «sin
// overrides».
func TestGrantRepo_InfraErrors(t *testing.T) {
	grant := domain.Grant{Pattern: "intake.read", Effect: domain.EffectDeny}
	cases := []struct {
		name   string
		prefix string
		call   func(r *GrantRepo) error
	}{
		{"grants_of_user_returns_nil", "iam: leer grants: ", func(r *GrantRepo) error {
			grants, err := r.GrantsOfUser(t.Context(), testUserID)
			if grants != nil {
				t.Errorf("GrantsOfUser con error devolvió %d grants; quiere nil", len(grants))
			}
			return err
		}},
		{"add_user_grant", "iam: añadir override de grant: ", func(r *GrantRepo) error {
			return r.AddUserGrant(t.Context(), testUserID, grant)
		}},
		{"remove_user_grant", "iam: quitar override de grant: ", func(r *GrantRepo) error {
			return r.RemoveUserGrant(t.Context(), testUserID, grant)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wantInfraError(t, c.name, c.call(NewGrantRepo(downPool(t))), c.prefix)
		})
	}
}
