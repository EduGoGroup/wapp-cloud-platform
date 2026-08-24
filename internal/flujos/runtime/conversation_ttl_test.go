// conversation_ttl_test.go cubre el TTL CONVERSACIONAL (Plan 029 · T9): cuándo un
// estado vivo se conserva y cuándo se descarta para tratar el entrante como arranque
// nuevo.
//
// # 🔧 ESTE FICHERO ERA `intent_signal_test.go`, Y SE RENOMBRÓ EN T1.6-4 (D-044.31)
//
// Nació cubriendo la SEÑAL DE INTENCIÓN: el Edge clasificaba el entrante, adjuntaba la
// etiqueta al mensaje (`IncomingMessage.intent`) y `buildSignal` la convertía en
// `trigger.Signal.Intent` tras pasar el gate de la feature `llm_intent`. El TTL vivía
// aquí de prestado, porque sus escenas usaban ese mismo montaje.
//
// T1.6-1 retiró `ClassifiedIntent` del contrato y T1.6-4 dejó de leerlo: **`sig.Intent`
// es hoy siempre nil**. Lo que cubría la mitad «intención» de este fichero dejó de ser
// alcanzable, y lo que queda —el TTL— es lo que da nombre al fichero.
//
// # LOS TRES TESTS QUE SE RETIRARON, Y POR QUÉ NO SE PODÍAN SALVAR
//
//   - `TestIntent_LLMRule_PreLoadsCart` — una regla `kind='llm'` con `event_kind`
//     arrancaba el carrito PRE-CARGADO con los `intent_params` que el clasificador
//     extraía. Sin etiqueta no hay params, y sin params no hay pre-carga: el escenario
//     no se puede montar. ⚠️ Con él se queda sin vigilancia el pre-carga por intención
//     entero (`modules.VarIntentParams`, `EnterPrimed`, el `Prime` del carrito), que
//     sigue en producción SIN un solo productor.
//   - `TestIntent_GateOff_IntentIgnored` — probaba que sin la feature `llm_intent` la
//     etiqueta se descartaba. Ese gate se fue con la etiqueta: `buildSignal` ya no
//     pregunta por ninguna feature porque no hay nada que gatear.
//   - `TestIntent_LiveConversation_TextWins` — probaba que sobre conversación viva el
//     TEXTO manda y la intención no interfiere. Sin intención no hay nada que pueda
//     interferir, y la mitad que sigue viva (el texto avanza el flujo) es exactamente
//     lo que fija `TestConversationTTL_NotExpired_KeepsLiveConversation`, aquí abajo.
//
// Y con ellos se fue `internal/flujos/runtime/intent_scope_test.go` entero (el scoping
// por `ActiveEventKind` de las reglas `llm` desde el runtime). Ese sigue cubierto donde
// hoy se puede ejercer de verdad: `internal/flujos/trigger/intent_scope_test.go`, que
// construye la `Signal` a mano contra el resolver.
package runtime_test

