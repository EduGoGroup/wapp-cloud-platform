package grpc

// POR QUÉ STREAM sale una inferencia (R-G15, R3.4.d; ADR-0048 regla 3). La pareja que no vale
// por separado —el Edge que dijo DOWN no recibe el prompt, y el que no lo dice SIGUE siendo
// elegible (T-6)— más el candidato vivo, el destino que manda sobre el origen, el reparto
// estable y el canal de control, que nunca es una sesión que atienda inferencias (T-5). La
// otra mitad de la pareja está en plaza_test.go: la plaza es del mismo Edge.

import (
	"bytes"
	"context"
	"errors"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"google.golang.org/protobuf/proto"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/session"
)

// declare anota lo que ese Edge ha dicho sobre su capacidad de inferencia, como un latido suyo.
func (r *inferRig) declare(tenantID, edgeID string, readiness cloudlinkv1.InferenceReadiness) {
	r.srv.noteReadiness(phone(tenantID, edgeID, ""), readiness)
}

// requireWentThrough lanza la inferencia varias veces —el recorrido de un map de Go es
// aleatorio: una sola no distingue «elige bien» de «tuvo suerte»— y afirma que CADA frame
// salió por want y ninguno por las demás.
func (r *inferRig) requireWentThrough(t *testing.T, tenantID string, req InferRequest, want *inferSession, others ...*inferSession) {
	t.Helper()
	const rounds = 10
	before := len(want.received())
	idle := make([]int, len(others))
	for i, o := range others {
		idle[i] = len(o.received())
	}
	for range rounds {
		if out, err := r.srv.Infer(context.Background(), tenantID, req); err != nil || out != modelOutput {
			t.Fatalf("Infer = (%q, %v), se esperaba la salida del modelo por %s", out, err, want.id)
		}
	}
	frames := want.received()[before:]
	if len(frames) != rounds {
		t.Fatalf("por %s salieron %d frames de %d", want.id, len(frames), rounds)
	}
	for _, f := range frames {
		if f.GetSessionId() != want.id || f.GetInferenceRequest().GetSessionId() != req.OriginSessionID {
			t.Fatalf("el frame va dirigido a %q con origen %q; se esperaba %q con origen %q",
				f.GetSessionId(), f.GetInferenceRequest().GetSessionId(), want.id, req.OriginSessionID)
		}
	}
	for i, o := range others {
		if got := len(o.received()) - idle[i]; got != 0 {
			t.Fatalf("por %s salieron %d frames: no le tocaba ninguno", o.id, got)
		}
	}
}

// requireNobody afirma que la inferencia falla honesta —edge_offline, sin command_id: no hubo
// a quién preguntar— y que a esas sesiones no les llegó NADA.
func (r *inferRig) requireNobody(t *testing.T, tenantID string, req InferRequest, silent ...*inferSession) {
	t.Helper()
	out, err := r.srv.Infer(context.Background(), tenantID, req)
	requireReason(t, err, ReasonEdgeOffline)
	if ie := asInferError(t, err); !errors.Is(err, session.ErrSessionOffline) || ie.CommandID() != "" || ie.SessionID() != "" || out != "" {
		t.Fatalf("Infer = (%q, %v), se esperaba ErrSessionOffline sin command_id ni sesión", out, err)
	}
	for _, s := range silent {
		if n := len(s.received()); n != 0 {
			t.Fatalf("la sesión %s recibió %d frames y no había nadie elegible", s.id, n)
		}
	}
	requireNoPendingInfers(t, r.srv)
}

