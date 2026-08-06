package cart

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// cartClosedEffect es el efecto de cierre con una línea, tal como lo emite la
// sub-máquina (camino en-proceso: []map[string]any).
func cartClosedEffect() modules.Effect {
	return modules.Effect{
		Kind: "persist",
		Name: EffectCartClosed,
		Payload: map[string]any{
			"total": 5000.0,
			"items": []map[string]any{
				{"sku": "emp-pino", "label": "Empanada de pino", "qty": 2, "unit_price": 2500.0},
			},
		},
	}
}

func projectorMeta() modules.EffectMeta {
	return modules.EffectMeta{TenantID: "tenant-1", ContactID: "contacto-opaco-1", SessionID: "sesion-1"}
}

// TestProjector_CartClosed_EscribeRevisionUno: al cerrar el carrito nace la
// revisión 1 de la solicitud (ADR-0031 §3), con la foto de lo que se cerró.
func TestProjector_CartClosed_EscribeRevisionUno(t *testing.T) {
	repo := store.NewMemoryRepository()
	revisions := intakes.NewMemoryStore()
	p := NewProjector(repo, revisions)
	ctx := context.Background()

	if err := p.Project(ctx, projectorMeta(), cartClosedEffect()); err != nil {
		t.Fatalf("Project(cart_closed): %v", err)
	}

	cerradas := repo.Intakes()
	if len(cerradas) != 1 {
		t.Fatalf("solicitudes cerradas: got %d, want 1", len(cerradas))
	}
	revs := revisions.Revisions(cerradas[0].ID)
	if len(revs) != 1 {
		t.Fatalf("revisiones de %s: got %d, want 1", cerradas[0].ID, len(revs))
	}

	rev := revs[0]
	if rev.RevisionNo != 1 {
		t.Errorf("revision_no: got %d, want 1", rev.RevisionNo)
	}
	if rev.Kind != intakes.RevisionKindCart {
		t.Errorf("kind: got %q, want %q", rev.Kind, intakes.RevisionKindCart)
	}
	if rev.CreatedBy != intakes.RevisionBySystem {
		t.Errorf("created_by: got %q, want %q", rev.CreatedBy, intakes.RevisionBySystem)
	}
	if rev.RenderedText != "" {
		t.Errorf("el cierre de carrito no renderiza texto al cliente: got %q", rev.RenderedText)
	}

	var payload struct {
		Version int     `json:"version"`
		Total   float64 `json:"total"`
		Items   []struct {
			SKU string `json:"sku"`
			Qty int    `json:"qty"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rev.Payload, &payload); err != nil {
		t.Fatalf("payload de la revisión (%s): %v", rev.Payload, err)
	}
	if payload.Version != intakes.RevisionPayloadVersion || payload.Total != 5000 {
		t.Errorf("payload: version=%d total=%v", payload.Version, payload.Total)
	}
	if len(payload.Items) != 1 || payload.Items[0].SKU != "emp-pino" || payload.Items[0].Qty != 2 {
		t.Errorf("la revisión no congeló la línea: %+v", payload.Items)
	}
}

// TestProjector_CartClosed_CuelgaLaRevisionDeLaSolicitudQUE_SeCerro: si ya había
// una solicitud "open", la revisión se le cuelga a ESA, no a una nueva. Es lo que
// prueba que el id que devuelve CloseIntake es el bueno.
func TestProjector_CartClosed_CuelgaLaRevisionDeLaSolicitudAbierta(t *testing.T) {
	repo := store.NewMemoryRepository()
	revisions := intakes.NewMemoryStore()
	p := NewProjector(repo, revisions)
	ctx := context.Background()
	meta := projectorMeta()

	// El primer item_added abre la solicitud; el cierre debe caer sobre ella.
	if err := p.Project(ctx, meta, modules.Effect{Name: EffectItemAdded}); err != nil {
		t.Fatalf("Project(item_added): %v", err)
	}
	abiertas := repo.Intakes()
	if len(abiertas) != 1 {
		t.Fatalf("tras item_added: got %d solicitudes, want 1", len(abiertas))
	}
	abierta := abiertas[0].ID

	if err := p.Project(ctx, meta, cartClosedEffect()); err != nil {
		t.Fatalf("Project(cart_closed): %v", err)
	}
	if total := len(repo.Intakes()); total != 1 {
		t.Fatalf("el cierre no debe crear otra solicitud: got %d, want 1", total)
	}
	if revs := revisions.Revisions(abierta); len(revs) != 1 {
		t.Fatalf("la revisión no quedó colgada de la solicitud abierta %s: %d revisiones", abierta, len(revs))
	}
}

// revisionRota es un RevisionWriter que siempre falla: sirve para comprobar que el
// fallo de la revisión SE PROPAGA (el dispatcher lo loguea) y que no se silencia.
type revisionRota struct{}

var errRevisionRota = errors.New("revisión caída")

func (revisionRota) InsertRevision(context.Context, intakes.Revision) (intakes.Revision, error) {
	return intakes.Revision{}, errRevisionRota
}

// TestProjector_CartClosed_FalloDeRevisionSePropaga: el cierre ya está confirmado
// (la solicitud y sus líneas sobreviven), pero el error sube en vez de tragarse.
func TestProjector_CartClosed_FalloDeRevisionSePropaga(t *testing.T) {
	repo := store.NewMemoryRepository()
	p := NewProjector(repo, revisionRota{})

	err := p.Project(context.Background(), projectorMeta(), cartClosedEffect())
	if !errors.Is(err, errRevisionRota) {
		t.Fatalf("error: got %v, want errRevisionRota", err)
	}
	cerradas := repo.Intakes()
	if len(cerradas) != 1 || cerradas[0].Status != intakeStatusClosed {
		t.Fatalf("el cierre debe sobrevivir al fallo de la revisión: %+v", cerradas)
	}
	if items := repo.IntakeItems(cerradas[0].ID); len(items) != 1 {
		t.Errorf("las líneas del cierre deben estar: got %d, want 1", len(items))
	}
}
