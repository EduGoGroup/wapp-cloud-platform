//go:build pendiente

package grpc

// El VOCABULARIO de la inferencia (R-G14): los motivos, las clases, los centinelas, el margen
// y la forma de InferRequest e InferError. Son textos y números que leen otros —el escritor
// de avisos, el Edge, el adaptador de llmvia—, así que se afirman LITERALES: el test vive en
// el lado del que los escribe, que es donde se escribiría el equivocado.

import (
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
)

// degradationReasons es el vocabulario de degradation.Reason, ESCRITO A MANO: `degradation`
// sigue en el árbol viejo (F4) y edge no lo importa. Lo que se puede escribir en
// owner_degradation_notices; un motivo del transporte que no esté aquí perdería su aviso en
// silencio.
var degradationReasons = []string{
	"ollama_down", "breaker_open", "edge_offline", "timeout",
	"api_error", "credencial", "lease_invalid", "edge_sin_capacidad",
}

// R-G13: el margen del Cloud sobre el plazo del Edge son 5 s. Está exportado porque el
// llamante tiene que reservarlo de su propio plazo: cambiarlo es cambiar esa aritmética.
func TestDefaultInferGraceIsFiveSeconds(t *testing.T) {
	t.Parallel()
	if DefaultInferGrace != 5*time.Second {
		t.Fatalf("DefaultInferGrace = %v, se esperaban 5s", DefaultInferGrace)
	}
}

// R-G14: los seis motivos, valor por valor. Son LITERALMENTE los de degradation.Reason: quien
// consume Motivo() lo convierte a motivo de aviso sin tabla de traducción.
func TestReasonsAreTheDegradationVocabulary(t *testing.T) {
	t.Parallel()
	want := map[string]string{
		ReasonOllamaDown:       "ollama_down",
		ReasonBreakerOpen:      "breaker_open",
		ReasonTimeout:          "timeout",
		ReasonLeaseInvalid:     "lease_invalid",
		ReasonEdgeSinCapacidad: "edge_sin_capacidad",
		ReasonEdgeOffline:      "edge_offline",
	}
	if len(want) != 6 {
		t.Fatalf("hay %d motivos distintos, se esperaban 6: dos constantes comparten valor", len(want))
	}
	for got, literal := range want {
		if got != literal {
			t.Errorf("motivo = %q, se esperaba el literal %q", got, literal)
		}
		if !slices.Contains(degradationReasons, got) {
			t.Errorf("el motivo %q no está en el vocabulario de degradación: su aviso se perdería", got)
		}
	}
}

// R-G14: el enum del proto no crece sin decidir su motivo. No se comprueba «que todos mapeen»
// (para eso está el default, que manda a ollama_down): se comprueba que el TAMAÑO sea el que se
// revisó a mano. Un valor nuevo obliga a pasar por aquí.
func TestTheProtoErrorEnumHasNotGrown(t *testing.T) {
	t.Parallel()
	const reviewed = 6 // UNSPECIFIED + los cinco errores nombrados
	if got := len(cloudlinkv1.InferenceError_name); got != reviewed {
		t.Fatalf("el enum InferenceError tiene %d valores y se revisaron %d: decide el motivo del nuevo", got, reviewed)
	}
}

// Las dos clases del campo `class` son vocabulario del CABLE: el Edge las lee como rótulo.
func TestClassesAreTheWireLabels(t *testing.T) {
	t.Parallel()
	if ClassInteractive != "interactivo" {
		t.Errorf("ClassInteractive = %q, se esperaba \"interactivo\"", ClassInteractive)
	}
	if ClassBatch != "lote" {
		t.Errorf("ClassBatch = %q, se esperaba \"lote\"", ClassBatch)
	}
}

