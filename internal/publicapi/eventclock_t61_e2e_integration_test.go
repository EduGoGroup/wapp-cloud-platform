// eventclock_t61_e2e_integration_test.go — T6.1 del Plan 043 (Ola 6): el e2e
// conversacional completo con el RELOJ INYECTADO, atravesando la cadena entera
// como conversationchain_w45_e2e_integration_test.go (su ancestro directo): runtime
// real → trigger real → birthEvent real → engine + cart/survey REALES → PersistSink
// real (con el hilo de decisiones) → proyector real → Postgres real.
//
// Los DOS relojes (runtime.WithClock y events.WithClock) se cablean sobre la MISMA
// función movible (t61Reloj), como exige el precedente del repo
// (internal/flujos/runtime/event_clock_test.go, newRelojRuntime). Mover el reloj es
// lo que permite «dejar vencer la inactividad» SIN esperar de verdad (🚫 nunca
// time.Sleep).
//
// ⚠️ CORREGIDO EN REVISIÓN (medido, no deducido). De los dos relojes, el ÚNICO que
// gobierna algo en este guion es events.WithClock: alimenta IsSuspended (el
// vencimiento por inactividad), Touch (last_activity_at) y closed_at, y los tres se
// afirman abajo — mutar events.Store.Touch a time.Now() pone este test en rojo.
// runtime.WithClock, en cambio, tiene UN solo consumidor en todo el runtime
// (conversationExpired, incoming.go), que aquí es inerte por dos razones
// independientes: el tenant no tiene fila en tenant_settings ⇒ ConversationTTL = 0
// ⇒ «nunca vence», y la comparación es contra flow_state.updated_at, que lo estampa
// Postgres con now() (reloj de PARED), de modo que un reloj inyectado en el pasado
// daría una diferencia negativa aunque el TTL no fuera cero. Verificado: quitar
// flowruntime.WithClock de este arranque deja el test IGUAL de verde. Se conserva
// porque el guion lo pide y porque es correcto tener los dos relojes coherentes,
// pero NO se le atribuye aquí un observable que no tiene: el TTL conversacional
// (limbo) no se ejerce en este test.
//
// El guion (tasks.md:1455-1465): saludo que NO crea evento ⇒ «carrito» ⇒ agregar
// líneas (cada una refresca el reloj) ⇒ dejar vencer la inactividad y mandar «2»
// (conversación nueva con el automensaje de rescate, evento cart VIVO) ⇒ «encuesta»
// (segundo tipo vivo) ⇒ «carrito» (vuelve, resumen AL CLIENTE, líneas del intake
// intactas — REQ-26b) ⇒ cerrar.
//
// Corre contra WAPP_TEST_DB_DSN (se omite sin ella; WAPP_TEST_REQUIRE_DB la exige).
// Datos con prefijo t61-.
package publicapi_test

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"sync"
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
	t61Flujo    = "t61-flujo-carrito"
	t61FlujoEnc = "t61-flujo-encuesta"
	t61Telefono = "573001116101"
)

// t61CartFlow es UN nodo `cart` sobre el catálogo de tenant_content, igual que el
// flujo `tienda` del entorno vivo (reusa el catálogo Café/Té/Flan de t45vCatalogo:
// misma forma, otro tenant).
func t61CartFlow() model.Flow {
	return model.Flow{
		FlowID:  t61Flujo,
		Initial: "cart",
		Nodes: map[string]model.Node{
			"cart": {Type: "cart", Content: &model.ContentRef{Source: "json", Ref: "t61-catalogo"}},
		},
	}
}

