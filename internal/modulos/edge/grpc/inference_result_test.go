package grpc

// La espera y la lectura del resultado de una inferencia (R-G13, la mitad del resultado):
// awaitInference, readInference, openInference y la traducción del enum del frame. Nace con
// el verde: inference_result.go no tiene exportados (T-17) y se prueba por dentro. La
// correlación de las inferencias en vuelo está en inference_result_pending_test.go.

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-shared/envelope"
	"google.golang.org/protobuf/proto"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/session"
)

// La salida del modelo de los tests. De mentira, y lo que ningún log puede contener.
const modelOutput = `{"version":1,"intent":"pedido","texto":"dos empanadas para Juana"}`

// inferRig es un Server con la privada de cifrado de tránsito de la nube y la pública con la
// que el Edge sella sus salidas. Las claves nacen en el test.
type inferRig struct {
	srv *Server
	reg *session.Registry
	log *logBuffer
	pub []byte
}

func newInferRig(t *testing.T, regOpts ...session.RegistryOption) *inferRig {
	t.Helper()
	pub, priv, err := envelope.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	log, buf := debugLog()
	reg := session.NewRegistry(regOpts...)
	return &inferRig{srv: New(reg, log, WithCloudEncPrivKey(priv)), reg: reg, log: buf, pub: pub}
}

// sealBytes sella esos bytes hacia la pública de la nube, como hace el Edge.
func (r *inferRig) sealBytes(t *testing.T, raw []byte) []byte {
	t.Helper()
	sealed, err := envelope.SealFor(r.pub, raw)
	if err != nil {
		t.Fatalf("SealFor: %v", err)
	}
	return sealed
}

// seal sella un InferenceOutput con ese raw_json.
func (r *inferRig) seal(t *testing.T, rawJSON string) []byte {
	t.Helper()
	raw, err := proto.Marshal(&cloudlinkv1.InferenceOutput{RawJson: rawJSON})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return r.sealBytes(t, raw)
}

func sealedResult(cmdID string, enc []byte) *cloudlinkv1.InferenceResult {
	return &cloudlinkv1.InferenceResult{CommandId: cmdID, Result: &cloudlinkv1.InferenceResult_EncOutput{EncOutput: enc}}
}

func errorResult(cmdID string, e cloudlinkv1.InferenceError) *cloudlinkv1.InferenceResult {
	return &cloudlinkv1.InferenceResult{CommandId: cmdID, Result: &cloudlinkv1.InferenceResult_Error{Error: e}}
}

// arrived es el canal de una inferencia cuyo resultado YA llegó (o, con nil, cuyo stream ya cayó).
func arrived(res *cloudlinkv1.InferenceResult) <-chan *cloudlinkv1.InferenceResult {
	ch := make(chan *cloudlinkv1.InferenceResult, 1)
	if res == nil {
		close(ch)
	} else {
		ch <- res
	}
	return ch
}

// requireReason afirma el motivo del error POR LA INTERFAZ ANÓNIMA, que es como lo consume el
// escritor de avisos: probarlo por el tipo concreto dejaría sin red el camino que se usa.
func requireReason(t *testing.T, err error, want string) {
	t.Helper()
	var withReason interface{ Motivo() string }
	if !errors.As(err, &withReason) {
		t.Fatalf("el error no expone Motivo(): %v", err)
	}
	if withReason.Motivo() != want {
		t.Fatalf("motivo = %q, se esperaba %q (%v)", withReason.Motivo(), want, err)
	}
}

// requireNoReason afirma que el error NO trae motivo: no es una degradación de la vía y al
// dueño no se le avisa.
func requireNoReason(t *testing.T, err error) {
	t.Helper()
	var withReason interface{ Motivo() string }
	if errors.As(err, &withReason) {
		t.Fatalf("el error trae motivo %q y no debía traer ninguno: %v", withReason.Motivo(), err)
	}
}

type inferResult struct {
	out string
	err error
}

