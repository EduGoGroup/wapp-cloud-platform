package runtime_test

// poison_event_id_leak_test.go — CENTINELA de E-3 (Plan 043 · Ola 6 · T6.3).
//
// El criterio de T6.3 exige comprobar, contra WhatsApp real, que «en ningún mensaje
// real aparece un identificador de evento». Eso se hace a ojo en el guion humano
// (docs/GUION-043-T63-e2e-real.md) — pero un ojo humano mira UNA corrida. Este
// fichero es la otra mitad, que vigila TODAS las corridas, para siempre, en CI: un
// centinela que siembra un identificador VENENOSO y reconocible en el nacimiento de
// cada evento, recorre los caminos de redacción del runtime que hablan con el
// cliente, y falla si el veneno aparece en cualquier texto que el runtime mandó a
// enviar.
//
// ── Método (el mismo que ya funcionó para H1, Ola 5) ──────────────────────────────
//  1. Sembrar el veneno: poisonEventStore reemplaza, en CADA evento que nace, su ID
//     TÉCNICO y su HistoryID por un marcador reconocible
//     ("EV-VENENO-NO-DEBE-SALIR-ID-<n>" / "…-HIST-<n>"). No es un valor cualquiera:
//     ID es lo que MenuOption.EventID transporta (events/menu.go) y HistoryID es
//     literalmente el identificador que E-3 prohíbe mencionar (events/events.go).
//  2. Ejercitar TODOS los caminos de redacción conocidos (ver la lista de abajo),
//     con el veneno viajando de verdad por ellos (un MenuOption.EventID poisoned, un
//     evento con HistoryID poisoned).
//  3. Afirmar, tras CADA turno, que ni un carácter del veneno aparece en ningún texto
//     que `fakeSender.SendText` recibió — capturado en el ÚNICO chokepoint por el que
//     pasa TODO texto saliente de este runtime (runtime/send.go:29, rt.sender.SendText).
//  4. Afirmar además, en cada turno, el NÚMERO EXACTO de envíos esperado. Esto es lo
//     que hace que el centinela FALLE ante lo que no sabe redactar en vez de callarse:
//     si una regresión futura añade un envío que este guion no esperaba —un camino de
//     redacción nuevo, o uno viejo que empieza a hablar dos veces—, el conteo se
//     descuadra y el test se pone rojo AUNQUE ese envío nuevo no llevara veneno. No
//     hay manera de que un camino nuevo pase por aquí en silencio.
//
// ── Alcance HONESTO: qué vigila y qué NO ──────────────────────────────────────────
//   - SÍ vigila: todo lo que el RUNTIME de este repo (cloud/wapp-cloud-platform)
//     compone y entrega a Sender.SendText — el menú del despachador
//     (presentMenu/events.Menu.Render), la oferta de retomar y la entrada de
//     conversación (sendOffer/events.Offering, BuildRescue/BuildOpening reales), el
//     automensaje de rescate (sendResumeSummary), la confirmación de event_stop
//     (stopNotice), el aviso de escape global (handleEscape) y las pantallas propias
//     del módulo `cart` (contenido de negocio, sin relación con events.Event, pero
//     igual barridas por pasar por el mismo chokepoint).
//   - NO vigila el Edge (edge/wapp-edge-agent): si el Edge compusiera texto por su
//     cuenta —hoy no lo hace, Cloud arma el payload completo (ADR-0005)— este
//     centinela no lo vería. Tampoco vigila lo que el Edge decida REENVIAR fuera de
//     forma (p. ej. si algún día registrara logs con el cuerpo del mensaje).
//   - NO vigila publicapi/events/telemetry.go (el lector de flow_events, T6.5): esa
//     tabla SÍ lleva history_id a propósito (es la bandeja del negocio, no la
//     conversación) y este centinela no la toca.
//   - NO ejercita TODAS las combinaciones de estado posibles del runtime; ejercita
//     las siete que T6.3 nombra por su letra (ver el mapeo turno-a-turno más abajo).
//     Un camino de redacción que exista pero al que este script nunca llegue —porque
//     ninguna combinación de reglas/estado de este fichero lo dispara— NO está
//     cubierto, y esta nota lo dice para que no se lea como si lo estuviera.
//
// ── Validado con la mutación real (ver el informe de T6.3) ────────────────────────
// events/menu.go optionLabel(), rama ActionResume, se mutó una vez a
//     return words(o.Kind).resume + " (" + o.EventID + ")"
// —el cambio más pequeño y más realista que de verdad mete el identificador en un
// mensaje— y TestCentinela_NingunIdentificadorDeEventoLlegaAlCliente se puso ROJO
// exactamente en el turno del menú del despachador y en el de la oferta de retomar
// (los dos que renderizan un ActionResume), con el texto ofensivo citado en el
// mensaje del `t.Fatalf`. La mutación se revirtió byte a byte tras capturar la
// salida (que queda pegada en el journal/guion, no en este fichero).

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

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
)

