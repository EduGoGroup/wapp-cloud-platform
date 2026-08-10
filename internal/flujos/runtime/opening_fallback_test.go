package runtime_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
)

// ---------------------------------------------------------------------------
// Plan 043 · Ola 3 · T3.8.4 — la lista SUSTITUYE al fallback, y SOLO ahí
// (REQ-27b, INV-20, D-043.20). La escena de Marta CON fallback.
// ---------------------------------------------------------------------------

// textoDelFallback es lo que el tenant tiene configurado hoy para quien escribe algo
// que no casa nada. La tarea entera consiste en que esta frase deje de ser lo que se
// envía — sin desaparecer: sigue siendo la red del caso vacío.
const textoDelFallback = "Perdona, no te entendí. Escribe MENU para ver opciones."

const testFallbackFlow = "fallback-marta"

// fallbackFlow es el flujo del `fallback` del tenant. Su nodo inicial es un menú y no
// un message a propósito: así deja flow_state VIVO con su FlowID, y el criterio (c)
// —«no se arranca el flujo de fallback»— se comprueba mirando el estado, que es prueba
// directa y no una inferencia sobre el texto.
func fallbackFlow() model.Flow {
	return model.Flow{
		FlowID:  testFallbackFlow,
		Initial: "no-entendi",
		Nodes: map[string]model.Node{
			"no-entendi": {
				Type:    model.NodeTypeMenu,
				Prompt:  textoDelFallback,
				Options: map[string]string{"1": "no-entendi"},
			},
		},
	}
}

// fakeOpening es el doble del constructor de T3.8.1-3 (Frente B). Cuenta las llamadas
// porque el criterio (f) no se prueba mirando el texto: hay que acreditar que en la
// rama Start NI SIQUIERA se pregunta por la lista — pedirla y descartarla también
// sería un fallo, y solo el contador lo ve.
type fakeOpening struct {
	apertura events.Offering
	rescate  events.Offering
	err      error
	veces    int
	rescates int
}

func (f *fakeOpening) BuildOpening(context.Context, events.ConversationRef) (events.Offering, error) {
	f.veces++
	return f.apertura, f.err
}

func (f *fakeOpening) BuildRescue(context.Context, events.ConversationRef) (events.Offering, error) {
	f.rescates++
	return f.rescate, nil
}

// BuildTagline no participa en estos escenarios —la coletilla es del camino con
// llm_intent (T3.8 punto 2) y aquí se prueba el de la lista—, pero el puerto la exige.
// Devolver "" es además el assert implícito de que este camino NO la lleva.
func (f *fakeOpening) BuildTagline(context.Context, events.ConversationRef) (string, error) {
	return "", nil
}

// ofertaConOpciones es la entrada que ofrece: lo que Marta debe ver en vez del «no te
// entendí». La opción 3 es la de retomar, que es la que T3.8 añade al final.
func ofertaConOpciones() events.Offering {
	return events.Offering{
		Text: "¿Qué quieres hacer?\n1) Hacer un pedido\n2) Responder la encuesta\n3) Retomar algo que dejaste a medias (1)",
		Menu: events.Menu{Options: []events.MenuOption{
			{Number: 1, Action: events.ActionStart, Kind: "cart"},
			{Number: 2, Action: events.ActionStart, Kind: "survey"},
			{Number: 3, Action: events.ActionRescue},
		}},
	}
}

// newMartaRuntime arma el tenant de la escena: su flujo de fallback sembrado y, según
// el caso, la regla que lo habilita. `extra` añade reglas (p. ej. la keyword de (f)).
//
// El EventStore va cableado porque sin él liveEventSwitch corta antes de llegar a
// menuChoice, y entonces el número con que el cliente contesta la lista no se
// interpretaría — que es justo lo que el test del «3» tiene que poder ver.
func newMartaRuntime(t *testing.T, ofrece *fakeOpening, conFallback bool, extra ...trigger.Rule) (
	*runtime.Runtime, *store.MemoryRepository, *fakeSender, *contact.MemoryResolver, *memEventStore,
) {
	t.Helper()
	ctx := context.Background()
	repo := store.NewMemoryRepository()
	for _, f := range []model.Flow{sampleFlow(), fallbackFlow()} {
		if _, err := repo.InsertDefinition(ctx, testTenant, f); err != nil {
			t.Fatalf("sembrar %s: %v", f.FlowID, err)
		}
	}
	ts := trigger.NewMemoryStore()
	reglas := append([]trigger.Rule{}, extra...)
	if conFallback {
		reglas = append(reglas, trigger.Rule{
			TenantID: testTenant, Kind: trigger.KindFallback, FlowID: testFallbackFlow, Enabled: true,
		})
	}
	for _, r := range reglas {
		if _, err := ts.Insert(ctx, r); err != nil {
			t.Fatalf("insert regla: %v", err)
		}
	}
	sender := &fakeSender{}
	contacts := contact.NewMemoryResolver(repo)
	evs := newMemEventStore(time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC))
	rt := runtime.New(repo, newEngine(), sender, fakeResolver{tenantID: testTenant}, contacts, discardLogger(),
		runtime.WithTriggerResolver(trigger.NewConfigResolver(ts)),
		runtime.WithEventStore(evs),
		runtime.WithOpeningBuilder(ofrece))
	return rt, repo, sender, contacts, evs
}

