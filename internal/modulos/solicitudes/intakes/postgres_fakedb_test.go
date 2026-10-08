//go:build pendiente

package intakes

// El driver de database/sql de mentira sobre el que corren los postgres*_test.go: apunta, en
// orden, cada cosa que le llega —abrir, confirmar o revertir una transacción, y cada sentencia
// con su texto y sus argumentos tal cual— y contesta lo que el test guioniza. No interpreta SQL.
// Sin Postgres.
//
// Es el de internal/modulos/solicitudes/tenantvars/postgres_fakedb_test.go más dos cosas que este
// adaptador necesita: filas de CUALQUIER anchura (aquí hay sentencias de 1 a 17 columnas; el
// número de columnas sale de la primera fila guionizada) y el fallo del rollback (el reflejo del
// CRM revierte a mano y promete un texto si eso falla). Sus nombres llevan el prefijo pg porque
// el paquete comparte tests con otros adaptadores.

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strconv"
	"sync"
	"testing"
)

// Las clases de evento que apunta el driver.
const (
	pgBegin    = "begin"
	pgCommit   = "commit"
	pgRollback = "rollback"
	pgExec     = "exec"
	pgQuery    = "query"
)

// pgEvent es una cosa que llegó al driver.
type pgEvent struct {
	kind string
	// query y args: el texto y los argumentos de una sentencia (exec o query).
	query string
	args  []driver.Value
	// inTx: la sentencia llegó por una conexión con una transacción abierta.
	inTx bool
}

// pgReply es lo que el driver contesta a UNA sentencia.
type pgReply struct {
	// err: la sentencia falla con él.
	err error
	// rows: las filas de una consulta, cada una con sus columnas.
	rows [][]driver.Value
	// endErr: si no es nil, el recorrido de las filas termina con él en vez de con io.EOF.
	endErr error
	// closeErr: si no es nil, las filas quedan abiertas al acabarse y su cierre falla con él.
	closeErr error
}

// pgFake es el guion y el registro del driver. Las respuestas se consumen en orden, una por
// sentencia; sin guion, una sentencia se contesta sin error y sin filas.
type pgFake struct {
	mu      sync.Mutex
	events  []pgEvent
	replies []pgReply
	// beginErr, commitErr y rollbackErr: si no son nil, abrir, confirmar o revertir una
	// transacción falla con ellos.
	beginErr, commitErr, rollbackErr error
}

// open devuelve un *sql.DB sobre el driver de mentira, que se cierra al acabar el test.
func (f *pgFake) open(t *testing.T) *sql.DB {
	t.Helper()
	db := sql.OpenDB(pgConnector{db: f})
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("cerrar el *sql.DB de mentira: %v", err)
		}
	})
	return db
}

// script encola las respuestas de las próximas sentencias.
func (f *pgFake) script(replies ...pgReply) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replies = append(f.replies, replies...)
}

// failTx hace que abrir (begin), confirmar (commit) o revertir (rollback) una transacción falle.
func (f *pgFake) failTx(begin, commit, rollback error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.beginErr, f.commitErr, f.rollbackErr = begin, commit, rollback
}

// seen devuelve lo que llegó al driver, en orden.
func (f *pgFake) seen() []pgEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]pgEvent(nil), f.events...)
}

// kinds devuelve solo la clase de cada evento, en orden: es la forma de la conversación.
func (f *pgFake) kinds() []string {
	seen := f.seen()
	out := make([]string, 0, len(seen))
	for _, e := range seen {
		out = append(out, e.kind)
	}
	return out
}

// statements devuelve solo las sentencias (exec y query), sin los eventos de transacción.
func (f *pgFake) statements() []pgEvent {
	var out []pgEvent
	for _, e := range f.seen() {
		if e.kind == pgExec || e.kind == pgQuery {
			out = append(out, e)
		}
	}
	return out
}

// note apunta un evento de transacción.
func (f *pgFake) note(kind string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, pgEvent{kind: kind})
}

