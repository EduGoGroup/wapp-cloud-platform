// event_telemetry_test.go — Plan 043 · T5.4 (D-043.11): los SEIS efectos de
// PLATAFORMA que la vida del evento conversacional emite hacia flow_events, y las
// TRES ramas donde el contrato (CONTRATO-OLA5.md §D2) prohíbe emitir.
//
// Los helpers de este fichero llevan el prefijo t54 (los cuatro implementadores de
// la Ola 5 comparten el paquete runtime_test) salvo cuando reutilizan uno YA
// existente del paquete (memEventStore, fakeAbandoner, eventStartRule,
// persistSinkWith, resolveID, loadState, incoming, discardLogger, newEngine): esos
// no se tocan, solo se llaman.
package runtime_test

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
)

// t54NewTelemetryRuntime arma el runtime de siempre (repo en memoria + plano de
// eventos en memoria) con el ÚNICO cable que estos tests necesitan y que los
// helpers compartidos no traen ya wireado: el PersistSink real, para que el fan-out
// llegue de verdad hasta flow_events (repo.FlowEvents()) y no se quede en un doble
// que solo capture el Effect en memoria.
func t54NewTelemetryRuntime(t *testing.T, rules ...trigger.Rule) (
	*runtime.Runtime, *store.MemoryRepository, *contact.MemoryResolver, *memEventStore, *fakeAbandoner,
) {
	t.Helper()
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar definición: %v", err)
	}
	ts := trigger.NewMemoryStore()
	for _, r := range rules {
		if _, err := ts.Insert(context.Background(), r); err != nil {
			t.Fatalf("insert regla: %v", err)
		}
	}
	contacts := contact.NewMemoryResolver(repo)
	evs := newMemEventStore(time.Date(2026, 8, 10, 9, 30, 0, 0, time.UTC))
	ab := &fakeAbandoner{}
	rt := runtime.New(repo, newEngine(), &fakeSender{}, fakeResolver{tenantID: testTenant}, contacts, discardLogger(),
		runtime.WithTriggerResolver(trigger.NewConfigResolver(ts)),
		runtime.WithEventStore(evs),
		runtime.WithEventSink(persistSinkWith(repo)),
		runtime.WithIntakeAbandoner(ab))
	return rt, repo, contacts, evs, ab
}

// t54Named filtra los flow_events del repo por Name, EN ORDEN de inserción.
func t54Named(repo *store.MemoryRepository, name string) []store.FlowEvent {
	var out []store.FlowEvent
	for _, fe := range repo.FlowEvents() {
		if fe.Name == name {
			out = append(out, fe)
		}
	}
	return out
}

// t54RequireOne exige EXACTAMENTE una fila con ese Name y la devuelve.
func t54RequireOne(t *testing.T, repo *store.MemoryRepository, name string) store.FlowEvent {
	t.Helper()
	got := t54Named(repo, name)
	if len(got) != 1 {
		t.Fatalf("esperaba EXACTAMENTE una fila %q en flow_events, hay %d: %+v", name, len(got), repo.FlowEvents())
	}
	return got[0]
}

// t54RequireNone exige CERO filas con ese Name.
func t54RequireNone(t *testing.T, repo *store.MemoryRepository, name string) {
	t.Helper()
	if got := t54Named(repo, name); len(got) != 0 {
		t.Fatalf("NO debía existir ninguna fila %q en flow_events, hay %d: %+v", name, len(got), got)
	}
}

// ---------------------------------------------------------------------------
// Los SEIS sitios de emisión (CONTRATO-OLA5.md §D2)
// ---------------------------------------------------------------------------

