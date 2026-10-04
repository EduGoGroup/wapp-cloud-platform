package entitlements_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
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

// --- El reloj falso ---------------------------------------------------------------------------

// fakeClock es un reloj que solo avanza cuando el test lo mueve.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// --- El driver de mentira -----------------------------------------------------------------------

// queryKind es cuál de las cuatro consultas del adaptador llegó al driver.
type queryKind int

const (
	queryOverride    queryKind = iota // SELECT enabled FROM public.tenant_features …
	queryPlanFeature                  // SELECT EXISTS(… public.plan_features …)
	queryTenantPlan                   // SELECT COALESCE(plan_id, 'basic') FROM public.tenants …
	queryFeatures                     // SELECT f.feature FROM (… UNION …) …
)

func (k queryKind) String() string {
	return [...]string{"override", "plan-feature", "tenant-plan", "features"}[k]
}

// classify reconoce la consulta por un fragmento que solo ella tiene.
func classify(query string) (queryKind, error) {
	switch {
	case strings.Contains(query, "SELECT enabled"):
		return queryOverride, nil
	case strings.Contains(query, "SELECT EXISTS("):
		return queryPlanFeature, nil
	case strings.Contains(query, "SELECT COALESCE(plan_id, 'basic')"):
		return queryTenantPlan, nil
	case strings.Contains(query, "SELECT f.feature"):
		return queryFeatures, nil
	}
	return 0, fmt.Errorf("fakeDB: consulta que no reconozco: %q", query)
}

// pair es la clave (tenant, feature) de lo que se siembra en el driver.
type pair struct{ tenant, feature string }

// fakeDB es el estado del driver de mentira: lo que contesta cada consulta, ya resuelto.
type fakeDB struct {
	mu sync.Mutex
	// overrides: si hay fila en tenant_features para el par, y su enabled.
	overrides map[pair]bool
	// planHas: lo que contesta el EXISTS del plan para el par (ausente ⇒ false).
	planHas map[pair]bool
	// plans: el plan efectivo del tenant; ausente ⇒ el tenant no existe (sin filas).
	plans map[string]string
	// features: las filas de la consulta de features del tenant, en el orden en que salen. Un nil
	// es un NULL, que no se puede escanear a string.
	features map[string][]driver.Value
	// featuresEndErr: si no es nil, la iteración de la consulta de features termina con él en
	// vez de con io.EOF.
	featuresEndErr error
	// fail: la consulta de ese tipo falla con ese error.
	fail map[queryKind]error
	// calls: cuántas consultas de cada tipo llegaron.
	calls map[queryKind]int
	// gate, si no es nil, se llama ANTES de contestar, fuera del mutex, con el tipo y el tenant.
	gate func(kind queryKind, tenant string)
}

func newFakeDB() *fakeDB {
	return &fakeDB{
		overrides: make(map[pair]bool),
		planHas:   make(map[pair]bool),
		plans:     make(map[string]string),
		features:  make(map[string][]driver.Value),
		fail:      make(map[queryKind]error),
		calls:     make(map[queryKind]int),
	}
}

// set cambia el estado del driver bajo su mutex.
func (f *fakeDB) set(change func(f *fakeDB)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(f)
}

// count devuelve cuántas consultas de ese tipo llegaron.
func (f *fakeDB) count(kind queryKind) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[kind]
}

// total devuelve cuántas consultas llegaron, de cualquier tipo.
func (f *fakeDB) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		n += c
	}
	return n
}

