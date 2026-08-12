package runtime_test

import (
	"context"
	"errors"
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
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
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
// cableado (proyecta intakes + flow_events). Igual patrón que newSurveyRuntime.
//
// `rules` es opcional (variadic, igual que newLifecycleRuntime): sin ninguna, el
// ConfigResolver sobre un store vacío se comporta como el NoopResolver de siempre
// (Ignore ante cualquier entrante sin conversación viva), así que las llamadas
// existentes que no pasan reglas no cambian de conducta. Se necesita para el
// disparador real («carrito») del hallazgo #29: TestCartResume_AfterCancel_
// AbreUnPedidoNuevoConElDisparadorReal lo pasa para arrancar un carrito nuevo tras
// cancelar, ahora que el flow_state terminal se suelta en vez de reanudarse solo.
func newCartRuntime(t *testing.T, rules ...trigger.Rule) (*runtime.Runtime, *store.MemoryRepository, *fakeSender, *contact.MemoryResolver) {
	t.Helper()
	repo := store.NewMemoryRepository()
	repo.SetTenantContent(testTenant, "catalogo", []byte(cartCatalogBlob))
	if _, err := repo.InsertDefinition(context.Background(), testTenant, cartFlow(testCartFlow)); err != nil {
		t.Fatalf("sembrar definición cart: %v", err)
	}
	ts := trigger.NewMemoryStore()
	for _, r := range rules {
		if _, err := ts.Insert(context.Background(), r); err != nil {
			t.Fatalf("insert regla: %v", err)
		}
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
		runtime.WithEventSink(persistSinkWith(repo)), cartResumeOpt(repo),
		runtime.WithTriggerResolver(trigger.NewConfigResolver(ts)))
	return rt, repo, sender, contacts
}

// cartAddCafe navega Bebidas→Café→Agregar→cantidad(2), dejando el carrito en L5
// (continue) con una solicitud "open" (el primer item_added la abre). base da waIDs
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

// seedCartOpen siembra DIRECTO (repo.Save, sin pasar por Start) un flow_state de
// carrito ya abierto en L0/categorías — el mismo punto de partida que producía
// Start()/Enter antes de Plan 054. Necesario desde T2.3 (D-054.5): Start() en un
// flujo con contenido durable YA NO puede abrir nada —cart lo es SIEMPRE, así que
// Start() da SIEMPRE ErrDurableFlowNeedsEvent, ver
// TestCartStart_SiempreExigeEvento_ElGotchaDeReinicioQuedaSuperado—, así que los
// tests que solo necesitan «un carrito ya abierto» como fixture para probar SU
// mecánica interna (resume, expiración, cancelación) siembran el estado a mano.
// loadState (modules/cart/state.go) trata unas Vars vacías como Level=Categories
// (L0), así que el punto de partida es IDÉNTICO al que dejaba Start/Enter.
func seedCartOpen(t *testing.T, repo *store.MemoryRepository, contacts *contact.MemoryResolver) string {
	t.Helper()
	cid := resolveID(t, contacts, testContact)
	if err := repo.Save(context.Background(), model.Conversation{
		TenantID: testTenant, SessionID: testSession, ContactID: cid,
		FlowID: testCartFlow, FlowVersion: 1, CurrentNode: "cart",
	}); err != nil {
		t.Fatalf("sembrar flow_state del carrito: %v", err)
	}
	return cid
}

func hasFlowEvent(repo *store.MemoryRepository, name string) bool {
	for _, e := range repo.FlowEvents() {
		if e.Name == name {
			return true
		}
	}
	return false
}

func openIntakeCount(repo *store.MemoryRepository, status string) int {
	n := 0
	for _, o := range repo.Intakes() {
		if o.Status == status {
			n++
		}
	}
	return n
}

// TestCartResume_NadaVencePorTiempo es LA ESCENA DE MARTA (D-041.16, T4.7), y
// sustituye al viejo TestCartResume_IntakeExpired_ResetsAndNotifies, que probaba la
// regla CONTRARIA: aquella daba por bueno que una solicitud "open" muriera sola al
// pasar su expires_at, y esa regla está DEROGADA. No se parcheó para que pasara: se
// invirtió, porque lo que hay que proteger ahora es justo lo que antes se rompía.
//
// Marta arma el carrito el lunes y vuelve el miércoles. El primer item_added abre la
// solicitud SIN vencimiento (expires_at zero); aun forzando en BD un expires_at
// pasado —una fila histórica del reloj viejo, que es el caso hostil— el entrante del
// miércoles NO reinicia nada: la solicitud sigue "open" con su contenido, el
// sub-estado del carrito sobrevive, nadie avisa de ningún vencimiento y cart_expired
// no aparece en flow_events.
//
// Sin reloj inyectado a propósito: la política de reanudación ya no tiene reloj que
// inyectar (T4.7 le quitó el campo `now`). Adelantar dos días es exactamente dejar
// expires_at en el pasado, y hacerlo así prueba de paso que ni siquiera un valor
// heredado del reloj viejo resucita el vencimiento.
func TestCartResume_NadaVencePorTiempo(t *testing.T) {
	rt, repo, sender, contacts := newCartRuntime(t)
	ctx := context.Background()
	// Sembrado directo (no Start): desde Plan 054 · T2.3 el carrito es SIEMPRE
	// durable y Start() ya no puede abrirlo — ver seedCartOpen. Lo que este test
	// mide (que nada vence por tiempo) es ortogonal a CÓMO se abrió el carrito.
	cid := seedCartOpen(t, repo, contacts)
	cartAddCafe(t, rt, "add")

	intakes := repo.Intakes()
	if len(intakes) != 1 || intakes[0].Status != "open" {
		t.Fatalf("esperaba 1 solicitud open, got %+v", intakes)
	}
	// La solicitud nace SIN vencimiento: item_added ya no fecha nada (D-041.16).
	if !intakes[0].ExpiresAt.IsZero() {
		t.Fatalf("item_added no debe fechar vencimiento; expires_at=%v", intakes[0].ExpiresAt)
	}
	// El carrito quedó armado: el sub-estado con su línea es lo que Marta espera
	// encontrar el miércoles.
	if _, ok := loadState(t, repo, cid).Vars["cart"]; !ok {
		t.Fatal("el carrito armado debe dejar sub-estado en Vars")
	}

	// Dos días después, con el peor dato posible: expires_at en el pasado (fila
	// heredada del reloj derogado).
	o := intakes[0]
	o.ExpiresAt = time.Now().Add(-48 * time.Hour)
	if err := repo.UpsertIntake(ctx, o); err != nil {
		t.Fatalf("sembrar expires_at histórico: %v", err)
	}

	before := sender.count()
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "2", "resume-1")); err != nil {
		t.Fatalf("resume: %v", err)
	}
	joined := strings.Join(sender.texts()[before:], "\n")
	if strings.Contains(joined, "expiró") {
		t.Fatalf("nadie debe avisar de un vencimiento que ya no existe: %q", joined)
	}
	if os := repo.Intakes(); len(os) != 1 || os[0].Status != "open" {
		t.Fatalf("la solicitud debe seguir open, got %+v", os)
	}
	if hasFlowEvent(repo, "cart_expired") {
		t.Fatalf("cart_expired ya no tiene productor, got %+v", repo.FlowEvents())
	}
	if _, ok := loadState(t, repo, cid).Vars["cart"]; !ok {
		t.Fatalf("el sub-estado del carrito debe SOBREVIVIR: %+v", loadState(t, repo, cid).Vars)
	}
}

