// Nace con D-F3-13 (hallazgo 81 de F3). No es copia de internal/bootstrap/arranque: el arranque
// viejo no tiene esta espera y conserva la suya (GracefulStop con el plazo entero).
package arranque

import (
	"time"

	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"
)

// grpcStopper es lo que la parada necesita de un *grpc.Server: las dos formas de pararlo.
// Existe para que la espera se pueda probar con un servidor de mentira y reloj simulado
// (testing/synctest), sin sockets; en producción es siempre un *grpc.Server.
type grpcStopper interface {
	// GracefulStop deja de aceptar, manda GOAWAY y vuelve cuando han terminado todos los
	// streams y sus handlers.
	GracefulStop()
	// Stop cierra las conexiones en el acto; el GracefulStop que estuviera esperando vuelve
	// cuando los handlers, ya sin stream, terminan.
	Stop()
}

const (
	// inFlightPollInterval es cada cuánto se le pregunta al gateway cuánto tiene en vuelo
	// mientras se espera para parar el CloudLink. Es un sondeo y no un aviso a propósito: el
	// gateway no sabe nada de la parada y no se le añade un canal para un camino que corre
	// una vez por vida del proceso. 25 ms porque la lectura cuesta dos len bajo dos mutex
	// —se puede hacer cuarenta veces por segundo sin que nadie lo note— y porque es el grano
	// de lo que la parada tarda de más: con un Edge conectado y nada en vuelo, dos
	// intervalos (ver inFlightQuietReads) frente a los 10 s de antes.
	inFlightPollInterval = 25 * time.Millisecond

	// inFlightQuietReads es cuántas lecturas SEGUIDAS a cero hacen falta para dar por bueno
	// «no hay nada en vuelo». Tres lecturas son dos intervalos: 50 ms de silencio. El porqué
	// de que no baste una está en stopWhenNothingInFlight.
	inFlightQuietReads = 3
)

// cloudLinkServer arma el servidor CloudLink que servir pone a escuchar, con su contador de
// lo que está en vuelo: el InFlight del ÚNICO gateway del proceso, c.gw —el mismo que la fase
// 8 registró en c.connectGS—. No construye nada. Sin gateway el contador queda nil, que es
// «la parada de siempre».
func cloudLinkServer(c *contenedor) grpcServer {
	s := grpcServer{gs: c.connectGS, lis: c.connectLis, addr: c.cfg.GRPCConnectAddr, name: "CloudLink (mTLS)"}
	if c.gw != nil {
		s.inFlight = c.gw.InFlight
	}
	return s
}

