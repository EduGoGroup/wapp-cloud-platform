package grpc

// El contrato de readiness.go visto desde el latido (R-G16): solo el FLANCO a READY calienta
// y avisa al pipeline; el cero (UNSPECIFIED) es «no lo dice» —ni calienta, ni olvida, ni se
// anota—; lo aprendido es por (tenant, Edge) y no por sesión; y los hooks corren inline, sin
// el candado del seguimiento tomado (T-13). El disparador de compatibilidad del registro
// (warmOnRegister) y el orden entre los dos se afirman en readiness_register_test.go.
//
// readiness.go no tiene exportados: nace en verde y se prueba por dentro, llamando a lo que
// llamará el bucle Recv de connect.go.

import (
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	cltransport "github.com/EduGoGroup/wapp-cloudlink/transport"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/session"
)

const (
	saysNothing = cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_UNSPECIFIED
	saysReady   = cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_READY
	saysDown    = cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_DOWN
)

// readyLogLine es la señal de que el flanco ocurrió, haya o no quien lo atienda.
const readyLogLine = "calentamiento: el Edge acaba de decir que puede servir inferencia"

type warmCall struct{ tenantID, edgeID, sessionID, kind string }

type readyCall struct{ tenantID, edgeID string }

// readinessRig es un Server con los dos hooks del flanco apuntando lo que reciben.
//
// 🔴 Sin mutex A PROPÓSITO: el contrato dice que los hooks corren INLINE, en la goroutine de
// quien llama. Si alguien los lanzara aparte, estas escrituras sin candado serían una carrera
// y -race lo diría.
type readinessRig struct {
	srv     *Server
	log     *logBuffer
	events  []string
	warms   []warmCall
	readies []readyCall
}

func newReadinessRig() *readinessRig {
	log, buf := capturedLog()
	r := &readinessRig{srv: New(session.NewRegistry(), log), log: buf}
	r.srv.OnWarmup = func(tenantID, edgeID, sessionID, kind string) {
		r.events = append(r.events, "warmup")
		r.warms = append(r.warms, warmCall{tenantID, edgeID, sessionID, kind})
	}
	r.srv.OnEdgeReady = func(tenantID, edgeID string) {
		r.events = append(r.events, "edge-ready")
		r.readies = append(r.readies, readyCall{tenantID, edgeID})
	}
	return r
}

// says hace llegar un latido de esa sesión con lo que el Edge dice.
func (r *readinessRig) says(cc connCtx, readiness cloudlinkv1.InferenceReadiness) {
	r.srv.observeReadiness(cc, &cloudlinkv1.Heartbeat{InferenceReadiness: readiness})
}

// requireEdges afirma cuántos flancos se han visto: tantos calentamientos como avisos al
// pipeline, y tantas líneas de log.
func (r *readinessRig) requireEdges(t *testing.T, want int) {
	t.Helper()
	if len(r.warms) != want || len(r.readies) != want {
		t.Fatalf("calentamientos = %d, avisos al pipeline = %d; se esperaban %d de cada", len(r.warms), len(r.readies), want)
	}
	if got := strings.Count(r.log.String(), readyLogLine); got != want {
		t.Fatalf("líneas de log del flanco = %d, se esperaban %d", got, want)
	}
}

// phone es el connCtx de una sesión real con identidad mTLS.
func phone(tenantID, edgeID, sessionID string) connCtx {
	return connCtx{sessionID: sessionID, tenantID: tenantID, edgeID: edgeID, hasIdentity: true}
}

