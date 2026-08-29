package usecase_test

// membership_integration_test.go — el criterio de campo de T1.0-2 (Plan 047 ·
// Ola 1.0): «un test de integración que da de alta por la vía nueva y la
// membresía la ve el canje».
//
// 🔴 POR QUÉ CONTRA POSTGRES Y NO CONTRA EL DOBLE. El doble en memoria copia la
// semántica de la tabla, pero la vía nueva y el canje se comunican por una tabla
// REAL: si el INSERT escribiera un tenant que TenantsOfUser no lee igual —un
// casteo, un tipo, un esquema distinto—, dos dobles compartidos no lo enseñarían
// nunca. Lo que se prueba aquí es justo la costura.
//
// ⚠️ Se SALTA sin WAPP_TEST_DB_DSN. `make test-integration` lo exporta contra un
// postgres:16 efímero; una corrida pelada de `go test ./...` no, y un --- SKIP
// no es un --- PASS.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	sharedjwt "github.com/EduGoGroup/wapp-shared/auth/jwt"
	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	iampostgres "github.com/EduGoGroup/wapp-cloud-platform/internal/iam/infra/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/usecase"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
)

// dsnEnv habilita los tests con BD real (mismo gate que el resto del repo).
const dsnEnv = "WAPP_TEST_DB_DSN"

// altaEnv agrupa el pool y un tenant recién sembrado.
type altaEnv struct {
	db       *sql.DB
	tenantID string
}

// newAltaEnv abre la BD (o salta), aplica migraciones y siembra un tenant único.
func newAltaEnv(t *testing.T) altaEnv {
	t.Helper()
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		if os.Getenv("WAPP_TEST_REQUIRE_DB") != "" {
			t.Fatalf("%s no definido pero WAPP_TEST_REQUIRE_DB exige BD: la integración DEBE correr", dsnEnv)
		}
		t.Skipf("%s no definido: se omiten los tests de integración con BD", dsnEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := postgres.Open(ctx, postgres.Config{DSN: dsn})
	if err != nil {
		if os.Getenv("WAPP_TEST_REQUIRE_DB") != "" {
			t.Fatalf("BD no disponible en %s (%v) pero WAPP_TEST_REQUIRE_DB exige BD", dsnEnv, err)
		}
		t.Skipf("BD no disponible en %s (%v): se omiten los tests de integración", dsnEnv, err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Logf("cerrando BD de test: %v", cerr)
		}
	})
	if _, err := migrations.Migrate(ctx, db); err != nil {
		t.Fatalf("migrando BD de test: %v", err)
	}
	slug := fmt.Sprintf("alta-it-%d", time.Now().UnixNano())
	tn, err := postgres.NewTenantRepository(db).Create(ctx, slug, "Alta IT")
	if err != nil {
		t.Fatalf("sembrar tenant: %v", err)
	}
	return altaEnv{db: db, tenantID: tn.ID}
}

// seedTenant siembra otro tenant en la misma base y devuelve su id.
func (e altaEnv) seedTenant(t *testing.T, prefijo string) string {
	t.Helper()
	slug := fmt.Sprintf("%s-%d", prefijo, time.Now().UnixNano())
	tn, err := postgres.NewTenantRepository(e.db).Create(context.Background(), slug, "Alta IT otra")
	if err != nil {
		t.Fatalf("sembrar tenant %s: %v", prefijo, err)
	}
	return tn.ID
}

