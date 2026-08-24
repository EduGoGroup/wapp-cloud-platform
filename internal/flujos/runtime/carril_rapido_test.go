package runtime_test

// Plan 043 · Ola 5 · T5.1 — el carril rápido (REQ-21): "la navegación numérica jamás
// invoca al clasificador". En ESTE repo (Cloud) no existe un "puerto del
// clasificador" como interfaz Go: el LLM vive en el Edge (edge/wapp-edge-intent) y
// llega aquí YA resuelto, viajando en trigger.Signal.Intent (Plan 029 · T7). El
// eslabón que sí vive aquí, y que la navegación numérica no debe tocar, es
// trigger.Resolver (internal/flujos/trigger/trigger.go:202-224): quien casa una
// intención LLM contra `flow_triggers kind='llm'` es *trigger.ConfigResolver.Resolve
// (internal/flujos/trigger/config_resolver.go:47-59). "Nunca llega al clasificador"
// se traduce aquí, con evidencia de código, en "nunca llama a Resolver.Resolve ni a
// Resolver.ResolveLive con una decisión que dependa de una intención".
//
// Este fichero fija tres caminos contra regresión (T5.1, criterio del plan):
//
//	(i)   un número DENTRO del menú del despachador (events.Menu — Ola 2/3, el
//	      "menú del despachador" nace de la palabra clave, no de un número, y su
//	      respuesta se interpreta con events.Menu.Resolve puro — runtime/events.go
//	      menuChoice).
//	(ii)  un número DENTRO de un flujo con conversación viva (el nodo `root` de
//	      sampleFlow, un NodeTypeMenu de toda la vida — runtime/incoming.go
//	      advanceLive → engine.Step). La escena se monta CON event store, igual que
//	      producción, para que el «2» recorra de verdad IsEscape y liveEventSwitch:
//	      es ahí donde una regresión metería el clasificador, y un test que se
//	      salte ese tramo no vigila nada (ver el fake y su campo `texto`).
//	(iii) entrada tardía (T3.2, Plan 043 · Ola 3): un número que llega DESPUÉS de
//	      que el evento activo se suspendiera por inactividad (event_inactivity_ttl)
//	      se trata como arranque nuevo y va al resolver de disparos de siempre —el
//	      despachador—, nunca a una decisión basada en una intención LLM.
//
// Alcance honesto — lo que este test NO vigila:
//   - No cubre el Edge (edge/wapp-edge-intent), donde vive el clasificador real: eso
//     es responsabilidad de ese repo.
//   - No prueba que una intención nunca LLEGA por el proto. 🔧 Desde T1.6-1 eso ya no
//     es «un hecho del Edge» sino del CONTRATO —`ClassifiedIntent` salió del proto y no
//     hay dónde ponerla—, así que la afirmación es hoy más fuerte y más barata. Lo que
//     este test prueba sigue siendo lo mismo: la navegación numérica y la entrada tardía
//     no dejan que ninguna intención alcance una decisión de Resolve/ResolveLive.
//   - IsEscape y ResolveLive NO pueden quedar bajo el "muere si te llaman": producción
//     las consulta en turnos numéricos legítimos. IsEscape la consulta advanceLive EN
//     TODO turno con conversación viva (runtime/incoming.go, justo antes de
//     liveEventSwitch), y ResolveLive la consulta liveEventSwitch en todo turno vivo
//     que el menú no haya contestado ya (runtime/events.go). Pero eso NO las deja sin
//     vigilar: ambas reciben `text string` SIN Signal —trigger.go, "Opera SIEMPRE
//     sobre texto" y "Recibe texto crudo, no Signal"—, así que la regresión posible no
//     es que se las llame, sino que se las llame con la SALIDA DEL CLASIFICADOR en vez
//     de con lo que tecleó el cliente. Es exactamente eso lo que el fake vigila (campo
//     `texto`). Fatalear a secas daría falsos rojos; no mirar nada dejaría la puerta
//     abierta: se mira el argumento.
//   - En el camino (iii) el resolver de disparos SÍ se invoca (es el despachador,
//     literal en el criterio del plan): lo que se fija no es "nunca se llama" sino
//     "nunca se llama con una intención puesta NI con el texto suplantado por la
//     etiqueta del clasificador".
//   - ⚠️ LÍMITE CONOCIDO (no lo tapa este fichero): en (iii) el gate de entitlements
//     (ADR-0022) es lo único que hoy poda la intención, y estos tests corren con
//     rt.entitlements nil. Con la feature llm_intent HABILITADA, producción SÍ le pasa
//     la intención al resolver en la entrada tardía, también cuando el texto es un «2»
//     — medido. La garantía de que un «2» nunca viaja con intención es del EDGE (el
//     carril rápido del Plan 029 no clasifica entradas cortas); en el Cloud no existe
//     hoy ninguna poda por forma del texto. Si se quiere esa garantía estructural
//     también aquí, es cambio de producción, no de test.

