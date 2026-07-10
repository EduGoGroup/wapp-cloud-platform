package runtime_test

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/content"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/cart"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/menu"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/survey"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

const testCartFlow = "carrito-flow"

// cartCatalogBlob es el blob por-tenant que sirve el adapter json (tenant_content):
// Bebidas (Café, Té) y Postres (Flan). Forma exacta del §3.1 del diseño.
const cartCatalogBlob = `{"categories":[
	{"code":"1","label":"Bebidas","items":[
		{"code":"1","sku":"CAFE","label":"Café","price":2.5,"description":"Espresso doble"},
		{"code":"2","sku":"TE","label":"Té","price":2.0,"description":"Verde o negro"}]},
	{"code":"2","label":"Postres","items":[
		{"code":"1","sku":"FLAN","label":"Flan","price":3.0,"description":"Casero"}]}]}`

// cartFlow arma un flujo de UN solo nodo "cart" que resuelve su catálogo por el
// adapter json (source "json", ref "catalogo") — design.md §4/§9.A.
func cartFlow(flowID string) model.Flow {
	return model.Flow{
		FlowID:  flowID,
		Initial: "cart",
		Nodes: map[string]model.Node{
			"cart": {Type: "cart", Content: &model.ContentRef{Source: "json", Ref: "catalogo"}},
		},
	}
}

// newCartRuntime arma un runtime con el módulo cart registrado, el catálogo
// sembrado en tenant_content, el content Router (static+json) y el PersistSink
// cableado (proyecta orders + flow_events). Igual patrón que newSurveyRuntime.
func newCartRuntime(t *testing.T) (*runtime.Runtime, *store.MemoryRepository, *fakeSender, *contact.MemoryResolver) {
	t.Helper()
	repo := store.NewMemoryRepository()
	repo.SetTenantContent(testTenant, "catalogo", []byte(cartCatalogBlob))
	if _, err := repo.InsertDefinition(context.Background(), testTenant, cartFlow(testCartFlow)); err != nil {
		t.Fatalf("sembrar definición cart: %v", err)
	}
	reg := modules.NewRegistry()
	reg.Register(menu.New())
	reg.Register(survey.New())
	reg.Register(cart.New())
	eng := engine.New(reg, engine.WithContentSource(
		content.NewRouter(content.NewStatic(), content.NewJSON(repo))))
	sender := &fakeSender{}
	contacts := contact.NewMemoryResolver(repo)
	rt := runtime.New(repo, eng, sender, fakeResolver{tenantID: testTenant}, contacts, discardLogger(),
		runtime.WithEventSink(persistSinkWith(repo)), cartResumeOpt(repo))
	return rt, repo, sender, contacts
}

// cartAddCafe navega Bebidas→Café→Agregar→cantidad(2), dejando el carrito en L5
// (continue) con una orden "open" (el primer item_added la abre). base da waIDs
// únicos por invocación para no chocar con la dedupe por last_wa_message_id.
func cartAddCafe(t *testing.T, rt *runtime.Runtime, base string) {
	t.Helper()
	ctx := context.Background()
	for i, in := range []string{"1", "1", "2", "2"} {
		if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, in, base+"-"+strconv.Itoa(i))); err != nil {
			t.Fatalf("HandleIncoming %q: %v", in, err)
		}
	}
}

func hasFlowEvent(repo *store.MemoryRepository, name string) bool {
	for _, e := range repo.FlowEvents() {
		if e.Name == name {
			return true
		}
	}
	return false
}

func openOrderCount(repo *store.MemoryRepository, status string) int {
	n := 0
	for _, o := range repo.Orders() {
		if o.Status == status {
			n++
		}
	}
	return n
}

// TestCartResume_OrderExpired_ResetsAndNotifies (design.md §4.3/§9.H): con una
// orden "open" vencida por TTL, el siguiente entrante NO se procesa como avance:
// el runtime transiciona la orden a "expired", registra cart_expired en
// flow_events, AVISA al usuario y arranca limpio (L1 categorías, sub-estado
// borrado). Evaluación PEREZOSA, sin cron.
func TestCartResume_OrderExpired_ResetsAndNotifies(t *testing.T) {
	rt, repo, sender, contacts := newCartRuntime(t)
	ctx := context.Background()
	if _, err := rt.Start(ctx, testTenant, testCartFlow, testSession, phoneRef(t, testContact)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	cartAddCafe(t, rt, "add")
	cid := resolveID(t, contacts, testContact)

	orders := repo.Orders()
	if len(orders) != 1 || orders[0].Status != "open" {
		t.Fatalf("esperaba 1 orden open, got %+v", orders)
	}
	// expires_at debe estar fijado a futuro (now + TTL default 1h).
	if orders[0].ExpiresAt.IsZero() || !orders[0].ExpiresAt.After(time.Now()) {
		t.Fatalf("expires_at debe ser futuro tras item_added: %v", orders[0].ExpiresAt)
	}

	// Fuerza el vencimiento (simula el paso del tiempo) llevando expires_at al pasado.
	o := orders[0]
	o.ExpiresAt = time.Now().Add(-time.Minute)
	if err := repo.UpsertOrder(ctx, o); err != nil {
		t.Fatalf("forzar vencimiento: %v", err)
	}

	before := sender.count()
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "2", "resume-1")); err != nil {
		t.Fatalf("resume: %v", err)
	}
	joined := strings.Join(sender.texts()[before:], "\n")
	if !strings.Contains(joined, "expiró") || !strings.Contains(joined, "Elige una categoría") {
		t.Fatalf("resume debía avisar del vencimiento y mostrar L1 fresco: %q", joined)
	}
	if os := repo.Orders(); len(os) != 1 || os[0].Status != "expired" {
		t.Fatalf("esperaba la orden en expired, got %+v", os)
	}
	if !hasFlowEvent(repo, "cart_expired") {
		t.Fatalf("esperaba flow_event cart_expired, got %+v", repo.FlowEvents())
	}
	st := loadState(t, repo, cid)
	if _, ok := st.Vars["cart"]; ok {
		t.Fatalf("el sub-estado del carrito debe borrarse en el reset: %+v", st.Vars)
	}
}

