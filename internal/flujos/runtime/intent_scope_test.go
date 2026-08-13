package runtime_test

// intent_scope_test.go — Plan 043 · T5.3, D-043.9. Atraviesa desde el TURNO
// ENTRANTE (Runtime.HandleIncoming) hasta el resolver de disparos, en vez de probar
// solo trigger.ConfigResolver.Resolve en aislado (eso vive en
// internal/flujos/trigger/intent_scope_test.go).
//
// ⚠️ LÍMITE DE ALCANCE, dicho con todas sus letras (decisión de Jhoan, 2026-08-10,
// CONTRATO-OLA5.md §5.1): el ÚNICO sitio de producción donde buildSignal puede
// construir una Signal con ActiveEventKind != "" es el menú PENDIENTE
// (incoming.go, rama `st.FlowID == ""` de advanceLive) — el estado que deja el
// despachador cuando el contacto pidió la lista sin conversación abierta. Con un
// evento `cart`/`survey`/`media` VIVO Y EN CURSO (dentro de un módulo), el entrante
// va por advanceLive → liveEventSwitch → ResolveLive, y ResolveLive NO consulta
// reglas kind='llm' a propósito (INV-02): el clasificador nunca interrumpe un
// módulo. Este fichero NO reproduce ese caso —nadie puede, sin tocar INV-02— y por
// eso los tests de abajo usan el ÚNICO evento activo alcanzable por el camino real:
// el del menú (`menu`).
//
// Sirve igual de prueba: es el turno completo (HandleIncoming → advanceLive →
// buildSignal → handleTrigger → Resolve), no el resolver desnudo.
//
// ⚠️ ACTUALIZACIÓN (Plan 054 · F2b, D-A — decisión de Jhoan 2026-08-12): una regla
// llm cuyo event_kind gana el scoping ahora PARE (o conmuta) ese evento — ver
// config_resolver.go. Los dos tests de abajo que resuelven con event_kind poblado
// reflejan esa consecuencia; el que la scoping FILTRA (ReglaAjena_NoArranca) no
// cambia, porque el filtrado en sí no se tocó.

import (
	"context"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
)

// t53NewScopeRuntime arma un runtime mínimo para el tramo turno→resolver: el motor
// de menú (sampleFlow/testFlow), un EventStore en memoria (para poder releer el
// tipo del evento activo vía rt.activeEventKind, D1) y la feature llm_intent
// habilitada (gate de verdad, ADR-0022 — sin ella buildSignal ni construye la
// intención).
func t53NewScopeRuntime(t *testing.T, rules ...trigger.Rule) (*runtime.Runtime, *store.MemoryRepository, *fakeSender, *contact.MemoryResolver, *memEventStore) {
	t.Helper()
	ctx := context.Background()
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(ctx, testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar definición: %v", err)
	}
	ts := trigger.NewMemoryStore()
	for _, r := range rules {
		if _, err := ts.Insert(ctx, r); err != nil {
			t.Fatalf("insert regla: %v", err)
		}
	}
	evs := newMemEventStore(time.Now())
	sender := &fakeSender{}
	contacts := contact.NewMemoryResolver(repo)
	ents := entitlements.NewFake()
	ents.Enable(testTenant, entitlements.FeatureLLMIntent)
	rt := runtime.New(repo, newEngine(), sender, fakeResolver{tenantID: testTenant}, contacts, discardLogger(),
		runtime.WithTriggerResolver(trigger.NewConfigResolver(ts)),
		runtime.WithEventStore(evs),
		runtime.WithEntitlements(ents),
	)
	return rt, repo, sender, contacts, evs
}

// t53SeedMenuPendiente deja un flow_state SIN flujo (FlowID="") apuntando a un
// evento vivo de tipo `menu` — el estado que saveMenuState (events.go) produce
// cuando el despachador presenta su lista. Es el ÚNICO estado de producción con
// EventID != "" que alcanza la rama de buildSignal con ActiveEventKind != "" (ver
// incoming.go: `if st.FlowID == "" { activeKind := rt.activeEventKind(...) ... }`).
func t53SeedMenuPendiente(t *testing.T, repo *store.MemoryRepository, evs *memEventStore, contactID string) string {
	t.Helper()
	evs.contactID = contactID
	ev := evs.seedAlive("menu", time.Now())
	if err := repo.Save(context.Background(), model.Conversation{
		TenantID: testTenant, SessionID: testSession, ContactID: contactID,
		EventID: ev.ID,
	}); err != nil {
		t.Fatalf("sembrar menú pendiente: %v", err)
	}
	return ev.ID
}

