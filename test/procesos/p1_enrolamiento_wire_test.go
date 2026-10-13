//go:build integracion

package procesos

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"
)

// P1 · la cara del Edge POR EL CABLE (F3-05: T3.27 parte local, T3.29 y T3.30). Lo que
// TestP1_EnrollmentAndLease no recorre y que el cierre de F3 pide ver de punta a punta, con el
// binario entero, mTLS real y Postgres, igual contra el viejo y contra el nuevo:
//
//   - el envío por la API pública (POST /api/v1/messages) llega al Edge conectado y la respuesta
//     trae su Ack; los demás métodos sobre la ruta son 405;
//   - arrancar un flujo (POST /admin/flows/start y su gemela pública) hacia una sesión SIN stream
//     contesta el 502 de «sesión offline»: es la prueba de que el centinela ErrSessionOffline
//     conserva su identidad entre el gateway y el handler de flujos, que no son del mismo módulo;
//   - el número propio que la sesión declara en su latido se guarda cifrado y con índice ciego;
//   - el corte comercial avisa a TODOS los Edge vivos de la empresa y a ninguno de otra (R-G21).
//
// Lo que necesita reiniciar el servidor va en p1_enrolamiento_wire_restart_test.go.
//
// Diferencias intencionadas del nuevo que este test NO toca (plan/DECISIONES.md): ningún caso
// depende de una reconexión rápida (D-F3-9), los Edge del corte comercial son todos de flota
// —conectaron con sesión y latieron— (D-F3-10) y no se pide ningún diagnóstico (D-F3-11).

const (
	// p1WireOfflineText es el texto con el que la nube dice que la sesión no tiene stream vivo. Lo
	// escriben, cada uno por su cuenta, los cuatro handlers de envío (messages.go y flows.go de la API
	// pública, flujos/admin/handlers.go y platform/httpapi/admin.go): todos lo deciden con
	// errors.Is(err, session.ErrSessionOffline).
	p1WireOfflineText = "sesión offline: no hay stream vivo para el Edge"
	// p1WireSendFailed y p1WireAdminSendFailed son las líneas ERROR que deja cada 502 del envío: una
	// la puerta pública y otra la de admin. Las dos de arranque de flujo no dejan ninguna.
	p1WireSendFailed      = "envío por la API pública fallido"
	p1WireAdminSendFailed = "envío admin fallido"

	// p1WireFlowID y p1WireFlowText son el flujo mínimo del proceso: un solo nodo de mensaje.
	p1WireFlowID   = "p1-wire"
	p1WireFlowText = "Hola, te escribe la ferretería."

	rutaMessages     = "/api/v1/messages"
	rutaAdminSend    = "/admin/messages/send"
	rutaAdminStart   = "/admin/flows/start"
	p1WirePublicFlow = "/api/v1/flows/" + p1WireFlowID + "/start"

	// p1WireSelfPnPlus y p1WireSelfPn son dos grafías del MISMO número propio: con «+» y sin él. La
	// nube lo normaliza a la segunda antes de cifrarlo y de calcular su índice ciego.
	p1WireSelfPnPlus = "+573009990123"
	p1WireSelfPn     = "573009990123"
)

var (
	// p1WireAckBody es el cuerpo del 200 de un envío que el Edge acusó bien: el Ack, sin campo error.
	p1WireAckBody = regexp.MustCompile(`^\{"acked_command_id":"([^"]+)","ok":true\}$`)
	// p1WireOfflineJSON es el cuerpo del 502 del envío por la API pública: el texto y el command_id
	// que el gateway le asignó al comando antes de saber que no había stream.
	p1WireOfflineJSON = regexp.MustCompile(`^\{"error":"` + p1WireOfflineText + `","command_id":"[^"]+"\}$`)
	// p1WireOfflinePlain es el cuerpo del 502 del envío por la puerta de admin: el mismo texto, en
	// texto plano y con el command_id entre paréntesis.
	p1WireOfflinePlain = regexp.MustCompile(`^` + p1WireOfflineText + ` \(command_id: [^)]+\)\n$`)
	// p1WirePanic casa lo que escribe net/http, o el runtime, cuando un handler entra en pánico.
	p1WirePanic = regexp.MustCompile(`(?m)http: panic serving|^panic: |runtime error: |^goroutine \d+ \[`)
)

