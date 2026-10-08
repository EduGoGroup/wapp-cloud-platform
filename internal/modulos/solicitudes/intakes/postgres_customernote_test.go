package intakes

import (
	"database/sql/driver"
	"reflect"
	"strings"
	"testing"
)

// TestPostgres_GetCustomerNote_FoundAndNotFound: la nota de una solicitud del tenant sale con
// found=true aunque esté vacía; sin fila —no existe o es de otro tenant— es found=false SIN error.
func TestPostgres_GetCustomerNote_FoundAndNotFound(t *testing.T) {
	cases := []struct {
		name      string
		reply     pgReply
		wantNote  string
		wantFound bool
	}{
		{"with note", pgOne("sin cebolla"), "sin cebolla", true},
		{"empty note is still found", pgOne(""), "", true},
		{"no row", pgReply{}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, fake := newFakePostgres(t)
			fake.script(tc.reply)
			note, found, err := store.GetCustomerNote(t.Context(), pgTenant, pgIntakeID)
			if err != nil || note != tc.wantNote || found != tc.wantFound {
				t.Errorf("GetCustomerNote = (%q, %v, %v), quería (%q, %v, nil)", note, found, err, tc.wantNote, tc.wantFound)
			}
			requirePgKinds(t, fake, pgQuery)
			if stmt := fake.statements()[0]; stmt.inTx || !reflect.DeepEqual(stmt.args, []driver.Value{pgTenant, pgIntakeID}) {
				t.Errorf("sentencia = %+v, quería una suelta acotada por tenant e id", stmt)
			}
		})
	}
}

// TestPostgres_GetCustomerNote_InvalidID_NotFoundWithoutQuerying: un id que no es UUID es el mismo
// «no encontrada» opaco, sin error y sin tocar la base.
func TestPostgres_GetCustomerNote_InvalidID_NotFoundWithoutQuerying(t *testing.T) {
	store, fake := newFakePostgres(t)
	note, found, err := store.GetCustomerNote(t.Context(), pgTenant, "no-es-un-uuid")
	if err != nil || found || note != "" {
		t.Errorf("GetCustomerNote = (%q, %v, %v), quería (\"\", false, nil)", note, found, err)
	}
	requirePgUntouched(t, fake)
}

// TestPostgres_GetCustomerNote_Error_NamesTheIntakeNotTheNote: el fallo de la base sale envuelto
// con la solicitud en el prefijo; la nota no aparece en ningún error.
func TestPostgres_GetCustomerNote_Error_NamesTheIntakeNotTheNote(t *testing.T) {
	store, fake := newFakePostgres(t)
	fake.script(pgReply{err: errPgBoom})
	note, found, err := store.GetCustomerNote(t.Context(), pgTenant, pgIntakeID)
	requirePgWrapped(t, err, "intakes: leer la indicación del cliente de la solicitud "+pgIntakeID+": ", errPgBoom)
	if found || note != "" {
		t.Errorf("con error devolvió (%q, %v), quería (\"\", false)", note, found)
	}

	// Una fila ilegible tampoco arrastra la nota al error.
	fake.script(pgOne(nil))
	_, _, err = store.GetCustomerNote(t.Context(), pgTenant, pgIntakeID)
	if err == nil || strings.Contains(err.Error(), "cebolla") {
		t.Errorf("error = %v, quería un fallo que no cite la nota", err)
	}
}

// Las sentencias, escritas APARTE y byte a byte (sangría y saltos de línea incluidos): son las
// del paquete viejo, y un cambio en el SQL de producción tiene que romper aquí.

// wantCustomerNoteSQL es la lectura de la indicación del cliente, acotada por tenant e id.
const wantCustomerNoteSQL = `
		SELECT customer_note
		FROM public.intakes
		WHERE tenant_id = $1 AND id = $2
	`

// TestPostgres_GetCustomerNote_SQLIsTheOldOneByteForByte: la lectura sale con el texto del paquete
// viejo.
func TestPostgres_GetCustomerNote_SQLIsTheOldOneByteForByte(t *testing.T) {
	store, fake := newFakePostgres(t)
	if _, _, err := store.GetCustomerNote(t.Context(), pgTenant, pgIntakeID); err != nil {
		t.Fatalf("GetCustomerNote: error inesperado %v", err)
	}
	requirePgSQL(t, fake, wantCustomerNoteSQL)
}
