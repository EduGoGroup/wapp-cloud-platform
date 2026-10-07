package degradation_test

// El driver de database/sql de mentira de los tests del adaptador Postgres: apunta cada
// sentencia que le llega (texto y argumentos, tal cual) y contesta lo que el test sembró. No
// interpreta SQL. Es el driver de internal/modulos/inferencia/tenantllm/fakedb_test.go más el
// fallo a mitad del recorrido (failAfterRows) y el del cierre de las filas (failOnClose).

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
	mu       sync.Mutex
	stmts    []statement
	err      error            // si no es nil, toda sentencia falla con él
	columns  []string         // las columnas de la respuesta a una consulta
	rows     [][]driver.Value // sus filas; ninguna ⇒ sql.ErrNoRows en QueryRow
	rowsErr  error            // si no es nil, el recorrido falla con él tras la última fila
	closeErr error            // si no es nil, las filas quedan abiertas al acabarse y su cierre falla con él
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

// answer siembra la respuesta a las consultas: sus columnas y sus filas.
func (f *fakeDB) answer(columns []string, rows ...[]driver.Value) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.columns, f.rows = columns, rows
}

// failAfterRows hace que el recorrido de las filas sembradas falle con err al acabarlas.
func (f *fakeDB) failAfterRows(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rowsErr = err
}

// failOnClose hace que el cierre de las filas falle con err. Ver fakeOpenRows: para que ese
// fallo llegue al llamante, las filas tienen que seguir abiertas al acabar el recorrido.
func (f *fakeDB) failOnClose(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closeErr = err
}

// fail hace que toda sentencia falle con err.
func (f *fakeDB) fail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

// statements devuelve una copia de lo que llegó al driver, en orden.
func (f *fakeDB) statements() []statement {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]statement(nil), f.stmts...)
}

// record apunta la sentencia y devuelve el error sembrado, si lo hay.
func (f *fakeDB) record(query string, named []driver.NamedValue) error {
	args := make([]driver.Value, len(named))
	for i, a := range named {
		args[i] = a.Value
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stmts = append(f.stmts, statement{query: query, args: args})
	return f.err
}

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
	if err := c.db.record(query, args); err != nil {
		return nil, err
	}
	return driver.RowsAffected(1), nil
}

func (c fakeConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if err := c.db.record(query, args); err != nil {
		return nil, err
	}
	c.db.mu.Lock()
	defer c.db.mu.Unlock()
	rows := &fakeRows{columns: c.db.columns, rows: c.db.rows, err: c.db.rowsErr}
	if c.db.closeErr != nil {
		return &fakeOpenRows{fakeRows: rows, closeErr: c.db.closeErr}, nil
	}
	return rows, nil
}

// fakeRows son las filas sembradas.
type fakeRows struct {
	columns []string
	rows    [][]driver.Value
	next    int
	err     error
}

func (r *fakeRows) Columns() []string { return r.columns }
func (r *fakeRows) Close() error      { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.next >= len(r.rows) {
		if r.err != nil {
			return r.err
		}
		return io.EOF
	}
	copy(dest, r.rows[r.next])
	r.next++
	return nil
}

// fakeOpenRows son unas filas cuyo cierre FALLA y a las que database/sql no cierra por su cuenta.
//
// El detalle es de database/sql (medido con go1.26.5): cuando el recorrido se acaba (io.EOF) o
// falla, Rows.Next cierra las filas él mismo y el error de ese cierre sale por Rows.Err() —o sea,
// como fallo «a mitad del recorrido»—; el Close posterior del llamante ya las encuentra cerradas y
// devuelve nil. El fallo llega POR EL CIERRE del llamante solo si al acabarse las filas el driver
// dice que queda otro conjunto de resultados (driver.RowsNextResultSet): entonces Next no cierra,
// Err() no trae nada y el primer cierre —el que puede fallar— es el del llamante.
type fakeOpenRows struct {
	*fakeRows
	closeErr error
}

func (r *fakeOpenRows) HasNextResultSet() bool { return true }
func (r *fakeOpenRows) NextResultSet() error   { return io.EOF }
func (r *fakeOpenRows) Close() error           { return r.closeErr }
