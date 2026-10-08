package intakes

import (
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// TestPostgres_UpdateStatus_InvalidID_NotFoundWithoutQuerying: un id que no es UUID no existe.
func TestPostgres_UpdateStatus_InvalidID_NotFoundWithoutQuerying(t *testing.T) {
	store, fake := newFakePostgres(t)
	var port Store = store
	if _, err := port.UpdateStatus(t.Context(), pgTenant, "x", StatusConfirmed, []string{StatusOpen}); !errors.Is(err, ErrNotFound) {
		t.Errorf("UpdateStatus = %v, quería ErrNotFound", err)
	}
	requirePgUntouched(t, fake)
}

// TestPostgres_UpdateStatus_PlainTransition_IsACompareAndSwapInATx: un destino sin efectos es
// UNA sentencia dentro de una transacción confirmada; `to` y `expected` viajan tal cual, acotados
// por tenant, y la cabecera sale normalizada.
func TestPostgres_UpdateStatus_PlainTransition_IsACompareAndSwapInATx(t *testing.T) {
	store, fake := newFakePostgres(t)
	fake.script(pgOne(pgIntakeRow(StatusClosedLegacy, 30)...))
	expected := []string{StatusClosedLegacy, StatusOpen}
	got, err := store.UpdateStatus(t.Context(), pgTenant, pgIntakeID, StatusClosedLegacy, expected)
	if err != nil {
		t.Fatalf("UpdateStatus: error inesperado %v", err)
	}
	if want := pgIntake(StatusConfirmed, 30); got != want {
		t.Errorf("UpdateStatus = %+v, quería %+v", got, want)
	}
	requirePgKinds(t, fake, pgBegin, pgQuery, pgCommit)
	cas := fake.statements()[0]
	if want := []driver.Value{pgTenant, pgIntakeID, StatusClosedLegacy, expected}; !cas.inTx || !reflect.DeepEqual(cas.args, want) {
		t.Errorf("CAS (en transacción: %v) con argumentos %v, quería %v dentro de la transacción", cas.inTx, cas.args, want)
	}
}

// TestPostgres_UpdateStatus_NoRowMoved_TellsNotFoundFromConflict: si el CAS no mueve nada, la
// relectura distingue «no existe en ese tenant» de «otro operador se adelantó». Las dos revierten.
func TestPostgres_UpdateStatus_NoRowMoved_TellsNotFoundFromConflict(t *testing.T) {
	cases := []struct {
		name   string
		reread pgReply
		want   error
	}{
		{"not in this tenant", pgReply{}, ErrNotFound},
		{"moved by someone else", pgOne(true), ErrConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, fake := newFakePostgres(t)
			fake.script(pgReply{}, tc.reread)
			got, err := store.UpdateStatus(t.Context(), pgTenant, pgIntakeID, StatusConfirmed, []string{StatusOpen})
			if !errors.Is(err, tc.want) || got != (Intake{}) {
				t.Errorf("UpdateStatus = (%+v, %v), quería (Intake{}, %v)", got, err, tc.want)
			}
			requirePgKinds(t, fake, pgBegin, pgQuery, pgQuery, pgRollback)
			if want := []driver.Value{pgTenant, pgIntakeID}; !reflect.DeepEqual(fake.statements()[1].args, want) {
				t.Errorf("argumentos de la relectura = %v, quería %v", fake.statements()[1].args, want)
			}
		})
	}
}

