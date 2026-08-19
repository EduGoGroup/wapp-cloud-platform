package gatewaygrpc

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
)

// Barrido final del bucle Recv (Plan 050 · Ola 1 · T1.14, REQ-050.1 y REQ-050.2).
//
// Estos tests no prueban el carril —eso es worklane_internal_test.go— sino el
// REPARTO: qué rama de route se queda dentro del bucle Recv y qué rama lo suelta.
// Son el freno de mano contra la regresión que este plan viene a evitar: que
// alguien vuelva a meter I/O en el bucle, o —el error simétrico, igual de caro—
// que mande al carril lo que tiene que resolverse inline.
//
// El método es el mismo en todos: se TAPONA el carril de la sesión (su worker se
// queda dentro de un job que no vuelve) y se rutea un frame. Con el carril tapado,
// «lo que ocurre antes de que route retorne» y «lo que se queda esperando en la
// cola» son observables distintos y sin ambigüedad — no hacen falta relojes ni
// esperas: si algo cambia de lado, el test lo cuenta.

// fleetVigilante es un fleet.Repository que ANOTA cada escritura y, si se le
// pide, EXIGE que el ctx traiga deadline.
//
// Esa exigencia es la mitad de REQ-050.1: el ctx del bucle Recv (el del stream)
// NO tiene deadline, mientras que el de un job del carril SIEMPRE lo tiene
// (runJob, T1.5). Así que una escritura sin deadline significa exactamente una
// cosa: volvió al bucle.
//
// 🔴 El fallo se ANOTA y lo afirma el test (exigirDeadline), NO se lanza como
// panic. Desde T1.7-T1.11 estas escrituras corren dentro del worker del carril,
// que es OTRA goroutine: `testing` no recupera un panic de ahí, así que un panic
// no pondría rojo este test — se llevaría por delante el binario del paquete
// entero, sepultando el diagnóstico bajo el de todos los demás tests.
//
// ⚠️ Por qué el deadline y no un marcador de goroutine (tasks.md, T1.14): Go no
// expone el ID de goroutine, y un marcador colgado del context tampoco
// discriminaría, porque context.WithoutCancel(streamCtx) CONSERVA los Values del
// stream y el job heredaría la marca.
type fleetVigilante struct {
	*fleet.MemoryRepository

	// exigeDeadline pide que la escritura traiga su propio presupuesto.
	//
	// ⚠️ Hasta la Ola 3 este campo distinguía además DÓNDE corría el trabajo: solo el
	// carril ponía reloj, así que «con deadline» equivalía a «en el carril». Eso dejó
	// de ser cierto con T3.4: el handshake sigue INLINE en el bucle Recv (ADR-0040
	// §Decisión.3) y ahora TAMBIÉN trae reloj (onSessionRegistered). O sea: la
	// ausencia de deadline sigue delatando una escritura sin presupuesto, pero su
	// presencia ya no prueba por sí sola que el trabajo se fue al carril. Lo que
	// discrimina el reparto es el otro observable —qué ocurrió ANTES de que route
	// retornara, con el carril tapado—, y ese no ha cambiado.
	exigeDeadline bool

	regMu     sync.Mutex
	regVistas []string
	// sinDeadline son las escrituras que llegaron con un ctx SIN reloj teniendo
	// exigeDeadline puesto. Se anotan bajo el mismo mutex que regVistas (las
	// escribe la goroutine del worker, las lee la del test) y las afirma
	// exigirDeadline.
	sinDeadline []string
}

// anota registra la escritura y, si el ctx no trae reloj, apunta el fallo para que
// el test lo afirme al final.
func (r *fleetVigilante) anota(ctx context.Context, metodo string) {
	_, conDeadline := ctx.Deadline()
	r.regMu.Lock()
	defer r.regMu.Unlock()
	if !conDeadline && r.exigeDeadline {
		r.sinDeadline = append(r.sinDeadline, metodo)
	}
	r.regVistas = append(r.regVistas, metodo)
}

// vistas devuelve una copia de las escrituras observadas, en orden.
func (r *fleetVigilante) vistas() []string {
	r.regMu.Lock()
	defer r.regMu.Unlock()
	return append([]string(nil), r.regVistas...)
}

