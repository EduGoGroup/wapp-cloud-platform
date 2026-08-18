package gatewaygrpc_test

import (
	"context"
	"fmt"
	"math"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
)

// ============================================================================
// T5.2 · MEDIR EL «ANTES»: la latencia del Ack con N heartbeats en vuelo.
//
// QUÉ SE MIDE, y por qué no es lo que el nombre sugiere. El Ack NO es la causa
// del head-of-line, es su VÍCTIMA (ADR-0040 §Contexto.4): deliverAck
// (send.go) es un lock, un delete y un envío no bloqueante a un canal con
// buffer 1 — O(1) en memoria, cero Postgres. Lo que se mide aquí es el reloj de
// pared de un SendText —el método Go que llama el handler de
// POST /api/v1/messages, no una RPC— desde que empuja su comando hasta que su
// Ack vuelve, MIENTRAS el bucle Recv de ese mismo stream está ocupado
// procesando heartbeats (que sí pagan persistHealth + renewLease + Push
// inline, connect.go).
//
// El camino completo del Ack, verificado sobre af457c9:
//   1. SendText registra el canal en s.acks[cmdID] (send.go) y llama a
//      registry.Push, que escribe el CloudToEdge en el stream desde una
//      goroutine PROPIA (session/registry.go) — no desde el bucle Recv.
//   2. El Edge recibe el comando y responde con un EdgeToCloud_Ack cuyo
//      acked_command_id es el command_id del comando.
//   3. Ese Ack entra en la cola de recepción del stream DETRÁS de los
//      heartbeats que el Edge ya había mandado, y solo lo consume el bucle
//      Recv de Connect, que es ÚNICO por stream y va serial.
//   4. route → deliverAck → el canal → awaitAck retorna.
// El paso 3 es el head-of-line: los pasos 1, 2 y 4 son microsegundos.
//
// La forma del escenario la fija el ADR-0008 (N sesiones multiplexadas sobre UN
// stream por Edge): N sesiones latiendo a la vez es exactamente la ráfaga que
// un Edge sano produce cada 30 s, y es lo que T5.3 tendrá que reproducir.
//
// ⚠️ ESTA MEDIDA SUBESTIMA LA PRODUCCIÓN, a propósito y de forma declarada:
//   - heartbeatCarga no lleva self_pn, así que persistSelfPn y warnDeviceLimit
//     retornan antes (connect.go) y de las SEIS consultas que el ADR-0040
//     §Contexto.2 cuenta por heartbeat aquí solo se pagan CUATRO: SaveHealth
//     (la única con latencia inyectada) y las tres del lease.
//   - SlowRepository solo decora el FLEET: las tres consultas del lease
//     (TenantRevoked + Get + Upsert) van contra el Postgres local sin recargo,
//     cuando en producción pagan el mismo RTT que las demás.
// El «antes» real es PEOR que este número. Se mide así porque es el escenario
// que T5.3 tiene que repetir con los mismos parámetros, y la comparación exige
// que ambos lados usen el mismo arnés y el mismo heartbeatCarga.
// ============================================================================

// ============================================================================
// 🔴 INVERTIDO EL 2026-08-18 · Plan 050 · Ola 1 · DECISIÓN DE JHOAN.
//
// QUÉ CAMBIÓ Y POR QUÉ, para que nadie lea esto como que alguien aflojó un test
// para ponerlo verde. Este fichero nació midiendo el ANTES (T5.2) y sus
// aserciones AFIRMABAN QUE EL HEAD-OF-LINE EXISTE: exigían que el p50 del Ack
// quedara POR ENCIMA del suelo aritmético de la ráfaga y que el escenario
// cargado fuera al menos factorOcioso× más lento que el ocioso. Eso era correcto
// —y necesario— mientras el bucle Recv hacía el trabajo inline: sin esas dos
// afirmaciones, la tabla del ANTES no probaba de quién era la culpa.
//
// Con el carril de la Ola 1 cableado esas dos afirmaciones dejan de ser ciertas,
// y el test se pondría rojo PORQUE LA OLA FUNCIONA. Así que las aserciones se
// INVIERTEN al criterio de T5.3: de «el Ack queda detrás de la ráfaga» a «el Ack
// YA NO queda detrás de la ráfaga». La medición, el arnés, los escenarios y los
// parámetros NO se tocan: si cambiara cualquiera de ellos, la comparación
// antes/después no significaría nada.
//
// La tabla del ANTES se conserva ENTERA y literal, aquí abajo, en constantes
// (antesT52) y en el comentario que las acompaña. Nada se borra: es contra ella
// contra la que se compara el después, y sin ella la mejora sería una afirmación
// sin número detrás.
//
// ⚠️ CÓMO SE MIDE EL DESPUÉS, y esto no es negociable: **N=20 y SIN `-race`**.
// Con `-race` el escenario N=60 dejó de ser reproducible al medir el antes (su
// p50 caía a 963,7 ms frente a 1,62/1,65 s), así que comparar un antes sin
// `-race` contra un después con él inventaría la mejora. El gate que corre este
// fichero —`make test-integration`— usa `go test -p 1 ./...` sin `-race`, que es
// exactamente lo que hace falta. `make ci-docker` corre con `-race` pero NO ve
// la integración, así que no puede ensuciar esta medida.
// ============================================================================

