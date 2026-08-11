// conversationeventcancel_e2e_integration_test.go es el criterio de T4.2+T4.3 del
// Plan 043 recorrido DESDE LA API: el camino completo de la cancelación, sin
// trozos simulados en medio.
//
// Lo que hace e2e a este test y no a los de conversationeventcancel_test.go: la
// petición entra por el mux montado con publicapi.Register —así que también prueba
// que la RUTA está cableada—, el canceller es el *runtime.Runtime DE VERDAD (el
// mismo objeto que satisface ConversationEventCanceller en producción) y los tres
// efectos quedan ESCRITOS en Postgres real:
//
//	POST cancel → guard open→cancelled + closed_at sellado (T4.2)
//	            → flow_state.event_id = NULL (la conversación suelta el puntero)
//	            → el intake colgante queda 'abandoned' con sus líneas INTACTAS (T4.3)
//
// Corre contra WAPP_TEST_DB_DSN (se omite sin ella; WAPP_TEST_REQUIRE_DB la
// exige), igual que el e2e del callback CRM. Datos con prefijo t42-.
package publicapi_test

import (
	"context"
	"database/sql"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	flowruntime "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	flowstore "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
)

const (
	// t42Contacto es el contact_id OPACO de la conversación sembrada. Tiene que ser
	// un UUID (columna UUID en conversation_events y flow_state); el prefijo t42 va
	// en el resto de los datos.
	t42Contacto = "42424242-4242-4242-8242-424242424242"
	t42Sesión   = "t42-sess-cancel"
	t42Flujo    = "t42-flujo-carrito"
	t42Key      = "key-t42-duena" // credencial de la dueña, con intakes.write
)

// t42IntakeAbandoner adapta intakes.Service a la firma del puerto IntakeAbandoner
// del runtime, IGUAL que lo hace el bootstrap en producción. La firma es POR
// EVENTO (T4.5.5a, D-043.21): el runtime no carga ids de hijo — pide «abandona
// el intake open que DECLARE este event_id» — y la puerta sigue siendo el
// servicio del dominio (quién valida la transición, ADR-0029 · E-11.5), nunca
// el UPDATE directo.
type t42IntakeAbandoner struct{ svc *intakes.Service }

func (a t42IntakeAbandoner) AbandonByEvent(ctx context.Context, tenantID, eventID string) error {
	return a.svc.AbandonByEvent(ctx, tenantID, eventID)
}

