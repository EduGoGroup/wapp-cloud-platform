package cart_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules/cart"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

// projection_test.go — el proyector del carrito: sus puertos, qué efectos reconoce y
// los dobles que comparten los demás tests de la proyección
// (projection_lines_test.go, projection_close_test.go, projection_buyer_test.go).
//
// El almacén de solicitudes es el gemelo en memoria de conversacion/store, que imita
// a Postgres (D-F8-7, D-F8-8): GetOpenIntake y GetIntakeByEvent devuelven la cabecera
// SIN la nota del cliente y UpsertIntake no la escribe. La fila entera se mira con
// repo.Intakes().

// Aserciones de compilación: el proyector es un modules.Projector y los gemelos en
// memoria satisfacen sus cuatro puertos.
var (
	_ modules.Projector    = (*cart.Projector)(nil)
	_ cart.ProjectionStore = (*store.MemoryRepository)(nil)
	_ cart.ProjectionStore = (*spyStore)(nil)
	_ cart.RevisionWriter  = (*intakes.MemoryStore)(nil)
	_ cart.ShippingEnsurer = (*intakes.MemoryStore)(nil)
	_ cart.ShippingEnsurer = (*shippingSpy)(nil)
	_ cart.BuyerDataWriter = (*intakes.MemoryStore)(nil)
)

const (
	tenantID  = "tenant-1"
	contactID = "contacto-opaco-1"
	sessionID = "sesion-1"
	eventID   = "3f2a1c4e-9b7d-4e21-8a55-6c0f1d2b3a44"
)

// meta es la identidad del turno, con su evento conversacional.
func meta() modules.EffectMeta {
	return modules.EffectMeta{TenantID: tenantID, ContactID: contactID, SessionID: sessionID,
		FlowID: "pedido", FlowVersion: 1, EventID: eventID}
}

// shippingSpy registra las peticiones de línea de envío y devuelve el error fijado.
type shippingSpy struct {
	calls    int
	tenantID string
	intakeID string
	policy   intakes.ShippingPolicy
	err      error
}

func (s *shippingSpy) EnsureShippingLine(_ context.Context, tenantID, intakeID string, policy intakes.ShippingPolicy) error {
	s.calls++
	s.tenantID, s.intakeID, s.policy = tenantID, intakeID, policy
	return s.err
}

// brokenRevisions es un cart.RevisionWriter que siempre falla.
type brokenRevisions struct{ calls int }

var errBrokenRevision = errors.New("revisión caída")

func (b *brokenRevisions) InsertRevision(context.Context, intakes.Revision) (intakes.Revision, error) {
	b.calls++
	return intakes.Revision{}, errBrokenRevision
}

// buyerSpy es un cart.BuyerDataWriter que registra lo que recibe.
type buyerSpy struct {
	puts [][3]string // {intakeID, key, value}
	err  error
}

func (b *buyerSpy) PutBuyerField(_ context.Context, intakeID, key, value string) error {
	b.puts = append(b.puts, [3]string{intakeID, key, value})
	return b.err
}

// spyStore envuelve el gemelo en memoria: anota el orden de las llamadas y deja
// inyectar un error por método.
type spyStore struct {
	*store.MemoryRepository
	calls []string
	fail  map[string]error
}

func newSpyStore() *spyStore {
	return &spyStore{MemoryRepository: store.NewMemoryRepository(), fail: map[string]error{}}
}

func (s *spyStore) hit(name string) error {
	s.calls = append(s.calls, name)
	return s.fail[name]
}

func (s *spyStore) GetOpenIntake(ctx context.Context, tenantID, contactID string) (store.Intake, bool, error) {
	if err := s.hit("GetOpenIntake"); err != nil {
		return store.Intake{}, false, err
	}
	return s.MemoryRepository.GetOpenIntake(ctx, tenantID, contactID)
}

func (s *spyStore) GetIntakeByEvent(ctx context.Context, tenantID, eventID string) (store.Intake, bool, error) {
	if err := s.hit("GetIntakeByEvent"); err != nil {
		return store.Intake{}, false, err
	}
	return s.MemoryRepository.GetIntakeByEvent(ctx, tenantID, eventID)
}

func (s *spyStore) UpsertIntake(ctx context.Context, o store.Intake) error {
	if err := s.hit("UpsertIntake"); err != nil {
		return err
	}
	return s.MemoryRepository.UpsertIntake(ctx, o)
}

func (s *spyStore) ReplaceIntakeItems(ctx context.Context, intakeID string, items []store.IntakeItem) error {
	if err := s.hit("ReplaceIntakeItems"); err != nil {
		return err
	}
	return s.MemoryRepository.ReplaceIntakeItems(ctx, intakeID, items)
}

func (s *spyStore) MarkIntakeStatus(ctx context.Context, intakeID, status string, total float64) error {
	if err := s.hit("MarkIntakeStatus"); err != nil {
		return err
	}
	return s.MemoryRepository.MarkIntakeStatus(ctx, intakeID, status, total)
}

func (s *spyStore) CloseIntake(ctx context.Context, in store.IntakeClose) (string, error) {
	if err := s.hit("CloseIntake"); err != nil {
		return "", err
	}
	return s.MemoryRepository.CloseIntake(ctx, in)
}

