//go:build integracion

package procesos

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// P3 · La conversación entera de un carrito (F8 · T8.36 / R8.8.a): el cliente escribe la palabra que
// abre el evento de carrito, navega el catálogo, añade una línea, confirma y recibe la pantalla de
// cierre. Es el único proceso con un nodo `cart`: los demás usan `menu` y `message`, y por eso
// ninguno llegaba a una solicitud nacida del carrito. El escenario (servidor, empresa, Edge) y las
// ayudas (expectText, sendSealedIncoming, p3WaitCounter…) son los de p3_entrante_test.go.
//
// Todo entra por la API pública: el catálogo por PUT /api/v1/tenant-content/{ref}, el flujo por
// POST /api/v1/flows y el disparo por POST /api/v1/triggers. Este fichero no siembra nada por SQL.

// Los textos de este recorrido. Son conducta observable (lo que recibe una persona en su WhatsApp),
// copiados literales de la transcripción del carrito (internal/flujos/modules/cart/testdata,
// cart_v1_transcript.golden.txt) y medidos contra el binario viejo.
const (
	cartCategories  = "🛒 Elige una categoría:\n1) Bebidas\n2) Postres"
	cartBadOption   = "Opción no válida. Responde con el número de una de las opciones.\n\n" + cartCategories
	cartArticles    = "Bebidas:\n1) Café · $2.50\n2) Té · $2.00\n0) ← Volver"
	cartArticle     = "Café · $2.50\n1) Ver descripción\n2) Agregar al pedido\n0) ← Volver"
	cartQuantity    = "¿Cuántos \"Café\"? Escribe la cantidad (0 ← volver)"
	cartBadQuantity = "Escribe una cantidad válida (un número mayor o igual a 1).\n\n" + cartQuantity
	cartAdded       = "Añadido al pedido ✅\n1) Agregar más de Bebidas\n2) Finalizar pedido\n3) ✏️ Indicación para este artículo\n9) Cancelar pedido\n0) ← Volver"
	cartSummary     = "🧾 Resumen del pedido:\nCafé x2  $5.00\nTOTAL  $5.00\n1) Confirmar y finalizar\n2) Seguir agregando\n3) ✏️ Indicación para todo el pedido\n9) Cancelar pedido"
	cartConfirmed   = "✅ ¡Pedido confirmado! Total $5.00."

	// cartFlowID, cartKeyword y cartContentRef son el flujo de prueba, la palabra que abre su evento
	// y la ref de tenant_content de la que su único nodo lee el catálogo.
	cartFlowID     = "cart-p3"
	cartKeyword    = "carrito"
	cartContentRef = "carta"
	// cartPn es el número del contacto que hace el pedido.
	cartPn = "573001110070"
	// cartEndNode es el centinela que el motor deja en flow_state.current_node cuando el flujo
	// terminó (internal/flujos/model: el fin de flujo declarado por el módulo).
	cartEndNode = "__wapp_flow_end__"
	// cartLine es la línea del pedido tal como queda en JSONB (claves en el orden de Postgres), y
	// cartClosedPayload el payload de `cart_closed` y de la solicitud: las líneas y el total.
	cartLine          = `{"qty": 2, "sku": "CAFE", "label": "Café", "unit_price": 2.5}`
	cartClosedPayload = `{"items": [` + cartLine + `], "total": 5}`

	// cartReplyBurst y cartReplyEvery son el tope anti-bucle de auto-respuestas por conversación con
	// el que arranca el servidor (internal/platform/config: WAPP_FLOW_REPLY_BURST = 3 y
	// WAPP_FLOW_REPLY_RATE = 0,5 por segundo, una ficha cada 2 s). El arnés no los mueve: son los de
	// producción. cartReplyEvery lleva 100 ms de margen sobre esos 2 s.
	cartReplyBurst = 3
	cartReplyEvery = 2100 * time.Millisecond

	// cartMsgUnresolved es la línea WARN con la que el motor dice que una consulta al modelo no se
	// resolvió (internal/bootstrap/arranque/fase7_flujos.go, observaConsultas).
	cartMsgUnresolved = "flujos: consulta NO resuelta"

	// Las tres series de la racha de auto-respuestas en /metrics: el gauge de la racha viva más larga
	// (lo TIRA el scrape del runtime) y la cuenta y la suma del histograma de rachas cerradas (las
	// EMPUJA el runtime al cerrar un episodio).
	cartStreakMax   = "wapp_flow_autoreply_streak_max"
	cartStreakCount = "wapp_flow_autoreply_streak_count"
	cartStreakSum   = "wapp_flow_autoreply_streak_sum"
)