// assertNoArrancoElFallback comprueba el criterio (c): pase lo que pase, el flujo del
// `fallback` no se arrancó. Se mira el ESTADO y no el texto — un flow_state con ese
// FlowID es la huella que deja startLocked, y es exactamente lo que el criterio
// prohíbe.
func assertNoArrancoElFallback(t *testing.T, repo *store.MemoryRepository, contacts *contact.MemoryResolver) {
	t.Helper()
	st, ok, err := repo.Load(context.Background(), store.Key{
		TenantID: testTenant, SessionID: testSession, ContactID: resolveID(t, contacts, testContact),
	})
	if err != nil {
		t.Fatalf("cargar estado: %v", err)
	}
	if ok && st.FlowID == testFallbackFlow {
		t.Fatalf("la lista NO debe arrancar el flujo de fallback, y quedó un flow_state en %q", st.FlowID)
	}
}

// TestMarta_LaListaSustituyeAlFallback cubre (a), (b) y (c) de un tirón, que es como
// ocurren: Marta escribe «buenas», no casa ninguna keyword y, en vez del «no te
// entendí», recibe lo que puede hacer.
func TestMarta_LaListaSustituyeAlFallback(t *testing.T) {
	ofrece := &fakeOpening{apertura: ofertaConOpciones()}
	rt, repo, sender, contacts, _ := newMartaRuntime(t, ofrece, true)

	if err := rt.HandleIncoming(context.Background(), testSession, incoming(testContact, "buenas", "wamid.marta1")); err != nil {
		t.Fatalf("buenas: %v", err)
	}

	// (a) EXACTAMENTE un saliente. El número importa tanto como el contenido: mandar la
	// lista Y el fallback sería «no te entendí» seguido de opciones, que es justo la
	// experiencia que D-043.20 quiere eliminar.
	if got := sender.count(); got != 1 {
		t.Fatalf("debe enviarse UN solo saliente, se enviaron %d: %q", got, sender.texts())
	}
	// (b) ese saliente es la oferta y NO lleva el texto del fallback (INV-20).
	enviado := sender.texts()[0]
	if strings.Contains(enviado, "no te entendí") {
		t.Fatalf("el saliente no puede llevar el texto del fallback (INV-20): %q", enviado)
	}
	if enviado != ofrece.apertura.Text {
		t.Fatalf("el saliente debe ser la oferta.\n got: %q\nwant: %q", enviado, ofrece.apertura.Text)
	}
	// (c) y el flujo del fallback no se arrancó.
	assertNoArrancoElFallback(t, repo, contacts)
	if ofrece.veces != 1 {
		t.Fatalf("la oferta se pide UNA vez por entrante, se pidió %d", ofrece.veces)
	}
}

// TestMarta_CasoVacioCaeAlFallbackDeSiempre es (d), y es LA REGRESIÓN a proteger: un
// tenant sin nada que ofrecer y un contacto sin nada que retomar no pueden quedarse
// mudos. Ahí el `fallback` sigue siendo exactamente lo que era.
func TestMarta_CasoVacioCaeAlFallbackDeSiempre(t *testing.T) {
	ofrece := &fakeOpening{apertura: events.Offering{}} // cero kinds habilitados y cero rescatables.
	rt, repo, sender, contacts, _ := newMartaRuntime(t, ofrece, true)

	if err := rt.HandleIncoming(context.Background(), testSession, incoming(testContact, "buenas", "wamid.marta2")); err != nil {
		t.Fatalf("buenas: %v", err)
	}

	if got := sender.count(); got != 1 {
		t.Fatalf("el caso vacío responde con el fallback (1 saliente), envió %d: %q", got, sender.texts())
	}
	if enviado := sender.texts()[0]; !strings.Contains(enviado, textoDelFallback) {
		t.Fatalf("con la oferta vacía, el saliente ES el texto del fallback: %q", enviado)
	}
	st := loadState(t, repo, resolveID(t, contacts, testContact))
	if st.FlowID != testFallbackFlow {
		t.Fatalf("y SÍ arranca su flujo: flow_state en %q, quería %q", st.FlowID, testFallbackFlow)
	}
}

