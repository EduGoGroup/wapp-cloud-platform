package grpc

// El despacho de la inferencia por la API exportada (R-G13): el frame que sale, lo que vuelve,
// y cada camino de salida de Infer con su motivo —o sin él— y sin dejar su entrada en la
// correlación. POR QUÉ STREAM sale (R-G15, la pareja ADR-0048) está en
// inference_dispatch_affinity_test.go. El Edge de estos tests contesta entregando el resultado
// por deliverInference, que es el camino exacto de un frame llegado por el stream.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/session"
)

// replyFunc decide qué contesta el Edge al InferenceRequest que recibe. Una función nil, o un
// resultado nil, es un Edge que se calla.
type replyFunc func(req *cloudlinkv1.InferenceRequest) *cloudlinkv1.InferenceResult

// inferSession es una sesión viva de un Edge: apunta los frames que le llegan y avisa por
// pushed con el command_id de cada uno.
type inferSession struct {
	id     string
	pushed chan string

	mu     sync.Mutex
	frames []*cloudlinkv1.CloudToEdge
}

func (s *inferSession) received() []*cloudlinkv1.CloudToEdge {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*cloudlinkv1.CloudToEdge(nil), s.frames...)
}

// only devuelve el único frame recibido, o falla.
func (s *inferSession) only(t *testing.T) *cloudlinkv1.CloudToEdge {
	t.Helper()
	frames := s.received()
	if len(frames) != 1 {
		t.Fatalf("la sesión %s recibió %d frames, se esperaba 1", s.id, len(frames))
	}
	return frames[0]
}

// live deja viva una sesión de ese Edge —con stream en el Registry y en el seguimiento del
// Server— cuyo Edge contesta con reply DENTRO del propio Send: la respuesta más rápida posible.
func (r *inferRig) live(t *testing.T, tenantID, edgeID, sessionID string, reply replyFunc) *inferSession {
	t.Helper()
	s := &inferSession{id: sessionID, pushed: make(chan string, 64)}
	t.Cleanup(r.reg.Register(sessionID, funcSender(func(msg *cloudlinkv1.CloudToEdge) error {
		s.mu.Lock()
		s.frames = append(s.frames, msg)
		s.mu.Unlock()
		if req := msg.GetInferenceRequest(); req != nil && reply != nil {
			if res := reply(req); res != nil {
				r.srv.deliverInference(res)
			}
		}
		s.pushed <- msg.GetCommandId()
		return nil
	})))
	r.srv.trackSession(phone(tenantID, edgeID, sessionID))
	return s
}

// answers es el Edge que devuelve esa salida, sellada hacia la nube.
func (r *inferRig) answers(t *testing.T, rawJSON string) replyFunc {
	t.Helper()
	enc := r.seal(t, rawJSON)
	return func(req *cloudlinkv1.InferenceRequest) *cloudlinkv1.InferenceResult {
		return sealedResult(req.GetCommandId(), enc)
	}
}

// goInfer lanza la inferencia y devuelve por dónde llega su resultado.
func goInfer(ctx context.Context, srv *Server, tenantID string, req InferRequest) <-chan inferResult {
	res := make(chan inferResult, 1)
	go func() {
		out, err := srv.Infer(ctx, tenantID, req)
		res <- inferResult{out, err}
	}()
	return res
}

// asInferError extrae el *InferError de err, o falla.
func asInferError(t *testing.T, err error) *InferError {
	t.Helper()
	var ie *InferError
	if !errors.As(err, &ie) {
		t.Fatalf("el error no es *InferError: %v", err)
	}
	return ie
}

// fullRequest pide todo lo que un llamante puede pedir.
var fullRequest = InferRequest{
	Prompt: "clasifica esto:\n«dos empanadas»", Format: `{"type":"object"}`, Temperature: 0.7,
	Timeout: 2 * time.Second, OriginSessionID: "s-origin-gone", MaxOutputTokens: 512,
	Class: ClassBatch, Warmup: true,
}

