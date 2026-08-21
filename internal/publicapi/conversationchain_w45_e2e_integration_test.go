// conversationchain_w45_e2e_integration_test.go — VERIFICACIÓN FUERTE de la Ola
// 4.5 (Plan 043, D-043.21/22/23): la cadena ENTERA in-proceso, reproduciendo el
// guion del fallo del ANALISIS-043 §9 — el que destapó que cada capa verificada
// contra dobles dejaba el bug vivo en las COSTURAS, porque el dato que nadie
// escribía en producción (la ligadura evento↔solicitud) los tests lo sembraban a
// mano.
//
// La regla de este test, y su razón de existir: NINGÚN paso siembra la ligadura.
// No hay UN SOLO INSERT/UPDATE de `intakes` en este archivo — la fila nace del
// PROYECTOR de producción (cart.ensureOpenIntake, con el meta.EventID que viaja
// por el cauce de efectos de T4.5.1) o no nace. Los únicos dobles son el
// TRANSPORTE WhatsApp/Edge (t45vSender) y los dos planos de identidad que los e2e
// del repo ya doblaban (contact.MemoryResolver para la PII de contactos y
// entitlements.Fake para el plan comercial). Todo lo demás es de producción:
//
//	runtime real → trigger real (event_start «carrito») → birthEvent (con
//	FlowVersion REAL, T4.5.6) → engine + módulo cart REALES sobre el catálogo de
//	tenant_content REAL → PersistSink real (con WithDecisionThread sobre el
//	events.Store real) → cart.Projector real → PostgreSQL real (CHECK/FK/vista de
//	la 0054) → cancel por el HANDLER PÚBLICO real (mux de publicapi.Register,
//	canceller = el mismo *runtime.Runtime, abandono por intakes.Service real).
//
// El guion (ANALISIS-043 §9, ahora con el rediseño puesto):
//
//	(a) entrante «carrito» ⇒ nace el evento (open, flow_version REAL);
//	(b) navegar y agregar una línea ⇒ la fila de `intakes` nace CON el event_id
//	    del evento vivo — la escribe el proyector, no este test;
//	(c) el hilo del evento tiene su(s) fila(s) `decision` (en claro, nivel 1) y
//	    CERO filas `message` (la feature llm_intake está apagada);
//	(d) cancelar por el handler público ⇒ evento `cancelled` + intake `abandoned`
//	    con sus intake_items INTACTOS — y el intake del VECINO (mismo tenant,
//	    otro contacto) ni se toca: AbandonByEvent abandona POR EVENTO, no por
//	    tenant;
//	(e) el evento cancelado ya no es rescatable y `content=alive` ya no lo
//	    devuelve (la vista event_content, D-043.22).
//
// Corre contra WAPP_TEST_DB_DSN (se omite sin ella; WAPP_TEST_REQUIRE_DB la
// exige). Datos con prefijo t45v- y limpieza por t.Cleanup.
package publicapi_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
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
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
)

const (
	t45vKey       = "key-t45v-duena" // credencial de la dueña: intakes.read + intakes.write
	t45vFlujo     = "t45v-flujo-tienda"
	t45vTelefonoA = "573001114551" // la clienta cuyo evento se cancela
	t45vTelefonoB = "573001114552" // el vecino del MISMO tenant que no debe perder nada
)

// t45vCatalogo es el blob por-tenant del adapter json (misma forma que el §3.1
// del diseño del carrito): con «1 → 1 → 2 → 2» se agrega Café ×2.
const t45vCatalogo = `{"categories":[
	{"code":"1","label":"Bebidas","items":[
		{"code":"1","sku":"CAFE","label":"Café","price":2.5,"description":"Espresso doble"},
		{"code":"2","sku":"TE","label":"Té","price":2.0,"description":"Verde o negro"}]},
	{"code":"2","label":"Postres","items":[
		{"code":"1","sku":"FLAN","label":"Flan","price":3.0,"description":"Casero"}]}]}`

// t45vSender es EL doble permitido: el transporte WhatsApp/Edge. Solo acusa
// recibo; lo que este e2e afirma son los efectos en Postgres, no los textos.
type t45vSender struct{ n int }