// TestScope_TurnoAVivo_MenuPendiente_ReglaAjena_NoArranca es el tramo completo del
// criterio del plan: con la config canónica (reglas event_start CON flow_id,
// admin/triggers.go:63) el menú es el único evento activo alcanzable por producción;
// sin embargo, un event_start sin flow_id puede dejar otro tipo con FlowID vacío.
// Aquí se prueba la rama canónica: con el menú y una regla llm anotada con OTRO
// event_kind, el turno entrante NO arranca el flujo de esa intención — cae al peldaño
// de texto, que aquí no tiene nada que casar, así que el turno se ignora sin enviar nada.
func TestScope_TurnoAVivo_MenuPendiente_ReglaAjena_NoArranca(t *testing.T) {
	rt, repo, sender, contacts, evs := t53NewScopeRuntime(t,
		trigger.Rule{TenantID: testTenant, Kind: trigger.KindLLM, Keyword: "pedir_encuesta", FlowID: testFlow, EventKind: "survey", Enabled: true},
	)
	ctx := context.Background()
	contactID := resolveID(t, contacts, testContact)
	t53SeedMenuPendiente(t, repo, evs, contactID)

	m := incomingIntent(testContact, "algo que no es una keyword", "wamid.t53a", "pedir_encuesta", nil)
	if err := rt.HandleIncoming(ctx, testSession, m); err != nil {
		t.Fatalf("HandleIncoming: %v", err)
	}

	if sender.count() != 0 {
		t.Fatalf("con event_kind ajeno al del menú activo, NO debe arrancar nada; envió %d: %v", sender.count(), sender.texts())
	}
	if _, ok, lerr := repo.Load(ctx, store.Key{TenantID: testTenant, SessionID: testSession, ContactID: contactID}); lerr != nil {
		t.Fatalf("Load: %v", lerr)
	} else if ok {
		t.Fatal("el estado del menú pendiente debe soltarse igual (se trata como entrante sin conversación)")
	}
}

// TestScope_TurnoAVivo_MenuPendiente_ReglaDelMismoTipo_Conmuta es el control
// positivo del mismo tramo: la MISMA regla, anotada con el tipo del evento que SÍ
// está activo (`menu`), SÍ resuelve y el turno se CONSUME.
//
// ⚠️ Lo que este test afirmaba antes de esta tarea ya no es cierto, y a propósito
// (Plan 054 · F2b, D-A — decisión de Jhoan 2026-08-12): con la segunda puerta sin
// construir, la Decision de esta regla no llevaba EventKind y el turno arrancaba
// `testFlow` a secas, IGNORANDO que había un evento `menu` vivo. Ahora la Decision
// SÍ lleva EventKind="menu" (ver config_resolver.go), así que entra por beginEvent
// como cualquier event_start del mismo tipo — y beginEvent es idempotente POR TIPO
// (D-043.4): con un evento `menu` YA vivo, CONMUTA hacia él en vez de arrancar un
// flujo aparte. Conmutar a `menu` significa presentMenu (D-043.3: el menú no es una
// fila de flow_definitions, lo renderiza el despachador) — y este runtime mínimo NO
// cablea `runtime.WithDispatcher`, así que presentMenu se calla (mismo no-op
// documentado en events.go). Es el MISMO trato que recibiría una regla event_start
// con event_kind="menu" en las mismas condiciones — no es un trato especial para la
// puerta llm, es la CONSECUENCIA correcta de compartir beginEvent.
//
// Lo que el test SÍ puede (y debe) seguir afirmando es que el turno no se pierde ni
// duplica el evento: sigue habiendo UN solo evento vivo (el mismo ID de antes).
//
// ⚠️ Medido, no supuesto: el flow_state de partida NO sobrevive al turno, y eso NO
// es nuevo aquí — lo destruye releaseOrphanMenu (incoming.go, «el quinto camino de
// suelta», Ola 6) ANTES de volver a entrar por handleTrigger, exactamente igual con
// una regla event_start que con esta llm. Sin `runtime.WithDispatcher` cableado en
// este runtime mínimo, presentMenu no lo recrea (mismo no-op documentado en
// events.go), así que el saldo del turno es: el evento sigue vivo y correctamente
// identificado (conmutar, no duplicar), pero sin un flow_state que lo apunte. En
// producción el despachador SIEMPRE está cableado (bootstrap.go), así que ese último
// paso —presentar el menú y volver a dejar dispatcher_menu_event_id en Vars— sí
// ocurre; no se reproduce aquí para no montar un Dispatcher completo por una prueba
// de scoping.
func TestScope_TurnoAVivo_MenuPendiente_ReglaDelMismoTipo_Conmuta(t *testing.T) {
	rt, repo, sender, contacts, evs := t53NewScopeRuntime(t,
		trigger.Rule{TenantID: testTenant, Kind: trigger.KindLLM, Keyword: "pedir_menu", FlowID: testFlow, EventKind: "menu", Enabled: true},
	)
	ctx := context.Background()
	contactID := resolveID(t, contacts, testContact)
	menuEventID := t53SeedMenuPendiente(t, repo, evs, contactID)

	m := incomingIntent(testContact, "algo que no es una keyword", "wamid.t53b", "pedir_menu", nil)
	if err := rt.HandleIncoming(ctx, testSession, m); err != nil {
		t.Fatalf("HandleIncoming: %v", err)
	}

	// Sin dispatcher cableado, presentMenu se calla: NINGÚN saliente, ni testFlow ni
	// ningún otro — la degradación silenciosa está documentada, no es un fallo.
	if sender.count() != 0 {
		t.Fatalf("sin despachador cableado, conmutar al menú no debe enviar nada; envió %d: %v", sender.count(), sender.texts())
	}
	// El evento NO se duplicó ni se cerró: sigue vivo el MISMO que ya estaba
	// (conmutar, no parir) — la prueba real de que la regla del MISMO tipo casó y
	// beginEvent reconoció el evento activo en vez de intentar una segunda fila.
	alive := evs.alive()
	if len(alive) != 1 || alive[0].ID != menuEventID {
		t.Fatalf("debe seguir habiendo UN evento vivo, el mismo de antes (%q); got %+v", menuEventID, alive)
	}
	// El flow_state de partida SÍ se suelta (releaseOrphanMenu, no-regresión: ya lo
	// hacía antes de esta tarea) y sin despachador cableado nada lo recrea.
	if _, ok, err := repo.Load(ctx, store.Key{TenantID: testTenant, SessionID: testSession, ContactID: contactID}); err != nil {
		t.Fatalf("Load: %v", err)
	} else if ok {
		t.Fatal("sin despachador cableado, presentMenu no debe recrear el flow_state")
	}
}

