package runtime_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
)

// ---------------------------------------------------------------------------
// Plan 043 · T3.8 · punto 2 — la COLETILLA del camino con llm_intent.
//
// «Con llm_intent: la intención inferida se atiende y se añade la coletilla al
// final de la respuesta. La coletilla se genera una sola vez por conversación.»
//
// Estos tests usan el Dispatcher REAL y no un doble que devuelva una cadena fija,
// y esa elección es la mitad de lo que acreditan: lo que hay que probar no es que
// «se pega algo al final», sino que lo que se pega dice «tu pedido» —la palabra
// que el cliente lee (decisión de Jhoan, 2026-08-09)— y no «carrito», ni un UUID,
// ni un history_id (E-3). Con un fake, el texto lo escribiría el test y el assert
// se comprobaría a sí mismo.
// ---------------------------------------------------------------------------

// laColetillaDeUnPedido es, literalmente, lo que el cliente lee cuando tiene UN
// pedido a medias. Se escribe entera y no por trozos: es texto de cara al cliente,
// y un assert por `strings.Contains("pedido")` seguiría verde si la frase se
// rompiera en cualquier otro punto.
const laColetillaDeUnPedido = "Por cierto, tu pedido sigue a medias — dime si quieres retomarlo."

// laPantallaDelMenu es lo que emite sampleFlow() al arrancar: la respuesta que
// ATIENDE lo que el cliente pidió. La coletilla se pega DETRÁS de esto, no en su
// lugar — que las dos mitades estén en el mismo saliente es el criterio.
const laPantallaDelMenu = "Hola 👋\n1) Ventas\n2) Soporte"

// sinTiposOfrecidos es el KindOffer que el Dispatcher exige por construcción y que
// BuildTagline NO usa: la coletilla habla de lo que el contacto dejó a medias, no
// del catálogo de lo que el tenant ofrece. Devolver nil es por tanto un assert
// implícito — si algún día BuildTagline empezara a componer con los tipos
// ofrecidos, estos tests no lo notarían y ese es el aviso de que hay que mirarlo.
type sinTiposOfrecidos struct{}

func (sinTiposOfrecidos) OfferedKinds(context.Context, string, string) ([]string, error) {
	return nil, nil
}

// newTaglineRuntime arma la escena: un tenant CON la feature llm_intent y con el
// carrito contratado (sin la segunda, el gate del despachador apartaría los
// pedidos del contacto y la coletilla saldría vacía por una razón que no es la
// que se está probando).
//
// El OpeningBuilder es el Dispatcher de verdad, montado sobre el mismo
// memEventStore que el runtime: así el rescatable que siembra el test es el mismo
// que la coletilla lee.
func newTaglineRuntime(t *testing.T, rules ...trigger.Rule) (
	*runtime.Runtime, *store.MemoryRepository, *fakeSender, *contact.MemoryResolver, *memEventStore,
) {
	t.Helper()
	ctx := context.Background()
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(ctx, testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar el flujo del menú: %v", err)
	}
	ts := trigger.NewMemoryStore()
	for _, r := range rules {
		if _, err := ts.Insert(ctx, r); err != nil {
			t.Fatalf("insert regla: %v", err)
		}
	}
	ents := entitlements.NewFake()
	ents.Enable(testTenant, entitlements.FeatureLLMIntent)
	ents.Enable(testTenant, entitlements.FeatureCartBasic)
	evs := newMemEventStore(time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC))
	sender := &fakeSender{}
	contacts := contact.NewMemoryResolver(repo)
	rt := runtime.New(repo, newEngine(), sender, fakeResolver{tenantID: testTenant}, contacts, discardLogger(),
		runtime.WithTriggerResolver(trigger.NewConfigResolver(ts)),
		runtime.WithEventStore(evs),
		runtime.WithEntitlements(ents),
		runtime.WithOpeningBuilder(events.NewDispatcher(evs, sinTiposOfrecidos{}, ents)))
	return rt, repo, sender, contacts, evs
}

