//go:build pendiente

package admin_test

// durable_flow_test.go — el adaptador EngineDurableFlowChecker, con dobles de sus dos
// puertos estrechos: lo que se prueba es QUÉ pregunta, a quién y qué hace con cada
// respuesta. Que el motor y los repositorios reales encajan en los puertos se afirma
// en compilación.

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/admin"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
)

// Los puertos los satisfacen los tipos reales SIN adaptador.
var (
	_ admin.DurableFlowChecker   = (*admin.EngineDurableFlowChecker)(nil)
	_ admin.FlowDefinitionReader = (*store.MemoryRepository)(nil)
	_ admin.FlowDefinitionReader = (*store.PostgresRepository)(nil)
	_ admin.DurableFlowEngine    = (*engine.Engine)(nil)
)

type ctxMarkerKey struct{}

// fakeDefinitionReader devuelve una definición o un error fijos y apunta con qué se
// le llamó.
type fakeDefinitionReader struct {
	flow model.Flow
	err  error

	calls     int
	gotMarker any
	gotTenant string
	gotFlowID string
}

func (f *fakeDefinitionReader) LatestDefinition(ctx context.Context, tenantID, flowID string) (model.Flow, error) {
	f.calls++
	f.gotMarker = ctx.Value(ctxMarkerKey{})
	f.gotTenant, f.gotFlowID = tenantID, flowID
	return f.flow, f.err
}

// fakeDurableEngine contesta siempre lo mismo y apunta la definición que le pasaron.
type fakeDurableEngine struct {
	answer bool

	calls int
	got   model.Flow
}

func (f *fakeDurableEngine) FlowProducesDurableContent(flow model.Flow) bool {
	f.calls++
	f.got = flow
	return f.answer
}

func cartFlow() model.Flow {
	return model.Flow{
		FlowID: "carrito-durable", Version: 3, Initial: "cart",
		Nodes: map[string]model.Node{"cart": {Type: "cart"}},
	}
}

func TestNewEngineDurableFlowChecker_ReadsNothingAtConstruction(t *testing.T) {
	t.Parallel()
	reader := &fakeDefinitionReader{flow: cartFlow()}
	eng := &fakeDurableEngine{answer: true}

	checker := admin.NewEngineDurableFlowChecker(reader, eng)
	if checker == nil {
		t.Fatal("NewEngineDurableFlowChecker devolvió nil")
	}
	if reader.calls != 0 || eng.calls != 0 {
		t.Errorf("al construir: lecturas=%d, consultas al motor=%d; quiero 0 y 0", reader.calls, eng.calls)
	}
}

// Con la definición leída, la respuesta es la del motor para ESA definición.
func TestEngineDurableFlowChecker_FlowHasDurableContent_AsksTheEngine(t *testing.T) {
	t.Parallel()
	for _, answer := range []bool{true, false} {
		t.Run(fmt.Sprintf("engine says %t", answer), func(t *testing.T) {
			t.Parallel()
			reader := &fakeDefinitionReader{flow: cartFlow()}
			eng := &fakeDurableEngine{answer: answer}
			ctx := context.WithValue(context.Background(), ctxMarkerKey{}, "ctx del llamante")

			got, err := admin.NewEngineDurableFlowChecker(reader, eng).FlowHasDurableContent(ctx, "tenant-a", "carrito-durable")
			if err != nil {
				t.Fatalf("FlowHasDurableContent: %v", err)
			}
			if got != answer {
				t.Errorf("durable = %t, quiero %t (lo que dijo el motor)", got, answer)
			}
			if reader.calls != 1 {
				t.Errorf("lecturas de la definición = %d, quiero 1", reader.calls)
			}
			if reader.gotTenant != "tenant-a" || reader.gotFlowID != "carrito-durable" {
				t.Errorf("se leyó (%q, %q), quiero (tenant-a, carrito-durable)", reader.gotTenant, reader.gotFlowID)
			}
			if reader.gotMarker != "ctx del llamante" {
				t.Errorf("el lector no recibió el ctx del llamante (marca = %v)", reader.gotMarker)
			}
			if eng.calls != 1 || !reflect.DeepEqual(eng.got, cartFlow()) {
				t.Errorf("el motor recibió %+v en %d llamadas, quiero la definición leída, una vez", eng.got, eng.calls)
			}
		})
	}
}

// Un flujo inexistente es «no durable» (fail-open) y no llega al motor, que aquí
// contestaría true: si llegara, el test lo vería.
func TestEngineDurableFlowChecker_FlowHasDurableContent_MissingFlowFailsOpen(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		reader admin.FlowDefinitionReader
	}{
		{"bare sentinel", &fakeDefinitionReader{err: store.ErrDefinitionNotFound}},
		{"wrapped sentinel", &fakeDefinitionReader{err: fmt.Errorf("leer: %w", store.ErrDefinitionNotFound)}},
		{"empty memory repository", store.NewMemoryRepository()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			eng := &fakeDurableEngine{answer: true}
			got, err := admin.NewEngineDurableFlowChecker(tc.reader, eng).
				FlowHasDurableContent(context.Background(), "tenant-a", "no-existe")
			if err != nil {
				t.Fatalf("un flujo inexistente no es un error: %v", err)
			}
			if got {
				t.Error("durable = true para un flujo inexistente, quiero false")
			}
			if eng.calls != 0 {
				t.Errorf("consultas al motor = %d, quiero 0", eng.calls)
			}
		})
	}
}

// Cualquier otro fallo SÍ se propaga: no saber no es lo mismo que «no es durable».
func TestEngineDurableFlowChecker_FlowHasDurableContent_PropagatesOtherErrors(t *testing.T) {
	t.Parallel()
	errDown := errors.New("base de datos caída")
	eng := &fakeDurableEngine{answer: true}

	got, err := admin.NewEngineDurableFlowChecker(&fakeDefinitionReader{err: errDown}, eng).
		FlowHasDurableContent(context.Background(), "tenant-a", "carrito-durable")
	if !errors.Is(err, errDown) {
		t.Fatalf("err = %v, quiero el del lector (%v)", err, errDown)
	}
	if got {
		t.Error("durable = true junto a un error, quiero false")
	}
	if eng.calls != 0 {
		t.Errorf("consultas al motor = %d, quiero 0", eng.calls)
	}
}