// answer contesta una consulta: las filas de su única columna, o el error sembrado.
func (f *fakeDB) answer(query string, args []driver.NamedValue) (driver.Rows, error) {
	kind, err := classify(query)
	if err != nil {
		return nil, err
	}
	tenant, feature, err := queryArgs(kind, args)
	if err != nil {
		return nil, err
	}

	f.mu.Lock()
	f.calls[kind]++
	gate := f.gate
	f.mu.Unlock()
	if gate != nil {
		gate(kind, tenant)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail[kind]; err != nil {
		return nil, err
	}
	switch kind {
	case queryOverride:
		if enabled, ok := f.overrides[pair{tenant, feature}]; ok {
			return &fakeRows{values: []driver.Value{enabled}}, nil
		}
		return &fakeRows{}, nil
	case queryPlanFeature:
		return &fakeRows{values: []driver.Value{f.planHas[pair{tenant, feature}]}}, nil
	case queryTenantPlan:
		if plan, ok := f.plans[tenant]; ok {
			return &fakeRows{values: []driver.Value{plan}}, nil
		}
		return &fakeRows{}, nil
	default:
		return &fakeRows{values: slices.Clone(f.features[tenant]), endErr: f.featuresEndErr}, nil
	}
}

// queryArgs lee los argumentos de la consulta: el tenant siempre es el primero, y el feature, el
// segundo de las dos consultas de Has.
func queryArgs(kind queryKind, args []driver.NamedValue) (tenant, feature string, err error) {
	if len(args) == 0 {
		return "", "", fmt.Errorf("fakeDB: consulta %v sin argumentos", kind)
	}
	tenant, ok := args[0].Value.(string)
	if !ok {
		return "", "", fmt.Errorf("fakeDB: consulta %v con tenant %T, no string", kind, args[0].Value)
	}
	if kind != queryOverride && kind != queryPlanFeature {
		return tenant, "", nil
	}
	if len(args) < 2 {
		return "", "", fmt.Errorf("fakeDB: consulta %v sin feature", kind)
	}
	if feature, ok = args[1].Value.(string); !ok {
		return "", "", fmt.Errorf("fakeDB: consulta %v con feature %T, no string", kind, args[1].Value)
	}
	return tenant, feature, nil
}

// fakeRows son las filas de una columna que devuelve el driver.
type fakeRows struct {
	values []driver.Value
	endErr error
	next   int
}

func (r *fakeRows) Columns() []string { return []string{"c"} }
func (r *fakeRows) Close() error      { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.next >= len(r.values) {
		if r.endErr != nil {
			return r.endErr
		}
		return io.EOF
	}
	dest[0] = r.values[r.next]
	r.next++
	return nil
}

// fakeConnector abre conexiones contra el fakeDB; fakeConn contesta por QueryContext.
type fakeConnector struct{ db *fakeDB }

func (c fakeConnector) Connect(context.Context) (driver.Conn, error) { return fakeConn(c), nil }
func (c fakeConnector) Driver() driver.Driver                        { return fakeDriver{} }

type fakeDriver struct{}

func (fakeDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("fakeDriver: se abre por fakeConnector")
}

type fakeConn struct{ db *fakeDB }

func (fakeConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("fakeConn: sin sentencias preparadas")
}
func (fakeConn) Close() error              { return nil }
func (fakeConn) Begin() (driver.Tx, error) { return nil, errors.New("fakeConn: sin transacciones") }
func (c fakeConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return c.db.answer(query, args)
}

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

// --- Has ----------------------------------------------------------------------------------------

// TestPostgres_Has_Resolution: el override manda en los dos sentidos; sin override manda lo que
// diga el plan; un tenant inexistente no tiene la feature, sin error.
func TestPostgres_Has_Resolution(t *testing.T) {
	cases := []struct {
		name     string
		override *bool
		planHas  bool
		want     bool
	}{
		{"overrideEnabled_WinsOverPlanWithout", new(true), false, true},
		{"overrideDisabled_WinsOverPlanWith", new(false), true, false},
		{"noOverride_PlanHasIt", nil, true, true},
		{"noOverride_PlanLacksIt", nil, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, fdb, _ := newPostgres(t)
			fdb.set(func(f *fakeDB) {
				if c.override != nil {
					f.overrides[pair{tenantA, featureF}] = *c.override
				}
				f.planHas[pair{tenantA, featureF}] = c.planHas
			})
			if got := mustHas(t, p, tenantA, featureF); got != c.want {
				t.Errorf("Has = %v, quería %v", got, c.want)
			}
		})
	}
}

