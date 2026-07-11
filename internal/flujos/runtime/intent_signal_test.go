package runtime_test

import (
	"context"
	"strings"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/content"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/cart"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/menu"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/survey"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
)

// incomingIntent arma un entrante que trae una intención LLM resuelta (lo que el
// Edge sella en SensitivePayload.intent y el gateway copia a IncomingMessage.Intent).
func incomingIntent(from, text, waID, intentName string, params map[string]string) *cloudlinkv1.IncomingMessage {
	return &cloudlinkv1.IncomingMessage{
		From: from, Text: text, WaMessageId: waID,
		Intent: &cloudlinkv1.ClassifiedIntent{
			Intent: intentName, Params: params, Confidence: 0.9, ConfigVersion: "v1",
		},
	}
}

// llmRule es una regla kind='llm' que ata el nombre de intent al flujo del carrito.
func llmRule(intentName, flowID string) trigger.Rule {
	return trigger.Rule{TenantID: testTenant, Kind: trigger.KindLLM, Keyword: intentName, FlowID: flowID, Enabled: true}
}

// newIntentRuntime arma un runtime con: cart+menu registrados, catálogo sembrado,
// content Router (static+json), PersistSink, ResumePolicy del cart, un ConfigResolver
// con las reglas dadas, y el gate de entitlements (feature llm_intent on/off). now
// inyecta el reloj del TTL (nil ⇒ time.Now).
func newIntentRuntime(t *testing.T, feature bool, now func() time.Time, rules ...trigger.Rule) (*runtime.Runtime, *store.MemoryRepository, *fakeSender, *contact.MemoryResolver) {
	t.Helper()
	ctx := context.Background()
	repo := store.NewMemoryRepository()
	repo.SetTenantContent(testTenant, "catalogo", []byte(cartCatalogBlob))
	if _, err := repo.InsertDefinition(ctx, testTenant, cartFlow(testCartFlow)); err != nil {
		t.Fatalf("sembrar cart flow: %v", err)
	}
	if _, err := repo.InsertDefinition(ctx, testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar menu flow: %v", err)
	}
	ts := trigger.NewMemoryStore()
	for _, r := range rules {
		if _, err := ts.Insert(ctx, r); err != nil {
			t.Fatalf("insert regla: %v", err)
		}
	}
	reg := modules.NewRegistry()
	reg.Register(menu.New())
	reg.Register(survey.New())
	reg.Register(cart.New())
	eng := engine.New(reg, engine.WithContentSource(content.NewRouter(content.NewStatic(), content.NewJSON(repo))))
	sender := &fakeSender{}
	contacts := contact.NewMemoryResolver(repo)
	ents := entitlements.NewFake()
	if feature {
		ents.Enable(testTenant, entitlements.FeatureLLMIntent)
	}
	opts := []runtime.Option{
		runtime.WithEventSink(persistSinkWith(repo)),
		cartResumeOpt(repo),
		runtime.WithTriggerResolver(trigger.NewConfigResolver(ts)),
		runtime.WithEntitlements(ents),
	}
	if now != nil {
		opts = append(opts, runtime.WithClock(now))
	}
	rt := runtime.New(repo, eng, sender, fakeResolver{tenantID: testTenant}, contacts, discardLogger(), opts...)
	return rt, repo, sender, contacts
}