// exigirDeadline pone ROJO el test si alguna escritura vigilada llegó sin reloj
// propio. Es el sustituto del panic y conserva intacta su capacidad de detección:
// si runJob dejara de acotar el ctx del job, el método aparecería aquí y el test
// falla nombrándolo. Se llama DESPUÉS de drenar el carril (antes no ha corrido
// nada) y desde la goroutine del test, que es la única que puede afirmar.
func (r *fleetVigilante) exigirDeadline(t *testing.T) {
	t.Helper()
	r.regMu.Lock()
	defer r.regMu.Unlock()
	for _, metodo := range r.sinDeadline {
		t.Errorf("fleet.%s llamado con un ctx SIN deadline: esa escritura perdió su presupuesto. "+
			"En el carril lo pone runJob (T1.14, REQ-050.1); en el handshake, onSessionRegistered "+
			"(T3.4). Un ctx pelado aquí es el ctx crudo del stream, que no trae reloj.", metodo)
	}
}

func (r *fleetVigilante) MarkOnline(ctx context.Context, tenantID, edgeID, sessionID string) error {
	r.anota(ctx, "MarkOnline")
	return r.MemoryRepository.MarkOnline(ctx, tenantID, edgeID, sessionID)
}

func (r *fleetVigilante) MarkOffline(ctx context.Context, tenantID, edgeID, sessionID string) error {
	r.anota(ctx, "MarkOffline")
	return r.MemoryRepository.MarkOffline(ctx, tenantID, edgeID, sessionID)
}

func (r *fleetVigilante) MarkLoggedOut(ctx context.Context, tenantID, edgeID, sessionID string) error {
	r.anota(ctx, "MarkLoggedOut")
	return r.MemoryRepository.MarkLoggedOut(ctx, tenantID, edgeID, sessionID)
}

func (r *fleetVigilante) SaveHealth(ctx context.Context, tenantID, edgeID, sessionID string, h fleet.HealthSnapshot) error {
	r.anota(ctx, "SaveHealth")
	return r.MemoryRepository.SaveHealth(ctx, tenantID, edgeID, sessionID, h)
}

func (r *fleetVigilante) SetSelfPn(ctx context.Context, tenantID, edgeID, sessionID, selfPn string) error {
	r.anota(ctx, "SetSelfPn")
	return r.MemoryRepository.SetSelfPn(ctx, tenantID, edgeID, sessionID, selfPn)
}

// carrilTapado devuelve un carril cuyo worker de sesion está OCUPADO: metido en un
// job que no retorna hasta que se llame a soltar. Con él así, todo lo que se encole
// para esa sesión se queda EN LA COLA, visible y contable, en vez de ejecutarse.
//
// Es el instrumento del barrido: sin tapón, un trabajo mal colocado se ejecutaría
// igual —solo que en otra goroutine— y ningún test lo notaría.
func carrilTapado(t *testing.T, sesion string) (lane *workLane, soltar func()) {
	t.Helper()
	lane = newWorkLane(context.Background(), 8, 2*time.Second, laneLog())

	dentro := make(chan struct{})
	libre := make(chan struct{})
	if err := lane.submit(sesion, jobReceipt, func(context.Context) {
		close(dentro)
		<-libre
	}); err != nil {
		t.Fatalf("submit del tapón: %v", err)
	}
	select {
	case <-dentro:
	case <-time.After(3 * time.Second):
		t.Fatal("el tapón no llegó a ejecutarse: el carril no arrancó su worker")
	}

	var unaVez sync.Once
	return lane, func() { unaVez.Do(func() { close(libre) }) }
}

// cerrarCarril suelta el tapón y cierra el carril en sus dos tiempos. Llamarlo
// siempre (defer): un tapón sin soltar dejaría el drenaje agotando su presupuesto
// y una goroutine viva al acabar el test.
func cerrarCarril(lane *workLane, soltar func()) {
	soltar()
	lane.seal()
	lane.drain(3 * time.Second)
}

// ccDePrueba arma el connCtx por-frame de una sesión CON identidad mTLS (que es la
// condición para que las escrituras de fleet lleguen a ocurrir).
func ccDePrueba(sessionID string) connCtx {
	return connCtx{sessionID: sessionID, tenantID: "t-1", edgeID: "e-1", hasIdentity: true}
}

func frameAck(sessionID, cmdID string) *cloudlinkv1.EdgeToCloud {
	return &cloudlinkv1.EdgeToCloud{
		SessionId: sessionID,
		Payload:   &cloudlinkv1.EdgeToCloud_Ack{Ack: &cloudlinkv1.Ack{AckedCommandId: cmdID, Ok: true}},
	}
}

func frameIncoming(sessionID string) *cloudlinkv1.EdgeToCloud {
	return &cloudlinkv1.EdgeToCloud{
		SessionId: sessionID,
		Payload: &cloudlinkv1.EdgeToCloud_Incoming{Incoming: &cloudlinkv1.IncomingMessage{
			From: "57300@s.whatsapp.net", Text: "hola", WaMessageId: "wamid.inline",
		}},
	}
}

