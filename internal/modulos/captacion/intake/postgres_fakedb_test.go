package intake

// El driver de database/sql de mentira sobre el que corren postgres_test.go,
// machine_postgres_test.go y postgres_reanalysis_test.go: apunta, en orden, cada sentencia que
// le llega con su texto y sus argumentos tal cual, y contesta lo que el test guioniza. No
// interpreta SQL. Sin Postgres.
//
// Es el de internal/modulos/solicitudes/tenantvars/postgres_fakedb_test.go sin transacciones
// (este adaptador no abre ninguna: son sentencias únicas) y con dos cosas más: las filas
// afectadas de un Exec —de ellas sale el `(false, nil)` de toda la máquina— y el fallo al
// contarlas.

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
)

// Las clases de sentencia que apunta el driver.
const (
	fakeExec  = "exec"
	fakeQuery = "query"
)

// fakeStatement es una sentencia que llegó al driver.
type fakeStatement struct {
	kind  string
	query string
	args  []driver.Value
}

// fakeReply es lo que el driver contesta a UNA sentencia.
type fakeReply struct {
	// err: la sentencia falla con él.
	err error
	// affected: las filas que un Exec dice haber tocado.
	affected int64
	// affectedErr: si no es nil, contar las filas afectadas falla con él.
	affectedErr error
	// rows: las filas de una consulta, cada una con sus columnas.
	rows [][]driver.Value
	// endErr: si no es nil, el recorrido de las filas termina con él en vez de con io.EOF.
	endErr error
	// closeErr: si no es nil, las filas quedan abiertas al acabarse y su cierre falla con él.
	closeErr error
}

// fakeDB es el guion y el registro del driver. Las respuestas se consumen en orden, una por
// sentencia; sin guion, un Exec toca 0 filas y una consulta no devuelve ninguna.
type fakeDB struct {
	mu         sync.Mutex
	statements []fakeStatement
	replies    []fakeReply
}

// newFakePostgres monta el adaptador sobre el driver de mentira.
func newFakePostgres(t *testing.T) (*Postgres, *fakeDB) {
	t.Helper()
	fake := &fakeDB{}
	db := sql.OpenDB(fakeConnector{db: fake})
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("cerrar el *sql.DB de mentira: %v", err)
		}
	})
	return NewPostgres(db), fake
}

// script encola las respuestas de las próximas sentencias.
func (f *fakeDB) script(replies ...fakeReply) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replies = append(f.replies, replies...)
}

// seen devuelve las sentencias que llegaron al driver, en orden.
func (f *fakeDB) seen() []fakeStatement {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeStatement(nil), f.statements...)
}

// take apunta la sentencia y devuelve su respuesta.
func (f *fakeDB) take(kind, query string, args []driver.NamedValue) fakeReply {
	f.mu.Lock()
	defer f.mu.Unlock()
	values := make([]driver.Value, 0, len(args))
	for _, a := range args {
		values = append(values, a.Value)
	}
	f.statements = append(f.statements, fakeStatement{kind: kind, query: query, args: values})
	if len(f.replies) == 0 {
		return fakeReply{}
	}
	r := f.replies[0]
	f.replies = f.replies[1:]
	return r
}

// requireUntouched afirma que al driver no le llegó ninguna sentencia.
func (f *fakeDB) requireUntouched(t *testing.T) {
	t.Helper()
	if seen := f.seen(); len(seen) != 0 {
		t.Errorf("llegaron %d sentencias a la base, quería 0: %+v", len(seen), seen)
	}
}

// requireOnly afirma que al driver le llegó UNA sola sentencia, de esa clase, y la devuelve.
func (f *fakeDB) requireOnly(t *testing.T, kind string) fakeStatement {
	t.Helper()
	seen := f.seen()
	if len(seen) != 1 {
		t.Fatalf("llegaron %d sentencias a la base, quería exactamente 1: %+v", len(seen), seen)
	}
	if seen[0].kind != kind {
		t.Fatalf("la sentencia llegó como %q, quería %q", seen[0].kind, kind)
	}
	return seen[0]
}

// requireSQL afirma que el texto de la sentencia es want, byte a byte.
func requireSQL(t *testing.T, stmt fakeStatement, want string) {
	t.Helper()
	if stmt.query != want {
		t.Errorf("SQL emitido:\n%s\nquería:\n%s", stmt.query, want)
	}
}

// requireWrapped afirma que err envuelve cause (si no es nil) y empieza por prefix, byte a byte.
func requireWrapped(t *testing.T, err, cause error, prefix string) {
	t.Helper()
	if err == nil {
		t.Fatalf("err = nil, quería un error que empiece por %q", prefix)
	}
	if !strings.HasPrefix(err.Error(), prefix) {
		t.Errorf("err = %q, quería el prefijo %q", err, prefix)
	}
	if cause != nil && !errors.Is(err, cause) {
		t.Errorf("err = %q no envuelve la causa %q", err, cause)
	}
}

// errFakeBoom es el fallo de la base que guionizan los tests.
var errFakeBoom = errors.New("base caída")

// fakeResult es el resultado de un Exec.
type fakeResult struct {
	affected int64
	err      error
}

func (r fakeResult) LastInsertId() (int64, error) {
	return 0, errors.New("fakeResult: sin LastInsertId")
}
func (r fakeResult) RowsAffected() (int64, error) { return r.affected, r.err }

// fakeRows son las filas que devuelve el driver. Tiene tantas columnas como su primera fila.
type fakeRows struct {
	rows   [][]driver.Value
	endErr error
	next   int
}

func (r *fakeRows) Columns() []string {
	n := 1
	if len(r.rows) > 0 {
		n = len(r.rows[0])
	}
	return make([]string, n)
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

// fakeOpenRows son unas filas cuyo cierre FALLA y a las que database/sql no cierra por su
// cuenta: al acabarse dicen que queda otro conjunto de resultados, así que el primer cierre es
// el del llamante (ver el fakedb de tenantvars).
type fakeOpenRows struct {
	*fakeRows
	closeErr error
}

func (r *fakeOpenRows) HasNextResultSet() bool { return true }
func (r *fakeOpenRows) NextResultSet() error   { return io.EOF }
func (r *fakeOpenRows) Close() error           { return r.closeErr }

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

// CheckNamedValue deja pasar todo argumento tal cual: el test mira lo que mandó el adaptador
// (un []string, un time.Time, un int), no lo que un driver de verdad haría con él.
func (*fakeConn) CheckNamedValue(*driver.NamedValue) error { return nil }

func (c *fakeConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	r := c.db.take(fakeExec, query, args)
	if r.err != nil {
		return nil, r.err
	}
	return fakeResult{affected: r.affected, err: r.affectedErr}, nil
}

func (c *fakeConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	r := c.db.take(fakeQuery, query, args)
	if r.err != nil {
		return nil, r.err
	}
	rows := &fakeRows{rows: r.rows, endErr: r.endErr}
	if r.closeErr != nil {
		return &fakeOpenRows{fakeRows: rows, closeErr: r.closeErr}, nil
	}
	return rows, nil
}
