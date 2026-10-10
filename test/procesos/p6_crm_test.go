//go:build integracion

package procesos

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// P6 · CRM (diseno.md §4, T9.19): el puente CRM del contrato wapp-crm-v1, de punta a punta y por las
// puertas reales. La configuración (GET/PUT/DELETE /api/v1/integrations), la IDA (una revisión de una
// solicitud → fila en webhook_outbox → el worker la entrega firmada al CRM falso → entregada, o
// reintento y agotada), el resumen de la cola (GET /api/v1/integrations/outbox) y la VUELTA (el
// callback firmado que refleja el estado del CRM sobre la solicitud).
//
// El proceso está partido por tema: este fichero trae el mundo, el montaje y el cierre; la
// configuración va en p6_crm_config_test.go, la ida en p6_crm_push_test.go, p6_crm_reanalysis_test.go y
// p6_crm_outbox_test.go, y
// la vuelta en p6_crm_callback_test.go y p6_crm_callback_adversarial_test.go.

const (
	// p6WebhookPoll y p6WebhookAttempts son las dos perillas del worker del outbox que el proceso fija
	// al arrancar (diseno.md §2): mira la cola cada 200 ms y da una entrega por agotada al 2.º fallo.
	p6WebhookPoll     = 200 * time.Millisecond
	p6WebhookAttempts = 2

	// p6Timeout es el tope de las esperas del worker. Lo que más tarda es el reintento: el backoff del
	// primer fallo es de 30 s ± 20 % (24–36 s) y no tiene perilla.
	p6Timeout = 60 * time.Second

	// Las rutas del proceso.
	p6PathIntegration = "/api/v1/integrations"
	p6PathOutbox      = "/api/v1/integrations/outbox"
	p6PathVariables   = "/api/v1/tenant-variables"

	// Los dos permisos que auditan las rutas de configuración (protect, internal/publicapi/publicapi.go).
	p6ActWrite = "integrations.write"

	// p6PlanNoBridge es un plan sembrado SIN la feature crm_bridge (0039: `basic` no trae ninguna).
	p6PlanNoBridge = "basic"

	// Los contactos del proceso: uno por solicitud (hallazgo 35: el tope de auto-respuestas es por
	// conversación). p6PnApproved es el de la solicitud que se aprueba y se refleja; p6PnDead el de la
	// solicitud cuya entrega agota los reintentos.
	p6PnApproved = "573006660002"
	p6PnDead     = "573006660003"

	// p6MetricDeliveries es el contador del worker (T-10: se mira DESPUÉS de provocar el evento) y
	// p6MetricLabel su etiqueta.
	p6MetricDeliveries = "wapp_webhook_deliveries_total"
	p6MetricLabel      = "status"

	// p6NBSP es el espacio de no separación U+00A0, uno de los adversarios de reglas.md §2.
	p6NBSP = "\u00a0"

	// p6MsgDead es la línea ERROR con la que el worker dice que una entrega agotó sus reintentos
	// (internal/integrations/worker.go, fail). Es el único ERROR que el proceso espera del worker.
	p6MsgDead = "webhook worker: entrega DEAD (reintentos agotados)"
	// p6MsgReflectFailed es la línea ERROR del callback cuando el reflejo falla en la base
	// (internal/publicapi/crmcallback.go): aquí, un intake_id que no es un UUID.
	p6MsgReflectFailed = "callback CRM: no se pudo reflejar el estado"

	// p6IntegrationMark es la marca de estado de la configuración del puente de una empresa: un md5 de
	// su fila ENTERA (todas las columnas, el sobre del secreto incluido), o «sin fila».
	p6IntegrationMark = `SELECT coalesce((SELECT md5(to_jsonb(t)::text) FROM public.tenant_integrations t WHERE tenant_id = $1), 'sin fila')`
	// p6IntakeMark es la marca de estado de una solicitud: un md5 de su fila ENTERA. Un callback
	// rechazado no puede mover ni una columna (ni el reflejo, ni el estado del dueño, ni los instantes).
	p6IntakeMark = `SELECT md5(to_jsonb(i)::text) FROM public.intakes i WHERE id = $1::uuid`
	// p6OutboxMark es la marca de la cola ENTERA: un md5 de todas las filas de webhook_outbox, con todas
	// sus columnas, o «sin filas».
	p6OutboxMark = `SELECT coalesce(md5(string_agg(to_jsonb(o)::text, '|' ORDER BY id)), 'sin filas') FROM public.webhook_outbox o`
)

