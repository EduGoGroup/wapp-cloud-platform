package degradation_test

// Los tests de fichero de degradation.Postgres, con un driver de database/sql de mentira
// (fakedb_test.go): el texto EXACTO de cada sentencia, sus argumentos (los instantes en UTC, el
// LastSeenAt cero, la página acotada), el mapeo de filas (`read_at` NULL) y los errores. Que ese
// SQL haga en un Postgres de verdad lo que el puerto promete —el dedupe por el índice único— lo
// prueba degradationhelpertest.Contrato en los procesos de F9.

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/degradation"
)

// El adaptador real cumple el puerto.
var _ degradation.Store = (*degradation.Postgres)(nil)

// Las dos sentencias, byte a byte, escritas aquí a mano y NO copiadas de una constante de
// producción: si alguien toca el SQL del adaptador, este fichero lo dice. Los espacios en blanco
// (el salto inicial, la tabulación de sangría, los espacios de alineación, el final sin salto)
// son los del literal del paquete viejo, internal/degradation/postgres.go @ ebf4eb7.
const (
	// Save: el dedupe. created_at y last_seen_at salen del mismo $6; el DO UPDATE solo toca
	// occurrences y last_seen_at.
	sqlSave = "\n" +
		"\tINSERT INTO public.owner_degradation_notices AS n\n" +
		"\t       (tenant_id, reason, via, window_start, window_end, occurrences, created_at, last_seen_at)\n" +
		"\tVALUES ($1, $2, $3, $4, $5, 1, $6, $6)\n" +
		"\tON CONFLICT (tenant_id, reason, via, window_start) DO UPDATE\n" +
		"\t   SET occurrences  = n.occurrences + 1,\n" +
		"\t       last_seen_at = GREATEST(n.last_seen_at, EXCLUDED.last_seen_at)\n" +
		"\tRETURNING n.id, n.occurrences, n.created_at, n.last_seen_at"
	// List: acotada al tenant, con el filtro «sin leer» como predicado y el orden con desempate.
	sqlList = "\n" +
		"\tSELECT id, tenant_id, reason, via, window_start, window_end,\n" +
		"\t       occurrences, read_at, created_at, last_seen_at\n" +
		"\t  FROM public.owner_degradation_notices\n" +
		"\t WHERE tenant_id = $1\n" +
		"\t   AND (NOT $2::boolean OR read_at IS NULL)\n" +
		"\t ORDER BY window_start DESC, created_at DESC, id\n" +
		"\t LIMIT $3 OFFSET $4"
)

const (
	pgTenant = "5e0b3a52-6a52-4a44-8b1c-2f0d6a3f9c11"
	pgID     = "0b9d4a1e-7c55-4c0e-9f43-1d2a3b4c5d6e"
	pgID2    = "7f3c2b1a-0e9d-4c8b-a7f6-5e4d3c2b1a09"
)

var (
	// Los instantes de entrada van en una zona que no es UTC para ver que el adaptador los normaliza.
	pgZone      = time.FixedZone("x", -3*3600)
	pgStart     = time.Date(2026, 8, 23, 7, 0, 0, 0, pgZone) // 10:00 UTC
	pgEnd       = pgStart.Add(15 * time.Minute)
	pgLastSeen  = pgStart.Add(4 * time.Minute)
	pgRead      = time.Date(2026, 8, 23, 11, 30, 0, 0, time.UTC)
	saveColumns = []string{"id", "occurrences", "created_at", "last_seen_at"}
	listColumns = []string{
		"id", "tenant_id", "reason", "via", "window_start", "window_end",
		"occurrences", "read_at", "created_at", "last_seen_at",
	}
)

// newPostgres monta el adaptador sobre el driver de mentira.
func newPostgres(t *testing.T) (*degradation.Postgres, *fakeDB) {
	t.Helper()
	fake, db := openFakeDB(t)
	return degradation.NewPostgres(db), fake
}

// pgNotice es el aviso que los tests escriben: trae rellenos también los campos que Save ignora.
func pgNotice() degradation.Notice {
	return degradation.Notice{
		ID: "lo-pone-la-base", TenantID: pgTenant, Reason: degradation.ReasonTimeout, Via: degradation.ViaAPI,
		WindowStart: pgStart, WindowEnd: pgEnd, Occurrences: 99,
		ReadAt: pgRead, CreatedAt: pgStart.Add(-time.Hour), LastSeenAt: pgLastSeen,
	}
}

