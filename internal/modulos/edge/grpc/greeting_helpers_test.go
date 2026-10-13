package grpc

// Los dobles del aviso de sesión pasiva: la flota que sabe saludar, el Edge que acusa y el
// banco que los monta. Van aparte de greeting_test.go para que los compartan sus trozos
// (greeting_ack_test.go, greeting_port_test.go, greeting_route_test.go).

import (
	"context"
	"strings"
	"sync"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/fleet"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/fleet/fleethelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/session"
)

// ownNumber es el número propio de la sesión de los tests: PII, así que ningún log puede
// llevarlo.
const ownNumber = "573001112233"

// greetCall es una llamada al puerto del saludo, con el (tenant, edge, sesión) que recibió.
type greetCall struct {
	method, tenantID, edgeID, sessionID string
}

// greeterFleet es un fleet.Repository que ADEMÁS sabe saludar: el doble LOCAL del
// sessionGreeter (el gemelo en memoria de fleet no lo cumple, y el puerto es de quien lo
// consume). Sus dos métodos siguen el contrato del adaptador Postgres, centinela incluido:
// MarkGreeted devuelve marked=false si la marca ya estaba puesta.
type greeterFleet struct {
	fleet.Repository
	// tl, si está, apunta las dos llamadas en la línea de tiempo del banco de route.
	tl *timeline
	// lookup, si está, dice el número leyendo de OTRO sitio (la fila que escribe
	// persistSelfPn) en vez del campo selfPn.
	lookup func(ctx context.Context, tenantID, edgeID, sessionID string) string

	mu         sync.Mutex
	selfPn     string
	greeted    bool
	pendingErr error
	markErr    error
	calls      []greetCall
}

func newGreeterFleet(selfPn string) *greeterFleet {
	return &greeterFleet{Repository: fleethelpertest.NewMemoria(), selfPn: selfPn}
}

func (f *greeterFleet) PendingGreeting(ctx context.Context, tenantID, edgeID, sessionID string) (string, bool, error) {
	if f.tl != nil {
		f.tl.at(ctx, "PendingGreeting")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, greetCall{"PendingGreeting", tenantID, edgeID, sessionID})
	if f.pendingErr != nil {
		return "", false, f.pendingErr
	}
	selfPn := f.selfPn
	if f.lookup != nil {
		selfPn = f.lookup(ctx, tenantID, edgeID, sessionID)
	}
	if f.greeted || selfPn == "" {
		return "", false, nil
	}
	return selfPn, true, nil
}

func (f *greeterFleet) MarkGreeted(ctx context.Context, tenantID, edgeID, sessionID string) (bool, error) {
	if f.tl != nil {
		f.tl.at(ctx, "MarkGreeted")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, greetCall{"MarkGreeted", tenantID, edgeID, sessionID})
	if f.markErr != nil {
		return false, f.markErr
	}
	if f.greeted { // el centinela: WHERE greeted_at IS NULL
		return false, nil
	}
	f.greeted = true
	return true, nil
}

// markedByAnother pone la marca por fuera, como el latido de OTRO stream que gana la carrera.
func (f *greeterFleet) markedByAnother() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.greeted = true
}

// failMark hace que MarkGreeted falle con err (nil lo deja funcionar otra vez).
func (f *greeterFleet) failMark(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.markErr = err
}

func (f *greeterFleet) isGreeted() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.greeted
}

// count dice cuántas veces se llamó al método.
func (f *greeterFleet) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.method == method {
			n++
		}
	}
	return n
}

func (f *greeterFleet) recorded() []greetCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]greetCall(nil), f.calls...)
}

// edgeReply es lo que el Edge hace con un SendText.
type edgeReply int

const (
	acksOK      edgeReply = iota // lo envió: Ack{ok=true}
	rejects                      // lo acusó sin enviarlo: Ack{ok=false, "lease no vigente"}
	staysSilent                  // lo recibió y no acusa nunca
	dropsStream                  // el stream cae con el envío en vuelo
)

// ackingEdge es el Edge del otro lado: apunta los frames que recibe y responde a cada
// SendText según el guion (el n-ésimo envío usa script[n-1]; pasado el guion, acusa ok). El
// Ack se entrega por deliverAck, como hace el bucle Recv; no bloquea (canal con buffer), así
// que va en línea y el test no depende de ninguna goroutine.
type ackingEdge struct {
	srv    *Server
	script []edgeReply
	// onText, si está, corre al recibir cada SendText, ANTES de responder.
	onText func()

	mu    sync.Mutex
	kinds []string // "text", "lease" u "other", en orden de llegada
	texts []*cloudlinkv1.CloudToEdge
}

