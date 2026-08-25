package gatewaygrpc

import (
	"io"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
)

// readiness_internal_test.go — EL CLOUD CALIENTA CUANDO EL EDGE DICE QUE PUEDE
// (Plan 044 · Ola 1.8 · T1.8-6, cierra DEUDA-044.7).
//
// 🔴 TODOS ESTOS TESTS ENTRAN POR `route`, QUE ES LA PUERTA REAL DEL FRAME, y no por
// `observaReadiness`. Es la misma lección que dejó TestElLatidoALIMENTA_ElAlmacen: un
// método perfecto sin consumidor deja los tests en verde y el campo sin calentar. Y
// CUENTAN LLAMADAS a OnWarmup, no líneas de log: lo que hay que demostrar es que el
// calentamiento OCURRE o NO OCURRE, no que se anuncie.
//
// El contador es un `int` pelado a propósito, sin mutex ni atómico: OnWarmup se
// invoca INLINE en la goroutine que llama a route (la del bucle Recv en producción,
// la del test aquí). Si algún día alguien mudara el aviso al carril, `-race` lo
// gritaría en vez de dejarlo pasar — que es exactamente lo que queremos que ocurra.

// srvConCalentamientosContados arma un Server con OnWarmup instrumentado. Donde el
// criterio dice «gateway fake», entiéndase «gateway real con OnWarmup instrumentado»:
// aquí no hace falta un doble, el Server se construye de verdad in-process.
func srvConCalentamientosContados(t *testing.T) (*Server, *int) {
	t.Helper()
	srv := New(session.NewRegistry(), logger.New(logger.WithWriter(io.Discard)))
	n := 0
	srv.OnWarmup = func(tenantID, edgeID, sessionID, kind string) {
		if tenantID != "t-1" || edgeID != "e-1" {
			t.Errorf("el aviso trae (%q,%q); el calentamiento es POR EDGE", tenantID, edgeID)
		}
		if sessionID == "" {
			t.Error("el aviso viaja sin session_id: intakeahead.Warm lo descartaría en silencio")
		}
		if kind != "" {
			t.Errorf("kind = %q; tiene que ir vacío (no se publicó ninguna config)", kind)
		}
		n++
	}
	return srv, &n
}

// latidoConReadiness arma el frame REAL: un Heartbeat con el campo 6 del contrato
// (v0.17.0) puesto. Sin LeaseCounter el job del carril no hace nada útil, pero eso da
// igual aquí: lo que se mide es lo que ocurre INLINE, antes del carril.
func latidoConReadiness(sessionID string, r cloudlinkv1.InferenceReadiness) *cloudlinkv1.EdgeToCloud {
	return &cloudlinkv1.EdgeToCloud{
		SessionId: sessionID,
		Payload: &cloudlinkv1.EdgeToCloud_Heartbeat{Heartbeat: &cloudlinkv1.Heartbeat{
			LeaseCounter:       1,
			InferenceReadiness: r,
		}},
	}
}

// TestReadiness_SoloElFlancoAREADYCalienta es el criterio (a) de T1.8-6, entero y en
// una sola secuencia porque las tres preguntas son la misma máquina vista en tres
// momentos: DOWN→READY dispara UNO, la cadencia READY→READY NINGUNO, y
// READY→DOWN→READY DOS.
//
// 🔴 POR QUÉ EL FLANCO Y NO EL ESTADO. `inference_readiness` viaja en TODOS los
// latidos (es estado, no transición: lo dice el propio contrato). Calentar «cuando
// llega READY» sería calentar en cada cadencia del Edge —decenas de veces por hora
// contra la plaza única de su Ollama—, así que lo que se dispara es el cambio.
//
// 🔬 MUTACIÓN: quitar `anterior != READY` del return de anotaReadiness (o sea, dejar
// `return r == READY`) ⇒ rojo en el paso 3, la cadencia.
func TestReadiness_SoloElFlancoAREADYCalienta(t *testing.T) {
	t.Parallel()
	srv, n := srvConCalentamientosContados(t)
	lane := carrilDePrueba(t)
	defer cerrarCarril(lane, func() {})

	cc := ccDePrueba("s-1")
	pasos := []struct {
		nombre   string
		readi    cloudlinkv1.InferenceReadiness
		acumulan int
		porque   string
	}{
		{"1. el Edge arranca sin cajero", cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_DOWN, 0,
			"un Edge que DICE que no puede no se calienta: el trabajo solo podría volver como OLLAMA_DOWN"},
		{"2. el cajero levanta", cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_READY, 1,
			"DOWN→READY es EL evento que esta tarea existe para consumir"},
		{"3. cadencia", cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_READY, 1,
			"READY→READY no es un cambio: calentar aquí sería calentar en cada latido"},
		{"4. el cajero se cae", cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_DOWN, 1,
			"caerse no calienta nada"},
		{"5. y vuelve", cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_READY, 2,
			"READY→DOWN→READY son DOS flancos, y el segundo hay que volver a atenderlo"},
	}
	for _, p := range pasos {
		srv.route(lane, cc, latidoConReadiness(cc.sessionID, p.readi))
		if *n != p.acumulan {
			t.Fatalf("%s: calentamientos acumulados = %d, quiero %d — %s", p.nombre, *n, p.acumulan, p.porque)
		}
	}
}