import (
	"context"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
)

// t51FatalResolver es EL FAKE (el corazón de T5.1) del puerto trigger.Resolver para
// los caminos (i) y (ii). Reparte las tres puertas del puerto en dos categorías, y el
// reparto ES la tesis del test:
//
//   - Resolve MATA SIEMPRE: es la ÚNICA puerta que recibe trigger.Signal y por tanto
//     la única por la que una decisión puede depender de una intención clasificada
//     (regla kind='llm', config_resolver.go:47-59). Un número no llega ahí jamás.
//   - IsEscape y ResolveLive reciben `text string` SIN Signal: estructuralmente no
//     pueden transportar una intención. Prohibirlas del todo daría falsos rojos —
//     producción las consulta en turnos numéricos legítimos (ver cabecera)— así que
//     en vez de ignorarlas se VIGILA SU ARGUMENTO: matan si el texto que reciben no
//     es el texto CRUDO que escribió la persona (campo `texto`). Esa es la puerta
//     trasera real: no que se las llame, sino que se las llame con la salida del
//     clasificador en lugar de con lo que tecleó el cliente.
//
// resolveLiveOK dice si la escena de este subtest recorre el tramo donde producción
// consulta ResolveLive (events.go liveEventSwitch, solo con el event store cableado).
// Con false, cualquier llamada es una regresión y mata; con true se permite y se
// CUENTA, para que el subtest pueda exigir que la escena de verdad pasó por ahí.
type t51FatalResolver struct {
	t                *testing.T
	texto            string
	resolveLiveOK    bool
	resolveLiveCalls int
}

func (r *t51FatalResolver) Resolve(_ context.Context, tenantID, sessionID string, sig trigger.Signal) (trigger.Decision, error) {
	r.t.Fatalf("t51 (REQ-21): el carril rápido invocó trigger.Resolver.Resolve — tenant=%q sesión=%q texto=%q intent=%+v; la navegación numérica NUNCA debe llegar aquí",
		tenantID, sessionID, sig.Text, sig.Intent)
	return trigger.Decision{}, nil
}

func (r *t51FatalResolver) IsEscape(_ context.Context, tenantID, sessionID, text string) (bool, string, error) {
	if text != r.texto {
		r.t.Fatalf("t51 (REQ-21): IsEscape recibió %q y el cliente escribió %q — tenant=%q sesión=%q; el escape sobre conversación viva lo decide el TEXTO de la persona, nunca la etiqueta del clasificador (trigger.go, IsEscape: «Opera SIEMPRE sobre texto»; design.md §4.c)",
			text, r.texto, tenantID, sessionID)
	}
	return false, "", nil
}

