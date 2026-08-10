package intakes_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// discard_integration_test.go verifica el descarte manual contra Postgres REAL, que
// es el único sitio donde se puede comprobar lo que este endpoint tiene de
// arriesgado:
//
//   - la guarda `live_event` pregunta por el estado del EVENTO que la solicitud
//     declara (intakes.event_id, D-043.21 — DT-043.2 saldada en la Ola 4.5), y el
//     par evento↔solicitud solo existe entero con las FKs de la 0054 puestas;
//   - la revisión `discarded` tiene que pasar el CHECK de `kind` de la 0045;
//   - la idempotencia se sostiene sobre una fila de verdad, no sobre un mapa.

// seedDescartePG inserta UNA solicitud con el contacto y la sesión que se le
// indiquen y devuelve su id. Desde la 0054 la fila declara a su padre: nace ligada
// a un evento propio ya `cancelled` — el pedido HUÉRFANO típico de la bandeja, el
// que el descarte existe para limpiar. Para montar el par con el evento en otro
// estado está seedDescarteConEventoPG. Limpia al terminar.
func seedDescartePG(t *testing.T, db *sql.DB, tenantID, sessionID, contactID, status string) string {
	id, _ := seedDescarteConEventoPG(t, db, tenantID, sessionID, contactID, status, "cancelled")
	return id
}

