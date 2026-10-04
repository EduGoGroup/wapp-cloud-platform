package iampostgres

// Parte del gemelo de canje.go: el driver de database/sql de mentira sobre el que corre Redeem
// entero, sin Postgres. Registra BEGIN/COMMIT/ROLLBACK y cada sentencia con una etiqueta legible,
// y contesta lo que el test guioniza. Los tests que lo usan viven en canje_tx_test.go.

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
)

// Etiquetas de los pasos que registra scriptedSQL, en el orden en que los da un canje limpio.
const (
	stepBegin        = "BEGIN"
	stepCommit       = "COMMIT"
	stepRollback     = "ROLLBACK"
	stepRead         = "read"
	stepLock         = "lock"
	stepCount        = "count"
	stepInsertMember = "insert-member"
	stepInsertRole   = "insert-role"
	stepMark         = "mark"
	stepCloseRequest = "close-request"
)

// redeemLabels traduce un trozo de SQL (con los espacios comprimidos) a su etiqueta.
var redeemLabels = []struct{ match, label string }{
	{"FROM public.tenant_invitations WHERE token_hash", stepRead},
	{"pg_advisory_xact_lock", stepLock},
	{"SELECT count(*)", stepCount},
	{"INSERT INTO public.tenant_members", stepInsertMember},
	{"INSERT INTO public.iam_user_roles", stepInsertRole},
	{"UPDATE public.tenant_invitations", stepMark},
	{"UPDATE public.access_requests", stepCloseRequest},
}

// redeemLabel devuelve la etiqueta de query, o la propia sentencia comprimida si no la conoce:
// una sentencia inesperada se ve tal cual en los pasos.
func redeemLabel(query string) string {
	q := strings.Join(strings.Fields(query), " ")
	for _, l := range redeemLabels {
		if strings.Contains(q, l.match) {
			return l.label
		}
	}
	return q
}

// scriptedSQL es el guion y el registro de un canje. Los campos de guion se fijan ANTES de
// llamar a Redeem; los pasos se leen con Steps.
type scriptedSQL struct {
	// invitation es la fila que contesta la lectura (las ocho columnas de readInvitation, la
	// última el now() de la base); nil es «no hay fila».
	invitation []driver.Value
	// others es lo que contesta el conteo de membresías en otras empresas.
	others int64
	// fail hace fallar la sentencia de esa etiqueta con ese error.
	fail map[string]error
	// zeroRows son las etiquetas cuya ejecución no afecta ninguna fila (por defecto, una).
	zeroRows map[string]bool
	// commitErr hace fallar el COMMIT.
	commitErr error

	mu    sync.Mutex
	steps []string
	// openTx cuenta las transacciones abiertas, en cualquier conexión.
	openTx int
}

// pool devuelve un *sql.DB sobre el guion; se cierra al acabar el test.
func (s *scriptedSQL) pool(t *testing.T) *sql.DB {
	t.Helper()
	db := sql.OpenDB(scriptedConnector{s})
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("cerrar el pool guionizado: %v", err)
		}
	})
	return db
}

// Steps devuelve los pasos registrados hasta ahora.
func (s *scriptedSQL) Steps() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.steps)
}

type scriptedConnector struct{ s *scriptedSQL }

// Connect da una conexión nueva cada vez: con una transacción abierta, una sentencia lanzada por
// el POOL sale por otra conexión y se registra con el prefijo "pool:" (ver scriptedConn.statement).
func (c scriptedConnector) Connect(context.Context) (driver.Conn, error) {
	return &scriptedConn{s: c.s}, nil
}
func (c scriptedConnector) Driver() driver.Driver { return scriptedDriver{} }

type scriptedDriver struct{}

func (scriptedDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("scriptedSQL: usa el conector")
}

// scriptedConn es una conexión del guion; inTx dice si tiene una transacción abierta.
type scriptedConn struct {
	s    *scriptedSQL
	inTx bool
}

func (*scriptedConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("scriptedSQL: sin Prepare")
}
func (*scriptedConn) Close() error { return nil }
func (c *scriptedConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *scriptedConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	c.s.mu.Lock()
	c.s.openTx++
	c.s.steps = append(c.s.steps, stepBegin)
	c.s.mu.Unlock()
	c.inTx = true
	return scriptedTx{c}, nil
}

// statement registra la sentencia y devuelve su etiqueta. Fuera de la transacción mientras hay
// una abierta en otra conexión, el paso lleva el prefijo "pool:".
func (c *scriptedConn) statement(query string) string {
	label := redeemLabel(query)
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	step := label
	if !c.inTx && c.s.openTx > 0 {
		step = "pool:" + label
	}
	c.s.steps = append(c.s.steps, step)
	return label
}

func (c *scriptedConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	label := c.statement(query)
	if err := c.s.fail[label]; err != nil {
		return nil, err
	}
	if c.s.zeroRows[label] {
		return driver.RowsAffected(0), nil
	}
	return driver.RowsAffected(1), nil
}

func (c *scriptedConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	label := c.statement(query)
	if err := c.s.fail[label]; err != nil {
		return nil, err
	}
	switch label {
	case stepRead:
		if c.s.invitation == nil {
			return &scriptedRows{}, nil
		}
		return &scriptedRows{rows: [][]driver.Value{c.s.invitation}}, nil
	case stepCount:
		return &scriptedRows{rows: [][]driver.Value{{c.s.others}}}, nil
	}
	return &scriptedRows{}, nil
}

type scriptedTx struct{ c *scriptedConn }

func (t scriptedTx) end(step string) {
	t.c.s.mu.Lock()
	t.c.s.openTx--
	t.c.s.steps = append(t.c.s.steps, step)
	t.c.s.mu.Unlock()
	t.c.inTx = false
}

func (t scriptedTx) Commit() error {
	if t.c.s.commitErr != nil {
		return t.c.s.commitErr
	}
	t.end(stepCommit)
	return nil
}

func (t scriptedTx) Rollback() error {
	t.end(stepRollback)
	return nil
}

// scriptedRows sirve las filas guionizadas; las columnas se llaman como la lectura del canje.
type scriptedRows struct {
	rows [][]driver.Value
	i    int
}

func (r *scriptedRows) Columns() []string {
	if len(r.rows) == 0 || len(r.rows[0]) == 1 {
		return []string{"c"}
	}
	return []string{"id", "tenant_id", "role_id", "expires_at", "redeemed_by", "redeemed_at", "revoked_at", "now"}
}

func (*scriptedRows) Close() error { return nil }

func (r *scriptedRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}
