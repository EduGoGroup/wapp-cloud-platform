package cart

// revision_no_anotado_test.go — el cierre del carrito ANOTA en el efecto el número
// REAL que la base le dio a su revisión (Plan 044 · T4.10, mitad 1). Es la mitad
// del cableado que vive en el proyector; la otra —que ese número llegue al cuerpo
// que se encola para el CRM— la cubren los tests del WebhookSink.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// TestProjector_CartClosed_AnotaElRevisionNoEnElEfecto: el caso simple —nadie
// escribió antes sobre esta solicitud— deja el 1 anotado. Importa que sea el
// número DEVUELTO por el store y no un valor por defecto: el test hermano de abajo
// es el que lo distingue.
func TestProjector_CartClosed_AnotaElRevisionNoEnElEfecto(t *testing.T) {
	repo := store.NewMemoryRepository()
	revisions := intakes.NewMemoryStore()
	p := NewProjector(repo, revisions, &envíoEspía{}, intakes.NewMemoryStore())

	eff := cartClosedEffect()
	if err := p.Project(context.Background(), projectorMeta(), eff); err != nil {
		t.Fatalf("Project(cart_closed): %v", err)
	}

	anotado, hay := eff.Payload["revision_no"]
	if !hay {
		t.Fatal("el cierre no anotó revision_no en el efecto: el WebhookSink no tiene de dónde leerlo " +
			"y volvería a emitir un número inventado hacia el CRM")
	}
	if got, ok := anotado.(int); !ok || got != 1 {
		t.Fatalf("revision_no anotado = %#v, quiero el int 1", anotado)
	}
}

// TestProjector_CartClosed_AnotaLaSegundaCuandoElPipelineYaEscribió reproduce el
// escenario REAL que dejó T4.0 (D-044.46) y que convierte en mentira cualquier
// número fijo: el pipeline del 044 cuelga su revisión `interpreted` de la MISMA
// fila que el carrito dejó en `open` —no crea otra ni le toca el estado—, así que
// cuando ese carrito cierra por su camino natural su revisión es la 2.
//
// 🔴 Con el literal `1` del sink, el push del cierre llegaba al puente con el
// mismo par (intake_id, 1) que la interpretación: un UPSERT por ese par —el que
// pide el manual del integrador §4— lo descarta como duplicado y el CRM se queda
// con lo que el LLM entendió, no con lo que el cliente compró. Sin un error.
func TestProjector_CartClosed_AnotaLaSegundaCuandoElPipelineYaEscribió(t *testing.T) {
	repo := store.NewMemoryRepository()
	revisions := intakes.NewMemoryStore()
	p := NewProjector(repo, revisions, &envíoEspía{}, intakes.NewMemoryStore())
	ctx := context.Background()
	meta := projectorMeta()

	// 1) El primer item_added abre la solicitud: es la fila `open` sobre la que
	//    caerán las DOS revisiones.
	if err := p.Project(ctx, meta, modules.Effect{Name: EffectItemAdded}); err != nil {
		t.Fatalf("Project(item_added): %v", err)
	}
	abiertas := repo.Intakes()
	if len(abiertas) != 1 {
		t.Fatalf("tras item_added: %d solicitudes, quiero 1", len(abiertas))
	}
	abierta := abiertas[0].ID

	// 2) El pipeline cuelga su interpretación de ESA fila (stages.Draft.cabecera:
	//    pregunta por el evento, encuentra la del carrito y no crea otra).
	if _, err := revisions.InsertRevision(ctx, intakes.Revision{
		IntakeID:  abierta,
		Kind:      intakes.RevisionKindInterpreted,
		Payload:   json.RawMessage(`{"version":1,"items":[]}`),
		CreatedBy: intakes.RevisionBySystem,
	}); err != nil {
		t.Fatalf("sembrar la revisión del pipeline: %v", err)
	}

	// 3) El cliente termina el carrito.
	eff := cartClosedEffect()
	if err := p.Project(ctx, meta, eff); err != nil {
		t.Fatalf("Project(cart_closed): %v", err)
	}

	revs := revisions.Revisions(abierta)
	if len(revs) != 2 {
		t.Fatalf("revisiones de %s: %d, quiero 2 (la del pipeline y la del cierre)", abierta, len(revs))
	}
	if revs[1].Kind != intakes.RevisionKindCart || revs[1].RevisionNo != 2 {
		t.Fatalf("la revisión del cierre es kind=%q no=%d, quiero cart/2", revs[1].Kind, revs[1].RevisionNo)
	}
	if got := eff.Payload["revision_no"]; got != 2 {
		t.Fatalf("revision_no anotado = %#v, quiero 2: el efecto lleva el número de la revisión "+
			"del PIPELINE (o uno inventado) y el push del cierre se perdería como duplicado", got)
	}
}

// TestProjector_CartClosed_NoAnotaRevisionNoSiLaRevisiónFalla: si el INSERT de la
// revisión revienta, el error sube (ya lo cubre FalloDeRevisionSePropaga) y el
// efecto NO se queda con un número a medias. Importa porque el sink emite lo que
// encuentre: un revision_no anotado sin revisión detrás sería un número que no
// existe en intake_revisions, y el puente lo trataría como estado real.
func TestProjector_CartClosed_NoAnotaRevisionNoSiLaRevisiónFalla(t *testing.T) {
	repo := store.NewMemoryRepository()
	p := NewProjector(repo, revisionRota{}, &envíoEspía{}, intakes.NewMemoryStore())

	eff := cartClosedEffect()
	if err := p.Project(context.Background(), projectorMeta(), eff); err == nil {
		t.Fatal("un fallo de la revisión debe propagarse")
	}
	if got, hay := eff.Payload["revision_no"]; hay {
		t.Fatalf("se anotó revision_no = %#v pese a que la revisión no llegó a escribirse", got)
	}
}