func (s *t45vSender) SendText(_ context.Context, _, _, _ string) (*cloudlinkv1.Ack, error) {
	s.n++
	return &cloudlinkv1.Ack{AckedCommandId: fmt.Sprintf("t45v-cmd-%d", s.n), Ok: true}, nil
}

func (s *t45vSender) SendMedia(_ context.Context, _, _, _, _, _, _, _ string) (*cloudlinkv1.Ack, error) {
	s.n++
	return &cloudlinkv1.Ack{AckedCommandId: fmt.Sprintf("t45v-cmd-%d", s.n), Ok: true}, nil
}

// t45vFlow es el flujo real de la tienda: UN nodo `cart` que resuelve su catálogo
// por el adapter json (tenant_content), como el flujo `tienda` del entorno vivo.
func t45vFlow() model.Flow {
	return model.Flow{
		FlowID:  t45vFlujo,
		Initial: "cart",
		Nodes: map[string]model.Node{
			"cart": {Type: "cart", Content: &model.ContentRef{Source: "json", Ref: "t45v-catalogo"}},
		},
	}
}

// t45vSeed crea el tenant y su sesión de fleet (para que el RESOLVER DE TENANT
// REAL —PostgresTenantResolver sobre fleet_sessions— haga su trabajo, en vez de
// doblarlo). Registra la limpieza: lo que no cuelga del tenant por FK (intakes,
// flow_events, tenant_content) se barre explícito; el DELETE del tenant arrastra
// por CASCADE eventos (y su hilo), flow_state, flow_definitions y fleet_sessions.
func t45vSeed(t *testing.T, db *sql.DB) (tenantID, sessionID string) {
	t.Helper()
	ctx := context.Background()
	sessionID = fmt.Sprintf("t45v-sess-%d", time.Now().UnixNano())

	if err := db.QueryRowContext(ctx,
		`INSERT INTO public.tenants (slug, display_name) VALUES ($1, $2) RETURNING id::text`,
		"t45v-cadena-"+sessionID, "T45V cadena entera e2e").Scan(&tenantID); err != nil {
		t.Fatalf("sembrando tenant: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		for _, q := range []string{
			`DELETE FROM public.intake_items
			  WHERE intake_id IN (SELECT id FROM public.intakes WHERE tenant_id = $1)`,
			`DELETE FROM public.intakes WHERE tenant_id = $1`,
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
		VALUES ($1::uuid, 't45v-edge', $2, 'online', 'active')
	`, tenantID, sessionID); err != nil {
		t.Fatalf("sembrando fleet_sessions: %v", err)
	}
	return tenantID, sessionID
}

// t45vRuntime arma el runtime CON EL CABLEADO DE PRODUCCIÓN (bootstrap.go) sobre
// la BD de test: engine con los módulos reales y el content Router static+json,
// PersistSink real con los proyectores de cart y survey Y el hilo de decisiones
// (WithDecisionThread sobre el MISMO events.Store), plano de eventos real,
// abandono por el intakes.Service real (la puerta del dominio, no el UPDATE) y
// el resolver de tenant REAL sobre fleet_sessions.
func t45vRuntime(t *testing.T, db *sql.DB, tenantID string, feats *entitlements.Fake) (*flowruntime.Runtime, *events.Store, *contact.MemoryResolver) {
	t.Helper()
	ctx := context.Background()

	repo := flowstore.NewPostgresRepository(db)
	// La definición se publica DOS veces a propósito: la vigente es la versión 2,
	// de modo que un birthEvent que volviera a nacer con 0 —o con un 1 cableado a
	// mano— deja este e2e en rojo (T4.5.6 contra la columna real).
	for i := 0; i < 2; i++ {
		if _, err := repo.InsertDefinition(ctx, tenantID, t45vFlow()); err != nil {
			t.Fatalf("publicar definición (%d): %v", i+1, err)
		}
	}
	if err := repo.UpsertTenantContent(ctx, tenantID, "t45v-catalogo", []byte(t45vCatalogo)); err != nil {
		t.Fatalf("sembrar catálogo: %v", err)
	}

	reg := modules.NewRegistry()
	reg.Register(menu.New())
	reg.Register(survey.New())
	reg.Register(cart.New())
	eng := engine.New(reg, engine.WithContentSource(
		content.NewRouter(content.NewStatic(), content.NewJSON(repo))))

	ts := trigger.NewMemoryStore()
	if _, err := ts.Insert(ctx, trigger.Rule{
		TenantID: tenantID, Kind: trigger.KindEventStart, Keyword: "carrito",
		MatchType: trigger.MatchExact, EventKind: "cart", FlowID: t45vFlujo, Enabled: true,
	}); err != nil {
		t.Fatalf("insert regla event_start: %v", err)
	}

	eventStore := events.NewStore(db, nil)
	intakeStore := intakes.NewPostgres(db)
	contacts := contact.NewMemoryResolver(nil)

	rt := flowruntime.New(repo, eng, &t45vSender{}, flowruntime.NewPostgresTenantResolver(db),
		contacts, e2eLogger(),
		flowruntime.WithEventSink(flowruntime.NewPersistSink(repo,
			cart.NewProjector(repo, intakeStore, intakeStore, intakes.NewPostgresBuyerData(db, nil)),
			survey.NewProjector(repo)).WithDecisionThread(eventStore)),
		flowruntime.WithResumePolicy(cart.NodeTypeCart, cart.NewResumePolicy(repo)),
		flowruntime.WithTriggerResolver(trigger.NewConfigResolver(ts)),
		flowruntime.WithEventStore(eventStore),
		flowruntime.WithIntakeAbandoner(intakes.NewService(intakeStore)),
		flowruntime.WithSummarySources(flowruntime.NewSummarySources(repo)),
		flowruntime.WithEntitlements(feats))
	return rt, eventStore, contacts
}

// t45vEntrante entrega un mensaje del cliente al runtime, como lo haría el
// gateway al recibirlo del Edge.
func t45vEntrante(ctx context.Context, t *testing.T, rt *flowruntime.Runtime, sessionID, from, text, waID string) {
	t.Helper()
	if err := rt.HandleIncoming(ctx, sessionID, &cloudlinkv1.IncomingMessage{
		From: from, Text: text, WaMessageId: waID,
	}); err != nil {
		t.Fatalf("HandleIncoming(%q, %s): %v", text, waID, err)
	}
}

// t45vAbreCarritoConLinea recorre el camino REAL de una clienta: «carrito» pare
// el evento y arranca el flujo; «1 → 1 → 2 → 2» navega Bebidas → Café → Agregar →
// cantidad 2, y ese último paso emite el item_added que el proyector materializa.
func t45vAbreCarritoConLinea(ctx context.Context, t *testing.T, rt *flowruntime.Runtime, sessionID, telefono, base string) {
	t.Helper()
	t45vEntrante(ctx, t, rt, sessionID, telefono, "carrito", base+"-w0")
	for i, in := range []string{"1", "1", "2", "2"} {
		t45vEntrante(ctx, t, rt, sessionID, telefono, in, fmt.Sprintf("%s-w%d", base, i+1))
	}
}

// t45vEventoDe lee el evento del contacto directamente de la tabla (los asserts
// van sobre lo que la BD guardó, no sobre lo que un store devolvió).
func t45vEventoDe(ctx context.Context, t *testing.T, db *sql.DB, tenantID, contactID string) (id, status string, flowVersion int, closedAt sql.NullTime) {
	t.Helper()
	err := db.QueryRowContext(ctx, `
		SELECT id::text, status, flow_version, closed_at
		FROM public.conversation_events
		WHERE tenant_id = $1::uuid AND contact_id = $2::uuid AND kind = 'cart'
	`, tenantID, contactID).Scan(&id, &status, &flowVersion, &closedAt)
	if err != nil {
		t.Fatalf("leyendo el evento de %s: %v", contactID, err)
	}
	return id, status, flowVersion, closedAt
}

// t45vIntakeDe lee la (única) solicitud del contacto: su estado, el padre que
// DECLARA y cuántas líneas conserva.
func t45vIntakeDe(ctx context.Context, t *testing.T, db *sql.DB, tenantID, contactID string) (id, status, eventID string, lineas int) {
	t.Helper()
	var evID sql.NullString
	err := db.QueryRowContext(ctx, `
		SELECT i.id::text, i.status, i.event_id::text,
		       (SELECT count(*) FROM public.intake_items it WHERE it.intake_id = i.id)
		FROM public.intakes i WHERE i.tenant_id = $1 AND i.contact_id = $2
	`, tenantID, contactID).Scan(&id, &status, &evID, &lineas)
	if err != nil {
		t.Fatalf("leyendo la solicitud de %s: %v", contactID, err)
	}
	return id, status, evID.String, lineas
}

// TestE2E_W45_CadenaEnteraSinSembrarLigaduras es el criterio de T4.5.3/T4.5.5/
// T4.5.6/T4.5.7 atravesado de una vez, por las costuras de producción.
func TestE2E_W45_CadenaEnteraSinSembrarLigaduras(t *testing.T) {
	db := e2eOpenDB(t)
	ctx := context.Background()
	tenantID, sessionID := t45vSeed(t, db)

	// Las features de tipo, para el gate real de la API (listado y cancel); la
	// feature llm_intake queda APAGADA a propósito — el estado de entrega de la
	// ola: cero filas `message` en el hilo.
	feats := conTodosLosTipos()
	for _, f := range events.KindFeatures() {
		feats.Enable(tenantID, f)
	}
	rt, eventStore, contacts := t45vRuntime(t, db, tenantID, feats)

	// ── (a) «carrito» ⇒ nace el evento. Y ANTES de tocar nada más: NADIE ha
	// escrito una sola fila de intakes en este tenant — la foto de partida es la
	// del sistema vivo, no la de un fixture.
	t45vQuiereFotoVacia(ctx, t, db, tenantID)
	t45vAbreCarritoConLinea(ctx, t, rt, sessionID, t45vTelefonoA, "t45v-a")
	cidA := t45vContacto(ctx, t, contacts, tenantID, t45vTelefonoA)

	// (a)+(b): evento open con flow_version REAL, y la solicitud que nació
	// DECLARANDO a ese padre — la escribió el proyector, no este test.
	evA, intakeA, lineasA := t45vQuiereParVivo(ctx, t, db, tenantID, cidA)

	// ── (c) el hilo tiene su fila `decision` y CERO `message` (feature apagada).
	t45vQuiereHiloConDecisiones(ctx, t, db, evA)

	// ── El VECINO: mismo tenant, otro contacto, mismo guion de producción. Es lo
	// que convierte el cancel de abajo en una prueba de que AbandonByEvent filtra
	// POR EVENTO y no barre el tenant.
	t45vAbreCarritoConLinea(ctx, t, rt, sessionID, t45vTelefonoB, "t45v-b")
	cidB := t45vContacto(ctx, t, contacts, tenantID, t45vTelefonoB)
	evB, _, _ := t45vQuiereParVivo(ctx, t, db, tenantID, cidB)

	// ── El listado real ANTES del cancel: `content=alive` devuelve LOS DOS.
	keys := intakesKeys()
	keys[t45vKey] = testIdentity{TenantID: tenantID, Subject: "t45v-duena", Grants: []string{"intakes.read", "intakes.write"}}
	api := newAPI(publicapi.Deps{
		EventCanceller:     rt,
		ConversationEvents: eventStore,
		Entitlements:       feats,
	}, keys)
	if vivos := t45vListaAlive(t, api); !vivos[evA] || !vivos[evB] {
		t.Fatalf("content=alive antes del cancel debe traer los dos eventos (%s, %s): %v", evA, evB, vivos)
	}

	// ── (d) la dueña cancela el evento de A por el HANDLER PÚBLICO real.
	rec := call(api, t45vKey, http.MethodPost, cancelURL(evA), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel: code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := decodeCancelado(t, rec.Body.Bytes()); got.Status != "cancelled" || got.ClosedAt == "" {
		t.Fatalf("cuerpo del cancel=%+v; quiero cancelled con closed_at sellado", got)
	}
	t45vQuiereCancelYVecinoIntacto(ctx, t, db, tenantID, cidA, cidB, intakeA, lineasA)

	// ── (e) el cancelado desapareció de los dos espejos: `content=alive` ya no lo
	// devuelve (la vista dice `discarded` y el evento ya ni está open) y no es
	// rescatable para su conversación.
	if vivos := t45vListaAlive(t, api); vivos[evA] || !vivos[evB] {
		t.Fatalf("content=alive tras el cancel debe traer SOLO al vecino (%s): %v", evB, vivos)
	}
	rescatables, err := eventStore.ListRescuable(ctx, tenantID, sessionID, cidA, 0)
	if err != nil {
		t.Fatalf("ListRescuable(A): %v", err)
	}
	if len(rescatables) != 0 {
		t.Fatalf("el evento cancelado no es rescatable; la lista de A trae %+v", rescatables)
	}
}

// t45vQuiereFotoVacia afirma que el tenant no tiene NI UNA solicitud: la prueba
// de que nada de lo que sigue viene sembrado.
func t45vQuiereFotoVacia(ctx context.Context, t *testing.T, db *sql.DB, tenantID string) {
	t.Helper()
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM public.intakes WHERE tenant_id = $1`, tenantID).Scan(&n); err != nil {
		t.Fatalf("contando intakes de partida: %v", err)
	}
	if n != 0 {
		t.Fatalf("la foto de partida debe estar VACÍA (nada sembrado); hay %d intakes", n)
	}
}

// t45vContacto resuelve el contact_id OPACO con el que el runtime escribió al
// contacto (mismo resolver, idempotente).
func t45vContacto(ctx context.Context, t *testing.T, contacts *contact.MemoryResolver, tenantID, telefono string) string {
	t.Helper()
	cid, err := contacts.Resolve(ctx, tenantID, []contact.Ref{t45vRef(t, telefono)}, "")
	if err != nil {
		t.Fatalf("resolver contacto %s: %v", telefono, err)
	}
	return cid
}

// t45vQuiereParVivo afirma la foto DESPUÉS de armar el carrito: evento open con
// flow_version REAL (2: la definición se publicó dos veces — un birthEvent que
// pasara 0, la mentira vieja, o un 1 cableado quedan en rojo, T4.5.6) y la
// solicitud open que lo DECLARA (D-043.21) con su línea. El CHECK de la 0054 hace
// además imposible el huérfano: sin la escritura del proyector, la fila ni nace.
func t45vQuiereParVivo(ctx context.Context, t *testing.T, db *sql.DB, tenantID, contactID string) (eventID, intakeID string, lineas int) {
	t.Helper()
	eventID, status, flowVersion, _ := t45vEventoDe(ctx, t, db, tenantID, contactID)
	if status != "open" {
		t.Fatalf("el evento de %s debe estar open, está %q", contactID, status)
	}
	if flowVersion != 2 {
		t.Fatalf("flow_version del evento = %d; birthEvent debe congelar la versión VIGENTE (2)", flowVersion)
	}
	intakeID, intakeStatus, declarado, lineas := t45vIntakeDe(ctx, t, db, tenantID, contactID)
	if intakeStatus != "open" || declarado != eventID {
		t.Fatalf("la solicitud de %s debe nacer open declarando a su padre %s; got (status=%q, event_id=%q)",
			contactID, eventID, intakeStatus, declarado)
	}
	if lineas < 1 {
		t.Fatalf("la solicitud de %s debe tener su línea (Café ×2); tiene %d", contactID, lineas)
	}
	return eventID, intakeID, lineas
}

// t45vQuiereHiloConDecisiones afirma el criterio (c): fila(s) `decision` del
// PersistSink de producción —voz del cliente, JSON en claro, sin cuerpo cifrado,
// sin la foto privada del carrito— y CERO filas `message` sin la feature.
func t45vQuiereHiloConDecisiones(ctx context.Context, t *testing.T, db *sql.DB, eventID string) {
	t.Helper()
	rows, err := db.QueryContext(ctx, `
		SELECT entry_kind, role, payload::text, body_enc IS NULL
		FROM public.conversation_event_messages WHERE event_id = $1::uuid ORDER BY seq
	`, eventID)
	if err != nil {
		t.Fatalf("leyendo el hilo: %v", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			t.Logf("cerrando hilo: %v", cerr)
		}
	}()
	var decisiones, mensajes int
	for rows.Next() {
		var kind, role string
		var payload sql.NullString
		var sinCuerpo bool
		if err := rows.Scan(&kind, &role, &payload, &sinCuerpo); err != nil {
			t.Fatalf("scan hilo: %v", err)
		}
		switch kind {
		case "decision":
			decisiones++
			t45vQuiereDecisionEnClaro(t, role, payload, sinCuerpo)
		case "message":
			mensajes++
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("recorriendo el hilo: %v", err)
	}
	if decisiones < 1 {
		t.Fatalf("el hilo debe tener su fila `decision` por la línea agregada; tiene %d", decisiones)
	}
	if mensajes != 0 {
		t.Fatalf("sin llm_intake el hilo literal queda VACÍO; hay %d filas message", mensajes)
	}
}

// t45vQuiereDecisionEnClaro afirma el grado completo de UNA fila decision.
func t45vQuiereDecisionEnClaro(t *testing.T, role string, payload sql.NullString, sinCuerpo bool) {
	t.Helper()
	if role != "client" || !payload.Valid || !sinCuerpo {
		t.Fatalf("una decisión es voz del CLIENTE, JSON en claro y sin cuerpo cifrado: (%s, valid=%v, sinCuerpo=%v)",
			role, payload.Valid, sinCuerpo)
	}
	var estructura map[string]any
	if err := json.Unmarshal([]byte(payload.String), &estructura); err != nil {
		t.Fatalf("payload de la decisión no parsea: %v", err)
	}
	if estructura["sku"] != "CAFE" {
		t.Fatalf("la decisión debe ser la línea agregada (sku CAFE): %v", estructura)
	}
	if _, filtrada := estructura["items"]; filtrada {
		t.Fatalf("la foto PRIVADA del carrito jamás entra en el hilo en claro: %v", estructura)
	}
}

// t45vQuiereCancelYVecinoIntacto afirma el criterio (d) sobre las tablas: el
// evento de A cancelado y sellado, su solicitud abandoned con las líneas
// INTACTAS (INV-09), y el par del vecino SIN TOCAR — si AbandonByEvent barriera
// el tenant, aquí muere el test.
func t45vQuiereCancelYVecinoIntacto(ctx context.Context, t *testing.T, db *sql.DB, tenantID, cidA, cidB, intakeA string, lineasA int) {
	t.Helper()
	_, statusA, _, closedA := t45vEventoDe(ctx, t, db, tenantID, cidA)
	if statusA != "cancelled" || !closedA.Valid {
		t.Fatalf("el evento de A debe quedar cancelled y sellado; got (%q, closed=%v)", statusA, closedA.Valid)
	}
	intakeA2, intakeStatusA, _, lineasA2 := t45vIntakeDe(ctx, t, db, tenantID, cidA)
	if intakeA2 != intakeA || intakeStatusA != intakes.StatusAbandoned || lineasA2 != lineasA {
		t.Fatalf("la solicitud de A debe quedar abandoned con sus %d líneas intactas; got (id=%s, %q, %d líneas)",
			lineasA, intakeA2, intakeStatusA, lineasA2)
	}
	if _, statusB, _, _ := t45vEventoDe(ctx, t, db, tenantID, cidB); statusB != "open" {
		t.Fatalf("el evento del vecino debe seguir open tras el cancel ajeno; está %q", statusB)
	}
	if _, intakeStatusB, _, _ := t45vIntakeDe(ctx, t, db, tenantID, cidB); intakeStatusB != "open" {
		t.Fatalf("la solicitud del VECINO debe seguir open (el abandono es POR EVENTO, no por tenant); está %q", intakeStatusB)
	}
}

// t45vListaAlive pide el listado real con content=alive y devuelve el conjunto de
// ids que enseñó.
func t45vListaAlive(t *testing.T, api *testAPI) map[string]bool {
	t.Helper()
	rec := call(api, t45vKey, http.MethodGet, "/api/v1/conversation-events?content=alive", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET content=alive: code=%d; body=%s", rec.Code, rec.Body.String())
	}
	out := map[string]bool{}
	for _, ev := range decodeEventos(t, rec.Body.Bytes()).Events {
		out[ev.ID] = true
	}
	return out
}

// t45vRef construye la ref phone_e164 del contacto o falla el test.
func t45vRef(t *testing.T, valor string) contact.Ref {
	t.Helper()
	ref, err := contact.NewRef(contact.KindPhoneE164, valor)
	if err != nil {
		t.Fatalf("NewRef(phone, %q): %v", valor, err)
	}
	return ref
}
