package runtime_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/content"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/cart"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/menu"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/ratelimit"
)

// ---------------------------------------------------------------------------
// Plan 054 · T2.3/T2.4 — la guarda en startLocked (D-054.5) y su degradación
// (D-054.3(b)) cuando el camino sin evento (API/keyword/fallback) intenta arrancar
// un flujo con contenido durable (cart/survey).
// ---------------------------------------------------------------------------

// newDurableGuardRuntime siembra el flujo de menú puro (sampleFlow, no durable) y el
// de carrito (cartFlow, durable) bajo el MISMO tenant, con cart+menu registrados y el
// content Router que el carrito necesita para renderizar categorías. `opening`, si no
// es nil, se cablea con WithOpeningBuilder (T2.4); nil dejа rt.opening == nil a
// propósito (la trampa MD-054.2 de esta tarea). `lim`, si no es nil, se cablea con
// WithReplyLimiter: hace falta para D1 (review de código) — con lim nil (todos los
// tests de este fichero salvo el del burst 1) replyAllowed siempre concede y el
// doble cobro del token anti-loop es invisible; con un limitador REAL de burst
// ajustado, el segundo cobro sí puede fallar. `rules` siembra flow_triggers.
func newDurableGuardRuntime(t *testing.T, opening runtime.OpeningBuilder, lim runtime.ReplyLimiter, rules ...trigger.Rule) (
	*runtime.Runtime, *store.MemoryRepository, *fakeSender, *contact.MemoryResolver, *memEventStore,
) {
	t.Helper()
	ctx := context.Background()
	repo := store.NewMemoryRepository()
	repo.SetTenantContent(testTenant, "catalogo", []byte(cartCatalogBlob))
	if _, err := repo.InsertDefinition(ctx, testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar %s: %v", testFlow, err)
	}
	if _, err := repo.InsertDefinition(ctx, testTenant, cartFlow(testCartFlow)); err != nil {
		t.Fatalf("sembrar %s: %v", testCartFlow, err)
	}
	ts := trigger.NewMemoryStore()
	for _, r := range rules {
		if _, err := ts.Insert(ctx, r); err != nil {
			t.Fatalf("insert regla: %v", err)
		}
	}
	reg := modules.NewRegistry()
	reg.Register(menu.New())
	reg.Register(cart.New())
	eng := engine.New(reg, engine.WithContentSource(
		content.NewRouter(content.NewStatic(), content.NewJSON(repo))))
	sender := &fakeSender{}
	contacts := contact.NewMemoryResolver(repo)
	evs := newMemEventStore(time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC))
	opts := []runtime.Option{
		runtime.WithEventSink(persistSinkWith(repo)),
		cartResumeOpt(repo),
		runtime.WithTriggerResolver(trigger.NewConfigResolver(ts)),
		runtime.WithEventStore(evs),
	}
	if opening != nil {
		opts = append(opts, runtime.WithOpeningBuilder(opening))
	}
	if lim != nil {
		opts = append(opts, runtime.WithReplyLimiter(lim))
	}
	rt := runtime.New(repo, eng, sender, fakeResolver{tenantID: testTenant}, contacts, discardLogger(), opts...)
	return rt, repo, sender, contacts, evs
}

// cartKeywordRuleDurable es la keyword pura (Action=Start, NUNCA pare evento) que
// apunta al flujo durable: el residual del hallazgo #001 que D-054.2 (T1) no cierra
// —solo cierra el `fallback`— y que esta guarda (T2.3) sí.
func cartKeywordRuleDurable() trigger.Rule {
	return trigger.Rule{
		TenantID: testTenant, Kind: trigger.KindKeyword, Keyword: "quiero-comprar",
		MatchType: trigger.MatchExact, FlowID: testCartFlow, Enabled: true,
	}
}

// cartFallbackRuleDurable reproduce EXACTAMENTE la configuración del hallazgo #003:
// el `fallback` del tenant apunta al flujo de único nodo cart.
func cartFallbackRuleDurable() trigger.Rule {
	return trigger.Rule{
		TenantID: testTenant, Kind: trigger.KindFallback, FlowID: testCartFlow, Enabled: true,
	}
}

