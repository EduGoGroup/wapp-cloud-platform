package intakes_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// Tests de integración de la REVALIDACIÓN (T4.9, D-041.25) contra Postgres real.
// Lo que solo se puede comprobar aquí y no en memoria:
//
//   - que el CHECK de `kind` de la migración 0045 admite `revalidated` (ya lo
//     admitía: esta tarea NO tuvo que ampliarlo, y este test es la prueba);
//   - que la escritura es QUIRÚRGICA de verdad — el `added_at` de las líneas que
//     sobreviven no se mueve, así que el pedido no se le reordena al cliente;
//   - que el índice único parcial de `_shipping` no se dispara y esa fila no se toca;
//   - que las líneas, el total de la cabecera y la revisión se confirman en la MISMA
//     transacción;
//   - que el payload que queda EN LA TABLA no lleva PII.

// solicitudRescatablePG siembra una solicitud `open` con tres líneas de cliente
// fechadas en orden y devuelve el store, su id y los `added_at` sembrados.
func solicitudRescatablePG(t *testing.T, db *sql.DB, tenant string) (*intakes.Postgres, string, []time.Time) {
	t.Helper()
	id := uuid.NewString()
	seedPG(t, db, tenant, []fixture{{id, intakes.StatusOpen, "sess-a", 1}})

	añadidos := []time.Time{
		time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 1, 1, 10, 5, 0, 0, time.UTC),
		time.Date(2026, 1, 1, 10, 9, 0, 0, time.UTC),
	}
	líneas := []struct {
		sku, label, custom string
		qty                int
		price              float64
	}{
		{"PAN", "Pan", "sin sal", 2, 2.00},
		{"QUESO", "Queso", "para la Sra. Marta Pérez, Av. Siempre Viva 742", 1, 3.00},
		{"LECHE", "Leche", "", 1, 1.00},
	}
	for i, l := range líneas {
		if _, err := db.ExecContext(context.Background(), `
			INSERT INTO public.intake_items (intake_id, sku, label, customization, qty, unit_price, added_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
		`, id, l.sku, l.label, l.custom, l.qty, l.price, añadidos[i]); err != nil {
			t.Fatalf("sembrando la línea %s: %v", l.sku, err)
		}
	}
	return intakes.NewPostgres(db), id, añadidos
}

// añadidosDe lee los `added_at` de las líneas en el orden en que se leen.
func añadidosDe(t *testing.T, db *sql.DB, intakeID string) map[string]time.Time {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `
		SELECT sku, added_at FROM public.intake_items WHERE intake_id = $1 ORDER BY added_at, id
	`, intakeID)
	if err != nil {
		t.Fatalf("leyendo added_at: %v", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			t.Logf("cerrando filas de added_at: %v", cerr)
		}
	}()

	out := map[string]time.Time{}
	for rows.Next() {
		var sku string
		var at time.Time
		if err := rows.Scan(&sku, &at); err != nil {
			t.Fatalf("escaneando added_at: %v", err)
		}
		out[sku] = at
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("recorriendo added_at: %v", err)
	}
	return out
}