// TestT54_EventStarted_AlNacer (sitio 1, runtime/events.go birthEvent): el
// nacimiento de un evento NO-menú emite event_started con el history_id y el kind
// de la fila recién creada, y el FlowID/FlowVersion del EffectContext son los del
// flujo con el que nació (D-043.11 + D-043.21).
func TestT54_EventStarted_AlNacer(t *testing.T) {
	rt, repo, _, evs, _ := t54NewTelemetryRuntime(t, eventStartRule("carrito", "cart"))
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "t54-es-1")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	ev := evs.alive()[0]

	fe := t54RequireOne(t, repo, runtime.EffectEventStarted)
	if fe.Payload["history_id"] != ev.HistoryID {
		t.Fatalf("history_id = %v, quiero %q", fe.Payload["history_id"], ev.HistoryID)
	}
	if fe.Payload["kind"] != "cart" {
		t.Fatalf("kind = %v, quiero \"cart\"", fe.Payload["kind"])
	}
	if fe.FlowID != testFlow || fe.FlowVersion != 1 {
		t.Fatalf("FlowID/FlowVersion del efecto = (%q, %d), quiero (%q, 1)", fe.FlowID, fe.FlowVersion, testFlow)
	}
}

// TestT54_EventStarted_TipoMenuFlowIDVacioVersionCero (encargo §4 del brief: el caso
// raro que hay que FIJAR explícitamente): el evento `menu` nace con FlowID="" y
// FlowVersion=0 (events.go:459-461, D-043.3 — el menú no es una fila de
// flow_definitions). flow_events.flow_id es NOT NULL y "" lo satisface: NO es una
// omisión, es la verdad de ese tipo. Este test fija el valor EXPLÍCITO en vez de
// dejarlo al azar de lo que ya hacían otros tests del paquete.
func TestT54_EventStarted_TipoMenuFlowIDVacioVersionCero(t *testing.T) {
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar definición: %v", err)
	}
	ts := trigger.NewMemoryStore()
	// A propósito NO se usa eventStartRule("menu", "menu"): ese helper compartido
	// fija FlowID=testFlow para CUALQUIER kind, y el caso que este test fija es
	// justo el de un tenant real, donde una regla event_start de tipo `menu` NO
	// lleva flujo (kindSpecs no lo exige para event_start, D1 del contrato) porque
	// el menú no es una fila de flow_definitions (D-043.3).
	if _, err := ts.Insert(context.Background(), trigger.Rule{
		TenantID: testTenant, Kind: trigger.KindEventStart, Keyword: "menu",
		MatchType: trigger.MatchExact, EventKind: "menu", FlowID: "", Enabled: true,
	}); err != nil {
		t.Fatalf("insert regla: %v", err)
	}
	contacts := contact.NewMemoryResolver(repo)
	evs := newMemEventStore(time.Date(2026, 8, 10, 9, 30, 0, 0, time.UTC))
	menu := events.Menu{Options: []events.MenuOption{{Number: 1, Action: events.ActionStart, Kind: "cart"}}}
	rt := runtime.New(repo, newEngine(), &fakeSender{}, fakeResolver{tenantID: testTenant}, contacts, discardLogger(),
		runtime.WithTriggerResolver(trigger.NewConfigResolver(ts)),
		runtime.WithEventStore(evs),
		runtime.WithEventSink(persistSinkWith(repo)),
		runtime.WithDispatcher(fakeDispatcher{menu: menu}),
		runtime.WithFlowForKind(fakeFlowForKind{flow: testFlow}))
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "menu", "t54-es-menu")); err != nil {
		t.Fatalf("menu: %v", err)
	}
	men := aliveOfKind(t, evs, "menu")

	fe := t54RequireOne(t, repo, runtime.EffectEventStarted)
	if fe.Payload["kind"] != "menu" {
		t.Fatalf("kind = %v, quiero \"menu\"", fe.Payload["kind"])
	}
	if fe.Payload["history_id"] != men.HistoryID {
		t.Fatalf("history_id = %v, quiero %q", fe.Payload["history_id"], men.HistoryID)
	}
	if fe.FlowID != "" {
		t.Fatalf("el tipo `menu` nace con FlowID=\"\" (D-043.3), y llegó %q", fe.FlowID)
	}
	if fe.FlowVersion != 0 {
		t.Fatalf("el tipo `menu` nace con FlowVersion=0, y llegó %d", fe.FlowVersion)
	}
}

