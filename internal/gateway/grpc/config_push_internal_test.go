package gatewaygrpc

import (
	"bytes"
	"context"
	"sync"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
)

// captureSender guarda los CloudToEdge que recibe (para inspeccionar el ConfigUpdate).
type captureSender struct {
	mu   sync.Mutex
	msgs []*cloudlinkv1.CloudToEdge
}

func (c *captureSender) Send(m *cloudlinkv1.CloudToEdge) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgs = append(c.msgs, m)
	return nil
}

func (c *captureSender) configUpdates() []*cloudlinkv1.ConfigUpdate {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []*cloudlinkv1.ConfigUpdate
	for _, m := range c.msgs {
		if cu := m.GetConfigUpdate(); cu != nil {
			out = append(out, cu)
		}
	}
	return out
}

func newTestServer() (*Server, *session.Registry) {
	reg := session.NewRegistry()
	return New(reg, logger.New(logger.WithWriter(&bytes.Buffer{}))), reg
}

// TestPushConfig_FanOutPorTenant verifica que PushConfig empuja el ConfigUpdate a
// TODAS las sesiones vivas del tenant y a NINGUNA de otro tenant.
func TestPushConfig_FanOutPorTenant(t *testing.T) {
	s, reg := newTestServer()

	a1, a2, b1 := &captureSender{}, &captureSender{}, &captureSender{}
	reg.Register("sess-a1", a1)
	reg.Register("sess-a2", a2)
	reg.Register("sess-b1", b1)
	// Dos Edges del tenant t1 (una sesión cada uno) + un Edge del tenant t2.
	s.trackSession(connCtx{tenantID: "t1", edgeID: "e1", sessionID: "sess-a1"})
	s.trackSession(connCtx{tenantID: "t1", edgeID: "e2", sessionID: "sess-a2"})
	s.trackSession(connCtx{tenantID: "t2", edgeID: "e3", sessionID: "sess-b1"})

	payload := []byte(`{"version":"v1"}`)
	if err := s.PushConfig(context.Background(), "t1", "intents", "ver-1", payload); err != nil {
		t.Fatalf("PushConfig: %v", err)
	}

	for name, snd := range map[string]*captureSender{"sess-a1": a1, "sess-a2": a2} {
		cus := snd.configUpdates()
		if len(cus) != 1 {
			t.Fatalf("%s recibió %d ConfigUpdate, quiero 1", name, len(cus))
		}
		cu := cus[0]
		if cu.GetKind() != "intents" || cu.GetVersion() != "ver-1" || !bytes.Equal(cu.GetPayload(), payload) {
			t.Fatalf("%s ConfigUpdate inesperado: %+v", name, cu)
		}
		if cu.GetCommandId() == "" || cu.GetSessionId() != name {
			t.Fatalf("%s: command_id/session_id mal poblados: %+v", name, cu)
		}
	}
	if got := len(b1.configUpdates()); got != 0 {
		t.Fatalf("la sesión de otro tenant recibió %d ConfigUpdate, quiero 0", got)
	}
}

// fakeProvider devuelve configs fijas para el push al conectar.
type fakeProvider struct {
	cfgs []ConfigPayload
	err  error
}

func (f fakeProvider) ConfigsForConnect(_ context.Context, _ string) ([]ConfigPayload, error) {
	return f.cfgs, f.err
}

// TestPushConfigsOnConnect empuja las configs del provider a la sesión recién
// conectada, y NO empuja nada sin identidad mTLS.
func TestPushConfigsOnConnect(t *testing.T) {
	s, reg := newTestServer()
	s.configProvider = fakeProvider{cfgs: []ConfigPayload{{Kind: "intents", Version: "v9", Payload: []byte(`{}`)}}}

	snd := &captureSender{}
	reg.Register("sess-1", snd)

	// Con identidad: se empuja.
	s.pushConfigsOnConnect(context.Background(), connCtx{tenantID: "t1", edgeID: "e1", sessionID: "sess-1", hasIdentity: true})
	if got := len(snd.configUpdates()); got != 1 {
		t.Fatalf("con identidad: %d ConfigUpdate, quiero 1", got)
	}

	// Sin identidad mTLS: no se conoce el tenant ⇒ no se empuja.
	snd2 := &captureSender{}
	reg.Register("sess-2", snd2)
	s.pushConfigsOnConnect(context.Background(), connCtx{sessionID: "sess-2", hasIdentity: false})
	if got := len(snd2.configUpdates()); got != 0 {
		t.Fatalf("sin identidad: %d ConfigUpdate, quiero 0", got)
	}
}