// t42Seed siembra la foto de ANTES: tenant nuevo, evento 'cart' abierto, intake
// 'open' con dos líneas que DECLARA su evento (intakes.event_id, D-043.21 — el
// hijo apunta al padre; el CHECK de la 0054 rechaza al huérfano), y flow_state
// apuntando al evento (la conversación lo tiene ACTIVO). Devuelve los ids
// generados. El evento nace ANTES que el intake: la FK va del hijo al padre.
func t42Seed(t *testing.T, db *sql.DB) (tenantID, eventID, intakeID string) {
	t.Helper()
	ctx := context.Background()

	// Slug ÚNICO por corrida (no un literal fijo): con slug fijo, una corrida
	// anterior que muriera entre este INSERT y el t.Cleanup de abajo dejaba el
	// tenant huérfano y toda corrida futura moría en 23505 (visto el 2026-08-10).
	// Y la limpieza del tenant se registra AQUÍ MISMO, no al final del seed, por
	// la misma razón: un Fatalf a mitad de la siembra ya no filtra nada.
	if err := db.QueryRowContext(ctx,
		`INSERT INTO public.tenants (slug, display_name) VALUES ($1, $2) RETURNING id::text`,
		"t42-cancel-"+uuid.NewString()[:8], "T42 cancelación e2e").Scan(&tenantID); err != nil {
		t.Fatalf("sembrando tenant: %v", err)
	}
	t.Cleanup(func() {
		// Corre el ÚLTIMO (LIFO): cascada conversation_events y flow_state; para
		// entonces el intake que declaraba el evento ya se barrió abajo.
		if _, err := db.ExecContext(context.Background(),
			`DELETE FROM public.tenants WHERE id = $1`, tenantID); err != nil {
			t.Logf("limpiando tenant: %v", err)
		}
	})
	if err := db.QueryRowContext(ctx, `
		INSERT INTO public.conversation_events
			(tenant_id, session_id, contact_id, kind, history_id, status, flow_id, flow_version)
		VALUES ($1, $2, $3, 'cart', 'cart-2026-08-10-1200', 'open', $4, 1)
		RETURNING id::text
	`, tenantID, t42Sesión, t42Contacto, t42Flujo).Scan(&eventID); err != nil {
		t.Fatalf("sembrando evento: %v", err)
	}
	if err := db.QueryRowContext(ctx, `
		INSERT INTO public.intakes
			(id, tenant_id, contact_id, session_id, status, total, created_at, updated_at, event_id)
		VALUES (gen_random_uuid(), $1, $2, $3, 'open', 300, now(), now(), $4::uuid)
		RETURNING id::text
	`, tenantID, t42Contacto, t42Sesión, eventID).Scan(&intakeID); err != nil {
		t.Fatalf("sembrando intake: %v", err)
	}
	for _, línea := range []struct {
		sku string
		qty int
	}{{"t42-empanada", 2}, {"t42-jugo", 1}} {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO public.intake_items (intake_id, sku, label, qty, unit_price)
			VALUES ($1, $2, $2, $3, 100)
		`, intakeID, línea.sku, línea.qty); err != nil {
			t.Fatalf("sembrando línea %s: %v", línea.sku, err)
		}
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.flow_state
			(tenant_id, session_id, contact_id, flow_id, flow_version, current_node, vars, event_id)
		VALUES ($1, $2, $3, $4, 1, 't42-nodo', '{}', $5)
	`, tenantID, t42Sesión, t42Contacto, t42Flujo, eventID); err != nil {
		t.Fatalf("sembrando flow_state: %v", err)
	}

	t.Cleanup(func() {
		ctx := context.Background()
		// Las líneas y el intake no cuelgan del tenant por FK, así que se barren
		// explícitos y en orden; el tenant lo barre su propio Cleanup (arriba).
		for _, q := range []string{
			`DELETE FROM public.intake_items WHERE intake_id = $1::uuid`,
			`DELETE FROM public.intakes WHERE id = $1::uuid`,
		} {
			if _, err := db.ExecContext(ctx, q, intakeID); err != nil {
				t.Logf("limpiando intake: %v", err)
			}
		}
	})
	return tenantID, eventID, intakeID
}

// t42Runtime construye el *runtime.Runtime REAL sobre la BD de test: el mismo
// objeto (y las mismas costuras: WithEventStore + WithIntakeAbandoner) con que el
// bootstrap satisface ConversationEventCanceller en producción. Sender y resolver
// de contactos van en nil A PROPÓSITO: la cancelación no envía ni resuelve
// identidades, y si alguna vez lo intentara, el pánico del nil es exactamente la
// alarma que este e2e debe hacer sonar.
func t42Runtime(db *sql.DB) *flowruntime.Runtime {
	return flowruntime.New(
		flowstore.NewPostgresRepository(db),
		engine.New(modules.NewRegistry()),
		nil, // Sender
		flowruntime.NewPostgresTenantResolver(db),
		nil, // contact.Resolver
		e2eLogger(),
		flowruntime.WithEventStore(events.NewStore(db, nil)),
		flowruntime.WithIntakeAbandoner(t42IntakeAbandoner{svc: intakes.NewService(intakes.NewPostgres(db))}),
	)
}

