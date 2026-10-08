package intakes

import (
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"testing"
)

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

// Las sentencias, escritas APARTE y byte a byte (sangría y saltos de línea incluidos): son las
// del paquete viejo, y un cambio en el SQL de producción tiene que romper aquí.

// wantApprovedTextsSQL es la lectura de las cotizaciones aprobadas, con el blanco EXPLÍCITO en el btrim.
const wantApprovedTextsSQL = `
	SELECT r.rendered_text
	FROM public.intake_revisions r
	JOIN public.intakes i ON i.id = r.intake_id
	WHERE i.tenant_id = $1
	  AND r.kind = $2
	  AND r.rendered_text IS NOT NULL
	  -- El conjunto de caracteres va EXPLÍCITO: btrim(x) a secas solo quita ESPACIOS,
	  -- mientras que el strings.TrimSpace del doble en memoria quita todo el blanco.
	  -- Con un texto de espacios y un salto de línea, Go lo descartaba y Postgres lo
	  -- dejaba pasar: el doble afirmaba una paridad que no existía. Lo cazó el test de
	  -- integración; el unitario contra el doble no podía verlo.
	  AND btrim(r.rendered_text, E' \t\n\r\f\v') <> ''
	ORDER BY r.created_at DESC, r.revision_no DESC
	LIMIT $3`

// TestPostgres_ApprovedRenderedTexts_SQLIsTheOldOneByteForByte: la consulta sale con el texto del
// paquete viejo, incluido el conjunto de caracteres en blanco del btrim (la paridad con
// strings.TrimSpace del doble en memoria).
func TestPostgres_ApprovedRenderedTexts_SQLIsTheOldOneByteForByte(t *testing.T) {
	store, fake := newFakePostgres(t)
	if _, err := store.ApprovedRenderedTexts(t.Context(), pgTenant, 3); err != nil {
		t.Fatalf("ApprovedRenderedTexts: error inesperado %v", err)
	}
	requirePgSQL(t, fake, wantApprovedTextsSQL)
	if !strings.Contains(wantApprovedTextsSQL, `btrim(r.rendered_text, E' \t\n\r\f\v') <> ''`) {
		t.Errorf("la consulta esperada no lleva el conjunto de blancos explícito")
	}
}
