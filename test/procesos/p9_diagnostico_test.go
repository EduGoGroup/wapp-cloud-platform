//go:build integracion

package procesos

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
)

// P9 · Diagnóstico remoto y configuración empujada (diseno.md §4, T9.16). Es el único tramo del gRPC
// —DiagnosticsRequest/DiagnosticsBundle y ConfigUpdate— que ningún otro proceso recorre. El proceso
// está partido por tema: este fichero trae el mundo, las ayudas y el cierre; el diagnóstico va en
// p9_diagnostico_remote_test.go y la configuración empujada en p9_diagnostico_config_push_test.go.

const (
	// p9MsgOrphan es la línea WARN con la que el gateway dice que un DiagnosticsBundle no casó con
	// ninguna solicitud pendiente (internal/gateway/grpc/diagnostics.go, storeDiagnosticsBundle): un
	// command_id que nadie pidió, uno ya contestado, uno vencido, o uno de otra sesión u otra empresa.
	p9MsgOrphan = "diagnóstico: bundle sin solicitud pendiente; ignorado"
	// p9MsgSendFailed es la línea ERROR que deja cada 502 de la API pública (writeSendError,
	// internal/publicapi/messages.go): aquí, el diagnóstico pedido a una sesión offline.
	p9MsgSendFailed = "envío por la API pública fallido"

	// Las tres acciones que auditan las rutas de P9: el permiso de la ruta es el `action` de
	// audit_events (protect, internal/publicapi/publicapi.go).
	p9ActDiag    = "diagnostics.request"
	p9ActIntents = "intents.write"
	p9ActProfile = "sessions.write"

	// p9PlanNoIntent es un plan sembrado SIN la feature llm_intent (0039: `basic` no trae ninguna).
	p9PlanNoIntent = "basic"

	// Las dos rutas de configuración que P9 recorre.
	p9PathIntents = "/api/v1/intents"
)

// p9Caller es quien llama a la API pública: el cliente con el token de la administradora y la
// empresa de ese token, que es bajo la que el servidor audita la llamada.
type p9Caller struct {
	client *clienteHTTP
	tenant string
}

// p9World es lo que comparten los subtests de P9: un servidor, la empresa del proceso con DOS Edges
// (para ver el fan-out por empresa y el bundle de otra sesión) y OTRA empresa con el suyo (para ver
// el aislamiento), de un plan sin llm_intent. Los contadores dicen qué tiene que haber quedado al
// final: las filas de audit_events y los DiagnosticsRequest que recibió cada Edge.
type p9World struct {
	root  *testing.T // el test del proceso: dueño de las conexiones que sobreviven a un subtest
	esc   edgeEscenario
	main  p9Caller
	other p9Caller
	e     *edge // primera sesión de la empresa del proceso
	e2    *edge // segunda sesión (otro Edge) de la misma empresa
	eOth  *edge // sesión de la otra empresa

	audit map[string]int // "tenant|action|result" → filas de audit_events esperadas
	// throttled cuenta los 429 que call reintentó (ver call).
	throttled int
	diags     map[string][]string // session_id → command_id de los DiagnosticsRequest esperados, en orden
}

// TestP9_DiagnosticsAndConfigPush es el proceso P9. Los subtests comparten UN servidor y corren EN
// ORDEN (ninguno es paralelo): cada uno parte del estado que dejó el anterior. Se afirma lo que hace
// el binario viejo; el mismo test corre contra el nuevo sin distinguirlos.
//
// Recorre: la config inicial al conectar; el diagnóstico remoto completo (pedir → el Edge recibe →
// pendiente → bundle → descarga), su aislamiento por empresa, la sesión offline, el vencimiento y el
// consentimiento; el catálogo de intenciones empujado (fan-out por empresa, versión por hash,
// reemplazo); el mapa de filtros empujado por el cambio de perfil; y lo que el Edge vuelve a recibir
// al reconectar. Cada puerta lleva su tabla de casos adversarios (reglas.md §2).
func TestP9_DiagnosticsAndConfigPush(t *testing.T) {
	t.Parallel()
	w := p9NewWorld(t)

	t.Run("config_inicial", w.initialConfig)
	t.Run("diagnostico_ciclo", w.diagnosticsCycle)
	t.Run("diagnostico_aislamiento", w.diagnosticsIsolation)
	t.Run("diagnostico_sesion_offline", w.diagnosticsOffline)
	t.Run("diagnostico_vencimiento", w.diagnosticsExpiry)
	t.Run("diagnostico_consentimiento", w.diagnosticsConsent)
	t.Run("diagnostico_adversarios", w.diagnosticsAdversarial)
	t.Run("intents_push", w.intentsPush)
	t.Run("intents_adversarios", w.intentsAdversarial)
	t.Run("filters_push", w.filtersPush)
	t.Run("filters_adversarios", w.filtersAdversarial)
	t.Run("reconexion", w.reconnect)
	t.Run("auditoria", w.auditTrail)
	t.Run("cierre", w.closing)
}