// p6World es lo que comparten los subtests de P6: un servidor con el worker del outbox acelerado, la
// empresa del proceso con su Edge y su guion (el escenario de P4), el CRM falso, otra empresa con su
// propio puente (para el aislamiento de la vuelta) y una tercera sin la feature crm_bridge.
type p6World struct {
	sc    *draftScene
	crm   *crmFake
	calls *p9World // solo por su `call`, que reintenta el 429 (hallazgo 32)

	admin    p9Caller // la administradora de la empresa del proceso
	viewer   p9Caller // un viewer de la misma empresa: lee, no escribe
	other    p9Caller // la administradora de OTRA empresa, con su propio puente
	noBridge p9Caller // la administradora de una empresa de un plan sin crm_bridge

	secret      string // el secreto de firma vigente de la empresa del proceso
	otherSecret string // el de la otra empresa

	approved string // solicitud que se corrige, se aprueba y se refleja
	dead     string // solicitud cuya entrega recibe 500 hasta agotarse

	// errors son las líneas ERROR del log que el proceso provoca a propósito (msg → cuántas).
	errors map[string]int
	// metrics son las series de wapp_webhook_deliveries_total que el proceso espera al final.
	metrics map[string]float64
}

// TestP6_CRMBridge es el proceso P6. Los subtests comparten UN servidor y corren EN ORDEN (ninguno
// es paralelo): cada uno parte del estado que dejó el anterior. Se afirma lo que hace el binario
// viejo; el mismo test corre contra el nuevo sin distinguirlos.
func TestP6_CRMBridge(t *testing.T) {
	t.Parallel()
	w := p6NewWorld(t)

	t.Run("config_default", w.configDefault)
	t.Run("config_adversarial", w.configAdversarial)
	t.Run("config_gates", w.configGates)
	t.Run("push_gate_closed", w.pushGateClosed)
	t.Run("config_secret_at_rest", w.configSecretAtRest)
	t.Run("push_first_attempt_fails", w.pushFirstAttemptFails)
	t.Run("push_reanalysis", w.pushReanalysis)
	t.Run("push_delivered", w.pushDelivered)
	t.Run("push_contract_fields", w.pushContractFields)
	t.Run("callback_reflects", w.callbackReflects)
	t.Run("callback_idempotent", w.callbackIdempotent)
	t.Run("callback_window", w.callbackWindow)
	t.Run("callback_auth_adversarial", w.callbackAuthAdversarial)
	t.Run("callback_body_adversarial", w.callbackBodyAdversarial)
	t.Run("callback_isolation", w.callbackIsolation)
	t.Run("push_exhausted", w.pushExhausted)
	t.Run("callback_gate", w.callbackGate)
	t.Run("outbox_view", w.outboxView)
	t.Run("config_delete", w.configDelete)
	t.Run("closing", w.closing)
}

// p6NewWorld arranca el servidor del proceso y deja listo el mundo. Falla (t.Fatalf) si algo del
// montaje no sale.
func p6NewWorld(t *testing.T) *p6World {
	t.Helper()
	sc := p6Scenario(t)
	w := &p6World{
		sc:          sc,
		calls:       &p9World{root: t, audit: map[string]int{}},
		admin:       p9Caller{client: sc.Pub, tenant: sc.Tenant},
		secret:      crmFakeRandomSecret(t),
		otherSecret: crmFakeRandomSecret(t),
		errors:      map[string]int{},
		metrics:     map[string]float64{},
	}
	w.crm = crmFakeNew(t, w.secret)
	w.viewer = p9Caller{
		client: sc.S.Publica(p10NewMember(t, sc.edgeEscenario, sc.Tenant, p2RoleViewer).Token),
		tenant: sc.Tenant,
	}
	w.other = p6NewTenant(t, sc, "p6-otra-empresa", planTenantPorDefecto)
	w.noBridge = p6NewTenant(t, sc, "p6-sin-puente", p6PlanNoBridge)
	return w
}

