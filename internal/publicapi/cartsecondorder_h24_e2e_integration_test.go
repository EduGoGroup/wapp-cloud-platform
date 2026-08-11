// cartsecondorder_h24_e2e_integration_test.go — hallazgo #24 del Plan 043 (Ola 6):
// confirmar un pedido debe CERRAR también el evento que lo contiene (T6.1), para
// que la MISMA conversación pueda pedir una segunda vez sin que el segundo pedido
// choque en silencio contra el índice único parcial `intakes_event_id_uidx`
// (migración 0054) y desaparezca sin que nadie —ni la clienta, ni Herminia— se
// entere (el PersistSink es best-effort por diseño).
//
// Antes del arreglo: confirmar el pedido dejaba el `intake` `closed` pero el EVENTO
// `cart` que lo contenía quedaba `open` para siempre (el módulo `cart` nunca fijaba
// `Result.Next`). Un segundo «carrito» dicho más tarde en la MISMA conversación
// encontraba ese evento todavía vivo y lo REUSABA (salto por tipo, E-11/MD-043.12)
// en vez de nacer uno nuevo; `ensureOpenIntake` intentaba abrir una segunda
// solicitud con el MISMO `event_id` y chocaba contra el único parcial.
//
// Reusa el cableado de producción de conversationchain_w45_e2e_integration_test.go
// (mismo paquete, mismos helpers t45v*): runtime real → trigger real (event_start
// «carrito») → engine + módulo cart REALES sobre el catálogo de tenant_content real
// → PersistSink real → cart.Projector real → PostgreSQL real (CHECK/índice de la
// 0054).
//
// Corre contra WAPP_TEST_DB_DSN (se omite sin ella; WAPP_TEST_REQUIRE_DB la exige).
// Datos con prefijo h24-, limpiados por t45vSeed (mismo tenant helper).
package publicapi_test

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"testing"

	flowruntime "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
)

// h24Telefono es la clienta que pide DOS veces en la misma conversación.
const h24Telefono = "573001114553"

// h24Confirma navega desde LevelContinue (donde t45vAbreCarritoConLinea deja la
// sub-máquina tras agregar Café ×2) hasta el pedido CONFIRMADO: «2» (Finalizar →
// resumen) → «1» (Confirmar → cierre, hallazgo #24).
func h24Confirma(ctx context.Context, t *testing.T, rt *flowruntime.Runtime, sessionID, telefono, base string) {
	t.Helper()
	t45vEntrante(ctx, t, rt, sessionID, telefono, "2", base+"-fin")
	t45vEntrante(ctx, t, rt, sessionID, telefono, "1", base+"-conf")
}

// h24EventoStatus lee status/closed_at de UN evento por id (sin ambigüedad posible:
// a diferencia de t45vEventoDe —que filtra por (tenant,contacto,kind) y con DOS
// pedidos del mismo contacto dejaría de identificar una fila única—, aquí el id ya
// es la clave).
func h24EventoStatus(ctx context.Context, t *testing.T, db *sql.DB, tenantID, eventID string) (status string, closedAt sql.NullTime) {
	t.Helper()
	if err := db.QueryRowContext(ctx, `
		SELECT status, closed_at FROM public.conversation_events
		WHERE id = $1::uuid AND tenant_id = $2::uuid
	`, eventID, tenantID).Scan(&status, &closedAt); err != nil {
		t.Fatalf("leyendo status del evento %s: %v", eventID, err)
	}
	return status, closedAt
}

