package cart

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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

// envíoEspía registra las peticiones de línea de envío del proyector. Es un espía
// y no el store de solicitudes de verdad porque en estos tests la solicitud vive
// en el repositorio de flujos: lo que aquí se prueba es QUÉ pide el carrito y
// SOBRE QUÉ solicitud, no cómo se materializa la línea (eso son los tests del
// paquete intakes y el de integración contra Postgres).
type envíoEspía struct {
	llamadas int
	tenantID string
	intakeID string
	política intakes.ShippingPolicy
	falla    error
}

func (e *envíoEspía) EnsureShippingLine(_ context.Context, tenantID, intakeID string, policy intakes.ShippingPolicy) error {
	e.llamadas++
	e.tenantID, e.intakeID, e.política = tenantID, intakeID, policy
	return e.falla
}

// TestProjector_CartClosed_EscribeRevisionUno: al cerrar el carrito nace la
// revisión 1 de la solicitud (ADR-0031 §3), con la foto de lo que se cerró.
func TestProjector_CartClosed_EscribeRevisionUno(t *testing.T) {
	repo := store.NewMemoryRepository()
	revisions := intakes.NewMemoryStore()
	p := NewProjector(repo, revisions, &envíoEspía{})
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
	p := NewProjector(repo, revisions, &envíoEspía{})
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
	p := NewProjector(repo, revisionRota{}, &envíoEspía{})

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

// TestProjector_CartClosed_PersisteLaNotaDelPedido: la indicación del pedido
// (D-041.19) llega desde el efecto hasta la CABECERA de la solicitud, en la misma
// transacción del cierre. Sin este paso, la tecla 3 del resumen escribiría en un
// estado conversacional que se tira al cerrar.
func TestProjector_CartClosed_PersisteLaNotaDelPedido(t *testing.T) {
	repo := store.NewMemoryRepository()
	p := NewProjector(repo, intakes.NewMemoryStore(), &envíoEspía{})

	eff := cartClosedEffect()
	eff.Payload["customer_note"] = "dejarlo en portería"
	if err := p.Project(context.Background(), projectorMeta(), eff); err != nil {
		t.Fatalf("Project(cart_closed): %v", err)
	}

	cerradas := repo.Intakes()
	if len(cerradas) != 1 {
		t.Fatalf("solicitudes cerradas: got %d, want 1", len(cerradas))
	}
	if cerradas[0].CustomerNote != "dejarlo en portería" {
		t.Fatalf("customer_note persistida = %q", cerradas[0].CustomerNote)
	}
	// INV-13: el total de la cabecera es el del payload, sin sumar nada por indicar.
	if cerradas[0].Total != 5000 {
		t.Fatalf("total=%v; la indicación movió el dinero", cerradas[0].Total)
	}
}

// TestProjector_CartClosed_SinNotaEsCadenaVacía: un cierre sin la clave (el
// carrito que no comentó, y todos los emitidos antes de T4.1c) cierra igual. Es la
// misma tolerancia que ya tenía `customization` por línea.
func TestProjector_CartClosed_SinNotaEsCadenaVacía(t *testing.T) {
	repo := store.NewMemoryRepository()
	p := NewProjector(repo, intakes.NewMemoryStore(), &envíoEspía{})

	if err := p.Project(context.Background(), projectorMeta(), cartClosedEffect()); err != nil {
		t.Fatalf("Project(cart_closed): %v", err)
	}
	cerradas := repo.Intakes()
	if len(cerradas) != 1 || cerradas[0].CustomerNote != "" {
		t.Fatalf("sin indicación, la cabecera guarda la cadena vacía: %+v", cerradas)
	}
}

// TestProjector_CartClosed_PideLaLíneaDeEnvío: el cierre pide la línea de envío
// SOBRE LA SOLICITUD QUE CERRÓ y con la política del carrito
// (ShippingOnlyIfZones, D-041.11). El id importa tanto como la política: pedirla
// sobre otra solicitud le cobraría el envío a un pedido ajeno.
func TestProjector_CartClosed_PideLaLíneaDeEnvío(t *testing.T) {
	repo := store.NewMemoryRepository()
	envío := &envíoEspía{}
	p := NewProjector(repo, intakes.NewMemoryStore(), envío)

	if err := p.Project(context.Background(), projectorMeta(), cartClosedEffect()); err != nil {
		t.Fatalf("Project(cart_closed): %v", err)
	}

	cerradas := repo.Intakes()
	if len(cerradas) != 1 {
		t.Fatalf("solicitudes cerradas: got %d, want 1", len(cerradas))
	}
	if envío.llamadas != 1 {
		t.Fatalf("peticiones de línea de envío: got %d, want 1", envío.llamadas)
	}
	if envío.intakeID != cerradas[0].ID || envío.tenantID != projectorMeta().TenantID {
		t.Errorf("se pidió el envío de (tenant=%q, solicitud=%q); quiero (%q, %q)",
			envío.tenantID, envío.intakeID, projectorMeta().TenantID, cerradas[0].ID)
	}
	if envío.política != intakes.ShippingOnlyIfZones {
		t.Errorf("política=%v; el cierre del carrito NO mete línea «por confirmar» a quien no cobra envío",
			envío.política)
	}
}

// TestProjector_CartClosed_FalloDelEnvíoSePropaga: como el de la revisión, el
// cierre ya está confirmado pero el error sube (el dispatcher lo loguea) en vez de
// tragarse. Un envío que no se pudo poner y nadie ve es un pedido mal cobrado.
func TestProjector_CartClosed_FalloDelEnvíoSePropaga(t *testing.T) {
	repo := store.NewMemoryRepository()
	errEnvío := errors.New("envío caído")
	p := NewProjector(repo, intakes.NewMemoryStore(), &envíoEspía{falla: errEnvío})

	err := p.Project(context.Background(), projectorMeta(), cartClosedEffect())
	if !errors.Is(err, errEnvío) {
		t.Fatalf("error: got %v, want errEnvío", err)
	}
	if cerradas := repo.Intakes(); len(cerradas) != 1 || cerradas[0].Status != intakeStatusClosed {
		t.Fatalf("el cierre debe sobrevivir al fallo del envío: %+v", cerradas)
	}
}

// TestShippingSKU_VaBajoElPrefijoReservado ata los dos literales que hacen segura
// la línea de envío y que viven en paquetes distintos por fuerza (el carrito
// importa solicitudes, así que solicitudes no puede importar al carrito).
//
// Si alguien cambiara el prefijo reservado sin mover el sku, el validador del
// import dejaría de proteger `_shipping` y un catálogo podría declarar un artículo
// que se hace pasar por la línea del sistema. Esto lo muerde aquí.
func TestShippingSKU_VaBajoElPrefijoReservado(t *testing.T) {
	if !strings.HasPrefix(intakes.ShippingSKU, SystemSKUPrefix) {
		t.Fatalf("el sku de envío %q no cae bajo el prefijo reservado %q",
			intakes.ShippingSKU, SystemSKUPrefix)
	}
	// El MISMO prefijo lo declara el dominio de solicitudes para su edición manual
	// (T4.10): es lo que decide qué líneas sobreviven a un reemplazo. Si los dos
	// literales divergieran, editar un presupuesto borraría su línea de envío —o
	// dejaría al dueño colar una segunda— sin que nadie lo notara hasta el pedido
	// mal cobrado.
	if intakes.ReservedSKUPrefix != SystemSKUPrefix {
		t.Fatalf("el prefijo reservado de solicitudes (%q) y el del catálogo (%q) tienen que ser el mismo",
			intakes.ReservedSKUPrefix, SystemSKUPrefix)
	}
}