// TestScope_TurnoAVivo_SinEventoActivo_ReglaAnotada_SigueArrancando fija que, sin
// NINGÚN estado previo (el caso normal: buildSignal con ActiveEventKind="" en los
// otros dos sitios de incoming.go), una regla llm anotada con event_kind casa
// igual — la guarda de retrocompatibilidad no depende de que la regla nunca lleve
// event_kind, depende de que no haya evento activo.
//
// ⚠️ Lo que arranca CAMBIÓ de forma (Plan 054 · F2b, D-A), aunque las dos
// aserciones originales (1 saliente, FlowID=testFlow) sigan cumpliéndose byte a
// byte: antes arrancaba testFlow a secas (Action=Start, sin evento); ahora PARE el
// evento `survey` primero (Action=StartEvent) y arranca testFlow COMO SU FLUJO
// (con eventID no vacío). Se añade la aserción que antes no hacía falta —el
// EventID no vacío— para dejar el cambio a la vista.
func TestScope_TurnoAVivo_SinEventoActivo_ReglaAnotada_SigueArrancando(t *testing.T) {
	rt, repo, sender, contacts, _ := t53NewScopeRuntime(t,
		trigger.Rule{TenantID: testTenant, Kind: trigger.KindLLM, Keyword: "pedir_encuesta", FlowID: testFlow, EventKind: "survey", Enabled: true},
	)
	ctx := context.Background()

	m := incomingIntent(testContact, "algo que no es una keyword", "wamid.t53c", "pedir_encuesta", nil)
	if err := rt.HandleIncoming(ctx, testSession, m); err != nil {
		t.Fatalf("HandleIncoming: %v", err)
	}

	if sender.count() != 1 {
		t.Fatalf("sin conversación viva no hay evento activo que acote; debe arrancar. envió %d", sender.count())
	}
	contactID := resolveID(t, contacts, testContact)
	st := loadState(t, repo, contactID)
	if st.FlowID != testFlow {
		t.Fatalf("debe haber arrancado testFlow, got %q", st.FlowID)
	}
	if st.EventID == "" {
		t.Fatalf("con event_kind poblado, el arranque debe parir su evento (Plan 054 · F2b): EventID vino vacío")
	}
}
