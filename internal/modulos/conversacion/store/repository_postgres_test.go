package store

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
)

// Los tests del adaptador Postgres son INTERNOS (package store) y corren sobre el driver de
// mentira de repository_postgres_fakedb_test.go: afirman lo que no necesita una base —qué se
// valida antes de ir a ella, cuántas sentencias salen y si van en transacción, el mapeo de filas y
// de NULL, y el texto con que se envuelve cada fallo—. Que el SQL haga lo que promete lo prueba
// storehelpertest.Contrato contra un Postgres de verdad (test/procesos).

// Aserciones de compilación de lo que promete repository_postgres.go.
var (
	_ Repository             = (*PostgresRepository)(nil)
	_ WelcomeStore           = (*PostgresRepository)(nil)
	_ TenantContentVersioner = (*PostgresRepository)(nil)

	_ func(*PostgresRepository, context.Context, Key) (bool, error)                              = (*PostgresRepository).Exists
	_ func(*PostgresRepository, context.Context, Key) (model.Conversation, bool, error)          = (*PostgresRepository).Load
	_ func(*PostgresRepository, context.Context, model.Conversation) error                       = (*PostgresRepository).Save
	_ func(*PostgresRepository, context.Context, Key) error                                      = (*PostgresRepository).Delete
	_ func(*PostgresRepository, context.Context, string, string) (model.Flow, error)             = (*PostgresRepository).LatestDefinition
	_ func(*PostgresRepository, context.Context, string, string, int) (model.Flow, error)        = (*PostgresRepository).GetDefinition
	_ func(*PostgresRepository, context.Context, string, model.Flow) (int, error)                = (*PostgresRepository).InsertDefinition
	_ func(*PostgresRepository, context.Context, string) ([]FlowSummary, error)                  = (*PostgresRepository).ListDefinitions
	_ func(*PostgresRepository, context.Context, []SurveyResult) error                           = (*PostgresRepository).InsertResults
	_ func(*PostgresRepository, context.Context, string, string, string) ([]SurveyResult, error) = (*PostgresRepository).ListResults
	_ func(*PostgresRepository, context.Context, FlowEvent) error                                = (*PostgresRepository).InsertFlowEvent
)

const (
	pgTenant   = "11111111-1111-4111-8111-111111111111"
	pgContact  = "22222222-2222-4222-8222-222222222222"
	pgEventID  = "33333333-3333-4333-8333-333333333333"
	pgIntakeID = "44444444-4444-4444-8444-444444444444"
	// pgNull es como pgArg escribe un argumento que viajó como NULL.
	pgNull = "NULL"
)

var (
	// errPgBoom es la causa con la que el driver de mentira hace fallar una sentencia.
	errPgBoom = errors.New("base caída")
	// pgAt es un instante cualquiera que el driver devuelve en las columnas de fecha.
	pgAt  = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	pgKey = Key{TenantID: pgTenant, SessionID: "sess-1", ContactID: pgContact}
)

// pgHarness es el adaptador bajo prueba junto al driver de mentira sobre el que corre.
type pgHarness struct {
	repo *PostgresRepository
	fake *pgFake
}

// newFakeRepository devuelve el adaptador sobre el driver de mentira, con esas respuestas ya
// encoladas, y el driver.
func newFakeRepository(t *testing.T, replies ...pgReply) pgHarness {
	t.Helper()
	fake := &pgFake{}
	fake.script(replies...)
	return pgHarness{repo: NewPostgresRepository(fake.open(t)), fake: fake}
}

// pgOne es la respuesta de una consulta que devuelve UNA fila con esas columnas.
func pgOne(row ...driver.Value) pgReply { return pgReply{rows: [][]driver.Value{row}} }

// pgFails es la respuesta de una sentencia que falla con errPgBoom.
func pgFails() pgReply { return pgReply{err: errPgBoom} }

// requireEqual afirma que got es want.
func requireEqual[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, quería %v", what, got, want)
	}
}