// p1WireRun es el estado que comparten los pasos: el escenario, dos Edge de la empresa del proceso,
// otro de OTRA empresa y los clientes HTTP de la administradora.
type p1WireRun struct {
	esc edgeEscenario
	// a y b son los dos Edge de la empresa del proceso; other es el de la otra empresa.
	a, b, other *edge
	otherTenant string
	pub, admin  *clienteHTTP
	// contacts cuenta los contactos usados: cada arranque de flujo va a uno nuevo, para que ninguno
	// tropiece con la conversación que dejó abierta el anterior (409).
	contacts int
	// sendFailures y adminSendFailures son las líneas ERROR p1WireSendFailed y p1WireAdminSendFailed
	// que el proceso provocó a propósito.
	sendFailures, adminSendFailures int
}

// TestP1_EdgeFaceOverTheWire recorre la cara del Edge por el cable sobre un solo servidor, con los
// pasos en orden (cada uno parte de lo que dejó el anterior). Las conexiones de los Edge se abren en
// el test padre: el stream vive mientras viva el contexto del test que lo abrió. Necesita Docker.
func TestP1_EdgeFaceOverTheWire(t *testing.T) {
	t.Parallel()
	esc := edgeEscenarioNuevo(t, "p1_wire", "p1-wire", true)
	p := &p1WireRun{esc: esc, pub: esc.S.Publica(esc.TokenAdmin), admin: esc.S.Admin(esc.TokenAdmin)}
	p.otherTenant = crearTenant(t, esc.S, esc.TokenStaff, "p1-wire-otra")
	p.a, p.b, p.other = p.newEdge(t, esc.Tenant), p.newEdge(t, esc.Tenant), p.newEdge(t, p.otherTenant)
	p.publishFlow(t)

	p.step(t, "messages_reach_the_online_edge", p.checkMessagesOnline)
	p.step(t, "messages_other_methods_405", p.checkMessagesMethods)
	p.step(t, "flow_start_reaches_the_online_edge", p.checkFlowStartOnline)
	p.step(t, "self_pn_sealed_with_blind_index", p.checkSelfPnSealed)
	p.step(t, "tenant_cut_reaches_both_live_edges", p.checkTenantCutBothEdges)
	p.a.desconectar(t)
	p.step(t, "offline_session_is_502", p.checkOfflineSession)
	p.step(t, "session_that_never_existed", p.checkUnknownSession)
	p.step(t, "no_panics_no_unexpected_errors", p.checkLog)
}

// step corre un paso como subtest y detiene el proceso si falla: los siguientes parten de él.
func (p *p1WireRun) step(t *testing.T, name string, fn func(t *testing.T)) {
	t.Helper()
	if !t.Run(name, fn) {
		t.Fatalf("la cara del Edge por el cable se detiene: falló el paso %s", name)
	}
}

// newEdge enrola un Edge en la empresa dada, lo conecta y espera la renovación de su primer latido:
// con eso el Edge opera y su sesión está en la flota (fleet_sessions), que es lo que D-F3-10 pide
// para que el corte comercial lo alcance en los dos binarios.
func (p *p1WireRun) newEdge(t *testing.T, tenant string) *edge {
	t.Helper()
	e := enrolar(t, p.esc.S, edgeEmitirCodigo(t, p.esc.S, p.esc.TokenStaff, tenant))
	e.conectar(t)
	e.esperarLeases(t, 2, edgeTopeFila)
	edgeEsperarValor(t, p.esc.DB, "online", "la sesión de "+e.EdgeID+" en la flota", edgeEstadoSesion, tenant, e.EdgeID, e.SessionID)
	if !e.puedeOperar() {
		t.Fatalf("el Edge %s recién conectado no puede operar", e.EdgeID)
	}
	return e
}