// p6NewTenant crea una empresa con el plan dado y devuelve a su administradora como llamante.
func p6NewTenant(t *testing.T, sc *draftScene, slug, plan string) p9Caller {
	t.Helper()
	tenant := crearTenantConPlan(t, sc.S, sc.TokenStaff, slug, plan)
	admin := p10NewMember(t, sc.edgeEscenario, tenant, edgeRolTenantAdmin)
	return p9Caller{client: sc.S.Publica(admin.Token), tenant: tenant}
}

// p6Scenario es draftScenario (p4_borrador_helpers_test.go) con el worker del outbox acelerado.
//
// ⚠️ LÍMITE DEL ARNÉS: draftScenario y edgeEscenarioNuevo arrancan el servidor con
// opcionesServidor{Proceso} y no dejan pasar SondeoWebhook ni MaxIntentosWebhook, que es lo único que
// P6 necesita distinto. Como los ficheros existentes no se tocan en este bloque, el montaje se repite
// aquí paso a paso; el día que esos dos helpers acepten opciones, esta función es una llamada.
func p6Scenario(t *testing.T) *draftScene {
	t.Helper()
	const slug = "p6-crm"
	s := arrancar(t, opcionesServidor{Proceso: "p6", SondeoWebhook: p6WebhookPoll, MaxIntentosWebhook: p6WebhookAttempts})
	db := s.Base.Abrir(t)
	staff := uuidAleatorio(t)
	altaStaffPlataforma(t, db, staff)
	tokenStaff := canjear(t, s, s.Identidad.TokenDe(staff, "wapp.bff"))
	esc := edgeEscenario{S: s, DB: db, TokenStaff: tokenStaff, Tenant: crearTenantConPlan(t, s, tokenStaff, slug, draftPlan)}
	admin := uuidAleatorio(t)
	edgeAltaAdminDelTenant(t, db, admin, esc.Tenant)
	esc.TokenAdmin = canjear(t, s, s.Identidad.TokenDe(admin, "wapp.bff"))

	script := newAmbarScript(t)
	e := enrolar(t, s, edgeEmitirCodigo(t, s, tokenStaff, esc.Tenant))
	e.Inferir = script.Infer
	e.conectar(t)
	e.esperarLeases(t, 2, edgeTopeFila)
	if !e.puedeOperar() {
		t.Fatalf("p6Scenario: el Edge recién conectado no puede operar")
	}
	e.esperarConfig(t, "filters", edgeTopeFila)
	sc := &draftScene{
		p3Scene: p3Scene{edgeEscenario: esc, Edge: e, Pub: s.Publica(esc.TokenAdmin)},
		Script:  script,
		beat:    5,
	}
	p6InstallDraftFlow(t, sc)
	sc.setProfile(t, "active")
	return sc
}

// p6InstallDraftFlow carga el catálogo de P4, publica su flujo y crea su disparo `event_start`: lo
// que draftScenario hace tras conectar el Edge.
func p6InstallDraftFlow(t *testing.T, sc *draftScene) {
	t.Helper()
	r := sc.Pub.Post(t, "/api/v1/catalog/import?mode=apply&ref="+draftCatalogRef, draftCatalog())
	if r.Codigo != http.StatusOK {
		t.Fatalf("p6Scenario: importar el catálogo: HTTP %d\ncuerpo: %s", r.Codigo, recortar(r.Cuerpo))
	}
	def := map[string]any{
		"flow_id": draftFlowID, "version": 1, "initial": "root",
		"nodes": map[string]any{
			"root": map[string]any{"type": "menu", "prompt": draftMenuPrompt, "options": map[string]string{"1": "fin"}},
			"fin":  map[string]any{"type": "message", "text": draftMenuExit, "next": nil},
		},
	}
	if r = sc.Pub.Post(t, "/api/v1/flows", map[string]any{"definition": def}); r.Codigo != http.StatusCreated {
		t.Fatalf("p6Scenario: publicar el flujo: HTTP %d\ncuerpo: %s", r.Codigo, recortar(r.Cuerpo))
	}
	r = sc.Pub.Post(t, "/api/v1/triggers", map[string]any{
		"kind": "event_start", "keyword": draftKeyword, "match_type": "contains",
		"event_kind": draftEventKind, "flow_id": draftFlowID,
	})
	if r.Codigo != http.StatusCreated {
		t.Fatalf("p6Scenario: crear el disparo event_start: HTTP %d\ncuerpo: %s", r.Codigo, recortar(r.Cuerpo))
	}
}

