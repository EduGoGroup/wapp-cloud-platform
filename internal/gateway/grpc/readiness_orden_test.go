package gatewaygrpc_test

import (
	"context"
	"sync"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"google.golang.org/grpc"
)

// readiness_orden_test.go — EL CRITERIO (b) DE T1.8-6, Y POR QUÉ NO SE PUEDE PROBAR
// DESDE DENTRO (Plan 044 · Ola 1.8, cierra DEUDA-044.7).
//
// 🔴 LO QUE SE CUSTODIA AQUÍ ES UN ORDEN, no una función. El bucle Recv de Connect
// registra la sesión al ver un session_id nuevo y DESPUÉS encamina el frame; y el
// frame que provoca ese registro es, en el caso normal, EL PRIMER LATIDO DE LA
// SESIÓN. Es decir: hasta T1.8-6 el gateway calentaba antes de haber leído lo que ese
// Edge decía sobre su capacidad de inferencia, así que un Edge que arrancaba sin
// cajero recibía igualmente un calentamiento que solo podía volver como OLLAMA_DOWN.
//
// La corrección fue mover el disparador de compatibilidad DETRÁS de route y
// condicionarlo a que el Edge no haya dicho nada. Las dos mitades hacen falta y
// ninguna se ve desde un test de unidad: un test que llamara a `route` y luego a
// `calientaPorRegistro` estaría afirmando la composición que YO escribí, no la que
// corre. Por eso estos tres entran por el Connect de verdad, sobre mTLS y bufconn.
//
// EL SECUENCIADOR, que es lo que hace que estos tests no sean flojos: `OnHeartbeat`
// se invoca INLINE al principio del case del Heartbeat, y el bucle Recv es UNA sola
// goroutine. Por tanto, ver el latido del frame N+1 demuestra que la iteración
// completa del frame N terminó — disparador de compatibilidad incluido. Sin ese
// apoyo, «cero calentamientos» solo podría comprobarse durmiendo, que es la clase de
// test que falla un martes por la tarde en la máquina de otro.

// calentamientosContados cuenta las llamadas a OnWarmup. Aquí SÍ lleva mutex (a
// diferencia del contador de readiness_internal_test.go): quien las hace es la
// goroutine del stream, no la del test.
type calentamientosContados struct {
	mu sync.Mutex
	n  int
}

func (c *calentamientosContados) suma() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
}

func (c *calentamientosContados) valor() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// esperaCalentamientos espera a que el contador llegue a n. Solo se usa para esperar
// algo que DEBE ocurrir; la ausencia nunca se comprueba con un reloj, se comprueba con
// el secuenciador de latidos.
func esperaCalentamientos(t *testing.T, c *calentamientosContados, n int) {
	t.Helper()
	limite := time.Now().Add(3 * time.Second)
	for time.Now().Before(limite) {
		if c.valor() >= n {
			return
		}
		time.Sleep(3 * time.Millisecond)
	}
	t.Fatalf("timeout esperando %d calentamiento(s); llegaron %d", n, c.valor())
}

// bancoDeReadiness levanta el harness mTLS con OnWarmup contado y OnHeartbeat como
// secuenciador, y abre el stream.
type bancoDeReadiness struct {
	cal     *calentamientosContados
	latidos chan struct{}
	stream  grpc.BidiStreamingClient[cloudlinkv1.EdgeToCloud, cloudlinkv1.CloudToEdge]
}

// siguienteLatido bloquea hasta que el servidor ha EMPEZADO a encaminar un latido más.
func (b *bancoDeReadiness) siguienteLatido(t *testing.T) {
	t.Helper()
	select {
	case <-b.latidos:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout esperando que el servidor encaminara el siguiente latido")
	}
}

func nuevoBancoDeReadiness(t *testing.T) *bancoDeReadiness {
	t.Helper()
	ca := newDevCA(t)
	h := newMTLSHarness(t, ca, issueEdgeCert(t, ca, testTenantID, testEdgeID))

	cal := &calentamientosContados{}
	latidos := make(chan struct{}, 16)
	// Se asignan ANTES de abrir el stream: no hay ninguna goroutine de Connect todavía
	// que pueda leerlos (el contrato del Server lo dice: los hooks se asignan antes de
	// poner el servidor a servir).
	h.srv.OnWarmup = func(_, _, _, _ string) { cal.suma() }
	h.srv.OnHeartbeat = func(string, *cloudlinkv1.Heartbeat) { latidos <- struct{}{} }

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	stream, err := h.client.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return &bancoDeReadiness{cal: cal, latidos: latidos, stream: stream}
}