// TestMarta_CasoVacioSinReglaDeFallback es (e): la decisión C de siempre. Sin regla de
// fallback no hay nada que decir, y la oferta vacía no inventa una conversación.
func TestMarta_CasoVacioSinReglaDeFallback(t *testing.T) {
	ofrece := &fakeOpening{apertura: events.Offering{}}
	rt, repo, sender, contacts, _ := newMartaRuntime(t, ofrece, false)

	if err := rt.HandleIncoming(context.Background(), testSession, incoming(testContact, "buenas", "wamid.marta3")); err != nil {
		t.Fatalf("buenas: %v", err)
	}

	if got := sender.count(); got != 0 {
		t.Fatalf("sin regla de fallback no se responde nada (decisión C), envió %d: %q", got, sender.texts())
	}
	if _, ok, err := repo.Load(context.Background(), store.Key{
		TenantID: testTenant, SessionID: testSession, ContactID: resolveID(t, contacts, testContact),
	}); err != nil || ok {
		t.Fatalf("tampoco se crea conversación (ok=%v err=%v)", ok, err)
	}
	if ofrece.veces != 0 {
		t.Fatalf("sin regla de fallback ni se llega a la rama: la oferta se pidió %d veces", ofrece.veces)
	}
}

// TestMarta_LaKeywordNoVeLaLista es (f): la anulación vive SOLO en la rama Fallback.
// Un texto que casa una keyword arranca el flujo del tenant como siempre y la oferta
// ni se pide.
func TestMarta_LaKeywordNoVeLaLista(t *testing.T) {
	ofrece := &fakeOpening{apertura: ofertaConOpciones()}
	keyword := trigger.Rule{
		TenantID: testTenant, Kind: trigger.KindKeyword, Keyword: "soporte",
		MatchType: trigger.MatchExact, FlowID: testFlow, Enabled: true,
	}
	rt, repo, sender, contacts, _ := newMartaRuntime(t, ofrece, true, keyword)

	if err := rt.HandleIncoming(context.Background(), testSession, incoming(testContact, "soporte", "wamid.marta4")); err != nil {
		t.Fatalf("soporte: %v", err)
	}

	if ofrece.veces != 0 {
		t.Fatalf("la rama Start no debe preguntar por la oferta; se pidió %d veces", ofrece.veces)
	}
	st := loadState(t, repo, resolveID(t, contacts, testContact))
	if st.FlowID != testFlow {
		t.Fatalf("la keyword arranca el flujo del tenant: flow_state en %q, quería %q", st.FlowID, testFlow)
	}
	if got := strings.Join(sender.texts(), "\n"); !strings.Contains(got, "1) Ventas") {
		t.Fatalf("y lo que se ve es el flujo del tenant, no una oferta: %q", got)
	}
}

// TestMarta_ElNumeroDeRetomarAbreLaLista es el encargo que faltaba: sin el case de
// ActionRescue, el «3» de la oferta caía al default de menuChoice y el cliente se
// quedaba mirando la nada. La opción existía en la lista y no respondía.
//
// Son DOS pasos y no uno: elegir «retomar» abre la lista de QUÉ retomar; el número
// siguiente ya llega como ActionResume con su EventID y ese camino ya existía.
func TestMarta_ElNumeroDeRetomarAbreLaLista(t *testing.T) {
	ofrece := &fakeOpening{
		apertura: ofertaConOpciones(),
		rescate: events.Offering{
			Text: "¿Qué quieres retomar?\n1) el pedido",
			Menu: events.Menu{Options: []events.MenuOption{
				{Number: 1, Action: events.ActionResume, EventID: "ev-1"},
			}},
		},
	}
	rt, repo, sender, contacts, _ := newMartaRuntime(t, ofrece, true)
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "buenas", "wamid.r1")); err != nil {
		t.Fatalf("buenas: %v", err)
	}
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "3", "wamid.r2")); err != nil {
		t.Fatalf("el 3: %v", err)
	}

	if ofrece.rescates != 1 {
		t.Fatalf("elegir «retomar» debe pedir la lista de rescatables UNA vez; se pidió %d", ofrece.rescates)
	}
	if got := sender.count(); got != 2 {
		t.Fatalf("debe haber respondido a los dos entrantes, hay %d salientes: %q", got, sender.texts())
	}
	if got := sender.texts()[1]; got != ofrece.rescate.Text {
		t.Fatalf("el segundo saliente es la lista de lo que se puede retomar.\n got: %q\nwant: %q", got, ofrece.rescate.Text)
	}
	// Y queda pendiente el menú del RESCATE, no el de la apertura: si no se sustituyera,
	// el «1» siguiente elegiría «hacer un pedido» en vez de retomar el que ya existe.
	st := loadState(t, repo, resolveID(t, contacts, testContact))
	raw, ok := st.Vars["dispatcher_menu"].(string)
	if !ok || !strings.Contains(raw, "ev-1") {
		t.Fatalf("debe quedar pendiente el menú del rescate (con su event_id): %+v", st.Vars)
	}
}

