package intakes

import (
	"database/sql/driver"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// Este fichero prueba la cabeza del adaptador (constructor y opciones) y lleva lo que comparten
// los postgres_<tema>_test.go: el montaje sobre el driver de mentira, las filas de ejemplo, el
// logger que captura y el cifrador de prueba.
//
// El texto BYTE A BYTE de cada sentencia lo afirma el test de su tema, contra constantes escritas
// aparte y sacadas del paquete viejo. Al final va el candado AST de la poda (R-07). El
// comportamiento del SQL contra un Postgres de verdad es de intakeshelpertest.Contrato en
// test/procesos.

// Identificadores de ejemplo. Los dos ids son UUID porque el adaptador rechaza lo que no lo es
// sin ir a la base.
const (
	pgTenant   = "tenant-1"
	pgIntakeID = "11111111-1111-4111-8111-111111111111"
	pgEventID  = "22222222-2222-4222-8222-222222222222"
	pgContact  = "contact-1"
	pgSession  = "session-1"
)

// Instantes de ejemplo.
var (
	pgCreated = time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	pgUpdated = time.Date(2026, 8, 2, 11, 0, 0, 0, time.UTC)
	pgAt      = time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
)

// errPgBoom es la causa con la que el driver de mentira hace fallar una sentencia.
var errPgBoom = errors.New("base caída")

// Keyring de prueba del cifrador del literal: una sola KEK de 32 bytes en base64.
const (
	pgKEKID    = "test-kek"
	pgKEKB64   = "IiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiI="
	pgIndexB64 = "RERERERERERERERERERERERERERERERERERERERERES="
)

// newPgCipher arma un cifrador de campo de verdad sobre el keyring de prueba.
func newPgCipher(t *testing.T) *crypto.FieldCipher {
	t.Helper()
	kp, err := crypto.NewEnvKeyProvider(crypto.KeyringConfig{
		KeyringB64: pgKEKID + ":" + pgKEKB64, CurrentID: pgKEKID, IndexB64: pgIndexB64,
	})
	if err != nil {
		t.Fatalf("KeyProvider de prueba: %v", err)
	}
	return crypto.NewFieldCipher(kp)
}

// pgBrokenKeys es un KeyProvider que no sabe envolver ni desenvolver: hace fallar al cifrador.
type pgBrokenKeys struct{ cause error }

func (b pgBrokenKeys) WrapDEK([]byte) ([]byte, string, error)   { return nil, "", b.cause }
func (b pgBrokenKeys) UnwrapDEK([]byte, string) ([]byte, error) { return nil, b.cause }
func (pgBrokenKeys) BlindIndex(string, string) string           { return "" }
func (pgBrokenKeys) CurrentKeyID() string                       { return pgKEKID }

// pgLogEntry es una línea que llegó al logger de retención.
type pgLogEntry struct {
	level, msg string
	args       []any
}

// pgLogSink es un logger.Logger que apunta lo que le llega.
type pgLogSink struct {
	mu      sync.Mutex
	entries []pgLogEntry
}

func (s *pgLogSink) add(level, msg string, args []any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, pgLogEntry{level: level, msg: msg, args: args})
}
func (s *pgLogSink) Debug(msg string, args ...any) { s.add("debug", msg, args) }
func (s *pgLogSink) Info(msg string, args ...any)  { s.add("info", msg, args) }
func (s *pgLogSink) Warn(msg string, args ...any)  { s.add("warn", msg, args) }
func (s *pgLogSink) Error(msg string, args ...any) { s.add("error", msg, args) }
func (s *pgLogSink) With(...any) logger.Logger     { return s }

// logged devuelve lo apuntado, en orden.
func (s *pgLogSink) logged() []pgLogEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]pgLogEntry(nil), s.entries...)
}

