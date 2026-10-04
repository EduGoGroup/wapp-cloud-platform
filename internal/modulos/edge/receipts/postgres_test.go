//go:build pendiente

package receipts_test

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/receipts"
)

// Las dos sentencias del adaptador, byte a byte (con su sangría): son las del fichero viejo.
const (
	saveSQL = `
		INSERT INTO public.message_receipts (session_id, command_id, message_id, status, receipt_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (session_id, message_id, status) DO UPDATE
		SET command_id = EXCLUDED.command_id,
		    receipt_at = EXCLUDED.receipt_at,
		    recorded_at = now()
	`
	listSQL = `
		SELECT id, session_id, command_id, message_id, status,
		       COALESCE(receipt_at, 'epoch'), recorded_at
		FROM public.message_receipts
		WHERE session_id = $1
		ORDER BY recorded_at DESC, id DESC
		LIMIT $2 OFFSET $3
	`
)

// newPostgresStore monta el adaptador sobre el driver de mentira.
func newPostgresStore(t *testing.T) (*receipts.PostgresStore, *fakeDB) {
	t.Helper()
	fake := &fakeDB{}
	return receipts.NewPostgresStore(fake.open(t)), fake
}

// onlyStatement devuelve la única sentencia que llegó al driver, o falla el test.
func onlyStatement(t *testing.T, fake *fakeDB) statement {
	t.Helper()
	seen := fake.seen()
	if len(seen) != 1 {
		t.Fatalf("llegaron %d sentencias al driver, quería 1: %+v", len(seen), seen)
	}
	return seen[0]
}

// TestNewPostgresStore_DoesNotQuery: construir el store no toca la base.
func TestNewPostgresStore_DoesNotQuery(t *testing.T) {
	store, fake := newPostgresStore(t)
	if store == nil {
		t.Fatal("NewPostgresStore devolvió nil")
	}
	if seen := fake.seen(); len(seen) != 0 {
		t.Errorf("construir el store emitió %d sentencias, quería 0", len(seen))
	}
}

// TestPostgresStore_Save_EmitsTheUpsert: un Save es UNA sentencia, el upsert literal, con los
// cinco argumentos en su orden y el instante en UTC.
func TestPostgresStore_Save_EmitsTheUpsert(t *testing.T) {
	store, fake := newPostgresStore(t)
	// Un instante con zona: el adaptador lo manda en UTC.
	at := time.Date(2026, 10, 4, 9, 30, 0, 0, time.FixedZone("UTC-5", -5*3600))
	err := store.Save(context.Background(), receipts.Receipt{
		SessionID: "session-1", CommandID: "cmd-1", MessageID: "msg-1",
		Status: receipts.StatusRead, ReceiptAt: at,
	})
	if err != nil {
		t.Fatalf("Save: error inesperado %v", err)
	}
	got := onlyStatement(t, fake)
	if got.query != saveSQL {
		t.Errorf("SQL emitido:\n%q\nquería:\n%q", got.query, saveSQL)
	}
	if len(got.args) != 5 {
		t.Fatalf("Save mandó %d argumentos, quería 5: %v", len(got.args), got.args)
	}
	for i, want := range []driver.Value{"session-1", "cmd-1", "msg-1", "read"} {
		if got.args[i] != want {
			t.Errorf("argumento $%d = %v (%T), quería %v", i+1, got.args[i], got.args[i], want)
		}
	}
	sent, ok := got.args[4].(time.Time)
	if !ok {
		t.Fatalf("argumento $5 = %v (%T), quería un time.Time", got.args[4], got.args[4])
	}
	if !sent.Equal(at) || sent.Location() != time.UTC {
		t.Errorf("argumento $5 = %v, quería %v en UTC", sent, at.UTC())
	}
}

// TestPostgresStore_Save_ZeroReceiptAtIsNull: un acuse sin instante informado guarda NULL, no el
// año 1.
func TestPostgresStore_Save_ZeroReceiptAtIsNull(t *testing.T) {
	store, fake := newPostgresStore(t)
	err := store.Save(context.Background(), receipts.Receipt{
		SessionID: "session-1", MessageID: "msg-1", Status: receipts.StatusDelivered,
	})
	if err != nil {
		t.Fatalf("Save: error inesperado %v", err)
	}
	got := onlyStatement(t, fake)
	if len(got.args) != 5 {
		t.Fatalf("Save mandó %d argumentos, quería 5: %v", len(got.args), got.args)
	}
	if got.args[1] != "" {
		t.Errorf("argumento $2 = %v, quería el command_id vacío", got.args[1])
	}
	if got.args[3] != "delivered" {
		t.Errorf("argumento $4 = %v, quería \"delivered\"", got.args[3])
	}
	if got.args[4] != nil {
		t.Errorf("argumento $5 = %v (%T), quería NULL", got.args[4], got.args[4])
	}
}