func framePong(sessionID string) *cloudlinkv1.EdgeToCloud {
	return &cloudlinkv1.EdgeToCloud{
		SessionId: sessionID,
		Payload:   &cloudlinkv1.EdgeToCloud_Pong{Pong: &cloudlinkv1.Pong{Nonce: 7}},
	}
}

// frameHeartbeatConSalud trae SessionHealth para que el job del latido llegue de
// verdad a fleet.SaveHealth (sin salud, persistHealth es un no-op y el test no
// mediría nada).
func frameHeartbeatConSalud(sessionID string) *cloudlinkv1.EdgeToCloud {
	return &cloudlinkv1.EdgeToCloud{
		SessionId: sessionID,
		Payload: &cloudlinkv1.EdgeToCloud_Heartbeat{Heartbeat: &cloudlinkv1.Heartbeat{
			LeaseCounter: 1,
			SessionHealth: &cloudlinkv1.SessionHealth{
				WhatsappSocketState:  cloudlinkv1.WhatsappSocketState_WHATSAPP_SOCKET_STATE_CONNECTED,
				LastInboundEventAgeS: 1,
				BinaryVersion:        "v-test",
			},
		}},
	}
}

// framesDelBarrido es UN frame por cada rama viva de route: las cuatro que se
// quedan inline (Incoming, Ack, Pong y el desconocido del default) y las seis que
// van al carril (Heartbeat, Receipt, DiagnosticsBundle y las tres de auth).
func framesDelBarrido(sessionID string) []*cloudlinkv1.EdgeToCloud {
	return []*cloudlinkv1.EdgeToCloud{
		frameIncoming(sessionID),
		frameAck(sessionID, "cmd-del-barrido"),
		framePong(sessionID),
		{SessionId: sessionID}, // sin payload: la rama default
		frameHeartbeatConSalud(sessionID),
		{SessionId: sessionID, Payload: &cloudlinkv1.EdgeToCloud_Receipt{Receipt: &cloudlinkv1.MessageReceipt{
			SessionId: sessionID, MessageIds: []string{"wamid.1"}, CommandId: "cmd-r",
		}}},
		{SessionId: sessionID, Payload: &cloudlinkv1.EdgeToCloud_DiagnosticsBundle{
			DiagnosticsBundle: &cloudlinkv1.DiagnosticsBundle{CommandId: "cmd-d"},
		}},
		{SessionId: sessionID, Payload: &cloudlinkv1.EdgeToCloud_UserLogin{
			UserLogin: &cloudlinkv1.UserLoginRequest{CommandId: "cmd-login", SessionId: sessionID},
		}},
		{SessionId: sessionID, Payload: &cloudlinkv1.EdgeToCloud_UserRefresh{
			UserRefresh: &cloudlinkv1.UserRefreshRequest{CommandId: "cmd-refresh", SessionId: sessionID},
		}},
		{SessionId: sessionID, Payload: &cloudlinkv1.EdgeToCloud_UserLogout{
			UserLogout: &cloudlinkv1.UserLogoutRequest{CommandId: "cmd-logout", SessionId: sessionID},
		}},
	}
}

// TestRouteSoloLasRamasPesadasEntranAlCarril es EL barrido de T1.14: se rutea un
// frame de CADA rama de route con el carril tapado y luego se hace el CENSO de la
// cola. Lo que quedó dentro es, exactamente, lo que se soltó al carril; lo que no
// aparece, se resolvió dentro del bucle Recv.
//
// Por qué discrimina en las DOS direcciones, que es lo que lo hace un freno de
// regresión y no un adorno:
//   - Si alguien devuelve una rama pesada al bucle (p. ej. el Heartbeat), su tipo
//     desaparece del censo y el total baja: rojo.
//   - Si alguien manda al carril una rama que debe ser inline —el Ack es el caso
//     que este plan protege por nombre—, aparece un job de más: el total sube y el
//     mapa deja de coincidir. Rojo también, y por el motivo correcto.
func TestRouteSoloLasRamasPesadasEntranAlCarril(t *testing.T) {
	t.Parallel()
	srv := New(session.NewRegistry(), laneLog())
	lane, soltar := carrilTapado(t, "s-1")
	defer cerrarCarril(lane, soltar)

	cc := ccDePrueba("s-1")
	for _, frame := range framesDelBarrido("s-1") {
		srv.route(lane, cc, frame)
	}

	// El tapón ya no está en la cola (el worker lo sacó antes de bloquearse), así
	// que lo que queda es SOLO lo que rutearon los frames del barrido.
	total, porTipo := lane.pending()
	quiero := map[string]int{"heartbeat": 1, "receipt": 1, "diagnostics": 1, "auth": 3}

	if total != 6 {
		t.Fatalf("jobs encolados = %d (%v), quiero 6: heartbeat, receipt, diagnostics y las tres de auth. "+
			"Uno de más significa que una rama inline (Incoming, Ack, Pong o default) se fue al carril; "+
			"uno de menos, que una rama pesada volvió al bucle Recv", total, porTipo)
	}
	if len(porTipo) != len(quiero) {
		t.Fatalf("tipos de job encolados = %v, quiero exactamente %v", porTipo, quiero)
	}
	for kind, n := range quiero {
		if porTipo[kind] != n {
			t.Fatalf("jobs de tipo %q encolados = %d, quiero %d (censo completo: %v)", kind, porTipo[kind], n, porTipo)
		}
	}
}