// newFakePostgres monta el adaptador sobre el driver de mentira.
func newFakePostgres(t *testing.T, opts ...PostgresOption) (*Postgres, *pgFake) {
	t.Helper()
	fake := &pgFake{}
	return NewPostgres(fake.open(t), opts...), fake
}

// pgIntakeRow es una fila de cabecera con sus once columnas, en el orden de la proyección:
// id, contacto, sesión, estado, total, creada, actualizada, nota y las tres fechas anulables.
func pgIntakeRow(status string, total float64) []driver.Value {
	return []driver.Value{pgIntakeID, pgContact, pgSession, status, total, pgCreated, pgUpdated, "sin cebolla", nil, nil, nil}
}

// pgIntake es la cabecera que sale de pgIntakeRow(status, total) cuando status ya es canónico.
func pgIntake(status string, total float64) Intake {
	return Intake{
		ID: pgIntakeID, ContactID: pgContact, SessionID: pgSession, Status: status, Total: total,
		CreatedAt: pgCreated, UpdatedAt: pgUpdated, CustomerNote: "sin cebolla",
	}
}

// pgOne es la respuesta de una consulta con una sola fila.
func pgOne(row ...driver.Value) pgReply { return pgReply{rows: [][]driver.Value{row}} }

// requirePgWrapped exige que err sea prefix seguido del texto de cause, byte a byte, y que cause
// siga alcanzable con errors.Is.
func requirePgWrapped(t *testing.T, err error, prefix string, cause error) {
	t.Helper()
	if err == nil {
		t.Fatalf("no hubo error, quería uno con el prefijo %q", prefix)
	}
	if want := prefix + cause.Error(); err.Error() != want {
		t.Errorf("error = %q, quería %q", err.Error(), want)
	}
	if !errors.Is(err, cause) {
		t.Errorf("el error %q no envuelve su causa (%v)", err, cause)
	}
}

// requirePgKinds exige la forma exacta de la conversación con la base.
func requirePgKinds(t *testing.T, fake *pgFake, want ...string) {
	t.Helper()
	if got := fake.kinds(); !slices.Equal(got, want) {
		t.Errorf("conversación con la base = %v, quería %v", got, want)
	}
}

// requirePgUntouched exige que no haya llegado NADA a la base.
func requirePgUntouched(t *testing.T, fake *pgFake) {
	t.Helper()
	if seen := fake.seen(); len(seen) != 0 {
		t.Errorf("llegaron %d eventos a la base, quería 0: %+v", len(seen), seen)
	}
}

// TestNewPostgres_DoesNotQuery: construir el store, con o sin opciones, no toca la base.
func TestNewPostgres_DoesNotQuery(t *testing.T) {
	var option PostgresOption = WithRetentionLog(&pgLogSink{})
	store, fake := newFakePostgres(t, WithLiteralCipher(newPgCipher(t)), option)
	if store == nil {
		t.Fatal("NewPostgres devolvió nil")
	}
	requirePgUntouched(t, fake)
}

// TestNewPostgres_WithoutOptions_WorksForPlainRevisions: sin cifrador el store funciona entero
// para lo que no lleva literal. Una revisión sin literal se escribe.
func TestNewPostgres_WithoutOptions_WorksForPlainRevisions(t *testing.T) {
	store, fake := newFakePostgres(t)
	fake.script(pgOne(int64(1), RevisionKindCart, []byte(`{"v":1,"total":10}`), nil, nil, pgCreated))
	rev, err := store.InsertRevision(t.Context(), Revision{
		IntakeID: pgIntakeID, Kind: RevisionKindCart, Payload: []byte(`{"v":1,"total":10}`),
	})
	if err != nil {
		t.Fatalf("InsertRevision sin literal y sin cifrador: error inesperado %v", err)
	}
	if rev.RevisionNo != 1 {
		t.Errorf("RevisionNo = %d, quería 1", rev.RevisionNo)
	}
}