// p9NewWorld arranca el servidor del proceso y deja conectados los tres Edges. Falla (t.Fatalf) si
// algo del montaje no sale.
func p9NewWorld(t *testing.T) *p9World {
	t.Helper()
	esc := edgeEscenarioNuevo(t, "p9", "p9-diagnostico", true)
	w := &p9World{
		root:  t,
		esc:   esc,
		main:  p9Caller{client: esc.S.Publica(esc.TokenAdmin), tenant: esc.Tenant},
		audit: map[string]int{},
		diags: map[string][]string{},
	}
	otherTenant := crearTenantConPlan(t, esc.S, esc.TokenStaff, "p9-otra-empresa", p9PlanNoIntent)
	otherAdmin := uuidAleatorio(t)
	edgeAltaAdminDelTenant(t, esc.DB, otherAdmin, otherTenant)
	w.other = p9Caller{
		client: esc.S.Publica(canjear(t, esc.S, esc.S.Identidad.TokenDe(otherAdmin, "wapp.bff"))),
		tenant: otherTenant,
	}
	w.e = w.newEdge(t, esc.Tenant)
	w.e2 = w.newEdge(t, esc.Tenant)
	w.eOth = w.newEdge(t, otherTenant)
	return w
}

// newEdge enrola un Edge en la empresa dada, lo conecta y espera a la renovación del lease que
// provoca su primer latido, para que el aviso de calentamiento del arranque (que sin catálogo no pide
// nada) no coincida con el del primer PUT del catálogo.
func (w *p9World) newEdge(t *testing.T, tenant string) *edge {
	t.Helper()
	e := enrolar(t, w.esc.S, edgeEmitirCodigo(t, w.esc.S, w.esc.TokenStaff, tenant))
	e.conectar(t)
	e.esperarLeases(t, 2, edgeTopeFila)
	return e
}

// reconnectEdge vuelve a conectar un Edge desde un subtest y espera a la renovación de su lease. La
// conexión se abre con el test del PROCESO (w.root) y no con el del subtest: conectar ata el stream
// al contexto del test que recibe y registra su cierre en ese test, así que una conexión abierta con
// el subtest moriría al terminar este y el Edge quedaría offline para los siguientes.
func (w *p9World) reconnectEdge(t *testing.T, e *edge) {
	t.Helper()
	if err := e.conectarErr(w.root); err != nil {
		t.Fatalf("reconectar (sesión %s): %v", e.SessionID, err)
	}
	e.esperarLeases(t, 2, edgeTopeFila)
}

// call hace la petición por la API pública como c y, si la ruta audita (action no vacío), apunta la
// fila de audit_events que tiene que dejar: `success` por debajo de 400, `failure` desde 400. Las
// rutas de P9 solo dejan de auditar cuando la petición no pasa del middleware (401 sin token, 403 sin
// permiso), y esos casos se llaman con action "".
//
// Un 429 se reintenta sondeando, con tope: la API pública limita cada credencial a 20 peticiones por
// segundo con ráfaga de 40 (WAPP_RATELIMIT_PUBLIC_RPS/_BURST, que el arnés no pone), y las tablas de
// adversarios de P9 pasan de esa ráfaga. El 429 lo corta el middleware antes del handler: no audita ni
// tiene efecto, así que la petición reintentada es la primera que el proceso «hizo».
func (w *p9World) call(t *testing.T, c p9Caller, action, method, path string, body any) respuesta {
	t.Helper()
	var r respuesta
	edgeEsperar(t, edgeTopeFila, method+" "+path+" sin 429", func() bool {
		r = c.client.hacer(t, method, path, body)
		if r.Codigo == http.StatusTooManyRequests {
			w.throttled++
			return false
		}
		return true
	})
	if action != "" {
		result := "success"
		if r.Codigo >= http.StatusBadRequest {
			result = "failure"
		}
		w.audit[c.tenant+"|"+action+"|"+result]++
	}
	return r
}