// TestIntegration_AltaDeMembresiaLaVeElCanje: se da de alta por la vía NUEVA
// (in.MembershipAdmin sobre el repositorio Postgres) y quien la lee es el canje
// REAL —ExchangeService cableado contra los mismos repositorios—, no una
// consulta escrita a mano en el test. Si el alta escribiera algo que el canje no
// resuelve, el Context Token saldría sin empresa y esto se vería aquí.
func TestIntegration_AltaDeMembresiaLaVeElCanje(t *testing.T) {
	t.Parallel()
	env := newAltaEnv(t)
	ctx := context.Background()
	userID := uuid.NewString()

	members := iampostgres.NewMembershipRepo(env.db, nil)
	// El doble de identity acredita sin ruido: lo que este test mide es la
	// membresía contra Postgres, no la acreditación (esa vive en
	// membership_acreditacion_test.go).
	altas, err := usecase.NewMembershipService(testResolver, members, &identidadDeMentira{}, quietLogger())
	if err != nil {
		t.Fatalf("NewMembershipService: %v", err)
	}

	issuerMgr, verifier := newIdentityPair(t)
	contexts := sharedjwt.NewJWTManager(testSigningKey, testIssuer)
	canje, err := usecase.NewExchangeService(
		verifier,
		members,
		iampostgres.NewRoleRepo(env.db),
		iampostgres.NewGrantRepo(env.db),
		iampostgres.NewAuditRepo(env.db),
		iampostgres.NewActiveTenantRepo(env.db),
		contexts,
		usecase.Config{},
	)
	if err != nil {
		t.Fatalf("NewExchangeService: %v", err)
	}
	emisor := exchangeFixture{issuer: issuerMgr, contexts: contexts}

	// ANTES del alta el canje contesta sin empresa (D-056.12), no con un error.
	// Sin esta mitad, el test podría pasar aunque el alta no escribiera nada y el
	// tenant viniera de cualquier otro sitio.
	tokenPrevio, _ := emisor.identityToken(t, userID, usecase.SystemWappBFF, 15*time.Minute)
	previo, err := canje.Exchange(ctx, in.ExchangeInput{IdentityToken: tokenPrevio})
	if err != nil {
		t.Fatalf("Exchange antes del alta: %v", err)
	}
	if previo.Context.TenantID != "" {
		t.Fatalf("antes del alta el canje no debía traer empresa, trajo %q", previo.Context.TenantID)
	}

	// El alta, por la vía nueva y con el tenant saliendo del CONTEXTO.
	if err := altas.AddMember(withCaller(ctx, in.Caller{TenantID: env.tenantID, UserID: "operador"}),
		in.MembershipInput{UserID: userID}); err != nil {
		t.Fatalf("AddMember: %v", err)
	}

	// Y ahora el canje la ve.
	token, _ := emisor.identityToken(t, userID, usecase.SystemWappBFF, 15*time.Minute)
	res, err := canje.Exchange(ctx, in.ExchangeInput{IdentityToken: token})
	if err != nil {
		t.Fatalf("Exchange tras el alta: %v", err)
	}
	if res.Context.TenantID != env.tenantID {
		t.Fatalf("el canje resolvió tenant %q, quiero %q: la membresía dada de alta por la vía nueva no llegó",
			res.Context.TenantID, env.tenantID)
	}
	// El tenant que viaja FIRMADO es el que importa, no el que devuelve el DTO.
	claims, err := contexts.ValidateToken(res.ContextToken)
	if err != nil {
		t.Fatalf("validando el Context Token emitido: %v", err)
	}
	if claims.TenantID != env.tenantID {
		t.Fatalf("tenant en el token firmado = %q, quiero %q", claims.TenantID, env.tenantID)
	}
}

// TestIntegration_LaSegundaEmpresaSeRechazaContraLaTabla ejercita la guarda
// compartida (iampostgres.CountOtherMemberships) contra Postgres, y comprueba lo
// que de verdad protege: que tras el rechazo el canje SIGUE funcionando.
//
// 🔧 Si la guarda fallara, la fila entraría y esa persona tendría dos empresas
// sin que nadie lo hubiera decidido. Hasta el 2026-08-29 el daño se describía
// como «el canje devolvería ErrMultipleTenants y dejaría de poder entrar»: eso ya
// no ocurre (Plan 047 · Ola 5 · T5.1). Lo que la mitad de abajo comprueba —que
// tras el rechazo el canje SIGUE funcionando— vale igual.
func TestIntegration_LaSegundaEmpresaSeRechazaContraLaTabla(t *testing.T) {
	t.Parallel()
	env := newAltaEnv(t)
	ctx := context.Background()
	otroTenant := env.seedTenant(t, "alta-it-otra")
	userID := uuid.NewString()

	members := iampostgres.NewMembershipRepo(env.db, nil)
	// El doble de identity acredita sin ruido: lo que este test mide es la
	// membresía contra Postgres, no la acreditación (esa vive en
	// membership_acreditacion_test.go).
	altas, err := usecase.NewMembershipService(testResolver, members, &identidadDeMentira{}, quietLogger())
	if err != nil {
		t.Fatalf("NewMembershipService: %v", err)
	}

	if err := altas.AddMember(withCaller(ctx, in.Caller{TenantID: env.tenantID}), in.MembershipInput{UserID: userID}); err != nil {
		t.Fatalf("AddMember (primera empresa): %v", err)
	}
	// Repetirla es idempotente contra la tabla real (ON CONFLICT DO NOTHING).
	if err := altas.AddMember(withCaller(ctx, in.Caller{TenantID: env.tenantID}), in.MembershipInput{UserID: userID}); err != nil {
		t.Fatalf("AddMember (repetida): %v", err)
	}

	err = altas.AddMember(withCaller(ctx, in.Caller{TenantID: otroTenant}), in.MembershipInput{UserID: userID})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("la segunda empresa: err = %v, quiero ErrConflict", err)
	}

	tenants, err := members.TenantsOfUser(ctx, userID)
	if err != nil {
		t.Fatalf("TenantsOfUser: %v", err)
	}
	if len(tenants) != 1 || tenants[0] != env.tenantID {
		t.Fatalf("membresías = %v, quiero solo [%s]: el canje necesita exactamente una", tenants, env.tenantID)
	}

	// Y la baja deja la tabla como estaba.
	if err := altas.RemoveMember(withCaller(ctx, in.Caller{TenantID: env.tenantID}), in.MembershipInput{UserID: userID}); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	tenants, err = members.TenantsOfUser(ctx, userID)
	if err != nil {
		t.Fatalf("TenantsOfUser tras la baja: %v", err)
	}
	if len(tenants) != 0 {
		t.Fatalf("tras la baja no debía quedar ninguna: %v", tenants)
	}
}
