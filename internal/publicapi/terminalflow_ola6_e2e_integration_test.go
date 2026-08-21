// terminalflow_ola6_e2e_integration_test.go — enrutado de un flow_state TERMINAL
// (Plan 043 · Ola 6, decisión de Jhoan 2026-08-11): efecto colateral MEDIDO del
// hallazgo #24. Confirmar un pedido fija CurrentNode al centinela
// (model.NodeTerminal); el camino vivo (advanceLive) no trataba eso como
// «ninguna conversación en curso» y el contacto quedaba MUDO PARA SIEMPRE —
// conversationExpired no lo vence (TTL default deshabilitado, <=0) y
// engine.Step sobre un estado ya Finished() ignora la entrada sin emitir nada.
//
// saveMenuState (events.go) ya trataba un flow_state terminal como «ninguna
// conversación abierta» desde la Ola 4 (`!ok || st.Finished()`); esta ola alinea
// el camino vivo con ese mismo criterio. El alcance es DELIBERADO y AMPLIO: no
// solo el carrito — TODO flujo terminal (menú, encuesta) entra por el mismo
// camino, y este fichero mide los tres.
//
// Reusa el andamiaje de conversationchain_w45_e2e_integration_test.go (mismo
// paquete, t45vSeed/t45vCatalogo/e2eLogger/conTodosLosTipos): runtime real →
// trigger real → engine + módulos REALES → PersistSink real → PostgreSQL real.
//
// Corre contra WAPP_TEST_DB_DSN (se omite sin ella; WAPP_TEST_REQUIRE_DB la
// exige). Datos con prefijo o6-, limpiados por o6Seed (mismo patrón que t45vSeed).
package publicapi_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

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

const (
	o6CartFlujo   = "o6-flujo-carrito"
	o6SurveyFlujo = "o6-flujo-encuesta"
	o6MenuFlujo   = "o6-flujo-menu"

	o6TelControl = "573001117901" // NUNCA toca ningún flujo: la MEDIDA de referencia.
	o6TelCart    = "573001117902"
	o6TelSurvey  = "573001117903"
	o6TelMenu    = "573001117904"
	o6TelEnCurso = "573001117905" // carrito EN CURSO, nunca confirmado (E-1/E-7).
)

// o6CartFlow: UN nodo `cart` sobre el catálogo de tenant_content (misma forma
// que t45vFlow/t61CartFlow, otro tenant).
func o6CartFlow() model.Flow {
	return model.Flow{
		FlowID:  o6CartFlujo,
		Initial: "cart",
		Nodes: map[string]model.Node{
			"cart": {Type: "cart", Content: &model.ContentRef{Source: "json", Ref: "o6-catalogo"}},
		},
	}
}

// o6SurveyFlow: dos preguntas encadenadas que terminan en un nodo message SIN
// next (hoja → NodeTerminal), igual que t61SurveyFlow.
func o6SurveyFlow() model.Flow {
	return model.Flow{
		FlowID:  o6SurveyFlujo,
		Initial: "q1",
		Nodes: map[string]model.Node{
			"q1": {
				Type: model.NodeTypeSurveyQuestion, Prompt: "¿Nos recomendarías?\n1) Sí\n2) No",
				QuestionID: "q1", Options: map[string]string{"1": "q2", "2": "q2"},
			},
			"q2": {
				Type: model.NodeTypeSurveyQuestion, Prompt: "¿Qué tal la atención?\n1) Buena\n2) Mala",
				QuestionID: "q2", Options: map[string]string{"1": "gracias", "2": "gracias"},
			},
			"gracias": {Type: model.NodeTypeMessage, Text: "¡Gracias por tu respuesta!"},
		},
	}
}