// stopWhenNothingInFlight es la espera de la parada del CloudLink (D-F3-13): con el
// GracefulStop ya lanzado por gracefulStopGRPC —done se cierra cuando vuelve—, espera a lo
// PRIMERO de:
//
//	(a) GracefulStop volvió solo: no había ningún Edge conectado. Sin log, como siempre.
//	(b) inFlight dio 0 inFlightQuietReads veces seguidas ⇒ Stop(), un Info y esperar a done.
//	(c) venció shutdownTimeout ⇒ el Warn de siempre (más cuánto quedaba), Stop(), esperar a done.
//
// 🔴 EL DEFECTO QUE ARREGLA. GracefulStop espera a que terminen los streams abiertos, y el
// stream bidi Connect de un Edge no termina nunca por sí solo. Con un Edge conectado, la
// parada del viejo agota SIEMPRE los 10 s y acaba en el Stop() forzado con su Warn (< 0,5 s
// sin Edge). Lo único útil de esa ventana es dejar terminar lo que está EN VUELO —un envío
// esperando el Ack del Edge, una inferencia esperando su InferenceResult—; cuando no hay
// nada, son 10 s de despliegue perdidos por reinicio. El final NO cambia: es el mismo Stop().
//
// 🔴 SIEMPRE SE ESPERA A done, también tras Stop(). Stop() cierra las conexiones pero NO
// espera a los handlers (grpc-go sin WaitForHandlers); quien los espera es el GracefulStop
// que sigue corriendo en su goroutine (handlersWG.Wait). Y el handler de Connect, al
// quedarse sin stream, hace su closeStream: cancela lo que quedara en vuelo, encola el
// MarkOffline de cada sesión y drena el carril. Volver sin esperar a done sería cerrar la
// base (c.cerrar) con ese MarkOffline a medias.
//
// ⚠️ POR QUÉ VARIAS LECTURAS A CERO Y NO UNA. InFlight es una foto (lo dice su contrato), y
// con los HTTP ya cerrados sigue habiendo quien pide cosas al Edge:
//
//   - el RUNTIME DE FLUJOS, que contesta a los Incoming que el Edge sigue mandando por su
//     stream abierto: corre en el carril del stream con un contexto que NO hereda la
//     cancelación (context.WithoutCancel), y una respuesta de varios mensajes es una cadena
//     envío → Ack → envío;
//   - los WORKERS DE FONDO (fase 9: pipeline de captación, inferencia anticipada, agregador,
//     ciclo de vida de flujos, webhooks): su ctx ya está cancelado y no empiezan trabajo
//     nuevo, pero el que tenían a medias puede encadenar otra inferencia u otro envío;
//   - el calentamiento (OnWarmup) y el saludo, que nacen de un latido o de un registro.
//
// En una cadena, entre el Ack de un paso y el registro del siguiente el contador pasa por
// cero durante microsegundos: una sola lectura que cayera ahí cortaría una respuesta por la
// mitad. Exigir 50 ms de silencio cubre ese hueco —el siguiente paso de la misma goroutine
// nace mucho antes— y cuesta 50 ms. Cualquier lectura distinta de cero reinicia la cuenta.
//
// ⚠️ LO QUE NO CUBRE, y es una decisión (D-F3-13), no un descuido. Ninguna cantidad de
// lecturas cierra la carrera: un envío puede nacer justo después de la última. Y el contador
// no ve lo que TODAVÍA no ha pedido nada al Edge: un Incoming que se está procesando en el
// carril y aún no ha llegado a su envío, o una cadena con una consulta larga a la base entre
// dos pasos. Lo que nazca después del corte no se queda colgado: se encuentra la sesión sin
// stream (ErrSessionOffline) o, si llegó a empujarse, lo despierta en el acto la caída del
// stream (ErrStreamClosed). Es la MISMA clase de corte que el viejo hace a los 10 s con lo
// que quede —allí también se corta, solo que después—; lo que cambia es que el trabajo que
// aún no era visible pierde esa ventana. Lo durable (intake_jobs, webhook_outbox) se retoma
// en el siguiente arranque en los dos casos.
//
// inFlight no puede ser nil: quien decide entre esta espera y la de siempre es
// gracefulStopGRPC. Se llama solo desde esta goroutine, una vez por lectura.
func stopWhenNothingInFlight(gs grpcStopper, done <-chan struct{}, name string, inFlight func() int, log sharedlogger.Logger) {
	start := time.Now()
	deadline := time.NewTimer(shutdownTimeout)
	defer deadline.Stop()
	poll := time.NewTicker(inFlightPollInterval)
	defer poll.Stop()

	quiet, last, peak := 0, 0, 0
	for {
		last = inFlight()
		peak = max(peak, last)
		if last == 0 {
			quiet++
		} else {
			quiet = 0
		}
		if quiet >= inFlightQuietReads {
			// Sin PII: un nombre de servidor, una duración y una cuenta.
			log.Info("shutdown gRPC: nada en vuelo; Stop() sin agotar el plazo",
				"servidor", name, "esperado", time.Since(start).String(), "max_en_vuelo", peak)
			gs.Stop()
			<-done
			return
		}
		select {
		case <-done:
			return
		case <-deadline.C:
			log.Warn("shutdown gRPC: GracefulStop excedió el timeout; forzando Stop()",
				"servidor", name, "timeout", shutdownTimeout, "en_vuelo", last)
			gs.Stop()
			<-done
			return
		case <-poll.C:
		}
	}
}
