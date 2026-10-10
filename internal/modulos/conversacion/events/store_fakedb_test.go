//go:build pendiente

package events

// El driver de database/sql de mentira sobre el que corren los tests del adaptador (store_test.go,
// store_list_test.go, store_append_test.go y thread_reader_test.go): apunta, en orden, cada
// sentencia que le llega con su texto y sus argumentos tal cual, y contesta lo que el test
// guioniza. No interpreta SQL. Sin Postgres.
//
// Es el de internal/modulos/conversacion/store/repository_postgres_fakedb_test.go
// (copia-adaptación), sin transacciones: este adaptador no abre ninguna. Lleva la etiqueta
// `pendiente` mientras lo usen solo tests en rojo.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// Las clases de sentencia que apunta el driver.
const (
	pgExec  = "exec"
	pgQuery = "query"
)

// pgStatement es una sentencia que llegó al driver.
type pgStatement struct {
	kind  string
	query string
	args  []driver.Value
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
	// noneAffected: un Exec contesta que afectó a CERO filas (sin esto, a una).
	noneAffected bool
}

// pgFake es el guion y el registro del driver. Las respuestas se consumen en orden, una por
// sentencia; sin guion, una sentencia se contesta sin error y sin filas.
type pgFake struct {
	mu         sync.Mutex
	statements []pgStatement
	replies    []pgReply
}

// take apunta la sentencia y devuelve su respuesta.
func (f *pgFake) take(kind, query string, args []driver.NamedValue) pgReply {
	f.mu.Lock()
	defer f.mu.Unlock()
	values := make([]driver.Value, 0, len(args))
	for _, a := range args {
		values = append(values, a.Value)
	}
	f.statements = append(f.statements, pgStatement{kind: kind, query: query, args: values})
	if len(f.replies) == 0 {
		return pgReply{}
	}
	r := f.replies[0]
	f.replies = f.replies[1:]
	return r
}

// seen devuelve las sentencias que llegaron al driver, en orden.
func (f *pgFake) seen() []pgStatement {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]pgStatement(nil), f.statements...)
}

// pgRows son las filas que devuelve el driver, con tantas columnas como su primera fila.
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

// pgOpenRows son unas filas cuyo cierre FALLA y a las que database/sql no cierra por su cuenta:
// al acabarse dicen que queda otro conjunto de resultados, así que el primer cierre es el del
// llamante (el ritual de D-17).
type pgOpenRows struct {
	*pgRows
	closeErr error
}

func (r *pgOpenRows) HasNextResultSet() bool { return true }
func (r *pgOpenRows) NextResultSet() error   { return io.EOF }
func (r *pgOpenRows) Close() error           { return r.closeErr }

type pgConnector struct{ db *pgFake }

func (c pgConnector) Connect(context.Context) (driver.Conn, error) { return &pgConn{db: c.db}, nil }
func (c pgConnector) Driver() driver.Driver                        { return pgDriver{} }

type pgDriver struct{}

func (pgDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("pgDriver: se abre por pgConnector")
}

type pgConn struct{ db *pgFake }

func (*pgConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("pgConn: sin sentencias preparadas")
}
func (*pgConn) Close() error { return nil }
func (*pgConn) Begin() (driver.Tx, error) {
	return nil, errors.New("pgConn: este adaptador no abre transacciones")
}

// CheckNamedValue deja pasar todo argumento tal cual: el test mira lo que mandó el adaptador.
func (*pgConn) CheckNamedValue(*driver.NamedValue) error { return nil }

func (c *pgConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	r := c.db.take(pgExec, query, args)
	if r.err != nil {
		return nil, r.err
	}
	if r.noneAffected {
		return driver.RowsAffected(0), nil
	}
	return driver.RowsAffected(1), nil
}

func (c *pgConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	r := c.db.take(pgQuery, query, args)
	if r.err != nil {
		return nil, r.err
	}
	return r.driverRows(), nil
}

