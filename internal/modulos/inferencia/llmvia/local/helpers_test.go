//go:build pendiente

package local_test

// Los dobles que comparten los tests del paquete: el transporte falso, un error del cable
// con motivo y las entradas mínimas. La tabla de las cinco etapas (stageCalls) vive en
// local_test.go.

import (
	"context"
	"sync"
	"testing"

	"github.com/EduGoGroup/wapp-shared/llm"

	edgegrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/grpc"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/llmvia/local"
)

// fakeFrame es el doble del transporte: guarda cada petición que recibe (la última es la
// que miran casi todos los tests) y contesta lo que se le diga. Lleva candado porque un
// test lo comparte entre goroutines.
type fakeFrame struct {
	mu      sync.Mutex
	seen    []edgegrpc.InferRequest
	tenants []string
	out     string
	err     error
}

func (f *fakeFrame) Infer(_ context.Context, tenantID string, req edgegrpc.InferRequest) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, req)
	f.tenants = append(f.tenants, tenantID)
	return f.out, f.err
}

// calls dice cuántas peticiones llegaron al cable.
func (f *fakeFrame) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.seen)
}

// last devuelve la última petición, y falla el test si no hubo exactamente `want`.
func (f *fakeFrame) last(t *testing.T, want int) edgegrpc.InferRequest {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.seen) != want {
		t.Fatalf("peticiones al transporte = %d, quiero %d", len(f.seen), want)
	}
	return f.seen[len(f.seen)-1]
}

// transportError es un error del cable CON motivo, con la misma forma por la que se
// consume *edgegrpc.InferError: el método Motivo().
type transportError struct{ reason string }

func (e *transportError) Error() string  { return "el cable falló: " + e.reason }
func (e *transportError) Motivo() string { return e.reason }

// validOutput es una salida que ExtractJSON acepta tal cual.
const validOutput = `{"version":1}`

func newProvider(t *testing.T, f local.Frame, opts ...local.Option) *local.Provider {
	t.Helper()
	p, err := local.New(f, "tenant-1", opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p == nil {
		t.Fatal("New devolvió un Provider nil sin error")
	}
	return p
}

// catalogInput es un catálogo mínimo pero REAL: con ejemplos, porque el few-shot es lo que
// el prompt compartido usa y un catálogo pelado daría un prompt legal y distinto del de
// producción.
func catalogInput() llm.ClassifyRequestInput {
	return llm.ClassifyRequestInput{
		Text: "quiero 3 pizzas para el viernes",
		Catalog: []llm.IntentSpec{{
			Name:        "intake_request",
			Description: "el cliente pide productos",
			Examples:    []llm.IntentExample{{Message: "me mandas 2 gaseosas"}},
		}},
		UnknownLabel: "desconocido",
	}
}