// take apunta la sentencia y devuelve su respuesta.
func (f *pgFake) take(kind, query string, args []driver.NamedValue, inTx bool) pgReply {
	f.mu.Lock()
	defer f.mu.Unlock()
	values := make([]driver.Value, 0, len(args))
	for _, a := range args {
		values = append(values, a.Value)
	}
	f.events = append(f.events, pgEvent{kind: kind, query: query, args: values, inTx: inTx})
	if len(f.replies) == 0 {
		return pgReply{}
	}
	r := f.replies[0]
	f.replies = f.replies[1:]
	return r
}

// pgRows son las filas que devuelve el driver. Tienen tantas columnas como su primera fila; sin
// filas no hay Scan que las cuente.
type pgRows struct {
	rows   [][]driver.Value
	endErr error
	next   int
}

func (r *pgRows) Columns() []string {
	if len(r.rows) == 0 {
		return nil
	}
	cols := make([]string, len(r.rows[0]))
	for i := range cols {
		cols[i] = "c" + strconv.Itoa(i)
	}
	return cols
}
func (r *pgRows) Close() error { return nil }
func (r *pgRows) Next(dest []driver.Value) error {
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

// pgOpenRows son unas filas cuyo cierre FALLA y a las que database/sql no cierra por su cuenta.
//
// Cuando el recorrido se acaba (io.EOF), Rows.Next cierra las filas él mismo y el Close del
// llamante ya las encuentra cerradas. El fallo llega POR EL CIERRE del llamante solo si al
// acabarse las filas el driver dice que queda otro conjunto de resultados
// (driver.RowsNextResultSet): entonces Next no cierra y el primer cierre es el del llamante.
type pgOpenRows struct {
	*pgRows
	closeErr error
}

func (r *pgOpenRows) HasNextResultSet() bool { return true }
func (r *pgOpenRows) NextResultSet() error   { return io.EOF }
func (r *pgOpenRows) Close() error           { return r.closeErr }

// pgConnector abre conexiones contra el pgFake. Cada conexión sabe si tiene una transacción
// abierta: database/sql ata la transacción a UNA conexión, así que una sentencia lanzada fuera
// de ella sale por otra conexión y queda apuntada con inTx=false.
type pgConnector struct{ db *pgFake }

func (c pgConnector) Connect(context.Context) (driver.Conn, error) { return &pgConn{db: c.db}, nil }
func (c pgConnector) Driver() driver.Driver                        { return pgDriver{} }

type pgDriver struct{}

func (pgDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("pgDriver: se abre por pgConnector")
}

type pgConn struct {
	db   *pgFake
	inTx bool
}

func (*pgConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("pgConn: sin sentencias preparadas")
}
func (*pgConn) Close() error { return nil }

// CheckNamedValue deja pasar todo argumento tal cual: el test mira el []string que mandó el
// adaptador, no lo que un driver de verdad haría con él.
func (*pgConn) CheckNamedValue(*driver.NamedValue) error { return nil }

func (c *pgConn) Begin() (driver.Tx, error) {
	c.db.mu.Lock()
	err := c.db.beginErr
	c.db.mu.Unlock()
	if err != nil {
		return nil, err
	}
	c.db.note(pgBegin)
	c.inTx = true
	return pgTx{conn: c}, nil
}

func (c *pgConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	r := c.db.take(pgExec, query, args, c.inTx)
	if r.err != nil {
		return nil, r.err
	}
	return driver.RowsAffected(1), nil
}

func (c *pgConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	r := c.db.take(pgQuery, query, args, c.inTx)
	if r.err != nil {
		return nil, r.err
	}
	rows := &pgRows{rows: r.rows, endErr: r.endErr}
	if r.closeErr != nil {
		return &pgOpenRows{pgRows: rows, closeErr: r.closeErr}, nil
	}
	return rows, nil
}

// pgTx es la transacción abierta en una conexión.
type pgTx struct{ conn *pgConn }

func (tx pgTx) Commit() error {
	tx.conn.inTx = false
	tx.conn.db.mu.Lock()
	err := tx.conn.db.commitErr
	tx.conn.db.mu.Unlock()
	if err != nil {
		return err
	}
	tx.conn.db.note(pgCommit)
	return nil
}

func (tx pgTx) Rollback() error {
	tx.conn.inTx = false
	tx.conn.db.note(pgRollback)
	tx.conn.db.mu.Lock()
	defer tx.conn.db.mu.Unlock()
	return tx.conn.db.rollbackErr
}