// TestPG_ApplyRevalidation_EscenaDeMarta: contra la tabla real, con su CHECK de
// `kind` y su índice único parcial. Se re-precia el pan, se retira el queso, se deja
// la leche y el total de la CABECERA queda cuadrado.
func TestPG_ApplyRevalidation_EscenaDeMarta(t *testing.T) {
	db := openTestDB(t)
	tenant := uuid.NewString()
	st, id, añadidos := solicitudRescatablePG(t, db, tenant)

	previas, err := st.Get(context.Background(), tenant, id)
	if err != nil {
		t.Fatalf("Get previo: %v", err)
	}
	rv := intakes.Revalidate(previas.Items, intakes.NewPriceList(map[string]intakes.CatalogEntry{
		"PAN":   {Label: "Pan de masa madre", Price: 2.50},
		"LECHE": {Label: "Leche", Price: 1.00},
	}))

	detail, err := st.ApplyRevalidation(context.Background(), tenant, id, rv,
		"Recuperé tu pedido del 1 de enero 🧾", intakes.StoredVariants(intakes.StatusOpen))
	if err != nil {
		t.Fatalf("ApplyRevalidation: %v", err)
	}

	if len(detail.Items) != 2 {
		t.Fatalf("líneas=%+v; quiero 2 (pan y leche)", detail.Items)
	}
	if detail.Items[0].SKU != "PAN" || detail.Items[0].UnitPrice != 2.50 || detail.Items[0].Label != "Pan de masa madre" {
		t.Fatalf("el pan no se re-preció: %+v", detail.Items[0])
	}
	if detail.Items[0].Customization != "sin sal" {
		t.Fatalf("el re-precio se llevó la indicación de la línea: %q", detail.Items[0].Customization)
	}
	if detail.Items[1].SKU != "LECHE" {
		t.Fatalf("la segunda línea es %q; quiero LECHE", detail.Items[1].SKU)
	}

	// El total de la CABECERA en BD, no solo el que devolvió la llamada.
	cabeceraEs(t, db, id, 6, intakes.StatusOpen)

	// La escritura fue QUIRÚRGICA: el `added_at` de lo que sobrevive no se movió.
	ahora := añadidosDe(t, db, id)
	if !ahora["PAN"].Equal(añadidos[0]) || !ahora["LECHE"].Equal(añadidos[2]) {
		t.Fatalf("el added_at se movió (PAN=%v LECHE=%v); quiero %v y %v — un DELETE+INSERT le reordenaría el pedido al cliente",
			ahora["PAN"], ahora["LECHE"], añadidos[0], añadidos[2])
	}
	if _, sigue := ahora["QUESO"]; sigue {
		t.Fatal("el queso retirado sigue en la tabla")
	}

	// 8.00 antes (2×2.00 + 1×3.00 + 1×1.00) y 6.00 después.
	revisiónÚnica(t, db, id, "Recuperé tu pedido del 1 de enero 🧾", 8, 6)
}

// revisiónÚnica lee la ÚNICA revisión de la solicitud y comprueba todo lo que la
// tabla real puede decir de ella: que el CHECK de `kind` de la 0045 aceptó
// `revalidated`, que la firma `system`, que su `rendered_text` es byte a byte el
// texto que se mandó y que su payload no lleva PII (criterios (f) y (g) del plan,
// comprobados BUSCANDO el dato en lo persistido).
func revisiónÚnica(t *testing.T, db *sql.DB, intakeID, mandado string, antes, después float64) {
	t.Helper()
	var (
		kind, createdBy, rendered string
		revNo                     int
		payload                   []byte
	)
	if err := db.QueryRowContext(context.Background(), `
		SELECT revision_no, kind, created_by, rendered_text, payload
		FROM public.intake_revisions WHERE intake_id = $1 ORDER BY revision_no
	`, intakeID).Scan(&revNo, &kind, &createdBy, &rendered, &payload); err != nil {
		t.Fatalf("leyendo la revisión: %v", err)
	}
	if revNo != 1 || kind != intakes.RevisionKindRevalidated || createdBy != intakes.RevisionBySystem {
		t.Fatalf("revisión no=%d kind=%q created_by=%q; quiero 1/revalidated/system", revNo, kind, createdBy)
	}
	if rendered != mandado {
		t.Fatalf("rendered_text=%q; tiene que ser byte a byte %q", rendered, mandado)
	}
	for _, aguja := range []string{"Marta", "Pérez", "Siempre Viva", "742", "sin sal", "customization"} {
		if strings.Contains(string(payload), aguja) {
			t.Fatalf("el payload en BD lleva %q: %s", aguja, payload)
		}
	}

	// Los totales se comprueban PARSEANDO, no por substring: la columna es JSONB y
	// Postgres la reescribe canonicalizada —reordena las claves y mete un espacio
	// tras los dos puntos—, así que `"total_before":8` no aparece nunca tal cual.
	var got struct {
		TotalBefore float64 `json:"total_before"`
		TotalAfter  float64 `json:"total_after"`
	}
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("payload ilegible: %v (%s)", err, payload)
	}
	if got.TotalBefore != antes || got.TotalAfter != después {
		t.Fatalf("payload total_before=%v total_after=%v; quiero %v y %v", got.TotalBefore, got.TotalAfter, antes, después)
	}
}

