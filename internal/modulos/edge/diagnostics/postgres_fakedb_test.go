package diagnostics

// El driver de database/sql de mentira sobre el que corren los tests del adaptador: apunta cada
// sentencia que le llega (texto y argumentos, tal cual) y contesta lo que el test guioniza, en
// orden. No abre transacciones: si el adaptador pidiera una, el test fallaría. Sin Postgres.

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
	// affected: las filas que dice haber tocado un Exec.
	affected int64
	// affectedErr: el Exec va bien pero leer sus filas afectadas falla con él.
	affectedErr error
	// row: la fila que contesta una consulta; nil es «sin filas».
	row []driver.Value
}

// fakeDB es el guion y el registro del driver. Las respuestas se consumen en orden; sin guion,
// una sentencia se contesta sin error, sin filas y con cero filas afectadas.
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

// fakeResult es el resultado de un Exec: las filas afectadas, o el error al leerlas.
type fakeResult struct {
	affected int64
	err      error
}

func (fakeResult) LastInsertId() (int64, error) {
	return 0, errors.New("fakeResult: sin LastInsertId")
}
func (r fakeResult) RowsAffected() (int64, error) { return r.affected, r.err }

// fakeRows es el resultado de una consulta: una fila o ninguna.
type fakeRows struct {
	row  []driver.Value
	done bool
}

func (r *fakeRows) Columns() []string {
	n := 1
	if r.row != nil {
		n = len(r.row)
	}
	return make([]string, n)
}
func (r *fakeRows) Close() error { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.row == nil || r.done {
		return io.EOF
	}
	copy(dest, r.row)
	r.done = true
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
	return fakeResult{affected: r.affected, err: r.affectedErr}, nil
}

func (c fakeConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	r := c.db.take(query, args)
	if r.err != nil {
		return nil, r.err
	}
	return &fakeRows{row: r.row}, nil
}