// TestWithLiteralCipher_Nil_RefusesToWriteTheLiteralInClear: con nil (igual que sin la opción) una
// revisión CON literal no se escribe: error con su texto exacto y ninguna sentencia.
func TestWithLiteralCipher_Nil_RefusesToWriteTheLiteralInClear(t *testing.T) {
	store, fake := newFakePostgres(t, WithLiteralCipher(nil))
	_, err := store.InsertRevision(t.Context(), Revision{
		IntakeID: pgIntakeID, Kind: RevisionKindInterpreted,
		Payload: []byte(`{"v":1,"source_text":"dos empanadas"}`),
	})
	const want = "intakes: la revisión lleva literal del cliente y el store no tiene FieldCipher: no se escribe en claro"
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, quería %q", err, want)
	}
	requirePgUntouched(t, fake)
}

// TestWithLiteralCipher_SealsTheLiteralOutOfThePayload: con cifrador, el literal NO viaja en la
// columna payload: va sellado en las tres columnas del sobre, y se puede abrir con la misma llave.
func TestWithLiteralCipher_SealsTheLiteralOutOfThePayload(t *testing.T) {
	cipher := newPgCipher(t)
	store, fake := newFakePostgres(t, WithLiteralCipher(cipher))
	fake.script(pgOne(int64(3), RevisionKindInterpreted, []byte(`{"v":1}`), nil, nil, pgCreated))
	_, err := store.InsertRevision(t.Context(), Revision{
		IntakeID: pgIntakeID, Kind: RevisionKindInterpreted,
		Payload: []byte(`{"v":1,"source_text":"dos empanadas"}`),
	})
	if err != nil {
		t.Fatalf("InsertRevision: error inesperado %v", err)
	}
	stmts := fake.statements()
	if len(stmts) != 1 || len(stmts[0].args) != 8 {
		t.Fatalf("sentencias = %+v, quería una con 8 argumentos", stmts)
	}
	args := stmts[0].args
	payload, ok := args[2].([]byte)
	if !ok || strings.Contains(string(payload), "empanadas") {
		t.Errorf("el payload que va a la base lleva el literal en claro: %q", payload)
	}
	enc, _ := args[5].([]byte)
	dek, _ := args[6].([]byte)
	kekID, _ := args[7].(string)
	if kekID != pgKEKID {
		t.Errorf("literal_kek_id = %q, quería %q", kekID, pgKEKID)
	}
	plain, derr := cipher.Decrypt(enc, dek, kekID)
	if derr != nil {
		t.Fatalf("el sobre que fue a la base no se abre con la misma llave: %v", derr)
	}
	if !strings.Contains(plain, "dos empanadas") {
		t.Errorf("el sobre abierto = %q, quería el literal del cliente", plain)
	}
}

// TestWithRetentionLog_Nil_KeepsTheLogger: un nil no sustituye al logger anterior; la poda sigue
// saliendo por el que había.
func TestWithRetentionLog_Nil_KeepsTheLogger(t *testing.T) {
	sink := &pgLogSink{}
	store, fake := newFakePostgres(t, WithRetentionLog(sink), WithRetentionLog(nil))
	scriptExpiredRevisionRead(fake, pgAt)
	if _, err := store.Get(t.Context(), pgTenant, pgIntakeID); err != nil {
		t.Fatalf("Get: error inesperado %v", err)
	}
	if got := sink.logged(); len(got) != 1 || got[0].level != "info" {
		t.Errorf("el logger de retención recibió %+v, quería el evento de poda", got)
	}
}

// Las tres piezas del candado de la poda. En el diseño y en el paquete viejo se llamaban
// revisionsOf, ejecutarPoda y sellarPodada, y vivían en postgres.go; por E-11 las dos últimas
// nacieron aquí como runPrune y sealPruned, y la lectura se partió a postgres_revisions_read.go.
const (
	pruneLockFile   = "postgres_revisions_read.go"
	pruneLockReader = "revisionsOf"
	pruneLockPruner = "runPrune"
	pruneLockSealer = "sealPruned"
)

