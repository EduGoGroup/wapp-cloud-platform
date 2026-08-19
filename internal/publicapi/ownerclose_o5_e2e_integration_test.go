package publicapi_test

// ownerclose_o5_e2e_integration_test.go — Plan 053 · Ola 5 · T5.1
// (✅ CIERRA REQ-053.2 e INV-053.2).
//
// EL ESCENARIO QUE MOTIVÓ EL PLAN ENTERO, contra Postgres real. El `tasks.md` del
// Plan 043 lo documentó como «consecuencia consciente»: un `cart` heredado bajo un
// `menu` activo NO CERRABA NUNCA. La causa era que `flow_state.event_id` hacía dos
// trabajos —«de quién es este estado» y «a quién le habla el contacto ahora»— y solo
// cabía una respuesta; cuando el `menu` se montaba encima, el puntero apuntaba al
// `menu` y el cierre del flujo mataba al evento equivocado (o a ninguno).
//
// Con `owner_event_id` (Ola 1) el cierre transiciona al DUEÑO y deja al ACTIVO en
// paz. Este fichero lo demuestra de punta a punta: runtime real → trigger real →
// engine + módulos REALES → despachador de menú REAL → PersistSink real →
// PostgreSQL real. Sin dobles en el camino que se mide.
//
// 🔬 CÓMO SE LLEGA AL ESTADO DIVERGENTE, y por qué el texto del turno importa.
// Con el `menu` activo, `menuChoice` (events.go) consume el entrante SI casa una
// opción de la lista — así el «2» de un carrito no se confunde con el «2» de una
// lista que ya nadie tiene delante. La lista que este montaje genera es exactamente
// «1. Hacer un pedido / 2. Retomar el pedido que dejaste a medias», así que el turno
// que tiene que llegar al flujo HEREDADO no puede ser «1» ni «2»: se usa «9», que la
// lista no ofrece y que dentro del carrito significa cancelar. Medido, no supuesto
// (ver el journal del 2026-08-19). De ahí que el desenlace sea `cancelled` y no
// `closed` — el criterio de T5.1 admite los dos, y lo que se mide es A QUIÉN se
// cierra, no con qué etiqueta.
//
// Corre contra WAPP_TEST_DB_DSN (se omite sin ella; WAPP_TEST_REQUIRE_DB la exige).

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/content"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/cart"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/menu"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/survey"
	flowruntime "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	flowstore "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

const o5Telefono = "573001119501"

// o5FlowForKind replica la resolución «tipo de evento → flujo» de producción
// (bootstrap.go, flowForKind) sobre el store en memoria: la MISMA regla event_start
// que ofrece un tipo es la que dice a qué flujo lleva. Dos fuentes distintas podrían
// ofrecer un tipo que luego no arrancara nada.
type o5FlowForKind struct{ ts *trigger.MemoryStore }

func (f o5FlowForKind) FlowForKind(ctx context.Context, tenantID, sessionID, kind string) (string, error) {
	rules, err := f.ts.ListByKind(ctx, tenantID, sessionID, trigger.KindEventStart)
	if err != nil {
		return "", err
	}
	for _, r := range rules {
		if r.Enabled && r.EventKind == kind && r.FlowID != "" {
			return r.FlowID, nil
		}
	}
	return "", nil
}