// publishFlow publica el flujo mínimo del proceso por la API pública: un nodo de mensaje y nada más.
func (p *p1WireRun) publishFlow(t *testing.T) {
	t.Helper()
	def := map[string]any{
		"flow_id": p1WireFlowID, "version": 1, "initial": "root",
		"nodes": map[string]any{"root": map[string]any{"type": "message", "text": p1WireFlowText, "next": nil}},
	}
	r := p.pub.Post(t, "/api/v1/flows", map[string]any{"definition": def})
	if want := `{"flow_id":"` + p1WireFlowID + `","version":1}`; r.Codigo != http.StatusCreated || string(r.Cuerpo) != want {
		t.Fatalf("publicar el flujo: HTTP %d %s, quería 201 %s", r.Codigo, recortar(r.Cuerpo), want)
	}
}

// contact devuelve un número de contacto que el proceso aún no ha usado.
func (p *p1WireRun) contact() string {
	p.contacts++
	return fmt.Sprintf("5730011101%02d", p.contacts)
}

// startBody es el cuerpo de un arranque de flujo hacia la sesión dada, con un contacto nuevo. La
// puerta de admin lleva el flow_id en el cuerpo; la pública lo lleva en la ruta y lo ignora aquí.
func (p *p1WireRun) startBody(sessionID string) (body map[string]any, contact string) {
	contact = p.contact()
	return map[string]any{
		"flow_id": p1WireFlowID, "session_id": sessionID,
		"contact_ref": map[string]string{"kind": "phone_e164", "value": contact},
	}, contact
}

// p1WireIs dice si la respuesta trae ese código y EXACTAMENTE ese cuerpo, byte a byte.
func p1WireIs(r respuesta, code int, body string) bool {
	return r.Codigo == code && string(r.Cuerpo) == body
}

// p1WireAcked exige el 200 con el Ack bueno del Edge y que el Edge haya «entregado» ESE comando: el
// mismo destinatario, el mismo texto y el command_id que la respuesta devuelve.
func p1WireAcked(t *testing.T, what string, r respuesta, e *edge, to, text string) {
	t.Helper()
	m := p1WireAckBody.FindSubmatch(r.Cuerpo)
	if r.Codigo != http.StatusOK || m == nil {
		t.Fatalf("%s: HTTP %d %s; quería 200 con %s", what, r.Codigo, recortar(r.Cuerpo), p1WireAckBody)
	}
	got := e.esperarTexto(t, 5*time.Second)
	if want := (textoRecibido{A: to, Texto: text, ComandoID: string(m[1])}); got != want {
		t.Errorf("%s: el Edge recibió %+v, quería %+v", what, got, want)
	}
}

// checkMessagesOnline es el hallazgo 70 de F3 en positivo: con el Edge conectado, el envío por la
// API pública sale por el stream de ESE Edge (lo recibe con su command_id y lo acusa) y el HTTP
// devuelve el Ack; la puerta de admin hace lo mismo. Y los rechazos de la puerta pública que no
// llegan al gateway: sin token 401, cuerpo incompleto 400, y una sesión de OTRA empresa —viva— 404
// sin que a su Edge le llegue nada.
func (p *p1WireRun) checkMessagesOnline(t *testing.T) {
	const to = "573001110000"
	for _, c := range []struct {
		name   string
		client *clienteHTTP
		path   string
	}{{"API pública", p.pub, rutaMessages}, {"admin", p.admin, rutaAdminSend}} {
		text := "hola por el cable (" + c.name + ")"
		r := c.client.Post(t, c.path, map[string]string{"session_id": p.a.SessionID, "to": to, "text": text})
		p1WireAcked(t, "enviar por "+c.name, r, p.a, to, text)
	}
	send := map[string]string{"session_id": p.a.SessionID, "to": to, "text": "no debe salir"}
	if r := p.esc.S.Publica("").Post(t, rutaMessages, send); r.Codigo != http.StatusUnauthorized {
		t.Errorf("enviar sin token: HTTP %d %s, quería 401", r.Codigo, recortar(r.Cuerpo))
	}
	if r := p.pub.Post(t, rutaMessages, map[string]string{"session_id": p.a.SessionID, "to": to}); !p1WireIs(r, http.StatusBadRequest, `{"error":"session_id, to y text son requeridos"}`) {
		t.Errorf("enviar sin texto: HTTP %d %s, quería 400", r.Codigo, recortar(r.Cuerpo))
	}
	send["session_id"] = p.other.SessionID
	if r := p.pub.Post(t, rutaMessages, send); !p1WireIs(r, http.StatusNotFound, `{"error":"sesión no encontrada para el tenant"}`) {
		t.Errorf("enviar a la sesión de otra empresa: HTTP %d %s, quería 404", r.Codigo, recortar(r.Cuerpo))
	}
	p.expectNoTexts(t, "tras los envíos rechazados")
}