// Solo la TRANSICIÓN a READY dispara: el estado viaja en todos los latidos y repetirlo no es
// un flanco; el cero ni dispara ni borra lo aprendido.
func TestOnlyTheTransitionToReadyFires(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		says []cloudlinkv1.InferenceReadiness
		want int
	}{
		{"first heartbeat seen is ready", []cloudlinkv1.InferenceReadiness{saysReady}, 1},
		{"silent then ready", []cloudlinkv1.InferenceReadiness{saysNothing, saysReady}, 1},
		{"down then ready", []cloudlinkv1.InferenceReadiness{saysDown, saysReady}, 1},
		{"ready repeated is state, not an event", []cloudlinkv1.InferenceReadiness{saysReady, saysReady, saysReady}, 1},
		{"ready, down, ready is two transitions", []cloudlinkv1.InferenceReadiness{saysReady, saysDown, saysReady}, 2},
		{"down never fires", []cloudlinkv1.InferenceReadiness{saysDown, saysDown}, 0},
		{"silence never fires", []cloudlinkv1.InferenceReadiness{saysNothing, saysNothing}, 0},
		{"silence does not forget ready", []cloudlinkv1.InferenceReadiness{saysReady, saysNothing, saysReady}, 1},
		{"silence does not forget down", []cloudlinkv1.InferenceReadiness{saysDown, saysNothing, saysReady}, 1},
		{"silence between two transitions", []cloudlinkv1.InferenceReadiness{saysReady, saysDown, saysNothing, saysReady}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rig := newReadinessRig()
			for _, r := range tc.says {
				rig.says(phone("tenant-1", "edge-1", "s-1"), r)
			}
			rig.requireEdges(t, tc.want)
		})
	}
}

// En el flanco: primero el calentamiento (el barato) y después el aviso al pipeline, los dos
// con la dirección del Edge; el kind va VACÍO; y la línea de log nombra tenant, Edge y sesión.
func TestTransitionToReadyWarmsFirstThenWakesThePipeline(t *testing.T) {
	t.Parallel()
	rig := newReadinessRig()
	rig.says(phone("tenant-1", "edge-1", "s-1"), saysReady)

	if got := strings.Join(rig.events, ","); got != "warmup,edge-ready" {
		t.Fatalf("orden de los hooks = %q, se esperaba warmup,edge-ready", got)
	}
	if want := (warmCall{"tenant-1", "edge-1", "s-1", ""}); rig.warms[0] != want {
		t.Errorf("OnWarmup recibió %+v, se esperaba %+v", rig.warms[0], want)
	}
	if want := (readyCall{"tenant-1", "edge-1"}); rig.readies[0] != want {
		t.Errorf("OnEdgeReady recibió %+v, se esperaba %+v", rig.readies[0], want)
	}
	for _, want := range []string{"level=INFO", readyLogLine, "tenant_id=tenant-1", "edge_id=edge-1", "session_id=s-1"} {
		if !rig.log.contains(want) {
			t.Errorf("al log del flanco le falta %q: %q", want, rig.log.String())
		}
	}
}

// El cero no se ANOTA: lo último dicho sigue siendo lo último dicho, y un Edge que nunca dijo
// nada no tiene entrada. Un Heartbeat nil se lee igual que uno sin el campo.
func TestSilenceIsNeverRecorded(t *testing.T) {
	t.Parallel()
	rig := newReadinessRig()
	cc := phone("tenant-1", "edge-1", "s-1")

	rig.says(cc, saysNothing)
	rig.srv.observeReadiness(cc, nil)
	if n := len(rig.srv.edgeReadiness); n != 0 {
		t.Fatalf("un Edge que no dice nada dejó %d entradas en edgeReadiness", n)
	}
	if got := rig.srv.readinessOf(cc); got != saysNothing {
		t.Fatalf("readinessOf de un Edge que no dijo nada = %v", got)
	}

	for _, said := range []cloudlinkv1.InferenceReadiness{saysDown, saysReady} {
		rig.says(cc, said)
		rig.says(cc, saysNothing)
		rig.srv.observeReadiness(cc, nil)
		if got := rig.srv.readinessOf(cc); got != said {
			t.Fatalf("tras decir %v y callar, readinessOf = %v: el silencio borró lo aprendido", said, got)
		}
	}
	rig.requireEdges(t, 1)
}

