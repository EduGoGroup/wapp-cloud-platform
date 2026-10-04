package domain

import (
	"reflect"
	"strings"
	"testing"
)

// Los literales de Effect viajan a la columna `effect` de los grants: no cambian.
func TestEffect_LiteralValues(t *testing.T) {
	for _, c := range []struct {
		name string
		got  Effect
		want string
	}{
		{"EffectAllow", EffectAllow, "allow"},
		{"EffectDeny", EffectDeny, "deny"},
	} {
		if string(c.got) != c.want {
			t.Errorf("%s = %q; quiere el literal %q", c.name, c.got, c.want)
		}
	}
}

// El discriminador del rol transversal es su id FIJO, el que siembra la migración 0059: si
// cambiara, la asignación global del platform_admin fallaría (fail-closed), así que un cambio
// aquí tiene que verse.
func TestRolTransversalID_IsTheSeededID(t *testing.T) {
	if RolTransversalID != "10000000-0000-0000-0000-000000000004" {
		t.Errorf("RolTransversalID = %q; quiere el id sembrado por 0059 %q",
			RolTransversalID, "10000000-0000-0000-0000-000000000004")
	}
}

// fieldsOf describe un struct como «Campo tipo» por campo, en orden de declaración.
func fieldsOf(v any) []string {
	typ := reflect.TypeOf(v)
	out := make([]string, 0, typ.NumField())
	for i := range typ.NumField() {
		f := typ.Field(i)
		out = append(out, f.Name+" "+f.Type.String())
	}
	return out
}

// La forma de cada entidad es contrato: los adaptadores la rellenan columna a columna y los
// comentarios prometen ausencias deliberadas (Membership son las tres columnas y ni una más;
// UserTenant no lleva Active; ninguna entidad lleva PII). Un campo de más o de menos se ve aquí.
func TestEntities_ExactShape(t *testing.T) {
	cases := []struct {
		name string
		got  []string
		want []string
	}{
		{"Role", fieldsOf(Role{}), []string{
			"ID string", "TenantID *string", "Name string", "ParentRoleID *string", "CreatedAt time.Time",
		}},
		{"Membership", fieldsOf(Membership{}), []string{
			"UserID string", "TenantID string", "CreatedAt time.Time",
		}},
		{"UserTenant", fieldsOf(UserTenant{}), []string{"ID string", "DisplayName string"}},
		{"Grant", fieldsOf(Grant{}), []string{"Pattern string", "Effect domain.Effect"}},
		{"AuditEvent", fieldsOf(AuditEvent{}), []string{
			"ID int64", "TenantID *string", "Actor string", "Action string", "Resource string",
			"Result string", "Meta map[string]interface {}", "At time.Time",
		}},
		{"IdentityContext", fieldsOf(IdentityContext{}), []string{
			"TenantID string", "UserID string", "Roles []string",
		}},
		{"IdentitySession", fieldsOf(IdentitySession{}), []string{
			"SessionID string", "IdentityToken string", "RefreshToken string", "ExpiresAt time.Time",
		}},
		{"AuthResult", fieldsOf(AuthResult{}), []string{
			"AccessToken string", "RefreshToken string", "TokenType string", "ExpiresAt time.Time",
			"Context domain.IdentityContext",
		}},
		{"IdentityUser", fieldsOf(IdentityUser{}), []string{"ID string", "Email string", "Created bool"}},
		{"IdentitySystemsDiff", fieldsOf(IdentitySystemsDiff{}), []string{
			"Systems []string", "Granted []string", "Revoked []string",
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if strings.Join(c.got, "; ") != strings.Join(c.want, "; ") {
				t.Errorf("%s tiene los campos\n  %v\nquiere exactamente\n  %v", c.name, c.got, c.want)
			}
		})
	}
}
