package session_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
)

// blockingSender bloquea en Send hasta que se cierra release: simula un Edge lento
// que no lee su stream (control de flujo gRPC), para ejercitar el deadline de Push.
type blockingSender struct {
	release <-chan struct{}
}

func (b *blockingSender) Send(*cloudlinkv1.CloudToEdge) error {
	<-b.release
	return nil
}

// TestRegistryPushTimeout comprueba que Push NO se cuelga con un Edge que no lee su
// stream: devuelve ErrPushTimeout dentro del sendTimeout (Plan 027 · Ola 1 · T5,
// cierra H6). Es la garantía de que el kill-switch (RevokeLease) no se atasca.
//
// CRITERIO 2 de T1.5-bis (Plan 050, ADR-0040 §Decisión.5 · Enmienda 1): con un ctx
// SIN DEADLINE el timer del Registry sigue siendo el techo, exactamente igual que
// antes de que Push recibiera ctx. Este test no cambia de semántica, solo de firma —
// y gana la aserción de abajo, que es un guardarraíl: si alguien "simplificara" el
// timer derivando un context.WithTimeout, el error pasaría a envolver
// context.DeadlineExceeded y ErrPushTimeout desaparecería sin que nada más chille.
func TestRegistryPushTimeout(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	defer close(release) // libera la goroutine de send al terminar el test.

	reg := session.NewRegistry(session.WithSendTimeout(20 * time.Millisecond))
	reg.Register("s1", &blockingSender{release: release})

	start := time.Now()
	err := reg.Push(context.Background(), "s1", newSendText("57300", "hola"))
	if !errors.Is(err, session.ErrPushTimeout) {
		t.Fatalf("Push a Edge bloqueado devolvió %v, quiero ErrPushTimeout", err)
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		t.Fatalf("el vencimiento del sendTimeout NO debe envolver un error de ctx: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Push tardó %v: no respetó el sendTimeout", elapsed)
	}
}

// TestRegistryPushCtxCanceladoNoEsPushTimeout es el CRITERIO 1 de T1.5-bis: un ctx
// que se cancela ANTES de que el Send conteste saca a Push AL INSTANTE con un error
// que envuelve context.Canceled y que NO es ErrPushTimeout. Los dos fallos son
// distintos —«el llamante se rindió» vs. «el Edge no lee su stream»— y la Ola 5 mide
// esa diferencia (ADR-0040 §Decisión.5 · Enmienda 1, regla 2).
//
// El sendTimeout se pone DELIBERADAMENTE alto (un minuto): si el brazo de ctx.Done()
// no existiera, este test no fallaría por aserción sino por tardar un minuto — que es
// justo lo que se quiere hacer visible.
//
// ⚠️ Lo que este test NO afirma, a propósito: que la goroutine interna del Send muera.
// Cancelar un ctx no desbloquea un stream.Send de gRPC; sobrevive hasta que el stream
// se desatasque o el Edge caiga (Enmienda 1, regla 1). Aquí la libera el close(release)
// diferido.
func TestRegistryPushCtxCanceladoNoEsPushTimeout(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	defer close(release)

	reg := session.NewRegistry(session.WithSendTimeout(time.Minute))
	reg.Register("s1", &blockingSender{release: release})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // el llamante se rinde antes de que el Send conteste.

	start := time.Now()
	err := reg.Push(ctx, "s1", newSendText("57300", "hola"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Push con ctx cancelado devolvió %v, quiero que envuelva context.Canceled", err)
	}
	if errors.Is(err, session.ErrPushTimeout) {
		t.Fatalf("Push con ctx cancelado NO debe envolver ErrPushTimeout: son fallos distintos (%v)", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Push tardó %v con el ctx ya cancelado: no salió por ctx.Done()", elapsed)
	}
}

// TestRegistryPushOfflineGanaAlCtxCancelado es el CRITERIO 3 de T1.5-bis: con la
// sesión inexistente gana ErrSessionOffline AUNQUE el ctx venga cancelado. La
// comprobación de sesión es previa y O(1), y un ctx muerto no debe convertir un «esa
// sesión no existe» en un «se acabó el tiempo» (Enmienda 1, regla 3). Sin este test,
// mover el select por encima del lookup pasaría inadvertido y el kill-switch
// empezaría a reportar timeouts donde hay sesiones que ya no están.
func TestRegistryPushOfflineGanaAlCtxCancelado(t *testing.T) {
	t.Parallel()
	reg := session.NewRegistry()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := reg.Push(ctx, "ausente", newSendText("57300", "hola"))
	if !errors.Is(err, session.ErrSessionOffline) {
		t.Fatalf("error = %v, quiero que envuelva ErrSessionOffline pese al ctx cancelado", err)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("una sesión ausente NO debe reportarse como cancelación del llamante: %v", err)
	}
}

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

	if err := reg.Push(context.Background(), "s1", newSendText("57300", "hola")); err != nil {
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

	err := reg.Push(context.Background(), "ausente", newSendText("57300", "hola"))
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
	if err := reg.Push(context.Background(), "s1", newSendText("57300", "hola")); err != nil {
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

	// context.Background() EXPLÍCITO: aquí se mide la seguridad de concurrencia del
	// Registry, no la propagación del ctx. Un ctx que vence a mitad haría fallar Push
	// por una razón ajena a lo que este test afirma.
	ctx := context.Background()

	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			if err := reg.Push(ctx, "s1", newSendText("57300", "hola")); err != nil {
				t.Errorf("Push concurrente devolvió error: %v", err)
			}
		}()
	}
	wg.Wait()

	if s.count() != n {
		t.Fatalf("el sender recibió %d mensajes, quiero %d", s.count(), n)
	}
}