const (
	// latenciaFleetT52 es la latencia inyectada a CADA consulta al fleet. 20 ms
	// no es un número redondo elegido al azar: es el orden del RTT que la
	// plataforma paga hoy contra un Postgres gestionado remoto (Neon), que es
	// donde corre de verdad — no contra el Postgres local del CI. Es además el
	// mismo valor que T5.1 fijó en latenciaPorConsulta, de modo que el escenario
	// de T5.2 y el arnés que lo sostiene hablan del mismo mundo.
	latenciaFleetT52 = 20 * time.Millisecond

	// sesionesT52 es cuántas sesiones se registran sobre el ÚNICO stream del
	// Edge (ADR-0008). Es el techo de N: cada escenario late con las primeras
	// esc.enVuelo de ellas, una sola vez por muestra, que es lo que hace un Edge
	// real con esas sesiones abiertas.
	sesionesT52 = 60

	// muestrasT52 es el tamaño de muestra de los escenarios que publican p99.
	// Por debajo de ~100 un p99 no significa nada (el percentil caería sobre la
	// única muestra máxima), así que 120 es el suelo con algo de holgura.
	muestrasT52 = 120

	// muestrasLinealidad es el tamaño de muestra del escenario de LINEALIDAD
	// (N grande). Es a propósito menor que muestrasT52: ese escenario no publica
	// un p99 defendible, solo comprueba que la latencia escala con N — que es lo
	// que demuestra que quien manda en la medida es el head-of-line y no otra
	// cosa.
	muestrasLinealidad = 40

	// destinoT52 y textoT52 son la carga del SendText medido. El destino es un
	// número inventado que NUNCA sale de este proceso: el Edge del arnés no
	// habla con WhatsApp, solo acusa.
	destinoT52 = "573000000000"
	textoT52   = "medicion t5.2"

	// factorOcioso es cuántas veces más lento tiene que ser el p50 del Ack CON
	// la ráfaga que el p99 del Ack con el bucle OCIOSO. Es la comprobación de
	// que el escenario satura de verdad: si no se cumple, N es demasiado bajo o
	// la latencia inyectada no basta, y el p99 publicado no probaría nada.
	//
	// ⚠️ 2026-08-18: el enunciado de arriba se conserva porque describe lo que
	// esta constante significaba mientras midió el ANTES. Invertido el test, ya
	// no se usa así: el 10 sigue siendo «un orden de magnitud», y de él se derivan
	// los dos umbrales del DESPUÉS (factorMejoraT53 y holguraOciosoT53).
	factorOcioso = 10

	// enVueloTitularT53 es el N del escenario TITULAR: el que publica el número
	// de T5.3 y el único con el que el criterio se declara cumplido. Está fijado
	// por la decisión del 2026-08-18 (medir el después con N=20 y sin `-race`),
	// no elegido aquí.
	enVueloTitularT53 = 20

	// factorMejoraT53 es el «orden de magnitud» literal del criterio de T5.3: el
	// p99 del después tiene que ser al menos factorMejoraT53 veces mejor que el
	// p99 del antes, con los MISMOS parámetros. Se escribe derivado de
	// factorOcioso a propósito: es el mismo 10, y que sea el mismo no es
	// casualidad sino la simetría del criterio (antes: 10× más lento que el
	// control; después: 10× mejor que el antes).
	factorMejoraT53 = factorOcioso

	// holguraOciosoT53 es cuánto se le tolera al escenario CARGADO por encima del
	// p99 del bucle ocioso medido EN LA MISMA CORRIDA. Antes de la Ola 1 el
	// cargado tenía que estar factorOcioso× POR ENCIMA del ocioso; después tiene
	// que quedarse DENTRO de factorOcioso² (100×) de él. No se aprieta más porque
	// el umbral estricto de verdad es el de factorMejoraT53: este es el control
	// —que la comparación se hace contra un bucle que sigue siendo rápido—, no la
	// medida.
	holguraOciosoT53 = factorOcioso * factorOcioso

	// pisoTechoOciosoT53 es el suelo absoluto de ese techo relativo. Un umbral
	// puramente relativo se vuelve inalcanzable en una máquina donde el ocioso
	// salga excepcionalmente rápido (100 × 40 µs = 4 ms no lo pasa nadie), y un
	// test de integración que depende de eso es flaky por construcción — la misma
	// lección que T5.1 ya se llevó con su chequeo del control. 10 ms sigue siendo
	// 55× mejor que el p50 del antes (557,2 ms): no perdona una regresión.
	pisoTechoOciosoT53 = 10 * time.Millisecond
)

// filaAntesT52 es una fila de la LÍNEA BASE: lo que este mismo escenario medía
// ANTES de la Ola 1. Se guarda como dato del test —y no solo en el plan— porque
// es el otro término de la comparación de T5.3: sin el antes en el código, el
// después es un número suelto.
type filaAntesT52 struct {
	enVuelo  int
	p50, p99 time.Duration
}