// TestE2E_ConversationEventCancel_CancelaLimpiaYAbandona es el criterio completo:
// una petición HTTP de la dueña deja los TRES efectos escritos, y repetirla no
// cambia nada.
func TestE2E_ConversationEventCancel_CancelaLimpiaYAbandona(t *testing.T) {
	db := e2eOpenDB(t)
	tenantID, eventID, intakeID := t42Seed(t, db)

	// Las cuatro features de tipo encendidas para ESTE tenant (el generado, no los
	// literales de los tests con dobles): el gate y el filtro por tipos son los de
	// producción, solo que el plan lo dicta el fake.
	feats := conTodosLosTipos()
	for _, f := range events.KindFeatures() {
		feats.Enable(tenantID, f)
	}
	keys := intakesKeys()
	keys[t42Key] = testIdentity{TenantID: tenantID, Subject: "t42-duena", Grants: []string{"intakes.write"}}
	api := newAPI(publicapi.Deps{EventCanceller: t42Runtime(db), Entitlements: feats}, keys)

	// ── La dueña cancela por la API.
	rec := call(api, t42Key, http.MethodPost, cancelURL(eventID), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := decodeCancelado(t, rec.Body.Bytes()); got.Status != "cancelled" || got.ClosedAt == "" {
		t.Fatalf("cuerpo=%+v; quiero status=cancelled con closed_at sellado", got)
	}

	ctx := context.Background()

	// ── 1. El evento quedó cancelado y sellado EN POSTGRES (T4.2).
	var status string
	var closedAt sql.NullTime
	if err := db.QueryRowContext(ctx,
		`SELECT status, closed_at FROM public.conversation_events WHERE id = $1`,
		eventID).Scan(&status, &closedAt); err != nil {
		t.Fatalf("releyendo el evento: %v", err)
	}
	if status != "cancelled" || !closedAt.Valid {
		t.Fatalf("evento (status=%q, closed_at válido=%v); quiero cancelled y sellado", status, closedAt.Valid)
	}

	// ── 2. La conversación soltó el puntero: flow_state.event_id = NULL (T4.2).
	var eventPtr sql.NullString
	if err := db.QueryRowContext(ctx, `
		SELECT event_id FROM public.flow_state
		WHERE tenant_id = $1 AND session_id = $2 AND contact_id = $3
	`, tenantID, t42Sesión, t42Contacto).Scan(&eventPtr); err != nil {
		t.Fatalf("releyendo flow_state: %v", err)
	}
	if eventPtr.Valid {
		t.Fatalf("flow_state.event_id=%q; la cancelación debe dejarlo NULL", eventPtr.String)
	}

	// ── 3. El intake colgante quedó 'abandoned' con sus líneas INTACTAS (T4.3):
	// jamás borrado — sigue consultable y exportable.
	var intakeStatus string
	var líneas int
	if err := db.QueryRowContext(ctx, `
		SELECT i.status, (SELECT count(*) FROM public.intake_items it WHERE it.intake_id = i.id)
		FROM public.intakes i WHERE i.id = $1
	`, intakeID).Scan(&intakeStatus, &líneas); err != nil {
		t.Fatalf("releyendo el intake: %v", err)
	}
	if intakeStatus != intakes.StatusAbandoned || líneas != 2 {
		t.Fatalf("intake (status=%q, líneas=%d); quiero abandoned con las 2 líneas intactas",
			intakeStatus, líneas)
	}

	// ── 4. Idempotencia sobre la BD real: la segunda llamada es 200 con la MISMA
	// fila (mismo closed_at), y nada de lo de arriba cambia.
	rec2 := call(api, t42Key, http.MethodPost, cancelURL(eventID), "")
	if rec2.Code != http.StatusOK {
		t.Fatalf("segunda llamada: code=%d, quiero 200; body=%s", rec2.Code, rec2.Body.String())
	}
	if rec.Body.String() != rec2.Body.String() {
		t.Fatalf("la segunda cancelación cambió la fila:\n1ª=%s\n2ª=%s", rec.Body.String(), rec2.Body.String())
	}
}