// poisonEventIDPrefix / poisonHistoryIDPrefix son el veneno RECONOCIBLE (T6.3 lo pide
// "algo como EV-VENENO-NO-DEBE-SALIR"). Dos marcadores y no uno porque son DOS campos
// de riesgo distintos con DOS puertas de entrada distintas al texto: HistoryID es el
// identificador legible que E-3 nombra explícitamente; ID es lo que MenuOption.EventID
// transporta y lo que un "optionLabel útil" tentaría a imprimir (ver la mutación de
// validación, más arriba). Envenenar solo uno dejaría la otra puerta sin vigilar.
const (
	poisonEventIDPrefix   = "EV-VENENO-NO-DEBE-SALIR-ID-"
	poisonHistoryIDPrefix = "EV-VENENO-NO-DEBE-SALIR-HIST-"
)

// poisonEventStore envenena CADA fila que nace: relojEventStore/memEventStore siguen
// imponiendo las mismas reglas de negocio (unicidad por tipo, rescatables, reloj); lo
// único que cambia es CON QUÉ IDENTIFICADOR nace cada evento. numerado por `seq` para
// que dos eventos del mismo test tengan marcadores DISTINGUIBLES entre sí (si un
// mensaje llevara el veneno del evento #2 en vez del #1, el Fatalf lo dice).
type poisonEventStore struct {
	*relojEventStore
	seq int
}

func newPoisonEventStore(inicio time.Time) *poisonEventStore {
	return &poisonEventStore{relojEventStore: nuevoRelojEventStore(inicio)}
}

func (p *poisonEventStore) CreateEvent(ctx context.Context, in events.NewEvent) (events.Event, error) {
	ev, err := p.relojEventStore.CreateEvent(ctx, in)
	if err != nil {
		return ev, err
	}
	p.seq++
	ev.ID = fmt.Sprintf("%s%d", poisonEventIDPrefix, p.seq)
	ev.HistoryID = fmt.Sprintf("%s%d", poisonHistoryIDPrefix, p.seq)
	// El doble subyacente guarda POR VALOR (m.rows []events.Event): hay que
	// reescribir la fila ya insertada, o toda lectura posterior (ListAlive,
	// ListRescuable, aliveByID…) devolvería el ID limpio que CreateEvent
	// devolvió por debajo y el veneno nunca llegaría a circular.
	p.mu.Lock()
	p.rows[len(p.rows)-1] = ev
	p.mu.Unlock()
	return ev, nil
}

// dynamicDispatcher es el puerto runtime.Dispatcher (el menú del despachador) con el
// menú resuelto POR FUNCIÓN y no por valor fijo: el veneno del evento `cart` solo se
// conoce DESPUÉS de que ese evento nazca (turno 1), y el menú que lo ofrece de vuelta
// se pinta en un turno posterior (turno 3) — closures, no una constante.
type dynamicDispatcher struct{ menu func() events.Menu }

func (d dynamicDispatcher) Build(context.Context, events.ConversationRef) (events.Menu, error) {
	return d.menu(), nil
}