// t61SurveyFlow es una encuesta de DOS preguntas encadenadas (como surveyFlow() del
// paquete runtime): con UNA sola pregunta, responderla la termina de inmediato
// (Next apunta al centinela) y no queda ABIERTA para poder abandonarla a medias —
// exactamente lo que el hito (f) de este guion necesita: q1 respondida (algo que
// resumir) y q2 pendiente (el evento sigue open) cuando el cliente vuelve al carrito.
func t61SurveyFlow() model.Flow {
	return model.Flow{
		FlowID:  t61FlujoEnc,
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

// t61Reloj es el reloj MOVIBLE compartido por runtime.WithClock y events.WithClock:
// el precedente exacto del repo (event_clock_test.go, newRelojRuntime) es mover LOS
// DOS relojes juntos, porque dos relojes que discreparan dentro de un mismo test
// serían la fuente de un flake, no una simplificación. Cuál de los dos gobierna algo
// EN ESTE guion está medido en el encabezado del fichero: el de events (IsSuspended,
// Touch, closed_at). El de runtime solo lo lee conversationExpired, inerte aquí.
type t61Reloj struct{ ahora time.Time }

func (r *t61Reloj) fn() func() time.Time   { return func() time.Time { return r.ahora } }
func (r *t61Reloj) avanza(d time.Duration) { r.ahora = r.ahora.Add(d) }

// t61Sender es el doble PERMITIDO (transporte WhatsApp/Edge): guarda el HISTORIAL
// completo de textos salientes, porque este e2e necesita distinguir el automensaje
// de rescate (T3.6) del resumen «al vuelo» del rescate por tipo (T3.4) — dos
// mensajes en el MISMO turno, ver sendTexts.
type t61Sender struct {
	mu   sync.Mutex
	msgs []string
}

func (s *t61Sender) SendText(_ context.Context, _, _, text string) (*cloudlinkv1.Ack, error) {
	s.mu.Lock()
	s.msgs = append(s.msgs, text)
	n := len(s.msgs)
	s.mu.Unlock()
	return &cloudlinkv1.Ack{AckedCommandId: fmt.Sprintf("t61-cmd-%d", n), Ok: true}, nil
}

func (s *t61Sender) SendMedia(_ context.Context, _, _, _, _, _, _, _ string) (*cloudlinkv1.Ack, error) {
	s.mu.Lock()
	s.msgs = append(s.msgs, "[media]")
	n := len(s.msgs)
	s.mu.Unlock()
	return &cloudlinkv1.Ack{AckedCommandId: fmt.Sprintf("t61-cmd-%d", n), Ok: true}, nil
}

// desde devuelve los textos salientes escritos DESPUÉS del índice dado (los que
// produjo el último turno), para poder afirmar sobre TODOS los mensajes de un turno
// y no solo sobre el último (un salto por tipo manda resumen + pantalla, dos envíos).
func (s *t61Sender) desde(i int) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.msgs[i:]...)
}

func (s *t61Sender) total() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.msgs)
}