// expectNoTexts comprueba que a ningún Edge le queda un texto sin leer: los rechazos ya contestaron,
// y un texto empujado por ellos estaría en el canal (el Edge «entrega» ANTES de acusar).
func (p *p1WireRun) expectNoTexts(t *testing.T, stage string) {
	t.Helper()
	for _, e := range []*edge{p.a, p.b, p.other} {
		if n := len(e.Textos()); n != 0 {
			t.Errorf("%s: el Edge %s tiene %d textos que nadie debió mandarle", stage, e.EdgeID, n)
		}
	}
}

// checkMessagesMethods es el hallazgo 70 en negativo: sobre /api/v1/messages solo existe POST. Los
// demás métodos contestan 405 con el cuerpo del enrutador, con token y sin él (el 405 lo decide el
// enrutador, antes de la autenticación), y no llegan a ningún handler: ni un texto al Edge. En el
// binario nuevo la ruta la sirve la cara nueva y el handler viejo quedó registrado detrás sin
// gateway: si un método lo alcanzara, sería un pánico, que el cierre del proceso busca en el log.
func (p *p1WireRun) checkMessagesMethods(t *testing.T) {
	body := map[string]string{"session_id": p.a.SessionID, "to": "573001110000", "text": "no debe salir"}
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		for name, client := range map[string]*clienteHTTP{"con token": p.pub, "sin token": p.esc.S.Publica("")} {
			if r := client.hacer(t, method, rutaMessages, body); !p1WireIs(r, http.StatusMethodNotAllowed, "Method Not Allowed\n") {
				t.Errorf("%s %s (%s): HTTP %d %q, quería 405 «Method Not Allowed»", method, rutaMessages, name, r.Codigo, recortar(r.Cuerpo))
			}
		}
	}
	if r := p.pub.hacer(t, http.MethodHead, rutaMessages, nil); !p1WireIs(r, http.StatusMethodNotAllowed, "") {
		t.Errorf("HEAD %s: HTTP %d %q, quería 405 sin cuerpo", rutaMessages, r.Codigo, recortar(r.Cuerpo))
	}
	p.expectNoTexts(t, "tras los métodos no permitidos")
}

// checkFlowStartOnline es el control positivo de T3.27: con el Edge conectado, arrancar el flujo
// por las dos puertas (admin y pública) manda su primer mensaje por el stream y devuelve el Ack. Sin
// este paso, el 502 de después podría deberse a un flujo mal publicado y no a la sesión sin stream.
// También el 405 de la puerta de admin, que lo decide el handler (tras la autenticación).
func (p *p1WireRun) checkFlowStartOnline(t *testing.T) {
	for _, c := range []struct {
		name   string
		client *clienteHTTP
		path   string
	}{{"admin", p.admin, rutaAdminStart}, {"API pública", p.pub, p1WirePublicFlow}} {
		body, contact := p.startBody(p.a.SessionID)
		p1WireAcked(t, "arrancar el flujo por "+c.name, c.client.Post(t, c.path, body), p.a, contact, p1WireFlowText)
	}
	if r := p.admin.Get(t, rutaAdminStart, nil); !p1WireIs(r, http.StatusMethodNotAllowed, "método no permitido (usar POST)\n") {
		t.Errorf("GET %s: HTTP %d %q, quería 405", rutaAdminStart, r.Codigo, recortar(r.Cuerpo))
	}
	p.expectNoTexts(t, "tras arrancar el flujo")
}

