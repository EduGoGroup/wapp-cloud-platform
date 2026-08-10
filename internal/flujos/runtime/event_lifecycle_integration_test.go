// event_lifecycle_integration_test.go — Plan 043 · Ola 4 contra POSTGRES REAL
// (gated por WAPP_TEST_DB_DSN, mismo patrón que el resto del paquete).
//
// El e2e de T4.1 atraviesa runtime + engine + almacenes REALES a propósito: la
// lección de la ola anterior es que verificar cada capa contra dobles deja el
// fallo escondido en las costuras. Aquí las costuras bajo prueba son de verdad:
// el CAS `AND status='open'` del store de eventos, el closed_at sellado por SQL,
// el índice único parcial que libera el tipo y el upsert de flow_state que apaga
// event_id. Los únicos dobles son los planos que NO están bajo prueba (sender,
// resolver de tenant, reglas en memoria).
package runtime_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// abandonaConElServicio es el adaptador REAL del test: la misma puerta que cablea
// el bootstrap (Service.Abandon, que por dentro es SetStatus y jamás
// Store.UpdateStatus — ADR-0029 · E-11.5), para que el abandono pase por
// CanTransition de verdad y traiga su idempotencia de verdad.
type abandonaConElServicio struct{ svc *intakes.Service }

func (a abandonaConElServicio) AbandonIntake(ctx context.Context, tenantID, intakeID string) error {
	return a.svc.Abandon(ctx, tenantID, intakeID)
}

// armaRuntimeReal construye el runtime de la ola sobre los almacenes Postgres
// reales, con la definición de menú publicada y las reglas dadas.
func armaRuntimeReal(t *testing.T, db *sql.DB, tenantID string, rules ...trigger.Rule) (*runtime.Runtime, *store.PostgresRepository, *events.Store, *contact.MemoryResolver) {
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
	abandoner := abandonaConElServicio{svc: intakes.NewService(intakes.NewPostgres(db))}
	rt := runtime.New(repo, newEngine(), &fakeSender{}, fakeResolver{tenantID: tenantID}, contacts, discardLogger(),
		runtime.WithTriggerResolver(trigger.NewConfigResolver(ts)),
		runtime.WithEventStore(evs),
		runtime.WithIntakeAbandoner(abandoner))
	return rt, repo, evs, contacts
}

// filaEvento lee lo que la BD guardó DE VERDAD del evento: los asserts van sobre
// esto, no sobre lo que un store devolvió.
type filaEvento struct {
	id       string
	status   string
	closedAt sql.NullTime
}