// t61Seed siembra el tenant y su sesión de fleet (igual que t45vSeed): el resolver
// de tenant REAL (PostgresTenantResolver sobre fleet_sessions) hace su trabajo.
func t61Seed(t *testing.T, db *sql.DB) (tenantID, sessionID string) {
	t.Helper()
	ctx := context.Background()
	sessionID = fmt.Sprintf("t61-sess-%d", time.Now().UnixNano())

	if err := db.QueryRowContext(ctx,
		`INSERT INTO public.tenants (slug, display_name) VALUES ($1, $2) RETURNING id::text`,
		"t61-reloj-"+sessionID, "T61 e2e reloj de conversación").Scan(&tenantID); err != nil {
		t.Fatalf("sembrando tenant: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		for _, q := range []string{
			`DELETE FROM public.intake_items
			  WHERE intake_id IN (SELECT id FROM public.intakes WHERE tenant_id = $1)`,
			`DELETE FROM public.intakes WHERE tenant_id = $1`,
			// survey_results.event_id declara a conversation_events (0054, D-043.21): sin
			// barrerla antes, el DELETE del tenant revienta por su FK (t61 es el primer e2e
			// de esta ola que hace nacer un evento `survey` de verdad).
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
		VALUES ($1::uuid, 't61-edge', $2, 'online', 'active')
	`, tenantID, sessionID); err != nil {
		t.Fatalf("sembrando fleet_sessions: %v", err)
	}
	return tenantID, sessionID
}

// t61Runtime arma el runtime de PRODUCCIÓN (engine con cart/survey/menu reales,
// PersistSink real con el hilo de decisiones, resolver de disparos real, plano de
// eventos real) MÁS el despachador (events.Dispatcher) cableado como OpeningBuilder
// (Plan 043 · T3.8): es lo que hace que un entrante sin evento vivo que no case
// ningún trigger reciba el automensaje de rescate en vez del fallback mudo.
func t61Runtime(t *testing.T, db *sql.DB, tenantID string, feats *entitlements.Fake, reloj *t61Reloj) (
	*flowruntime.Runtime, *events.Store, *contact.MemoryResolver, *t61Sender,
) {
	t.Helper()
	ctx := context.Background()

	repo := flowstore.NewPostgresRepository(db)
	if _, err := repo.InsertDefinition(ctx, tenantID, t61CartFlow()); err != nil {
		t.Fatalf("publicar flujo carrito: %v", err)
	}
	if _, err := repo.InsertDefinition(ctx, tenantID, t61SurveyFlow()); err != nil {
		t.Fatalf("publicar flujo encuesta: %v", err)
	}
	if err := repo.UpsertTenantContent(ctx, tenantID, "t61-catalogo", []byte(t45vCatalogo)); err != nil {
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
			MatchType: trigger.MatchExact, EventKind: "cart", FlowID: t61Flujo, Enabled: true},
		{TenantID: tenantID, Kind: trigger.KindEventStart, Keyword: "encuesta",
			MatchType: trigger.MatchExact, EventKind: "survey", FlowID: t61FlujoEnc, Enabled: true},
		// Fallback SIN keyword: casa cualquier texto que no case event_start/keyword
		// (el saludo, y el «2» que llega tras vencer la inactividad). Su FlowID no se
		// usa mientras la oferta (BuildOpening) no venga vacía — aquí siempre hay algo
		// que ofrecer (carrito/encuesta), así que startPlainFlow nunca se alcanza.
		{TenantID: tenantID, Kind: trigger.KindFallback, FlowID: t61Flujo, Enabled: true},
	}
	for _, r := range reglas {
		if _, err := ts.Insert(ctx, r); err != nil {
			t.Fatalf("insert regla: %v", err)
		}
	}

	eventStore := events.NewStore(db, nil, events.WithClock(reloj.fn()))
	intakeStore := intakes.NewPostgres(db)
	contacts := contact.NewMemoryResolver(nil)
	sender := &t61Sender{}

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
		flowruntime.WithClock(reloj.fn()),
		flowruntime.WithOpeningBuilder(events.NewDispatcher(eventStore, events.NewTriggerKindOffer(ts), feats)),
	)
	return rt, eventStore, contacts, sender
}

// t61EventoStatus lee el status EN BD de un evento por id, acotado al tenant: cada
// hito del guion consulta aquí, nunca deduce el estado de lo que hay en memoria.
func t61EventoStatus(t *testing.T, db *sql.DB, tenantID, eventID string) events.Status {
	t.Helper()
	var status string
	if err := db.QueryRowContext(context.Background(),
		`SELECT status FROM public.conversation_events WHERE id = $1 AND tenant_id = $2::uuid`,
		eventID, tenantID).Scan(&status); err != nil {
		t.Fatalf("leyendo status del evento %s: %v", eventID, err)
	}
	return events.Status(status)
}

// t61EventoLastActivity lee last_activity_at EN BD: es lo único que demuestra que
// Touch corrió (o no corrió) en un turno dado, sin fiarse de lo que hay en memoria.
func t61EventoLastActivity(t *testing.T, db *sql.DB, eventID string) time.Time {
	t.Helper()
	var la time.Time
	if err := db.QueryRowContext(context.Background(),
		`SELECT last_activity_at FROM public.conversation_events WHERE id = $1`,
		eventID).Scan(&la); err != nil {
		t.Fatalf("leyendo last_activity_at de %s: %v", eventID, err)
	}
	return la
}

// t61ContarEventos cuenta TODOS los conversation_events del tenant: la foto de
// partida (tras el saludo) debe ser CERO, la prueba de que «el saludo no crea
// evento» (E-6) no es una afirmación deducida sino observada.
func t61ContarEventos(t *testing.T, db *sql.DB, tenantID string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM public.conversation_events WHERE tenant_id = $1::uuid`,
		tenantID).Scan(&n); err != nil {
		t.Fatalf("contando eventos: %v", err)
	}
	return n
}

// t61ContarIntakeItems cuenta las líneas del intake que declara EventID: es la
// verificación de REQ-26b («líneas del intake intactas») — se relee tras el rescate
// para comprobar que sobrevivieron al reinicio del sub-estado del carrito.
func t61ContarIntakeItems(t *testing.T, db *sql.DB, tenantID, eventID string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), `
		SELECT count(*) FROM public.intake_items it
		JOIN public.intakes i ON i.id = it.intake_id
		WHERE i.tenant_id = $1 AND i.event_id = $2::uuid
	`, tenantID, eventID).Scan(&n); err != nil {
		t.Fatalf("contando intake_items del evento %s: %v", eventID, err)
	}
	return n
}

// t61ContarSummaries cuenta las filas entry_kind='summary' DE UN EVENTO CONCRETO,
// para poder afirmar A CUÁL evento pertenece cada una.
//
// ⚠️ Es «por evento», NO «de todo el tenant» (el comentario original prometía las dos
// cosas y la consulta solo hacía la primera): el criterio «UNA fila summary en todo
// el guion» lo comprueba t61SummariesDelTenant, abajo.
func t61ContarSummaries(t *testing.T, db *sql.DB, eventID string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), `
		SELECT count(*) FROM public.conversation_event_messages
		WHERE event_id = $1::uuid AND entry_kind = 'summary'
	`, eventID).Scan(&n); err != nil {
		t.Fatalf("contando summaries de %s: %v", eventID, err)
	}
	return n
}