// cartCatalog es el catálogo del recorrido: el v1 de la transcripción (catalog_v1.json), dos
// categorías y tres artículos, sin un solo campo del contrato v2.
func cartCatalog() map[string]any {
	item := func(code, sku, label string, price float64, description string) map[string]any {
		return map[string]any{"code": code, "sku": sku, "label": label, "price": price, "description": description}
	}
	return map[string]any{"categories": []any{
		map[string]any{"code": "1", "label": "Bebidas", "items": []any{
			item("1", "CAFE", "Café", 2.5, "Espresso doble"),
			item("2", "TE", "Té", 2.0, "Verde o negro"),
		}},
		map[string]any{"code": "2", "label": "Postres", "items": []any{
			item("1", "FLAN", "Flan", 3.0, "Casero"),
		}},
	}}
}

// installCart deja a la empresa lista para vender por carrito, por la API pública: el catálogo en
// tenant_content, un flujo de UN nodo `cart` que lo lee (`content.source` json) y el disparo
// `event_start` de tipo `cart` que lo abre. Un disparo `keyword` no vale: arrancaría el flujo sin
// evento padre, y la solicitud del carrito cuelga de su evento. Comprueba las tres respuestas y sus
// filas. Falla (t.Fatalf) si algo no sale.
func (sc p3Scene) installCart(t *testing.T) {
	t.Helper()
	r := sc.Pub.Put(t, "/api/v1/tenant-content/"+cartContentRef, cartCatalog())
	if r.Codigo != http.StatusOK || strings.TrimSpace(string(r.Cuerpo)) != `{"ref":"`+cartContentRef+`"}` {
		t.Fatalf("guardar el catálogo: HTTP %d, quería 200 con la ref\ncuerpo: %s", r.Codigo, recortar(r.Cuerpo))
	}
	def := map[string]any{
		"flow_id": cartFlowID, "version": 1, "initial": "root",
		"nodes": map[string]any{
			"root": map[string]any{"type": "cart", "content": map[string]string{"source": "json", "ref": cartContentRef}},
		},
	}
	if r = sc.Pub.Post(t, "/api/v1/flows", map[string]any{"definition": def}); r.Codigo != http.StatusCreated {
		t.Fatalf("publicar el flujo del carrito: HTTP %d, quería 201\ncuerpo: %s", r.Codigo, recortar(r.Cuerpo))
	}
	r = sc.Pub.Post(t, "/api/v1/triggers", map[string]string{
		"kind": "event_start", "keyword": cartKeyword, "event_kind": "cart", "flow_id": cartFlowID,
	})
	var trig struct {
		TriggerID string `json:"trigger_id"`
		Kind      string `json:"kind"`
		EventKind string `json:"event_kind"`
		FlowID    string `json:"flow_id"`
		Enabled   bool   `json:"enabled"`
	}
	r.JSON(t, &trig)
	if r.Codigo != http.StatusCreated || trig.TriggerID == "" || trig.Kind != "event_start" || trig.EventKind != "cart" || trig.FlowID != cartFlowID || !trig.Enabled {
		t.Fatalf("crear el disparo event_start: HTTP %d %+v, quería 201 con el disparo activo\ncuerpo: %s", r.Codigo, trig, recortar(r.Cuerpo))
	}
	if n := consultaEntero(t, sc.DB, `SELECT count(*) FROM public.tenant_content
		WHERE tenant_id::text = $1 AND ref = $2`, sc.Tenant, cartContentRef); n != 1 {
		t.Fatalf("tenant_content tiene %d filas del catálogo, quería 1", n)
	}
	if n := consultaEntero(t, sc.DB, `SELECT count(*) FROM public.flow_definitions
		WHERE tenant_id::text = $1 AND flow_id = $2 AND version = 1 AND definition->'nodes'->'root'->>'type' = 'cart'`, sc.Tenant, cartFlowID); n != 1 {
		t.Fatalf("flow_definitions tiene %d filas del flujo del carrito, quería 1", n)
	}
	if n := consultaEntero(t, sc.DB, `SELECT count(*) FROM public.flow_triggers
		WHERE tenant_id::text = $1 AND trigger_id = $2::uuid AND kind = 'event_start' AND event_kind = 'cart' AND flow_id = $3 AND enabled`,
		sc.Tenant, trig.TriggerID, cartFlowID); n != 1 {
		t.Fatalf("flow_triggers tiene %d filas del disparo, quería 1", n)
	}
}

