package entitlements_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
)

// Los tests de Postgres son UNITARIOS: no hay base de datos. NewPostgres recibe un *sql.DB sobre un
// driver de mentira (fakeConnector) que contesta desde memoria las cuatro consultas del adaptador,
// cuenta cuántas le llegan y sabe fallar o quedarse bloqueado; el reloj es falso (WithClock), así
// que la caducidad se prueba moviéndolo, nunca durmiendo. Es la misma frontera que el
// httptest.Server de iam/infra/identity: se finge el otro lado del cable, no el objeto.
//
// Las reglas de RESOLUCIÓN (el SQL de verdad: override en los dos sentidos, plan NULL ⇒ basic, el
// anti-join de los overrides que apagan) no se prueban aquí: el driver de mentira contesta lo que
// se le siembra. Las prueba la suite entitlementshelpertest.ContratoResolver contra Postgres, en
// test/procesos/entitlements_contrato_test.go.

const (
	// testTTL es el TTL con el que se construye el Postgres de estos tests.
	testTTL = 10 * time.Second
	// defaultTTL es el TTL por defecto que promete el contrato.
	defaultTTL = 60 * time.Second

	tenantA = "tenant-a"
	tenantB = "tenant-b"
	// tenantSlow es el tenant cuyas consultas se quedan bloqueadas en el test del candado.
	tenantSlow = "tenant-slow"
	featureF   = "feature-f"
	featureG   = "feature-g"

	// watchdog acota la espera de lo que, con el candado sostenido durante la consulta, no
	// terminaría nunca. No decide nada de la lógica: solo convierte un cuelgue en un fallo.
	watchdog = 10 * time.Second
)

// errBoom es el fallo de infraestructura que siembra el driver de mentira.
var errBoom = errors.New("boom: la base no contesta")

// --- El arnés -----------------------------------------------------------------------------------

// newPostgres construye un Postgres sobre un fakeDB nuevo, con testTTL y un reloj falso, más las
// opciones extra (que se aplican después y pueden pisar esas).
func newPostgres(t *testing.T, extra ...entitlements.Option) (*entitlements.Postgres, *fakeDB, *fakeClock) {
	t.Helper()
	fdb := newFakeDB()
	clock := newFakeClock()
	db := sql.OpenDB(fakeConnector{db: fdb})
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("cerrar el *sql.DB de mentira: %v", err)
		}
	})
	opts := append([]entitlements.Option{entitlements.WithTTL(testTTL), entitlements.WithClock(clock.Now)}, extra...)
	return entitlements.NewPostgres(db, opts...), fdb, clock
}

// mustHas llama a Has y falla el test si devuelve error.
func mustHas(t *testing.T, p *entitlements.Postgres, tenant, feature string) bool {
	t.Helper()
	has, err := p.Has(context.Background(), tenant, feature)
	if err != nil {
		t.Fatalf("Has(%q, %q): error inesperado %v", tenant, feature, err)
	}
	return has
}

// mustList llama a ListEffective y falla el test si devuelve error.
func mustList(t *testing.T, p *entitlements.Postgres, tenant string) (string, []string) {
	t.Helper()
	plan, features, err := p.ListEffective(context.Background(), tenant)
	if err != nil {
		t.Fatalf("ListEffective(%q): error inesperado %v", tenant, err)
	}
	return plan, features
}

// --- Construcción y TTL -------------------------------------------------------------------------

// TestNewPostgres_DefaultTTLIsSixtySeconds: sin WithTTL, CacheTTL es 60 s. Construir no consulta.
func TestNewPostgres_DefaultTTLIsSixtySeconds(t *testing.T) {
	fdb := newFakeDB()
	db := sql.OpenDB(fakeConnector{db: fdb})
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("cerrar el *sql.DB de mentira: %v", err)
		}
	})
	p := entitlements.NewPostgres(db)
	if got := p.CacheTTL(); got != defaultTTL {
		t.Errorf("CacheTTL() sin WithTTL = %v, quería %v", got, defaultTTL)
	}
	if n := fdb.total(); n != 0 {
		t.Errorf("NewPostgres hizo %d consultas; construir no consulta la BD", n)
	}
}

// TestWithTTL: un TTL positivo se aplica; cero o negativo se ignoran y queda el de 60 s.
func TestWithTTL(t *testing.T) {
	cases := []struct {
		name string
		ttl  time.Duration
		want time.Duration
	}{
		{"positive_IsApplied", 5 * time.Second, 5 * time.Second},
		{"zero_IsIgnored", 0, defaultTTL},
		{"negative_IsIgnored", -time.Second, defaultTTL},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := entitlements.NewPostgres(nil, entitlements.WithTTL(c.ttl)).CacheTTL(); got != c.want {
				t.Errorf("CacheTTL() con WithTTL(%v) = %v, quería %v", c.ttl, got, c.want)
			}
		})
	}
}

// TestWithClock_DrivesExpiry: el reloj inyectado es el que decide la caducidad (sin él, mover el
// reloj falso no vencería nada).
func TestWithClock_DrivesExpiry(t *testing.T) {
	p, fdb, clock := newPostgres(t)
	mustHas(t, p, tenantA, featureF)
	clock.Advance(testTTL)
	mustHas(t, p, tenantA, featureF)
	if n := fdb.count(queryOverride); n != 2 {
		t.Errorf("tras mover el reloj inyectado un TTL hubo %d consultas de override; quería 2 (la entrada vence con ESE reloj)", n)
	}
}

// TestWithClock_NilIsIgnored: WithClock(nil) deja el reloj real; el Postgres sigue funcionando y
// cacheando (con 60 s de TTL, dos llamadas seguidas caen dentro).
func TestWithClock_NilIsIgnored(t *testing.T) {
	p, fdb, _ := newPostgres(t, entitlements.WithClock(nil), entitlements.WithTTL(defaultTTL))
	mustHas(t, p, tenantA, featureF)
	mustHas(t, p, tenantA, featureF)
	if n := fdb.count(queryOverride); n != 1 {
		t.Errorf("con WithClock(nil) hubo %d consultas de override; quería 1 (reloj real, segunda llamada en caché)", n)
	}
}