// requireNoError afirma que la llamada no falló.
func requireNoError(t *testing.T, what string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

// requirePgWrapped exige que err sea prefix seguido del texto de cause, byte a byte, y que cause
// siga alcanzable con errors.Is.
func requirePgWrapped(t *testing.T, err error, prefix string, cause error) {
	t.Helper()
	if err == nil {
		t.Fatalf("no hubo error, quería uno con el prefijo %q", prefix)
	}
	requireEqual(t, "error", err.Error(), prefix+cause.Error())
	if !errors.Is(err, cause) {
		t.Errorf("el error %q no envuelve su causa (%v)", err, cause)
	}
}

// requireSentinel exige que err sea el centinela, con ese texto exacto.
func requireSentinel(t *testing.T, what string, err, sentinel error, text string) {
	t.Helper()
	if !errors.Is(err, sentinel) {
		t.Fatalf("%s: err = %v, quería %v", what, err, sentinel)
	}
	requireEqual(t, what+": texto", err.Error(), text)
}

// requireErrPrefix exige un error cuyo texto empiece por prefix.
func requireErrPrefix(t *testing.T, what string, err error, prefix string) {
	t.Helper()
	if err == nil || !strings.HasPrefix(err.Error(), prefix) {
		t.Errorf("%s: err = %v, quería un error que empezara por %q", what, err, prefix)
	}
}

// requirePgUntouched exige que a la base no haya llegado NADA.
func requirePgUntouched(t *testing.T, fake *pgFake) {
	t.Helper()
	if seen := fake.seen(); len(seen) != 0 {
		t.Errorf("llegaron %d eventos a la base, quería 0: %+v", len(seen), seen)
	}
}

// statements devuelve las n sentencias que llegaron a la base, tras exigir que sean n y que TODAS
// salieran como dice inTx: dentro de una transacción, o sueltas.
func statements(t *testing.T, fake *pgFake, n int, inTx bool) []pgEvent {
	t.Helper()
	stmts := fake.statements()
	if len(stmts) != n {
		t.Fatalf("%d sentencias, quería %d: %+v", len(stmts), n, stmts)
	}
	for i, s := range stmts {
		if s.inTx != inTx {
			t.Errorf("la sentencia %d salió con inTx=%v, quería %v", i, s.inTx, inTx)
		}
	}
	return stmts
}

// loose son las n sentencias que llegaron a la base, todas SUELTAS (fuera de una transacción).
func loose(t *testing.T, fake *pgFake, n int) []pgEvent {
	t.Helper()
	return statements(t, fake, n, false)
}

// pgArg escribe un argumento tal como viajó, para compararlo: pgNull si fue NULL (nil, o un valor
// anulable de database/sql vacío), el texto de un []byte, y el valor en los demás casos. No
// distingue un int de un int64 ni un 9 de un 9.0: lo que importa es qué viajó, no con qué tipo de Go.
func pgArg(t *testing.T, arg driver.Value) string {
	t.Helper()
	if valuer, ok := arg.(driver.Valuer); ok {
		v, err := valuer.Value()
		if err != nil {
			t.Fatalf("leer el argumento %T: %v", arg, err)
		}
		arg = v
	}
	switch v := arg.(type) {
	case nil:
		return pgNull
	case []byte:
		return string(v)
	default:
		return fmt.Sprint(v)
	}
}

// requireArgs exige que la sentencia llevara exactamente esos argumentos, en orden (ver pgArg). Un
// want nil es un NULL.
func requireArgs(t *testing.T, what string, stmt pgEvent, want ...any) {
	t.Helper()
	if len(stmt.args) != len(want) {
		t.Fatalf("%s: %d argumentos, quería %d: %v", what, len(stmt.args), len(want), stmt.args)
	}
	for i, w := range want {
		if got := pgArg(t, stmt.args[i]); got != pgArg(t, w) {
			t.Errorf("%s: el argumento %d viajó como %q, quería %q", what, i+1, got, pgArg(t, w))
		}
	}
}

// TestNewPostgresRepository_DoesNotTouchTheDatabase: construir el adaptador no lanza ninguna
// sentencia.
func TestNewPostgresRepository_DoesNotTouchTheDatabase(t *testing.T) {
	h := newFakeRepository(t)
	if h.repo == nil {
		t.Fatal("NewPostgresRepository devolvió nil")
	}
	requirePgUntouched(t, h.fake)
}

// wrappedCase es un método de UNA sentencia suelta y el prefijo con que envuelve su fallo.
type wrappedCase struct {
	name, prefix string
	call         func(*testing.T, *PostgresRepository) error
}

// runWrappedCases afirma, de cada caso, que el fallo de la base vuelve envuelto con su prefijo
// literal y que fue UNA sentencia suelta.
func runWrappedCases(t *testing.T, cases []wrappedCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newFakeRepository(t, pgFails())
			requirePgWrapped(t, tc.call(t, h.repo), tc.prefix, errPgBoom)
			loose(t, h.fake, 1)
		})
	}
}

