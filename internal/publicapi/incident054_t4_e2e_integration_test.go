// incident054_t4_e2e_integration_test.go — T4 del Plan 054: regresión e2e contra
// Postgres real de los hallazgos #001 y #003
// (docs/runbooks/bitacora-errores-uat.md, 2026-08-12).
//
// #001: un tenant con reglas kind='keyword' hacia un flujo cuyo único nodo es
// {"type":"cart"} dejaba completar el pedido entero y luego perdía la comanda en
// silencio —el proyector intentaba un INSERT en intakes sin event_id (NOT NULL,
// migración 0054) y el fan-out best-effort del ADR-0003 se tragaba el
// SQLSTATE 23502—. #003: la propia corrección del #001 (sembrar reglas
// event_start) dejó viva, sobre el MISMO flujo, una regla kind='fallback' que
// ARMABA la misma trampa: cualquier entrante que no casara ninguna keyword
// («buenas tardes» bastaba) abría el carrito sin padre.
//
// Este fichero reproduce la configuración LITERAL de los dos hallazgos —no una
// parecida— sobre la tabla REAL public.flow_triggers (trigger.PostgresStore, a
// diferencia del MemoryStore que usan o6/t61/h24 en este mismo paquete): la
// instrucción de T4 es "reproducir esa configuración", y el kind de la regla es
// justo lo que MemoryStore no distingue de una fila real.
//
//   - inc054ReglasRed() siembra las TRES filas presentes en UAT tras el #003: la
//     regla event_start que el fix del #001 añadió (bitácora §4, aquí basta UNA
//     para que la oferta del despachador no salga vacía), la regla kind='keyword'
//     que causó el #001 y la fila LITERAL kind='fallback' del #003 (bitácora
//     hallazgo #003 §2: ('…', 'fallback', null, 'exact', 'tienda', 1, true, null)).
//   - inc054ReglasSinRed() aísla el corner MD-054.2 en un tenant separado: SOLO la
//     regla kind='keyword', sin NINGUNA event_start viva — la lectura más literal
//     de la causa raíz narrada en la bitácora ("un trigger se siembra como una
//     regla GENÉRICA kind='keyword'"), sin la red de rescate que el fix añadió.
//
// El runtime es el de PRODUCCIÓN: engine + módulo cart reales, PersistSink real,
// resolver de disparos real sobre la tabla real, y WithOpeningBuilder cableado
// (T1) — sin él estas dos escenas medirían el defecto del #003 otra vez, no su
// arreglo.
//
// Corre contra WAPP_TEST_DB_DSN (se omite sin ella; WAPP_TEST_REQUIRE_DB la
// exige). Datos con prefijo inc054-, limpiados por inc054Seed.
package publicapi_test

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/admin"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/content"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/cart"
	flowruntime "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	flowstore "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
)

const (
	inc054Flujo = "inc054-flujo-carrito"

	inc054TelKeyword  = "573001118001" // dispara la regla kind=keyword del #001.
	inc054TelFallback = "573001118002" // no casa nada; cae al fallback del #003.
	inc054TelHappy    = "573001118003" // camino feliz: event_start con evento padre.
	inc054TelAdmin    = "573001118004" // POST /admin/flows/start.
	inc054TelAPI      = "573001118005" // POST /api/v1/flows/{id}/start.

	inc054TelSinRed = "573001118011" // tenant AISLADO sin ninguna regla event_start.
)

// inc054CartFlow es UN nodo `cart` sobre el catálogo de tenant_content — la misma
// forma que el flujo `tienda` de UAT (bitácora #003 §2: único nodo, tipo cart).
func inc054CartFlow() model.Flow {
	return model.Flow{
		FlowID:  inc054Flujo,
		Initial: "cart",
		Nodes: map[string]model.Node{
			"cart": {Type: "cart", Content: &model.ContentRef{Source: "json", Ref: "inc054-catalogo"}},
		},
	}
}

// inc054Sender es el doble PERMITIDO (transporte WhatsApp/Edge): guarda el
// historial de textos salientes porque la medida necesita el TEXTO exacto (la
// oferta del despachador, no el texto crudo del flujo cart).
type inc054Sender struct{ msgs []string }

