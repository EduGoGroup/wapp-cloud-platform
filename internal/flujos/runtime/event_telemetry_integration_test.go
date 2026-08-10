// event_telemetry_integration_test.go — Plan 043 · T5.4 (D-043.11) contra POSTGRES
// REAL (gated por WAPP_TEST_DB_DSN, mismo patrón que el resto del paquete: ver
// event_lifecycle_integration_test.go).
//
// El criterio de calidad de esta tarea exige un test que ATRAVIESE el proceso
// entero hasta flow_events —no solo el doble en memoria de event_telemetry_test.go—
// porque verificar cada capa contra dobles deja el fallo escondido en las costuras
// (lección de la Ola anterior, citada en el brief). Aquí las costuras reales son:
// el dispatch() del runtime, el PersistSink real, el InsertFlowEvent de
// PostgresRepository y la columna JSONB sin CHECK de la migración 0009.
package runtime_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
)

// t54ArmaRuntimeReal es armaRuntimeReal (event_lifecycle_integration_test.go) MÁS el
// ÚNICO cable que esa ola no necesitaba y esta sí: el PersistSink real. Sin él el
// fan-out de dispatch() nunca llega a flow_events y este fichero no probaría nada
// que event_telemetry_test.go no probara ya con el doble en memoria.
func t54ArmaRuntimeReal(t *testing.T, db *sql.DB, tenantID string, rules ...trigger.Rule) (*runtime.Runtime, *store.PostgresRepository, *events.Store, *contact.MemoryResolver) {
	t.Helper()
	ctx := context.Background()
	repo := store.NewPostgresRepository(db)
	if _, err := repo.InsertDefinition(ctx, tenantID, sampleFlow()); err != nil {
		t.Fatalf("publicar definición: %v", err)
	}
	ts := trigger.NewMemoryStore()
	for _, r := range rules {
		if _, err := ts.Insert(ctx, r); err != nil {
			t.Fatalf("insert regla: %v", err)
		}
	}
	evs := events.NewStore(db, nil)
	contacts := contact.NewMemoryResolver(nil)
	abandoner := abandonaPorEvento{db: db}
	rt := runtime.New(repo, newEngine(), &fakeSender{}, fakeResolver{tenantID: tenantID}, contacts, discardLogger(),
		runtime.WithTriggerResolver(trigger.NewConfigResolver(ts)),
		runtime.WithEventStore(evs),
		runtime.WithIntakeAbandoner(abandoner),
		runtime.WithEventSink(runtime.NewPersistSink(repo)))
	return rt, repo, evs, contacts
}

// t54FilaTelemetría es lo que el SELECT del criterio de T5.4 lee de verdad.
type t54FilaTelemetría struct {
	name    string
	payload string // JSONB serializado, tal como lo devuelve Postgres
}