// R-G13: JSON crudo sale. La salida sellada se abre y se devuelve TAL CUAL la produjo el
// modelo —sin parsear ni validar: si no es JSON, eso exactamente es lo que llega arriba—, y
// una salida vacía es una salida, no un error.
func TestAwaitInferenceReturnsTheModelOutputVerbatim(t *testing.T) {
	t.Parallel()
	for name, raw := range map[string]string{
		"json":           modelOutput,
		"not json":       "Claro, aquí tienes: {\"intent\": ",
		"blank and utf8": "  \n\t«ñandú» ",
		"empty":          "",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rig := newInferRig(t)
			out, err := rig.srv.awaitInference(context.Background(), arrived(sealedResult("cmd-1", rig.seal(t, raw))), "cmd-1", "s-1", time.Second)
			if err != nil {
				t.Fatalf("awaitInference = %v", err)
			}
			if out != raw {
				t.Fatalf("salida = %q, se esperaba %q sin tocar", out, raw)
			}
		})
	}
}

// R-G13: cada error del frame trae SU motivo (1:1 con el enum del proto), en un *InferError
// con el command_id y la sesión, y el nombre del enum como causa.
func TestAwaitInferenceNamedErrorCarriesItsReason(t *testing.T) {
	t.Parallel()
	for e, reason := range map[cloudlinkv1.InferenceError]string{
		cloudlinkv1.InferenceError_INFERENCE_ERROR_OLLAMA_DOWN:        "ollama_down",
		cloudlinkv1.InferenceError_INFERENCE_ERROR_BREAKER_OPEN:       "breaker_open",
		cloudlinkv1.InferenceError_INFERENCE_ERROR_TIMEOUT:            "timeout",
		cloudlinkv1.InferenceError_INFERENCE_ERROR_LEASE_INVALID:      "lease_invalid",
		cloudlinkv1.InferenceError_INFERENCE_ERROR_EDGE_SIN_CAPACIDAD: "edge_sin_capacidad",
	} {
		t.Run(e.String(), func(t *testing.T) {
			t.Parallel()
			rig := newInferRig(t)
			out, err := rig.srv.awaitInference(context.Background(), arrived(errorResult("cmd-1", e)), "cmd-1", "s-1", time.Second)
			if out != "" {
				t.Errorf("un error del Edge trajo salida %q", out)
			}
			requireReason(t, err, reason)
			var ie *InferError
			if !errors.As(err, &ie) || ie.CommandID() != "cmd-1" || ie.SessionID() != "s-1" {
				t.Fatalf("el error no es un *InferError de (cmd-1, s-1): %v", err)
			}
			if want := "gatewaygrpc: inferencia cmd-1 por la sesión s-1: " + reason + ": " + e.String(); err.Error() != want {
				t.Errorf("Error() = %q, se esperaba %q", err.Error(), want)
			}
			if errors.Is(err, ErrInferenceAbandoned) || errors.Is(err, ErrStreamClosed) {
				t.Errorf("un error nombrado por el Edge se confunde con otro desenlace: %v", err)
			}
		})
	}
}

// El error nombrado va EN CLARO y se lee aunque la nube no pueda abrir sobres: el Cloud tiene
// que poder decidir su degradación cuando el sellado es justo lo que falla.
func TestAwaitInferenceNamedErrorNeedsNoEncryptionKey(t *testing.T) {
	t.Parallel()
	srv := New(session.NewRegistry(), quietLog()) // sin clave de cifrado
	res := errorResult("cmd-1", cloudlinkv1.InferenceError_INFERENCE_ERROR_BREAKER_OPEN)
	_, err := srv.awaitInference(context.Background(), arrived(res), "cmd-1", "s-1", time.Second)
	requireReason(t, err, ReasonBreakerOpen)
}