// newTaglineRuntimeCon es newTaglineRuntime con un trigger.Resolver puesto a mano,
// para poder ejercer decisiones que el ConfigResolver no produce hoy pero que el
// puerto permite.
func newTaglineRuntimeCon(t *testing.T, res trigger.Resolver) (
	*runtime.Runtime, *fakeSender, *contact.MemoryResolver, *memEventStore,
) {
	t.Helper()
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar el flujo: %v", err)
	}
	ents := entitlements.NewFake()
	ents.Enable(testTenant, entitlements.FeatureLLMIntent)
	ents.Enable(testTenant, entitlements.FeatureCartBasic)
	evs := newMemEventStore(time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC))
	sender := &fakeSender{}
	contacts := contact.NewMemoryResolver(repo)
	rt := runtime.New(repo, newEngine(), sender, fakeResolver{tenantID: testTenant}, contacts, discardLogger(),
		runtime.WithTriggerResolver(res),
		runtime.WithEventStore(evs),
		runtime.WithEntitlements(ents),
		runtime.WithOpeningBuilder(events.NewDispatcher(evs, sinTiposOfrecidos{}, ents)))
	return rt, sender, contacts, evs
}

// resolverQueParteEventoConIntencion es un trigger.Resolver del test que devuelve
// una decisión con IntentName Y EventKind a la vez.
//
// No modela una configuración imposible: `trigger.Resolver` es un PUERTO público
// (trigger/trigger.go:202) y el runtime no puede apoyarse en lo que hoy hace una
// implementación concreta. Que el ConfigResolver de producción no combine hoy las
// dos cosas es una propiedad SUYA, no del contrato — y el camino que se prueba
// aquí es del runtime, que las acepta juntas sin quejarse.
type resolverQueParteEventoConIntencion struct{}

func (resolverQueParteEventoConIntencion) Resolve(context.Context, string, string, trigger.Signal) (trigger.Decision, error) {
	return trigger.Decision{
		Action: trigger.Start, FlowID: testFlow,
		EventKind: trigger.EventKindCart, IntentName: "pedir",
	}, nil
}

func (resolverQueParteEventoConIntencion) IsEscape(context.Context, string, string, string) (bool, string, error) {
	return false, "", nil
}

func (resolverQueParteEventoConIntencion) ResolveLive(context.Context, string, string, string) (trigger.Decision, error) {
	return trigger.Decision{Action: trigger.Ignore}, nil
}

// sembrarPedidoAMedias deja un carrito `open` del contacto: lo que la coletilla
// tiene que anunciar. Devuelve el evento para poder afirmar después que NADA suyo
// —ni su id ni su history_id— se coló en el texto.
func sembrarPedidoAMedias(t *testing.T, evs *memEventStore, contacts *contact.MemoryResolver) events.Event {
	t.Helper()
	evs.contactID = resolveID(t, contacts, testContact)
	return evs.seedAlive("cart", "", time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC))
}

// assertSinIdentificadores es E-3 escrito como comprobación: lo que se le dice al
// cliente nombra TIPOS, nunca identificadores. El history_id existe para que un
// humano hable de «ese pedido» en la bandeja del negocio, no para la conversación.
func assertSinIdentificadores(t *testing.T, texto string, ev events.Event) {
	t.Helper()
	if ev.HistoryID != "" && strings.Contains(texto, ev.HistoryID) {
		t.Fatalf("E-3: el history_id %q no puede aparecer en lo que lee el cliente: %q", ev.HistoryID, texto)
	}
	if strings.Contains(texto, ev.ID) {
		t.Fatalf("E-3: el id del evento %q no puede aparecer en lo que lee el cliente: %q", ev.ID, texto)
	}
}