// TestPostgres_Has_MissingTenantIsFalseWithoutError: un tenant que no existe no tiene derechos, y
// no es un error.
func TestPostgres_Has_MissingTenantIsFalseWithoutError(t *testing.T) {
	p, _, _ := newPostgres(t)
	if mustHas(t, p, "tenant-que-no-existe", featureF) {
		t.Error("Has de un tenant inexistente = true; no tiene derechos")
	}
}

// TestPostgres_Has_CachesPerPair: dentro del TTL el mismo par no vuelve a consultar; otro feature
// del mismo tenant u otro tenant con el mismo feature son entradas propias.
func TestPostgres_Has_CachesPerPair(t *testing.T) {
	p, fdb, clock := newPostgres(t)
	fdb.set(func(f *fakeDB) { f.overrides[pair{tenantA, featureF}] = true })

	if !mustHas(t, p, tenantA, featureF) {
		t.Fatal("primer Has(A, f) = false, quería true")
	}
	clock.Advance(testTTL / 2)
	fdb.set(func(f *fakeDB) { f.overrides[pair{tenantA, featureF}] = false })
	if !mustHas(t, p, tenantA, featureF) {
		t.Error("segundo Has(A, f) dentro del TTL = false: debía servirse de la caché (true)")
	}
	if n := fdb.count(queryOverride); n != 1 {
		t.Errorf("dos Has(A, f) dentro del TTL hicieron %d consultas de override; quería 1", n)
	}

	mustHas(t, p, tenantA, featureG)
	if n := fdb.count(queryOverride); n != 2 {
		t.Errorf("Has(A, g) tras Has(A, f): %d consultas de override, quería 2 (otro par, otra entrada)", n)
	}
	mustHas(t, p, tenantB, featureF)
	if n := fdb.count(queryOverride); n != 3 {
		t.Errorf("Has(B, f) tras Has(A, f): %d consultas de override, quería 3 (otro tenant, otra entrada)", n)
	}
}

// TestPostgres_Has_CachesFalse: el false también se cachea.
func TestPostgres_Has_CachesFalse(t *testing.T) {
	p, fdb, _ := newPostgres(t)
	if mustHas(t, p, tenantA, featureF) {
		t.Fatal("primer Has = true sin override ni plan; quería false")
	}
	fdb.set(func(f *fakeDB) { f.overrides[pair{tenantA, featureF}] = true })
	if mustHas(t, p, tenantA, featureF) {
		t.Error("segundo Has dentro del TTL = true: el false debía quedar en la caché")
	}
	if n := fdb.count(queryOverride); n != 1 {
		t.Errorf("dos Has con respuesta false hicieron %d consultas de override; quería 1 (el false se cachea)", n)
	}
}

// TestPostgres_Has_ExpiresExactlyAtTTL: un instante antes del vencimiento la entrada vale; en el
// instante exacto ya no, y se vuelve a consultar.
func TestPostgres_Has_ExpiresExactlyAtTTL(t *testing.T) {
	p, fdb, clock := newPostgres(t)
	fdb.set(func(f *fakeDB) { f.overrides[pair{tenantA, featureF}] = true })
	mustHas(t, p, tenantA, featureF)
	fdb.set(func(f *fakeDB) { f.overrides[pair{tenantA, featureF}] = false })

	clock.Advance(testTTL - time.Nanosecond)
	if !mustHas(t, p, tenantA, featureF) {
		t.Error("Has un nanosegundo antes del vencimiento = false: la entrada aún valía (true)")
	}
	clock.Advance(time.Nanosecond)
	if mustHas(t, p, tenantA, featureF) {
		t.Error("Has en el instante del vencimiento = true: la entrada ya no valía y debía re-consultar (false)")
	}
	if n := fdb.count(queryOverride); n != 2 {
		t.Errorf("hubo %d consultas de override; quería 2 (la inicial y la del vencimiento)", n)
	}
}