// o5Runtime arma el runtime de producción CON despachador de menú real — la
// diferencia con o6Runtime, que lo deja sin cablear. Aquí hace falta: sin
// despachador no hay evento `menu`, y sin evento `menu` no hay nada que herede el
// flow_state del carrito, que es el escenario entero.
func o5Runtime(t *testing.T, db *sql.DB, tenantID string) (*flowruntime.Runtime, *events.Store, *contact.MemoryResolver) {
	t.Helper()
	ctx := context.Background()
	reloj := &t61Reloj{ahora: time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)}

	repo := flowstore.NewPostgresRepository(db)
	if _, err := repo.InsertDefinition(ctx, tenantID, t61CartFlow()); err != nil {
		t.Fatalf("publicar el flujo del carrito: %v", err)
	}
	if err := repo.UpsertTenantContent(ctx, tenantID, "t61-catalogo", []byte(t45vCatalogo)); err != nil {
		t.Fatalf("sembrar el catálogo: %v", err)
	}

	reg := modules.NewRegistry()
	reg.Register(menu.New())
	reg.Register(survey.New())
	reg.Register(cart.New())
	eng := engine.New(reg, engine.WithContentSource(
		content.NewRouter(content.NewStatic(), content.NewJSON(repo))))

	ts := trigger.NewMemoryStore()
	for _, r := range []trigger.Rule{
		{TenantID: tenantID, Kind: trigger.KindEventStart, Keyword: "carrito",
			MatchType: trigger.MatchExact, EventKind: "cart", FlowID: t61Flujo, Enabled: true},
		// El `menu` va SIN flow_id: no es una fila de flow_definitions (D-043.3), lo
		// renderiza el despachador. Es justo lo que le permite montarse encima de un
		// flow_state ajeno sin traer flujo propio — el origen del hallazgo.
		{TenantID: tenantID, Kind: trigger.KindEventStart, Keyword: "menu",
			MatchType: trigger.MatchExact, EventKind: "menu", FlowID: "", Enabled: true},
		{TenantID: tenantID, Kind: trigger.KindFallback, FlowID: t61Flujo, Enabled: true},
	} {
		if _, err := ts.Insert(ctx, r); err != nil {
			t.Fatalf("insert regla: %v", err)
		}
	}

	feats := entitlements.NewFake()
	for _, f := range events.KindFeatures() {
		feats.Enable(tenantID, f)
	}
	eventStore := events.NewStore(db, nil, events.WithClock(reloj.fn()))
	intakeStore := intakes.NewPostgres(db)
	contacts := contact.NewMemoryResolver(nil)
	disp := events.NewDispatcher(eventStore, events.NewTriggerKindOffer(ts), feats)

	rt := flowruntime.New(repo, eng, &t61Sender{}, flowruntime.NewPostgresTenantResolver(db),
		contacts, e2eLogger(),
		flowruntime.WithEventSink(flowruntime.NewPersistSink(repo,
			cart.NewProjector(repo, intakeStore, intakeStore, intakes.NewPostgresBuyerData(db, nil)),
			survey.NewProjector(repo)).WithDecisionThread(eventStore)),
		flowruntime.WithResumePolicy(cart.NodeTypeCart, cart.NewResumePolicy(repo)),
		flowruntime.WithTriggerResolver(trigger.NewConfigResolver(ts)),
		flowruntime.WithEventStore(eventStore),
		flowruntime.WithIntakeAbandoner(intakes.NewService(intakeStore)),
		flowruntime.WithSummarySources(flowruntime.NewSummarySources(repo)),
		flowruntime.WithEntitlements(feats),
		flowruntime.WithClock(reloj.fn()),
		flowruntime.WithDispatcher(disp),
		flowruntime.WithFlowForKind(o5FlowForKind{ts}),
		flowruntime.WithOpeningBuilder(disp),
	)
	return rt, eventStore, contacts
}

// o5Punteros lee los DOS punteros del flow_state directamente de Postgres. Se leen
// de la BD y no de un helper en memoria por la misma razón que o6FlowState: la
// medida tiene que ser lo que Postgres guardó, no lo que el runtime creía.
func o5Punteros(ctx context.Context, t *testing.T, db *sql.DB, tenantID, sessionID, contactID string) (flowID, currentNode, activo, dueno string) {
	t.Helper()
	var f, n, a, d sql.NullString
	err := db.QueryRowContext(ctx, `
		SELECT flow_id, current_node, event_id::text, owner_event_id::text
		  FROM public.flow_state
		 WHERE tenant_id = $1::uuid AND session_id = $2 AND contact_id = $3::uuid`,
		tenantID, sessionID, contactID).Scan(&f, &n, &a, &d)
	if err != nil {
		t.Fatalf("leyendo los punteros del flow_state: %v", err)
	}
	return f.String, n.String, a.String, d.String
}

// o5Status lee el status EN BD de un evento, acotado al tenant.
func o5Status(ctx context.Context, t *testing.T, db *sql.DB, tenantID, eventID string) events.Status {
	t.Helper()
	var s string
	if err := db.QueryRowContext(ctx,
		`SELECT status FROM public.conversation_events WHERE id = $1::uuid AND tenant_id = $2::uuid`,
		eventID, tenantID).Scan(&s); err != nil {
		t.Fatalf("leyendo el status del evento %s: %v", eventID, err)
	}
	return events.Status(s)
}