import (
	"context"
	"testing"
	"time"

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

// startEventRule ata un TEXTO EXACTO a un flujo y a un tipo de evento.
//
// 🔧 Ocupa el sitio de `llmEventRule`, que ataba un NOMBRE DE INTENCIÓN a lo mismo. La
// diferencia importa y por eso está escrita: la regla `event_start` casa por texto y se
// resuelve dentro del turno, así que sigue siendo alcanzable; la `llm` casaba por una
// etiqueta que ya nadie produce.
//
// El hermano `eventStartRule` (event_switch_test.go) fija el flujo a `testFlow`; este
// deja elegirlo porque aquí hace falta el del carrito.
func startEventRule(keyword, flowID, eventKind string) trigger.Rule {
	return trigger.Rule{
		TenantID: testTenant, Kind: trigger.KindEventStart, Keyword: keyword,
		MatchType: trigger.MatchExact, EventKind: eventKind, FlowID: flowID, Enabled: true,
	}
}

// newTTLRuntime arma un runtime con: cart+menu+survey registrados, catálogo sembrado,
// content Router (static+json), PersistSink, ResumePolicy del cart, un ConfigResolver
// con las reglas dadas y un EventStore en memoria (sin él beginEvent trata cualquier
// Decision como si no hubiera plano de eventos, INV-6). `now` inyecta el reloj del TTL
// (nil ⇒ time.Now).
//
// 🔧 Es el antiguo `newIntentRuntime` SIN su parámetro `feature bool`. Ese booleano
// encendía la feature `llm_intent`, que gateaba la etiqueta del clasificador dentro de
// `buildSignal`; hoy `buildSignal` no pregunta por ninguna feature, así que el
// parámetro no podía cambiar la conducta de nada y se retiró en vez de dejarlo
// mintiendo.
func newTTLRuntime(t *testing.T, now func() time.Time, rules []trigger.Rule) (
	*runtime.Runtime, *store.MemoryRepository, *fakeSender, *contact.MemoryResolver, *memEventStore,
) {
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
	evs := newMemEventStore(time.Now())
	opts := []runtime.Option{
		runtime.WithEventSink(persistSinkWith(repo)),
		cartResumeOpt(repo),
		runtime.WithTriggerResolver(trigger.NewConfigResolver(ts)),
		runtime.WithEventStore(evs),
	}
	if now != nil {
		opts = append(opts, runtime.WithClock(now))
	}
	rt := runtime.New(repo, eng, sender, fakeResolver{tenantID: testTenant}, contacts, discardLogger(), opts...)
	return rt, repo, sender, contacts, evs
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
//
// Y de paso fija lo que el retirado `TestIntent_LiveConversation_TextWins` decía por su
// cuenta: sobre una conversación viva manda el TEXTO (engine.Step) y no se re-dispara
// ninguna regla de arranque.
func TestConversationTTL_NotExpired_KeepsLiveConversation(t *testing.T) {
	// Reloj +1min contra un TTL de 1h ⇒ NO vencido.
	clock := func() time.Time { return time.Now().Add(time.Minute) }
	rt, repo, _, contacts, _ := newTTLRuntime(t, clock, nil)
	seedConversationTTL(t, repo, time.Hour)
	ctx := context.Background()
	// Sembrado directo (no Start): desde Plan 054 · T2.3 el carrito es SIEMPRE durable
	// y Start() ya no puede abrirlo — ver seedCartOpen (cart_resume_test.go). Lo que
	// este test mide es ortogonal a CÓMO se abrió esa conversación.
	seedCartOpen(t, repo, contacts)
	// Carrito vivo en L1 categorías. Llega "1" (Bebidas): debe avanzar por el texto.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "wamid.ttlok")); err != nil {
		t.Fatalf("HandleIncoming: %v", err)
	}
	st := loadState(t, repo, resolveID(t, contacts, testContact))
	cs, ok := st.Vars["cart"].(map[string]any)
	if !ok || cs["level"] != "articles" || cs["cat_code"] != "1" {
		t.Fatalf("dentro del TTL la conversación viva avanza normal (a artículos de Bebidas), got %+v", st.Vars["cart"])
	}
	if len(repo.Intakes()) != 0 {
		t.Fatalf("navegar no debe abrir solicitudes, got %+v", repo.Intakes())
	}
}

// TestConversationTTL_Expired_RestartsFromScratch (T9): con TTL vencido, el estado vivo
// se DESCARTA y el entrante se trata como ARRANQUE NUEVO — nace su propio evento.
//
// 🔧 Se llamaba `TestConversationTTL_Expired_RestartsViaLLM` y arrancaba por una regla
// `kind='llm'` con `event_kind`, comprobando además que el carrito quedaba PRE-CARGADO
// con los params del clasificador («Agregué… Flan»). Las dos cosas murieron con el push
// (D-044.31): sin etiqueta no hay regla llm que gane, y sin params no hay pre-carga. El
// arranque se ejerce hoy por la puerta que sigue viva —una regla `event_start` que casa
// por texto— y lo que se afirma es lo que el TTL de verdad promete: que el estado viejo
// se suelta y el turno abre un evento NUEVO.
//
// La pieza que hace la prueba limpia es el estado sembrado SIN evento: `seedCartOpen`
// deja `EventID` vacío, así que un `st.EventID` no vacío al final solo puede venir del
// arranque de este turno.
func TestConversationTTL_Expired_RestartsFromScratch(t *testing.T) {
	// Reloj +2h contra un TTL de 1h ⇒ vencido.
	clock := func() time.Time { return time.Now().Add(2 * time.Hour) }
	rt, repo, sender, contacts, evs := newTTLRuntime(t, clock,
		[]trigger.Rule{startEventRule("quiero un flan", testCartFlow, trigger.EventKindCart)})
	seedConversationTTL(t, repo, time.Hour)
	ctx := context.Background()
	// Conversación vieja: un carrito recién iniciado en L1, sin evento padre.
	cid := seedCartOpen(t, repo, contacts)
	if st := loadState(t, repo, cid); st.EventID != "" {
		t.Fatalf("PREMISA ROTA: el estado sembrado debe venir SIN evento para que el assert final signifique algo; EventID=%q", st.EventID)
	}
	if vivos := evs.alive(); len(vivos) != 0 {
		t.Fatalf("PREMISA ROTA: no debe haber ningún evento vivo antes del turno, got %+v", vivos)
	}
	before := sender.count()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "quiero un flan", "wamid.ttlexp")); err != nil {
		t.Fatalf("HandleIncoming: %v", err)
	}

	if sender.count() <= before {
		t.Fatalf("el arranque tras el TTL debe responder algo; salientes %d → %d", before, sender.count())
	}
	st := loadState(t, repo, cid)
	if st.EventID == "" {
		t.Fatalf("el estado viejo debe soltarse y el turno debe arrancar un evento NUEVO; EventID vacío (vars=%+v)", st.Vars)
	}
	alive := evs.alive()
	if len(alive) != 1 || alive[0].ID != st.EventID || alive[0].Kind != trigger.EventKindCart {
		t.Fatalf("debe haber nacido UN evento cart, el mismo que apunta flow_state: %+v (st.EventID=%q)", alive, st.EventID)
	}
}