// seedDescarteConEventoPG monta el PAR completo (D-043.21): un evento del tenant en
// `eventStatus` y una solicitud en `status` que lo declara como padre. Devuelve los
// dos ids. Limpia la solicitud al terminar (el evento lo limpia el CASCADE del
// tenant, ver ensureTenantPG).
func seedDescarteConEventoPG(t *testing.T, db *sql.DB, tenantID, sessionID, contactID, status, eventStatus string) (intakeID, eventID string) {
	t.Helper()
	ensureTenantPG(t, db, tenantID)
	eventID = seedEventoPG(t, db, tenantID, eventStatus)
	intakeID = uuid.NewString()
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO public.intakes
			(id, tenant_id, contact_id, session_id, status, total, event_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, 18000, $6, now(), now())
	`, intakeID, tenantID, contactID, sessionID, status, eventID); err != nil {
		t.Fatalf("sembrando solicitud %s: %v", intakeID, err)
	}
	t.Cleanup(func() {
		if _, err := db.ExecContext(context.Background(),
			`DELETE FROM public.intakes WHERE id = $1`, intakeID); err != nil {
			t.Logf("limpiando solicitud %s: %v", intakeID, err)
		}
	})
	return intakeID, eventID
}

// seedTenantPG crea un tenant de verdad. Hace falta porque flow_state.tenant_id
// tiene FK a public.tenants: sin la fila, la conversación no se puede sembrar.
func seedTenantPG(t *testing.T, db *sql.DB) string {
	t.Helper()
	var id string
	slug := "tenant-descarte-" + uuid.NewString()
	if err := db.QueryRowContext(context.Background(),
		`INSERT INTO public.tenants (slug, display_name) VALUES ($1, $2) RETURNING id::text`,
		slug, "Descarte T4.8").Scan(&id); err != nil {
		t.Fatalf("creando tenant: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db.ExecContext(context.Background(),
			`DELETE FROM public.tenants WHERE id = $1`, id); err != nil {
			t.Logf("limpiando tenant %s: %v", id, err)
		}
	})
	return id
}

// estadoEventoPG lee el estado y el closed_at del evento: lo que permite afirmar
// que el descarte cerró (o respetó) el contenedor sin abrir otra puerta.
func estadoEventoPG(t *testing.T, db *sql.DB, eventID string) (status string, closed bool) {
	t.Helper()
	var closedAt sql.NullTime
	if err := db.QueryRowContext(context.Background(),
		`SELECT status, closed_at FROM public.conversation_events WHERE id = $1`,
		eventID).Scan(&status, &closedAt); err != nil {
		t.Fatalf("leyendo evento %s: %v", eventID, err)
	}
	return status, closedAt.Valid
}

// TestPostgres_Discard_DescartaYAudita: el camino feliz contra la tabla real. La
// solicitud queda en `abandoned`, con su revisión `discarded` — que además tiene que
// pasar el CHECK de `kind` de la migración 0045.
func TestPostgres_Discard_DescartaYAudita(t *testing.T) {
	db := openTestDB(t)
	store := intakes.NewPostgres(db)
	ctx := context.Background()
	tenant := seedTenantPG(t, db)
	contacto := uuid.NewString()
	id := seedDescartePG(t, db, tenant, "sess-a", contacto, intakes.StatusOpen)

	out, err := store.Discard(ctx, tenant, id, intakes.DiscardableStatuses())
	if err != nil {
		t.Fatalf("Discard: %v", err)
	}
	if !out.Discarded || out.Status != intakes.StatusOpen {
		t.Fatalf("outcome=%+v; quiero descartada viniendo de open", out)
	}

	det, err := store.Get(ctx, tenant, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if det.Status != intakes.StatusAbandoned {
		t.Fatalf("status=%q, quiero abandoned", det.Status)
	}
	if len(det.Revisions) != 1 {
		t.Fatalf("revisiones=%d, quiero 1", len(det.Revisions))
	}
	rev := det.Revisions[0]
	if rev.Kind != intakes.RevisionKindDiscarded || rev.CreatedBy != intakes.RevisionByOwner {
		t.Fatalf("revisión=%+v; quiero kind=discarded creada por owner", rev)
	}
	var payload struct {
		Version    int     `json:"version"`
		FromStatus string  `json:"from_status"`
		Total      float64 `json:"total"`
	}
	if err := json.Unmarshal(rev.Payload, &payload); err != nil {
		t.Fatalf("payload ilegible (%v): %s", err, rev.Payload)
	}
	if payload.FromStatus != intakes.StatusOpen || payload.Total != 18000 {
		t.Fatalf("payload=%+v; quiero from_status=open y total=18000", payload)
	}
}

// TestPostgres_Discard_EventoVivo es DT-043.2 SALDADA puesta por escrito
// (T4.5.5(c)): la guarda `live_event` mira el EVENTO que la solicitud declara —
// evento `open` + solicitud suya `open` ⇒ `live_event`, sin escribir NADA (ni en la
// solicitud ni en el evento: eso se cancela por su propia puerta). El mismo par con
// el evento ya `cancelled` —el huérfano del journal 2026-08-10, el que la
// aproximación por flow_state dejaba SIN vía de reparación— hoy se descarta.
func TestPostgres_Discard_EventoVivo(t *testing.T) {
	db := openTestDB(t)
	store := intakes.NewPostgres(db)
	ctx := context.Background()
	tenant := seedTenantPG(t, db)
	id, evento := seedDescarteConEventoPG(t, db, tenant, "t45i-sess-viva", uuid.NewString(),
		intakes.StatusOpen, "open")

	out, err := store.Discard(ctx, tenant, id, intakes.DiscardableStatuses())
	if err != nil {
		t.Fatalf("Discard con evento vivo: %v", err)
	}
	if out.Discarded {
		t.Fatal("se descartó una solicitud cuyo evento sigue open: la guarda no miró el evento")
	}
	if !out.LiveEvent {
		t.Fatalf("outcome=%+v; quiero LiveEvent=true", out)
	}
	det, err := store.Get(ctx, tenant, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if det.Status != intakes.StatusOpen || len(det.Revisions) != 0 {
		t.Fatalf("un rechazo no escribe NADA: status=%q revisiones=%d", det.Status, len(det.Revisions))
	}
	if status, closed := estadoEventoPG(t, db, evento); status != "open" || closed {
		t.Fatalf("el rechazo tampoco toca el evento: status=%q closed=%v", status, closed)
	}

	// El MISMO par cuando el evento muere (la cancelación selló open→cancelled):
	// la solicitud queda huérfana y el descarte procede — exactamente el caso que
	// la vieja guarda por flow_state rebotaba para siempre.
	if _, err := db.ExecContext(ctx, `
		UPDATE public.conversation_events SET status='cancelled', closed_at=now()
		WHERE id = $1 AND status='open'`, evento); err != nil {
		t.Fatalf("cancelando el evento: %v", err)
	}
	out, err = store.Discard(ctx, tenant, id, intakes.DiscardableStatuses())
	if err != nil {
		t.Fatalf("Discard del huérfano: %v", err)
	}
	if !out.Discarded || out.LiveEvent {
		t.Fatalf("outcome=%+v; con el evento muerto el descarte procede", out)
	}
}

// TestPostgres_Discard_ElCarritoNuevoYaNoFrena fija la otra mitad de DT-043.2: la
// sobre-protección de la aproximación murió con ella. Que el tenant tenga OTRO
// evento `open` (el carrito nuevo de esa conversación, que con la vieja guarda por
// flow_state frenaba el descarte de cualquier solicitud vieja del contacto) ya no
// frena nada: lo único que la guarda mira es el evento que ESA solicitud declara —
// que está `cancelled`.
func TestPostgres_Discard_ElCarritoNuevoYaNoFrena(t *testing.T) {
	db := openTestDB(t)
	store := intakes.NewPostgres(db)
	tenant := seedTenantPG(t, db)
	contacto := uuid.NewString()
	// La solicitud vieja, huérfana de un evento ya cancelado…
	id := seedDescartePG(t, db, tenant, "t45i-sess-a", contacto, intakes.StatusOpen)
	// …y el carrito NUEVO de la conversación: otro evento, este sí vivo.
	seedEventoPG(t, db, tenant, "open")

	out, err := store.Discard(context.Background(), tenant, id, intakes.DiscardableStatuses())
	if err != nil {
		t.Fatalf("Discard: %v", err)
	}
	if !out.Discarded || out.LiveEvent {
		t.Fatalf("outcome=%+v; el evento vivo es OTRO, no el de esta solicitud", out)
	}
}

// TestPostgres_Discard_Idempotente: descartar dos veces la misma fila deja el mismo
// estado y UNA sola revisión. Sobre una acción irreversible, esto es lo que hace
// seguro reintentar un lote que se cortó.
func TestPostgres_Discard_Idempotente(t *testing.T) {
	db := openTestDB(t)
	store := intakes.NewPostgres(db)
	ctx := context.Background()
	tenant := seedTenantPG(t, db)
	id := seedDescartePG(t, db, tenant, "sess-a", uuid.NewString(), intakes.StatusOpen)

	if _, err := store.Discard(ctx, tenant, id, intakes.DiscardableStatuses()); err != nil {
		t.Fatalf("primer descarte: %v", err)
	}
	out, err := store.Discard(ctx, tenant, id, intakes.DiscardableStatuses())
	if err != nil {
		t.Fatalf("segundo descarte: %v", err)
	}
	if out.Discarded {
		t.Fatal("el segundo descarte volvió a escribir: no es idempotente")
	}
	if out.Status != intakes.StatusAbandoned {
		t.Fatalf("status=%q, quiero abandoned", out.Status)
	}

	var revisiones int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM public.intake_revisions WHERE intake_id = $1 AND kind = $2`,
		id, intakes.RevisionKindDiscarded).Scan(&revisiones); err != nil {
		t.Fatalf("contando revisiones: %v", err)
	}
	if revisiones != 1 {
		t.Fatalf("revisiones discarded=%d, quiero 1", revisiones)
	}
}

