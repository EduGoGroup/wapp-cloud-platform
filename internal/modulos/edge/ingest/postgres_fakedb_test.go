package ingest

// El driver de database/sql de mentira sobre el que corre postgres_test.go: apunta cada sentencia
// que le llega (texto y argumentos, tal cual) y contesta como lo haría la tabla
// public.ingest_dedupe: el INSERT … ON CONFLICT DO NOTHING afecta una fila si la clave es nueva
// y ninguna si ya estaba. Sin Postgres.

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// Las dos clases de sentencia del adaptador.
const (
	kindInsert = "insert"
	kindSweep  = "sweep"
)

// statement es una sentencia que llegó al driver.
type statement struct {
	kind  string
	query string
	args  []driver.Value
}

// fakeDB es el estado y el registro del driver.
type fakeDB struct {
	mu         sync.Mutex
	keys       map[[2]string]bool
	statements []statement
	// insertErr: el INSERT falla con él (y no guarda la clave).
	insertErr error
	// affectedErr: el INSERT va bien pero leer sus filas afectadas falla con él.
	affectedErr error
	// sweepErr: el DELETE de la poda falla con él.
	sweepErr error
}

func newFakeDB() *fakeDB { return &fakeDB{keys: make(map[[2]string]bool)} }

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

// set cambia el estado del driver bajo su mutex.
func (f *fakeDB) set(change func(f *fakeDB)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(f)
}

// seen devuelve las sentencias que llegaron, en orden.
func (f *fakeDB) seen() []statement {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]statement(nil), f.statements...)
}

// kinds devuelve la clase de cada sentencia que llegó, en orden.
func (f *fakeDB) kinds() []string {
	seen := f.seen()
	out := make([]string, 0, len(seen))
	for _, st := range seen {
		out = append(out, st.kind)
	}
	return out
}

// sweeps devuelve solo las podas que llegaron, en orden.
func (f *fakeDB) sweeps() []statement {
	var out []statement
	for _, st := range f.seen() {
		if st.kind == kindSweep {
			out = append(out, st)
		}
	}
	return out
}

// exec apunta la sentencia y la contesta.
func (f *fakeDB) exec(query string, args []driver.NamedValue) (driver.Result, error) {
	values := make([]driver.Value, 0, len(args))
	for _, a := range args {
		values = append(values, a.Value)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case strings.Contains(query, "INSERT INTO public.ingest_dedupe"):
		f.statements = append(f.statements, statement{kind: kindInsert, query: query, args: values})
		if f.insertErr != nil {
			return nil, f.insertErr
		}
		if len(values) != 2 {
			return nil, fmt.Errorf("fakeDB: el INSERT trae %d argumentos, no 2", len(values))
		}
		session, okSession := values[0].(string)
		message, okMessage := values[1].(string)
		if !okSession || !okMessage {
			return nil, fmt.Errorf("fakeDB: el INSERT trae (%T, %T), no dos textos", values[0], values[1])
		}
		key := [2]string{session, message}
		affected := int64(1)
		if f.keys[key] {
			affected = 0
		}
		f.keys[key] = true
		return fakeResult{affected: affected, err: f.affectedErr}, nil
	case strings.Contains(query, "DELETE FROM public.ingest_dedupe"):
		f.statements = append(f.statements, statement{kind: kindSweep, query: query, args: values})
		if f.sweepErr != nil {
			return nil, f.sweepErr
		}
		return fakeResult{}, nil
	}
	return nil, fmt.Errorf("fakeDB: sentencia que no reconozco: %q", query)
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

// fakeConnector abre conexiones contra el fakeDB; fakeConn contesta ExecContext.
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
	return c.db.exec(query, args)
}