// checkOfflineSession es T3.27 (parte local) y la mitad offline del hallazgo 70: con el Edge
// enrolado, que conectó y se fue, las cuatro puertas que empujan un comando contestan 502 con el
// texto de «sesión offline». Las cuatro llegan a ese texto por errors.Is contra el MISMO centinela;
// si el gateway devolviera otro error con el mismo mensaje, flows/start caería al 500 «no se pudo
// iniciar la conversación». El Edge, sin stream, no recibe nada.
func (p *p1WireRun) checkOfflineSession(t *testing.T) {
	esc, e := p.esc, p.a
	edgeEsperarValor(t, esc.DB, "offline", "la sesión tras desconectar", edgeEstadoSesion, esc.Tenant, e.EdgeID, e.SessionID)
	p.expectNoStream(t, e.SessionID, "sesión offline")
	if n := len(e.Textos()); n != 0 {
		t.Errorf("el Edge desconectado tiene %d textos: algo se le entregó sin stream", n)
	}
	// La otra sesión de la empresa sigue viva: «offline» es de la sesión, no de la empresa. Su Edge
	// quedó cortado por el paso anterior (pegajoso sin reconectar), así que el comando LLEGA y el
	// Edge lo rechaza: 200 con ok=false, que es un Ack y no un 502.
	r := p.pub.Post(t, rutaMessages, map[string]string{"session_id": p.b.SessionID, "to": "573001110000", "text": "no debe salir"})
	if r.Codigo != http.StatusOK || !strings.Contains(string(r.Cuerpo), `"ok":false,"error":"`+edgeLeaseNotValidText+`"`) {
		t.Errorf("enviar a la sesión viva de un Edge cortado: HTTP %d %s; quería 200 con ok=false y %q", r.Codigo, recortar(r.Cuerpo), edgeLeaseNotValidText)
	}
}

// expectNoStream manda un comando a sessionID por las cuatro puertas y exige en cada una el 502 de
// «sesión offline» con su cuerpo exacto: JSON con el command_id en el envío público, JSON a secas en
// el arranque público y texto plano en las dos de admin (con el command_id en la de envío).
func (p *p1WireRun) expectNoStream(t *testing.T, sessionID, stage string) {
	t.Helper()
	send := map[string]string{"session_id": sessionID, "to": "573001110000", "text": "¿sigues ahí?"}
	r := p.pub.Post(t, rutaMessages, send)
	p.sendFailures++
	if r.Codigo != http.StatusBadGateway || !p1WireOfflineJSON.Match(r.Cuerpo) {
		t.Errorf("%s, POST %s: HTTP %d %s; quería 502 con %s", stage, rutaMessages, r.Codigo, recortar(r.Cuerpo), p1WireOfflineJSON)
	}
	r = p.admin.Post(t, rutaAdminSend, send)
	p.adminSendFailures++
	if r.Codigo != http.StatusBadGateway || !p1WireOfflinePlain.Match(r.Cuerpo) {
		t.Errorf("%s, POST %s: HTTP %d %q; quería 502 con %s", stage, rutaAdminSend, r.Codigo, recortar(r.Cuerpo), p1WireOfflinePlain)
	}
	body, _ := p.startBody(sessionID)
	if r := p.admin.Post(t, rutaAdminStart, body); !p1WireIs(r, http.StatusBadGateway, p1WireOfflineText+"\n") {
		t.Errorf("%s, POST %s: HTTP %d %q; quería 502 %q (ErrSessionOffline con su identidad)", stage, rutaAdminStart, r.Codigo, recortar(r.Cuerpo), p1WireOfflineText)
	}
	body, _ = p.startBody(sessionID)
	if r := p.pub.Post(t, p1WirePublicFlow, body); !p1WireIs(r, http.StatusBadGateway, `{"error":"`+p1WireOfflineText+`"}`) {
		t.Errorf("%s, POST %s: HTTP %d %s; quería 502 con el error %q", stage, p1WirePublicFlow, r.Codigo, recortar(r.Cuerpo), p1WireOfflineText)
	}
}