// p9DiagPath es la ruta de petición de diagnóstico de una sesión; el session_id va URL-escapado,
// para que los adversarios (espacios Unicode, dígitos no ASCII) lleguen al servidor tal cual.
func p9DiagPath(sessionID string) string {
	return "/api/v1/sessions/" + url.PathEscape(sessionID) + "/diagnostics"
}

// p9BundlePath es la ruta de descarga de un bundle; el command_id va URL-escapado.
func p9BundlePath(commandID string) string {
	return "/api/v1/diagnostics/" + url.PathEscape(commandID)
}

// p9ProfilePath es la ruta del perfil de una sesión; el session_id va URL-escapado.
func p9ProfilePath(sessionID string) string {
	return "/api/v1/sessions/" + url.PathEscape(sessionID) + "/profile"
}

// p9Scalar ejecuta una consulta de una fila y una columna de texto y la devuelve; sin filas devuelve
// «». Falla (t.Fatalf) si el SQL falla.
func p9Scalar(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), topeFixture)
	defer cancel()
	var v sql.NullString
	err := db.QueryRowContext(ctx, query, args...).Scan(&v)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("consulta %q: %v", query, err)
	}
	return v.String
}

// p9LogLines devuelve las líneas del log del servidor con el mensaje msg cuyos campos de texto
// coinciden con todos los de fields, en orden.
func p9LogLines(s *servidor, msg string, fields map[string]string) []map[string]any {
	var out []map[string]any
	for _, l := range s.LineasLog() {
		if l["msg"] != msg {
			continue
		}
		match := true
		for k, v := range fields {
			if got, ok := l[k].(string); !ok || got != v {
				match = false
				break
			}
		}
		if match {
			out = append(out, l)
		}
	}
	return out
}

// p9WaitLogLines espera, con tope, a que el log tenga al menos n líneas p9LogLines(msg, fields).
func p9WaitLogLines(t *testing.T, s *servidor, msg string, fields map[string]string, n int) {
	t.Helper()
	edgeEsperar(t, edgeTopeFila, fmt.Sprintf("%d líneas de log %q con %v", n, msg, fields), func() bool {
		return len(p9LogLines(s, msg, fields)) >= n
	})
}

// p9ConfigsSince devuelve los ConfigUpdate que el Edge recibió DESDE la posición from de su lista
// (Configs acumula desde que el Edge existe: se cuenta por delta), filtrados por kind; kind «» = todos.
func p9ConfigsSince(e *edge, from int, kind string) []configRecibida {
	var out []configRecibida
	for _, c := range e.Configs()[from:] {
		if kind == "" || c.Kind == kind {
			out = append(out, c)
		}
	}
	return out
}

// p9WaitConfigs espera, con tope, a que el Edge haya recibido desde from al menos n ConfigUpdate del
// kind dado, y los devuelve.
func p9WaitConfigs(t *testing.T, e *edge, from int, kind string, n int) []configRecibida {
	t.Helper()
	var got []configRecibida
	edgeEsperar(t, edgeTopeFila, fmt.Sprintf("%d ConfigUpdate %q nuevos en %s", n, kind, e.SessionID), func() bool {
		got = p9ConfigsSince(e, from, kind)
		return len(got) >= n
	})
	return got
}

// p9Kinds devuelve los kinds de una lista de ConfigUpdate, en orden de llegada.
func p9Kinds(cfgs []configRecibida) []string {
	kinds := make([]string, len(cfgs))
	for i, c := range cfgs {
		kinds[i] = c.Kind
	}
	return kinds
}

