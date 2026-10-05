package fleet

// El driver de database/sql de mentira sobre el que corren los cinco repository_postgres*_test.go:
// apunta cada sentencia que le llega (texto y argumentos, tal cual) y contesta lo que el test
// guioniza. Sin Postgres. Es la variante LOCAL de fleet (decisión de Jhoan en el inventario E-12
// de F3-02) y junta lo que ofrecen por separado los de receipts (guion por sentencia, endErr),
// diagnostics (filas afectadas programables) y entitlements (closeErr).
//
// Begin devuelve error A PROPÓSITO: el adaptador no abre transacciones (son once sentencias
// sueltas), así que un BeginTx que se cuele en el porte rompe el test que lo recorra.

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// statement es una sentencia que llegó al driver.
type statement struct {
	query string
	args  []driver.Value
}

// reply es lo que el driver contesta a UNA sentencia.
type reply struct {
	// err: la sentencia falla con él.
	err error
	// rows: las filas de una consulta, cada una con sus columnas. El número de columnas que el
	// driver declara es el de la primera fila.
	rows [][]driver.Value
	// endErr: si no es nil, la iteración de las filas termina con él en vez de con io.EOF.
	endErr error
	// closeErr: lo que devuelve el Close de las filas.
	closeErr error
	// affected: las filas que dice haber tocado un Exec.
	affected int64
	// affectedErr: el Exec va bien pero leer sus filas afectadas falla con él.
	affectedErr error
}

// fakeDB es el guion y el registro del driver. Las respuestas se consumen en orden; sin guion,
// una sentencia se contesta sin error, sin filas y con cero filas afectadas.
type fakeDB struct {
	mu         sync.Mutex
	statements []statement
	replies    []reply
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

// seen devuelve las sentencias que llegaron, en orden.
func (f *fakeDB) seen() []statement {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]statement(nil), f.statements...)
}

// take apunta la sentencia y devuelve su respuesta.
func (f *fakeDB) take(query string, args []driver.NamedValue) reply {
	f.mu.Lock()
	defer f.mu.Unlock()
	values := make([]driver.Value, 0, len(args))
	for _, a := range args {
		values = append(values, a.Value)
	}
	f.statements = append(f.statements, statement{query: query, args: values})
	if len(f.replies) == 0 {
		return reply{}
	}
	r := f.replies[0]
	f.replies = f.replies[1:]
	return r
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

// fakeRows son las filas que devuelve el driver.
type fakeRows struct {
	rows     [][]driver.Value
	endErr   error
	closeErr error
	next     int
}

func (r *fakeRows) Columns() []string {
	n := 1
	if len(r.rows) > 0 {
		n = len(r.rows[0])
	}
	return make([]string, n)
}
func (r *fakeRows) Close() error { return r.closeErr }
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

// fakeConnector abre conexiones contra el fakeDB; fakeConn contesta ExecContext y QueryContext.
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
	r := c.db.take(query, args)
	if r.err != nil {
		return nil, r.err
	}
	return fakeResult{affected: r.affected, err: r.affectedErr}, nil
}

func (c fakeConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	r := c.db.take(query, args)
	if r.err != nil {
		return nil, r.err
	}
	return &fakeRows{rows: r.rows, endErr: r.endErr, closeErr: r.closeErr}, nil
}

// ── El montaje común de los cinco tests ────────────────────────────────────────────────────────

const (
	pgTenant  = "5e0b3a52-6a52-4a44-8b1c-2f0d6a3f9c11"
	pgEdge    = "edge-pg-1"
	pgSession = "session-pg-1"
)

// errBoom es el fallo del driver que los tests inyectan.
var errBoom = errors.New("base caída")

// key32 devuelve una clave de 32 bytes rellena con b, en base64 estándar.
func key32(b byte) string {
	return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32))
}

// keyringKP es un KeyProvider con keyring versionado, la KEK current dada y una indexKey fija.
func keyringKP(t *testing.T, keyring, current string) crypto.KeyProvider {
	t.Helper()
	kp, err := crypto.NewEnvKeyProvider(crypto.KeyringConfig{KeyringB64: keyring, CurrentID: current, IndexB64: key32(0x44)})
	if err != nil {
		t.Fatalf("KeyProvider %s (current %s): %v", keyring, current, err)
	}
	return kp
}

// warning es una llamada a Logger.Warn.
type warning struct {
	msg  string
	args []any
}

// spyLogger apunta los avisos que el adaptador emite.
type spyLogger struct {
	mu       sync.Mutex
	warnings []warning
}

func (l *spyLogger) Warn(msg string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warnings = append(l.warnings, warning{msg: msg, args: args})
}

func (l *spyLogger) seen() []warning {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]warning(nil), l.warnings...)
}

// fixture es el adaptador montado sobre el driver de mentira, con lo que el test necesita para
// mirar por dentro: el guion, el stack de claves con el que se construyó y el logger espía.
//
// El keyring tiene DOS KEK, "A" (retirada) y "B" (current): así el key_id que viaja a la
// sentencia se distingue del de una fila vieja, y un sobre sellado con una tercera KEK no abre.
type fixture struct {
	repo   *PostgresRepository
	fake   *fakeDB
	kp     crypto.KeyProvider
	cipher *crypto.FieldCipher
	log    *spyLogger
}

// fixtureKeyring y fixtureCurrent son el keyring del montaje y su KEK current.
var fixtureKeyring = "A:" + key32(0x11) + ",B:" + key32(0x22)

const fixtureCurrent = "B"

// newFixture monta el adaptador con el guion dado.
func newFixture(t *testing.T, replies ...reply) fixture {
	t.Helper()
	fake := &fakeDB{}
	fake.script(replies...)
	kp := keyringKP(t, fixtureKeyring, fixtureCurrent)
	cipher := crypto.NewFieldCipher(kp)
	log := &spyLogger{}
	return fixture{
		repo:   NewPostgresRepository(fake.open(t), cipher, kp, WithLogger(log)),
		fake:   fake,
		kp:     kp,
		cipher: cipher,
		log:    log,
	}
}

// requireStatements afirma que al driver llegaron exactamente esas sentencias, en ese orden, con
// ese texto byte a byte y esos argumentos (en número, orden, tipo y valor).
func requireStatements(t *testing.T, fake *fakeDB, want ...statement) {
	t.Helper()
	got := fake.seen()
	if len(got) != len(want) {
		t.Fatalf("llegaron %d sentencias al driver, quería %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].query != want[i].query {
			t.Errorf("sentencia %d:\n%q\nquería, byte a byte:\n%q", i+1, got[i].query, want[i].query)
		}
		if !reflect.DeepEqual(got[i].args, want[i].args) {
			t.Errorf("sentencia %d: argumentos = %#v, quería %#v", i+1, got[i].args, want[i].args)
		}
	}
}

// requireWrapped afirma que err empieza por prefix y envuelve cause.
func requireWrapped(t *testing.T, err, cause error, prefix string) {
	t.Helper()
	if err == nil {
		t.Fatalf("err = nil, quería un error con el prefijo %q", prefix)
	}
	if !strings.HasPrefix(err.Error(), prefix) {
		t.Errorf("err = %q, quería el prefijo %q", err, prefix)
	}
	if !errors.Is(err, cause) {
		t.Errorf("err = %q no envuelve la causa %q", err, cause)
	}
}