// TestPostgres_UpdateStatus_DepositRequested_SetsTheDueDateInTheSameTx: al pedir la seña se lee el
// plazo del tenant y se fija la fecha límite en la MISMA transacción; sin fila de config, o con
// un plazo que no es positivo, vale el de por defecto. Devuelve la cabecera con la fecha puesta.
func TestPostgres_UpdateStatus_DepositRequested_SetsTheDueDateInTheSameTx(t *testing.T) {
	cases := []struct {
		name     string
		settings pgReply
		wantDays int
	}{
		{"tenant without settings", pgReply{}, DefaultDepositDueDays},
		{"tenant with seven days", pgOne(int64(7)), 7},
		{"tenant with zero days", pgOne(int64(0)), DefaultDepositDueDays},
		{"tenant with negative days", pgOne(int64(-2)), DefaultDepositDueDays},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, fake := newFakePostgres(t)
			withDue := pgIntakeRow(StatusDepositRequested, 30)
			withDue[8] = pgAt
			fake.script(pgOne(pgIntakeRow(StatusDepositRequested, 30)...), tc.settings, pgOne(withDue...))
			got, err := store.UpdateStatus(t.Context(), pgTenant, pgIntakeID, StatusDepositRequested, []string{StatusPendingApproval})
			if err != nil {
				t.Fatalf("UpdateStatus: error inesperado %v", err)
			}
			if !got.DepositDueAt.Equal(pgAt) || !got.DepositRemindedAt.IsZero() {
				t.Errorf("cabecera = %+v, quería DepositDueAt = %v y sin recordatorio", got, pgAt)
			}
			requirePgKinds(t, fake, pgBegin, pgQuery, pgQuery, pgQuery, pgCommit)
			stmts := fake.statements()
			if want := []driver.Value{pgTenant}; !reflect.DeepEqual(stmts[1].args, want) {
				t.Errorf("argumentos de la lectura del plazo = %v, quería %v", stmts[1].args, want)
			}
			if want := []driver.Value{pgTenant, pgIntakeID, tc.wantDays}; !reflect.DeepEqual(stmts[2].args, want) {
				t.Errorf("argumentos de la fecha límite = %v, quería %v", stmts[2].args, want)
			}
		})
	}
}

// TestPostgres_UpdateStatus_PendingApproval_MaterializesShipping: al entrar en aprobación se
// materializa la línea de envío (política ShippingAlways: también sin zonas) y, como cambió algo,
// se recalcula el total y esa es la cabecera que se devuelve.
func TestPostgres_UpdateStatus_PendingApproval_MaterializesShipping(t *testing.T) {
	store, fake := newFakePostgres(t)
	fake.script(pgOne(pgIntakeRow(StatusPendingApproval, 30)...), pgReply{}, pgReply{}, pgReply{},
		pgOne(pgIntakeRow(StatusPendingApproval, 33)...))
	got, err := store.UpdateStatus(t.Context(), pgTenant, pgIntakeID, StatusPendingApproval, []string{StatusOpen})
	if err != nil {
		t.Fatalf("UpdateStatus: error inesperado %v", err)
	}
	if got.Total != 33 {
		t.Errorf("Total = %v, quería el recalculado (33)", got.Total)
	}
	requirePgKinds(t, fake, pgBegin, pgQuery, pgQuery, pgQuery, pgExec, pgQuery, pgCommit)
	insert := fake.statements()[3]
	if want := []driver.Value{pgIntakeID, ShippingSKU, ShippingPendingLabel, 1, float64(0)}; !reflect.DeepEqual(insert.args, want) {
		t.Errorf("argumentos de la línea de envío = %v, quería %v", insert.args, want)
	}
}

// TestPostgres_UpdateStatus_PendingApproval_ShippingUnchanged_KeepsTheCASHeader: si la línea de
// envío ya estaba y no hay que tocarla, no se recalcula nada y vuelve la cabecera del CAS.
func TestPostgres_UpdateStatus_PendingApproval_ShippingUnchanged_KeepsTheCASHeader(t *testing.T) {
	store, fake := newFakePostgres(t)
	fake.script(pgOne(pgIntakeRow(StatusPendingApproval, 30)...), pgReply{},
		pgOne(int64(9), "Envío a mano", int64(1), float64(5)))
	got, err := store.UpdateStatus(t.Context(), pgTenant, pgIntakeID, StatusPendingApproval, []string{StatusOpen})
	if err != nil || got.Total != 30 {
		t.Fatalf("UpdateStatus = (%+v, %v), quería la cabecera del CAS", got, err)
	}
	requirePgKinds(t, fake, pgBegin, pgQuery, pgQuery, pgQuery, pgCommit)
}