// R-G13: stream caído despierta EN EL ACTO. Con el canal cerrado, la espera vuelve con
// edge_offline sin consumir su presupuesto —aquí el de producción, 30 s + 5 s— y deja el
// rastro con el command_id y la sesión.
func TestAwaitInferenceClosedChannelIsEdgeOfflineAtOnce(t *testing.T) {
	t.Parallel()
	rig := newInferRig(t)
	done := make(chan inferResult, 1)
	go func() {
		out, err := rig.srv.awaitInference(context.Background(), arrived(nil), "cmd-1", "s-1", 0)
		done <- inferResult{out, err}
	}()

	got := await(t, done, "que la espera despierte al cerrarse su canal")
	requireReason(t, got.err, ReasonEdgeOffline)
	if !errors.Is(got.err, ErrStreamClosed) || errors.Is(got.err, context.DeadlineExceeded) || errors.Is(got.err, ErrInferenceAbandoned) {
		t.Errorf("el stream caído volvió como %v, se esperaba la causa ErrStreamClosed", got.err)
	}
	var ie *InferError
	if !errors.As(got.err, &ie) || ie.CommandID() != "cmd-1" || ie.SessionID() != "s-1" || got.out != "" {
		t.Errorf("el error no es un *InferError de (cmd-1, s-1) sin salida: %v", got.err)
	}
	requireLogHas(t, rig.log, `level=WARN msg="inferencia: cancelada porque el stream de la sesión cayó"`, "command_id=cmd-1", "session_id=s-1")
}

// R-G13: el presupuesto del Cloud vence → motivo timeout, con causa DeadlineExceeded y SIN
// confundirse con el llamante que se rinde (su ctx sigue vivo). El rastro dice el presupuesto
// entero: el plazo del Edge MÁS el margen.
func TestAwaitInferenceOwnBudgetExpiresAsTimeout(t *testing.T) {
	t.Parallel()
	rig := newInferRig(t)
	rig.srv.inferGrace = 2 * time.Millisecond

	_, err := rig.srv.awaitInference(context.Background(), make(chan *cloudlinkv1.InferenceResult), "cmd-1", "s-1", 3*time.Millisecond)

	requireReason(t, err, ReasonTimeout)
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrInferenceAbandoned) || errors.Is(err, ErrStreamClosed) {
		t.Errorf("el presupuesto vencido volvió como %v", err)
	}
	var ie *InferError
	if !errors.As(err, &ie) || ie.CommandID() != "cmd-1" || ie.SessionID() != "s-1" {
		t.Errorf("el error no es un *InferError de (cmd-1, s-1): %v", err)
	}
	requireLogHas(t, rig.log, `level=WARN msg="inferencia: se agotó el presupuesto del Cloud sin respuesta del Edge"`,
		"command_id=cmd-1", "session_id=s-1", "budget=5ms")
}

// Los DOS sumandos del presupuesto cuentan: el plazo del Edge y el margen. Con uno de ellos
// larguísimo y el otro mínimo, la espera NO vence: sigue ahí cuando llega el resultado. Y un
// plazo no positivo son 30 s, no «ya» (D-F2-10).
//
// La negativa («no venció») no admite prueba determinista sin reloj: al código bueno no le
// puede fallar, y al que olvide un sumando se le da ocasión de sobra antes de entregar.
func TestAwaitInferenceWaitsTheTimeoutPlusTheGrace(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct{ timeout, grace time.Duration }{
		"long timeout, tiny grace":  {time.Hour, time.Nanosecond},
		"tiny timeout, long grace":  {time.Nanosecond, time.Hour},
		"zero timeout is 30s":       {0, time.Nanosecond},
		"negative timeout is 30s":   {-time.Hour, time.Nanosecond},
		"production grace suffices": {time.Nanosecond, DefaultInferGrace},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rig := newInferRig(t)
			rig.srv.inferGrace = tc.grace
			ch := make(chan *cloudlinkv1.InferenceResult, 1)
			done := make(chan inferResult, 1)
			go func() {
				out, err := rig.srv.awaitInference(context.Background(), ch, "cmd-1", "s-1", tc.timeout)
				done <- inferResult{out, err}
			}()

			letRun()
			select {
			case got := <-done:
				t.Fatalf("la espera venció sola antes de su presupuesto: %v", got.err)
			case <-time.After(20 * time.Millisecond): // el contrato ES un plazo: no vence en 20 ms
			}
			ch <- sealedResult("cmd-1", rig.seal(t, modelOutput))

			if got := await(t, done, "que la espera vuelva con el resultado"); got.err != nil || got.out != modelOutput {
				t.Fatalf("la espera volvió con (%q, %v), se esperaba la salida del modelo", got.out, got.err)
			}
		})
	}
}

