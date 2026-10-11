//go:build integracion

package procesos

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// P3 · Del entrante a la respuesta (diseno.md §4 · T9.15), pasos 1–5: el recorrido de un mensaje
// de WhatsApp desde que el Edge lo sube sellado hasta que el servidor le manda la respuesta del
// flujo, con el perfil de la sesión, el dedupe de ingesta, los acuses y el aviso de sesión pasiva.
// Los casos adversarios van en p3_entrante_adversarial_test.go, las tres reglas de public.contacts
// (pasos 6–8) en p3_entrante_contacts_test.go y el reintento de WithTx (paso 9) en
// p3_entrante_txretry_test.go. Aquí viven además el escenario y las ayudas que comparten los cuatro.

// Los textos que el servidor manda en este recorrido. Son conducta observable (lo que recibe una
// persona en su WhatsApp), medida contra el binario viejo. p3MenuPrompt, p3SalesText y
// p3SupportText los pone el propio test en la definición del flujo; los otros dos los pone el
// servidor.
const (
	// p3Welcome es la bienvenida por defecto: sale UNA vez, antes de cualquier respuesta del flujo,
	// al primer entrante de un contacto nuevo en una empresa cuyo plan trae la captación con LLM
	// (el plan por defecto del arnés). ✎ diseno.md §4 no la nombra: medida en T9.15.
	p3Welcome = "¡Hola! Recibimos tu mensaje y lo estamos procesando. Te respondemos en unos minutos."
	// p3MenuPrompt es el texto del nodo menú del flujo de prueba.
	p3MenuPrompt = "Hola, ¿con quién quieres hablar?\n1) Ventas\n2) Soporte"
	// p3SalesText y p3SupportText son los textos de los dos nodos destino del menú.
	p3SalesText   = "Te paso con Ventas."
	p3SupportText = "Cuéntame tu problema."
	// p3InvalidOption es lo que contesta un menú a una opción que no casa: el aviso y el menú otra vez.
	p3InvalidOption = "Opción no válida. Responde con el número de una de las opciones.\n\n" + p3MenuPrompt
	// p3HelpOption es lo que contesta en su lugar cuando los intentos fallidos seguidos llegan al tope.
	p3HelpOption = "No logré entender tu respuesta. Por favor elige una de las opciones escribiendo solo su número.\n\n" + p3MenuPrompt

	// p3FlowID y p3Keyword son el flujo de prueba y la palabra que lo dispara.
	p3FlowID  = "menu-p3"
	p3Keyword = "hola"

	// p3MsgIncomingFailed es la línea ERROR con la que el servidor dice que perdió un entrante: no
	// hay reintento por encima (internal/flujos/runtime/incoming.go, OnIncoming).
	p3MsgIncomingFailed = "runtime: procesar entrante"
	// p3MsgDuplicate es la línea (DEBUG) del entrante que el dedupe de ingesta corta.
	p3MsgDuplicate = "runtime: entrante duplicado ignorado (dedupe de ingesta)"
	// p3MsgRateLimited es la línea (WARN) de la respuesta que el tope por conversación no deja salir.
	p3MsgRateLimited = "runtime: auto-respuesta limitada por rate-limit de conversación"
	// p3MsgSaturated es el prefijo de la línea (WARN) del entrante descartado por falta de cupo.
	p3MsgSaturated = "runtime: entrante descartado por saturación"

	// p3MetricBlocked y p3MetricReceipts son los dos contadores que este proceso mira en /metrics.
	p3MetricBlocked  = "wapp_flow_reactive_blocked_total"
	p3MetricReceipts = "wapp_receipts_total"

	// p3FrozenNoticeID es el identificador del literal congelado del aviso de sesión pasiva, y
	// p3FrozenNoticeFile el fichero del repo que lo contiene, relativo a test/procesos.
	p3FrozenNoticeID   = "AVISO_SESION_PASIVA_V1"
	p3FrozenNoticeFile = "../../documentations/literal-aviso-sesion-pasiva.md"

	// p3SelfPn es el número propio que la sesión declara en su latido. Ningún remitente lo usa, salvo
	// en p3_entrante_selfloop_test.go, que es el proceso de esa regla: un entrante del número propio
	// de una sesión activa se corta por anti-bucle.
	p3SelfPn = "573009990000"

	// p3TextTimeout es el tope para que llegue un SendText del servidor.
	p3TextTimeout = 10 * time.Second
)