var _ session.Sender = (*ackingEdge)(nil)

func (e *ackingEdge) Send(msg *cloudlinkv1.CloudToEdge) error {
	if msg.GetSendText() == nil {
		kind := "other"
		if msg.GetLeaseUpdate() != nil {
			kind = "lease"
		}
		e.mu.Lock()
		e.kinds = append(e.kinds, kind)
		e.mu.Unlock()
		return nil
	}
	e.mu.Lock()
	e.kinds = append(e.kinds, "text")
	e.texts = append(e.texts, msg)
	n := len(e.texts)
	e.mu.Unlock()
	if e.onText != nil {
		e.onText()
	}

	reply := acksOK
	if n <= len(e.script) {
		reply = e.script[n-1]
	}
	switch reply {
	case acksOK:
		e.srv.deliverAck(&cloudlinkv1.Ack{AckedCommandId: msg.GetCommandId(), Ok: true})
	case rejects:
		e.srv.deliverAck(&cloudlinkv1.Ack{AckedCommandId: msg.GetCommandId(), Ok: false, Error: "lease no vigente"})
	case dropsStream:
		e.srv.cancelSessionAcks(msg.GetSessionId())
	case staysSilent:
	}
	return nil
}

// sent devuelve los frames SendText recibidos, en orden.
func (e *ackingEdge) sent() []*cloudlinkv1.CloudToEdge {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]*cloudlinkv1.CloudToEdge(nil), e.texts...)
}

// order devuelve los tipos de frame recibidos, en orden, unidos por " > ".
func (e *ackingEdge) order() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return strings.Join(e.kinds, " > ")
}

// greetingRig es un Server con una flota que sabe saludar y UNA sesión viva ("s-1", del
// Edge edge-1 de tenant-1) cuyo Edge responde según el guion.
type greetingRig struct {
	srv   *Server
	fleet *greeterFleet
	edge  *ackingEdge
	log   *logBuffer
	cc    connCtx
}

func newGreetingRig(t *testing.T, selfPn string, script []edgeReply, opts ...Option) *greetingRig {
	t.Helper()
	log, buf := debugLog()
	r := &greetingRig{fleet: newGreeterFleet(selfPn), log: buf, cc: phone("tenant-1", "edge-1", "s-1")}
	reg := session.NewRegistry()
	r.srv = New(reg, log, append([]Option{WithFleet(r.fleet)}, opts...)...)
	r.edge = &ackingEdge{srv: r.srv, script: script}
	t.Cleanup(reg.Register("s-1", r.edge))
	t.Cleanup(func() { requireNoPII(t, buf) })
	return r
}

// beat es un latido visto desde el saludo: lo que el job del latido llama al final.
func (r *greetingRig) beat() {
	r.srv.greetIfNeeded(context.Background(), r.cc)
}

// requireNoPII afirma que el log no lleva ni el número propio ni una sola línea del aviso.
func requireNoPII(t *testing.T, log *logBuffer) {
	t.Helper()
	out := log.String()
	if strings.Contains(out, ownNumber) {
		t.Errorf("el log lleva el número propio de la sesión (PII): %q", out)
	}
	for line := range strings.SplitSeq(passiveSessionNoticeV1, "\n") {
		if line != "" && strings.Contains(out, line) {
			t.Errorf("el log lleva texto del aviso (%q): %q", line, out)
		}
	}
}

// requireLog afirma que el log tiene UNA línea con ese nivel y ese mensaje, y que esa línea
// lleva cada uno de los fragmentos pedidos. Devuelve la línea.
func requireLog(t *testing.T, log *logBuffer, level, msg string, fragments ...string) string {
	t.Helper()
	var found []string
	for line := range strings.SplitSeq(log.String(), "\n") {
		if strings.Contains(line, `msg="`+msg+`"`) {
			found = append(found, line)
		}
	}
	if len(found) != 1 {
		t.Fatalf("el log tiene %d líneas con el mensaje %q, se esperaba 1: %q", len(found), msg, log.String())
	}
	for _, want := range append([]string{"level=" + level}, fragments...) {
		if !strings.Contains(found[0], want) {
			t.Errorf("a la línea del log le falta %q: %q", want, found[0])
		}
	}
	return found[0]
}

// requireSilent afirma que el saludo no escribió NADA en el log.
func requireSilent(t *testing.T, log *logBuffer) {
	t.Helper()
	if out := log.String(); strings.Contains(out, "saludo:") {
		t.Errorf("el saludo dejó rastro en el log y debía callar: %q", out)
	}
}
