// Nuevo: las suites de contrato de los siete puertos persistentes de iam/ports/out (diseño F2
// §2, tareas T2.6). No tienen fichero viejo: los casos salen de los comentarios de los puertos y
// de los tests viejos de internal/iam/infra/{memory,postgres} @ 9a77307, leídos, no portados.

// Package outhelpertest es la suite de contrato de los puertos persistentes de out: una suite
// por puerto y Contrato, que las corre todas. Ningún código de producción lo importa: arrastra
// "testing" (mismo criterio que internal/nucleo/contact/contacthelpertest).
//
// La corren todas las implementaciones de cada puerto: los dobles de iam/infra/memory en
// unitario, y los adaptadores de iam/infra/postgres en los procesos de F9, con el arnés de
// testcontainers (P4). Así memoria y Postgres se comportan igual: lo que una deja pasar, la otra
// también.
//
// Cada suite tiene la forma de D-F1-1, ContratoX(t, nuevo func(t) MontajeX): nuevo se llama una
// vez por caso y devuelve un Montaje LIMPIO, con dos tenants que existen (UUID; con Postgres,
// filas de public.tenants, que las FK exigen) y, donde el puerto no deja ver todo lo que su
// operación toca, las tablas con que la suite siembra y observa ese estado (hallazgo 35 de F1:
// la observación vigila TODAS las columnas que la operación puede tocar). Ningún caso usa BD,
// reloj real ni t.Skip: los instantes que la suite escribe son fijos o relativos al reloj de la
// implementación.
//
// Un fichero por puerto, todos con el sufijo _contrato del método (D-F1-12):
//   - contrato.go: la entrada. Contrato, Montajes y lo común a las suites (tenants, tabla de
//     casos, ayudas).
//   - memberships_contrato.go: ContratoMembershipRepo (MembershipRepo).
//   - roles_contrato.go: ContratoRoleRepo (RoleRepo).
//   - grants_contrato.go: ContratoGrantRepo (GrantRepo).
//   - audit_contrato.go: ContratoAuditRepo (AuditRepo).
//   - invitations_contrato.go: ContratoInvitationRepo (InvitationRepo).
//   - active_tenant_contrato.go: ContratoActiveTenantRepo (ActiveTenantRepo).
//   - redeem_contrato.go: ContratoInvitationRedeemRepo (InvitationRedeemRepo).
//
// Los tres clientes de identity no tienen suite aquí (diseño F2 §2): se prueban en
// infra/identity contra un httptest.Server.
package outhelpertest

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
)

// Montajes reúne una fábrica de Montaje por puerto: lo que Contrato necesita para correr las
// siete suites contra un juego completo de implementaciones (el agregado memory.Store, o los
// siete adaptadores de Postgres sobre la misma base). Todas son obligatorias.
type Montajes struct {
	MembershipRepo       func(t *testing.T) MontajeMembershipRepo
	RoleRepo             func(t *testing.T) MontajeRoleRepo
	GrantRepo            func(t *testing.T) MontajeGrantRepo
	AuditRepo            func(t *testing.T) MontajeAuditRepo
	InvitationRepo       func(t *testing.T) MontajeInvitationRepo
	ActiveTenantRepo     func(t *testing.T) MontajeActiveTenantRepo
	InvitationRedeemRepo func(t *testing.T) MontajeInvitationRedeemRepo
}