// h24IntakeDeEvento lee LA solicitud que declara a un evento concreto. A diferencia
// de t45vIntakeDe (que asume una única solicitud por contacto), este test deja DOS
// solicitudes del MISMO contacto sobre la mesa —una por pedido— y necesita poder
// mirar cada una por separado, por el evento que la declara.
//
// Devuelve las líneas por su CONTENIDO y no por su número (corregido en revisión):
// con un simple conteo, una proyección que escribiera el número correcto de líneas
// con el sku, la etiqueta, la cantidad y el precio EQUIVOCADOS pasaba el test —
// medido con esa mutación exacta—. Lo que el hallazgo #24 promete no es «hay una
// fila», es «el pedido que la clienta hizo está guardado».
func h24IntakeDeEvento(ctx context.Context, t *testing.T, db *sql.DB, tenantID, eventID string) (id, status string, lineas []string) {
	t.Helper()
	if err := db.QueryRowContext(ctx, `
		SELECT i.id::text, i.status
		FROM public.intakes i WHERE i.tenant_id = $1 AND i.event_id = $2::uuid
	`, tenantID, eventID).Scan(&id, &status); err != nil {
		t.Fatalf("leyendo la solicitud del evento %s: %v", eventID, err)
	}
	rows, err := db.QueryContext(ctx, `
		SELECT sku, label, qty, unit_price FROM public.intake_items
		WHERE intake_id = $1::uuid ORDER BY id
	`, id)
	if err != nil {
		t.Fatalf("leyendo las líneas de la solicitud %s: %v", id, err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			t.Fatalf("cerrando las líneas de %s: %v", id, cerr)
		}
	}()
	for rows.Next() {
		var sku, label string
		var qty int
		var precio float64
		if err := rows.Scan(&sku, &label, &qty, &precio); err != nil {
			t.Fatalf("escaneando línea de %s: %v", id, err)
		}
		lineas = append(lineas, fmt.Sprintf("%s|%s|%d|%.2f", sku, label, qty, precio))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterando las líneas de %s: %v", id, err)
	}
	return id, status, lineas
}

// h24LineasDelPedido es lo que t45vAbreCarritoConLinea deja en intake_items: Café ×2
// a 2.50 (el catálogo t45vCatalogo, recorrido «1→1→2→2»). Es la MISMA compra en los
// dos pedidos del guion, así que sirve para los dos.
var h24LineasDelPedido = []string{"CAFE|Café|2|2.50"}

