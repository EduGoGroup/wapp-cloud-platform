package apipublica_test

// eventstelemetry_store_fakedb_test.go — el driver de database/sql de mentira de los tests de
// eventstelemetry_store.go: apunta cada sentencia que le llega (texto y argumentos, tal cual) y
// contesta lo que el test sembró. No interpreta SQL. Es el driver de
// internal/modulos/inferencia/degradation/fakedb_test.go, con los nombres prefijados porque este
// paquete de test lo comparten todas las áreas de la cara.

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"sync"
	"testing"
)

// telemetryStatement es una sentencia tal como llegó al driver.
type telemetryStatement struct {
	query string
	args  []driver.Value
}

// telemetryFakeDB es el estado del driver: lo que llegó y lo que contesta.
type telemetryFakeDB struct {
	mu       sync.Mutex
	stmts    []telemetryStatement
	err      error            // si no es nil, toda sentencia falla con él
	columns  []string         // las columnas de la respuesta a una consulta
	rows     [][]driver.Value // sus filas
	rowsErr  error            // si no es nil, el recorrido falla con él tras la última fila
	closeErr error            // si no es nil, las filas quedan abiertas al acabarse y su cierre falla con él
}

// openTelemetryFakeDB abre un *sql.DB sobre un telemetryFakeDB nuevo y lo cierra al acabar el
// test.
func openTelemetryFakeDB(t *testing.T) (*telemetryFakeDB, *sql.DB) {
	t.Helper()
	fake := &telemetryFakeDB{}
	db := sql.OpenDB(telemetryFakeConnector{db: fake})
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("cerrando el *sql.DB de mentira: %v", err)
		}
	})
	return fake, db
}

// answer siembra la respuesta a las consultas: las cinco columnas de la lectura y sus filas.
func (f *telemetryFakeDB) answer(rows ...[]driver.Value) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.columns, f.rows = []string{"id", "name", "event_kind", "payload", "created_at"}, rows
}

// statements devuelve una copia de lo que llegó al driver, en orden.
func (f *telemetryFakeDB) statements() []telemetryStatement {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]telemetryStatement(nil), f.stmts...)
}

// record apunta la sentencia y devuelve el error sembrado, si lo hay.
func (f *telemetryFakeDB) record(query string, named []driver.NamedValue) error {
	args := make([]driver.Value, len(named))
	for i, a := range named {
		args[i] = a.Value
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stmts = append(f.stmts, telemetryStatement{query: query, args: args})
	return f.err
}

type telemetryFakeConnector struct{ db *telemetryFakeDB }

func (c telemetryFakeConnector) Connect(context.Context) (driver.Conn, error) {
	return telemetryFakeConn(c), nil
}
func (c telemetryFakeConnector) Driver() driver.Driver { return telemetryFakeDriver{} }

type telemetryFakeDriver struct{}

func (telemetryFakeDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("telemetryFakeDriver: se abre por telemetryFakeConnector")
}

type telemetryFakeConn struct{ db *telemetryFakeDB }

func (telemetryFakeConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("telemetryFakeConn: sin sentencias preparadas")
}
func (telemetryFakeConn) Close() error { return nil }
func (telemetryFakeConn) Begin() (driver.Tx, error) {
	return nil, errors.New("telemetryFakeConn: sin transacciones")
}

func (c telemetryFakeConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if err := c.db.record(query, args); err != nil {
		return nil, err
	}
	c.db.mu.Lock()
	defer c.db.mu.Unlock()
	rows := &telemetryFakeRows{columns: c.db.columns, rows: c.db.rows, err: c.db.rowsErr}
	if c.db.closeErr != nil {
		return &telemetryFakeOpenRows{telemetryFakeRows: rows, closeErr: c.db.closeErr}, nil
	}
	return rows, nil
}

// telemetryFakeRows son las filas sembradas.
type telemetryFakeRows struct {
	columns []string
	rows    [][]driver.Value
	next    int
	err     error
}

func (r *telemetryFakeRows) Columns() []string { return r.columns }
func (r *telemetryFakeRows) Close() error      { return nil }
func (r *telemetryFakeRows) Next(dest []driver.Value) error {
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

// telemetryFakeOpenRows son unas filas cuyo cierre FALLA y a las que database/sql no cierra por
// su cuenta.
//
// El detalle es de database/sql (medido con go1.26.5 en el driver del que este se copia): cuando
// el recorrido se acaba (io.EOF) o falla, Rows.Next cierra las filas él mismo y el error de ese
// cierre sale por Rows.Err(); el Close posterior del llamante ya las encuentra cerradas y
// devuelve nil. El fallo llega POR EL CIERRE del llamante solo si al acabarse las filas el driver
// dice que queda otro conjunto de resultados (driver.RowsNextResultSet): entonces Next no cierra,
// Err() no trae nada y el primer cierre —el que puede fallar— es el del llamante.
type telemetryFakeOpenRows struct {
	*telemetryFakeRows
	closeErr error
}

func (r *telemetryFakeOpenRows) HasNextResultSet() bool { return true }
func (r *telemetryFakeOpenRows) NextResultSet() error   { return io.EOF }
func (r *telemetryFakeOpenRows) Close() error           { return r.closeErr }