// TestPostgres_Discard_NoDescartables: los estados desde los que no se descarta se
// rechazan SIN escribir, y el outcome dice dónde está la solicitud para que el
// dominio elija la razón.
func TestPostgres_Discard_NoDescartables(t *testing.T) {
	db := openTestDB(t)
	store := intakes.NewPostgres(db)
	ctx := context.Background()
	tenant := seedTenantPG(t, db)

	for _, status := range []string{
		intakes.StatusConfirmed, intakes.StatusClosedLegacy, intakes.StatusCancelled,
		intakes.StatusPendingApproval, intakes.StatusSettled,
	} {
		id := seedDescartePG(t, db, tenant, "sess-a", uuid.NewString(), status)
		out, err := store.Discard(ctx, tenant, id, intakes.DiscardableStatuses())
		if err != nil {
			t.Fatalf("Discard desde %q: %v", status, err)
		}
		if out.Discarded {
			t.Fatalf("se descartó una solicitud en %q", status)
		}
		if out.Status != intakes.NormalizeStatus(status) {
			t.Fatalf("outcome.Status=%q desde %q; el store devuelve el estado NORMALIZADO",
				out.Status, status)
		}
	}
}

// TestPostgres_Discard_AisladoPorTenant: una solicitud de otro tenant es
// ErrNotFound, indistinguible de inexistente (INV-8) — y no se toca.
func TestPostgres_Discard_AisladoPorTenant(t *testing.T) {
	db := openTestDB(t)
	store := intakes.NewPostgres(db)
	ctx := context.Background()
	tenant := seedTenantPG(t, db)
	id := seedDescartePG(t, db, tenant, "sess-a", uuid.NewString(), intakes.StatusOpen)

	ajeno := uuid.NewString()
	if _, err := store.Discard(ctx, ajeno, id, intakes.DiscardableStatuses()); err == nil {
		t.Fatal("un tenant ajeno descartó una solicitud que no es suya")
	} else if !errors.Is(err, intakes.ErrNotFound) {
		t.Fatalf("err=%v, quiero ErrNotFound", err)
	}
	// Y un id que no existe en ninguna parte da EXACTAMENTE lo mismo.
	if _, err := store.Discard(ctx, tenant, uuid.NewString(), intakes.DiscardableStatuses()); !errors.Is(err, intakes.ErrNotFound) {
		t.Fatalf("id inexistente: err=%v, quiero ErrNotFound", err)
	}
	// Un id que ni siquiera es un UUID tampoco consulta la BD: mismo 404.
	if _, err := store.Discard(ctx, tenant, "esto-no-es-un-uuid", intakes.DiscardableStatuses()); !errors.Is(err, intakes.ErrNotFound) {
		t.Fatalf("id no-UUID: err=%v, quiero ErrNotFound", err)
	}

	det, err := store.Get(ctx, tenant, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if det.Status != intakes.StatusOpen {
		t.Fatalf("la solicitud quedó en %q; el intento ajeno no podía tocarla", det.Status)
	}
}