// call hace la petición por la API pública como c, reintentando el 429 (p9World.call, hallazgo 32).
// action es p6ActWrite cuando la petición es una escritura que llega a auditarse, y «» si no (las
// lecturas, y los 401/403 que corta el middleware antes de auditar): la cuenta se comprueba al cerrar.
func (w *p6World) call(t *testing.T, c p9Caller, action, method, path string, body any) respuesta {
	t.Helper()
	return w.calls.call(t, c, action, method, path, body)
}

// raw es call para un cuerpo que no se puede pasar por json.Marshal sin cambiarlo: uno que no es JSON
// o uno relleno de espacios hasta un tamaño exacto. Reintenta el 429 igual, sondeando con tope, y
// apunta la fila de auditoría con la misma regla que p9World.call.
func (w *p6World) raw(t *testing.T, c p9Caller, action, method, path string, body []byte) respuesta {
	t.Helper()
	var r respuesta
	edgeEsperar(t, edgeTopeFila, method+" "+path+" sin 429", func() bool {
		var err error
		if r, err = c.client.despachar(t.Context(), method, path, body); err != nil {
			t.Fatal(err)
		}
		if r.Codigo == http.StatusTooManyRequests {
			w.calls.throttled++
			return false
		}
		return true
	})
	if action != "" {
		result := "success"
		if r.Codigo >= http.StatusBadRequest {
			result = "failure"
		}
		w.calls.audit[c.tenant+"|"+action+"|"+result]++
	}
	return r
}

// post manda un callback del puente a la API pública, reintentando el 429: la ruta no lleva token y aun
// así pasa por el límite de peticiones (el mismo 429 «demasiadas peticiones» del hallazgo 32), que
// corta antes del handler, así que el callback reintentado es el primero que la puerta «vio».
func (w *p6World) post(t *testing.T, cb crmFakeCallback) respuesta {
	t.Helper()
	var r respuesta
	edgeEsperar(t, edgeTopeFila, "el callback sin 429", func() bool {
		r = cb.Post(t, w.sc.S)
		if r.Codigo == http.StatusTooManyRequests {
			w.calls.throttled++
			return false
		}
		return true
	})
	return r
}

// p6Fields decodifica el cuerpo de una respuesta como objeto JSON. Falla (t.Fatalf) si no lo es.
func p6Fields(t *testing.T, r respuesta) map[string]any {
	t.Helper()
	var fields map[string]any
	r.JSON(t, &fields)
	return fields
}

// p6WantError exige que la respuesta traiga el código dado y que su campo `error` contenga el texto.
func p6WantError(t *testing.T, what string, r respuesta, code int, text string) {
	t.Helper()
	if r.Codigo != code || !strings.Contains(string(r.Cuerpo), text) {
		t.Errorf("%s: HTTP %d %s; quería %d con «%s»", what, r.Codigo, recortar(r.Cuerpo), code, text)
	}
}

// p6WantMark exige que la consulta de marca siga valiendo want. when dice tras qué, para el mensaje.
func p6WantMark(t *testing.T, sc *draftScene, when, want, query string, args ...any) {
	t.Helper()
	if got := p9Scalar(t, sc.DB, query, args...); got != want {
		t.Errorf("%s: la marca de estado cambió (%s → %s)\nconsulta: %s", when, want, got, query)
	}
}

// p6Poll sondea cond cada 250 ms hasta que dé true, con tope p6Timeout. Falla (t.Fatalf) con la
// descripción —evaluada al vencer, para decir lo último visto— si no se da.
func p6Poll(t *testing.T, describe func() string, cond func() bool) {
	t.Helper()
	if !draftPoll(t, p6Timeout, cond) {
		t.Fatalf("pasaron %s sin que se diera: %s", p6Timeout, describe())
	}
}

