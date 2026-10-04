package session

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// watchdog es el plazo tras el cual un test da por COLGADA una llamada que debía volver en el
// acto. No sincroniza nada: solo convierte un cuelgue en un fallo legible.
const watchdog = 5 * time.Second

// recordingSender captura los mensajes enviados, de forma segura para concurrencia (que es lo
// que el contrato de Sender exige a quien lo implemente).
type recordingSender struct {
	mu   sync.Mutex
	sent []*cloudlinkv1.CloudToEdge
	err  error
}

func (f *recordingSender) Send(msg *cloudlinkv1.CloudToEdge) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, msg)
	return nil
}

func (f *recordingSender) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

// blockingSender simula un Edge que no lee su stream: Send avisa por entered y se queda
// bloqueado hasta que se cierra release.
type blockingSender struct {
	entered chan struct{}
	release chan struct{}
}

// newBlockingSender devuelve el doble y deja programada su liberación al terminar el test,
// para que la goroutine del Send (que sobrevive a Push, a propósito) no quede colgada.
func newBlockingSender(t *testing.T) *blockingSender {
	t.Helper()
	b := &blockingSender{entered: make(chan struct{}, 64), release: make(chan struct{})}
	t.Cleanup(func() { close(b.release) })
	return b
}

func (b *blockingSender) Send(*cloudlinkv1.CloudToEdge) error {
	b.entered <- struct{}{}
	<-b.release
	return nil
}

func newSendText(text string) *cloudlinkv1.CloudToEdge {
	return &cloudlinkv1.CloudToEdge{
		SessionId: "s1",
		Payload: &cloudlinkv1.CloudToEdge_SendText{
			SendText: &cloudlinkv1.SendText{To: "57300", Text: text},
		},
	}
}

// expiredContext devuelve un ctx cuyo plazo ya venció.
func expiredContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
	t.Cleanup(cancel)
	return ctx
}

// cancelledContext devuelve un ctx ya cancelado.
func cancelledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// D-F3-2 · trampa T-3: el centinela ES el de platform (identidad), no un gemelo.
func TestErrSessionOfflineIsThePlatformSentinel(t *testing.T) {
	t.Parallel()
	if ErrSessionOffline != httpapi.ErrSessionOffline { //nolint:errorlint // se afirma IDENTIDAD, no envoltura
		t.Fatal("ErrSessionOffline no es la misma variable que httpapi.ErrSessionOffline")
	}
	if !errors.Is(ErrSessionOffline, httpapi.ErrSessionOffline) {
		t.Fatal("errors.Is(session.ErrSessionOffline, httpapi.ErrSessionOffline) = false")
	}
}

// Los textos de los centinelas son observables y se copian literales (diseno.md §5).
func TestSentinelTexts(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"offline", ErrSessionOffline, "sesión offline"},
		{"push timeout", ErrPushTimeout, "timeout empujando comando al Edge"},
		{"push abandoned", ErrPushAbandoned, "el llamante se rindió empujando el comando al Edge"},
	}
	for _, c := range cases {
		if got := c.err.Error(); got != c.want {
			t.Errorf("%s: texto = %q, quiero %q", c.name, got, c.want)
		}
	}
	if errors.Is(ErrPushTimeout, ErrPushAbandoned) || errors.Is(ErrPushAbandoned, ErrPushTimeout) ||
		errors.Is(ErrPushTimeout, ErrSessionOffline) || errors.Is(ErrPushAbandoned, ErrSessionOffline) {
		t.Error("los tres centinelas tienen que ser distintos entre sí")
	}
}

