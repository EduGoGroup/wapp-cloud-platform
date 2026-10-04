package receipts_test

// El driver de database/sql de mentira sobre el que corre postgres_test.go: apunta cada sentencia
// que le llega (texto y argumentos, tal cual) y contesta lo que el test guioniza. Sin Postgres.

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"sync"
	"testing"
)

// statement es una sentencia que llegó al driver.
type statement struct {
	query string
	args  []driver.Value
}

// reply es lo que el driver contesta a UNA sentencia.
type reply struct {
	// err: la sentencia falla con él.
	err error
	// rows: las filas de una consulta, cada una con sus columnas.
	rows [][]driver.Value
	// endErr: si no es nil, la iteración de las filas termina con él en vez de con io.EOF.
	endErr error
}

// fakeDB es el guion y el registro del driver. Las respuestas se consumen en orden; sin guion,
// una sentencia se contesta sin error y sin filas.
type fakeDB struct {
	mu         sync.Mutex
	statements []statement
	replies    []reply
}

// open devuelve un *sql.DB sobre el driver de mentira, que se cierra al acabar el test.
func (f *fakeDB) open(t *testing.T) *sql.DB {
	t.Helper()
	db := sql.OpenDB(fakeConnector{db: f})
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("cerrar el *sql.DB de mentira: %v", err)
		}
	})
	return db
}

// script encola las respuestas de las próximas sentencias.
func (f *fakeDB) script(replies ...reply) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replies = append(f.replies, replies...)
}

// seen devuelve las sentencias que llegaron, en orden.
func (f *fakeDB) seen() []statement {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]statement(nil), f.statements...)
}

// take apunta la sentencia y devuelve su respuesta.
func (f *fakeDB) take(query string, args []driver.NamedValue) reply {
	f.mu.Lock()
	defer f.mu.Unlock()
	values := make([]driver.Value, 0, len(args))
	for _, a := range args {
		values = append(values, a.Value)
	}
	f.statements = append(f.statements, statement{query: query, args: values})
	if len(f.replies) == 0 {
		return reply{}
	}
	r := f.replies[0]
	f.replies = f.replies[1:]
	return r
}

// fakeRows son las filas que devuelve el driver.
type fakeRows struct {
	rows   [][]driver.Value
	endErr error
	next   int
}

func (r *fakeRows) Columns() []string {
	n := 1
	if len(r.rows) > 0 {
		n = len(r.rows[0])
	}
	return make([]string, n)
}
func (r *fakeRows) Close() error { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.next >= len(r.rows) {
		if r.endErr != nil {
			return r.endErr
		}
		return io.EOF
	}
	copy(dest, r.rows[r.next])
	r.next++
	return nil
}

// fakeConnector abre conexiones contra el fakeDB; fakeConn contesta ExecContext y QueryContext.
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

func (c fakeConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	r := c.db.take(query, args)
	if r.err != nil {
		return nil, r.err
	}
	return driver.RowsAffected(1), nil
}

func (c fakeConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	r := c.db.take(query, args)
	if r.err != nil {
		return nil, r.err
	}
	return &fakeRows{rows: r.rows, endErr: r.endErr}, nil
}