// TestINV17_ElDescarteGanaLaCarreraAlMenuPintado es la mitad de INV-17 que faltaba: el
// invariante dice «no se lista, NO SE RESCATA y no se menciona», y hasta aquí solo se
// cumplían las dos visibles.
//
// La carrera es real y estrecha: el menú se pinta con el pedido todavía vivo, el dueño
// lo descarta desde su bandeja mientras el cliente lee, y el número llega después. El
// evento sigue `open` —el descarte toca la SOLICITUD, y nada mata un evento por
// tiempo (D-041.18)—, así que quien tiene que decir «esto ya no se retoma» es el
// predicado de rescatables y no el estado del evento.
//
// Los asserts van sobre lo que un rescate DEJARÍA si hubiera ocurrido: el puntero
// apuntando al evento y su reloj refrescado. Comprobar solo el texto no valdría —el
// silencio se parece demasiado a otras cosas—.
func TestINV17_ElDescarteGanaLaCarreraAlMenuPintado(t *testing.T) {
	ctx := context.Background()
	ofrece := &fakeOpening{apertura: ofertaConOpciones()}
	rt, repo, sender, contacts, evs := newMartaRuntime(t, ofrece, true)
	cid := resolveID(t, contacts, testContact)
	evs.contactID = cid
	// El pedido de Marta, vivo y con su solicitud, cuando se pinta el menú.
	vivo := evs.seedAlive("cart", "intake-marta", time.Date(2026, 3, 1, 9, 30, 0, 0, time.UTC))
	ofrece.rescate = events.Offering{
		Text: "¿Qué quieres retomar?\n1) el pedido",
		Menu: events.Menu{Options: []events.MenuOption{
			{Number: 1, Action: events.ActionResume, EventID: vivo.ID},
		}},
	}

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "buenas", "wamid.inv1")); err != nil {
		t.Fatalf("buenas: %v", err)
	}
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "3", "wamid.inv2")); err != nil {
		t.Fatalf("el 3: %v", err)
	}
	enviadosAntes := sender.count()

	// AQUÍ el dueño descarta: la solicitud se abandona y el evento se queda `open`.
	evs.descartar(vivo.ID)

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "wamid.inv3")); err != nil {
		t.Fatalf("el 1: %v", err)
	}

	st := loadState(t, repo, resolveID(t, contacts, testContact))
	if st.EventID == vivo.ID {
		t.Fatalf("un pedido descartado NO se rescata (INV-17): el puntero quedó apuntándolo (%q)", st.EventID)
	}
	if got := evs.alive(); len(got) != 1 || !got[0].LastActivityAt.Equal(time.Date(2026, 3, 1, 9, 30, 0, 0, time.UTC)) {
		t.Fatalf("y tampoco se le refresca el reloj, porque no hubo rescate: %+v", got)
	}
	if got := sender.count(); got != enviadosAntes {
		t.Fatalf("no hay nada que retomar, así que no se le responde con el evento: %q", sender.texts()[enviadosAntes:])
	}
}

// TestMarta_SiNoQuedaNadaQueRetomarElTextoSigueSuCamino: el dueño descartó el último
// pedido entre que se pintó la oferta y el cliente pulsó su número. No se le deja
// mirando un silencio: el turno NO se consume y el texto sigue hasta el fallback, que
// vuelve a ofrecerle lo que sí puede hacer.
func TestMarta_SiNoQuedaNadaQueRetomarElTextoSigueSuCamino(t *testing.T) {
	ofrece := &fakeOpening{apertura: ofertaConOpciones(), rescate: events.Offering{}}
	rt, _, sender, _, _ := newMartaRuntime(t, ofrece, true)
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "buenas", "wamid.v1")); err != nil {
		t.Fatalf("buenas: %v", err)
	}
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "3", "wamid.v2")); err != nil {
		t.Fatalf("el 3: %v", err)
	}

	if ofrece.rescates != 1 {
		t.Fatalf("se preguntó %d veces por los rescatables, quería 1", ofrece.rescates)
	}
	if got := sender.count(); got != 2 {
		t.Fatalf("el cliente no puede quedarse sin respuesta: hay %d salientes: %q", got, sender.texts())
	}
	if got := sender.texts()[1]; got != ofrece.apertura.Text {
		t.Fatalf("sin nada que retomar, el texto sigue su camino y vuelve a ofrecerse la entrada.\n got: %q", got)
	}
}
