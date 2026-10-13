//go:build integracion && edgereal

package procesos

// El carrito con Cloud y Edge reales (fase FW · TW.8 · cierra la deuda D-38).
//
// Es el recorrido de TestP3_CartConversation (p3_entrante_cart_test.go), con las MISMAS constantes
// de texto, pero el Edge ya no es el doble del arnés: es el binario `wapp-edge-agent` con el
// transporte de WhatsApp falso. Cada mensaje del cliente entra por la puerta real del Edge (las
// guardas, la cola cifrada, el despachador, CloudLink) y cada pantalla se lee de lo que el Edge
// «envió». Lo que este caso mide y el del Edge falso no: el sellado del Edge de verdad, su lease,
// su gate y su Ack.

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// edgeRealCartBatch es el lote (y con él el wa_message_id) del mensaje n del cliente del carrito.
func edgeRealCartBatch(n int) string { return fmt.Sprintf("CART-%d", n) }

// cartSay manda el mensaje n (desde 1) del cliente del carrito. A partir del cartReplyBurst+1
// espera antes cartReplyEvery: es el ritmo del cliente frente al tope anti-bucle del servidor (ver
// p3Scene.cartSay).
func (sc edgeRealScene) cartSay(t *testing.T, n int, text string) {
	t.Helper()
	if n > cartReplyBurst {
		select {
		case <-time.After(cartReplyEvery):
		case <-t.Context().Done():
			t.Fatalf("el test terminó esperando el ritmo del mensaje %d", n)
		}
	}
	sc.say(t, cartPn, edgeRealCartBatch(n), text)
}

// TestEdgeReal_CartConversation recorre la conversación de carrito con el Edge real:
//
//   - Antes de nada, qué binario corre (edgeRealStart): la revisión, la palanca gritada y el
//     CloudLink con validador de lease y sellado.
//   - La sesión nace por el emparejamiento de siempre, pasiva; el servidor le manda el aviso de
//     sesión pasiva a su número propio, y el Edge lo «envía» (queda capturado).
//   - Con el perfil activo: «carrito» → categoría → artículo → cantidad → resumen → confirmar, con
//     los dos adversarios del caso original («8» y «abc»). Cada pantalla, en orden y byte a byte,
//     es la constante del caso con el Edge falso.
//   - En Postgres queda lo mismo que con el Edge falso, y cada referencia de la ventana de
//     captación lleva la marca de lo sintético: lo inyectado viaja marcado hasta la nube.
//   - Tras revocar el lease, un cliente nuevo escribe «carrito» y el Edge no «envía» nada.
func TestEdgeReal_CartConversation(t *testing.T) {
	t.Parallel()
	sc := edgeRealNewScene(t, "ercart", "er-carrito")
	p3Scene{edgeEscenario: sc.edgeEscenario, Pub: sc.Pub}.installCart(t)
	sc.expectText(t, sc.SelfPn, p3PassiveNotice(t))
	sc.setProfile(t, "active")

	edgeRealCartBrowse(t, sc)
	edgeRealCartConfirm(t, sc)
	edgeRealCartRevoked(t, sc)

	if strings.Contains(sc.S.Log(), cartPn) {
		t.Errorf("el número del contacto aparece en el log del servidor")
	}
	if strings.Contains(sc.Edge.Log(), cartPn) {
		t.Errorf("el número del contacto aparece en el log del Edge real")
	}
	edgeSinErrores(t, sc.S, nil)
}

// edgeRealCartBrowse va de la palabra que abre el evento hasta el resumen del pedido.
func edgeRealCartBrowse(t *testing.T, sc edgeRealScene) {
	t.Helper()
	sc.cartSay(t, 1, cartKeyword)
	sc.expectText(t, cartPn, p3Welcome)
	sc.expectText(t, cartPn, cartCategories)

	sc.cartSay(t, 2, "8")
	sc.expectText(t, cartPn, cartBadOption)
	sc.cartSay(t, 3, "1")
	sc.expectText(t, cartPn, cartArticles)
	sc.cartSay(t, 4, "1")
	sc.expectText(t, cartPn, cartArticle)
	sc.cartSay(t, 5, "2")
	sc.expectText(t, cartPn, cartQuantity)

	// «abc» en la cantidad: el carrito eleva una consulta y el motor se la pregunta al modelo de la
	// empresa por la vía local, que es este Edge. No tiene cajero ni Ollama, así que contesta él
	// mismo que el modelo no está: la consulta falla y el carrito repregunta.
	sc.cartSay(t, 6, "abc")
	sc.expectText(t, cartPn, cartBadQuantity)
	if l := edgeEsperarLinea(t, sc.S, cartMsgUnresolved, "nivel", "quantity"); l["clase"] != "cantidad" || l["desenlace"] != "fallo" {
		t.Errorf("la consulta no resuelta se anotó como %v, quería clase cantidad y desenlace fallo", l)
	}

	sc.cartSay(t, 7, "2")
	sc.expectText(t, cartPn, cartAdded)
	sc.cartSay(t, 8, "2")
	sc.expectText(t, cartPn, cartSummary)

	edgeEsperarValor(t, sc.DB, cartFlowID+"|root|con evento|"+edgeRealWaID(edgeRealCartBatch(8))+"|summary|["+cartLine+"]|", "flow_state en el resumen",
		cartStateQuery, sc.Tenant, sc.SessionID)
	cartWaitMetric(t, sc.S, cartStreakMax, 8)
	sc.expectNoPendingText(t, "tras el resumen")
}

