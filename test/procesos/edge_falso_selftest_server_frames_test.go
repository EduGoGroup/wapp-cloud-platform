//go:build integracion

package procesos

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	cllease "github.com/EduGoGroup/wapp-cloudlink/lease"
)

// TestArnes_EdgeFrames, contra el servidor real: los frames del Edge de prueba que dejan un efecto
// observable (envíos, entrante sellado, acuses, diagnóstico, renovación, reconexión, revocación).
// Sale de edge_falso_test.go (D-F9-11: solo se movieron declaraciones).

// TestArnes_EdgeFrames prueba, contra el servidor real, los frames del Edge que dejan un efecto que
// se puede observar: el SendText que el servidor empuja por las dos rutas de envío (llega al
// canal y el servidor recibe el Ack), el entrante sellado (el servidor lo abre, no registra el
// texto, lo deduplica), los acuses (filas de message_receipts), el diagnóstico remoto (el Edge
// recibe el DiagnosticsRequest y el bundle sale por la ruta de descarga), la renovación del lease
// por latido y el anti-replay del Validator, la desconexión (sesión offline, envío con 502) y la
// reconexión, y la revocación del lease con su efecto: tras revocar, el envío del servidor ya no se
// entrega (200 con ok=false y «lease no vigente»), tampoco tras reconectar. Necesita Docker.
//
// NO ejercita contra el servidor: los Ping (no hay ruta que los provoque), la inferencia (aquí no:
// el calentamiento y su gate de lease los recorre TestArnes_EdgeInferenceLeaseGate; la que pide la
// canalización LLM la monta T9.17), ni el camino del entrante hasta una respuesta (perfil y flujos,
// que monta P3).
func TestArnes_EdgeFrames(t *testing.T) {
	t.Parallel()
	esc := edgeEscenarioNuevo(t, "edge_frames", "edge-frames", true)
	e := enrolar(t, esc.S, edgeEmitirCodigo(t, esc.S, esc.TokenStaff, esc.Tenant))
	e.conectar(t)
	e.esperarLeases(t, 2, edgeTopeFila)

	edgeProbarTextoDelServidor(t, esc, e)
	edgeProbarEntranteSellado(t, esc, e)
	edgeProbarAcuses(t, esc, e)
	edgeProbarDiagnostico(t, esc, e)
	edgeProbarRenovacionPorLatido(t, esc, e)
	edgeProbarReconexion(t, esc, e)
	edgeProbarRevocacion(t, esc, e)

	// El único error esperado es el del latido de contador viejo de edgeProbarRenovacionPorLatido.
	if errs := e.Errores(); len(errs) != 1 || !errors.Is(errs[0], cllease.ErrStaleCounter) {
		t.Errorf("errores del núcleo del Edge = %v, quería solo el ErrStaleCounter provocado", errs)
	}
	// El servidor registra como ERROR cada 502: el que provoca el envío con la sesión offline.
	edgeSinErrores(t, esc.S, map[string]int{"envío por la API pública fallido": 1})
}

// edgeProbarTextoDelServidor manda un texto por la ruta pública y otro por la de admin; cada una
// debe responder 200 con el Ack del Edge y dejar el texto en el canal con el mismo command_id.
func edgeProbarTextoDelServidor(t *testing.T, esc edgeEscenario, e *edge) {
	t.Helper()
	rutas := []struct {
		nombre  string
		cliente *clienteHTTP
		ruta    string
	}{
		{"API pública", esc.S.Publica(esc.TokenAdmin), "/api/v1/messages"},
		{"admin", esc.S.Admin(esc.TokenAdmin), "/admin/messages/send"},
	}
	for i, r := range rutas {
		texto := fmt.Sprintf("hola desde la nube (%s)", r.nombre)
		resp := r.cliente.Post(t, r.ruta, map[string]string{"session_id": e.SessionID, "to": "573001110000", "text": texto})
		var ack struct {
			AckedCommandID string `json:"acked_command_id"`
			OK             bool   `json:"ok"`
		}
		resp.JSON(t, &ack)
		if resp.Codigo != http.StatusOK || !ack.OK || ack.AckedCommandID == "" {
			t.Fatalf("%s: HTTP %d %+v, quería 200 con el Ack del Edge\ncuerpo: %s", r.nombre, resp.Codigo, ack, recortar(resp.Cuerpo))
		}
		txt := e.esperarTexto(t, 5*time.Second)
		if quiere := (textoRecibido{A: "573001110000", Texto: texto, ComandoID: ack.AckedCommandID}); txt != quiere {
			t.Errorf("paso %d, %s: el Edge recibió %+v, quería %+v", i, r.nombre, txt, quiere)
		}
	}
}

