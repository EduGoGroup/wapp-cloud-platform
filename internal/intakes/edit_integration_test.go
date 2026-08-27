package intakes_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// Tests de integración de la EDICIÓN MANUAL (T4.10) contra Postgres real. Lo que
// solo se puede comprobar aquí y no en memoria: que el índice único parcial de
// `_shipping` (migración 0045) no se dispare, que la revisión y las líneas se
// confirmen en la MISMA transacción, y que el DELETE por prefijo respete de verdad
// las filas del sistema.

// solicitudPorAprobarPG siembra una solicitud del tenant en `pending_approval` con
// una línea de catálogo (1 × 8) y le materializa la línea de envío por el mismo
// camino que producción (EnsureShippingLine con la política del presupuesto).
// Devuelve el store y el id.
func solicitudPorAprobarPG(t *testing.T, db *sql.DB, tenant string) (*intakes.Postgres, string) {
	t.Helper()
	id := uuid.NewString()
	seedPG(t, db, tenant, []fixture{{id, intakes.StatusPendingApproval, "sess-a", 6}})
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO public.intake_items (intake_id, sku, label, customization, qty, unit_price)
		VALUES ($1, 'HAMB', 'Hamburguesa', 'con queso extra', 1, 8)
	`, id); err != nil {
		t.Fatalf("sembrando la línea del cliente: %v", err)
	}
	st := intakes.NewPostgres(db)
	if err := st.EnsureShippingLine(context.Background(), tenant, id, intakes.ShippingAlways); err != nil {
		t.Fatalf("materializando la línea de envío: %v", err)
	}
	return st, id
}

// contarLíneas cuenta las líneas de la solicitud, separando las del sistema.
func contarLíneas(t *testing.T, db *sql.DB, intakeID string) (cliente, sistema int) {
	t.Helper()
	if err := db.QueryRowContext(context.Background(), `
		SELECT count(*) FILTER (WHERE left(sku,1) <> '_'),
		       count(*) FILTER (WHERE left(sku,1)  = '_')
		FROM public.intake_items WHERE intake_id = $1
	`, intakeID).Scan(&cliente, &sistema); err != nil {
		t.Fatalf("contando líneas: %v", err)
	}
	return cliente, sistema
}

// TestPG_ReplaceItems_EscenaDelQuesoExtra: la edición sustituye las líneas del
// cliente, deja la de envío intacta, cuadra el total y escribe UNA revisión
// `corrected` de `owner` — contra la tabla real, con su índice único parcial y su
// CHECK de `kind` (que ya admitía `corrected` desde la 0045: esta tarea NO amplió
// la lista).
func TestPG_ReplaceItems_EscenaDelQuesoExtra(t *testing.T) {
	db := openTestDB(t)
	tenant := uuid.NewString()
	st, id := solicitudPorAprobarPG(t, db, tenant)

	detail, err := st.ReplaceItems(context.Background(), tenant, id, []intakes.Item{
		{SKU: "HAMB", Label: "Hamburguesa", Customization: "con queso extra", Qty: 1, UnitPrice: 8},
		{SKU: "QUESO-EX", Label: "Queso extra", Qty: 1, UnitPrice: 1},
	}, intakes.StoredVariants(intakes.StatusPendingApproval), intakes.EditPlain)
	if err != nil {
		t.Fatalf("ReplaceItems: %v", err)
	}

	if detail.Total != 9 {
		t.Fatalf("total=%v, quiero 9 (8 + 1 + 0 del envío por confirmar)", detail.Total)
	}
	cliente, sistema := contarLíneas(t, db, id)
	if cliente != 2 || sistema != 1 {
		t.Fatalf("líneas cliente=%d sistema=%d; quiero 2 y 1 (el envío ni se duplica ni se borra)", cliente, sistema)
	}

	// El total de la CABECERA en BD, no solo el que devolvió la llamada.
	var total float64
	if err := db.QueryRowContext(context.Background(),
		`SELECT total FROM public.intakes WHERE id = $1`, id).Scan(&total); err != nil {
		t.Fatalf("leyendo el total: %v", err)
	}
	if total != 9 {
		t.Fatalf("total en BD=%v, quiero 9", total)
	}

	// La revisión, con su payload cuadrado, en la misma transacción.
	var (
		kind, createdBy string
		revNo           int
		payload         []byte
	)
	if err := db.QueryRowContext(context.Background(), `
		SELECT revision_no, kind, created_by, payload FROM public.intake_revisions
		WHERE intake_id = $1 ORDER BY revision_no
	`, id).Scan(&revNo, &kind, &createdBy, &payload); err != nil {
		t.Fatalf("leyendo la revisión: %v", err)
	}
	if revNo != 1 || kind != intakes.RevisionKindCorrected || createdBy != intakes.RevisionByOwner {
		t.Fatalf("revisión no=%d kind=%q created_by=%q; quiero 1/corrected/owner", revNo, kind, createdBy)
	}
	var foto struct {
		Version int     `json:"version"`
		Total   float64 `json:"total"`
		Items   []struct {
			SKU string `json:"sku"`
		} `json:"items"`
	}
	if err := json.Unmarshal(payload, &foto); err != nil {
		t.Fatalf("payload ilegible: %v; raw=%s", err, payload)
	}
	if foto.Total != 9 || len(foto.Items) != 3 {
		t.Fatalf("foto total=%v con %d líneas; quiero 9 y las 3 (envío incluido)", foto.Total, len(foto.Items))
	}
}

// TestPG_ReplaceItems_NoDuplicaElEnvío: repetir la edición N veces deja SIEMPRE una
// sola línea de envío. Si el reemplazo intentara reinsertarla, el índice único
// parcial de la 0045 convertiría el intento en un error de escritura — y este test
// es quien lo vería.
func TestPG_ReplaceItems_NoDuplicaElEnvío(t *testing.T) {
	db := openTestDB(t)
	tenant := uuid.NewString()
	st, id := solicitudPorAprobarPG(t, db, tenant)

	for i := 0; i < 3; i++ {
		if _, err := st.ReplaceItems(context.Background(), tenant, id, []intakes.Item{
			{SKU: "HAMB", Label: "Hamburguesa", Qty: i + 1, UnitPrice: 8},
		}, intakes.StoredVariants(intakes.StatusPendingApproval), intakes.EditPlain); err != nil {
			t.Fatalf("edición %d: %v", i+1, err)
		}
		cliente, sistema := contarLíneas(t, db, id)
		if cliente != 1 || sistema != 1 {
			t.Fatalf("tras la edición %d: cliente=%d sistema=%d; quiero 1 y 1", i+1, cliente, sistema)
		}
	}

	// Tres actos, tres revisiones numeradas: la auditoría no se pierde ninguna.
	var revisiones int
	if err := db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM public.intake_revisions WHERE intake_id = $1`, id).Scan(&revisiones); err != nil {
		t.Fatalf("contando revisiones: %v", err)
	}
	if revisiones != 3 {
		t.Fatalf("revisiones=%d, quiero 3", revisiones)
	}
}