// p9ArabicDigits devuelve s con cada dígito ASCII cambiado por su dígito árabe-índico (U+0660…): el
// mismo «número» para una persona y otra cadena para un comparador de bytes. Si s no tiene dígitos
// le añade uno, para que el resultado nunca sea igual a s.
func p9ArabicDigits(s string) string {
	out := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return 0x0660 + (r - '0')
		}
		return r
	}, s)
	if out == s {
		return s + "١"
	}
	return out
}

// p9ErrorIs dice si la respuesta trae el código dado y su cuerpo contiene el texto.
func p9ErrorIs(r respuesta, code int, text string) bool {
	return r.Codigo == code && strings.Contains(string(r.Cuerpo), text)
}

// auditTrail compara lo que quedó en audit_events con lo que el proceso apuntó llamada a llamada:
// por empresa, acción y resultado, las tres acciones de P9. La fila se escribe DESPUÉS de responder
// (AuditMiddleware), así que la última se espera sondeando.
func (w *p9World) auditTrail(t *testing.T) {
	const query = `SELECT count(*)::text FROM public.audit_events WHERE tenant_id = $1::uuid AND action = $2 AND result = $3`
	keys := make([]string, 0, len(w.audit))
	for k := range w.audit {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		parts := strings.Split(k, "|")
		edgeEsperarValor(t, w.esc.DB, fmt.Sprint(w.audit[k]), "audit_events de "+k, query, parts[0], parts[1], parts[2])
	}
	// Nada más con esas tres acciones: ni otro resultado ni otra empresa.
	total := 0
	for _, n := range w.audit {
		total += n
	}
	if got := p9Scalar(t, w.esc.DB, `SELECT count(*)::text FROM public.audit_events WHERE action = ANY($1::text[])`,
		"{"+p9ActDiag+","+p9ActIntents+","+p9ActProfile+"}"); got != fmt.Sprint(total) {
		t.Errorf("audit_events de las tres acciones de P9 = %s filas, quería %d (%v)", got, total, w.audit)
	}
	// El actor es el subject del token y la auditoría no lleva contenido: solo el código HTTP.
	if got := p9Scalar(t, w.esc.DB, `SELECT count(*)::text FROM public.audit_events
		WHERE action = ANY($1::text[]) AND (actor = '' OR NOT (meta ? 'status') OR resource NOT IN ('session','diagnostics','intents'))`,
		"{"+p9ActDiag+","+p9ActIntents+","+p9ActProfile+"}"); got != "0" {
		t.Errorf("hay %s filas de audit_events de P9 sin actor, sin meta.status o con otro resource", got)
	}
}

// closing es el cierre del proceso, ANTES de parar el servidor: cada Edge recibió exactamente los
// DiagnosticsRequest que el proceso pidió con éxito (ni uno de los rechazados), ninguna inferencia
// que no fuera un calentamiento, el núcleo de ningún Edge anotó errores y el log del servidor solo
// trae el ERROR del 502 de la sesión offline.
func (w *p9World) closing(t *testing.T) {
	for name, e := range map[string]*edge{"e": w.e, "e2": w.e2, "eOth": w.eOth} {
		var got []string
		for _, d := range e.Diagnosticos() {
			got = append(got, d.ComandoID)
			if d.Sesion != e.SessionID {
				t.Errorf("%s recibió un DiagnosticsRequest de la sesión %q", name, d.Sesion)
			}
		}
		if !slices.Equal(got, w.diags[e.SessionID]) {
			t.Errorf("%s recibió los DiagnosticsRequest %v, quería %v", name, got, w.diags[e.SessionID])
		}
		for _, req := range e.Inferencias() {
			if !req.GetWarmup() {
				t.Errorf("%s recibió una inferencia que no es un calentamiento (class %q)", name, req.GetClass())
			}
		}
		if errs := e.Errores(); len(errs) != 0 {
			t.Errorf("el núcleo de %s anotó errores: %v", name, errs)
		}
	}
	if n := len(w.eOth.Inferencias()); n != 0 {
		t.Errorf("el Edge de la empresa sin llm_intent recibió %d peticiones de inferencia", n)
	}
	edgeSinErrores(t, w.esc.S, map[string]int{p9MsgSendFailed: 1})
	t.Logf("peticiones reintentadas por 429 (límite por credencial de la API pública): %d", w.throttled)
}
