package arranque

// La parada del CloudLink (D-F3-13, hallazgo 81 de F3): con un Edge conectado, parar en cuanto
// no queda nada en vuelo, con shutdownTimeout como tope. La espera se prueba con un servidor
// de mentira y RELOJ SIMULADO (testing/synctest): cada instante que se afirma es exacto, no un
// «menos de». Lo que solo puede decir un grpc de verdad —que Stop() no espera a los handlers y
// GracefulStop sí— está en servir_cloudlink_wire_test.go.

import (
	"bytes"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"
)

// Los dos mensajes de la parada, por el trozo que los distingue.
const (
	logStoppedEarly = "nada en vuelo; Stop() sin agotar el plazo"
	logForcedStop   = "GracefulStop excedió el timeout; forzando Stop()"
)

// stopLog captura lo que escribe la parada desde cualquier goroutine.
type stopLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *stopLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

// lines devuelve las líneas escritas que contienen sub.
func (l *stopLog) lines(sub string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for line := range strings.SplitSeq(l.buf.String(), "\n") {
		if strings.Contains(line, sub) {
			out = append(out, line)
		}
	}
	return out
}

func newStopLog() (sharedlogger.Logger, *stopLog) {
	l := &stopLog{}
	return sharedlogger.New(sharedlogger.WithWriter(l)), l
}

// fakeGRPC es un servidor gRPC de mentira con la conducta que la parada necesita del de
// verdad: con un stream abierto, GracefulStop no vuelve hasta que alguien llama a Stop(), y
// entonces tarda `closing` más —lo que tardan los handlers, ya sin stream, en cerrar—.
type fakeGRPC struct {
	// openStream: hay un stream que no termina solo (un Edge conectado, o una rpc colgada).
	openStream bool
	// closing es lo que tardan los handlers en terminar después del Stop().
	closing time.Duration

	born      time.Time
	once      sync.Once
	stopped   chan struct{}
	stops     atomic.Int32
	stoppedAt atomic.Int64 // desde born, en ns; el del PRIMER Stop()
}

func newFakeGRPC(openStream bool, closing time.Duration) *fakeGRPC {
	return &fakeGRPC{openStream: openStream, closing: closing, born: time.Now(), stopped: make(chan struct{})}
}

func (f *fakeGRPC) GracefulStop() {
	if !f.openStream {
		return
	}
	<-f.stopped
	time.Sleep(f.closing) // reloj simulado: es la duración del cierre de los handlers, no una espera del test
}

func (f *fakeGRPC) Stop() {
	f.stops.Add(1)
	f.once.Do(func() {
		f.stoppedAt.Store(int64(time.Since(f.born)))
		close(f.stopped)
	})
}

// requireStop afirma cuántas veces se llamó a Stop() y, si se llamó, en qué instante.
func (f *fakeGRPC) requireStop(t *testing.T, who string, wantStops int32, wantAt time.Duration) {
	t.Helper()
	if got := f.stops.Load(); got != wantStops {
		t.Fatalf("%s: Stop() se llamó %d veces, se esperaban %d", who, got, wantStops)
	}
	if got := time.Duration(f.stoppedAt.Load()); wantStops > 0 && got != wantAt {
		t.Fatalf("%s: Stop() a los %s, se esperaba a los %s", who, got, wantAt)
	}
}

// inFlightUntil es un contador que dice n hasta el instante `until` y 0 después. Es función
// pura del reloj simulado: no hay goroutine que lo mueva.
func inFlightUntil(n int, until time.Duration) func() int {
	born := time.Now()
	return func() int {
		if time.Since(born) < until {
			return n
		}
		return 0
	}
}

// quietWindow es lo que tarda la parada en dar por bueno «nada en vuelo» desde la primera
// lectura a cero: las lecturas que faltan, a un intervalo cada una.
const quietWindow = (inFlightQuietReads - 1) * inFlightPollInterval

// requireLog afirma cuántas líneas de cada clase escribió la parada, y que todas nombran al
// servidor.
func requireLog(t *testing.T, log *stopLog, server string, wantEarly, wantForced int) {
	t.Helper()
	early, forced := log.lines(logStoppedEarly), log.lines(logForcedStop)
	if len(early) != wantEarly || len(forced) != wantForced {
		t.Fatalf("log de la parada: %d «nada en vuelo» y %d «forzando Stop()», se esperaban %d y %d\n%s\n%s",
			len(early), len(forced), wantEarly, wantForced, strings.Join(early, "\n"), strings.Join(forced, "\n"))
	}
	for _, line := range append(early, forced...) {
		if !strings.Contains(line, server) {
			t.Errorf("la línea no nombra al servidor %q: %s", server, line)
		}
	}
}