// --- T2.3: la guarda en startLocked, y su gemelo positivo ---

// TestStart_FlujoDurableSinEvento_RechazaSinRastro cubre el caso NEGATIVO de T2.3
// sobre la puerta API (Start nunca trae eventID: start.go lo pasa "" a propósito):
// arrancar el flujo de único nodo cart por /admin/flows/start (o su gemelo público)
// debe rechazarse con ErrDurableFlowNeedsEvent SIN dejar rastro — cero flow_state,
// cero efectos, cero envío (REQ-054.2). Es el ÚNICO sitio donde un rechazo se puede
// comprobar sin que ninguna degradación lo intercepte primero (Start no atrapa el
// error, a diferencia de startPlainFlow).
func TestStart_FlujoDurableSinEvento_RechazaSinRastro(t *testing.T) {
	rt, repo, sender, contacts := newCartRuntime(t)
	ctx := context.Background()

	_, err := rt.Start(ctx, testTenant, testCartFlow, testSession, phoneRef(t, testContact))
	if !errors.Is(err, runtime.ErrDurableFlowNeedsEvent) {
		t.Fatalf("Start sobre flujo durable sin evento = %v, quiero ErrDurableFlowNeedsEvent", err)
	}
	if sender.count() != 0 {
		t.Fatalf("un rechazo no debe enviar nada, envió %d: %q", sender.count(), sender.texts())
	}
	cid := resolveID(t, contacts, testContact)
	if ok, eerr := repo.Exists(ctx, store.Key{TenantID: testTenant, SessionID: testSession, ContactID: cid}); eerr != nil || ok {
		t.Fatalf("un rechazo no debe dejar flow_state: ok=%v err=%v", ok, eerr)
	}
	if got := repo.Intakes(); len(got) != 0 {
		t.Fatalf("un rechazo no debe dejar ninguna solicitud: %+v", got)
	}
	if got := repo.FlowEvents(); len(got) != 0 {
		t.Fatalf("un rechazo no debe dejar ningún flow_event: %+v", got)
	}
}

// TestStart_FlujoDurableConEventoSimulado_ArrancaNormal es el gemelo POSITIVO exigido
// por la tarea: con eventID != "" el guard NUNCA dispara. La única puerta de
// producción que pasa un eventID real a startLocked es enterEventFlow (Plan 043 ·
// T4.5.1), así que se ejercita por su camino real: un entrante que casa una regla
// event_start (Kind=event_start, EventKind poblado) hace nacer el evento ANTES de
// llamar a startLocked (beginEvent → birthEvent → enterEventFlow), y ese eventID SÍ
// viaja. El flujo debe arrancar exactamente como si no hubiera guarda: se renderiza
// el nodo inicial y el flow_state queda con el evento estampado.
func TestStart_FlujoDurableConEventoSimulado_ArrancaNormal(t *testing.T) {
	rule := trigger.Rule{
		TenantID: testTenant, Kind: trigger.KindEventStart, Keyword: "carrito",
		MatchType: trigger.MatchExact, EventKind: "cart", FlowID: testCartFlow, Enabled: true,
	}
	rt, repo, sender, contacts, _ := newDurableGuardRuntime(t, nil, nil, rule)
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "wamid.evento1")); err != nil {
		t.Fatalf("HandleIncoming: %v", err)
	}
	if sender.count() != 1 {
		t.Fatalf("con eventID real el carrito debe renderizar su nodo inicial (1 envío), hubo %d: %q", sender.count(), sender.texts())
	}
	cid := resolveID(t, contacts, testContact)
	st := loadState(t, repo, cid)
	if st.FlowID != testCartFlow {
		t.Fatalf("flow_state debe apuntar al carrito: %+v", st)
	}
	if st.EventID == "" {
		t.Fatalf("con la guarda satisfecha (eventID real) el puntero debe quedar estampado: %+v", st)
	}
}