// TestCartStart_ConPedidoVencido_SigueDando409 era la otra puerta del mismo reloj:
// restartableOnStart consultaba la MISMA política, así que un /start sobre un
// pedido "vencido" reiniciaba en vez de dar 409. Derogado el vencimiento, un pedido
// en curso es un pedido en curso por viejo que sea.
//
// ⚠️ Plan 054 · T2.3 (D-054.5) SUPERSEDE esta protección para el carrito: la guarda
// de startLocked se dispara ANTES de rt.store.Exists, y cart es SIEMPRE durable
// (ProducesDurableContent()==true), así que Start() sobre un flujo cart YA NO
// alcanza jamás restartableOnStart —vencido o no—. El resultado es SIEMPRE
// ErrDurableFlowNeedsEvent, así que ya no se compara con errIsConvExists
// (ErrConversationExists). El bootstrap se siembra a mano (seedCartOpen) porque
// Start tampoco puede abrir el carrito para dejarlo "en curso". Se conserva el
// test —con la aserción invertida— porque sigue demostrando algo real: ni un
// pedido vencido reabre una vía para que Start clobbee una solicitud en curso; solo
// que ahora el motivo del 409 es uno solo y más fuerte que el viejo gotcha.
func TestCartStart_ConPedidoVencido_SigueDando409(t *testing.T) {
	rt, repo, _, contacts := newCartRuntime(t)
	ctx := context.Background()
	seedCartOpen(t, repo, contacts)
	cartAddCafe(t, rt, "add")

	o := repo.Intakes()[0]
	o.ExpiresAt = time.Now().Add(-48 * time.Hour)
	if err := repo.UpsertIntake(ctx, o); err != nil {
		t.Fatalf("sembrar expires_at histórico: %v", err)
	}

	if _, err := rt.Start(ctx, testTenant, testCartFlow, testSession, phoneRef(t, testContact)); !errors.Is(err, runtime.ErrDurableFlowNeedsEvent) {
		t.Fatalf("Start sobre un flujo durable da SIEMPRE ErrDurableFlowNeedsEvent (T2.3), vencido o no, dio: %v", err)
	}
	if os := repo.Intakes(); len(os) != 1 || os[0].Status != "open" {
		t.Fatalf("la solicitud debe seguir open e intacta, got %+v", os)
	}
}

