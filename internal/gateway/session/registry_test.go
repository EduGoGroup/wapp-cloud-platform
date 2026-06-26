package session_test

import (
	"errors"
	"sync"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
)

// fakeSender captura los mensajes enviados, de forma segura para concurrencia.
type fakeSender struct {
	mu   sync.Mutex
	sent []*cloudlinkv1.CloudToEdge
	err  error
}

func (f *fakeSender) Send(msg *cloudlinkv1.CloudToEdge) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, msg)
	return nil
}

func (f *fakeSender) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

func newSendText(to, text string) *cloudlinkv1.CloudToEdge {
	return &cloudlinkv1.CloudToEdge{
		SessionId: "s1",
		Payload: &cloudlinkv1.CloudToEdge_SendText{
			SendText: &cloudlinkv1.SendText{To: to, Text: text},
		},
	}
}

func TestRegistryRegisterPushOnline(t *testing.T) {
	t.Parallel()
	reg := session.NewRegistry()

	if reg.Online("s1") {
		t.Fatal("sesión no debería estar online antes de registrar")
	}
	if reg.Count() != 0 {
		t.Fatalf("Count inicial = %d, quiero 0", reg.Count())
	}

	s := &fakeSender{}
	release := reg.Register("s1", s)

	if !reg.Online("s1") {
		t.Fatal("sesión debería estar online tras Register")
	}
	if reg.Count() != 1 {
		t.Fatalf("Count = %d, quiero 1", reg.Count())
	}

	if err := reg.Push("s1", newSendText("57300", "hola")); err != nil {
		t.Fatalf("Push devolvió error: %v", err)
	}
	if s.count() != 1 {
		t.Fatalf("el sender recibió %d mensajes, quiero 1", s.count())
	}

	release()

	if reg.Online("s1") {
		t.Fatal("sesión debería estar offline tras release")
	}
	if reg.Count() != 0 {
		t.Fatalf("Count tras release = %d, quiero 0", reg.Count())
	}
}

func TestRegistryPushOffline(t *testing.T) {
	t.Parallel()
	reg := session.NewRegistry()

	err := reg.Push("ausente", newSendText("57300", "hola"))
	if err == nil {
		t.Fatal("Push a sesión ausente debería fallar")
	}
	if !errors.Is(err, session.ErrSessionOffline) {
		t.Fatalf("error = %v, quiero envolver ErrSessionOffline", err)
	}
}

func TestRegistryDoubleRegisterLastWins(t *testing.T) {
	t.Parallel()
	reg := session.NewRegistry()

	s1 := &fakeSender{}
	release1 := reg.Register("s1", s1)

	s2 := &fakeSender{}
	release2 := reg.Register("s1", s2)

	if reg.Count() != 1 {
		t.Fatalf("Count tras doble register = %d, quiero 1", reg.Count())
	}

	// El Push debe ir al último sender registrado (última-gana).
	if err := reg.Push("s1", newSendText("57300", "hola")); err != nil {
		t.Fatalf("Push devolvió error: %v", err)
	}
	if s1.count() != 0 {
		t.Fatalf("el sender viejo recibió %d mensajes, quiero 0", s1.count())
	}
	if s2.count() != 1 {
		t.Fatalf("el sender nuevo recibió %d mensajes, quiero 1", s2.count())
	}

	// release1 es un no-op: la sesión ya no le pertenece.
	release1()
	if !reg.Online("s1") {
		t.Fatal("release de la sesión reemplazada no debe marcar offline")
	}

	// release2 sí libera la sesión vigente.
	release2()
	if reg.Online("s1") {
		t.Fatal("sesión debería estar offline tras release del sender vigente")
	}
}

func TestRegistryConcurrentSends(t *testing.T) {
	t.Parallel()
	reg := session.NewRegistry()
	s := &fakeSender{}
	reg.Register("s1", s)

	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			if err := reg.Push("s1", newSendText("57300", "hola")); err != nil {
				t.Errorf("Push concurrente devolvió error: %v", err)
			}
		}()
	}
	wg.Wait()

	if s.count() != n {
		t.Fatalf("el sender recibió %d mensajes, quiero %d", s.count(), n)
	}
}