// edgeProbarEntranteSellado manda un entrante sellado y comprueba que el servidor lo abre (línea de
// log con el tamaño del sobre y cero bytes en claro), registra el wa_message_id en ingest_dedupe, nunca
// escribe el texto en el log y reconoce el reenvío del mismo id como duplicado.
func edgeProbarEntranteSellado(t *testing.T, esc edgeEscenario, e *edge) {
	t.Helper()
	const texto, waID = "quiero un presupuesto de tornillos galvanizados", "WA-IN-1"
	e.entrante(t, "573001110000@s.whatsapp.net", texto, waID)

	linea := edgeEsperarLinea(t, esc.S, "ingreso: enc_payload sellado abierto", "wa_message_id", waID)
	if plano, ok := linea["text_plano_en_cable_len"].(float64); !ok || plano != 0 {
		t.Errorf("text_plano_en_cable_len = %v, quería 0 (nada sensible en claro)", linea["text_plano_en_cable_len"])
	}
	if sobre, ok := linea["enc_payload_bytes"].(float64); !ok || sobre <= 0 {
		t.Errorf("enc_payload_bytes = %v, quería > 0", linea["enc_payload_bytes"])
	}
	edgeEsperarValor(t, esc.DB, "1", "el entrante en ingest_dedupe",
		`SELECT count(*)::text FROM public.ingest_dedupe WHERE session_id = $1 AND wa_message_id = $2`, e.SessionID, waID)

	e.entrante(t, "573001110000@s.whatsapp.net", texto, waID)
	edgeEsperarLinea(t, esc.S, "runtime: entrante duplicado ignorado (dedupe de ingesta)", "wa_message_id", waID)
	if strings.Contains(esc.S.Log(), texto) {
		t.Errorf("el texto del cliente aparece en el log del servidor")
	}
}

// edgeProbarAcuses manda un acuse de entregado y otro de leído del mismo mensaje y comprueba que
// el servidor los persiste como dos filas de message_receipts de la sesión del Edge.
func edgeProbarAcuses(t *testing.T, esc edgeEscenario, e *edge) {
	t.Helper()
	const waID = "WA-OUT-9"
	e.acuse(t, waID, false)
	e.acuse(t, waID, true)
	edgeEsperarValor(t, esc.DB, "delivered,read", "los acuses en message_receipts",
		`SELECT string_agg(status, ',' ORDER BY status) FROM public.message_receipts WHERE session_id = $1 AND message_id = $2`, e.SessionID, waID)
}

// edgeProbarDiagnostico recorre el diagnóstico remoto completo: la administradora lo pide (202), el
// Edge recibe el DiagnosticsRequest con el scope, la descarga responde «pendiente» (202) hasta que el
// Edge manda su bundle, y entonces responde 200 con la cola de log. Un bundle de un command_id que
// nadie pidió queda en el log como huérfano y no rompe el stream.
func edgeProbarDiagnostico(t *testing.T, esc edgeEscenario, e *edge) {
	t.Helper()
	pub := esc.S.Publica(esc.TokenAdmin)
	r := pub.Post(t, "/api/v1/sessions/"+e.SessionID+"/diagnostics", map[string]string{"scope": "logs"})
	var pedido struct {
		CommandID string `json:"command_id"`
		Status    string `json:"status"`
	}
	r.JSON(t, &pedido)
	if r.Codigo != http.StatusAccepted || pedido.CommandID == "" || pedido.Status != "pending" {
		t.Fatalf("pedir el diagnóstico: HTTP %d %+v, quería 202 pendiente\ncuerpo: %s", r.Codigo, pedido, recortar(r.Cuerpo))
	}
	edgeEsperar(t, edgeTopeFila, "el DiagnosticsRequest en el Edge", func() bool { return len(e.Diagnosticos()) == 1 })
	if d := e.Diagnosticos()[0]; d != (diagnosticoPedido{ComandoID: pedido.CommandID, Scope: "logs", Sesion: e.SessionID}) {
		t.Errorf("el Edge recibió %+v", d)
	}
	ruta := "/api/v1/diagnostics/" + pedido.CommandID
	if antes := pub.Get(t, ruta, nil); antes.Codigo != http.StatusAccepted {
		t.Errorf("la descarga antes del bundle: HTTP %d, quería 202 (pendiente)", antes.Codigo)
	}

	e.bundle(t, pedido.CommandID, "línea de log del Edge")
	var descarga struct {
		CommandID string `json:"command_id"`
		LogTail   string `json:"log_tail"`
	}
	edgeEsperar(t, edgeTopeFila, "la descarga del bundle", func() bool {
		d := pub.Get(t, ruta, nil)
		if d.Codigo != http.StatusOK {
			return false
		}
		d.JSON(t, &descarga)
		return true
	})
	if descarga.CommandID != pedido.CommandID || descarga.LogTail != "línea de log del Edge" {
		t.Errorf("la descarga trae %+v", descarga)
	}

	e.bundle(t, "diag-que-nadie-pidio", "x")
	edgeEsperarLinea(t, esc.S, "diagnóstico: bundle sin solicitud pendiente; ignorado", "command_id", "diag-que-nadie-pidio")
}