// NewRegistry + WithSendTimeout + SendTimeout: el plazo por defecto es 10 s y un valor <=0 cae
// a él.
func TestNewRegistrySendTimeout(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		opts []RegistryOption
		want time.Duration
	}{
		{"no options uses the default", nil, 10 * time.Second},
		{"positive value is kept", []RegistryOption{WithSendTimeout(25 * time.Millisecond)}, 25 * time.Millisecond},
		{"zero falls back to the default", []RegistryOption{WithSendTimeout(0)}, 10 * time.Second},
		{"negative falls back to the default", []RegistryOption{WithSendTimeout(-time.Second)}, 10 * time.Second},
		{"last option wins", []RegistryOption{WithSendTimeout(time.Second), WithSendTimeout(3 * time.Second)}, 3 * time.Second},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := NewRegistry(c.opts...).SendTimeout(); got != c.want {
				t.Fatalf("SendTimeout() = %v, quiero %v", got, c.want)
			}
		})
	}
}

// NewRegistry nace vacío; Register pone la sesión online; release la quita.
func TestRegisterOnlineCountRelease(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()

	if reg.Online("s1") {
		t.Fatal("la sesión no debería estar online antes de registrarla")
	}
	if got := reg.Count(); got != 0 {
		t.Fatalf("Count inicial = %d, quiero 0", got)
	}

	release := reg.Register("s1", &recordingSender{})
	if !reg.Online("s1") {
		t.Fatal("la sesión debería estar online tras Register")
	}
	if reg.Online("s2") {
		t.Fatal("registrar s1 no debe poner online a s2")
	}
	if got := reg.Count(); got != 1 {
		t.Fatalf("Count = %d, quiero 1", got)
	}

	releaseOther := reg.Register("s2", &recordingSender{})
	if got := reg.Count(); got != 2 {
		t.Fatalf("Count con dos sesiones = %d, quiero 2", got)
	}

	release()
	if reg.Online("s1") {
		t.Fatal("la sesión debería estar offline tras release")
	}
	if !reg.Online("s2") {
		t.Fatal("el release de s1 no debe tocar a s2")
	}
	if got := reg.Count(); got != 1 {
		t.Fatalf("Count tras release = %d, quiero 1", got)
	}

	// Idempotente: un segundo release no hace nada.
	release()
	if got := reg.Count(); got != 1 {
		t.Fatalf("Count tras un segundo release = %d, quiero 1", got)
	}
	releaseOther()
	if got := reg.Count(); got != 0 {
		t.Fatalf("Count final = %d, quiero 0", got)
	}
}

// Push entrega el MISMO mensaje al Sender de la sesión, y solo a ese.
func TestPushDeliversToTheSessionSender(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()
	s1, s2 := &recordingSender{}, &recordingSender{}
	reg.Register("s1", s1)
	reg.Register("s2", s2)

	msg := newSendText("hola")
	if err := reg.Push(context.Background(), "s1", msg); err != nil {
		t.Fatalf("Push devolvió error: %v", err)
	}
	if s1.count() != 1 || s1.sent[0] != msg {
		t.Fatalf("el sender de s1 recibió %d mensajes (o no el mismo puntero), quiero el mensaje tal cual", s1.count())
	}
	if s2.count() != 0 {
		t.Fatalf("el sender de s2 recibió %d mensajes, quiero 0", s2.count())
	}
}

// Si el Send contesta a tiempo, Push devuelve SU error tal cual, sin envolver.
func TestPushReturnsTheSenderErrorUnwrapped(t *testing.T) {
	t.Parallel()
	sendErr := errors.New("stream roto")
	reg := NewRegistry()
	reg.Register("s1", &recordingSender{err: sendErr})

	err := reg.Push(context.Background(), "s1", newSendText("hola"))
	if err != sendErr { //nolint:errorlint // se afirma que llega SIN envolver
		t.Fatalf("Push devolvió %v, quiero el error del Sender tal cual", err)
	}
}

