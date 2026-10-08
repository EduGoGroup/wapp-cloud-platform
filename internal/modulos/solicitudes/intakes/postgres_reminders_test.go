//go:build pendiente

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

// LO QUE EL VERDE AÑADIRÁ (F6-03): el texto byte a byte de los dos compare-and-swap, de la lectura
// de señas vencidas y de la de la config de notificación.

// TestPostgres_MarkReminded_CompareAndSwap: los dos «Mark» son UNA sentencia suelta. Con fila,
// (solicitud marcada, true); sin fila, (cero, false) y sin error: no le tocaba. Un id que no es
// UUID es ErrNotFound sin tocar la base.
func TestPostgres_MarkReminded_CompareAndSwap(t *testing.T) {
	type mark func(*Postgres, context.Context, string, string, time.Time) (Intake, bool, error)
	cases := []struct {
		name     string
		call     mark
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
			store, fake := newFakePostgres(t)
			row := pgIntakeRow(StatusDepositRequested, 30)
			row[tc.column] = pgAt
			fake.script(pgOne(row...))
			got, ok, err := tc.call(store, t.Context(), pgTenant, pgIntakeID, pgAt)
			if err != nil || !ok || got.ID != pgIntakeID {
				t.Fatalf("Mark = (%+v, %v, %v), quería la solicitud marcada y true", got, ok, err)
			}
			if marks := []time.Time{got.DepositRemindedAt, got.ExpiryRemindedAt}; !marks[tc.column-9].Equal(pgAt) {
				t.Errorf("la marca no salió en su campo: %+v", got)
			}
			stmts := fake.statements()
			if len(stmts) != 1 || stmts[0].inTx || !reflect.DeepEqual(stmts[0].args, tc.wantArgs) {
				t.Errorf("sentencias = %+v, quería una suelta con argumentos %v", stmts, tc.wantArgs)
			}
			requirePgKinds(t, fake, pgQuery)
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