// requireOnlyStatement afirma que al driver llegó UNA sentencia con ese texto exacto y esos
// argumentos (número, orden, tipo y valor). Un instante esperado tiene que llegar como el mismo
// instante y EN UTC.
func requireOnlyStatement(t *testing.T, fake *fakeDB, wantQuery string, wantArgs ...driver.Value) {
	t.Helper()
	stmts := fake.statements()
	if len(stmts) != 1 {
		t.Fatalf("llegaron %d sentencias al driver, quería 1: %+v", len(stmts), stmts)
	}
	if stmts[0].query != wantQuery {
		t.Errorf("SQL emitido:\n%q\nquería, byte a byte:\n%q", stmts[0].query, wantQuery)
	}
	args := stmts[0].args
	if len(args) != len(wantArgs) {
		t.Fatalf("llegaron %d argumentos, quería %d: %#v", len(args), len(wantArgs), args)
	}
	for i, want := range wantArgs {
		wantInstant, isInstant := want.(time.Time)
		if !isInstant {
			if !reflect.DeepEqual(args[i], want) {
				t.Errorf("argumento $%d = %#v, quería %#v", i+1, args[i], want)
			}
			continue
		}
		got, ok := args[i].(time.Time)
		if !ok || !got.Equal(wantInstant) || got.Location() != time.UTC {
			t.Errorf("argumento $%d = %#v, quería el instante %s en UTC", i+1, args[i], wantInstant.UTC())
		}
	}
}

// requireWrapped afirma que err envuelve la causa y empieza por el prefijo del adaptador.
func requireWrapped(t *testing.T, err, cause error, prefix string) {
	t.Helper()
	if !errors.Is(err, cause) {
		t.Fatalf("error = %v, quería uno que envuelva %v", err, cause)
	}
	if !strings.HasPrefix(err.Error(), prefix) {
		t.Errorf("error = %q, quería el prefijo %q", err, prefix)
	}
}

// TestNewPostgres_DoesNotTouchTheDatabase: construir no abre ni consulta nada.
func TestNewPostgres_DoesNotTouchTheDatabase(t *testing.T) {
	store, fake := newPostgres(t)
	if store == nil {
		t.Fatal("NewPostgres devolvió nil")
	}
	if n := len(fake.statements()); n != 0 {
		t.Errorf("construir el adaptador emitió %d sentencias, quería 0", n)
	}
}

// TestPostgresSave_Statement: la sentencia exacta y sus seis argumentos —los tres instantes en
// UTC, el motivo como cadena— y creado = (occurrences == 1). ID, Occurrences, ReadAt y CreatedAt
// del aviso de entrada no viajan.
func TestPostgresSave_Statement(t *testing.T) {
	cases := []struct {
		name        string
		occurrences int64
		wantCreated bool
	}{
		{"first failure of the window", 1, true},
		{"second failure collapses", 2, false},
		{"twenty fifth failure collapses", 25, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store, fake := newPostgres(t)
			fake.answer(saveColumns, []driver.Value{pgID, c.occurrences, pgStart.UTC(), pgLastSeen.UTC()})
			created, err := store.Save(context.Background(), pgNotice())
			if err != nil || created != c.wantCreated {
				t.Errorf("Save = (%v, %v), quería (%v, nil)", created, err, c.wantCreated)
			}
			requireOnlyStatement(t, fake, sqlSave, pgTenant, "timeout", "api", pgStart, pgEnd, pgLastSeen)
		})
	}
}

// TestPostgresSave_ZeroLastSeenAt_UsesWindowEnd: sin instante de último-visto, el $6 —del que
// salen created_at y last_seen_at— es el FIN DE LA VENTANA, no el reloj.
func TestPostgresSave_ZeroLastSeenAt_UsesWindowEnd(t *testing.T) {
	store, fake := newPostgres(t)
	fake.answer(saveColumns, []driver.Value{pgID, int64(1), pgEnd.UTC(), pgEnd.UTC()})
	notice := pgNotice()
	notice.LastSeenAt = time.Time{}
	if _, err := store.Save(context.Background(), notice); err != nil {
		t.Fatalf("Save: error inesperado %v", err)
	}
	requireOnlyStatement(t, fake, sqlSave, pgTenant, "timeout", "api", pgStart, pgEnd, pgEnd)
}

// TestPostgresSave_DoesNotValidateVocabulary: el puerto persiste; un motivo sano o una vía
// inventada llegan al driver tal cual (los custodia Notifier antes y el CHECK de la tabla
// después).
func TestPostgresSave_DoesNotValidateVocabulary(t *testing.T) {
	store, fake := newPostgres(t)
	fake.answer(saveColumns, []driver.Value{pgID, int64(1), pgStart.UTC(), pgLastSeen.UTC()})
	notice := pgNotice()
	notice.Reason, notice.Via = "fastlane", "edge"
	if created, err := store.Save(context.Background(), notice); err != nil || !created {
		t.Fatalf("Save = (%v, %v), quería (true, nil): el adaptador no valida el vocabulario", created, err)
	}
	requireOnlyStatement(t, fake, sqlSave, pgTenant, "fastlane", "edge", pgStart, pgEnd, pgLastSeen)
}