// p3Scene es el escenario de un test de P3: servidor, empresa, administradora, y un Edge enrolado
// y conectado con un lease vigente, más el cliente de la API pública con el token de la administradora.
type p3Scene struct {
	edgeEscenario
	Edge *edge
	Pub  *clienteHTTP
}

// p3NewScene arranca un servidor con su base para el proceso dado, crea la empresa slug con su
// administradora, y enrola y conecta un Edge. Vuelve cuando el Edge puede operar y ya recibió el
// primer empuje de filtros (el de registro de la sesión, que nace PASIVA). Falla (t.Fatalf) si algo
// de eso no sale.
func p3NewScene(t *testing.T, proceso, slug string) p3Scene {
	t.Helper()
	esc := edgeEscenarioNuevo(t, proceso, slug, true)
	e := enrolar(t, esc.S, edgeEmitirCodigo(t, esc.S, esc.TokenStaff, esc.Tenant))
	e.conectar(t)
	e.esperarLeases(t, 2, edgeTopeFila)
	if !e.puedeOperar() {
		t.Fatalf("el Edge recién conectado no puede operar")
	}
	e.esperarConfig(t, "filters", edgeTopeFila)
	return p3Scene{edgeEscenario: esc, Edge: e, Pub: esc.S.Publica(esc.TokenAdmin)}
}

// p3ActiveMenuScene es p3NewScene con la sesión ya en perfil activo y el menú instalado: lo que
// necesitan los pasos 6–9, donde cada entrante tiene que llegar hasta public.contacts.
func p3ActiveMenuScene(t *testing.T, proceso, slug string) p3Scene {
	t.Helper()
	sc := p3NewScene(t, proceso, slug)
	sc.installMenu(t)
	sc.setProfile(t, "active")
	return sc
}

// profileQuery lee el perfil de la sesión del Edge en la flota.
const p3ProfileQuery = `SELECT profile FROM public.fleet_sessions WHERE tenant_id = $1::uuid AND edge_id = $2 AND session_id = $3`

// filterPushes devuelve los ConfigUpdate de kind «filters» que el Edge lleva recibidos, en orden.
func (sc p3Scene) filterPushes() []configRecibida {
	var out []configRecibida
	for _, c := range sc.Edge.Configs() {
		if c.Kind == "filters" {
			out = append(out, c)
		}
	}
	return out
}

// setProfile cambia el perfil de la sesión por la API pública y comprueba las tres caras del cambio:
// la respuesta (200 con session_id y profile), el empuje al Edge (un ConfigUpdate «filters» MÁS, que
// trae ese perfil para esta sesión) y la fila de fleet_sessions. Falla (t.Fatalf) si alguna no se da.
func (sc p3Scene) setProfile(t *testing.T, profile string) {
	t.Helper()
	before := len(sc.filterPushes())
	r := sc.Pub.Post(t, "/api/v1/sessions/"+sc.Edge.SessionID+"/profile", map[string]string{"profile": profile})
	var body struct {
		SessionID string `json:"session_id"`
		Profile   string `json:"profile"`
	}
	r.JSON(t, &body)
	if r.Codigo != http.StatusOK || body.SessionID != sc.Edge.SessionID || body.Profile != profile {
		t.Fatalf("perfil %s: HTTP %d %+v, quería 200 con la sesión y el perfil\ncuerpo: %s", profile, r.Codigo, body, recortar(r.Cuerpo))
	}
	edgeEsperar(t, edgeTopeFila, "el empuje de filtros tras cambiar el perfil a "+profile, func() bool {
		return len(sc.filterPushes()) == before+1
	})
	var pushed struct {
		Sessions map[string]struct {
			Profile string `json:"profile"`
		} `json:"sessions"`
	}
	last := sc.filterPushes()[before]
	if err := json.Unmarshal(last.Payload, &pushed); err != nil {
		t.Fatalf("el empuje de filtros no es JSON: %v\n%s", err, last.Payload)
	}
	if got := pushed.Sessions[sc.Edge.SessionID].Profile; got != profile {
		t.Fatalf("el empuje de filtros trae el perfil %q para la sesión, quería %q\n%s", got, profile, last.Payload)
	}
	edgeEsperarValor(t, sc.DB, profile, "el perfil en fleet_sessions", p3ProfileQuery, sc.Tenant, sc.Edge.EdgeID, sc.Edge.SessionID)
}

