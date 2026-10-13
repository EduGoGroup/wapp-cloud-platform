package platformadmin

// Parte de access_requests_postgres_test.go, partido por tamaño (F2-03, a petición de Jhoan; solo se movieron
// declaraciones): el driver de database/sql de mentira que registra BEGIN/COMMIT/ROLLBACK.

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

	iampostgres "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/infra/postgres"
)

// fixedNow es un created_at fijo de las filas programadas.
var fixedNow = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

// ── fakeSQL: un driver de database/sql que no es una base ────────────────────────────────────

// fakeRule programa la respuesta a las sentencias cuyo texto (con los espacios comprimidos)
// contiene match: filas para una consulta, filas afectadas para una ejecución, o un error.
type fakeRule struct {
	match       string
	rows        [][]driver.Value
	affected    int64
	affectedErr error
	err         error
}

// fakeSQL registra cada paso con una etiqueta legible (ver label) y los argumentos de cada
// sentencia. Las reglas se miran en orden: gana la primera que case.
type fakeSQL struct {
	mu        sync.Mutex
	rules     []fakeRule
	steps     []string
	args      map[string][]driver.Value
	beginErr  error
	commitErr error
	// openTx cuenta las transacciones abiertas (en cualquier conexión).
	openTx int
}

// newFakeSQL devuelve un fakeSQL con las reglas por defecto de un alta limpia: la persona no es
// de otra empresa (count 0) y toda ejecución toca una fila.
func newFakeSQL(rules ...fakeRule) *fakeSQL {
	return &fakeSQL{
		rules: append(rules, fakeRule{match: "SELECT count(*)", rows: [][]driver.Value{{int64(0)}}}),
		args:  map[string][]driver.Value{},
	}
}

// repo devuelve un Repository sobre f con el resolver features.
func (f *fakeSQL) repo(t *testing.T, features iampostgres.FeatureResolver) *Repository {
	t.Helper()
	db := sql.OpenDB(fakeConnector{f})
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("cerrando el pool de mentira: %v", err)
		}
	})
	return NewRepository(db, features)
}

// labels traduce un trozo de SQL al paso que registra el fake.
var labels = []struct{ match, label string }{
	{"pg_advisory_xact_lock", "lock"},
	{"SELECT count(*)", "count"},
	{"INSERT INTO public.tenant_members", "insert-member"},
	{"INSERT INTO public.iam_user_roles", "insert-role"},
	{"SET status = 'approved'", "approve"},
	{"SET status = 'rejected'", "reject"},
	{"SELECT true FROM public.access_requests", "exists-request"},
	{"INSERT INTO public.access_requests", "insert-request"},
	{"SELECT user_id::text, status", "lookup"},
	{"FROM public.iam_roles", "role-id"},
	{"FROM public.tenant_members WHERE user_id = $1 AND tenant_id = $2", "retry-member"},
	{"FROM public.iam_user_roles WHERE", "retry-role"},
	{"FROM public.access_requests WHERE status = $1", "list"},
}

func compact(query string) string { return strings.Join(strings.Fields(query), " ") }

func label(query string) string {
	q := compact(query)
	for _, l := range labels {
		if strings.Contains(q, l.match) {
			return l.label
		}
	}
	return q
}

func (f *fakeSQL) rule(query string) (fakeRule, bool) {
	q := compact(query)
	for _, r := range f.rules {
		if strings.Contains(q, r.match) {
			return r, true
		}
	}
	return fakeRule{}, false
}

func (f *fakeSQL) record(step, query string, args []driver.NamedValue) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.steps = append(f.steps, step)
	if query != "" {
		vals := make([]driver.Value, 0, len(args))
		for _, a := range args {
			vals = append(vals, a.Value)
		}
		f.args[label(query)] = vals
	}
}

// Steps devuelve los pasos registrados.
func (f *fakeSQL) Steps() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.steps)
}

// argsOf devuelve los argumentos de la última sentencia con esa etiqueta.
func (f *fakeSQL) argsOf(l string) []driver.Value {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.args[l]
}

type fakeConnector struct{ f *fakeSQL }

// Connect da una conexión nueva cada vez: con una transacción abierta, una sentencia por el POOL
// va por otra conexión, y el fake la registra con el prefijo "pool:" (ver fakeConn.step).
func (c fakeConnector) Connect(context.Context) (driver.Conn, error) { return &fakeConn{f: c.f}, nil }
func (c fakeConnector) Driver() driver.Driver                        { return fakeDriver{} }

type fakeDriver struct{}

func (fakeDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("fakeSQL: usa el conector")
}

// fakeConn es una conexión del fake; inTx dice si tiene una transacción abierta.
type fakeConn struct {
	f    *fakeSQL
	inTx bool
}

func (*fakeConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("fakeSQL: sin Prepare") }
func (*fakeConn) Close() error                        { return nil }
func (c *fakeConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *fakeConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	if c.f.beginErr != nil {
		return nil, c.f.beginErr
	}
	c.f.mu.Lock()
	c.f.openTx++
	c.f.mu.Unlock()
	c.inTx = true
	c.f.record("BEGIN", "", nil)
	return fakeTx{c}, nil
}

// step es la etiqueta de la sentencia: con una transacción abierta en OTRA conexión, una
// sentencia fuera de ella lleva el prefijo "pool:" (escribir por el pool y no por la tx).
func (c *fakeConn) step(query string) string {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	if !c.inTx && c.f.openTx > 0 {
		return "pool:" + label(query)
	}
	return label(query)
}

func (c *fakeConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.f.record(c.step(query), query, args)
	r, ok := c.f.rule(query)
	if !ok {
		return fakeResult{affected: 1}, nil
	}
	if r.err != nil {
		return nil, r.err
	}
	return fakeResult{affected: r.affected, err: r.affectedErr}, nil
}

func (c *fakeConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.f.record(c.step(query), query, args)
	r, _ := c.f.rule(query)
	if r.err != nil {
		return nil, r.err
	}
	return &fakeRows{rows: r.rows}, nil
}

type fakeTx struct{ c *fakeConn }

func (t fakeTx) end(step string) {
	t.c.f.mu.Lock()
	t.c.f.openTx--
	t.c.f.mu.Unlock()
	t.c.inTx = false
	t.c.f.record(step, "", nil)
}

func (t fakeTx) Commit() error {
	if t.c.f.commitErr != nil {
		return t.c.f.commitErr
	}
	t.end("COMMIT")
	return nil
}

func (t fakeTx) Rollback() error {
	t.end("ROLLBACK")
	return nil
}

type fakeResult struct {
	affected int64
	err      error
}

func (fakeResult) LastInsertId() (int64, error)   { return 0, errors.New("fakeSQL: sin LastInsertId") }
func (r fakeResult) RowsAffected() (int64, error) { return r.affected, r.err }

type fakeRows struct {
	rows [][]driver.Value
	i    int
}

func (r *fakeRows) Columns() []string {
	if len(r.rows) == 0 {
		return []string{"c"}
	}
	cols := make([]string, len(r.rows[0]))
	for i := range cols {
		cols[i] = fmt.Sprintf("c%d", i)
	}
	return cols
}

func (r *fakeRows) Close() error { return nil }

func (r *fakeRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}