// cartWaID es el wa_message_id del mensaje n (desde 1) del cliente del carrito.
func cartWaID(n int) string { return fmt.Sprintf("P3-CART-%d", n) }

// cartSay manda el mensaje n (desde 1) del cliente del carrito, sellado, con ese texto.
//
// ⏱️ A partir del mensaje cartReplyBurst+1 espera antes cartReplyEvery. No es una espera a ciegas de
// que el servidor termine (eso lo dice la pantalla que llega): es el ritmo del cliente. El motor
// contesta a una conversación con un tope anti-bucle de 3 respuestas seguidas y después una cada
// 2 s; un recorrido de nueve pantallas mandado sin pausa pierde la cuarta (medido: el servidor la
// corta con «auto-respuesta limitada por rate-limit de conversación» y el carrito no avanza). Quien
// llama ya leyó la pantalla anterior, así que la ficha gastada en ella tiene más de 2 s al mandar.
func (sc p3Scene) cartSay(t *testing.T, n int, text string) {
	t.Helper()
	if n > cartReplyBurst {
		select {
		case <-time.After(cartReplyEvery):
		case <-t.Context().Done():
			t.Fatalf("el test terminó esperando el ritmo del mensaje %d", n)
		}
	}
	sc.Edge.sendSealedIncoming(t, sealedIncoming{From: cartPn + "@s.whatsapp.net", WaID: cartWaID(n), Text: text, FromPn: cartPn})
}

// cartWaitMetric espera, con tope, a que la serie SIN etiquetas name de /metrics valga exactamente
// want. Falla (t.Fatalf) si no llega a valerlo.
func cartWaitMetric(t *testing.T, s *servidor, name string, want float64) {
	t.Helper()
	edgeEsperar(t, edgeTopeFila, fmt.Sprintf("que %s valga %v en /metrics", name, want), func() bool {
		// p3Counter busca la muestra cuya etiqueta dada vale el valor dado; con la etiqueta vacía
		// casa la muestra sin etiquetas (una etiqueta ausente se lee «»).
		return p3Counter(t, s, name, "", "") == want
	})
}

// cartStateQuery lee, del estado de la conversación del carrito, el flujo, el nodo, si los dos
// punteros de evento (activo y dueño) están puestos y son el mismo, el último wa_message_id, el
// nivel del carrito, sus líneas y el desenlace que el módulo declaró.
const cartStateQuery = `SELECT concat_ws('|', flow_id, current_node,
		CASE WHEN event_id IS NULL AND owner_event_id IS NULL THEN 'sin evento'
		     WHEN event_id = owner_event_id THEN 'con evento' ELSE 'punteros distintos' END,
		coalesce(last_wa_message_id, ''), coalesce(vars->'cart'->>'level', ''),
		coalesce((vars->'cart'->'lines')::text, ''), coalesce(vars->>'flow_outcome', ''))
	FROM public.flow_state WHERE tenant_id::text = $1 AND session_id = $2`

// TestP3_CartConversation recorre una conversación entera de carrito contra el servidor real: una
// empresa con el plan por defecto (trae `cart_basic` y `llm_intake`), su catálogo, un flujo de un
// nodo `cart`, el disparo `event_start` y un Edge conectado con la sesión en perfil activo.
//
//   - «carrito» abre el evento: la bienvenida (una vez, por `llm_intake`) y las categorías.
//   - «8» no es una categoría: el aviso y las categorías otra vez, sin preguntarle a ningún modelo.
//   - «1», «1», «2»: Bebidas, Café, la cantidad.
//   - «abc» no es una cantidad: el carrito le pregunta al modelo de la empresa (UNA inferencia baja
//     al Edge, que no tiene Ollama), la consulta no se resuelve y pide la cantidad otra vez.
//   - «2», «2»: la línea añadida y el resumen. La racha viva de auto-respuestas vale 8.
//   - «1» confirma: la pantalla de cierre, y en Postgres el evento cerrado, su solicitud cerrada con
//     su línea y su revisión, los efectos en flow_events y el hilo. La racha viva vale 9.
//   - «1» otra vez, ya cerrado: ningún texto; el estado terminal se suelta y la racha se cierra.
//
// Ninguna línea ERROR en el log del servidor y ningún texto de más en el Edge.
func TestP3_CartConversation(t *testing.T) {
	t.Parallel()
	sc := p3NewScene(t, "p3cart", "p3-carrito")
	sc.installCart(t)
	sc.setProfile(t, "active")

	p3CartBrowse(t, sc)
	p3CartConfirm(t, sc)
	p3CartAfterClose(t, sc)

	if errs := sc.Edge.Errores(); len(errs) != 0 {
		t.Errorf("errores del núcleo del Edge: %v", errs)
	}
	if strings.Contains(sc.S.Log(), cartPn) {
		t.Errorf("el número del contacto aparece en el log del servidor")
	}
	edgeSinErrores(t, sc.S, nil)
}