// TestPostgres_Has_ErrorIsNotCached: con la BD fallando, Has devuelve (false, err); la llamada
// siguiente vuelve a consultar.
func TestPostgres_Has_ErrorIsNotCached(t *testing.T) {
	p, fdb, _ := newPostgres(t)
	fdb.set(func(f *fakeDB) {
		f.fail[queryOverride] = errBoom
		f.overrides[pair{tenantA, featureF}] = true
	})
	has, err := p.Has(context.Background(), tenantA, featureF)
	if err == nil || has {
		t.Fatalf("Has con la BD fallando = (%v, %v); quería (false, error)", has, err)
	}
	fdb.set(func(f *fakeDB) { delete(f.fail, queryOverride) })
	if !mustHas(t, p, tenantA, featureF) {
		t.Error("Has tras un error = false: el error no debía cachearse y la BD ya contesta true")
	}
}

// TestPostgres_Has_ErrorTexts: cada fallo de la BD sale envuelto, con su texto literal.
func TestPostgres_Has_ErrorTexts(t *testing.T) {
	cases := []struct {
		name   string
		kind   queryKind
		prefix string
	}{
		{"overrideQueryFails", queryOverride, "entitlements: leer override de feature: "},
		{"planQueryFails", queryPlanFeature, "entitlements: resolver feature del plan: "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, fdb, _ := newPostgres(t)
			fdb.set(func(f *fakeDB) { f.fail[c.kind] = errBoom })
			has, err := p.Has(context.Background(), tenantA, featureF)
			if has {
				t.Error("Has con error = true; un error nunca concede la feature")
			}
			if !errors.Is(err, errBoom) {
				t.Errorf("Has: err = %v; quería que envolviera el error de la BD", err)
			}
			if err == nil || !strings.HasPrefix(err.Error(), c.prefix) {
				t.Errorf("Has: err = %v; quería el prefijo literal %q", err, c.prefix)
			}
		})
	}
}

// --- ListEffective ------------------------------------------------------------------------------

// TestPostgres_ListEffective_PlanAndSortedFeatures: devuelve el plan del tenant y sus features en
// orden por bytes ('_' antes que 'b'), sea cual sea el orden en que la BD las entregue.
func TestPostgres_ListEffective_PlanAndSortedFeatures(t *testing.T) {
	p, fdb, _ := newPostgres(t)
	fdb.set(func(f *fakeDB) {
		f.plans[tenantA] = "pro"
		f.features[tenantA] = []driver.Value{"menu", "contract_ab", "cart_basic", "contract_a_b"}
	})
	plan, features := mustList(t, p, tenantA)
	if plan != "pro" {
		t.Errorf("plan = %q, quería %q", plan, "pro")
	}
	if want := []string{"cart_basic", "contract_a_b", "contract_ab", "menu"}; !slices.Equal(features, want) {
		t.Errorf("features = %v, quería %v (orden alfabético por bytes)", features, want)
	}
}

// TestPostgres_ListEffective_EmptyIsNotError: un plan sin features da la lista vacía, sin error.
func TestPostgres_ListEffective_EmptyIsNotError(t *testing.T) {
	p, fdb, _ := newPostgres(t)
	fdb.set(func(f *fakeDB) { f.plans[tenantA] = "basic" })
	plan, features := mustList(t, p, tenantA)
	if plan != "basic" || len(features) != 0 {
		t.Errorf("ListEffective = (%q, %v); quería (\"basic\", vacía)", plan, features)
	}
}

// TestPostgres_ListEffective_MissingTenant: un tenant que no existe da ("", nil, nil).
func TestPostgres_ListEffective_MissingTenant(t *testing.T) {
	p, _, _ := newPostgres(t)
	plan, features, err := p.ListEffective(context.Background(), "tenant-que-no-existe")
	if plan != "" || features != nil || err != nil {
		t.Errorf("ListEffective de un tenant inexistente = (%q, %v, %v); quería (\"\", nil, nil)", plan, features, err)
	}
}

