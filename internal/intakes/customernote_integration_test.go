// customernote_integration_test.go cubre GetCustomerNote contra Postgres real: es
// la lectura de la que depende que el puente CRM siga recibiendo la indicación del
// cliente después de que la Ola 5 la sacara del payload persistido. Si esta
// consulta miente, el arreglo de la fuga se convierte en una pérdida de dato — y
// eso no lo puede acreditar un store en memoria, porque la mitad de la lógica es
// el WHERE.
package intakes_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// TestPostgres_GetCustomerNote cubre los cuatro caminos de la función en un solo
// fixture, cada uno en su subtest para que un fallo diga cuál se rompió.
func TestPostgres_GetCustomerNote(t *testing.T) {
	db := openTestDB(t)
	store := intakes.NewPostgres(db)
	tenant := uuid.NewString()
	ctx := context.Background()

	conNota, sinNota := uuid.NewString(), uuid.NewString()
	seedPG(t, db, tenant, []fixture{
		{conNota, intakes.StatusConfirmed, "sess-a", 2},
		{sinNota, intakes.StatusConfirmed, "sess-a", 1},
	})
	if _, err := db.ExecContext(ctx,
		`UPDATE public.intakes SET customer_note = $2 WHERE id = $1`, conNota, notaDePedido); err != nil {
		t.Fatalf("sembrando la indicación del pedido: %v", err)
	}

	t.Run("la devuelve", func(t *testing.T) {
		note, found, err := store.GetCustomerNote(ctx, tenant, conNota)
		if err != nil {
			t.Fatalf("GetCustomerNote: %v", err)
		}
		if !found || note != notaDePedido {
			t.Fatalf("note=%q found=%v; quiero %q y true — sin esto el puente pierde la "+
				"indicación que quien prepara el pedido necesita leer", note, found, notaDePedido)
		}
	})

	t.Run("sin nota es cadena vacía, no NULL", func(t *testing.T) {
		note, found, err := store.GetCustomerNote(ctx, tenant, sinNota)
		if err != nil {
			t.Fatalf("GetCustomerNote: %v", err)
		}
		if !found || note != "" {
			t.Fatalf("note=%q found=%v; la columna es NOT NULL con la cadena vacía por "+
				"defecto (0045): «sin nota» y «vacía» son el mismo caso", note, found)
		}
	})

	t.Run("de otro tenant no existe", func(t *testing.T) {
		note, found, err := store.GetCustomerNote(ctx, uuid.NewString(), conNota)
		if err != nil {
			t.Fatalf("GetCustomerNote: %v", err)
		}
		if found || note != "" {
			t.Fatalf("note=%q found=%v: la nota de una empresa cruzó a otra (INV-8)", note, found)
		}
	})

	t.Run("id que no es UUID no revienta", func(t *testing.T) {
		note, found, err := store.GetCustomerNote(ctx, tenant, "i-1")
		if err != nil {
			t.Fatalf("un intake_id malformado debe ser un 404 opaco, no un error de Postgres: %v", err)
		}
		if found || note != "" {
			t.Fatalf("note=%q found=%v", note, found)
		}
	})
}