// TestT54_EventSwitched_AlConmutar (sitio 2, switchToEvent): el salto por tipo
// hacia un evento YA vivo emite event_switched con el history_id/kind del evento al
// que se conmuta (no del que se abandona).
func TestT54_EventSwitched_AlConmutar(t *testing.T) {
	rt, repo, contacts, evs, _ := t54NewTelemetryRuntime(t,
		eventStartRule("carrito", "cart"), eventStartRule("encuesta", "survey"))
	ctx := context.Background()
	cid := resolveID(t, contacts, testContact)

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "t54-sw-1")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	cart := aliveOfKind(t, evs, "cart")
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "encuesta", "t54-sw-2")); err != nil {
		t.Fatalf("encuesta: %v", err)
	}
	// Hasta aquí NINGÚN switch: los dos nacimientos son event_started.
	t54RequireNone(t, repo, runtime.EffectEventSwitched)

	// Volver al carrito SÍ conmuta (el carrito ya estaba vivo).
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "t54-sw-3")); err != nil {
		t.Fatalf("vuelta al carrito: %v", err)
	}
	if st := loadState(t, repo, cid); st.EventID != cart.ID {
		t.Fatalf("el puntero debe volver al carrito (%q): %q", cart.ID, st.EventID)
	}
	fe := t54RequireOne(t, repo, runtime.EffectEventSwitched)
	if fe.Payload["kind"] != "cart" || fe.Payload["history_id"] != cart.HistoryID {
		t.Fatalf("event_switched debe hablar del carrito al que se conmutó: %+v (quiero kind=cart history_id=%q)", fe.Payload, cart.HistoryID)
	}
}

// TestT54_EventDeactivated_AlEventStop (sitio 3, stopEvent): decir la palabra de
// escape apaga el puntero y emite event_deactivated con el TIPO del evento que se
// deja de atender — la misma relectura que nombra la confirmación (E-3).
func TestT54_EventDeactivated_AlEventStop(t *testing.T) {
	rules := []trigger.Rule{
		eventStartRule("carrito", "cart"),
		{TenantID: testTenant, Kind: trigger.KindEventStop, Keyword: "salir", MatchType: trigger.MatchExact, Enabled: true},
	}
	rt, repo, _, evs, _ := t54NewTelemetryRuntime(t, rules...)
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "t54-ds-1")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	cart := aliveOfKind(t, evs, "cart")
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "salir", "t54-ds-2")); err != nil {
		t.Fatalf("salir: %v", err)
	}

	fe := t54RequireOne(t, repo, runtime.EffectEventDeactivated)
	if fe.Payload["kind"] != "cart" || fe.Payload["history_id"] != cart.HistoryID {
		t.Fatalf("event_deactivated = %+v, quiero kind=cart history_id=%q", fe.Payload, cart.HistoryID)
	}
	// La fila sigue OPEN (event_stop desactiva, no mata): ningún otro efecto de
	// ciclo de vida debió dispararse.
	if got := evs.statuses()[cart.ID]; got != events.StatusOpen {
		t.Fatalf("event_stop no debe tocar el status: quedó %q", got)
	}
	t54RequireNone(t, repo, runtime.EffectEventClosed)
	t54RequireNone(t, repo, runtime.EffectEventCancelled)
}

// TestT54_EventInactivityExpired_AlVencerLaVentana (sitio 4, eventClock rama
// IsSuspended): un entrante que llega tras vencer la ventana de silencio suelta la
// conversación y emite event_inactivity_expired con el evento que venció — DESPUÉS
// de soltarla, nunca antes (si releaseForNewConversation fallara, no habría vencido
// nada).
func TestT54_EventInactivityExpired_AlVencerLaVentana(t *testing.T) {
	t0 := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar definición: %v", err)
	}
	s := store.DefaultTenantSettings(testTenant)
	s.EventInactivityTTL = time.Hour
	repo.SetTenantSettings(s)
	ts := trigger.NewMemoryStore()
	if _, err := ts.Insert(context.Background(), eventStartRule("carrito", "cart")); err != nil {
		t.Fatalf("insert regla: %v", err)
	}
	contacts := contact.NewMemoryResolver(repo)
	evs := newMemEventStore(t0)
	rt := runtime.New(repo, newEngine(), &fakeSender{}, fakeResolver{tenantID: testTenant}, contacts, discardLogger(),
		runtime.WithTriggerResolver(trigger.NewConfigResolver(ts)),
		runtime.WithEventStore(evs),
		runtime.WithEventSink(persistSinkWith(repo)))
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "t54-tt-1")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	nacido := evs.alive()[0]
	t54RequireNone(t, repo, runtime.EffectEventInactivityExpired)

	// 90 minutos de silencio contra 1h de tolerancia: el entrante suelta la
	// conversación y el reloj vence.
	evs.now = t0.Add(90 * time.Minute)
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "hola de nuevo", "t54-tt-2")); err != nil {
		t.Fatalf("tras vencer: %v", err)
	}

	fe := t54RequireOne(t, repo, runtime.EffectEventInactivityExpired)
	if fe.Payload["kind"] != "cart" || fe.Payload["history_id"] != nacido.HistoryID {
		t.Fatalf("event_inactivity_expired = %+v, quiero kind=cart history_id=%q", fe.Payload, nacido.HistoryID)
	}
	// La fila SIGUE open: vencer no toca status (E-6, no existe `expired`).
	if got := evs.statuses()[nacido.ID]; got != events.StatusOpen {
		t.Fatalf("vencer la inactividad no debe tocar el status: quedó %q", got)
	}
}