// R-G13: el llamante que se rinde NO tiene motivo. Su ctx —cancelado o vencido— viaja junto a
// ErrInferenceAbandoned (doble %w): «el llamante se rindió» y «el Edge no contestó» son fallos
// distintos, y de esa distinción depende que se avise o no al dueño.
func TestAwaitInferenceCallerGivingUpHasNoReason(t *testing.T) {
	t.Parallel()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, cancelExpired := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer cancelExpired()

	for name, tc := range map[string]struct {
		ctx   context.Context
		cause error
		text  string
	}{
		"cancelled": {cancelled, context.Canceled, "gatewaygrpc: el llamante se rindió esperando la inferencia: inferencia cmd-1: context canceled"},
		"expired":   {expired, context.DeadlineExceeded, "gatewaygrpc: el llamante se rindió esperando la inferencia: inferencia cmd-1: context deadline exceeded"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rig := newInferRig(t) // presupuesto de producción: quien corta es el llamante
			out, err := rig.srv.awaitInference(tc.ctx, make(chan *cloudlinkv1.InferenceResult), "cmd-1", "s-1", 0)
			if !errors.Is(err, ErrInferenceAbandoned) || !errors.Is(err, tc.cause) || out != "" {
				t.Fatalf("el llamante rendido volvió con (%q, %v)", out, err)
			}
			requireNoReason(t, err)
			if err.Error() != tc.text {
				t.Errorf("Error() = %q, se esperaba %q", err.Error(), tc.text)
			}
			if rig.log.contains("se agotó el presupuesto del Cloud") {
				t.Error("el llamante rendido se anotó como presupuesto del Cloud vencido")
			}
		})
	}
}

