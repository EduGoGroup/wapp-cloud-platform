package grpc

// DOS EDGE A LA VEZ, Y CADA RESPUESTA POR SU CABLE (R-G9, R3.4.a; T-5, ADR-0048).
//
// 🔴 El incidente que esto congela (2026-09-03, UAT): los frames de auth de TODOS los Edge
// estampan `__wapp_control__`, y la respuesta se buscaba en session.Registry, que indexa por
// session_id sin tenant y con política última-gana. El login de un Edge —con sus tokens—
// salía por el cable del último que se hubiera registrado, fuera o no de la misma empresa.
//
// Aquí cada Edge es un STREAM distinto (un cable en memoria) con el MISMO session_id de
// control, y el Registry tiene además esa clave ocupada por un tercero. Lo que se afirma es
// que la respuesta sale por el cable de quien preguntó y por ninguno más. El mismo caso con
// dos conexiones mTLS de verdad, y el registro del canal de control, van con connect.go.
//
// 🔬 Mutación que pone esto en rojo: devolver pushAuthResponse a
// `s.registry.Push(ctx, cc.sessionID, msg)`.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	cltransport "github.com/EduGoGroup/wapp-cloudlink/transport"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/session"
)

// liveEdge es un Edge conectado: su identidad mTLS y su propio stream, hablando por el canal
// de control.
type liveEdge struct {
	tenantID string
	stream   *liveSession
	cc       connCtx
}

func connectEdge(tenantID, edgeID string) *liveEdge {
	stream := &liveSession{id: "el stream de " + edgeID}
	return &liveEdge{tenantID: tenantID, stream: stream, cc: controlChannel(tenantID, edgeID, stream)}
}

// requireOwnAnswers afirma que el Edge recibió exactamente las respuestas a SUS peticiones, en
// cualquier orden, y que cada una trae el par de SU tenant.
func (e *liveEdge) requireOwnAnswers(t *testing.T, cmdIDs ...string) {
	t.Helper()
	frames := e.stream.received()
	if len(frames) != len(cmdIDs) {
		t.Fatalf("%s recibió %d frames, se esperaban %d", e.stream.id, len(frames), len(cmdIDs))
	}
	pending := make(map[string]bool, len(cmdIDs))
	for _, id := range cmdIDs {
		pending[id] = true
	}
	for _, frame := range frames {
		if !pending[frame.GetCommandId()] {
			t.Fatalf("%s recibió la respuesta de %q, que no es suya (o le llegó dos veces)", e.stream.id, frame.GetCommandId())
		}
		delete(pending, frame.GetCommandId())
		requireTokens(t, requireAuthResponse(t, frame, e.cc, frame.GetCommandId()), tokensFor(e.tenantID, "user-1"))
	}
}

// Los dos casos van EN PAREJA: dos Edge de la MISMA empresa (disponibilidad: el login de uno
// no se pierde por el cable del otro) y dos Edge de EMPRESAS DISTINTAS (la severidad real: el
// token de un operador no viaja a la máquina de otra). Un arreglo que solo calificara la clave
// por tenant pasaría el primero y seguiría filtrando en el segundo.
//
// El orden es deliberado: el segundo Edge pide DESPUÉS del primero —con el diseño anterior
// quedaba como último registrado bajo la clave compartida— y solo entonces el primero vuelve
// a pedir.
func TestTwoEdgesEachGetTheirOwnAnswer(t *testing.T) {
	t.Parallel()
	cases := map[string]struct{ firstTenant, secondTenant string }{
		"same tenant":       {"tenant-a", "tenant-a"},
		"different tenants": {"tenant-a", "tenant-b"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rig := newAuthRig(t, echoTenantAuth("user-1"))
			first := connectEdge(tc.firstTenant, "edge-1")
			second := connectEdge(tc.secondTenant, "edge-2")
			ctx := context.Background()

			rig.srv.handleUserLogin(ctx, first.cc, loginRequest("first-1"))
			first.requireOwnAnswers(t, "first-1")
			rig.srv.handleUserLogin(ctx, second.cc, loginRequest("second-1"))
			second.requireOwnAnswers(t, "second-1")
			rig.srv.handleUserLogin(ctx, first.cc, loginRequest("first-2"))
			rig.srv.handleUserRefresh(ctx, second.cc, refreshRequest("second-2", "refresh-"+tc.secondTenant))

			first.requireOwnAnswers(t, "first-1", "first-2")
			second.requireOwnAnswers(t, "second-1", "second-2")
			requireNothing(t, rig.intruder)
		})
	}
}