// TestT54_EventClosed_AlCerrarNatural (sitio 5, event_lifecycle.go
// closeIfFinished): completar el flujo de un evento activo lo cierra y emite
// event_closed con la fila que se releyó ANTES de la transición (aliveByID ya no la
// vería después).
func TestT54_EventClosed_AlCerrarNatural(t *testing.T) {
	rt, repo, _, evs, _ := t54NewTelemetryRuntime(t, eventStartRule("carrito", "cart"))
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "t54-ec-1")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	nacido := evs.alive()[0]
	t54RequireNone(t, repo, runtime.EffectEventClosed)

	// «1» transiciona el menú a un message sin next ⇒ el flujo TERMINA.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "t54-ec-2")); err != nil {
		t.Fatalf("completar: %v", err)
	}

	fe := t54RequireOne(t, repo, runtime.EffectEventClosed)
	if fe.Payload["kind"] != "cart" || fe.Payload["history_id"] != nacido.HistoryID {
		t.Fatalf("event_closed = %+v, quiero kind=cart history_id=%q", fe.Payload, nacido.HistoryID)
	}
	if got := evs.statuses()[nacido.ID]; got != events.StatusClosed {
		t.Fatalf("el evento debe quedar closed, quedó %q", got)
	}
}

// TestT54_EventCancelled_ViaCancelEventForTenant_SinTurno (sitio 6,
// cancelAndAbandon, PUERTA HTTP — encargo §5 del brief): CancelEventForTenant entra
// SIN turno de mensaje (lo llama el BFF de la app del dueño) y aun así emite
// event_cancelled con el contexto ENTERO sacado de la fila del evento — la prueba
// literal de que emitEventEffect no depende de nada que HandleIncoming deje puesto.
func TestT54_EventCancelled_ViaCancelEventForTenant_SinTurno(t *testing.T) {
	rt, repo, _, evs, ab := t54NewTelemetryRuntime(t, eventStartRule("carrito", "cart"))
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "t54-cx-1")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	ev := evs.alive()[0]
	t54RequireNone(t, repo, runtime.EffectEventCancelled)

	got, err := rt.CancelEventForTenant(ctx, testTenant, ev.ID)
	if err != nil {
		t.Fatalf("CancelEventForTenant: %v", err)
	}
	if got.Status != events.StatusCancelled {
		t.Fatalf("debe quedar cancelled: %+v", got)
	}
	fe := t54RequireOne(t, repo, runtime.EffectEventCancelled)
	if fe.Payload["kind"] != "cart" || fe.Payload["history_id"] != ev.HistoryID {
		t.Fatalf("event_cancelled = %+v, quiero kind=cart history_id=%q", fe.Payload, ev.HistoryID)
	}
	if fe.TenantID != testTenant {
		t.Fatalf("el TenantID del efecto debe salir de la fila del evento: %q", fe.TenantID)
	}
	if got := ab.seen(); len(got) != 1 || got[0] != ev.ID {
		t.Fatalf("el abandono sigue pidiéndose por evento: %v", got)
	}
}

