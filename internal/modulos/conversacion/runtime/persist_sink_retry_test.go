package runtime

import (
	"errors"
	"slices"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
)

// RetryProjection, el reintento que retoma en la proyección (D-F8-15, hallazgo 34c), y la marca
// con la que el despacho distingue un fallo de proyección de uno de outbox. Partido de
// persist_sink_test.go por E-13; el montaje (newPersistRig y sus dobles) está allí.

// TestPersistSink_RetryProjection_OnlyProjects: repite la proyección —mismo ctx, los seis campos
// del contexto, el efecto ENTERO con su mismo mapa— y no toca ni el outbox ni el hilo.
func TestPersistSink_RetryProjection_OnlyProjects(t *testing.T) {
	rig := newPersistRig([]string{"item_added"}, []string{"item_added"})
	rig.first.annotate = func(payload map[string]any) { payload["intake_id"] = "intake-1" }
	ec, eff := persistContext(), persistItemAdded()

	if err := rig.sink.RetryProjection(persistCtx(), ec, eff); err != nil {
		t.Fatalf("RetryProjection = %v, quería nil", err)
	}

	if !slices.Equal(rig.trace.steps, []string{"project:first"}) {
		t.Errorf("traza = %v, quería SOLO la proyección del primer proyector que acepta (ni outbox ni hilo)", rig.trace.steps)
	}
	if len(rig.events()) != 0 || len(rig.thread.calls) != 0 {
		t.Errorf("filas=%d, decisiones=%d; RetryProjection no escribe ni el outbox ni el hilo", len(rig.events()), len(rig.thread.calls))
	}
	call := rig.first.calls[0]
	want := modules.EffectMeta{
		TenantID: ec.TenantID, ContactID: ec.ContactID, SessionID: ec.SessionID,
		FlowID: ec.FlowID, FlowVersion: ec.FlowVersion, EventID: ec.EventID,
	}
	if !call.marked || call.meta != want {
		t.Errorf("proyección: ctx marcado=%v, meta=%+v; quería el mismo ctx y %+v", call.marked, call.meta, want)
	}
	if call.eff.Kind != eff.Kind || call.eff.Name != eff.Name || call.payload["sku"] != "A1" {
		t.Errorf("efecto proyectado = %+v, quería el efecto entero", call.eff)
	}
	if eff.Payload["intake_id"] != "intake-1" {
		t.Error("lo que el proyector anotó no se ve en el Payload del llamante: no recibió el mismo mapa")
	}
}

// TestPersistSink_RetryProjection_PrivateKindStillProjects: un efecto privado se proyecta igual
// (su sitio es el proyector) y sigue sin tocar el outbox ni el hilo.
func TestPersistSink_RetryProjection_PrivateKindStillProjects(t *testing.T) {
	rig := newPersistRig([]string{"buyer_data_captured"}, nil)
	eff := modules.Effect{Kind: modules.KindPrivate, Name: "buyer_data_captured", Payload: map[string]any{"name": "Ana"}}

	if err := rig.sink.RetryProjection(persistCtx(), persistContext(), eff); err != nil {
		t.Fatalf("RetryProjection = %v, quería nil", err)
	}
	if !slices.Equal(rig.trace.steps, []string{"project:first"}) {
		t.Errorf("traza = %v, quería solo la proyección", rig.trace.steps)
	}
}

// TestPersistSink_RetryProjection_FailureIsMarkedLikeHandle: si el proyector vuelve a fallar, el
// error lleva la marca de materialización, el error de origen y EL MISMO texto que da Handle.
func TestPersistSink_RetryProjection_FailureIsMarkedLikeHandle(t *testing.T) {
	rig := newPersistRig([]string{"item_added"}, nil)
	boom := errors.New("intakes: not null")
	rig.first.err = boom

	fromHandle := rig.sink.Handle(persistCtx(), persistContext(), persistItemAdded())
	err := rig.sink.RetryProjection(persistCtx(), persistContext(), persistItemAdded())

	if !errors.Is(err, ErrMaterializationFailed) || !errors.Is(err, boom) {
		t.Fatalf("error = %v, quería uno marcado con ErrMaterializationFailed que envuelva el del proyector", err)
	}
	if want := "runtime: la materialización del efecto falló: proyección: intakes: not null"; err.Error() != want {
		t.Errorf("texto = %q, quería %q", err.Error(), want)
	}
	if fromHandle == nil || err.Error() != fromHandle.Error() {
		t.Errorf("texto = %q, quería el mismo que da Handle: %v", err.Error(), fromHandle)
	}
	if !isProjectionFailure(err) {
		t.Error("el fallo de RetryProjection no se reconoce como fallo de proyección: el siguiente intento repetiría el Handle entero")
	}
	if len(rig.events()) != 1 || len(rig.thread.calls) != 1 {
		t.Errorf("filas=%d, decisiones=%d; quería las del Handle (1 y 1) y ninguna más", len(rig.events()), len(rig.thread.calls))
	}
}

// TestPersistSink_RetryProjection_NothingToProject: sin proyector que acepte el efecto, o con un
// receptor nil, no hay nada que reintentar: nil.
func TestPersistSink_RetryProjection_NothingToProject(t *testing.T) {
	rig := newPersistRig([]string{"survey_answer"}, nil)
	if err := rig.sink.RetryProjection(persistCtx(), persistContext(), persistItemAdded()); err != nil {
		t.Errorf("sin proyector que acepte: RetryProjection = %v, quería nil", err)
	}
	if len(rig.trace.steps) != 0 {
		t.Errorf("traza = %v, quería ninguna escritura", rig.trace.steps)
	}
	var none *PersistSink
	if err := none.RetryProjection(persistCtx(), persistContext(), persistItemAdded()); err != nil {
		t.Errorf("receptor nil: RetryProjection = %v, quería nil", err)
	}
}

// TestPersistSink_Handle_OnlyTheProjectionFailureIsRecognised: el despacho distingue en qué paso
// falló Handle. Solo el fallo de la proyección se reconoce —también cuando viaja UNIDO al del
// hilo—; el del outbox y el aislado del hilo, no.
func TestPersistSink_Handle_OnlyTheProjectionFailureIsRecognised(t *testing.T) {
	boom := errors.New("caído")
	cases := []struct {
		name    string
		arrange func(rig *persistRig)
		want    bool
	}{
		{"projection", func(rig *persistRig) { rig.first.err = boom }, true},
		{"projection joined with the thread failure", func(rig *persistRig) { rig.first.err, rig.thread.err = boom, boom }, true},
		{"outbox", func(rig *persistRig) { rig.outbox.err = boom }, false},
		{"thread alone", func(rig *persistRig) { rig.thread.err = boom }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rig := newPersistRig([]string{"item_added"}, nil)
			tc.arrange(rig)

			err := rig.sink.Handle(persistCtx(), persistContext(), persistItemAdded())

			if err == nil {
				t.Fatal("Handle = nil, quería el fallo inyectado")
			}
			if got := isProjectionFailure(err); got != tc.want {
				t.Errorf("isProjectionFailure(%v) = %v, quería %v", err, got, tc.want)
			}
		})
	}
	if isProjectionFailure(nil) {
		t.Error("isProjectionFailure(nil) = true")
	}
}