// TestPostgresSave_Failure_IsWrapped: un fallo del driver, o una sentencia que no devuelve fila
// (lo que pasaría con un `DO NOTHING`), vuelven envueltos con el tenant, el motivo y la vía, y
// sin decir «creado».
func TestPostgresSave_Failure_IsWrapped(t *testing.T) {
	const prefix = "degradation: escribir aviso de " + pgTenant + " (timeout/api): "
	cause := errors.New("conexión rota")

	store, fake := newPostgres(t)
	fake.fail(cause)
	created, err := store.Save(context.Background(), pgNotice())
	if created || err == nil || err.Error() != prefix+cause.Error() {
		t.Errorf("Save con el driver caído = (%v, %v), quería (false, %q)", created, err, prefix+cause.Error())
	}
	requireWrapped(t, err, cause, prefix)

	store, _ = newPostgres(t) // sin filas sembradas: el RETURNING no trae nada
	created, err = store.Save(context.Background(), pgNotice())
	if created {
		t.Error("Save sin fila de vuelta dijo haber creado el aviso")
	}
	requireWrapped(t, err, sql.ErrNoRows, prefix)
}

// TestPostgresList_BoundsThePage: la sentencia exacta y sus cuatro argumentos. La página se acota
// antes del SQL: límite <= 0 ⇒ 50; por encima de 200 ⇒ 200, en silencio; desplazamiento < 0 ⇒ 0.
func TestPostgresList_BoundsThePage(t *testing.T) {
	cases := []struct {
		name       string
		filter     degradation.ListFilter
		wantUnread bool
		wantLimit  int64
		wantOffset int64
	}{
		{"zero filter", degradation.ListFilter{}, false, 50, 0},
		{"only unread", degradation.ListFilter{SoloSinLeer: true}, true, 50, 0},
		{"negative limit", degradation.ListFilter{Limit: -3}, false, 50, 0},
		{"limit of one", degradation.ListFilter{Limit: 1}, false, 1, 0},
		{"limit just under the default", degradation.ListFilter{Limit: 49}, false, 49, 0},
		{"limit at the cap", degradation.ListFilter{Limit: 200}, false, 200, 0},
		{"limit just over the cap", degradation.ListFilter{Limit: 201}, false, 200, 0},
		{"huge limit", degradation.ListFilter{Limit: 5000}, false, 200, 0},
		{"negative offset", degradation.ListFilter{Offset: -1}, false, 50, 0},
		{"offset", degradation.ListFilter{Limit: 10, Offset: 7}, false, 10, 7},
		{"everything at once", degradation.ListFilter{SoloSinLeer: true, Limit: 999, Offset: -20}, true, 200, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store, fake := newPostgres(t)
			fake.answer(listColumns)
			if _, err := store.List(context.Background(), pgTenant, c.filter); err != nil {
				t.Fatalf("List: error inesperado %v", err)
			}
			requireOnlyStatement(t, fake, sqlList, pgTenant, c.wantUnread, c.wantLimit, c.wantOffset)
		})
	}
}

// TestPostgresList_MapsTheRows: cada columna a su campo, en el orden en que la base las da.
// `read_at` NULL llega como instante cero —sin leer—, y un motivo que este código no conoce se
// enseña igual, sin validar: una fila que la base admitió no desaparece de la lectura.
func TestPostgresList_MapsTheRows(t *testing.T) {
	start, end, seen := pgStart.UTC(), pgEnd.UTC(), pgLastSeen.UTC()
	store, fake := newPostgres(t)
	fake.answer(listColumns,
		[]driver.Value{pgID, pgTenant, "timeout", "api", start, end, int64(3), nil, start, seen},
		[]driver.Value{pgID2, pgTenant, "motivo_de_manana", "local", start.Add(-time.Hour), end.Add(-time.Hour), int64(1), pgRead, seen, seen},
	)
	got, err := store.List(context.Background(), pgTenant, degradation.ListFilter{})
	if err != nil {
		t.Fatalf("List: error inesperado %v", err)
	}
	want := []degradation.Notice{
		{
			ID: pgID, TenantID: pgTenant, Reason: degradation.ReasonTimeout, Via: degradation.ViaAPI,
			WindowStart: start, WindowEnd: end, Occurrences: 3, CreatedAt: start, LastSeenAt: seen,
		},
		{
			ID: pgID2, TenantID: pgTenant, Reason: "motivo_de_manana", Via: degradation.ViaLocal,
			WindowStart: start.Add(-time.Hour), WindowEnd: end.Add(-time.Hour), Occurrences: 1,
			ReadAt: pgRead, CreatedAt: seen, LastSeenAt: seen,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("List =\n%+v\nquería\n%+v", got, want)
	}
	if !got[0].ReadAt.IsZero() || got[0].Leida() {
		t.Errorf("read_at NULL llegó como %s (Leida=%v), quería el instante cero y sin leer", got[0].ReadAt, got[0].Leida())
	}
	if !got[1].Leida() {
		t.Error("un aviso con read_at puesto llegó sin leer")
	}
	if got[1].Reason.Valid() {
		t.Error("un motivo fuera del vocabulario se dio por válido al leerlo")
	}
}

// TestPostgresList_NoRows_EmptyNotNil: sin avisos devuelve una lista vacía y NO nil, para que
// quien la serialice produzca `[]` y no `null`.
func TestPostgresList_NoRows_EmptyNotNil(t *testing.T) {
	store, fake := newPostgres(t)
	fake.answer(listColumns)
	got, err := store.List(context.Background(), pgTenant, degradation.ListFilter{SoloSinLeer: true})
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("List sin filas = (%#v, %v), quería una lista vacía no nil y sin error", got, err)
	}
}