// p3CartBrowse va de la palabra que abre el evento hasta el resumen del pedido, con los dos
// adversarios por el camino, y comprueba cada pantalla en orden. Al llegar al resumen el estado de
// la conversación sigue en el nodo del carrito con su evento, la solicitud está abierta y sin
// total, y el gauge de la racha viva vale 8: las ocho pantallas del flujo (la bienvenida no cuenta,
// no la emite el flujo).
func p3CartBrowse(t *testing.T, sc p3Scene) {
	t.Helper()
	sc.cartSay(t, 1, cartKeyword)
	sc.expectText(t, cartPn, p3Welcome)
	sc.expectText(t, cartPn, cartCategories)

	sc.cartSay(t, 2, "8")
	sc.expectText(t, cartPn, cartBadOption)
	if n := len(sc.Edge.Inferencias()); n != 0 {
		t.Errorf("una categoría que no existe hizo bajar %d inferencias al Edge, quería 0", n)
	}

	sc.cartSay(t, 3, "1")
	sc.expectText(t, cartPn, cartArticles)
	sc.cartSay(t, 4, "1")
	sc.expectText(t, cartPn, cartArticle)
	sc.cartSay(t, 5, "2")
	sc.expectText(t, cartPn, cartQuantity)

	// «abc» en la cantidad: ni el código exacto ni la cascada determinista lo resuelven, así que el
	// carrito eleva una consulta y el motor se la pregunta al modelo de la empresa. La vía es la
	// local (el Edge), que contesta OLLAMA_DOWN: la consulta falla y el carrito repregunta.
	sc.cartSay(t, 6, "abc")
	sc.expectText(t, cartPn, cartBadQuantity)
	if n := len(sc.Edge.Inferencias()); n != 1 {
		t.Errorf("la cantidad ininteligible hizo bajar %d inferencias al Edge, quería 1 (la consulta al modelo)", n)
	}
	if l := edgeEsperarLinea(t, sc.S, cartMsgUnresolved, "nivel", "quantity"); l["clase"] != "cantidad" || l["desenlace"] != "fallo" {
		t.Errorf("la consulta no resuelta se anotó como %v, quería clase cantidad y desenlace fallo", l)
	}

	sc.cartSay(t, 7, "2")
	sc.expectText(t, cartPn, cartAdded)
	sc.cartSay(t, 8, "2")
	sc.expectText(t, cartPn, cartSummary)

	edgeEsperarValor(t, sc.DB, cartFlowID+"|root|con evento|"+cartWaID(8)+"|summary|["+cartLine+"]|", "flow_state en el resumen",
		cartStateQuery, sc.Tenant, sc.Edge.SessionID)
	edgeEsperarValor(t, sc.DB, "open|0", "la solicitud del carrito antes de confirmar",
		`SELECT string_agg(status || '|' || total::text, ',') FROM public.intakes WHERE tenant_id::text = $1`, sc.Tenant)
	cartWaitMetric(t, sc.S, cartStreakMax, 8)
	sc.expectNoPendingText(t, "tras el resumen")
}