// dynamicOpening es el puerto runtime.OpeningBuilder (entrada + rescate) con las
// mismas dos mitades resueltas por función, por la misma razón que dynamicDispatcher.
type dynamicOpening struct {
	apertura func() events.Offering
	rescate  func() events.Offering
}

func (o dynamicOpening) BuildOpening(context.Context, events.ConversationRef) (events.Offering, error) {
	return o.apertura(), nil
}

func (o dynamicOpening) BuildRescue(context.Context, events.ConversationRef) (events.Offering, error) {
	return o.rescate(), nil
}

func (o dynamicOpening) BuildTagline(context.Context, events.ConversationRef) (string, error) {
	return "", nil
}

// assertSinVeneno es EL assert de todo este fichero: recorre TODOS los textos vistos
// por fakeSender hasta ahora (no solo los del último turno — un veneno que se coló en
// un turno anterior y nadie miró sigue siendo un veneno) y falla citando el texto
// ofensivo si aparece cualquiera de los dos marcadores.
func assertSinVeneno(t *testing.T, etapa string, sender *fakeSender) {
	t.Helper()
	for i, txt := range sender.texts() {
		if strings.Contains(txt, poisonEventIDPrefix) || strings.Contains(txt, poisonHistoryIDPrefix) {
			t.Fatalf("E-3 (%s): el mensaje #%d salido al cliente lleva un identificador de evento: %q", etapa, i, txt)
		}
	}
}

// mustTurn manda UN entrante y falla con el rótulo del turno si HandleIncoming
// devuelve error. Extraído para que TestCentinela_NingunIdentificadorDeEventoLlegaAlCliente
// no acumule un `if err != nil` por turno — es lo único que le sobraba a gocyclo, y
// el guion en sí es lineal (turno tras turno, sin ramas de negocio).
func mustTurn(t *testing.T, rt *runtime.Runtime, etapa, texto, waID string) {
	t.Helper()
	if err := rt.HandleIncoming(context.Background(), testSession, incoming(testContact, texto, waID)); err != nil {
		t.Fatalf("%s: %v", etapa, err)
	}
}

// assertEnvios fija el NÚMERO EXACTO de envíos tras un turno — no "al menos", exacto.
// Es la mitad del centinela que hace que un camino de redacción NUEVO, o uno viejo que
// empieza a hablar de más, no pueda colarse en silencio: si el conteo no cuadra, el
// test se pone rojo aunque el texto de más no llevara veneno, porque significa que
// este guion dejó de saber qué está pasando en su propio escenario.
func assertEnvios(t *testing.T, etapa string, sender *fakeSender, antes, esperados int) {
	t.Helper()
	got := sender.count() - antes
	if got != esperados {
		t.Fatalf("%s: se esperaban %d envíos nuevos y hubo %d (textos: %q) — un camino de redacción "+
			"se movió y este centinela no lo tenía previsto; revisa si el nuevo envío es legítimo y "+
			"AÑÁDELO al guion antes de continuar", etapa, esperados, got, sender.texts()[antes:])
	}
}

// TestCentinela_FormaConocidaDeEventsEvent es la mitad "falla ante lo desconocido" por
// REFLEXIÓN SOBRE LA ESTRUCTURA DE ENTRADA (T6.3, mismo patrón que ya usó H1/Ola 5): en
// vez de enumerar a mano qué campos de events.Event hay que vigilar, se enumeran los
// campos REALES por reflect y se comparan contra la lista que este test conoce. El día
// que alguien añada un campo a events.Event, este test se pone ROJO —no se salta en
// silencio— y el mensaje de fallo señala exactamente qué revisar: si el campo nuevo
// puede llegar a un texto de cara al cliente, hay que sumarlo al veneno de
// TestCentinela_NingunIdentificadorDeEventoLlegaAlCliente antes de dar el cambio por
// bueno.
func TestCentinela_FormaConocidaDeEventsEvent(t *testing.T) {
	conocidos := []string{
		"ID", "TenantID", "SessionID", "ContactID", "Kind", "HistoryID", "Status",
		"FlowID", "FlowVersion", "CreatedAt", "LastActivityAt", "ClosedAt",
	}
	tipo := reflect.TypeOf(events.Event{})
	if tipo.NumField() != len(conocidos) {
		t.Fatalf("events.Event tiene %d campos y este centinela conoce %d — revisa el campo nuevo "+
			"(¿puede llegar a un mensaje al cliente?) y actualiza `conocidos` en este test", tipo.NumField(), len(conocidos))
	}
	for i, nombre := range conocidos {
		real := tipo.Field(i).Name
		if real != nombre {
			t.Fatalf("events.Event cambió de forma: el campo #%d es %q, este centinela esperaba %q — "+
				"revisa si el cambio puede llegar a un mensaje al cliente y actualiza este test", i, real, nombre)
		}
	}
}