// o6MenuFlow: un `menu` clásico (root → hoja message SIN next), el caso REALISTA
// que H2 (event_lifecycle_test.go) también usa: sampleFlow, aquí con otro nombre
// porque vive en el paquete publicapi_test.
func o6MenuFlow() model.Flow {
	return model.Flow{
		FlowID:  o6MenuFlujo,
		Initial: "root",
		Nodes: map[string]model.Node{
			"root": {
				Type:    model.NodeTypeMenu,
				Prompt:  "Hola 👋\n1) Ventas\n2) Soporte",
				Options: map[string]string{"1": "ventas", "2": "soporte"},
			},
			"ventas":  {Type: model.NodeTypeMessage, Text: "Te paso con Ventas."},
			"soporte": {Type: model.NodeTypeMessage, Text: "Cuéntame tu problema."},
		},
	}
}

// o6Sender es el doble PERMITIDO (transporte WhatsApp/Edge): guarda el
// HISTORIAL completo de textos salientes — la medida «antes/después» de este
// fichero necesita comparar EL TEXTO del contacto de control contra el del
// contacto que acaba de confirmar, no solo contar envíos.
type o6Sender struct{ msgs []string }

func (s *o6Sender) SendText(_ context.Context, _, _, text string) (*cloudlinkv1.Ack, error) {
	s.msgs = append(s.msgs, text)
	return &cloudlinkv1.Ack{AckedCommandId: fmt.Sprintf("o6-cmd-%d", len(s.msgs)), Ok: true}, nil
}

func (s *o6Sender) SendMedia(_ context.Context, _, _, _, _, _, _, _ string) (*cloudlinkv1.Ack, error) {
	s.msgs = append(s.msgs, "[media]")
	return &cloudlinkv1.Ack{AckedCommandId: fmt.Sprintf("o6-cmd-%d", len(s.msgs)), Ok: true}, nil
}

func (s *o6Sender) desde(i int) []string { return append([]string(nil), s.msgs[i:]...) }
func (s *o6Sender) total() int           { return len(s.msgs) }

// o6Seed siembra el tenant y su sesión de fleet (mismo patrón que t45vSeed).
func o6Seed(t *testing.T, db *sql.DB) (tenantID, sessionID string) {
	t.Helper()
	ctx := context.Background()
	sessionID = fmt.Sprintf("o6-sess-%d", time.Now().UnixNano())

	if err := db.QueryRowContext(ctx,
		`INSERT INTO public.tenants (slug, display_name) VALUES ($1, $2) RETURNING id::text`,
		"o6-terminal-"+sessionID, "Ola 6 — enrutado de flow_state terminal").Scan(&tenantID); err != nil {
		t.Fatalf("sembrando tenant: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		for _, q := range []string{
			`DELETE FROM public.intake_items
			  WHERE intake_id IN (SELECT id FROM public.intakes WHERE tenant_id = $1)`,
			`DELETE FROM public.intakes WHERE tenant_id = $1`,
			`DELETE FROM public.survey_results WHERE tenant_id = $1`,
			`DELETE FROM public.flow_events WHERE tenant_id = $1`,
			`DELETE FROM public.tenant_content WHERE tenant_id = $1`,
			`DELETE FROM public.tenants WHERE id = $1::uuid`,
		} {
			if _, err := db.ExecContext(ctx, q, tenantID); err != nil {
				t.Logf("limpiando (%s): %v", q, err)
			}
		}
	})
	if _, err := db.ExecContext(ctx, `
		-- profile EXPLÍCITO (Plan 046 · T1.1): el DEFAULT de la 0063 es pasivo y el
		-- runtime ya decide por esa columna, así que sin este 'active' el e2e no
		-- llegaría al motor reactivo. Es sembrado, no aserción.
		-- Y va AQUÍ y no vía SetProfile (T3.1): SetProfile es el único UPDATE que
		-- escribe profile_updated_at (0065) y movería la versión de filtros — el
		-- sembrado no debe ejercitar rutas de escritura que este e2e no afirma.
		INSERT INTO public.fleet_sessions (tenant_id, edge_id, session_id, state, profile)
		VALUES ($1::uuid, 'o6-edge', $2, 'online', 'active')
	`, tenantID, sessionID); err != nil {
		t.Fatalf("sembrando fleet_sessions: %v", err)
	}
	return tenantID, sessionID
}