// TestReadiness_ElCeroNoEsDOWN_NoCalientaNiOlvida cubre la mitad que el criterio (d)
// pone a prueba desde dentro: INFERENCE_READINESS_UNSPECIFIED significa «este Edge no
// lo dice», jamás «no puede».
//
// Las dos afirmaciones van juntas porque son la misma regla:
//
//   - un latido con el cero NO dispara nada (no es un flanco: no dice nada);
//   - y NO BORRA lo aprendido. Si el cero se anotara, un Edge que dijo READY y luego
//     callara volvería a «no lo dice», y el siguiente READY se leería como flanco:
//     un calentamiento inventado por un latido que no cambió nada.
//
// 🔬 MUTACIÓN: en anotaReadiness, cambiar el `return false` del cero por
// `r = DOWN` (leer el cero como DOWN) ⇒ rojo, porque el READY posterior pasa a ser un
// flanco DOWN→READY y el contador sube a 2.
func TestReadiness_ElCeroNoEsDOWN_NoCalientaNiOlvida(t *testing.T) {
	t.Parallel()
	srv, n := srvConCalentamientosContados(t)
	lane := carrilDePrueba(t)
	defer cerrarCarril(lane, func() {})

	cc := ccDePrueba("s-1")
	srv.route(lane, cc, latidoConReadiness(cc.sessionID, cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_READY))
	if *n != 1 {
		t.Fatalf("calentamientos = %d tras el primer READY, quiero 1", *n)
	}
	// Un Edge viejo del mismo (tenant, edge) —o el mismo tras una actualización a la
	// inversa— deja de mandar el campo. Eso no es un veredicto.
	srv.route(lane, cc, latidoConReadiness(cc.sessionID, cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_UNSPECIFIED))
	if *n != 1 {
		t.Fatalf("calentamientos = %d tras un latido que NO DICE NADA, quiero 1: el cero no es un flanco", *n)
	}
	if r := srv.readinessDelEdge(cc); r != cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_READY {
		t.Fatalf("tras el latido mudo el Edge quedó en %v, quiero READY: un silencio no borra lo aprendido", r)
	}
	srv.route(lane, cc, latidoConReadiness(cc.sessionID, cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_READY))
	if *n != 1 {
		t.Fatalf("calentamientos = %d, quiero 1: el silencio de en medio no puede fabricar un flanco", *n)
	}
}