// 🔴 Mitad 1 de la pareja ADR-0048 (R3.4.d): el Edge que dijo DOWN no recibe el prompt. Está
// puesto para que el orden alfabético lo ELIGIERA (s-aaa < s-zzz): sin el filtro, el frame
// saldría por él. Con toda la flota en DOWN no se envía nada.
func TestInferTheEdgeThatSaidDownDoesNotGetThePrompt(t *testing.T) {
	t.Parallel()
	rig := newInferRig(t)
	down := rig.live(t, "tenant-1", "edge-down", "s-aaa", rig.answers(t, modelOutput))
	ready := rig.live(t, "tenant-1", "edge-ready", "s-zzz", rig.answers(t, modelOutput))
	rig.declare("tenant-1", "edge-down", saysDown)
	rig.declare("tenant-1", "edge-ready", saysReady)

	rig.requireWentThrough(t, "tenant-1", InferRequest{Prompt: "p"}, ready, down)

	// El único que podía, deja de poder: nadie elegible, y nada sale por ningún cable.
	rig.declare("tenant-1", "edge-ready", saysDown)
	sent := len(ready.received())
	rig.requireNobody(t, "tenant-1", InferRequest{Prompt: "p"}, down)
	if len(ready.received()) != sent {
		t.Fatal("con toda la flota en DOWN salió un frame")
	}

	// Y vuelve a poder en cuanto lo dice.
	rig.declare("tenant-1", "edge-down", saysReady)
	rig.requireWentThrough(t, "tenant-1", InferRequest{Prompt: "p"}, down, ready)
}

// 🔴 Mitad 2 de la pareja (T-6): el Edge que NO LO DICE sigue siendo elegible. El cero es «no
// lo dice» —un Edge anterior a v0.17.0, o recién arrancado—, jamás «no puede»: exigir READY
// dejaría sin inferencia a toda la flota que no publica el campo, sin un solo error. READY es
// una PREFERENCIA que ordena, no una puerta que cierra.
func TestInferTheEdgeThatDoesNotSayStaysEligible(t *testing.T) {
	t.Parallel()
	t.Run("alone", func(t *testing.T) {
		t.Parallel()
		rig := newInferRig(t)
		quiet := rig.live(t, "tenant-1", "edge-quiet", "s-1", rig.answers(t, modelOutput))
		rig.requireWentThrough(t, "tenant-1", InferRequest{Prompt: "p"}, quiet)

		// Decir UNSPECIFIED tampoco lo saca: ni siquiera se anota.
		rig.declare("tenant-1", "edge-quiet", saysNothing)
		rig.requireWentThrough(t, "tenant-1", InferRequest{Prompt: "p"}, quiet)
	})
	t.Run("ready is preferred over silent", func(t *testing.T) {
		t.Parallel()
		rig := newInferRig(t)
		quiet := rig.live(t, "tenant-1", "edge-quiet", "s-aaa", rig.answers(t, modelOutput))
		ready := rig.live(t, "tenant-1", "edge-ready", "s-zzz", rig.answers(t, modelOutput))
		rig.declare("tenant-1", "edge-ready", saysReady)
		rig.requireWentThrough(t, "tenant-1", InferRequest{Prompt: "p"}, ready, quiet)
	})
	t.Run("silent is chosen over down", func(t *testing.T) {
		t.Parallel()
		rig := newInferRig(t)
		down := rig.live(t, "tenant-1", "edge-down", "s-aaa", rig.answers(t, modelOutput))
		quiet := rig.live(t, "tenant-1", "edge-quiet", "s-zzz", rig.answers(t, modelOutput))
		rig.declare("tenant-1", "edge-down", saysDown)
		rig.requireWentThrough(t, "tenant-1", InferRequest{Prompt: "p"}, quiet, down)
	})
	t.Run("ready that falls silent keeps what it said", func(t *testing.T) {
		t.Parallel()
		rig := newInferRig(t)
		quiet := rig.live(t, "tenant-1", "edge-quiet", "s-aaa", rig.answers(t, modelOutput))
		ready := rig.live(t, "tenant-1", "edge-ready", "s-zzz", rig.answers(t, modelOutput))
		rig.declare("tenant-1", "edge-ready", saysReady)
		rig.declare("tenant-1", "edge-ready", saysNothing) // un latido sin el campo no borra lo aprendido
		rig.requireWentThrough(t, "tenant-1", InferRequest{Prompt: "p"}, ready, quiet)
	})
}

