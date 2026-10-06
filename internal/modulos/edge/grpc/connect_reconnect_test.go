package grpc

// El cierre del stream (closeStream) y la reconexión rápida (R-G4, R-G5; DEUDA-050.1): al
// colgar, cada sesión sale del Registry, sus envíos en vuelo dejan de esperar SI se quedó sin
// stream, y su MarkOffline va al carril como último job; el carril se sella y se drena antes
// de que Connect vuelva. Si la sesión YA volvió por otro stream, el cierre del viejo no la
// toca: ni la saca del Registry, ni le cancela los acuses, ni la marca offline.

import (
	"context"
	"errors"
	"io"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/fleet"
)

const drainAbandonedLine = "carril: drenaje abandonado por presupuesto"

// requireLive afirma que ninguna llamada apuntada llegó con un ctx ya terminado.
func (tl *timeline) requireLive(t *testing.T) {
	t.Helper()
	tl.mu.Lock()
	defer tl.mu.Unlock()
	if len(tl.dead) != 0 {
		t.Errorf("llamadas con un ctx YA terminado: %v", tl.dead)
	}
}

// sinkFunc adapta una función al puerto ReceiptSink.
type sinkFunc func(context.Context, *cloudlinkv1.MessageReceipt) error

func (f sinkFunc) Record(ctx context.Context, r *cloudlinkv1.MessageReceipt) error { return f(ctx, r) }

// Lo que el stream deja escrito de principio a fin con un latido por medio, en orden: online y
// lease al registrar; el job del latido; y el offline, lo ÚLTIMO (D-050.2). Cuando Connect
// vuelve ya está todo: el cierre sella y drena el carril, no lo abandona.
func TestConnectClosesByDrainingTheLaneAndMarkingOfflineLast(t *testing.T) {
	t.Parallel()
	rig := newRouteRig(t)
	edge := openStream(t, rig.srv, forgedIdentity("tenant-1", "edge-1"))
	cc := phone("tenant-1", "edge-1", "s-1")

	edge.send(t, heartbeatFrame("s-1", fullHeartbeat()), receiptFrame("s-1", "cmd-1"), receiptFrame("s-1", "cmd-2"))
	edge.hangUp(t)

	want := "MarkOnline edges=0 > Upsert edges=0 > " + heartbeatTimeline + " > MarkOffline edges=1"
	if got := rig.tl.String(); got != want {
		t.Fatalf("el stream escribió %q, se esperaba %q", got, want)
	}
	rig.tl.requireBounded(t)
	if row := rig.row(t, cc); row.State != fleet.StateOffline || row.SelfPn != "573001112233" {
		t.Errorf("fila = (%q, self_pn %q), se esperaba offline con lo que persistió el latido", row.State, row.SelfPn)
	}
	if len(rig.sink.got) != 2 {
		t.Errorf("al volver Connect el sink tiene %d acuses, se esperaban los 2", len(rig.sink.got))
	}
	if rig.log.contains(drainAbandonedLine) || rig.log.contains("carril: el trabajo no se encoló") {
		t.Errorf("un cierre limpio abandonó o perdió trabajo: %q", rig.log.String())
	}
}

// El carril es lo que deja al bucle Recv seguir leyendo mientras el trabajo pesado corre: con
// un acuse parado DENTRO del sink, los frames siguientes se aceptan igual (se encolan, con el
// tope y el presupuesto del Server). Y el cierre ESPERA a lo que hay en vuelo: Connect no
// vuelve aunque el Edge ya colgó; vuelve cuando el trabajo termina, con todo hecho.
func TestConnectKeepsReadingWhileTheLaneWorksAndWaitsForItOnClose(t *testing.T) {
	t.Parallel()
	inSink, letGo := make(chan struct{}), make(chan struct{})
	var recorded atomic.Int32
	var budgets [4]time.Duration
	rig := newRouteRig(t, WithWorkTimeout(time.Hour), WithReceiptSink(sinkFunc(func(ctx context.Context, _ *cloudlinkv1.MessageReceipt) error {
		if dl, ok := ctx.Deadline(); ok {
			budgets[recorded.Load()] = time.Until(dl)
		}
		if recorded.Add(1) == 1 {
			close(inSink)
			<-letGo
		}
		return nil
	})))
	edge := openStream(t, rig.srv, nil)
	edge.send(t, receiptFrame("s-1", "cmd-1"))
	await(t, inSink, "que el acuse entre en el sink")
	edge.send(t, receiptFrame("s-1", "cmd-2"), receiptFrame("s-1", "cmd-3"), receiptFrame("s-1", "cmd-4"))

	edge.hung.Store(true)
	edge.end <- io.EOF
	letRun()
	select {
	case err := <-edge.done:
		t.Fatalf("Connect volvió (%v) con un trabajo del carril todavía en vuelo", err)
	default:
	}
	close(letGo)
	if err := await(t, edge.done, "que Connect vuelva al terminar el trabajo"); err != nil {
		t.Errorf("Connect devolvió %v", err)
	}
	if got := recorded.Load(); got != 4 {
		t.Errorf("al volver Connect se habían registrado %d acuses, se esperaban los 4", got)
	}
	for i, left := range budgets {
		if left < 30*time.Minute {
			t.Errorf("el acuse %d corrió con %v de presupuesto, se esperaba el del Server (1 h)", i+1, left)
		}
	}
}