// TestRouteElAckSeResuelveInlineConElCarrilTapado (REQ-050.2) es la aserción que
// el plan pide por nombre: el Ack es la VÍCTIMA del head-of-line, no su causa.
//
// La prueba es de SINCRONÍA, no de tiempo: cuando route retorna en ESTA goroutine,
// el Ack ya tiene que estar entregado. Con el Ack en el carril no lo estaría —el
// tapón tiene ocupado al worker de la sesión— y el select cae por el default. Eso
// es lo que distingue un Ack movido al carril de uno que no, sin medir latencias
// (que sería flaky) y sin sondear (que le daría tiempo a llegar).
func TestRouteElAckSeResuelveInlineConElCarrilTapado(t *testing.T) {
	t.Parallel()
	srv := New(session.NewRegistry(), laneLog())
	lane, soltar := carrilTapado(t, "s-1")
	defer cerrarCarril(lane, soltar)

	// El comando pendiente que espera su acuse, como lo deja SendText.
	ch := make(chan *cloudlinkv1.Ack, 1)
	srv.acksMu.Lock()
	srv.acks["cmd-inline"] = pendingAck{ch: ch, sessionID: "s-1"}
	srv.acksMu.Unlock()

	srv.route(lane, ccDePrueba("s-1"), frameAck("s-1", "cmd-inline"))

	select {
	case ack := <-ch:
		if ack.GetAckedCommandId() != "cmd-inline" || !ack.GetOk() {
			t.Fatalf("ack entregado = %+v, quiero el de cmd-inline con ok=true", ack)
		}
	default:
		t.Fatal("route retornó SIN entregar el Ack: se fue al carril y está esperando detrás del trabajo " +
			"pesado de su sesión, que es exactamente la latencia que el Plan 050 viene a quitar del camino del Ack")
	}
}

// TestRouteElIncomingSeResuelveInlineConElCarrilTapado: el ingreso (decodeIncoming
// + OnIncoming) sigue byte a byte dentro del bucle. Es memoria y CPU —abrir un
// sellado y llamar al motor—, no I/O, y el motor de flujos ya tiene su propia cola.
func TestRouteElIncomingSeResuelveInlineConElCarrilTapado(t *testing.T) {
	t.Parallel()
	srv := New(session.NewRegistry(), laneLog())
	entregado := make(chan string, 1)
	srv.OnIncoming = func(sessionID string, _ *cloudlinkv1.IncomingMessage) { entregado <- sessionID }

	lane, soltar := carrilTapado(t, "s-1")
	defer cerrarCarril(lane, soltar)

	srv.route(lane, ccDePrueba("s-1"), frameIncoming("s-1"))

	select {
	case sid := <-entregado:
		if sid != "s-1" {
			t.Fatalf("OnIncoming recibió session_id %q, quiero s-1", sid)
		}
	default:
		t.Fatal("route retornó sin entregar el IncomingMessage: el ingreso dejó de resolverse inline")
	}
}

// TestRouteElPongSeResuelveInlineConElCarrilTapado: el Pong es un log y nada más.
// Mandarlo al carril le costaría una cola por un frame que no escribe en ningún
// sitio.
func TestRouteElPongSeResuelveInlineConElCarrilTapado(t *testing.T) {
	t.Parallel()
	buf := &laneLogBuf{}
	srv := New(session.NewRegistry(), logger.New(logger.WithWriter(buf), logger.WithLevel(slog.LevelDebug)))

	lane, soltar := carrilTapado(t, "s-1")
	defer cerrarCarril(lane, soltar)

	srv.route(lane, ccDePrueba("s-1"), framePong("s-1"))

	if !buf.contiene("pong recibido") {
		t.Fatalf("route retornó sin haber registrado el pong: %q", buf.String())
	}
}