// R-G13: con dos sesiones vivas decide el ORIGEN. La conversación que preguntó sale por SU
// Edge, no por el primero alfabético; sin origen, o con un origen que ya no tiene stream, sale
// por el primero alfabético —y no es un error—, con el origen intacto en el payload.
func TestInferOriginDecidesWithTwoLiveSessions(t *testing.T) {
	t.Parallel()
	rig := newInferRig(t)
	a := rig.live(t, "tenant-1", "edge-a", "s-a", rig.answers(t, modelOutput))
	b := rig.live(t, "tenant-1", "edge-b", "s-b", rig.answers(t, modelOutput))

	rig.requireWentThrough(t, "tenant-1", InferRequest{Prompt: "p", OriginSessionID: "s-b"}, b, a)
	rig.requireWentThrough(t, "tenant-1", InferRequest{Prompt: "p"}, a, b)
	rig.requireWentThrough(t, "tenant-1", InferRequest{Prompt: "p", OriginSessionID: "s-gone"}, a, b)
}

// El candidato vivo NO mira la readiness, y es doctrina: el Edge que sostiene la conversación
// es el único que la atiende. Si su Ollama está caído, lo correcto es su `ollama_down`
// honesto, no la respuesta de otra instalación que no ha visto ni un mensaje de esa charla.
func TestInferLiveOriginWinsEvenIfItsEdgeSaidDown(t *testing.T) {
	t.Parallel()
	rig := newInferRig(t)
	down := rig.live(t, "tenant-1", "edge-down", "s-down", rig.answers(t, modelOutput))
	ready := rig.live(t, "tenant-1", "edge-ready", "s-ready", rig.answers(t, modelOutput))
	rig.declare("tenant-1", "edge-down", saysDown)
	rig.declare("tenant-1", "edge-ready", saysReady)

	rig.requireWentThrough(t, "tenant-1", InferRequest{Prompt: "p", OriginSessionID: "s-down"}, down, ready)
	rig.requireWentThrough(t, "tenant-1", InferRequest{Prompt: "p", TargetSessionID: "s-down"}, down, ready)
}

// R-G13: el DESTINO manda sobre el origen y NO viaja en el payload. El calentamiento tiene que
// salir por un Edge concreto sin que lo haya originado ninguna conversación: el envelope lleva
// el destino, el payload sigue llevando el origen (o nada), y el id del destino no aparece en
// ningún campo del InferenceRequest.
func TestInferTargetBeatsOriginAndStaysOutOfThePayload(t *testing.T) {
	t.Parallel()
	rig := newInferRig(t)
	origin := rig.live(t, "tenant-1", "edge-a", "s-a-origin", rig.answers(t, modelOutput))
	target := rig.live(t, "tenant-1", "edge-b", "s-b-target", rig.answers(t, modelOutput))

	rig.requireWentThrough(t, "tenant-1", InferRequest{Prompt: "p", OriginSessionID: "s-a-origin", TargetSessionID: "s-b-target"}, target, origin)
	rig.requireWentThrough(t, "tenant-1", InferRequest{Prompt: "p", TargetSessionID: "s-b-target", Warmup: true}, target, origin)

	for _, f := range target.received() {
		payload, err := proto.Marshal(f.GetInferenceRequest())
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		if bytes.Contains(payload, []byte("s-b-target")) {
			t.Fatalf("el destino viajó en el payload: %v", f.GetInferenceRequest())
		}
	}

	// 🟡 Conducta del viejo, copiada: un destino SIN stream no cae al origen aunque el origen
	// esté vivo; cae directamente al reparto del tenant (aquí, la primera alfabética, que
	// resulta ser la de origen: por eso el caso de abajo usa un tercer Edge que la precede).
	first := rig.live(t, "tenant-1", "edge-0", "s-0-first", rig.answers(t, modelOutput))
	rig.requireWentThrough(t, "tenant-1", InferRequest{Prompt: "p", OriginSessionID: "s-b-target", TargetSessionID: "s-gone"}, first, origin, target)
}

