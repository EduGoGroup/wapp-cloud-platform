package fleet_test

import (
	"context"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet"
)

func TestMemoryOnlineThenOffline(t *testing.T) {
	t.Parallel()
	repo := fleet.NewMemoryRepository()
	ctx := context.Background()

	if err := repo.MarkOnline(ctx, "t", "edge-1", "s1"); err != nil {
		t.Fatalf("MarkOnline: %v", err)
	}

	s, found, err := repo.Get(ctx, "t", "edge-1", "s1")
	if err != nil || !found {
		t.Fatalf("Get: found=%v err=%v", found, err)
	}
	if s.State != fleet.StateOnline {
		t.Fatalf("estado: got %q, want online", s.State)
	}
	if s.LastConnectedAt.IsZero() || s.LastSeenAt.IsZero() {
		t.Fatal("last_connected_at/last_seen_at deberían estar poblados")
	}

	if err := repo.MarkOffline(ctx, "t", "edge-1", "s1"); err != nil {
		t.Fatalf("MarkOffline: %v", err)
	}
	s, _, err = repo.Get(ctx, "t", "edge-1", "s1")
	if err != nil {
		t.Fatalf("Get tras offline: %v", err)
	}
	if s.State != fleet.StateOffline {
		t.Fatalf("estado: got %q, want offline", s.State)
	}
}

func TestMemoryOfflineUnknownIsNoError(t *testing.T) {
	t.Parallel()
	repo := fleet.NewMemoryRepository()
	if err := repo.MarkOffline(context.Background(), "t", "e", "missing"); err != nil {
		t.Fatalf("MarkOffline de sesión inexistente no debería fallar: %v", err)
	}
}

func TestMemoryListByTenant(t *testing.T) {
	t.Parallel()
	repo := fleet.NewMemoryRepository()
	ctx := context.Background()

	for _, s := range []struct{ tenant, edge, sess string }{
		{"t1", "e1", "s1"},
		{"t1", "e1", "s2"},
		{"t2", "e9", "s9"},
	} {
		if err := repo.MarkOnline(ctx, s.tenant, s.edge, s.sess); err != nil {
			t.Fatalf("MarkOnline: %v", err)
		}
	}

	got, err := repo.List(ctx, "t1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("List(t1): got %d sesiones, want 2", len(got))
	}
}