// TestHandleIncoming_FlujoSinDurable_EventoArrancaIgualQueAntes es la CUARTA vía de
// la no-regresión de T2.3: un event_start hacia un flujo SIN nodos durables (aquí se
// reusa sampleFlow/testFlow, un menú puro) debe arrancar exactamente igual que antes
// de esta tarea — la guarda de D-054.5 nunca debía tocar este camino. Las otras tres
// vías (API, keyword, fallback-sin-oferta) sobre un flujo no durable ya las cubren,
// sin tocarlas, TestStart_EnviaMenuYCreaEstado, TestMarta_LaKeywordNoVeLaLista y
// TestMarta_CasoVacioCaeAlFallbackDeSiempre (opening_fallback_test.go /
// runtime_engine_test.go): siguen en verde con la guarda puesta, que es la prueba de
// no-regresión para esas tres.
func TestHandleIncoming_FlujoSinDurable_EventoArrancaIgualQueAntes(t *testing.T) {
	// EventKind "cart" es una etiqueta arbitraria aquí (el flujo al que apunta NO es
	// el carrito): la guarda no mira EventKind, solo si LatestDefinition(FlowID)
	// tiene algún nodo durable, así que esta combinación (mal configurada a
	// propósito) es la manera más directa de aislar la variable bajo prueba.
	rule := trigger.Rule{
		TenantID: testTenant, Kind: trigger.KindEventStart, Keyword: "soporte-evento",
		MatchType: trigger.MatchExact, EventKind: "cart", FlowID: testFlow, Enabled: true,
	}
	rt, repo, sender, contacts, _ := newDurableGuardRuntime(t, nil, nil, rule)
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "soporte-evento", "wamid.evmenu")); err != nil {
		t.Fatalf("HandleIncoming: %v", err)
	}
	if sender.count() != 1 {
		t.Fatalf("un flujo no durable debe arrancar igual (1 envío), hubo %d: %q", sender.count(), sender.texts())
	}
	cid := resolveID(t, contacts, testContact)
	st := loadState(t, repo, cid)
	if st.FlowID != testFlow || st.EventID == "" {
		t.Fatalf("el flujo no durable arranca CON el evento que lo parió, sin que la guarda interfiera: %+v", st)
	}
}

// --- T2.4: la degradación en startPlainFlow ---

// TestStartPlainFlow_KeywordDurable_SeDegradaALaOferta reproduce el residual que
// D-054.2 (T1, WithOpeningBuilder) NO cierra: una keyword PURA (nunca pasa por
// openWithOffer) que apunta a un flujo durable. Antes de T2.4 esto arrancaba el
// carrito sin evento (hallazgo #001 exacto); con T2.4 se degrada a la MISMA oferta
// que usa el fallback (D-054.3(b)) y el carrito NUNCA arranca.
func TestStartPlainFlow_KeywordDurable_SeDegradaALaOferta(t *testing.T) {
	ofrece := &fakeOpening{apertura: ofertaConOpciones()}
	rt, repo, sender, contacts, _ := newDurableGuardRuntime(t, ofrece, nil, cartKeywordRuleDurable())
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "quiero-comprar", "wamid.kw1")); err != nil {
		t.Fatalf("HandleIncoming: %v", err)
	}
	if got := sender.count(); got != 1 {
		t.Fatalf("debe enviarse UN solo saliente (la oferta), se enviaron %d: %q", got, sender.texts())
	}
	if got := sender.texts()[0]; got != ofrece.apertura.Text {
		t.Fatalf("el saliente debe ser la oferta del despachador.\n got: %q\nwant: %q", got, ofrece.apertura.Text)
	}
	if ofrece.veces != 1 {
		t.Fatalf("la oferta se pide UNA vez, se pidió %d", ofrece.veces)
	}
	// El carrito NUNCA arrancó: el flow_state que SÍ queda es el de la oferta
	// PENDIENTE (sendOffer→saveMenuState, D-043.3: el menú del despachador no es una
	// fila de flow_definitions), nunca uno con FlowID==testCartFlow — y cero
	// intakes/flow_events, que es la huella que dejaría el carrito si hubiera abierto.
	cid := resolveID(t, contacts, testContact)
	st := loadState(t, repo, cid)
	if st.FlowID == testCartFlow {
		t.Fatalf("el carrito NO debe arrancar: flow_state quedó en %q", st.FlowID)
	}
	if got := repo.Intakes(); len(got) != 0 {
		t.Fatalf("el carrito no debe haber abierto ninguna solicitud: %+v", got)
	}
	if got := repo.FlowEvents(); len(got) != 0 {
		t.Fatalf("el carrito no debe haber dejado ningún flow_event: %+v", got)
	}
}