// El caso del hallazgo 81: un Edge conectado y nada en vuelo. La parada NO agota el plazo: da
// Stop() tras la ventana de silencio y vuelve cuando los handlers han terminado —ni antes—.
func TestCloudLinkStopsAtOnceWhenNothingIsInFlight(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		const closing = time.Second
		log, logs := newStopLog()
		gs := newFakeGRPC(true, closing)
		start := time.Now()

		gracefulStopGRPC(gs, "cloudlink", inFlightUntil(0, 0), log)

		gs.requireStop(t, "cloudlink", 1, quietWindow)
		if got, want := time.Since(start), quietWindow+closing; got != want {
			t.Fatalf("la parada volvió a los %s, se esperaba a los %s: Stop() tras el silencio y luego "+
				"esperar a que GracefulStop vuelva (los handlers y su closeStream)", got, want)
		}
		if quietWindow+closing >= shutdownTimeout {
			t.Fatalf("el caso no distingue nada: %s no es menos que el plazo (%s)", quietWindow+closing, shutdownTimeout)
		}
		requireLog(t, logs, "cloudlink", 1, 0)
	})
}

// Con algo en vuelo la parada ESPERA: no da Stop() mientras el contador no sea 0, y lo da en
// cuanto se resuelve (más la ventana de silencio), sin llegar al plazo.
func TestCloudLinkWaitsForWhatIsInFlight(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		const resolvesAt = 3 * time.Second
		log, logs := newStopLog()
		gs := newFakeGRPC(true, 0)
		start := time.Now()

		gracefulStopGRPC(gs, "cloudlink", inFlightUntil(2, resolvesAt), log)

		gs.requireStop(t, "cloudlink", 1, resolvesAt+quietWindow)
		if got, want := time.Since(start), resolvesAt+quietWindow; got != want {
			t.Fatalf("la parada volvió a los %s, se esperaba a los %s", got, want)
		}
		requireLog(t, logs, "cloudlink", 1, 0)
		if line := logs.lines(logStoppedEarly)[0]; !strings.Contains(line, "max_en_vuelo=2") {
			t.Errorf("el Info no dice cuánto llegó a haber en vuelo (max_en_vuelo=2): %s", line)
		}
	})
}

// Si lo que está en vuelo no se resuelve, el tope es el de siempre: Stop() al vencer
// shutdownTimeout, con su Warn —que además dice cuánto quedaba—. Ni antes ni después; y
// también aquí se espera a que los handlers terminen.
func TestCloudLinkStopsAtTheDeadlineWhenItNeverResolves(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		const closing = time.Second
		log, logs := newStopLog()
		gs := newFakeGRPC(true, closing)
		start := time.Now()

		gracefulStopGRPC(gs, "cloudlink", func() int { return 4 }, log)

		gs.requireStop(t, "cloudlink", 1, shutdownTimeout)
		if got, want := time.Since(start), shutdownTimeout+closing; got != want {
			t.Fatalf("la parada volvió a los %s, se esperaba a los %s: Stop() al vencer el plazo y luego "+
				"esperar a que GracefulStop vuelva (los handlers y su closeStream)", got, want)
		}
		requireLog(t, logs, "cloudlink", 0, 1)
		if line := logs.lines(logForcedStop)[0]; !strings.Contains(line, "en_vuelo=4") {
			t.Errorf("el Warn no dice cuánto quedaba en vuelo (en_vuelo=4): %s", line)
		}
	})
}

// Una sola lectura a cero no basta: el contador pasa por cero entre dos pasos de una cadena
// (envío → Ack → envío). Una lectura distinta de cero REINICIA la cuenta del silencio.
func TestCloudLinkAZeroBetweenTwoSendsDoesNotStopIt(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		// Lecturas cada 25 ms: 0, 0, 1 (la del instante 50 ms), y de ahí en adelante 0.
		const busyFrom, busyUntil = 2 * inFlightPollInterval, 3 * inFlightPollInterval
		born := time.Now()
		counter := func() int {
			if at := time.Since(born); at >= busyFrom && at < busyUntil {
				return 1
			}
			return 0
		}
		log, logs := newStopLog()
		gs := newFakeGRPC(true, 0)

		gracefulStopGRPC(gs, "cloudlink", counter, log)

		gs.requireStop(t, "cloudlink", 1, busyUntil+quietWindow)
		requireLog(t, logs, "cloudlink", 1, 0)
	})
}

