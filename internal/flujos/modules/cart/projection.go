package cart

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// Estados del ciclo de vida de una solicitud (public.intakes). Viven aquí porque la
// proyección del carrito es su dueña (design.md §3.4).
const (
	intakeStatusOpen      = "open"
	intakeStatusClosed    = "closed"
	intakeStatusCancelled = "cancelled"
	intakeStatusExpired   = "expired"
)

// ProjectionStore es lo que el proyector del carrito necesita del almacén para
// materializar sus efectos en intakes/intake_items. Interfaz mínima (ISP) que
// satisface *store.PostgresRepository / *MemoryRepository.
type ProjectionStore interface {
	GetTenantSettings(ctx context.Context, tenantID string) (store.TenantSettings, error)
	GetOpenIntake(ctx context.Context, tenantID, contactID string) (store.Intake, bool, error)
	UpsertIntake(ctx context.Context, o store.Intake) error
	MarkIntakeStatus(ctx context.Context, intakeID, status string, total float64) error
	CloseIntake(ctx context.Context, in store.IntakeClose) (string, error)
}

// RevisionWriter es lo ÚNICO que el proyector necesita del dominio de solicitudes:
// dejar constancia de la versión que acaba de cerrar. Lo satisfacen
// *intakes.Postgres y *intakes.MemoryStore.
//
// Se declara aquí, del lado del consumidor (idioma Go), en vez de importar el
// puerto entero: el carrito no lista solicitudes, no las transiciona y no debe
// poder hacerlo.
type RevisionWriter interface {
	InsertRevision(ctx context.Context, rev intakes.Revision) (intakes.Revision, error)
}

// Projector implementa modules.Projector para los efectos del carrito (Plan 027 ·
// Ola 3 · T8, cierra H10): item_added asegura la solicitud "open" (+refresca TTL),
// cart_closed cierra atómicamente solicitud+líneas, y cart_cancelled/cart_expired
// transicionan la solicitud. Es un adaptador IMPURO; produce EXACTAMENTE las mismas
// filas que producía el switch central del PersistSink (retrofit por efectos, §9.D).
type Projector struct {
	store     ProjectionStore
	revisions RevisionWriter
	now       func() time.Time
}

// NewProjector construye el proyector del carrito sobre el almacén de solicitudes
// y el escritor de revisiones.
//
// `revisions` es un parámetro OBLIGATORIO y no una opción con cero-valor a
// propósito: un proyector sin escritor de revisiones cerraría carritos sin dejar
// rastro y ningún test lo notaría. Si falta, que rompa en compilación.
func NewProjector(s ProjectionStore, revisions RevisionWriter) *Projector {
	return &Projector{store: s, revisions: revisions, now: time.Now}
}

// Handles reconoce los efectos que el carrito PROYECTA a tablas tipadas. Los efectos
// de navegación/telemetría (cart_started, category_selected, …) NO se proyectan (ya
// quedan en flow_events por el sink) y devuelven false.
func (Projector) Handles(name string) bool {
	switch name {
	case EffectItemAdded, EffectCartClosed, EffectCartCancelled, EffectCartExpired:
		return true
	default:
		return false
	}
}

// Project materializa el efecto del carrito. El sink solo lo llama para los efectos
// cuyo Handles devolvió true.
func (p *Projector) Project(ctx context.Context, meta modules.EffectMeta, eff modules.Effect) error {
	switch eff.Name {
	case EffectItemAdded:
		return p.ensureOpenIntake(ctx, meta)
	case EffectCartClosed:
		return p.closeIntake(ctx, meta, eff)
	case EffectCartCancelled:
		return p.transitionOpenIntake(ctx, meta, intakeStatusCancelled)
	case EffectCartExpired:
		return p.transitionOpenIntake(ctx, meta, intakeStatusExpired)
	default:
		return nil
	}
}

// ensureOpenIntake garantiza UNA solicitud "open" para (tenant, contact) al primer
// item_added (design.md §3.4) y FIJA/REFRESCA su TTL (expires_at = now +
// tenant_settings.order_ttl). Idempotente por identidad de negocio: si ya hay abierta
// NO crea otra, pero la "toca" (refresca expires_at) para que el pedido activo no
// venza mientras el usuario sigue agregando. La evaluación del vencimiento es
// perezosa (al reanudar, en la ResumePolicy); aquí solo se fija la marca.
func (p *Projector) ensureOpenIntake(ctx context.Context, meta modules.EffectMeta) error {
	settings, err := p.store.GetTenantSettings(ctx, meta.TenantID)
	if err != nil {
		return err
	}
	expiresAt := p.now().Add(settings.OrderTTL)
	existing, found, err := p.store.GetOpenIntake(ctx, meta.TenantID, meta.ContactID)
	if err != nil {
		return err
	}
	if found {
		existing.ExpiresAt = expiresAt
		return p.store.UpsertIntake(ctx, existing)
	}
	return p.store.UpsertIntake(ctx, store.Intake{
		ID:        uuid.NewString(),
		TenantID:  meta.TenantID,
		ContactID: meta.ContactID,
		SessionID: meta.SessionID,
		Status:    intakeStatusOpen,
		ExpiresAt: expiresAt,
	})
}