// D-050.5: el trabajo del carril NO muere con el stream. Con el contexto del stream ya
// cancelado —el Edge se fue a media conexión— el latido pendiente y el MarkOffline se
// persisten con un ctx VIVO, y Connect devuelve el error del stream.
func TestConnectPersistsTheGoodbyeAfterTheStreamContextIsGone(t *testing.T) {
	t.Parallel()
	rig := newRouteRig(t)
	edge := openStream(t, rig.srv, forgedIdentity("tenant-1", "edge-1"))
	edge.send(t, pongFrame("s-1", 1), heartbeatFrame("s-1", fullHeartbeat()))
	edge.cancel()
	if err := edge.closeWith(t, context.Canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Connect devolvió %v, se esperaba el error del stream", err)
	}

	rig.tl.requireLive(t)
	rig.tl.requireBounded(t)
	if row := rig.row(t, phone("tenant-1", "edge-1", "s-1")); row.State != fleet.StateOffline {
		t.Errorf("fila en %q: la despedida no se persistió tras morir el stream", row.State)
	}
}

// El MarkOffline de CADA sesión se encola ANTES de sellar el carril. Sellar primero despierta a
// los workers ociosos para que mueran, y el offline de una sesión cuyo worker ya murió rebota:
// la flota mostraría online un Edge que se fue. Con muchas sesiones, cada una con su worker
// ocioso, ninguna se queda sin su offline y ningún trabajo rebota.
func TestConnectMarksEverySessionOfflineBeforeSealingTheLane(t *testing.T) {
	t.Parallel()
	// Los workers de sesiones distintas corren a la vez: el sink tiene que aguantarlo.
	discard := sinkFunc(func(context.Context, *cloudlinkv1.MessageReceipt) error { return nil })
	rig := newRouteRig(t, WithReceiptSink(discard))
	edge := openStream(t, rig.srv, forgedIdentity("tenant-1", "edge-1"))
	sessions := make([]string, 0, 48)
	for i := range cap(sessions) {
		sid := "s-" + strconv.Itoa(i)
		sessions = append(sessions, sid)
		edge.send(t, receiptFrame(sid, "cmd-"+sid)) // registra la sesión y deja vivo su worker
	}

	edge.hangUp(t)

	for _, sid := range sessions {
		if got := rig.row(t, phone("tenant-1", "edge-1", sid)).State; got != fleet.StateOffline {
			t.Errorf("%s quedó %q en flota: su MarkOffline se perdió en el cierre", sid, got)
		}
	}
	if rig.log.contains("carril: el trabajo no se encoló") {
		t.Errorf("el cierre perdió trabajo: %q", rig.log.String())
	}
}

// El handshake cuelga del contexto del STREAM, también el del canal de control: si el Edge se
// fue a media conexión, registrar la sesión o empujarle config a nadie se rinde con él y queda
// anotado como stream caído.
func TestConnectHandshakesUnderTheStreamContext(t *testing.T) {
	t.Parallel()
	rig := newRouteRig(t, WithConfigProvider(&stubProvider{cfgs: connectCfgs}))
	edge := openStream(t, rig.srv, forgedIdentity("tenant-1", "edge-1"))
	edge.settle(t)
	edge.cancel()

	edge.send(t, pongFrame("s-1", 1), loginFrame(control, "cmd-1"))

	requireLogHas(t, rig.log, handshakeGoneLine, controlGoneLine)
	if rig.log.contains(handshakeBudgetLine) || rig.log.contains(controlBudgetLine) {
		t.Errorf("un stream caído se anotó como presupuesto vencido: %q", rig.log.String())
	}
}

// Al colgar el ÚNICO stream de una sesión, sus envíos en vuelo dejan de esperar YA —no se
// comen el plazo del Ack— con el error de stream caído; los de otras sesiones siguen esperando.
func TestConnectCancelsTheSendsInFlightOfASessionLeftWithoutStream(t *testing.T) {
	t.Parallel()
	rig := newRouteRig(t)
	edge := openStream(t, rig.srv, forgedIdentity("tenant-1", "edge-1"))
	edge.send(t, pongFrame("s-1", 1))
	elsewhere := seedAck(rig.srv, "cmd-elsewhere", "s-elsewhere")
	pushed := make(chan struct{}, 1)
	edge.onSend = func() { pushed <- struct{}{} }

	sent := goSend(context.Background(), rig.srv, sendKinds[0].send, "s-1")
	await(t, pushed, "que el SendText salga por el stream")
	edge.hangUp(t)

	res := await(t, sent, "que el envío en vuelo deje de esperar al colgar el Edge")
	if !errors.Is(res.err, ErrStreamClosed) || !asSendError(t, res.err).StreamCaido() {
		t.Errorf("el envío volvió con %v, se esperaba el error de stream caído", res.err)
	}
	if ack, closed := ackState(elsewhere); ack != nil || closed {
		t.Error("colgar un stream canceló el envío en vuelo de una sesión que no era suya")
	}
}