// installMenu publica el flujo de prueba (un menú con dos opciones que llevan a dos mensajes
// finales) y el disparo por palabra clave que lo arranca, por la API pública, y comprueba las dos
// respuestas 201 y las filas de flow_definitions y flow_triggers. Falla (t.Fatalf) si algo no sale.
func (sc p3Scene) installMenu(t *testing.T) {
	t.Helper()
	def := map[string]any{
		"flow_id": p3FlowID, "version": 1, "initial": "root",
		"nodes": map[string]any{
			"root":    map[string]any{"type": "menu", "prompt": p3MenuPrompt, "options": map[string]string{"1": "ventas", "2": "soporte"}},
			"ventas":  map[string]any{"type": "message", "text": p3SalesText, "next": nil},
			"soporte": map[string]any{"type": "message", "text": p3SupportText, "next": nil},
		},
	}
	r := sc.Pub.Post(t, "/api/v1/flows", map[string]any{"definition": def})
	var flow struct {
		FlowID  string `json:"flow_id"`
		Version int    `json:"version"`
	}
	r.JSON(t, &flow)
	if r.Codigo != http.StatusCreated || flow.FlowID != p3FlowID || flow.Version != 1 {
		t.Fatalf("publicar el flujo: HTTP %d %+v, quería 201 con %s v1\ncuerpo: %s", r.Codigo, flow, p3FlowID, recortar(r.Cuerpo))
	}
	r = sc.Pub.Post(t, "/api/v1/triggers", map[string]string{"kind": "keyword", "keyword": p3Keyword, "flow_id": p3FlowID})
	var trig struct {
		TriggerID string `json:"trigger_id"`
		Kind      string `json:"kind"`
		Keyword   string `json:"keyword"`
		FlowID    string `json:"flow_id"`
		Enabled   bool   `json:"enabled"`
	}
	r.JSON(t, &trig)
	if r.Codigo != http.StatusCreated || trig.TriggerID == "" || trig.Kind != "keyword" || trig.Keyword != p3Keyword || trig.FlowID != p3FlowID || !trig.Enabled {
		t.Fatalf("crear el disparo: HTTP %d %+v, quería 201 con el disparo activo\ncuerpo: %s", r.Codigo, trig, recortar(r.Cuerpo))
	}
	if n := consultaEntero(t, sc.DB, `SELECT count(*) FROM public.flow_definitions
		WHERE tenant_id = $1::uuid AND flow_id = $2 AND version = 1 AND definition->>'initial' = 'root'`, sc.Tenant, p3FlowID); n != 1 {
		t.Fatalf("flow_definitions tiene %d filas del flujo, quería 1", n)
	}
	if n := consultaEntero(t, sc.DB, `SELECT count(*) FROM public.flow_triggers
		WHERE tenant_id = $1::uuid AND trigger_id = $2::uuid AND kind = 'keyword' AND keyword = $3 AND flow_id = $4 AND enabled`,
		sc.Tenant, trig.TriggerID, p3Keyword, p3FlowID); n != 1 {
		t.Fatalf("flow_triggers tiene %d filas del disparo, quería 1", n)
	}
}

// expectText espera el siguiente SendText del servidor y exige que vaya a to con ese texto exacto.
// Falla (t.Fatalf) si no llega en p3TextTimeout o si es otro: en este proceso el ORDEN de los
// textos es parte de lo que se afirma, y seguir tras un texto inesperado solo daría ruido.
func (sc p3Scene) expectText(t *testing.T, to, text string) {
	t.Helper()
	got := sc.Edge.esperarTexto(t, p3TextTimeout)
	if got.A != to || got.Texto != text {
		t.Fatalf("el Edge recibió un SendText a %q con %q; quería a %q con %q", got.A, got.Texto, to, text)
	}
}

// expectNoPendingText comprueba que el Edge no tiene ningún SendText sin leer. No espera: quien la
// llama ya comprobó, sondeando un efecto del servidor, que el entrante en cuestión terminó de
// procesarse. when dice en qué momento, para el mensaje. Falla con t.Errorf.
func (sc p3Scene) expectNoPendingText(t *testing.T, when string) {
	t.Helper()
	select {
	case got := <-sc.Edge.Textos():
		t.Errorf("%s: el Edge recibió un SendText que no debía llegar: a %q, %q", when, got.A, got.Texto)
	default:
	}
}