// Lo aprendido es por (tenant, Edge): otra sesión del mismo Edge diciendo READY no es un
// flanco; otro Edge del tenant y el mismo edge_id bajo otro tenant son Edges distintos.
func TestReadinessIsLearnedPerTenantAndEdge(t *testing.T) {
	t.Parallel()
	rig := newReadinessRig()

	rig.says(phone("tenant-1", "edge-1", "s-1"), saysReady)
	rig.says(phone("tenant-1", "edge-1", "s-2"), saysReady)
	rig.requireEdges(t, 1)

	rig.says(phone("tenant-1", "edge-2", "s-3"), saysReady)
	rig.requireEdges(t, 2)
	rig.says(phone("tenant-2", "edge-1", "s-4"), saysReady)
	rig.requireEdges(t, 3)

	rig.says(phone("tenant-1", "edge-2", "s-3"), saysDown)
	want := map[edgeKey]cloudlinkv1.InferenceReadiness{
		{tenantID: "tenant-1", edgeID: "edge-1"}: saysReady,
		{tenantID: "tenant-1", edgeID: "edge-2"}: saysDown,
		{tenantID: "tenant-2", edgeID: "edge-1"}: saysReady,
	}
	if len(rig.srv.edgeReadiness) != len(want) {
		t.Fatalf("edgeReadiness tiene %d entradas, se esperaban %d", len(rig.srv.edgeReadiness), len(want))
	}
	for k, w := range want {
		if got := rig.srv.edgeReadiness[k]; got != w {
			t.Errorf("edgeReadiness[%+v] = %v, se esperaba %v", k, got, w)
		}
	}
	if got := rig.srv.readinessOf(phone("tenant-1", "edge-2", "s-otra")); got != saysDown {
		t.Errorf("readinessOf pregunta por Edge, no por sesión: %v", got)
	}
}

// Sin hooks el gateway hace lo mismo: aprende, y deja la línea de log del flanco (una, no una
// por latido). Con uno solo cableado, ese se atiende y el otro no revienta.
func TestWithoutHooksTheTransitionIsStillLearnedAndLogged(t *testing.T) {
	t.Parallel()
	cc := phone("tenant-1", "edge-1", "s-1")

	t.Run("no hooks", func(t *testing.T) {
		t.Parallel()
		rig := newReadinessRig()
		rig.srv.OnWarmup, rig.srv.OnEdgeReady = nil, nil
		rig.says(cc, saysReady)
		rig.says(cc, saysReady)
		if got := strings.Count(rig.log.String(), readyLogLine); got != 1 {
			t.Errorf("líneas de log del flanco = %d, se esperaba 1", got)
		}
		if got := rig.srv.readinessOf(cc); got != saysReady {
			t.Errorf("sin hooks no se aprendió lo que el Edge dijo: %v", got)
		}
	})
	t.Run("only OnEdgeReady", func(t *testing.T) {
		t.Parallel()
		rig := newReadinessRig()
		rig.srv.OnWarmup = nil
		rig.says(cc, saysReady)
		if len(rig.readies) != 1 || len(rig.warms) != 0 {
			t.Errorf("avisos al pipeline = %d, calentamientos = %d", len(rig.readies), len(rig.warms))
		}
	})
	t.Run("only OnWarmup", func(t *testing.T) {
		t.Parallel()
		rig := newReadinessRig()
		rig.srv.OnEdgeReady = nil
		rig.says(cc, saysReady)
		if len(rig.warms) != 1 || len(rig.readies) != 0 {
			t.Errorf("calentamientos = %d, avisos al pipeline = %d", len(rig.warms), len(rig.readies))
		}
	})
}

