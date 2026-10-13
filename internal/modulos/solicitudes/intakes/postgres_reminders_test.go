package intakes

import (
	"context"
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestPostgres_MarkReminded_CompareAndSwap: los dos «Mark» son UNA sentencia suelta. Con fila,
// (solicitud marcada, true); sin fila, (cero, false) y sin error: no le tocaba. Un id que no es
// UUID es ErrNotFound sin tocar la base.
func TestPostgres_MarkReminded_CompareAndSwap(t *testing.T) {
	cases := []struct {
		name     string
		call     pgMark
		column   int
		wantArgs []driver.Value
	}{
		{"deposit", (*Postgres).MarkDepositReminded, 9,
			[]driver.Value{pgTenant, pgIntakeID, pgAt, []string{StatusDepositRequested}}},
		{"expiry", (*Postgres).MarkExpiryReminded, 10,
			[]driver.Value{pgTenant, pgIntakeID, pgAt, []string{StatusPendingApproval}, pgAt.Add(-QuoteDeadline)}},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/wins the reminder", func(t *testing.T) {
			requireMarkWins(t, tc.call, tc.column, tc.wantArgs)
		})
		t.Run(tc.name+"/not its turn", func(t *testing.T) {
			store, _ := newFakePostgres(t)
			got, ok, err := tc.call(store, t.Context(), pgTenant, pgIntakeID, pgAt)
			if err != nil || ok || got != (Intake{}) {
				t.Errorf("Mark sin fila = (%+v, %v, %v), quería (Intake{}, false, nil)", got, ok, err)
			}
		})
		t.Run(tc.name+"/invalid id", func(t *testing.T) {
			store, fake := newFakePostgres(t)
			got, ok, err := tc.call(store, t.Context(), pgTenant, "x", pgAt)
			if !errors.Is(err, ErrNotFound) || ok || got != (Intake{}) {
				t.Errorf("Mark con id inválido = (%+v, %v, %v), quería (Intake{}, false, ErrNotFound)", got, ok, err)
			}
			requirePgUntouched(t, fake)
		})
		t.Run(tc.name+"/database fails", func(t *testing.T) {
			store, fake := newFakePostgres(t)
			fake.script(pgReply{err: errPgBoom})
			got, ok, err := tc.call(store, t.Context(), pgTenant, pgIntakeID, pgAt)
			requirePgWrapped(t, err, "intakes: leer solicitud: ", errPgBoom)
			if ok || got != (Intake{}) {
				t.Errorf("Mark con error = (%+v, %v), quería (Intake{}, false)", got, ok)
			}
		})
	}
}

// pgMark es la forma común de los dos «Mark» del store.
type pgMark func(*Postgres, context.Context, string, string, time.Time) (Intake, bool, error)

// requireMarkWins: con fila, el «Mark» devuelve (solicitud marcada, true), con la marca en su
// campo (`column` es la columna de la fila que la lleva), y fue UNA consulta suelta con `wantArgs`.
func requireMarkWins(t *testing.T, call pgMark, column int, wantArgs []driver.Value) {
	t.Helper()
	store, fake := newFakePostgres(t)
	row := pgIntakeRow(StatusDepositRequested, 30)
	row[column] = pgAt
	fake.script(pgOne(row...))
	got, ok, err := call(store, t.Context(), pgTenant, pgIntakeID, pgAt)
	if err != nil || !ok || got.ID != pgIntakeID {
		t.Fatalf("Mark = (%+v, %v, %v), quería la solicitud marcada y true", got, ok, err)
	}
	if marks := []time.Time{got.DepositRemindedAt, got.ExpiryRemindedAt}; !marks[column-9].Equal(pgAt) {
		t.Errorf("la marca no salió en su campo: %+v", got)
	}
	stmts := fake.statements()
	if len(stmts) != 1 || stmts[0].inTx || !reflect.DeepEqual(stmts[0].args, wantArgs) {
		t.Errorf("sentencias = %+v, quería una suelta con argumentos %v", stmts, wantArgs)
	}
	requirePgKinds(t, fake, pgQuery)
}

// TestPostgres_SatisfiesTheReminderPorts: los puertos estrechos de los recordatorios.
func TestPostgres_SatisfiesTheReminderPorts(t *testing.T) {
	store, _ := newFakePostgres(t)
	var (
		_ DepositStore   = store
		_ ExpiryStore    = store
		_ SettingsReader = store
	)
}