// TestPostgres_BaseTopic_DatabaseFailure_Wrapped: todo fallo de la base vuelve envuelto con el
// prefijo literal de su método, en UNA sentencia suelta (ninguno de estos abre transacción).
func TestPostgres_BaseTopic_DatabaseFailure_Wrapped(t *testing.T) {
	runWrappedCases(t, []wrappedCase{
		{"Exists", "store: exists estado: ", func(t *testing.T, r *PostgresRepository) error {
			_, err := r.Exists(t.Context(), pgKey)
			return err
		}},
		{"Load", "store: leer estado: ", func(t *testing.T, r *PostgresRepository) error {
			_, _, err := r.Load(t.Context(), pgKey)
			return err
		}},
		{"Save", "store: upsert estado: ", func(t *testing.T, r *PostgresRepository) error {
			return r.Save(t.Context(), model.Conversation{TenantID: pgTenant})
		}},
		{"Delete", "store: borrar estado: ", func(t *testing.T, r *PostgresRepository) error {
			return r.Delete(t.Context(), pgKey)
		}},
		{"LatestDefinition", "store: leer definición: ", func(t *testing.T, r *PostgresRepository) error {
			_, err := r.LatestDefinition(t.Context(), pgTenant, "menu")
			return err
		}},
		{"GetDefinition", "store: leer definición por versión: ", func(t *testing.T, r *PostgresRepository) error {
			_, err := r.GetDefinition(t.Context(), pgTenant, "menu", 1)
			return err
		}},
		{"InsertDefinition", "store: insertar definición: ", func(t *testing.T, r *PostgresRepository) error {
			_, err := r.InsertDefinition(t.Context(), pgTenant, model.Flow{FlowID: "menu"})
			return err
		}},
		{"ListDefinitions", "store: listar definiciones: ", func(t *testing.T, r *PostgresRepository) error {
			_, err := r.ListDefinitions(t.Context(), pgTenant)
			return err
		}},
		{"InsertResults", "store: insertar resultados de encuesta: ", func(t *testing.T, r *PostgresRepository) error {
			return r.InsertResults(t.Context(), []SurveyResult{{TenantID: pgTenant}})
		}},
		{"ListResults", "store: listar resultados de encuesta: ", func(t *testing.T, r *PostgresRepository) error {
			_, err := r.ListResults(t.Context(), pgTenant, pgContact, "menu")
			return err
		}},
		{"InsertFlowEvent", "store: insertar efecto de flujo: ", func(t *testing.T, r *PostgresRepository) error {
			return r.InsertFlowEvent(t.Context(), FlowEvent{TenantID: pgTenant})
		}},
	})
}

// pgStateRow es una fila de flow_state con sus once columnas, en el orden de la consulta de Load.
func pgStateRow(vars string, lastWa, event, owner driver.Value) pgReply {
	return pgOne(pgTenant, "sess-1", pgContact, "menu", int64(3), "root", []byte(vars), lastWa, pgAt, event, owner)
}