// o6Runtime arma el runtime de PRODUCCIÓN: engine con cart/survey/menu reales,
// PersistSink real, plano de eventos real, resolver de disparos real con un
// event_start por tipo MÁS un fallback SIN keyword — sin OpeningBuilder cableado
// (rt.opening == nil): openWithOffer cae directo a startPlainFlow, sin el rodeo
// del automensaje de rescate (eso lo mide otra suite, incident054_t4). Es justo
// lo que la medida «antes/después» del hallazgo necesita: el MISMO texto para el
// contacto de control y para el que acaba de confirmar.
//
// ⚠️ El fallback apunta a o6MenuFlujo (NO durable, menu.Module.
// ProducesDurableContent()==false), NO a o6CartFlujo (Plan 054 · D-054.5/T2.3,
// A1 del arreglo de la regresión). Antes de la guarda apuntaba al carrito: con
// la guarda puesta y rt.opening==nil a propósito (arriba), startLocked rechaza
// CUALQUIER arranque sin evento de un flujo con contenido durable —el fallback
// jamás trae uno— y sin oferta a la que degradar (MD-054.2) el contacto se
// queda mudo. Esa combinación es exactamente la que este plan dejó de permitir
// configurar en producción (D-054.5 en runtime; D-054.8/T2.7 en el CRUD), así
// que el fixture pasó a describir un mundo que ya no existe y el test se ponía
// en rojo por el motivo CORRECTO. Apuntar el fallback a un flujo NO durable
// preserva la intención original —el fallback arranca su flujo de verdad, sin
// rodeo, y CONTROL/terminal reciben el MISMO texto— sin resucitar una
// configuración que D-054.5 rechaza.
func o6Runtime(t *testing.T, db *sql.DB, tenantID string, feats *entitlements.Fake) (
	*flowruntime.Runtime, *events.Store, *contact.MemoryResolver, *o6Sender,
) {
	t.Helper()
	ctx := context.Background()

	repo := flowstore.NewPostgresRepository(db)
	for _, f := range []model.Flow{o6CartFlow(), o6SurveyFlow(), o6MenuFlow()} {
		if _, err := repo.InsertDefinition(ctx, tenantID, f); err != nil {
			t.Fatalf("publicar flujo %s: %v", f.FlowID, err)
		}
	}
	if err := repo.UpsertTenantContent(ctx, tenantID, "o6-catalogo", []byte(t45vCatalogo)); err != nil {
		t.Fatalf("sembrar catálogo: %v", err)
	}

	reg := modules.NewRegistry()
	reg.Register(menu.New())
	reg.Register(survey.New())
	reg.Register(cart.New())
	eng := engine.New(reg, engine.WithContentSource(
		content.NewRouter(content.NewStatic(), content.NewJSON(repo))))

	ts := trigger.NewMemoryStore()
	reglas := []trigger.Rule{
		{TenantID: tenantID, Kind: trigger.KindEventStart, Keyword: "carrito",
			MatchType: trigger.MatchExact, EventKind: "cart", FlowID: o6CartFlujo, Enabled: true},
		{TenantID: tenantID, Kind: trigger.KindEventStart, Keyword: "encuesta",
			MatchType: trigger.MatchExact, EventKind: "survey", FlowID: o6SurveyFlujo, Enabled: true},
		// EventKind NO puede ser "menu": ese nombre está RESERVADO al despachador
		// integrado (D-043.3, trigger.EventKindMenu) — enterEventFlow lo especial-casa
		// e IGNORA cualquier FlowID (events.go: `if kind == trigger.EventKindMenu {
		// return rt.presentMenu(...) }`, sin llegar siquiera a mirar flowID). o6MenuFlow
		// es un flujo NORMAL cuyo NODO es de tipo model.NodeTypeMenu (una cosa
		// completamente distinta del EventKind del despachador), así que su regla usa
		// un EventKind libre: "catalogo".
		{TenantID: tenantID, Kind: trigger.KindEventStart, Keyword: "catalogo",
			MatchType: trigger.MatchExact, EventKind: "catalogo", FlowID: o6MenuFlujo, Enabled: true},
		// FlowID=o6MenuFlujo (NO durable): ver la nota de A1 en el docstring de
		// o6Runtime — un fallback SIN evento hacia un flujo durable ya no lo permite
		// D-054.5, así que este fixture apunta al menú, no al carrito.
		{TenantID: tenantID, Kind: trigger.KindFallback, FlowID: o6MenuFlujo, Enabled: true},
	}
	for _, r := range reglas {
		if _, err := ts.Insert(ctx, r); err != nil {
			t.Fatalf("insert regla: %v", err)
		}
	}

	eventStore := events.NewStore(db, nil)
	intakeStore := intakes.NewPostgres(db)
	contacts := contact.NewMemoryResolver(nil)
	sender := &o6Sender{}

	rt := flowruntime.New(repo, eng, sender, flowruntime.NewPostgresTenantResolver(db),
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
	)
	return rt, eventStore, contacts, sender
}