// TestPostgres_PendingDepositReminders_NonPositiveLimit_DoesNotQuery: pedir cero no toca la base.
func TestPostgres_PendingDepositReminders_NonPositiveLimit_DoesNotQuery(t *testing.T) {
	for _, limit := range []int{0, -3} {
		store, fake := newFakePostgres(t)
		got, err := store.PendingDepositReminders(t.Context(), pgTenant, pgContact, pgAt, limit)
		if err != nil || got == nil || len(got) != 0 {
			t.Errorf("PendingDepositReminders(limit=%d) = (%#v, %v), quería ([]Intake{}, nil)", limit, got, err)
		}
		requirePgUntouched(t, fake)
	}
}

// TestPostgres_PendingDepositReminders_OnlyReads: UNA consulta suelta con tenant, contacto,
// instante, límite y las variantes del estado; las filas salen en el orden de la base y sin filas
// el slice es vacío y no nil. No marca nada.
func TestPostgres_PendingDepositReminders_OnlyReads(t *testing.T) {
	store, fake := newFakePostgres(t)
	second := pgIntakeRow(StatusDepositRequested, 12)
	second[0] = "33333333-3333-4333-8333-333333333333"
	fake.script(pgReply{rows: [][]driver.Value{pgIntakeRow(StatusDepositRequested, 30), second}})
	got, err := store.PendingDepositReminders(t.Context(), pgTenant, pgContact, pgAt, 5)
	if err != nil || len(got) != 2 || got[0].ID != pgIntakeID || got[1].Total != 12 {
		t.Fatalf("PendingDepositReminders = (%+v, %v), quería las dos filas en orden", got, err)
	}
	requirePgKinds(t, fake, pgQuery)
	want := []driver.Value{pgTenant, pgContact, pgAt, 5, []string{StatusDepositRequested}}
	if stmt := fake.statements()[0]; stmt.inTx || !reflect.DeepEqual(stmt.args, want) {
		t.Errorf("argumentos = %v, quería %v", stmt.args, want)
	}

	empty, _ := newFakePostgres(t)
	if got, err := empty.PendingDepositReminders(t.Context(), pgTenant, pgContact, pgAt, 5); err != nil || got == nil || len(got) != 0 {
		t.Errorf("sin filas = (%#v, %v), quería ([]Intake{}, nil)", got, err)
	}
}

// TestPostgres_PendingDepositReminders_Errors: los fallos de la lectura, con su prefijo.
func TestPostgres_PendingDepositReminders_Errors(t *testing.T) {
	closeBoom := errors.New("cierre roto")
	cases := []struct {
		name   string
		reply  pgReply
		prefix string
		cause  error
	}{
		{"query fails", pgReply{err: errPgBoom}, "intakes: listar señas vencidas del contacto: ", errPgBoom},
		{"iteration fails", pgReply{endErr: errPgBoom}, "intakes: recorrer señas vencidas: ", errPgBoom},
		{"close fails", pgReply{closeErr: closeBoom}, "intakes: cerrar filas de señas vencidas: ", closeBoom},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, fake := newFakePostgres(t)
			fake.script(tc.reply)
			got, err := store.PendingDepositReminders(t.Context(), pgTenant, pgContact, pgAt, 5)
			requirePgWrapped(t, err, tc.prefix, tc.cause)
			if got != nil {
				t.Errorf("con error devolvió %+v, quería nil", got)
			}
		})
	}
	t.Run("row cannot be scanned", func(t *testing.T) {
		store, fake := newFakePostgres(t)
		bad := pgIntakeRow(StatusDepositRequested, 1)
		bad[0] = nil
		fake.script(pgOne(bad...))
		_, err := store.PendingDepositReminders(t.Context(), pgTenant, pgContact, pgAt, 5)
		if err == nil || !strings.HasPrefix(err.Error(), "intakes: leer solicitud: ") {
			t.Errorf("error = %v, quería el prefijo %q", err, "intakes: leer solicitud: ")
		}
	})
}