// antesT52 es LA TABLA DEL ANTES, medida el 2026-08-18 sobre el commit base de
// producción `af457c9` (arnés test-only `76e2aa6`). NO SE BORRA NI SE ACTUALIZA:
// si alguien vuelve a medir, mide el DESPUÉS, que es lo que produce este test en
// cada corrida.
//
// Condiciones exactas de la medida, que el escenario de este fichero reproduce
// tal cual: **20 ms inyectados por consulta al fleet** (latenciaFleetT52), lease
// sobre **POSTGRES** (3 consultas más por heartbeat), **60 sesiones sobre UN solo
// stream** (ADR-0008, sesionesT52), `ackTimeout` del servidor 8 s. Hardware:
// **Apple M1 Pro, 8 cores, 16 GB, macOS 27.0, Go 1.26.5 darwin/arm64, Postgres 16
// en Docker 29.2.0**. Sin `-race`.
//
//	| escenario                | n   | p50      | p95      | p99      | máx      | ventana muerta | K  |
//	|--------------------------|-----|----------|----------|----------|----------|----------------|----|
//	| N=0 · Recv ocioso        | 120 | 35 µs    | 77 µs    | 204 µs   | 233 µs   | 186 µs         | 0  |
//	| N=20 · TITULAR           | 120 | 557,2 ms | 601,4 ms | 619,6 ms | 668,2 ms | 667,6 ms       | 20 |
//	| N=60 · linealidad        | 40  | 1,621 s  | 1,743 s  | 1,762 s  | 1,762 s  | 1,761 s        | 60 |
//
// 🔴 Por qué estos números NO se pueden alcanzar por accidente en una máquina
// lenta ni fallar por accidente en una rápida: la latencia del antes está
// dominada por los 20 ms × N que SlowRepository duerme, y un `time.Sleep` cuesta
// lo mismo en cualquier hardware. El suelo del antes para N=20 es 400 ms de
// espera pura. Por eso un p99 de 62 ms (el techo que exige factorMejoraT53) es
// INALCANZABLE sin el carril, en cualquier máquina — que es justo lo que hace
// que este test se ponga rojo si la Ola 1 se revierte.
var antesT52 = []filaAntesT52{
	{enVuelo: 0, p50: 35 * time.Microsecond, p99: 204 * time.Microsecond},
	{enVuelo: enVueloTitularT53, p50: 557200 * time.Microsecond, p99: 619600 * time.Microsecond},
	{enVuelo: 60, p50: 1621 * time.Millisecond, p99: 1762 * time.Millisecond},
}

// antesDe devuelve la línea base del escenario con ese N, si la hay.
func antesDe(enVuelo int) (filaAntesT52, bool) {
	for _, f := range antesT52 {
		if f.enVuelo == enVuelo {
			return f, true
		}
	}
	return filaAntesT52{}, false
}

// edgeStream es el stream Connect visto desde el EDGE: manda EdgeToCloud y
// recibe CloudToEdge. Se declara como interfaz mínima (igual que edgeSender y
// edgeReceiver del arnés) para no atarse al nombre del tipo genérico que
// produce protoc-gen-go-grpc, pero aquí hacen falta las DOS mitades a la vez:
// el Edge de T5.2 no puede limitarse a drenar, tiene que CONTESTAR.
type edgeStream interface {
	Send(*cloudlinkv1.EdgeToCloud) error
	Recv() (*cloudlinkv1.CloudToEdge, error)
}

// edgeCliente es el Edge del escenario: un Edge SANO que lee su stream (lo que
// configArnes.drenarStream modela) y que además ACUSA los comandos que recibe,
// que es lo que drenar no hace — descarta los frames, y sin Ack no hay nada que
// medir.
//
// Por qué no se reutiliza loadHarness.drenar: son dos cosas distintas. drenar
// cuenta LeaseUpdate y tira el resto; este lector distingue el comando de envío
// del LeaseUpdate, responde con el command_id correcto y anota el instante en
// que el Ack sale al cable (ver ventana muerta). Sigue contando los LeaseUpdate
// en el MISMO contador del arnés (h.leaseUpdates), así que esperarLeases sirve
// igual como barrera y la vigilancia del lease del arnés no pierde su mitad que
// mira.
type edgeCliente struct {
	h      *loadHarness
	stream edgeStream

	// muSend serializa los Send del lado cliente. grpc-go permite un goroutine
	// mandando y otro recibiendo sobre el mismo stream, pero NO dos mandando: y
	// aquí mandan tres (el registro, la ráfaga de heartbeats y el lector cuando
	// contesta un Ack).
	muSend sync.Mutex

	mu sync.Mutex
	// ackEnCable anota, por command_id, el instante en que el Edge puso su Ack
	// en el cable. Es la mitad que el servidor no puede ver y sin la cual la
	// ventana muerta sería una estimación en vez de una medida.
	ackEnCable map[string]time.Time
	// ultimaRecepcion y mayorSilencio miden el hueco más largo entre dos frames
	// consecutivos recibidos por el Edge. Es la ventana muerta vista desde el
	// otro lado: con un LeaseUpdate por heartbeat, un hueco grande solo puede
	// significar que el bucle Recv del servidor estuvo atascado en un frame.
	ultimaRecepcion time.Time
	mayorSilencio   time.Duration
	// profundidadAlAcusar anota, por command_id, cuántos heartbeats llevaba
	// PROCESADOS el bucle Recv en el instante en que el Ack salió al cable.
	// Restándolo del contador al retornar el SendText sale K: cuántos heartbeats
	// se colaron POR DELANTE del Ack. Es la medida directa del head-of-line —sin
	// ella «el Ack esperó 560 ms» es un número sin mecanismo detrás— y es lo
	// único que distingue «la ráfaga entera por delante» de «media ráfaga».
	profundidadAlAcusar map[string]int64
	// errEnvio guarda el PRIMER error de envío del lector o de la ráfaga. No se
	// puede llamar a t.Fatalf desde una goroutine que no es la del test, así que
	// el error se guarda y el test lo consulta.
	errEnvio error

	// procesados lo incrementa el hook OnHeartbeat del Server, que corre DENTRO
	// del bucle Recv (route, connect.go) al empezar la rama Heartbeat. Atómico
	// porque lo escribe la goroutine del stream y lo lee la del test.
	procesados atomic.Int64
}

