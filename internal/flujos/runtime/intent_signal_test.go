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

// llmEventRule es una regla kind='llm' que ADEMÁS puebla event_kind: la SEGUNDA
// puerta del nacimiento (Plan 054 · F2b, D-A). Con event_kind, la intención que casa
// PARE (o conmuta) el evento de ese tipo — Action=StartEvent, ver config_resolver.go —
// en vez de arrancar el flujo a secas como llmRule.
func llmEventRule(intentName, flowID, eventKind string) trigger.Rule {
	return trigger.Rule{TenantID: testTenant, Kind: trigger.KindLLM, Keyword: intentName, FlowID: flowID, EventKind: eventKind, Enabled: true}
}

// newIntentRuntime arma un runtime con: cart+menu registrados, catálogo sembrado,
// content Router (static+json), PersistSink, ResumePolicy del cart, un ConfigResolver
// con las reglas dadas, un EventStore en memoria (necesario desde el Plan 054 · F2b
// para que una regla llm con event_kind pueda parir de verdad — sin él beginEvent
// trata cualquier Decision como si no hubiera plano de eventos, INV-6), y el gate de
// entitlements (feature llm_intent on/off). now inyecta el reloj del TTL (nil ⇒
// time.Now). `extra` son Options adicionales (Plan 054 · T2.4: algunos tests
// necesitan WithOpeningBuilder para observar la degradación de una intención LLM
// hacia un flujo durable SIN event_kind); las llamadas que no lo necesitan lo omiten
// y no cambian de conducta.
func newIntentRuntime(t *testing.T, feature bool, now func() time.Time, rules []trigger.Rule, extra ...runtime.Option) (*runtime.Runtime, *store.MemoryRepository, *fakeSender, *contact.MemoryResolver, *memEventStore) {
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
	evs := newMemEventStore(time.Now())
	opts := []runtime.Option{
		runtime.WithEventSink(persistSinkWith(repo)),
		cartResumeOpt(repo),
		runtime.WithTriggerResolver(trigger.NewConfigResolver(ts)),
		runtime.WithEntitlements(ents),
		runtime.WithEventStore(evs),
	}
	if now != nil {
		opts = append(opts, runtime.WithClock(now))
	}
	opts = append(opts, extra...)
	rt := runtime.New(repo, eng, sender, fakeResolver{tenantID: testTenant}, contacts, discardLogger(), opts...)
	return rt, repo, sender, contacts, evs
}

