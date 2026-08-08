// sink_phase_test.go cubre el hueco que el review de las Olas 1-3 encontró
// abierto (Plan 042 · Ola 3.1): la correlación del `intake_id` entre el proyector
// del carrito y el WebhookSink dependía del ORDEN en que bootstrap.go registraba
// los sinks, y NINGÚN test la ejercitaba de extremo a extremo — los que llegaban
// al WebhookSink con un intake_id lo inyectaban a mano en el fixture, y el único
// que registraba ambos sinks juntos construía el webhook con sender/gate nil, que
// retorna antes de leer el payload.
//
// Estos tests corren el camino REAL (módulo → efecto → PersistSink con
// cart.Projector → WebhookSink con queuer real) y, a propósito, registran el
// WebhookSink PRIMERO: si algún día alguien quita las fases y el fan-out vuelve a
// respetar el orden de registro, este test se pone rojo en vez de dejar salir
// entregas con `intake_id: ""` hacia el CRM del cliente.
package runtime_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/cart"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

// capturaQueuer guarda el payload encolado para inspeccionarlo, sin tocar Postgres.
type capturaQueuer struct {
	payloads []json.RawMessage
}

func (q *capturaQueuer) EnqueueWebhook(_ context.Context, _, _ string, payload json.RawMessage) (int64, error) {
	q.payloads = append(q.payloads, payload)
	return int64(len(q.payloads)), nil
}

// gateAbierto deja pasar a cualquier tenant: estos tests miran el ORDEN del
// fan-out, no el gate (que tiene sus propios tests).
type gateAbierto struct{}

func (gateAbierto) Enabled(context.Context, string) (bool, error) { return true, nil }

// TestSinkPhase_WebhookLeeElIntakeIDAunqueSeRegistrePrimero es el test de
// regresión del hallazgo #1 del review: con el WebhookSink registrado ANTES del
// PersistSink —el orden que rompía la correlación—, el payload encolado tiene que
// llevar igualmente el intake_id que generó store.CloseIntake.
func TestSinkPhase_WebhookLeeElIntakeIDAunqueSeRegistrePrimero(t *testing.T) {
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar definición: %v", err)
	}
	q := &capturaQueuer{}

	rt := runtime.New(repo, newEffectEngine([]modules.Effect{cartClosedEmit()}), &fakeSender{},
		fakeResolver{tenantID: testTenant}, contact.NewMemoryResolver(repo), discardLogger(),
		// A PROPÓSITO primero el que NOTIFICA y después el que PROYECTA: es el
		// orden que dejaba el intake_id vacío antes de que existiera SinkPhase.
		runtime.WithEventSink(runtime.NewWebhookSink(discardLogger(), cart.EffectCartClosed, q, gateAbierto{})),
		runtime.WithEventSink(persistSinkWith(repo)),
	)
	if err := startAndStep(t, rt); err != nil {
		t.Fatalf("HandleIncoming: %v", err)
	}

	if len(q.payloads) != 1 {
		t.Fatalf("el webhook debía encolar exactamente 1 entrega, encoló %d", len(q.payloads))
	}
	proyectadas := repo.Intakes()
	if len(proyectadas) != 1 {
		t.Fatalf("el proyector debía crear exactamente 1 solicitud, creó %d", len(proyectadas))
	}

	var encolado struct {
		IntakeID string `json:"intake_id"`
	}
	if err := json.Unmarshal(q.payloads[0], &encolado); err != nil {
		t.Fatalf("el payload encolado no es JSON válido: %v", err)
	}
	if encolado.IntakeID == "" {
		t.Fatal("el payload encolado llegó SIN intake_id: el WebhookSink corrió antes " +
			"de que el proyector lo anotara — la ordenación por SinkPhase dejó de aplicarse")
	}
	if encolado.IntakeID != proyectadas[0].ID {
		t.Fatalf("intake_id encolado=%q, pero la solicitud proyectada es %q — no correlacionan",
			encolado.IntakeID, proyectadas[0].ID)
	}
}

// sinkGrabador es un EventSink que solo apunta su nombre en el orden en que se le
// llama, para afirmar sobre la SECUENCIA del fan-out.
type sinkGrabador struct {
	nombre string
	orden  *[]string
}

func (s sinkGrabador) Handle(context.Context, runtime.EffectContext, modules.Effect) error {
	*s.orden = append(*s.orden, s.nombre)
	return nil
}

// sinkGrabadorNotify es el mismo grabador pero declarando PhaseNotify.
type sinkGrabadorNotify struct{ sinkGrabador }

func (sinkGrabadorNotify) Phase() runtime.SinkPhase { return runtime.PhaseNotify }

// TestSinkPhase_OrdenaPorFaseYEsEstableDentroDeLaFase: los sinks de PhaseNotify
// corren después de los de PhaseProject aunque se registren antes, y dentro de una
// misma fase se respeta el orden de registro (ordenación ESTABLE — dos sinks de
// proyección cableados en cierto orden lo conservan).
func TestSinkPhase_OrdenaPorFaseYEsEstableDentroDeLaFase(t *testing.T) {
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar definición: %v", err)
	}
	var orden []string

	rt := runtime.New(repo, newEffectEngine([]modules.Effect{sampleEffect()}), &fakeSender{},
		fakeResolver{tenantID: testTenant}, contact.NewMemoryResolver(repo), discardLogger(),
		runtime.WithEventSink(sinkGrabadorNotify{sinkGrabador{nombre: "notifica", orden: &orden}}),
		runtime.WithEventSink(sinkGrabador{nombre: "proyecta-A", orden: &orden}),
		runtime.WithEventSink(sinkGrabador{nombre: "proyecta-B", orden: &orden}),
	)
	if err := startAndStep(t, rt); err != nil {
		t.Fatalf("HandleIncoming: %v", err)
	}

	if len(orden) != 3 {
		t.Fatalf("los tres sinks debían recibir el efecto, recibieron %d: %v", len(orden), orden)
	}
	want := []string{"proyecta-A", "proyecta-B", "notifica"}
	for i, n := range want {
		if orden[i] != n {
			t.Fatalf("orden del fan-out = %v, quiero %v (proyección antes que notificación, "+
				"y estable dentro de la fase)", orden, want)
		}
	}
}
