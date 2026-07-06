package fleet_test

import (
	"context"
	"errors"
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

// TestMemoryDefaultRoleIsBot: una sesión recién marcada online nace con rol bot
// (espeja la columna DEFAULT 'bot' ⇒ no-regresión).
func TestMemoryDefaultRoleIsBot(t *testing.T) {
	t.Parallel()
	repo := fleet.NewMemoryRepository()
	ctx := context.Background()
	if err := repo.MarkOnline(ctx, "t", "e", "s1"); err != nil {
		t.Fatalf("MarkOnline: %v", err)
	}
	s, _, err := repo.Get(ctx, "t", "e", "s1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if s.Role != fleet.RoleBot {
		t.Fatalf("rol por defecto: got %q, want bot", s.Role)
	}
}

// TestMemorySetRolePassivePreservedOnReconnect: SetRole a passive persiste y una
// reconexión (MarkOnline) NO revierte el rol (lo gobierna SetRole, no la señal
// de conexión).
func TestMemorySetRolePassivePreservedOnReconnect(t *testing.T) {
	t.Parallel()
	repo := fleet.NewMemoryRepository()
	ctx := context.Background()
	if err := repo.MarkOnline(ctx, "t", "e", "s1"); err != nil {
		t.Fatalf("MarkOnline: %v", err)
	}
	found, err := repo.SetRole(ctx, "t", "s1", fleet.RolePassive)
	if err != nil || !found {
		t.Fatalf("SetRole: found=%v err=%v", found, err)
	}
	if err := repo.MarkOnline(ctx, "t", "e", "s1"); err != nil {
		t.Fatalf("MarkOnline reconexión: %v", err)
	}
	s, _, err := repo.Get(ctx, "t", "e", "s1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if s.Role != fleet.RolePassive {
		t.Fatalf("el rol passive debería preservarse al reconectar, got %q", s.Role)
	}
}

// TestMemorySetRoleInvalid: un rol desconocido se rechaza con ErrInvalidRole y no
// muta nada.
func TestMemorySetRoleInvalid(t *testing.T) {
	t.Parallel()
	repo := fleet.NewMemoryRepository()
	ctx := context.Background()
	if err := repo.MarkOnline(ctx, "t", "e", "s1"); err != nil {
		t.Fatalf("MarkOnline: %v", err)
	}
	if _, err := repo.SetRole(ctx, "t", "s1", fleet.Role("supervisor")); !errors.Is(err, fleet.ErrInvalidRole) {
		t.Fatalf("rol inválido debería dar ErrInvalidRole, dio: %v", err)
	}
}

// TestMemorySetRoleTenantIsolation: SetRole solo toca sesiones del tenant dado. Una
// sesión con el MISMO session_id bajo otro tenant queda intacta y found=false para
// el tenant que no la posee (aislamiento multi-tenant, INV-8).
func TestMemorySetRoleTenantIsolation(t *testing.T) {
	t.Parallel()
	repo := fleet.NewMemoryRepository()
	ctx := context.Background()
	if err := repo.MarkOnline(ctx, "t1", "e", "shared-sess"); err != nil {
		t.Fatalf("MarkOnline t1: %v", err)
	}
	if err := repo.MarkOnline(ctx, "t2", "e", "shared-sess"); err != nil {
		t.Fatalf("MarkOnline t2: %v", err)
	}

	// t1 marca su sesión passive.
	found, err := repo.SetRole(ctx, "t1", "shared-sess", fleet.RolePassive)
	if err != nil || !found {
		t.Fatalf("SetRole t1: found=%v err=%v", found, err)
	}
	// La sesión de t2 (mismo session_id) NO se ve afectada: sigue bot.
	s2, _, err := repo.Get(ctx, "t2", "e", "shared-sess")
	if err != nil {
		t.Fatalf("Get t2: %v", err)
	}
	if s2.Role != fleet.RoleBot {
		t.Fatalf("aislamiento roto: la sesión de t2 cambió a %q", s2.Role)
	}
	// Un tenant que no posee la sesión no la encuentra (found=false).
	found, err = repo.SetRole(ctx, "t-otro", "shared-sess", fleet.RolePassive)
	if err != nil {
		t.Fatalf("SetRole t-otro: %v", err)
	}
	if found {
		t.Fatal("un tenant ajeno no debería encontrar la sesión (found=true)")
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