// TestTagline_LaIntencionSeAtiendeYLaRespuestaTerminaConLaColetilla es el criterio
// (a) de T3.8 punto 2, entero y en un solo turno.
//
// La escena: el contacto tiene un pedido a medias y escribe algo que el
// clasificador entiende como «consulta». Se le atiende la consulta —arranca el
// flujo que la regla llm mapea— y esa MISMA respuesta termina recordándole el
// pedido.
//
// El assert del número de salientes no es decorativo: mandar la coletilla como
// mensaje aparte serían DOS mensajes y DOS tokens del anti-loop, y con el cupo
// justo el segundo no saldría. Un turno, un mensaje.
func TestTagline_LaIntencionSeAtiendeYLaRespuestaTerminaConLaColetilla(t *testing.T) {
	rt, _, sender, contacts, evs := newTaglineRuntime(t, llmRule("consulta", testFlow))
	pedido := sembrarPedidoAMedias(t, evs, contacts)

	m := incomingIntent(testContact, "¿a qué hora abren?", "wamid.tag1", "consulta", nil)
	if err := rt.HandleIncoming(context.Background(), testSession, m); err != nil {
		t.Fatalf("HandleIncoming: %v", err)
	}

	if got := sender.count(); got != 1 {
		t.Fatalf("la coletilla va PEGADA a la respuesta: debe haber 1 saliente, hay %d: %q", got, sender.texts())
	}
	enviado := sender.texts()[0]
	// La intención se atendió: la respuesta del flujo sigue estando, y entera.
	if !strings.HasPrefix(enviado, laPantallaDelMenu) {
		t.Fatalf("la respuesta debe atender la intención primero.\n got: %q\nwant prefijo: %q", enviado, laPantallaDelMenu)
	}
	// Y termina con la coletilla.
	if !strings.HasSuffix(enviado, laColetillaDeUnPedido) {
		t.Fatalf("la respuesta debe TERMINAR con la coletilla.\n got: %q\nwant sufijo: %q", enviado, laColetillaDeUnPedido)
	}
	// La palabra que el cliente lee es «pedido», nunca «carrito».
	if strings.Contains(strings.ToLower(enviado), "carrito") {
		t.Fatalf("el cliente lee «pedido», nunca «carrito»: %q", enviado)
	}
	assertSinIdentificadores(t, enviado, pedido)
}

// TestTagline_SinRescatablesNoHayColetilla es el criterio (b): sin nada que
// retomar, BuildTagline devuelve "" y la respuesta se queda EXACTAMENTE como
// estaba. La igualdad es estricta a propósito — un `HasPrefix` dejaría pasar una
// coletilla vacía pegada con sus dos saltos de línea, que es basura invisible en
// WhatsApp y no se vería en ningún otro assert.
func TestTagline_SinRescatablesNoHayColetilla(t *testing.T) {
	rt, _, sender, _, _ := newTaglineRuntime(t, llmRule("consulta", testFlow))

	m := incomingIntent(testContact, "¿a qué hora abren?", "wamid.tag2", "consulta", nil)
	if err := rt.HandleIncoming(context.Background(), testSession, m); err != nil {
		t.Fatalf("HandleIncoming: %v", err)
	}

	if got := sender.count(); got != 1 {
		t.Fatalf("debe responderse la intención (1 saliente), hay %d: %q", got, sender.texts())
	}
	if got := sender.texts()[0]; got != laPantallaDelMenu {
		t.Fatalf("sin rescatables la respuesta va TAL CUAL, sin coletilla ni saltos sobrantes.\n got: %q\nwant: %q",
			got, laPantallaDelMenu)
	}
}

// TestTagline_NoSeRepiteEnLaSegundaInteraccion es el «una sola vez por
// conversación» del plan.
//
// ⚠️ Lo que este test acredita, con precisión: que la SEGUNDA interacción no
// vuelve a llevarla. Quien lo garantiza hoy es el SITIO —la coletilla se pega en
// startLocked, y a startLocked solo se llega arrancando— y no la marca
// `tagline_offered`, que se escribe y hoy nadie lee. Por eso el test comprueba las
// dos cosas por separado: el comportamiento (el segundo saliente no la lleva) y la
// marca (queda escrita). Si algún día la coletilla se emite desde otro punto del
// camino, el primer assert seguirá siendo el que muerda.
func TestTagline_NoSeRepiteEnLaSegundaInteraccion(t *testing.T) {
	rt, repo, sender, contacts, evs := newTaglineRuntime(t, llmRule("consulta", testFlow))
	sembrarPedidoAMedias(t, evs, contacts)
	ctx := context.Background()

	m := incomingIntent(testContact, "¿a qué hora abren?", "wamid.tag3a", "consulta", nil)
	if err := rt.HandleIncoming(ctx, testSession, m); err != nil {
		t.Fatalf("primer entrante: %v", err)
	}
	if got := sender.texts()[0]; !strings.HasSuffix(got, laColetillaDeUnPedido) {
		t.Fatalf("la primera respuesta SÍ la lleva (si no, el resto del test no prueba nada): %q", got)
	}

	// Segunda interacción de la MISMA conversación: el «1» del menú que quedó vivo.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "wamid.tag3b")); err != nil {
		t.Fatalf("segundo entrante: %v", err)
	}

	if got := sender.count(); got != 2 {
		t.Fatalf("deben haberse respondido los dos entrantes, hay %d: %q", got, sender.texts())
	}
	segundo := sender.texts()[1]
	if strings.Contains(segundo, "sigue a medias") {
		t.Fatalf("la coletilla es UNA vez por conversación; la segunda respuesta la repite: %q", segundo)
	}
	if segundo != "Te paso con Ventas." {
		t.Fatalf("y la segunda respuesta es la del flujo, tal cual.\n got: %q", segundo)
	}
	// La marca quedó en el estado de ESTA conversación (y muere con ella).
	st := loadState(t, repo, resolveID(t, contacts, testContact))
	if st.Vars["tagline_offered"] != true {
		t.Fatalf("la conversación debe recordar que ya se le ofreció: %+v", st.Vars)
	}
}