// p6WaitScalar espera, con tope p6Timeout, a que la consulta de una fila y una columna valga want.
func p6WaitScalar(t *testing.T, sc *draftScene, want, what, query string, args ...any) {
	t.Helper()
	last := ""
	p6Poll(t, func() string { return fmt.Sprintf("%s: vale %q, quería %q", what, last, want) }, func() bool {
		last = p9Scalar(t, sc.DB, query, args...)
		return last == want
	})
}

// auditTrail compara las filas de audit_events de las escrituras del puente con lo que el proceso
// apuntó llamada a llamada, por empresa y resultado. La fila se escribe DESPUÉS de responder, así que
// se espera sondeando. Ni una fila lleva el secreto: la auditoría guarda el código HTTP, no el cuerpo.
func (w *p6World) auditTrail(t *testing.T) {
	const query = `SELECT count(*)::text FROM public.audit_events WHERE tenant_id = $1::uuid AND action = $2 AND result = $3`
	total := 0
	for key, n := range w.calls.audit {
		parts := strings.Split(key, "|")
		edgeEsperarValor(t, w.sc.DB, fmt.Sprint(n), "audit_events de "+key, query, parts[0], parts[1], parts[2])
		total += n
	}
	if got := p9Scalar(t, w.sc.DB, `SELECT count(*)::text FROM public.audit_events WHERE action = $1`, p6ActWrite); got != fmt.Sprint(total) {
		t.Errorf("audit_events de %s = %s filas, quería %d (%v)", p6ActWrite, got, total, w.calls.audit)
	}
	for _, secret := range []string{w.secret, w.otherSecret} {
		if got := p9Scalar(t, w.sc.DB, `SELECT count(*)::text FROM public.audit_events a WHERE position($1 in to_jsonb(a)::text) > 0`, secret); got != "0" {
			t.Errorf("FUGA: %s filas de audit_events llevan un secreto de firma en claro", got)
		}
	}
}

// closing es el cierre del proceso, ANTES de parar el servidor (D-F6-7: el worker del outbox puede
// loguear ERROR al cancelarse su contexto en la parada): los contadores del worker, lo que el doble
// recibió, el guion, el Edge y el log.
func (w *p6World) closing(t *testing.T) {
	for status, want := range w.metrics {
		p3WaitCounter(t, w.sc.S, p6MetricDeliveries, p6MetricLabel, status, want)
	}
	// Ninguna otra serie: ni claim_lost ni una etiqueta que el proceso no provocó.
	r := w.sc.S.Admin("").Get(t, "/metrics", nil)
	exp, err := p0ParsearExposicion(string(r.Cuerpo))
	if err != nil {
		t.Fatalf("/metrics no se puede interpretar: %v", err)
	}
	for _, m := range exp.Muestras {
		if m.Nombre != p6MetricDeliveries {
			continue
		}
		if _, ok := w.metrics[m.Etiquetas[p6MetricLabel]]; !ok {
			t.Errorf("%s trae una serie que el proceso no provocó: %v = %v", p6MetricDeliveries, m.Etiquetas, m.Valor)
		}
	}
	w.auditTrail(t)
	for _, d := range w.crm.Deliveries() {
		if !d.SignatureOK || !d.InWindow || d.SchemaErr != nil {
			t.Errorf("el CRM falso recibió una entrega que un puente rechazaría (id %s): firma=%v ventana=%v schema=%v",
				d.ID, d.SignatureOK, d.InWindow, d.SchemaErr)
		}
	}
	if problems := w.sc.Script.Problems(); len(problems) != 0 {
		t.Errorf("el guion no supo atender: %v", problems)
	}
	if errs := w.sc.Edge.Errores(); len(errs) != 0 {
		t.Errorf("errores del núcleo del Edge: %v", errs)
	}
	w.sc.expectNoPendingText(t, "al cerrar el proceso")
	// D-F7-9: ni las ventanas del proceso ni el re-análisis de pushReanalysis (T8.40: su job nace
	// con su sobre) pueden dejar un job muerto por «no trae literal que analizar».
	requireNoJobWithoutLiteral(t, w.sc)
	edgeSinErrores(t, w.sc.S, w.errors)
	t.Logf("peticiones reintentadas por 429: %d", w.calls.throttled)
}