// calledName devuelve el identificador final de una llamada: `f(...)` → "f", `p.f(...)` → "f".
// El receptor no importa: mirarlo ataría el candado a que la variable se siga llamando `p`.
func calledName(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		return fn.Sel.Name
	}
	return ""
}

// discardsItsValue dice si el nodo tira el valor de una llamada a `name`: la llamada suelta como
// sentencia, o asignada entera al identificador en blanco.
func discardsItsValue(n ast.Node, name string) bool {
	switch stmt := n.(type) {
	case *ast.ExprStmt:
		call, ok := stmt.X.(*ast.CallExpr)
		return ok && calledName(call) == name
	case *ast.AssignStmt:
		if len(stmt.Rhs) != 1 {
			return false
		}
		call, ok := stmt.Rhs[0].(*ast.CallExpr)
		if !ok || calledName(call) != name {
			return false
		}
		for _, lhs := range stmt.Lhs {
			if id, ok := lhs.(*ast.Ident); !ok || id.Name != "_" {
				return false
			}
		}
		return true
	}
	return false
}

// TestPrune_TheSealedInstantIsNotDiscarded es el candado del cableado de la poda (R-07, R6.2.d).
// Era TestPoda_ElInstanteSelladoNoSeDescarta en el viejo. La poda del literal SELLA su instante
// y lo devuelve; la lectura que poda no puede decir «esta revisión nunca tuvo texto» de la que
// acaba de destruir. Por eso, dentro de revisionsOf, lo que devuelve runPrune no se descarta y
// sealPruned se llama. Un resultado descartado no da error y no lo caza ni el compilador ni vet:
// hay que preguntarle al AST.
//
// La regla es «no se descarta», no «se compone en una línea»: partirlo en `sealed := p.runPrune(…)`
// y `sealPruned(out, n, sealed)` es un refactor legítimo y sigue pasando.
//
// GUARDA ANTI-HUECO: un barrido que no encuentra nada pasa siempre. Si la función se renombra o
// se muda de fichero, el candado exige encontrar las tres piezas antes de dar veredicto.
func TestPrune_TheSealedInstantIsNotDiscarded(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, pruneLockFile, nil, 0)
	if err != nil {
		t.Fatalf("parseando %s: %v", pruneLockFile, err)
	}

	var reader *ast.FuncDecl
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == pruneLockReader {
			reader = fn
			break
		}
	}
	if reader == nil {
		t.Fatalf("no se encontró %s en %s: el candado estaría vigilando una pared", pruneLockReader, pruneLockFile)
	}

	var sawPruner, sawSealer bool
	var discarded token.Pos
	ast.Inspect(reader, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			switch calledName(call) {
			case pruneLockPruner:
				sawPruner = true
			case pruneLockSealer:
				sawSealer = true
			}
		}
		if discardsItsValue(n, pruneLockPruner) {
			discarded = n.Pos()
		}
		return true
	})

	// Control positivo ANTES del veredicto: sin las dos llamadas, el silencio de abajo no
	// significaría nada.
	if !sawPruner {
		t.Fatalf("%s no llama a %s: ¿se movió la poda de sitio?", pruneLockReader, pruneLockPruner)
	}
	if !sawSealer {
		t.Fatalf("%s no llama a %s: el instante de la poda no llega a la respuesta, "+
			"así que la lectura que poda vuelve a decir «nunca hubo texto»", pruneLockReader, pruneLockSealer)
	}
	if discarded.IsValid() {
		t.Fatalf("%s: el resultado de %s se descarta en %s. Ese valor es el "+
			"literal_pruned_at que la respuesta tiene que publicar en ESTA lectura, "+
			"porque la columna todavía es NULL cuando el cursor la lee",
			pruneLockReader, pruneLockPruner, fset.Position(discarded))
	}
}
