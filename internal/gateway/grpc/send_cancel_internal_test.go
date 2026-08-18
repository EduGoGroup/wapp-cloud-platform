package gatewaygrpc

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
)

// Los tests de la cancelación de acuses al caer un stream (Plan 050 · Ola 2). Son
// INTERNOS porque miran s.acks y llaman a closeStream/cancelSessionAcks: lo que se
// afirma aquí —que el mapa queda vacío por los tres caminos, y quién puede cerrar
// qué canal— no es observable desde fuera del paquete, y exponerlo solo para el test
// convertiría un detalle de implementación en API.

// mudo acepta todo lo que se le empuje y NUNCA acusa: es el Edge cuyo stream muere
// con el envío en vuelo. Desde el punto de vista del que espera, es idéntico al Edge
// saturado del incidente del 2026-08-06 — y esa indistinguibilidad es justamente el
// defecto que la Ola 2 corrige, dándole a cada caso su propio error.
func mudo() senderFunc {
	return senderFunc(func(*cloudlinkv1.CloudToEdge) error { return nil })
}

// carrilDePrueba da un carril real (no un doble) con la vida atada al test. Los
// cierres que estos tests ejercitan no encolan nada —los connCtx van sin identidad,
// así que onStreamClosed sale antes de tocar fleet—, de modo que el drain es
// inmediato y no introduce espera en las mediciones de latencia.
func carrilDePrueba(t *testing.T) *workLane {
	t.Helper()
	return newWorkLane(context.Background(), 4, time.Second, laneLog())
}

// exigirAcksVacio afirma el criterio literal de T2.1: no queda ni una entrada
// pendiente en el mapa de acuses.
func exigirAcksVacio(t *testing.T, s *Server) {
	t.Helper()
	s.acksMu.Lock()
	defer s.acksMu.Unlock()
	if len(s.acks) != 0 {
		t.Fatalf("s.acks quedó con %d entradas (%v): un camino de salida no está retirando la suya", len(s.acks), s.acks)
	}
}

// TestLosTresCaminosDeSalidaDejanElMapaDeAcksVacio (T2.1) es el criterio de la ola
// escrito como aserción: da igual cómo termine un envío —acuse recibido, plazo
// vencido o sesión sin stream—, su entrada de s.acks se retira.
//
// Es la mitad que faltaba del análisis del T1.1: allí se corrigió el comentario que
// afirmaba que "NADA limpia s.acks" cuando el stream muere sin acusar, pero esa
// corrección no venía con nadie que la sostuviera. Este test es quien la sostiene, y
// con el tercer camino recién nacido es además la garantía de que la cancelación
// nueva no se convierta ella misma en la fuga que decía no existir.
func TestLosTresCaminosDeSalidaDejanElMapaDeAcksVacio(t *testing.T) {
	t.Parallel()

	t.Run("ack recibido", func(t *testing.T) {
		t.Parallel()
		reg := session.NewRegistry()
		srv := New(reg, laneLog(), WithAckTimeout(5*time.Second))

		release := reg.Register("s-viva", senderFunc(func(msg *cloudlinkv1.CloudToEdge) error {
			go srv.deliverAck(&cloudlinkv1.Ack{AckedCommandId: msg.GetCommandId(), Ok: true})
			return nil
		}))
		defer release()

		if _, err := srv.SendText(context.Background(), "s-viva", "57301", "hola"); err != nil {
			t.Fatalf("SendText contra un Edge que acusa: %v", err)
		}
		exigirAcksVacio(t, srv)
	})

	t.Run("plazo vencido", func(t *testing.T) {
		t.Parallel()
		reg := session.NewRegistry()
		release := reg.Register("s-muda", mudo())
		defer release()
		srv := New(reg, laneLog(), WithAckTimeout(50*time.Millisecond))

		_, err := srv.SendText(context.Background(), "s-muda", "57301", "hola")
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("errors.Is(err, context.DeadlineExceeded) = false; err = %v", err)
		}
		exigirAcksVacio(t, srv)
	})

	t.Run("sesión sin stream", func(t *testing.T) {
		t.Parallel()
		srv := New(session.NewRegistry(), laneLog())

		// Registry vacío: el Push falla antes de que haya nada que esperar, y la
		// entrada se retira igual — este camino ni siquiera llega a awaitAck.
		_, err := srv.SendText(context.Background(), "fantasma", "57301", "hola")
		if !errors.Is(err, session.ErrSessionOffline) {
			t.Fatalf("errors.Is(err, ErrSessionOffline) = false; err = %v", err)
		}
		exigirAcksVacio(t, srv)
	})
}

