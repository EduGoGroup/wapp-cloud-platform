package integrations_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/integrations"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/integrations/integrationshelpertest"
)

// Este test es EXTERNO (package integrations_test), como todos los del paquete:
// integrationshelpertest importa integrations, así que un test interno que usara la suite o el
// doble daría un ciclo de imports.
//
// store.go no tiene lógica (tipos, constantes, el centinela y el puerto): nació en verde. Las
// reglas del puerto Store las afirma la suite integrationshelpertest.Contrato.

// Las dos implementaciones del puerto lo son: lo comprueba el compilador.
var (
	_ integrations.Store = (*integrations.Postgres)(nil)
	_ integrations.Store = (*integrationshelpertest.Memoria)(nil)
)

// outboxMigration es la migración que fija el vocabulario de estados de la cola.
const outboxMigration = "../../../platform/storage/postgres/migrations/structure/0046_webhook_outbox.sql"

// TestErrClaimLost_TextAndWrapping: el centinela tiene su texto byte a byte, se reconoce ENVUELTO
// con errors.Is (así lo devuelven los almacenes), y otro error con el mismo texto no es él.
func TestErrClaimLost_TextAndWrapping(t *testing.T) {
	const want = "integrations: el claim de la entrega ya no es vigente"
	if got := integrations.ErrClaimLost.Error(); got != want {
		t.Errorf("ErrClaimLost = %q, quería %q", got, want)
	}
	wrapped := fmt.Errorf("integrations: entrega 7 hacia delivered: %w", integrations.ErrClaimLost)
	if !errors.Is(wrapped, integrations.ErrClaimLost) {
		t.Error("errors.Is no reconoce ErrClaimLost envuelto con %w")
	}
	if errors.Is(errors.New(want), integrations.ErrClaimLost) {
		t.Error("un error distinto con el mismo texto pasa por ErrClaimLost: se reconoce por identidad, no por texto")
	}
}

// TestStatuses_AreTheVocabularyOfTheTable: los cuatro estados son los literales del wire, y son
// EXACTAMENTE el CHECK de la migración 0046 (un estado que la tabla no admite reventaría en la
// primera escritura; uno que la tabla admite y Go no conoce quedaría sin contar).
func TestStatuses_AreTheVocabularyOfTheTable(t *testing.T) {
	got := []string{
		integrations.StatusPending, integrations.StatusDelivering,
		integrations.StatusDelivered, integrations.StatusDead,
	}
	want := []string{"pending", "delivering", "delivered", "dead"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("estado %d = %q, quería %q", i, got[i], want[i])
		}
	}
	sql, err := os.ReadFile(outboxMigration)
	if err != nil {
		t.Fatalf("leer la migración 0046: %v", err)
	}
	check := "CHECK (status IN ('" + strings.Join(got, "','") + "'))"
	if !strings.Contains(string(sql), check) {
		t.Errorf("la migración 0046 no lleva %q: el vocabulario de Go y el de la tabla ya no coinciden", check)
	}
}

// TestWebhookOutbox_ZeroMeansNoErrorAndNoClaim: en una entrega, LastError vacío es «nunca falló»
// y ClaimedAt cero es «sin claim vigente» (los dos NULL de la tabla); el payload es JSON crudo y
// viaja tal cual al serializarlo.
func TestWebhookOutbox_ZeroMeansNoErrorAndNoClaim(t *testing.T) {
	row := integrations.WebhookOutbox{ID: 7, TenantID: "tenant-1", Kind: "intake.push", Payload: json.RawMessage(`{"n":1}`)}
	if row.LastError != "" || !row.ClaimedAt.IsZero() || row.Attempts != 0 {
		t.Errorf("una entrega recién descrita trae (last_error=%q, claimed_at=%v, attempts=%d), quería los tres a cero",
			row.LastError, row.ClaimedAt, row.Attempts)
	}
	out, err := json.Marshal(struct{ P json.RawMessage }{row.Payload})
	if err != nil || string(out) != `{"P":{"n":1}}` {
		t.Errorf("el payload serializado = %s (err=%v), quería el JSON crudo tal cual", out, err)
	}
}