func (s *inc054Sender) SendText(_ context.Context, _, _, text string) (*cloudlinkv1.Ack, error) {
	s.msgs = append(s.msgs, text)
	return &cloudlinkv1.Ack{AckedCommandId: fmt.Sprintf("inc054-cmd-%d", len(s.msgs)), Ok: true}, nil
}

func (s *inc054Sender) SendMedia(_ context.Context, _, _, _, _, _, _, _ string) (*cloudlinkv1.Ack, error) {
	s.msgs = append(s.msgs, "[media]")
	return &cloudlinkv1.Ack{AckedCommandId: fmt.Sprintf("inc054-cmd-%d", len(s.msgs)), Ok: true}, nil
}

func (s *inc054Sender) desde(i int) []string { return append([]string(nil), s.msgs[i:]...) }
func (s *inc054Sender) total() int           { return len(s.msgs) }

// inc054Logger escribe en el buffer dado (en vez de al discard de e2eLogger): esta
// suite necesita AFIRMAR sobre el log —cero SQLSTATE 23502, el WARN de la
// degradación— y no solo sobre la BD.
func inc054Logger(buf *bytes.Buffer) sharedlogger.Logger {
	return sharedlogger.New(sharedlogger.WithWriter(buf))
}

// inc054Seed siembra un tenant AISLADO (mismo patrón que o6Seed/t61Seed) y limpia,
// ADEMÁS de lo que esas dos limpian, public.flow_triggers: este fichero es el
// primero de esta suite que siembra reglas en la tabla REAL
// (trigger.PostgresStore) en vez del MemoryStore que usan o6/t61/h24/w45 — flow_
// triggers.tenant_id no lleva FK a tenants (verificado contra
// migrations/structure/0023_flow_triggers.sql: sin REFERENCES), así que borrar el
// tenant NO se lleva sus reglas por delante.
func inc054Seed(t *testing.T, db *sql.DB, etiqueta string) (tenantID, sessionID string) {
	t.Helper()
	ctx := context.Background()
	sessionID = fmt.Sprintf("inc054-%s-sess-%d", etiqueta, time.Now().UnixNano())

	if err := db.QueryRowContext(ctx,
		`INSERT INTO public.tenants (slug, display_name) VALUES ($1, $2) RETURNING id::text`,
		"inc054-"+etiqueta+"-"+sessionID, "Plan 054 T4 - "+etiqueta).Scan(&tenantID); err != nil {
		t.Fatalf("sembrando tenant: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		for _, q := range []string{
			`DELETE FROM public.intake_items
			  WHERE intake_id IN (SELECT id FROM public.intakes WHERE tenant_id = $1)`,
			`DELETE FROM public.intakes WHERE tenant_id = $1`,
			`DELETE FROM public.flow_triggers WHERE tenant_id = $1::uuid`,
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
		INSERT INTO public.fleet_sessions (tenant_id, edge_id, session_id, state, profile)
		VALUES ($1::uuid, 'inc054-edge', $2, 'online', 'active')
	`, tenantID, sessionID); err != nil {
		t.Fatalf("sembrando fleet_sessions: %v", err)
	}
	return tenantID, sessionID
}

// inc054ReglasRed es el estado final de UAT tras el hallazgo #003: la regla
// event_start que el fix del #001 añadió (basta UNA para que buildOpeningOffer no
// salga vacío), la regla kind='keyword' que causó el #001 (palabra DISTINTA de la
// del event_start, para que el resolver no tenga ambigüedad de cuál gana) y la
// fila LITERAL kind='fallback' del #003 (bitácora hallazgo #003 §2).
func inc054ReglasRed() []trigger.Rule {
	return []trigger.Rule{
		// La regla SANA (bitácora #001 §4): event_start con event_kind='cart'.
		{Kind: trigger.KindEventStart, Keyword: "carrito", MatchType: trigger.MatchExact,
			EventKind: "cart", FlowID: inc054Flujo, Priority: 5, Enabled: true},
		// La regla que CAUSÓ el #001: kind='keyword' sin event_kind hacia el mismo
		// flujo durable.
		{Kind: trigger.KindKeyword, Keyword: "pedido", MatchType: trigger.MatchExact,
			FlowID: inc054Flujo, Priority: 1, Enabled: true},
		// La fila LITERAL del hallazgo #003: kind='fallback', keyword=NULL, MISMO
		// flow_id, priority=1, enabled=true, event_kind=NULL.
		{Kind: trigger.KindFallback, MatchType: trigger.MatchExact,
			FlowID: inc054Flujo, Priority: 1, Enabled: true},
	}
}

// inc054ReglasSinRed es el corner MD-054.2: SOLO la regla kind='keyword' que causó
// el #001, sin NINGUNA event_start viva.
//
// ⚠️ Esta configuración YA NO SE PUEDE SEMBRAR por la API: T2.7 (D-054.8) la rechaza
// con 422, y desde el arreglo A2 lo hace tanto para kind='fallback' como para
// kind='keyword' (blocksDurableWithoutEventStart, internal/flujos/admin/triggers.go).
// El test la siembra a propósito ESCRIBIENDO DIRECTAMENTE en la tabla vía
// trigger.PostgresStore, saltándose el CRUD, y eso es lo que le da su valor: ejercita
// la guarda de RUNTIME (D-054.5) sobre datos que entraron por otra vía —filas
// heredadas de antes de la validación, un `UPDATE` a mano en la base, una migración—.
// Las dos capas son necesarias: la del CRUD impide sembrarlo, la del runtime aguanta
// lo que ya estaba sembrado. Este test cubre la segunda.
func inc054ReglasSinRed() []trigger.Rule {
	return []trigger.Rule{
		{Kind: trigger.KindKeyword, Keyword: "pedido", MatchType: trigger.MatchExact,
			FlowID: inc054Flujo, Priority: 1, Enabled: true},
	}
}

// inc054Runtime arma el runtime de PRODUCCIÓN sobre el flujo de único nodo `cart`:
// engine + módulo cart reales, PersistSink real, resolver de disparos real sobre
// trigger.PostgresStore (la tabla REAL) y WithOpeningBuilder cableado (T1) — la
// pieza que decide si estas escenas miden el arreglo o el defecto que T1 cerró.
func inc054Runtime(t *testing.T, db *sql.DB, tenantID string, feats *entitlements.Fake, logBuf *bytes.Buffer, reglas []trigger.Rule) (
	*flowruntime.Runtime, *contact.MemoryResolver, *inc054Sender,
) {
	t.Helper()
	ctx := context.Background()

	repo := flowstore.NewPostgresRepository(db)
	if _, err := repo.InsertDefinition(ctx, tenantID, inc054CartFlow()); err != nil {
		t.Fatalf("publicar flujo carrito: %v", err)
	}
	if err := repo.UpsertTenantContent(ctx, tenantID, "inc054-catalogo", []byte(t45vCatalogo)); err != nil {
		t.Fatalf("sembrar catálogo: %v", err)
	}

	reg := modules.NewRegistry()
	reg.Register(cart.New())
	eng := engine.New(reg, engine.WithContentSource(
		content.NewRouter(content.NewStatic(), content.NewJSON(repo))))

	ts := trigger.NewPostgresStore(db)
	for _, r := range reglas {
		r.TenantID = tenantID
		if _, err := ts.Insert(ctx, r); err != nil {
			t.Fatalf("insert regla (kind=%s): %v", r.Kind, err)
		}
	}

	eventStore := events.NewStore(db, nil)
	intakeStore := intakes.NewPostgres(db)
	contacts := contact.NewMemoryResolver(nil)
	sender := &inc054Sender{}

	rt := flowruntime.New(repo, eng, sender, flowruntime.NewPostgresTenantResolver(db),
		contacts, inc054Logger(logBuf),
		flowruntime.WithEventSink(flowruntime.NewPersistSink(repo,
			cart.NewProjector(repo, intakeStore, intakeStore, intakes.NewPostgresBuyerData(db, nil))).WithDecisionThread(eventStore)),
		flowruntime.WithResumePolicy(cart.NodeTypeCart, cart.NewResumePolicy(repo)),
		flowruntime.WithTriggerResolver(trigger.NewConfigResolver(ts)),
		flowruntime.WithEventStore(eventStore),
		flowruntime.WithIntakeAbandoner(intakes.NewService(intakeStore)),
		flowruntime.WithSummarySources(flowruntime.NewSummarySources(repo)),
		flowruntime.WithEntitlements(feats),
		flowruntime.WithOpeningBuilder(events.NewDispatcher(eventStore, events.NewTriggerKindOffer(ts), feats)),
	)
	return rt, contacts, sender
}

// inc054Feats enciende las features de tipo de fábrica para tenantID (o6/t61
// hacen lo mismo: conTodosLosTipos() solo cubre tenantA/tenantB, no un tenant
// sembrado en caliente).
func inc054Feats(tenantID string) *entitlements.Fake {
	feats := conTodosLosTipos()
	for _, f := range events.KindFeatures() {
		feats.Enable(tenantID, f)
	}
	return feats
}

// inc054Entrante entrega un mensaje del cliente al runtime.
func inc054Entrante(ctx context.Context, t *testing.T, rt *flowruntime.Runtime, sessionID, from, text, waID string) {
	t.Helper()
	if err := rt.HandleIncoming(ctx, sessionID, &cloudlinkv1.IncomingMessage{
		From: from, Text: text, WaMessageId: waID,
	}); err != nil {
		t.Fatalf("HandleIncoming(%q, %s): %v", text, waID, err)
	}
}

// inc054ContarIntakes cuenta TODAS las solicitudes del tenant: la prueba directa
// de que ninguna de las dos puertas dejó una fila huérfana (ni ninguna otra).
func inc054ContarIntakes(ctx context.Context, t *testing.T, db *sql.DB, tenantID string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM public.intakes WHERE tenant_id = $1`, tenantID).Scan(&n); err != nil {
		t.Fatalf("contando intakes: %v", err)
	}
	return n
}

// inc054FlowStateDelFlujoDurable comprueba si quedó flow_state CON flow_id =
// inc054Flujo para (sesión, contacto): la guarda de D-054.5 rechaza ANTES del
// Save, así que un rechazo no debe dejar arrancado EL FLUJO DURABLE.
//
// OJO —medido, no supuesto—: no basta con "cero filas de flow_state a secas".
// Con el despachador cableado (T1), la degradación de T2.4 SÍ deja una fila:
// sendOfferNow → saveMenuState persiste el "menú pendiente" (para poder resolver
// la respuesta numérica del cliente en el siguiente turno) con flow_id="" y
// current_node="" —el mismo mecanismo, inofensivo, que deja cualquier oferta del
// despachador (T3.8 del Plan 043), nada específico de esta guarda—. Lo que la
// guarda promete es que NUNCA hay una fila con flow_id=inc054Flujo (el carrito
// arrancado de verdad), y eso es lo que esta función comprueba.
func inc054FlowStateDelFlujoDurable(ctx context.Context, t *testing.T, db *sql.DB, tenantID, sessionID, contactID string) bool {
	t.Helper()
	var n int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM public.flow_state
		WHERE tenant_id = $1::uuid AND session_id = $2 AND contact_id = $3::uuid AND flow_id = $4
	`, tenantID, sessionID, contactID, inc054Flujo).Scan(&n); err != nil {
		t.Fatalf("comprobando flow_state del flujo durable: %v", err)
	}
	return n > 0
}

// inc054Contacto resuelve el contact_id OPACO del teléfono dado.
func inc054Contacto(ctx context.Context, t *testing.T, contacts *contact.MemoryResolver, tenantID, telefono string) string {
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

// inc054IntakeDelContacto lee la solicitud del contacto (si existe): status y si
// event_id quedó poblado — el criterio del camino feliz.
func inc054IntakeDelContacto(ctx context.Context, t *testing.T, db *sql.DB, tenantID, contactID string) (existe bool, status string, eventID sql.NullString) {
	t.Helper()
	err := db.QueryRowContext(ctx, `
		SELECT status, event_id::text FROM public.intakes WHERE tenant_id = $1 AND contact_id = $2
	`, tenantID, contactID).Scan(&status, &eventID)
	switch {
	case err == sql.ErrNoRows:
		return false, "", sql.NullString{}
	case err != nil:
		t.Fatalf("leyendo intake de %s: %v", contactID, err)
	}
	return true, status, eventID
}

// inc054AssertSinRastro centraliza los tres asertos que las escenas de
// degradación (#001/#003) comparten: cero crecimiento de intakes, cero
// flow_state para la clave y cero "23502" en lo que el turno escribió al log.
func inc054AssertSinRastro(ctx context.Context, t *testing.T, db *sql.DB, tenantID, sessionID, cid, etiqueta string, intakesAntes int, logTail string) {
	t.Helper()
	if got := inc054ContarIntakes(ctx, t, db, tenantID); got != intakesAntes {
		t.Fatalf("%s: intakes debe seguir en %d, hay %d — comanda huérfana", etiqueta, intakesAntes, got)
	}
	if inc054FlowStateDelFlujoDurable(ctx, t, db, tenantID, sessionID, cid) {
		t.Fatalf("%s: NO debe quedar flow_state con flow_id=%s (el carrito arrancado de verdad) — la guarda rechaza ANTES del Save", etiqueta, inc054Flujo)
	}
	if strings.Contains(logTail, "23502") {
		t.Fatalf("%s: el log NO debe contener SQLSTATE 23502 — el INSERT sin padre nunca debe intentarse: %s", etiqueta, logTail)
	}
}

// inc054StartViaAdmin dispara POST /admin/flows/start sobre inc054Flujo con el
// MISMO Starter (rt) que produce en la instalación real.
func inc054StartViaAdmin(ctx context.Context, t *testing.T, rt *flowruntime.Runtime, tenantID, sessionID, telefono string) (code int, body string) {
	t.Helper()
	mux := http.NewServeMux()
	admin.Register(mux, nil, rt, nil)
	req := httptest.NewRequestWithContext(httpapi.WithIdentity(ctx, httpapi.Identity{TenantID: tenantID, Subject: "inc054-admin"}),
		http.MethodPost, "/admin/flows/start",
		strings.NewReader(fmt.Sprintf(`{"flow_id":%q,"session_id":%q,"contact":%q}`, inc054Flujo, sessionID, telefono)))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// inc054StartViaAPI dispara POST /api/v1/flows/{id}/start sobre inc054Flujo con
// el MISMO Starter (rt) — comparte motor con /admin/flows/start (T2.5).
func inc054StartViaAPI(t *testing.T, rt *flowruntime.Runtime, tenantID, sessionID, telefono string) (code int, body string) {
	t.Helper()
	keys := map[string]testIdentity{
		"inc054-key": {TenantID: tenantID, Subject: "inc054-api", Grants: []string{"flows.*"}},
	}
	api := newAPI(publicapi.Deps{FlowDeps: publicapi.FlowDeps{Starter: rt}}, keys)
	rec := call(api, "inc054-key", http.MethodPost, "/api/v1/flows/"+inc054Flujo+"/start",
		fmt.Sprintf(`{"session_id":%q,"contact":%q}`, sessionID, telefono))
	return rec.Code, rec.Body.String()
}

// inc054ArmaYConfirmaCarrito recorre el camino REAL de una clienta hasta el
// pedido CONFIRMADO: «carrito» (event_start, pare el evento) → 1→1→2→2 (Bebidas →
// Café → Agregar → cantidad 2) → 2 (Finalizar) → 1 (Confirmar). Misma secuencia
// que o6ArmaYConfirmaCarrito/h24Confirma en este paquete.
func inc054ArmaYConfirmaCarrito(ctx context.Context, t *testing.T, rt *flowruntime.Runtime, sessionID, telefono, base string) {
	t.Helper()
	inc054Entrante(ctx, t, rt, sessionID, telefono, "carrito", base+"-w0")
	for i, in := range []string{"1", "1", "2", "2", "2", "1"} {
		inc054Entrante(ctx, t, rt, sessionID, telefono, in, fmt.Sprintf("%s-w%d", base, i+1))
	}
}

// inc054RedFixture agrupa el andamiaje de TestE2E_Plan054_T4_RedCompleta para que
// esa función se quede en pura orquestación (gocyclo): cada escena vive en su
// propio método, igual que o6Fixture en terminalflow_ola6_e2e_integration_test.go.
type inc054RedFixture struct {
	ctx       context.Context
	db        *sql.DB
	tenantID  string
	sessionID string
	rt        *flowruntime.Runtime
	contacts  *contact.MemoryResolver
	sender    *inc054Sender
	logBuf    *bytes.Buffer
}

// degradaSinPerderComanda es el cuerpo COMPARTIDO de las escenas #001 (keyword) y
// #003 (fallback): entregar `texto` al teléfono dado debe dejar CERO rastro
// (inc054AssertSinRastro) y debe devolver EXACTAMENTE la oferta del despachador,
// nunca el arranque directo del flujo cart (eso es, literalmente, el bug).
//
// ⚠️ MEDIDO, no supuesto —y es lo que separa las dos escenas por dentro, aunque el
// resultado observable sea idéntico—: para kind='keyword' (#001) el log SÍ trae el
// WARN "flujo con contenido durable no puede arrancar sin evento" de
// degradeDurableStart: startFromDecision llama a startPlainFlow, que SÍ invoca
// startLocked y choca con la guarda T2.3. Para kind='fallback' (#003), en cambio,
// el log queda VACÍO: openWithOffer (incoming.go) llama primero a
// buildOpeningOffer y, con una event_start viva, manda la oferta de inmediato SIN
// llegar a intentar startPlainFlow —la guarda T2.3 ni se invoca—. El corte real de
// #003 con la red completa es T1 (el cable de WithOpeningBuilder), no la guarda
// T2.3; la guarda es la red de seguridad para cuando buildOpeningOffer NO tiene
// nada que ofrecer (ver TestE2E_Plan054_T4_SinRedEventStart, donde SÍ aparece el
// WARN, por el camino keyword). Los dos mecanismos cumplen la misma promesa
// observable —cero comanda huérfana, cero 23502— por rutas de código distintas;
// por eso este método NO afirma sobre el contenido del log, solo sobre su ausencia
// de "23502" (inc054AssertSinRastro) y sobre lo que el cliente recibió.
func (f inc054RedFixture) degradaSinPerderComanda(t *testing.T, telefono, texto, waID, etiqueta string) {
	t.Helper()
	antes := inc054ContarIntakes(f.ctx, t, f.db, f.tenantID)
	logAntes := f.logBuf.Len()
	msgAntes := f.sender.total()

	inc054Entrante(f.ctx, t, f.rt, f.sessionID, telefono, texto, waID)

	cid := inc054Contacto(f.ctx, t, f.contacts, f.tenantID, telefono)
	logTail := f.logBuf.String()[logAntes:]
	inc054AssertSinRastro(f.ctx, t, f.db, f.tenantID, f.sessionID, cid, etiqueta, antes, logTail)

	turno := f.sender.desde(msgAntes)
	if len(turno) != 1 {
		t.Fatalf("%s: quiero EXACTAMENTE 1 saliente (la oferta), llegaron %d: %v", etiqueta, len(turno), turno)
	}
	if !strings.Contains(turno[0], "¿Qué quieres hacer?") {
		t.Fatalf("%s: el cliente debe recibir la OFERTA del despachador, no silencio ni el flujo cart; llegó %q", etiqueta, turno[0])
	}
	if strings.Contains(turno[0], "Elige una categoría") {
		t.Fatalf("%s: NO debe arrancar el flujo cart directamente (eso es exactamente el bug); llegó %q", etiqueta, turno[0])
	}
}

// apis409EnLasDosRutas es la regresión EN PAREJA (T4 · criterio 3): el MISMO flujo
// durable, arrancado por las dos puertas HTTP que comparten Starter — ambas deben
// rechazar con 409 y ninguna debe dejar rastro.
func (f inc054RedFixture) apis409EnLasDosRutas(t *testing.T) {
	t.Helper()
	antes := inc054ContarIntakes(f.ctx, t, f.db, f.tenantID)

	codeAdmin, bodyAdmin := inc054StartViaAdmin(f.ctx, t, f.rt, f.tenantID, "inc054-sess-admin", inc054TelAdmin)
	if codeAdmin != http.StatusConflict {
		t.Fatalf("/admin/flows/start: code=%d, quiero 409; body=%s", codeAdmin, bodyAdmin)
	}
	cidAdmin := inc054Contacto(f.ctx, t, f.contacts, f.tenantID, inc054TelAdmin)
	if inc054FlowStateDelFlujoDurable(f.ctx, t, f.db, f.tenantID, "inc054-sess-admin", cidAdmin) {
		t.Fatalf("/admin/flows/start: el 409 no debe dejar flow_state del flujo durable")
	}

	codeAPI, bodyAPI := inc054StartViaAPI(t, f.rt, f.tenantID, "inc054-sess-api", inc054TelAPI)
	if codeAPI != http.StatusConflict {
		t.Fatalf("/api/v1/flows/{id}/start: code=%d, quiero 409; body=%s", codeAPI, bodyAPI)
	}
	cidAPI := inc054Contacto(f.ctx, t, f.contacts, f.tenantID, inc054TelAPI)
	if inc054FlowStateDelFlujoDurable(f.ctx, t, f.db, f.tenantID, "inc054-sess-api", cidAPI) {
		t.Fatalf("/api/v1/flows/{id}/start: el 409 no debe dejar flow_state del flujo durable")
	}

	// Los dos cuerpos deben ser DISTINGUIBLES del 409 de ErrConversationExists
	// (T2.5) — mismo criterio que TestFlowsStart_FlujoDurable_409EnLasDosRutas.
	const convExistsText = "ya existe una conversación viva para la clave"
	for _, body := range []string{bodyAdmin, bodyAPI} {
		if strings.Contains(body, convExistsText) {
			t.Fatalf("el 409 no debe confundirse con el de ErrConversationExists: %s", body)
		}
	}

	if got := inc054ContarIntakes(f.ctx, t, f.db, f.tenantID); got != antes {
		t.Fatalf("las dos puertas HTTP no deben abrir ninguna solicitud; antes=%d ahora=%d", antes, got)
	}
}

// caminoFelizEventStart es la escena que demuestra que T2/T3 NO rompieron el
// producto: SIN ella, las otras tres solo demuestran que el motor aprendió a no
// hacer nada. Con event_start, el MISMO flujo arranca CON evento padre y la
// comanda SÍ queda en intakes con su event_id poblado.
func (f inc054RedFixture) caminoFelizEventStart(t *testing.T) {
	t.Helper()
	inc054ArmaYConfirmaCarrito(f.ctx, t, f.rt, f.sessionID, inc054TelHappy, "inc054-happy")

	cid := inc054Contacto(f.ctx, t, f.contacts, f.tenantID, inc054TelHappy)
	existe, status, eventID := inc054IntakeDelContacto(f.ctx, t, f.db, f.tenantID, cid)
	if !existe {
		t.Fatal("camino feliz: el pedido confirmado por event_start debe dejar SU solicitud en intakes")
	}
	if status != "closed" {
		t.Fatalf("camino feliz: la solicitud confirmada debe quedar closed; está %q", status)
	}
	if !eventID.Valid || eventID.String == "" {
		t.Fatal("camino feliz: event_id debe quedar POBLADO (el evento que la declara) — es justo lo que el #001/#003 nunca lograban escribir")
	}
}

// TestE2E_Plan054_T4_RedCompleta reproduce, sobre la RED completa (event_start +
// keyword + fallback, el estado final de UAT tras el #003), las cuatro escenas
// que T4 exige: la degradación por keyword (#001), la degradación por fallback
// (#003), el 409 en las dos puertas HTTP (regresión de extremo a extremo) y el
// camino feliz (event_start completa con event_id poblado) — sin esta última,
// las otras tres solo demuestran que el motor aprendió a no hacer nada.
func TestE2E_Plan054_T4_RedCompleta(t *testing.T) {
	db := e2eOpenDB(t)
	ctx := context.Background()
	tenantID, sessionID := inc054Seed(t, db, "red")
	feats := inc054Feats(tenantID)
	logBuf := &bytes.Buffer{}
	rt, contacts, sender := inc054Runtime(t, db, tenantID, feats, logBuf, inc054ReglasRed())
	f := inc054RedFixture{ctx: ctx, db: db, tenantID: tenantID, sessionID: sessionID, rt: rt, contacts: contacts, sender: sender, logBuf: logBuf}

	t.Run("Hallazgo001_KeywordNoPierdeLaComanda", func(t *testing.T) {
		f.degradaSinPerderComanda(t, inc054TelKeyword, "pedido", "inc054-kw-1", "hallazgo #001 (keyword)")
	})
	t.Run("Hallazgo003_FallbackNoPierdeLaComanda", func(t *testing.T) {
		// «buenas tardes»: no casa ninguna keyword (ni «carrito» del event_start, ni
		// «pedido» del keyword) — cae a la regla kind='fallback', la fila LITERAL del
		// hallazgo #003 (bitácora §2).
		f.degradaSinPerderComanda(t, inc054TelFallback, "buenas tardes", "inc054-fb-1", "hallazgo #003 (fallback)")
	})
	t.Run("APIs409EnLasDosRutas", func(t *testing.T) {
		f.apis409EnLasDosRutas(t)
	})
	t.Run("CaminoFeliz_EventStartCompletaConEventId", func(t *testing.T) {
		f.caminoFelizEventStart(t)
	})
}

// TestE2E_Plan054_T4_SinRedEventStart aísla el corner MD-054.2 (hallazgo #001 en
// su forma MÁS literal: solo la regla kind='keyword' hacia el flujo durable, sin
// NINGUNA event_start viva) en su propio tenant. No es un criterio numerado de
// tasks.md T4, pero es la lectura más fiel de la causa raíz narrada en la
// bitácora, y demuestra el límite EXACTO de lo que T2 garantiza: la comanda
// jamás se pierde (cero intakes, cero flow_state, cero 23502), aunque sin ninguna
// event_start a la que degradar el contacto se queda sin respuesta (MD-054.1/
// D-054.8, documentado y NO resuelto por T2.7 — ver docstring de
// inc054ReglasSinRed).
func TestE2E_Plan054_T4_SinRedEventStart(t *testing.T) {
	db := e2eOpenDB(t)
	ctx := context.Background()
	tenantID, sessionID := inc054Seed(t, db, "sinred")
	feats := inc054Feats(tenantID)
	logBuf := &bytes.Buffer{}
	rt, contacts, sender := inc054Runtime(t, db, tenantID, feats, logBuf, inc054ReglasSinRed())

	antes := inc054ContarIntakes(ctx, t, db, tenantID)
	logAntes := logBuf.Len()
	msgAntes := sender.total()

	inc054Entrante(ctx, t, rt, sessionID, inc054TelSinRed, "pedido", "inc054-sinred-1")

	cid := inc054Contacto(ctx, t, contacts, tenantID, inc054TelSinRed)
	logTail := logBuf.String()[logAntes:]
	inc054AssertSinRastro(ctx, t, db, tenantID, sessionID, cid, "keyword sin red event_start", antes, logTail)

	// La consecuencia observable AQUÍ es silencio, no la oferta (MD-054.2: sin
	// ninguna event_start viva, buildOpeningOffer no tiene nada que ofrecer y
	// degradeDurableStart no reintenta para no entrar en bucle). Se afirma tal
	// cual, sin maquillarlo de "oferta": es el hueco que T2.7 deja abierto para
	// kind='keyword' (T2.7 solo valida altas de kind='fallback').
	turno := sender.desde(msgAntes)
	if len(turno) != 0 {
		t.Fatalf("keyword sin red event_start: el contacto debe quedarse SIN RESPUESTA (MD-054.2), no recibir %v", turno)
	}
}
