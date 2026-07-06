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

// TestMemoryMarkLoggedOut: MarkLoggedOut deja la sesión en StateLoggedOut, un
// estado DISTINTO de offline (zombie por señal explícita, no offline por red).
func TestMemoryMarkLoggedOut(t *testing.T) {
	t.Parallel()
	repo := fleet.NewMemoryRepository()
	ctx := context.Background()
	if err := repo.MarkOnline(ctx, "t", "e", "s1"); err != nil {
		t.Fatalf("MarkOnline: %v", err)
	}
	if err := repo.MarkLoggedOut(ctx, "t", "e", "s1"); err != nil {
		t.Fatalf("MarkLoggedOut: %v", err)
	}
	s, found, err := repo.Get(ctx, "t", "e", "s1")
	if err != nil || !found {
		t.Fatalf("Get: found=%v err=%v", found, err)
	}
	if s.State != fleet.StateLoggedOut {
		t.Fatalf("estado: got %q, want loggedout", s.State)
	}
	if s.State == fleet.StateOffline {
		t.Fatal("loggedout no debe confundirse con offline")
	}
}

// TestMemoryMarkLoggedOutUnknownIsNoError: marcar zombie una sesión inexistente
// no falla (mismo contrato que MarkOffline).
func TestMemoryMarkLoggedOutUnknownIsNoError(t *testing.T) {
	t.Parallel()
	repo := fleet.NewMemoryRepository()
	if err := repo.MarkLoggedOut(context.Background(), "t", "e", "missing"); err != nil {
		t.Fatalf("MarkLoggedOut de sesión inexistente no debería fallar: %v", err)
	}
}

// TestMemorySetStateValidationAndIsolation: SetState rechaza estados no admin
// (online / arbitrario) con ErrInvalidState, y solo toca sesiones del tenant dado
// (aislamiento INV-8; found=false para un tenant ajeno).
func TestMemorySetStateValidationAndIsolation(t *testing.T) {
	t.Parallel()
	repo := fleet.NewMemoryRepository()
	ctx := context.Background()
	if err := repo.MarkOnline(ctx, "t1", "e", "s1"); err != nil {
		t.Fatalf("MarkOnline t1: %v", err)
	}
	if err := repo.MarkOnline(ctx, "t2", "e", "s1"); err != nil {
		t.Fatalf("MarkOnline t2: %v", err)
	}

	// online NO es admin-admitido: ErrInvalidState.
	if _, err := repo.SetState(ctx, "t1", "s1", fleet.StateOnline); !errors.Is(err, fleet.ErrInvalidState) {
		t.Fatalf("StateOnline debería dar ErrInvalidState, dio: %v", err)
	}
	// loggedout sí: retira la sesión de t1.
	found, err := repo.SetState(ctx, "t1", "s1", fleet.StateLoggedOut)
	if err != nil || !found {
		t.Fatalf("SetState loggedout: found=%v err=%v", found, err)
	}
	// La sesión de t2 (mismo session_id) NO se ve afectada: sigue online.
	s2, _, err := repo.Get(ctx, "t2", "e", "s1")
	if err != nil {
		t.Fatalf("Get t2: %v", err)
	}
	if s2.State != fleet.StateOnline {
		t.Fatalf("aislamiento roto: la sesión de t2 cambió a %q", s2.State)
	}
	// Un tenant ajeno no encuentra la sesión (found=false).
	found, err = repo.SetState(ctx, "t-otro", "s1", fleet.StateOffline)
	if err != nil {
		t.Fatalf("SetState t-otro: %v", err)
	}
	if found {
		t.Fatal("un tenant ajeno no debería encontrar la sesión (found=true)")
	}
}

// TestMemoryCountLiveBySelfPn: cuenta sesiones vivas por self_pn dentro del tenant;
// una sesión zombie (loggedout) NO cuenta; otro tenant no contamina el conteo.
func TestMemoryCountLiveBySelfPn(t *testing.T) {
	t.Parallel()
	repo := fleet.NewMemoryRepository()
	ctx := context.Background()
	const pn = "593999000111"

	// Tres sesiones del mismo tenant con el mismo self_pn: dos vivas, una zombie.
	for _, sess := range []string{"s1", "s2", "s3"} {
		if err := repo.MarkOnline(ctx, "t", "e", sess); err != nil {
			t.Fatalf("MarkOnline %s: %v", sess, err)
		}
		if err := repo.SetSelfPn(ctx, "t", "e", sess, pn); err != nil {
			t.Fatalf("SetSelfPn %s: %v", sess, err)
		}
	}
	if err := repo.MarkLoggedOut(ctx, "t", "e", "s3"); err != nil {
		t.Fatalf("MarkLoggedOut s3: %v", err)
	}
	// Otro tenant con el mismo número no debe contaminar.
	if err := repo.MarkOnline(ctx, "t2", "e", "x1"); err != nil {
		t.Fatalf("MarkOnline t2: %v", err)
	}
	if err := repo.SetSelfPn(ctx, "t2", "e", "x1", pn); err != nil {
		t.Fatalf("SetSelfPn t2: %v", err)
	}

	n, err := repo.CountLiveBySelfPn(ctx, "t", pn)
	if err != nil {
		t.Fatalf("CountLiveBySelfPn: %v", err)
	}
	if n != 2 {
		t.Fatalf("conteo vivas: got %d, want 2 (s3 zombie no cuenta; t2 es otro tenant)", n)
	}
	// selfPn vacío ⇒ 0 sin error.
	if n, err := repo.CountLiveBySelfPn(ctx, "t", ""); err != nil || n != 0 {
		t.Fatalf("CountLiveBySelfPn vacío: n=%d err=%v, want 0/nil", n, err)
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