// TestPostgres_UpdateStatus_Errors: cada fallo sale con su prefijo y REVIERTE la transacción: la
// solicitud no queda movida a medias.
func TestPostgres_UpdateStatus_Errors(t *testing.T) {
	moved := pgOne(pgIntakeRow(StatusDepositRequested, 30)...)
	cases := []struct {
		name   string
		to     string
		script []pgReply
		prefix string
	}{
		{"cas fails", StatusConfirmed, []pgReply{{err: errPgBoom}}, "intakes: leer solicitud: "},
		{"reread fails", StatusConfirmed, []pgReply{{}, {err: errPgBoom}}, "intakes: verificar solicitud: "},
		{"due days read fails", StatusDepositRequested, []pgReply{moved, {err: errPgBoom}}, "intakes: leer el plazo de la seña del tenant: "},
		{"due date update fails", StatusDepositRequested, []pgReply{moved, {}, {err: errPgBoom}}, "intakes: leer solicitud: "},
		{"shipping zones read fails", StatusPendingApproval, []pgReply{moved, {err: errPgBoom}}, "intakes: leer las zonas de envío del tenant: "},
		{"total recompute fails", StatusPendingApproval, []pgReply{moved, {}, {}, {}, {err: errPgBoom}}, "intakes: leer solicitud: "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, fake := newFakePostgres(t)
			fake.script(tc.script...)
			got, err := store.UpdateStatus(t.Context(), pgTenant, pgIntakeID, tc.to, []string{StatusOpen})
			requirePgWrapped(t, err, tc.prefix, errPgBoom)
			if got != (Intake{}) {
				t.Errorf("UpdateStatus con error devolvió %+v, quería Intake{}", got)
			}
			kinds := fake.kinds()
			if kinds[len(kinds)-1] != pgRollback {
				t.Errorf("conversación = %v, quería acabar en rollback", kinds)
			}
		})
	}
}

// TestPostgres_UpdateStatus_TxFailures: si la transacción no se abre no se lanza nada, y si no se
// confirma el método falla; en los dos casos el error envuelve la causa.
func TestPostgres_UpdateStatus_TxFailures(t *testing.T) {
	t.Run("begin fails", func(t *testing.T) {
		store, fake := newFakePostgres(t)
		fake.failTx(errPgBoom, nil, nil)
		_, err := store.UpdateStatus(t.Context(), pgTenant, pgIntakeID, StatusConfirmed, []string{StatusOpen})
		if !errors.Is(err, errPgBoom) {
			t.Errorf("UpdateStatus = %v, quería un error que envuelva la causa", err)
		}
		requirePgUntouched(t, fake)
	})
	t.Run("commit fails", func(t *testing.T) {
		store, fake := newFakePostgres(t)
		fake.failTx(nil, errPgBoom, nil)
		fake.script(pgOne(pgIntakeRow(StatusConfirmed, 30)...))
		got, err := store.UpdateStatus(t.Context(), pgTenant, pgIntakeID, StatusConfirmed, []string{StatusOpen})
		if !errors.Is(err, errPgBoom) || got != (Intake{}) {
			t.Errorf("UpdateStatus = (%+v, %v), quería (Intake{}, error que envuelva la causa)", got, err)
		}
		if strings.Contains(strings.Join(fake.kinds(), ","), pgCommit) {
			t.Errorf("conversación = %v: se apuntó un commit que falló", fake.kinds())
		}
	})
}

// Las sentencias de la transición, escritas APARTE y byte a byte: son las del paquete viejo.

// wantCASSQL es el compare-and-swap del estado.
const wantCASSQL = `
		UPDATE public.intakes
		SET status = $3, updated_at = now()
		WHERE tenant_id = $1 AND id = $2 AND status = ANY($4)
		RETURNING id::text, contact_id, session_id, status, total, created_at, updated_at, customer_note,
	deposit_due_at, deposit_reminded_at, expiry_reminded_at`