// El reparto sin candidato es ESTABLE y del tenant: siempre la primera sesión en orden
// alfabético de las suyas —entre Edges y entre las varias de un Edge—, de modo que dos
// peticiones seguidas van al mismo Edge (su breaker y su modelo cargado significan algo). Las
// sesiones de otro tenant no cuentan aunque vayan antes.
func TestInferFallbackIsStableAndScopedToTheTenant(t *testing.T) {
	t.Parallel()
	rig := newInferRig(t)
	stranger := rig.live(t, "tenant-0", "edge-1", "s-000", rig.answers(t, modelOutput))
	c := rig.live(t, "tenant-1", "edge-1", "s-ccc", rig.answers(t, modelOutput))
	b := rig.live(t, "tenant-1", "edge-2", "s-bbb", rig.answers(t, modelOutput))
	d := rig.live(t, "tenant-1", "edge-2", "s-ddd", rig.answers(t, modelOutput))
	a := rig.live(t, "tenant-1", "edge-3", "s-aaa", rig.answers(t, modelOutput))

	rig.requireWentThrough(t, "tenant-1", InferRequest{Prompt: "p"}, a, b, c, d, stranger)

	// El orden es DENTRO de cada grupo: con edge-2 en READY gana su primera sesión, no s-aaa.
	rig.declare("tenant-1", "edge-2", saysReady)
	rig.requireWentThrough(t, "tenant-1", InferRequest{Prompt: "p"}, b, a, c, d, stranger)
	rig.requireWentThrough(t, "tenant-0", InferRequest{Prompt: "p"}, stranger, a, b, c, d)
}

// 🟡 Conducta del viejo, copiada y afirmada (no se corrige aquí): el candidato vivo NO se
// contrasta con el tenant. Si el llamante pasa como origen —o como destino— una sesión viva de
// OTRO tenant, el frame sale por ella. Hoy quien llama pasa la sesión de su propia
// conversación; el gateway no lo defiende.
func TestInferLiveCandidateIsNotCheckedAgainstTheTenant(t *testing.T) {
	t.Parallel()
	rig := newInferRig(t)
	own := rig.live(t, "tenant-1", "edge-1", "s-own", rig.answers(t, modelOutput))
	foreign := rig.live(t, "tenant-2", "edge-9", "s-foreign", rig.answers(t, modelOutput))

	rig.requireWentThrough(t, "tenant-1", InferRequest{Prompt: "p", OriginSessionID: "s-foreign"}, foreign, own)
	rig.requireWentThrough(t, "tenant-1", InferRequest{Prompt: "p", TargetSessionID: "s-foreign"}, foreign, own)
}