// TestT54_EventCancelled_ViaE11 (sitio 6, la OTRA puerta: retireForNew/E-11): el
// cliente que empieza uno nuevo sobre un vencido suyo también cierra por
// cancelAndAbandon, y también emite event_cancelled — las DOS puertas comparten el
// mismo helper y el mismo efecto (D2: «cubre sus dos puertas de una vez»).
func TestT54_EventCancelled_ViaE11(t *testing.T) {
	quince := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar definición: %v", err)
	}
	contacts := contact.NewMemoryResolver(repo)
	evs := newMemEventStore(quince)
	ab := &fakeAbandoner{}
	rt := runtime.New(repo, newEngine(), &fakeSender{}, fakeResolver{tenantID: testTenant}, contacts, discardLogger(),
		runtime.WithEventStore(evs), runtime.WithIntakeAbandoner(ab),
		runtime.WithEventSink(persistSinkWith(repo)))
	ctx := context.Background()

	cid := resolveID(t, contacts, testContact)
	evs.contactID = cid
	viejo := evs.seedAlive("cart", time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC))

	key := store.Key{TenantID: testTenant, SessionID: testSession, ContactID: cid}
	if _, err := rt.StartNewOfKind(ctx, key, testSession, "cart", testFlow, ""); err != nil {
		t.Fatalf("StartNewOfKind: %v", err)
	}

	if got := evs.statuses()[viejo.ID]; got != events.StatusCancelled {
		t.Fatalf("el vencido debe quedar cancelled: %q", got)
	}
	fe := t54RequireOne(t, repo, runtime.EffectEventCancelled)
	if fe.Payload["kind"] != "cart" || fe.Payload["history_id"] != viejo.HistoryID {
		t.Fatalf("event_cancelled = %+v, quiero kind=cart history_id=%q", fe.Payload, viejo.HistoryID)
	}
	// Un evento vivo nuevo nació aparte y NO debe llevar event_started duplicado de más
	// de uno (uno solo, el del nuevo).
	if got := t54Named(repo, runtime.EffectEventStarted); len(got) != 1 {
		t.Fatalf("debe haber UN solo event_started (el del carrito nuevo), hay %d", len(got))
	}
}

// ---------------------------------------------------------------------------
// Anti-PII (INV-01): ningún payload de estos efectos lleva texto del cliente ni
// patrón de teléfono/JID. Precedente: crmcallback_e2e_integration_test.go:326.
// ---------------------------------------------------------------------------

// t54DigitRun detecta una tirada de 7+ dígitos seguidos — el patrón mínimo de un
// número de teléfono E.164 sin separadores. Los history_id ("cart-2026-08-10-0930")
// van con guiones entre grupos de 2/4 dígitos y NUNCA llegan a 7 seguidos, así que
// el patrón no da falsos positivos contra el propio identificador legible.
var t54DigitRun = regexp.MustCompile(`\d{7,}`)