// wantCASRereadSQL es la relectura que distingue «no existe» de «conflicto».
const wantCASRereadSQL = `SELECT true FROM public.intakes WHERE tenant_id = $1 AND id = $2`

// wantDueDaysSQL es la lectura del plazo de la seña del tenant.
const wantDueDaysSQL = `SELECT deposit_due_days FROM public.tenant_settings WHERE tenant_id = $1`

// wantDueDateSQL fija la fecha límite de la seña y limpia el recordatorio.
const wantDueDateSQL = `
		UPDATE public.intakes
		SET deposit_due_at = now() + make_interval(days => $3::int),
		    deposit_reminded_at = NULL,
		    updated_at = now()
		WHERE tenant_id = $1 AND id = $2
		RETURNING id::text, contact_id, session_id, status, total, created_at, updated_at, customer_note,
	deposit_due_at, deposit_reminded_at, expiry_reminded_at`

// wantShippingZonesSQL es la lectura de las zonas de envío.
const wantShippingZonesSQL = `SELECT shipping_zones FROM public.tenant_settings WHERE tenant_id = $1`

// wantShippingLineSQL es la lectura de la línea de envío almacenada.
const wantShippingLineSQL = `
		SELECT id, label, qty, unit_price
		FROM public.intake_items
		WHERE intake_id = $1 AND sku = $2
	`

// wantShippingInsertSQL es el alta de la línea de envío.
const wantShippingInsertSQL = `
			INSERT INTO public.intake_items (intake_id, sku, label, customization, qty, unit_price)
			VALUES ($1, $2, $3, '', $4, $5)
		`

// wantRecomputeTotalSQL recalcula el total entero desde las líneas.
const wantRecomputeTotalSQL = `
		UPDATE public.intakes i
		SET total = COALESCE((
			    SELECT SUM(it.qty * it.unit_price)
			    FROM public.intake_items it
			    WHERE it.intake_id = i.id), 0),
		    updated_at = now()
		WHERE i.tenant_id = $1 AND i.id = $2
		RETURNING id::text, contact_id, session_id, status, total, created_at, updated_at, customer_note,
	deposit_due_at, deposit_reminded_at, expiry_reminded_at`

// TestPostgres_UpdateStatus_SQLIsTheOldOneByteForByte: el texto de cada sentencia de los tres
// caminos de la transición, en su orden.
func TestPostgres_UpdateStatus_SQLIsTheOldOneByteForByte(t *testing.T) {
	cases := []struct {
		name   string
		to     string
		script []pgReply
		want   []string
	}{
		{"cas and reread", StatusConfirmed, []pgReply{{}, pgOne(true)}, []string{wantCASSQL, wantCASRereadSQL}},
		{"deposit requested", StatusDepositRequested,
			[]pgReply{pgOne(pgIntakeRow(StatusDepositRequested, 30)...), {}, pgOne(pgIntakeRow(StatusDepositRequested, 30)...)},
			[]string{wantCASSQL, wantDueDaysSQL, wantDueDateSQL}},
		{"pending approval", StatusPendingApproval,
			[]pgReply{pgOne(pgIntakeRow(StatusPendingApproval, 30)...), {}, {}, {}, pgOne(pgIntakeRow(StatusPendingApproval, 33)...)},
			[]string{wantCASSQL, wantShippingZonesSQL, wantShippingLineSQL, wantShippingInsertSQL, wantRecomputeTotalSQL}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, fake := newFakePostgres(t)
			fake.script(tc.script...)
			// El error no importa aquí (el primer caso acaba en ErrConflict): se mira el texto.
			_, _ = store.UpdateStatus(t.Context(), pgTenant, pgIntakeID, tc.to, []string{StatusOpen})
			requirePgSQL(t, fake, tc.want...)
		})
	}
}