func nuevoEdgeCliente(h *loadHarness, stream edgeStream) *edgeCliente {
	return &edgeCliente{
		h:                   h,
		stream:              stream,
		ackEnCable:          make(map[string]time.Time),
		profundidadAlAcusar: make(map[string]int64),
	}
}

// enviar manda un frame al servidor serializando con los demás emisores.
func (e *edgeCliente) enviar(msg *cloudlinkv1.EdgeToCloud) error {
	e.muSend.Lock()
	defer e.muSend.Unlock()
	return e.stream.Send(msg)
}

// anotarError guarda el primer error visto por una goroutine que no es la del
// test.
func (e *edgeCliente) anotarError(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.errEnvio == nil {
		e.errEnvio = err
	}
}

// fallo devuelve el primer error anotado, si lo hubo.
func (e *edgeCliente) fallo() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.errEnvio
}

// arrancarLector pone al Edge a leer su stream. Termina solo cuando el cleanup
// del arnés cierra la conexión y Recv devuelve error.
func (e *edgeCliente) arrancarLector() {
	go func() {
		for {
			msg, err := e.stream.Recv()
			if err != nil {
				return // cierre del arnés: no es un fallo del escenario.
			}
			e.anotarRecepcion(time.Now())
			if msg.GetLeaseUpdate() != nil {
				e.h.leaseUpdates.Add(1)
			}
			if msg.GetSendText() == nil {
				continue
			}
			e.acusar(msg)
		}
	}()
}

// acusar responde el Ack de un comando de envío con su command_id, anotando
// ANTES el instante en que sale al cable. Se anota antes y no después a
// propósito: el servidor puede entregar el Ack y devolver el SendText mientras
// esta goroutine sigue dentro de Send, y entonces el test leería un mapa vacío.
// El precio es que la ventana muerta incluye el propio Send del cliente
// (microsegundos sobre bufconn), lo que la hace CONSERVADORA, nunca optimista.
func (e *edgeCliente) acusar(msg *cloudlinkv1.CloudToEdge) {
	cmdID := msg.GetCommandId()
	e.mu.Lock()
	e.ackEnCable[cmdID] = time.Now()
	e.profundidadAlAcusar[cmdID] = e.procesados.Load()
	e.mu.Unlock()

	ack := &cloudlinkv1.EdgeToCloud{
		SessionId: msg.GetSessionId(),
		Payload: &cloudlinkv1.EdgeToCloud_Ack{
			Ack: &cloudlinkv1.Ack{AckedCommandId: cmdID, Ok: true},
		},
	}
	if err := e.enviar(ack); err != nil {
		e.anotarError(fmt.Errorf("el Edge no pudo acusar %s: %w", cmdID, err))
	}
}

// anotarRecepcion actualiza el mayor silencio observado por el Edge.
func (e *edgeCliente) anotarRecepcion(ahora time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.ultimaRecepcion.IsZero() {
		if hueco := ahora.Sub(e.ultimaRecepcion); hueco > e.mayorSilencio {
			e.mayorSilencio = hueco
		}
	}
	e.ultimaRecepcion = ahora
}

// reiniciarSilencio pone a cero el medidor de silencio: cada escenario mide el
// suyo. Sin esto, el escenario ocioso (donde el Edge pasa segundos sin recibir
// nada porque nadie late) contaminaría el máximo global con un hueco que no
// tiene nada que ver con un bucle atascado.
func (e *edgeCliente) reiniciarSilencio() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.mayorSilencio = 0
	e.ultimaRecepcion = time.Time{}
}

// silencio devuelve el mayor hueco entre recepciones del escenario en curso.
func (e *edgeCliente) silencio() time.Duration {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.mayorSilencio
}

// puestaEnCable devuelve cuándo salió al cable el Ack del command_id dado y
// cuántos heartbeats llevaba procesados el bucle Recv en ese instante.
func (e *edgeCliente) puestaEnCable(cmdID string) (salida time.Time, procesados int64, ok bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	salida, ok = e.ackEnCable[cmdID]
	return salida, e.profundidadAlAcusar[cmdID], ok
}

// escenarioT52 es una fila de la tabla: N heartbeats en vuelo y cuántas veces se
// mide.
type escenarioT52 struct {
	nombre   string
	enVuelo  int
	muestras int
}

// resultadoT52 es lo que se publica de cada escenario.
type resultadoT52 struct {
	esc               escenarioT52
	n                 int
	p50, p95, p99     time.Duration
	maximo            time.Duration
	ventanaMuerta     time.Duration
	silencioCliente   time.Duration
	fallos            int
	costePorHeartbeat time.Duration
	// orden es la muestra ORDENADA. Se conserva para publicar los deciles: los
	// percentiles solos no distinguen una distribución apretada de una BIMODAL,
	// y esa diferencia decide si el p50 de un escenario es reproducible o no.
	orden []time.Duration
	// profundidades es, ORDENADA, la K de cada muestra: cuántos heartbeats se
	// procesaron por delante del Ack mientras este esperaba en la cola.
	profundidades []int64
}

// escenariosT52: el bucle OCIOSO es el control (sin él, un p99 alto no prueba
// que la culpa sea de la ráfaga), N=20 es el escenario titular y N=60 comprueba
// que la latencia ESCALA con N, que es la firma del head-of-line.
var escenariosT52 = []escenarioT52{
	{nombre: "N=0 · bucle Recv ocioso (control)", enVuelo: 0, muestras: muestrasT52},
	{nombre: "N=20 · 20 sesiones latiendo (TITULAR)", enVuelo: enVueloTitularT53, muestras: muestrasT52},
	{nombre: "N=60 · linealidad", enVuelo: 60, muestras: muestrasLinealidad},
}