func (r *t51FatalResolver) ResolveLive(_ context.Context, tenantID, sessionID, text string) (trigger.Decision, error) {
	if !r.resolveLiveOK {
		r.t.Fatalf("t51 (REQ-21): el carril rápido invocó trigger.Resolver.ResolveLive — tenant=%q sesión=%q texto=%q; en esta escena el número lo contesta el menú ANTES de consultar disparo alguno (events.go liveEventSwitch: menuChoice va primero)",
			tenantID, sessionID, text)
	}
	if text != r.texto {
		r.t.Fatalf("t51 (REQ-21): ResolveLive recibió %q y el cliente escribió %q — tenant=%q sesión=%q; el salto entre eventos lo decide el TEXTO de la persona, nunca la etiqueta del clasificador (trigger.go, ResolveLive: «Recibe texto crudo, no Signal»)",
			text, r.texto, tenantID, sessionID)
	}
	r.resolveLiveCalls++
	return trigger.Decision{Action: trigger.Ignore}, nil
}

var _ trigger.Resolver = (*t51FatalResolver)(nil)

// t51LateEntrySpy envuelve el resolver REAL para el camino (iii), entrada tardía: a
// diferencia de (i)/(ii), aquí SÍ se espera que Resolve se llame —es la puerta al
// despachador, literal en el criterio del plan ("va al despachador")—, así que este
// doble no fatalea por ser invocado. Fatalea si, alguna vez, esa llamada trae una
// intención puesta (sig.Intent != nil): eso SÍ sería "llegar al clasificador"
// (kind='llm', config_resolver.go:47-59), y es justo lo que T3.2 no debe hacer.
type t51LateEntrySpy struct {
	t            *testing.T
	inner        trigger.Resolver
	resolveCalls int
	// textos son los textos CRUDOS que se esperan, en orden, en cada llamada a
	// Resolve (se consumen uno a uno). Vacío ⇒ no se vigila el texto.
	textos []string
}

func (s *t51LateEntrySpy) Resolve(ctx context.Context, tenantID, sessionID string, sig trigger.Signal) (trigger.Decision, error) {
	s.resolveCalls++
	if sig.Intent != nil {
		s.t.Fatalf("t51 (REQ-21): la entrada tardía (T3.2) le pasó una intención LLM al resolver — tenant=%q texto=%q intent=%+v; T3.2 debe ir al despachador, no a una decisión con intención",
			tenantID, sig.Text, sig.Intent)
	}
	// Y no basta con que sig.Intent venga vacío: la SEÑAL que llega al despachador
	// tiene que seguir siendo lo que escribió la persona. Si alguien sustituyera el
	// texto por el nombre de la intención (sig.Text = intent), sig.Intent seguiría nil
	// y la decisión la tomaría igualmente el clasificador — por esa rendija se cuela
	// REQ-21 sin que ninguna aserción sobre Intent se entere.
	if len(s.textos) > 0 {
		esperado := s.textos[0]
		s.textos = s.textos[1:]
		if sig.Text != esperado {
			s.t.Fatalf("t51 (REQ-21): el despachador recibió la señal %q y el cliente escribió %q — tenant=%q; la entrada tardía va al despachador con el TEXTO de la persona, no con la salida del clasificador",
				sig.Text, esperado, tenantID)
		}
	}
	return s.inner.Resolve(ctx, tenantID, sessionID, sig)
}

func (s *t51LateEntrySpy) IsEscape(ctx context.Context, tenantID, sessionID, text string) (bool, string, error) {
	return s.inner.IsEscape(ctx, tenantID, sessionID, text)
}

func (s *t51LateEntrySpy) ResolveLive(ctx context.Context, tenantID, sessionID, text string) (trigger.Decision, error) {
	return s.inner.ResolveLive(ctx, tenantID, sessionID, text)
}

var _ trigger.Resolver = (*t51LateEntrySpy)(nil)