// TestPostgres_ListEffective_CachesPerTenant: dentro del TTL el mismo tenant no vuelve a
// consultar; otro tenant es otra entrada.
func TestPostgres_ListEffective_CachesPerTenant(t *testing.T) {
	p, fdb, clock := newPostgres(t)
	fdb.set(func(f *fakeDB) {
		f.plans[tenantA], f.plans[tenantB] = "pro", "basic"
		f.features[tenantA] = []driver.Value{"menu"}
	})
	mustList(t, p, tenantA)
	clock.Advance(testTTL / 2)
	fdb.set(func(f *fakeDB) { f.plans[tenantA] = "basic" })
	if plan, features := mustList(t, p, tenantA); plan != "pro" || !slices.Equal(features, []string{"menu"}) {
		t.Errorf("segundo ListEffective(A) dentro del TTL = (%q, %v); quería lo cacheado (\"pro\", [menu])", plan, features)
	}
	if n := fdb.count(queryTenantPlan); n != 1 {
		t.Errorf("dos ListEffective(A) dentro del TTL hicieron %d consultas de plan; quería 1", n)
	}
	if plan, _ := mustList(t, p, tenantB); plan != "basic" {
		t.Errorf("ListEffective(B) = plan %q; quería \"basic\" (otra entrada)", plan)
	}
	if n := fdb.count(queryTenantPlan); n != 2 {
		t.Errorf("ListEffective(B) tras ListEffective(A): %d consultas de plan, quería 2", n)
	}
}

// TestPostgres_ListEffective_ExpiresExactlyAtTTL: la misma frontera que Has.
func TestPostgres_ListEffective_ExpiresExactlyAtTTL(t *testing.T) {
	p, fdb, clock := newPostgres(t)
	fdb.set(func(f *fakeDB) { f.plans[tenantA] = "pro" })
	mustList(t, p, tenantA)
	fdb.set(func(f *fakeDB) { f.plans[tenantA] = "basic" })

	clock.Advance(testTTL - time.Nanosecond)
	if plan, _ := mustList(t, p, tenantA); plan != "pro" {
		t.Errorf("ListEffective un nanosegundo antes del vencimiento = plan %q; la entrada aún valía (\"pro\")", plan)
	}
	clock.Advance(time.Nanosecond)
	if plan, _ := mustList(t, p, tenantA); plan != "basic" {
		t.Errorf("ListEffective en el instante del vencimiento = plan %q; debía re-consultar (\"basic\")", plan)
	}
}

// TestPostgres_ListEffective_ReturnsCopy: mutar la lista devuelta —la del fallo de caché o la del
// acierto— no corrompe lo cacheado.
func TestPostgres_ListEffective_ReturnsCopy(t *testing.T) {
	p, fdb, _ := newPostgres(t)
	fdb.set(func(f *fakeDB) {
		f.plans[tenantA] = "pro"
		f.features[tenantA] = []driver.Value{"cart_basic", "menu"}
	})
	want := []string{"cart_basic", "menu"}

	_, miss := mustList(t, p, tenantA)
	miss[0] = "poisoned-on-miss"
	_, hit := mustList(t, p, tenantA)
	if !slices.Equal(hit, want) {
		t.Fatalf("tras mutar la lista del fallo de caché, el acierto devuelve %v; quería %v", hit, want)
	}
	hit[1] = "poisoned-on-hit"
	if _, again := mustList(t, p, tenantA); !slices.Equal(again, want) {
		t.Errorf("tras mutar la lista de un acierto, el siguiente devuelve %v; quería %v", again, want)
	}
	if n := fdb.count(queryTenantPlan); n != 1 {
		t.Errorf("hubo %d consultas de plan; quería 1 (todo lo demás, de la caché)", n)
	}
}