// TestPG_ApplyRevalidation_ElEnvíoNoSeToca es el criterio (d) contra la tabla: la
// línea de la plataforma sobrevive con el precio que le puso el dueño aunque el
// catálogo se haya llevado todas las del cliente, y el índice único parcial de la
// 0045 no se dispara.
func TestPG_ApplyRevalidation_ElEnvíoNoSeToca(t *testing.T) {
	db := openTestDB(t)
	tenant := uuid.NewString()
	st, id, _ := solicitudRescatablePG(t, db, tenant)

	if err := st.EnsureShippingLine(context.Background(), tenant, id, intakes.ShippingAlways); err != nil {
		t.Fatalf("materializando la línea de envío: %v", err)
	}
	// El dueño le pone precio a mano: es literalmente lo que dice D-041.11 en v1.
	if _, err := db.ExecContext(context.Background(), `
		UPDATE public.intake_items SET unit_price = 4, label = 'Envío — Providencia'
		WHERE intake_id = $1 AND sku = $2
	`, id, intakes.ShippingSKU); err != nil {
		t.Fatalf("precificando el envío: %v", err)
	}

	previas, err := st.Get(context.Background(), tenant, id)
	if err != nil {
		t.Fatalf("Get previo: %v", err)
	}
	// Catálogo LEÍDO y vacío: se lleva las tres líneas de cliente.
	rv := intakes.Revalidate(previas.Items, intakes.NewPriceList(nil))

	detail, err := st.ApplyRevalidation(context.Background(), tenant, id, rv,
		"ya no queda nada", intakes.StoredVariants(intakes.StatusOpen))
	if err != nil {
		t.Fatalf("ApplyRevalidation: %v", err)
	}

	cliente, sistema := contarLíneas(t, db, id)
	if cliente != 0 || sistema != 1 {
		t.Fatalf("líneas cliente=%d sistema=%d; quiero 0 y 1 (el envío ni se borra ni se duplica)", cliente, sistema)
	}
	if len(detail.Items) != 1 || detail.Items[0].UnitPrice != 4 || detail.Items[0].Label != "Envío — Providencia" {
		t.Fatalf("el envío se tocó: %+v", detail.Items)
	}
	// Criterio (i) contra la tabla: sin líneas de cliente, sigue `open` y su total es
	// el del envío. La mata un humano, no el catálogo.
	cabeceraEs(t, db, id, 4, intakes.StatusOpen)
}

// cabeceraEs comprueba el total y el estado de la solicitud EN LA TABLA, no el que
// devolvió la llamada: es la diferencia entre "la función calculó bien" y "quedó
// escrito".
func cabeceraEs(t *testing.T, db *sql.DB, intakeID string, total float64, status string) {
	t.Helper()
	var (
		gotTotal  float64
		gotStatus string
	)
	if err := db.QueryRowContext(context.Background(),
		`SELECT total, status FROM public.intakes WHERE id = $1`, intakeID).Scan(&gotTotal, &gotStatus); err != nil {
		t.Fatalf("leyendo la cabecera: %v", err)
	}
	if gotTotal != total {
		t.Fatalf("total en BD=%v; quiero %v", gotTotal, total)
	}
	if gotStatus != status {
		t.Fatalf("status=%q; quiero %q (la revalidación NO mueve el estado, E-7)", gotStatus, status)
	}
}

// TestPG_ApplyRevalidation_LaSolicitudSeMovió: el CAS de estado. Si alguien la
// confirmó entre el cálculo del diff y la escritura, no se escribe NADA.
func TestPG_ApplyRevalidation_LaSolicitudSeMovió(t *testing.T) {
	db := openTestDB(t)
	tenant := uuid.NewString()
	st, id, _ := solicitudRescatablePG(t, db, tenant)

	previas, err := st.Get(context.Background(), tenant, id)
	if err != nil {
		t.Fatalf("Get previo: %v", err)
	}
	rv := intakes.Revalidate(previas.Items, intakes.NewPriceList(nil))

	if _, err := db.ExecContext(context.Background(),
		`UPDATE public.intakes SET status = 'confirmed' WHERE id = $1`, id); err != nil {
		t.Fatalf("moviendo la solicitud: %v", err)
	}

	if _, err := st.ApplyRevalidation(context.Background(), tenant, id, rv,
		"aviso", intakes.StoredVariants(intakes.StatusOpen)); err == nil {
		t.Fatal("quiero ErrConflict: la solicitud ya no está donde se validó")
	}
	cliente, _ := contarLíneas(t, db, id)
	if cliente != 3 {
		t.Fatalf("líneas de cliente=%d; el rechazo NO puede escribir nada", cliente)
	}
	var revisiones int
	if err := db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM public.intake_revisions WHERE intake_id = $1`, id).Scan(&revisiones); err != nil {
		t.Fatalf("contando revisiones: %v", err)
	}
	if revisiones != 0 {
		t.Fatalf("revisiones=%d; el rechazo NO escribe revisión", revisiones)
	}
}