// TestTagline_ArranquePorKeywordNoLlevaColetilla es LA REGRESIÓN que protege a
// todos los demás caminos: startLocked es por donde pasan TODOS los arranques —la
// API, la keyword, el fallback, el evento—, y solo el de la intención lleva
// coletilla.
//
// El montaje es idéntico al del caso (a) —mismo contacto, mismo pedido a medias,
// mismo runtime— y lo único que cambia es la puerta por la que entra. Si alguien
// mueve la emisión a un sitio más cómodo (a `send`, por ejemplo), este es el test
// que se pone rojo.
func TestTagline_ArranquePorKeywordNoLlevaColetilla(t *testing.T) {
	keyword := trigger.Rule{
		TenantID: testTenant, Kind: trigger.KindKeyword, Keyword: "hola",
		MatchType: trigger.MatchExact, FlowID: testFlow, Enabled: true,
	}
	rt, _, sender, contacts, evs := newTaglineRuntime(t, keyword)
	sembrarPedidoAMedias(t, evs, contacts)

	if err := rt.HandleIncoming(context.Background(), testSession, incoming(testContact, "hola", "wamid.tag4")); err != nil {
		t.Fatalf("HandleIncoming: %v", err)
	}

	if got := sender.count(); got != 1 {
		t.Fatalf("la keyword arranca su flujo (1 saliente), hay %d: %q", got, sender.texts())
	}
	if got := sender.texts()[0]; got != laPantallaDelMenu {
		t.Fatalf("un arranque por keyword NO lleva coletilla, ni aunque el contacto tenga un pedido a medias.\n got: %q\nwant: %q",
			got, laPantallaDelMenu)
	}
}

// TestTagline_ArranquePorEventStartNoLlevaColetilla fija una verdad MEDIDA del
// resolver que no es evidente leyendo el runtime, y que conviene que quede escrita
// porque de ella depende que el otro punto de sutura sea inofensivo:
//
// el ConfigResolver NUNCA combina IntentName con EventKind. La rama de intención
// devuelve {Start, FlowID, Params, IntentName} SIN EventKind
// (trigger/config_resolver.go), y la de event_start devuelve {StartEvent, FlowID,
// EventKind} SIN IntentName. Son excluyentes.
//
// Consecuencia: el arranque que PARE un evento no puede traer intención, así que
// la llamada a taglineFor de enterEventFlow recibe hoy siempre "" y no emite nada.
// Si alguien hace que una regla llm mapee a un event_kind, este test se pondrá
// rojo — y ese rojo es el aviso de que hay que excluir el evento RECIÉN NACIDO de
// la coletilla, o el cliente leerá «tu pedido sigue a medias» sobre el pedido que
// acaba de abrir (un evento nuevo cumple `status='open' AND intake_id IS NULL`, que
// es exactamente el predicado de rescatable).
func TestTagline_ArranquePorEventStartNoLlevaColetilla(t *testing.T) {
	eventStart := trigger.Rule{
		TenantID: testTenant, Kind: trigger.KindEventStart, Keyword: "encuesta",
		MatchType: trigger.MatchExact, FlowID: testFlow, EventKind: trigger.EventKindSurvey, Enabled: true,
	}
	rt, _, sender, contacts, evs := newTaglineRuntime(t, eventStart)
	sembrarPedidoAMedias(t, evs, contacts)

	if err := rt.HandleIncoming(context.Background(), testSession, incoming(testContact, "encuesta", "wamid.tag5")); err != nil {
		t.Fatalf("HandleIncoming: %v", err)
	}

	if got := sender.count(); got != 1 {
		t.Fatalf("el event_start arranca su flujo (1 saliente), hay %d: %q", got, sender.texts())
	}
	if got := sender.texts()[0]; got != laPantallaDelMenu {
		t.Fatalf("un arranque que PARE un evento no trae intención y por tanto no lleva coletilla.\n got: %q\nwant: %q",
			got, laPantallaDelMenu)
	}
}