// driverRows son las filas de la respuesta: las normales o, si su cierre tiene que fallar, las que
// database/sql no cierra por su cuenta.
func (r pgReply) driverRows() driver.Rows {
	rows := &pgRows{rows: r.rows, endErr: r.endErr}
	if r.closeErr != nil {
		return &pgOpenRows{pgRows: rows, closeErr: r.closeErr}
	}
	return rows
}

// ── Lo que comparten los tests del adaptador ────────────────────────────────────────────────

// Los fallos que el guion le hace devolver al driver.
var (
	errPgBoom   = errors.New("boom de la base")
	errPgUnique = &pgconn.PgError{Code: "23505", Message: "duplicate key value violates unique constraint"}
)

// pgNow es el instante del reloj inyectado en los tests del adaptador, en una zona que NO es UTC:
// el adaptador tiene que mandarlo a la base en UTC.
var pgNow = time.Date(2026, 8, 9, 14, 30, 45, 0, time.FixedZone("-04", -4*60*60))

// Los identificadores de los tests del adaptador.
const (
	pgTenant  = "11111111-1111-4111-8111-111111111111"
	pgContact = "22222222-2222-4222-8222-222222222222"
	pgEvent   = "33333333-3333-4333-8333-333333333333"
	pgSession = "sess-1"
)

// pgHarness es un Store sobre el driver de mentira, con su guion.
type pgHarness struct {
	store *Store
	fake  *pgFake
}

// newFakeStore construye un Store CON cipher y con el reloj en pgNow sobre un driver que contesta
// replies, en orden.
func newFakeStore(t *testing.T, replies ...pgReply) pgHarness {
	t.Helper()
	return newFakeStoreWith(t, testCipher(t), replies...)
}

// newFakeStoreWith es newFakeStore con el cipher que se le dé (nil = sin cipher).
func newFakeStoreWith(t *testing.T, cipher *crypto.FieldCipher, replies ...pgReply) pgHarness {
	t.Helper()
	fake := &pgFake{replies: replies}
	db := sql.OpenDB(pgConnector{db: fake})
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("cerrar el *sql.DB de mentira: %v", err)
		}
	})
	return pgHarness{store: NewStore(db, cipher, WithClock(func() time.Time { return pgNow })), fake: fake}
}

// testCipher devuelve un FieldCipher con una KEK aleatoria de esta corrida. Es la KEK del
// envelope de PII de negocio, NO la DEK del ADR-0007.
func testCipher(t *testing.T) *crypto.FieldCipher {
	t.Helper()
	random := func() string {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			t.Fatalf("generar una clave de test: %v", err)
		}
		return base64.StdEncoding.EncodeToString(key)
	}
	kp, err := crypto.NewEnvKeyProvider(crypto.KeyringConfig{MasterB64: random(), IndexB64: random()})
	if err != nil {
		t.Fatalf("KeyProvider de test: %v", err)
	}
	return crypto.NewFieldCipher(kp)
}

// pgOne es una respuesta con una sola fila.
func pgOne(row ...driver.Value) pgReply { return pgReply{rows: [][]driver.Value{row}} }

// pgFails es una sentencia que falla.
func pgFails() pgReply { return pgReply{err: errPgBoom} }

// pgEventRow es una fila de conversation_events con las doce columnas que el adaptador lee, en
// su orden. closedAt nil es NULL.
func pgEventRow(id, kind string, status Status, closedAt driver.Value) []driver.Value {
	return []driver.Value{id, pgTenant, pgSession, pgContact, kind, kind + "-2026-08-09-1800", string(status),
		"flujo", int64(4), pgBorn, pgTouched, closedAt}
}

// Los dos instantes de una fila de evento de mentira.
var (
	pgBorn    = time.Date(2026, 8, 9, 18, 0, 0, 0, time.UTC)
	pgTouched = time.Date(2026, 8, 9, 18, 20, 0, 0, time.UTC)
)