// TestRouteElHeartbeatLlegaAFleetConSuPropioDeadline cierra las DOS mitades de
// REQ-050.1 sobre la rama que lo motivó:
//
//	(a) el latido NO toca la base dentro del bucle Recv — con el carril tapado,
//	    fleet no ha visto nada cuando route retorna;
//	(b) el trabajo LLEGA igual, solo que fuera, y llega con RELOJ PROPIO — si el
//	    ctx no trajera deadline, el vigilante lo anota y exigirDeadline pone rojo
//	    el test nombrando el método.
//
// La mitad (b) importa tanto como la (a): un «arreglo» que se limitara a dejar de
// hacer el trabajo también vaciaría el bucle, y sin ella este test lo aprobaría.
func TestRouteElHeartbeatLlegaAFleetConSuPropioDeadline(t *testing.T) {
	t.Parallel()
	repo := &fleetVigilante{MemoryRepository: fleet.NewMemoryRepository(), exigeDeadline: true}
	srv := New(session.NewRegistry(), laneLog(), WithFleet(repo))

	lane, soltar := carrilTapado(t, "s-1")
	// Doble llamada a propósito: la de abajo es la del cuerpo del test (hay que
	// drenar para ver el efecto) y esta cubre la salida por t.Fatalf, que se
	// llevaría por delante el cierre y dejaría viva la goroutine del tapón.
	// cerrarCarril es idempotente: soltar es un sync.Once, seal ignora el segundo
	// sellado y drain vuelve al instante con los workers ya muertos.
	defer cerrarCarril(lane, soltar)

	srv.route(lane, ccDePrueba("s-1"), frameHeartbeatConSalud("s-1"))

	if vistas := repo.vistas(); len(vistas) != 0 {
		t.Fatalf("fleet vio %v ANTES de que route retornara: el latido volvió a escribir dentro del bucle Recv", vistas)
	}

	cerrarCarril(lane, soltar)

	// La mitad (b): el trabajo corrió con reloj propio. Va antes de la aserción de
	// abajo y con t.Errorf —no t.Fatalf— para que un fallo de deadline no oculte lo
	// que el censo tenga que decir.
	repo.exigirDeadline(t)

	if vistas := repo.vistas(); len(vistas) != 1 || vistas[0] != "SaveHealth" {
		t.Fatalf("fleet vio %v tras drenar el carril, quiero exactamente [SaveHealth]: "+
			"sacar el trabajo del bucle no puede significar dejar de hacerlo", vistas)
	}
}

// TestElHandshakeSigueResolviendoseEnElBucleRecv documenta la EXCEPCIÓN que T1.14
// deja escrita: las escrituras del registro de sesión (fleet online, lease inicial
// y su push, evento de auditoría) se quedan en el bucle Recv por decisión explícita
// (ADR-0040 §Decisión.3). No es un descuido del barrido: es que el handshake ordena
// —el Edge no puede recibir un LeaseUpdate de renovación antes que su lease
// inicial—, y el carril, que es asíncrono, no puede garantizar eso frente al resto
// del bucle.
//
// Lo que este test afirma es UNA sola cosa: que al retornar onSessionRegistered el
// MarkOnline YA ocurrió. Ese «ya» es lo que significa inline, y es lo que T3.4 no
// podía tocar.
//
// ⚠️ El vigilante no exige deadline aquí, pero ya NO porque el handshake carezca de
// reloj: desde T3.4 lo tiene (s.workBudget, puesto en onSessionRegistered). Quien
// afirma ese reloj es TestElHandshakeTraeSuPropioReloj
// (connect_handshake_reloj_internal_test.go); aquí se deja fuera para que este test
// siga midiendo el reparto y nada más.
func TestElHandshakeSigueResolviendoseEnElBucleRecv(t *testing.T) {
	t.Parallel()
	repo := &fleetVigilante{MemoryRepository: fleet.NewMemoryRepository()}
	srv := New(session.NewRegistry(), laneLog(), WithFleet(repo))

	srv.onSessionRegistered(context.Background(), ccDePrueba("s-1"))

	if vistas := repo.vistas(); len(vistas) != 1 || vistas[0] != "MarkOnline" {
		t.Fatalf("fleet vio %v al retornar onSessionRegistered, quiero [MarkOnline] YA: "+
			"el registro de la sesión se resuelve en el bucle, no en el carril (ADR-0040 §Decisión.3)", vistas)
	}
}