// Y a la vez de verdad: dos Edge de empresas distintas piden en paralelo, muchas veces, y
// ninguna respuesta cruza de cable.
func TestTwoEdgesLoggingInConcurrentlyNeverCross(t *testing.T) {
	t.Parallel()
	const perEdge = 40
	rig := newAuthRig(t, echoTenantAuth("user-1"))
	edges := []*liveEdge{connectEdge("tenant-a", "edge-1"), connectEdge("tenant-b", "edge-2")}

	start := make(chan struct{})
	var wg sync.WaitGroup
	ids := make([][]string, len(edges))
	for i, edge := range edges {
		for n := range perEdge {
			cmdID := fmt.Sprintf("edge-%d/%d", i+1, n)
			ids[i] = append(ids[i], cmdID)
			wg.Go(func() {
				<-start
				rig.srv.handleUserLogin(context.Background(), edge.cc, loginRequest(cmdID))
			})
		}
	}
	close(start)
	wg.Wait()

	for i, edge := range edges {
		edge.requireOwnAnswers(t, ids[i]...)
	}
	requireNothing(t, rig.intruder)
}

// Apagar un Edge no deja al otro sin auth: quien ocupaba la clave de control en el Registry se
// va, la clave desaparece, y el refresh del que sigue conectado llega igual por su cable.
func TestAnotherEdgeLeavingDoesNotBreakAuth(t *testing.T) {
	t.Parallel()
	rig := newAuthRig(t, echoTenantAuth("user-1"))
	edge := connectEdge("tenant-a", "edge-1")
	ctx := context.Background()

	rig.srv.handleUserLogin(ctx, edge.cc, loginRequest("cmd-1"))
	rig.evict()
	if rig.reg.Count() != 0 {
		t.Fatalf("el Registry debía quedar vacío al irse el otro Edge: %d sesiones", rig.reg.Count())
	}
	rig.srv.handleUserRefresh(ctx, edge.cc, refreshRequest("cmd-2", "refresh-tenant-a"))

	edge.requireOwnAnswers(t, "cmd-1", "cmd-2")
	requireNothing(t, rig.intruder)
}

// Atender auth no registra nada en el Registry: el canal de control no es una sesión.
func TestAuthNeverRegistersTheControlChannel(t *testing.T) {
	t.Parallel()
	srv := New(session.NewRegistry(), quietLog(), WithAuthenticator(echoTenantAuth("user-1")))
	edge := connectEdge("tenant-a", "edge-1")

	srv.handleUserLogin(context.Background(), edge.cc, loginRequest("cmd-1"))
	srv.handleUserLogout(context.Background(), edge.cc, logoutRequest("cmd-2", "refresh-tenant-a", false))

	if n := srv.registry.Count(); n != 0 {
		t.Errorf("el Registry tiene %d sesiones tras atender auth, se esperaba ninguna", n)
	}
	if got := srv.sessionsForEdge("tenant-a", "edge-1"); len(got) != 0 {
		t.Errorf("el canal de control quedó como sesión del Edge: %v", got)
	}
}