// closeIntake proyecta cart_closed: cierra ATÓMICAMENTE la solicitud abierta (o crea una
// "closed" coherente) con el total del payload e inserta TODAS las líneas (fuente de
// verdad). Delega en store.CloseIntake (una transacción, Plan 027 · Ola 1 · T4) y
// después le cuelga la REVISIÓN 1 (ADR-0031 §3): la foto de lo que el carrito armó.
//
// La revisión se escribe FUERA de la transacción del cierre, y eso es una decisión
// consciente con un costo asumido: si su INSERT falla, queda una solicitud cerrada
// y coherente —líneas incluidas— sin su revisión 1, y el error sube al dispatcher,
// que lo LOGUEA sin abortar el avance de la conversación (ver PersistSink.Handle).
// Se acepta porque la verdad de lo vendido es intake_items, no la revisión: perder
// el rastro de la negociación degrada la auditoría, mientras que meter el escritor
// de revisiones dentro de la transacción del motor obligaría al carrito a compartir
// la conexión con otro módulo. Lo que NO ocurre es que se pierdan las dos: el
// cierre ya está confirmado cuando esto se intenta.
func (p *Projector) closeIntake(ctx context.Context, meta modules.EffectMeta, eff modules.Effect) error {
	items := cartItems(eff.Payload)
	total := modules.AsFloat(eff.Payload["total"])

	intakeID, err := p.store.CloseIntake(ctx, store.IntakeClose{
		TenantID:  meta.TenantID,
		ContactID: meta.ContactID,
		SessionID: meta.SessionID,
		Total:     total,
		Items:     items,
	})
	if err != nil {
		return err
	}

	payload, err := intakes.CartRevisionPayload(total, revisionLines(items))
	if err != nil {
		return err
	}
	if _, err := p.revisions.InsertRevision(ctx, intakes.Revision{
		IntakeID:  intakeID,
		Kind:      intakes.RevisionKindCart,
		Payload:   payload,
		CreatedBy: intakes.RevisionBySystem,
	}); err != nil {
		return fmt.Errorf("cart: revisión del cierre de la solicitud %s: %w", intakeID, err)
	}
	return nil
}

// revisionLines congela las líneas del cierre en la forma del payload de la
// revisión. No lleva added_at: la revisión ya está fechada entera y el instante de
// cada línea es del carrito, no de la foto.
func revisionLines(items []store.IntakeItem) []intakes.RevisionLine {
	out := make([]intakes.RevisionLine, 0, len(items))
	for _, it := range items {
		out = append(out, intakes.RevisionLine{
			SKU: it.SKU, Label: it.Label, Qty: it.Qty, UnitPrice: it.UnitPrice,
		})
	}
	return out
}

// transitionOpenIntake lleva la solicitud "open" a cancelled/expired (design.md §3.4).
// Sin solicitud abierta es un no-op sin error (idempotente / nada que transicionar).
func (p *Projector) transitionOpenIntake(ctx context.Context, meta modules.EffectMeta, status string) error {
	intake, found, err := p.store.GetOpenIntake(ctx, meta.TenantID, meta.ContactID)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	return p.store.MarkIntakeStatus(ctx, intake.ID, status, intake.Total)
}

// cartItems extrae las líneas del payload de cart_closed a []store.IntakeItem. Tolera
// ambas formas de la lista: el camino en-proceso ([]map[string]any que construye el
// módulo) y el round-trip JSON ([]any de map[string]any). Ítems mal formados se
// omiten sin panica. El IntakeID lo fija store.CloseIntake.
func cartItems(payload map[string]any) []store.IntakeItem {
	var out []store.IntakeItem
	switch items := payload["items"].(type) {
	case []map[string]any:
		for _, m := range items {
			out = append(out, intakeItemFromMap(m))
		}
	case []any:
		for _, e := range items {
			if m, ok := e.(map[string]any); ok {
				out = append(out, intakeItemFromMap(m))
			}
		}
	}
	return out
}

func intakeItemFromMap(m map[string]any) store.IntakeItem {
	return store.IntakeItem{
		SKU:       modules.AsString(m["sku"]),
		Label:     modules.AsString(m["label"]),
		Qty:       modules.AsInt(m["qty"]),
		UnitPrice: modules.AsFloat(m["unit_price"]),
	}
}
