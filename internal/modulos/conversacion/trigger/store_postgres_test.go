package trigger_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
)

// Lo que este fichero NO prueba es lo que importa del adaptador —que su SQL hace contra
// public.flow_triggers lo que el puerto promete—: eso es triggerhelpertest.Contrato contra un
// Postgres de verdad (test/procesos/trigger_contrato_test.go). Aquí, con un driver de mentira,
// solo lo que no necesita base: qué sentencia sale, con qué argumentos, cómo se mapea una fila y
// cómo se envuelve cada fallo.

// Aserciones de compilación: PostgresStore cumple el puerto y su constructor recibe el *sql.DB y
// no devuelve error.
var (
	_ trigger.Store                        = (*trigger.PostgresStore)(nil)
	_ func(*sql.DB) *trigger.PostgresStore = trigger.NewPostgresStore
)

// Las cinco sentencias del adaptador, byte a byte (con su sangría): son las del fichero viejo.
const (
	insertSQL = `
		INSERT INTO public.flow_triggers
			(tenant_id, kind, keyword, match_type, flow_id, priority, enabled, message, session_id, event_kind)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING trigger_id
	`
	listSQL = `
		SELECT tenant_id, trigger_id, kind, keyword, match_type, flow_id, priority, enabled, message, session_id, event_kind
		FROM public.flow_triggers
		WHERE tenant_id = $1
		ORDER BY trigger_id
	`
	listByKindSQL = `
		SELECT tenant_id, trigger_id, kind, keyword, match_type, flow_id, priority, enabled, message, session_id, event_kind
		FROM public.flow_triggers
		WHERE tenant_id = $1 AND kind = $2 AND (session_id = $3 OR session_id IS NULL)
		ORDER BY trigger_id
	`
	getSQL = `
		SELECT tenant_id, trigger_id, kind, keyword, match_type, flow_id, priority, enabled, message, session_id, event_kind
		FROM public.flow_triggers
		WHERE tenant_id = $1 AND trigger_id = $2
	`
	deleteSQL = `
		DELETE FROM public.flow_triggers
		WHERE tenant_id = $1 AND trigger_id = $2
	`
)

// Una fila de flow_triggers con todo y otra con los cinco nullable a NULL, y las reglas que les
// corresponden.
var (
	fullRow   = []driver.Value{"tenant-1", "id-2", "event_start", "carrito", "contains", "flow", int64(5), true, "aviso", "session-x", "cart"}
	sparseRow = []driver.Value{"tenant-1", "id-1", "fallback", nil, "exact", nil, int64(0), false, nil, nil, nil}

	fullRule = trigger.Rule{
		TenantID: "tenant-1", TriggerID: "id-2", Kind: trigger.KindEventStart, Keyword: "carrito",
		MatchType: trigger.MatchContains, FlowID: "flow", Priority: 5, Enabled: true, Message: "aviso",
		SessionID: "session-x", EventKind: trigger.EventKindCart,
	}
	sparseRule = trigger.Rule{TenantID: "tenant-1", TriggerID: "id-1", Kind: trigger.KindFallback, MatchType: trigger.MatchExact}
)

// newPostgresStore monta el adaptador sobre el driver de mentira.
func newPostgresStore(t *testing.T) (*trigger.PostgresStore, *fakeDB) {
	t.Helper()
	fake := &fakeDB{}
	return trigger.NewPostgresStore(fake.open(t)), fake
}

// requireStatement afirma que al driver llegó UNA sentencia: de esa clase, con ese texto y esos
// argumentos.
func requireStatement(t *testing.T, fake *fakeDB, kind, query string, args ...driver.Value) {
	t.Helper()
	seen := fake.seen()
	if len(seen) != 1 {
		t.Fatalf("llegaron %d sentencias al driver, quería 1: %+v", len(seen), seen)
	}
	got := seen[0]
	if got.kind != kind {
		t.Errorf("la sentencia llegó como %q, quería %q", got.kind, kind)
	}
	if got.query != query {
		t.Errorf("SQL emitido:\n%q\nquería:\n%q", got.query, query)
	}
	if !slices.Equal(got.args, args) {
		t.Errorf("argumentos = %#v, quería %#v", got.args, args)
	}
}

