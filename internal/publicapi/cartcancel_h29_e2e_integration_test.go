// cartcancel_h29_e2e_integration_test.go — hallazgo #29 del Plan 043 (Ola 6, segunda
// vuelta · decisión de Jhoan 2026-08-11): cancelar un pedido DENTRO de la conversación
// mata el evento que lo contiene, y lo mata como lo que fue —`cancelled`—, no como un
// «fin natural del flujo».
//
// Es el GEMELO de cartsecondorder_h24 (la mitad de confirmar) y comparte su cableado
// de producción y sus helpers t45v*/h24*: runtime real → trigger real (event_start
// «carrito») → engine + módulo cart REALES sobre el catálogo de tenant_content real →
// PersistSink real → cart.Projector real → PostgreSQL real. Lo que aquí se mide y allí
// no es el ESTADO TERMINAL: el H24 afirma `closed` tras confirmar; este afirma
// `cancelled` tras cancelar. Los dos juntos son la bifurcación entera.
//
// POR QUÉ EXISTE ESTE ARCHIVO (y no una línea más en otro test): hasta esta ola
// NINGÚN test permanente ataba «cancelar dentro del flujo» con el estado que queda en
// conversation_events. La condición del centinela se probaba en el módulo puro
// (cart_fin_de_flujo_test.go) y el estado del intake en el runtime
// (cart_resume_test.go), pero nadie los miraba EN PAREJA — el patrón de los criterios
// gemelos que este repo ya pagó antes. Medido: con la traducción revertida (que
// closeIfFinished escriba siempre `closed`), toda la batería seguía verde.
//
// Corre contra WAPP_TEST_DB_DSN (se omite sin ella; WAPP_TEST_REQUIRE_DB la exige).
// Datos con prefijo h29-, limpiados por t45vSeed (mismo tenant helper).
package publicapi_test

import (
	"context"
	"database/sql"
	"slices"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
)

// h29Telefono es la clienta que cancela su pedido y después hace otro.
const h29Telefono = "573001114554"

// h29ContarEfecto cuenta las filas de flow_events del tenant con ese nombre. Es lo que
// permite afirmar que el EFECTO sigue al ESTADO: con desenlace cancelado se emite
// `event_cancelled` (el mismo nombre que la cancelación desde la app) y NO
// `event_closed` — un `event_closed` junto a una fila que dice `cancelled` sería una
// contradicción escrita en una bitácora append-only, y dejaría el recuento de
// cancelaciones sin la más frecuente de todas.
func h29ContarEfecto(ctx context.Context, t *testing.T, db *sql.DB, tenantID, name string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM public.flow_events WHERE tenant_id = $1 AND name = $2`,
		tenantID, name).Scan(&n); err != nil {
		t.Fatalf("contando flow_events %q: %v", name, err)
	}
	return n
}

// h29QuierePedidoCancelado fija las DOS primeras mitades del criterio gemelo sobre la
// MISMA fila: el evento en `cancelled` con su closed_at sellado, y su solicitud en
// `cancelled` —ni `closed` (eso es confirmar) ni `abandoned` (eso es la cancelación
// desde la app del dueño, que además abandona)— con sus líneas intactas: cancelar no
// borra el pedido (INV-09), lo deja en la bandeja marcado como lo que fue.
func h29QuierePedidoCancelado(ctx context.Context, t *testing.T, db *sql.DB, tenantID, evID string) (intakeID string) {
	t.Helper()
	status, closedAt := h24EventoStatus(ctx, t, db, tenantID, evID)
	if status != "cancelled" || !closedAt.Valid {
		t.Fatalf("cancelar el pedido debe dejar su evento en \"cancelled\" con closed_at sellado; status=%q closed_at.Valid=%v (si dice \"closed\", alguien revirtió la traducción del desenlace en closeIfFinished)",
			status, closedAt.Valid)
	}
	intakeID, intakeStatus, lineas := h24IntakeDeEvento(ctx, t, db, tenantID, evID)
	if intakeStatus != "cancelled" || !slices.Equal(lineas, h24LineasDelPedido) {
		t.Fatalf("la solicitud del pedido cancelado debe quedar \"cancelled\" con SUS líneas intactas (sku|etiqueta|cantidad|precio); status=%q lineas=%q, quiero %q",
			intakeStatus, lineas, h24LineasDelPedido)
	}
	return intakeID
}

// TestE2E_H29_CancelarDejaElEventoCancelled es el criterio gemelo completo: pedir,
// cancelar, pedir otra vez. En la MISMA prueba —esa es la gracia— el evento queda
// `cancelled`, su solicitud queda `cancelled`, y el pedido siguiente nace con evento e
// intake PROPIOS.
func TestE2E_H29_CancelarDejaElEventoCancelled(t *testing.T) {
	db := e2eOpenDB(t)
	ctx := context.Background()
	tenantID, sessionID := t45vSeed(t, db)

	feats := conTodosLosTipos()
	for _, f := range events.KindFeatures() {
		feats.Enable(tenantID, f)
	}
	rt, eventStore, contacts := t45vRuntime(t, db, tenantID, feats)

	// ── Primer pedido: «carrito» → Café×2 → «9» (Cancelar pedido, desde L5).
	t45vAbreCarritoConLinea(ctx, t, rt, sessionID, h29Telefono, "h29-a")
	cid := t45vContacto(ctx, t, contacts, tenantID, h29Telefono)
	ev1 := h24GetAlive(ctx, t, eventStore, tenantID, sessionID, cid, "tras el primer «carrito»")
	t45vEntrante(ctx, t, rt, sessionID, h29Telefono, "9", "h29-a-cancel")

	// ── Mitades 1 y 2: evento `cancelled` + solicitud `cancelled`, atadas aquí.
	intake1ID := h29QuierePedidoCancelado(ctx, t, db, tenantID, ev1.ID)

	// ── El efecto sigue al estado, contra la tabla de verdad.
	if n := h29ContarEfecto(ctx, t, db, tenantID, "event_cancelled"); n != 1 {
		t.Fatalf("cancelar debe dejar EXACTAMENTE un event_cancelled en flow_events; hay %d", n)
	}
	if n := h29ContarEfecto(ctx, t, db, tenantID, "event_closed"); n != 0 {
		t.Fatalf("un pedido cancelado NO puede dejar un event_closed en la bitácora; hay %d", n)
	}

	// ── Segundo pedido en la MISMA conversación: «carrito» otra vez.
	t45vAbreCarritoConLinea(ctx, t, rt, sessionID, h29Telefono, "h29-b")

	// ── Mitad 3: evento e intake PROPIOS. Se reusa el aserto del H24 tal cual —el
	// criterio es el mismo, cambia solo cómo murió el primero—, y con él la
	// comprobación de que el segundo intake existe DE VERDAD (con sus líneas) y no es
	// el primero reciclado.
	ev2 := h24GetAlive(ctx, t, eventStore, tenantID, sessionID, cid, "tras el segundo «carrito»")
	h24QuiereSegundoPedidoPropio(ctx, t, db, tenantID, ev1, ev2, intake1ID)

	// ── Y a lo ancho del contacto: DOS pedidos, DOS filas. El cancelado sigue ahí.
	if n := h24ContarIntakes(ctx, t, db, tenantID, cid); n != 2 {
		t.Fatalf("el contacto debe tener EXACTAMENTE 2 solicitudes (la cancelada + la nueva); hay %d", n)
	}
}
