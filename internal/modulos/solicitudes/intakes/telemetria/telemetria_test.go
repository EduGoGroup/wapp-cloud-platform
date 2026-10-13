package telemetria_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes/telemetria"
)

// El cable en tiempo de compilación: si alguien cambia la firma de un lado, esto no
// compila en vez de dejar el arranque sin telemetría en silencio.
var (
	_ intakes.MetricsPublisher = (*telemetria.Publisher)(nil)
	_ telemetria.Outbox        = (*fakeOutbox)(nil)
)

// fakeOutbox retiene lo escrito y puede fallar a voluntad.
type fakeOutbox struct {
	rows []store.FlowEvent
	err  error
}

func (o *fakeOutbox) InsertFlowEvent(_ context.Context, ev store.FlowEvent) error {
	if o.err != nil {
		return o.err
	}
	o.rows = append(o.rows, ev)
	return nil
}

// Los valores del wire, clavados a su literal: son lo que filtran las consultas del
// runbook, y un cambio aquí es un cambio de contrato, no un renombre.
func TestInboxFlow_WireLiterals(t *testing.T) {
	if telemetria.InboxFlow != "_intake_inbox" {
		t.Errorf("InboxFlow = %q; quiero el literal del wire %q", telemetria.InboxFlow, "_intake_inbox")
	}
	if telemetria.InboxFlowVersion != 1 {
		t.Errorf("InboxFlowVersion = %d; quiero 1", telemetria.InboxFlowVersion)
	}
}

// Las dos mitades del prefijo y la versión: `_` reserva el identificador para la
// plataforma, no coincidir con el del pipeline permite separar por SQL lo que
// INTERPRETÓ la máquina de lo que DECIDIÓ el dueño, y 0 sería «versión desconocida».
func TestInboxFlow_ReservedAndNotThePipeline(t *testing.T) {
	if telemetria.InboxFlow == "" || telemetria.InboxFlow[0] != '_' {
		t.Fatalf("InboxFlow = %q: sin el prefijo reservado, un flujo de un tenant podría colisionar",
			telemetria.InboxFlow)
	}
	if telemetria.InboxFlow == "_intake_llm" {
		t.Error("la bandeja firma con el flujo del PIPELINE: las dos telemetrías dejarían de poder separarse")
	}
	if telemetria.InboxFlowVersion == 0 {
		t.Error("InboxFlowVersion = 0 significa «versión desconocida», y este emisor sí tiene contrato")
	}
}

func TestNew_ReturnsAPublisher(t *testing.T) {
	p := telemetria.New(&fakeOutbox{})
	if p == nil {
		t.Fatal("New devolvió nil; quiero un publicador")
	}
}

// Una llamada, una fila: firmada con las tres columnas que la bandeja no elige, y con
// el resto tal cual llegó del dominio.
func TestPublisher_PublishMetric_SignsTheRowWithTheInboxFlow(t *testing.T) {
	outbox := &fakeOutbox{}
	p := telemetria.New(outbox)

	payload := map[string]any{"lines_corrected": 2, "lines_total": 4}
	err := p.PublishMetric(context.Background(), "tenant-1", "opaque-contact-1",
		intakes.EventLineCorrected, payload)
	if err != nil {
		t.Fatalf("PublishMetric: error inesperado: %v", err)
	}

	if len(outbox.rows) != 1 {
		t.Fatalf("filas escritas = %d; quiero 1", len(outbox.rows))
	}
	want := store.FlowEvent{
		TenantID:    "tenant-1",
		ContactID:   "opaque-contact-1",
		FlowID:      "_intake_inbox",
		FlowVersion: 1,
		Kind:        "event",
		Name:        "intake_line_corrected",
		Payload:     map[string]any{"lines_corrected": 2, "lines_total": 4},
	}
	// La fila ENTERA: un campo de más que el adaptador rellenase (una sesión, un turno)
	// también es una firma que la bandeja no pidió.
	if got := outbox.rows[0]; !reflect.DeepEqual(got, want) {
		t.Errorf("la fila salió %+v; quiero %+v", got, want)
	}
}

// El payload es del dominio (design §10): el adaptador no lo retoca.
func TestPublisher_PublishMetric_PassesThePayloadUntouched(t *testing.T) {
	cases := []struct {
		name    string
		event   string
		payload map[string]any
	}{
		{"approved counters", intakes.EventApproved, map[string]any{"rev": 3, "elapsed_from_draft_ms": 1900000}},
		{"info requested", intakes.EventInfoRequested, map[string]any{"questions": 1}},
		{"empty payload", intakes.EventApproved, map[string]any{}},
		{"nil payload", intakes.EventApproved, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outbox := &fakeOutbox{}
			if err := telemetria.New(outbox).PublishMetric(context.Background(),
				"tenant-2", "opaque-contact-2", tc.event, tc.payload); err != nil {
				t.Fatalf("PublishMetric: error inesperado: %v", err)
			}
			if len(outbox.rows) != 1 {
				t.Fatalf("filas escritas = %d; quiero 1", len(outbox.rows))
			}
			got := outbox.rows[0]
			if got.Name != tc.event {
				t.Errorf("Name = %q; quiero %q", got.Name, tc.event)
			}
			if (got.Payload == nil) != (tc.payload == nil) || !reflect.DeepEqual(got.Payload, tc.payload) {
				t.Errorf("el adaptador tocó el payload: %v; quiero %v", got.Payload, tc.payload)
			}
		})
	}
}

// El adaptador no se traga el fallo: quien decide qué hacer con él es el dominio.
func TestPublisher_PublishMetric_ReturnsTheOutboxError(t *testing.T) {
	failure := errors.New("la base no responde")
	p := telemetria.New(&fakeOutbox{err: failure})

	err := p.PublishMetric(context.Background(), "tenant-1", "opaque-contact-1",
		intakes.EventApproved, map[string]any{"rev": 1})
	if !errors.Is(err, failure) {
		t.Fatalf("error = %v; el adaptador se tragó el fallo del outbox", err)
	}
}