// t61SummariesDelTenant devuelve TODAS las filas entry_kind='summary' del tenant,
// como "<kind del evento>|<payload>", en orden de escritura.
//
// Existe porque afirmar «0 del carrito» y «1 de la encuesta» NO es afirmar «1 en todo
// el guion»: una tercera fila colgada de un evento que el test no nombra (un `menu`
// del despachador, por ejemplo) pasaría inadvertida. Y devuelve el PAYLOAD, no un
// conteo, porque contar filas no comprueba que la fila DIGA algo: una mutación que
// escribiera el resumen mudo (`{"kind":"","level":""}`) en el evento correcto dejaba
// el test en verde — justo lo que responder q1 más arriba pretendía evitar.
func t61SummariesDelTenant(t *testing.T, db *sql.DB, tenantID string) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `
		SELECT e.kind, COALESCE(m.payload::text, '<null>')
		  FROM public.conversation_event_messages m
		  JOIN public.conversation_events e ON e.id = m.event_id
		 WHERE e.tenant_id = $1::uuid AND m.entry_kind = 'summary'
		 ORDER BY m.id
	`, tenantID)
	if err != nil {
		t.Fatalf("leyendo los summaries del tenant: %v", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			t.Fatalf("cerrando filas de summaries: %v", cerr)
		}
	}()
	var out []string
	for rows.Next() {
		var kind, payload string
		if err := rows.Scan(&kind, &payload); err != nil {
			t.Fatalf("leyendo summary: %v", err)
		}
		out = append(out, kind+"|"+payload)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("recorriendo summaries: %v", err)
	}
	return out
}

// t61Bitacora devuelve, EN ORDEN, los nombres de los efectos de CICLO DE VIDA
// (flow_events, kind='event') de UN evento, localizados por su history_id.
//
// Es la respuesta a la pregunta que la secuencia de `status` no puede contestar: la
// tabla conversation_events guarda el estado ACTUAL, así que muestrear el status en
// cuatro hitos no demuestra qué pasó ENTRE ellos. La bitácora sí: es append-only
// (D-043.11) y registra los siete momentos en que la vida del evento cambia
// —event_started, event_switched, event_deactivated, event_inactivity_expired,
// event_closed, event_cancelled, event_escaped—. En este guion el vencimiento por
// inactividad y el salto por tipo son invisibles al `status` (E-6: no lo tocan) y
// PERFECTAMENTE visibles aquí.
func t61Bitacora(t *testing.T, db *sql.DB, tenantID, eventID string) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `
		SELECT name FROM public.flow_events
		 WHERE tenant_id = $1 AND kind = 'event'
		   AND payload->>'history_id' = (
		       SELECT history_id FROM public.conversation_events WHERE id = $2::uuid)
		 ORDER BY created_at, id
	`, tenantID, eventID)
	if err != nil {
		t.Fatalf("leyendo la bitácora del evento %s: %v", eventID, err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			t.Fatalf("cerrando filas de bitácora: %v", cerr)
		}
	}()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("leyendo efecto: %v", err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("recorriendo la bitácora: %v", err)
	}
	return out
}

// t61ClosedAt lee closed_at (NULL ⇒ zero) del evento.
func t61ClosedAt(t *testing.T, db *sql.DB, eventID string) (time.Time, bool) {
	t.Helper()
	var ca sql.NullTime
	if err := db.QueryRowContext(context.Background(),
		`SELECT closed_at FROM public.conversation_events WHERE id = $1`, eventID).Scan(&ca); err != nil {
		t.Fatalf("leyendo closed_at de %s: %v", eventID, err)
	}
	return ca.Time, ca.Valid
}

// t61FlowStateEventID lee flow_state.event_id de la conversación (NULL ⇒ "" ): sirve
// para comprobar que releaseForNewConversation soltó el puntero al vencer la
// inactividad (T3.2) sin tener que adivinarlo de la respuesta.
func t61FlowStateEventID(t *testing.T, db *sql.DB, tenantID, sessionID, contactID string) string {
	t.Helper()
	var eventID sql.NullString
	err := db.QueryRowContext(context.Background(), `
		SELECT event_id FROM public.flow_state
		WHERE tenant_id = $1::uuid AND session_id = $2 AND contact_id = $3::uuid
	`, tenantID, sessionID, contactID).Scan(&eventID)
	switch {
	case err == sql.ErrNoRows:
		return ""
	case err != nil:
		t.Fatalf("leyendo flow_state.event_id: %v", err)
	}
	return eventID.String
}