// percentil devuelve el percentil q (0..1) por rango más próximo sobre una
// muestra YA ORDENADA. Sin interpolación a propósito: con n=120 la
// interpolación solo maquilla, y el valor devuelto es siempre una medida real y
// no un promedio de dos.
func percentil(orden []time.Duration, q float64) time.Duration {
	if len(orden) == 0 {
		return 0
	}
	i := int(math.Ceil(q*float64(len(orden)))) - 1
	if i < 0 {
		i = 0
	}
	if i >= len(orden) {
		i = len(orden) - 1
	}
	return orden[i]
}

// deciles devuelve d10, d20 … d90 de una muestra ya ordenada. Es lo que delata
// una distribución BIMODAL: si el Ack unas veces cae detrás de la ráfaga entera
// y otras a mitad de ella, los deciles saltan en dos mesetas y el p50 deja de
// ser reproducible aunque el p95 lo sea.
func deciles(orden []time.Duration) []time.Duration {
	out := make([]time.Duration, 0, 9)
	for q := 1; q <= 9; q++ {
		out = append(out, percentil(orden, float64(q)/10).Round(time.Millisecond))
	}
	return out
}

// kPercentil es percentil sobre la muestra de K (ya ordenada).
func kPercentil(orden []int64, q float64) int64 {
	if len(orden) == 0 {
		return 0
	}
	i := int(math.Ceil(q*float64(len(orden)))) - 1
	if i < 0 {
		i = 0
	}
	if i >= len(orden) {
		i = len(orden) - 1
	}
	return orden[i]
}

// registrarSesionesT52 abre las n sesiones sobre el ÚNICO stream del Edge y
// espera a que TODAS estén registradas (2 LeaseUpdate por sesión: el inicial de
// onSessionRegistered y la renovación del heartbeat de registro) y a que la
// última haya llegado a Postgres. Se hace con latencia 0 para que el coste del
// registro no entre en ninguna medida.
func registrarSesionesT52(ctx context.Context, t *testing.T, h *loadHarness, edge *edgeCliente, n int) []string {
	t.Helper()
	sesiones := make([]string, n)
	for i := range sesiones {
		sesiones[i] = sessionIDUnico(fmt.Sprintf("t52-%03d", i))
		if err := edge.enviar(heartbeatCarga(sesiones[i], 1)); err != nil {
			t.Fatalf("registrando la sesión %d: %v", i, err)
		}
	}
	h.esperarLeases(t, int64(2*n))
	h.esperarUptime(ctx, t, sesiones[n-1], 1)
	return sesiones
}

// medicionT52 es el estado que las muestras van acumulando de escenario en
// escenario: la ronda (marcador de secuencia de los heartbeats) y el total de
// LeaseUpdate esperados, que es la barrera de drenaje entre muestras.
type medicionT52 struct {
	ronda  int64
	leases int64
}

// muestraT52 toma UNA muestra: dispara la ráfaga de N heartbeats y, EN
// PARALELO, el SendText cuyo Ack se cronometra. El paralelismo no es un adorno:
// si la ráfaga se esperase entera antes del envío, el número de heartbeats por
// delante del Ack sería una constante impuesta por el test, y lo que se quiere
// medir es justamente dónde cae el Ack cuando las dos cosas ocurren a la vez.
func muestraT52(ctx context.Context, t *testing.T, h *loadHarness, edge *edgeCliente,
	sesiones []string, enVuelo int, m *medicionT52) (lat, ventana time.Duration, prof int64, err error) {
	t.Helper()

	m.ronda++
	var wg sync.WaitGroup
	if enVuelo > 0 {
		wg.Add(1)
		go func(r int64) {
			defer wg.Done()
			for j := 0; j < enVuelo; j++ {
				if serr := edge.enviar(heartbeatCarga(sesiones[j], r)); serr != nil {
					edge.anotarError(fmt.Errorf("ráfaga de heartbeats: %w", serr))
					return
				}
			}
		}(m.ronda)
	}

	inicio := time.Now()
	ack, serr := h.srv.SendText(ctx, sesiones[0], destinoT52, textoT52)
	fin := time.Now()
	wg.Wait()

	if enVuelo > 0 {
		// Barrera de drenaje: la siguiente muestra no puede empezar con la
		// ráfaga anterior a medio procesar, o mediría una cola acumulada en vez
		// de N. El LeaseUpdate se empuja al FINAL de la rama Heartbeat
		// (route → renewLease → Push), así que verlo en el cliente es la señal
		// de que ese heartbeat terminó su viaje entero.
		//
		// ⚠️ 2026-08-18 — esta barrera es además lo que mantiene la cuenta EXACTA
		// con el carril de la Ola 1. La coalescencia de heartbeats (D-050.4) es
		// POR SESIÓN, y aquí cada ráfaga manda UN heartbeat a cada una de N
		// sesiones DISTINTAS: dentro de una misma ráfaga no hay nada que
		// coalescer. Entre rondas tampoco, porque esta barrera espera a que los N
		// LeaseUpdate hayan llegado antes de mandar la siguiente. Si alguien
		// quitara la barrera, dos rondas podrían solaparse en la cola de una
		// sesión, el latido viejo sería sustituido y `esperarLeases` esperaría un
		// LeaseUpdate que ya nadie va a empujar: el test moriría por timeout, no
		// en silencio.
		m.leases += int64(enVuelo)
		h.esperarLeases(t, m.leases)
	}
	if serr != nil {
		return fin.Sub(inicio), 0, 0, serr
	}
	if puesta, procesadosAlAcusar, ok := edge.puestaEnCable(ack.GetAckedCommandId()); ok {
		ventana = fin.Sub(puesta)
		prof = edge.procesados.Load() - procesadosAlAcusar
	}
	return fin.Sub(inicio), ventana, prof, nil
}

