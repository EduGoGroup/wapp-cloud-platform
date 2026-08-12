package runtime_test

import (
	"context"
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
// inyecta el reloj del TTL (nil ⇒ time.Now). `extra` son Options adicionales
// (Plan 054 · T2.4: algunos tests necesitan WithOpeningBuilder para observar la
// degradación de una intención LLM hacia un flujo durable); las llamadas que no lo
// necesitan lo omiten y no cambian de conducta.
func newIntentRuntime(t *testing.T, feature bool, now func() time.Time, rules []trigger.Rule, extra ...runtime.Option) (*runtime.Runtime, *store.MemoryRepository, *fakeSender, *contact.MemoryResolver) {
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
	opts = append(opts, extra...)
	rt := runtime.New(repo, eng, sender, fakeResolver{tenantID: testTenant}, contacts, discardLogger(), opts...)
	return rt, repo, sender, contacts
}

// TestIntent_LLMRule_PreLoadsCart (T7 + T8) documentaba que una intención LLM
// arrancaba el carrito PRE-CARGADO. ⚠️ HALLAZGO de Plan 054 · T2.3/T2.4, no
// anticipado por design.md: una regla kind='llm' que casa produce SIEMPRE
// Decision{Action:Start} (config_resolver.go, rama de intención, líneas ~72-80) —
// NUNCA Action:StartEvent ni EventKind poblado. decisionFor() (config_resolver.go
// ~172) solo puebla EventKind para kind='event_start'. Por eso startFromDecision ve
// dec.EventKind=="" y beginEvent no la consume: cae a startPlainFlow con eventID=""
// como CUALQUIER keyword. La "puerta LLM con event_kind" que design.md §4 asume ya
// construida NO EXISTE hoy — es la brecha que probablemente cierre el Plan 044
// (Carrito LLM 2.0, el norte de docs/plans/). Con la guarda de D-054.5 puesta, una
// intención LLM hacia un flujo durable (cart) se degrada EXACTAMENTE como una
// keyword (T2.4): el cliente ve la oferta del despachador, NO el carrito
// pre-cargado. Este test queda REESCRITO para probar esa degradación (con
// WithOpeningBuilder cableado) en vez de fingir que el pre-carga sigue intacto —
// Jhoan decide si Plan 044 necesita que decisionFor() también arme un EventKind
// para 'llm' (ver el informe del frente F2).
func TestIntent_LLMRule_PreLoadsCart(t *testing.T) {
	ofrece := &fakeOpening{apertura: ofertaConOpciones()}
	rt, repo, sender, contacts := newIntentRuntime(t, true, nil, []trigger.Rule{llmRule("pedido", testCartFlow)},
		runtime.WithOpeningBuilder(ofrece))
	ctx := context.Background()

	m := incomingIntent(testContact, "quiero 2 cafés", "wamid.llm", "pedido", map[string]string{"producto": "cafe", "cantidad": "2"})
	if err := rt.HandleIncoming(ctx, testSession, m); err != nil {
		t.Fatalf("HandleIncoming intent: %v", err)
	}
	if sender.count() != 1 {
		t.Fatalf("la degradación debe enviar 1 saliente (la oferta), envió %d", sender.count())
	}
	if got := sender.texts()[0]; got != ofrece.apertura.Text {
		t.Fatalf("el saliente debe ser la oferta del despachador, NO el carrito pre-cargado.\n got: %q\nwant: %q", got, ofrece.apertura.Text)
	}
	// El carrito NUNCA arrancó: ni flow_state propio, ni intake, ni item_added.
	if st, ok, err := repo.Load(ctx, store.Key{TenantID: testTenant, SessionID: testSession, ContactID: resolveID(t, contacts, testContact)}); err != nil {
		t.Fatalf("cargar estado: %v", err)
	} else if ok && st.FlowID == testCartFlow {
		t.Fatalf("el carrito NO debe haber arrancado: flow_state en %q", st.FlowID)
	}
	if openIntakeCount(repo, "open") != 0 {
		t.Fatalf("sin evento, el pre-add NUNCA debe abrir una solicitud: %+v", repo.Intakes())
	}
}

// TestIntent_GateOff_IntentIgnored (T7 gate): sin la feature llm_intent, la intención
// se DESCARTA (camino actual): la regla llm no dispara y —sin keyword ni fallback que
// case el texto— no arranca nada.
func TestIntent_GateOff_IntentIgnored(t *testing.T) {
	rt, repo, sender, contacts := newIntentRuntime(t, false, nil, []trigger.Rule{llmRule("pedido", testCartFlow)})
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
	rt, repo, sender, contacts := newIntentRuntime(t, true, nil, []trigger.Rule{llmRule("pedido", testCartFlow)})
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
	rt, repo, _, contacts := newIntentRuntime(t, true, clock, []trigger.Rule{llmRule("pedido", testCartFlow)})
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
// DESCARTA y el entrante se trata como arranque nuevo, no como avance del estado
// viejo — eso es lo que este test prueba, y SIGUE siendo cierto.
//
// ⚠️ Plan 054 · T2.3/T2.4: lo que YA NO prueba es que la intención "arranque el flujo
// llm (carrito pre-cargado)" tal cual decía el docstring viejo — ver el hallazgo
// documentado en TestIntent_LLMRule_PreLoadsCart: una intención LLM hacia un flujo
// durable se degrada EXACTAMENTE como una keyword. Este runtime no cablea
// WithOpeningBuilder a propósito (esa prueba ya vive en el otro test), así que la
// degradación cae en silencio (MD-054.2): el criterio de ESTE test pasa a ser «el
// estado viejo se descarta y NO SE RESUCITA con contenido pre-cargado», no «se
// re-arranca pre-cargado».
func TestConversationTTL_Expired_RestartsViaLLM(t *testing.T) {
	// Reloj +2h contra un TTL de 1h ⇒ vencido.
	clock := func() time.Time { return time.Now().Add(2 * time.Hour) }
	rt, repo, sender, contacts := newIntentRuntime(t, true, clock, []trigger.Rule{llmRule("pedido", testCartFlow)})
	seedConversationTTL(t, repo, time.Hour)
	ctx := context.Background()
	// Conversación vieja: un carrito recién iniciado en L1 (sin líneas ni solicitud).
	// Sembrado directo (no Start): ver el comentario de TestIntent_LiveConversation_TextWins.
	cid := seedCartOpen(t, repo, contacts)
	before := sender.count()
	// Llega una intención tras el vencimiento: el TTL descarta el estado viejo y lo
	// trata como arranque nuevo — que ahora, por ser durable y sin evento, degrada en
	// silencio en vez de re-arrancar pre-cargado.
	m := incomingIntent(testContact, "quiero un flan", "wamid.ttlexp", "pedido", map[string]string{"producto": "flan"})
	if err := rt.HandleIncoming(ctx, testSession, m); err != nil {
		t.Fatalf("HandleIncoming: %v", err)
	}
	if got := sender.count(); got != before {
		t.Fatalf("sin opening cableado la degradación es MUDA (MD-054.2); no debió enviar nada, envió %d: %q", got-before, sender.texts()[before:])
	}
	// El estado VIEJO no sobrevive (el TTL lo descartó) y NO se resucita con
	// contenido pre-cargado: ninguna solicitud queda open.
	if st, ok, err := repo.Load(ctx, store.Key{TenantID: testTenant, SessionID: testSession, ContactID: cid}); err != nil {
		t.Fatalf("cargar estado: %v", err)
	} else if ok && st.FlowID == testCartFlow {
		t.Fatalf("el carrito NO debe haberse re-arrancado tras el TTL: flow_state en %q", st.FlowID)
	}
	if openIntakeCount(repo, "open") != 0 {
		t.Fatalf("sin evento, el pre-add NUNCA debe abrir una solicitud: %+v", repo.Intakes())
	}
}