func eventosDeConversacion(ctx context.Context, t *testing.T, db *sql.DB, tenantID, sessionID string) []filaEvento {
	t.Helper()
	rows, err := db.QueryContext(ctx, `
		SELECT id, status, closed_at FROM public.conversation_events
		 WHERE tenant_id = $1 AND session_id = $2 ORDER BY created_at, id`, tenantID, sessionID)
	if err != nil {
		t.Fatalf("leer conversation_events: %v", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			t.Logf("cerrar filas de eventos: %v", cerr)
		}
	}()
	var out []filaEvento
	for rows.Next() {
		var f filaEvento
		if err := rows.Scan(&f.id, &f.status, &f.closedAt); err != nil {
			t.Fatalf("scan evento: %v", err)
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("recorrer eventos: %v", err)
	}
	return out
}

// eventIDDeFlowState lee flow_state.event_id crudo (NULL ⇒ "").
func eventIDDeFlowState(ctx context.Context, t *testing.T, db *sql.DB, tenantID, sessionID, contactID string) string {
	t.Helper()
	var eventID sql.NullString
	err := db.QueryRowContext(ctx, `
		SELECT event_id::text FROM public.flow_state
		 WHERE tenant_id = $1 AND session_id = $2 AND contact_id = $3`,
		tenantID, sessionID, contactID).Scan(&eventID)
	if err != nil {
		t.Fatalf("leer flow_state.event_id: %v", err)
	}
	return eventID.String
}

// limpiaTenant borra el tenant al terminar; el CASCADE se lleva sus filas.
func limpiaTenant(t *testing.T, db *sql.DB, tenantID string) {
	t.Cleanup(func() {
		if _, err := db.ExecContext(context.Background(),
			`DELETE FROM public.tenants WHERE id = $1`, tenantID); err != nil {
			t.Logf("limpiar tenant %s: %v", tenantID, err)
		}
	})
}

// reintenta corre fn una segunda vez si falló: los tests de integración comparten
// BD con otra corrida concurrente y un fallo transitorio no es un hallazgo.
func reintenta(t *testing.T, fn func() error) {
	t.Helper()
	if err := fn(); err != nil {
		t.Logf("primer intento falló (%v); reintentando una vez", err)
		if err := fn(); err != nil {
			t.Fatalf("segundo intento: %v", err)
		}
	}
}

// TestIntegration_T41_CierreNatural_E2E es el criterio LITERAL de T4.1 sobre las
// capas reales: completar el flujo del evento ⇒ status='closed' con closed_at no
// nulo y flow_state.event_id NULL; el tipo queda libre y un «carrito» nuevo crea
// OTRA fila (el índice único parcial ya no colisiona); la cerrada sigue en la
// tabla (INV-09: nada se borra).
func TestIntegration_T41_CierreNatural_E2E(t *testing.T) {
	db := openTestDB(t)
	tenantID := seedTenant(t, db)
	limpiaTenant(t, db, tenantID)
	ctx := context.Background()
	regla := trigger.Rule{
		TenantID: tenantID, Kind: trigger.KindEventStart, Keyword: "carrito",
		MatchType: trigger.MatchExact, EventKind: "cart", FlowID: testFlow, Enabled: true,
	}
	rt, _, _, contacts := armaRuntimeReal(t, db, tenantID, regla)
	session := fmt.Sprintf("t41-sess-%d", time.Now().UnixNano())
	telefono := "573001114141"

	reintenta(t, func() error {
		return rt.HandleIncoming(ctx, session, incoming(telefono, "carrito", session+"-w1"))
	})
	cid, err := contacts.Resolve(ctx, tenantID, []contact.Ref{phoneRef(t, telefono)}, "")
	if err != nil {
		t.Fatalf("resolver contacto: %v", err)
	}
	nacidos := eventosDeConversacion(ctx, t, db, tenantID, session)
	if len(nacidos) != 1 || nacidos[0].status != "open" {
		t.Fatalf("debe nacer UN evento open, got %+v", nacidos)
	}
	if got := eventIDDeFlowState(ctx, t, db, tenantID, session, cid); got != nacidos[0].id {
		t.Fatalf("el puntero debe apuntar al nacido (%s), y vale %q", nacidos[0].id, got)
	}

	// «1» → message sin next ⇒ el flujo del evento TERMINA.
	reintenta(t, func() error {
		return rt.HandleIncoming(ctx, session, incoming(telefono, "1", session+"-w2"))
	})
	tras := eventosDeConversacion(ctx, t, db, tenantID, session)
	if len(tras) != 1 || tras[0].status != "closed" {
		t.Fatalf("completar el flujo debe dejar la fila closed, got %+v", tras)
	}
	if !tras[0].closedAt.Valid {
		t.Fatal("closed_at debe quedar NO NULO en el cierre natural")
	}
	if got := eventIDDeFlowState(ctx, t, db, tenantID, session, cid); got != "" {
		t.Fatalf("flow_state.event_id debe quedar NULL, y vale %q", got)
	}

	// El tipo quedó libre: OTRO «carrito» inserta una SEGUNDA fila sin chocar con
	// conversation_events_one_alive_per_kind_idx, y la cerrada sobrevive.
	reintenta(t, func() error {
		return rt.HandleIncoming(ctx, session, incoming(telefono, "carrito", session+"-w3"))
	})
	final := eventosDeConversacion(ctx, t, db, tenantID, session)
	if len(final) != 2 {
		t.Fatalf("deben quedar DOS filas (la cerrada no se borra, INV-09), hay %d: %+v", len(final), final)
	}
	if final[0].id != tras[0].id || final[0].status != "closed" {
		t.Fatalf("la primera fila sigue siendo la cerrada: %+v", final[0])
	}
	if final[1].status != "open" || final[1].id == final[0].id {
		t.Fatalf("la segunda debe ser un evento NUEVO y open: %+v", final[1])
	}
}

// siembraIntakeConLineas crea la solicitud open con sus líneas, con SQL directo:
// lo que está bajo prueba es qué le pasa al CANCELAR, no el ciclo que la produce.
func siembraIntakeConLineas(ctx context.Context, t *testing.T, db *sql.DB, tenantID, sessionID, contactID string, skus ...string) string {
	t.Helper()
	var intakeID string
	err := db.QueryRowContext(ctx, `
		INSERT INTO public.intakes (id, tenant_id, contact_id, session_id, status)
		VALUES (gen_random_uuid(), $1, $2, $3, 'open') RETURNING id`,
		tenantID, contactID, sessionID).Scan(&intakeID)
	if err != nil {
		t.Fatalf("sembrar intake: %v", err)
	}
	for i, sku := range skus {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO public.intake_items (intake_id, sku, label, customization, qty, unit_price)
			VALUES ($1, $2, $3, '', $4, 10.5)`, intakeID, sku, "Línea "+sku, i+1); err != nil {
			t.Fatalf("sembrar línea %s: %v", sku, err)
		}
	}
	return intakeID
}

// TestIntegration_T43_CancelarAbandonaElIntakeConSusLineasIntactas es el criterio
// LITERAL de T4.3 sobre Postgres: carrito con 2 líneas ⇒ cancelar el evento ⇒ el
// intake queda en `abandoned` con sus intake_items INTACTOS y consultable; el
// puntero de la conversación se apaga; y la segunda cancelación es idempotente
// contra la fila real (mismo closed_at, sin segundo abandono).
func TestIntegration_T43_CancelarAbandonaElIntakeConSusLineasIntactas(t *testing.T) {
	db := openTestDB(t)
	tenantID := seedTenant(t, db)
	limpiaTenant(t, db, tenantID)
	ctx := context.Background()
	rt, repo, evs, _ := armaRuntimeReal(t, db, tenantID)
	session := fmt.Sprintf("t41-sess-%d", time.Now().UnixNano())
	contactID := "aaaaaaaa-0000-4000-8000-000000004143"

	intakeID := siembraIntakeConLineas(ctx, t, db, tenantID, session, contactID, "t41-sku-1", "t41-sku-2")
	ev := siembraEventoApuntado(ctx, t, evs, repo, tenantID, session, contactID, intakeID)

	got, err := rt.CancelEventForTenant(ctx, tenantID, ev.ID)
	if err != nil {
		t.Fatalf("CancelEventForTenant: %v", err)
	}
	if got.Status != events.StatusCancelled || got.ClosedAt.IsZero() {
		t.Fatalf("la fila devuelta debe venir cancelled con closed_at, got %+v", got)
	}

	// La fila REAL quedó cancelada y sellada.
	filas := eventosDeConversacion(ctx, t, db, tenantID, session)
	if len(filas) != 1 || filas[0].status != "cancelled" || !filas[0].closedAt.Valid {
		t.Fatalf("conversation_events debe quedar cancelled con closed_at: %+v", filas)
	}
	// El intake quedó ABANDONADO — jamás borrado (INV-09)…
	if got := statusDeIntake(ctx, t, db, intakeID); got != intakes.StatusAbandoned {
		t.Fatalf("el intake debe quedar en abandoned, y está en %q", got)
	}
	// …con sus DOS líneas intactas.
	if lineas := lineasDeIntake(ctx, t, db, intakeID); lineas != 2 {
		t.Fatalf("las líneas quedan INTACTAS: esperaba 2, hay %d", lineas)
	}
	// Y el puntero de su conversación se apagó.
	if got := eventIDDeFlowState(ctx, t, db, tenantID, session, contactID); got != "" {
		t.Fatalf("flow_state.event_id debe quedar NULL, y vale %q", got)
	}

	// Idempotencia contra la fila REAL: la segunda llamada no cambia nada.
	segunda, err := rt.CancelEventForTenant(ctx, tenantID, ev.ID)
	if err != nil {
		t.Fatalf("segunda cancelación: %v", err)
	}
	if !segunda.ClosedAt.Equal(got.ClosedAt) || segunda.Status != events.StatusCancelled {
		t.Fatalf("la fila NO cambia entre llamadas: %+v vs %+v", got, segunda)
	}
	if s := statusDeIntake(ctx, t, db, intakeID); s != intakes.StatusAbandoned {
		t.Fatalf("el intake sigue abandoned (sin dobles escrituras): %q", s)
	}
}

// siembraEventoApuntado crea el evento cart (real, por el store) ligado a la
// solicitud dada y deja el flow_state de su conversación apuntándolo.
func siembraEventoApuntado(ctx context.Context, t *testing.T, evs *events.Store, repo *store.PostgresRepository, tenantID, session, contactID, intakeID string) events.Event {
	t.Helper()
	ev, err := evs.CreateEvent(ctx, events.NewEvent{
		TenantID: tenantID, SessionID: session, ContactID: contactID,
		Kind: "cart", FlowID: testFlow, FlowVersion: 1, IntakeID: intakeID,
	})
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}
	if err := repo.Save(ctx, model.Conversation{
		TenantID: tenantID, SessionID: session, ContactID: contactID,
		FlowID: testFlow, FlowVersion: 1, CurrentNode: "root", EventID: ev.ID,
	}); err != nil {
		t.Fatalf("sembrar flow_state: %v", err)
	}
	return ev
}

// statusDeIntake lee el estado crudo de la solicitud; que la consulta FUNCIONE es
// parte del criterio («sigue siendo consultable»).
func statusDeIntake(ctx context.Context, t *testing.T, db *sql.DB, intakeID string) string {
	t.Helper()
	var status string
	if err := db.QueryRowContext(ctx,
		`SELECT status FROM public.intakes WHERE id = $1`, intakeID).Scan(&status); err != nil {
		t.Fatalf("el intake debe seguir CONSULTABLE: %v", err)
	}
	return status
}

// lineasDeIntake cuenta las líneas que le quedan a la solicitud.
func lineasDeIntake(ctx context.Context, t *testing.T, db *sql.DB, intakeID string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM public.intake_items WHERE intake_id = $1`, intakeID).Scan(&n); err != nil {
		t.Fatalf("contar líneas: %v", err)
	}
	return n
}

// TestIntegration_T43_EventoSinIntakeSeCancelaSinError: la otra mitad del
// criterio de T4.3, contra la BD real.
func TestIntegration_T43_EventoSinIntakeSeCancelaSinError(t *testing.T) {
	db := openTestDB(t)
	tenantID := seedTenant(t, db)
	limpiaTenant(t, db, tenantID)
	ctx := context.Background()
	rt, _, evs, _ := armaRuntimeReal(t, db, tenantID)
	session := fmt.Sprintf("t41-sess-%d", time.Now().UnixNano())

	ev, err := evs.CreateEvent(ctx, events.NewEvent{
		TenantID: tenantID, SessionID: session,
		ContactID: "bbbbbbbb-0000-4000-8000-000000004143",
		Kind:      "survey", FlowID: testFlow, FlowVersion: 1,
	})
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}

	got, err := rt.CancelEventForTenant(ctx, tenantID, ev.ID)
	if err != nil {
		t.Fatalf("un evento sin intake debe cancelarse sin error: %v", err)
	}
	if got.Status != events.StatusCancelled || got.ClosedAt.IsZero() {
		t.Fatalf("debe quedar cancelled con closed_at: %+v", got)
	}
}