// TestT54_AntiPII_PayloadsSinRastroDeContacto ejercita TRES de los seis sitios
// (started, deactivated, cancelled) sobre un contacto cuyo teléfono es el número de
// prueba del paquete y comprueba que NINGÚN payload de flow_events —ni ninguna otra
// columna en claro que el efecto alimente— contiene el número, un JID, ni una tirada
// de dígitos larga. El ContactID que SÍ viaja en la fila es el opaco que asigna
// contact.MemoryResolver (Plan 010 / ADR-0010), no el teléfono.
//
// Tres y no seis es SUFICIENTE aquí, y conviene decir por qué en vez de dejarlo
// implícito: los seis payloads los construye UNA sola línea de emitEventEffect
// (`{history_id, kind}` de la fila del evento), así que los seis tienen exactamente
// la misma superficie. Lo que este test acota es esa superficie, no cada sitio.
//
// ⚠️ ALCANCE REAL de la aserción (medido por mutación): el recorrido es GENÉRICO —
// mira TODAS las filas `event_*`, TODAS las claves de su payload— pero el predicado
// es de FORMA: mata el teléfono literal, una tirada de 7+ dígitos y cualquier cadena
// de más de 30 caracteres. Un texto de cliente CORTO y sin dígitos metido en el
// payload sobreviviría. La garantía dura de «cero texto de cliente» no la da este
// test sino la CONSTRUCCIÓN del payload (dos campos de vocabulario cerrado que salen
// de la fila del evento, jamás del turno).
func TestT54_AntiPII_PayloadsSinRastroDeContacto(t *testing.T) {
	rules := []trigger.Rule{
		eventStartRule("carrito", "cart"),
		{TenantID: testTenant, Kind: trigger.KindEventStop, Keyword: "salir", MatchType: trigger.MatchExact, Enabled: true},
	}
	rt, repo, contacts, evs, _ := t54NewTelemetryRuntime(t, rules...)
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "t54-pii-1")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "salir", "t54-pii-2")); err != nil {
		t.Fatalf("salir: %v", err)
	}
	ev := evs.alive()[0]
	if _, err := rt.CancelEventForTenant(ctx, testTenant, ev.ID); err != nil {
		t.Fatalf("CancelEventForTenant: %v", err)
	}
	cid := resolveID(t, contacts, testContact)

	for _, fe := range repo.FlowEvents() {
		if len(fe.Name) < 6 || fe.Name[:6] != "event_" {
			continue // fuera del alcance de T5.4 (T6 aísla con LIKE 'event_%').
		}
		if fe.ContactID == testContact {
			t.Fatalf("FUGA: fe.ContactID es el teléfono en claro, no el opaco: %+v", fe)
		}
		if fe.ContactID != cid {
			t.Fatalf("fe.ContactID = %q, quiero el opaco %q", fe.ContactID, cid)
		}
		for k, v := range fe.Payload {
			s, ok := v.(string)
			if !ok {
				continue
			}
			if s == testContact {
				t.Fatalf("FUGA: %s.%s = el teléfono en claro: %+v", fe.Name, k, fe)
			}
			if t54DigitRun.MatchString(s) {
				t.Fatalf("FUGA: %s.%s = %q tiene una tirada de 7+ dígitos (patrón de teléfono)", fe.Name, k, s)
			}
			if len(s) > 30 { // history_id nunca pasa de "survey-2026-08-10-0930" (22 chars); margen generoso
				t.Fatalf("%s.%s = %q es sospechosamente largo para history_id/kind", fe.Name, k, s)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Las TRES ramas donde NO se emite (CONTRATO-OLA5.md §D2, "🔴 Las tres ramas donde
// NO se emite"). Los tres tests de esta sección son los que se VERIFICARON por
// mutación (ver la tabla del informe): se les insertó a mano la emisión prohibida
// en events.go/event_lifecycle.go, se confirmó que el test se ponía ROJO, y se
// restauró el fichero byte a byte (md5sum idéntico) antes de dejar el trabajo por
// terminado.
// ---------------------------------------------------------------------------

// t54CarreraNacimiento envuelve memEventStore y fuerza events.ErrAliveExists en el
// PRIMER CreateEvent —simula que GetAliveByKind (la comprobación previa de
// beginEvent) dijo «no hay ninguno vivo» pero, para cuando CreateEvent corre, otro
// entrante de la misma conversación ya insertó uno—. Los CreateEvent siguientes
// delegan tal cual, para no afectar a ningún otro nacimiento del mismo test.
type t54CarreraNacimiento struct {
	*memEventStore
	fuerza bool
}

func (c *t54CarreraNacimiento) CreateEvent(ctx context.Context, in events.NewEvent) (events.Event, error) {
	if c.fuerza {
		c.fuerza = false
		return events.Event{}, events.ErrAliveExists
	}
	return c.memEventStore.CreateEvent(ctx, in)
}

// TestT54_NoEmite_CarreraDeNacimiento (RAMA PROHIBIDA 1, runtime/events.go
// birthEvent, `errors.Is(err, events.ErrAliveExists)`): perder la carrera del
// nacimiento no debe dejar NINGUNA fila event_started — el evento no nació en este
// camino, así que no hay history_id/kind que sean ciertos.
func TestT54_NoEmite_CarreraDeNacimiento(t *testing.T) {
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar definición: %v", err)
	}
	ts := trigger.NewMemoryStore()
	if _, err := ts.Insert(context.Background(), eventStartRule("carrito", "cart")); err != nil {
		t.Fatalf("insert regla: %v", err)
	}
	contacts := contact.NewMemoryResolver(repo)
	evsBase := newMemEventStore(time.Date(2026, 8, 10, 9, 30, 0, 0, time.UTC))
	carrera := &t54CarreraNacimiento{memEventStore: evsBase, fuerza: true}
	rt := runtime.New(repo, newEngine(), &fakeSender{}, fakeResolver{tenantID: testTenant}, contacts, discardLogger(),
		runtime.WithTriggerResolver(trigger.NewConfigResolver(ts)),
		runtime.WithEventStore(carrera),
		runtime.WithEventSink(persistSinkWith(repo)))
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "t54-race1")); err != nil {
		t.Fatalf("la carrera benigna NO debe abortar el turno: %v", err)
	}
	if got := evsBase.total(); got != 0 {
		t.Fatalf("perder la carrera no debe dejar NINGUNA fila de evento, hay %d", got)
	}
	t54RequireNone(t, repo, runtime.EffectEventStarted)
}

// t54CierreCarrera envuelve memEventStore y, en TransitionEvent, sella la muerte
// AJENA (cancelled) justo ANTES del intento nuestro — reproduciendo la VENTANA
// exacta que closeIfFinished necesita para ejercitar su rama prohibida: la lectura
// PREVIA (activeEvent) todavía ve la fila open (corre ANTES de este método), y
// nuestro propio TransitionEvent pierde el CAS con ErrNotOpen. Mismo patrón que
// carreraCancel (event_lifecycle_test.go, T4.2).
type t54CierreCarrera struct {
	*memEventStore
}

func (c *t54CierreCarrera) TransitionEvent(ctx context.Context, eventID string, to events.Status) error {
	if err := c.memEventStore.TransitionEvent(ctx, eventID, events.StatusCancelled); err != nil {
		return err
	}
	return events.ErrNotOpen
}

// TestT54_NoEmite_CierreCarrera (RAMA PROHIBIDA 2, event_lifecycle.go
// closeIfFinished, `errors.Is(err, events.ErrNotOpen)`): si OTRO escritor selló la
// muerte del evento (aquí, una cancelación) en la MISMA ventana en que closeIfFinished
// intenta su propia transición, NO debe emitirse event_closed — sería telemetría
// FALSA sobre un evento que en realidad quedó cancelled.
func TestT54_NoEmite_CierreCarrera(t *testing.T) {
	evsBase := newMemEventStore(time.Date(2026, 8, 10, 9, 30, 0, 0, time.UTC))
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar definición: %v", err)
	}
	ts := trigger.NewMemoryStore()
	if _, err := ts.Insert(context.Background(), eventStartRule("carrito", "cart")); err != nil {
		t.Fatalf("insert regla: %v", err)
	}
	contacts := contact.NewMemoryResolver(repo)
	carrera := &t54CierreCarrera{memEventStore: evsBase}
	rt := runtime.New(repo, newEngine(), &fakeSender{}, fakeResolver{tenantID: testTenant}, contacts, discardLogger(),
		runtime.WithTriggerResolver(trigger.NewConfigResolver(ts)),
		runtime.WithEventStore(carrera),
		runtime.WithEventSink(persistSinkWith(repo)))
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "t54-race2-1")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	ev := evsBase.alive()[0]
	t54RequireOne(t, repo, runtime.EffectEventStarted) // el nacimiento SÍ ocurrió limpio.

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "t54-race2-2")); err != nil {
		t.Fatalf("completar sobre la carrera debe ser benigno: %v", err)
	}
	if got := evsBase.statuses()[ev.ID]; got != events.StatusCancelled {
		t.Fatalf("la muerte ajena no se pisa: debe seguir cancelled, quedó %q", got)
	}
	t54RequireNone(t, repo, runtime.EffectEventClosed)
}