// medirEscenario corre las muestras de un escenario y devuelve su fila de la
// tabla.
func medirEscenario(ctx context.Context, t *testing.T, h *loadHarness, edge *edgeCliente,
	sesiones []string, esc escenarioT52, m *medicionT52) resultadoT52 {
	t.Helper()

	edge.reiniciarSilencio()
	latencias := make([]time.Duration, 0, esc.muestras)
	profundidades := make([]int64, 0, esc.muestras)
	res := resultadoT52{esc: esc}
	var suma time.Duration

	for i := 0; i < esc.muestras; i++ {
		lat, ventana, prof, err := muestraT52(ctx, t, h, edge, sesiones, esc.enVuelo, m)
		if err != nil {
			// Un SendText que se rinde es una muestra CENSURADA en el reloj del
			// ackTimeout (8 s), no un dato: entra en el recuento de fallos y no
			// en los percentiles, que si no quedarían truncados sin decirlo.
			res.fallos++
			t.Logf("muestra %d de %q: SendText falló tras %v: %v", i, esc.nombre, lat, err)
			if res.fallos > esc.muestras/10 {
				t.Logf("%q: más del 10%% de las muestras se rindieron; se corta el escenario", esc.nombre)
				break
			}
			continue
		}
		latencias = append(latencias, lat)
		profundidades = append(profundidades, prof)
		suma += lat
		if ventana > res.ventanaMuerta {
			res.ventanaMuerta = ventana
		}
	}

	slices.Sort(latencias)
	res.orden = latencias
	res.n = len(latencias)
	res.p50 = percentil(latencias, 0.50)
	res.p95 = percentil(latencias, 0.95)
	res.p99 = percentil(latencias, 0.99)
	if len(latencias) > 0 {
		res.maximo = latencias[len(latencias)-1]
		if esc.enVuelo > 0 {
			// Coste efectivo por heartbeat del bucle Recv: lo que tarda el Ack en
			// atravesar la cola, repartido entre los frames que tenía delante.
			res.costePorHeartbeat = time.Duration(int64(suma) / int64(len(latencias)) / int64(esc.enVuelo))
		}
	}
	res.silencioCliente = edge.silencio()
	slices.Sort(profundidades)
	res.profundidades = profundidades
	return res
}

// TestIntegration_CargaAckBajoHeartbeatsEnVuelo mide y PUBLICA la latencia del
// Ack. Nació siendo T5.2 (el ANTES) y desde el 2026-08-18 es también el gate de
// T5.3 (el DESPUÉS): mismo arnés, mismos parámetros, aserciones invertidas. El
// nombre se conserva a propósito —renombrarlo rompería la trazabilidad con la
// tabla publicada del antes— y por eso su función de comprobación sigue
// llamándose comprobarTablaT52.
//
// No es solo un impresor de números: afirma las tres cosas sin las cuales la
// mejora no probaría nada (el enunciado de las dos primeras ANTES de invertirse
// se conserva literal en comprobarEscenarioCargadoT53).
//  1. Que la ráfaga YA NO DOMINA: el p50 del Ack con N heartbeats en vuelo cae
//     por debajo de la mitad del suelo aritmético (N × latencia inyectada). Con
//     el bucle Recv haciendo el trabajo inline esto es imposible de cumplir.
//  2. Que el p99 es al menos un orden de magnitud mejor que el de la línea base
//     (antesT52), que es el criterio literal de T5.3.
//  3. Que el bucle OCIOSO sigue siendo el control: el escenario cargado se queda
//     ahora DENTRO de su orden de magnitud, en vez de por encima. Sin este
//     control, una mejora podría ser de la máquina y no del carril.
func TestIntegration_CargaAckBajoHeartbeatsEnVuelo(t *testing.T) {
	// drenarStream queda en false porque el drenaje lo pone este test: su lector
	// es un SUPERCONJUNTO de drenar (lee todo, cuenta los mismos LeaseUpdate en
	// h.leaseUpdates y además acusa). Encender los dos pondría dos goroutines a
	// hacer Recv sobre el mismo stream, que es justo lo que no se puede hacer.
	// leasePostgres SÍ va encendido: sin él, cada heartbeat se ahorraría las tres
	// consultas del lease y la línea base saldría más bonita de lo que es.
	h := newLoadHarness(t, configArnes{leasePostgres: true})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	stream, err := h.client.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	edge := nuevoEdgeCliente(h, stream)
	// El hook corre DENTRO del bucle Recv (route, connect.go) al empezar la rama
	// Heartbeat, así que su contador es el reloj del propio bucle: con él se mide
	// K, la profundidad de cola por delante del Ack. Se asigna antes de mandar el
	// primer frame —el patrón de la casa (server_test.go con OnIncoming)—: hasta
	// que llega un heartbeat nadie lo lee, y el camino que lo lee está sincronizado
	// con esta escritura por el propio transporte.
	h.srv.OnHeartbeat = func(string, *cloudlinkv1.Heartbeat) { edge.procesados.Add(1) }
	edge.arrancarLector()

	sesiones := registrarSesionesT52(ctx, t, h, edge, sesionesT52)

	// A partir de aquí cada consulta al fleet paga la latencia del parámetro. El
	// registro quedó fuera.
	h.slow.SetDelay(latenciaFleetT52)

	m := &medicionT52{leases: int64(2 * len(sesiones))}
	tabla := make([]resultadoT52, 0, len(escenariosT52))
	for _, esc := range escenariosT52 {
		tabla = append(tabla, medirEscenario(ctx, t, h, edge, sesiones, esc, m))
	}

	if ferr := edge.fallo(); ferr != nil {
		t.Fatalf("el Edge del arnés falló durante la medición: %v", ferr)
	}

	publicarTablaT52(t, tabla)
	comprobarTablaT52(t, tabla)
}