// TestCartResume_AfterCancel_AbreUnPedidoNuevoConElDisparadorReal (design.md
// §3.4/§4.2, hallazgo #29 · salida (A) · decisión de Jhoan 2026-08-11): tras
// cancelar (9), la solicitud queda "cancelled" y AHORA TAMBIÉN se cierra el evento
// que contenía el pedido —cart.Module.Step fija Next al centinela para
// LevelCancelled exactamente igual que para LevelClosed (#24), así que
// closeIfFinished cierra el evento por el camino natural en el mismo turno (ver
// cart.go y cart_fin_de_flujo_test.go)—. El flow_state terminal se suelta
// (advanceLive, incoming.go) y la conversación NO se queda bloqueada, pero un
// pedido nuevo YA NO nace de cualquier texto: hace falta el disparador real
// («carrito») para abrirlo, con evento e intake propios.
//
// CONDUCTA VIEJA QUE ESTE TEST REEMPLAZA (no se borra el nombre viejo sin decir por
// qué): se llamaba TestCartResume_AfterCancel_RestartsAndEnablesNewIntake y mandaba
// «hola» tras cancelar esperando que reanudara el MISMO carrito en L1. Eso ocurría
// porque cancelar dejaba el flow_state vivo (Level=Cancelled, Finished()==false) y
// cart.ResumePolicy.Restart (isTerminal) lo reiniciaba ante CUALQUIER entrada,
// sin mirar el texto. Con el evento cerrándose de verdad, el flow_state
// desaparece: «hola» ya no tiene flujo que reanudar y cae al
// fallback/oferta del tenant —aquí sin configurar, así que no contesta nada—,
// mientras que «carrito» sí casa la regla keyword y arranca un carrito NUEVO. Es el
// cambio de producto que el #29 acepta a propósito, no una regresión.
//
// ⚠️ Segunda vuelta, Plan 054 · T2.3/T2.4: «carrito» YA NO puede ser una keyword
// PURA hacia un flujo durable —esa combinación es EXACTAMENTE el hallazgo #001
// (D-054.5 la rechaza; sin opening cableado en este runtime, T2.4 la degradaría a
// silencio, no a un carrito nuevo). Así que el disparador REAL pasa a ser una
// regla event_start (cartEventRule, la misma de event_clock_test.go): «carrito»
// sigue siendo la palabra, pero ahora PARE EVENTO antes de llegar a startLocked
// (beginEvent → birthEvent → enterEventFlow), que es la única puerta por la que un
// flujo durable puede arrancar. El comportamiento observable para el cliente es
// IDÉNTICO —la misma palabra abre el mismo carrito—; lo que cambia es que ahora deja
// una fila en conversation_events, que es justo lo que este plan exige.
func TestCartResume_AfterCancel_AbreUnPedidoNuevoConElDisparadorReal(t *testing.T) {
	rt, repo, sender, _, _ := newDurableGuardRuntime(t, nil, nil, cartEventRule())
	ctx := context.Background()
	// La apertura inicial TAMBIÉN pasa por el disparador real (event_start): Start()
	// ya no puede abrir el carrito (T2.3), así que «carrito» hace las dos veces el
	// mismo trabajo que antes hacían Start() (apertura) y la keyword (reapertura).
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "open-1")); err != nil {
		t.Fatalf("carrito (apertura): %v", err)
	}
	cartAddCafe(t, rt, "add") // carrito en L5 con solicitud open
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "9", "cancel-1")); err != nil {
		t.Fatalf("cancelar: %v", err)
	}
	if os := repo.Intakes(); len(os) != 1 || os[0].Status != "cancelled" {
		t.Fatalf("esperaba la solicitud en cancelled, got %+v", os)
	}

	// Un texto CUALQUIERA («hola») ya NO reanuda nada: el flow_state terminal se
	// soltó con el cierre natural del evento, y sin regla que case (ni fallback
	// configurado en este runtime), el resolver de disparos decide Ignore.
	before := sender.count()
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "hola", "noise-1")); err != nil {
		t.Fatalf("hola: %v", err)
	}
	if got := sender.texts()[before:]; len(got) != 0 {
		t.Fatalf("un texto sin disparador NO debe reanudar el carrito cancelado: %q", got)
	}

	// El disparador REAL («carrito») sí abre un pedido nuevo (L1 fresco).
	before = sender.count()
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "resume-1")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	if got := strings.Join(sender.texts()[before:], "\n"); !strings.Contains(got, "Elige una categoría") {
		t.Fatalf("el disparador «carrito» debe abrir un carrito nuevo (L1): %q", got)
	}

	// Pedido NUEVO posible: agregar otro artículo abre una segunda solicitud "open".
	cartAddCafe(t, rt, "add2")
	if open, cancelled := openIntakeCount(repo, "open"), openIntakeCount(repo, "cancelled"); open != 1 || cancelled != 1 {
		t.Fatalf("esperaba 1 open (pedido nuevo) + 1 cancelled, got %+v", repo.Intakes())
	}
}