// TestPostgres_ListEffective_ErrorIsNotCached: con la BD fallando devuelve ("", nil, err); la
// llamada siguiente vuelve a consultar.
func TestPostgres_ListEffective_ErrorIsNotCached(t *testing.T) {
	p, fdb, _ := newPostgres(t)
	fdb.set(func(f *fakeDB) {
		f.plans[tenantA] = "pro"
		f.fail[queryFeatures] = errBoom
	})
	if _, _, err := p.ListEffective(context.Background(), tenantA); err == nil {
		t.Fatal("ListEffective con la BD fallando: err = nil")
	}
	fdb.set(func(f *fakeDB) { delete(f.fail, queryFeatures) })
	if plan, _ := mustList(t, p, tenantA); plan != "pro" {
		t.Errorf("ListEffective tras un error = plan %q; quería \"pro\" (el error no se cachea)", plan)
	}
}

// TestPostgres_ListEffective_ErrorTexts: cada fallo sale como ("", nil, err), envuelto y con su
// texto literal.
func TestPostgres_ListEffective_ErrorTexts(t *testing.T) {
	cases := []struct {
		name    string
		seed    func(f *fakeDB)
		prefix  string
		wrapped bool // si err envuelve errBoom
	}{
		{"planQueryFails", func(f *fakeDB) { f.fail[queryTenantPlan] = errBoom },
			"entitlements: resolver el plan del tenant: ", true},
		{"featuresQueryFails", func(f *fakeDB) { f.fail[queryFeatures] = errBoom },
			"entitlements: listar features efectivas: ", true},
		{"nullFeatureRow", func(f *fakeDB) { f.features[tenantA] = []driver.Value{"menu", nil} },
			"entitlements: scan de feature: ", false},
		{"iterationFails", func(f *fakeDB) {
			f.features[tenantA] = []driver.Value{"menu"}
			f.featuresEndErr = errBoom
		}, "entitlements: iterar features: ", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, fdb, _ := newPostgres(t)
			fdb.set(func(f *fakeDB) {
				f.plans[tenantA] = "pro"
				c.seed(f)
			})
			plan, features, err := p.ListEffective(context.Background(), tenantA)
			if plan != "" || features != nil {
				t.Errorf("ListEffective con error = (%q, %v); quería (\"\", nil)", plan, features)
			}
			if err == nil || !strings.HasPrefix(err.Error(), c.prefix) {
				t.Errorf("ListEffective: err = %v; quería el prefijo literal %q", err, c.prefix)
			}
			if c.wrapped && !errors.Is(err, errBoom) {
				t.Errorf("ListEffective: err = %v; quería que envolviera el error de la BD", err)
			}
		})
	}
}

// --- Las dos cachés, el candado y la concurrencia ----------------------------------------------

// TestPostgres_CachesAreSeparate: lo que cachea Has no sirve a ListEffective, ni al revés.
func TestPostgres_CachesAreSeparate(t *testing.T) {
	p, fdb, _ := newPostgres(t)
	fdb.set(func(f *fakeDB) { f.plans[tenantA], f.plans[tenantB] = "pro", "pro" })

	mustHas(t, p, tenantA, featureF)
	mustList(t, p, tenantA)
	if n := fdb.count(queryTenantPlan); n != 1 {
		t.Errorf("ListEffective(A) tras Has(A, f): %d consultas de plan, quería 1 (la caché de Has no le sirve)", n)
	}

	mustList(t, p, tenantB)
	mustHas(t, p, tenantB, featureF)
	if n := fdb.count(queryOverride); n != 2 {
		t.Errorf("Has(B, f) tras ListEffective(B): %d consultas de override, quería 2 (la caché por tenant no le sirve)", n)
	}
}