// requireWrapped afirma que err empieza por prefix y envuelve cause (si cause no es nil).
func requireWrapped(t *testing.T, err, cause error, prefix string) {
	t.Helper()
	if err == nil {
		t.Fatalf("err = nil, quería un error con el prefijo %q", prefix)
	}
	if !strings.HasPrefix(err.Error(), prefix) {
		t.Errorf("err = %q, quería el prefijo %q", err, prefix)
	}
	if cause != nil && !errors.Is(err, cause) {
		t.Errorf("err = %q no envuelve la causa %q", err, cause)
	}
}

// TestNewPostgresStore_DoesNotQuery: construir el store no toca la base.
func TestNewPostgresStore_DoesNotQuery(t *testing.T) {
	store, fake := newPostgresStore(t)
	if store == nil {
		t.Fatal("NewPostgresStore devolvió nil")
	}
	if seen := fake.seen(); len(seen) != 0 {
		t.Errorf("construir el store emitió %d sentencias, quería 0: %+v", len(seen), seen)
	}
}

// TestPostgresStore_Insert_SendsEveryColumn: una consulta, la literal, con las diez columnas en
// su orden. El TriggerID del argumento NO viaja: el que vuelve es el del RETURNING.
func TestPostgresStore_Insert_SendsEveryColumn(t *testing.T) {
	store, fake := newPostgresStore(t)
	fake.script(reply{rows: [][]driver.Value{{"assigned-id"}}})
	in := fullRule
	in.TriggerID = "the-caller-id"
	out, err := store.Insert(context.Background(), in)
	if err != nil {
		t.Fatalf("Insert: error inesperado %v", err)
	}
	requireStatement(t, fake, eventQuery, insertSQL,
		"tenant-1", "event_start", "carrito", "contains", "flow", int64(5), true, "aviso", "session-x", "cart")
	want := fullRule
	want.TriggerID = "assigned-id"
	if out != want {
		t.Errorf("Insert devolvió %+v, quería el argumento con el trigger_id del RETURNING: %+v", out, want)
	}
}

// TestPostgresStore_Insert_EmptyOptionalsTravelAsNull: keyword, flow_id, message, session_id y
// event_kind vacíos viajan como NULL; kind y match_type vacíos viajan como texto vacío, y
// priority 0 y enabled false tal cual (no se dejan al DEFAULT de la tabla).
func TestPostgresStore_Insert_EmptyOptionalsTravelAsNull(t *testing.T) {
	store, fake := newPostgresStore(t)
	fake.script(reply{rows: [][]driver.Value{{"assigned-id"}}})
	if _, err := store.Insert(context.Background(), trigger.Rule{TenantID: "tenant-1"}); err != nil {
		t.Fatalf("Insert: error inesperado %v", err)
	}
	requireStatement(t, fake, eventQuery, insertSQL,
		"tenant-1", "", nil, "", nil, int64(0), false, nil, nil, nil)
}

// TestPostgresStore_Insert_Error: el fallo sale envuelto y la regla devuelta es la cero.
func TestPostgresStore_Insert_Error(t *testing.T) {
	boom := errors.New("base caída")
	store, fake := newPostgresStore(t)
	fake.script(reply{err: boom})
	out, err := store.Insert(context.Background(), fullRule)
	requireWrapped(t, err, boom, "trigger: insertar regla: ")
	if out != (trigger.Rule{}) {
		t.Errorf("Insert con error devolvió %+v, quería la regla cero", out)
	}
}

// TestPostgresStore_List_EmitsTheQueryAndMapsTheRows: la consulta literal con el tenant, y cada
// fila con sus once columnas en su sitio, NULL leído como "", en el orden en que las da la base
// (el adaptador no reordena: ordena el ORDER BY).
func TestPostgresStore_List_EmitsTheQueryAndMapsTheRows(t *testing.T) {
	store, fake := newPostgresStore(t)
	fake.script(reply{rows: [][]driver.Value{fullRow, sparseRow}})
	got, err := store.List(context.Background(), "tenant-1")
	if err != nil {
		t.Fatalf("List: error inesperado %v", err)
	}
	requireStatement(t, fake, eventQuery, listSQL, "tenant-1")
	if want := []trigger.Rule{fullRule, sparseRule}; !slices.Equal(got, want) {
		t.Errorf("List = %+v, quería %+v", got, want)
	}
}

