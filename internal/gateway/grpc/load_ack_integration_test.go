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
	factorOcioso = 10
)

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
	{nombre: "N=20 · 20 sesiones latiendo", enVuelo: 20, muestras: muestrasT52},
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

// TestIntegration_CargaAckBajoHeartbeatsEnVuelo es T5.2: mide y PUBLICA la
// latencia del Ack antes del Plan 050.
//
// No es solo un impresor de números: afirma las dos cosas sin las cuales la
// tabla no probaría nada.
//  1. Que la ráfaga DOMINA: el p50 del Ack con N heartbeats en vuelo queda por
//     encima de la mitad del suelo aritmético (N × latencia inyectada). Si esto
//     falla, el Ack no se está colando detrás de la ráfaga y el escenario no
//     reproduce el head-of-line.
//  2. Que el bucle OCIOSO es órdenes de magnitud más rápido. Sin este control,
//     un p99 alto podría ser de la máquina, del bufconn o de Postgres, y la
//     comparación de T5.3 no significaría nada.
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
	t.Logf("================ T5.2 · latencia del Ack ANTES del Plan 050 ================")
	t.Logf("SHA medido: 76e2aa6 (arnés test-only) sobre el commit base de producción af457c9")
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
	t.Logf("ventana  = lo que el Ack, YA en el cable, esperó a que el bucle Recv lo consumiera")
	t.Logf("silencio = mayor hueco entre dos frames consecutivos recibidos por el Edge")
	t.Logf("===========================================================================")
}

// comprobarTablaT52 afirma las dos cosas que hacen publicable la tabla.
func comprobarTablaT52(t *testing.T, tabla []resultadoT52) {
	t.Helper()
	var ocioso *resultadoT52
	for i := range tabla {
		r := &tabla[i]
		if r.n == 0 {
			t.Fatalf("el escenario %q no dejó ni una muestra válida (%d fallos)", r.esc.nombre, r.fallos)
		}
		if r.esc.enVuelo == 0 {
			ocioso = r
			continue
		}
		// (1) La ráfaga manda: el Ack se cuela DETRÁS de ella. El umbral es la
		// mitad del suelo aritmético y no el suelo entero porque el SendText
		// arranca a la vez que la ráfaga: los primeros frames pueden estar ya
		// procesados cuando el Ack entra en la cola.
		suelo := time.Duration(r.esc.enVuelo) * latenciaFleetT52 / 2
		if r.p50 < suelo {
			t.Errorf("%q: p50 del Ack = %v, por debajo de %v (mitad de %d × %v). El Ack no está "+
				"quedando detrás de la ráfaga: el escenario NO reproduce el head-of-line y su p99 no prueba nada",
				r.esc.nombre, r.p50, suelo, r.esc.enVuelo, latenciaFleetT52)
		}
	}
	if ocioso == nil {
		t.Fatal("falta el escenario de control (N=0): sin él la tabla no dice de quién es la culpa")
	}
	// (2) El control: con el bucle vacío el MISMO Ack vuelve órdenes de magnitud
	// antes. Si no, lo que se está midiendo no es el bucle.
	for i := range tabla {
		r := &tabla[i]
		if r.esc.enVuelo == 0 {
			continue
		}
		if r.p50 < time.Duration(factorOcioso)*ocioso.p99 {
			t.Errorf("%q: p50 = %v y el bucle ocioso tiene p99 = %v; se esperaba al menos %d× más lento. "+
				"O N es demasiado bajo, o la latencia inyectada no basta para saturar el bucle",
				r.esc.nombre, r.p50, ocioso.p99, factorOcioso)
		}
	}
}
