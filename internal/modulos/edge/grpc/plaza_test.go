package grpc

// La dirección de la plaza (R-G15, R3.4.d; T-12): QUÉ EDGE atendería una inferencia. Es la
// otra mitad de la pareja ADR-0048 —la de Infer está en inference_dispatch_affinity_test.go—:
// el aforo tiene que proteger al mismo Edge por el que saldrá el frame, así que PlazaDe hereda
// el criterio entero, incluida la readiness (T-6).

import (
	"context"
	"sync"
	"testing"
)

// requirePlaza afirma la plaza varias veces: el recorrido de un map es aleatorio y una sola no
// distingue «elige bien» de «tuvo suerte». want vacío = no hay plaza.
func (r *inferRig) requirePlaza(t *testing.T, tenantID, originSessionID, want string) {
	t.Helper()
	for range 10 {
		edgeID, ok := r.srv.PlazaDe(tenantID, originSessionID)
		if ok != (want != "") || edgeID != want {
			t.Fatalf("PlazaDe(%q, %q) = (%q, %v), se esperaba (%q, %v)", tenantID, originSessionID, edgeID, ok, want, want != "")
		}
	}
}

// T-1: el aforo por Edge busca PlazaDe por ASERCIÓN DE TIPO, con este nombre y esta firma. Si
// cambian, la aserción da false y el aforo se apaga con un Warn y ningún rojo.
func TestPlazaDeKeepsTheSignatureTheGateAssertsOn(t *testing.T) {
	t.Parallel()
	var srv any = newInferRig(t).srv
	if _, ok := srv.(interface {
		PlazaDe(tenantID, originSessionID string) (string, bool)
	}); !ok {
		t.Fatal("*Server ya no cumple la interfaz por la que el aforo busca PlazaDe")
	}
}

// 🔴 Mitad 1 de la pareja ADR-0048: el Edge que dijo DOWN no tiene la plaza. Está puesto para
// que el orden alfabético lo ELIGIERA (s-aaa < s-zzz). Con toda la flota del tenant en DOWN no
// hay plaza.
func TestPlazaDeTheEdgeThatSaidDownIsNotThePlaza(t *testing.T) {
	t.Parallel()
	rig := newInferRig(t)
	rig.live(t, "tenant-1", "edge-down", "s-aaa", nil)
	rig.live(t, "tenant-1", "edge-ready", "s-zzz", nil)
	rig.declare("tenant-1", "edge-down", saysDown)
	rig.declare("tenant-1", "edge-ready", saysReady)

	rig.requirePlaza(t, "tenant-1", "", "edge-ready")

	rig.declare("tenant-1", "edge-ready", saysDown)
	rig.requirePlaza(t, "tenant-1", "", "")

	rig.declare("tenant-1", "edge-down", saysReady)
	rig.requirePlaza(t, "tenant-1", "", "edge-down")
}

// 🔴 Mitad 2 de la pareja (T-6): el Edge que NO LO DICE sigue teniendo plaza. READY ordena, no
// cierra: gana sobre el que calla, y el que calla gana sobre el que dijo DOWN.
func TestPlazaDeTheEdgeThatDoesNotSayStaysEligible(t *testing.T) {
	t.Parallel()
	rig := newInferRig(t)
	rig.live(t, "tenant-1", "edge-quiet", "s-mmm", nil)
	rig.requirePlaza(t, "tenant-1", "", "edge-quiet")
	rig.declare("tenant-1", "edge-quiet", saysNothing)
	rig.requirePlaza(t, "tenant-1", "", "edge-quiet")

	rig.live(t, "tenant-1", "edge-down", "s-aaa", nil)
	rig.declare("tenant-1", "edge-down", saysDown)
	rig.requirePlaza(t, "tenant-1", "", "edge-quiet")

	rig.live(t, "tenant-1", "edge-ready", "s-zzz", nil)
	rig.declare("tenant-1", "edge-ready", saysReady)
	rig.requirePlaza(t, "tenant-1", "", "edge-ready")
}

// T-12: la plaza es del EDGE, no de la sesión. Tres teléfonos de un Edge son un solo Ollama:
// pregunte la conversación que pregunte, la plaza es la misma. Y la conversación viva manda:
// su plaza es la de SU Edge aunque haya dicho DOWN (el candidato vivo no mira la readiness).
func TestPlazaDeIsTheEdgeOfTheLiveOrigin(t *testing.T) {
	t.Parallel()
	rig := newInferRig(t)
	for _, sid := range []string{"s-b1", "s-b2", "s-b3"} {
		rig.live(t, "tenant-1", "edge-b", sid, nil)
	}
	rig.live(t, "tenant-1", "edge-a", "s-a1", nil)

	for _, sid := range []string{"s-b1", "s-b2", "s-b3"} {
		rig.requirePlaza(t, "tenant-1", sid, "edge-b")
	}
	rig.requirePlaza(t, "tenant-1", "s-a1", "edge-a")
	rig.requirePlaza(t, "tenant-1", "", "edge-a")       // sin origen: la primera alfabética
	rig.requirePlaza(t, "tenant-1", "s-gone", "edge-a") // origen sin stream: igual, y no es un error

	rig.declare("tenant-1", "edge-b", saysDown)
	rig.declare("tenant-1", "edge-a", saysReady)
	rig.requirePlaza(t, "tenant-1", "s-b2", "edge-b")
}