// TestOpenWithOffer_FallbackDurable_SeDegradaALaOferta reproduce el hallazgo #003
// EXACTO: un `fallback` (Action=Fallback, sí pasa por openWithOffer) que apunta al
// flujo de único nodo cart. Con la oferta disponible (event_start sembrados), la
// vía natural es openWithOffer→sendOffer y NUNCA se llega a startPlainFlow, así que
// este test fija que el camino directo también queda cubierto si algún día
// openWithOffer decide no ofrecer (p. ej. un BuildOpening que solo lista OTROS
// tipos): la guarda de startPlainFlow sigue ahí como red.
func TestOpenWithOffer_FallbackDurable_SeDegradaALaOferta(t *testing.T) {
	ofrece := &fakeOpening{apertura: ofertaConOpciones()}
	rt, repo, sender, contacts, _ := newDurableGuardRuntime(t, ofrece, nil, cartFallbackRuleDurable())
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "buenas tardes", "wamid.fb1")); err != nil {
		t.Fatalf("HandleIncoming: %v", err)
	}
	if got := sender.count(); got != 1 {
		t.Fatalf("debe enviarse UN solo saliente (la oferta), se enviaron %d: %q", got, sender.texts())
	}
	if got := sender.texts()[0]; got != ofrece.apertura.Text {
		t.Fatalf("el saliente debe ser la oferta del despachador (INV-20 no se rompe).\n got: %q\nwant: %q", got, ofrece.apertura.Text)
	}
	// Igual que arriba: el flow_state que queda es el de la oferta pendiente, nunca
	// el del carrito (D-043.3), y cero solicitudes/flow_events.
	cid := resolveID(t, contacts, testContact)
	st := loadState(t, repo, cid)
	if st.FlowID == testCartFlow {
		t.Fatalf("el carrito del fallback NO debe haber arrancado: flow_state en %q", st.FlowID)
	}
	if got := repo.Intakes(); len(got) != 0 {
		t.Fatalf("el fallback durable no debe haber abierto ninguna solicitud: %+v", got)
	}
}

// TestStartPlainFlow_KeywordDurable_SinOpeningNoPanickeaYSeQuedaMudo cubre la trampa
// #1 de la tarea (MD-054.2): con rt.opening == nil (Option nunca cableada — sigue
// siendo opcional pese a T1) no hay a dónde degradar. El contrato exigido es NO
// PANIC: se registra y el contacto se queda sin respuesta (silencio elegido, no un
// crash ni una reentrada en startPlainFlow). Este corner lo cierra D-054.8/T2.7 en
// tiempo de configuración — OTRO frente, no implementado aquí.
func TestStartPlainFlow_KeywordDurable_SinOpeningNoPanickeaYSeQuedaMudo(t *testing.T) {
	rt, repo, sender, contacts, _ := newDurableGuardRuntime(t, nil, nil, cartKeywordRuleDurable())
	ctx := context.Background()

	err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "quiero-comprar", "wamid.nopanic"))
	if err != nil {
		t.Fatalf("sin opening cableado, HandleIncoming NO debe propagar error: %v", err)
	}
	if got := sender.count(); got != 0 {
		t.Fatalf("sin oferta a la que degradar, el contacto se queda MUDO (0 envíos), hubo %d: %q", got, sender.texts())
	}
	cid := resolveID(t, contacts, testContact)
	if ok, eerr := repo.Exists(ctx, store.Key{TenantID: testTenant, SessionID: testSession, ContactID: cid}); eerr != nil || ok {
		t.Fatalf("tampoco debe quedar flow_state: ok=%v err=%v", ok, eerr)
	}
}