// o6Entrante entrega un mensaje del cliente al runtime.
func o6Entrante(ctx context.Context, t *testing.T, rt *flowruntime.Runtime, sessionID, from, text, waID string) {
	t.Helper()
	if err := rt.HandleIncoming(ctx, sessionID, &cloudlinkv1.IncomingMessage{
		From: from, Text: text, WaMessageId: waID,
	}); err != nil {
		t.Fatalf("HandleIncoming(%q, %s): %v", text, waID, err)
	}
}

// o6FlowState lee current_node/event_id EN BD directamente (no del store en
// memoria del test): la medida tiene que ser lo que Postgres guardó, no lo que
// un helper dedujo.
func o6FlowState(t *testing.T, db *sql.DB, tenantID, sessionID, contactID string) (currentNode string, eventID sql.NullString, ok bool) {
	t.Helper()
	err := db.QueryRowContext(context.Background(), `
		SELECT current_node, event_id FROM public.flow_state
		WHERE tenant_id = $1::uuid AND session_id = $2 AND contact_id = $3::uuid
	`, tenantID, sessionID, contactID).Scan(&currentNode, &eventID)
	switch {
	case err == sql.ErrNoRows:
		return "", sql.NullString{}, false
	case err != nil:
		t.Fatalf("leyendo flow_state: %v", err)
	}
	return currentNode, eventID, true
}

// o6Contacto resuelve el contact_id OPACO del teléfono dado.
func o6Contacto(ctx context.Context, t *testing.T, contacts *contact.MemoryResolver, tenantID, telefono string) string {
	t.Helper()
	ref, err := contact.NewRef(contact.KindPhoneE164, telefono)
	if err != nil {
		t.Fatalf("NewRef(%s): %v", telefono, err)
	}
	cid, err := contacts.Resolve(ctx, tenantID, []contact.Ref{ref}, "")
	if err != nil {
		t.Fatalf("resolver contacto %s: %v", telefono, err)
	}
	return cid
}

// o6ArmaYConfirmaCarrito recorre «carrito» → 1→1→2→2 (Café ×2) → 2 (Finalizar) →
// 1 (Confirmar): el pedido queda CONFIRMADO (LevelClosed) y, con el arreglo del
// hallazgo #24, el flujo llega al centinela y closeIfFinished cierra el evento
// en el MISMO turno.
func o6ArmaYConfirmaCarrito(ctx context.Context, t *testing.T, rt *flowruntime.Runtime, sessionID, telefono, base string) {
	t.Helper()
	o6Entrante(ctx, t, rt, sessionID, telefono, "carrito", base+"-w0")
	for i, in := range []string{"1", "1", "2", "2", "2", "1"} {
		o6Entrante(ctx, t, rt, sessionID, telefono, in, fmt.Sprintf("%s-w%d", base, i+1))
	}
}