// TestCarrilRapido_NumeroEnElMenuDelDespachador es el camino (i): un número que
// contesta el menú del despachador (events.Menu, Ola 2/3) despacha por
// events.Menu.Resolve puro (runtime/events.go menuChoice) y NUNCA toca el resolver
// de disparos (REQ-21).
//
// El truco de las DOS runtime instances es lo que aísla la pata bajo prueba: rt1
// arma la escena con el resolver REAL (necesario para que la palabra «menu» —un
// disparo por KEYWORD, no numérico, y por tanto fuera del alcance de este test—
// pinte el menú y lo deje pendiente en flow_state); rt2 comparte el MISMO
// repo/eventos/contactos (la MISMA conversación persistida) pero solo conoce a
// t51FatalResolver, así que el «1» que sigue no tiene ningún resolver real al que
// escapar: si tocara Resolve o ResolveLive, moriría ahí mismo.
func TestCarrilRapido_NumeroEnElMenuDelDespachador(t *testing.T) {
	ctx := context.Background()
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(ctx, testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar definición: %v", err)
	}
	contacts := contact.NewMemoryResolver(repo)
	evs := newMemEventStore(time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC))
	ts := trigger.NewMemoryStore()
	if _, err := ts.Insert(ctx, eventStartRule("menu", "menu")); err != nil {
		t.Fatalf("insert regla: %v", err)
	}
	menuDelDespachador := events.Menu{Options: []events.MenuOption{{Number: 1, Action: events.ActionStart, Kind: "cart"}}}

	rt1 := runtime.New(repo, newEngine(), &fakeSender{}, fakeResolver{tenantID: testTenant}, contacts, discardLogger(),
		runtime.WithTriggerResolver(trigger.NewConfigResolver(ts)),
		runtime.WithEventStore(evs),
		runtime.WithDispatcher(fakeDispatcher{menu: menuDelDespachador}),
		runtime.WithFlowForKind(fakeFlowForKind{flow: testFlow}))
	if err := rt1.HandleIncoming(ctx, testSession, incoming(testContact, "menu", "wamid.t51i-a")); err != nil {
		t.Fatalf("palabra del menú: %v", err)
	}

	sender2 := &fakeSender{}
	rt2 := runtime.New(repo, newEngine(), sender2, fakeResolver{tenantID: testTenant}, contacts, discardLogger(),
		runtime.WithTriggerResolver(&t51FatalResolver{t: t, texto: "1"}),
		runtime.WithEventStore(evs),
		runtime.WithFlowForKind(fakeFlowForKind{flow: testFlow}))
	if err := rt2.HandleIncoming(ctx, testSession, incoming(testContact, "1", "wamid.t51i-b")); err != nil {
		t.Fatalf("elección numérica: %v", err)
	}

	cart := aliveOfKind(t, evs, "cart")
	st := loadState(t, repo, resolveID(t, contacts, testContact))
	if st.EventID != cart.ID {
		t.Fatalf("el «1» debe despachar al carrito SIN pasar por el resolver; event_id=%q, carrito=%q", st.EventID, cart.ID)
	}
	if sender2.count() == 0 {
		t.Fatal("elegir del menú debe responder algo")
	}
}

