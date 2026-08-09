// event_seams_integration_test.go prueba contra Postgres real la costura Go de la
// migración 0052 (Plan 043 · Ola 1 · T1.3): flow_state.event_id y los dos TTL nuevos
// de tenant_settings.
//
// Va contra la BD y no solo contra el gemelo por dos razones concretas: event_id es
// UUID y el estado conversacional viaja como texto —el choque de tipos no se ve en un
// mapa Go—, y el DEFAULT 7200 de la columna solo existe en el esquema, así que la
// diferencia entre «no hay fila» y «hay fila con 0» no se puede simular.
package store_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

// conTTLsDeEvento siembra la fila de tenant_settings nombrando EXPLÍCITAMENTE las dos
// columnas de TTL de evento. Nombrarlas es el punto: una fila que las omitiera
// recibiría los DEFAULT del esquema y no probaría nada sobre el override.
func conTTLsDeEvento(t *testing.T, inactividadSecs, historiaSecs int) (*store.PostgresRepository, string) {
	t.Helper()
	db := openTestDB(t)
	tenant := uuid.NewString()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.tenant_settings
			(tenant_id, page_size, event_inactivity_ttl_seconds, event_history_ttl_seconds)
		VALUES ($1, 5, $2, $3)
	`, tenant, inactividadSecs, historiaSecs); err != nil {
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

// TestIntegration_GetTenantSettings_TTLsDeEventoSinFila: el tenant que nunca configuró
// nada hereda el reloj de plataforma (7200 s = 2 h) y la retención en 0 (sin poda).
// Es la primera mitad del criterio de T1.3.
func TestIntegration_GetTenantSettings_TTLsDeEventoSinFila(t *testing.T) {
	db := openTestDB(t)
	repo := store.NewPostgresRepository(db)

	got, err := repo.GetTenantSettings(context.Background(), uuid.NewString())
	if err != nil {
		t.Fatalf("GetTenantSettings (sin fila): %v", err)
	}
	if got.EventInactivityTTL != 2*time.Hour {
		t.Fatalf("EventInactivityTTL sin fila = %v, esperaba 2h (7200s)", got.EventInactivityTTL)
	}
	if got.EventHistoryTTL != 0 {
		t.Fatalf("EventHistoryTTL sin fila = %v, esperaba 0", got.EventHistoryTTL)
	}
}

// TestIntegration_GetTenantSettings_CeroExplícitoEsSinVencimiento es la segunda mitad
// del criterio y la que de verdad se rompe sola: 0 es el cero de Go, así que un
// GetTenantSettings que confunda «no hay fila» con «hay fila con 0» devolverá aquí las
// 2 h y habrá convertido, en silencio, un «sin vencimiento» decidido por la empresa en
// una conversación que muere a las dos horas.
func TestIntegration_GetTenantSettings_CeroExplícitoEsSinVencimiento(t *testing.T) {
	repo, tenant := conTTLsDeEvento(t, 0, 0)

	got, err := repo.GetTenantSettings(context.Background(), tenant)
	if err != nil {
		t.Fatalf("GetTenantSettings: %v", err)
	}
	if got.EventInactivityTTL != 0 {
		t.Fatalf("event_inactivity_ttl_seconds=0 se leyó como %v: el default pisó el override",
			got.EventInactivityTTL)
	}
}

// TestIntegration_GetTenantSettings_LaFilaMandaSobreElDefault comprueba que se LEE la
// columna, no que se elige entre dos constantes. Los dos tests de arriba, solos, los
// aprobaría una implementación que contestara 7200 cuando no hay fila y 0 cuando la
// hay, sin mirar nunca el valor guardado; con valores arbitrarios (900 y 86400) esa
// implementación cae.
func TestIntegration_GetTenantSettings_LaFilaMandaSobreElDefault(t *testing.T) {
	repo, tenant := conTTLsDeEvento(t, 900, 86400)

	got, err := repo.GetTenantSettings(context.Background(), tenant)
	if err != nil {
		t.Fatalf("GetTenantSettings: %v", err)
	}
	if got.EventInactivityTTL != 15*time.Minute {
		t.Fatalf("EventInactivityTTL = %v, esperaba 15m (900s de la fila)", got.EventInactivityTTL)
	}
	if got.EventHistoryTTL != 24*time.Hour {
		t.Fatalf("EventHistoryTTL = %v, esperaba 24h (86400s de la fila)", got.EventHistoryTTL)
	}
}

// TestIntegration_FlowStateEventID recorre la vida del puntero contra la columna real:
// nace NULL (nadie parió evento todavía), se enciende con un UUID y —lo que se rompe
// si el upsert no escribe la columna— se APAGA al guardar un estado sin evento.
//
// El UUID del evento es inventado a propósito: la 0052 deja event_id SIN FK hacia
// conversation_events, y este test lo confirma de paso.
func TestIntegration_FlowStateEventID(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	repo := store.NewPostgresRepository(db)
	tenantID := seedTenant(t, db)
	key := store.Key{
		TenantID:  tenantID,
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

	// 1. Sin evento: la columna queda NULL y el modelo lo ve como "".
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
	if col := leerEventIDCrudo(t, db, key); col.Valid {
		t.Fatalf("event_id debería ser NULL sin evento, y vale %q", col.String)
	}

	// 2. Con evento: el UUID viaja a la columna y vuelve como cadena.
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

	// 3. Apagado: cerrar o cancelar el evento es guardar el estado sin él.
	if err := repo.Save(ctx, base); err != nil {
		t.Fatalf("Save apagando el evento: %v", err)
	}
	apagado, _, err := repo.Load(ctx, key)
	if err != nil {
		t.Fatalf("Load tras apagar: %v", err)
	}
	if apagado.EventID != "" {
		t.Fatalf("el puntero no se apagó: EventID = %q", apagado.EventID)
	}
	if col := leerEventIDCrudo(t, db, key); col.Valid {
		t.Fatalf("event_id debería haber vuelto a NULL, y vale %q", col.String)
	}
}

// leerEventIDCrudo lee la columna SIN pasar por el repositorio: es lo que distingue
// «el modelo dice ""» de «la columna es NULL». Un Save que no escribiera event_id
// dejaría el modelo contento y la fila mintiendo.
func leerEventIDCrudo(t *testing.T, db *sql.DB, key store.Key) sql.NullString {
	t.Helper()
	var out sql.NullString
	if err := db.QueryRowContext(context.Background(), `
		SELECT event_id::text FROM public.flow_state
		WHERE tenant_id = $1 AND session_id = $2 AND contact_id = $3
	`, key.TenantID, key.SessionID, key.ContactID).Scan(&out); err != nil {
		t.Fatalf("leyendo event_id crudo: %v", err)
	}
	return out
}
