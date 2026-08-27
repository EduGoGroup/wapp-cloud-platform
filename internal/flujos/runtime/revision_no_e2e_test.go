// revision_no_e2e_test.go — el número de revisión recorre TODO el camino real:
// módulo → efecto → PersistSink (cart.Projector, que escribe la revisión y anota
// su número) → WebhookSink (que lo encola) → cuerpo de webhook_outbox.payload
// (Plan 044 · T4.10, mitad 1, criterio (a): «de extremo a extremo, no el push se
// disparó», y verificado en el CUERPO ENTREGADO, no en el log).
//
// Es el hermano de sink_phase_test.go, que hace lo mismo con el intake_id. Los dos
// datos viajan por la misma puerta —el mapa compartido eff.Payload— y por el mismo
// motivo: el sink del CRM no puede consultar la BD en línea con el mensaje.
package runtime_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/cart"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/survey"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// pipelineYaEscribió es un RevisionWriter que se interpone delante del store real
// para reproducir lo que T4.0 (D-044.46) dejó montado en producción: cuando el
// carrito va a escribir su revisión, el pipeline del 044 YA colgó la suya
// (`interpreted`) de esa MISMA solicitud —la encontró por el evento, en `open`, y
// no le tocó el estado—. El carrito recibe entonces la 2, no la 1.
//
// Se interpone en vez de sembrar antes porque el id de la solicitud lo genera
// store.CloseIntake DENTRO de la corrida: no existe hasta que el carrito cierra.
// La numeración la sigue calculando el store de verdad (intakes.MemoryStore, misma
// aritmética que Postgres), así que el 2 no lo fabrica este doble.
type pipelineYaEscribió struct {
	store     *intakes.MemoryStore
	sembradas map[string]bool
}

func (w *pipelineYaEscribió) InsertRevision(ctx context.Context, rev intakes.Revision) (intakes.Revision, error) {
	if !w.sembradas[rev.IntakeID] {
		w.sembradas[rev.IntakeID] = true
		if _, err := w.store.InsertRevision(ctx, intakes.Revision{
			IntakeID:  rev.IntakeID,
			Kind:      intakes.RevisionKindInterpreted,
			Payload:   json.RawMessage(`{"version":1,"items":[]}`),
			CreatedBy: intakes.RevisionBySystem,
		}); err != nil {
			return intakes.Revision{}, err
		}
	}
	return w.store.InsertRevision(ctx, rev)
}

// TestRevisionNo_E2E_ElCuerpoEncoladoLlevaElNúmeroQueEscribióLaBase corre la
// conversación completa y mira el JSON que acabaría en webhook_outbox.payload.
//
// 🔴 Con el literal que T4.10 retiró, este test salía verde emitiendo `1` mientras
// intake_revisions guardaba la 2: el puente CRM hace UPSERT por (intake_id,
// revision_no) y habría descartado el CIERRE como duplicado de la interpretación
// del LLM. El cliente ve su pedido confirmado por WhatsApp y en el CRM queda lo
// que el modelo entendió. Sin un error en ningún log ni en ninguna métrica.
func TestRevisionNo_E2E_ElCuerpoEncoladoLlevaElNúmeroQueEscribióLaBase(t *testing.T) {
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar definición: %v", err)
	}
	revisiones := &pipelineYaEscribió{store: intakes.NewMemoryStore(), sembradas: map[string]bool{}}
	q := &capturaQueuer{}

	rt := runtime.New(repo, newEffectEngine([]modules.Effect{cartClosedEmit()}), &fakeSender{},
		fakeResolver{tenantID: testTenant}, contact.NewMemoryResolver(repo), discardLogger(),
		runtime.WithEventSink(runtime.NewWebhookSink(discardLogger(), cart.EffectCartClosed, q, gateAbierto{})),
		runtime.WithEventSink(runtime.NewPersistSink(repo,
			cart.NewProjector(repo, revisiones, sinEnvío{}, intakes.NewMemoryStore()),
			survey.NewProjector(repo))),
	)
	if err := startAndStep(t, rt); err != nil {
		t.Fatalf("HandleIncoming: %v", err)
	}

	if len(q.payloads) != 1 {
		t.Fatalf("el webhook debía encolar 1 entrega, encoló %d", len(q.payloads))
	}
	proyectadas := repo.Intakes()
	if len(proyectadas) != 1 {
		t.Fatalf("el proyector debía crear 1 solicitud, creó %d", len(proyectadas))
	}

	// La VERDAD está en el store de revisiones, no en el número que esperemos: se
	// lee de ahí y se compara con el cuerpo. Así el test no se vuelve tautológico
	// contra una constante escrita en el propio test.
	revs := revisiones.store.Revisions(proyectadas[0].ID)
	if len(revs) != 2 {
		t.Fatalf("revisiones de %s: %d, quiero 2 (interpretada + cierre)", proyectadas[0].ID, len(revs))
	}
	var delCierre intakes.Revision
	for _, r := range revs {
		if r.Kind == intakes.RevisionKindCart {
			delCierre = r
		}
	}
	if delCierre.RevisionNo != 2 {
		t.Fatalf("la revisión del cierre quedó con el nº %d y se esperaba la 2: el escenario que "+
			"este test reproduce no se montó, así que no está probando nada", delCierre.RevisionNo)
	}

	var cuerpo map[string]any
	if err := json.Unmarshal(q.payloads[0], &cuerpo); err != nil {
		t.Fatalf("el cuerpo encolado no es JSON válido: %v", err)
	}
	if got := cuerpo["revision_no"]; got != float64(delCierre.RevisionNo) {
		t.Fatalf("🔴 el cuerpo encolado lleva revision_no=%#v y la base escribió la %d. "+
			"El puente hace UPSERT por (intake_id, revision_no): con el número equivocado el "+
			"cierre se pierde como duplicado y el CRM se queda con la interpretación del LLM",
			got, delCierre.RevisionNo)
	}
}