// publicarTablaT52 escribe la tabla que pide el criterio de T5.2. Va por t.Logf
// (visible con -v) porque el entregable es el número, y el número tiene que
// quedar en la salida del gate, no en un fichero que alguien tenga que creerse.
func publicarTablaT52(t *testing.T, tabla []resultadoT52) {
	t.Helper()
	t.Logf("========= T5.3 · latencia del Ack DESPUÉS de la Ola 1 del Plan 050 =========")
	t.Logf("(este encabezado decía «T5.2 · latencia del Ack ANTES del Plan 050» hasta el 2026-08-18: " +
		"el arnés es EL MISMO, lo que cambió es el código que mide y el lado de las aserciones)")
	t.Logf("línea base del ANTES: 76e2aa6 (arnés test-only) sobre el commit base de producción af457c9")
	t.Logf("el DESPUÉS lo produce esta corrida · válido SOLO sin -race, con el titular N=%d",
		enVueloTitularT53)
	t.Logf("latencia inyectada: %v por consulta al fleet · lease sobre POSTGRES (3 consultas/heartbeat)",
		latenciaFleetT52)
	t.Logf("sesiones registradas sobre UN stream (ADR-0008): %d · ackTimeout del servidor: 8s",
		sesionesT52)
	t.Logf("%-36s %5s %10s %10s %10s %10s %12s %12s %9s %7s",
		"escenario", "n", "p50", "p95", "p99", "max", "ventana", "silencio", "coste/hb", "fallos")
	for _, r := range tabla {
		t.Logf("%-36s %5d %10v %10v %10v %10v %12v %12v %9v %7d",
			r.esc.nombre, r.n, r.p50.Round(time.Microsecond), r.p95.Round(time.Microsecond),
			r.p99.Round(time.Microsecond), r.maximo.Round(time.Microsecond),
			r.ventanaMuerta.Round(time.Microsecond), r.silencioCliente.Round(time.Microsecond),
			r.costePorHeartbeat.Round(time.Microsecond), r.fallos)
	}
	for _, r := range tabla {
		t.Logf("deciles de %-34s %v", r.esc.nombre, deciles(r.orden))
	}
	for _, r := range tabla {
		t.Logf("K (heartbeats por delante del Ack) de %-34s d10=%d d50=%d d90=%d max=%d",
			r.esc.nombre, kPercentil(r.profundidades, 0.10), kPercentil(r.profundidades, 0.50),
			kPercentil(r.profundidades, 0.90), kPercentil(r.profundidades, 1))
	}
	// La comparación cara a cara, que es el entregable de T5.3. Se publica aunque
	// el test pase: el criterio del plan pide el NÚMERO, no un verde.
	t.Logf("---- ANTES (2026-08-18 · af457c9) vs DESPUÉS (esta corrida · Ola 1) ----")
	for _, r := range tabla {
		antes, ok := antesDe(r.esc.enVuelo)
		if !ok {
			t.Logf("%-36s sin línea base del ANTES para N=%d", r.esc.nombre, r.esc.enVuelo)
			continue
		}
		t.Logf("%-36s p50 %v → %v · p99 %v → %v (techo T5.3 del p99: %v)",
			r.esc.nombre,
			antes.p50.Round(time.Microsecond), r.p50.Round(time.Microsecond),
			antes.p99.Round(time.Microsecond), r.p99.Round(time.Microsecond),
			(antes.p99 / factorMejoraT53).Round(time.Microsecond))
	}
	t.Logf("ventana  = lo que el Ack, YA en el cable, esperó a que el bucle Recv lo consumiera")
	t.Logf("silencio = mayor hueco entre dos frames consecutivos recibidos por el Edge")
	t.Logf("===========================================================================")
}

// comprobarTablaT52 afirma lo que hace publicable la tabla.
//
// 🔴 INVERTIDA EL 2026-08-18 (Plan 050 · Ola 1, decisión de Jhoan). Hasta hoy
// afirmaba que el head-of-line EXISTE —p50 ≥ suelo, y cargado al menos
// factorOcioso× más lento que el ocioso—, que es lo que T5.2 tenía que demostrar
// para que su tabla del ANTES significara algo. Con el carril cableado eso deja
// de ser cierto y esas aserciones se pondrían rojas PORQUE LA OLA FUNCIONA. Lo
// que se afirma ahora es el criterio de T5.3, ni más ni menos. El enunciado
// original de las dos aserciones se conserva literal en cada una.
//
// Las funciones están partidas (y no en `t.Run` con closures inline) porque un
// solo cuerpo con las tres comprobaciones y sus bucles se pasaría del umbral de
// gocyclo (15) del `.golangci.yml`, y los `t.Run` inline no lo bajan.
func comprobarTablaT52(t *testing.T, tabla []resultadoT52) {
	t.Helper()
	ocioso := controlT53(t, tabla)
	for i := range tabla {
		r := &tabla[i]
		if r.esc.enVuelo == 0 {
			continue
		}
		comprobarEscenarioCargadoT53(t, r, ocioso)
	}
}