// TestPG_ReplaceItems_ElEnvíoPrecificadoAManoSobrevive: el dueño le pone precio al
// «Envío por confirmar» y después edita las líneas. El precio que puso a mano
// tiene que seguir ahí (D-041.11: v1, el dueño precifica) y contar en el total.
func TestPG_ReplaceItems_ElEnvíoPrecificadoAManoSobrevive(t *testing.T) {
	db := openTestDB(t)
	tenant := uuid.NewString()
	st, id := solicitudPorAprobarPG(t, db, tenant)

	if _, err := db.ExecContext(context.Background(), `
		UPDATE public.intake_items SET unit_price = 3 WHERE intake_id = $1 AND sku = $2
	`, id, intakes.ShippingSKU); err != nil {
		t.Fatalf("precificando el envío a mano: %v", err)
	}

	detail, err := st.ReplaceItems(context.Background(), tenant, id, []intakes.Item{
		{SKU: "HAMB", Label: "Hamburguesa", Qty: 2, UnitPrice: 8},
	}, intakes.StoredVariants(intakes.StatusPendingApproval), intakes.EditPlain)
	if err != nil {
		t.Fatalf("ReplaceItems: %v", err)
	}
	if detail.Total != 19 {
		t.Fatalf("total=%v, quiero 19 (2×8 + 3 del envío puesto a mano)", detail.Total)
	}
	var precio float64
	if err := db.QueryRowContext(context.Background(), `
		SELECT unit_price FROM public.intake_items WHERE intake_id = $1 AND sku = $2
	`, id, intakes.ShippingSKU).Scan(&precio); err != nil {
		t.Fatalf("leyendo el precio del envío: %v", err)
	}
	if precio != 3 {
		t.Fatalf("el envío quedó a %v; la edición no puede borrar el precio del dueño", precio)
	}
}

// TestPG_ReplaceItems_ConflictoNoEscribeNada: si la solicitud ya no está en el
// estado esperado, la transacción entera se descarta — ni líneas, ni revisión.
func TestPG_ReplaceItems_ConflictoNoEscribeNada(t *testing.T) {
	db := openTestDB(t)
	tenant := uuid.NewString()
	st, id := solicitudPorAprobarPG(t, db, tenant)

	_, err := st.ReplaceItems(context.Background(), tenant, id, []intakes.Item{
		{SKU: "OTRO", Label: "Otro", Qty: 1, UnitPrice: 100},
	}, intakes.StoredVariants(intakes.StatusOpen), intakes.EditPlain) // esperaba `open`; está en pending_approval
	if !errors.Is(err, intakes.ErrConflict) {
		t.Fatalf("err=%v, quiero ErrConflict", err)
	}

	cliente, sistema := contarLíneas(t, db, id)
	if cliente != 1 || sistema != 1 {
		t.Fatalf("cliente=%d sistema=%d; el conflicto no puede haber escrito nada", cliente, sistema)
	}
	var revisiones int
	if err := db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM public.intake_revisions WHERE intake_id = $1`, id).Scan(&revisiones); err != nil {
		t.Fatalf("contando revisiones: %v", err)
	}
	if revisiones != 0 {
		t.Fatalf("revisiones=%d; una edición en conflicto no deja rastro", revisiones)
	}
}

// TestPG_ReplaceItems_AisladoPorTenant: una solicitud de otro tenant no existe
// (INV-8), y su contenido queda intacto.
func TestPG_ReplaceItems_AisladoPorTenant(t *testing.T) {
	db := openTestDB(t)
	tenant := uuid.NewString()
	st, id := solicitudPorAprobarPG(t, db, tenant)

	_, err := st.ReplaceItems(context.Background(), uuid.NewString(), id, []intakes.Item{
		{SKU: "OTRO", Label: "Otro", Qty: 1, UnitPrice: 100},
	}, intakes.StoredVariants(intakes.StatusPendingApproval), intakes.EditPlain)
	if !errors.Is(err, intakes.ErrNotFound) {
		t.Fatalf("err=%v, quiero ErrNotFound", err)
	}
	if cliente, sistema := contarLíneas(t, db, id); cliente != 1 || sistema != 1 {
		t.Fatalf("cliente=%d sistema=%d; el tenant ajeno no toca nada", cliente, sistema)
	}
}
