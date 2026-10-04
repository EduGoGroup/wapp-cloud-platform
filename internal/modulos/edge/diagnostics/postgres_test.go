package diagnostics

import (
	"context"
	"database/sql/driver"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// Las siete sentencias del adaptador, byte a byte (con su sangría): son las del fichero viejo.
const (
	consentSQL = `
		SELECT enabled
		FROM public.tenant_diagnostics_consent
		WHERE tenant_id = $1
	`
	purgeSQL = `
		DELETE FROM public.diagnostics_bundles WHERE expires_at < now()
	`
	insertSQL = `
		INSERT INTO public.diagnostics_bundles
			(command_id, tenant_id, session_id, requested_by, requested_at, expires_at, status)
		VALUES ($1, $2, $3, $4, now(), $5, 'pending')
	`
	deleteSQL = `
		DELETE FROM public.diagnostics_bundles WHERE tenant_id = $1 AND command_id = $2
	`
	saveSQL = `
		UPDATE public.diagnostics_bundles
		SET status = 'ready',
		    received_at = now(),
		    log_tail = $1,
		    goroutine_dump = $2,
		    subsystems_json = $3
		WHERE command_id = $4
		  AND tenant_id = $5
		  AND session_id = $6
		  AND status = 'pending'
		  AND expires_at > now()
	`
	selectSQL = `
		SELECT session_id, requested_by, requested_at, expires_at, status,
		       received_at, log_tail, goroutine_dump, subsystems_json
		FROM public.diagnostics_bundles
		WHERE tenant_id = $1 AND command_id = $2
	`
	lazyDeleteSQL = `
			DELETE FROM public.diagnostics_bundles WHERE tenant_id = $1 AND command_id = $2
		`
)

var errBoom = errors.New("base caída")

// newPostgres monta el adaptador sobre el driver de mentira, con el guion dado.
func newPostgres(t *testing.T, replies ...reply) (*Postgres, *fakeDB) {
	t.Helper()
	fake := &fakeDB{}
	fake.script(replies...)
	return NewPostgres(fake.open(t)), fake
}

// requireStatements afirma que al driver llegaron exactamente esas sentencias, en ese orden.
func requireStatements(t *testing.T, fake *fakeDB, want ...statement) {
	t.Helper()
	got := fake.seen()
	if len(got) != len(want) {
		t.Fatalf("llegaron %d sentencias al driver, quería %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].query != want[i].query {
			t.Errorf("sentencia %d:\n%q\nquería:\n%q", i+1, got[i].query, want[i].query)
		}
		if !slices.Equal(got[i].args, want[i].args) {
			t.Errorf("sentencia %d: argumentos = %v, quería %v", i+1, got[i].args, want[i].args)
		}
	}
}

// requireWrapped afirma que err empieza por prefix y envuelve errBoom.
func requireWrapped(t *testing.T, err error, prefix string) {
	t.Helper()
	if err == nil {
		t.Fatalf("err = nil, quería un error con el prefijo %q", prefix)
	}
	if !strings.HasPrefix(err.Error(), prefix) {
		t.Errorf("err = %q, quería el prefijo %q", err, prefix)
	}
	if !errors.Is(err, errBoom) {
		t.Errorf("err = %q no envuelve la causa %q", err, errBoom)
	}
}

// TestNewPostgres_DoesNotQuery: construir el store no toca la base.
func TestNewPostgres_DoesNotQuery(t *testing.T) {
	store, fake := newPostgres(t)
	if store == nil {
		t.Fatal("NewPostgres devolvió nil")
	}
	requireStatements(t, fake)
}

// TestPostgres_ConsentEnabled: sin fila, consentido; con fila, lo que diga; y un fallo de la
// base no abre la capacidad.
func TestPostgres_ConsentEnabled(t *testing.T) {
	cases := []struct {
		name  string
		reply reply
		want  bool
	}{
		{"no row is consent", reply{}, true},
		{"row enabled", reply{row: []driver.Value{true}}, true},
		{"row disabled is opt-out", reply{row: []driver.Value{false}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store, fake := newPostgres(t, c.reply)
			got, err := store.ConsentEnabled(context.Background(), "tenant-1")
			if err != nil {
				t.Fatalf("ConsentEnabled: error inesperado %v", err)
			}
			if got != c.want {
				t.Errorf("ConsentEnabled = %v, quería %v", got, c.want)
			}
			requireStatements(t, fake, statement{consentSQL, []driver.Value{"tenant-1"}})
		})
	}
	t.Run("infra failure is false and wrapped", func(t *testing.T) {
		store, _ := newPostgres(t, reply{err: errBoom})
		got, err := store.ConsentEnabled(context.Background(), "tenant-1")
		requireWrapped(t, err, "diagnostics: leer consentimiento: ")
		if got {
			t.Error("ConsentEnabled con la base caída devolvió true: un error no consiente")
		}
	})
}

// TestPostgres_CreateRequest_PurgesThenInserts: dos sentencias, en ese orden y sin transacción;
// la purga no lleva argumentos y el INSERT lleva los cinco en su sitio.
func TestPostgres_CreateRequest_PurgesThenInserts(t *testing.T) {
	store, fake := newPostgres(t)
	expiresAt := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	err := store.CreateRequest(context.Background(), "tenant-1", "session-1", "cmd-1", "user-1", expiresAt)
	if err != nil {
		t.Fatalf("CreateRequest: error inesperado %v", err)
	}
	requireStatements(t, fake,
		statement{purgeSQL, nil},
		statement{insertSQL, []driver.Value{"cmd-1", "tenant-1", "session-1", "user-1", expiresAt}},
	)
}

// TestPostgres_CreateRequest_PurgeFailsNoInsert: si la purga falla, no se inserta.
func TestPostgres_CreateRequest_PurgeFailsNoInsert(t *testing.T) {
	store, fake := newPostgres(t, reply{err: errBoom})
	err := store.CreateRequest(context.Background(), "tenant-1", "session-1", "cmd-1", "user-1", time.Now())
	requireWrapped(t, err, "diagnostics: purgar vencidas: ")
	if got := fake.seen(); len(got) != 1 || got[0].query != purgeSQL {
		t.Errorf("tras fallar la purga llegaron %d sentencias, quería solo la purga: %+v", len(got), got)
	}
}

// TestPostgres_CreateRequest_InsertFailsAfterPurge: si falla el INSERT, la purga ya se hizo y no
// se deshace nada (no hay transacción).
func TestPostgres_CreateRequest_InsertFailsAfterPurge(t *testing.T) {
	store, fake := newPostgres(t, reply{}, reply{err: errBoom})
	err := store.CreateRequest(context.Background(), "tenant-1", "session-1", "cmd-1", "user-1", time.Now())
	requireWrapped(t, err, "diagnostics: crear solicitud: ")
	got := fake.seen()
	if len(got) != 2 || got[0].query != purgeSQL || got[1].query != insertSQL {
		t.Errorf("llegaron %d sentencias, quería la purga y el INSERT y nada más: %+v", len(got), got)
	}
}

// TestPostgres_DeleteRequest: un DELETE acotado por tenant y command_id.
func TestPostgres_DeleteRequest(t *testing.T) {
	store, fake := newPostgres(t)
	if err := store.DeleteRequest(context.Background(), "tenant-1", "cmd-1"); err != nil {
		t.Fatalf("DeleteRequest: error inesperado %v", err)
	}
	requireStatements(t, fake, statement{deleteSQL, []driver.Value{"tenant-1", "cmd-1"}})

	failing, _ := newPostgres(t, reply{err: errBoom})
	requireWrapped(t, failing.DeleteRequest(context.Background(), "tenant-1", "cmd-1"), "diagnostics: borrar solicitud: ")
}

// TestPostgres_SaveBundle: un UPDATE con los seis argumentos en su sitio; found es «tocó alguna
// fila».
func TestPostgres_SaveBundle(t *testing.T) {
	bundle := Bundle{LogTail: "log", GoroutineDump: "dump", SubsystemsJSON: `{"x":1}`}
	cases := []struct {
		name     string
		affected int64
		want     bool
	}{
		{"no row is an orphan", 0, false},
		{"one row correlates", 1, true},
		{"more than one row still correlates", 2, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store, fake := newPostgres(t, reply{affected: c.affected})
			found, err := store.SaveBundle(context.Background(), "tenant-1", "session-1", "cmd-1", bundle)
			if err != nil {
				t.Fatalf("SaveBundle: error inesperado %v", err)
			}
			if found != c.want {
				t.Errorf("found = %v, quería %v", found, c.want)
			}
			requireStatements(t, fake, statement{saveSQL,
				[]driver.Value{"log", "dump", `{"x":1}`, "cmd-1", "tenant-1", "session-1"}})
		})
	}
	failures := []struct {
		name   string
		reply  reply
		prefix string
	}{
		{"update fails", reply{err: errBoom}, "diagnostics: guardar bundle: "},
		{"rows affected fails", reply{affected: 1, affectedErr: errBoom}, "diagnostics: filas afectadas: "},
	}
	for _, c := range failures {
		t.Run(c.name, func(t *testing.T) {
			store, _ := newPostgres(t, c.reply)
			found, err := store.SaveBundle(context.Background(), "tenant-1", "session-1", "cmd-1", bundle)
			requireWrapped(t, err, c.prefix)
			if found {
				t.Error("SaveBundle con error devolvió found = true")
			}
		})
	}
}

