package domain

import (
	"errors"
	"fmt"
	"testing"
)

// sentinelCase une un centinela con su texto observable, copiado byte a byte del viejo.
type sentinelCase struct {
	name string
	err  error
	text string
}

// errorsSentinels son los 19 centinelas de errors.go con su texto literal (diseño F2 §5).
func errorsSentinels() []sentinelCase {
	return []sentinelCase{
		{"ErrNotFound", ErrNotFound, "iam: recurso no encontrado"},
		{"ErrConflict", ErrConflict, "iam: conflicto de unicidad"},
		{"ErrInvalidInput", ErrInvalidInput, "iam: entrada inválida"},
		{"ErrNoTenant", ErrNoTenant, "iam: el contexto de identidad no trae tenant"},
		{"ErrGlobalRoleImmutable", ErrGlobalRoleImmutable, "iam: las plantillas de rol globales no se modifican desde un tenant"},
		{"ErrRoleScopeInvalid", ErrRoleScopeInvalid, "iam: un rol de empresa no puede asignarse con ámbito global"},
		{"ErrInvalidCredentials", ErrInvalidCredentials, "iam: credenciales inválidas"},
		{"ErrUserInactive", ErrUserInactive, "iam: usuario inactivo"},
		{"ErrRefreshInvalid", ErrRefreshInvalid, "iam: refresh token inválido"},
		{"ErrIdentityTokenInvalid", ErrIdentityTokenInvalid, "iam: identity token inválido"},
		{"ErrIdentityTokenExpiring", ErrIdentityTokenExpiring, "iam: al identity token le queda muy poca vida para canjearlo"},
		{"ErrUserNotMigrated", ErrUserNotMigrated, "iam: el sujeto del identity token no es miembro de ningún tenant de wApp"},
		{"ErrIdentityUnavailable", ErrIdentityUnavailable, "iam: identity no está disponible"},
		{"ErrMachineCredentialInvalid", ErrMachineCredentialInvalid, "iam: identity rechazó la credencial M2M de wApp"},
		{"ErrEmailTaken", ErrEmailTaken, "iam: el correo ya está registrado en identity"},
		{"ErrPasswordPolicy", ErrPasswordPolicy, "iam: la contraseña no cumple la política de identity"},
		{"ErrRateLimited", ErrRateLimited, "iam: identity aplicó su límite de peticiones"},
		{"ErrIdentityNotConfigured", ErrIdentityNotConfigured, "iam: el cliente M2M de identity no está configurado en este despliegue"},
		{"ErrSystemNotAllowed", ErrSystemNotAllowed, "iam: identity rechazó alguna aplicación del conjunto"},
	}
}

// El texto de cada centinela es observable y no cambia ni un byte.
func TestSentinels_LiteralText(t *testing.T) {
	cases := errorsSentinels()
	if len(cases) != 19 {
		t.Fatalf("la tabla tiene %d centinelas; errors.go promete 19", len(cases))
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.err == nil {
				t.Fatalf("%s es nil", c.name)
			}
			if got := c.err.Error(); got != c.text {
				t.Errorf("%s = %q; quiere el literal %q", c.name, got, c.text)
			}
		})
	}
}

// errors.Is es la forma de inspeccionarlos: un centinela envuelto con %w se sigue
// reconociendo, y ninguno se confunde con otro (cada uno es una identidad propia, aunque dos
// textos se parecieran).
func TestSentinels_InspectedWithErrorsIs(t *testing.T) {
	cases := errorsSentinels()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wrapped := fmt.Errorf("contexto del llamante: %w", c.err)
			if !errors.Is(wrapped, c.err) {
				t.Errorf("errors.Is no reconoce %s envuelto con %%w", c.name)
			}
			for _, other := range cases {
				if other.name != c.name && errors.Is(wrapped, other.err) {
					t.Errorf("%s envuelto se reconoce también como %s", c.name, other.name)
				}
			}
		})
	}
}