// TestPostgres_NotifySettings: sin fila de config no es un error (plantilla vacía y plazo por
// defecto); un plazo que no es positivo sale como el de por defecto y la plantilla, tal cual.
func TestPostgres_NotifySettings(t *testing.T) {
	cases := []struct {
		name  string
		reply pgReply
		want  NotifySettings
	}{
		{"tenant without settings", pgReply{}, NotifySettings{DepositDueDays: DefaultDepositDueDays}},
		{"template and days", pgOne("  Hola {total}  ", int64(7)), NotifySettings{DepositTemplate: "  Hola {total}  ", DepositDueDays: 7}},
		{"zero days", pgOne("Hola", int64(0)), NotifySettings{DepositTemplate: "Hola", DepositDueDays: DefaultDepositDueDays}},
		{"negative days", pgOne("", int64(-1)), NotifySettings{DepositDueDays: DefaultDepositDueDays}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, fake := newFakePostgres(t)
			fake.script(tc.reply)
			got, err := store.NotifySettings(t.Context(), pgTenant)
			if err != nil || got != tc.want {
				t.Errorf("NotifySettings = (%+v, %v), quería (%+v, nil)", got, err, tc.want)
			}
			if stmt := fake.statements()[0]; stmt.inTx || !reflect.DeepEqual(stmt.args, []driver.Value{pgTenant}) {
				t.Errorf("sentencia = %+v, quería una suelta con el tenant", stmt)
			}
		})
	}
	t.Run("database fails", func(t *testing.T) {
		store, fake := newFakePostgres(t)
		fake.script(pgReply{err: errPgBoom})
		got, err := store.NotifySettings(t.Context(), pgTenant)
		requirePgWrapped(t, err, "intakes: leer la config de notificación del tenant: ", errPgBoom)
		if got != (NotifySettings{}) {
			t.Errorf("con error devolvió %+v, quería NotifySettings{}", got)
		}
	})
}

// Las sentencias, escritas APARTE y byte a byte (sangría y saltos de línea incluidos): son las
// del paquete viejo, y un cambio en el SQL de producción tiene que romper aquí.

// wantMarkDepositRemindedSQL es el compare-and-swap del recordatorio de la seña.
const wantMarkDepositRemindedSQL = `
	UPDATE public.intakes
	SET deposit_reminded_at = $3
	WHERE tenant_id = $1 AND id = $2
	  AND status = ANY($4)
	  AND deposit_due_at IS NOT NULL
	  AND deposit_due_at <= $3
	  AND deposit_reminded_at IS NULL
	RETURNING id::text, contact_id, session_id, status, total, created_at, updated_at, customer_note,
	deposit_due_at, deposit_reminded_at, expiry_reminded_at`

// wantMarkExpiryRemindedSQL es el compare-and-swap del aviso del plazo: el corte viaja en $5.
const wantMarkExpiryRemindedSQL = `
	UPDATE public.intakes
	SET expiry_reminded_at = $3
	WHERE tenant_id = $1 AND id = $2
	  AND status = ANY($4)
	  AND updated_at <= $5
	  AND expiry_reminded_at IS NULL
	RETURNING id::text, contact_id, session_id, status, total, created_at, updated_at, customer_note,
	deposit_due_at, deposit_reminded_at, expiry_reminded_at`

// wantPendingDepositRemindersSQL es la lectura de las señas vencidas de un contacto.
const wantPendingDepositRemindersSQL = `
	SELECT id::text, contact_id, session_id, status, total, created_at, updated_at, customer_note,
	deposit_due_at, deposit_reminded_at, expiry_reminded_at
	FROM public.intakes
	WHERE tenant_id = $1 AND contact_id = $2
	  AND status = ANY($5)
	  AND deposit_due_at IS NOT NULL
	  AND deposit_due_at <= $3
	  AND deposit_reminded_at IS NULL
	ORDER BY deposit_due_at
	LIMIT $4`

// wantNotifySettingsSQL es la lectura de la config de notificación del tenant.
const wantNotifySettingsSQL = `SELECT deposit_template, deposit_due_days FROM public.tenant_settings WHERE tenant_id = $1`

// TestPostgres_Reminders_SQLIsTheOldOneByteForByte: los dos compare-and-swap, la lectura de señas
// vencidas y la de la config salen con el texto del paquete viejo.
func TestPostgres_Reminders_SQLIsTheOldOneByteForByte(t *testing.T) {
	store, fake := newFakePostgres(t)
	if _, _, err := store.MarkDepositReminded(t.Context(), pgTenant, pgIntakeID, pgAt); err != nil {
		t.Fatalf("MarkDepositReminded: error inesperado %v", err)
	}
	if _, _, err := store.MarkExpiryReminded(t.Context(), pgTenant, pgIntakeID, pgAt); err != nil {
		t.Fatalf("MarkExpiryReminded: error inesperado %v", err)
	}
	if _, err := store.PendingDepositReminders(t.Context(), pgTenant, pgContact, pgAt, 5); err != nil {
		t.Fatalf("PendingDepositReminders: error inesperado %v", err)
	}
	if _, err := store.NotifySettings(t.Context(), pgTenant); err != nil {
		t.Fatalf("NotifySettings: error inesperado %v", err)
	}
	requirePgSQL(t, fake, wantMarkDepositRemindedSQL, wantMarkExpiryRemindedSQL, wantPendingDepositRemindersSQL, wantNotifySettingsSQL)
}
