package gatewaygrpc

import (
	"context"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet/fleettest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
)

// T3.4 — el registro de sesión gana RELOJ sin salir del bucle Recv (Plan 050 · Ola 3).
//
// Los dos tests de abajo cubren las dos mitades del cambio, y las dos hacen falta:
// que el reloj EXISTA (el handshake corría sobre el ctx crudo del stream, que no trae
// deadline) y que sirva PARA ALGO (que el bucle se rinda en vez de colgarse). Lo que
// NO debe cambiar —que el trabajo siga inline— lo guarda
// TestElHandshakeSigueResolviendoseEnElBucleRecv, en connect_lane_internal_test.go.

// TestElHandshakeTraeSuPropioReloj: las escrituras del registro de sesión llegan a
// fleet con un ctx CON deadline. Reusa el vigilante del barrido con exigeDeadline
// puesto — el mismo detector, ahora apuntado al handshake.
func TestElHandshakeTraeSuPropioReloj(t *testing.T) {
	t.Parallel()
	repo := &fleetVigilante{MemoryRepository: fleet.NewMemoryRepository(), exigeDeadline: true}
	srv := New(session.NewRegistry(), laneLog(), WithFleet(repo))

	srv.onSessionRegistered(context.Background(), ccDePrueba("s-1"))

	repo.exigirDeadline(t)
	if vistas := repo.vistas(); len(vistas) != 1 || vistas[0] != "MarkOnline" {
		t.Fatalf("fleet vio %v, quiero exactamente [MarkOnline]: poner reloj no puede "+
			"significar dejar de registrar la sesión", vistas)
	}
}

// TestElHandshakeSeRindeYNoCuelgaElBucleRecv es el criterio literal de T3.4: con una
// base que no contesta, onSessionRegistered VUELVE al vencer su presupuesto en vez de
// retener el bucle Recv del Edge. Sin reloj, esta llamada tardaría los 3 s completos de
// la latencia inyectada (y contra una base de verdad atascada, sin techo).
//
// El presupuesto se baja con WithWorkTimeout, que es la MISMA perilla que usa el
// carril: si el registro dejara de colgar de s.workBudget, el default de 5 s haría que
// la llamada esperara los 3 s de latencia y el test se pondría rojo por tiempo. Es
// decir, este test también ata el reloj a su fuente, no solo a su existencia.
func TestElHandshakeSeRindeYNoCuelgaElBucleRecv(t *testing.T) {
	t.Parallel()
	const presupuesto = 80 * time.Millisecond
	const latencia = 3 * time.Second

	lento := fleettest.NewSlow(fleet.NewMemoryRepository(), latencia)
	srv := New(session.NewRegistry(), laneLog(), WithFleet(lento), WithWorkTimeout(presupuesto))

	inicio := time.Now()
	srv.onSessionRegistered(context.Background(), ccDePrueba("s-1"))
	tardo := time.Since(inicio)

	// El margen es amplio a propósito: lo que se afirma es «se rindió», no «se rindió
	// en 80 ms exactos». Un CI cargado puede estirar el plazo; lo que no puede es
	// convertirlo en los 3 s de la base atascada.
	if tardo >= latencia/2 {
		t.Fatalf("onSessionRegistered tardó %v con un presupuesto de %v y una base a %v: "+
			"el registro se quedó colgado del bucle Recv (T3.4)", tardo, presupuesto, latencia)
	}
}
