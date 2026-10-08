//go:build pendiente

package casebank_test

// El driver de database/sql de mentira sobre el que corre postgres_test.go: apunta cada sentencia
// que le llega (texto y argumentos, tal cual) y contesta lo que el test sembró. No interpreta SQL.
// Sin Postgres. Es el de internal/modulos/inferencia/degradation/fakedb_test.go reducido a lo que
// este adaptador usa: consultas de UNA fila con UNA columna (el id del RETURNING, el booleano del
// EXISTS).

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"sync"
	"testing"
)

// statement es una sentencia tal como llegó al driver.
type statement struct {
	query string
	args  []driver.Value
}

// fakeDB es el estado del driver: lo que llegó y lo que contesta.
type fakeDB struct {
	mu    sync.Mutex
	stmts []statement
	err   error          // si no es nil, toda sentencia falla con él
	row   []driver.Value // la fila de la respuesta; nil ⇒ sin filas (sql.ErrNoRows en QueryRow)
}

// openFakeDB abre un *sql.DB sobre un fakeDB nuevo y lo cierra al acabar el test.
func openFakeDB(t *testing.T) (*fakeDB, *sql.DB) {
	t.Helper()
	fake := &fakeDB{}
	db := sql.OpenDB(fakeConnector{db: fake})
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("cerrando el *sql.DB de mentira: %v", err)
		}
	})
	return fake, db
}

// answer siembra la fila con la que se contesta a las consultas.
func (f *fakeDB) answer(values ...driver.Value) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.row = values
}

// fail hace que toda sentencia falle con err.
func (f *fakeDB) fail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

// seen devuelve las sentencias que llegaron, en orden.
func (f *fakeDB) seen() []statement {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]statement(nil), f.stmts...)
}

// fakeRows es la respuesta: como mucho una fila, de una columna.
type fakeRows struct {
	row  []driver.Value
	done bool
}

func (r *fakeRows) Columns() []string { return []string{"col"} }
func (r *fakeRows) Close() error      { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.done || r.row == nil {
		return io.EOF
	}
	copy(dest, r.row)
	r.done = true
	return nil
}

type fakeConnector struct{ db *fakeDB }

func (c fakeConnector) Connect(context.Context) (driver.Conn, error) { return &fakeConn{db: c.db}, nil }
func (c fakeConnector) Driver() driver.Driver                        { return fakeDriver{} }

type fakeDriver struct{}

func (fakeDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("fakeDriver: se abre por fakeConnector")
}

type fakeConn struct{ db *fakeDB }

func (*fakeConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("fakeConn: sin sentencias preparadas")
}
func (*fakeConn) Close() error { return nil }
func (*fakeConn) Begin() (driver.Tx, error) {
	return nil, errors.New("fakeConn: este adaptador no abre transacciones")
}

// ExecContext existe para delatar: el adaptador solo consulta (QueryRow), así que una sentencia
// que llegue por aquí falla.
func (*fakeConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return nil, errors.New("fakeConn: el adaptador no ejecuta sentencias sin filas")
}

func (c *fakeConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.db.mu.Lock()
	defer c.db.mu.Unlock()
	values := make([]driver.Value, 0, len(args))
	for _, a := range args {
		values = append(values, a.Value)
	}
	c.db.stmts = append(c.db.stmts, statement{query: query, args: values})
	if c.db.err != nil {
		return nil, c.db.err
	}
	return &fakeRows{row: c.db.row}, nil
}
