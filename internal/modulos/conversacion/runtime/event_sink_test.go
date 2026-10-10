package runtime

import (
	"context"
	"slices"
	"strconv"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
)

// Tests de event_sink.go: los de sus exportados (datos e interfaces) y los de la
// ordenación por fase, que no es exportada y nació en el verde (F8-04b).
//
// Mutantes de la ordenación, aplicados a mano con -race:
//
//   - ordenación NO estable (`slices.SortFunc`) → muere
//     TestSortSinksByPhase_StableWithinPhase/many_sinks_interleaved (hacen falta más de 12
//     elementos: por debajo, la ordenación de Go es una inserción, que es estable).
//   - comparación invertida (`cmp.Compare(phaseOf(b), phaseOf(a))`) → muere
//     TestSortSinksByPhase_ProjectBeforeNotify.
//   - default distinto de PhaseProject (`return PhaseNotify` en phaseOf) → muere
//     TestPhaseOf_UnphasedSinkIsProject.
//
// De conducta con el Runtime (ola siguiente): el fan-out entrega en el orden
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

// namedSink es un EventSink SIN fase que se distingue por su nombre: sirve para afirmar
// sobre el ORDEN en que queda una lista de sinks.
type namedSink struct {
	plainSink
	name string
}

// namedPhasedSink es un namedSink que declara la fase que se le dé.
type namedPhasedSink struct {
	namedSink
	phase SinkPhase
}

func (s *namedPhasedSink) Phase() SinkPhase { return s.phase }

func unphased(name string) EventSink { return &namedSink{name: name} }

func phased(name string, phase SinkPhase) EventSink {
	return &namedPhasedSink{namedSink: namedSink{name: name}, phase: phase}
}

// sinkNames devuelve los nombres de los sinks en el orden en que están.
func sinkNames(t *testing.T, sinks []EventSink) []string {
	t.Helper()
	names := make([]string, 0, len(sinks))
	for _, s := range sinks {
		switch v := s.(type) {
		case *namedSink:
			names = append(names, v.name)
		case *namedPhasedSink:
			names = append(names, v.name)
		default:
			t.Fatalf("sink inesperado en la lista: %T", s)
		}
	}
	return names
}

// TestPhaseOf_UnphasedSinkIsProject: un EventSink sin Phase() cuenta como PhaseProject, y
// uno con Phase() cuenta como lo que declara.
func TestPhaseOf_UnphasedSinkIsProject(t *testing.T) {
	if got := phaseOf(&plainSink{}); got != PhaseProject {
		t.Errorf("phaseOf de un sink sin Phase() = %d, quería PhaseProject (%d)", got, PhaseProject)
	}
	if got := phaseOf(&notifySink{}); got != PhaseNotify {
		t.Errorf("phaseOf de un sink de notificación = %d, quería PhaseNotify (%d)", got, PhaseNotify)
	}
	if got := phaseOf(phased("mid", 50)); got != 50 {
		t.Errorf("phaseOf de un sink de fase 50 = %d, quería 50", got)
	}
}

// TestSortSinksByPhase_ProjectBeforeNotify: un PhaseNotify registrado PRIMERO queda detrás
// de todos los PhaseProject, declaren la fase o no.
func TestSortSinksByPhase_ProjectBeforeNotify(t *testing.T) {
	sinks := []EventSink{
		phased("notify", PhaseNotify),
		unphased("project-A"),
		phased("project-B", PhaseProject),
	}
	sortSinksByPhase(sinks)

	want := []string{"project-A", "project-B", "notify"}
	if got := sinkNames(t, sinks); !slices.Equal(got, want) {
		t.Errorf("orden = %v, quería %v (proyección antes que notificación)", got, want)
	}
}

// TestSortSinksByPhase_StableWithinPhase: dentro de una misma fase se conserva el orden de
// registro, y una fase intermedia cae entre las dos.
func TestSortSinksByPhase_StableWithinPhase(t *testing.T) {
	// Muchos sinks intercalados: n-i, m-i y p-i en cada vuelta. Ordenados, primero todos
	// los p en su orden, luego los m y luego los n.
	const rounds = 20
	interleaved := make([]EventSink, 0, 3*rounds)
	wantP := make([]string, 0, rounds)
	wantM := make([]string, 0, rounds)
	wantN := make([]string, 0, rounds)
	for i := range rounds {
		id := strconv.Itoa(i)
		interleaved = append(interleaved,
			phased("n-"+id, PhaseNotify), phased("m-"+id, 50), unphased("p-"+id))
		wantP, wantM, wantN = append(wantP, "p-"+id), append(wantM, "m-"+id), append(wantN, "n-"+id)
	}

	cases := []struct {
		name  string
		sinks []EventSink
		want  []string
	}{
		{"no sinks", nil, []string{}},
		{"one sink", []EventSink{phased("only", PhaseNotify)}, []string{"only"}},
		{
			"all in the same phase keep registration order",
			[]EventSink{unphased("c"), phased("a", PhaseProject), unphased("b")},
			[]string{"c", "a", "b"},
		},
		{
			"two notify sinks keep registration order behind project",
			[]EventSink{phased("n-2", PhaseNotify), phased("n-1", PhaseNotify), unphased("p")},
			[]string{"p", "n-2", "n-1"},
		},
		{
			"intermediate phase sits between project and notify",
			[]EventSink{phased("notify", PhaseNotify), phased("mid", 50), unphased("project")},
			[]string{"project", "mid", "notify"},
		},
		{"many sinks interleaved", interleaved, slices.Concat(wantP, wantM, wantN)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sortSinksByPhase(tc.sinks)
			if got := sinkNames(t, tc.sinks); !slices.Equal(got, tc.want) {
				t.Errorf("orden = %v, quería %v", got, tc.want)
			}
		})
	}
}