// late manda un latido con el readiness indicado.
func (b *bancoDeReadiness) late(t *testing.T, counter int64, r cloudlinkv1.InferenceReadiness) {
	t.Helper()
	err := b.stream.Send(&cloudlinkv1.EdgeToCloud{
		SessionId: "s1",
		Payload: &cloudlinkv1.EdgeToCloud_Heartbeat{Heartbeat: &cloudlinkv1.Heartbeat{
			LeaseCounter:       counter,
			InferenceReadiness: r,
		}},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
}

// TestReadinessOrden_PrimerLatidoSinDecirNada_ElRegistroCalientaComoSiempre es la
// primera mitad del criterio (b): la COMPATIBILIDAD sigue viva.
//
// Un Edge que todavía no publica `inference_readiness` manda el cero en todos sus
// latidos. El cero significa «este Edge no lo dice», así que el disparador del
// registro tiene que actuar exactamente como antes de esta tarea: si no actuara, toda
// la flota vieja dejaría de calentarse sin producir un solo error — la forma más cara
// de fallar.
//
// 🔬 MUTACIÓN (criterio d): leer el cero como DOWN en anotaReadiness ⇒ rojo aquí, y en
// ningún otro sitio.
func TestReadinessOrden_PrimerLatidoSinDecirNada_ElRegistroCalientaComoSiempre(t *testing.T) {
	t.Parallel()
	b := nuevoBancoDeReadiness(t)

	b.late(t, 1, cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_UNSPECIFIED)
	esperaCalentamientos(t, b.cal, 1)

	// Y NO se repite en cada latido: el disparador es del REGISTRO, y la sesión ya
	// está registrada. El segundo latido secuencia la iteración del primero.
	b.late(t, 2, cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_UNSPECIFIED)
	b.siguienteLatido(t)
	b.siguienteLatido(t)
	if got := b.cal.valor(); got != 1 {
		t.Fatalf("calentamientos = %d, quiero 1: un Edge mudo se calienta al registrarse y no en cada latido", got)
	}
}

// TestReadinessOrden_PrimerLatidoDOWN_CeroCalentamientosHastaElREADY es la segunda
// mitad del criterio (b), y es LA TAREA: el defecto que cierra DEUDA-044.7.
//
// 🔴 ESTE TEST ESTABA ROJO ANTES DE MOVER EL DISPARADOR, y no por la lógica de
// readiness sino por el ORDEN: onSessionRegistered corría ANTES de route, así que el
// calentamiento salía antes de leer el DOWN que venía en ese mismísimo frame. Con el
// disparador donde estaba, ninguna condición sobre el readiness podía salvarlo: no
// había nada aprendido todavía que consultar.
//
// 🔬 MUTACIÓN (criterio d): devolver `s.calientaPorRegistro(frameCC)` a dentro de
// onSessionRegistered (o sea, delante de route) ⇒ rojo en la primera mitad.
func TestReadinessOrden_PrimerLatidoDOWN_CeroCalentamientosHastaElREADY(t *testing.T) {
	t.Parallel()
	b := nuevoBancoDeReadiness(t)

	// El Edge arranca con el cajero caído y lo DICE desde el primer latido, que es el
	// mismo frame que provoca el registro de la sesión.
	b.late(t, 1, cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_DOWN)
	b.late(t, 2, cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_DOWN)
	// Ver el segundo latido demuestra que la iteración del primero terminó ENTERA.
	b.siguienteLatido(t)
	b.siguienteLatido(t)
	if got := b.cal.valor(); got != 0 {
		t.Fatalf("calentamientos = %d con un Edge que DICE que no puede, quiero 0: cada uno de ellos es "+
			"un trabajo que solo puede volver como OLLAMA_DOWN, y nadie lo reintenta", got)
	}

	// El cajero levanta. AHORA sí, y es el evento —no un reloj— quien lo dispara.
	b.late(t, 3, cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_READY)
	esperaCalentamientos(t, b.cal, 1)

	b.late(t, 4, cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_READY)
	b.siguienteLatido(t)
	b.siguienteLatido(t)
	if got := b.cal.valor(); got != 1 {
		t.Fatalf("calentamientos = %d, quiero exactamente 1: el flanco a READY, y la cadencia posterior nada", got)
	}
}

// TestReadinessOrden_PrimerLatidoREADY_LoDisparaLaTransicionYNoElRegistro cierra el
// criterio (b) por su tercer lado, y es el que impide que la corrección degenere en
// «calentar dos veces».
//
// Si el primer frame de la sesión ya dice READY, quien calienta es la TRANSICIÓN. El
// disparador del registro corre después, ve que este Edge ya dijo algo y se calla. Sin
// esa condición habría dos avisos por una sola conexión: el segundo caería en el
// cerrojo «uno en vuelo por Edge» del pool la mayoría de las veces, pero apoyarse en
// eso sería resolver aguas abajo un dato que aquí se tiene exacto.
//
// 🔬 MUTACIÓN (criterio d): quitar la consulta del readiness de calientaPorRegistro
// (dejarlo disparando siempre) ⇒ rojo aquí con 2 calentamientos.
func TestReadinessOrden_PrimerLatidoREADY_LoDisparaLaTransicionYNoElRegistro(t *testing.T) {
	t.Parallel()
	b := nuevoBancoDeReadiness(t)

	b.late(t, 1, cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_READY)
	b.late(t, 2, cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_READY)
	b.siguienteLatido(t)
	b.siguienteLatido(t)
	if got := b.cal.valor(); got != 1 {
		t.Fatalf("calentamientos = %d en una conexión cuyo primer latido ya dice READY, quiero 1: "+
			"lo dispara la transición, y el disparador del registro tiene que verlo y callarse", got)
	}
}