// TestT54_NoEmite_ReintentoIdempotente (RAMA PROHIBIDA 3, event_lifecycle.go
// repairCancelled): la segunda llamada a CancelEventForTenant sobre un evento YA
// cancelled reintenta sus efectos colaterales (T4.3, la reparación de la costura de
// fallo parcial) pero NO debe volver a despachar event_cancelled — el criterio de
// T6.2 pide LA fila, en singular. t54RequireOne exige exactamente una.
func TestT54_NoEmite_ReintentoIdempotente(t *testing.T) {
	rt, repo, _, evs, ab := t54NewTelemetryRuntime(t, eventStartRule("carrito", "cart"))
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "t54-race3-1")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	ev := evs.alive()[0]

	primera, err := rt.CancelEventForTenant(ctx, testTenant, ev.ID)
	if err != nil {
		t.Fatalf("primera cancelación: %v", err)
	}
	segunda, err := rt.CancelEventForTenant(ctx, testTenant, ev.ID)
	if err != nil {
		t.Fatalf("la segunda cancelación (reparación idempotente) no debe fallar: %v", err)
	}
	if !segunda.ClosedAt.Equal(primera.ClosedAt) {
		t.Fatalf("la fila del evento no cambia entre llamadas: %+v vs %+v", primera, segunda)
	}
	if got := ab.seen(); len(got) != 2 {
		t.Fatalf("el reintento SÍ debe re-pedir el abandono (reparación de la costura): %v", got)
	}
	// La prueba de la rama prohibida: UNA sola fila event_cancelled pese a las DOS
	// llamadas — repairCancelled reparó el abandono y el puntero, pero no dobló la
	// telemetría.
	t54RequireOne(t, repo, runtime.EffectEventCancelled)
}