// Contrato corre las siete suites de los puertos persistentes, cada una en su subtest con el
// nombre del puerto, con las fábricas de montajes. Falla el test t, sin correr nada, si falta
// alguna fábrica: un puerto sin implementación es un hueco, no un caso que no aplica.
func Contrato(t *testing.T, montajes Montajes) {
	t.Helper()
	switch {
	case montajes.MembershipRepo == nil, montajes.RoleRepo == nil, montajes.GrantRepo == nil,
		montajes.AuditRepo == nil, montajes.InvitationRepo == nil, montajes.ActiveTenantRepo == nil,
		montajes.InvitationRedeemRepo == nil:
		t.Fatal("outhelpertest.Contrato: falta alguna fábrica de Montajes; hacen falta las siete")
	}
	t.Run("MembershipRepo", func(t *testing.T) { ContratoMembershipRepo(t, montajes.MembershipRepo) })
	t.Run("RoleRepo", func(t *testing.T) { ContratoRoleRepo(t, montajes.RoleRepo) })
	t.Run("GrantRepo", func(t *testing.T) { ContratoGrantRepo(t, montajes.GrantRepo) })
	t.Run("AuditRepo", func(t *testing.T) { ContratoAuditRepo(t, montajes.AuditRepo) })
	t.Run("InvitationRepo", func(t *testing.T) { ContratoInvitationRepo(t, montajes.InvitationRepo) })
	t.Run("ActiveTenantRepo", func(t *testing.T) { ContratoActiveTenantRepo(t, montajes.ActiveTenantRepo) })
	t.Run("InvitationRedeemRepo", func(t *testing.T) { ContratoInvitationRedeemRepo(t, montajes.InvitationRedeemRepo) })
}

// FeatureSwitch es la mitad del resolver de entitlements que las suites de alta necesitan
// mover (MembershipRepo.Add e InvitationRedeemRepo.Redeem): conceder multi_empresa y tirar el
// resolver. Con memoria, un entitlementshelpertest.Fake; con Postgres, filas de
// public.tenant_features o un resolver que falla. El Montaje lo entrega con ningún tenant con
// multi_empresa y el resolver funcionando. Cada método falla el test t si no puede.
type FeatureSwitch interface {
	// GrantMultiCompany concede multi_empresa (entitlements.FeatureMultiCompany) al tenant.
	GrantMultiCompany(t *testing.T, tenantID string)
	// BreakResolver deja el resolver caído: desde ahí, toda pregunta por un derecho falla.
	BreakResolver(t *testing.T)
}

// testCase es una promesa de un puerto: su nombre (el del t.Run, en inglés y diciendo la regla)
// y la función que la afirma con un Montaje limpio.
type testCase[M any] struct {
	name string
	run  func(t *testing.T, m M)
}

// runCases corre cada caso en su subtest con un Montaje recién hecho por nuevo, después de
// validarlo con validate. suite nombra la suite en el mensaje si nuevo es nil.
func runCases[M any](t *testing.T, suite string, nuevo func(t *testing.T) M, validate func(t *testing.T, m M), cases []testCase[M]) {
	t.Helper()
	if nuevo == nil {
		t.Fatalf("outhelpertest.%s: nuevo es nil; hace falta una función que devuelva un Montaje", suite)
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := nuevo(t)
			validate(t, m)
			c.run(t, m)
		})
	}
}

// validateTenants exige dos tenants distintos y con forma de UUID.
func validateTenants(t *testing.T, tenantA, tenantB string) {
	t.Helper()
	if tenantA == tenantB {
		t.Fatalf("Montaje: TenantA y TenantB deben ser distintos y son %q", tenantA)
	}
	for _, tenant := range []string{tenantA, tenantB} {
		if _, err := uuid.Parse(tenant); err != nil {
			t.Fatalf("Montaje: el tenant %q no es un UUID bien formado: %v", tenant, err)
		}
	}
}

// newUser devuelve el UUID de una persona nueva: user_id no tiene FK (la persona vive en
// identity), pero las columnas son UUID.
func newUser() string { return uuid.NewString() }

// newDigest devuelve un digest de 32 bytes que no repite ningún otro caso: token_hash es único
// en toda la tabla y, con Postgres, los casos pueden compartir base.
func newDigest() []byte { return domain.HashInvitationToken(uuid.NewString()) }

// ptr devuelve un puntero a una copia de v.
func ptr[V any](v V) *V { return &v }

// bg es el contexto de las llamadas de la suite: ninguna promesa depende de él.
func bg() context.Context { return context.Background() }