// p3Counter raspa GET :8100/metrics y devuelve el valor de la muestra del contador name cuya
// etiqueta label vale value, o 0 si aún no existe (un CounterVec no aparece hasta su primer
// incremento: trampa T-10). Falla (t.Fatalf) si /metrics no responde 200 o no se puede interpretar.
func p3Counter(t *testing.T, s *servidor, name, label, value string) float64 {
	t.Helper()
	r := s.Admin("").Get(t, "/metrics", nil)
	if r.Codigo != http.StatusOK {
		t.Fatalf("GET /metrics = %d, quería 200", r.Codigo)
	}
	exp, err := p0ParsearExposicion(string(r.Cuerpo))
	if err != nil {
		t.Fatalf("/metrics no se puede interpretar: %v", err)
	}
	for _, m := range exp.Muestras {
		if m.Nombre == name && m.Etiquetas[label] == value {
			return m.Valor
		}
	}
	return 0
}

// p3WaitCounter espera, con tope, a que el contador valga exactamente want. Falla (t.Fatalf) con el
// último valor visto si no llega.
func p3WaitCounter(t *testing.T, s *servidor, name, label, value string, want float64) {
	t.Helper()
	var last float64
	edgeEsperar(t, edgeTopeFila, fmt.Sprintf("que %s{%s=%q} valga %v", name, label, value, want), func() bool {
		last = p3Counter(t, s, name, label, value)
		return last == want
	})
}

// p3IncomingErrors devuelve las líneas ERROR «runtime: procesar entrante» del log del servidor: las
// del wa_message_id dado o, si es vacío, todas.
func p3IncomingErrors(s *servidor, waID string) []map[string]any {
	var out []map[string]any
	for _, l := range s.LineasLog() {
		if l["level"] != "ERROR" || l["msg"] != p3MsgIncomingFailed {
			continue
		}
		if waID == "" || l["wa_message_id"] == waID {
			out = append(out, l)
		}
	}
	return out
}

// p3PassiveNotice lee del repo, en ejecución, el literal congelado del aviso de sesión pasiva: el
// bloque ```text que sigue a la línea «ID del literal» con p3FrozenNoticeID. El texto NO se copia
// al test (🔒 contrato congelado). Falla (t.Fatalf) si el fichero no está o cambió de forma.
func p3PassiveNotice(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(p3FrozenNoticeFile)
	if err != nil {
		t.Fatalf("leer el literal del aviso: %v", err)
	}
	lines := strings.Split(string(raw), "\n")
	i := 0
	for ; i < len(lines); i++ {
		if strings.Contains(lines[i], "ID del literal") && strings.Contains(lines[i], p3FrozenNoticeID) {
			break
		}
	}
	for ; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "```text" {
			break
		}
	}
	if i >= len(lines) {
		t.Fatalf("%s ya no declara el ID %s seguido de un bloque ```text", p3FrozenNoticeFile, p3FrozenNoticeID)
	}
	var body []string
	for j := i + 1; j < len(lines); j++ {
		if strings.HasPrefix(strings.TrimSpace(lines[j]), "```") {
			if len(body) == 0 {
				t.Fatalf("el bloque del aviso en %s está vacío", p3FrozenNoticeFile)
			}
			return strings.Join(body, "\n")
		}
		body = append(body, lines[j])
	}
	t.Fatalf("el bloque ```text del aviso en %s no se cierra", p3FrozenNoticeFile)
	return ""
}

// beatWithSelfPn manda un latido que declara el número propio de la sesión y espera a que el
// servidor conteste con la renovación del lease. El contador tiene que superar al último aplicado.
func (sc p3Scene) beatWithSelfPn(t *testing.T, counter int64) {
	t.Helper()
	before := sc.Edge.Leases()
	hb := edgeLatido(sc.Edge.SessionID, counter)
	hb.GetHeartbeat().SelfPn = p3SelfPn
	if err := sc.Edge.emitir(hb); err != nil {
		t.Fatalf("latido con número propio: %v", err)
	}
	sc.Edge.esperarLeases(t, before+1, edgeTopeFila)
}