// TestPostgres_Load_MapsTheRow: las once columnas salen por su campo, acotadas por la clave; las
// tres anulables (last_wa_message_id, event_id, owner_event_id) en NULL son la cadena vacía.
func TestPostgres_Load_MapsTheRow(t *testing.T) {
	h := newFakeRepository(t,
		pgStateRow(`{"nombre": "Ana"}`, "wamid.A", pgEventID, pgIntakeID), pgStateRow(`{}`, nil, nil, nil))

	got, found, err := h.repo.Load(t.Context(), pgKey)
	requireNoError(t, "Load", err)
	requireEqual(t, "found", found, true)
	requireEqual(t, "Vars[nombre]", got.Vars["nombre"], any("Ana"))
	got.Vars = nil
	requireEqual(t, "Load", fmt.Sprintf("%+v", got), fmt.Sprintf("%+v", model.Conversation{
		TenantID: pgTenant, SessionID: "sess-1", ContactID: pgContact, FlowID: "menu", FlowVersion: 3,
		CurrentNode: "root", LastWaMessageID: "wamid.A", EventID: pgEventID, OwnerEventID: pgIntakeID, UpdatedAt: pgAt,
	}))

	got, found, err = h.repo.Load(t.Context(), pgKey)
	requireNoError(t, "Load con NULL", err)
	requireEqual(t, "found con NULL", found, true)
	requireEqual(t, "las tres columnas anulables en NULL", got.LastWaMessageID+got.EventID+got.OwnerEventID, "")

	stmts := loose(t, h.fake, 2)
	requireArgs(t, "Load", stmts[0], pgTenant, "sess-1", pgContact)
}

// TestPostgres_Load_NoRowAndBrokenVars: sin fila es found=false sin error; unas vars ilegibles son
// un error con su texto.
func TestPostgres_Load_NoRowAndBrokenVars(t *testing.T) {
	h := newFakeRepository(t, pgReply{}, pgStateRow(`{rota`, nil, nil, nil))

	got, found, err := h.repo.Load(t.Context(), pgKey)
	requireNoError(t, "Load sin fila", err)
	requireEqual(t, "found sin fila", found, false)
	requireEqual(t, "tenant del estado sin fila", got.TenantID, "")

	_, found, err = h.repo.Load(t.Context(), pgKey)
	requireErrPrefix(t, "Load con vars ilegibles", err, "store: deserializar vars: ")
	requireEqual(t, "found con vars ilegibles", found, false)
	loose(t, h.fake, 2)
}

// TestPostgres_Save_WritesNullForTheEmptyPointers: una sentencia suelta con diez argumentos. Unas
// Vars nil viajan como `{}`, y las tres cadenas vacías (LastWaMessageID, EventID, OwnerEventID)
// como NULL: es lo que apaga un puntero.
func TestPostgres_Save_WritesNullForTheEmptyPointers(t *testing.T) {
	h := newFakeRepository(t, pgReply{}, pgReply{})
	state := model.Conversation{TenantID: pgTenant, SessionID: "sess-1", ContactID: pgContact,
		FlowID: "menu", FlowVersion: 3, CurrentNode: "root"}
	requireNoError(t, "Save", h.repo.Save(t.Context(), state))
	state.Vars = map[string]any{"n": 1}
	state.LastWaMessageID, state.EventID, state.OwnerEventID = "wamid.A", pgEventID, pgIntakeID
	requireNoError(t, "Save con todo", h.repo.Save(t.Context(), state))

	stmts := loose(t, h.fake, 2)
	requireArgs(t, "Save sin punteros", stmts[0],
		pgTenant, "sess-1", pgContact, "menu", 3, "root", `{}`, nil, nil, nil)
	requireArgs(t, "Save con todo", stmts[1],
		pgTenant, "sess-1", pgContact, "menu", 3, "root", `{"n":1}`, "wamid.A", pgEventID, pgIntakeID)
}

// TestPostgres_ExistsAndDelete: Exists devuelve el booleano de la consulta; Delete no es un error
// aunque no borre nada. Los dos van acotados por la clave entera.
func TestPostgres_ExistsAndDelete(t *testing.T) {
	h := newFakeRepository(t, pgOne(true), pgOne(false), pgReply{noneAffected: true})
	for _, want := range []bool{true, false} {
		got, err := h.repo.Exists(t.Context(), pgKey)
		requireNoError(t, "Exists", err)
		requireEqual(t, "Exists", got, want)
	}
	requireNoError(t, "Delete sin filas afectadas", h.repo.Delete(t.Context(), pgKey))
	for i, stmt := range loose(t, h.fake, 3) {
		requireArgs(t, fmt.Sprintf("sentencia %d", i), stmt, pgTenant, "sess-1", pgContact)
	}
}
