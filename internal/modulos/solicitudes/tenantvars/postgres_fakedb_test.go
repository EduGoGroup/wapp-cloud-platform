//go:build pendiente

package tenantvars_test

// El driver de database/sql de mentira sobre el que corre postgres_test.go: apunta, en orden, cada
// cosa que le llega —abrir, confirmar o revertir una transacción, y cada sentencia con su texto y
// sus argumentos tal cual— y contesta lo que el test guioniza. No interpreta SQL. Sin Postgres.
//
// Es el driver de internal/modulos/edge/receipts/postgres_fakedb_test.go más tres cosas que este
// adaptador necesita: transacciones (Replace va en postgres.WithTx), argumentos []string (los
// text[] del DELETE y del upsert, que database/sql rechazaría sin CheckNamedValue) y el fallo del
// cierre de las filas (fakeOpenRows, de internal/modulos/inferencia/degradation/fakedb_test.go).

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"sync"
	"testing"
)

// Las clases de evento que apunta el driver.
const (
	eventBegin    = "begin"
	eventCommit   = "commit"
	eventRollback = "rollback"
	eventExec     = "exec"
	eventQuery    = "query"
)

// event es una cosa que llegó al driver.
type event struct {
	kind string
	// query y args: el texto y los argumentos de una sentencia (exec o query).
	query string
	args  []driver.Value
	// inTx: la sentencia llegó por una conexión con una transacción abierta.
	inTx bool
}

// reply es lo que el driver contesta a UNA sentencia.
type reply struct {
	// err: la sentencia falla con él.
	err error
	// rows: las filas de una consulta, cada una con sus columnas.
	rows [][]driver.Value
	// endErr: si no es nil, el recorrido de las filas termina con él en vez de con io.EOF.
	endErr error
	// closeErr: si no es nil, las filas quedan abiertas al acabarse y su cierre falla con él.
	closeErr error
}

// fakeDB es el guion y el registro del driver. Las respuestas se consumen en orden, una por
// sentencia; sin guion, una sentencia se contesta sin error y sin filas.
type fakeDB struct {
	mu      sync.Mutex
	events  []event
	replies []reply
	// beginErr y commitErr: si no son nil, abrir o confirmar una transacción falla con ellos.
	beginErr, commitErr error
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

// failTx hace que abrir (begin) o confirmar (commit) una transacción falle.
func (f *fakeDB) failTx(begin, commit error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.beginErr, f.commitErr = begin, commit
}

// seen devuelve lo que llegó al driver, en orden.
func (f *fakeDB) seen() []event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]event(nil), f.events...)
}

// note apunta un evento de transacción.
func (f *fakeDB) note(kind string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, event{kind: kind})
}

// take apunta la sentencia y devuelve su respuesta.
func (f *fakeDB) take(kind, query string, args []driver.NamedValue, inTx bool) reply {
	f.mu.Lock()
	defer f.mu.Unlock()
	values := make([]driver.Value, 0, len(args))
	for _, a := range args {
		values = append(values, a.Value)
	}
	f.events = append(f.events, event{kind: kind, query: query, args: values, inTx: inTx})
	if len(f.replies) == 0 {
		return reply{}
	}
	r := f.replies[0]
	f.replies = f.replies[1:]
	return r
}

// fakeRows son las filas que devuelve el driver: tres columnas, las de tenant_variables.
type fakeRows struct {
	rows   [][]driver.Value
	endErr error
	next   int
}

func (r *fakeRows) Columns() []string { return []string{"key", "value", "updated_at"} }
func (r *fakeRows) Close() error      { return nil }
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

// fakeOpenRows son unas filas cuyo cierre FALLA y a las que database/sql no cierra por su cuenta.
//
// Cuando el recorrido se acaba (io.EOF), Rows.Next cierra las filas él mismo y el Close del
// llamante ya las encuentra cerradas. El fallo llega POR EL CIERRE del llamante solo si al
// acabarse las filas el driver dice que queda otro conjunto de resultados
// (driver.RowsNextResultSet): entonces Next no cierra y el primer cierre es el del llamante.
type fakeOpenRows struct {
	*fakeRows
	closeErr error
}

func (r *fakeOpenRows) HasNextResultSet() bool { return true }
func (r *fakeOpenRows) NextResultSet() error   { return io.EOF }
func (r *fakeOpenRows) Close() error           { return r.closeErr }

// fakeConnector abre conexiones contra el fakeDB. Cada conexión sabe si tiene una transacción
// abierta: database/sql ata la transacción a UNA conexión, así que una sentencia lanzada fuera
// de ella sale por otra conexión y queda apuntada con inTx=false.
type fakeConnector struct{ db *fakeDB }

func (c fakeConnector) Connect(context.Context) (driver.Conn, error) { return &fakeConn{db: c.db}, nil }
func (c fakeConnector) Driver() driver.Driver                        { return fakeDriver{} }

type fakeDriver struct{}

func (fakeDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("fakeDriver: se abre por fakeConnector")
}

type fakeConn struct {
	db   *fakeDB
	inTx bool
}

func (*fakeConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("fakeConn: sin sentencias preparadas")
}
func (*fakeConn) Close() error { return nil }

// CheckNamedValue deja pasar todo argumento tal cual: el test mira el []string que mandó el
// adaptador, no lo que un driver de verdad haría con él.
func (*fakeConn) CheckNamedValue(*driver.NamedValue) error { return nil }

func (c *fakeConn) Begin() (driver.Tx, error) {
	c.db.mu.Lock()
	err := c.db.beginErr
	c.db.mu.Unlock()
	if err != nil {
		return nil, err
	}
	c.db.note(eventBegin)
	c.inTx = true
	return fakeTx{conn: c}, nil
}

func (c *fakeConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	r := c.db.take(eventExec, query, args, c.inTx)
	if r.err != nil {
		return nil, r.err
	}
	return driver.RowsAffected(1), nil
}

func (c *fakeConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	r := c.db.take(eventQuery, query, args, c.inTx)
	if r.err != nil {
		return nil, r.err
	}
	rows := &fakeRows{rows: r.rows, endErr: r.endErr}
	if r.closeErr != nil {
		return &fakeOpenRows{fakeRows: rows, closeErr: r.closeErr}, nil
	}
	return rows, nil
}

// fakeTx es la transacción abierta en una conexión.
type fakeTx struct{ conn *fakeConn }

func (tx fakeTx) Commit() error {
	tx.conn.inTx = false
	tx.conn.db.mu.Lock()
	err := tx.conn.db.commitErr
	tx.conn.db.mu.Unlock()
	if err != nil {
		return err
	}
	tx.conn.db.note(eventCommit)
	return nil
}

func (tx fakeTx) Rollback() error {
	tx.conn.inTx = false
	tx.conn.db.note(eventRollback)
	return nil
}
