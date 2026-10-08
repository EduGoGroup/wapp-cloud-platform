//go:build pendiente

package intakes

import (
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// LO QUE EL VERDE AÑADIRÁ (F6-03): el texto byte a byte de la consulta, con su conjunto de
// caracteres en blanco EXPLÍCITO en el btrim (la paridad con strings.TrimSpace del doble).

// TestPostgres_ApprovedRenderedTexts_NonPositiveLimit_NilWithoutQuerying: pedir cero ejemplos no
// es un error ni toca la base; devuelve nil.
func TestPostgres_ApprovedRenderedTexts_NonPositiveLimit_NilWithoutQuerying(t *testing.T) {
	for _, limit := range []int{0, -1} {
		store, fake := newFakePostgres(t)
		if got, err := store.ApprovedRenderedTexts(t.Context(), pgTenant, limit); err != nil || got != nil {
			t.Errorf("ApprovedRenderedTexts(limit=%d) = (%#v, %v), quería (nil, nil)", limit, got, err)
		}
		requirePgUntouched(t, fake)
	}
}

// TestPostgres_ApprovedRenderedTexts_QueriesApprovedOfTheTenant: UNA consulta suelta con el
// tenant, el tipo `approved` y el límite; por encima de la cota el límite se recorta en silencio.
// Los textos salen en el orden de la base y sin filas el slice es vacío y no nil.
func TestPostgres_ApprovedRenderedTexts_QueriesApprovedOfTheTenant(t *testing.T) {
	for limit, wantLimit := range map[int]int{3: 3, MaxApprovedTexts: MaxApprovedTexts, MaxApprovedTexts + 1: MaxApprovedTexts, 100000: MaxApprovedTexts} {
		store, fake := newFakePostgres(t)
		fake.script(pgReply{rows: [][]driver.Value{{"la más reciente"}, {"la anterior"}}})
		got, err := store.ApprovedRenderedTexts(t.Context(), pgTenant, limit)
		if want := []string{"la más reciente", "la anterior"}; err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("ApprovedRenderedTexts(limit=%d) = (%v, %v), quería (%v, nil)", limit, got, err, want)
		}
		requirePgKinds(t, fake, pgQuery)
		want := []driver.Value{pgTenant, RevisionKindApproved, wantLimit}
		if stmt := fake.statements()[0]; stmt.inTx || !reflect.DeepEqual(stmt.args, want) {
			t.Errorf("limit=%d: argumentos = %v, quería %v", limit, stmt.args, want)
		}
	}

	store, _ := newFakePostgres(t)
	if got, err := store.ApprovedRenderedTexts(t.Context(), pgTenant, 3); err != nil || got == nil || len(got) != 0 {
		t.Errorf("sin filas = (%#v, %v), quería un slice vacío no nil", got, err)
	}
}

// TestPostgres_ApprovedRenderedTexts_Errors: los cuatro fallos, con su prefijo y sin textos a medias.
func TestPostgres_ApprovedRenderedTexts_Errors(t *testing.T) {
	closeBoom := errors.New("cierre roto")
	cases := []struct {
		name   string
		reply  pgReply
		prefix string
		cause  error
	}{
		{"query fails", pgReply{err: errPgBoom}, "intakes: listar cotizaciones aprobadas del tenant: ", errPgBoom},
		{"iteration fails", pgReply{rows: [][]driver.Value{{"una"}}, endErr: errPgBoom}, "intakes: recorrer cotizaciones aprobadas: ", errPgBoom},
		{"close fails", pgReply{closeErr: closeBoom}, "intakes: cerrar filas de cotizaciones aprobadas: ", closeBoom},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, fake := newFakePostgres(t)
			fake.script(tc.reply)
			got, err := store.ApprovedRenderedTexts(t.Context(), pgTenant, 3)
			requirePgWrapped(t, err, tc.prefix, tc.cause)
			if got != nil {
				t.Errorf("con error devolvió %v, quería nil", got)
			}
		})
	}
	t.Run("row cannot be scanned", func(t *testing.T) {
		store, fake := newFakePostgres(t)
		fake.script(pgOne(nil))
		got, err := store.ApprovedRenderedTexts(t.Context(), pgTenant, 3)
		if err == nil || !strings.HasPrefix(err.Error(), "intakes: leer cotización aprobada: ") || got != nil {
			t.Errorf("= (%v, %v), quería (nil, error con prefijo %q)", got, err, "intakes: leer cotización aprobada: ")
		}
	})
}