// R-G13: prompt entra, JSON crudo sale. Y el envelope del frame: command_id nuevo —el mismo
// en el envelope y en el payload—, el stream elegido en el envelope y la conversación de
// origen en el payload (aunque ya no tenga stream: es trazabilidad, no enrutado).
func TestInferSendsThePromptAndReturnsTheRawJSON(t *testing.T) {
	t.Parallel()
	rig := newInferRig(t)
	edge := rig.live(t, "tenant-1", "edge-1", "s-1", rig.answers(t, modelOutput))

	out, err := rig.srv.Infer(context.Background(), "tenant-1", fullRequest)

	if err != nil || out != modelOutput {
		t.Fatalf("Infer = (%q, %v), se esperaba la salida del modelo", out, err)
	}
	frame := edge.only(t)
	req := frame.GetInferenceRequest()
	if req == nil {
		t.Fatalf("el frame no es un InferenceRequest: %v", frame.GetPayload())
	}
	if !uuidV4.MatchString(frame.GetCommandId()) || req.GetCommandId() != frame.GetCommandId() {
		t.Errorf("command_id del envelope %q y del payload %q: se esperaba el mismo UUID v4", frame.GetCommandId(), req.GetCommandId())
	}
	if frame.GetSessionId() != "s-1" {
		t.Errorf("session_id del envelope = %q, se esperaba el stream elegido (s-1)", frame.GetSessionId())
	}
	if req.GetSessionId() != "s-origin-gone" {
		t.Errorf("session_id del payload = %q, se esperaba la conversación de origen, tal cual", req.GetSessionId())
	}
	requireNoPendingInfers(t, rig.srv)
}

// El payload lleva lo que pidió el llamante, verbatim: prompt, formato, temperatura y tope de
// salida con presencia, el plazo en milisegundos, la clase y la marca de calentamiento.
func TestInferPayloadCarriesWhatTheCallerAsked(t *testing.T) {
	t.Parallel()
	rig := newInferRig(t)
	edge := rig.live(t, "tenant-1", "edge-1", "s-1", rig.answers(t, modelOutput))

	if _, err := rig.srv.Infer(context.Background(), "tenant-1", fullRequest); err != nil {
		t.Fatalf("Infer = %v", err)
	}

	req := edge.only(t).GetInferenceRequest()
	if req.GetPrompt() != "clasifica esto:\n«dos empanadas»" || req.GetFormat() != `{"type":"object"}` {
		t.Errorf("el prompt o el formato no viajaron verbatim: %q / %q", req.GetPrompt(), req.GetFormat())
	}
	if req.Temperature == nil || req.GetTemperature() != float32(0.7) {
		t.Errorf("temperature = %v, se esperaba 0.7 con presencia", req.Temperature)
	}
	if req.GetTimeoutMs() != 2000 {
		t.Errorf("timeout_ms = %d, se esperaban 2000", req.GetTimeoutMs())
	}
	if req.MaxOutputTokens == nil || req.GetMaxOutputTokens() != 512 {
		t.Errorf("max_output_tokens = %v, se esperaban 512 con presencia", req.MaxOutputTokens)
	}
	if req.GetClass() != "lote" || !req.GetWarmup() {
		t.Errorf("class = %q, warmup = %v; se esperaba lote y calentamiento", req.GetClass(), req.GetWarmup())
	}
}