// TestStartPlainFlow_KeywordDurable_OfertaVaciaTampocoReentra es la MISMA trampa
// #1/MD-054.2 pero con rt.opening SÍ cableado y devolviendo una oferta VACÍA
// (Offering.Empty, trampa #3): buildOpeningOffer también da ok=false aquí, así que
// el resultado observable debe ser IDÉNTICO al de opening==nil — sin panic, sin
// reentrar en startPlainFlow (que repetiría la guarda sobre el MISMO flujo durable
// en bucle), silencio.
func TestStartPlainFlow_KeywordDurable_OfertaVaciaTampocoReentra(t *testing.T) {
	ofrece := &fakeOpening{apertura: events.Offering{}} // cero kinds, cero rescatables.
	rt, repo, sender, contacts, _ := newDurableGuardRuntime(t, ofrece, nil, cartKeywordRuleDurable())
	ctx := context.Background()

	err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "quiero-comprar", "wamid.vacio"))
	if err != nil {
		t.Fatalf("con oferta vacía, HandleIncoming NO debe propagar error: %v", err)
	}
	if got := sender.count(); got != 0 {
		t.Fatalf("oferta vacía sin nada que degradar: 0 envíos, hubo %d: %q", got, sender.texts())
	}
	if ofrece.veces != 1 {
		t.Fatalf("la oferta se pide UNA vez (y sale vacía), se pidió %d", ofrece.veces)
	}
	cid := resolveID(t, contacts, testContact)
	if ok, eerr := repo.Exists(ctx, store.Key{TenantID: testTenant, SessionID: testSession, ContactID: cid}); eerr != nil || ok {
		t.Fatalf("tampoco debe quedar flow_state: ok=%v err=%v", ok, eerr)
	}
}

// TestOpenWithOffer_CasoVacioSigueCayendoAlFallback es la REGRESIÓN de la trampa #3
// (Offering.Empty): sobre la rama Fallback DE SIEMPRE (D-043.20/INV-20), con una
// oferta vacía el `fallback` del tenant se sigue arrancando —si su propio flujo NO es
// durable—. No se toca ni se debilita REQ-27b/INV-20/INV-054.1: la degradación de
// T2.4 solo actúa cuando la guarda de D-054.5 rechaza, y aquí el flujo del fallback
// (sampleFlow/testFlow) no lo hace.
func TestOpenWithOffer_CasoVacioSigueCayendoAlFallback(t *testing.T) {
	ofrece := &fakeOpening{apertura: events.Offering{}}
	rule := trigger.Rule{TenantID: testTenant, Kind: trigger.KindFallback, FlowID: testFlow, Enabled: true}
	rt, repo, sender, contacts, _ := newDurableGuardRuntime(t, ofrece, nil, rule)
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "buenas", "wamid.regr")); err != nil {
		t.Fatalf("HandleIncoming: %v", err)
	}
	if got := sender.count(); got != 1 {
		t.Fatalf("con oferta vacía y fallback NO durable, debe arrancar el fallback (1 envío), hubo %d: %q", got, sender.texts())
	}
	if got := sender.texts()[0]; !strings.Contains(got, "Ventas") {
		t.Fatalf("el saliente debe ser el menú del fallback (testFlow), no una degradación: %q", got)
	}
	cid := resolveID(t, contacts, testContact)
	st := loadState(t, repo, cid)
	if st.FlowID != testFlow {
		t.Fatalf("el fallback (no durable) SÍ debe arrancar: flow_state en %q, quería %q", st.FlowID, testFlow)
	}
}

// --- D1 (review de código sobre F2): el doble cobro del token anti-loop dejaba
// MUDA la degradación de T2.4 ---