// controlT53 exige que todos los escenarios dejaran muestra y devuelve el de
// CONTROL (N=0). El control sobrevive intacto a la inversión: sin un bucle
// ocioso con el que comparar, ni el antes probaba de quién era la culpa ni el
// después prueba que la mejora sea del carril y no de la máquina.
func controlT53(t *testing.T, tabla []resultadoT52) *resultadoT52 {
	t.Helper()
	var ocioso *resultadoT52
	for i := range tabla {
		r := &tabla[i]
		if r.n == 0 {
			t.Fatalf("el escenario %q no dejó ni una muestra válida (%d fallos)", r.esc.nombre, r.fallos)
		}
		if r.esc.enVuelo == 0 {
			ocioso = r
		}
	}
	if ocioso == nil {
		t.Fatal("falta el escenario de control (N=0): sin él la tabla no dice de quién es la culpa")
	}
	return ocioso
}

// comprobarEscenarioCargadoT53 aplica al escenario con ráfaga las TRES
// afirmaciones del después. Cada una lleva, literal, lo que afirmaba antes de la
// inversión del 2026-08-18.
func comprobarEscenarioCargadoT53(t *testing.T, r, ocioso *resultadoT52) {
	t.Helper()

	// (1) INVERTIDA. Antes afirmaba: «La ráfaga manda: el Ack se cuela DETRÁS de
	// ella» y exigía p50 ≥ suelo. Ahora afirma lo contrario: el Ack YA NO queda
	// detrás de la ráfaga, así que su p50 tiene que caer POR DEBAJO de ese mismo
	// suelo. El umbral no se ha movido ni un milisegundo —sigue siendo la mitad
	// del suelo aritmético N × latencia inyectada—: lo único que cambia es el
	// lado de la desigualdad, que es exactamente lo que la Ola 1 promete.
	suelo := time.Duration(r.esc.enVuelo) * latenciaFleetT52 / 2
	if r.p50 >= suelo {
		t.Errorf("%q: p50 del Ack = %v, en o por encima de %v (mitad de %d × %v). El Ack SIGUE "+
			"quedando detrás de la ráfaga: el carril de la Ola 1 no está soltando el trabajo del bucle Recv "+
			"(antes de la ola este mismo escenario daba %v de p50)",
			r.esc.nombre, r.p50, suelo, r.esc.enVuelo, latenciaFleetT52, antesP50De(r.esc.enVuelo))
	}

	// (2) EL CRITERIO LITERAL DE T5.3: el p99 del después es al menos un orden de
	// magnitud mejor que el del antes, con los mismos parámetros. Es la aserción
	// que se pone roja si se revierte la Ola 1, y no puede pasar por accidente en
	// una máquina rápida: el antes está dominado por los N × 20 ms que
	// SlowRepository DUERME, y dormir cuesta lo mismo en cualquier hardware.
	antes, ok := antesDe(r.esc.enVuelo)
	if !ok {
		t.Errorf("%q: no hay línea base del ANTES para N=%d en antesT52, así que no hay con qué comparar "+
			"el p99 medido (%v). O se añade la fila del antes, o el escenario no puede publicar mejora",
			r.esc.nombre, r.esc.enVuelo, r.p99)
		return
	}
	if techo := antes.p99 / factorMejoraT53; r.p99 > techo {
		t.Errorf("%q: p99 del Ack = %v, por encima de %v. T5.3 exige que el DESPUÉS sea al menos %d× "+
			"mejor que el ANTES (p99 del antes = %v, medido el 2026-08-18 sobre af457c9 con los MISMOS "+
			"parámetros: %d sesiones en vuelo, %v inyectados por consulta al fleet, lease sobre Postgres). "+
			"Recuerda que este número solo vale medido SIN -race",
			r.esc.nombre, r.p99, techo, factorMejoraT53, antes.p99, r.esc.enVuelo, latenciaFleetT52)
	}

	// (3) INVERTIDA. Antes afirmaba: «El control: con el bucle vacío el MISMO Ack
	// vuelve órdenes de magnitud antes», y exigía que el cargado fuera al menos
	// factorOcioso× MÁS LENTO que el ocioso. Ahora exige lo contrario: que el
	// cargado se quede DENTRO de factorOcioso² del ocioso de la MISMA corrida. Es
	// el control, no la medida —el umbral duro es el (2)—, y por eso lleva un piso
	// absoluto: un techo puramente relativo se vuelve inalcanzable si el ocioso
	// sale excepcionalmente rápido, y un test de integración que dependa de eso es
	// flaky por construcción.
	techoOcioso := ocioso.p99 * holguraOciosoT53
	if techoOcioso < pisoTechoOciosoT53 {
		techoOcioso = pisoTechoOciosoT53
	}
	if r.p50 > techoOcioso {
		t.Errorf("%q: p50 = %v y el bucle ocioso tiene p99 = %v; tras la Ola 1 el escenario cargado debía "+
			"quedarse por debajo de %v (%d× el ocioso, con piso de %v). Que siga lejos del ocioso significa que "+
			"la ráfaga TODAVÍA pesa sobre el camino del Ack",
			r.esc.nombre, r.p50, ocioso.p99, techoOcioso, holguraOciosoT53, pisoTechoOciosoT53)
	}
}

// antesP50De devuelve el p50 de la línea base de ese N, o 0 si no la hay. Existe
// para que el mensaje de fallo de la aserción (1) pueda decir contra qué número
// se está comparando sin repetir la búsqueda ni complicar la función.
func antesP50De(enVuelo int) time.Duration {
	f, ok := antesDe(enVuelo)
	if !ok {
		return 0
	}
	return f.p50
}