// o6TerminaEncuesta recorre «encuesta» → 1 (q1) → 1 (q2): las dos preguntas
// respondidas dejan la encuesta en su hoja message (centinela).
func o6TerminaEncuesta(ctx context.Context, t *testing.T, rt *flowruntime.Runtime, sessionID, telefono, base string) {
	t.Helper()
	o6Entrante(ctx, t, rt, sessionID, telefono, "encuesta", base+"-w0")
	o6Entrante(ctx, t, rt, sessionID, telefono, "1", base+"-w1")
	o6Entrante(ctx, t, rt, sessionID, telefono, "1", base+"-w2")
}

// o6TerminaMenu recorre «catalogo» → 1 (Ventas): la hoja message SIN next deja
// el menú en el centinela.
func o6TerminaMenu(ctx context.Context, t *testing.T, rt *flowruntime.Runtime, sessionID, telefono, base string) {
	t.Helper()
	o6Entrante(ctx, t, rt, sessionID, telefono, "catalogo", base+"-w0")
	o6Entrante(ctx, t, rt, sessionID, telefono, "1", base+"-w1")
}

// o6Fixture agrupa el andamiaje COMÚN a los cinco escenarios de la medida, para
// que TestE2E_Ola6_FlowStateTerminalNoDejaMudo se quede en pura orquestación
// (gocyclo): cada escenario vive en su propia función "o6Quiere*".
type o6Fixture struct {
	t         *testing.T
	ctx       context.Context
	db        *sql.DB
	tenantID  string
	sessionID string
	rt        *flowruntime.Runtime
	evs       *events.Store
	contacts  *contact.MemoryResolver
	sender    *o6Sender
}

// o6QuiereTerminal fija la precondición COMÚN de (a)/(c)/(d): tras terminar su
// flujo, el flow_state del contacto queda en el centinela CON EL PUNTERO YA
// APAGADO (closeIfFinished lo cerró en el mismo turno, T4.1) — la guarda exacta
// que separa el arreglo del reintento de E-8 §4.
func (f o6Fixture) o6QuiereTerminal(cid, etiqueta string) {
	f.t.Helper()
	nodo, ev, ok := o6FlowState(f.t, f.db, f.tenantID, f.sessionID, cid)
	if !ok || nodo != model.NodeTerminal {
		f.t.Fatalf("precondición %s: flow_state debe quedar TERMINAL; current_node=%q ok=%v", etiqueta, nodo, ok)
	}
	if ev.Valid && ev.String != "" {
		f.t.Fatalf("precondición %s: el puntero debe estar apagado tras el cierre natural; event_id=%q", etiqueta, ev.String)
	}
}

// o6QuiereMismaRespuestaQueControl entrega un texto cualquiera y exige EXACTAMENTE
// la misma respuesta que recibió el CONTROL — la medida central del hallazgo:
// antes de esta tarea, aquí había SILENCIO.
func (f o6Fixture) o6QuiereMismaRespuestaQueControl(telefono, waID, control, etiqueta string) {
	f.t.Helper()
	antes := f.sender.total()
	o6Entrante(f.ctx, f.t, f.rt, f.sessionID, telefono, "gracias!", waID)
	turno := f.sender.desde(antes)
	if len(turno) != 1 || turno[0] != control {
		f.t.Fatalf("%s tras terminar: quiero la misma respuesta que el CONTROL (%q), llegó %v", etiqueta, control, turno)
	}
}

