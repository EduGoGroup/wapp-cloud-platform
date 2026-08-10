// intake_items_integration_test.go cubre contra POSTGRES REAL el reemplazo de
// líneas del que depende que un pedido abierto tenga las suyas al día (Plan 043 ·
// Ola 3). Lo que se prueba aquí no lo puede probar el repositorio en memoria: el
// DELETE con el filtro del prefijo reservado, que el borrado y la escritura sean una
// sola transacción, y que el cierre no duplique lo ya materializado.
package store_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// abrirSolicitud crea una solicitud "open" y devuelve su id, con tenant y contacto
// únicos para que dos corridas no se pisen. Desde la 0054 la fila declara a su
// padre (event_id): el evento se siembra de verdad (seedTenantEventoPG) porque el
// CHECK y la FK no aceptan otra cosa; el tenant TEXT de la solicitud sigue siendo
// el opaco de siempre — la ligadura es solicitud→evento, no tenant→tenant.
func abrirSolicitud(t *testing.T, db *sql.DB, repo *store.PostgresRepository) (intakeID, tenantID, contactID string) {
	t.Helper()
	sufijo := fmt.Sprintf("%d", time.Now().UnixNano())
	intakeID, tenantID, contactID = uuid.NewString(), "t-lineas-"+sufijo, "c-lineas-"+sufijo
	_, eventID := seedTenantEventoPG(t, db, "cancelled")
	if err := repo.UpsertIntake(context.Background(), store.Intake{
		ID: intakeID, TenantID: tenantID, ContactID: contactID,
		SessionID: "s-lineas", Status: "open", EventID: eventID,
	}); err != nil {
		t.Fatalf("UpsertIntake: %v", err)
	}
	return intakeID, tenantID, contactID
}

// líneasDe lee las líneas EN EL ORDEN EN QUE LAS VE EL RESTO DEL SISTEMA
// (intakes.itemsOf ordena por added_at, id): que el orden del pedido sobreviva a un
// reemplazo completo solo se puede comprobar leyendo como se lee de verdad.
func líneasDe(t *testing.T, db *sql.DB, intakeID string) []store.IntakeItem {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `
		SELECT sku, label, customization, qty, unit_price
		FROM public.intake_items
		WHERE intake_id = $1
		ORDER BY added_at, id
	`, intakeID)
	if err != nil {
		t.Fatalf("leer líneas: %v", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			t.Errorf("cerrar filas de líneas: %v", cerr)
		}
	}()
	var out []store.IntakeItem
	for rows.Next() {
		var it store.IntakeItem
		if err := rows.Scan(&it.SKU, &it.Label, &it.Customization, &it.Qty, &it.UnitPrice); err != nil {
			t.Fatalf("escanear línea: %v", err)
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("recorrer líneas: %v", err)
	}
	return out
}

func exigirSKUs(t *testing.T, got []store.IntakeItem, quiero ...string) {
	t.Helper()
	if len(got) != len(quiero) {
		t.Fatalf("líneas: got %d, want %d\n  %+v", len(got), len(quiero), got)
	}
	for i, sku := range quiero {
		if got[i].SKU != sku {
			t.Fatalf("línea %d: got sku %q, want %q\n  %+v", i, got[i].SKU, sku, got)
		}
	}
}

// TestPG_ReplaceIntakeItems_EsUnEspejoYConservaElOrden: reemplazar dos veces deja lo
// de la segunda vez, sin duplicar, y con el pedido en el orden del carrito.
func TestPG_ReplaceIntakeItems_EsUnEspejoYConservaElOrden(t *testing.T) {
	db := openTestDB(t)
	repo := store.NewPostgresRepository(db)
	ctx := context.Background()
	intakeID, _, _ := abrirSolicitud(t, db, repo)

	if err := repo.ReplaceIntakeItems(ctx, intakeID, []store.IntakeItem{
		{SKU: "CAFE", Label: "Café", Qty: 2, UnitPrice: 2.5},
	}); err != nil {
		t.Fatalf("ReplaceIntakeItems (1.ª): %v", err)
	}
	exigirSKUs(t, líneasDe(t, db, intakeID), "CAFE")

	// La foto siguiente trae la primera línea con OTRA cantidad y una segunda nueva.
	if err := repo.ReplaceIntakeItems(ctx, intakeID, []store.IntakeItem{
		{SKU: "CAFE", Label: "Café", Customization: "sin azúcar", Qty: 5, UnitPrice: 2.5},
		{SKU: "TE", Label: "Té", Qty: 3, UnitPrice: 2.0},
	}); err != nil {
		t.Fatalf("ReplaceIntakeItems (2.ª): %v", err)
	}

	got := líneasDe(t, db, intakeID)
	exigirSKUs(t, got, "CAFE", "TE")
	if got[0].Qty != 5 || got[0].Customization != "sin azúcar" || got[0].UnitPrice != 2.5 {
		t.Errorf("la línea 0 no es la de la última foto: %+v", got[0])
	}
	if got[1].Qty != 3 || got[1].Label != "Té" {
		t.Errorf("la línea 1 no es la de la última foto: %+v", got[1])
	}
}