// Los instantes de las filas de GetBundle. El vencimiento queda a una hora del reloj del
// proceso, por delante o por detrás; el borde exacto lo prueba postgres_clock_test.go.
var (
	requestedAt = time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	receivedAt  = time.Date(2026, 10, 4, 9, 0, 5, 0, time.UTC)
)

func future() time.Time { return time.Now().Add(time.Hour) }
func past() time.Time   { return time.Now().Add(-time.Hour) }

// bundleRow es la fila que contesta la lectura de GetBundle: sus nueve columnas.
func bundleRow(expiresAt time.Time, status string, received, logTail, dump, subsystems driver.Value) []driver.Value {
	return []driver.Value{"session-1", "user-1", requestedAt, expiresAt, status, received, logTail, dump, subsystems}
}

// getBundle llama a GetBundle y afirma que la PRIMERA sentencia fue la lectura, con sus
// argumentos; devuelve lo que contestó y cuántas sentencias llegaron en total.
func getBundle(t *testing.T, replies ...reply) (Record, []statement, error) {
	t.Helper()
	store, fake := newPostgres(t, replies...)
	rec, err := store.GetBundle(context.Background(), "tenant-1", "cmd-1")
	seen := fake.seen()
	if len(seen) == 0 || seen[0].query != selectSQL || !slices.Equal(seen[0].args, []driver.Value{"tenant-1", "cmd-1"}) {
		t.Fatalf("la primera sentencia de GetBundle no es la lectura con (tenant, command): %+v", seen)
	}
	return rec, seen, err
}