// R-G13: los fallos de la NUBE o del protocolo —sin clave, sobre ilegible, oneof vacío— NO
// traen motivo: avisar al dueño con cualquiera de los seis sería mandarlo a mirar su equipo,
// que está bien. Se ven en el log de ERROR, con tamaños y nunca con contenido.
func TestReadInferenceCloudFaultsHaveNoReason(t *testing.T) {
	t.Parallel()
	rig := newInferRig(t)
	notAProto := rig.sealBytes(t, []byte{0xff, 0xff, 0xff})
	cases := []struct {
		name    string
		withKey bool
		res     *cloudlinkv1.InferenceResult
		want    error
		logLine string
	}{
		{"nil result", true, nil, ErrInferenceNoOutput, ""},
		{"empty oneof", true, &cloudlinkv1.InferenceResult{CommandId: "cmd-1"}, ErrInferenceNoOutput, ""},
		{"empty sealed output", true, sealedResult("cmd-1", nil), ErrInferenceNoOutput, ""},
		{"error branch left unspecified", true, errorResult("cmd-1", cloudlinkv1.InferenceError_INFERENCE_ERROR_UNSPECIFIED), ErrInferenceNoOutput, ""},
		{"no encryption key", false, sealedResult("cmd-1", rig.seal(t, modelOutput)), ErrInferenceNoEncryptionKey,
			`level=ERROR msg="inferencia: salida sellada pero la nube no tiene clave de cifrado"`},
		{"corrupt envelope", true, sealedResult("cmd-1", []byte("no es un sobre")), ErrInferenceSealedUnreadable,
			`level=ERROR msg="inferencia: no se pudo abrir la salida sellada"`},
		{"sealed for another key", true, sealedResult("cmd-1", newInferRig(t).seal(t, modelOutput)), ErrInferenceSealedUnreadable,
			`level=ERROR msg="inferencia: no se pudo abrir la salida sellada"`},
		{"opens but is not an InferenceOutput", true, sealedResult("cmd-1", notAProto), ErrInferenceSealedUnreadable,
			`level=ERROR msg="inferencia: la salida sellada abrió pero no deserializa"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			log, logs := capturedLog()
			srv := New(session.NewRegistry(), log)
			if tc.withKey {
				srv.cloudEncPriv = rig.srv.cloudEncPriv
			}

			out, err := srv.readInference(tc.res, "cmd-1", "s-1")

			if !errors.Is(err, tc.want) || out != "" {
				t.Fatalf("readInference = (%q, %v), se esperaba %v", out, err, tc.want)
			}
			requireNoReason(t, err)
			for _, other := range []error{ErrInferenceNoOutput, ErrInferenceNoEncryptionKey, ErrInferenceSealedUnreadable, ErrInferenceAbandoned} {
				if !errors.Is(tc.want, other) && errors.Is(err, other) {
					t.Errorf("el fallo se confunde con %v: %v", other, err)
				}
			}
			if tc.logLine == "" {
				if logs.String() != "" {
					t.Errorf("un oneof vacío dejó log: %q", logs.String())
				}
				return
			}
			requireLogHas(t, logs, tc.logLine, "command_id=cmd-1", "session_id=s-1")
			if logs.contains("pedido") || logs.contains("Juana") {
				t.Errorf("el log de error contiene la salida del modelo: %q", logs.String())
			}
		})
	}
}

// La evidencia del sellado en tránsito son TAMAÑOS, nunca contenido: la salida del modelo
// puede llevar texto literal del cliente.
func TestOpenInferenceLogsSizesNeverContent(t *testing.T) {
	t.Parallel()
	rig := newInferRig(t)
	enc := rig.seal(t, modelOutput)

	out, err := rig.srv.openInference(enc, "cmd-1", "s-1")

	if err != nil || out != modelOutput {
		t.Fatalf("openInference = (%q, %v)", out, err)
	}
	requireLogHas(t, rig.log, `level=INFO msg="inferencia: salida sellada abierta"`, "command_id=cmd-1", "session_id=s-1",
		"enc_output_bytes="+strconv.Itoa(len(enc)), "raw_json_len="+strconv.Itoa(len(modelOutput)))
	for _, secret := range []string{"pedido", "empanadas", "Juana"} {
		if rig.log.contains(secret) {
			t.Errorf("el log contiene %q, que viajaba sellado: %q", secret, rig.log.String())
		}
	}
}

// R-G14: la traducción del enum del proto, valor por valor. Los cinco errores nombrados dan
// cinco motivos DISTINTOS; UNSPECIFIED y un valor desconocido —un Edge más nuevo que esta
// nube— caen a ollama_down, nunca a un motivo inventado.
func TestReasonOfFrameMapsEveryProtoValue(t *testing.T) {
	t.Parallel()
	want := map[cloudlinkv1.InferenceError]string{
		cloudlinkv1.InferenceError_INFERENCE_ERROR_UNSPECIFIED:        "ollama_down",
		cloudlinkv1.InferenceError_INFERENCE_ERROR_OLLAMA_DOWN:        "ollama_down",
		cloudlinkv1.InferenceError_INFERENCE_ERROR_BREAKER_OPEN:       "breaker_open",
		cloudlinkv1.InferenceError_INFERENCE_ERROR_TIMEOUT:            "timeout",
		cloudlinkv1.InferenceError_INFERENCE_ERROR_LEASE_INVALID:      "lease_invalid",
		cloudlinkv1.InferenceError_INFERENCE_ERROR_EDGE_SIN_CAPACIDAD: "edge_sin_capacidad",
		cloudlinkv1.InferenceError(99):                                "ollama_down",
		cloudlinkv1.InferenceError(-1):                                "ollama_down",
	}
	for e, reason := range want {
		if got := reasonOfFrame(e); got != reason {
			t.Errorf("reasonOfFrame(%v) = %q, se esperaba %q", e, got, reason)
		}
	}
	// Ningún valor del enum generado queda sin revisar aquí, ni produce un motivo de fuera del
	// vocabulario.
	for v := range cloudlinkv1.InferenceError_name {
		e := cloudlinkv1.InferenceError(v)
		if _, reviewed := want[e]; !reviewed {
			t.Errorf("el valor %v del proto no está en la tabla de este test", e)
		}
		if got := reasonOfFrame(e); !slices.Contains(inferenceReasons, got) {
			t.Errorf("reasonOfFrame(%v) = %q, que no está en el vocabulario", e, got)
		}
	}
	// edge_offline es el ÚNICO motivo que no viene del Edge: ningún valor del frame lo produce.
	for e, reason := range want {
		if reason == ReasonEdgeOffline || strings.Contains(reasonOfFrame(e), "offline") {
			t.Errorf("el valor %v del frame produce edge_offline, que solo decide el Cloud", e)
		}
	}
}
