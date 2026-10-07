package arranque

// La parada del CloudLink contra un grpc.Server DE VERDAD (bufconn, en memoria), que es lo
// único que puede decir si las dos cosas que servir_cloudlink.go da por ciertas lo son:
//
//   - con un stream abierto que no termina solo, la parada NO agota shutdownTimeout;
//   - tras el Stop(), la parada vuelve solo cuando el handler ha terminado su cierre (en el
//     gateway: closeStream, con el MarkOffline y el drenaje del carril). Stop() no espera a
//     los handlers; quien los espera es el GracefulStop que sigue corriendo.
//
// El servicio es uno de mentira con un solo stream bidi cuyo handler hace lo que hace Connect:
// se queda hasta que el stream muere y entonces limpia.

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// wireWatchdog convierte un cuelgue en un fallo legible. No sincroniza nada.
const wireWatchdog = 5 * time.Second

func awaitWire[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(wireWatchdog):
		t.Fatalf("colgado esperando: %s", what)
		panic("inalcanzable")
	}
}

// holdService es un servicio con un stream bidi, /arranque.test.Hold/Hold, cuyo handler avisa
// al entrar, espera a que el stream muera, avisa, y no termina su «cierre» hasta que el test
// abre cleanup.
type holdService struct {
	entered    chan struct{}
	streamDead chan struct{}
	cleanup    chan struct{}
	cleaned    chan struct{}
}

func (h *holdService) hold(_ any, stream grpc.ServerStream) error {
	close(h.entered)
	<-stream.Context().Done()
	close(h.streamDead)
	<-h.cleanup
	close(h.cleaned)
	return nil
}

var holdStream = grpc.StreamDesc{StreamName: "Hold", ServerStreams: true, ClientStreams: true}

// serveHold sirve el servicio por bufconn y deja un cliente con el stream abierto y su
// handler ya dentro.
func serveHold(t *testing.T) (*grpc.Server, *holdService) {
	t.Helper()
	h := &holdService{
		entered: make(chan struct{}), streamDead: make(chan struct{}),
		cleanup: make(chan struct{}), cleaned: make(chan struct{}),
	}
	gs := grpc.NewServer()
	desc := holdStream
	desc.Handler = h.hold
	gs.RegisterService(&grpc.ServiceDesc{
		ServiceName: "arranque.test.Hold",
		HandlerType: (*any)(nil),
		Streams:     []grpc.StreamDesc{desc},
	}, h)

	lis := bufconn.Listen(1 << 16)
	served := make(chan error, 1)
	go func() { served <- gs.Serve(lis) }()
	t.Cleanup(func() {
		gs.Stop()
		awaitWire(t, served, "Serve vuelve")
	})

	conn, err := grpc.NewClient("passthrough:///hold",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if _, err := conn.NewStream(ctx, &holdStream, "/arranque.test.Hold/Hold"); err != nil {
		t.Fatalf("abriendo el stream: %v", err)
	}
	awaitWire(t, h.entered, "el handler recibe el stream")
	return gs, h
}

// Un grpc.Server de verdad con un stream abierto y nada en vuelo: la parada corta el stream
// sin agotar el plazo, y NO vuelve hasta que el handler termina su cierre.
func TestCloudLinkStopOverARealServerWaitsForTheHandlers(t *testing.T) {
	t.Parallel()
	gs, h := serveHold(t)
	log, logs := newStopLog()

	start := time.Now()
	returned := make(chan struct{})
	go func() {
		gracefulStopGRPC(gs, "cloudlink", func() int { return 0 }, log)
		close(returned)
	}()

	// El stream del cliente no termina solo: si muere, es porque la parada dio Stop().
	awaitWire(t, h.streamDead, "la parada corta el stream abierto")
	cut := time.Since(start)
	if cut >= shutdownTimeout/2 {
		t.Fatalf("el stream se cortó a los %s: con nada en vuelo no debe agotarse el plazo (%s)", cut, shutdownTimeout)
	}

	// El handler sigue en su cierre: la parada no puede haber vuelto.
	select {
	case <-returned:
		t.Fatal("la parada volvió con el handler todavía cerrando: se perdería su closeStream (MarkOffline, drenaje)")
	default:
	}
	close(h.cleanup)
	awaitWire(t, returned, "la parada vuelve cuando el handler termina")
	select {
	case <-h.cleaned:
	default:
		t.Fatal("la parada volvió antes de que el handler terminara su cierre")
	}
	requireLog(t, logs, "cloudlink", 1, 0)
}

// El tipo de producción cumple el puerto de la parada.
var _ grpcStopper = (*grpc.Server)(nil)