// TestPostgresList_Failure_IsWrapped: el fallo de la consulta, una fila ilegible y un fallo a
// mitad del recorrido vuelven envueltos, cada uno con su texto y el tenant, y sin lista: una
// lectura a medias no se entrega como si fuera entera.
func TestPostgresList_Failure_IsWrapped(t *testing.T) {
	cause := errors.New("conexión rota")
	start, end := pgStart.UTC(), pgEnd.UTC()
	good := []driver.Value{pgID, pgTenant, "timeout", "api", start, end, int64(1), nil, start, start}
	// window_start no admite NULL: una fila que lo trae no se puede leer.
	unreadable := []driver.Value{pgID2, pgTenant, "timeout", "api", nil, end, int64(1), nil, start, start}

	cases := []struct {
		name   string
		setup  func(fake *fakeDB)
		prefix string
		cause  error
	}{
		{"query failure", func(fake *fakeDB) { fake.fail(cause) }, "degradation: listar avisos de " + pgTenant + ": ", cause},
		{"unreadable row after a good one", func(fake *fakeDB) { fake.answer(listColumns, good, unreadable) }, "degradation: leer aviso de " + pgTenant + ": ", nil},
		{"failure in the middle of the rows", func(fake *fakeDB) {
			fake.answer(listColumns, good)
			fake.failAfterRows(cause)
		}, "degradation: recorrer avisos de " + pgTenant + ": ", cause},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store, fake := newPostgres(t)
			c.setup(fake)
			got, err := store.List(context.Background(), pgTenant, degradation.ListFilter{})
			if err == nil || got != nil {
				t.Fatalf("List = (%+v, %v), quería (nil, error)", got, err)
			}
			if !strings.HasPrefix(err.Error(), c.prefix) {
				t.Errorf("error = %q, quería el prefijo %q", err, c.prefix)
			}
			if c.cause != nil {
				requireWrapped(t, err, c.cause, c.prefix)
			}
		})
	}
}

// TestPostgresList_CloseFailure_IsWrapped: si el cierre de las filas falla y no hay otro error en
// curso, vuelve envuelto con su texto y el tenant, y sin lista aunque se hubieran leído filas: la
// lectura pudo quedarse a medias. Con otro error en curso —una fila ilegible— manda ese otro.
func TestPostgresList_CloseFailure_IsWrapped(t *testing.T) {
	const prefix = "degradation: cerrar filas de avisos de " + pgTenant + ": "
	cause := errors.New("cierre roto")
	start, end := pgStart.UTC(), pgEnd.UTC()
	good := []driver.Value{pgID, pgTenant, "timeout", "api", start, end, int64(1), nil, start, start}
	unreadable := []driver.Value{pgID2, pgTenant, "timeout", "api", nil, end, int64(1), nil, start, start}

	store, fake := newPostgres(t)
	fake.answer(listColumns, good)
	fake.failOnClose(cause)
	got, err := store.List(context.Background(), pgTenant, degradation.ListFilter{})
	if got != nil || err == nil || err.Error() != prefix+cause.Error() {
		t.Fatalf("List con el cierre roto = (%+v, %v), quería (nil, %q)", got, err, prefix+cause.Error())
	}
	requireWrapped(t, err, cause, prefix)

	store, fake = newPostgres(t)
	fake.answer(listColumns, good, unreadable)
	fake.failOnClose(cause)
	got, err = store.List(context.Background(), pgTenant, degradation.ListFilter{})
	if got != nil || err == nil || errors.Is(err, cause) ||
		!strings.HasPrefix(err.Error(), "degradation: leer aviso de "+pgTenant+": ") {
		t.Errorf("List con fila ilegible y cierre roto = (%+v, %v), quería (nil, el error de la fila)", got, err)
	}
}