// TestPostgres_QueryDoesNotHoldTheLock: mientras una consulta de un tenant está colgada en la BD,
// los demás llamantes —los que aciertan en la caché y los que tienen que consultar— terminan.
func TestPostgres_QueryDoesNotHoldTheLock(t *testing.T) {
	cases := []struct {
		name string
		slow func(p *entitlements.Postgres) error
	}{
		{"slowHas", func(p *entitlements.Postgres) error {
			_, err := p.Has(context.Background(), tenantSlow, featureF)
			return err
		}},
		{"slowListEffective", func(p *entitlements.Postgres) error {
			_, _, err := p.ListEffective(context.Background(), tenantSlow)
			return err
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, fdb, _ := newPostgres(t)
			fdb.set(func(f *fakeDB) { f.plans[tenantA], f.plans[tenantSlow] = "pro", "pro" })
			// Con la BD sana se llenan las entradas de A, que luego se servirán de la caché.
			mustHas(t, p, tenantA, featureF)
			mustList(t, p, tenantA)

			started := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			fdb.set(func(f *fakeDB) {
				f.gate = func(_ queryKind, tenant string) {
					if tenant == tenantSlow {
						once.Do(func() { close(started) })
						<-release
					}
				}
			})
			slowDone := make(chan error, 1)
			go func() { slowDone <- c.slow(p) }()
			<-started

			othersDone := make(chan error, 1)
			go func() {
				ctx := context.Background()
				_, errHit := p.Has(ctx, tenantA, featureF)         // acierto
				_, _, errListHit := p.ListEffective(ctx, tenantA)  // acierto
				_, errMiss := p.Has(ctx, tenantB, featureG)        // fallo: consulta
				_, _, errListMiss := p.ListEffective(ctx, tenantB) // fallo: consulta
				othersDone <- errors.Join(errHit, errListHit, errMiss, errListMiss)
			}()
			var othersErr error
			finished := false
			select {
			case othersErr = <-othersDone:
				finished = true
			case <-time.After(watchdog):
				t.Errorf("con una consulta de %q colgada, los demás llamantes no terminaron en %v: el candado se sostiene durante la consulta", tenantSlow, watchdog)
			}
			close(release)
			if err := <-slowDone; err != nil {
				t.Errorf("la consulta lenta terminó con error %v", err)
			}
			if !finished {
				othersErr = <-othersDone
			}
			if othersErr != nil {
				t.Errorf("los demás llamantes terminaron con error: %v", othersErr)
			}
		})
	}
}

// TestPostgres_ConcurrentUse: Has y ListEffective en paralelo, con el reloj avanzando para que se
// mezclen aciertos y fallos de caché. Bajo -race, toda escritura sin proteger se ve aquí; y cada
// respuesta es la sembrada.
func TestPostgres_ConcurrentUse(t *testing.T) {
	p, fdb, clock := newPostgres(t)
	tenants := []string{tenantA, tenantB, tenantSlow}
	fdb.set(func(f *fakeDB) {
		for _, tenant := range tenants {
			f.plans[tenant] = "pro"
			f.features[tenant] = []driver.Value{"menu", "cart_basic"}
			f.overrides[pair{tenant, featureF}] = true
		}
	})
	const workers = 16
	var wg sync.WaitGroup
	errs := make(chan string, workers*len(tenants)*2)
	for i := range workers {
		wg.Go(func() {
			ctx := context.Background()
			for _, tenant := range tenants {
				if has, err := p.Has(ctx, tenant, featureF); err != nil || !has {
					errs <- fmt.Sprintf("Has(%q) en paralelo = (%v, %v); quería (true, nil)", tenant, has, err)
				}
				plan, features, err := p.ListEffective(ctx, tenant)
				if err != nil || plan != "pro" || !slices.Equal(features, []string{"cart_basic", "menu"}) {
					errs <- fmt.Sprintf("ListEffective(%q) en paralelo = (%q, %v, %v)", tenant, plan, features, err)
				}
				if len(features) > 0 {
					features[0] = "poisoned" // cada llamante recibe su copia
				}
			}
			if i%4 == 0 {
				clock.Advance(testTTL)
			}
		})
	}
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Error(msg)
	}
}
