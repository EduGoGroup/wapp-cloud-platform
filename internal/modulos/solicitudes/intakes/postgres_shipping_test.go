//go:build pendiente

package intakes

import (
	"database/sql/driver"
	"errors"
	"reflect"
	"testing"
)

// LO QUE EL VERDE AÑADIRÁ (F6-03): el texto byte a byte del bloqueo, de la lectura de zonas, de
// la lectura / INSERT / UPDATE de la línea de envío y del recálculo del total.

// pgOneZone es la config de un tenant con una sola zona con nombre: la línea deseada tiene precio.
const pgOneZone = `[{"code":"z1","label":"Providencia","price":3000}]`

// TestPostgres_EnsureShippingLine_NotFound: un id que no es UUID no toca la base; una cabecera
// que no existe en ese tenant revierte sin escribir.
func TestPostgres_EnsureShippingLine_NotFound(t *testing.T) {
	store, fake := newFakePostgres(t)
	if err := store.EnsureShippingLine(t.Context(), pgTenant, "x", ShippingAlways); !errors.Is(err, ErrNotFound) {
		t.Errorf("EnsureShippingLine(id inválido) = %v, quería ErrNotFound", err)
	}
	requirePgUntouched(t, fake)

	if err := store.EnsureShippingLine(t.Context(), pgTenant, pgIntakeID, ShippingAlways); !errors.Is(err, ErrNotFound) {
		t.Errorf("EnsureShippingLine(sin cabecera) = %v, quería ErrNotFound", err)
	}
	requirePgKinds(t, fake, pgBegin, pgQuery, pgRollback)
	if lock := fake.statements()[0]; !lock.inTx || !reflect.DeepEqual(lock.args, []driver.Value{pgTenant, pgIntakeID}) {
		t.Errorf("bloqueo = %+v, quería tenant e id dentro de la transacción", lock)
	}
}

// TestPostgres_EnsureShippingLine_Scenarios: la forma de la conversación según la política, las
// zonas y la línea que ya hubiera. Solo se recalcula el total si la línea cambió.
func TestPostgres_EnsureShippingLine_Scenarios(t *testing.T) {
	lock := pgOne(true)
	total := pgOne(pgIntakeRow(StatusPendingApproval, 3030)...)
	cases := []struct {
		name      string
		policy    ShippingPolicy
		script    []pgReply
		wantKinds []string
		writeAt   int
		wantWrite []driver.Value
	}{
		{"policy does not apply without zones", ShippingOnlyIfZones, []pgReply{lock, {}},
			[]string{pgBegin, pgQuery, pgQuery, pgCommit}, -1, nil},
		{"always: inserts the pending line", ShippingAlways, []pgReply{lock, {}, {}, {}, total},
			[]string{pgBegin, pgQuery, pgQuery, pgQuery, pgExec, pgQuery, pgCommit}, 3,
			[]driver.Value{pgIntakeID, ShippingSKU, ShippingPendingLabel, 1, float64(0)}},
		{"one zone: inserts the priced line", ShippingOnlyIfZones, []pgReply{lock, pgOne([]byte(pgOneZone)), {}, {}, total},
			[]string{pgBegin, pgQuery, pgQuery, pgQuery, pgExec, pgQuery, pgCommit}, 3,
			[]driver.Value{pgIntakeID, ShippingSKU, "Envío — Providencia", 1, float64(3000)}},
		{"one zone: updates a stale line by row id", ShippingAlways,
			[]pgReply{lock, pgOne([]byte(pgOneZone)), pgOne(int64(9), ShippingPendingLabel, int64(1), float64(0)), {}, total},
			[]string{pgBegin, pgQuery, pgQuery, pgQuery, pgExec, pgQuery, pgCommit}, 3,
			[]driver.Value{int64(9), "Envío — Providencia", 1, float64(3000)}},
		{"one zone: line already right", ShippingAlways,
			[]pgReply{lock, pgOne([]byte(pgOneZone)), pgOne(int64(9), "Envío — Providencia", int64(1), float64(3000))},
			[]string{pgBegin, pgQuery, pgQuery, pgQuery, pgCommit}, -1, nil},
		{"no zones: a hand-priced line is not overwritten", ShippingAlways,
			[]pgReply{lock, {}, pgOne(int64(9), "Envío a mano", int64(1), float64(5))},
			[]string{pgBegin, pgQuery, pgQuery, pgQuery, pgCommit}, -1, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, fake := newFakePostgres(t)
			fake.script(tc.script...)
			if err := store.EnsureShippingLine(t.Context(), pgTenant, pgIntakeID, tc.policy); err != nil {
				t.Fatalf("EnsureShippingLine: error inesperado %v", err)
			}
			requirePgKinds(t, fake, tc.wantKinds...)
			if tc.writeAt >= 0 {
				if got := fake.statements()[tc.writeAt].args; !reflect.DeepEqual(got, tc.wantWrite) {
					t.Errorf("argumentos de la escritura = %v, quería %v", got, tc.wantWrite)
				}
			}
		})
	}
}