// R-S1: Push a sesión inexistente → ErrSessionOffline, con el session_id entre comillas.
func TestPushOfflineSession(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()
	reg.Register("otra", &recordingSender{})

	err := reg.Push(context.Background(), "ausente", newSendText("hola"))
	if !errors.Is(err, ErrSessionOffline) {
		t.Fatalf("error = %v, quiero que envuelva ErrSessionOffline", err)
	}
	if !errors.Is(err, httpapi.ErrSessionOffline) {
		t.Fatalf("error = %v, quiero que envuelva también el centinela de platform (D-F3-2)", err)
	}
	if want := `sesión offline: "ausente"`; err.Error() != want {
		t.Fatalf("texto = %q, quiero %q", err.Error(), want)
	}
}

// R-S1 (Enmienda 1, regla 3): la sesión inexistente GANA a un ctx ya cancelado.
func TestPushOfflineWinsOverACancelledContext(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()

	err := reg.Push(cancelledContext(), "ausente", newSendText("hola"))
	if !errors.Is(err, ErrSessionOffline) {
		t.Fatalf("error = %v, quiero que envuelva ErrSessionOffline pese al ctx cancelado", err)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, ErrPushAbandoned) {
		t.Fatalf("una sesión ausente NO debe reportarse como abandono del llamante: %v", err)
	}
}

// Tras el release la sesión vuelve a estar offline para Push, y el Sender no se toca.
func TestPushAfterReleaseIsOffline(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()
	s := &recordingSender{}
	reg.Register("s1", s)()

	if err := reg.Push(context.Background(), "s1", newSendText("hola")); !errors.Is(err, ErrSessionOffline) {
		t.Fatalf("error = %v, quiero ErrSessionOffline tras el release", err)
	}
	if s.count() != 0 {
		t.Fatalf("el sender liberado recibió %d mensajes, quiero 0", s.count())
	}
}

// R-S4: doble registro, última-gana; el release del reemplazado no borra al nuevo (compara
// identidad de la entrada, no el session_id).
func TestDoubleRegisterLastWins(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()

	old := &recordingSender{}
	releaseOld := reg.Register("s1", old)
	current := &recordingSender{}
	releaseCurrent := reg.Register("s1", current)

	if got := reg.Count(); got != 1 {
		t.Fatalf("Count tras el doble registro = %d, quiero 1", got)
	}
	if err := reg.Push(context.Background(), "s1", newSendText("hola")); err != nil {
		t.Fatalf("Push devolvió error: %v", err)
	}
	if old.count() != 0 {
		t.Fatalf("el sender viejo recibió %d mensajes, quiero 0", old.count())
	}
	if current.count() != 1 {
		t.Fatalf("el sender nuevo recibió %d mensajes, quiero 1", current.count())
	}

	releaseOld()
	if !reg.Online("s1") {
		t.Fatal("el release de la sesión reemplazada no debe marcarla offline")
	}
	if err := reg.Push(context.Background(), "s1", newSendText("otra")); err != nil || current.count() != 2 {
		t.Fatalf("tras el release del reemplazado: err=%v, el sender vigente lleva %d mensajes (quiero 2)", err, current.count())
	}

	releaseCurrent()
	if reg.Online("s1") {
		t.Fatal("la sesión debería estar offline tras el release del sender vigente")
	}
}

// R-S4, variante: el MISMO Sender registrado dos veces sigue siendo dos entradas distintas. El
// release compara la entrada de cada Register, no el Sender.
func TestReleaseComparesTheEntryNotTheSender(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()
	shared := &recordingSender{}

	releaseFirst := reg.Register("s1", shared)
	releaseSecond := reg.Register("s1", shared)

	releaseFirst()
	if !reg.Online("s1") {
		t.Fatal("el release del primer registro borró al segundo: compara el Sender y no la entrada")
	}
	releaseSecond()
	if reg.Online("s1") {
		t.Fatal("la sesión debería estar offline tras el release del registro vigente")
	}
}