// TestCartStart_SiempreExigeEvento_ElGotchaDeReinicioQuedaSuperado REEMPLAZA a
// TestCartStart_AfterCancel_NoBlocking409 (gotcha 409: un /start sobre un carrito
// TERMINADO —cancelado— NO devolvía 409 sino que reiniciaba; mientras el pedido
// estaba EN CURSO sí bloqueaba con 409 para no clobbear).
//
// ⚠️ Plan 054 · T2.3 (D-054.5): la guarda de startLocked se dispara ANTES de
// rt.store.Exists, así que Start() sobre un flujo durable (cart SIEMPRE lo es) YA
// NO alcanza jamás restartableOnStart —fresco, en curso o cancelado dan ahora el
// MISMO ErrDurableFlowNeedsEvent—. El caso "tras cancelar reinicia sin 409" DEJÓ DE
// SER ALCANZABLE por esta puerta: el operador tiene que abrir el carrito por la
// puerta de eventos (event_start), no por /admin/flows/start. Es la decisión de
// producto que este plan toma a propósito (design.md §4): el 409 avisa ANTES de
// tocar la base en vez de dejar la bandeja vacía horas después. Se conserva el
// nombre viejo en el comentario (convención de este fichero) para que quien busque
// "NoBlocking409" encuentre por qué dejó de existir.
func TestCartStart_SiempreExigeEvento_ElGotchaDeReinicioQuedaSuperado(t *testing.T) {
	rt, repo, _, contacts := newCartRuntime(t)
	ctx := context.Background()

	// Fresco: nunca hubo conversación, y aun así Start da el MISMO 409 (D-054.5 va
	// antes de rt.store.Exists, así que ni siquiera llega a comprobar si existe).
	if _, err := rt.Start(ctx, testTenant, testCartFlow, testSession, phoneRef(t, testContact)); !errors.Is(err, runtime.ErrDurableFlowNeedsEvent) {
		t.Fatalf("Start fresco sobre un flujo durable = %v, quiero ErrDurableFlowNeedsEvent", err)
	}

	// En curso: se siembra a mano (Start no puede abrirlo) y Start sigue dando el
	// MISMO error, NUNCA ErrConversationExists (el viejo gotcha 409).
	seedCartOpen(t, repo, contacts)
	cartAddCafe(t, rt, "add")
	if _, err := rt.Start(ctx, testTenant, testCartFlow, testSession, phoneRef(t, testContact)); !errors.Is(err, runtime.ErrDurableFlowNeedsEvent) {
		t.Fatalf("Start con pedido en curso = %v, quiero ErrDurableFlowNeedsEvent (no ErrConversationExists)", err)
	}

	// Tras cancelar: el flow_state terminal se suelta (advanceLive) y Start SIGUE
	// dando el mismo 409 — el viejo "reinicia sin 409" ya no es alcanzable por esta
	// puerta.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "9", "cancel-1")); err != nil {
		t.Fatalf("cancelar: %v", err)
	}
	if _, err := rt.Start(ctx, testTenant, testCartFlow, testSession, phoneRef(t, testContact)); !errors.Is(err, runtime.ErrDurableFlowNeedsEvent) {
		t.Fatalf("Start tras cancelar = %v, quiero ErrDurableFlowNeedsEvent (el viejo 'reinicia sin 409' ya no es alcanzable)", err)
	}
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
	if len(repo.Intakes()) != 0 {
		t.Fatalf("el menú NO debe crear intakes: %+v", repo.Intakes())
	}
}