// TestCentinela_NingunIdentificadorDeEventoLlegaAlCliente es el centinela de E-3
// (T6.3). Guion turno a turno — cada turno está rotulado con el CASO del criterio de
// T6.3 que ejercita, cuando aplica, y con el camino de redacción que vigila.
func TestCentinela_NingunIdentificadorDeEventoLlegaAlCliente(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 8, 11, 9, 0, 0, 0, time.UTC)

	repo := store.NewMemoryRepository()
	repo.SetTenantContent(testTenant, "catalogo", []byte(cartCatalogBlob))
	if _, err := repo.InsertDefinition(ctx, testTenant, cartFlow(testCartFlow)); err != nil {
		t.Fatalf("sembrar cart flow: %v", err)
	}
	if _, err := repo.InsertDefinition(ctx, testTenant, fallbackFlow()); err != nil {
		t.Fatalf("sembrar fallback flow: %v", err)
	}

	reg := modules.NewRegistry()
	reg.Register(menu.New())
	reg.Register(cart.New())
	eng := engine.New(reg, engine.WithContentSource(content.NewRouter(content.NewStatic(), content.NewJSON(repo))))

	ts := trigger.NewMemoryStore()
	for _, r := range []trigger.Rule{
		{TenantID: testTenant, Kind: trigger.KindEventStart, Keyword: "carrito", MatchType: trigger.MatchExact,
			EventKind: "cart", FlowID: testCartFlow, Enabled: true},
		{TenantID: testTenant, Kind: trigger.KindEventStart, Keyword: "menu", MatchType: trigger.MatchExact,
			EventKind: trigger.EventKindMenu, Enabled: true}, // D-043.3: sin FlowID, lo pinta el despachador.
		{TenantID: testTenant, Kind: trigger.KindEventStop, Keyword: "parar", MatchType: trigger.MatchExact, Enabled: true},
		{TenantID: testTenant, Kind: trigger.KindEscape, Keyword: "salir", MatchType: trigger.MatchExact, Enabled: true},
		{TenantID: testTenant, Kind: trigger.KindFallback, FlowID: testFallbackFlow, Enabled: true},
	} {
		if _, err := ts.Insert(ctx, r); err != nil {
			t.Fatalf("insert regla: %v", err)
		}
	}

	evs := newPoisonEventStore(t0)
	sender := &fakeSender{}
	contacts := contact.NewMemoryResolver(repo)
	ec := &ecSink{}

	// El veneno del `cart` se conoce DESPUÉS del turno 1: las dos dobles de abajo lo
	// leen por closure sobre esta variable, resuelta en el momento en que el runtime
	// las llama (turnos 3+), nunca antes.
	var cartPoisonedID string

	rt := runtime.New(repo, eng, sender, fakeResolver{tenantID: testTenant}, contacts, discardLogger(),
		runtime.WithTriggerResolver(trigger.NewConfigResolver(ts)),
		runtime.WithEventStore(evs),
		runtime.WithEventSink(persistSinkWith(repo)),
		runtime.WithEventSink(ec),
		cartResumeOpt(repo),
		runtime.WithSummarySources(runtime.NewSummarySources(repo)),
		runtime.WithClock(evs.ahora),
		runtime.WithDispatcher(dynamicDispatcher{menu: func() events.Menu {
			// "el menú del despachador": UNA opción de empezar y UNA de retomar el
			// carrito envenenado — el MenuOption.EventID que lleva el veneno de ID.
			return events.Menu{Options: []events.MenuOption{
				{Number: 1, Action: events.ActionStart, Kind: "survey"},
				{Number: 2, Action: events.ActionResume, Kind: "cart", EventID: cartPoisonedID},
			}}
		}}),
		runtime.WithOpeningBuilder(dynamicOpening{
			apertura: func() events.Offering {
				// "avisos genéricos": la entrada de conversación sin evento activo
				// (BuildOpening real, D-043.17 colapsado en una sola línea de rescate).
				m := events.Menu{Options: []events.MenuOption{{Number: 1, Action: events.ActionRescue, Count: 1}}}
				return events.Offering{Text: m.Render(), Menu: m}
			},
			rescate: func() events.Offering {
				// "oferta de retomar": BuildRescue real con el carrito envenenado.
				m := events.Menu{Options: []events.MenuOption{
					{Number: 1, Action: events.ActionResume, Kind: "cart", EventID: cartPoisonedID},
				}}
				return events.Offering{Text: m.Render(), Menu: m}
			},
		}))

	// Turno 1 — "palabra de inicio" (caso 1 del criterio T6.3): nace el evento cart
	// (envenenado) y el flujo del carrito manda su pantalla propia (contenido de
	// negocio, no de events.Event, pero igual barrido por el chokepoint).
	antes := sender.count()
	mustTurn(t, rt, "turno 1 (carrito)", "carrito", "cnt-1")
	assertEnvios(t, "turno1 carrito", sender, antes, 1)
	assertSinVeneno(t, "turno1 carrito", sender)
	cartEv := aliveOfKind(t, evs.memEventStore, "cart")
	if !strings.HasPrefix(cartEv.ID, poisonEventIDPrefix) || !strings.HasPrefix(cartEv.HistoryID, poisonHistoryIDPrefix) {
		t.Fatalf("precondición: el evento cart debe nacer ENVENENADO, llegó ID=%q HistoryID=%q", cartEv.ID, cartEv.HistoryID)
	}
	cartPoisonedID = cartEv.ID

	// Turno 2 — se arma una línea real (Café x2) para que el automensaje de rescate
	// del turno 4 tenga contenido de verdad que renderizar, no un resumen vacío que se
	// calla. cartAddCafe manda sus propios 4 turnos; solo interesa que no envenenen.
	antesAdd := sender.count()
	cartAddCafe(t, rt, "cnt-2")
	assertSinVeneno(t, "turno2 agregar café", sender)
	_ = antesAdd

	// Turno 3 — "el menú del despachador": nace el evento `menu` (D-043.3, sin flujo)
	// y presentMenu renderiza la opción 2 con el EventID envenenado del carrito.
	antes = sender.count()
	mustTurn(t, rt, "turno 3 (menu)", "menu", "cnt-3")
	assertEnvios(t, "turno3 menu", sender, antes, 1)
	assertSinVeneno(t, "turno3 menú del despachador", sender)

	// Turno 4 — "confirmación tras el «2»" / "automensaje de rescate": elegir la
	// opción 2 del menú del despachador retoma el carrito (switchToEvent):
	// sendResumeSummary (las líneas del pedido, reales) + la pantalla del flujo del
	// carrito al re-entrar. Dos envíos.
	antes = sender.count()
	mustTurn(t, rt, "turno 4 («2»)", "2", "cnt-4")
	assertEnvios(t, "turno4 «2»", sender, antes, 2)
	assertSinVeneno(t, "turno4 confirmación/rescate", sender)
	nuevos := sender.texts()[antes:]
	if !strings.Contains(nuevos[0], "Café") {
		t.Fatalf("turno4: el automensaje de rescate debe traer la línea real del pedido: %q", nuevos[0])
	}

	// Turno 5 — "event_stop" (caso 3 del criterio T6.3): stopNotice nombra el TIPO
	// ("pedido"), nunca el identificador.
	antes = sender.count()
	mustTurn(t, rt, "turno 5 (parar)", "parar", "cnt-5")
	assertEnvios(t, "turno5 parar", sender, antes, 1)
	assertSinVeneno(t, "turno5 aviso de event_stop", sender)
	if !strings.Contains(sender.texts()[len(sender.texts())-1], "pedido") {
		t.Fatalf("turno5: la confirmación de event_stop debe nombrar el TIPO: %q", sender.texts()[len(sender.texts())-1])
	}

	// Turno 6 — "avisos genéricos": nada casa ningún trigger ⇒ cae al fallback, que
	// con OpeningBuilder cableado se sustituye por la entrada que ofrece (T3.8.4,
	// INV-20) — BuildOpening real.
	antes = sender.count()
	mustTurn(t, rt, "turno 6 (fallback)", "buenas tardes", "cnt-6")
	assertEnvios(t, "turno6 fallback/apertura", sender, antes, 1)
	assertSinVeneno(t, "turno6 avisos genéricos", sender)

	// Turno 7 — "oferta de retomar": elegir la ÚNICA opción de la entrada
	// ("Retomar algo que dejaste a medias") pide la lista (BuildRescue real), que
	// enseña el carrito envenenado.
	antes = sender.count()
	mustTurn(t, rt, "turno 7 (retomar)", "1", "cnt-7")
	assertEnvios(t, "turno7 retomar", sender, antes, 1)
	assertSinVeneno(t, "turno7 oferta de retomar", sender)

	// Turno 8 — reactiva el carrito diciendo su palabra ("salto por tipo", caso 2 del
	// criterio T6.3): el carrito está vivo pero inactivo desde el turno 5 (event_stop
	// lo desactivó sin matarlo) ⇒ gestureGoTo conmuta hacia él (switchToEvent otra
	// vez: resumen + pantalla del flujo).
	antes = sender.count()
	mustTurn(t, rt, "turno 8 (carrito, salto por tipo)", "carrito", "cnt-8")
	assertEnvios(t, "turno8 salto por tipo", sender, antes, 2)
	assertSinVeneno(t, "turno8 salto por tipo", sender)

	// Turno 9 — "escape global" (caso 4 del criterio T6.3): handleEscape borra el
	// flow_state y avisa con el mensaje genérico de escape (sin identificador —
	// estructuralmente no puede llevarlo: el aviso no recibe el evento, solo el
	// texto configurado en la regla).
	antes = sender.count()
	mustTurn(t, rt, "turno 9 (salir, escape global)", "salir", "cnt-9")
	assertEnvios(t, "turno9 escape global", sender, antes, 1)
	assertSinVeneno(t, "turno9 aviso de escape", sender)
	if _, visto := findEffectContext(ec, runtime.EffectEventEscaped); !visto {
		t.Fatalf("turno9: el escape global debe dejar event_escaped en flow_events (Ola 6, E5); efectos vistos=%v",
			effectNames(ec))
	}

	// Turno 10 — el QUINTO camino de suelta (releaseOrphanMenu, Ola 6 · E6): «menu»
	// vuelve a nacer (nuevo evento `menu`, sin flujo) y un texto que el menú
	// pendiente no reconoce (ni número, ni palabra clave) suelta el flow_state
	// huérfano y emite event_escaped otra vez. Vigilado por el MISMO chokepoint: si
	// algún día este camino compusiera un aviso propio, este centinela lo vería
	// igual que a cualquier otro.
	antes = sender.count()
	mustTurn(t, rt, "turno 10 (menu #2)", "menu", "cnt-10")
	assertEnvios(t, "turno10 menu #2", sender, antes, 1)
	assertSinVeneno(t, "turno10 menú #2", sender)
	mustTurn(t, rt, "turno 10b (texto huérfano)", "esto no es un número", "cnt-11")
	assertSinVeneno(t, "turno10b quinto camino de suelta", sender)

	// Cierre: TODO lo dicho en la conversación entera, otra vez, por si acaso.
	assertSinVeneno(t, "cierre", sender)
}

// findEffectContext y effectNames viven en escape_effect_test.go (mismo paquete).
