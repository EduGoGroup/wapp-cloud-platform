// event_seams_test.go cubre, SIN BD, la costura Go del modelo de evento (Plan 043 ·
// Ola 1, migración 0052): el puntero al evento activo en el estado conversacional y
// los dos TTL nuevos de tenant_settings, sobre el gemelo en memoria.
//
// El gemelo importa porque no es un juguete: lo usan tests de otras capas. Si diverge
// del repositorio Postgres ante un `event_id` NULL o ante el override a 0, esos tests
// siguen en verde mientras describen un sistema que no existe.
package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

// TestMemoryTenantSettings_SinFilaTraeLosDefaultsDelEvento: un tenant que nunca
// configuró nada hereda el reloj de plataforma (2 h) y la retención en 0 (sin poda).
func TestMemoryTenantSettings_SinFilaTraeLosDefaultsDelEvento(t *testing.T) {
	repo := store.NewMemoryRepository()

	got, err := repo.GetTenantSettings(context.Background(), uuid.NewString())
	if err != nil {
		t.Fatalf("GetTenantSettings: %v", err)
	}
	if got.EventInactivityTTL != 2*time.Hour {
		t.Fatalf("EventInactivityTTL sin fila = %v, esperaba 2h", got.EventInactivityTTL)
	}
	if got.EventHistoryTTL != 0 {
		t.Fatalf("EventHistoryTTL sin fila = %v, esperaba 0", got.EventHistoryTTL)
	}
}

// TestMemoryTenantSettings_CeroExplícitoSobreviveAlDefault es el caso que se rompe
// solo: 0 es el cero de Go, así que un código que trate «no hay fila» y «hay fila con
// 0» por el mismo camino devolvería aquí las 2 h del default y convertiría un «sin
// vencimiento» decidido por la empresa en un vencimiento a las dos horas.
func TestMemoryTenantSettings_CeroExplícitoSobreviveAlDefault(t *testing.T) {
	repo := store.NewMemoryRepository()
	tenant := uuid.NewString()
	fila := store.DefaultTenantSettings(tenant)
	fila.EventInactivityTTL = 0 // override explícito: esta empresa no vence conversaciones.
	repo.SetTenantSettings(fila)

	got, err := repo.GetTenantSettings(context.Background(), tenant)
	if err != nil {
		t.Fatalf("GetTenantSettings: %v", err)
	}
	if got.EventInactivityTTL != 0 {
		t.Fatalf("un 0 explícito se leyó como %v: el default pisó el override", got.EventInactivityTTL)
	}
}

// TestMemoryTenantSettings_LaFilaMandaSobreElDefault distingue «devuelve la fila» de
// «devuelve el default»: sin este caso, un GetTenantSettings que ignorase la fila y
// contestase siempre 2 h pasaría el test de arriba por casualidad.
func TestMemoryTenantSettings_LaFilaMandaSobreElDefault(t *testing.T) {
	repo := store.NewMemoryRepository()
	tenant := uuid.NewString()
	fila := store.DefaultTenantSettings(tenant)
	fila.EventInactivityTTL = 15 * time.Minute
	fila.EventHistoryTTL = 24 * time.Hour
	repo.SetTenantSettings(fila)

	got, err := repo.GetTenantSettings(context.Background(), tenant)
	if err != nil {
		t.Fatalf("GetTenantSettings: %v", err)
	}
	if got.EventInactivityTTL != 15*time.Minute {
		t.Fatalf("EventInactivityTTL de la fila = %v, esperaba 15m", got.EventInactivityTTL)
	}
	if got.EventHistoryTTL != 24*time.Hour {
		t.Fatalf("EventHistoryTTL de la fila = %v, esperaba 24h", got.EventHistoryTTL)
	}
}

// TestMemoryFlowState_EventIDIdaVueltaYApagado recorre la vida del puntero en el
// gemelo: nace vacío (nadie parió evento), se enciende, y —lo que de verdad se
// rompe— se APAGA al guardar un estado sin evento. Un upsert que conservara el valor
// previo dejaría la conversación pegada a un evento ya cerrado.
func TestMemoryFlowState_EventIDIdaVueltaYApagado(t *testing.T) {
	repo := store.NewMemoryRepository()
	ctx := context.Background()
	key := store.Key{
		TenantID:  uuid.NewString(),
		SessionID: "sess-eventos",
		ContactID: uuid.NewString(),
	}
	base := model.Conversation{
		TenantID:    key.TenantID,
		SessionID:   key.SessionID,
		ContactID:   key.ContactID,
		FlowID:      "pedido",
		FlowVersion: 1,
		CurrentNode: "root",
		Vars:        map[string]any{},
	}

	if err := repo.Save(ctx, base); err != nil {
		t.Fatalf("Save sin evento: %v", err)
	}
	sinEvento, found, err := repo.Load(ctx, key)
	if err != nil || !found {
		t.Fatalf("Load sin evento: found=%v err=%v", found, err)
	}
	if sinEvento.EventID != "" {
		t.Fatalf("una conversación sin evento trajo EventID %q", sinEvento.EventID)
	}

	evento := uuid.NewString()
	conEvento := base
	conEvento.EventID = evento
	if err := repo.Save(ctx, conEvento); err != nil {
		t.Fatalf("Save con evento: %v", err)
	}
	leído, _, err := repo.Load(ctx, key)
	if err != nil {
		t.Fatalf("Load con evento: %v", err)
	}
	if leído.EventID != evento {
		t.Fatalf("EventID leído = %q, esperaba %q", leído.EventID, evento)
	}

	if err := repo.Save(ctx, base); err != nil { // base sigue con EventID "": apagar.
		t.Fatalf("Save apagando el evento: %v", err)
	}
	apagado, _, err := repo.Load(ctx, key)
	if err != nil {
		t.Fatalf("Load tras apagar: %v", err)
	}
	if apagado.EventID != "" {
		t.Fatalf("el puntero no se apagó: EventID = %q", apagado.EventID)
	}
}