// TestP3_IncomingToReply recorre los pasos 1–5 de P3 contra el servidor real, con un tenant, su
// administradora y un Edge conectado. El orden es el de la vida de una sesión, no el de la lista de
// diseno.md: la sesión NACE pasiva, así que el paso 5 (aviso y silencio) va primero y se repite al
// final, tras volver a pasiva.
//
//   - Sesión pasiva recién emparejada: al latir con su número propio recibe el aviso, byte a byte el
//     literal congelado, una sola vez; un entrante con la palabra clave no obtiene respuesta ni toca
//     public.contacts.
//   - Perfil activo (200, empuje de filtros, fila): el entrante sellado con la palabra clave obtiene
//     la bienvenida y el menú; la opción, el texto del nodo destino.
//   - El mismo wa_message_id otra vez: ninguna respuesta, una sola fila en ingest_dedupe.
//   - Acuses: filas en message_receipts y el contador en /metrics.
//   - La tabla adversaria (p3_entrante_adversarial_test.go).
//   - De vuelta a pasiva: ninguna respuesta.
//
// Las ERROR esperadas son las dos de los entrantes adversarios que no dejan ninguna ref de contacto.
func TestP3_IncomingToReply(t *testing.T) {
	t.Parallel()
	sc := p3NewScene(t, "p3", "p3-entrante")
	sc.installMenu(t)

	p3PassiveAtBirth(t, sc)
	sc.setProfile(t, "active")
	p3MenuRoundTrip(t, sc)
	p3Receipts(t, sc)
	lost := p3Adversaries(t, sc)
	p3BackToPassive(t, sc)

	if errs := sc.Edge.Errores(); len(errs) != 0 {
		t.Errorf("errores del núcleo del Edge: %v", errs)
	}
	edgeSinErrores(t, sc.S, map[string]int{p3MsgIncomingFailed: lost})
}

// p3PassiveAtBirth es el paso 5 sobre la sesión recién nacida. La sesión nace pasiva y sin número
// propio; al declarar el suyo en un latido, el servidor le manda a ESE número el aviso —el literal
// congelado, byte a byte— y marca greeted_at cuando el Edge lo acusa; dos latidos más no lo repiten.
// Un entrante con la palabra clave del menú, que con el perfil activo arrancaría el flujo, se corta:
// sube el contador de bloqueos por «passive», no sale ningún texto y no se resuelve ningún contacto
// (el corte va antes de contacts.Resolve), aunque el dedupe de ingesta sí lo anota.
func p3PassiveAtBirth(t *testing.T, sc p3Scene) {
	t.Helper()
	edgeEsperarValor(t, sc.DB, "passive", "el perfil de la sesión recién registrada", p3ProfileQuery, sc.Tenant, sc.Edge.EdgeID, sc.Edge.SessionID)
	sc.expectNoPendingText(t, "antes de declarar el número propio")

	sc.beatWithSelfPn(t, 5)
	sc.expectText(t, p3SelfPn, p3PassiveNotice(t))
	edgeEsperarValor(t, sc.DB, "true", "greeted_at tras el Ack del aviso",
		`SELECT (greeted_at IS NOT NULL)::text FROM public.fleet_sessions WHERE tenant_id = $1::uuid AND edge_id = $2 AND session_id = $3`,
		sc.Tenant, sc.Edge.EdgeID, sc.Edge.SessionID)
	// El aviso sale al final del trabajo del latido, después de renovar el lease, y los trabajos de
	// una sesión van en serie: cuando llega el lease del SEGUNDO latido de aquí, el trabajo del
	// primero ya terminó entero, con su decisión de avisar o no.
	sc.beatWithSelfPn(t, 7)
	sc.beatWithSelfPn(t, 9)
	sc.expectNoPendingText(t, "tras latir otra vez con la sesión ya avisada")

	blocked := p3Counter(t, sc.S, p3MetricBlocked, "reason", "passive")
	sc.Edge.sendSealedIncoming(t, sealedIncoming{From: "573001110090@s.whatsapp.net", WaID: "P3-PASSIVE-1", Text: p3Keyword, FromPn: "573001110090"})
	p3WaitCounter(t, sc.S, p3MetricBlocked, "reason", "passive", blocked+1)
	sc.expectNoPendingText(t, "entrante con la sesión pasiva")
	if n := consultaEntero(t, sc.DB, `SELECT count(*) FROM public.contacts WHERE tenant_id = $1::uuid`, sc.Tenant); n != 0 {
		t.Errorf("con la sesión pasiva hay %d filas en contacts, quería 0", n)
	}
	if n := consultaEntero(t, sc.DB, `SELECT count(*) FROM public.ingest_dedupe WHERE session_id = $1 AND wa_message_id = 'P3-PASSIVE-1'`, sc.Edge.SessionID); n != 1 {
		t.Errorf("ingest_dedupe tiene %d filas del entrante pasivo, quería 1", n)
	}
}