// TestIntent_LLMRule_PreLoadsCart (T7 + T8) documenta que una intención LLM
// arranca el carrito PRE-CARGADO, CON evento padre.
//
// ⚠️ Historia del test (para quien lea el blame): el frente T2.3/T2.4 del Plan 054
// lo había reescrito para afirmar la DEGRADACIÓN (WithOpeningBuilder + oferta) — en
// ese momento la regla kind='llm' NUNCA poblaba EventKind (config_resolver.go
// arrancaba {Action:Start} a secas) y la guarda D-054.5 (ningún flujo durable
// arranca sin evento) tumbaba el pre-carga siempre. Ese era exactamente el hallazgo
// que abrió el frente F2b: el Plan 043 · T2.5/REQ-01b declaraba TRES puertas para
// el nacimiento del evento y la segunda —«una intención LLM que mapee a un
// event_kind»— nunca se había construido. F2b la construye (ver
// config_resolver.go, rama sig.Intent != nil): una regla llm con event_kind
// poblado ahora produce {Action:StartEvent, EventKind, FlowID, Params, IntentName},
// así que entra por beginEvent → birthEvent → enterEventFlow → startLocked CON
// eventID no vacío, la guarda D-054.5 no corta, y el Prime del carrito recibe sus
// intent_params con padre. Este test vuelve a fijar el pre-carga — la versión
// anterior a T2.3/T2.4 (git show 73ed7cc~1) probaba lo mismo SIN evento; esta
// versión prueba lo mismo CON él, que es la diferencia que F2b introduce.
func TestIntent_LLMRule_PreLoadsCart(t *testing.T) {
	rt, repo, sender, contacts, evs := newIntentRuntime(t, true, nil,
		[]trigger.Rule{llmEventRule("pedido", testCartFlow, trigger.EventKindCart)})
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
	// LA diferencia con la versión anterior a T2.3/T2.4: el arranque tiene padre.
	if st.EventID == "" {
		t.Fatalf("el pre-carga debe llevar EventID no vacío (evento padre, Plan 054 · F2b)")
	}
	alive := evs.alive()
	if len(alive) != 1 || alive[0].ID != st.EventID || alive[0].Kind != trigger.EventKindCart {
		t.Fatalf("debe haber nacido UN evento cart, el mismo que apunta flow_state: %+v (st.EventID=%q)", alive, st.EventID)
	}
	cs, ok := st.Vars["cart"].(map[string]any)
	if !ok || cs["level"] != "continue" {
		t.Fatalf("el carrito debe quedar en la confirmación de ítem (continue), got %+v", st.Vars["cart"])
	}
	// item_added abrió la solicitud "open" (design.md §3.4) y quedó en flow_events.
	if openIntakeCount(repo, "open") != 1 {
		t.Fatalf("el pre-add debe abrir 1 solicitud open, got %+v", repo.Intakes())
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
	rt, repo, sender, contacts, _ := newIntentRuntime(t, false, nil, []trigger.Rule{llmRule("pedido", testCartFlow)})
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
	if len(repo.Intakes()) != 0 {
		t.Fatalf("sin la feature no debe abrir solicitudes, got %+v", repo.Intakes())
	}
}

// TestIntent_LiveConversation_TextWins (T7): con una conversación viva, la intención
// NO interfiere: el texto manda (engine.Step), no se re-dispara la regla llm.
func TestIntent_LiveConversation_TextWins(t *testing.T) {
	rt, repo, sender, contacts, _ := newIntentRuntime(t, true, nil, []trigger.Rule{llmRule("pedido", testCartFlow)})
	ctx := context.Background()
	// Sembrado directo (no Start): desde Plan 054 · T2.3 el carrito es SIEMPRE
	// durable y Start() ya no puede abrirlo — ver seedCartOpen (cart_resume_test.go).
	// Lo que este test mide (que el texto manda sobre una conversación viva) es
	// ortogonal a CÓMO se abrió esa conversación.
	seedCartOpen(t, repo, contacts)
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
	if len(repo.Intakes()) != 0 {
		t.Fatalf("navegar no debe abrir solicitudes (la intención no pre-cargó), got %+v", repo.Intakes())
	}
}

// seedConversationTTL siembra la config del tenant con el TTL conversacional pedido
// PARTIENDO DE LOS DEFAULTS (store.DefaultTenantSettings), no de un TenantSettings
// construido campo a campo. No es estilo, es corrección del banco de pruebas:
// MemoryRepository.SetTenantSettings siembra la fila LITERALMENTE —como un INSERT que
// nombra todas las columnas—, así que lo que no rellenes queda en el cero de Go. Con
// EventInactivityTTL ese cero NO es «no configurado»: significa «SIN VENCIMIENTO», un
// override explícito de la empresa (D-043.7), mientras que la misma fila creada en
// Postgres sin nombrar la columna trae el DEFAULT 7200 (2 h, migración 0052). Un tenant
// sembrado a mano describiría, por tanto, una configuración que producción no tiene — y
// estos tests seguirían en verde el día que la Ola 2 del Plan 043 cablee el reloj.
//
// La relectura con su aserción no sobra: es lo único que impide volver a la siembra a
// mano sin que nada se queje. El t.Logf deja el valor a la vista con `go test -v`.
func seedConversationTTL(t *testing.T, repo *store.MemoryRepository, ttl time.Duration) {
	t.Helper()
	seeded := store.DefaultTenantSettings(testTenant)
	seeded.ConversationTTL = ttl
	repo.SetTenantSettings(seeded)

	got, err := repo.GetTenantSettings(context.Background(), testTenant)
	if err != nil {
		t.Fatalf("releer la config sembrada: %v", err)
	}
	if got.EventInactivityTTL != store.DefaultEventInactivityTTL {
		t.Fatalf("el tenant sembrado trae EventInactivityTTL=%v y debe traer el default de plataforma %v: siembra desde store.DefaultTenantSettings, no a mano (un 0 aquí significa «sin vencimiento», que no es lo que hace Postgres)",
			got.EventInactivityTTL, store.DefaultEventInactivityTTL)
	}
	t.Logf("tenant sembrado desde defaults: EventInactivityTTL=%v · EventHistoryTTL=%v · ConversationTTL=%v · PageSize=%d · OrderTTL=%v",
		got.EventInactivityTTL, got.EventHistoryTTL, got.ConversationTTL, got.PageSize, got.OrderTTL)
}

// TestConversationTTL_NotExpired_KeepsLiveConversation (T9): con TTL configurado pero
// dentro del plazo, la conversación viva NO vence: el entrante avanza normal.
func TestConversationTTL_NotExpired_KeepsLiveConversation(t *testing.T) {
	// Reloj +1min contra un TTL de 1h ⇒ NO vencido.
	clock := func() time.Time { return time.Now().Add(time.Minute) }
	rt, repo, _, contacts, _ := newIntentRuntime(t, true, clock, []trigger.Rule{llmRule("pedido", testCartFlow)})
	seedConversationTTL(t, repo, time.Hour)
	ctx := context.Background()
	// Sembrado directo (no Start): ver el comentario de TestIntent_LiveConversation_TextWins.
	seedCartOpen(t, repo, contacts)
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
// DESCARTA y el entrante se trata como arranque nuevo; con intención presente Y
// event_kind en la regla, arranca el flujo llm PRE-CARGADO CON evento padre — eso
// es lo que este test prueba de nuevo.
//
// ⚠️ Historia (para quien lea el blame): el frente T2.3/T2.4 del Plan 054 lo había
// reescrito para probar que la degradación queda MUDA (MD-054.2) — en ese momento
// la regla llm no poblaba EventKind y la guarda D-054.5 tumbaba cualquier re-carga
// del carrito tras el TTL. F2b construye la segunda puerta del nacimiento
// (config_resolver.go, rama sig.Intent != nil): con la regla anotada con
// event_kind, el arranque tras el TTL PARE su propio evento y el pre-carga vuelve a
// funcionar — igual que TestIntent_LLMRule_PreLoadsCart, con la diferencia de que
// aquí el arranque viene precedido de un estado VIEJO que el TTL tiene que soltar
// primero.
func TestConversationTTL_Expired_RestartsViaLLM(t *testing.T) {
	// Reloj +2h contra un TTL de 1h ⇒ vencido.
	clock := func() time.Time { return time.Now().Add(2 * time.Hour) }
	rt, repo, sender, contacts, evs := newIntentRuntime(t, true, clock,
		[]trigger.Rule{llmEventRule("pedido", testCartFlow, trigger.EventKindCart)})
	seedConversationTTL(t, repo, time.Hour)
	ctx := context.Background()
	// Conversación vieja: un carrito recién iniciado en L1 (sin líneas ni solicitud).
	// Sembrado directo (no Start): ver el comentario de TestIntent_LiveConversation_TextWins.
	cid := seedCartOpen(t, repo, contacts)
	before := sender.count()
	// Llega una intención tras el vencimiento: el TTL descarta el estado viejo y
	// arranca llm — que ahora PARE su propio evento y pre-carga.
	m := incomingIntent(testContact, "quiero un flan", "wamid.ttlexp", "pedido", map[string]string{"producto": "flan"})
	if err := rt.HandleIncoming(ctx, testSession, m); err != nil {
		t.Fatalf("HandleIncoming: %v", err)
	}
	if got := strings.Join(sender.texts()[before:], "\n"); !strings.Contains(got, "Agregué") || !strings.Contains(got, "Flan") {
		t.Fatalf("tras el TTL vencido, la intención debe arrancar el carrito pre-cargado: %q", got)
	}
	st := loadState(t, repo, cid)
	cs, ok := st.Vars["cart"].(map[string]any)
	if !ok || cs["level"] != "continue" {
		t.Fatalf("el estado viejo debe descartarse y arrancar pre-cargado (continue), got %+v", st.Vars["cart"])
	}
	// LA diferencia con la versión anterior a T2.3/T2.4: el arranque tiene padre.
	if st.EventID == "" {
		t.Fatalf("el arranque tras el TTL debe llevar EventID no vacío (evento padre, Plan 054 · F2b)")
	}
	alive := evs.alive()
	if len(alive) != 1 || alive[0].ID != st.EventID || alive[0].Kind != trigger.EventKindCart {
		t.Fatalf("debe haber nacido UN evento cart, el mismo que apunta flow_state: %+v (st.EventID=%q)", alive, st.EventID)
	}
	if openIntakeCount(repo, "open") != 1 {
		t.Fatalf("el pre-add tras el TTL debe abrir 1 solicitud, got %+v", repo.Intakes())
	}
}