// Sin sesión viva del tenant no hay plaza, y la plaza es SIEMPRE de un Edge del tenant: el
// mismo edge_id bajo dos empresas son dos máquinas, y cada una ve la suya.
func TestPlazaDeIsScopedToTheTenant(t *testing.T) {
	t.Parallel()
	rig := newInferRig(t)
	rig.requirePlaza(t, "tenant-1", "", "")

	rig.live(t, "tenant-2", "edge-shared-id", "s-2", nil)
	rig.live(t, "tenant-2", "edge-only-2", "s-0", nil)
	rig.requirePlaza(t, "tenant-1", "", "")
	rig.requirePlaza(t, "tenant-1", "s-gone", "")

	rig.live(t, "tenant-1", "edge-shared-id", "s-1", nil)
	rig.requirePlaza(t, "tenant-1", "", "edge-shared-id")
	rig.requirePlaza(t, "tenant-2", "", "edge-only-2")
	rig.requirePlaza(t, "tenant-2", "s-2", "edge-shared-id")
}

// R-G15: «PlazaDe es el mismo Edge que atiende la inferencia». Para cada escenario se pregunta
// la plaza y se lanza la inferencia: el frame sale por una sesión de ESE Edge. Si los dos
// caminos se separaran, el aforo protegería una máquina y la petición saldría por otra.
func TestPlazaDeIsTheEdgeThatServesTheInference(t *testing.T) {
	t.Parallel()
	rig := newInferRig(t)
	edgeOf := map[string]string{"s-d1": "edge-down", "s-q1": "edge-quiet", "s-r1": "edge-ready", "s-r2": "edge-ready"}
	sessions := make(map[string]*inferSession)
	for sid, edgeID := range edgeOf {
		sessions[sid] = rig.live(t, "tenant-1", edgeID, sid, rig.answers(t, modelOutput))
	}
	rig.declare("tenant-1", "edge-down", saysDown)
	rig.declare("tenant-1", "edge-ready", saysReady)

	for _, origin := range []string{"", "s-gone", "s-d1", "s-q1", "s-r1", "s-r2"} {
		plaza, ok := rig.srv.PlazaDe("tenant-1", origin)
		if !ok {
			t.Fatalf("PlazaDe(origen %q) no encontró plaza", origin)
		}
		before := make(map[string]int)
		for sid, s := range sessions {
			before[sid] = len(s.received())
		}
		if _, err := rig.srv.Infer(context.Background(), "tenant-1", InferRequest{Prompt: "p", OriginSessionID: origin}); err != nil {
			t.Fatalf("Infer(origen %q) = %v", origin, err)
		}
		for sid, s := range sessions {
			if len(s.received()) != before[sid] && edgeOf[sid] != plaza {
				t.Errorf("origen %q: la plaza es de %s y el frame salió por %s, que es de %s", origin, plaza, sid, edgeOf[sid])
			}
		}
	}
}

// 🟡 Conducta del viejo, copiada y afirmada (no se corrige aquí): hay dos casos en que Infer
// SÍ envía y PlazaDe dice que no hay plaza, porque el Edge se busca solo en el seguimiento del
// tenant: la sesión viva de OTRO tenant pasada como origen, y la sesión que sigue en el
// Registry pero ya no en el seguimiento (la reconexión rápida del hallazgo 45).
func TestPlazaDeHasNoPlazaForALiveOriginItCannotPlace(t *testing.T) {
	t.Parallel()
	rig := newInferRig(t)
	rig.live(t, "tenant-1", "edge-1", "s-own", nil)
	rig.live(t, "tenant-2", "edge-9", "s-foreign", nil)
	rig.live(t, "tenant-1", "edge-2", "s-untracked", nil)
	rig.srv.untrackSession(phone("tenant-1", "edge-2", "s-untracked"))

	rig.requirePlaza(t, "tenant-1", "s-foreign", "")
	rig.requirePlaza(t, "tenant-1", "s-untracked", "")
	rig.requirePlaza(t, "tenant-1", "s-own", "edge-1")
}

// La plaza se lee BAJO EL CANDADO del seguimiento: mientras las sesiones de otro Edge entran y
// salen (cada registro y cada cierre de stream escriben ese mapa), PlazaDe sigue contestando
// lo mismo. Sin el candado, el detector de carreras lo canta aquí.
func TestPlazaDeIsSafeWhileSessionsComeAndGo(t *testing.T) {
	t.Parallel()
	rig := newInferRig(t)
	rig.live(t, "tenant-1", "edge-1", "s-1", nil)
	const rounds = 300

	var churn sync.WaitGroup
	churn.Go(func() {
		passing := phone("tenant-1", "edge-2", "s-passing")
		for range rounds {
			rig.srv.trackSession(passing)
			rig.srv.untrackSession(passing)
		}
	})
	for range rounds {
		if edgeID, ok := rig.srv.PlazaDe("tenant-1", "s-1"); !ok || edgeID != "edge-1" {
			t.Errorf("PlazaDe = (%q, %v) con otro Edge entrando y saliendo, se esperaba edge-1", edgeID, ok)
			break
		}
	}
	churn.Wait()
}
