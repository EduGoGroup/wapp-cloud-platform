// conversationeventcancel_t62_e2e_integration_test.go — T6.2 del Plan 043 (Ola 6):
// el e2e de la cancelación desde la app, atravesando la cadena ENTERA como su
// ancestro directo (conversationchain_w45_e2e_integration_test.go): trigger real
// («carrito») → birthEvent real → engine + cart REALES sobre el catálogo de
// tenant_content → PersistSink real (proyector cart real) → Postgres real → cancel
// por el HANDLER PÚBLICO (mux de publicapi.Register, canceller = el *runtime.Runtime
// real), NO por el store interno.
//
// A diferencia de conversationeventcancel_e2e_integration_test.go (T4.2/T4.3, que
// SIEMBRA el evento/intake/flow_state a mano para aislar el transporte), aquí NADA
// se siembra con INSERT: el evento vivo y el intake colgante nacen de la MISMA
// conversación real que T6.1 usa («carrito» ⇒ agregar líneas), y este archivo añade
// las DOS condiciones del criterio de T6.2 que ese test no cubre: el efecto
// event_cancelled en flow_events y que el cliente pueda abrir un carrito NUEVO
// inmediatamente después del cancel.
//
// Corre contra WAPP_TEST_DB_DSN (se omite sin ella; WAPP_TEST_REQUIRE_DB la exige).
// Datos con prefijo t62-.
package publicapi_test

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

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
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
)

const (
	t62Flujo    = "t62-flujo-carrito"
	t62Telefono = "573001116201"
	t62Key      = "key-t62-duena" // credencial de la dueña: intakes.write
)

func t62CartFlow() model.Flow {
	return model.Flow{
		FlowID:  t62Flujo,
		Initial: "cart",
		Nodes: map[string]model.Node{
			"cart": {Type: "cart", Content: &model.ContentRef{Source: "json", Ref: "t62-catalogo"}},
		},
	}
}