// TestPostgresStore_ListByKind_EmitsTheQueryAndMapsTheRows: tenant, kind y sesión, en ese orden;
// la sesión vacía viaja como texto vacío (no casa ninguna fila: quedan las de session_id NULL).
func TestPostgresStore_ListByKind_EmitsTheQueryAndMapsTheRows(t *testing.T) {
	for _, session := range []string{"session-x", ""} {
		store, fake := newPostgresStore(t)
		fake.script(reply{rows: [][]driver.Value{sparseRow, fullRow}})
		got, err := store.ListByKind(context.Background(), "tenant-1", session, trigger.KindEventStart)
		if err != nil {
			t.Fatalf("ListByKind(sesión %q): error inesperado %v", session, err)
		}
		requireStatement(t, fake, eventQuery, listByKindSQL, "tenant-1", "event_start", session)
		if want := []trigger.Rule{sparseRule, fullRule}; !slices.Equal(got, want) {
			t.Errorf("ListByKind(sesión %q) = %+v, quería %+v", session, got, want)
		}
	}
}

// TestPostgresStore_Lists_NoRows_EmptyNotNil: sin filas, un slice vacío que no es nil.
func TestPostgresStore_Lists_NoRows_EmptyNotNil(t *testing.T) {
	store, _ := newPostgresStore(t)
	all, err := store.List(context.Background(), "tenant-1")
	if err != nil || all == nil || len(all) != 0 {
		t.Errorf("List sin filas = (%#v, %v), quería un slice vacío no nil y sin error", all, err)
	}
	byKind, err := store.ListByKind(context.Background(), "tenant-1", "", trigger.KindKeyword)
	if err != nil || byKind == nil || len(byKind) != 0 {
		t.Errorf("ListByKind sin filas = (%#v, %v), quería un slice vacío no nil y sin error", byKind, err)
	}
}

// TestPostgresStore_Lists_Errors: los tres fallos de un listado salen envueltos, cada uno con su
// prefijo byte a byte, y sin filas a medias. Solo el de la consulta distingue a List de ListByKind.
func TestPostgresStore_Lists_Errors(t *testing.T) {
	boom := errors.New("base caída")
	// Un kind NULL no se puede escanear a string: el error es de database/sql.
	bad := []driver.Value{"tenant-1", "id-3", nil, nil, "exact", nil, int64(0), false, nil, nil, nil}
	lists := []struct {
		name        string
		queryPrefix string
		call        func(*trigger.PostgresStore) ([]trigger.Rule, error)
	}{
		{"List", "trigger: listar reglas: ", func(s *trigger.PostgresStore) ([]trigger.Rule, error) {
			return s.List(context.Background(), "tenant-1")
		}},
		{"ListByKind", "trigger: listar reglas por kind: ", func(s *trigger.PostgresStore) ([]trigger.Rule, error) {
			return s.ListByKind(context.Background(), "tenant-1", "", trigger.KindKeyword)
		}},
	}
	for _, l := range lists {
		cases := []struct {
			name   string
			reply  reply
			prefix string
			cause  error
		}{
			{"query fails", reply{err: boom}, l.queryPrefix, boom},
			{"row does not scan", reply{rows: [][]driver.Value{fullRow, bad}}, "trigger: escanear regla: ", nil},
			{"iteration fails", reply{rows: [][]driver.Value{fullRow}, endErr: boom}, "trigger: iterar reglas: ", boom},
		}
		for _, c := range cases {
			t.Run(l.name+"/"+c.name, func(t *testing.T) {
				store, fake := newPostgresStore(t)
				fake.script(c.reply)
				got, err := l.call(store)
				requireWrapped(t, err, c.cause, c.prefix)
				if got != nil {
					t.Errorf("%s con error devolvió %+v, quería nil", l.name, got)
				}
			})
		}
	}
}

// TestPostgresStore_List_CloseFailureIsDiscarded es la deuda D-17, portada tal cual: si cerrar las
// filas falla, el listado devuelve las filas y ningún error.
func TestPostgresStore_List_CloseFailureIsDiscarded(t *testing.T) {
	store, fake := newPostgresStore(t)
	fake.script(reply{rows: [][]driver.Value{fullRow}, closeErr: errors.New("cierre roto")})
	got, err := store.List(context.Background(), "tenant-1")
	if err != nil {
		t.Fatalf("List con el cierre roto: err = %v, quería nil (D-17: se descarta)", err)
	}
	if want := []trigger.Rule{fullRule}; !slices.Equal(got, want) {
		t.Errorf("List = %+v, quería %+v", got, want)
	}
}

