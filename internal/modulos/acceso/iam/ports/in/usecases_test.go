package in

import (
	"context"
	"reflect"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// ctxKey es la clave con la que los tests marcan un contexto para reconocerlo al otro lado.
type ctxKey struct{}

// CallerResolverFunc.Caller llama a la función con el MISMO contexto y devuelve, sin tocarlos,
// el Caller y el ok que ella devuelva: con ok=true y con ok=false.
func TestCallerResolverFunc_Caller_DelegatesArgsAndResult(t *testing.T) {
	for _, c := range []struct {
		name   string
		caller Caller
		ok     bool
	}{
		{"authenticated_with_tenant", Caller{TenantID: "t-1", UserID: "u-1"}, true},
		{"authenticated_without_tenant", Caller{UserID: "u-2"}, true},
		{"not_authenticated", Caller{}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), ctxKey{}, c.name)
			calls := 0
			var resolver CallerResolver = CallerResolverFunc(func(got context.Context) (Caller, bool) {
				calls++
				if got.Value(ctxKey{}) != c.name {
					t.Errorf("la función recibió otro contexto: Value = %v; quiere %q", got.Value(ctxKey{}), c.name)
				}
				return c.caller, c.ok
			})
			gotCaller, gotOK := resolver.Caller(ctx)
			if calls != 1 {
				t.Errorf("la función se llamó %d veces; quiere 1", calls)
			}
			if gotCaller != c.caller || gotOK != c.ok {
				t.Errorf("Caller() = (%+v, %v); quiere (%+v, %v)", gotCaller, gotOK, c.caller, c.ok)
			}
		})
	}
}

// AuditInput es un ALIAS del DTO de platform/httpapi, no un tipo propio: se asigna en los dos
// sentidos sin conversión (si fuera un tipo definido, esto no compilaría) y reflect ve el mismo
// tipo.
func TestAuditInput_IsAliasOfHTTPAPIAuditInput(t *testing.T) {
	ours := AuditInput{TenantID: "t-1", Actor: "u-1", Action: "role.create", Result: "success"}
	// Entra un in.AuditInput como httpapi.AuditInput y sale de vuelta como in.AuditInput, sin
	// conversión en ningún sentido.
	roundTrip := func(v httpapi.AuditInput) AuditInput { return v }
	back := roundTrip(ours)
	if reflect.TypeOf(ours) != reflect.TypeOf(httpapi.AuditInput{}) {
		t.Errorf("reflect.TypeOf(in.AuditInput) = %v; quiere el mismo tipo que httpapi.AuditInput (%v)",
			reflect.TypeOf(ours), reflect.TypeOf(httpapi.AuditInput{}))
	}
	if back.Action != "role.create" {
		t.Errorf("el valor cambió al cruzar el alias: Action = %q; quiere %q", back.Action, "role.create")
	}
	// Un Auditor de este paquete satisface el AuditRecorder del middleware sin adaptador.
	var _ httpapi.AuditRecorder = Auditor(nil)
}

// Los DTOs no tienen conducta: se mencionan para el candado exportados_cubiertos (E-9). Las
// interfaces se mencionan como tipo de una variable nil.
func TestExportedSymbols_AreMentioned(t *testing.T) {
	_ = LoginInput{}
	_ = RefreshInput{}
	_ = LogoutInput{}
	_ = VerifyResult{}
	_ = ExchangeInput{}
	_ = ExchangeResult{}
	_ = CreateRoleInput{}
	_ = RoleAssignmentInput{}
	_ = RoleGrantInput{}
	_ = UserGrantInput{}
	_ = MembershipInput{}
	_ = IssueInvitationInput{}
	_ = IssuedInvitation{}

	var (
		_ TokenVerifier   = nil
		_ Authenticator   = nil
		_ Exchanger       = nil
		_ Auditor         = nil
		_ RoleAdmin       = nil
		_ MembershipAdmin = nil
		_ InvitationAdmin = nil
	)
	// Authenticator incluye TokenVerifier: quien autentica también verifica.
	var a Authenticator
	var _ TokenVerifier = a
}