// t62Seed siembra tenant + fleet_sessions (el resolver de tenant REAL sobre
// fleet_sessions hace su trabajo, igual que t45vSeed/t61Seed).
func t62Seed(t *testing.T, db *sql.DB) (tenantID, sessionID string) {
	t.Helper()
	ctx := context.Background()
	sessionID = fmt.Sprintf("t62-sess-%d", time.Now().UnixNano())

	if err := db.QueryRowContext(ctx,
		`INSERT INTO public.tenants (slug, display_name) VALUES ($1, $2) RETURNING id::text`,
		"t62-cancel-"+sessionID, "T62 e2e cancelación desde la app").Scan(&tenantID); err != nil {
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
		VALUES ($1::uuid, 't62-edge', $2, 'online', 'active')
	`, tenantID, sessionID); err != nil {
		t.Fatalf("sembrando fleet_sessions: %v", err)
	}
	return tenantID, sessionID
}

// t62Runtime arma el runtime de PRODUCCIÓN sobre Postgres real: el mismo objeto
// (*runtime.Runtime) satisface trigger real, cart real y ConversationEventCanceller
// del handler público de cancelación — es EL canceller de producción, no un doble.
func t62Runtime(t *testing.T, db *sql.DB, tenantID string) (*flowruntime.Runtime, *events.Store, *contact.MemoryResolver) {
	t.Helper()
	ctx := context.Background()

	repo := flowstore.NewPostgresRepository(db)
	if _, err := repo.InsertDefinition(ctx, tenantID, t62CartFlow()); err != nil {
		t.Fatalf("publicar flujo carrito: %v", err)
	}
	if err := repo.UpsertTenantContent(ctx, tenantID, "t62-catalogo", []byte(t45vCatalogo)); err != nil {
		t.Fatalf("sembrar catálogo: %v", err)
	}

	reg := modules.NewRegistry()
	reg.Register(cart.New())
	eng := engine.New(reg, engine.WithContentSource(
		content.NewRouter(content.NewStatic(), content.NewJSON(repo))))

	ts := trigger.NewMemoryStore()
	if _, err := ts.Insert(ctx, trigger.Rule{
		TenantID: tenantID, Kind: trigger.KindEventStart, Keyword: "carrito",
		MatchType: trigger.MatchExact, EventKind: "cart", FlowID: t62Flujo, Enabled: true,
	}); err != nil {
		t.Fatalf("insert regla event_start: %v", err)
	}

	eventStore := events.NewStore(db, nil)
	intakeStore := intakes.NewPostgres(db)
	contacts := contact.NewMemoryResolver(nil)

	rt := flowruntime.New(repo, eng, &t45vSender{}, flowruntime.NewPostgresTenantResolver(db),
		contacts, e2eLogger(),
		flowruntime.WithEventSink(flowruntime.NewPersistSink(repo,
			cart.NewProjector(repo, intakeStore, intakeStore, intakes.NewPostgresBuyerData(db, nil))).
			WithDecisionThread(eventStore)),
		flowruntime.WithResumePolicy(cart.NodeTypeCart, cart.NewResumePolicy(repo)),
		flowruntime.WithTriggerResolver(trigger.NewConfigResolver(ts)),
		flowruntime.WithEventStore(eventStore),
		flowruntime.WithIntakeAbandoner(intakes.NewService(intakeStore)),
	)
	return rt, eventStore, contacts
}

// t62FlowStateEventID lee flow_state.event_id (condición 3: «flow_state liberado»).
func t62FlowStateEventID(t *testing.T, db *sql.DB, tenantID, sessionID, contactID string) (found bool, eventID string) {
	t.Helper()
	var ev sql.NullString
	err := db.QueryRowContext(context.Background(), `
		SELECT event_id FROM public.flow_state
		WHERE tenant_id = $1::uuid AND session_id = $2 AND contact_id = $3::uuid
	`, tenantID, sessionID, contactID).Scan(&ev)
	switch {
	case err == sql.ErrNoRows:
		return false, ""
	case err != nil:
		t.Fatalf("leyendo flow_state.event_id: %v", err)
	}
	return true, ev.String
}

// t62ContarEfectoCancelado cuenta las filas flow_events del efecto event_cancelled
// PARA ESTE evento concreto (condición 4): filtra por payload->>'history_id', que es
// el identificador ESTABLE que emitEventEffect graba (ver event_effects.go) — filtrar
// por columna flow_id no serviría para kind=cart (aquí sí hay flow_id, pero se
// prefiere el mismo campo que garantiza unicidad para TODOS los tipos, incluido
// menú).
func t62ContarEfectoCancelado(t *testing.T, db *sql.DB, tenantID, historyID string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), `
		SELECT count(*) FROM public.flow_events
		WHERE tenant_id = $1 AND kind = 'event' AND name = 'event_cancelled'
		  AND payload->>'history_id' = $2
	`, tenantID, historyID).Scan(&n); err != nil {
		t.Fatalf("contando flow_events(event_cancelled): %v", err)
	}
	return n
}

// t62Intake lee status y cuenta de líneas del intake que declara el evento dado
// (condición 2: «intake en abandoned con sus líneas»).
func t62Intake(t *testing.T, db *sql.DB, tenantID, eventID string) (status string, lineas int) {
	t.Helper()
	if err := db.QueryRowContext(context.Background(), `
		SELECT i.status, (SELECT count(*) FROM public.intake_items it WHERE it.intake_id = i.id)
		FROM public.intakes i WHERE i.tenant_id = $1 AND i.event_id = $2::uuid
	`, tenantID, eventID).Scan(&status, &lineas); err != nil {
		t.Fatalf("leyendo el intake del evento %s: %v", eventID, err)
	}
	return status, lineas
}

// t62Lineas devuelve el CONTENIDO de las líneas del intake del evento, en orden
// estable, con la forma "sku|label|qty".
//
// ⚠️ Por qué no basta con contar (revisión de la Ola 6): el criterio de T6.2 dice
// «intake en `abandoned` CON SUS LÍNEAS», y comparar dos conteos NO lo comprueba —
// una mutación que deja las mismas N filas con el sku, la etiqueta y la cantidad
// destruidos (`UPDATE intake_items SET qty=0, sku='XXX'`) sobrevivía con el test en
// verde. «Siguen ahí» es una afirmación sobre lo que DICEN las filas, no sobre
// cuántas hay.
func t62Lineas(t *testing.T, db *sql.DB, tenantID, eventID string) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `
		SELECT it.sku, it.label, it.qty
		  FROM public.intake_items it
		  JOIN public.intakes i ON i.id = it.intake_id
		 WHERE i.tenant_id = $1 AND i.event_id = $2::uuid
		 ORDER BY it.sku, it.id
	`, tenantID, eventID)
	if err != nil {
		t.Fatalf("leyendo las líneas del intake de %s: %v", eventID, err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			t.Fatalf("cerrando filas de líneas: %v", cerr)
		}
	}()
	var out []string
	for rows.Next() {
		var sku, label string
		var qty int
		if err := rows.Scan(&sku, &label, &qty); err != nil {
			t.Fatalf("leyendo línea: %v", err)
		}
		out = append(out, fmt.Sprintf("%s|%s|%d", sku, label, qty))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("recorriendo líneas: %v", err)
	}
	return out
}

// TestE2E_T62_CancelacionDesdeLaApp_CicloCompleto es el criterio de T6.2 recorrido
// entero por la cadena real: evento vivo + intake colgando (nacidos de un «carrito»
// real, sin un solo INSERT que siembre la ligadura), cancelado por el HANDLER
// PÚBLICO, con las SEIS condiciones verificadas una a una en Postgres.
func TestE2E_T62_CancelacionDesdeLaApp_CicloCompleto(t *testing.T) {
	db := e2eOpenDB(t)
	ctx := context.Background()
	tenantID, sessionID := t62Seed(t, db)

	feats := conTodosLosTipos()
	for _, f := range events.KindFeatures() {
		feats.Enable(tenantID, f)
	}
	rt, evs, contacts := t62Runtime(t, db, tenantID)

	ref, err := contact.NewRef(contact.KindPhoneE164, t62Telefono)
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
			From: t62Telefono, Text: text, WaMessageId: fmt.Sprintf("t62-w%d", wa),
		}); err != nil {
			t.Fatalf("HandleIncoming(%q): %v", text, err)
		}
	}

	// ── Setup real: «carrito» pare el evento; «1 → 1 → 2 → 2» agrega Café x2. NINGÚN
	// INSERT siembra la ligadura evento↔intake — nace del proyector de producción.
	entrante("carrito")
	ev, alive, err := evs.GetAliveByKind(ctx, tenantID, sessionID, cid, "cart")
	if err != nil || !alive {
		t.Fatalf("GetAliveByKind(cart) tras «carrito»: ok=%v err=%v", alive, err)
	}
	for _, in := range []string{"1", "1", "2", "2"} {
		entrante(in)
	}
	contenidoAntes, lineasAntes := t62QuiereParVivoAntesDelCancel(t, db, tenantID, sessionID, cid, ev.ID)

	// ── La dueña cancela por el HANDLER PÚBLICO real (mux de publicapi.Register).
	keys := intakesKeys()
	keys[t62Key] = testIdentity{TenantID: tenantID, Subject: "t62-duena", Grants: []string{"intakes.write"}}
	api := newAPI(publicapi.Deps{
		EventCanceller:     rt,
		ConversationEvents: evs,
		Entitlements:       feats,
	}, keys)
	rec := call(api, t62Key, http.MethodPost, cancelURL(ev.ID), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel: code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := decodeCancelado(t, rec.Body.Bytes()); got.Status != "cancelled" || got.ClosedAt == "" {
		t.Fatalf("cuerpo del cancel=%+v; quiero cancelled con closed_at sellado", got)
	}

	t62QuiereSeisCondiciones(t, db, tenantID, sessionID, cid, ev, contenidoAntes, lineasAntes, entrante, evs)
}

// t62QuiereParVivoAntesDelCancel fija la FOTO DE PARTIDA: intake `open` con su línea
// Café x2 (contenido exacto, no un conteo) y flow_state apuntando al evento. Sin esta
// foto, comparar «antes» con «después» se cumpliría sobre nada — dos listas vacías
// también son iguales.
func t62QuiereParVivoAntesDelCancel(t *testing.T, db *sql.DB, tenantID, sessionID, cid, eventID string) ([]string, int) {
	t.Helper()
	statusAntes, lineasAntes := t62Intake(t, db, tenantID, eventID)
	if statusAntes != intakes.StatusOpen || lineasAntes < 1 {
		t.Fatalf("el intake colgante debe nacer open con su línea; got (status=%q, líneas=%d)", statusAntes, lineasAntes)
	}
	contenidoAntes := t62Lineas(t, db, tenantID, eventID)
	if len(contenidoAntes) != 1 || contenidoAntes[0] != "CAFE|Café|2" {
		t.Fatalf("el intake colgante debe llevar exactamente la línea Café x2; got=%v", contenidoAntes)
	}
	if found, got := t62FlowStateEventID(t, db, tenantID, sessionID, cid); !found || got != eventID {
		t.Fatalf("flow_state.event_id debe apuntar al evento ANTES del cancel; found=%v got=%q quiero=%q", found, got, eventID)
	}
	return contenidoAntes, lineasAntes
}

// t62QuiereSeisCondiciones comprueba, una a una y contra Postgres, las SEIS
// condiciones del criterio de T6.2 sobre el evento ya cancelado por el handler
// público. Vive aparte del guion (mismo patrón que los t45vQuiereX del ancestro)
// para que la secuencia de la conversación se lea de un vistazo y las aserciones
// tengan nombre propio.
func t62QuiereSeisCondiciones(t *testing.T, db *sql.DB, tenantID, sessionID, cid string,
	ev events.Event, contenidoAntes []string, lineasAntes int, entrante func(string), evs *events.Store) {
	t.Helper()
	ctx := context.Background()
	// ── Condición 1: evento `cancelled`.
	var status string
	var closedAt sql.NullTime
	if err := db.QueryRowContext(ctx,
		`SELECT status, closed_at FROM public.conversation_events WHERE id = $1 AND tenant_id = $2::uuid`,
		ev.ID, tenantID).Scan(&status, &closedAt); err != nil {
		t.Fatalf("releyendo el evento: %v", err)
	}
	if status != string(events.StatusCancelled) {
		t.Fatalf("condición 1: evento debe quedar cancelled; está %q", status)
	}
	// ── Condición 2: con `closed_at` sellado.
	if !closedAt.Valid {
		t.Fatalf("condición 2: closed_at debe quedar sellado (no NULL)")
	}

	// ── Condición 3: intake en `abandoned` CON SUS LÍNEAS (no solo el estado: se
	// comprueba que las líneas SIGUEN AHÍ, no solo que el conteo no cambió de signo).
	statusDespues, lineasDespues := t62Intake(t, db, tenantID, ev.ID)
	if statusDespues != intakes.StatusAbandoned {
		t.Fatalf("condición 3a: intake debe quedar abandoned; está %q", statusDespues)
	}
	if lineasDespues != lineasAntes {
		t.Fatalf("condición 3b: las líneas deben seguir INTACTAS; antes=%d después=%d", lineasAntes, lineasDespues)
	}
	// 3b (de verdad): el CONTENIDO, no el conteo. Ver el contrato de t62Lineas.
	if contenidoDespues := t62Lineas(t, db, tenantID, ev.ID); !slices.Equal(contenidoDespues, contenidoAntes) {
		t.Fatalf("condición 3b: las líneas deben seguir INTACTAS en su CONTENIDO; antes=%v después=%v",
			contenidoAntes, contenidoDespues)
	}

	// ── Condición 4: flow_state LIBERADO — el puntero al evento cancelado se apaga
	// (NULL), la conversación queda LIBRE para abrir otra cosa.
	if found, got := t62FlowStateEventID(t, db, tenantID, sessionID, cid); found && got != "" {
		t.Fatalf("condición 4: flow_state.event_id debe quedar NULL tras el cancel; found=%v got=%q", found, got)
	}

	// ── Condición 5: efecto `event_cancelled` en flow_events (bitácora append-only,
	// D-043.11) — EXACTAMENTE una fila para ESTE evento.
	if n := t62ContarEfectoCancelado(t, db, tenantID, ev.HistoryID); n != 1 {
		t.Fatalf("condición 5: flow_events debe tener EXACTAMENTE 1 fila event_cancelled de %s; hay %d",
			ev.HistoryID, n)
	}

	// ── Condición 6: el cliente puede abrir un carrito NUEVO inmediatamente después
	// — el tipo `cart` quedó libre porque el índice único parcial solo cuenta eventos
	// `open`, y el cancelado ya no lo es.
	entrante("carrito")
	nuevo, aliveNuevo, err := evs.GetAliveByKind(ctx, tenantID, sessionID, cid, "cart")
	if err != nil || !aliveNuevo {
		t.Fatalf("condición 6: tras cancelar debe poder abrirse un carrito NUEVO; GetAliveByKind ok=%v err=%v", aliveNuevo, err)
	}
	if nuevo.ID == ev.ID {
		t.Fatalf("condición 6: el carrito nuevo debe ser OTRA fila, no la cancelada; got=%s", nuevo.ID)
	}
	if nuevo.Status != events.StatusOpen {
		t.Fatalf("condición 6: el carrito nuevo debe nacer open; está %q", nuevo.Status)
	}
}