func o5Contiene(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// TestE2E_CartHeredadoBajoMenu_CierraElDuenoNoElActivo es el criterio de T5.1.
//
// Guion: se arma un carrito a medias, se pide el `menu` encima —que HEREDA el
// flow_state del carrito sin traer flujo propio— y se manda un texto que la lista
// del menú no ofrece, de modo que llega al flujo heredado y lo lleva a su nodo
// terminal. Lo que se mide es a QUIÉN cierra ese final.
func TestE2E_CartHeredadoBajoMenu_CierraElDuenoNoElActivo(t *testing.T) {
	db := e2eOpenDB(t)
	tenantID, sessionID := t61Seed(t, db)
	rt, evs, contacts := o5Runtime(t, db, tenantID)
	ctx := context.Background()
	cid := o6Contacto(ctx, t, contacts, tenantID, o5Telefono)

	// ── (a) Un carrito a medias: Bebidas → Café → cantidad 2. ─────────────────────
	o6Entrante(ctx, t, rt, sessionID, o5Telefono, "carrito", "o5-w0")
	for i, in := range []string{"1", "1", "2", "2"} {
		o6Entrante(ctx, t, rt, sessionID, o5Telefono, in, "o5-w"+string(rune('1'+i)))
	}
	vivos, err := evs.ListAlive(ctx, tenantID, sessionID, cid)
	if err != nil {
		t.Fatalf("listar vivos: %v", err)
	}
	if len(vivos) != 1 || vivos[0].Kind != "cart" {
		t.Fatalf("precondición (a): debía haber UN evento `cart` vivo; hay %d: %+v", len(vivos), vivos)
	}
	cartID := vivos[0].ID

	// ── (b) El `menu` se monta ENCIMA. Aquí nace la divergencia. ──────────────────
	o6Entrante(ctx, t, rt, sessionID, o5Telefono, "menu", "o5-w5")

	var menuID string
	for _, ev := range o5Vivos(ctx, t, evs, tenantID, sessionID, cid) {
		if ev.Kind == "menu" {
			menuID = ev.ID
		}
	}
	if menuID == "" {
		t.Fatal("precondición (b): debía nacer un evento `menu`")
	}
	flowID, nodo, activo, dueno := o5Punteros(ctx, t, db, tenantID, sessionID, cid)
	if activo != menuID || dueno != cartID {
		t.Fatalf("precondición (b) — EL ESTADO DIVERGENTE, que es el plan entero:\n"+
			"  quiero activo=%s (el `menu`) y dueño=%s (el `cart`)\n"+
			"  tengo activo=%s dueño=%s\n"+
			"Si el dueño viene vacío, la Ola 1 no está estampando owner_event_id y el resto del test no mide nada.",
			menuID, cartID, activo, dueno)
	}
	if flowID != t61Flujo || nodo == model.NodeTerminal {
		t.Fatalf("precondición (b): el flow_state debe seguir siendo el del CARRITO y estar vivo; flow=%q nodo=%q", flowID, nodo)
	}

	// ── (c) El turno que termina el flujo HEREDADO. ───────────────────────────────
	// «9» no está en la lista del menú («1. Hacer un pedido / 2. Retomar…»), así que
	// menuChoice no lo consume y llega al carrito, donde cancela el pedido y lleva el
	// flujo a su nodo terminal.
	o6Entrante(ctx, t, rt, sessionID, o5Telefono, "9", "o5-w6")

	// ── LO QUE SE MIDE ────────────────────────────────────────────────────────────
	// Cada medida vive en su propia función NOMBRADA, y no inline: gocyclo (min 15,
	// run.tests true) suma cada `if` de aserción a la función que lo contiene, y las
	// cinco juntas daban 27. Extraerlas también las hace legibles de una en una.
	o5MideLosStatus(ctx, t, db, tenantID, cartID, menuID)
	o5MideLaBandeja(ctx, t, db, evs, tenantID, sessionID, cid, cartID, menuID)
	o5MideLaTelemetria(t, db, tenantID, cartID, menuID)
	o5MidePunteros(ctx, t, db, tenantID, sessionID, cid, menuID)
	o5MideCancelIdempotente(ctx, t, rt, db, tenantID, cartID, menuID)
}

// o5MideLosStatus — medida 1: el DUEÑO muere, el ACTIVO vive.
func o5MideLosStatus(ctx context.Context, t *testing.T, db *sql.DB, tenantID, cartID, menuID string) {
	t.Helper()
	if got := o5Status(ctx, t, db, tenantID, cartID); got == events.StatusOpen {
		t.Fatalf("REQ-053.2: el `cart` DUEÑO debe cerrarse al terminar SU flujo; sigue %q.\n"+
			"Es la patología exacta que el Plan 043 documentó como «consecuencia consciente»: el carrito heredado no cerraba nunca.", got)
	}
	if got := o5Status(ctx, t, db, tenantID, menuID); got != events.StatusOpen {
		t.Fatalf("NO-REGRESIÓN: el `menu` ACTIVO no lo cierra nadie aquí — su flujo no ha terminado, es el del carrito el que terminó; quedó %q.\n"+
			"Cerrar el activo es el error de destinatario que este plan corrige.", got)
	}
}

// o5MideLaBandeja — medida 2: el `cart` sale de la lista de abiertos y el `menu`
// sigue en ella. Es lo que un dueño ve en GET /api/v1/conversation-events?status=open,
// que lee exactamente de aquí.
func o5MideLaBandeja(ctx context.Context, t *testing.T, db *sql.DB, evs *events.Store,
	tenantID, sessionID, cid, cartID, menuID string,
) {
	t.Helper()
	_ = db
	abiertos := o5Vivos(ctx, t, evs, tenantID, sessionID, cid)
	for _, ev := range abiertos {
		if ev.ID == cartID {
			t.Fatalf("REQ-053.2: el `cart` cerrado NO debe seguir en la lista de abiertos; sigue ahí: %+v.\n"+
				"Ésta es la fila fantasma que ensucia la bandeja del dueño.", ev)
		}
	}
	if len(abiertos) != 1 || abiertos[0].ID != menuID {
		t.Fatalf("la lista de abiertos debe quedar con EXACTAMENTE el `menu` (%s); quedó %+v", menuID, abiertos)
	}
}

// o5MideLaTelemetria — medida 3: el efecto terminal cuelga del CART, no del MENÚ.
// Antes de este plan la guarda de posesión cortaba antes y no se emitía ninguno.
//
// La bitácora se lee con el helper YA EXISTENTE del paquete (t61Bitacora): une
// flow_events con conversation_events por `payload->>'history_id'`, porque
// flow_events NO tiene columna event_id — el vínculo con el evento vive dentro del
// payload. Medido contra el esquema real, no supuesto.
func o5MideLaTelemetria(t *testing.T, db *sql.DB, tenantID, cartID, menuID string) {
	t.Helper()
	delCart := t61Bitacora(t, db, tenantID, cartID)
	if !o5Contiene(delCart, flowruntime.EffectEventCancelled) && !o5Contiene(delCart, flowruntime.EffectEventClosed) {
		t.Fatalf("REQ-053.2: el `cart` debe dejar su efecto terminal en flow_events; sus efectos son %v.\n"+
			"Sin él, el evento muere en BD sin rastro y GET /api/v1/events/telemetry no lo ve.", delCart)
	}
	delMenu := t61Bitacora(t, db, tenantID, menuID)
	if o5Contiene(delMenu, flowruntime.EffectEventCancelled) || o5Contiene(delMenu, flowruntime.EffectEventClosed) {
		t.Fatalf("NO-REGRESIÓN: ningún efecto terminal debe colgar del `menu`, que sigue vivo; los suyos son %v", delMenu)
	}
}

// o5MidePunteros — medida 4: el dueño se apaga; el activo NO, porque no coincidían
// (closeIfFinished solo apaga st.EventID cuando eraElMismo).
func o5MidePunteros(ctx context.Context, t *testing.T, db *sql.DB, tenantID, sessionID, cid, menuID string) {
	t.Helper()
	_, _, activoPost, duenoPost := o5Punteros(ctx, t, db, tenantID, sessionID, cid)
	if duenoPost != "" {
		t.Fatalf("INV-053.2: tras cerrar al dueño, owner_event_id debe quedar vacío; quedó %q", duenoPost)
	}
	if activoPost != menuID {
		t.Fatalf("INV-053.2: el puntero ACTIVO debe conservarse (el `menu` sigue vivo y hablándole al contacto); quedó %q, quiero %s.\n"+
			"Apagarlo aquí desactivaría un evento vivo por un cierre que no fue suyo.", activoPost, menuID)
	}
}

// o5MideCancelIdempotente — medida 5: cancelar por API un evento YA terminal no
// revienta ni lo resucita. Es la tercera superficie que enumera REQ-053.2.
func o5MideCancelIdempotente(ctx context.Context, t *testing.T, rt *flowruntime.Runtime, db *sql.DB,
	tenantID, cartID, menuID string,
) {
	t.Helper()
	ev, cerr := rt.CancelEventForTenant(ctx, tenantID, cartID)
	if cerr != nil {
		t.Fatalf("REQ-053.2: cancelar un evento ya terminal debe ser idempotente, no un error; dio: %v", cerr)
	}
	if ev.Status == events.StatusOpen {
		t.Fatalf("tras la cancelación idempotente el evento no puede volver a `open`; quedó %q", ev.Status)
	}
	if got := o5Status(ctx, t, db, tenantID, menuID); got != events.StatusOpen {
		t.Fatalf("cancelar el `cart` por API no puede tocar al `menu`; quedó %q", got)
	}
}

// o5Vivos lista los eventos vivos y falla el test si la consulta falla: los
// llamantes de arriba solo quieren la lista. El ctx va PRIMERO (revive,
// context-as-argument), como en todo el paquete.
func o5Vivos(ctx context.Context, t *testing.T, evs *events.Store, tenantID, sessionID, contactID string) []events.Event {
	t.Helper()
	vivos, err := evs.ListAlive(ctx, tenantID, sessionID, contactID)
	if err != nil {
		t.Fatalf("listar eventos vivos: %v", err)
	}
	return vivos
}
