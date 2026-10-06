package grpc

// El emisor del aviso de sesión pasiva (greetIfNeeded; R-G23…R-G30). greeting.go no tiene
// exportados: nace en verde (T-17) y se prueba por dentro, llamando a greetIfNeeded como lo
// llama el job del latido. Aquí van el envío, la idempotencia, las guardas de entrada y lo que
// pasa alrededor de la marca; «sin Ack no hay marca» y «rechazo no marca» están en
// greeting_ack_test.go, el puerto en greeting_port_test.go, el literal en
// greeting_literal_test.go y el enganche al job del latido en greeting_route_test.go.
//
// El banco (greeting_helpers_test.go) comprueba al cerrar CADA test que el log no lleva ni el
// número ni el texto: «cero PII en los logs» vale para todos los caminos.

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/session"
)

// El aviso sale por SendText, a la sesión del stream, AL NÚMERO que devolvió la flota y con el
// literal EXACTO; la flota se consulta y se marca con el (tenant, edge, sesión) del stream.
func TestGreetingSendsTheNoticeToTheOwnNumberOfTheSession(t *testing.T) {
	t.Parallel()
	rig := newGreetingRig(t, ownNumber, nil)

	rig.beat()

	sent := rig.edge.sent()
	if len(sent) != 1 {
		t.Fatalf("el Edge recibió %d SendText, se esperaba 1", len(sent))
	}
	frame := sent[0]
	if got := frame.GetSendText().GetText(); got != passiveSessionNoticeV1 {
		t.Errorf("el texto enviado no es el literal del aviso:\n%q", got)
	}
	if got := frame.GetSendText().GetTo(); got != ownNumber {
		t.Errorf("destino = %q, se esperaba el número que devolvió la flota", got)
	}
	if frame.GetSessionId() != "s-1" || !uuidV4.MatchString(frame.GetCommandId()) {
		t.Errorf("frame = (session_id %q, command_id %q), se esperaba la sesión del stream y un UUIDv4",
			frame.GetSessionId(), frame.GetCommandId())
	}
	want := []greetCall{
		{"PendingGreeting", "tenant-1", "edge-1", "s-1"},
		{"MarkGreeted", "tenant-1", "edge-1", "s-1"},
	}
	if got := rig.fleet.recorded(); !reflect.DeepEqual(got, want) {
		t.Errorf("llamadas al puerto = %v, se esperaban %v", got, want)
	}
	if !rig.fleet.isGreeted() {
		t.Error("con un Ack ok=true la sesión no quedó marcada")
	}
	requireLog(t, rig.log, "INFO", "saludo: aviso de sesión pasiva entregado al número de la propia sesión",
		"session_id=s-1", "edge_id=edge-1", "literal=AVISO_SESION_PASIVA_V1", "command_id="+frame.GetCommandId())
}

// Un latido cada 30 s no es un saludo cada 30 s: tres latidos seguidos de la misma sesión son
// UN mensaje y UNA marca. Los latidos posteriores SÍ preguntan (no hay memoria en proceso) y
// se callan.
func TestGreetingIsIdempotentAcrossHeartbeats(t *testing.T) {
	t.Parallel()
	rig := newGreetingRig(t, ownNumber, nil)

	rig.beat()
	rig.beat()
	rig.beat()

	if n := len(rig.edge.sent()); n != 1 {
		t.Fatalf("tres latidos enviaron %d avisos, se esperaba 1", n)
	}
	if got := rig.fleet.count("MarkGreeted"); got != 1 {
		t.Errorf("MarkGreeted se llamó %d veces, se esperaba 1 (solo el latido que entregó)", got)
	}
	if got := rig.fleet.count("PendingGreeting"); got != 3 {
		t.Errorf("PendingGreeting se llamó %d veces, se esperaban 3: cada latido pregunta", got)
	}
}

// Las guardas de entrada. Sin número no hay a quién escribir: se pregunta y se calla. Sin
// identidad mTLS o sin session_id no se sabe de qué fila se habla: ni se pregunta.
func TestGreetingDoesNotFireWithoutSelfPnOrIdentity(t *testing.T) {
	t.Parallel()
	anonymous := phone("tenant-1", "edge-1", "s-1")
	anonymous.hasIdentity = false
	cases := []struct {
		name        string
		selfPn      string
		cc          connCtx
		wantQueries int
	}{
		{"without self_pn", "", phone("tenant-1", "edge-1", "s-1"), 1},
		{"without identity", ownNumber, anonymous, 0},
		{"without session id", ownNumber, phone("tenant-1", "edge-1", ""), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rig := newGreetingRig(t, tc.selfPn, nil)

			rig.srv.greetIfNeeded(context.Background(), tc.cc)

			if n := len(rig.edge.sent()); n != 0 {
				t.Errorf("se enviaron %d avisos, se esperaba 0", n)
			}
			if got := rig.fleet.count("PendingGreeting"); got != tc.wantQueries {
				t.Errorf("PendingGreeting se llamó %d veces, se esperaban %d", got, tc.wantQueries)
			}
			if got := rig.fleet.count("MarkGreeted"); got != 0 {
				t.Errorf("MarkGreeted se llamó %d veces, se esperaba 0", got)
			}
			requireSilent(t, rig.log)
		})
	}
}