// edgeProbarRenovacionPorLatido comprueba que un latido con contador 10 hace que el servidor renueve
// el lease con 11 (en el Edge y en Postgres) y que el Validator lo acepta; y que un latido con un
// contador viejo (4) produce un lease que el Validator rechaza con ErrStaleCounter (anti-replay),
// sin quitarle al Edge su lease vigente.
func edgeProbarRenovacionPorLatido(t *testing.T, esc edgeEscenario, e *edge) {
	t.Helper()
	const consulta = `SELECT counter::text FROM public.leases WHERE tenant_id = $1::uuid AND edge_id = $2`
	antes := e.Leases()
	e.latir(t, 10)
	e.esperarLeases(t, antes+1, edgeTopeFila)
	edgeEsperarValor(t, esc.DB, "11", "el contador del lease tras un latido de 10", consulta, esc.Tenant, e.EdgeID)
	if len(e.Errores()) != 0 || !e.puedeOperar() {
		t.Fatalf("tras el latido de 10: errores %v, puedeOperar %v", e.Errores(), e.puedeOperar())
	}

	antes = e.Leases()
	e.latir(t, 4)
	e.esperarLeases(t, antes+1, edgeTopeFila)
	edgeEsperar(t, edgeTopeFila, "el rechazo del lease de contador viejo", func() bool {
		errs := e.Errores()
		return len(errs) == 1 && errors.Is(errs[0], cllease.ErrStaleCounter)
	})
	if !e.puedeOperar() {
		t.Errorf("el lease rechazado le quitó al Edge su lease vigente")
	}
}

// edgeProbarReconexion comprueba la caída y la vuelta: al desconectar, la sesión pasa a offline en la
// flota y el envío del servidor responde 502 (no hay stream vivo); al reconectar, la sesión vuelve a
// online, el Edge recibe un lease inicial nuevo y el envío vuelve a llegar.
func edgeProbarReconexion(t *testing.T, esc edgeEscenario, e *edge) {
	t.Helper()
	envio := map[string]string{"session_id": e.SessionID, "to": "573001110000", "text": "¿sigues ahí?"}

	e.desconectar(t)
	edgeEsperarValor(t, esc.DB, "offline", "la sesión tras desconectar", edgeEstadoSesion, esc.Tenant, e.EdgeID, e.SessionID)
	if r := esc.S.Publica(esc.TokenAdmin).Post(t, "/api/v1/messages", envio); r.Codigo != http.StatusBadGateway {
		t.Errorf("enviar con la sesión offline: HTTP %d, quería 502\ncuerpo: %s", r.Codigo, recortar(r.Cuerpo))
	}

	e.conectar(t)
	e.esperarLeases(t, 2, edgeTopeFila)
	edgeEsperarValor(t, esc.DB, "online", "la sesión tras reconectar", edgeEstadoSesion, esc.Tenant, e.EdgeID, e.SessionID)
	if !e.puedeOperar() {
		t.Errorf("tras reconectar, el Edge no puede operar")
	}
	if r := esc.S.Publica(esc.TokenAdmin).Post(t, "/api/v1/messages", envio); r.Codigo != http.StatusOK {
		t.Fatalf("enviar tras reconectar: HTTP %d, quería 200\ncuerpo: %s", r.Codigo, recortar(r.Cuerpo))
	}
	if txt := e.esperarTexto(t, 5*time.Second); txt.Texto != envio["text"] {
		t.Errorf("tras reconectar el Edge recibió %+v", txt)
	}
}