// TestE2E_T61_RelojDeConversacion_CicloCompleto es el criterio de T6.1 recorrido
// hito a hito, con la secuencia de `status` verificada EN BD en cada uno (nunca
// deducida del estado en memoria del runtime).
func TestE2E_T61_RelojDeConversacion_CicloCompleto(t *testing.T) {
	db := e2eOpenDB(t)
	ctx := context.Background()
	tenantID, sessionID := t61Seed(t, db)

	feats := conTodosLosTipos()
	for _, f := range events.KindFeatures() {
		feats.Enable(tenantID, f)
	}

	reloj := &t61Reloj{ahora: time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)}
	rt, evs, contacts, sender := t61Runtime(t, db, tenantID, feats, reloj)

	ref, err := contact.NewRef(contact.KindPhoneE164, t61Telefono)
	if err != nil {
		t.Fatalf("NewRef: %v", err)
	}
	cid, err := contacts.Resolve(ctx, tenantID, []contact.Ref{ref}, "")
	if err != nil {
		t.Fatalf("resolver contacto: %v", err)
	}

	wa := 0
	entrante := func(text string) {
		t.Helper()
		wa++
		if err := rt.HandleIncoming(ctx, sessionID, &cloudlinkv1.IncomingMessage{
			From: t61Telefono, Text: text, WaMessageId: fmt.Sprintf("t61-w%d", wa),
		}); err != nil {
			t.Fatalf("HandleIncoming(%q): %v", text, err)
		}
	}

	// ── (a) saludo: tiempo muerto, NO crea evento (E-6). La foto de partida es la
	// prueba: cero filas en conversation_events, no una deducción.
	entrante("hola, buenas")
	if n := t61ContarEventos(t, db, tenantID); n != 0 {
		t.Fatalf("el saludo NO debe crear evento; hay %d filas en conversation_events", n)
	}

	// ── (b) «carrito» ⇒ nace el evento. Observación #1 de la secuencia de status.
	entrante("carrito")
	cartID, cartAlive, err := evs.GetAliveByKind(ctx, tenantID, sessionID, cid, "cart")
	if err != nil || !cartAlive {
		t.Fatalf("GetAliveByKind(cart) tras «carrito»: ok=%v err=%v", cartAlive, err)
	}
	// Cuatro observaciones: nace, tras vencer, tras el rescate por tipo, tras el cancel.
	secuencia := make([]events.Status, 0, 4)
	secuencia = append(secuencia, t61EventoStatus(t, db, tenantID, cartID.ID))
	if secuencia[0] != events.StatusOpen {
		t.Fatalf("observación #1 (nace) = %q, quiero %q", secuencia[0], events.StatusOpen)
	}

	// ── (c) agregar líneas: «1 → 1 → 2 → 2» navega Bebidas → Café → Agregar →
	// cantidad 2 (mismo guion que t45vAbreCarritoConLinea), y CADA entrada refresca
	// el reloj del evento (Touch) porque el evento sigue dentro de su ventana.
	lineasTrasArmar := t61HitoArmarPedido(t, db, tenantID, cartID.ID, reloj, entrante)

	// ── (d) dejar vencer la inactividad (2h default, movemos 3h en el reloj
	// COMPARTIDO por runtime y events.Store) y mandar «2»: no casa ningún trigger, así
	// que entra por Fallback → openWithOffer → el AUTOMENSAJE DE RESCATE (T3.6). El
	// evento cart NO se toca: sigue open y su reloj NO se refresca (E-6: «un entrante
	// que no entra al evento no refresca su reloj»).
	relojAlVencer := t61EventoLastActivity(t, db, cartID.ID)
	reloj.avanza(3 * time.Hour)
	antesTurno := sender.total()
	entrante("2")
	secuencia = append(secuencia, t61EventoStatus(t, db, tenantID, cartID.ID))
	t61QuiereSueltaSinMatar(t, db, tenantID, sessionID, cid, cartID.ID,
		secuencia[1], relojAlVencer, sender.desde(antesTurno))

	// ── (e) «encuesta»: segundo tipo VIVO. Nace por beginEvent/birthEvent normal
	// (StartEvent), NO por elegir la opción de rescate del automensaje. previo (el
	// flow_state con el menú de la oferta pendiente) tiene EventID="" en este punto
	// —no es un abandono de verdad (summarizeAbandoned exige previo.EventID != "")—
	// así que NO se persiste ningún resumen del carrito aquí. Es la primera de las
	// desviaciones del guion literal que este test documenta (ver el informe).
	antesTurno = sender.total()
	entrante("encuesta")
	surveyID, surveyAlive, err := evs.GetAliveByKind(ctx, tenantID, sessionID, cid, "survey")
	if err != nil || !surveyAlive {
		t.Fatalf("GetAliveByKind(survey) tras «encuesta»: ok=%v err=%v", surveyAlive, err)
	}
	t61QuiereNacimientoLimpio(t, db, tenantID, cartID.ID, sender.desde(antesTurno))

	// Responde q1 (deja q2 pendiente A PROPÓSITO): sin ALGO decidido, el abandono de
	// abajo escribiría un resumen VACÍO y PersistSummary no escribe nada (sum.Empty(),
	// "normal, no error") — no probaría la fila summary, solo su ausencia.
	entrante("1")
	if got := t61EventoStatus(t, db, tenantID, surveyID.ID); got != events.StatusOpen {
		t.Fatalf("tras responder q1 (q2 queda pendiente) la encuesta debe seguir open; está %q", got)
	}

	// ── (f) «carrito»: SALTO POR TIPO con la encuesta TODAVÍA viva (mid-pregunta) y el
	// carrito TAMBIÉN vivo (D-043.2). Esto SÍ es un abandono real de la encuesta
	// (previo.EventID = surveyID, destino = cartID): summarizeAbandoned escribe UNA
	// fila `summary` — del evento ENCUESTA, no del carrito. El cliente recibe el
	// resumen «al vuelo» del CARRITO (T3.4) MÁS la pantalla del carrito reiniciado:
	// dos envíos en el mismo turno.
	antesTurno = sender.total()
	entrante("carrito")
	secuencia = append(secuencia, t61EventoStatus(t, db, tenantID, cartID.ID))
	t61QuiereRescatePorTipo(t, db, tenantID, cartID.ID, surveyID.ID, secuencia[2],
		lineasTrasArmar, sender.desde(antesTurno))

	// ── (g) confirmar el pedido ⇒ cierra el carrito Y su evento (hallazgo #24, Plan
	// 043 · Ola 6, decisión de Jhoan 2026-08-11: «arreglarlo en esta ola — que el
	// cierre confirmado del carrito cierre también su evento»). Hasta este arreglo,
	// el módulo cart JAMÁS fijaba Result.Next («el carrito permanece en el MISMO
	// nodo» era la doctrina completa, sin excepción), así que un flujo cuyo nodo de
	// espera es un `cart` no llegaba nunca al centinela, closeIfFinished no se
	// disparaba y `closed` no era alcanzable para ESTE guion — se medía por el otro
	// extremo: llevar el carrito hasta «Confirmar y finalizar» dejaba el intake en
	// `closed` y el EVENTO en `open` PARA SIEMPRE (es la misma causa, medida por
	// ejecución, del hallazgo #24: el segundo pedido de una conversación se perdía
	// en silencio contra `intakes_event_id_uidx`). Con el arreglo, Step fija
	// Result.Next al centinela justo cuando la sub-máquina llega a LevelClosed
	// (pedido CONFIRMADO, nunca LevelCancelled): closeIfFinished dispara en el MISMO
	// turno, cierra el evento y emite su event_closed. El criterio literal de T6.1
	// («…→closed») ya NO necesita el rodeo de la cancelación por la app —esa vía
	// sigue cubierta, sin ambigüedad, por T6.2—.
	//
	// El carrito volvió a L1 (categorías) tras el rescate por tipo del hito (f)
	// (switchToEvent borra el flow_state al conmutar, D-043.4: se pierde el
	// PROGRESO DE NODO, no las líneas —que siguen en intake_items, REQ-26b—), así
	// que se re-arma el pedido con el mismo guion de (c) («1→1→2→2», Café ×2) y se
	// confirma: «2» (Finalizar → resumen) → «1» (Confirmar → cierre).
	t61HitoArmarPedido(t, db, tenantID, cartID.ID, reloj, entrante)
	entrante("2") // Finalizar → L6 resumen
	entrante("1") // Confirmar → pedido CONFIRMADO: cierra el carrito Y su evento (#24)
	secuencia = append(secuencia, t61EventoStatus(t, db, tenantID, cartID.ID))
	if secuencia[3] != events.StatusClosed {
		t.Fatalf("observación #4 (cierre) = %q, quiero %q (el arreglo del hallazgo #24 hace closed alcanzable)",
			secuencia[3], events.StatusClosed)
	}

	t61QuiereCierreCoherente(t, db, tenantID, cartID.ID, surveyID.ID, reloj, secuencia)
}