// TestPostgres_EnsureShippingLine_Errors: cada fallo sale con su prefijo y revierte la transacción.
func TestPostgres_EnsureShippingLine_Errors(t *testing.T) {
	lock := pgOne(true)
	zone := pgOne([]byte(pgOneZone))
	stale := pgOne(int64(9), ShippingPendingLabel, int64(1), float64(0))
	cases := []struct {
		name   string
		script []pgReply
		prefix string
	}{
		{"lock fails", []pgReply{{err: errPgBoom}}, "intakes: bloquear la solicitud: "},
		{"zones read fails", []pgReply{lock, {err: errPgBoom}}, "intakes: leer las zonas de envío del tenant: "},
		{"line read fails", []pgReply{lock, {}, {err: errPgBoom}}, "intakes: leer la línea de envío: "},
		{"insert fails", []pgReply{lock, {}, {}, {err: errPgBoom}}, "intakes: insertar la línea de envío: "},
		{"update fails", []pgReply{lock, zone, stale, {err: errPgBoom}}, "intakes: actualizar la línea de envío: "},
		{"total recompute fails", []pgReply{lock, {}, {}, {}, {err: errPgBoom}}, "intakes: leer solicitud: "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, fake := newFakePostgres(t)
			fake.script(tc.script...)
			err := store.EnsureShippingLine(t.Context(), pgTenant, pgIntakeID, ShippingAlways)
			requirePgWrapped(t, err, tc.prefix, errPgBoom)
			kinds := fake.kinds()
			if kinds[len(kinds)-1] != pgRollback {
				t.Errorf("conversación = %v, quería acabar en rollback", kinds)
			}
		})
	}
}

// TestPostgres_ShippingZones: una lectura suelta. Sin fila de config, (nil, nil); con fila, las
// zonas interpretadas; un JSON ilegible es el error de ParseShippingZones tal cual.
func TestPostgres_ShippingZones(t *testing.T) {
	store, fake := newFakePostgres(t)
	if got, err := store.ShippingZones(t.Context(), pgTenant); err != nil || got != nil {
		t.Errorf("ShippingZones sin config = (%#v, %v), quería (nil, nil)", got, err)
	}
	if stmt := fake.statements()[0]; stmt.inTx || !reflect.DeepEqual(stmt.args, []driver.Value{pgTenant}) {
		t.Errorf("sentencia = %+v, quería una suelta con el tenant", stmt)
	}

	fake.script(pgOne([]byte(pgOneZone)))
	got, err := store.ShippingZones(t.Context(), pgTenant)
	if want := []ShippingZone{{Code: "z1", Label: "Providencia", Price: 3000}}; err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("ShippingZones = (%+v, %v), quería (%+v, nil)", got, err, want)
	}

	fake.script(pgOne([]byte(`{"no":"es una lista"}`)))
	_, err = store.ShippingZones(t.Context(), pgTenant)
	if _, parseErr := ParseShippingZones([]byte(`{"no":"es una lista"}`)); err == nil || err.Error() != parseErr.Error() {
		t.Errorf("ShippingZones con JSON ilegible = %v, quería el error de ParseShippingZones (%v)", err, parseErr)
	}

	fake.script(pgReply{err: errPgBoom})
	got, err = store.ShippingZones(t.Context(), pgTenant)
	requirePgWrapped(t, err, "intakes: leer las zonas de envío del tenant: ", errPgBoom)
	if got != nil {
		t.Errorf("con error devolvió %+v, quería nil", got)
	}
}