// rig es un proyector montado sobre dobles observables.
type rig struct {
	p         *cart.Projector
	repo      *spyStore
	revisions *intakes.MemoryStore
	shipping  *shippingSpy
	buyer     *buyerSpy
}

func newRig() *rig {
	r := &rig{repo: newSpyStore(), revisions: intakes.NewMemoryStore(), shipping: &shippingSpy{}, buyer: &buyerSpy{}}
	r.p = cart.NewProjector(r.repo, r.revisions, r.shipping, r.buyer)
	return r
}

// project proyecta un efecto con la meta dada y falla si da error.
func (r *rig) project(t *testing.T, m modules.EffectMeta, eff modules.Effect) {
	t.Helper()
	if err := r.p.Project(context.Background(), m, eff); err != nil {
		t.Fatalf("Project(%s): %v", eff.Name, err)
	}
}

// onlyIntake exige UNA sola solicitud en el almacén y la devuelve entera.
func (r *rig) onlyIntake(t *testing.T) store.Intake {
	t.Helper()
	all := r.repo.Intakes()
	if len(all) != 1 {
		t.Fatalf("solicitudes = %d, quiero 1: %+v", len(all), all)
	}
	return all[0]
}

// lines devuelve las líneas persistidas de la solicitud con la forma de las del
// carrito, y comprueba que cada una cuelga de ella.
func (r *rig) lines(t *testing.T, intakeID string) []cartLine {
	t.Helper()
	items := r.repo.IntakeItems(intakeID)
	out := make([]cartLine, 0, len(items))
	for _, it := range items {
		if it.IntakeID != intakeID {
			t.Errorf("la línea %+v cuelga de %q, no de %q", it, it.IntakeID, intakeID)
		}
		out = append(out, cartLine{SKU: it.SKU, Label: it.Label, Qty: it.Qty, UnitPrice: it.UnitPrice, Customization: it.Customization})
	}
	return out
}

// captureLog redirige el slog por defecto a un buffer mientras dura el test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buf
}

// snapshotEffect es un item_added con la foto de líneas dada (camino en proceso).
func snapshotEffect(name string, lines ...map[string]any) modules.Effect {
	return modules.Effect{Kind: kindEvent, Name: name, PrivateKeys: []string{"items"},
		Payload: map[string]any{"items": lines}}
}

func line(sku string, qty int, price float64) map[string]any {
	return map[string]any{"sku": sku, "label": "Etiqueta de " + sku, "qty": qty, "unit_price": price}
}

// Handles reconoce, por nombre exacto, los seis efectos que se proyectan; los de pura
// telemetría y cualquier otro nombre, no.
func TestProjector_Handles(t *testing.T) {
	p := newRig().p
	cases := []struct {
		name string
		want bool
	}{
		{cart.EffectItemAdded, true},
		{cart.EffectNoteAdded, true},
		{cart.EffectCartClosed, true},
		{cart.EffectCartCancelled, true},
		{cart.EffectCartExpired, true},
		{cart.EffectBuyerDataCaptured, true},
		{cart.EffectCartStarted, false},
		{cart.EffectCategorySelected, false},
		{cart.EffectItemViewed, false},
		{"survey_answer", false},
		{"Item_Added", false},
		{" item_added", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := p.Handles(tc.name); got != tc.want {
			t.Errorf("Handles(%q) = %v, quiero %v", tc.name, got, tc.want)
		}
	}
}

// Un efecto que el proyector no reconoce no hace nada: nil y ni una llamada.
func TestProjector_Project_IgnoresUnknownEffects(t *testing.T) {
	r := newRig()
	for _, name := range []string{cart.EffectCartStarted, cart.EffectCategorySelected, cart.EffectItemViewed, "otro", ""} {
		eff := modules.Effect{Kind: modules.KindPrivate, Name: name, Payload: map[string]any{"items": []map[string]any{line("X", 1, 1)}}}
		r.project(t, meta(), eff)
	}
	if len(r.repo.calls) != 0 || r.shipping.calls != 0 || len(r.buyer.puts) != 0 || len(r.repo.Intakes()) != 0 {
		t.Errorf("un efecto no reconocido tocó algo: almacén %v, envío %d, comprador %v", r.repo.calls, r.shipping.calls, r.buyer.puts)
	}
}

// Project decide por el NOMBRE del efecto, no por su Kind.
func TestProjector_Project_DispatchesByNameNotKind(t *testing.T) {
	r := newRig()
	eff := snapshotEffect(cart.EffectItemAdded, line("PAN", 1, 2))
	eff.Kind = "lo-que-sea"
	r.project(t, meta(), eff)
	if got := r.onlyIntake(t); got.Status != "open" {
		t.Errorf("solicitud = %+v, quiero una abierta", got)
	}
	if !reflect.DeepEqual(r.repo.calls, []string{"GetOpenIntake", "GetIntakeByEvent", "UpsertIntake", "ReplaceIntakeItems"}) {
		t.Errorf("llamadas = %v", r.repo.calls)
	}
}