// TestCarrilRapido_NumeroDentroDeUnFlujoVivo es el camino (ii): un número que
// contesta el nodo `root` de un flujo YA vivo (sampleFlow, un NodeTypeMenu) avanza
// por engine.Step (runtime/incoming.go advanceLive) y NUNCA toca Resolve, la única
// puerta con Signal (REQ-21). ResolveLive e IsEscape sí se consultan —producción lo
// hace en todo turno vivo— y por eso se vigila que reciban el texto CRUDO «2» y no
// una etiqueta del clasificador.
//
// El flow_state se siembra DIRECTO con repo.Save (sin pasar por HandleIncoming): así
// el «2» llega como AVANCE de un flujo ya vivo y no como arranque —evita el disparo
// legítimo por palabra clave que arrancaría sampleFlow, y aísla justo la pata que
// REQ-21 exige: numérico + conversación viva.
func TestCarrilRapido_NumeroDentroDeUnFlujoVivo(t *testing.T) {
	ctx := context.Background()
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(ctx, testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar definición: %v", err)
	}
	contacts := contact.NewMemoryResolver(repo)
	contactID := resolveID(t, contacts, testContact)
	if err := repo.Save(ctx, model.Conversation{
		TenantID: testTenant, SessionID: testSession, ContactID: contactID,
		FlowID: testFlow, FlowVersion: 1, CurrentNode: "root",
	}); err != nil {
		t.Fatalf("sembrar flow_state: %v", err)
	}
	sender := &fakeSender{}
	// El event store VA CABLEADO a propósito: es lo que hace que la escena recorra el
	// mismo tramo que producción (events.go liveEventSwitch). Sin él, rt.events queda
	// nil, liveEventSwitch corta en su primera línea y el «2» no llega nunca al sitio
	// donde una regresión metería el clasificador — el test se protegería a sí mismo
	// en vez de proteger el código.
	res := &t51FatalResolver{t: t, texto: "2", resolveLiveOK: true}
	rt := runtime.New(repo, newEngine(), sender, fakeResolver{tenantID: testTenant}, contacts, discardLogger(),
		runtime.WithTriggerResolver(res),
		runtime.WithEventStore(newMemEventStore(time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC))))

	// 🔧 AQUÍ IBA UN SEÑUELO Y SE FUE CON EL CAMPO (T1.6-1/T1.6-4). El «2» llegaba con
	// una etiqueta del clasificador pegada, y eso le daba filo al fake: un cambio que
	// sustituyera el texto por la etiqueta en IsEscape/ResolveLive tenía algo que
	// sustituir y se veía. Hoy `IncomingMessage` NO TIENE dónde llevar una etiqueta
	// —D-044.31 retiró `ClassifiedIntent` del contrato—, así que el señuelo no se puede
	// montar y esa mutación concreta ya no es expresable: no hay etiqueta que poner en
	// lugar del texto. Lo que este subtest sigue fijando —que el «2» vivo atraviesa
	// liveEventSwitch y que ResolveLive se consulta con el texto crudo— no cambia.
	numerico := incoming(testContact, "2", "wamid.t51ii-a")
	if err := rt.HandleIncoming(ctx, testSession, numerico); err != nil {
		t.Fatalf("navegar con «2»: %v", err)
	}

	// La escena TIENE que haber pasado por liveEventSwitch: si un día deja de pasar,
	// este subtest seguiría verde sin vigilar nada y hay que enterarse.
	if res.resolveLiveCalls != 1 {
		t.Fatalf("el «2» vivo debe atravesar liveEventSwitch (ResolveLive consultado con texto crudo) exactamente una vez; se consultó %d veces", res.resolveLiveCalls)
	}

	// El nodo `soporte` es un NodeTypeMessage (sampleFlow): el engine lo renderiza Y
	// cierra el flujo en el mismo Step (pasa al centinela __wapp_flow_end__), así que
	// lo que prueba que el «2» SÍ despachó a `soporte` —y no, por ejemplo, quedó
	// atascado o cayó al reprompt del propio menú— es el TEXTO enviado, no el nodo en
	// que queda el puntero.
	if sender.count() != 1 {
		t.Fatalf("debe enviarse UNA respuesta, se enviaron %d", sender.count())
	}
	if got := sender.texts()[0]; got != "Cuéntame tu problema." {
		t.Fatalf("«2» debe despachar al nodo `soporte` (Options[\"2\"]=\"soporte\" de sampleFlow); se envió %q", got)
	}
}