// edgeProbarRevocacion comprueba el kill-switch de punta a punta: la administradora revoca el lease
// del Edge (204) y el Edge recibe la revocación firmada, queda revocado y sin poder operar, y
// Postgres lo refleja; a partir de ahí el envío del servidor NO se entrega (edgeCheckNotDelivered);
// y reconectar no lo arregla: conectar vuelve sin error —el servidor manda la revocación como
// lease inicial y el Validator la acepta—, pero el Edge sigue revocado y sin entregar.
func edgeProbarRevocacion(t *testing.T, esc edgeEscenario, e *edge) {
	t.Helper()
	r := esc.S.Admin(esc.TokenAdmin).Post(t, "/admin/leases/revoke", map[string]string{"edge_id": e.EdgeID})
	if r.Codigo != http.StatusNoContent {
		t.Fatalf("revocar el lease: HTTP %d, quería 204\ncuerpo: %s", r.Codigo, recortar(r.Cuerpo))
	}
	edgeEsperar(t, edgeTopeFila, "que el Edge quede revocado", e.revocado)
	if e.puedeOperar() {
		t.Errorf("el Edge revocado sigue pudiendo operar")
	}
	edgeEsperarValor(t, esc.DB, "true", "leases.revoked en Postgres",
		`SELECT revoked::text FROM public.leases WHERE tenant_id = $1::uuid AND edge_id = $2`, esc.Tenant, e.EdgeID)
	edgeCheckNotDelivered(t, esc, e, "tras revocar")

	e.conectar(t) // no falla: una revocación aceptada por el Validator no es un rechazo
	if e.puedeOperar() || !e.revocado() {
		t.Errorf("tras reconectar el Edge revocado: puedeOperar=%v revocado=%v; quería falso y verdadero", e.puedeOperar(), e.revocado())
	}
	edgeEsperarValor(t, esc.DB, "online", "la sesión del Edge revocado tras reconectar", edgeEstadoSesion, esc.Tenant, e.EdgeID, e.SessionID)
	edgeCheckNotDelivered(t, esc, e, "tras reconectar revocado")
}

// edgeCheckNotDelivered manda un texto por las dos rutas de envío del servidor (la pública y la de
// admin) a un Edge que no puede operar, y comprueba lo que ve quien llama: 200 con el Ack del
// Edge, ok=false y error «lease no vigente» (la API refleja el Ack, no lo convierte en un 5xx), y
// que el texto NO llegó al canal de textos. Recibe la etapa, para los mensajes. Falla el test con
// t.Errorf por cada incumplimiento.
func edgeCheckNotDelivered(t *testing.T, esc edgeEscenario, e *edge, stage string) {
	t.Helper()
	routes := []struct {
		name   string
		client *clienteHTTP
		path   string
	}{
		{"API pública", esc.S.Publica(esc.TokenAdmin), "/api/v1/messages"},
		{"admin", esc.S.Admin(esc.TokenAdmin), "/admin/messages/send"},
	}
	for _, r := range routes {
		resp := r.client.Post(t, r.path, map[string]string{"session_id": e.SessionID, "to": "573001110000", "text": "esto no debe salir"})
		var ack struct {
			AckedCommandID string `json:"acked_command_id"`
			OK             bool   `json:"ok"`
			Error          string `json:"error"`
		}
		resp.JSON(t, &ack)
		if resp.Codigo != http.StatusOK || ack.OK || ack.Error != edgeLeaseNotValidText || ack.AckedCommandID == "" {
			t.Errorf("%s, %s: HTTP %d %+v; quería 200 con ok=false y error %q\ncuerpo: %s",
				stage, r.name, resp.Codigo, ack, edgeLeaseNotValidText, recortar(resp.Cuerpo))
		}
	}
	// El Edge decide ANTES de acusar: con las respuestas ya recibidas, si hubiera publicado algo
	// estaría en el canal.
	if n := len(e.Textos()); n != 0 {
		t.Errorf("%s: hay %d textos en el canal de un Edge que no puede operar", stage, n)
	}
}
