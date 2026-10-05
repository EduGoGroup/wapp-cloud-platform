package grpc

// La correlación de acuses por dentro (R-G11, la mitad de los acuses; R-G22). Nace con el
// verde: deliverAck, clearAck, cancelSessionAcks y handleReceipt no son exportados, y hasta
// que llegue connect.go nadie más los llama.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/session"
)

// seedAck deja un envío en vuelo en la correlación, como hace SendText antes de empujar, y
// devuelve su canal.
func seedAck(srv *Server, cmdID, sessionID string) chan *cloudlinkv1.Ack {
	ch := make(chan *cloudlinkv1.Ack, 1)
	srv.acksMu.Lock()
	srv.acks[cmdID] = pendingAck{ch: ch, sessionID: sessionID}
	srv.acksMu.Unlock()
	return ch
}

// pendingAckIDs devuelve, ordenados, los command_id que siguen en la correlación.
func pendingAckIDs(srv *Server) []string {
	srv.acksMu.Lock()
	defer srv.acksMu.Unlock()
	ids := make([]string, 0, len(srv.acks))
	for id := range srv.acks {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// requireNoPendingAcks afirma que no queda ni una entrada pendiente.
func requireNoPendingAcks(t *testing.T, srv *Server) {
	t.Helper()
	if ids := pendingAckIDs(srv); len(ids) != 0 {
		t.Fatalf("la correlación de acuses quedó con %d entradas (%v): un camino de salida no retira la suya", len(ids), ids)
	}
}

// ackState dice, sin bloquear, qué hay en el canal de un envío: un Ack, cerrado o nada.
func ackState(ch chan *cloudlinkv1.Ack) (ack *cloudlinkv1.Ack, closed bool) {
	select {
	case a, ok := <-ch:
		return a, !ok
	default:
		return nil, false
	}
}

// debugLog captura también el nivel debug (el ack huérfano se registra ahí).
func debugLog() (logger.Logger, *logBuffer) {
	buf := &logBuffer{}
	return logger.New(logger.WithWriter(buf), logger.WithLevel(slog.LevelDebug)), buf
}

// seedGrid siembra perSession envíos en vuelo para cada sesión y devuelve sus canales por
// command_id ("<sesión>/<n>"). Varias sesiones y varios envíos: un caso «solo los de X» con
// dos entradas contra un mapa sería probabilístico (hallazgo 24).
func seedGrid(srv *Server, sessions []string, perSession int) map[string]chan *cloudlinkv1.Ack {
	chans := make(map[string]chan *cloudlinkv1.Ack)
	for _, sid := range sessions {
		for i := range perSession {
			id := fmt.Sprintf("%s/%d", sid, i)
			chans[id] = seedAck(srv, id, sid)
		}
	}
	return chans
}

// deliverAck entrega el Ack al envío correlacionado por acked_command_id —a ese y a ningún
// otro— y retira su entrada.
func TestDeliverAckHandsTheAckToItsCommandOnly(t *testing.T) {
	t.Parallel()
	srv := New(session.NewRegistry(), quietLog())
	chans := seedGrid(srv, []string{"s-1", "s-2", "s-3"}, 3)
	ack := &cloudlinkv1.Ack{AckedCommandId: "s-2/1", Ok: true}

	srv.deliverAck(ack)

	for id, ch := range chans {
		got, closed := ackState(ch)
		switch {
		case closed:
			t.Errorf("deliverAck cerró el canal de %s", id)
		case id == "s-2/1" && got != ack:
			t.Errorf("el envío s-2/1 recibió %v, se esperaba su Ack", got)
		case id != "s-2/1" && got != nil:
			t.Errorf("el envío %s recibió un Ack que no era suyo", id)
		}
	}
	if ids := pendingAckIDs(srv); len(ids) != 8 || strings.Contains(strings.Join(ids, " "), "s-2/1") {
		t.Fatalf("pendientes tras el Ack = %v, se esperaban los otros 8", ids)
	}
}

// Un Ack sin envío pendiente (huérfano, duplicado o tardío) no rompe nada: se registra en
// debug y la correlación queda como estaba.
func TestDeliverAckOrphanIsLoggedAndIgnored(t *testing.T) {
	t.Parallel()
	log, logs := debugLog()
	srv := New(session.NewRegistry(), log)
	ch := seedAck(srv, "cmd-1", "s-1")

	srv.deliverAck(&cloudlinkv1.Ack{AckedCommandId: "cmd-unknown", Ok: true})

	if !logs.contains("ack sin comando pendiente") || !logs.contains("acked_command_id=cmd-unknown") {
		t.Errorf("el ack huérfano no dejó su línea de debug: %q", logs.String())
	}
	if got, closed := ackState(ch); got != nil || closed {
		t.Error("un ack huérfano tocó el canal de otro envío")
	}

	// El duplicado: el primero se entrega, el segundo ya es huérfano.
	first := &cloudlinkv1.Ack{AckedCommandId: "cmd-1", Ok: true}
	srv.deliverAck(first)
	srv.deliverAck(&cloudlinkv1.Ack{AckedCommandId: "cmd-1", Ok: false})
	if got, _ := ackState(ch); got != first {
		t.Errorf("el envío recibió %v, se esperaba el PRIMER Ack", got)
	}
	if got, closed := ackState(ch); got != nil || closed {
		t.Error("el Ack duplicado llegó al canal o lo cerró")
	}
	requireNoPendingAcks(t, srv)
}

// deliverAck no bloquea NUNCA al bucle Recv: si nadie puede recibir por el canal, suelta el
// Ack y sigue (la entrada se retira igual).
func TestDeliverAckNeverBlocks(t *testing.T) {
	t.Parallel()
	srv := New(session.NewRegistry(), quietLog())
	unbuffered := make(chan *cloudlinkv1.Ack) // sin buffer y sin lector
	srv.acksMu.Lock()
	srv.acks["cmd-1"] = pendingAck{ch: unbuffered, sessionID: "s-1"}
	srv.acksMu.Unlock()

	done := make(chan struct{})
	go func() {
		srv.deliverAck(&cloudlinkv1.Ack{AckedCommandId: "cmd-1", Ok: true})
		close(done)
	}()
	await(t, done, "deliverAck vuelve sin lector en el canal")
	requireNoPendingAcks(t, srv)
}

// clearAck retira la entrada —solo la suya— y NO cierra el canal: quien sale por ahí es el
// propio llamante del envío, y una segunda mano capaz de cerrar rompería la invariante.
func TestClearAckRemovesTheEntryWithoutClosingItsChannel(t *testing.T) {
	t.Parallel()
	srv := New(session.NewRegistry(), quietLog())
	chans := seedGrid(srv, []string{"s-1", "s-2"}, 3)

	srv.clearAck("s-1/1")
	srv.clearAck("s-1/1")     // repetido: inocuo
	srv.clearAck("not-there") // desconocido: inocuo

	if ids := pendingAckIDs(srv); len(ids) != 5 || strings.Contains(strings.Join(ids, " "), "s-1/1") {
		t.Fatalf("pendientes tras clearAck = %v, se esperaban los otros 5", ids)
	}
	if _, closed := ackState(chans["s-1/1"]); closed {
		t.Fatal("clearAck cerró el canal")
	}
	// Un Ack tardío para el envío ya retirado es huérfano: no llega a su canal.
	srv.deliverAck(&cloudlinkv1.Ack{AckedCommandId: "s-1/1", Ok: true})
	if got, closed := ackState(chans["s-1/1"]); got != nil || closed {
		t.Fatal("un Ack tardío llegó al canal de un envío ya retirado")
	}
}

// R-G11: la caída de un stream cancela DE GOLPE los envíos en vuelo de ESA sesión —cierra
// sus canales y retira sus entradas— y deja intactos los de las demás. Dice cuántos canceló y
// deja UNA línea con la cifra.
func TestCancelSessionAcksCancelsOnlyThatSession(t *testing.T) {
	t.Parallel()
	log, logs := capturedLog()
	srv := New(session.NewRegistry(), log)
	chans := seedGrid(srv, []string{"s-1", "s-2", "s-3", "s-4"}, 3)

	if n := srv.cancelSessionAcks("s-3"); n != 3 {
		t.Fatalf("cancelSessionAcks = %d, se esperaban 3", n)
	}

	for id, ch := range chans {
		got, closed := ackState(ch)
		mine := strings.HasPrefix(id, "s-3/")
		if mine && !closed {
			t.Errorf("el envío %s de la sesión caída sigue esperando", id)
		}
		if !mine && (closed || got != nil) {
			t.Errorf("la caída de s-3 tocó el envío %s de otra sesión", id)
		}
	}
	if ids := pendingAckIDs(srv); len(ids) != 9 || strings.Contains(strings.Join(ids, " "), "s-3/") {
		t.Fatalf("pendientes tras la cancelación = %v, se esperaban los 9 de las otras sesiones", ids)
	}
	out := logs.String()
	if !strings.Contains(out, "gateway: el stream cayó con envíos esperando ack") ||
		!strings.Contains(out, "session_id=s-3") || !strings.Contains(out, "cancelados=3") {
		t.Errorf("la cancelación no dejó su línea con la cifra: %q", out)
	}
	if lines := strings.Count(out, "\n"); lines != 1 {
		t.Errorf("la cancelación escribió %d líneas, se esperaba 1: %s", lines, out)
	}
}

// Sin nada en vuelo para esa sesión no cancela nada y NO escribe: el cierre limpio de un
// stream es el caso normal y no es noticia.
func TestCancelSessionAcksWithNothingInFlightIsSilent(t *testing.T) {
	t.Parallel()
	log, logs := capturedLog()
	srv := New(session.NewRegistry(), log)
	seedGrid(srv, []string{"s-1", "s-2", "s-3"}, 2)

	srv.cancelSessionAcks("s-2")
	before := logs.String()

	if n := srv.cancelSessionAcks("s-2"); n != 0 {
		t.Errorf("segunda cancelación de la misma sesión = %d, se esperaba 0", n)
	}
	if n := srv.cancelSessionAcks("s-unknown"); n != 0 {
		t.Errorf("cancelación de una sesión sin envíos = %d, se esperaba 0", n)
	}
	if after := logs.String(); after != before {
		t.Errorf("una cancelación sin nada en vuelo escribió en el log: %q", strings.TrimPrefix(after, before))
	}
	if ids := pendingAckIDs(srv); len(ids) != 4 {
		t.Errorf("pendientes = %v, se esperaban los 4 de las otras sesiones", ids)
	}
}

// R-G11 · la invariante de cierre: solo cierra el canal quien logra RETIRAR su entrada. Un
// Ack tardío y el cierre del stream, a la vez sobre el mismo envío, no pueden escribir en un
// canal cerrado ni cerrarlo dos veces (los dos son pánicos), y el envío acaba en UNO de los
// dos desenlaces, nunca en los dos ni en ninguno. Corre bajo -race.
func TestLateAckAndStreamCloseNeverShareAChannel(t *testing.T) {
	t.Parallel()
	srv := New(session.NewRegistry(), quietLog())

	var delivered, cancelled int
	for i := range 400 {
		cmdID := fmt.Sprintf("cmd-%d", i)
		ch := seedAck(srv, cmdID, "s-1")
		ack := &cloudlinkv1.Ack{AckedCommandId: cmdID, Ok: true}

		var wg sync.WaitGroup
		wg.Add(3)
		go func() { defer wg.Done(); srv.deliverAck(ack) }()
		go func() { defer wg.Done(); srv.cancelSessionAcks("s-1") }()
		go func() { defer wg.Done(); srv.cancelSessionAcks("s-1") }() // dos cierres a la vez
		wg.Wait()

		got, closed := ackState(ch)
		switch {
		case got == ack:
			delivered++
			if _, alsoClosed := ackState(ch); alsoClosed {
				t.Fatalf("ronda %d: el envío recibió su Ack Y además le cerraron el canal", i)
			}
		case closed:
			cancelled++
		default:
			t.Fatalf("ronda %d: el envío ni recibió su Ack ni fue cancelado", i)
		}
	}
	requireNoPendingAcks(t, srv)
	t.Logf("desenlaces: %d entregados, %d cancelados", delivered, cancelled)
}

type receiptCtxKey struct{}

// R-G22: el acuse se registra correlado (sesión del stream, command_id, estado, ids) y va al
// sink, tal cual y con el ctx del job.
func TestHandleReceiptLogsAndForwardsToTheSink(t *testing.T) {
	t.Parallel()
	log, logs := capturedLog()
	sink := &recordingSink{}
	srv := New(session.NewRegistry(), log, WithReceiptSink(sink))
	ctx := context.WithValue(context.Background(), receiptCtxKey{}, "job")
	receipt := &cloudlinkv1.MessageReceipt{
		SessionId:  "s-from-frame",
		CommandId:  "cmd-7",
		MessageIds: []string{"wamid-1", "wamid-2"},
		Status:     cloudlinkv1.ReceiptStatus_RECEIPT_STATUS_DELIVERED,
		Timestamp:  1700000001,
	}

	srv.handleReceipt(ctx, connCtx{sessionID: "s-stream"}, receipt)

	if len(sink.got) != 1 || sink.got[0] != receipt {
		t.Fatalf("el sink recibió %v, se esperaba el mismo receipt una vez", sink.got)
	}
	if sink.ctxs[0].Value(receiptCtxKey{}) != "job" {
		t.Error("el sink no recibió el ctx del job")
	}
	for _, want := range []string{
		"acuse recibido del Edge", "session_id=s-stream", "command_id=cmd-7",
		"status=RECEIPT_STATUS_DELIVERED", "wamid-1", "wamid-2", "timestamp=1700000001",
	} {
		if !logs.contains(want) {
			t.Errorf("la línea del acuse no contiene %q: %s", want, logs.String())
		}
	}
}

// Un receipt nil se ignora entero; un sink que falla deja su línea de error y no revienta el
// carril.
func TestHandleReceiptNilAndFailingSink(t *testing.T) {
	t.Parallel()
	log, logs := capturedLog()
	sink := &recordingSink{err: errors.New("tabla caída")}
	srv := New(session.NewRegistry(), log, WithReceiptSink(sink))

	srv.handleReceipt(context.Background(), connCtx{sessionID: "s-1"}, nil)
	if len(sink.got) != 0 || logs.String() != "" {
		t.Fatalf("un receipt nil llegó al sink o al log: %v / %q", sink.got, logs.String())
	}

	srv.handleReceipt(context.Background(), connCtx{sessionID: "s-1"}, &cloudlinkv1.MessageReceipt{CommandId: "cmd-9"})
	if len(sink.got) != 1 {
		t.Fatalf("el sink recibió %d receipts, se esperaba 1", len(sink.got))
	}
	for _, want := range []string{
		"acuse: el sink no pudo registrar el receipt", "level=ERROR", "session_id=s-1", "command_id=cmd-9", "tabla caída",
	} {
		if !logs.contains(want) {
			t.Errorf("el fallo del sink no dejó %q en el log: %s", want, logs.String())
		}
	}
}