// edgeRealCartConfirm confirma el pedido y comprueba la pantalla de cierre y lo que queda en
// Postgres: el estado terminal, la solicitud cerrada con su línea y las nueve referencias de la
// ventana de captación, cada una con la marca de lo sintético.
func edgeRealCartConfirm(t *testing.T, sc edgeRealScene) {
	t.Helper()
	sc.cartSay(t, 9, "1")
	sc.expectText(t, cartPn, cartConfirmed)

	wait := func(want, what, query string, args ...any) {
		t.Helper()
		edgeEsperarValor(t, sc.DB, want, what, query, append([]any{sc.Tenant}, args...)...)
	}
	wait(cartFlowID+"|"+cartEndNode+"|sin evento|"+edgeRealWaID(edgeRealCartBatch(9))+"|closed|["+cartLine+"]|completed", "flow_state tras confirmar",
		cartStateQuery, sc.SessionID)
	wait("event:event_started,event:cart_started,event:category_selected,event:item_added,event:event_closed,persist:cart_closed",
		"los efectos del carrito en flow_events",
		`SELECT string_agg(kind || ':' || name, ',' ORDER BY id) FROM public.flow_events WHERE tenant_id::text = $1 AND flow_id = $2`, cartFlowID)
	wait("order|closed|5", "la solicitud del carrito",
		`SELECT string_agg(concat_ws('|', intake_type, status, total), ',') FROM public.intakes WHERE tenant_id::text = $1`)
	wait("CAFE|Café|2|2.5|", "las líneas de la solicitud",
		`SELECT string_agg(concat_ws('|', it.sku, it.label, it.qty, it.unit_price, it.customization), ',' ORDER BY it.id)
		FROM public.intake_items it JOIN public.intakes i ON i.id::text = it.intake_id::text WHERE i.tenant_id::text = $1`)
	refs := make([]string, 0, 9)
	for n := 1; n <= 9; n++ {
		refs = append(refs, edgeRealWaID(edgeRealCartBatch(n)))
	}
	wait(strings.Join(refs, ","), "las referencias de la ventana de captación del evento",
		`SELECT string_agg((SELECT string_agg(ref, ',' ORDER BY ord) FROM jsonb_array_elements_text(j.source_refs) WITH ORDINALITY AS x(ref, ord)), ';')
		FROM public.intake_jobs j JOIN public.conversation_events e ON e.id::text = j.event_id::text WHERE j.tenant_id::text = $1`)
	p3PhoneNotInClear(t, p3Scene{edgeEscenario: sc.edgeEscenario}, cartPn)
	cartWaitMetric(t, sc.S, cartStreakMax, 9)
	sc.expectNoPendingText(t, "tras confirmar el pedido")
}

const (
	// edgeRealRevokedPn es el cliente que escribe con el lease ya revocado.
	edgeRealRevokedPn = "573001110071"
	// edgeRealMsgWelcomeRefused es la línea WARN con la que el motor dice que el Edge le rechazó la
	// bienvenida, y edgeRealLeaseRefusal el motivo que el Edge real pone en su Ack.
	edgeRealMsgWelcomeRefused = "runtime: el Edge rechazó la bienvenida; NO se marca y el próximo mensaje reintenta"
	edgeRealLeaseRefusal      = "lease no vigente"
	// edgeRealMsgBlocked es la línea con la que el Edge real dice que su gate cortó un SendText.
	edgeRealMsgBlocked = "SendText BLOQUEADO por lease no vigente"
)

// edgeRealCartRevoked revoca el lease del Edge (el kill-switch) y hace que un cliente nuevo escriba
// «carrito»: con el perfil activo eso abre otro carrito, y el servidor manda la bienvenida y las
// categorías. El entrante sube (el lease no gobierna lo que entra) y el servidor ordena los dos
// envíos; es el Edge real quien los corta en su gate, antes del transporte: contesta a cada uno con
// un Ack de fallo y no «envía» nada. La doble llave no tiene excepción de pruebas.
//
// El caso no espera a ciegas: sondea la línea del servidor con el rechazo del Edge y las dos líneas
// del Edge con los dos cortes, y solo entonces mira que no haya ningún envío capturado de más.
func edgeRealCartRevoked(t *testing.T, sc edgeRealScene) {
	t.Helper()
	if n := strings.Count(sc.Edge.Log(), edgeRealMsgBlocked); n != 0 {
		t.Fatalf("el Edge real ya había cortado %d envíos antes de revocar el lease", n)
	}
	if r := sc.S.Admin(sc.TokenAdmin).Post(t, rutaLeaseRevoke, map[string]string{"edge_id": sc.Edge.EdgeID}); r.Codigo != http.StatusNoContent {
		t.Fatalf("revocar el lease: HTTP %d, quería 204\ncuerpo: %s", r.Codigo, recortar(r.Cuerpo))
	}
	edgeEsperarValor(t, sc.DB, "true", "el lease revocado en Postgres",
		`SELECT bool_and(revoked)::text FROM public.leases WHERE tenant_id = $1::uuid AND edge_id = $2`, sc.Tenant, sc.Edge.EdgeID)

	sc.say(t, edgeRealRevokedPn, "REVOKED-1", cartKeyword)
	edgeEsperarLinea(t, sc.S, edgeRealMsgWelcomeRefused, "edge_error", edgeRealLeaseRefusal)
	edgeEsperar(t, edgeTopeFila, "que el Edge real corte los dos envíos (la bienvenida y las categorías)", func() bool {
		return strings.Count(sc.Edge.Log(), edgeRealMsgBlocked) >= 2
	})
	sc.expectNoPendingText(t, "con el lease revocado")
	if n := strings.Count(sc.Edge.Log(), edgeRealMsgBlocked); n != 2 {
		t.Errorf("el Edge real cortó %d envíos con el lease revocado, quería 2", n)
	}
	if strings.Contains(sc.Edge.Log(), edgeRealRevokedPn) {
		t.Errorf("el número del contacto aparece en el log del Edge real")
	}
}
