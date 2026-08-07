// buyer_fields_integration_test.go prueba la LECTURA de tenant_settings.buyer_fields
// contra Postgres real (Plan 041 · T4.5, D-041.13). Es la config con la que el
// carrito decide si pregunta algo antes de cerrar; leerla mal significa o preguntar
// lo que nadie pidió, o no preguntar lo que el dueño exige.
package store_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

// conBuyerFields siembra la fila de tenant_settings con el JSON dado tal cual (una
// cadena, no un struct: se prueba lo que Postgres devuelve, no lo que Go serializa).
func conBuyerFields(t *testing.T, jsonBuyerFields string) (*store.PostgresRepository, string) {
	t.Helper()
	db := openTestDB(t)
	tenant := uuid.NewString()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.tenant_settings (tenant_id, page_size, buyer_fields)
		VALUES ($1, 5, $2::jsonb)
	`, tenant, jsonBuyerFields); err != nil {
		t.Fatalf("sembrando tenant_settings: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db.ExecContext(context.Background(),
			`DELETE FROM public.tenant_settings WHERE tenant_id = $1`, tenant); err != nil {
			t.Logf("limpiando tenant_settings: %v", err)
		}
	})
	return store.NewPostgresRepository(db), tenant
}

// TestGetTenantSettings_BuyerFields lee el checklist en su forma CANÓNICA (la del
// design D-041.13 y la del COMMENT de la columna, corregido en esta tarea): lista
// de objetos con key, label y required.
func TestGetTenantSettings_BuyerFields(t *testing.T) {
	repo, tenant := conBuyerFields(t,
		`[{"key":"rut","label":"RUT","required":true},{"key":"referencia","label":"Referencia","required":false}]`)

	got, err := repo.GetTenantSettings(context.Background(), tenant)
	if err != nil {
		t.Fatalf("GetTenantSettings: %v", err)
	}
	if len(got.BuyerFields) != 2 {
		t.Fatalf("buyer_fields leídos: %d, esperaba 2 (%+v)", len(got.BuyerFields), got.BuyerFields)
	}
	if got.BuyerFields[0] != (store.BuyerField{Key: "rut", Label: "RUT", Required: true}) {
		t.Fatalf("primer campo = %+v", got.BuyerFields[0])
	}
	if got.BuyerFields[1].Required {
		t.Fatalf("el segundo campo se leyó como required: %+v", got.BuyerFields[1])
	}
}

// TestGetTenantSettings_BuyerFieldsPorDefectoVacío: ni el tenant sin fila ni el que
// nunca configuró el checklist preguntan nada. Es lo que hace que T4.5 no cambie
// nada para los tenants de hoy (INV-15).
func TestGetTenantSettings_BuyerFieldsPorDefectoVacío(t *testing.T) {
	repo, tenant := conBuyerFields(t, `[]`) // el DEFAULT de la columna
	ctx := context.Background()

	got, err := repo.GetTenantSettings(ctx, tenant)
	if err != nil {
		t.Fatalf("GetTenantSettings (fila con default): %v", err)
	}
	if len(got.BuyerFields) != 0 {
		t.Fatalf("un tenant con buyer_fields '[]' devolvió %+v", got.BuyerFields)
	}

	sinFila, err := repo.GetTenantSettings(ctx, uuid.NewString())
	if err != nil {
		t.Fatalf("GetTenantSettings (sin fila): %v", err)
	}
	if len(sinFila.BuyerFields) != 0 {
		t.Fatalf("un tenant SIN fila devolvió %+v", sinFila.BuyerFields)
	}
}

// TestGetTenantSettings_BuyerFieldsIlegiblesNoRompenLaConversación: esta lectura
// está en el camino de CADA mensaje del cliente. Un JSON con otra forma —el dueño
// editó la columna a mano siguiendo el COMMENT viejo, que decía ["rut"]— tiene que
// dejar al carrito vendiendo sin preguntar, no tumbar la conversación.
func TestGetTenantSettings_BuyerFieldsIlegiblesNoRompenLaConversación(t *testing.T) {
	repo, tenant := conBuyerFields(t, `["rut","direccion"]`)

	got, err := repo.GetTenantSettings(context.Background(), tenant)
	if err != nil {
		t.Fatalf("un buyer_fields con otra forma devolvió error: %v", err)
	}
	if len(got.BuyerFields) != 0 {
		t.Fatalf("un buyer_fields ilegible se interpretó como %+v", got.BuyerFields)
	}
	if got.PageSize != store.DefaultPageSize {
		t.Fatalf("el resto de la config se perdió: page_size=%d", got.PageSize)
	}
}
