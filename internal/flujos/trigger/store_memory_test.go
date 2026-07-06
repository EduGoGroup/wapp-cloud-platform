package trigger_test

import (
	"context"
	"errors"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
)

// mustInsert inserta y devuelve la regla persistida, fallando ante error.
func mustInsert(t *testing.T, s trigger.Store, r trigger.Rule) trigger.Rule {
	t.Helper()
	out, err := s.Insert(context.Background(), r)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	return out
}

func TestMemoryStore_InsertAssignsIDAndPersists(t *testing.T) {
	ctx := context.Background()
	s := trigger.NewMemoryStore()

	out := mustInsert(t, s, trigger.Rule{TenantID: "t1", Kind: trigger.KindKeyword, Keyword: "pedido", MatchType: trigger.MatchExact, FlowID: "carrito", Priority: 5, Enabled: true})
	if out.TriggerID == "" {
		t.Fatal("Insert debe asignar trigger_id")
	}

	got, err := s.Get(ctx, "t1", out.TriggerID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Keyword != "pedido" || got.FlowID != "carrito" || got.Priority != 5 || !got.Enabled {
		t.Fatalf("regla persistida no coincide: %+v", got)
	}
}

func TestMemoryStore_TenantIsolation(t *testing.T) {
	ctx := context.Background()
	s := trigger.NewMemoryStore()
	r := mustInsert(t, s, trigger.Rule{TenantID: "t1", Kind: trigger.KindKeyword, Keyword: "x", Enabled: true})

	if _, err := s.Get(ctx, "t2", r.TriggerID); !errors.Is(err, trigger.ErrTriggerNotFound) {
		t.Fatalf("Get cross-tenant debe ser ErrTriggerNotFound, got %v", err)
	}
	if err := s.Delete(ctx, "t2", r.TriggerID); !errors.Is(err, trigger.ErrTriggerNotFound) {
		t.Fatalf("Delete cross-tenant debe ser ErrTriggerNotFound, got %v", err)
	}
	if _, err := s.Get(ctx, "t1", r.TriggerID); err != nil {
		t.Fatalf("la regla de t1 no debió borrarse: %v", err)
	}
}

func TestMemoryStore_ListAndListByKind(t *testing.T) {
	ctx := context.Background()
	s := trigger.NewMemoryStore()
	mustInsert(t, s, trigger.Rule{TenantID: "t1", Kind: trigger.KindKeyword, Keyword: "a", Enabled: true})
	mustInsert(t, s, trigger.Rule{TenantID: "t1", Kind: trigger.KindFallback, FlowID: "menu", Enabled: true})
	mustInsert(t, s, trigger.Rule{TenantID: "t2", Kind: trigger.KindKeyword, Keyword: "b", Enabled: true})

	all, err := s.List(ctx, "t1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("List t1 esperaba 2, got %d", len(all))
	}
	kws, err := s.ListByKind(ctx, "t1", trigger.KindKeyword)
	if err != nil {
		t.Fatalf("listByKind: %v", err)
	}
	if len(kws) != 1 || kws[0].Keyword != "a" {
		t.Fatalf("ListByKind keyword t1 inesperado: %+v", kws)
	}
	fbs, err := s.ListByKind(ctx, "t1", trigger.KindFallback)
	if err != nil {
		t.Fatalf("listByKind fallback: %v", err)
	}
	if len(fbs) != 1 || fbs[0].FlowID != "menu" {
		t.Fatalf("ListByKind fallback t1 inesperado: %+v", fbs)
	}
}

func TestMemoryStore_DeleteRemoves(t *testing.T) {
	ctx := context.Background()
	s := trigger.NewMemoryStore()
	r := mustInsert(t, s, trigger.Rule{TenantID: "t1", Kind: trigger.KindKeyword, Keyword: "x", Enabled: true})
	if err := s.Delete(ctx, "t1", r.TriggerID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.Get(ctx, "t1", r.TriggerID); !errors.Is(err, trigger.ErrTriggerNotFound) {
		t.Fatalf("tras Delete, Get debe ser ErrTriggerNotFound, got %v", err)
	}
}
