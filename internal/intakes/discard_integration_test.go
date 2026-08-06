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
//   - la derivación de `live_event` cruza public.intakes (TEXT) con
//     public.flow_state (UUID) — el choque de tipos no se ve en un store en memoria;
//   - la revisión `discarded` tiene que pasar el CHECK de `kind` de la 0045;
//   - la idempotencia se sostiene sobre una fila de verdad, no sobre un mapa.

// seedDescartePG inserta UNA solicitud con el contacto y la sesión que se le
// indiquen (a diferencia de seedPG, que fabrica un contact_id opaco no-UUID) y
// devuelve su id. Limpia al terminar.
func seedDescartePG(t *testing.T, db *sql.DB, tenantID, sessionID, contactID, status string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO public.intakes
			(id, tenant_id, contact_id, session_id, status, total, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, 18000, now(), now())
	`, id, tenantID, contactID, sessionID, status); err != nil {
		t.Fatalf("sembrando solicitud %s: %v", id, err)
	}
	t.Cleanup(func() {
		if _, err := db.ExecContext(context.Background(),
			`DELETE FROM public.intakes WHERE id = $1`, id); err != nil {
			t.Logf("limpiando solicitud %s: %v", id, err)
		}
	})
	return id
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

// seedConversaciónPG inserta la fila de flow_state de (tenant, sesión, contacto)
// con el `vars` dado. Es la señal de la que hoy se deriva "hay evento vivo".
func seedConversaciónPG(t *testing.T, db *sql.DB, tenantID, sessionID, contactID, vars string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO public.flow_state
			(tenant_id, session_id, contact_id, flow_id, flow_version, current_node, vars)
		VALUES ($1, $2, $3, 'flujo-pedidos', 1, 'nodo-cart', $4::jsonb)
		ON CONFLICT (tenant_id, session_id, contact_id) DO UPDATE SET vars = EXCLUDED.vars
	`, tenantID, sessionID, contactID, vars); err != nil {
		t.Fatalf("sembrando conversación: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db.ExecContext(context.Background(), `
			DELETE FROM public.flow_state
			WHERE tenant_id = $1 AND session_id = $2 AND contact_id = $3
		`, tenantID, sessionID, contactID); err != nil {
			t.Logf("limpiando conversación: %v", err)
		}
	})
}

// carritoVivo es un `vars` como el que serializa el módulo cart: la clave `cart`
// con el estado de la sub-máquina dentro (cart/state.go).
const carritoVivo = `{"cart":{"level":"summary","lines":[{"sku":"torta-v1","qty":1}]}}`

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

// TestPostgres_Discard_ConversaciónViva es la APROXIMACIÓN de `live_event` probada
// contra la señal real: una fila de flow_state con `cart` en su `vars` frena el
// descarte; la misma fila SIN carrito no lo frena.
//
// Es también donde se ve el choque de tipos: intakes.tenant_id/contact_id son TEXT
// y flow_state.tenant_id/contact_id son UUID. El cruce solo encuentra la fila porque
// la solicitud guarda el UUID del tenant y del contacto en su columna de texto, que
// es lo que escribe el motor en producción (store.CloseIntake con la clave de la
// conversación).
func TestPostgres_Discard_ConversaciónViva(t *testing.T) {
	db := openTestDB(t)
	store := intakes.NewPostgres(db)
	ctx := context.Background()
	tenant := seedTenantPG(t, db)
	contacto := uuid.NewString()
	id := seedDescartePG(t, db, tenant, "sess-viva", contacto, intakes.StatusOpen)
	seedConversaciónPG(t, db, tenant, "sess-viva", contacto, carritoVivo)

	out, err := store.Discard(ctx, tenant, id, intakes.DiscardableStatuses())
	if err != nil {
		t.Fatalf("Discard con conversación viva: %v", err)
	}
	if out.Discarded {
		t.Fatal("se descartó una solicitud con carrito vivo: la guarda no vio la fila de flow_state")
	}
	if !out.LiveCart {
		t.Fatalf("outcome=%+v; quiero LiveCart=true", out)
	}
	det, err := store.Get(ctx, tenant, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if det.Status != intakes.StatusOpen || len(det.Revisions) != 0 {
		t.Fatalf("un rechazo no escribe NADA: status=%q revisiones=%d", det.Status, len(det.Revisions))
	}

	// La MISMA conversación sin carrito ya no frena nada: lo que se mira es el
	// carrito, no la mera existencia de la fila.
	seedConversaciónPG(t, db, tenant, "sess-viva", contacto, `{"otra":"cosa"}`)
	out, err = store.Discard(ctx, tenant, id, intakes.DiscardableStatuses())
	if err != nil {
		t.Fatalf("Discard sin carrito: %v", err)
	}
	if !out.Discarded || out.LiveCart {
		t.Fatalf("outcome=%+v; sin carrito en vars el descarte procede", out)
	}
}

// TestPostgres_Discard_ConversaciónDeOtraSesión: la ligadura es por la CLAVE
// COMPLETA (tenant, sesión, contacto). Una conversación viva del mismo contacto en
// OTRA sesión no frena el descarte — si lo hiciera, un tenant con dos teléfonos no
// podría limpiar nunca su bandeja.
func TestPostgres_Discard_ConversaciónDeOtraSesión(t *testing.T) {
	db := openTestDB(t)
	store := intakes.NewPostgres(db)
	tenant := seedTenantPG(t, db)
	contacto := uuid.NewString()
	id := seedDescartePG(t, db, tenant, "sess-a", contacto, intakes.StatusOpen)
	seedConversaciónPG(t, db, tenant, "sess-b", contacto, carritoVivo)

	out, err := store.Discard(context.Background(), tenant, id, intakes.DiscardableStatuses())
	if err != nil {
		t.Fatalf("Discard: %v", err)
	}
	if !out.Discarded {
		t.Fatalf("outcome=%+v; la conversación viva es de OTRA sesión", out)
	}
}

// TestPostgres_Discard_ContactoQueNoEsUUID es el borde del choque TEXT/UUID puesto
// por escrito: una solicitud cuyo contact_id NO es un UUID —el fixture histórico de
// este repo las siembra así— no puede tener fila en flow_state, porque esa columna
// es UUID y no admitiría el valor. La consulta ni siquiera se lanza (se parsea en
// Go) y el descarte procede sin reventar con "invalid input syntax for type uuid",
// que es lo que pasaría si el parámetro viajara crudo.
func TestPostgres_Discard_ContactoQueNoEsUUID(t *testing.T) {
	db := openTestDB(t)
	store := intakes.NewPostgres(db)
	tenant := seedTenantPG(t, db)
	id := seedDescartePG(t, db, tenant, "sess-a", "contacto-opaco-legado", intakes.StatusOpen)

	out, err := store.Discard(context.Background(), tenant, id, intakes.DiscardableStatuses())
	if err != nil {
		t.Fatalf("Discard con contacto no-UUID: %v", err)
	}
	if !out.Discarded {
		t.Fatalf("outcome=%+v; quiero que se descarte", out)
	}

	// Y lo mismo con un TENANT que no es UUID (una base con datos de otra época).
	otro := seedDescartePG(t, db, "tenant-que-no-es-uuid", "sess-a", uuid.NewString(), intakes.StatusOpen)
	out, err = store.Discard(context.Background(), "tenant-que-no-es-uuid", otro, intakes.DiscardableStatuses())
	if err != nil {
		t.Fatalf("Discard con tenant no-UUID: %v", err)
	}
	if !out.Discarded {
		t.Fatalf("outcome=%+v; quiero que se descarte", out)
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
