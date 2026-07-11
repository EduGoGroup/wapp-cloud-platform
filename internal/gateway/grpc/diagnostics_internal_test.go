package gatewaygrpc

import (
	"context"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/diagnostics"
)

// diagnosticsRequests filtra los DiagnosticsRequest empujados (helper de este test,
// sobre el captureSender de config_push_internal_test.go).
func (c *captureSender) diagnosticsRequests() []*cloudlinkv1.DiagnosticsRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []*cloudlinkv1.DiagnosticsRequest
	for _, m := range c.msgs {
		if dr := m.GetDiagnosticsRequest(); dr != nil {
			out = append(out, dr)
		}
	}
	return out
}

// fakeReceiver captura la invocación de SaveBundle del demux.
type fakeReceiver struct {
	called            bool
	tenant, sess, cmd string
	bundle            diagnostics.Bundle
	found             bool
	err               error
}

func (f *fakeReceiver) SaveBundle(_ context.Context, tenantID, sessionID, commandID string, b diagnostics.Bundle) (bool, error) {
	f.called = true
	f.tenant, f.sess, f.cmd, f.bundle = tenantID, sessionID, commandID, b
	return f.found, f.err
}

func TestRequestDiagnostics_EmpujaFrame(t *testing.T) {
	s, reg := newTestServer()
	snd := &captureSender{}
	reg.Register("sess-a", snd)

	if err := s.RequestDiagnostics(context.Background(), "sess-a", "cmd-1", "full"); err != nil {
		t.Fatalf("RequestDiagnostics: %v", err)
	}
	drs := snd.diagnosticsRequests()
	if len(drs) != 1 {
		t.Fatalf("empujó %d DiagnosticsRequest, quiero 1", len(drs))
	}
	dr := drs[0]
	if dr.GetCommandId() != "cmd-1" || dr.GetSessionId() != "sess-a" || dr.GetScope() != "full" {
		t.Fatalf("DiagnosticsRequest mal poblado: %+v", dr)
	}
}

func TestStoreDiagnosticsBundle_Correlaciona(t *testing.T) {
	s, _ := newTestServer()
	rcv := &fakeReceiver{found: true}
	s.diag = rcv

	cc := connCtx{tenantID: "t1", edgeID: "e1", sessionID: "sess-a", hasIdentity: true}
	bundle := &cloudlinkv1.DiagnosticsBundle{
		CommandId: "cmd-1", LogTail: "log", GoroutineDump: "dump", SubsystemsJson: `{"x":1}`,
	}
	s.storeDiagnosticsBundle(context.Background(), cc, bundle)

	if !rcv.called || rcv.tenant != "t1" || rcv.sess != "sess-a" || rcv.cmd != "cmd-1" {
		t.Fatalf("SaveBundle mal invocado: %+v", rcv)
	}
	if rcv.bundle.LogTail != "log" || rcv.bundle.GoroutineDump != "dump" || rcv.bundle.SubsystemsJSON != `{"x":1}` {
		t.Fatalf("bundle mal mapeado: %+v", rcv.bundle)
	}
}

func TestStoreDiagnosticsBundle_HuerfanoNoRompe(t *testing.T) {
	s, _ := newTestServer()
	rcv := &fakeReceiver{found: false} // sin solicitud pendiente que case
	s.diag = rcv

	cc := connCtx{tenantID: "t1", edgeID: "e1", sessionID: "sess-a", hasIdentity: true}
	// No debe entrar en pánico ni propagar: un huérfano se ignora con log.
	s.storeDiagnosticsBundle(context.Background(), cc, &cloudlinkv1.DiagnosticsBundle{CommandId: "cmd-x"})
	if !rcv.called {
		t.Fatal("SaveBundle debió invocarse (y devolver found=false)")
	}
}

func TestStoreDiagnosticsBundle_SinIdentidad_NoOp(t *testing.T) {
	s, _ := newTestServer()
	rcv := &fakeReceiver{}
	s.diag = rcv

	// Sin identidad mTLS no se conoce el tenant ⇒ no se toca el store.
	cc := connCtx{sessionID: "sess-a", hasIdentity: false}
	s.storeDiagnosticsBundle(context.Background(), cc, &cloudlinkv1.DiagnosticsBundle{CommandId: "cmd-1"})
	if rcv.called {
		t.Fatal("sin identidad no debió invocar SaveBundle")
	}
}