// TestReadiness_ReinicioDelCloud_ElPrimerLatidoVistoCalienta es el criterio (c).
//
// El estado se APRENDE, no se pregunta: no hay tabla, no hay consulta al Edge y no
// hay barrendero. Un reinicio del Cloud llega con el mapa vacío y el Edge —que no se
// ha enterado de nada y sigue latiendo READY en cadencia— se lo vuelve a enseñar en
// su primer latido. Ese latido es un flanco (desde «no lo dice» hasta READY) y por
// tanto calienta: es la reposición del estado, y es gratis porque nadie espera detrás.
//
// 🔬 MUTACIÓN: hacer que el flanco exija venir de DOWN (`anterior == DOWN` en vez de
// `anterior != READY`) ⇒ rojo aquí, porque tras el reinicio el anterior es el cero.
func TestReadiness_ReinicioDelCloud_ElPrimerLatidoVistoCalienta(t *testing.T) {
	t.Parallel()
	cc := ccDePrueba("s-1")
	frame := latidoConReadiness(cc.sessionID, cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_READY)

	// Antes del reinicio: el Cloud ya sabía que este Edge podía.
	antes, nAntes := srvConCalentamientosContados(t)
	laneAntes := carrilDePrueba(t)
	defer cerrarCarril(laneAntes, func() {})
	antes.route(laneAntes, cc, frame)
	if *nAntes != 1 {
		t.Fatalf("calentamientos antes del reinicio = %d, quiero 1", *nAntes)
	}

	// El reinicio: proceso nuevo, memoria nueva. El Edge sigue mandando lo mismo.
	despues, nDespues := srvConCalentamientosContados(t)
	laneDespues := carrilDePrueba(t)
	defer cerrarCarril(laneDespues, func() {})
	if r := despues.readinessDelEdge(cc); r != cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_UNSPECIFIED {
		t.Fatalf("un Cloud recién arrancado ya sabía %v de un Edge al que no ha oído latir", r)
	}
	despues.route(laneDespues, cc, frame)
	if *nDespues != 1 {
		t.Fatalf("calentamientos tras el reinicio = %d, quiero 1: el primer latido visto tiene que "+
			"reponer el estado y calentar, sin preguntarle a nadie", *nDespues)
	}
}

// TestUntrackSession_OlvidaElReadinessSOLOConLaUltimaSesion custodia la trampa de la
// limpieza, que es donde este trabajo se rompe sin dar error.
//
// 🔴 EL DATO ES POR EDGE; EL UNTRACK ES POR SESIÓN. onStreamClosed se invoca UNA VEZ
// POR CADA SESIÓN del stream (closeStream itera `releases`), y un Edge multiplexa N
// sesiones sobre un stream (ADR-0008). Un `delete(s.edgeReadiness, k)` incondicional
// —el error natural— borraría en la primera iteración lo aprendido de un Edge que
// todavía tiene teléfonos vivos.
//
// 🔬 MUTACIÓN: sacar el `delete(s.edgeReadiness, k)` fuera del `if len(set) == 0` de
// untrackSession ⇒ rojo en la primera mitad.
func TestUntrackSession_OlvidaElReadinessSOLOConLaUltimaSesion(t *testing.T) {
	t.Parallel()
	srv, n := srvConCalentamientosContados(t)
	lane := carrilDePrueba(t)
	defer cerrarCarril(lane, func() {})

	// Un Edge (t-1/e-1) con DOS teléfonos sobre el mismo stream.
	uno, dos := ccDePrueba("s-1"), ccDePrueba("s-2")
	srv.trackSession(uno)
	srv.trackSession(dos)
	srv.route(lane, uno, latidoConReadiness(uno.sessionID, cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_READY))
	if *n != 1 {
		t.Fatalf("calentamientos = %d, quiero 1", *n)
	}

	// Se va UNO de los dos teléfonos. El Edge —y su Ollama— siguen ahí.
	srv.untrackSession(uno)
	if r := srv.readinessDelEdge(dos); r != cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_READY {
		t.Fatalf("al irse una de las DOS sesiones el Edge quedó en %v, quiero READY: el readiness es del "+
			"EDGE, no de la sesión, y borrarlo aquí fabricaría un flanco falso en el siguiente latido", r)
	}
	srv.route(lane, dos, latidoConReadiness(dos.sessionID, cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_READY))
	if *n != 1 {
		t.Fatalf("calentamientos = %d tras el latido de la sesión que queda, quiero 1: no hubo cambio", *n)
	}

	// Se va el último: ahora sí, el Edge entero se fue y no se recuerda nada de él.
	srv.untrackSession(dos)
	if r := srv.readinessDelEdge(dos); r != cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_UNSPECIFIED {
		t.Fatalf("el Edge se quedó sin sesiones y el readiness sobrevivió (%v): sería memoria que solo "+
			"crece, y le mentiría al disparador de compatibilidad cuando ese Edge reconecte", r)
	}
}