// Sin ningún Edge conectado GracefulStop vuelve solo: no hay Stop(), no hay espera y no hay
// log, haya o no contador.
func TestCloudLinkWithoutStreamsJustStops(t *testing.T) {
	t.Parallel()
	for name, counter := range map[string]func() int{"with counter": func() int { return 7 }, "without counter": nil} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				log, logs := newStopLog()
				gs := newFakeGRPC(false, 0)
				start := time.Now()

				gracefulStopGRPC(gs, "cloudlink", counter, log)

				gs.requireStop(t, "cloudlink", 0, 0)
				if got := time.Since(start); got != 0 {
					t.Fatalf("la parada sin streams tardó %s, se esperaba que volviera en el acto", got)
				}
				requireLog(t, logs, "cloudlink", 0, 0)
			})
		})
	}
}

// Sin contador (nil) la conducta es la de siempre, la del arranque viejo: con un stream
// abierto se agota el plazo entero y se fuerza Stop() con el Warn.
func TestGracefulStopWithoutCounterKeepsTheOldWait(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		const closing = time.Second
		log, logs := newStopLog()
		gs := newFakeGRPC(true, closing)
		start := time.Now()

		gracefulStopGRPC(gs, "enroll", nil, log)

		gs.requireStop(t, "enroll", 1, shutdownTimeout)
		if got, want := time.Since(start), shutdownTimeout+closing; got != want {
			t.Fatalf("la parada volvió a los %s, se esperaba el plazo entero más el cierre de los handlers (%s)", got, want)
		}
		requireLog(t, logs, "enroll", 0, 1)
	})
}

// shutdownAll: el contador es SOLO del CloudLink. El servidor de enrolamiento no cambia: con
// una rpc colgada agota su plazo aunque el contador diga 0, y no lo consulta. El CloudLink,
// después, para tras la ventana de silencio.
func TestShutdownAllOnlyTheCloudLinkLooksAtTheCounter(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		log, logs := newStopLog()
		enroll, connect := newFakeGRPC(true, 0), newFakeGRPC(true, 0)
		var reads atomic.Int32
		var firstReadAt atomic.Int64
		start := time.Now()
		counter := func() int {
			if reads.Add(1) == 1 {
				firstReadAt.Store(int64(time.Since(start)))
			}
			return 0
		}

		// Dos http.Server sin arrancar: su Shutdown vuelve en el acto.
		idle := func() *http.Server { return &http.Server{ReadHeaderTimeout: readHeaderTimeout} }
		shutdownAll(idle(), idle(), enroll, connect, counter, log)

		enroll.requireStop(t, "enroll", 1, shutdownTimeout)
		connect.requireStop(t, "cloudlink", 1, shutdownTimeout+quietWindow)
		if got := time.Duration(firstReadAt.Load()); got != shutdownTimeout {
			t.Fatalf("el contador se leyó por primera vez a los %s: la parada del enrolamiento no debe "+
				"consultarlo (se esperaba a los %s, ya con el CloudLink)", got, shutdownTimeout)
		}
		if got := reads.Load(); got != inFlightQuietReads {
			t.Fatalf("el contador se leyó %d veces, se esperaban %d", got, inFlightQuietReads)
		}
		if len(logs.lines(logForcedStop)) != 1 || !strings.Contains(logs.lines(logForcedStop)[0], "enroll") {
			t.Errorf("se esperaba UN Warn de plazo agotado, el del enrolamiento: %v", logs.lines(logForcedStop))
		}
		if len(logs.lines(logStoppedEarly)) != 1 || !strings.Contains(logs.lines(logStoppedEarly)[0], "cloudlink") {
			t.Errorf("se esperaba UN Info de parada sin espera, el del CloudLink: %v", logs.lines(logStoppedEarly))
		}
	})
}

// El cableado: el servidor CloudLink que servir pone a escuchar es c.connectGS con el InFlight
// del gateway del contenedor —el único del proceso (TestCableado_TheBootBuildsOneNewGateway)—
// y en reposo dice 0. Sin gateway, el contador es nil: la parada de siempre.
func TestCloudLinkServerCarriesTheGatewayCounter(t *testing.T) {
	c := contenedorDeHuella(t, "minimo")
	s := cloudLinkServer(c)
	if s.gs != c.connectGS || s.lis != c.connectLis {
		t.Fatal("cloudLinkServer no devuelve el servidor y el listener CloudLink del contenedor")
	}
	if s.inFlight == nil {
		t.Fatal("el CloudLink no lleva contador: su parada sería la de siempre (10 s con un Edge conectado)")
	}
	if got, want := reflect.ValueOf(s.inFlight).Pointer(), reflect.ValueOf(c.gw.InFlight).Pointer(); got != want {
		t.Error("el contador del CloudLink no es el InFlight del gateway")
	}
	if got := s.inFlight(); got != 0 {
		t.Errorf("contador en reposo = %d, se esperaba 0", got)
	}

	if s := cloudLinkServer(&contenedor{}); s.inFlight != nil {
		t.Error("sin gateway el contador debe ser nil")
	}
}