// TestPG_ReplaceIntakeItems_NoSeLlevaLaLíneaDeEnvío: el DELETE filtra por el prefijo
// reservado, así que la línea que pone la plataforma sobrevive al reemplazo. Es la
// mitad del SQL que ningún test en memoria puede acreditar.
func TestPG_ReplaceIntakeItems_NoSeLlevaLaLíneaDeEnvío(t *testing.T) {
	db := openTestDB(t)
	repo := store.NewPostgresRepository(db)
	ctx := context.Background()
	intakeID, _, _ := abrirSolicitud(t, db, repo)

	if err := repo.ReplaceIntakeItems(ctx, intakeID, []store.IntakeItem{
		{SKU: "CAFE", Label: "Café", Qty: 1, UnitPrice: 2.5},
	}); err != nil {
		t.Fatalf("ReplaceIntakeItems: %v", err)
	}
	// La línea de envío la escribe el dominio de solicitudes, no el carrito: aquí se
	// pone con el mismo INSERT que usa aquél (sku reservado).
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.intake_items (intake_id, sku, label, customization, qty, unit_price)
		VALUES ($1, $2, 'Envío', '', 1, 3000)
	`, intakeID, intakes.ShippingSKU); err != nil {
		t.Fatalf("sembrar la línea de envío: %v", err)
	}

	if err := repo.ReplaceIntakeItems(ctx, intakeID, []store.IntakeItem{
		{SKU: "TE", Label: "Té", Qty: 2, UnitPrice: 2.0},
	}); err != nil {
		t.Fatalf("ReplaceIntakeItems (2.ª): %v", err)
	}
	exigirSKUs(t, líneasDe(t, db, intakeID), intakes.ShippingSKU, "TE")
}

// TestPG_CloseIntake_NoDuplicaLasLíneasYaMaterializadas es el riesgo crítico contra
// la base real: la solicitud llega al cierre CON sus líneas (las escribió la
// proyección de item_added) y el cierre trae el mismo conjunto. Antes de esta tarea
// el cierre insertaba, y aquí habría 4 filas donde tiene que haber 2.
func TestPG_CloseIntake_NoDuplicaLasLíneasYaMaterializadas(t *testing.T) {
	db := openTestDB(t)
	repo := store.NewPostgresRepository(db)
	ctx := context.Background()
	intakeID, tenantID, contactID := abrirSolicitud(t, db, repo)

	vivas := []store.IntakeItem{
		{SKU: "CAFE", Label: "Café", Qty: 2, UnitPrice: 2.5},
		{SKU: "TE", Label: "Té", Qty: 1, UnitPrice: 2.0},
	}
	if err := repo.ReplaceIntakeItems(ctx, intakeID, vivas); err != nil {
		t.Fatalf("ReplaceIntakeItems: %v", err)
	}
	exigirSKUs(t, líneasDe(t, db, intakeID), "CAFE", "TE")

	// El cierre trae el conjunto FINAL, que además lleva la personalización que el
	// cliente escribió después del último item_added.
	final := []store.IntakeItem{
		{SKU: "CAFE", Label: "Café", Customization: "sin azúcar", Qty: 2, UnitPrice: 2.5},
		{SKU: "TE", Label: "Té", Qty: 1, UnitPrice: 2.0},
	}
	cerrada, err := repo.CloseIntake(ctx, store.IntakeClose{
		TenantID: tenantID, ContactID: contactID, SessionID: "s-lineas",
		Total: 7.0, Items: final,
	})
	if err != nil {
		t.Fatalf("CloseIntake: %v", err)
	}
	if cerrada != intakeID {
		t.Fatalf("el cierre cayó sobre otra solicitud: got %q, want %q", cerrada, intakeID)
	}

	got := líneasDe(t, db, intakeID)
	exigirSKUs(t, got, "CAFE", "TE")
	if got[0].Customization != "sin azúcar" {
		t.Errorf("el cierre debe dejar la verdad final de la línea: %+v", got[0])
	}
}