// Sin flota (WithFleet no montado) el saludo es un no-op: ni envía ni rompe.
func TestGreetingWithoutFleetIsANoOp(t *testing.T) {
	t.Parallel()
	log, buf := debugLog()
	reg := session.NewRegistry()
	srv := New(reg, log)
	edge := &ackingEdge{srv: srv}
	t.Cleanup(reg.Register("s-1", edge))

	srv.greetIfNeeded(context.Background(), phone("tenant-1", "edge-1", "s-1"))

	if n := len(edge.sent()); n != 0 {
		t.Errorf("sin flota se enviaron %d avisos, se esperaba 0", n)
	}
	requireSilent(t, buf)
}

// Si la consulta falla no se envía ni se marca: un Warn con IDs opacos, y el siguiente latido
// vuelve a preguntar.
func TestGreetingQueryFailureOnlyWarns(t *testing.T) {
	t.Parallel()
	rig := newGreetingRig(t, ownNumber, nil)
	rig.fleet.pendingErr = errors.New("sobre ilegible")

	rig.beat()

	if n := len(rig.edge.sent()); n != 0 {
		t.Errorf("con la consulta fallida se enviaron %d avisos, se esperaba 0", n)
	}
	if got := rig.fleet.count("MarkGreeted"); got != 0 {
		t.Errorf("MarkGreeted se llamó %d veces, se esperaba 0", got)
	}
	requireLog(t, rig.log, "WARN", "saludo: no se pudo consultar si la sesión está pendiente de aviso",
		"session_id=s-1", "edge_id=edge-1", `error="sobre ilegible"`)
}

// El aviso salió y la marca no se pudo poner: es un Error —el dueño recibirá un duplicado— y,
// en efecto, el siguiente latido lo manda otra vez. Se fija tal cual: no hay nada aquí que se
// arregle solo, y callarlo sería peor.
func TestGreetingMarkFailureIsLoudAndTheNextHeartbeatSendsAgain(t *testing.T) {
	t.Parallel()
	rig := newGreetingRig(t, ownNumber, nil)
	rig.fleet.failMark(errors.New("base caída"))

	rig.beat()

	sent := rig.edge.sent()
	if len(sent) != 1 || rig.fleet.isGreeted() {
		t.Fatalf("primer latido: %d envíos, marcada=%v; se esperaba 1 envío sin marca", len(sent), rig.fleet.isGreeted())
	}
	requireLog(t, rig.log, "ERROR", "saludo: el aviso se entregó pero no se pudo marcar; el dueño recibirá un duplicado",
		"session_id=s-1", "edge_id=edge-1", "literal=AVISO_SESION_PASIVA_V1",
		"command_id="+sent[0].GetCommandId(), `error="base caída"`)

	rig.fleet.failMark(nil)
	rig.beat()

	if n := len(rig.edge.sent()); n != 2 || !rig.fleet.isGreeted() {
		t.Errorf("segundo latido: %d envíos acumulados, marcada=%v; se esperaban 2 (el duplicado) y la marca",
			n, rig.fleet.isGreeted())
	}
}

// Otro latido marcó entre la consulta y la marca (dos streams en una reconexión): el centinela
// devuelve marked=false y ESTE envío se reconoce como el duplicado, en Warn. No es un Info: no
// fue este camino el que avisó primero.
func TestGreetingLostRaceIsReportedAsADuplicate(t *testing.T) {
	t.Parallel()
	rig := newGreetingRig(t, ownNumber, nil)
	rig.edge.onText = rig.fleet.markedByAnother // con el aviso ya en vuelo

	rig.beat()

	sent := rig.edge.sent()
	if len(sent) != 1 {
		t.Fatalf("el Edge recibió %d SendText, se esperaba 1", len(sent))
	}
	if got := rig.fleet.count("MarkGreeted"); got != 1 {
		t.Errorf("MarkGreeted se llamó %d veces, se esperaba 1", got)
	}
	requireLog(t, rig.log, "WARN", "saludo: otro latido marcó el aviso primero; este envío fue un duplicado",
		"session_id=s-1", "edge_id=edge-1", "literal=AVISO_SESION_PASIVA_V1", "command_id="+sent[0].GetCommandId())
	if rig.log.contains("aviso de sesión pasiva entregado") {
		t.Errorf("el duplicado se anunció como entrega: %q", rig.log.String())
	}
}

// commandIDOf saca el command_id de un error que lo lleve —por duck-typing, esté envuelto o
// no— y devuelve vacío para el que no: un command_id inventado sería peor que ninguno.
func TestCommandIDOf(t *testing.T) {
	t.Parallel()
	sendFailure := sendErr("cmd-7", "s-1", session.ErrSessionOffline)
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"send error", sendFailure, "cmd-7"},
		{"wrapped send error", fmt.Errorf("envoltorio: %w", sendFailure), "cmd-7"},
		{"error without command", errors.New("generando command_id: sin entropía"), ""},
		{"nil", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := commandIDOf(tc.err); got != tc.want {
				t.Errorf("commandIDOf(%v) = %q, se esperaba %q", tc.err, got, tc.want)
			}
		})
	}
}