// t61HitoArmarPedido recorre «1 → 1 → 2 → 2» (Bebidas → Café → Agregar → cantidad 2,
// mismo guion que t45vAbreCarritoConLinea) comprobando que CADA entrada refresca el
// reloj del evento con el valor EXACTO del reloj inyectado, y devuelve cuántas líneas
// quedaron. Que el Touch se compruebe entrada a entrada, y no solo al final, es lo que
// convierte «el reloj se refrescó alguna vez» en «se refrescó SIEMPRE».
func t61HitoArmarPedido(t *testing.T, db *sql.DB, tenantID, cartID string, reloj *t61Reloj, entrante func(string)) int {
	t.Helper()
	antesDeLineas := t61EventoLastActivity(t, db, cartID)
	for i, in := range []string{"1", "1", "2", "2"} {
		reloj.avanza(time.Minute) // separa los instantes para que el refresco sea observable
		entrante(in)
		la := t61EventoLastActivity(t, db, cartID)
		if !la.Equal(reloj.ahora) {
			t.Fatalf("línea %d: last_activity_at = %v, quería %v (el Touch no refrescó)", i, la, reloj.ahora)
		}
	}
	if got := t61EventoLastActivity(t, db, cartID); !got.After(antesDeLineas) {
		t.Fatalf("agregar líneas debe haber refrescado el reloj del evento: antes=%v después=%v", antesDeLineas, got)
	}
	n := t61ContarIntakeItems(t, db, tenantID, cartID)
	if n < 1 {
		t.Fatalf("tras agregar Café x2 debe haber al menos una línea; hay %d", n)
	}
	return n
}