// TestIntent_LLMRule_PreLoadsCart (T7 + T8): con la feature activa, un entrante con
// intención "pedido" casa la regla llm y arranca el carrito PRE-CARGADO con el
// producto extraído, saltando a la confirmación de ítem y abriendo la orden.
func TestIntent_LLMRule_PreLoadsCart(t *testing.T) {
	rt, repo, sender, contacts := newIntentRuntime(t, true, nil, llmRule("pedido", testCartFlow))
	ctx := context.Background()

	m := incomingIntent(testContact, "quiero 2 cafés", "wamid.llm", "pedido", map[string]string{"producto": "cafe", "cantidad": "2"})
	if err := rt.HandleIncoming(ctx, testSession, m); err != nil {
		t.Fatalf("HandleIncoming intent: %v", err)
	}
	if sender.count() != 1 {
		t.Fatalf("el pre-carga debe enviar 1 confirmación, envió %d", sender.count())
	}
	if got := sender.texts()[0]; !strings.Contains(got, "Agregué") || !strings.Contains(got, "Café") || !strings.Contains(got, "Finalizar") {
		t.Fatalf("confirmación de pre-carga inesperada: %q", got)
	}
	st := loadState(t, repo, resolveID(t, contacts, testContact))
	if st.FlowID != testCartFlow {
		t.Fatalf("debe arrancar el flujo del carrito, got %q", st.FlowID)
	}
	cs, ok := st.Vars["cart"].(map[string]any)
	if !ok || cs["level"] != "continue" {
		t.Fatalf("el carrito debe quedar en la confirmación de ítem (continue), got %+v", st.Vars["cart"])
	}
	// item_added abrió la orden "open" (design.md §3.4) y quedó en flow_events.
	if openOrderCount(repo, "open") != 1 {
		t.Fatalf("el pre-add debe abrir 1 orden open, got %+v", repo.Orders())
	}
	if !hasFlowEvent(repo, "item_added") {
		t.Fatalf("el pre-add debe declarar item_added, got %+v", repo.FlowEvents())
	}
	// intent_params consumidos: no persisten en el estado guardado.
	if _, ok := st.Vars[modules.VarIntentParams]; ok {
		t.Fatalf("intent_params debe consumirse tras el pre-add: %+v", st.Vars)
	}
}

// TestIntent_GateOff_IntentIgnored (T7 gate): sin la feature llm_intent, la intención
// se DESCARTA (camino actual): la regla llm no dispara y —sin keyword ni fallback que
// case el texto— no arranca nada.
func TestIntent_GateOff_IntentIgnored(t *testing.T) {
	rt, repo, sender, contacts := newIntentRuntime(t, false, nil, llmRule("pedido", testCartFlow))
	ctx := context.Background()

	m := incomingIntent(testContact, "quiero 2 cafés", "wamid.gate", "pedido", map[string]string{"producto": "cafe", "cantidad": "2"})
	if err := rt.HandleIncoming(ctx, testSession, m); err != nil {
		t.Fatalf("HandleIncoming intent gate-off: %v", err)
	}
	if sender.count() != 0 {
		t.Fatalf("sin la feature la intención se ignora ⇒ 0 envíos, got %d", sender.count())
	}
	if _, ok, lerr := repo.Load(ctx, store.Key{TenantID: testTenant, SessionID: testSession, ContactID: resolveID(t, contacts, testContact)}); lerr != nil || ok {
		t.Fatalf("sin la feature no debe arrancar ni crear estado (ok=%v err=%v)", ok, lerr)
	}
	if len(repo.Orders()) != 0 {
		t.Fatalf("sin la feature no debe abrir órdenes, got %+v", repo.Orders())
	}
}

// TestIntent_LiveConversation_TextWins (T7): con una conversación viva, la intención
// NO interfiere: el texto manda (engine.Step), no se re-dispara la regla llm.
func TestIntent_LiveConversation_TextWins(t *testing.T) {
	rt, repo, sender, contacts := newIntentRuntime(t, true, nil, llmRule("pedido", testCartFlow))
	ctx := context.Background()
	if _, err := rt.Start(ctx, testTenant, testCartFlow, testSession, phoneRef(t, testContact)); err != nil {
		t.Fatalf("Start cart: %v", err)
	}
	// Carrito vivo en L1 categorías. Llega "1" (Bebidas) CON intención "pedido": debe
	// avanzar por el texto (a L2 artículos), NO pre-cargar por la intención.
	before := sender.count()
	m := incomingIntent(testContact, "1", "wamid.live", "pedido", map[string]string{"producto": "flan"})
	if err := rt.HandleIncoming(ctx, testSession, m); err != nil {
		t.Fatalf("HandleIncoming live: %v", err)
	}
	if sender.count() != before+1 {
		t.Fatalf("el avance debe enviar 1 pantalla, got %d", sender.count()-before)
	}
	st := loadState(t, repo, resolveID(t, contacts, testContact))
	cs, ok := st.Vars["cart"].(map[string]any)
	if !ok || cs["level"] != "articles" || cs["cat_code"] != "1" {
		t.Fatalf("el texto '1' debe avanzar a artículos de Bebidas, got %+v", st.Vars["cart"])
	}
	if len(repo.Orders()) != 0 {
		t.Fatalf("navegar no debe abrir órdenes (la intención no pre-cargó), got %+v", repo.Orders())
	}
}