// 🔴 T-5: sin cable en el connCtx la respuesta NO se entrega —ni por el Registry, donde la
// clave de control es de otro— y se grita un Error que nombra al Edge. Sin tokens en el log.
func TestAuthResponseWithoutSenderNeverFallsBackToTheRegistry(t *testing.T) {
	t.Parallel()
	rig := newAuthRig(t, echoTenantAuth("user-1"))

	rig.srv.handleUserLogin(context.Background(), controlChannel("tenant-1", "edge-1", nil), loginRequest("cmd-1"))

	requireNothing(t, rig.intruder)
	for _, want := range []string{
		"level=ERROR", `msg="auth: el connCtx no trae el stream emisor; la respuesta NO se entrega"`,
		"session_id=" + cltransport.ControlSessionID, "command_id=cmd-1", "edge_id=edge-1",
	} {
		if !rig.log.contains(want) {
			t.Errorf("al log le falta %q: %q", want, rig.log.String())
		}
	}
	requireNoSecrets(t, "el log", rig.log.String())
}

// La respuesta usa los dos relojes de BoundedSend (T-7). El propio: el plazo del Registry. Con
// el Edge atascado se rinde por ese plazo, lo deja en debug nombrando al EDGE (el session_id de
// control no identifica a nadie) y no tumba nada. Sin tokens en el log.
func TestAuthResponseIsBoundedByTheRegistrySendTimeout(t *testing.T) {
	t.Parallel()
	rig := newAuthRig(t, echoTenantAuth("user-1"), session.WithSendTimeout(time.Millisecond))
	stream, entered := stuckStream(t)

	done := make(chan struct{})
	go func() {
		defer close(done)
		rig.srv.handleUserLogin(context.Background(), controlChannel("tenant-1", "edge-1", stream), loginRequest("cmd-1"))
	}()
	await(t, done, "que la respuesta de auth se rinda por el plazo del Registry")
	await(t, entered, "la escritura hacia el Edge atascado")

	for _, want := range []string{
		"level=DEBUG", `msg="auth: la respuesta no salió por el stream que la pidió"`,
		"command_id=cmd-1", "edge_id=edge-1", session.ErrPushTimeout.Error(), `\"edge-1\"`,
	} {
		if !rig.log.contains(want) {
			t.Errorf("al log le falta %q: %q", want, rig.log.String())
		}
	}
	requireNoSecrets(t, "el log", rig.log.String())
	requireNothing(t, rig.intruder)
}

// El otro reloj: el ctx de quien llama (el del job del carril). Si termina, se deja de esperar
// al Edge atascado sin agotar el plazo del Registry.
func TestAuthResponseStopsWaitingWhenItsContextEnds(t *testing.T) {
	t.Parallel()
	rig := newAuthRig(t, echoTenantAuth("user-1"), session.WithSendTimeout(time.Hour))
	stream, entered := stuckStream(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		rig.srv.handleUserLogin(ctx, controlChannel("tenant-1", "edge-1", stream), loginRequest("cmd-1"))
	}()
	await(t, entered, "que la escritura llegue al Edge atascado")
	cancel()
	await(t, done, "que la respuesta de auth vuelva al terminar su ctx")

	if !rig.log.contains(session.ErrPushAbandoned.Error()) || !rig.log.contains("command_id=cmd-1") {
		t.Errorf("la respuesta abandonada no dejó su rastro: %q", rig.log.String())
	}
}

// Un stream que rechaza la escritura no tumba nada: se registra en debug y se sigue.
func TestAuthResponseSurvivesABrokenStream(t *testing.T) {
	t.Parallel()
	rig := newAuthRig(t, echoTenantAuth("user-1"))
	broken := funcSender(func(*cloudlinkv1.CloudToEdge) error { return errors.New("stream roto") })

	rig.srv.handleUserLogin(context.Background(), controlChannel("tenant-1", "edge-1", broken), loginRequest("cmd-1"))

	if got := strings.Count(rig.log.String(), "auth: la respuesta no salió por el stream que la pidió"); got != 1 {
		t.Errorf("líneas de entrega fallida = %d, se esperaba 1: %q", got, rig.log.String())
	}
	requireNothing(t, rig.intruder)
}