// «YA» es antes de drenar el carril: con un trabajo de la sesión parado dentro del carril —el
// cierre lo espera, y puede esperar el presupuesto entero—, el envío en vuelo despierta igual,
// con Connect todavía sin volver. Cancelar después del drenaje sería hacerle pagar al llamante
// HTTP la cola de una sesión que ya no tiene Edge.
func TestConnectCancelsTheSendsInFlightBeforeDrainingTheLane(t *testing.T) {
	t.Parallel()
	inSink, letGo := make(chan struct{}), make(chan struct{})
	var entered sync.Once
	rig := newRouteRig(t, WithWorkTimeout(time.Hour), WithReceiptSink(sinkFunc(func(context.Context, *cloudlinkv1.MessageReceipt) error {
		entered.Do(func() { close(inSink) })
		<-letGo
		return nil
	})))
	edge := openStream(t, rig.srv, nil)
	release := sync.OnceFunc(func() { close(letGo) })
	t.Cleanup(release)
	edge.send(t, receiptFrame("s-1", "cmd-1"))
	await(t, inSink, "que el acuse entre en el sink y tape el carril")
	waiting := seedAck(rig.srv, "cmd-send", "s-1")

	edge.hung.Store(true)
	edge.end <- io.EOF

	if ack := await(t, waiting, "que el envío en vuelo despierte con el carril todavía tapado"); ack != nil {
		t.Fatalf("el envío recibió un Ack (%v), se esperaba la cancelación por stream caído", ack)
	}
	select {
	case err := <-edge.done:
		t.Fatalf("Connect volvió (%v) con el carril tapado: el test no probó el orden", err)
	default:
	}
	release()
	if err := await(t, edge.done, "que Connect vuelva al destaparse el carril"); err != nil {
		t.Errorf("Connect devolvió %v", err)
	}
}

// R-G4, la reconexión rápida de extremo a extremo: el Edge vuelve por un stream NUEVO antes de
// que el viejo termine de morir. El cierre del viejo no pisa a la sesión viva: sigue en el
// Registry —y lo que se le empuja sale por el stream nuevo—, sus envíos en vuelo siguen
// esperando su Ack, y la flota NO la marca offline.
func TestConnectReconnectionSurvivesTheOldStreamClosing(t *testing.T) {
	t.Parallel()
	rig := newRouteRig(t)
	cc := phone("tenant-1", "edge-1", "s-1")
	stale := openStream(t, rig.srv, forgedIdentity("tenant-1", "edge-1"))
	stale.send(t, pongFrame("s-1", 1))
	fresh := openStream(t, rig.srv, forgedIdentity("tenant-1", "edge-1"))
	fresh.send(t, pongFrame("s-1", 1))
	inFlight := seedAck(rig.srv, "cmd-1", "s-1")
	staleFrames := len(stale.received())

	stale.hangUp(t)

	if !rig.reg.Online("s-1") {
		t.Fatal("el cierre del stream viejo sacó del Registry a la sesión que ya había vuelto")
	}
	if ack, closed := ackState(inFlight); ack != nil || closed {
		t.Error("el cierre del stream viejo canceló un envío en vuelo de una sesión viva")
	}
	if row := rig.row(t, cc); row.State != fleet.StateOnline {
		t.Errorf("la sesión reconectada quedó %q en flota", row.State)
	}
	if got, want := rig.tl.String(), "MarkOnline edges=0 > Upsert edges=0 > MarkOnline edges=0 > Upsert edges=0"; got != want {
		t.Errorf("se escribió %q, se esperaba %q (ningún MarkOffline)", got, want)
	}
	requireLogHas(t, rig.log, reconnectedLine)
	if err := rig.srv.Ping(t.Context(), "s-1", 7); err != nil {
		t.Fatalf("Ping a la sesión reconectada: %v", err)
	}
	if frames := fresh.received(); frames[len(frames)-1].GetPing() == nil || len(stale.received()) != staleFrames {
		t.Error("el empuje a la sesión reconectada no salió por el stream nuevo")
	}

	// 🟡 Conducta del viejo, copiada tal cual: el seguimiento por Edge NO distingue streams. El
	// cierre del viejo deja de rastrear la sesión aunque siga viva por el nuevo, y nada vuelve a
	// rastrearla (el nuevo ya la registró): hasta su siguiente reconexión, el fan-out de config y
	// el kill-switch por Edge no la alcanzan. No se corrige aquí (equivalencia); queda afirmado.
	if got := sortedSessions(rig.srv, "tenant-1", "edge-1"); got != "" {
		t.Errorf("seguimiento tras la reconexión = %q; el viejo la deja sin rastrear", got)
	}

	// Y el gemelo: cuando cuelga el stream que SÍ la tiene, la sesión queda sin nadie.
	fresh.hangUp(t)
	if _, closed := ackState(inFlight); !closed || rig.reg.Online("s-1") || rig.row(t, cc).State != fleet.StateOffline {
		t.Error("al colgar el stream nuevo la sesión no quedó cancelada, fuera del Registry y offline")
	}
}