// TestTenantIntegration_IsComparableAndCarriesNoSecret: la integración es comparable (el valor
// cero es «sin fila») y no tiene dónde llevar el secreto: solo dice si lo hay.
func TestTenantIntegration_IsComparableAndCarriesNoSecret(t *testing.T) {
	ti := integrations.TenantIntegration{TenantID: "tenant-1", CatalogAdapter: "local", EventsAdapter: "webhook", HasSecret: true}
	if ti == (integrations.TenantIntegration{}) {
		t.Error("una integración con datos es igual al valor cero")
	}
	if ti != (integrations.TenantIntegration{TenantID: "tenant-1", CatalogAdapter: "local", EventsAdapter: "webhook", HasSecret: true}) {
		t.Errorf("dos integraciones con los mismos campos no son iguales: %+v", ti)
	}
	fields := reflect.TypeOf(ti)
	for i := range fields.NumField() {
		if name := fields.Field(i).Name; name != "HasSecret" && strings.Contains(strings.ToLower(name), "secret") {
			t.Errorf("TenantIntegration tiene el campo %s: el secreto solo sale por GetTenantSecret", name)
		}
	}
}

// TestStore_QueueIsUsableThroughThePort: un consumidor que solo conoce el puerto encola, reclama y
// cierra por él. Es el recorrido mínimo; las promesas, una a una, están en la suite.
func TestStore_QueueIsUsableThroughThePort(t *testing.T) {
	var store integrations.Store = integrationshelpertest.NewMemoria()
	ctx := context.Background()

	id, err := store.EnqueueWebhook(ctx, "tenant-1", "intake.push", json.RawMessage(`{"n":1}`))
	if err != nil {
		t.Fatalf("EnqueueWebhook: error inesperado %v", err)
	}
	batch, err := store.ClaimWebhookBatch(ctx, 10)
	if err != nil || len(batch) != 1 {
		t.Fatalf("ClaimWebhookBatch = (%+v, %v), quería una entrega", batch, err)
	}
	if batch[0].ID != id || batch[0].Status != integrations.StatusDelivering {
		t.Errorf("entrega reclamada = %+v, quería la %d en vuelo", batch[0], id)
	}
	if err := store.MarkWebhookDelivered(ctx, batch[0]); err != nil {
		t.Fatalf("MarkWebhookDelivered: error inesperado %v", err)
	}
	if err := store.MarkWebhookDead(ctx, batch[0], "tarde"); !errors.Is(err, integrations.ErrClaimLost) {
		t.Errorf("cerrar dos veces el mismo claim: err = %v, quería ErrClaimLost", err)
	}
	if err := store.MarkWebhookFailed(ctx, batch[0], batch[0].CreatedAt, "tarde"); !errors.Is(err, integrations.ErrClaimLost) {
		t.Errorf("reprogramar un claim ya cerrado: err = %v, quería ErrClaimLost", err)
	}
	if n, err := store.RecoverOrphanDeliveries(ctx, 0); err != nil || n != 0 {
		t.Errorf("RecoverOrphanDeliveries = (%d, %v), quería (0, nil): no queda nada en vuelo", n, err)
	}
}

// TestStore_TenantConfigIsUsableThroughThePort: y por el mismo puerto configura la integración de
// un tenant, lee si tiene secreto, lee el secreto aparte y la borra.
func TestStore_TenantConfigIsUsableThroughThePort(t *testing.T) {
	var store integrations.Store = integrationshelpertest.NewMemoria()
	ctx := context.Background()

	cfg := integrations.TenantIntegration{TenantID: "tenant-1", CatalogAdapter: "local", EventsAdapter: "webhook", Enabled: true}
	if err := store.UpsertTenantIntegration(ctx, cfg, "s3cr3t"); err != nil {
		t.Fatalf("UpsertTenantIntegration: error inesperado %v", err)
	}
	ti, found, err := store.GetTenantIntegration(ctx, "tenant-1")
	if err != nil || !found {
		t.Fatalf("GetTenantIntegration = (found=%v, err=%v), quería la integración", found, err)
	}
	if !ti.HasSecret || !ti.Enabled || ti.EventsAdapter != "webhook" {
		t.Errorf("GetTenantIntegration = %+v, quería la integración encendida y con secreto", ti)
	}
	secret, found, err := store.GetTenantSecret(ctx, "tenant-1")
	if err != nil || !found {
		t.Fatalf("GetTenantSecret = (found=%v, err=%v), quería el secreto", found, err)
	}
	if secret != "s3cr3t" {
		t.Error("GetTenantSecret no devolvió el secreto guardado")
	}
	if err := store.DeleteTenantIntegration(ctx, "tenant-1"); err != nil {
		t.Fatalf("DeleteTenantIntegration: error inesperado %v", err)
	}
	if _, found, err := store.GetTenantIntegration(ctx, "tenant-1"); err != nil || found {
		t.Errorf("tras borrar: found=%v, err=%v; quería que no quedara fila", found, err)
	}
}
