package runtime

// El driver de database/sql de mentira sobre el que corren los tests de los dos adaptadores
// Postgres del paquete (tenant_resolver.go y self_numbers.go): apunta cada sentencia que le llega
// (texto y argumentos, tal cual) y contesta lo que el test guioniza, en orden. Solo consultas: ni
// Exec ni transacciones, que estos adaptadores no usan (pedirlas es un error). Sin Postgres.

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"sync"
	"testing"
)

// pgStatement es una sentencia que llegó al driver.
type pgStatement struct {
	query string
	args  []driver.Value
}

// pgReply es lo que el driver contesta a UNA consulta.
type pgReply struct {
	// err: la consulta falla con él.
	err error
	// rows: las filas que contesta, en orden; nil es «sin filas».
	rows [][]driver.Value
	// iterErr: después de servir rows, recorrerlas falla con él en vez de terminar.
	iterErr error
}

// pgFake es el guion y el registro del driver. Las respuestas se consumen en orden; sin guion,
// una consulta se contesta sin error y sin filas.
type pgFake struct {
	mu         sync.Mutex
	statements []pgStatement
	replies    []pgReply
}

// open devuelve un *sql.DB sobre el driver de mentira, que se cierra al acabar el test.
func (f *pgFake) open(t *testing.T) *sql.DB {
	t.Helper()
	db := sql.OpenDB(pgConnector{fake: f})
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("cerrar el *sql.DB de mentira: %v", err)
		}
	})
	return db
}

// seen devuelve las sentencias que llegaron, en orden.
func (f *pgFake) seen() []pgStatement {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]pgStatement(nil), f.statements...)
}

// take apunta la sentencia y devuelve su respuesta.
func (f *pgFake) take(query string, args []driver.NamedValue) pgReply {
	f.mu.Lock()
	defer f.mu.Unlock()
	values := make([]driver.Value, 0, len(args))
	for _, a := range args {
		values = append(values, a.Value)
	}
	f.statements = append(f.statements, pgStatement{query: query, args: values})
	if len(f.replies) == 0 {
		return pgReply{}
	}
	r := f.replies[0]
	f.replies = f.replies[1:]
	return r
}

// pgRows es el resultado de una consulta: sus filas y, si el guion lo pide, el fallo al recorrerlas.
type pgRows struct {
	rows    [][]driver.Value
	iterErr error
	width   int
}

func (r *pgRows) Columns() []string { return make([]string, r.width) }
func (r *pgRows) Close() error      { return nil }
func (r *pgRows) Next(dest []driver.Value) error {
	if len(r.rows) == 0 {
		if r.iterErr != nil {
			return r.iterErr
		}
		return io.EOF
	}
	copy(dest, r.rows[0])
	r.rows = r.rows[1:]
	return nil
}

// pgConnector abre conexiones contra el pgFake; pgConn solo contesta QueryContext.
type pgConnector struct{ fake *pgFake }

func (c pgConnector) Connect(context.Context) (driver.Conn, error) { return pgConn(c), nil }
func (c pgConnector) Driver() driver.Driver                        { return pgDriver{} }

type pgDriver struct{}

func (pgDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("pgDriver: se abre por pgConnector")
}

type pgConn struct{ fake *pgFake }

func (pgConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("pgConn: sin sentencias preparadas")
}
func (pgConn) Close() error              { return nil }
func (pgConn) Begin() (driver.Tx, error) { return nil, errors.New("pgConn: sin transacciones") }

// QueryContext contesta la consulta con el guion. El ancho de las filas es el de la primera; sin
// filas, el que pide width (una consulta sin filas también tiene columnas).
func (c pgConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	r := c.fake.take(query, args)
	if r.err != nil {
		return nil, r.err
	}
	width := 1
	if len(r.rows) > 0 {
		width = len(r.rows[0])
	}
	return &pgRows{rows: r.rows, iterErr: r.iterErr, width: width}, nil
}

// requirePgStatements afirma cuántas sentencias llegaron al driver y las devuelve.
func requirePgStatements(t *testing.T, f *pgFake, want int) []pgStatement {
	t.Helper()
	got := f.seen()
	if len(got) != want {
		t.Fatalf("llegaron %d sentencias a la base, quería %d: %v", len(got), want, got)
	}
	return got
}
