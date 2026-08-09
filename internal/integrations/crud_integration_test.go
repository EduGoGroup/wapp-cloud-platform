// crud_integration_test.go verifica contra Postgres real lo que el CRUD de
// /api/v1/integrations (Plan 042 · T5.1) apoya en el store: que la huella del
// secreto sobrevive el viaje de ida y vuelta por el envelope de la KEK, y que la
// regla «secret vacío conserva el existente» es de verdad y no del fake.
//
// Un doble en memoria no puede demostrar ninguna de las dos: la primera pasa por
// cifrado real y la segunda, por el ON CONFLICT del upsert.
package integrations_test

import (
	"context"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/integrations"
)

// Los mismos valores que usa el test de la API, con su huella calculada FUERA
// (`printf '%s' … | shasum -a 256`).
const (
	//nolint:gosec // no es una credencial: es material de firma de un test
	secretoCRUD = "secreto-de-firma-del-puente-jjx-2026"
	huellaCRUD  = "e5c47775"
	//nolint:gosec // ídem
	rotadoCRUD    = "secreto-ROTADO-del-puente-jjx-2026"
	huellaRotCRUD = "b7d7294f"
)

// TestSecretFingerprint_SobreviveElEnvelope: se guarda cifrado con la KEK y la
// huella que sale al leer es la del secreto ORIGINAL. Es lo que hace útil el
// campo del GET — si no coincidiera con el sha256 del secreto que el dueño tiene
// en su puente, la comparación que justifica publicarlo no serviría de nada.
func TestSecretFingerprint_SobreviveElEnvelope(t *testing.T) {
	db := openTestDB(t)
	wipeWebhookTables(t, db)
	store := integrations.NewPostgres(db, cipherDePrueba(t))
	ctx := context.Background()

	const tenant = "t-huella"
	base := integrations.TenantIntegration{
		TenantID:       tenant,
		CatalogAdapter: "local",
		EventsAdapter:  "webhook",
		EndpointURL:    "https://puente.example/hook",
		Enabled:        true,
	}
	if err := store.UpsertTenantIntegration(ctx, base, secretoCRUD); err != nil {
		t.Fatalf("upsert con secreto: %v", err)
	}

	exigeHuella(t, store, tenant, huellaCRUD)

	// Reconfigurar SIN secreto no lo toca: es la regla en la que se apoya el PUT
	// para poder cambiar el endpoint sin reenviar un valor que el GET no devuelve.
	base.EndpointURL = "https://otro.example/hook"
	if err := store.UpsertTenantIntegration(ctx, base, ""); err != nil {
		t.Fatalf("upsert sin secreto: %v", err)
	}
	exigeHuella(t, store, tenant, huellaCRUD)

	// Rotar sí la cambia.
	if err := store.UpsertTenantIntegration(ctx, base, rotadoCRUD); err != nil {
		t.Fatalf("upsert rotando: %v", err)
	}
	exigeHuella(t, store, tenant, huellaRotCRUD)

	// Y borrar la fila deja al tenant sin secreto (es la ÚNICA forma de retirarlo).
	if err := store.DeleteTenantIntegration(ctx, tenant); err != nil {
		t.Fatalf("borrar: %v", err)
	}
	if _, found, ferr := store.SecretFingerprint(ctx, tenant); ferr != nil || found {
		t.Fatalf("tras borrar: found=%v err=%v, quiero false y sin error", found, ferr)
	}
	if _, existe, gerr := store.GetTenantIntegration(ctx, tenant); gerr != nil || existe {
		t.Fatalf("tras borrar: la fila sigue (existe=%v, err=%v)", existe, gerr)
	}
}

// exigeHuella lee la huella del tenant y exige que sea la esperada.
func exigeHuella(t *testing.T, store *integrations.Postgres, tenant, quiero string) {
	t.Helper()
	fp, found, err := store.SecretFingerprint(context.Background(), tenant)
	if err != nil {
		t.Fatalf("SecretFingerprint: %v", err)
	}
	if !found || fp != quiero {
		t.Fatalf("huella=%q found=%v, quiero %q y true", fp, found, quiero)
	}
}

// TestSecretFingerprint_SinFilaNiSecreto: los dos «no hay» se distinguen del
// error y ninguno inventa huella.
func TestSecretFingerprint_SinFilaNiSecreto(t *testing.T) {
	db := openTestDB(t)
	wipeWebhookTables(t, db)
	store := integrations.NewPostgres(db, cipherDePrueba(t))
	ctx := context.Background()

	if fp, found, err := store.SecretFingerprint(ctx, "t-inexistente"); err != nil || found || fp != "" {
		t.Fatalf("tenant sin fila: fp=%q found=%v err=%v", fp, found, err)
	}

	const tenant = "t-sin-secreto"
	if err := store.UpsertTenantIntegration(ctx, integrations.TenantIntegration{
		TenantID: tenant, CatalogAdapter: "local", EventsAdapter: "local",
	}, ""); err != nil {
		t.Fatalf("upsert sin secreto: %v", err)
	}
	if fp, found, err := store.SecretFingerprint(ctx, tenant); err != nil || found || fp != "" {
		t.Fatalf("fila sin secreto: fp=%q found=%v err=%v", fp, found, err)
	}
}