// Sin identidad mTLS o sin session_id no se aprende nada: no hay (tenant, Edge) que indexar
// ni quien limpie la entrada después.
func TestObserveReadinessNeedsIdentityAndSession(t *testing.T) {
	t.Parallel()
	cases := map[string]connCtx{
		"without identity":   {sessionID: "s-1", tenantID: "tenant-1", edgeID: "edge-1"},
		"without session id": {tenantID: "tenant-1", edgeID: "edge-1", hasIdentity: true},
		"anonymous stream":   {sessionID: "s-1"},
	}
	for name, cc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rig := newReadinessRig()
			rig.says(cc, saysReady)
			rig.says(cc, saysDown)
			rig.requireEdges(t, 0)
			if n := len(rig.srv.edgeReadiness); n != 0 {
				t.Errorf("quedaron %d entradas en edgeReadiness", n)
			}
		})
	}
}

// Conducta del viejo, afirmada tal cual: observeReadiness NO excluye el canal de control (los
// latidos nunca llevan ese id; la guarda que sí pesa es la de warmOnRegister).
func TestObserveReadinessDoesNotSingleOutTheControlChannel(t *testing.T) {
	t.Parallel()
	rig := newReadinessRig()
	rig.says(phone("tenant-1", "edge-1", cltransport.ControlSessionID), saysReady)
	rig.requireEdges(t, 1)
}

// T-13: los hooks se llaman SIN el candado del seguimiento tomado. Un hook que pregunta al
// propio Server por ese Edge no se cuelga.
func TestReadinessHooksRunWithoutTheTrackingLock(t *testing.T) {
	t.Parallel()
	rig := newReadinessRig()
	cc := phone("tenant-1", "edge-1", "s-1")
	rig.srv.trackSession(phone("tenant-1", "edge-1", "s-1"))
	rig.srv.trackSession(phone("tenant-1", "edge-2", "s-9"))
	var seen []string
	rig.srv.OnWarmup = func(tenantID, edgeID, _, _ string) {
		seen = append(seen, "warmup:"+strings.Join(rig.srv.sessionsForEdge(tenantID, edgeID), ","))
	}
	rig.srv.OnEdgeReady = func(string, string) {
		seen = append(seen, "edge-ready:"+rig.srv.readinessOf(cc).String())
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		rig.says(cc, saysReady)
		rig.srv.warmOnRegister(phone("tenant-1", "edge-2", "s-9")) // el del registro tampoco lo retiene
	}()
	await(t, done, "que los hooks vuelvan: se llamaron con trackMu tomado")

	want := "warmup:s-1|edge-ready:INFERENCE_READINESS_READY|warmup:s-9"
	if got := strings.Join(seen, "|"); got != want {
		t.Fatalf("lo que vieron los hooks = %q, se esperaba %q", got, want)
	}
}

// Muchas sesiones del mismo Edge diciendo READY a la vez son UN flanco: leer lo anterior y
// anotar lo nuevo es una sola operación bajo el candado.
func TestSimultaneousReadyHeartbeatsOfOneEdgeAreOneTransition(t *testing.T) {
	t.Parallel()
	srv := New(session.NewRegistry(), quietLog())
	var warmups, wakeups atomic.Int32
	srv.OnWarmup = func(_, _, _, _ string) { warmups.Add(1) }
	srv.OnEdgeReady = func(_, _ string) { wakeups.Add(1) }

	const sessions = 64
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range sessions {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cc := phone("tenant-1", "edge-1", "s-"+strconv.Itoa(i))
			<-start
			srv.observeReadiness(cc, &cloudlinkv1.Heartbeat{InferenceReadiness: saysReady})
			if got := srv.readinessOf(cc); got != saysReady {
				t.Errorf("readinessOf tras decir READY = %v", got)
			}
		}()
	}
	close(start)
	wg.Wait()

	if w, k := warmups.Load(), wakeups.Load(); w != 1 || k != 1 {
		t.Fatalf("calentamientos = %d, avisos al pipeline = %d; se esperaba 1 de cada", w, k)
	}
}