// §5: los cuatro centinelas SIN motivo, texto por texto, con el prefijo gatewaygrpc: (T-15).
// Son cuatro valores distintos: ninguno «es» otro.
func TestInferenceSentinelsTextAndIdentity(t *testing.T) {
	t.Parallel()
	sentinels := []struct {
		err  error
		text string
	}{
		{ErrInferenceNoEncryptionKey, "gatewaygrpc: inferencia sellada pero la nube no tiene clave de cifrado"},
		{ErrInferenceSealedUnreadable, "gatewaygrpc: la salida sellada de la inferencia no se pudo leer"},
		{ErrInferenceNoOutput, "gatewaygrpc: InferenceResult sin salida ni error"},
		{ErrInferenceAbandoned, "gatewaygrpc: el llamante se rindió esperando la inferencia"},
	}
	for i, s := range sentinels {
		if s.err.Error() != s.text {
			t.Errorf("centinela %d = %q, se esperaba %q", i, s.err.Error(), s.text)
		}
		for j, other := range sentinels {
			if i != j && errors.Is(s.err, other.err) {
				t.Errorf("el centinela %d «es» el %d: tienen que ser distinguibles", i, j)
			}
		}
		// Ninguno trae motivo: son fallos de la nube o del llamante, y no se avisa al dueño.
		var withReason interface{ Motivo() string }
		if errors.As(s.err, &withReason) {
			t.Errorf("el centinela %d trae motivo %q", i, withReason.Motivo())
		}
	}
}

// El contrato por duck-typing: quien consume el error (llmvia) lo hace por las interfaces
// anónimas, no por el tipo. Si un método cambia de nombre o de firma, deja de cumplirlas —y
// allí nadie se enteraría—. (Como test y no como `var _`: hallazgo 42.)
func TestInferErrorSatisfiesTheDuckTypedContracts(t *testing.T) {
	t.Parallel()
	var e any = &InferError{}
	if _, ok := e.(interface{ Motivo() string }); !ok {
		t.Error("*InferError no expone Motivo() string")
	}
	if _, ok := e.(interface{ CommandID() string }); !ok {
		t.Error("*InferError no expone CommandID() string")
	}
	if _, ok := e.(interface{ SessionID() string }); !ok {
		t.Error("*InferError no expone SessionID() string")
	}
	if _, ok := e.(interface{ Unwrap() error }); !ok {
		t.Error("*InferError no expone Unwrap() error")
	}
	if _, ok := e.(error); !ok {
		t.Error("*InferError no es un error")
	}
}

// Un InferError sin rellenar no inventa nada: ni motivo, ni command_id, ni sesión, ni causa; y
// su texto conserva el prefijo gatewaygrpc: (T-15).
func TestInferErrorZeroValueInventsNothing(t *testing.T) {
	t.Parallel()
	e := &InferError{}
	if e.Motivo() != "" || e.CommandID() != "" || e.SessionID() != "" {
		t.Errorf("el InferError vacío trae (%q, %q, %q)", e.Motivo(), e.CommandID(), e.SessionID())
	}
	if e.Unwrap() != nil {
		t.Errorf("el InferError vacío trae causa: %v", e.Unwrap())
	}
	if got, want := e.Error(), "gatewaygrpc: inferencia  por la sesión : : <nil>"; got != want {
		t.Errorf("Error() = %q, se esperaba %q", got, want)
	}
}

// T-2: InferRequest es NOMINAL y el adaptador de llmvia lo convierte campo a campo. Estos son
// sus nueve campos y sus tipos; uno nuevo tiene que pasar por aquí y por el adaptador, o
// viajaría a cero sin ningún rojo.
func TestInferRequestHasTheNineFieldsTheBridgeConverts(t *testing.T) {
	t.Parallel()
	want := []struct{ name, typ string }{
		{"Prompt", "string"},
		{"Format", "string"},
		{"Temperature", "float64"},
		{"Timeout", "time.Duration"},
		{"OriginSessionID", "string"},
		{"TargetSessionID", "string"},
		{"MaxOutputTokens", "int32"},
		{"Class", "string"},
		{"Warmup", "bool"},
	}
	rt := reflect.TypeFor[InferRequest]()
	if rt.NumField() != len(want) {
		t.Fatalf("InferRequest tiene %d campos, se esperaban %d", rt.NumField(), len(want))
	}
	for i, w := range want {
		f := rt.Field(i)
		if f.Name != w.name || f.Type.String() != w.typ {
			t.Errorf("campo %d = %s %s, se esperaba %s %s", i, f.Name, f.Type, w.name, w.typ)
		}
	}
}