// TestPostgresStore_Get_EmitsTheQueryAndMapsTheRow: la consulta literal con tenant y trigger_id.
func TestPostgresStore_Get_EmitsTheQueryAndMapsTheRow(t *testing.T) {
	for _, c := range []struct {
		row  []driver.Value
		want trigger.Rule
	}{{fullRow, fullRule}, {sparseRow, sparseRule}} {
		store, fake := newPostgresStore(t)
		fake.script(reply{rows: [][]driver.Value{c.row}})
		got, err := store.Get(context.Background(), "tenant-1", c.want.TriggerID)
		if err != nil {
			t.Fatalf("Get: error inesperado %v", err)
		}
		requireStatement(t, fake, eventQuery, getSQL, "tenant-1", c.want.TriggerID)
		if got != c.want {
			t.Errorf("Get = %+v, quería %+v", got, c.want)
		}
	}
}

// TestPostgresStore_Get_Errors: sin fila es el centinela, SIN envolver y sin prefijo; cualquier
// otro fallo sale envuelto y no pasa por «no encontrada». En los dos, la regla cero.
func TestPostgresStore_Get_Errors(t *testing.T) {
	boom := errors.New("base caída")
	t.Run("no row", func(t *testing.T) {
		store, _ := newPostgresStore(t)
		got, err := store.Get(context.Background(), "tenant-1", "id-1")
		if err != trigger.ErrTriggerNotFound { //nolint:errorlint // se afirma que sale SIN envolver
			t.Errorf("err = %v, quería ErrTriggerNotFound tal cual", err)
		}
		if got != (trigger.Rule{}) {
			t.Errorf("Get sin fila devolvió %+v, quería la regla cero", got)
		}
	})
	t.Run("query fails", func(t *testing.T) {
		store, fake := newPostgresStore(t)
		fake.script(reply{err: boom})
		got, err := store.Get(context.Background(), "tenant-1", "id-1")
		requireWrapped(t, err, boom, "trigger: leer regla: ")
		if errors.Is(err, trigger.ErrTriggerNotFound) {
			t.Errorf("err = %q pasa por ErrTriggerNotFound; un fallo de la base no es «no existe»", err)
		}
		if got != (trigger.Rule{}) {
			t.Errorf("Get con error devolvió %+v, quería la regla cero", got)
		}
	})
}

// TestPostgresStore_Delete: un Exec, el literal, con tenant y trigger_id; con una fila afectada
// no hay error.
func TestPostgresStore_Delete(t *testing.T) {
	store, fake := newPostgresStore(t)
	fake.script(reply{affected: 1})
	if err := store.Delete(context.Background(), "tenant-1", "id-1"); err != nil {
		t.Fatalf("Delete: error inesperado %v", err)
	}
	requireStatement(t, fake, eventExec, deleteSQL, "tenant-1", "id-1")
}

// TestPostgresStore_Delete_Errors: ninguna fila afectada es el centinela sin envolver; el fallo
// de la sentencia y el de contar las filas salen envueltos, cada uno con su prefijo.
func TestPostgresStore_Delete_Errors(t *testing.T) {
	boom := errors.New("base caída")
	t.Run("no row affected", func(t *testing.T) {
		store, fake := newPostgresStore(t)
		fake.script(reply{affected: 0})
		if err := store.Delete(context.Background(), "tenant-1", "id-1"); err != trigger.ErrTriggerNotFound { //nolint:errorlint // se afirma que sale SIN envolver
			t.Errorf("err = %v, quería ErrTriggerNotFound tal cual", err)
		}
	})
	t.Run("statement fails", func(t *testing.T) {
		store, fake := newPostgresStore(t)
		fake.script(reply{err: boom})
		requireWrapped(t, store.Delete(context.Background(), "tenant-1", "id-1"), boom, "trigger: borrar regla: ")
	})
	t.Run("rows affected cannot be counted", func(t *testing.T) {
		store, fake := newPostgresStore(t)
		fake.script(reply{affected: 1, affectedErr: boom})
		requireWrapped(t, store.Delete(context.Background(), "tenant-1", "id-1"), boom, "trigger: filas afectadas: ")
	})
}