// TestCierreDeStreamDespiertaAlInstanteElEnvioEnVuelo (T2.2 · T2.3) es el test del
// defecto entero: el ackTimeout es el de producción (8 s), así que si la cancelación
// no funcionara este test no fallaría por una aserción sino por tardar 8 s — que es
// exactamente lo que hoy le pasa al llamante HTTP cuando el Edge se desconecta con
// un envío a medias.
func TestCierreDeStreamDespiertaAlInstanteElEnvioEnVuelo(t *testing.T) {
	t.Parallel()
	reg := session.NewRegistry()
	srv := New(reg, laneLog(), WithAckTimeout(defaultAckTimeout))

	empujado := make(chan struct{}, 1)
	release := reg.Register("s-1", senderFunc(func(*cloudlinkv1.CloudToEdge) error {
		select {
		case empujado <- struct{}{}:
		default:
		}
		return nil
	}))

	type salida struct {
		err error
	}
	res := make(chan salida, 1)
	go func() {
		_, err := srv.SendText(context.Background(), "s-1", "57301", "hola")
		res <- salida{err: err}
	}()

	// El comando ya viajó: la entrada de s.acks se registra ANTES del Push, así que a
	// partir de aquí el envío está en vuelo con toda seguridad.
	<-empujado

	cierre := time.Now()
	srv.closeStream(carrilDePrueba(t), connCtx{}, map[string]func(){"s-1": release})
	got := <-res
	tardanza := time.Since(cierre)

	if tardanza > 100*time.Millisecond {
		t.Fatalf("el envío en vuelo tardó %v en rendirse tras el cierre (ack_timeout=%v): "+
			"el llamante sigue pagando la espera entera por un acuse que nadie va a mandar",
			tardanza, defaultAckTimeout)
	}
	if !errors.Is(got.err, ErrStreamClosed) {
		t.Fatalf("errors.Is(err, ErrStreamClosed) = false; err = %v", got.err)
	}
	// Y NO es un timeout: distinguir las dos cosas es lo que permite al mapeo HTTP
	// (T2.4) contestar distinto a "no contestó a tiempo" y a "se cayó".
	if errors.Is(got.err, context.DeadlineExceeded) {
		t.Fatalf("el error de cierre se sigue confundiendo con el del plazo vencido: %v", got.err)
	}

	// El command_id es lo que hace diagnosticable el fallo: el comando YA se empujó al
	// Edge, así que sin él nadie puede averiguar después si el mensaje llegó a salir.
	var se *SendError
	if !errors.As(got.err, &se) {
		t.Fatalf("el error no es *SendError: %v", got.err)
	}
	if se.CommandID() == "" {
		t.Fatal("la cancelación por cierre no lleva command_id: se pierde la correlación con el outbox del Edge")
	}
	if se.SessionID() != "s-1" {
		t.Fatalf("session_id = %q, quiero s-1", se.SessionID())
	}

	exigirAcksVacio(t, srv)
}

// TestCierreDelStreamViejoNoCancelaSiLaSesionSigueOnline protege el hallazgo de la
// Ola 2: cancelar "los acuses de la sesión" a secas al cerrar un stream es un FALSO
// POSITIVO en cuanto el Edge reconecta rápido.
//
// El montaje reproduce la secuencia real: el stream A registra la sesión, el Edge
// reconecta y el stream B la vuelve a registrar (Register es última-gana), y solo
// DESPUÉS termina de morir el A. Su release() compara identidad, así que es un no-op
// deliberado — y si el cierre no preguntara por Online(), mataría los envíos en vuelo
// del stream B, contándole al operador que la sesión se cayó sobre un Edge sano.
//
// Sin este test, el falso positivo vuelve en la primera refactorización que "limpie"
// la condición por parecer redundante. La segunda mitad —cuando también cae B— está
// aquí para que el test no pueda pasar por vacuidad: si cancelSessionAcks estuviera
// muerto, la primera mitad quedaría verde igual.
func TestCierreDelStreamViejoNoCancelaSiLaSesionSigueOnline(t *testing.T) {
	t.Parallel()
	reg := session.NewRegistry()
	srv := New(reg, laneLog())

	releaseViejo := reg.Register("s-1", mudo())
	releaseNuevo := reg.Register("s-1", mudo())

	ch := make(chan *cloudlinkv1.Ack, 1)
	srv.acksMu.Lock()
	srv.acks["cmd-en-vuelo"] = pendingAck{ch: ch, sessionID: "s-1"}
	srv.acksMu.Unlock()

	srv.closeStream(carrilDePrueba(t), connCtx{}, map[string]func(){"s-1": releaseViejo})

	srv.acksMu.Lock()
	_, sigue := srv.acks["cmd-en-vuelo"]
	srv.acksMu.Unlock()
	if !sigue {
		t.Fatal("el cierre del stream VIEJO retiró un acuse en vuelo con la sesión todavía online: " +
			"el Ack del stream nuevo se quedaría sin destino y el llamante vería un fallo inventado")
	}
	select {
	case _, ok := <-ch:
		if !ok {
			t.Fatal("el cierre del stream VIEJO cerró el canal de un envío que el stream NUEVO todavía puede acusar")
		}
		t.Fatal("llegó un ack que nadie mandó")
	default:
	}

	// Ahora sí cae el stream que está registrado: la sesión se queda sin nadie y el
	// acuse en vuelo se cancela.
	srv.closeStream(carrilDePrueba(t), connCtx{}, map[string]func(){"s-1": releaseNuevo})
	// Con reloj y no con un `<-ch` a pelo: si la cancelación desapareciera del cierre,
	// un receive desnudo colgaría el test hasta el timeout de `go test` en vez de
	// decir qué falló.
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("llegó un ack que nadie mandó")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("el canal sigue abierto tras caer el ÚLTIMO stream de la sesión: nadie cancela los acuses en vuelo")
	}
	exigirAcksVacio(t, srv)
}