// TestPostgresStore_Save_WrapsTheError: el fallo de la base sale envuelto y reconocible.
func TestPostgresStore_Save_WrapsTheError(t *testing.T) {
	store, fake := newPostgresStore(t)
	boom := errors.New("base caída")
	fake.script(reply{err: boom})
	err := store.Save(context.Background(), receipts.Receipt{SessionID: "s", MessageID: "m", Status: receipts.StatusRead})
	requireWrapped(t, err, boom, "receipts: guardar acuse: ")
}

// TestPostgresStore_List_EmitsTheQuery: la consulta literal, con la sesión, el límite y el offset.
func TestPostgresStore_List_EmitsTheQuery(t *testing.T) {
	cases := []struct {
		name                  string
		limit, offset         int
		wantLimit, wantOffset int64
	}{
		{"as given", 25, 50, 25, 50},
		{"zero limit is 100", 0, 3, 100, 3},
		{"negative limit is 100", -7, 0, 100, 0},
		{"negative offset is 0", 10, -1, 10, 0},
		{"limit of one is kept", 1, 0, 1, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store, fake := newPostgresStore(t)
			got, err := store.List(context.Background(), "session-1", c.limit, c.offset)
			if err != nil {
				t.Fatalf("List: error inesperado %v", err)
			}
			if got != nil {
				t.Errorf("List sin filas = %v, quería nil", got)
			}
			st := onlyStatement(t, fake)
			if st.query != listSQL {
				t.Errorf("SQL emitido:\n%q\nquería:\n%q", st.query, listSQL)
			}
			want := []driver.Value{"session-1", c.wantLimit, c.wantOffset}
			if len(st.args) != len(want) {
				t.Fatalf("List mandó %d argumentos, quería %d: %v", len(st.args), len(want), st.args)
			}
			for i := range want {
				if st.args[i] != want[i] {
					t.Errorf("argumento $%d = %v (%T), quería %v", i+1, st.args[i], st.args[i], want[i])
				}
			}
		})
	}
}

// TestPostgresStore_List_MapsTheRows: cada fila sale con sus siete columnas en su sitio y en el
// orden en que las da la base.
func TestPostgresStore_List_MapsTheRows(t *testing.T) {
	store, fake := newPostgresStore(t)
	receiptAt := time.Unix(1_700_000_000, 0).UTC()
	recordedAt := time.Unix(1_700_000_100, 0).UTC()
	epoch := time.Unix(0, 0).UTC()
	fake.script(reply{rows: [][]driver.Value{
		{int64(9), "session-1", "cmd-9", "msg-9", "read", receiptAt, recordedAt},
		{int64(4), "session-1", "", "msg-4", "delivered", epoch, recordedAt.Add(-time.Minute)},
	}})
	got, err := store.List(context.Background(), "session-1", 10, 0)
	if err != nil {
		t.Fatalf("List: error inesperado %v", err)
	}
	want := []receipts.Stored{
		{
			Receipt: receipts.Receipt{
				SessionID: "session-1", CommandID: "cmd-9", MessageID: "msg-9",
				Status: receipts.StatusRead, ReceiptAt: receiptAt,
			},
			ID: 9, RecordedAt: recordedAt,
		},
		{
			Receipt: receipts.Receipt{
				SessionID: "session-1", MessageID: "msg-4",
				Status: receipts.StatusDelivered, ReceiptAt: epoch,
			},
			ID: 4, RecordedAt: recordedAt.Add(-time.Minute),
		},
	}
	if len(got) != len(want) {
		t.Fatalf("List devolvió %d filas, quería %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("fila %d = %+v, quería %+v", i, got[i], want[i])
		}
	}
}

// TestPostgresStore_List_Errors: los tres fallos posibles salen envueltos, cada uno con su
// prefijo, y sin filas a medias.
func TestPostgresStore_List_Errors(t *testing.T) {
	boom := errors.New("base caída")
	row := []driver.Value{int64(1), "s", "c", "m", "read", time.Unix(0, 0), time.Unix(0, 0)}
	cases := []struct {
		name   string
		reply  reply
		prefix string
		cause  error
	}{
		{"query fails", reply{err: boom}, "receipts: listar acuses: ", boom},
		{"iteration fails", reply{rows: [][]driver.Value{row}, endErr: boom}, "receipts: iterar acuses: ", boom},
		// Un id que no es un entero no se puede escanear: el error es de database/sql.
		{
			"row does not scan",
			reply{rows: [][]driver.Value{{"no-es-un-id", "s", "c", "m", "read", time.Unix(0, 0), time.Unix(0, 0)}}},
			"receipts: escanear acuse: ", nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store, fake := newPostgresStore(t)
			fake.script(c.reply)
			got, err := store.List(context.Background(), "s", 10, 0)
			requireWrapped(t, err, c.cause, c.prefix)
			if got != nil {
				t.Errorf("List con error devolvió %+v, quería nil", got)
			}
		})
	}
}

// requireWrapped afirma que err empieza por prefix y, si cause no es nil, que lo envuelve.
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
