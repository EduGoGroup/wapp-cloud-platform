package casebank_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/casebank"
)

// postgres_test.go — lo que el adaptador promete SIN base: el SQL que emite, sus
// argumentos y el mapeo de filas y errores, con un driver de mentira. Las reglas
// del puerto (el CHECK, la idempotencia, el aislamiento) las prueba
// casebankhelpertest.Contrato contra Postgres real en F9.

// Aserciones de compilación de lo que Postgres promete: las firmas del puerto y un
// constructor que recibe el *sql.DB y no devuelve error.
var (
	_ func(*sql.DB) *casebank.Postgres                                        = casebank.NewPostgres
	_ func(*casebank.Postgres, context.Context, casebank.Case) (int64, error) = (*casebank.Postgres).Insert
	_ func(*casebank.Postgres, context.Context, string, string) (bool, error) = (*casebank.Postgres).Exists
	_ casebank.Store                                                          = (*casebank.Postgres)(nil)
)

// Las dos sentencias del adaptador, byte a byte: son las del fichero viejo.
const (
	insertSQL = `
INSERT INTO public.intake_case_bank (tenant_id, consented, source_text, expected)
VALUES ($1, $2, $3, $4)
RETURNING id`
	existsSQL = `
SELECT EXISTS (
    SELECT 1 FROM public.intake_case_bank
     WHERE tenant_id = $1 AND source_text = $2
)`
)

// newPostgres monta el adaptador sobre el driver de mentira.
func newPostgres(t *testing.T) (*casebank.Postgres, *fakeDB) {
	t.Helper()
	fake, db := openFakeDB(t)
	return casebank.NewPostgres(db), fake
}

// requireOneStatement exige que al driver haya llegado UNA sentencia, con ese
// texto y esos argumentos.
func requireOneStatement(t *testing.T, fake *fakeDB, query string, args ...driver.Value) {
	t.Helper()
	seen := fake.seen()
	if len(seen) != 1 {
		t.Fatalf("llegaron %d sentencias, se esperaba 1: %+v", len(seen), seen)
	}
	if seen[0].query != query {
		t.Errorf("SQL emitido:\n%q\nse esperaba el del viejo, byte a byte:\n%q", seen[0].query, query)
	}
	if !reflect.DeepEqual(seen[0].args, args) {
		t.Errorf("argumentos = %#v; se esperaban %#v", seen[0].args, args)
	}
}

// TestNewPostgres_DoesNotQuery: construir el store no toca la base.
func TestNewPostgres_DoesNotQuery(t *testing.T) {
	store, fake := newPostgres(t)
	if store == nil {
		t.Fatal("NewPostgres devolvió nil")
	}
	if seen := fake.seen(); len(seen) != 0 {
		t.Errorf("construir el store emitió %d sentencias, se esperaban 0: %+v", len(seen), seen)
	}
}