// h24ContarIntakes cuenta TODAS las solicitudes del contacto: la prueba directa de
// que el segundo pedido no desapareció. Antes del arreglo, el segundo item_added
// chocaba contra intakes_event_id_uidx y esta cuenta se quedaba en 1 —el segundo
// pedido no queda en NINGUNA tabla y nadie se entera, tal cual mide el hallazgo #24—.
func h24ContarIntakes(ctx context.Context, t *testing.T, db *sql.DB, tenantID, contactID string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM public.intakes WHERE tenant_id = $1 AND contact_id = $2`,
		tenantID, contactID).Scan(&n); err != nil {
		t.Fatalf("contando solicitudes de %s: %v", contactID, err)
	}
	return n
}

// h24GetAlive envuelve eventStore.GetAliveByKind con el Fatal ya resuelto: cada
// hito de este guion necesita el evento `cart` VIVO en ese instante, y repetir el
// «err != nil || !alive» dos veces es lo que dispara gocyclo en el test principal.
func h24GetAlive(ctx context.Context, t *testing.T, eventStore *events.Store, tenantID, sessionID, cid, momento string) events.Event {
	t.Helper()
	ev, alive, err := eventStore.GetAliveByKind(ctx, tenantID, sessionID, cid, "cart")
	if err != nil || !alive {
		t.Fatalf("GetAliveByKind(cart) %s: ok=%v err=%v", momento, alive, err)
	}
	return ev
}

// h24QuierePrimerPedidoCerrado fija la mitad 1 del hallazgo #24: confirmar el
// primer pedido cierra su evento (T6.1) y su solicitud, líneas incluidas.
func h24QuierePrimerPedidoCerrado(ctx context.Context, t *testing.T, db *sql.DB, tenantID, ev1ID string) (intake1ID string) {
	t.Helper()
	status1, closedAt1 := h24EventoStatus(ctx, t, db, tenantID, ev1ID)
	if status1 != "closed" || !closedAt1.Valid {
		t.Fatalf("el PRIMER evento debe quedar closed con closed_at sellado tras confirmar; status=%q closed_at.Valid=%v",
			status1, closedAt1.Valid)
	}
	intake1ID, intake1Status, lineas1 := h24IntakeDeEvento(ctx, t, db, tenantID, ev1ID)
	if intake1Status != "closed" || !slices.Equal(lineas1, h24LineasDelPedido) {
		t.Fatalf("la solicitud del primer pedido debe quedar closed con SUS líneas (sku|etiqueta|cantidad|precio); status=%q lineas=%q, quiero %q",
			intake1Status, lineas1, h24LineasDelPedido)
	}
	return intake1ID
}

// h24QuiereSegundoPedidoPropio fija la mitad 2: el segundo «carrito» nace un
// evento PROPIO (open, distinto del primero) con su propia solicitud (open, con
// sus líneas, distinta de la primera). Sin el arreglo del hallazgo #24, ev2 sería
// el MISMO ev1 (el salto por tipo reusaría el evento todavía `open`) y esta
// función lo atrapa antes de mirar nada más.
func h24QuiereSegundoPedidoPropio(ctx context.Context, t *testing.T, db *sql.DB, tenantID string, ev1, ev2 events.Event, intake1ID string) {
	t.Helper()
	if ev2.ID == ev1.ID {
		t.Fatalf("el segundo «carrito» debe nacer un evento PROPIO (el primero ya está closed); sigue siendo %s", ev1.ID)
	}
	status2, _ := h24EventoStatus(ctx, t, db, tenantID, ev2.ID)
	if status2 != "open" {
		t.Fatalf("el SEGUNDO evento debe estar open; está %q", status2)
	}
	intake2ID, intake2Status, lineas2 := h24IntakeDeEvento(ctx, t, db, tenantID, ev2.ID)
	if intake2Status != "open" || !slices.Equal(lineas2, h24LineasDelPedido) {
		t.Fatalf("el SEGUNDO pedido debe existir de verdad, con su intake propio y SUS líneas (sku|etiqueta|cantidad|precio); status=%q lineas=%q, quiero %q",
			intake2Status, lineas2, h24LineasDelPedido)
	}
	if intake2ID == intake1ID {
		t.Fatalf("el segundo pedido debe tener SU PROPIA solicitud, distinta de la primera (%s)", intake1ID)
	}
}

// TestE2E_H24_SegundoPedidoNoSePierde es el hallazgo #24: pedir, confirmar, pedir
// otra vez. El segundo pedido tiene que existir DE VERDAD —su propio evento `cart`
// (open, id distinto del primero, que quedó closed) y su propia solicitud (open,
// con sus líneas)— y no desaparecer en silencio.
func TestE2E_H24_SegundoPedidoNoSePierde(t *testing.T) {
	db := e2eOpenDB(t)
	ctx := context.Background()
	tenantID, sessionID := t45vSeed(t, db)

	feats := conTodosLosTipos()
	for _, f := range events.KindFeatures() {
		feats.Enable(tenantID, f)
	}
	rt, eventStore, contacts := t45vRuntime(t, db, tenantID, feats)

	// ── Primer pedido: «carrito» → Café×2 → Finalizar → Confirmar.
	t45vAbreCarritoConLinea(ctx, t, rt, sessionID, h24Telefono, "h24-a")
	cid := t45vContacto(ctx, t, contacts, tenantID, h24Telefono)
	ev1 := h24GetAlive(ctx, t, eventStore, tenantID, sessionID, cid, "tras el primer «carrito»")
	h24Confirma(ctx, t, rt, sessionID, h24Telefono, "h24-a")

	// ── #24, mitad 1: confirmar CIERRA el evento del primer pedido (T6.1).
	intake1ID := h24QuierePrimerPedidoCerrado(ctx, t, db, tenantID, ev1.ID)

	// ── Segundo pedido, en la MISMA conversación: «carrito» otra vez.
	t45vAbreCarritoConLinea(ctx, t, rt, sessionID, h24Telefono, "h24-b")

	// ── #24, mitad 2: el segundo «carrito» nace un evento PROPIO —el primero ya no
	// está vivo—, en vez de reusar el cerrado (que es justo lo que el hallazgo mide:
	// SIN el arreglo, el evento del primer pedido seguía `open` y este GetAliveByKind
	// habría devuelto ev1 otra vez).
	ev2 := h24GetAlive(ctx, t, eventStore, tenantID, sessionID, cid, "tras el segundo «carrito»")
	h24QuiereSegundoPedidoPropio(ctx, t, db, tenantID, ev1, ev2, intake1ID)

	// ── La prueba final, a lo ancho del contacto: DOS pedidos, DOS filas. Ni el
	// segundo se perdió en silencio, ni el primero se pisó.
	if n := h24ContarIntakes(ctx, t, db, tenantID, cid); n != 2 {
		t.Fatalf("el contacto debe tener EXACTAMENTE 2 solicitudes (una por pedido); hay %d", n)
	}
}