// t61QuiereSueltaSinMatar fija el hito (d): vencida la inactividad, el evento sigue
// `open`, su reloj NO se refrescó (el «2» no entró al evento), el cliente vio el
// automensaje de rescate y la conversación soltó el puntero.
func t61QuiereSueltaSinMatar(t *testing.T, db *sql.DB, tenantID, sessionID, cid, cartID string,
	obs events.Status, relojAlVencer time.Time, turno []string) {
	t.Helper()
	if obs != events.StatusOpen {
		t.Fatalf("observación #2 (tras vencer+«2») = %q, quiero %q (el vencimiento NUNCA transiciona status)",
			obs, events.StatusOpen)
	}
	if got := t61EventoLastActivity(t, db, cartID); !got.Equal(relojAlVencer) {
		t.Fatalf("un «2» que NO entra al evento no debe refrescar su reloj: antes=%v después=%v", relojAlVencer, got)
	}
	if !anyContains(turno, "Retomar algo que dejaste a medias (1)") {
		t.Fatalf("tras vencer la inactividad debe verse el automensaje de rescate mencionando el pedido colgado; turno=%v", turno)
	}
	if fs := t61FlowStateEventID(t, db, tenantID, sessionID, cid); fs != "" {
		t.Fatalf("la conversación nueva no debe apuntar a ningún evento activo tras soltar por inactividad; flow_state.event_id=%q", fs)
	}
}

// t61QuiereNacimientoLimpio fija el hito (e): nacer «encuesta» no toca el carrito, no
// escribe resumen suyo y manda EXACTAMENTE la primera pregunta.
//
// El turno se MIRA (la entrega original capturaba el índice y no lo leía nunca —
// ineffassign lo delataba): nacer la encuesta no le vuelve a contar al cliente el
// carrito que dejó a medias, porque ese aviso ya se lo dio el automensaje de rescate
// del hito (d) y repetirlo en cada arranque sería la coletilla que taglineFor reserva
// para el camino de la intención.
func t61QuiereNacimientoLimpio(t *testing.T, db *sql.DB, tenantID, cartID string, turno []string) {
	t.Helper()
	if got := t61EventoStatus(t, db, tenantID, cartID); got != events.StatusOpen {
		t.Fatalf("el carrito no debe verse afectado por el nacimiento de la encuesta; está %q", got)
	}
	if n := t61ContarSummaries(t, db, cartID); n != 0 {
		t.Fatalf("nacer «encuesta» NO abandona el carrito (previo.EventID vacío): 0 summaries del carrito esperados, hay %d", n)
	}
	if len(turno) != 1 || !strings.Contains(turno[0], "¿Nos recomendarías?") {
		t.Fatalf("nacer «encuesta» debe mandar EXACTAMENTE su primera pregunta; turno=%v", turno)
	}
	if anyContains(turno, "Café") {
		t.Fatalf("nacer «encuesta» no debe re-anunciarle el carrito al cliente; turno=%v", turno)
	}
}

// t61QuiereRescatePorTipo fija el hito (f): el carrito sigue `open`, el cliente recibe
// su resumen AL VUELO (T3.4), el abandono de la ENCUESTA deja exactamente una fila
// summary —y el carrito ninguna— y las líneas del intake sobreviven (REQ-26b).
func t61QuiereRescatePorTipo(t *testing.T, db *sql.DB, tenantID, cartID, surveyID string,
	obs events.Status, lineasTrasArmar int, turno []string) {
	t.Helper()
	if obs != events.StatusOpen {
		t.Fatalf("observación #3 (rescate por tipo) = %q, quiero %q", obs, events.StatusOpen)
	}
	if !anyContains(turno, "Café") || !anyContains(turno, "x2") {
		t.Fatalf("el rescate por tipo debe recordarle al cliente su Café x2 (resumen AL VUELO, T3.4); turno=%v", turno)
	}
	if n := t61ContarSummaries(t, db, surveyID); n != 1 {
		t.Fatalf("el abandono de la encuesta (al volver al carrito) debe dejar EXACTAMENTE 1 fila summary; hay %d", n)
	}
	if n := t61ContarSummaries(t, db, cartID); n != 0 {
		t.Fatalf("el carrito no genera su propio summary al ser rescatado (no es un abandono de sí mismo); hay %d", n)
	}
	if n := t61ContarIntakeItems(t, db, tenantID, cartID); n != lineasTrasArmar {
		t.Fatalf("REQ-26b: las líneas del intake deben seguir INTACTAS tras el rescate; antes=%d ahora=%d",
			lineasTrasArmar, n)
	}
}