// o6QuiereCartTerminalYPedidoNuevo fija (a) y (b) para el contacto del carrito:
// confirmar deja el flow_state terminal con el puntero apagado, un texto
// cualquiera recibe la MISMA respuesta que el CONTROL (a), y el MISMO contacto
// puede empezar un pedido nuevo con normalidad (b) — restartableOnStart/
// cart.ResumePolicy es AJENO a esta tarea y no debe alterarse.
func (f o6Fixture) o6QuiereCartTerminalYPedidoNuevo(telefono, control string) string {
	f.t.Helper()
	o6ArmaYConfirmaCarrito(f.ctx, f.t, f.rt, f.sessionID, telefono, "o6-cart")
	cid := o6Contacto(f.ctx, f.t, f.contacts, f.tenantID, telefono)
	f.o6QuiereTerminal(cid, "CART")
	f.o6QuiereMismaRespuestaQueControl(telefono, "o6-cart-mudo", control, "CART")

	antes := f.sender.total()
	o6Entrante(f.ctx, f.t, f.rt, f.sessionID, telefono, "carrito", "o6-cart-segundo")
	segundo := f.sender.desde(antes)
	if len(segundo) != 1 || !strings.Contains(segundo[0], "Elige una categoría") {
		f.t.Fatalf("CART segundo pedido: quiero el arranque L1 del carrito, llegó %v", segundo)
	}
	ev2, alive2, err := f.evs.GetAliveByKind(f.ctx, f.tenantID, f.sessionID, cid, "cart")
	if err != nil || !alive2 || ev2.ID == "" {
		f.t.Fatalf("CART segundo pedido: debe nacer un evento cart VIVO propio: ok=%v err=%v id=%q", alive2, err, ev2.ID)
	}
	return cid
}

// o6QuiereSurveyMudoArreglado fija (c): la encuesta terminal recibe la MISMA
// respuesta que el CONTROL, no silencio.
func (f o6Fixture) o6QuiereSurveyMudoArreglado(control string) {
	f.t.Helper()
	o6TerminaEncuesta(f.ctx, f.t, f.rt, f.sessionID, o6TelSurvey, "o6-survey")
	cid := o6Contacto(f.ctx, f.t, f.contacts, f.tenantID, o6TelSurvey)
	f.o6QuiereTerminal(cid, "SURVEY")
	f.o6QuiereMismaRespuestaQueControl(o6TelSurvey, "o6-survey-mudo", control, "SURVEY")
}

// o6QuiereMenuMudoArreglado fija (d): un flujo cuyo NODO es de tipo `menu`
// (hoja SIN next, el caso REALISTA de H2) recibe la MISMA respuesta que el
// CONTROL, no silencio.
func (f o6Fixture) o6QuiereMenuMudoArreglado(control string) {
	f.t.Helper()
	o6TerminaMenu(f.ctx, f.t, f.rt, f.sessionID, o6TelMenu, "o6-menu")
	cid := o6Contacto(f.ctx, f.t, f.contacts, f.tenantID, o6TelMenu)
	f.o6QuiereTerminal(cid, "MENU")
	f.o6QuiereMismaRespuestaQueControl(o6TelMenu, "o6-menu-mudo", control, "MENU")
}