// El frame de lo que el llamante NO fija: la temperatura viaja SIEMPRE con presencia (0.0 es
// lo que más se pide y a la vez el cero del campo), el plazo no positivo son 30 s, y el tope de
// salida no positivo NO se pone (un puntero a 0 le pediría al Edge cero tokens).
func TestInferFrameOfWhatTheCallerDoesNotSet(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		req         InferRequest
		timeoutMs   int64
		wantsTokens bool
	}{
		{"only a prompt", InferRequest{Prompt: "p"}, 30_000, false},
		{"negative timeout and tokens", InferRequest{Prompt: "p", Timeout: -time.Second, MaxOutputTokens: -1}, 30_000, false},
		{"one token", InferRequest{Prompt: "p", Timeout: 1500 * time.Microsecond, MaxOutputTokens: 1}, 1, true},
		// 🟡 Conducta del viejo, copiada: un plazo positivo por debajo del milisegundo viaja
		// como timeout_ms = 0 (se trunca), no como 30 s.
		{"sub-millisecond timeout truncates to zero", InferRequest{Prompt: "p", Timeout: 999 * time.Microsecond}, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rig := newInferRig(t)
			edge := rig.live(t, "tenant-1", "edge-1", "s-1", rig.answers(t, "{}"))

			if _, err := rig.srv.Infer(context.Background(), "tenant-1", tc.req); err != nil {
				t.Fatalf("Infer = %v", err)
			}

			req := edge.only(t).GetInferenceRequest()
			if req.Temperature == nil || req.GetTemperature() != 0 {
				t.Errorf("temperature = %v, se esperaba 0.0 CON presencia", req.Temperature)
			}
			if req.GetTimeoutMs() != tc.timeoutMs {
				t.Errorf("timeout_ms = %d, se esperaban %d", req.GetTimeoutMs(), tc.timeoutMs)
			}
			if got := req.MaxOutputTokens != nil; got != tc.wantsTokens {
				t.Errorf("max_output_tokens presente = %v (%d), se esperaba %v", got, req.GetMaxOutputTokens(), tc.wantsTokens)
			}
			if req.GetSessionId() != "" || req.GetFormat() != "" || req.GetClass() != "" || req.GetWarmup() {
				t.Errorf("el frame trae algo que el llamante no pidió: %v", req)
			}
		})
	}
}

// Cada inferencia estrena command_id: dos llamadas seguidas no comparten correlación.
func TestInferCommandIDsAreFresh(t *testing.T) {
	t.Parallel()
	rig := newInferRig(t)
	edge := rig.live(t, "tenant-1", "edge-1", "s-1", rig.answers(t, "{}"))
	for range 2 {
		if _, err := rig.srv.Infer(context.Background(), "tenant-1", InferRequest{Prompt: "p"}); err != nil {
			t.Fatalf("Infer = %v", err)
		}
	}
	frames := edge.received()
	if len(frames) != 2 || frames[0].GetCommandId() == frames[1].GetCommandId() || !uuidV4.MatchString(frames[1].GetCommandId()) {
		t.Fatalf("command_ids = %q y %q, se esperaban dos UUID v4 distintos", frames[0].GetCommandId(), frames[1].GetCommandId())
	}
}

// R-G13: el error que NOMBRA el Edge vuelve con su motivo, en un *InferError que dice qué
// inferencia fue y por qué stream se pidió.
func TestInferErrorNamedByTheEdgeCarriesItsReason(t *testing.T) {
	t.Parallel()
	rig := newInferRig(t)
	edge := rig.live(t, "tenant-1", "edge-1", "s-1", func(req *cloudlinkv1.InferenceRequest) *cloudlinkv1.InferenceResult {
		return errorResult(req.GetCommandId(), cloudlinkv1.InferenceError_INFERENCE_ERROR_EDGE_SIN_CAPACIDAD)
	})

	out, err := rig.srv.Infer(context.Background(), "tenant-1", InferRequest{Prompt: "p", OriginSessionID: "s-origin-gone"})

	requireReason(t, err, ReasonEdgeSinCapacidad)
	if ie := asInferError(t, err); ie.CommandID() != edge.only(t).GetCommandId() || ie.SessionID() != "s-1" || out != "" {
		t.Errorf("el error dice (%q, %q) y trae salida %q; se esperaba el command_id del frame y la sesión s-1", ie.CommandID(), ie.SessionID(), out)
	}
	requireNoPendingInfers(t, rig.srv)
}