// p3BackToPassive vuelve la sesión a pasiva (200, empuje de filtros, fila) y manda la palabra clave
// desde un número nuevo: se corta igual que al nacer, sin texto y sin contacto nuevo. La sesión ya
// fue avisada, así que tampoco llega otro aviso.
func p3BackToPassive(t *testing.T, sc p3Scene) {
	t.Helper()
	sc.setProfile(t, "passive")
	contacts := consultaEntero(t, sc.DB, `SELECT count(*) FROM public.contacts WHERE tenant_id = $1::uuid`, sc.Tenant)
	blocked := p3Counter(t, sc.S, p3MetricBlocked, "reason", "passive")
	sc.Edge.sendSealedIncoming(t, sealedIncoming{From: "573001110091@s.whatsapp.net", WaID: "P3-PASSIVE-2", Text: p3Keyword, FromPn: "573001110091"})
	p3WaitCounter(t, sc.S, p3MetricBlocked, "reason", "passive", blocked+1)
	sc.expectNoPendingText(t, "entrante tras volver a pasiva")
	if n := consultaEntero(t, sc.DB, `SELECT count(*) FROM public.contacts WHERE tenant_id = $1::uuid`, sc.Tenant); n != contacts {
		t.Errorf("tras volver a pasiva contacts pasó de %d a %d filas", contacts, n)
	}
}

// p3MainPn es el número del contacto que recorre el camino feliz.
const p3MainPn = "573001110001"

// p3MenuRoundTrip son los pasos 2 y 3 con el perfil activo. El entrante sellado con la palabra clave
// recibe la bienvenida y el menú, y deja el estado de la conversación en el nodo raíz; la opción
// «1» recibe el texto de Ventas y deja anotado su wa_message_id. Después, el PRIMER wa_message_id
// repetido: el dedupe lo corta (su línea de log lo prueba), no sale ningún texto —sin dedupe,
// la palabra clave volvería a arrancar el menú—, ingest_dedupe sigue con una fila y el estado no se
// mueve. En Postgres queda un contacto de una fila en la que el teléfono no aparece en claro, y
// este recorrido (palabra clave a un menú plano) no escribe ni flow_events ni conversation_events.
func p3MenuRoundTrip(t *testing.T, sc p3Scene) {
	t.Helper()
	msg := sealedIncoming{From: p3MainPn + "@s.whatsapp.net", WaID: "P3-IN-1", Text: p3Keyword, FromPn: p3MainPn}
	const stateQuery = `SELECT flow_id || '|' || (current_node = 'root')::text || '|' || coalesce(last_wa_message_id, '')
		FROM public.flow_state WHERE tenant_id = $1::uuid AND session_id = $2`

	sc.Edge.sendSealedIncoming(t, msg)
	sc.expectText(t, p3MainPn, p3Welcome)
	sc.expectText(t, p3MainPn, p3MenuPrompt)
	edgeEsperarValor(t, sc.DB, p3FlowID+"|true|", "flow_state tras el menú", stateQuery, sc.Tenant, sc.Edge.SessionID)

	sc.Edge.sendSealedIncoming(t, sealedIncoming{From: msg.From, WaID: "P3-IN-2", Text: "1", FromPn: p3MainPn})
	sc.expectText(t, p3MainPn, p3SalesText)
	edgeEsperarValor(t, sc.DB, p3FlowID+"|false|P3-IN-2", "flow_state tras la opción", stateQuery, sc.Tenant, sc.Edge.SessionID)

	sc.Edge.sendSealedIncoming(t, msg)
	edgeEsperarLinea(t, sc.S, p3MsgDuplicate, "wa_message_id", msg.WaID)
	sc.expectNoPendingText(t, "wa_message_id repetido")
	if n := consultaEntero(t, sc.DB, `SELECT count(*) FROM public.ingest_dedupe WHERE session_id = $1 AND wa_message_id = $2`, sc.Edge.SessionID, msg.WaID); n != 1 {
		t.Errorf("ingest_dedupe tiene %d filas de %s, quería 1", n, msg.WaID)
	}
	edgeEsperarValor(t, sc.DB, p3FlowID+"|false|P3-IN-2", "flow_state tras el duplicado", stateQuery, sc.Tenant, sc.Edge.SessionID)

	p3PhoneNotInClear(t, sc, p3MainPn)
	for _, table := range []string{"flow_events", "conversation_events"} {
		if n := consultaEntero(t, sc.DB, `SELECT count(*) FROM public.`+table); n != 0 {
			t.Errorf("%s tiene %d filas; el viejo no escribe ninguna en este recorrido", table, n)
		}
	}
	if strings.Contains(sc.S.Log(), p3MainPn) {
		t.Errorf("el número del contacto aparece en el log del servidor")
	}
}