// TestT54_NoEmite_StopEventQueNoPuedeReleerElEvento (sitio 3, la mitad BEST-EFFORT
// de stopEvent que el contrato exige y que ningún otro test cubría): si la relectura
// previa no devuelve la fila —el puntero apunta a un evento que el dueño mató desde
// su app entre dos entrantes— NO se emite NADA. Lo que se prohíbe no es solo la fila
// «de más»: es la fila con `{"history_id":"","kind":""}`, que ensuciaría el
// `WHERE name LIKE 'event_%'` de T6 sin informar de nada (CONTRATO-OLA5.md §D2,
// sitio 3: «🚫 Si conocido == false NO se emite»).
//
// Verificado por mutación DOBLE: la protección está por partida doble (el `if
// conocido` de stopEvent y el `if ev.ID == ""` de emitEventEffect), así que hay que
// quitar las dos a la vez para producir la fila mala — y entonces este test es el
// ÚNICO del paquete que se pone rojo.
func TestT54_NoEmite_StopEventQueNoPuedeReleerElEvento(t *testing.T) {
	rules := []trigger.Rule{
		eventStartRule("carrito", "cart"),
		{TenantID: testTenant, Kind: trigger.KindEventStop, Keyword: "salir", MatchType: trigger.MatchExact, Enabled: true},
	}
	rt, repo, contacts, evs, _ := t54NewTelemetryRuntime(t, rules...)
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "t54-stop-bf-1")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	ev := evs.alive()[0]
	// El dueño lo cancela desde su app SIN pasar por el runtime: la fila deja de estar
	// viva y aliveByID ya no la devuelve, pero flow_state.event_id sigue apuntándola.
	if err := evs.TransitionEvent(ctx, ev.ID, events.StatusCancelled); err != nil {
		t.Fatalf("matar la fila fuera del runtime: %v", err)
	}

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "salir", "t54-stop-bf-2")); err != nil {
		t.Fatalf("salir: %v", err)
	}
	// stopEvent SÍ corrió (si no, el test no probaría nada): apagó el puntero.
	cid := resolveID(t, contacts, testContact)
	if st := loadState(t, repo, cid); st.EventID != "" {
		t.Fatalf("stopEvent debe haber apagado el puntero (el test sería vacuo si no): %q", st.EventID)
	}
	t54RequireNone(t, repo, runtime.EffectEventDeactivated)
	// Y la propiedad de fondo, sobre TODAS las filas de ciclo de vida: ninguna con
	// identificador vacío.
	for _, fe := range repo.FlowEvents() {
		if len(fe.Name) < 6 || fe.Name[:6] != "event_" {
			continue
		}
		h, esCadena := fe.Payload["history_id"].(string)
		if !esCadena || h == "" {
			t.Fatalf("fila de telemetría con history_id VACÍO: %+v", fe)
		}
	}
}