// R-G13, R3.4.d: sin sesión viva del tenant → edge_offline, envolviendo ErrSessionOffline, y
// SIN command_id ni sesión: no hubo a quién preguntar. No se envía nada a nadie —tampoco al
// Edge de otro tenant—.
func TestInferWithoutLiveSessionIsEdgeOffline(t *testing.T) {
	t.Parallel()
	rig := newInferRig(t)
	stranger := rig.live(t, "tenant-2", "edge-1", "s-stranger", rig.answers(t, modelOutput))

	out, err := rig.srv.Infer(context.Background(), "tenant-1", InferRequest{Prompt: "p"})

	requireReason(t, err, ReasonEdgeOffline)
	if !errors.Is(err, session.ErrSessionOffline) || out != "" {
		t.Errorf("Infer = (%q, %v), se esperaba ErrSessionOffline envuelto", out, err)
	}
	if ie := asInferError(t, err); ie.CommandID() != "" || ie.SessionID() != "" {
		t.Errorf("sin sesión a la que preguntar, el error trae (%q, %q)", ie.CommandID(), ie.SessionID())
	}
	const want = "gatewaygrpc: inferencia  por la sesión : edge_offline: sesión offline: el tenant no tiene ninguna sesión viva en esta réplica"
	if err.Error() != want {
		t.Errorf("Error() = %q, se esperaba %q", err.Error(), want)
	}
	if n := len(stranger.received()); n != 0 {
		t.Errorf("el Edge de otro tenant recibió %d frames", n)
	}
	requireNoPendingInfers(t, rig.srv)
}

// Los fallos del EMPUJE, cada uno con su motivo: la sesión elegida ya no tiene stream →
// edge_offline; el Edge no lee su stream → timeout (T-7: el empuje va por los dos relojes del
// Registry, así que un Edge atascado no cuelga a nadie).
func TestInferPushFailuresCarryAReason(t *testing.T) {
	t.Parallel()
	t.Run("chosen session has no stream any more", func(t *testing.T) {
		t.Parallel()
		rig := newInferRig(t)
		rig.srv.trackSession(phone("tenant-1", "edge-1", "s-gone")) // en el seguimiento, sin stream

		_, err := rig.srv.Infer(context.Background(), "tenant-1", InferRequest{Prompt: "p"})

		requireReason(t, err, ReasonEdgeOffline)
		ie := asInferError(t, err)
		if !errors.Is(err, session.ErrSessionOffline) || !uuidV4.MatchString(ie.CommandID()) || ie.SessionID() != "s-gone" {
			t.Errorf("Infer = %v, se esperaba ErrSessionOffline de la sesión s-gone con su command_id", err)
		}
		requireNoPendingInfers(t, rig.srv)
	})
	t.Run("edge does not read its stream", func(t *testing.T) {
		t.Parallel()
		rig := newInferRig(t, session.WithSendTimeout(time.Millisecond)) // el contrato ES el plazo del empuje
		stuck, entered := stuckStream(t)
		t.Cleanup(rig.reg.Register("s-stuck", stuck))
		rig.srv.trackSession(phone("tenant-1", "edge-1", "s-stuck"))

		_, err := rig.srv.Infer(context.Background(), "tenant-1", InferRequest{Prompt: "p"})

		await(t, entered, "que el frame entre al stream atascado")
		requireReason(t, err, ReasonTimeout)
		if !errors.Is(err, session.ErrPushTimeout) || errors.Is(err, ErrInferenceAbandoned) || asInferError(t, err).SessionID() != "s-stuck" {
			t.Errorf("Infer = %v, se esperaba ErrPushTimeout de la sesión s-stuck", err)
		}
		requireNoPendingInfers(t, rig.srv)
	})
	// 🟡 Conducta del viejo, copiada: cualquier fallo del empuje que no sea «sesión offline»
	// —también el error que devuelva el propio stream— se rotula timeout.
	t.Run("stream write error is labelled timeout", func(t *testing.T) {
		t.Parallel()
		rig := newInferRig(t)
		t.Cleanup(rig.reg.Register("s-broken", funcSender(func(*cloudlinkv1.CloudToEdge) error { return io.ErrClosedPipe })))
		rig.srv.trackSession(phone("tenant-1", "edge-1", "s-broken"))

		_, err := rig.srv.Infer(context.Background(), "tenant-1", InferRequest{Prompt: "p"})

		requireReason(t, err, ReasonTimeout)
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Errorf("Infer = %v, se esperaba la causa del stream", err)
		}
		requireNoPendingInfers(t, rig.srv)
	})
}