// p3PhoneNotInClear comprueba que el tenant tiene UN contacto de una fila phone_e164, con su valor
// cifrado y su índice ciego poblados, y que el literal del número no está en ninguna parte de la
// fila: ni dentro de value_enc o value_dek (como bytes), ni en ninguna columna vista como texto
// (to_jsonb de la fila entera: value_bidx, y cualquier columna en claro que apareciera).
func p3PhoneNotInClear(t *testing.T, sc p3Scene, pn string) {
	t.Helper()
	if n := consultaEntero(t, sc.DB, `SELECT count(*) FROM public.contacts
		WHERE tenant_id = $1::uuid AND kind = 'phone_e164' AND length(value_enc) > 0 AND length(value_dek) > 0
		  AND value_bidx <> '' AND value_kek_id <> ''`, sc.Tenant); n != 1 {
		t.Fatalf("contacts tiene %d filas phone_e164 con el valor cifrado, quería 1", n)
	}
	if n := consultaEntero(t, sc.DB, `SELECT count(*) FROM public.contacts WHERE tenant_id = $1::uuid`, sc.Tenant); n != 1 {
		t.Errorf("contacts tiene %d filas del tenant, quería 1", n)
	}
	if n := consultaEntero(t, sc.DB, `SELECT count(*) FROM public.contacts c
		WHERE tenant_id = $1::uuid AND (position($2::bytea in value_enc) > 0 OR position($2::bytea in value_dek) > 0
		   OR to_jsonb(c)::text LIKE '%' || $3 || '%')`, sc.Tenant, []byte(pn), pn); n != 0 {
		t.Errorf("el teléfono aparece en claro en %d filas de contacts", n)
	}
}

// p3Receipts es el paso 4: un acuse de «leído» y otro de «entregado» del mismo mensaje dejan dos
// filas en message_receipts y suben en uno cada serie de wapp_receipts_total. El «leído» repetido
// no añade fila (UNIQUE de sesión, mensaje y estado) pero el viejo SÍ lo cuenta: el contador mide
// acuses recibidos, no filas (medido en T9.15).
func p3Receipts(t *testing.T, sc p3Scene) {
	t.Helper()
	const waID = "P3-OUT-1"
	const rows = `SELECT coalesce(string_agg(status, ',' ORDER BY status), '') FROM public.message_receipts WHERE session_id = $1 AND message_id = $2`
	read := p3Counter(t, sc.S, p3MetricReceipts, "status", "read")
	delivered := p3Counter(t, sc.S, p3MetricReceipts, "status", "delivered")

	sc.Edge.acuse(t, waID, true)
	p3WaitCounter(t, sc.S, p3MetricReceipts, "status", "read", read+1)
	edgeEsperarValor(t, sc.DB, "read", "el acuse de leído en message_receipts", rows, sc.Edge.SessionID, waID)

	sc.Edge.acuse(t, waID, false)
	p3WaitCounter(t, sc.S, p3MetricReceipts, "status", "delivered", delivered+1)
	edgeEsperarValor(t, sc.DB, "delivered,read", "los dos acuses en message_receipts", rows, sc.Edge.SessionID, waID)

	sc.Edge.acuse(t, waID, true)
	p3WaitCounter(t, sc.S, p3MetricReceipts, "status", "read", read+2)
	edgeEsperarValor(t, sc.DB, "delivered,read", "message_receipts tras el acuse repetido", rows, sc.Edge.SessionID, waID)
}
