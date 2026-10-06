package grpc

// El disparador de compatibilidad del registro (warmOnRegister) y su orden con el latido
// (R-G16, R-G17 «calienta salvo el canal de control»). Es la mitad de readiness.go donde vive
// T-6: el cero es «no lo dice» y ese Edge se calienta igual; solo se calla ante quien YA
// habló —READY, porque su flanco ya calentó; DOWN, porque el trabajo volvería OLLAMA_DOWN—.
//
// El orden real lo pone el bucle de Connect: encamina el frame (observeReadiness) y DESPUÉS
// pregunta (warmOnRegister). Aquí se reproduce llamando a los dos en ese orden.

import (
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	cltransport "github.com/EduGoGroup/wapp-cloudlink/transport"
)

// requireWarmups afirma cuántos calentamientos van y que ninguno despertó al pipeline de más.
func (r *readinessRig) requireWarmups(t *testing.T, warmups, wakeups int) {
	t.Helper()
	if len(r.warms) != warmups || len(r.readies) != wakeups {
		t.Fatalf("calentamientos = %d, avisos al pipeline = %d; se esperaban %d y %d",
			len(r.warms), len(r.readies), warmups, wakeups)
	}
}

// Un Edge que no ha dicho nada se calienta al registrar una sesión, con el kind VACÍO y la
// dirección de esa sesión. No es un flanco: ni despierta al pipeline ni deja su línea de log.
func TestWarmOnRegisterWarmsAnEdgeThatSaysNothing(t *testing.T) {
	t.Parallel()
	rig := newReadinessRig()
	rig.srv.warmOnRegister(phone("tenant-1", "edge-1", "s-1"))

	rig.requireWarmups(t, 1, 0)
	if want := (warmCall{"tenant-1", "edge-1", "s-1", ""}); rig.warms[0] != want {
		t.Errorf("OnWarmup recibió %+v, se esperaba %+v", rig.warms[0], want)
	}
	if rig.log.String() != "" {
		t.Errorf("el calentamiento del registro no deja rastro en el log: %q", rig.log.String())
	}
	if n := len(rig.srv.edgeReadiness); n != 0 {
		t.Errorf("preguntar dejó %d entradas en edgeReadiness", n)
	}
}

// Orden 1 — el primer latido no dice nada: el registro calienta como siempre. Y lo hace por
// CADA sesión que se registre mientras el Edge siga callado (no hay cerrojo aquí: el «uno en
// vuelo por Edge» es del consumidor).
func TestRegisterAfterASilentHeartbeatWarmsAsBefore(t *testing.T) {
	t.Parallel()
	rig := newReadinessRig()
	first, second := phone("tenant-1", "edge-1", "s-1"), phone("tenant-1", "edge-1", "s-2")

	rig.says(first, saysNothing)
	rig.srv.warmOnRegister(first)
	rig.requireWarmups(t, 1, 0)

	rig.says(second, saysNothing)
	rig.srv.warmOnRegister(second)
	rig.requireWarmups(t, 2, 0)
	if rig.warms[1].sessionID != "s-2" {
		t.Errorf("el segundo calentamiento va por %q, se esperaba s-2", rig.warms[1].sessionID)
	}
}

// Orden 2 — el primer latido dice DOWN: cero calentamientos hasta que diga READY, y entonces
// el que dispara es el flanco.
func TestRegisterAfterADownHeartbeatDoesNotWarmUntilReady(t *testing.T) {
	t.Parallel()
	rig := newReadinessRig()
	cc := phone("tenant-1", "edge-1", "s-1")

	rig.says(cc, saysDown)
	rig.srv.warmOnRegister(cc)
	rig.srv.warmOnRegister(phone("tenant-1", "edge-1", "s-2")) // otra sesión del mismo Edge
	rig.requireWarmups(t, 0, 0)

	rig.says(cc, saysNothing) // callar después no lo devuelve a «no lo dice»
	rig.srv.warmOnRegister(cc)
	rig.requireWarmups(t, 0, 0)

	rig.says(cc, saysReady)
	rig.requireWarmups(t, 1, 1)
}

// Orden 3 — el primer latido dice READY: lo dispara la transición y el registro no repite el
// aviso, ni para esa sesión ni para las siguientes de ese Edge.
func TestRegisterAfterAReadyHeartbeatDoesNotWarmTwice(t *testing.T) {
	t.Parallel()
	rig := newReadinessRig()
	cc := phone("tenant-1", "edge-1", "s-1")

	rig.says(cc, saysReady)
	rig.srv.warmOnRegister(cc)
	rig.srv.warmOnRegister(phone("tenant-1", "edge-1", "s-2"))
	rig.requireWarmups(t, 1, 1)
}

// La pregunta es por (tenant, Edge): que un Edge haya hablado no calla el registro de otro
// Edge del tenant, ni el del mismo edge_id bajo otro tenant.
func TestWarmOnRegisterAsksAboutThatEdgeOnly(t *testing.T) {
	t.Parallel()
	for _, said := range []cloudlinkv1.InferenceReadiness{saysDown, saysReady} {
		t.Run(said.String(), func(t *testing.T) {
			t.Parallel()
			rig := newReadinessRig()
			rig.says(phone("tenant-1", "edge-1", "s-1"), said)
			before := len(rig.warms)

			rig.srv.warmOnRegister(phone("tenant-1", "edge-2", "s-2"))
			rig.srv.warmOnRegister(phone("tenant-2", "edge-1", "s-3"))
			if got := len(rig.warms) - before; got != 2 {
				t.Fatalf("calentamientos de los otros dos Edge = %d, se esperaban 2", got)
			}
			rig.srv.warmOnRegister(phone("tenant-1", "edge-1", "s-4"))
			if got := len(rig.warms) - before; got != 2 {
				t.Fatalf("el Edge que ya habló se calentó por registro (%d)", got)
			}
		})
	}
}

// El canal de control NO se calienta (no hay teléfono detrás), y sin identidad mTLS tampoco.
func TestWarmOnRegisterSkipsTheControlChannelAndAnonymousStreams(t *testing.T) {
	t.Parallel()
	cases := map[string]connCtx{
		"control channel":  phone("tenant-1", "edge-1", cltransport.ControlSessionID),
		"without identity": {sessionID: "s-1", tenantID: "tenant-1", edgeID: "edge-1"},
	}
	for name, cc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rig := newReadinessRig()
			rig.srv.warmOnRegister(cc)
			rig.requireWarmups(t, 0, 0)
		})
	}
}

// Sin OnWarmup cableado no hay a quién avisar y no revienta; y el aviso del registro nunca
// pasa por OnEdgeReady.
func TestWarmOnRegisterWithoutAWarmupHookDoesNothing(t *testing.T) {
	t.Parallel()
	rig := newReadinessRig()
	rig.srv.OnWarmup = nil
	rig.srv.warmOnRegister(phone("tenant-1", "edge-1", "s-1"))
	rig.requireWarmups(t, 0, 0)
}