// TestConversationTTL_NotExpired_KeepsLiveConversation (T9): con TTL configurado pero
// dentro del plazo, la conversación viva NO vence: el entrante avanza normal.
func TestConversationTTL_NotExpired_KeepsLiveConversation(t *testing.T) {
	// Reloj +1min contra un TTL de 1h ⇒ NO vencido.
	clock := func() time.Time { return time.Now().Add(time.Minute) }
	rt, repo, _, contacts := newIntentRuntime(t, true, clock, llmRule("pedido", testCartFlow))
	repo.SetTenantSettings(store.TenantSettings{TenantID: testTenant, PageSize: store.DefaultPageSize, OrderTTL: store.DefaultOrderTTL, ConversationTTL: time.Hour})
	ctx := context.Background()
	if _, err := rt.Start(ctx, testTenant, testCartFlow, testSession, phoneRef(t, testContact)); err != nil {
		t.Fatalf("Start cart: %v", err)
	}
	m := incomingIntent(testContact, "1", "wamid.ttlok", "pedido", map[string]string{"producto": "flan"})
	if err := rt.HandleIncoming(ctx, testSession, m); err != nil {
		t.Fatalf("HandleIncoming: %v", err)
	}
	st := loadState(t, repo, resolveID(t, contacts, testContact))
	cs, ok := st.Vars["cart"].(map[string]any)
	if !ok || cs["level"] != "articles" {
		t.Fatalf("dentro del TTL la conversación viva avanza normal, got %+v", st.Vars["cart"])
	}
}

// TestConversationTTL_Expired_RestartsViaLLM (T9): con TTL vencido, el estado vivo se
// DESCARTA y el entrante se trata como arranque nuevo; con intención presente, arranca
// el flujo llm (carrito pre-cargado), no un avance del estado viejo.
func TestConversationTTL_Expired_RestartsViaLLM(t *testing.T) {
	// Reloj +2h contra un TTL de 1h ⇒ vencido.
	clock := func() time.Time { return time.Now().Add(2 * time.Hour) }
	rt, repo, sender, contacts := newIntentRuntime(t, true, clock, llmRule("pedido", testCartFlow))
	repo.SetTenantSettings(store.TenantSettings{TenantID: testTenant, PageSize: store.DefaultPageSize, OrderTTL: store.DefaultOrderTTL, ConversationTTL: time.Hour})
	ctx := context.Background()
	// Conversación vieja: un carrito recién iniciado en L1 (sin líneas ni orden).
	if _, err := rt.Start(ctx, testTenant, testCartFlow, testSession, phoneRef(t, testContact)); err != nil {
		t.Fatalf("Start cart: %v", err)
	}
	before := sender.count()
	// Llega una intención tras el vencimiento: descarta el estado viejo y arranca llm.
	m := incomingIntent(testContact, "quiero un flan", "wamid.ttlexp", "pedido", map[string]string{"producto": "flan"})
	if err := rt.HandleIncoming(ctx, testSession, m); err != nil {
		t.Fatalf("HandleIncoming: %v", err)
	}
	if got := strings.Join(sender.texts()[before:], "\n"); !strings.Contains(got, "Agregué") || !strings.Contains(got, "Flan") {
		t.Fatalf("tras el TTL vencido, la intención debe arrancar el carrito pre-cargado: %q", got)
	}
	st := loadState(t, repo, resolveID(t, contacts, testContact))
	cs, ok := st.Vars["cart"].(map[string]any)
	if !ok || cs["level"] != "continue" {
		t.Fatalf("el estado viejo debe descartarse y arrancar pre-cargado (continue), got %+v", st.Vars["cart"])
	}
	if openOrderCount(repo, "open") != 1 {
		t.Fatalf("el pre-add tras el TTL debe abrir 1 orden, got %+v", repo.Orders())
	}
}
