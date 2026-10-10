package runtime

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
)

// Qué pasa cuando el almacén, el hilo o un proyector fallan (D-054.4, RT-10): qué corta, qué no,
// y qué error lleva la marca ErrMaterializationFailed. El montaje está en persist_sink_test.go.

// TestPersistSink_Handle_OutboxFailureCutsAndIsMarked: si el INSERT del outbox falla, no hay
// decisión ni proyección, y el error lleva la marca de materialización y el error de origen.
func TestPersistSink_Handle_OutboxFailureCutsAndIsMarked(t *testing.T) {
	rig := newPersistRig([]string{"item_added"}, nil)
	boom := errors.New("base caída")
	rig.outbox.err = boom

	err := rig.sink.Handle(persistCtx(), persistContext(), persistItemAdded())

	if !errors.Is(err, ErrMaterializationFailed) || !errors.Is(err, boom) {
		t.Fatalf("error = %v, quería uno marcado con ErrMaterializationFailed que envuelva el del almacén", err)
	}
	if want := "runtime: la materialización del efecto falló: outbox: base caída"; err.Error() != want {
		t.Errorf("texto = %q, quería %q", err.Error(), want)
	}
	if !slices.Equal(rig.trace.steps, []string{"outbox"}) {
		t.Errorf("traza = %v: tras fallar el outbox no se escribe la decisión ni se proyecta", rig.trace.steps)
	}
}

// TestPersistSink_Handle_ProjectionFailureIsMarked: el fallo del proyector sale marcado; el
// outbox y la decisión ya estaban escritos.
func TestPersistSink_Handle_ProjectionFailureIsMarked(t *testing.T) {
	rig := newPersistRig([]string{"item_added"}, nil)
	boom := errors.New("intakes: not null")
	rig.first.err = boom

	err := rig.sink.Handle(persistCtx(), persistContext(), persistItemAdded())

	if !errors.Is(err, ErrMaterializationFailed) || !errors.Is(err, boom) {
		t.Fatalf("error = %v, quería uno marcado con ErrMaterializationFailed que envuelva el del proyector", err)
	}
	if want := "runtime: la materialización del efecto falló: proyección: intakes: not null"; err.Error() != want {
		t.Errorf("texto = %q, quería %q", err.Error(), want)
	}
	if len(rig.events()) != 1 || len(rig.thread.calls) != 1 {
		t.Errorf("filas=%d, decisiones=%d; quería 1 y 1 (se escriben antes de proyectar)", len(rig.events()), len(rig.thread.calls))
	}
}

// TestPersistSink_Handle_ThreadFailureIsBestEffort: un fallo AISLADO del hilo no corta la
// proyección y su error sale SIN la marca de materialización (D-043.23): perder una fila de
// historial no puede costar una fila de negocio, ni el turno de un módulo durable.
func TestPersistSink_Handle_ThreadFailureIsBestEffort(t *testing.T) {
	rig := newPersistRig([]string{"item_added"}, nil)
	boom := errors.New("hilo caído")
	rig.thread.err = boom

	err := rig.sink.Handle(persistCtx(), persistContext(), persistItemAdded())

	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, quería el del hilo (el despacho lo loguea)", err)
	}
	if errors.Is(err, ErrMaterializationFailed) {
		t.Error("el fallo aislado del hilo lleva ErrMaterializationFailed: cortaría el turno de un pedido que SÍ se guardó")
	}
	if want := `runtime: escribir la decisión "item_added" en el hilo del evento: hilo caído`; err.Error() != want {
		t.Errorf("texto = %q, quería %q", err.Error(), want)
	}
	if len(rig.first.calls) != 1 || len(rig.events()) != 1 {
		t.Errorf("proyecciones=%d, filas=%d; quería 1 y 1: el hilo no corta nada", len(rig.first.calls), len(rig.events()))
	}

	t.Run("no projector either", func(t *testing.T) {
		alone := newPersistRig(nil, nil)
		alone.thread.err = boom
		got := alone.sink.Handle(persistCtx(), persistContext(), persistItemAdded())
		if !errors.Is(got, boom) || errors.Is(got, ErrMaterializationFailed) {
			t.Errorf("error = %v, quería el del hilo y sin marca", got)
		}
	})
}

// TestPersistSink_Handle_UnserializableDecisionDoesNotReachTheThread: si el payload público no se
// puede serializar a JSON, no se llama al hilo, el error sale sin marca y la proyección sigue.
func TestPersistSink_Handle_UnserializableDecisionDoesNotReachTheThread(t *testing.T) {
	rig := newPersistRig([]string{"item_added"}, nil)
	eff := modules.Effect{Kind: "event", Name: "item_added", Payload: map[string]any{"bad": make(chan int)}}

	err := rig.sink.Handle(persistCtx(), persistContext(), eff)

	var unsupported *json.UnsupportedTypeError
	if !errors.As(err, &unsupported) || errors.Is(err, ErrMaterializationFailed) {
		t.Fatalf("error = %v, quería el de serialización y sin marca", err)
	}
	if want := `runtime: serializar la decisión "item_added" para el hilo: ` + unsupported.Error(); err.Error() != want {
		t.Errorf("texto = %q, quería %q", err.Error(), want)
	}
	if len(rig.thread.calls) != 0 || len(rig.first.calls) != 1 {
		t.Errorf("decisiones=%d, proyecciones=%d; quería 0 y 1", len(rig.thread.calls), len(rig.first.calls))
	}
}

// TestPersistSink_Handle_ThreadAndProjectionFailuresTravelTogether: si fallan los dos, el error
// reconoce los dos orígenes y lleva la marca (que es de la proyección).
func TestPersistSink_Handle_ThreadAndProjectionFailuresTravelTogether(t *testing.T) {
	rig := newPersistRig([]string{"item_added"}, nil)
	threadErr := errors.New("hilo caído")
	projectErr := errors.New("proyector caído")
	rig.thread.err = threadErr
	rig.first.err = projectErr

	err := rig.sink.Handle(persistCtx(), persistContext(), persistItemAdded())

	if !errors.Is(err, threadErr) || !errors.Is(err, projectErr) || !errors.Is(err, ErrMaterializationFailed) {
		t.Fatalf("error = %v, quería los dos orígenes y la marca de materialización", err)
	}
	want := `runtime: escribir la decisión "item_added" en el hilo del evento: hilo caído` + "\n" +
		"runtime: la materialización del efecto falló: proyección: proyector caído"
	if err.Error() != want {
		t.Errorf("texto = %q, quería %q", err.Error(), want)
	}
}