// TestPostgres_GetBundle_Ready: viva y lista, el Record entero con UNA sola sentencia.
func TestPostgres_GetBundle_Ready(t *testing.T) {
	rec, seen, err := getBundle(t, reply{row: bundleRow(future(), "ready", receivedAt, "log", "dump", `{"x":1}`)})
	if err != nil {
		t.Fatalf("GetBundle: error inesperado %v", err)
	}
	want := Record{
		CommandID: "cmd-1", SessionID: "session-1", RequestedBy: "user-1",
		RequestedAt: requestedAt, ReceivedAt: receivedAt,
		Bundle: Bundle{LogTail: "log", GoroutineDump: "dump", SubsystemsJSON: `{"x":1}`},
	}
	if rec != want {
		t.Errorf("GetBundle = %+v, quería %+v", rec, want)
	}
	if len(seen) != 1 {
		t.Errorf("una descarga viva emitió %d sentencias, quería 1 (no borra nada)", len(seen))
	}
}

// TestPostgres_GetBundle_ReadyWithNullColumns: las columnas a NULL salen como vacío y cero.
func TestPostgres_GetBundle_ReadyWithNullColumns(t *testing.T) {
	rec, _, err := getBundle(t, reply{row: bundleRow(future(), "ready", nil, nil, nil, nil)})
	if err != nil {
		t.Fatalf("GetBundle: error inesperado %v", err)
	}
	if rec.Bundle != (Bundle{}) || !rec.ReceivedAt.IsZero() {
		t.Errorf("GetBundle con columnas NULL = (%+v, recibido %v), quería el bundle vacío y el cero", rec.Bundle, rec.ReceivedAt)
	}
	if rec.CommandID != "cmd-1" || rec.SessionID != "session-1" {
		t.Errorf("GetBundle con columnas NULL perdió la solicitud: %+v", rec)
	}
}