// El Registry es seguro para uso concurrente: N Push simultáneos llegan todos. La
// serialización del Send es del Sender (contrato de Sender), no del Registry.
func TestConcurrentPushes(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()
	s := &recordingSender{}
	reg.Register("s1", s)

	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			if err := reg.Push(context.Background(), "s1", newSendText("hola")); err != nil {
				t.Errorf("Push concurrente devolvió error: %v", err)
			}
		}()
	}
	wg.Wait()

	if s.count() != n {
		t.Fatalf("el sender recibió %d mensajes, quiero %d", s.count(), n)
	}
}

// Registros, releases, consultas y envíos a la vez sobre sesiones distintas: lo caza -race.
func TestConcurrentRegisterReleaseAndQueries(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()
	ids := []string{"s1", "s2", "s3", "s4", "s5", "s6", "s7", "s8"}

	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				release := reg.Register(id, &recordingSender{})
				_ = reg.Online(id)
				_ = reg.Count()
				// La sesión la registró esta misma goroutine y nadie más toca su id.
				if err := reg.Push(context.Background(), id, newSendText("hola")); err != nil {
					t.Errorf("Push a %s devolvió error: %v", id, err)
				}
				release()
			}
		}()
	}
	wg.Wait()

	if got := reg.Count(); got != 0 {
		t.Fatalf("Count final = %d, quiero 0", got)
	}
}

// El candado del Registry no se retiene durante el Send: con un Edge atascado en s1, el resto
// del registro sigue respondiendo (es lo que mantiene vivo al kill-switch).
func TestABlockedSendDoesNotBlockTheRegistry(t *testing.T) {
	t.Parallel()
	reg := NewRegistry(WithSendTimeout(time.Minute))
	stuck := newBlockingSender(t)
	reg.Register("s1", stuck)
	healthy := &recordingSender{}
	reg.Register("s2", healthy)

	ctx, cancel := context.WithCancel(context.Background())
	pushed := make(chan error, 1)
	go func() { pushed <- reg.Push(ctx, "s1", newSendText("atascado")) }()
	<-stuck.entered

	done := make(chan error, 1)
	go func() {
		release := reg.Register("s3", &recordingSender{})
		defer release()
		if !reg.Online("s1") || reg.Count() != 3 {
			done <- errors.New("Online/Count no reflejan las tres sesiones")
			return
		}
		done <- reg.Push(context.Background(), "s2", newSendText("sano"))
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("con s1 atascada, el resto del registro falló: %v", err)
		}
	case <-time.After(watchdog):
		t.Fatal("con s1 atascada, Register/Online/Count/Push a otra sesión se colgaron")
	}
	if healthy.count() != 1 {
		t.Fatalf("el sender sano recibió %d mensajes, quiero 1", healthy.count())
	}

	cancel()
	if err := <-pushed; !errors.Is(err, ErrPushAbandoned) {
		t.Fatalf("el Push atascado devolvió %v al cancelar, quiero ErrPushAbandoned", err)
	}
}

// El Sender es una interfaz de UN método: cualquier cosa con Send(*CloudToEdge) error sirve.
var _ Sender = (*recordingSender)(nil)

// Las firmas del Registry: las consumen el gateway gRPC y el arranque, y varias viajan por
// interfaces estructurales de los consumidores. Las expresiones de método fijan cada una.
var (
	_ func(...RegistryOption) *Registry                                        = NewRegistry
	_ func(*Registry, string, Sender) func()                                   = (*Registry).Register
	_ func(*Registry, context.Context, string, *cloudlinkv1.CloudToEdge) error = (*Registry).Push
	_ func(*Registry) time.Duration                                            = (*Registry).SendTimeout
	_ func(*Registry, string) bool                                             = (*Registry).Online
	_ func(*Registry) int                                                      = (*Registry).Count
	// Los tests de BoundedSend están en registry_clocks_test.go.
	_ func(context.Context, Sender, *cloudlinkv1.CloudToEdge, time.Duration, string) error = BoundedSend
)