// pgEventOf es lo que pgEventRow da una vez leído.
func pgEventOf(id, kind string, status Status, closedAt time.Time) Event {
	return Event{ID: id, TenantID: pgTenant, SessionID: pgSession, ContactID: pgContact, Kind: kind,
		HistoryID: kind + "-2026-08-09-1800", Status: status, FlowID: "flujo", FlowVersion: 4,
		CreatedAt: pgBorn, LastActivityAt: pgTouched, ClosedAt: closedAt}
}

// requireStatements exige que al driver hayan llegado exactamente n sentencias y las devuelve.
func requireStatements(t *testing.T, fake *pgFake, n int) []pgStatement {
	t.Helper()
	seen := fake.seen()
	if len(seen) != n {
		t.Fatalf("llegaron %d sentencias a la base, quería %d: %+v", len(seen), n, seen)
	}
	return seen
}

// requireArgs exige los argumentos de una sentencia, en orden. Un instante se compara con Equal y
// tiene que ir en UTC.
func requireArgs(t *testing.T, what string, stmt pgStatement, want ...any) {
	t.Helper()
	if len(stmt.args) != len(want) {
		t.Fatalf("%s: %d argumentos (%v), quería %d (%v)", what, len(stmt.args), stmt.args, len(want), want)
	}
	for i, w := range want {
		got := stmt.args[i]
		if wt, ok := w.(time.Time); ok {
			gt, isTime := got.(time.Time)
			if !isTime || !gt.Equal(wt) || gt.Location() != time.UTC {
				t.Errorf("%s: argumento $%d = %v, quería el instante %v en UTC", what, i+1, got, wt.UTC())
			}
			continue
		}
		if !sameValue(got, w) {
			t.Errorf("%s: argumento $%d = %#v, quería %#v", what, i+1, got, w)
		}
	}
}

// sameValue compara un argumento con el esperado: nil con nil, y lo demás por su texto (un Status
// o un Role viajan como su cadena; un []string y un []byte, por su contenido).
func sameValue(got driver.Value, want any) bool {
	if got == nil || want == nil {
		return got == nil && want == nil
	}
	return argText(got) == argText(want)
}

func argText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	case []string:
		return "[" + strings.Join(x, ",") + "]"
	case Status:
		return string(x)
	case Role:
		return string(x)
	case EntryKind:
		return string(x)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case bool:
		return strconv.FormatBool(x)
	default:
		return "?"
	}
}

// exactly dice si err es el centinela A SECAS: el mismo error, sin envolver ni añadirle texto.
func exactly(err, sentinel error) bool {
	return errors.Is(err, sentinel) && err.Error() == sentinel.Error()
}

// requireWrapped exige que err envuelva cause y empiece por el prefijo literal del contrato.
func requireWrapped(t *testing.T, what string, err error, prefix string, cause error) {
	t.Helper()
	if err == nil || !errors.Is(err, cause) || !strings.HasPrefix(err.Error(), prefix) {
		t.Errorf("%s: err = %v; quería %q envolviendo %v", what, err, prefix, cause)
	}
}

// requireSameEvent compara dos eventos campo a campo (los instantes, con Equal).
func requireSameEvent(t *testing.T, what string, got, want Event) {
	t.Helper()
	same := got.ID == want.ID && got.TenantID == want.TenantID && got.SessionID == want.SessionID &&
		got.ContactID == want.ContactID && got.Kind == want.Kind && got.HistoryID == want.HistoryID &&
		got.Status == want.Status && got.FlowID == want.FlowID && got.FlowVersion == want.FlowVersion &&
		got.CreatedAt.Equal(want.CreatedAt) && got.LastActivityAt.Equal(want.LastActivityAt) &&
		got.ClosedAt.Equal(want.ClosedAt)
	if !same {
		t.Errorf("%s = %+v, quería %+v", what, got, want)
	}
}