// T-5 (ADR-0048): el canal de control NO es una sesión que atienda inferencias. Un Edge con
// el operador dentro y CERO teléfonos no recibe el prompt —ni sin candidato, ni nombrando el
// canal de control como origen o como destino—: `__wapp_control__` es la misma clave en todos
// los Edge del planeta. En cuanto empareja un teléfono, la inferencia sale por esa sesión.
func TestInferNeverUsesTheControlChannel(t *testing.T) {
	t.Parallel()
	rig := newInferRig(t)
	edge := openStream(t, rig.srv, forgedIdentity("tenant-1", "edge-1"))
	edge.send(t, pongFrame(control, 1))

	for name, req := range map[string]InferRequest{
		"no candidate":      {Prompt: "p"},
		"control as origin": {Prompt: "p", OriginSessionID: control},
		"control as target": {Prompt: "p", TargetSessionID: control},
	} {
		rig.requireNobody(t, "tenant-1", req)
		for _, f := range edge.received() {
			if f.GetInferenceRequest() != nil {
				t.Fatalf("%s: el prompt salió por el cable del canal de control: %v", name, f)
			}
		}
	}

	// El control positivo: el mismo Edge, con un teléfono.
	edge.send(t, pongFrame("s-phone", 1))
	pushed := make(chan struct{}, 8)
	edge.onSend = func() { pushed <- struct{}{} }
	res := goInfer(context.Background(), rig.srv, "tenant-1", InferRequest{Prompt: "p", OriginSessionID: control})
	await(t, pushed, "que el InferenceRequest salga por el stream del Edge")
	ids := pendingInferIDs(rig.srv)
	if len(ids) != 1 {
		t.Fatalf("inferencias en vuelo = %v, se esperaba una", ids)
	}
	rig.srv.deliverInference(sealedResult(ids[0], rig.seal(t, modelOutput)))
	if got := await(t, res, "que la inferencia vuelva"); got.err != nil || got.out != modelOutput {
		t.Fatalf("Infer = (%q, %v)", got.out, got.err)
	}
	frames := edge.received()
	if frame := frames[len(frames)-1]; frame.GetInferenceRequest() == nil || frame.GetCommandId() != ids[0] || frame.GetSessionId() != "s-phone" {
		t.Fatalf("el frame no es el InferenceRequest dirigido a la sesión del teléfono: %v", frame)
	}
}

// «Sin candidato» es sin candidato: el origen vacío NO se busca en el Registry. Si alguien
// registrara un stream bajo el session_id vacío —hoy connect no lo hace—, la inferencia que no
// nombra sesión no sale por él: ni cuando el tenant tiene a quién preguntar, ni cuando no.
func TestInferWithoutCandidateNeverRoutesThroughABlankSession(t *testing.T) {
	t.Parallel()
	rig := newInferRig(t)
	answer := rig.answers(t, modelOutput)
	blank := &inferSession{id: "", pushed: make(chan string, 64)}
	t.Cleanup(rig.reg.Register("", funcSender(func(msg *cloudlinkv1.CloudToEdge) error {
		blank.mu.Lock()
		blank.frames = append(blank.frames, msg)
		blank.mu.Unlock()
		rig.srv.deliverInference(answer(msg.GetInferenceRequest()))
		return nil
	})))

	rig.requireNobody(t, "tenant-1", InferRequest{Prompt: "p"}, blank)

	own := rig.live(t, "tenant-1", "edge-1", "s-1", answer)
	rig.requireWentThrough(t, "tenant-1", InferRequest{Prompt: "p"}, own, blank)
}

// El reparto sin candidato lee la flota BAJO SU CANDADO: mientras otros Edge conectan, laten y
// se van, las inferencias siguen eligiendo. Sin el candado es una carrera sobre el mapa de
// sesiones, y quien la ve es el detector (-race).
func TestInferFallbackReadsTheFleetUnderItsLock(t *testing.T) {
	t.Parallel()
	rig := newInferRig(t)
	own := rig.live(t, "tenant-1", "edge-1", "s-1", rig.answers(t, modelOutput))

	stop, churned := make(chan struct{}), make(chan struct{})
	started := make(chan struct{}, 1)
	go func() {
		defer close(churned)
		other := phone("tenant-2", "edge-9", "s-churn")
		for {
			rig.srv.trackSession(other)
			rig.srv.noteReadiness(other, saysReady)
			rig.srv.untrackSession(other)
			select {
			case started <- struct{}{}:
			default:
			}
			select {
			case <-stop:
				return
			default:
			}
		}
	}()

	await(t, started, "que la flota empiece a moverse")
	for range 5 {
		rig.requireWentThrough(t, "tenant-1", InferRequest{Prompt: "p"}, own)
	}
	close(stop)
	await(t, churned, "que la flota deje de moverse")
}