// checkUnknownSession: una sesión que NUNCA existió. El envío público la corta antes del gateway
// (404: no es de la empresa del token) y no deja línea ERROR; las otras tres puertas no miran la
// flota y la tratan como una sesión sin stream: el mismo 502 que la desconectada.
func (p *p1WireRun) checkUnknownSession(t *testing.T) {
	const ghost = "sesion-que-nunca-existio"
	send := map[string]string{"session_id": ghost, "to": "573001110000", "text": "¿hay alguien?"}
	r := p.pub.Post(t, rutaMessages, send)
	if !p1WireIs(r, http.StatusNotFound, `{"error":"sesión no encontrada para el tenant"}`) {
		t.Errorf("enviar a una sesión que nunca existió: HTTP %d %s, quería 404", r.Codigo, recortar(r.Cuerpo))
	}
	r = p.admin.Post(t, rutaAdminSend, send)
	if r.Codigo != http.StatusBadGateway || !p1WireOfflinePlain.Match(r.Cuerpo) {
		t.Errorf("POST %s a una sesión que nunca existió: HTTP %d %q; quería 502 con %s", rutaAdminSend, r.Codigo, recortar(r.Cuerpo), p1WireOfflinePlain)
	}
	p.adminSendFailures++
	body, _ := p.startBody(ghost)
	if r := p.admin.Post(t, rutaAdminStart, body); !p1WireIs(r, http.StatusBadGateway, p1WireOfflineText+"\n") {
		t.Errorf("POST %s a una sesión que nunca existió: HTTP %d %q; quería 502", rutaAdminStart, r.Codigo, recortar(r.Cuerpo))
	}
	body, _ = p.startBody(ghost)
	if r := p.pub.Post(t, p1WirePublicFlow, body); !p1WireIs(r, http.StatusBadGateway, `{"error":"`+p1WireOfflineText+`"}`) {
		t.Errorf("POST %s a una sesión que nunca existió: HTTP %d %s; quería 502", p1WirePublicFlow, r.Codigo, recortar(r.Cuerpo))
	}
	if n := consultaEntero(t, p.esc.DB, `SELECT count(*) FROM public.fleet_sessions WHERE session_id = $1`, ghost); n != 0 {
		t.Errorf("mandar a una sesión que nunca existió dejó %d filas en fleet_sessions", n)
	}
}

// checkLog es el cierre, ANTES de parar el servidor: ningún handler entró en pánico (en el binario
// nuevo, el handler viejo de mensajes quedó registrado sin gateway detrás de la cara nueva: si
// alguna petición de este proceso lo hubiera alcanzado, estaría aquí), el Validator de ningún Edge
// rechazó un lease y las únicas líneas ERROR son las de los 502 de envío provocados.
func (p *p1WireRun) checkLog(t *testing.T) {
	p1WireNoPanics(t, p.esc.S.Log())
	for _, e := range []*edge{p.a, p.b, p.other} {
		if errs := e.Errores(); len(errs) != 0 {
			t.Errorf("el núcleo de %s anotó errores: %v", e.EdgeID, errs)
		}
	}
	want := map[string]int{p1WireSendFailed: p.sendFailures, p1WireAdminSendFailed: p.adminSendFailures}
	edgeSinErrores(t, p.esc.S, want)
	for msg, n := range want {
		if got := len(p9LogLines(p.esc.S, msg, map[string]string{"level": "ERROR"})); got != n {
			t.Errorf("líneas ERROR %q = %d, quería %d (una por cada 502 del envío)", msg, got, n)
		}
	}
}

// p1WireNoPanics falla el test si el log (el texto entero, también lo que no es JSON) trae la
// huella de un pánico: la línea de net/http, la cabecera del runtime o una traza de goroutines.
func p1WireNoPanics(t *testing.T, log string) {
	t.Helper()
	if loc := p1WirePanic.FindStringIndex(log); loc != nil {
		t.Errorf("el log del servidor trae un pánico:\n%s", ultimasLineas(log[:min(len(log), loc[1]+2000)], 40))
	}
}