// TestAckTardioYCierreConcurrentesNoEscribenEnCanalCerrado ejercita la invariante de
// cancelSessionAcks a golpes: un Ack que llega justo cuando el stream se cierra.
//
// Los dos modos de fallo que caza son PÁNICOS, no aserciones —enviar sobre un canal
// cerrado, o cerrarlo dos veces—, así que el test no necesita comprobar nada al
// final: si la invariante se rompiera (por ejemplo, si deliverAck pasara a escribir
// antes de retirar la entrada, o si clearAck empezara a cerrar), el binario de test
// se cae. Corre bajo -race como todos los del paquete.
func TestAckTardioYCierreConcurrentesNoEscribenEnCanalCerrado(t *testing.T) {
	t.Parallel()
	srv := New(session.NewRegistry(), laneLog())

	const rondas = 300
	for i := range rondas {
		cmdID := fmt.Sprintf("cmd-%d", i)
		ch := make(chan *cloudlinkv1.Ack, 1)
		srv.acksMu.Lock()
		srv.acks[cmdID] = pendingAck{ch: ch, sessionID: "s-1"}
		srv.acksMu.Unlock()

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			srv.deliverAck(&cloudlinkv1.Ack{AckedCommandId: cmdID, Ok: true})
		}()
		go func() {
			defer wg.Done()
			srv.cancelSessionAcks("s-1")
		}()
		wg.Wait()
	}

	exigirAcksVacio(t, srv)
}

// TestElCierreDeUnaSesionNoCancelaLosEnviosDeOtra defiende el filtro por sesión de
// cancelSessionAcks, que hasta el 2026-08-18 no tenía quien lo sostuviera: quitarlo
// dejaba el paquete ENTERO en verde. Salió de la verificación por mutación de la Ola
// 2, y lo que deja pasar no es un detalle — en un gateway con varios Edges, la caída
// del stream de UNO cancelaría los envíos en vuelo de TODOS los demás, cada uno con
// un ErrStreamClosed que miente sobre una conexión perfectamente sana.
//
// La aserción que importa es la NEGATIVA: que el envío de la sesión intacta SIGA
// esperando. Comprobar que el de s-1 se cancela no prueba nada aquí —de eso ya se
// encarga el test de arriba—, y por eso el segundo envío se verifica con una espera
// que debe AGOTARSE sin traer resultado.
func TestElCierreDeUnaSesionNoCancelaLosEnviosDeOtra(t *testing.T) {
	t.Parallel()
	reg := session.NewRegistry()
	srv := New(reg, laneLog(), WithAckTimeout(defaultAckTimeout))

	enVuelo := func(sid string) (chan struct{}, func()) {
		empujado := make(chan struct{}, 1)
		release := reg.Register(sid, senderFunc(func(*cloudlinkv1.CloudToEdge) error {
			select {
			case empujado <- struct{}{}:
			default:
			}
			return nil
		}))
		return empujado, release
	}

	empujado1, release1 := enVuelo("s-1")
	empujado2, release2 := enVuelo("s-2")

	res1 := make(chan error, 1)
	res2 := make(chan error, 1)
	go func() {
		_, err := srv.SendText(context.Background(), "s-1", "57301", "uno")
		res1 <- err
	}()
	go func() {
		_, err := srv.SendText(context.Background(), "s-2", "57302", "dos")
		res2 <- err
	}()
	<-empujado1
	<-empujado2

	// Cae SOLO el stream de s-1. El de s-2 sigue vivo y registrado.
	srv.closeStream(carrilDePrueba(t), connCtx{}, map[string]func(){"s-1": release1})

	if err := <-res1; !errors.Is(err, ErrStreamClosed) {
		t.Fatalf("el envío de la sesión que SÍ cayó no se canceló: err = %v", err)
	}

	select {
	case err := <-res2:
		t.Fatalf("el envío de s-2 murió al caer el stream de s-1 (err = %v): cancelSessionAcks "+
			"dejó de filtrar por sesión y se lleva por delante los envíos de los demás Edges", err)
	case <-time.After(200 * time.Millisecond):
	}

	// Cierre ordenado: sin esto la goroutine de s-2 colgaría hasta el ackTimeout.
	srv.closeStream(carrilDePrueba(t), connCtx{}, map[string]func(){"s-2": release2})
	<-res2
	exigirAcksVacio(t, srv)
}