// TestCartResume_AfterCancel_RestartsAndEnablesNewOrder (design.md §3.4/§4.2):
// tras cancelar (9), la orden queda "cancelled" y la conversación NO se queda
// bloqueada: el siguiente entrante arranca un carrito limpio (L1) y un pedido
// nuevo es posible (abre otra orden "open").
func TestCartResume_AfterCancel_RestartsAndEnablesNewOrder(t *testing.T) {
	rt, repo, sender, _ := newCartRuntime(t)
	ctx := context.Background()
	if _, err := rt.Start(ctx, testTenant, testCartFlow, testSession, phoneRef(t, testContact)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	cartAddCafe(t, rt, "add") // carrito en L5 con orden open
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "9", "cancel-1")); err != nil {
		t.Fatalf("cancelar: %v", err)
	}
	if os := repo.Orders(); len(os) != 1 || os[0].Status != "cancelled" {
		t.Fatalf("esperaba la orden en cancelled, got %+v", os)
	}

	// Reanudar tras cancelar → arranca limpio (L1), sin conversación viva bloqueando.
	before := sender.count()
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "hola", "resume-1")); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if got := strings.Join(sender.texts()[before:], "\n"); !strings.Contains(got, "Elige una categoría") {
		t.Fatalf("tras cancelar, reanudar debe mostrar L1 fresco: %q", got)
	}

	// Pedido NUEVO posible: agregar otro artículo abre una segunda orden "open".
	cartAddCafe(t, rt, "add2")
	if open, cancelled := openOrderCount(repo, "open"), openOrderCount(repo, "cancelled"); open != 1 || cancelled != 1 {
		t.Fatalf("esperaba 1 open (pedido nuevo) + 1 cancelled, got %+v", repo.Orders())
	}
}

// TestCartStart_AfterCancel_NoBlocking409 (gotcha 409): un /start sobre un carrito
// TERMINADO (cancelado) NO devuelve 409 sino que reinicia; pero mientras el pedido
// está EN CURSO (orden open vigente) sí bloquea con 409 (no se clobbea).
func TestCartStart_AfterCancel_NoBlocking409(t *testing.T) {
	rt, _, sender, _ := newCartRuntime(t)
	ctx := context.Background()
	if _, err := rt.Start(ctx, testTenant, testCartFlow, testSession, phoneRef(t, testContact)); err != nil {
		t.Fatalf("Start 1: %v", err)
	}
	cartAddCafe(t, rt, "add") // orden open vigente

	// Con un pedido en curso, un segundo Start debe seguir devolviendo 409.
	if _, err := rt.Start(ctx, testTenant, testCartFlow, testSession, phoneRef(t, testContact)); !errIsConvExists(err) {
		t.Fatalf("Start con pedido en curso debe dar 409, dio: %v", err)
	}

	// Cancela y reintenta: ahora el carrito está terminado ⇒ Start reinicia (sin 409).
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "9", "cancel-1")); err != nil {
		t.Fatalf("cancelar: %v", err)
	}
	before := sender.count()
	if _, err := rt.Start(ctx, testTenant, testCartFlow, testSession, phoneRef(t, testContact)); err != nil {
		t.Fatalf("Start tras cancelar NO debe dar 409: %v", err)
	}
	if got := strings.Join(sender.texts()[before:], "\n"); !strings.Contains(got, "Elige una categoría") {
		t.Fatalf("Start tras cancelar debe renderizar L1: %q", got)
	}
}

// errIsConvExists evita importar errors solo para el test: compara por el mensaje
// del centinela exportado.
func errIsConvExists(err error) bool {
	return err != nil && err.Error() == runtime.ErrConversationExists.Error()
}

// TestCartNoRegression_MenuNotResetNorPaged: un flujo de MENÚ resumido NO pasa por
// el gate del carrito (no se resetea, no gana la clave cart_page_size en sus
// Vars): la no-regresión de menú/encuesta. Avanza normal a su hoja.
func TestCartNoRegression_MenuNotResetNorPaged(t *testing.T) {
	rt, repo, _, contacts := newMenuRuntimePersist(t)
	ctx := context.Background()
	if _, err := rt.Start(ctx, testTenant, testFlow, testSession, phoneRef(t, testContact)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "wamid.1")); err != nil {
		t.Fatalf("HandleIncoming: %v", err)
	}
	st := loadState(t, repo, resolveID(t, contacts, testContact))
	if !st.Finished() {
		t.Fatalf("el menú debe avanzar a su hoja (Finished), got %+v", st)
	}
	if _, ok := st.Vars["cart_page_size"]; ok {
		t.Fatalf("un flujo de menú NO debe ganar cart_page_size en sus Vars: %+v", st.Vars)
	}
	if len(repo.Orders()) != 0 {
		t.Fatalf("el menú NO debe crear orders: %+v", repo.Orders())
	}
}