// TestPostgres_Insert_EmitsTheInsertAndReturnsTheID: un INSERT … RETURNING id con
// los cuatro argumentos en orden, y el id es el que devuelve la base.
func TestPostgres_Insert_EmitsTheInsertAndReturnsTheID(t *testing.T) {
	store, fake := newPostgres(t)
	fake.answer(int64(4821))
	expected := json.RawMessage(`{"version":1}`)
	id, err := store.Insert(context.Background(), casebank.Case{
		TenantID: "t-1", Consented: true, SourceText: "quiero [NOMBRE] tortas", Expected: expected,
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if id != 4821 {
		t.Errorf("Insert devolvió id %d; se esperaba el del RETURNING, 4821", id)
	}
	requireOneStatement(t, fake, insertSQL, "t-1", true, "quiero [NOMBRE] tortas", []byte(expected))
}

// TestPostgres_Insert_EmptyExpected_TravelsAsSQLNull: nil y longitud cero viajan
// como NULL, no como un []byte vacío ni como el literal JSON `null`.
func TestPostgres_Insert_EmptyExpected_TravelsAsSQLNull(t *testing.T) {
	for name, empty := range map[string]json.RawMessage{"nil": nil, "zero length": {}} {
		t.Run(name, func(t *testing.T) {
			store, fake := newPostgres(t)
			fake.answer(int64(1))
			if _, err := store.Insert(context.Background(), casebank.Case{
				TenantID: "t-1", Consented: true, SourceText: "x", Expected: empty,
			}); err != nil {
				t.Fatalf("Insert: %v", err)
			}
			requireOneStatement(t, fake, insertSQL, "t-1", true, "x", nil)
		})
	}
}

// TestPostgres_Insert_ConsentedTravelsAsParameter: el store NO cablea `true`.
// Escribe el `false` que le dan, y quien lo rechaza es el CHECK de la base: es lo
// que mantiene viva esa red.
func TestPostgres_Insert_ConsentedTravelsAsParameter(t *testing.T) {
	store, fake := newPostgres(t)
	fake.answer(int64(1))
	if _, err := store.Insert(context.Background(), casebank.Case{
		TenantID: "t-1", Consented: false, SourceText: "x",
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	requireOneStatement(t, fake, insertSQL, "t-1", false, "x", nil)
}

// TestPostgres_Insert_Errors_Wrapped: el fallo de la sentencia y la falta de fila
// salen envueltos con el prefijo del viejo, y el id es 0.
func TestPostgres_Insert_Errors_Wrapped(t *testing.T) {
	boom := errors.New("boom")

	t.Run("statement fails", func(t *testing.T) {
		store, fake := newPostgres(t)
		fake.fail(boom)
		id, err := store.Insert(context.Background(), casebank.Case{TenantID: "t-1", Consented: true, SourceText: "x"})
		if !errors.Is(err, boom) || id != 0 {
			t.Fatalf("Insert = (%d, %v); se esperaba (0, boom envuelto)", id, err)
		}
		if got, want := err.Error(), "insertando en intake_case_bank: boom"; got != want {
			t.Errorf("error = %q, se esperaba %q", got, want)
		}
	})

	t.Run("no row returned", func(t *testing.T) {
		store, _ := newPostgres(t)
		id, err := store.Insert(context.Background(), casebank.Case{TenantID: "t-1", Consented: true, SourceText: "x"})
		if !errors.Is(err, sql.ErrNoRows) || id != 0 {
			t.Fatalf("Insert = (%d, %v); se esperaba (0, sql.ErrNoRows envuelto)", id, err)
		}
		if got, want := err.Error(), "insertando en intake_case_bank: sql: no rows in result set"; got != want {
			t.Errorf("error = %q, se esperaba %q", got, want)
		}
	})
}

// TestPostgres_Exists_EmitsTheSelectAndMapsTheBoolean: un SELECT EXISTS con
// (tenant, literal), y devuelve lo que dice la base.
func TestPostgres_Exists_EmitsTheSelectAndMapsTheBoolean(t *testing.T) {
	for _, want := range []bool{true, false} {
		store, fake := newPostgres(t)
		fake.answer(want)
		got, err := store.Exists(context.Background(), "t-1", "  literal exacto ")
		if err != nil {
			t.Fatalf("Exists: %v", err)
		}
		if got != want {
			t.Errorf("Exists = %t; la base contestó %t", got, want)
		}
		requireOneStatement(t, fake, existsSQL, "t-1", "  literal exacto ")
	}
}

// TestPostgres_Exists_Error_Wrapped: el fallo sale envuelto con el prefijo del
// viejo, y la respuesta es false.
func TestPostgres_Exists_Error_Wrapped(t *testing.T) {
	store, fake := newPostgres(t)
	boom := errors.New("boom")
	fake.fail(boom)
	found, err := store.Exists(context.Background(), "t-1", "x")
	if !errors.Is(err, boom) || found {
		t.Fatalf("Exists = (%t, %v); se esperaba (false, boom envuelto)", found, err)
	}
	if got, want := err.Error(), "consultando intake_case_bank: boom"; got != want {
		t.Errorf("error = %q, se esperaba %q", got, want)
	}
}
