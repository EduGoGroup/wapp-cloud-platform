package trigger_test

// El driver de database/sql de mentira sobre el que corre store_postgres_test.go: apunta, en
// orden, cada sentencia que le llega con su texto y sus argumentos —ya convertidos como los
// vería un driver de verdad: un sql.NullString no válido llega como nil— y contesta lo que el
// test guioniza. No interpreta SQL. Sin Postgres.
//
// Es el de internal/modulos/solicitudes/tenantvars/postgres_fakedb_test.go sin transacciones (este
// adaptador no abre ninguna) y con dos cosas más: filas de cualquier ancho (once columnas las de
// una regla, una el RETURNING del Insert) y el resultado de un Exec (cuántas filas afectó, o el
// fallo de contarlas).

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

// event es una sentencia que llegó al driver.
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
	// endErr: si no es nil, el recorrido de las filas termina con él en vez de con io.EOF.
	endErr error
	// closeErr: si no es nil, las filas quedan abiertas al acabarse y su cierre falla con él.
	closeErr error
	// affected: las filas que dice haber afectado un Exec.
	affected int64
	// affectedErr: si no es nil, el Exec va bien pero contar sus filas afectadas falla con él.
	affectedErr error
}

// fakeDB es el guion y el registro del driver. Las respuestas se consumen en orden, una por
// sentencia; sin guion, una sentencia se contesta sin error, sin filas y sin filas afectadas.
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

// fakeRows son las filas que devuelve el driver. Su ancho es el de la primera fila.
type fakeRows struct {
	rows   [][]driver.Value
	endErr error
	next   int
}

func (r *fakeRows) Columns() []string {
	if len(r.rows) == 0 {
		return nil
	}
	return make([]string, len(r.rows[0]))
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

// fakeResult es el resultado de un Exec.
type fakeResult struct {
	affected    int64
	affectedErr error
}

func (fakeResult) LastInsertId() (int64, error) {
	return 0, errors.New("fakeResult: sin LastInsertId")
}
func (r fakeResult) RowsAffected() (int64, error) { return r.affected, r.affectedErr }

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
func (*fakeConn) Begin() (driver.Tx, error) {
	return nil, errors.New("fakeConn: este adaptador no abre transacciones")
}

func (c *fakeConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	r := c.db.take(eventExec, query, args)
	if r.err != nil {
		return nil, r.err
	}
	return fakeResult{affected: r.affected, affectedErr: r.affectedErr}, nil
}

func (c *fakeConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	r := c.db.take(eventQuery, query, args)
	if r.err != nil {
		return nil, r.err
	}
	return r.driverRows(), nil
}

// driverRows son las filas de la respuesta: las normales o, si su cierre tiene que fallar, las
// que database/sql no cierra por su cuenta.
func (r reply) driverRows() driver.Rows {
	rows := &fakeRows{rows: r.rows, endErr: r.endErr}
	if r.closeErr != nil {
		return &fakeOpenRows{fakeRows: rows, closeErr: r.closeErr}
	}
	return rows
}
