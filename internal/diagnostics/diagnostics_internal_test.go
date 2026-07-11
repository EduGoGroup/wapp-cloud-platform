package diagnostics

import (
	"context"
	"errors"
	"testing"
	"time"
)

// consentOK falla el test si ConsentEnabled devuelve error, y devuelve el flag.
func consentOK(t *testing.T, m *MemoryStore, tenantID string) bool {
	t.Helper()
	ok, err := m.ConsentEnabled(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("ConsentEnabled(%s): %v", tenantID, err)
	}
	return ok
}

// mustCreate crea una solicitud o falla el test.
func mustCreate(t *testing.T, m *MemoryStore, tenantID, sessionID, commandID string, exp time.Time) {
	t.Helper()
	if err := m.CreateRequest(context.Background(), tenantID, sessionID, commandID, "user-1", exp); err != nil {
		t.Fatalf("CreateRequest(%s): %v", commandID, err)
	}
}

// saved corre SaveBundle y falla ante error de infraestructura, devolviendo found.
func saved(t *testing.T, m *MemoryStore, tenantID, sessionID, commandID string, b Bundle) bool {
	t.Helper()
	found, err := m.SaveBundle(context.Background(), tenantID, sessionID, commandID, b)
	if err != nil {
		t.Fatalf("SaveBundle(%s): %v", commandID, err)
	}
	return found
}

func TestConsent_DefaultOn_OptOut(t *testing.T) {
	m := NewMemoryStore()

	// Default ON (opt-out): sin fila ⇒ consentido.
	if !consentOK(t, m, "t1") {
		t.Fatal("default debe ser ON")
	}
	// Opt-out explícito ⇒ off.
	m.SetConsent("t1", false)
	if consentOK(t, m, "t1") {
		t.Fatal("tras opt-out debe ser OFF")
	}
	// Re-consentir ⇒ on.
	m.SetConsent("t1", true)
	if !consentOK(t, m, "t1") {
		t.Fatal("tras re-consentir debe ser ON")
	}
	// Otro tenant no se ve afectado.
	if !consentOK(t, m, "t2") {
		t.Fatal("otro tenant conserva el default ON")
	}
}

func TestRequest_Save_Get_Correlacion(t *testing.T) {
	m := NewMemoryStore()
	ctx := context.Background()

	mustCreate(t, m, "t1", "sess-1", "cmd-1", time.Now().Add(time.Hour))
	// Aún sin bundle ⇒ pendiente.
	if _, err := m.GetBundle(ctx, "t1", "cmd-1"); !errors.Is(err, ErrPending) {
		t.Fatalf("esperaba ErrPending, got %v", err)
	}
	// Llega el bundle correlacionado por command_id + (tenant, sesión).
	if !saved(t, m, "t1", "sess-1", "cmd-1", Bundle{LogTail: "log", GoroutineDump: "dump"}) {
		t.Fatal("SaveBundle debió correlacionar")
	}
	rec, err := m.GetBundle(ctx, "t1", "cmd-1")
	if err != nil {
		t.Fatalf("GetBundle ready: %v", err)
	}
	if rec.Bundle.LogTail != "log" || rec.RequestedBy != "user-1" || rec.SessionID != "sess-1" || rec.ReceivedAt.IsZero() {
		t.Fatalf("record incompleto: %+v", rec)
	}
}

func TestSaveBundle_Huerfano_Y_Mismatch(t *testing.T) {
	m := NewMemoryStore()

	// Bundle sin solicitud pendiente ⇒ huérfano (found=false, se ignora).
	if saved(t, m, "t1", "sess-1", "cmd-x", Bundle{}) {
		t.Fatal("un bundle huérfano no debe correlacionar")
	}

	mustCreate(t, m, "t1", "sess-1", "cmd-1", time.Now().Add(time.Hour))
	// Mismatch de tenant ⇒ no correlaciona.
	if saved(t, m, "t2", "sess-1", "cmd-1", Bundle{}) {
		t.Fatal("un bundle de otro tenant no debe correlacionar")
	}
	// Mismatch de sesión ⇒ no correlaciona.
	if saved(t, m, "t1", "sess-otra", "cmd-1", Bundle{}) {
		t.Fatal("un bundle de otra sesión no debe correlacionar")
	}
}

func TestGetBundle_Expirado_YBorradoPerezoso(t *testing.T) {
	m := NewMemoryStore()
	ctx := context.Background()
	// Reloj fijo: solicitud que vence en el pasado.
	base := time.Now()
	m.now = func() time.Time { return base }
	mustCreate(t, m, "t1", "sess-1", "cmd-1", base.Add(time.Minute))
	_ = saved(t, m, "t1", "sess-1", "cmd-1", Bundle{LogTail: "x"})

	// Avanza el reloj más allá del TTL ⇒ 410 (ErrExpired) + borrado perezoso.
	m.now = func() time.Time { return base.Add(2 * time.Minute) }
	if _, err := m.GetBundle(ctx, "t1", "cmd-1"); !errors.Is(err, ErrExpired) {
		t.Fatalf("esperaba ErrExpired, got %v", err)
	}
	// Ya no existe (borrado perezoso).
	if _, err := m.GetBundle(ctx, "t1", "cmd-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tras expirar+borrar esperaba ErrNotFound, got %v", err)
	}
}

func TestCreateRequest_PurgaVencidas(t *testing.T) {
	m := NewMemoryStore()
	ctx := context.Background()
	base := time.Now()
	m.now = func() time.Time { return base }
	mustCreate(t, m, "t1", "sess-1", "cmd-viejo", base.Add(time.Minute))

	// Nueva solicitud con el reloj avanzado: la vieja (vencida) se purga de paso.
	m.now = func() time.Time { return base.Add(2 * time.Minute) }
	mustCreate(t, m, "t1", "sess-1", "cmd-nuevo", base.Add(time.Hour))
	if _, err := m.GetBundle(ctx, "t1", "cmd-viejo"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("la solicitud vencida debió purgarse, got %v", err)
	}
	if _, err := m.GetBundle(ctx, "t1", "cmd-nuevo"); !errors.Is(err, ErrPending) {
		t.Fatalf("la solicitud nueva debe seguir pendiente, got %v", err)
	}
}

func TestDeleteRequest_Rollback(t *testing.T) {
	m := NewMemoryStore()
	ctx := context.Background()
	mustCreate(t, m, "t1", "sess-1", "cmd-1", time.Now().Add(time.Hour))
	// Un tenant ajeno no puede borrarla.
	if err := m.DeleteRequest(ctx, "t2", "cmd-1"); err != nil {
		t.Fatalf("DeleteRequest ajeno: %v", err)
	}
	if _, err := m.GetBundle(ctx, "t1", "cmd-1"); !errors.Is(err, ErrPending) {
		t.Fatalf("no debió borrarse por otro tenant, got %v", err)
	}
	// El dueño sí.
	if err := m.DeleteRequest(ctx, "t1", "cmd-1"); err != nil {
		t.Fatalf("DeleteRequest dueño: %v", err)
	}
	if _, err := m.GetBundle(ctx, "t1", "cmd-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tras DeleteRequest esperaba ErrNotFound, got %v", err)
	}
}