// R-G13: el llamante que se rinde NO tiene motivo, lo pille empujando o esperando. Empujando:
// ErrInferenceAbandoned junto a session.ErrPushAbandoned y al error de su ctx.
func TestInferCallerGivingUpDuringThePushHasNoReason(t *testing.T) {
	t.Parallel()
	rig := newInferRig(t) // plazo de empuje de producción: quien corta es el llamante
	stuck, _ := stuckStream(t)
	t.Cleanup(rig.reg.Register("s-stuck", stuck))
	rig.srv.trackSession(phone("tenant-1", "edge-1", "s-stuck"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	out, err := rig.srv.Infer(ctx, "tenant-1", InferRequest{Prompt: "p"})

	if !errors.Is(err, ErrInferenceAbandoned) || !errors.Is(err, session.ErrPushAbandoned) || !errors.Is(err, context.Canceled) || out != "" {
		t.Fatalf("Infer = (%q, %v), se esperaba el abandono del llamante", out, err)
	}
	requireNoReason(t, err)
	requireNoPendingInfers(t, rig.srv)
}

// Esperando: el frame ya salió y el Edge calla; el llamante cancela y vuelve en el acto, sin
// motivo, aunque al presupuesto del Cloud le queden 35 s.
func TestInferCallerGivingUpWhileWaitingHasNoReason(t *testing.T) {
	t.Parallel()
	rig := newInferRig(t)
	edge := rig.live(t, "tenant-1", "edge-1", "s-1", nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res := goInfer(ctx, rig.srv, "tenant-1", InferRequest{Prompt: "p"})

	await(t, edge.pushed, "que el frame llegue al Edge")
	cancel()

	got := await(t, res, "que Infer vuelva cuando el llamante se rinde")
	if !errors.Is(got.err, ErrInferenceAbandoned) || !errors.Is(got.err, context.Canceled) || got.out != "" {
		t.Fatalf("Infer = (%q, %v), se esperaba el abandono del llamante", got.out, got.err)
	}
	requireNoReason(t, got.err)
	requireNoPendingInfers(t, rig.srv)
}

// R-G13: el presupuesto del Cloud vence → timeout, con el command_id del frame que salió. El
// resultado que llegue DESPUÉS es huérfano: no rompe nada.
func TestInferOwnBudgetExpiresAsTimeout(t *testing.T) {
	t.Parallel()
	rig := newInferRig(t)
	rig.srv.inferGrace = time.Millisecond // el contrato ES el plazo: plazo + margen = 2 ms
	edge := rig.live(t, "tenant-1", "edge-1", "s-1", nil)

	out, err := rig.srv.Infer(context.Background(), "tenant-1", InferRequest{Prompt: "p", Timeout: time.Millisecond})

	requireReason(t, err, ReasonTimeout)
	cmdID := edge.only(t).GetCommandId()
	if ie := asInferError(t, err); ie.CommandID() != cmdID || ie.SessionID() != "s-1" || out != "" {
		t.Errorf("el error dice (%q, %q); se esperaba (%s, s-1)", ie.CommandID(), ie.SessionID(), cmdID)
	}
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrInferenceAbandoned) {
		t.Errorf("el presupuesto vencido volvió como %v", err)
	}
	requireNoPendingInfers(t, rig.srv)

	rig.srv.deliverInference(sealedResult(cmdID, rig.seal(t, modelOutput)))
	requireLogHas(t, rig.log, `msg="inferencia sin petición pendiente"`, "command_id="+cmdID)
	requireNoPendingInfers(t, rig.srv)
}

// R-G13: los fallos de la nube no traen motivo, tampoco de extremo a extremo. Una salida
// sellada con la nube sin clave de cifrado es ErrInferenceNoEncryptionKey a secas.
func TestInferCloudFaultHasNoReason(t *testing.T) {
	t.Parallel()
	rig := newInferRig(t)
	edge := rig.live(t, "tenant-1", "edge-1", "s-1", rig.answers(t, modelOutput))
	rig.srv.cloudEncPriv = nil

	out, err := rig.srv.Infer(context.Background(), "tenant-1", InferRequest{Prompt: "p"})

	if !errors.Is(err, ErrInferenceNoEncryptionKey) || out != "" || len(edge.received()) != 1 {
		t.Fatalf("Infer = (%q, %v), se esperaba ErrInferenceNoEncryptionKey", out, err)
	}
	requireNoReason(t, err)
	requireNoPendingInfers(t, rig.srv)
}

// R-G13: «stream caído despierta en el acto» y «la entrada pendiente no se fuga». Con la
// inferencia en vuelo hay UNA entrada, bajo el command_id del frame y con la sesión del STREAM
// ELEGIDO —no la de origen—: es por la que la caída de ese stream la encuentra. Cuando cae,
// Infer vuelve ya con edge_offline (al presupuesto le quedan 35 s) y la entrada desaparece.
func TestInferInFlightWakesWhenItsStreamFalls(t *testing.T) {
	t.Parallel()
	rig := newInferRig(t)
	edge := rig.live(t, "tenant-1", "edge-1", "s-wire", nil)
	res := goInfer(context.Background(), rig.srv, "tenant-1", InferRequest{Prompt: "p", OriginSessionID: "s-origin-gone"})

	cmdID := await(t, edge.pushed, "que el frame llegue al Edge")
	if ids := pendingInferIDs(rig.srv); len(ids) != 1 || ids[0] != cmdID {
		t.Fatalf("con la inferencia en vuelo la correlación tiene %v, se esperaba [%s]", ids, cmdID)
	}
	if n := rig.srv.cancelSessionInfers("s-origin-gone"); n != 0 {
		t.Fatalf("la caída de la sesión de ORIGEN canceló %d inferencias: la entrada no lleva la sesión del stream", n)
	}
	if n := rig.srv.cancelSessionInfers("s-wire"); n != 1 {
		t.Fatalf("la caída del stream elegido canceló %d inferencias, se esperaba 1", n)
	}

	got := await(t, res, "que la inferencia en vuelo deje de esperar al caer su stream")
	requireReason(t, got.err, ReasonEdgeOffline)
	if ie := asInferError(t, got.err); !errors.Is(got.err, ErrStreamClosed) || ie.CommandID() != cmdID || ie.SessionID() != "s-wire" {
		t.Errorf("Infer = %v, se esperaba ErrStreamClosed de (%s, s-wire)", got.err, cmdID)
	}
	requireNoPendingInfers(t, rig.srv)
}

// Infer es seguro en concurrencia: muchas inferencias a la vez por el mismo stream, y cada una
// recibe SU salida (la correlación es por command_id, no por orden de llegada).
func TestInferConcurrentCallsEachGetTheirOwnOutput(t *testing.T) {
	t.Parallel()
	const n = 32
	rig := newInferRig(t)
	rig.live(t, "tenant-1", "edge-1", "s-1", func(req *cloudlinkv1.InferenceRequest) *cloudlinkv1.InferenceResult {
		return sealedResult(req.GetCommandId(), rig.seal(t, "salida de "+req.GetPrompt()))
	})

	results := make([]<-chan inferResult, n)
	for i := range n {
		results[i] = goInfer(context.Background(), rig.srv, "tenant-1", InferRequest{Prompt: fmt.Sprintf("p-%d", i)})
	}
	for i, res := range results {
		got := await(t, res, "que cada inferencia vuelva")
		if want := fmt.Sprintf("salida de p-%d", i); got.err != nil || got.out != want {
			t.Errorf("la inferencia %d volvió con (%q, %v), se esperaba %q", i, got.out, got.err, want)
		}
	}
	requireNoPendingInfers(t, rig.srv)
}
