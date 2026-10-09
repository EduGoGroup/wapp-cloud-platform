package intentcfg_test

// El driver de database/sql de mentira sobre el que corre store_postgres_test.go: apunta, en
// orden, cada sentencia con su texto y sus argumentos tal cual, y contesta lo que el test
// guioniza. No interpreta SQL. Sin Postgres.
//
// Es el de internal/modulos/solicitudes/tenantvars/postgres_fakedb_test.go sin lo que este
// adaptador no usa: no hay transacciones (Get y Upsert son una sentencia suelta cada uno; abrir
// una hace fallar el test) ni fallo del cierre de las filas (Get usa QueryRow).

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"sync"
	"testing"
)

// Las clases de sentencia que apunta el driver.
const (
	eventExec  = "exec"
	eventQuery = "query"
)

// event es una sentencia que llegó al driver: su clase, su texto y sus argumentos.
type event struct {
	kind  string
	query string
	args  []driver.Value
}

// reply es lo que el driver contesta a UNA sentencia.
type reply struct {
	// err: la sentencia falla con él.
	err error
	// rows: las filas de una consulta, cada una con sus columnas.
	rows [][]driver.Value
}

// fakeDB es el guion y el registro del driver. Las respuestas se consumen en orden, una por
// sentencia; sin guion, una sentencia se contesta sin error y sin filas.
type fakeDB struct {
	mu      sync.Mutex
	events  []event
	replies []reply
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

// seen devuelve lo que llegó al driver, en orden.
func (f *fakeDB) seen() []event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]event(nil), f.events...)
}

// take apunta la sentencia y devuelve su respuesta.
func (f *fakeDB) take(kind, query string, args []driver.NamedValue) reply {
	f.mu.Lock()
	defer f.mu.Unlock()
	values := make([]driver.Value, 0, len(args))
	for _, a := range args {
		values = append(values, a.Value)
	}
	f.events = append(f.events, event{kind: kind, query: query, args: values})
	if len(f.replies) == 0 {
		return reply{}
	}
	r := f.replies[0]
	f.replies = f.replies[1:]
	return r
}

// fakeRows son las filas que devuelve el driver: tres columnas, las del SELECT de Get.
type fakeRows struct {
	rows [][]driver.Value
	next int
}

func (r *fakeRows) Columns() []string { return []string{"version", "config", "updated_at"} }
func (r *fakeRows) Close() error      { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.next >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.next])
	r.next++
	return nil
}

// fakeConnector abre conexiones contra el fakeDB.
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

// Begin falla: este adaptador no abre transacciones, y si un día lo hace su test tiene que decirlo.
func (*fakeConn) Begin() (driver.Tx, error) {
	return nil, errors.New("fakeConn: el adaptador no debe abrir transacciones")
}

func (c *fakeConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	r := c.db.take(eventExec, query, args)
	if r.err != nil {
		return nil, r.err
	}
	return driver.RowsAffected(1), nil
}

func (c *fakeConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	r := c.db.take(eventQuery, query, args)
	if r.err != nil {
		return nil, r.err
	}
	return &fakeRows{rows: r.rows}, nil
}
