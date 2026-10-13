package grpc

// La correlación de las inferencias en vuelo (R-G13: «resultado huérfano no rompe», «la
// entrada pendiente no se fuga», «stream caído despierta en el acto»): deliverInference,
// clearInfer y cancelSessionInfers. Es el gemelo de la de acuses (send_ack_test.go), con la
// misma invariante de cierre: solo cierra el canal quien logra RETIRAR su entrada.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/session"
)

// seedInfer deja una inferencia en vuelo en la correlación, como hace Infer antes de empujar,
// y devuelve su canal.
func seedInfer(srv *Server, cmdID, sessionID string) chan *cloudlinkv1.InferenceResult {
	ch := make(chan *cloudlinkv1.InferenceResult, 1)
	srv.infersMu.Lock()
	srv.infers[cmdID] = pendingInfer{ch: ch, sessionID: sessionID}
	srv.infersMu.Unlock()
	return ch
}

// pendingInferIDs devuelve, ordenados, los command_id que siguen en la correlación.
func pendingInferIDs(srv *Server) []string {
	srv.infersMu.Lock()
	defer srv.infersMu.Unlock()
	ids := make([]string, 0, len(srv.infers))
	for id := range srv.infers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// requireNoPendingInfers afirma que no queda ni una inferencia pendiente.
func requireNoPendingInfers(t *testing.T, srv *Server) {
	t.Helper()
	if ids := pendingInferIDs(srv); len(ids) != 0 {
		t.Fatalf("la correlación de inferencias quedó con %d entradas (%v): un camino de salida no retira la suya", len(ids), ids)
	}
}

// inferState dice, sin bloquear, qué hay en el canal de una inferencia: un resultado, cerrado
// o nada.
func inferState(ch chan *cloudlinkv1.InferenceResult) (res *cloudlinkv1.InferenceResult, closed bool) {
	select {
	case r, ok := <-ch:
		return r, !ok
	default:
		return nil, false
	}
}

// seedInferGrid siembra perSession inferencias en vuelo para cada sesión ("<sesión>/<n>").
// Varias sesiones y varias inferencias: «solo las de X» con dos entradas contra un mapa sería
// probabilístico (hallazgo 24).
func seedInferGrid(srv *Server, sessions []string, perSession int) map[string]chan *cloudlinkv1.InferenceResult {
	chans := make(map[string]chan *cloudlinkv1.InferenceResult)
	for _, sid := range sessions {
		for i := range perSession {
			id := fmt.Sprintf("%s/%d", sid, i)
			chans[id] = seedInfer(srv, id, sid)
		}
	}
	return chans
}

// deliverInference entrega el resultado a la inferencia correlacionada por command_id —a esa y
// a ninguna otra— y retira su entrada.
func TestDeliverInferenceHandsTheResultToItsCommandOnly(t *testing.T) {
	t.Parallel()
	srv := New(session.NewRegistry(), quietLog())
	chans := seedInferGrid(srv, []string{"s-1", "s-2", "s-3"}, 3)
	res := &cloudlinkv1.InferenceResult{CommandId: "s-2/1"}

	srv.deliverInference(res)

	for id, ch := range chans {
		got, closed := inferState(ch)
		switch {
		case closed:
			t.Errorf("deliverInference cerró el canal de %s", id)
		case id == "s-2/1" && got != res:
			t.Errorf("la inferencia s-2/1 recibió %v, se esperaba su resultado", got)
		case id != "s-2/1" && got != nil:
			t.Errorf("la inferencia %s recibió un resultado que no era suyo", id)
		}
	}
	if ids := pendingInferIDs(srv); len(ids) != 8 || strings.Contains(strings.Join(ids, " "), "s-2/1") {
		t.Fatalf("pendientes tras el resultado = %v, se esperaban las otras 8", ids)
	}
}

// R-G13: un resultado huérfano —tardío, duplicado, de un llamante que ya se rindió, o nil— no
// rompe nada: se anota en debug y la correlación queda como estaba.
func TestDeliverInferenceOrphanIsLoggedAndIgnored(t *testing.T) {
	t.Parallel()
	log, logs := debugLog()
	srv := New(session.NewRegistry(), log)
	ch := seedInfer(srv, "cmd-1", "s-1")

	srv.deliverInference(&cloudlinkv1.InferenceResult{CommandId: "cmd-unknown"})
	srv.deliverInference(nil)

	requireLogHas(t, logs, `level=DEBUG msg="inferencia sin petición pendiente"`, "command_id=cmd-unknown")
	if got := strings.Count(logs.String(), "inferencia sin petición pendiente"); got != 2 {
		t.Errorf("líneas de huérfano = %d, se esperaban 2 (el desconocido y el nil)", got)
	}
	if got, closed := inferState(ch); got != nil || closed {
		t.Error("un resultado huérfano tocó el canal de otra inferencia")
	}

	// El duplicado: el primero se entrega, el segundo ya es huérfano.
	first := &cloudlinkv1.InferenceResult{CommandId: "cmd-1"}
	srv.deliverInference(first)
	srv.deliverInference(&cloudlinkv1.InferenceResult{CommandId: "cmd-1"})
	if got, _ := inferState(ch); got != first {
		t.Errorf("la inferencia recibió %v, se esperaba el PRIMER resultado", got)
	}
	if got, closed := inferState(ch); got != nil || closed {
		t.Error("el resultado duplicado llegó al canal o lo cerró")
	}
	requireNoPendingInfers(t, srv)
}

// deliverInference no bloquea NUNCA al bucle Recv: si nadie puede recibir por el canal, suelta
// el resultado y sigue (la entrada se retira igual).
func TestDeliverInferenceNeverBlocks(t *testing.T) {
	t.Parallel()
	srv := New(session.NewRegistry(), quietLog())
	unbuffered := make(chan *cloudlinkv1.InferenceResult) // sin buffer y sin lector
	srv.infersMu.Lock()
	srv.infers["cmd-1"] = pendingInfer{ch: unbuffered, sessionID: "s-1"}
	srv.infersMu.Unlock()

	done := make(chan struct{})
	go func() {
		srv.deliverInference(&cloudlinkv1.InferenceResult{CommandId: "cmd-1"})
		close(done)
	}()
	await(t, done, "deliverInference vuelve sin lector en el canal")
	requireNoPendingInfers(t, srv)
}

// deliverInference NO abre el sobre (ADR-0040 §Decisión.3): en el bucle Recv solo cabe lo que
// es O(1) en memoria. Un sellado que la nube no podría abrir —aquí no hay ni clave— se entrega
// tal cual y sin una línea de log: abrirlo es cosa del llamante, en su goroutine.
func TestDeliverInferenceDoesNotOpenTheEnvelope(t *testing.T) {
	t.Parallel()
	log, logs := debugLog()
	srv := New(session.NewRegistry(), log) // sin clave de cifrado
	ch := seedInfer(srv, "cmd-1", "s-1")
	res := sealedResult("cmd-1", []byte("no es un sobre"))

	srv.deliverInference(res)

	if got, closed := inferState(ch); got != res || closed {
		t.Fatalf("el resultado sellado no llegó intacto a su inferencia: (%v, cerrado=%v)", got, closed)
	}
	if logs.String() != "" {
		t.Errorf("entregar el resultado dejó log (¿se intentó abrir en el bucle Recv?): %q", logs.String())
	}
}

// clearInfer retira la entrada —solo la suya— y NO cierra el canal: quien sale por ahí es el
// propio llamante de Infer, y una segunda mano capaz de cerrar rompería la invariante.
func TestClearInferRemovesTheEntryWithoutClosingItsChannel(t *testing.T) {
	t.Parallel()
	srv := New(session.NewRegistry(), quietLog())
	chans := seedInferGrid(srv, []string{"s-1", "s-2"}, 3)

	srv.clearInfer("s-1/1")
	srv.clearInfer("s-1/1")     // repetido: inocuo
	srv.clearInfer("not-there") // desconocido: inocuo

	if ids := pendingInferIDs(srv); len(ids) != 5 || strings.Contains(strings.Join(ids, " "), "s-1/1") {
		t.Fatalf("pendientes tras clearInfer = %v, se esperaban las otras 5", ids)
	}
	if _, closed := inferState(chans["s-1/1"]); closed {
		t.Fatal("clearInfer cerró el canal")
	}
	// Un resultado tardío para la inferencia ya retirada es huérfano: no llega a su canal.
	srv.deliverInference(&cloudlinkv1.InferenceResult{CommandId: "s-1/1"})
	if got, closed := inferState(chans["s-1/1"]); got != nil || closed {
		t.Fatal("un resultado tardío llegó al canal de una inferencia ya retirada")
	}
}

// R-G13: la caída de un stream cancela DE GOLPE las inferencias en vuelo de ESA sesión —cierra
// sus canales y retira sus entradas— y deja intactas las de las demás. Dice cuántas canceló y
// deja UNA línea con la cifra. Los acuses en vuelo de esa misma sesión son OTRO mapa: no se tocan.
func TestCancelSessionInfersCancelsOnlyThatSession(t *testing.T) {
	t.Parallel()
	log, logs := capturedLog()
	srv := New(session.NewRegistry(), log)
	chans := seedInferGrid(srv, []string{"s-1", "s-2", "s-3", "s-4"}, 3)
	ack := seedAck(srv, "s-3/ack", "s-3")

	if n := srv.cancelSessionInfers("s-3"); n != 3 {
		t.Fatalf("cancelSessionInfers = %d, se esperaban 3", n)
	}

	for id, ch := range chans {
		got, closed := inferState(ch)
		mine := strings.HasPrefix(id, "s-3/")
		if mine && !closed {
			t.Errorf("la inferencia %s de la sesión caída sigue esperando", id)
		}
		if !mine && (closed || got != nil) {
			t.Errorf("la inferencia %s de otra sesión se canceló o recibió algo", id)
		}
	}
	if ids := pendingInferIDs(srv); len(ids) != 9 || strings.Contains(strings.Join(ids, " "), "s-3/") {
		t.Fatalf("pendientes tras la caída de s-3 = %v, se esperaban las 9 de las otras sesiones", ids)
	}
	if got, closed := ackState(ack); got != nil || closed || len(pendingAckIDs(srv)) != 1 {
		t.Error("cancelar las inferencias de la sesión tocó sus acuses en vuelo")
	}
	requireLogHas(t, logs, `level=WARN msg="gateway: el stream cayó con inferencias en vuelo"`, "session_id=s-3", "cancelados=3")
	if got := strings.Count(logs.String(), "el stream cayó con inferencias en vuelo"); got != 1 {
		t.Errorf("líneas de la caída = %d, se esperaba 1", got)
	}
}

// Sin nada en vuelo, la caída del stream es el caso normal: cero cancelados y ni una línea. Y
// repetirla es inocua (no hay doble cierre).
func TestCancelSessionInfersWithNothingInFlightIsSilent(t *testing.T) {
	t.Parallel()
	log, logs := capturedLog()
	srv := New(session.NewRegistry(), log)
	seedInfer(srv, "cmd-1", "s-1")

	if n := srv.cancelSessionInfers("s-other"); n != 0 {
		t.Fatalf("cancelSessionInfers de una sesión sin inferencias = %d", n)
	}
	if logs.String() != "" {
		t.Errorf("una caída sin inferencias en vuelo dejó log: %q", logs.String())
	}
	if n := srv.cancelSessionInfers("s-1"); n != 1 {
		t.Fatalf("cancelSessionInfers(s-1) = %d, se esperaba 1", n)
	}
	if n := srv.cancelSessionInfers("s-1"); n != 0 {
		t.Fatalf("la segunda caída de s-1 canceló %d: sus entradas ya estaban retiradas", n)
	}
	requireNoPendingInfers(t, srv)
}

// R-G13, el camino entero: la inferencia que está esperando DESPIERTA EN EL ACTO cuando su
// sesión se queda sin stream —con el presupuesto de producción puesto, 35 s— y sale con
// edge_offline; la de otra sesión sigue esperando.
func TestCancelSessionInfersWakesTheWaiterAtOnce(t *testing.T) {
	t.Parallel()
	rig := newInferRig(t)
	mine, other := seedInfer(rig.srv, "cmd-1", "s-1"), seedInfer(rig.srv, "cmd-2", "s-2")
	done := make(chan inferResult, 1)
	go func() {
		out, err := rig.srv.awaitInference(context.Background(), mine, "cmd-1", "s-1", 0)
		done <- inferResult{out, err}
	}()

	if n := rig.srv.cancelSessionInfers("s-1"); n != 1 {
		t.Fatalf("cancelSessionInfers = %d, se esperaba 1", n)
	}

	got := await(t, done, "que la inferencia en vuelo deje de esperar al caer su stream")
	requireReason(t, got.err, ReasonEdgeOffline)
	if !errors.Is(got.err, ErrStreamClosed) {
		t.Errorf("la inferencia volvió con %v, se esperaba la causa ErrStreamClosed", got.err)
	}
	if res, closed := inferState(other); res != nil || closed {
		t.Error("la caída de s-1 despertó a la inferencia de s-2")
	}
}

// La invariante de cierre bajo carrera: la entrega y la caída del stream compiten por CADA
// entrada, y solo una la retira. Ninguna inferencia se queda sin desenlace, ninguna tiene los
// dos, y nadie escribe en un canal cerrado ni lo cierra dos veces (lo dirían un panic o -race).
func TestDeliverAndCancelNeverShareAnEntry(t *testing.T) {
	t.Parallel()
	const n = 64
	srv := New(session.NewRegistry(), quietLog())
	chans := make([]chan *cloudlinkv1.InferenceResult, n)
	for i := range n {
		chans[i] = seedInfer(srv, fmt.Sprintf("cmd-%d", i), "s-1")
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			<-start
			srv.deliverInference(&cloudlinkv1.InferenceResult{CommandId: fmt.Sprintf("cmd-%d", i)})
		})
	}
	cancelled := make(chan int, 2)
	for range 2 {
		wg.Go(func() {
			<-start
			cancelled <- srv.cancelSessionInfers("s-1")
		})
	}
	close(start)
	wg.Wait()

	delivered, closedCount := 0, 0
	for i, ch := range chans {
		res, closed := inferState(ch)
		switch {
		case closed:
			closedCount++
		case res != nil:
			delivered++
			if _, closedAfter := inferState(ch); closedAfter {
				t.Errorf("la inferencia %d recibió su resultado Y vio cerrarse su canal", i)
			}
		default:
			t.Errorf("la inferencia %d quedó sin resultado y sin cierre", i)
		}
	}
	if total := <-cancelled + <-cancelled; total != closedCount || delivered+closedCount != n {
		t.Fatalf("entregadas=%d, cerradas=%d, canceladas según cancelSessionInfers=%d (de %d)", delivered, closedCount, total, n)
	}
	requireNoPendingInfers(t, srv)
}