// t61QuiereCierreCoherente es el criterio de T6.1 leído sobre TODO lo que la BD
// guardó: la secuencia de status, el closed_at contra el reloj inyectado, la bitácora
// COMPLETA de ciclo de vida (que es el historial de transiciones que el status no
// puede dar) y la única fila summary del guion, con su contenido.
func t61QuiereCierreCoherente(t *testing.T, db *sql.DB, tenantID, cartID, surveyID string,
	reloj *t61Reloj, secuencia []events.Status) {
	t.Helper()
	// nunca aparece «paused» ni «expired» (esos valores no existen en el vocabulario
	// de Status; la comprobación es sobre lo que la BD devolvió, no sobre el enum Go).
	for i, st := range secuencia {
		if st == "paused" || st == "expired" {
			t.Fatalf("observación #%d = %q: ese estado no debe existir NUNCA (E-6 lo deroga)", i+1, st)
		}
	}
	// Criterio LITERAL de T6.1: open → open → open → closed. Alcanzable desde el
	// arreglo del hallazgo #24 (ver el comentario del hito (g)): confirmar el pedido
	// cierra el carrito Y su evento en el mismo turno.
	quiero := []events.Status{events.StatusOpen, events.StatusOpen, events.StatusOpen, events.StatusClosed}
	if !equalStatus(secuencia, quiero) {
		t.Fatalf("secuencia de status observada = %v, quiero %v", secuencia, quiero)
	}

	// ── El closed_at lo sella events.Store.TransitionEvent con el RELOJ INYECTADO, y
	// aquí se comprueba contra él y no contra «no es NULL». Es la afirmación más
	// fuerte que este test puede hacer sobre el reloj de eventos en su momento
	// terminal: con el reloj de pared (o con cualquier otro) el valor no casa.
	closedAt, sellado := t61ClosedAt(t, db, cartID)
	if !sellado {
		t.Fatalf("confirmar el pedido debe sellar closed_at (#24)")
	}
	if !closedAt.UTC().Equal(reloj.ahora) {
		t.Fatalf("closed_at = %v, quiero %v (el reloj INYECTADO, no el de pared)", closedAt.UTC(), reloj.ahora)
	}

	// ── La secuencia de `status` es un MUESTREO en cuatro hitos, y por sí sola no
	// demuestra qué pasó entre ellos. La bitácora append-only de flow_events sí: es el
	// historial COMPLETO de transiciones del evento, y aquí se fija entero y en orden.
	// Los dos momentos que el `status` no puede ver —el vencimiento por inactividad y
	// el salto por tipo— son exactamente donde una implementación que hubiese
	// introducido un `paused`/`expired` habría dejado su huella.
	bitacora := t61Bitacora(t, db, tenantID, cartID)
	quieroBit := []string{
		flowruntime.EffectEventStarted,           // (b) nace por «carrito»
		flowruntime.EffectEventInactivityExpired, // (d) vence la inactividad: SUELTA, no mata
		flowruntime.EffectEventSwitched,          // (f) el cliente vuelve al carrito
		flowruntime.EffectEventClosed,            // (g) confirmar el pedido cierra el evento (#24)
	}
	if !slices.Equal(bitacora, quieroBit) {
		t.Fatalf("bitácora de ciclo de vida del carrito = %v, quiero %v", bitacora, quieroBit)
	}
	// El escape global NUNCA ocurrió en este guion (a diferencia de event_closed, que
	// desde el arreglo del hallazgo #24 SÍ debe aparecer — ver quieroBit arriba).
	if slices.Contains(bitacora, flowruntime.EffectEventEscaped) {
		t.Fatalf("la bitácora del carrito no debe contener %q; es %v", flowruntime.EffectEventEscaped, bitacora)
	}
	if bit := t61Bitacora(t, db, tenantID, surveyID); !slices.Equal(bit, []string{flowruntime.EffectEventStarted}) {
		t.Fatalf("la encuesta se abandona pero NO muere (E-6): su bitácora debe ser solo [event_started]; es %v", bit)
	}

	// ── «UNA sola fila summary en todo el guion», comprobado A LO ANCHO DEL TENANT y
	// con su CONTENIDO — no como dos conteos por evento que dejarían pasar una tercera
	// fila colgada de un evento que este test no nombra, ni como una fila muda.
	sums := t61SummariesDelTenant(t, db, tenantID)
	if len(sums) != 1 {
		t.Fatalf("en TODO el guion debe haber EXACTAMENTE 1 fila entry_kind='summary'; hay %d: %v", len(sums), sums)
	}
	if !strings.HasPrefix(sums[0], "survey|") {
		t.Fatalf("la única fila summary debe ser del evento ENCUESTA (el único abandono real); es %q", sums[0])
	}
	if !strings.Contains(sums[0], `"question_id": "q1"`) || !strings.Contains(sums[0], `"answer_code": "1"`) {
		t.Fatalf("el resumen del abandono debe DECIR lo que la clienta ya había decidido (q1 = 1); es %q", sums[0])
	}
}

// anyContains reporta si alguno de los textos contiene sub.
func anyContains(msgs []string, sub string) bool {
	for _, m := range msgs {
		if strings.Contains(m, sub) {
			return true
		}
	}
	return false
}

// equalStatus compara dos secuencias de status en orden — la aserción central del
// criterio de T6.1 quiere el valor exacto, no solo la longitud.
func equalStatus(a, b []events.Status) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
