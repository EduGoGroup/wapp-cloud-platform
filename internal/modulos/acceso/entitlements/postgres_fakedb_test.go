package entitlements_test

// Parte de postgres_test.go, partido por tamaño (F2-03, a petición de Jhoan; solo se movieron
// declaraciones): el driver de database/sql de mentira y el reloj falso.

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"
)

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
	// featuresCloseErr: si no es nil, cerrar las filas de la consulta de features falla con él.
	featuresCloseErr error
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
		return &fakeRows{values: slices.Clone(f.features[tenant]), endErr: f.featuresEndErr, closeErr: f.featuresCloseErr}, nil
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
	// closeErr es lo que devuelve Close.
	closeErr error
	next     int
}

func (r *fakeRows) Columns() []string { return []string{"c"} }
func (r *fakeRows) Close() error      { return r.closeErr }
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