// TestTagline_ElEventoQueAcabaDeNacerNoSeAnunciaASiMismo es el defecto de verdad,
// y el único de esta tanda que describe algo que el cliente vería.
//
// La escena: el contacto NO tiene nada a medias. Escribe algo que se entiende como
// «quiero pedir», y eso PARE un pedido nuevo. Si la coletilla se resolviera después
// de crear la fila, `ListRescuable` devolvería ese pedido recién nacido —cumple
// `status='open' AND intake_id IS NULL`, que es literalmente el predicado de
// rescatable (events/store.go, rescuableWhere)— y la respuesta terminaría diciendo
// «tu pedido sigue a medias» sobre el pedido que se acaba de abrir.
//
// El montaje sin ningún evento previo es lo que hace la prueba limpia: si aparece
// una coletilla, solo puede estar hablando del que acaba de nacer.
func TestTagline_ElEventoQueAcabaDeNacerNoSeAnunciaASiMismo(t *testing.T) {
	rt, sender, contacts, evs := newTaglineRuntimeCon(t, resolverQueParteEventoConIntencion{})
	evs.contactID = resolveID(t, contacts, testContact)

	m := incomingIntent(testContact, "quiero pedir una torta", "wamid.tag7", "pedir", nil)
	if err := rt.HandleIncoming(context.Background(), testSession, m); err != nil {
		t.Fatalf("HandleIncoming: %v", err)
	}

	// El pedido nació: si no, el test no estaría probando lo que dice.
	if got := evs.alive(); len(got) != 1 || got[0].Kind != "cart" {
		t.Fatalf("la decisión debe parir un pedido (si no, el escenario no se dio): %+v", got)
	}
	todo := strings.Join(sender.texts(), "\n")
	if strings.Contains(todo, "sigue a medias") {
		t.Fatalf("el pedido que ACABA de nacer no puede anunciarse a sí mismo como «a medias»: %q", todo)
	}
}

// assertVarsSinMarca es la comprobación negativa que acompaña a (b): sin coletilla
// tampoco se marca nada. Marcar sin haber dicho nada dejaría a la conversación
// creyendo que ya la vio, y el día que la coletilla se emita desde otro punto —para
// eso está la marca— ese contacto no la recibiría nunca.
func assertVarsSinMarca(t *testing.T, st model.Conversation) {
	t.Helper()
	if _, marcada := st.Vars["tagline_offered"]; marcada {
		t.Fatalf("sin coletilla no debe marcarse la conversación: %+v", st.Vars)
	}
}

// TestTagline_SinColetillaTampocoSeMarcaLaConversacion cierra el par del criterio
// (b): no basta con no decir nada, hay que no haberlo apuntado.
func TestTagline_SinColetillaTampocoSeMarcaLaConversacion(t *testing.T) {
	rt, repo, _, contacts, _ := newTaglineRuntime(t, llmRule("consulta", testFlow))

	m := incomingIntent(testContact, "¿a qué hora abren?", "wamid.tag6", "consulta", nil)
	if err := rt.HandleIncoming(context.Background(), testSession, m); err != nil {
		t.Fatalf("HandleIncoming: %v", err)
	}

	assertVarsSinMarca(t, loadState(t, repo, resolveID(t, contacts, testContact)))
}