// TestDegradeDurableStart_ConLimiteBurst1_LaDegradacionSiEmite reproduce D1: antes
// del arreglo, startPlainFlow cobraba el ÚNICO token del ReplyLimiter en
// replyAllowed (incoming.go) ANTES de intentar startLocked; cuando startLocked
// rebotaba con ErrDurableFlowNeedsEvent, degradeDurableStart llamaba a sendOffer,
// que volvía a cobrar (events.go). Con burst 1 ese segundo cobro fallaba y la
// oferta NUNCA se enviaba, aunque el WARN que la precede («se degrada a la oferta
// del despachador») dijera lo contrario.
//
// Ningún test existente lo veía porque newDurableGuardRuntime nunca cableaba un
// ReplyLimiter: con rt.replyLimiter nil, replyAllowed siempre concede
// (incoming.go:956) y el doble cobro es invisible. Aquí sí se cablea uno REAL de
// burst 1 (Plan 020 · T0): con un único saliente por un único entrante debe bastar
// un único token, así que la oferta debe salir.
func TestDegradeDurableStart_ConLimiteBurst1_LaDegradacionSiEmite(t *testing.T) {
	lim := ratelimit.NewLimiter(rate.Every(time.Hour), 1) // burst 1, prácticamente sin recarga
	ofrece := &fakeOpening{apertura: ofertaConOpciones()}
	rt, repo, sender, contacts, _ := newDurableGuardRuntime(t, ofrece, lim, cartKeywordRuleDurable())
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "quiero-comprar", "wamid.burst1")); err != nil {
		t.Fatalf("HandleIncoming: %v", err)
	}
	if got := sender.count(); got != 1 {
		t.Fatalf("con burst 1 y UN solo cobro del token, la degradación debe emitir la oferta (1 envío); hubo %d: %q", got, sender.texts())
	}
	if got := sender.texts()[0]; got != ofrece.apertura.Text {
		t.Fatalf("el saliente debe ser la oferta del despachador.\n got: %q\nwant: %q", got, ofrece.apertura.Text)
	}
	cid := resolveID(t, contacts, testContact)
	st := loadState(t, repo, cid)
	if st.FlowID == testCartFlow {
		t.Fatalf("el carrito NO debe haber arrancado: flow_state en %q", st.FlowID)
	}
}

// --- D5 (review de código sobre F2, menor): BuildOpening se pedía dos veces ---

// TestOpenWithOffer_FallbackDurableConOfertaVacia_BuildOpeningUnaSolaVez cubre D5:
// en la ruta openWithOffer (oferta vacía) → startPlainFlow → degradeDurableStart →
// buildOpeningOffer, la oferta se construía DOS veces para la MISMA decisión
// —openWithOffer ya sabía que buildOpeningOffer daba ok=false, y degradeDurableStart
// la volvía a pedir sin necesidad—, duplicando el WARN si BuildOpening fallara.
//
// El fallback (Action=Fallback) apunta al carrito (durable) y la oferta sale
// VACÍA: openWithOffer pregunta una vez (ok=false, cae a startPlainFlow con
// offerAlreadyEmpty=true), la guarda rechaza por durable-sin-evento, y
// degradeDurableStart YA NO debe volver a preguntar.
func TestOpenWithOffer_FallbackDurableConOfertaVacia_BuildOpeningUnaSolaVez(t *testing.T) {
	ofrece := &fakeOpening{apertura: events.Offering{}} // vacía: ok=false en openWithOffer
	rt, repo, sender, contacts, _ := newDurableGuardRuntime(t, ofrece, nil, cartFallbackRuleDurable())
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "buenas", "wamid.d5")); err != nil {
		t.Fatalf("con oferta vacía y flujo durable, HandleIncoming NO debe propagar error: %v", err)
	}
	if got := sender.count(); got != 0 {
		t.Fatalf("sin oferta a la que degradar, el contacto se queda MUDO: %d envíos: %q", got, sender.texts())
	}
	if ofrece.veces != 1 {
		t.Fatalf("BuildOpening debe pedirse UNA sola vez (openWithOffer ya sabía que salía vacía), se pidió %d", ofrece.veces)
	}
	cid := resolveID(t, contacts, testContact)
	if ok, eerr := repo.Exists(ctx, store.Key{TenantID: testTenant, SessionID: testSession, ContactID: cid}); eerr != nil || ok {
		t.Fatalf("tampoco debe quedar flow_state: ok=%v err=%v", ok, eerr)
	}
}
