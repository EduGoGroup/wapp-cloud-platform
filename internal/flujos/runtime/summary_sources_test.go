package runtime_test

import (
	"context"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

// ---------------------------------------------------------------------------
// Plan 043 · Ola 3 · T3.4 — el adaptador de la fuente durable del resumen
// ---------------------------------------------------------------------------

// sembrarPedido deja una solicitud "open" de `sessionID` con una línea, y devuelve su
// id. Escribe por las mismas puertas que el carrito en producción (UpsertIntake +
// ReplaceIntakeItems), no por un atajo del doble.
func sembrarPedido(t *testing.T, repo *store.MemoryRepository, id, sessionID string) string {
	t.Helper()
	ctx := context.Background()
	if err := repo.UpsertIntake(ctx, store.Intake{
		ID: id, TenantID: testTenant, ContactID: "c-1", SessionID: sessionID, Status: "open",
	}); err != nil {
		t.Fatalf("sembrar solicitud: %v", err)
	}
	if err := repo.ReplaceIntakeItems(ctx, id, []store.IntakeItem{
		{IntakeID: id, SKU: "CAFE", Label: "Café", Qty: 2, UnitPrice: 2.5, Customization: "sin azúcar"},
	}); err != nil {
		t.Fatalf("sembrar líneas: %v", err)
	}
	return id
}

// TestIntakeLines_DevuelveLasLineasDeSuSesion: el camino normal, y de paso el mapeo —
// que la personalización viaje es parte del contrato (D-041.17): quien prepara el
// pedido tiene que leer el «sin azúcar».
func TestIntakeLines_DevuelveLasLineasDeSuSesion(t *testing.T) {
	repo := store.NewMemoryRepository()
	sembrarPedido(t, repo, "3f2b1c44-1d9e-4a7a-9c11-0a1b2c3d4e5f", testSession)

	lineas, err := runtime.NewSummarySources(repo).Lines.OpenIntakeLines(context.Background(), testTenant, testSession, "c-1")
	if err != nil {
		t.Fatalf("OpenIntakeLines: %v", err)
	}
	if len(lineas) != 1 {
		t.Fatalf("esperaba UNA línea, hay %d: %+v", len(lineas), lineas)
	}
	l := lineas[0]
	if l.SKU != "CAFE" || l.Label != "Café" || l.Qty != 2 || l.UnitPrice != 2.5 {
		t.Fatalf("la línea no se mapeó entera: %+v", l)
	}
	if l.Customization != "sin azúcar" {
		t.Fatalf("la personalización tiene que viajar (D-041.17): %q", l.Customization)
	}
}

// TestIntakeLines_NoSeLlevaElPedidoDeOtraSesion es REQ-18, y es el assert que justifica
// que este adaptador exista en vez de llamar a GetOpenIntake directamente.
//
// La trampa es fina: GetOpenIntake resuelve por (tenant, contacto) SIN sesión —hay UNA
// solicitud abierta por contacto—, mientras que un evento es de (tenant, SESIÓN,
// contacto). Un tenant con dos sesiones hablando con la misma persona tiene un solo
// pedido abierto, y sin este filtro el resumen del evento de la sesión B enseñaría lo
// que se armó en la A.
func TestIntakeLines_NoSeLlevaElPedidoDeOtraSesion(t *testing.T) {
	repo := store.NewMemoryRepository()
	sembrarPedido(t, repo, "3f2b1c44-1d9e-4a7a-9c11-0a1b2c3d4e5f", "sess-A")

	// El evento vive en sess-B; la solicitud abierta del contacto es de sess-A.
	lineas, err := runtime.NewSummarySources(repo).Lines.OpenIntakeLines(context.Background(), testTenant, "sess-B", "c-1")
	if err != nil {
		t.Fatalf("OpenIntakeLines: %v", err)
	}
	if len(lineas) != 0 {
		t.Fatalf("el pedido de otra sesión NO se resume (REQ-18); llegaron %d líneas: %+v", len(lineas), lineas)
	}
}

// TestIntakeLines_SinSolicitudAbiertaNoEsUnError: no tener nada que resumir es normal
// —la mayoría de las conversaciones no tienen pedido abierto— y por eso devuelve la
// lista vacía sin error. Convertirlo en error llenaría el log de ruido y haría que un
// abandono corriente pareciera un fallo.
func TestIntakeLines_SinSolicitudAbiertaNoEsUnError(t *testing.T) {
	repo := store.NewMemoryRepository()

	lineas, err := runtime.NewSummarySources(repo).Lines.OpenIntakeLines(context.Background(), testTenant, testSession, "c-sin-pedido")
	if err != nil {
		t.Fatalf("sin solicitud abierta no es un error: %v", err)
	}
	if len(lineas) != 0 {
		t.Fatalf("y no hay líneas que devolver: %+v", lineas)
	}
}