// TestCarrilRapido_EntradaTardiaVaAlDespachadorNoAlLLM es el camino (iii), T3.2: un
// «2» que llega DESPUÉS de que el evento activo (cart) se suspendiera por
// inactividad (event_inactivity_ttl_seconds = 1 h, 90 minutos de silencio) se trata
// como arranque nuevo (runtime/incoming.go conversationClock → eventClock →
// releaseForNewConversation) y va al resolver de disparos de siempre —el
// despachador, vía handleTrigger→openWithOffer, literal en el criterio del
// plan—, nunca a una decisión con una intención LLM.
//
// 🔧 EL MENSAJE TARDÍO LLEVABA UN `ClassifiedIntent` SIMULADO, y ya no puede: el
// campo se retiró del contrato (T1.6-1, D-044.31) y buildSignal dejó de leerlo
// (T1.6-4). La aserción del spy sobre `sig.Intent != nil` sigue en pie y hoy prueba
// algo MÁS FUERTE que antes —entonces probaba que el gate de la feature podaba la
// etiqueta; hoy prueba que no hay etiqueta que podar, venga de donde venga—, pero
// hay que decir lo que se perdió: ya no hay forma de simular un Edge que mande la
// etiqueta igualmente, porque el proto no tiene dónde ponerla.
func TestCarrilRapido_EntradaTardiaVaAlDespachadorNoAlLLM(t *testing.T) {
	t0 := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	ctx := context.Background()
	repo := store.NewMemoryRepository()
	for _, f := range []model.Flow{sampleFlow(), fallbackFlow()} {
		if _, err := repo.InsertDefinition(ctx, testTenant, f); err != nil {
			t.Fatalf("sembrar %s: %v", f.FlowID, err)
		}
	}
	sembrarInactividad(t, repo, time.Hour, 0)
	ts := trigger.NewMemoryStore()
	for _, r := range []trigger.Rule{
		eventStartRule("carrito", "cart"),
		{TenantID: testTenant, Kind: trigger.KindFallback, FlowID: testFallbackFlow, Enabled: true},
	} {
		if _, err := ts.Insert(ctx, r); err != nil {
			t.Fatalf("insert regla: %v", err)
		}
	}
	sender := &fakeSender{}
	contacts := contact.NewMemoryResolver(repo)
	evs := newMemEventStore(t0)
	ofrece := &fakeOpening{apertura: ofertaConOpciones()}
	spy := &t51LateEntrySpy{t: t, inner: trigger.NewConfigResolver(ts), textos: []string{"carrito", "2"}}
	rt := runtime.New(repo, newEngine(), sender, fakeResolver{tenantID: testTenant}, contacts, discardLogger(),
		runtime.WithTriggerResolver(spy),
		runtime.WithEventStore(evs),
		runtime.WithOpeningBuilder(ofrece))

	// «Carrito» pare el evento cart (T2.2): UNA llamada legítima a Resolve, por
	// keyword —no numérica, fuera del alcance de REQ-21—.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "wamid.t51iii-a")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	llamadasTrasArranque := spy.resolveCalls

	// 90 minutos de silencio contra 1 h de tolerancia (T3.1/T3.2, REQ-09): el evento
	// activo queda suspendido, la conversación se suelta y el «2» que sigue entra
	// como ENTRADA TARDÍA (arranque nuevo, no avance).
	evs.now = t0.Add(90 * time.Minute)
	tarde := incoming(testContact, "2", "wamid.t51iii-b")
	if err := rt.HandleIncoming(ctx, testSession, tarde); err != nil {
		t.Fatalf("entrada tardía: %v", err)
	}

	if spy.resolveCalls != llamadasTrasArranque+1 {
		t.Fatalf("la entrada tardía debe consultar el resolver de disparos EXACTAMENTE una vez más (el despachador, REQ-21); iba en %d y quedó en %d",
			llamadasTrasArranque, spy.resolveCalls)
	}
	if ofrece.veces != 1 {
		t.Fatalf("y esa consulta debe caer al fallback, que abre la oferta del despachador; se pidió %d veces", ofrece.veces)
	}
	st := loadState(t, repo, resolveID(t, contacts, testContact))
	if st.EventID != "" {
		t.Fatalf("la entrada tardía NO debe heredar el evento vencido; event_id=%q", st.EventID)
	}
}