// p3CartConfirm confirma el pedido y comprueba la pantalla de cierre y lo que queda en Postgres:
//
//   - flow_state en el centinela de fin de flujo, sin punteros de evento, con el carrito en `closed`
//     y el desenlace `completed`;
//   - UN evento de conversación de tipo `cart`, cerrado;
//   - seis filas en flow_events, en este orden: el nacimiento del evento, el arranque del carrito,
//     la categoría, la línea, el cierre del evento y `cart_closed` (el único `persist`) con las
//     líneas y el total;
//   - UNA solicitud `order` cerrada, colgada de ese evento, con su línea, su revisión 1 y sin datos
//     de comprador; nada en webhook_outbox (la empresa no tiene integración);
//   - el hilo del evento: cada mensaje del cliente y cada pantalla, cifrados, y la decisión de la
//     línea en claro; y la ventana de captación con las nueve referencias.
//
// El gauge de la racha viva sube a 9: el estado terminal sigue ahí, y con él el episodio.
func p3CartConfirm(t *testing.T, sc p3Scene) {
	t.Helper()
	sc.cartSay(t, 9, "1")
	sc.expectText(t, cartPn, cartConfirmed)

	wait := func(want, what, query string, args ...any) {
		t.Helper()
		edgeEsperarValor(t, sc.DB, want, what, query, append([]any{sc.Tenant}, args...)...)
	}
	wait(cartFlowID+"|"+cartEndNode+"|sin evento|"+cartWaID(9)+"|closed|["+cartLine+"]|completed", "flow_state tras confirmar",
		cartStateQuery, sc.Edge.SessionID)
	wait("cart|closed|"+cartFlowID+"|1|true|true", "el evento de conversación del carrito",
		`SELECT string_agg(concat_ws('|', kind, status, flow_id, flow_version, (closed_at IS NOT NULL)::text, (history_id LIKE 'cart-%')::text), ',')
		FROM public.conversation_events WHERE tenant_id::text = $1 AND session_id = $2`, sc.Edge.SessionID)
	wait("event:event_started,event:cart_started,event:category_selected,event:item_added,event:event_closed,persist:cart_closed",
		"los efectos del carrito en flow_events",
		`SELECT string_agg(kind || ':' || name, ',' ORDER BY id) FROM public.flow_events WHERE tenant_id::text = $1 AND flow_id = $2`, cartFlowID)
	wait(cartLine+"\n"+cartClosedPayload, "los payloads de item_added y cart_closed",
		`SELECT string_agg(payload::text, E'\n' ORDER BY id) FROM public.flow_events
		WHERE tenant_id::text = $1 AND name IN ('item_added', 'cart_closed')`)
	wait("order|closed|5|true|", "la solicitud del carrito",
		`SELECT string_agg(concat_ws('|', i.intake_type, i.status, i.total, (i.event_id::text = e.id::text AND i.contact_id::text = e.contact_id::text)::text, i.customer_note), ',')
		FROM public.intakes i JOIN public.conversation_events e ON e.tenant_id::text = i.tenant_id::text AND e.session_id = i.session_id
		WHERE i.tenant_id::text = $1`)
	wait("CAFE|Café|2|2.5|", "las líneas de la solicitud",
		`SELECT string_agg(concat_ws('|', it.sku, it.label, it.qty, it.unit_price, it.customization), ',' ORDER BY it.id)
		FROM public.intake_items it JOIN public.intakes i ON i.id::text = it.intake_id::text WHERE i.tenant_id::text = $1`)
	wait(`1|cart|system|{"items": [`+cartLine+`], "total": 5, "version": 1}`, "la revisión de la solicitud",
		`SELECT string_agg(concat_ws('|', r.revision_no, r.kind, r.created_by, r.payload::text), ',' ORDER BY r.revision_no)
		FROM public.intake_revisions r JOIN public.intakes i ON i.id::text = r.intake_id::text WHERE i.tenant_id::text = $1`)

	// El hilo: por cada uno de los nueve mensajes, la voz del cliente y la pantalla del negocio, y
	// delante del séptimo turno (la cantidad que añade la línea) la decisión de esa línea. La
	// bienvenida no está: sale antes de que exista el evento.
	turn := "client:message,business:message"
	thread := strings.Join([]string{turn, turn, turn, turn, turn, turn, "client:decision", turn, turn, turn}, ",")
	wait(thread, "el hilo del evento",
		`SELECT string_agg(m.role || ':' || m.entry_kind, ',' ORDER BY m.seq)
		FROM public.conversation_event_messages m JOIN public.conversation_events e ON e.id::text = m.event_id::text WHERE e.tenant_id::text = $1`)
	wait("18|1", "el cifrado del hilo (mensajes cifrados | decisiones en claro con su línea)",
		`SELECT count(*) FILTER (WHERE m.entry_kind = 'message' AND m.payload IS NULL AND length(m.body_enc) > 0 AND length(m.body_dek) > 0)::text
			|| '|' || count(*) FILTER (WHERE m.entry_kind = 'decision' AND m.body_enc IS NULL AND m.payload = $2::jsonb)::text
		FROM public.conversation_event_messages m JOIN public.conversation_events e ON e.id::text = m.event_id::text WHERE e.tenant_id::text = $1`, cartLine)
	refs := make([]string, 0, 9)
	for n := 1; n <= 9; n++ {
		refs = append(refs, cartWaID(n))
	}
	wait(strings.Join(refs, ","), "las referencias de la ventana de captación del evento",
		`SELECT string_agg((SELECT string_agg(ref, ',' ORDER BY ord) FROM jsonb_array_elements_text(j.source_refs) WITH ORDINALITY AS x(ref, ord)), ';')
		FROM public.intake_jobs j JOIN public.conversation_events e ON e.id::text = j.event_id::text WHERE j.tenant_id::text = $1`)

	for table, want := range map[string]int{"intake_buyer_data": 0, "webhook_outbox": 0, "conversation_welcomes": 1, "owner_degradation_notices": 1} {
		if n := consultaEntero(t, sc.DB, `SELECT count(*) FROM public.`+table); n != want {
			t.Errorf("%s tiene %d filas, quería %d", table, n, want)
		}
	}
	p3PhoneNotInClear(t, sc, cartPn)
	cartWaitMetric(t, sc.S, cartStreakMax, 9)
	sc.expectNoPendingText(t, "tras confirmar el pedido")
}

