package trigger_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
)

// dsnEnv habilita los tests de integración con BD real (igual que en store/lease).
const dsnEnv = "WAPP_TEST_DB_DSN"

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		t.Skipf("%s no definido: se omiten los tests de integración con BD", dsnEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, err := postgres.Open(ctx, postgres.Config{DSN: dsn})
	if err != nil {
		t.Skipf("BD no disponible en %s (%v): se omiten", dsnEnv, err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Logf("cerrando BD de test: %v", cerr)
		}
	})
	if _, err := migrations.Migrate(ctx, db); err != nil {
		t.Fatalf("migrando BD de test: %v", err)
	}
	return db
}

// seedTenant crea un tenant con slug único y devuelve su UUID (tenant_id es UUID
// en flow_triggers, así que necesitamos un tenant real).
func seedTenant(t *testing.T, db *sql.DB) string {
	t.Helper()
	repo := postgres.NewTenantRepository(db)
	slug := fmt.Sprintf("tenant-trigger-%d", time.Now().UnixNano())
	ten, err := repo.Create(context.Background(), slug, "Trigger Store Test")
	if err != nil {
		t.Fatalf("crear tenant: %v", err)
	}
	return ten.ID
}

func TestIntegration_TriggerStore_InsertGetDelete(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	s := trigger.NewPostgresStore(db)
	tid := seedTenant(t, db)

	out := mustInsert(t, s, trigger.Rule{TenantID: tid, Kind: trigger.KindKeyword, Keyword: "pedido", MatchType: trigger.MatchExact, FlowID: "carrito", Priority: 7, Enabled: true})
	if out.TriggerID == "" {
		t.Fatal("Insert debe asignar trigger_id (RETURNING)")
	}

	got, err := s.Get(ctx, tid, out.TriggerID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Keyword != "pedido" || got.FlowID != "carrito" || got.Priority != 7 || got.MatchType != trigger.MatchExact || !got.Enabled {
		t.Fatalf("regla persistida no coincide: %+v", got)
	}

	if err := s.Delete(ctx, tid, out.TriggerID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.Get(ctx, tid, out.TriggerID); !errors.Is(err, trigger.ErrTriggerNotFound) {
		t.Fatalf("tras Delete, Get debe ser ErrTriggerNotFound, got %v", err)
	}
}

func TestIntegration_TriggerStore_NullMappingAndList(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	s := trigger.NewPostgresStore(db)
	tid := seedTenant(t, db)

	mustInsert(t, s, trigger.Rule{TenantID: tid, Kind: trigger.KindKeyword, Keyword: "pedido", MatchType: trigger.MatchExact, FlowID: "carrito", Enabled: true})
	// fallback: keyword NULL debe leerse como "".
	fb := mustInsert(t, s, trigger.Rule{TenantID: tid, Kind: trigger.KindFallback, FlowID: "menu", Enabled: true})

	gotFb, err := s.Get(ctx, tid, fb.TriggerID)
	if err != nil {
		t.Fatalf("get fallback: %v", err)
	}
	if gotFb.Keyword != "" || gotFb.FlowID != "menu" || gotFb.Message != "" {
		t.Fatalf("fallback mapea NULL keyword/message mal: %+v", gotFb)
	}

	// escape con message: round-trip por la columna message (Plan 019 · T4b).
	esc := mustInsert(t, s, trigger.Rule{TenantID: tid, Kind: trigger.KindEscape, Keyword: "salir", MatchType: trigger.MatchExact, Enabled: true, Message: "Hasta pronto"})
	gotEsc, err := s.Get(ctx, tid, esc.TriggerID)
	if err != nil {
		t.Fatalf("get escape: %v", err)
	}
	if gotEsc.Message != "Hasta pronto" {
		t.Fatalf("message no hizo round-trip: %+v", gotEsc)
	}

	all, err := s.List(ctx, tid)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("List esperaba 3, got %d", len(all))
	}
	kws, err := s.ListByKind(ctx, tid, "", trigger.KindKeyword)
	if err != nil {
		t.Fatalf("listByKind: %v", err)
	}
	if len(kws) != 1 {
		t.Fatalf("ListByKind keyword esperaba 1, got %d", len(kws))
	}
}

// TestIntegration_TriggerStore_SessionScope verifica el filtro por-sesión de
// ListByKind (Plan 020 · T4): con una regla global (session_id NULL) y una
// específica de sesión, la consulta por sesión sX devuelve ambas; por sY solo la
// global; y la vista global ("") solo la global. Round-trip de session_id incluido.
func TestIntegration_TriggerStore_SessionScope(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	s := trigger.NewPostgresStore(db)
	tid := seedTenant(t, db)

	glob := mustInsert(t, s, trigger.Rule{TenantID: tid, Kind: trigger.KindKeyword, Keyword: "hola", MatchType: trigger.MatchExact, FlowID: "g", Enabled: true})
	spec := mustInsert(t, s, trigger.Rule{TenantID: tid, Kind: trigger.KindKeyword, Keyword: "hola", MatchType: trigger.MatchExact, FlowID: "s", Enabled: true, SessionID: "sX"})

	// round-trip de session_id por Get.
	gotSpec, err := s.Get(ctx, tid, spec.TriggerID)
	if err != nil {
		t.Fatalf("get spec: %v", err)
	}
	if gotSpec.SessionID != "sX" {
		t.Fatalf("session_id no hizo round-trip: %+v", gotSpec)
	}
	if gotGlob, gerr := s.Get(ctx, tid, glob.TriggerID); gerr != nil || gotGlob.SessionID != "" {
		t.Fatalf("regla global debe leer session_id vacío, got %+v err=%v", gotGlob, gerr)
	}

	inSX, err := s.ListByKind(ctx, tid, "sX", trigger.KindKeyword)
	if err != nil {
		t.Fatalf("listByKind sX: %v", err)
	}
	if len(inSX) != 2 {
		t.Fatalf("sesión sX debe ver global+específica (2), got %d", len(inSX))
	}
	inSY, err := s.ListByKind(ctx, tid, "sY", trigger.KindKeyword)
	if err != nil {
		t.Fatalf("listByKind sY: %v", err)
	}
	if len(inSY) != 1 || inSY[0].FlowID != "g" {
		t.Fatalf("sesión sY solo debe ver la global, got %+v", inSY)
	}
	inGlob, err := s.ListByKind(ctx, tid, "", trigger.KindKeyword)
	if err != nil {
		t.Fatalf("listByKind global: %v", err)
	}
	if len(inGlob) != 1 || inGlob[0].FlowID != "g" {
		t.Fatalf("vista global solo debe ver la global, got %+v", inGlob)
	}
}

func TestIntegration_TriggerStore_TenantIsolation(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	s := trigger.NewPostgresStore(db)
	t1 := seedTenant(t, db)
	t2 := seedTenant(t, db)

	r := mustInsert(t, s, trigger.Rule{TenantID: t1, Kind: trigger.KindKeyword, Keyword: "x", MatchType: trigger.MatchExact, FlowID: "f", Enabled: true})

	if _, err := s.Get(ctx, t2, r.TriggerID); !errors.Is(err, trigger.ErrTriggerNotFound) {
		t.Fatalf("Get cross-tenant debe ser ErrTriggerNotFound (INV-8), got %v", err)
	}
	list, err := s.List(ctx, t2)
	if err != nil {
		t.Fatalf("list t2: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("t2 no debe ver reglas de t1, got %d", len(list))
	}
}