// t54LeerTelemetría corre LITERALMENTE el SELECT que pide el brief («SELECT name,
// payload FROM flow_events WHERE name LIKE 'event_%'») y lo deja en el log del test
// para el informe (requisito 4 del entregable).
func t54LeerTelemetría(ctx context.Context, t *testing.T, db *sql.DB, tenantID string) []t54FilaTelemetría {
	t.Helper()
	rows, err := db.QueryContext(ctx, `
		SELECT name, payload::text FROM public.flow_events
		 WHERE tenant_id = $1 AND name LIKE 'event_%' ORDER BY id`, tenantID)
	if err != nil {
		t.Fatalf("SELECT name, payload FROM flow_events WHERE name LIKE 'event_%%': %v", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			t.Logf("cerrar filas de telemetría: %v", cerr)
		}
	}()
	var out []t54FilaTelemetría
	for rows.Next() {
		var f t54FilaTelemetría
		if err := rows.Scan(&f.name, &f.payload); err != nil {
			t.Fatalf("scan telemetría: %v", err)
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("recorrer telemetría: %v", err)
	}
	return out
}

// TestIntegration_T54_TelemetriaDeEventos_E2E es el criterio LITERAL de T5.4 sobre
// las capas reales: un recorrido que nace un cart, lo conmuta desde una encuesta,
// lo desactiva con event_stop, lo cancela por la puerta HTTP (CancelEventForTenant,
// SIN turno de mensaje) y —en un segundo evento— lo cierra por el cierre NATURAL.
// Verifica CADA fila contra columnas reales (name, payload, flow_id, flow_version)
// y termina con el SELECT exacto que pide el criterio de T6.2.
func TestIntegration_T54_TelemetriaDeEventos_E2E(t *testing.T) {
	db := openTestDB(t)
	tenantID := seedTenant(t, db)
	limpiaTenant(t, db, tenantID)
	ctx := context.Background()
	session := fmt.Sprintf("t54-tel-sess-%d", time.Now().UnixNano())
	telefono := "573001114154"

	rules := []trigger.Rule{
		{TenantID: tenantID, Kind: trigger.KindEventStart, Keyword: "carrito",
			MatchType: trigger.MatchExact, EventKind: "cart", FlowID: testFlow, Enabled: true},
		{TenantID: tenantID, Kind: trigger.KindEventStart, Keyword: "encuesta",
			MatchType: trigger.MatchExact, EventKind: "survey", FlowID: testFlow, Enabled: true},
		{TenantID: tenantID, Kind: trigger.KindEventStop, Keyword: "salir",
			MatchType: trigger.MatchExact, Enabled: true},
	}
	rt, _, _, contacts := t54ArmaRuntimeReal(t, db, tenantID, rules...)

	cid, err := contacts.Resolve(ctx, tenantID, []contact.Ref{phoneRef(t, telefono)}, "")
	if err != nil {
		t.Fatalf("resolver contacto: %v", err)
	}
	t54RecorridoTelemetria(ctx, t, rt, db, tenantID, session, telefono, cid)
	t54VerificaTelemetria(ctx, t, db, tenantID, session)
}

// t54RecorridoTelemetria ejecuta los SEIS pasos del turno (nacer, conmutar,
// desactivar, cancelar por HTTP, nacer de nuevo, cerrar natural) que disparan los
// seis efectos de ciclo de vida. Separado de la verificación para mantener la
// complejidad ciclomática de cada función dentro del límite del proyecto.
func t54RecorridoTelemetria(ctx context.Context, t *testing.T, rt *runtime.Runtime, db *sql.DB, tenantID, session, telefono, cid string) {
	t.Helper()

	// (1) Nace el carrito ⇒ event_started.
	reintenta(t, func() error {
		return rt.HandleIncoming(ctx, session, incoming(telefono, "carrito", session+"-w1"))
	})
	cartID := eventIDDeFlowState(ctx, t, db, tenantID, session, cid)
	if cartID == "" {
		t.Fatal("el carrito debe quedar activo tras nacer")
	}

	// (2) Nace la encuesta ⇒ SEGUNDO event_started; el carrito queda vivo pero no
	// activo.
	reintenta(t, func() error {
		return rt.HandleIncoming(ctx, session, incoming(telefono, "encuesta", session+"-w2"))
	})

	// (3) Volver al carrito: YA estaba vivo ⇒ event_switched (no un tercer
	// event_started).
	reintenta(t, func() error {
		return rt.HandleIncoming(ctx, session, incoming(telefono, "carrito", session+"-w3"))
	})
	if got := eventIDDeFlowState(ctx, t, db, tenantID, session, cid); got != cartID {
		t.Fatalf("el puntero debe volver al carrito (%s): %q", cartID, got)
	}

	// (4) event_stop ⇒ event_deactivated. La fila SIGUE open.
	reintenta(t, func() error {
		return rt.HandleIncoming(ctx, session, incoming(telefono, "salir", session+"-w4"))
	})
	if got := eventIDDeFlowState(ctx, t, db, tenantID, session, cid); got != "" {
		t.Fatalf("event_stop debe apagar el puntero: %q", got)
	}

	// (5) Cancelación por la puerta HTTP, SIN turno de mensaje (encargo §5 del
	// brief): CancelEventForTenant directo ⇒ event_cancelled.
	cancelado, err := rt.CancelEventForTenant(ctx, tenantID, cartID)
	if err != nil {
		t.Fatalf("CancelEventForTenant: %v", err)
	}
	if cancelado.Status != events.StatusCancelled {
		t.Fatalf("debe quedar cancelled: %+v", cancelado)
	}
	// (5-bis) La MISMA llamada, otra vez, DE VERDAD: CancelEventForTenant es
	// reintentable por diseño (Ola 4, repairCancelled) y el reintento repara sus
	// efectos colaterales SIN volver a despachar telemetría. Se ejerce contra
	// Postgres real —y no solo contra el doble en memoria— porque «exactamente una
	// fila event_cancelled» es una propiedad de flow_events, y flow_events es una
	// tabla, no un mapa: el conteo final de este recorrido (7 filas) es quien la
	// afirma.
	repetido, err := rt.CancelEventForTenant(ctx, tenantID, cartID)
	if err != nil {
		t.Fatalf("segunda CancelEventForTenant (rama idempotente): %v", err)
	}
	if !repetido.ClosedAt.Equal(cancelado.ClosedAt) {
		t.Fatalf("el reintento no debe re-sellar la fila: %+v vs %+v", cancelado, repetido)
	}

	// (6) Un SEGUNDO carrito nace (el tipo quedó libre tras cancelar el primero) y
	// se cierra NATURALMENTE con «1» (sampleFlow: root→ventas, hoja sin next) ⇒
	// TERCER event_started + event_closed.
	reintenta(t, func() error {
		return rt.HandleIncoming(ctx, session, incoming(telefono, "carrito", session+"-w5"))
	})
	segundoCartID := eventIDDeFlowState(ctx, t, db, tenantID, session, cid)
	if segundoCartID == "" || segundoCartID == cartID {
		t.Fatalf("debe nacer un SEGUNDO carrito distinto del cancelado: %q (viejo %q)", segundoCartID, cartID)
	}
	reintenta(t, func() error {
		return rt.HandleIncoming(ctx, session, incoming(telefono, "1", session+"-w6"))
	})
	if got := eventIDDeFlowState(ctx, t, db, tenantID, session, cid); got != "" {
		t.Fatalf("el cierre natural debe apagar el puntero: %q", got)
	}
}

// t54VerificaTelemetria comprueba el recorrido fila a fila contra columnas REALES y
// deja constancia del SELECT literal del criterio (entregable §4 del informe).
func t54VerificaTelemetria(ctx context.Context, t *testing.T, db *sql.DB, tenantID, session string) {
	t.Helper()

	filas := eventosDeConversacion(ctx, t, db, tenantID, session)
	if len(filas) != 3 {
		t.Fatalf("deben existir TRES filas de evento (2 cart + 1 survey), hay %d: %+v", len(filas), filas)
	}

	started := t54LeerNombrados(ctx, t, db, tenantID, runtime.EffectEventStarted)
	if len(started) != 3 {
		t.Fatalf("deben existir TRES event_started (2 cart + 1 survey), hay %d", len(started))
	}
	for _, fe := range started {
		if fe.FlowID != testFlow || fe.FlowVersion != 1 {
			t.Fatalf("event_started debe llevar el flujo real con el que nació: %+v", fe)
		}
	}
	t54AssertUno(ctx, t, db, tenantID, runtime.EffectEventSwitched, "cart")
	t54AssertUno(ctx, t, db, tenantID, runtime.EffectEventDeactivated, "cart")
	t54AssertUno(ctx, t, db, tenantID, runtime.EffectEventCancelled, "cart")
	t54AssertUno(ctx, t, db, tenantID, runtime.EffectEventClosed, "cart")
	// Ninguna de las TRES ramas prohibidas dejó fila (no hay carreras en este
	// recorrido secuencial, así que su ausencia aquí es la línea base; la prueba
	// POR MUTACIÓN de esas ramas vive en event_telemetry_test.go, contra los
	// dobles en memoria, que es donde se pueden fabricar las carreras a voluntad).
	if got := len(t54LeerNombrados(ctx, t, db, tenantID, runtime.EffectEventInactivityExpired)); got != 0 {
		t.Fatalf("este recorrido no vence ningún evento; hay %d event_inactivity_expired", got)
	}

	// --- El SELECT literal del criterio (entregable §4 del informe) ---
	sel := t54LeerTelemetría(ctx, t, db, tenantID)
	if len(sel) != 7 { // 3 started + 1 switched + 1 deactivated + 1 cancelled + 1 closed
		t.Fatalf("el SELECT debe traer 7 filas event_%%, trajo %d: %+v", len(sel), sel)
	}
	for _, f := range sel {
		t.Logf("flow_events: name=%s payload=%s", f.name, f.payload)
	}
}

// t54LeerNombrados lee del flujo real las filas flow_events con ese Name.
func t54LeerNombrados(ctx context.Context, t *testing.T, db *sql.DB, tenantID, name string) []store.FlowEvent {
	t.Helper()
	rows, err := db.QueryContext(ctx, `
		SELECT contact_id, flow_id, flow_version, kind, name, payload::text
		FROM public.flow_events WHERE tenant_id = $1 AND name = $2 ORDER BY id`, tenantID, name)
	if err != nil {
		t.Fatalf("leer flow_events(%s): %v", name, err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			t.Logf("cerrar filas: %v", cerr)
		}
	}()
	var out []store.FlowEvent
	for rows.Next() {
		var fe store.FlowEvent
		var payloadRaw string
		if err := rows.Scan(&fe.ContactID, &fe.FlowID, &fe.FlowVersion, &fe.Kind, &fe.Name, &payloadRaw); err != nil {
			t.Fatalf("scan flow_events(%s): %v", name, err)
		}
		out = append(out, fe)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("recorrer flow_events(%s): %v", name, err)
	}
	return out
}

// t54AssertUno exige EXACTAMENTE una fila con ese Name y que su payload declare el
// kind esperado (columna `kind`="event"; payload->>'kind'=eventKind, la colisión de
// nombres CONSCIENTE que documenta event_effects.go).
// El COUNT(*) no es decorativo: QueryRow devuelve la PRIMERA fila sin error cuando
// hay dos, así que sin él este helper diría «exactamente una» y estaría midiendo
// «al menos una» — justo lo que el criterio de T6.2 no acepta.
func t54AssertUno(ctx context.Context, t *testing.T, db *sql.DB, tenantID, name, eventKind string) {
	t.Helper()
	var n int
	var kind, payloadKind string
	err := db.QueryRowContext(ctx, `
		SELECT count(*), min(kind), min(payload->>'kind') FROM public.flow_events
		 WHERE tenant_id = $1 AND name = $2`, tenantID, name).Scan(&n, &kind, &payloadKind)
	if err != nil {
		t.Fatalf("esperaba EXACTAMENTE una fila %q: %v", name, err)
	}
	if n != 1 {
		t.Fatalf("esperaba EXACTAMENTE una fila %q, hay %d", name, n)
	}
	if kind != "event" {
		t.Fatalf("%s: columna kind = %q, quiero \"event\"", name, kind)
	}
	if payloadKind != eventKind {
		t.Fatalf("%s: payload->>'kind' = %q, quiero %q", name, payloadKind, eventKind)
	}
}

// TestIntegration_T54_EventStartedDeTipoMenu_FlowIDVacioEnColumnaNotNull cierra la
// única afirmación de T5.4 que solo vivía contra el repositorio EN MEMORIA: el tipo
// `menu` nace con FlowID="" y FlowVersion=0 (D-043.3) y flow_events.flow_id es TEXT
// NOT NULL (migración 0009). Que la cadena vacía satisfaga un NOT NULL es cierto en Postgres, pero
// «es cierto en Postgres» y «esta fila entra» son dos afirmaciones distintas: el
// MemoryRepository no impone la columna, así que sin este test un CHECK futuro sobre
// flow_id rompería la telemetría del menú y ningún test del paquete se enteraría.
func TestIntegration_T54_EventStartedDeTipoMenu_FlowIDVacioEnColumnaNotNull(t *testing.T) {
	db := openTestDB(t)
	tenantID := seedTenant(t, db)
	limpiaTenant(t, db, tenantID)
	ctx := context.Background()
	session := fmt.Sprintf("t54-menu-sess-%d", time.Now().UnixNano())

	repo := store.NewPostgresRepository(db)
	if _, err := repo.InsertDefinition(ctx, tenantID, sampleFlow()); err != nil {
		t.Fatalf("publicar definición: %v", err)
	}
	ts := trigger.NewMemoryStore()
	// La regla de un tenant REAL para el menú NO lleva flujo: el menú no es una fila
	// de flow_definitions.
	if _, err := ts.Insert(ctx, trigger.Rule{
		TenantID: tenantID, Kind: trigger.KindEventStart, Keyword: "menu",
		MatchType: trigger.MatchExact, EventKind: "menu", FlowID: "", Enabled: true,
	}); err != nil {
		t.Fatalf("insert regla: %v", err)
	}
	menu := events.Menu{Options: []events.MenuOption{{Number: 1, Action: events.ActionStart, Kind: "cart"}}}
	rt := runtime.New(repo, newEngine(), &fakeSender{}, fakeResolver{tenantID: tenantID},
		contact.NewMemoryResolver(nil), discardLogger(),
		runtime.WithTriggerResolver(trigger.NewConfigResolver(ts)),
		runtime.WithEventStore(events.NewStore(db, nil)),
		runtime.WithEventSink(runtime.NewPersistSink(repo)),
		runtime.WithDispatcher(fakeDispatcher{menu: menu}),
		runtime.WithFlowForKind(fakeFlowForKind{flow: testFlow}))

	reintenta(t, func() error {
		return rt.HandleIncoming(ctx, session, incoming("573001114155", "menu", session+"-m1"))
	})

	var n, flowVersion int
	var flowID, payload string
	if err := db.QueryRowContext(ctx, `
		SELECT count(*), min(flow_id), min(flow_version), min(payload::text)
		  FROM public.flow_events WHERE tenant_id = $1 AND name = $2`,
		tenantID, runtime.EffectEventStarted).Scan(&n, &flowID, &flowVersion, &payload); err != nil {
		t.Fatalf("leer el event_started del menú: %v", err)
	}
	if n != 1 {
		t.Fatalf("el nacimiento del menú debe dejar UNA fila event_started, hay %d", n)
	}
	if flowID != "" || flowVersion != 0 {
		t.Fatalf("el tipo `menu` nace con (flow_id, flow_version) = (\"\", 0); la fila real trae (%q, %d)", flowID, flowVersion)
	}
	if !strings.Contains(payload, `"kind": "menu"`) {
		t.Fatalf("el payload debe declarar kind=menu: %s", payload)
	}
}