// TestPostgres_GetBundle_Sentinels: cada desenlace que no es una descarga devuelve su centinela,
// el Record vacío y las sentencias justas.
func TestPostgres_GetBundle_Sentinels(t *testing.T) {
	cases := []struct {
		name       string
		replies    []reply
		want       error
		statements int
	}{
		{"no row", []reply{{}}, ErrNotFound, 1},
		{"alive and pending", []reply{{row: bundleRow(future(), "pending", nil, nil, nil, nil)}}, ErrPending, 1},
		{"alive with an unknown status", []reply{{row: bundleRow(future(), "other", nil, nil, nil, nil)}}, ErrPending, 1},
		{"expired and pending", []reply{{row: bundleRow(past(), "pending", nil, nil, nil, nil)}, {affected: 1}}, ErrExpired, 2},
		{"expired wins over ready", []reply{{row: bundleRow(past(), "ready", receivedAt, "log", "dump", "{}")}, {affected: 1}}, ErrExpired, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec, seen, err := getBundle(t, c.replies...)
			if !errors.Is(err, c.want) {
				t.Errorf("err = %v, quería %v", err, c.want)
			}
			if rec != (Record{}) {
				t.Errorf("GetBundle con error devolvió %+v, quería el Record vacío", rec)
			}
			if len(seen) != c.statements {
				t.Fatalf("llegaron %d sentencias, quería %d: %+v", len(seen), c.statements, seen)
			}
			if c.statements == 2 {
				// El borrado perezoso: la segunda sentencia, acotada por tenant y command_id.
				if seen[1].query != lazyDeleteSQL || !slices.Equal(seen[1].args, []driver.Value{"tenant-1", "cmd-1"}) {
					t.Errorf("segunda sentencia = %+v, quería el borrado perezoso de (tenant-1, cmd-1)", seen[1])
				}
			}
		})
	}
}

// TestPostgres_GetBundle_ReadFails: el fallo de la lectura sale envuelto, sin borrar nada.
func TestPostgres_GetBundle_ReadFails(t *testing.T) {
	rec, seen, err := getBundle(t, reply{err: errBoom})
	requireWrapped(t, err, "diagnostics: leer bundle: ")
	if rec != (Record{}) || len(seen) != 1 {
		t.Errorf("GetBundle con la lectura caída = (%+v, %d sentencias), quería (vacío, 1)", rec, len(seen))
	}
}

// TestPostgres_GetBundle_LazyDeleteFails: son dos sentencias sin transacción; si el borrado de
// la vencida falla, sale ESE error, no ErrExpired.
func TestPostgres_GetBundle_LazyDeleteFails(t *testing.T) {
	rec, seen, err := getBundle(t,
		reply{row: bundleRow(past(), "ready", receivedAt, "log", "dump", "{}")},
		reply{err: errBoom},
	)
	requireWrapped(t, err, "diagnostics: borrar vencida: ")
	if errors.Is(err, ErrExpired) {
		t.Error("el fallo del borrado perezoso se confunde con ErrExpired")
	}
	if rec != (Record{}) || len(seen) != 2 {
		t.Errorf("GetBundle con el borrado caído = (%+v, %d sentencias), quería (vacío, 2)", rec, len(seen))
	}
}
