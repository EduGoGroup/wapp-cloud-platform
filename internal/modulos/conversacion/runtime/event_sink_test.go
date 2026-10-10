//go:build pendiente

package runtime

import (
	"context"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
)

// event_sink.go solo declara datos e interfaces: no tiene ningún cuerpo que entre en
// pánico, así que estos tests pasan ya en el rojo. Llevan la etiqueta `pendiente` porque el
// fichero NO está terminado: le falta la función que ordena los sinks por fase (no
// exportada), que nace en el verde con sus tests:
//
//   - TestSortSinksByPhase_ProjectBeforeNotify: un PhaseNotify registrado primero queda
//     detrás de los PhaseProject.
//   - TestSortSinksByPhase_StableWithinPhase: dos sinks de la misma fase conservan su
//     orden de registro (tabla con 0, 1 y varios sinks, y con una fase intermedia).
//   - TestPhaseOf_UnphasedSinkIsProject: un EventSink sin Phase() cuenta como PhaseProject.
//   - Mutantes: ordenación no estable, comparación invertida, default distinto de
//     PhaseProject.
//
// Y de conducta con el Runtime (ola siguiente): el fan-out entrega en el orden
// proyecta-A, proyecta-B, notifica; el WebhookSink registrado antes que el PersistSink
// encola el intake_id que generó la proyección.

// plainSink es un EventSink que NO declara fase.
type plainSink struct{}

func (*plainSink) Handle(context.Context, EffectContext, modules.Effect) error { return nil }

// notifySink es un EventSink que declara PhaseNotify.
type notifySink struct{ plainSink }

func (*notifySink) Phase() SinkPhase { return PhaseNotify }

// Aserciones de compilación: quién satisface cada puerto.
var (
	_ EventSink  = (*plainSink)(nil)
	_ EventSink  = (*notifySink)(nil)
	_ PhasedSink = (*notifySink)(nil)
	// Todo PhasedSink ES un EventSink: la interfaz lo embebe.
	_ EventSink = PhasedSink(nil)
)

// TestSinkPhase_ValuesAndOrder: PhaseProject vale 0, PhaseNotify vale 100, y proyectar va
// antes que notificar.
func TestSinkPhase_ValuesAndOrder(t *testing.T) {
	if PhaseProject != 0 {
		t.Errorf("PhaseProject = %d, quería 0", PhaseProject)
	}
	if PhaseNotify != 100 {
		t.Errorf("PhaseNotify = %d, quería 100", PhaseNotify)
	}
	if PhaseProject >= PhaseNotify {
		t.Errorf("PhaseProject (%d) tiene que ordenar ANTES que PhaseNotify (%d)", PhaseProject, PhaseNotify)
	}
}

// TestSinkPhase_ZeroValueIsProject: la fase por defecto es la de proyección, también
// para quien deja un SinkPhase sin inicializar.
func TestSinkPhase_ZeroValueIsProject(t *testing.T) {
	var zero SinkPhase
	if zero != PhaseProject {
		t.Errorf("el valor cero de SinkPhase es %d, quería PhaseProject (%d)", zero, PhaseProject)
	}
}

// TestPhasedSink_OptInOnly: implementar la fase es OPCIONAL. Un sink sin Phase() sigue
// siendo un EventSink y no es un PhasedSink; uno con Phase() es las dos cosas y declara
// la suya.
func TestPhasedSink_OptInOnly(t *testing.T) {
	var plain EventSink = &plainSink{}
	if _, ok := plain.(PhasedSink); ok {
		t.Error("un sink sin Phase() no puede contar como PhasedSink")
	}

	var notify EventSink = &notifySink{}
	phased, ok := notify.(PhasedSink)
	if !ok {
		t.Fatal("un sink con Phase() tiene que satisfacer PhasedSink")
	}
	if got := phased.Phase(); got != PhaseNotify {
		t.Errorf("Phase() = %d, quería PhaseNotify (%d)", got, PhaseNotify)
	}
}

// TestEventSink_SameEffectPayloadIsShared: el Payload del efecto es un mapa, así que lo
// que un sink anota lo ve el siguiente sin copiar nada. Es la propiedad de la que depende
// la correlación del intake_id y la razón de que exista SinkPhase.
func TestEventSink_SameEffectPayloadIsShared(t *testing.T) {
	eff := modules.Effect{Kind: "persist", Name: "cart_closed", Payload: map[string]any{}}
	seen := ""
	enrich := sinkFunc(func(_ EffectContext, e modules.Effect) { e.Payload["intake_id"] = "intake-1" })
	read := sinkFunc(func(_ EffectContext, e modules.Effect) {
		if id, ok := e.Payload["intake_id"].(string); ok {
			seen = id
		}
	})

	for _, s := range []EventSink{enrich, read} {
		if err := s.Handle(context.Background(), EffectContext{}, eff); err != nil {
			t.Fatalf("Handle devolvió %v", err)
		}
	}
	if seen != "intake-1" {
		t.Errorf("el segundo sink leyó intake_id = %q, quería %q: el payload no se comparte", seen, "intake-1")
	}
}

// sinkFunc adapta una función a EventSink.
type sinkFunc func(ec EffectContext, eff modules.Effect)

func (f sinkFunc) Handle(_ context.Context, ec EffectContext, eff modules.Effect) error {
	f(ec, eff)
	return nil
}

// TestEffectContext_ZeroValueIsSafe: el valor cero es «sin evento y no durable», que es lo
// que reciben los efectos de ciclo de vida y cualquier llamante que no fije nada.
func TestEffectContext_ZeroValueIsSafe(t *testing.T) {
	var ec EffectContext
	if ec.EventID != "" {
		t.Errorf("EventID del valor cero = %q, quería vacío («no hay evento»)", ec.EventID)
	}
	if ec.Durable {
		t.Error("Durable del valor cero es true; el valor seguro (ADR-0003, best-effort) es false")
	}
}

// TestEffectContext_CarriesConversationIdentity: los siete campos viajan por valor, tal
// cual, hasta el sink.
func TestEffectContext_CarriesConversationIdentity(t *testing.T) {
	want := EffectContext{
		TenantID: "t-1", ContactID: "c-opaque", SessionID: "s-1",
		FlowID: "f-1", FlowVersion: 3, EventID: "e-1", Durable: true,
	}
	var got EffectContext
	sink := sinkFunc(func(ec EffectContext, _ modules.Effect) { got = ec })

	if err := sink.Handle(context.Background(), want, modules.Effect{}); err != nil {
		t.Fatalf("Handle devolvió %v", err)
	}
	if got != want {
		t.Errorf("el sink recibió %+v, quería %+v", got, want)
	}
}