// o6QuiereEnCursoIntacto fija (e), E-1/E-7: un carrito EN CURSO (nunca
// confirmado) NO se ve afectado — sigue respondiendo por su propio módulo, no
// por el fallback genérico del CONTROL, y no se reinicia a L1.
func (f o6Fixture) o6QuiereEnCursoIntacto(control string) {
	f.t.Helper()
	o6Entrante(f.ctx, f.t, f.rt, f.sessionID, o6TelEnCurso, "carrito", "o6-encurso-w0")
	o6Entrante(f.ctx, f.t, f.rt, f.sessionID, o6TelEnCurso, "1", "o6-encurso-w1") // Bebidas
	cid := o6Contacto(f.ctx, f.t, f.contacts, f.tenantID, o6TelEnCurso)
	nodo, _, ok := o6FlowState(f.t, f.db, f.tenantID, f.sessionID, cid)
	if !ok || nodo == model.NodeTerminal {
		f.t.Fatalf("precondición EN CURSO: el carrito debe seguir navegando, NO terminal; current_node=%q ok=%v", nodo, ok)
	}

	antes := f.sender.total()
	o6Entrante(f.ctx, f.t, f.rt, f.sessionID, o6TelEnCurso, "1", "o6-encurso-w2") // Café → L3
	enCurso := f.sender.desde(antes)
	if len(enCurso) != 1 {
		f.t.Fatalf("EN CURSO: el módulo debe seguir contestando su propia navegación, llegó %v", enCurso)
	}
	if enCurso[0] == control {
		f.t.Fatalf("EN CURSO: NO debe caer al fallback genérico del CONTROL — el carrito sigue vivo y debe atender su propio Step; dijo %q", enCurso[0])
	}
	if strings.Contains(enCurso[0], "Elige una categoría") {
		f.t.Fatalf("EN CURSO: no debe reiniciar a L1 —el carrito EN CURSO no se toca—; dijo %q", enCurso[0])
	}
}

// o6QuiereControl fija la referencia: sin flow_state, un texto cualquiera cae
// al fallback (D-043.2 vía startPlainFlow) con la pantalla de arranque del menú
// (o6MenuFlujo, NO durable — ver la nota de A1 en el docstring de o6Runtime).
// Devuelve el texto exacto para que el resto de escenarios lo comparen.
func (f o6Fixture) o6QuiereControl() string {
	f.t.Helper()
	antes := f.sender.total()
	o6Entrante(f.ctx, f.t, f.rt, f.sessionID, o6TelControl, "gracias!", "o6-control-1")
	control := f.sender.desde(antes)
	if len(control) != 1 {
		f.t.Fatalf("CONTROL: quiero EXACTAMENTE 1 saliente (el fallback), llegaron %d: %v", len(control), control)
	}
	if !strings.Contains(control[0], "Ventas") {
		f.t.Fatalf("CONTROL: el fallback debe ofrecer el arranque del menú; dijo %q", control[0])
	}
	// El fallback ARRANCA el flujo del menú de verdad (startPlainFlow): el
	// CONTROL termina con un flow_state fresco en L1, navegando — la referencia de
	// esta medida es el TEXTO que dijo, no si quedó o no flow_state.
	return control[0]
}

// TestE2E_Ola6_FlowStateTerminalNoDejaMudo es la medida COMPLETA del hallazgo:
// un contacto de CONTROL (nunca toca ningún flujo) fija la referencia, y tres
// contactos —cart, survey, menu— que TERMINARON su flujo reciben, tras un
// entrante cualquiera, EXACTAMENTE la misma respuesta que el control. Un cuarto
// contacto, con un carrito EN CURSO (no confirmado), sigue respondiendo por su
// propio módulo — la prueba de que nada muere por conmutar (E-1/E-7). El quinto
// hito confirma que el MISMO contacto que confirmó puede pedir de nuevo.
func TestE2E_Ola6_FlowStateTerminalNoDejaMudo(t *testing.T) {
	db := e2eOpenDB(t)
	ctx := context.Background()
	tenantID, sessionID := o6Seed(t, db)

	feats := conTodosLosTipos()
	for _, f := range events.KindFeatures() {
		feats.Enable(tenantID, f)
	}
	rt, evs, contacts, sender := o6Runtime(t, db, tenantID, feats)
	f := o6Fixture{t: t, ctx: ctx, db: db, tenantID: tenantID, sessionID: sessionID, rt: rt, evs: evs, contacts: contacts, sender: sender}

	control := f.o6QuiereControl()
	f.o6QuiereCartTerminalYPedidoNuevo(o6TelCart, control) // (a) + (b)
	f.o6QuiereSurveyMudoArreglado(control)                 // (c)
	f.o6QuiereMenuMudoArreglado(control)                   // (d)
	f.o6QuiereEnCursoIntacto(control)                      // (e), E-1/E-7
}
