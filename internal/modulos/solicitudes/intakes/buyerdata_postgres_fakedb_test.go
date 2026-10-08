//go:build pendiente

package intakes

// El driver de database/sql de mentira sobre el que corre buyerdata_postgres_test.go: apunta, en
// orden, cada cosa que le llega —abrir, confirmar o revertir una transacción, y cada sentencia
// con su texto y sus argumentos tal cual— y contesta lo que el test guioniza. No interpreta SQL.
// Sin Postgres.
//
// Es el de internal/modulos/solicitudes/tenantvars/postgres_fakedb_test.go con las tres columnas
// del sobre de intake_buyer_data y una cosa más que este adaptador necesita: el fallo del
// ROLLBACK. Sus nombres llevan el prefijo bd para no chocar con el driver de mentira de los
// demás adaptadores de este paquete.

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
	bdEventBegin    = "begin"
	bdEventCommit   = "commit"
	bdEventRollback = "rollback"
	bdEventExec     = "exec"
	bdEventQuery    = "query"
)

// bdEvent es una cosa que llegó al driver.
type bdEvent struct {
	kind string
	// query y args: el texto y los argumentos de una sentencia (exec o query).
	query string
	args  []driver.Value
	// inTx: la sentencia llegó por una conexión con una transacción abierta.
	inTx bool
}

// bdReply es lo que el driver contesta a UNA sentencia.
type bdReply struct {
	// err: la sentencia falla con él.
	err error
	// rows: las filas de una consulta, cada una con sus tres columnas.
	rows [][]driver.Value
}

// bdFakeDB es el guion y el registro del driver. Las respuestas se consumen en orden, una por
// sentencia; sin guion, una sentencia se contesta sin error y sin filas.
type bdFakeDB struct {
	mu      sync.Mutex
	events  []bdEvent
	replies []bdReply
	// Si no son nil, abrir, confirmar o revertir una transacción falla con ellos.
	beginErr, commitErr, rollbackErr error
}

// open devuelve un *sql.DB sobre el driver de mentira, que se cierra al acabar el test.
func (f *bdFakeDB) open(t *testing.T) *sql.DB {
	t.Helper()
	db := sql.OpenDB(bdConnector{db: f})
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("cerrar el *sql.DB de mentira: %v", err)
		}
	})
	return db
}

// script encola las respuestas de las próximas sentencias.
func (f *bdFakeDB) script(replies ...bdReply) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replies = append(f.replies, replies...)
}

// seen devuelve lo que llegó al driver, en orden.
func (f *bdFakeDB) seen() []bdEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]bdEvent(nil), f.events...)
}

// kinds devuelve solo las clases de lo que llegó, en orden.
func (f *bdFakeDB) kinds() []string {
	events := f.seen()
	kinds := make([]string, 0, len(events))
	for _, e := range events {
		kinds = append(kinds, e.kind)
	}
	return kinds
}

// txErr devuelve el fallo guionizado para esa operación de transacción.
func (f *bdFakeDB) txErr(kind string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch kind {
	case bdEventBegin:
		return f.beginErr
	case bdEventCommit:
		return f.commitErr
	default:
		return f.rollbackErr
	}
}

// note apunta un evento de transacción.
func (f *bdFakeDB) note(kind string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, bdEvent{kind: kind})
}

// take apunta la sentencia y devuelve su respuesta.
func (f *bdFakeDB) take(kind, query string, args []driver.NamedValue, inTx bool) bdReply {
	f.mu.Lock()
	defer f.mu.Unlock()
	values := make([]driver.Value, 0, len(args))
	for _, a := range args {
		values = append(values, a.Value)
	}
	f.events = append(f.events, bdEvent{kind: kind, query: query, args: values, inTx: inTx})
	if len(f.replies) == 0 {
		return bdReply{}
	}
	r := f.replies[0]
	f.replies = f.replies[1:]
	return r
}

// bdRows son las filas que devuelve el driver: las tres columnas del sobre.
type bdRows struct {
	rows [][]driver.Value
	next int
}

func (r *bdRows) Columns() []string { return []string{"data_enc", "data_dek", "data_kek_id"} }
func (r *bdRows) Close() error      { return nil }
func (r *bdRows) Next(dest []driver.Value) error {
	if r.next >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.next])
	r.next++
	return nil
}

// bdConnector abre conexiones contra el bdFakeDB. Cada conexión sabe si tiene una transacción
// abierta: database/sql ata la transacción a UNA conexión, así que una sentencia lanzada fuera
// de ella sale por otra conexión y queda apuntada con inTx=false.
type bdConnector struct{ db *bdFakeDB }

func (c bdConnector) Connect(context.Context) (driver.Conn, error) { return &bdConn{db: c.db}, nil }
func (c bdConnector) Driver() driver.Driver                        { return bdDriver{} }

type bdDriver struct{}

func (bdDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("bdDriver: se abre por bdConnector")
}

type bdConn struct {
	db   *bdFakeDB
	inTx bool
}

func (*bdConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("bdConn: sin sentencias preparadas")
}
func (*bdConn) Close() error { return nil }

// CheckNamedValue deja pasar todo argumento tal cual: el test mira lo que mandó el adaptador,
// no lo que un driver de verdad haría con él.
func (*bdConn) CheckNamedValue(*driver.NamedValue) error { return nil }

func (c *bdConn) Begin() (driver.Tx, error) {
	if err := c.db.txErr(bdEventBegin); err != nil {
		return nil, err
	}
	c.db.note(bdEventBegin)
	c.inTx = true
	return bdTx{conn: c}, nil
}

func (c *bdConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	r := c.db.take(bdEventExec, query, args, c.inTx)
	if r.err != nil {
		return nil, r.err
	}
	return driver.RowsAffected(1), nil
}

func (c *bdConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	r := c.db.take(bdEventQuery, query, args, c.inTx)
	if r.err != nil {
		return nil, r.err
	}
	return &bdRows{rows: r.rows}, nil
}

// bdTx es la transacción abierta en una conexión. El intento de revertir se apunta SIEMPRE,
// falle o no: lo que el test quiere ver es que el adaptador lo pidió.
type bdTx struct{ conn *bdConn }

func (tx bdTx) Commit() error {
	tx.conn.inTx = false
	if err := tx.conn.db.txErr(bdEventCommit); err != nil {
		return err
	}
	tx.conn.db.note(bdEventCommit)
	return nil
}

func (tx bdTx) Rollback() error {
	tx.conn.inTx = false
	tx.conn.db.note(bdEventRollback)
	return tx.conn.db.txErr(bdEventRollback)
}