// p3CartAfterClose manda un mensaje más con el pedido ya confirmado. El flujo terminó y «1» no
// casa ningún disparo: el motor suelta el estado terminal (la fila de flow_state desaparece) y no
// contesta nada. Soltarlo cierra el episodio de auto-respuestas: el gauge de la racha viva baja a 0
// y el histograma recibe UNA racha de 9. El evento y su solicitud no se mueven, y el mensaje no
// entra en la ventana de captación del evento cerrado.
func p3CartAfterClose(t *testing.T, sc p3Scene) {
	t.Helper()
	sc.cartSay(t, 10, "1")
	edgeEsperarValor(t, sc.DB, "0", "flow_state tras el mensaje posterior al cierre",
		`SELECT count(*)::text FROM public.flow_state WHERE tenant_id::text = $1`, sc.Tenant)
	cartWaitMetric(t, sc.S, cartStreakMax, 0)
	cartWaitMetric(t, sc.S, cartStreakCount, 1)
	cartWaitMetric(t, sc.S, cartStreakSum, 9)
	sc.expectNoPendingText(t, "mensaje posterior al cierre")

	if n := consultaEntero(t, sc.DB, `SELECT count(*) FROM public.conversation_events
		WHERE tenant_id::text = $1 AND kind = 'cart' AND status = 'closed'`, sc.Tenant); n != 1 {
		t.Errorf("conversation_events tiene %d eventos de carrito cerrados, quería 1", n)
	}
	if n := consultaEntero(t, sc.DB, `SELECT count(*) FROM public.conversation_events WHERE tenant_id::text = $1`, sc.Tenant); n != 1 {
		t.Errorf("conversation_events tiene %d filas, quería 1: el mensaje posterior al cierre no abre otro evento", n)
	}
	if n := consultaEntero(t, sc.DB, `SELECT count(*) FROM public.intakes WHERE tenant_id::text = $1 AND status = 'closed' AND total = 5`, sc.Tenant); n != 1 {
		t.Errorf("intakes tiene %d solicitudes cerradas de 5, quería 1", n)
	}
	if n := consultaEntero(t, sc.DB, `SELECT coalesce(sum(jsonb_array_length(source_refs)), 0) FROM public.intake_jobs WHERE tenant_id::text = $1`, sc.Tenant); n != 9 {
		t.Errorf("la ventana de captación tiene %d referencias, quería 9: el mensaje posterior al cierre no entra", n)
	}
	if n := consultaEntero(t, sc.DB, `SELECT count(*) FROM public.flow_events WHERE tenant_id::text = $1`, sc.Tenant); n != 6 {
		t.Errorf("flow_events tiene %d filas, quería 6: el mensaje posterior al cierre no escribe ninguna", n)
	}
}
