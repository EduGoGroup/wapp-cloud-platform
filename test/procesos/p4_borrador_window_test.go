//go:build integracion

package procesos

import (
	"net/http"
	"strings"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
)

// Las reglas de la VENTANA de captación que solo fijaban los tests viejos de integración de
// internal/intake (postgres_, machine_, retry_ y despertar_integration_test.go) y que se ven desde la
// puerta: las anclas de la ventana, la base de fechas, la segunda ventana sobre el mismo evento y el
// alcance por empresa del flanco a READY. Servidor y base propios, para no mezclar sus jobs con los
// del recorrido de p4_borrador_test.go.

const (
	// p4AnchorPn es el contacto cuyos mensajes traen la hora del cliente FIJADA, y p4WakePn y
	// p4OtherTenantPn los de las dos ráfagas del subtest del flanco.
	p4AnchorPn      = "573004450001"
	p4WakePn        = "573004450002"
	p4OtherTenantPn = "573004450003"

	// p4AnchorDate es la fecha del primer mensaje (un lunes) y p4AnchorDelivery el «miércoles de la
	// semana que viene» contado desde ella. Van escritas a mano, como en
	// internal/intake/stages/p4_test.go: recalcularlas aquí pasaría con la aritmética equivocada.
	p4AnchorDate     = "2026-07-13"
	p4AnchorDelivery = "2026-07-22"
)

// p4AnchorTS es la hora del cliente del primer mensaje de la ráfaga: las 09:55 del lunes 13 de julio
// de 2026 en UTC−3, la del caso Ámbar (messageTSDeAmbar). Es fija y lejana a propósito: una fecha de
// entrega calculada desde el reloj del servidor no puede coincidir con la calculada desde ella.
var p4AnchorTS = time.Date(2026, 7, 13, 12, 55, 0, 0, time.UTC)

// p4SendAt manda un entrante sellado cuya hora del cliente (ts_unix) es at, en vez de «ahora».
func p4SendAt(t *testing.T, sc *draftScene, in sealedIncoming, at time.Time) {
	t.Helper()
	msg, err := sc.Edge.buildSealedIncoming(in)
	if err != nil {
		t.Fatalf("p4SendAt %s: %v", in.WaID, err)
	}
	msg.GetIncoming().TsUnix = at.Unix()
	if err := sc.Edge.emitir(msg); err != nil {
		t.Fatalf("p4SendAt %s: %v", in.WaID, err)
	}
}

// p4Anchors lee las tres horas de la ventana que lleva waID: cuándo nació, su última actividad y la
// hora del cliente.
func p4Anchors(t *testing.T, sc *draftScene, waID string) (created, updated, messageTS time.Time) {
	t.Helper()
	if err := sc.DB.QueryRowContext(t.Context(), `SELECT created_at, updated_at, message_ts FROM public.intake_jobs
		WHERE tenant_id = $1 AND source_refs ? $2`, sc.Tenant, waID).Scan(&created, &updated, &messageTS); err != nil {
		t.Fatalf("leer las anclas de la ventana de %s: %v", waID, err)
	}
	return created, updated, messageTS
}

// TestP4_WindowRules recorre, contra un servidor propio, las reglas de la ventana de captación:
//
//   - anclas: `message_ts` es la hora del cliente del PRIMER mensaje y no se mueve; `created_at`
//     tampoco; `updated_at` avanza con cada mensaje y no es la hora del cliente. La fecha de entrega
//     sale EXACTA de esa base (D-044.9), no del reloj del servidor.
//   - segunda ventana: con el primer job terminado, otro mensaje del mismo cliente sobre el mismo
//     evento abre una ventana NUEVA (el índice de ventanas vivas es parcial), con solo su referencia;
//     la primera no se amplía ni cambia.
//   - flanco por empresa: el flanco a READY del Edge de una empresa reanuda su job en backoff y NO el
//     de otra empresa.
func TestP4_WindowRules(t *testing.T) {
	t.Parallel()
	sc := draftScenario(t, "p4_window", "p4-ventana")
	var first p4Run
	t.Run("anclas", func(t *testing.T) { first = p4WindowAnchors(t, sc) })
	t.Run("segunda_ventana", func(t *testing.T) { p4SecondWindow(t, sc, first) })
	t.Run("flanco_por_empresa", func(st *testing.T) { p4WakeIsPerTenant(st, t, sc) })
	t.Run("cierre", func(t *testing.T) {
		if problems := sc.Script.Problems(); len(problems) != 0 {
			t.Errorf("el guion no supo atender: %v", problems)
		}
		if errs := sc.Edge.Errores(); len(errs) != 0 {
			t.Errorf("errores del núcleo del Edge: %v", errs)
		}
		requireNoJobWithoutLiteral(t, sc)
		edgeSinErrores(t, sc.S, nil)
	})
}

// p4WindowAnchors manda la ráfaga canónica con la hora del cliente fijada (el primero en p4AnchorTS,
// los otros dos 30 y 70 segundos después) y afirma las anclas tras el primer mensaje y tras el
// tercero, y la fecha de entrega del borrador.
func p4WindowAnchors(t *testing.T, sc *draftScene) p4Run {
	run := p4Run{contact: p4AnchorPn}
	texts := draftBurst()
	from := run.contact + "@s.whatsapp.net"
	holdDraftWindow(t, sc.DB, sc.Tenant)

	run.ids = []string{draftWaID(run.contact, 0), draftWaID(run.contact, 1), draftWaID(run.contact, 2)}
	p4SendAt(t, sc, sealedIncoming{From: from, WaID: run.ids[0], Text: texts[0], FromPn: run.contact}, p4AnchorTS)
	sc.expectText(t, run.contact, p3Welcome)
	sc.expectText(t, run.contact, draftMenuPrompt)
	draftWaitJob(t, sc, run.ids[0], "aggregating|1", "la ventana con el primer mensaje")
	created, updated, messageTS := p4Anchors(t, sc, run.ids[0])
	if !messageTS.Equal(p4AnchorTS) {
		t.Fatalf("message_ts = %s, quería la hora del cliente del primer mensaje (%s)", messageTS, p4AnchorTS)
	}

	for i, offset := range []time.Duration{30 * time.Second, 70 * time.Second} {
		in := sealedIncoming{From: from, WaID: run.ids[i+1], Text: texts[i+1], FromPn: run.contact}
		p4SendAt(t, sc, in, p4AnchorTS.Add(offset))
		sc.expectText(t, run.contact, draftMenuInvalid)
	}
	draftWaitJob(t, sc, run.ids[0], "aggregating|3", "la ventana con la ráfaga entera")
	created2, updated2, messageTS2 := p4Anchors(t, sc, run.ids[0])
	if !messageTS2.Equal(p4AnchorTS) {
		t.Errorf("message_ts pasó a %s con los mensajes siguientes; quería que siguiera en %s", messageTS2, p4AnchorTS)
	}
	if !created2.Equal(created) {
		t.Errorf("created_at se movió con los mensajes siguientes (%s → %s): el techo de la ventana se reiniciaría", created, created2)
	}
	if !updated2.After(updated) {
		t.Errorf("updated_at no avanzó con los mensajes siguientes (%s → %s): el silencio se mediría desde el primero", updated, updated2)
	}
	if updated2.Sub(p4AnchorTS) < 24*time.Hour {
		t.Errorf("updated_at (%s) es la hora del cliente y no la de Postgres", updated2)
	}

	flushDraftWindow(t, sc)
	run.jobID, run.intakeID = waitDraft(t, sc, run.contact)
	payload := p4RevisionPayload(t, sc, run.intakeID)
	if payload.DeliveryDate != p4AnchorDelivery || !payload.MessageTS.Equal(p4AnchorTS) {
		t.Errorf("la revisión trae delivery_date %q y message_ts %s; quería %s (el miércoles de la semana siguiente al %s) y %s",
			payload.DeliveryDate, payload.MessageTS, p4AnchorDelivery, p4AnchorDate, p4AnchorTS)
	}
	const basis = `SELECT (artifacts->'p4'->>'delivery_date') || '|' || (artifacts->'p4'->>'delivery_date_basis') FROM public.intake_jobs WHERE id = $1::uuid`
	if got, want := p9Scalar(t, sc.DB, basis, run.jobID), p4AnchorDelivery+"|message_ts="+p4AnchorDate; got != want {
		t.Errorf("el artefacto de P4 = %q, quería %q", got, want)
	}
	calls := sc.Script.Calls(stageP4)
	if len(calls) != 1 || !strings.Contains(calls[0].Prompt, "lunes "+p4AnchorDate) ||
		!strings.Contains(calls[0].Prompt, `"message_ts=`+p4AnchorDate+`"`) {
		t.Errorf("el prompt de P4 no lleva la fecha del MENSAJE (lunes %s) como referencia", p4AnchorDate)
	}
	return run
}

// p4SecondWindow manda un cuarto mensaje del mismo cliente con su primer job ya terminado. El evento
// sigue vivo, así que el mensaje abre OTRA ventana: una fila nueva `aggregating` con solo su
// referencia. El job terminado conserva sus tres referencias y su estado. Cerrada, la segunda ventana
// corre el pipeline entero otra vez sobre el hilo del evento y deja la revisión 2 de la MISMA
// solicitud (su id se deriva del evento), fechada contra la hora de SU primer mensaje; la revisión 1
// no cambia.
func p4SecondWindow(t *testing.T, sc *draftScene, first p4Run) {
	holdDraftWindow(t, sc.DB, sc.Tenant)
	waID := draftWaID(first.contact, 3)
	sc.Edge.sendSealedIncoming(t, sealedIncoming{From: first.contact + "@s.whatsapp.net", WaID: waID, Text: "Y sumale otro paquete, porfa", FromPn: first.contact})
	if got := sc.Edge.esperarTexto(t, p3TextTimeout); got.A != first.contact {
		t.Fatalf("la respuesta del flujo al cuarto mensaje fue a %q, quería a %q", got.A, first.contact)
	}
	draftWaitJob(t, sc, waID, "aggregating|1", "la segunda ventana del mismo evento")
	draftWaitJob(t, sc, first.ids[0], "done|3", "el primer job, intacto")
	const sameEvent = `SELECT count(*)::text || '|' || count(DISTINCT event_id)::text FROM public.intake_jobs
		WHERE tenant_id = $1 AND contact_id = (SELECT contact_id FROM public.intake_jobs WHERE id = $2::uuid)`
	if got := p9Scalar(t, sc.DB, sameEvent, sc.Tenant, first.jobID); got != "2|1" {
		t.Errorf("los jobs del contacto = %q (filas|eventos distintos), quería dos ventanas sobre el MISMO evento", got)
	}

	before := len(sc.Script.Calls(""))
	flushDraftWindow(t, sc)
	const second = `SELECT status || '|' || coalesce(stage, '-') || '|' || coalesce(intake_id::text, '-') || '|' || coalesce(artifacts->'draft'->>'revision_no', '-')
		FROM public.intake_jobs WHERE tenant_id = $1 AND source_refs ? $2`
	last := ""
	if !draftPoll(t, draftTimeout, func() bool {
		last = p9Scalar(t, sc.DB, second, sc.Tenant, waID)
		return strings.HasPrefix(last, "done|") || strings.HasPrefix(last, "failed|")
	}) {
		t.Fatalf("el job de la segunda ventana no terminó en %s: %q", draftTimeout, last)
	}
	if want := "done|draft|" + first.intakeID + "|2"; last != want {
		t.Fatalf("el job de la segunda ventana = %q, quería %q: la MISMA solicitud, con su revisión 2", last, want)
	}
	if n := len(sc.Script.Calls("")) - before; n != 5 {
		t.Errorf("la segunda ventana provocó %d inferencias, quería 5 (el pipeline entero otra vez)", n)
	}
	if n := consultaEntero(t, sc.DB, `SELECT count(*) FROM public.intakes WHERE tenant_id = $1`, sc.Tenant); n != 1 {
		t.Errorf("intakes tiene %d filas de la empresa, quería 1: la segunda ventana del evento no crea otra solicitud", n)
	}
	const revisions = `SELECT string_agg(revision_no::text || ':' || kind || ':' || created_by, ',' ORDER BY revision_no)
		FROM public.intake_revisions WHERE intake_id = $1::uuid`
	if got := p9Scalar(t, sc.DB, revisions, first.intakeID); got != "1:interpreted:system,2:interpreted:system" {
		t.Errorf("las revisiones de la solicitud = %q, quería la 1 y la 2, interpreted, de system", got)
	}
	if got := p4RevisionPayload(t, sc, first.intakeID).DeliveryDate; got != p4AnchorDelivery {
		t.Errorf("la revisión 1 cambió al nacer la 2: delivery_date %q, quería %q", got, p4AnchorDelivery)
	}
	const secondBase = `SELECT (payload->>'delivery_date' <> $2)::text FROM public.intake_revisions WHERE intake_id = $1::uuid AND revision_no = 2`
	if got := p9Scalar(t, sc.DB, secondBase, first.intakeID, p4AnchorDelivery); got != "true" {
		t.Errorf("la revisión 2 fecha contra la base de la primera ventana; quería la hora de SU primer mensaje")
	}
}

// p4SecondTenant deja en el MISMO servidor otra empresa lista para captar pedidos, con su Edge y su
// guion: lo que draftScenario hace para la primera. root es el test del PROCESO: el Edge y el guion se
// atan a él, no al subtest (hallazgo 33).
func p4SecondTenant(t, root *testing.T, sc *draftScene) *draftScene {
	t.Helper()
	tenant := crearTenantConPlan(t, sc.S, sc.TokenStaff, "p4-ventana-otra", draftPlan)
	admin := uuidAleatorio(t)
	edgeAltaAdminDelTenant(t, sc.DB, admin, tenant)
	esc := edgeEscenario{S: sc.S, DB: sc.DB, Tenant: tenant, TokenStaff: sc.TokenStaff,
		TokenAdmin: canjear(t, sc.S, sc.S.Identidad.TokenDe(admin, "wapp.bff"))}
	script := newAmbarScript(root)
	e := enrolar(t, sc.S, edgeEmitirCodigo(t, sc.S, sc.TokenStaff, tenant))
	e.Inferir = script.Infer
	e.conectar(root)
	e.esperarLeases(t, 2, edgeTopeFila)
	e.esperarConfig(t, "filters", edgeTopeFila)
	other := &draftScene{p3Scene: p3Scene{edgeEscenario: esc, Edge: e, Pub: sc.S.Publica(esc.TokenAdmin)}, Script: script, beat: 5}

	if r := other.Pub.Post(t, "/api/v1/catalog/import?mode=apply&ref="+draftCatalogRef, draftCatalog()); r.Codigo != http.StatusOK {
		t.Fatalf("importar el catálogo de la segunda empresa: HTTP %d\ncuerpo: %s", r.Codigo, recortar(r.Cuerpo))
	}
	def := map[string]any{
		"flow_id": draftFlowID, "version": 1, "initial": "root",
		"nodes": map[string]any{
			"root": map[string]any{"type": "menu", "prompt": draftMenuPrompt, "options": map[string]string{"1": "fin"}},
			"fin":  map[string]any{"type": "message", "text": draftMenuExit, "next": nil},
		},
	}
	if r := other.Pub.Post(t, "/api/v1/flows", map[string]any{"definition": def}); r.Codigo != http.StatusCreated {
		t.Fatalf("publicar el flujo de la segunda empresa: HTTP %d\ncuerpo: %s", r.Codigo, recortar(r.Cuerpo))
	}
	trigger := map[string]any{"kind": "event_start", "keyword": draftKeyword, "match_type": "contains", "event_kind": draftEventKind, "flow_id": draftFlowID}
	if r := other.Pub.Post(t, "/api/v1/triggers", trigger); r.Codigo != http.StatusCreated {
		t.Fatalf("crear el disparo de la segunda empresa: HTTP %d\ncuerpo: %s", r.Codigo, recortar(r.Cuerpo))
	}
	other.setProfile(t, "active")
	return other
}

// p4WakeIsPerTenant deja a DOS empresas con un job cada una en backoff (P2 caído en sus dos Edges),
// repone los dos modelos y hace que SOLO el Edge de la primera pase de DOWN a READY: su job termina
// antes de vencer su backoff, y el de la otra empresa sigue en `pending` con su intento y su marca en
// el futuro, sin que su Edge reciba otra petición.
func p4WakeIsPerTenant(t, root *testing.T, sc *draftScene) {
	other := p4SecondTenant(t, root, sc)
	const state = `SELECT status || '|' || attempts::text || '|' || (next_attempt_at > now())::text FROM public.intake_jobs WHERE tenant_id = $1 AND source_refs ? $2`
	for _, x := range []struct {
		scene   *draftScene
		contact string
	}{{sc, p4WakePn}, {other, p4OtherTenantPn}} {
		x.scene.Script.Fail(stageP2, cloudlinkv1.InferenceError_INFERENCE_ERROR_OLLAMA_DOWN)
		sendDraftBurst(t, x.scene, x.contact, draftBurst())
		flushDraftWindow(t, x.scene)
		last := ""
		if !draftPoll(t, draftTimeout, func() bool {
			last = p9Scalar(t, sc.DB, state, x.scene.Tenant, draftWaID(x.contact, 0))
			return last == "pending|1|true"
		}) {
			t.Fatalf("el job de %s no quedó en backoff: %q", x.contact, last)
		}
		x.scene.Script.Clear(stageP2)
	}
	otherCalls := len(other.Script.Calls(""))

	sc.beatReadiness(t, cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_DOWN)
	sc.beatReadiness(t, cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_READY)
	waitDraft(t, sc, p4WakePn)

	if got := p9Scalar(t, sc.DB, state, other.Tenant, draftWaID(p4OtherTenantPn, 0)); got != "pending|1|true" {
		t.Errorf("el job de la OTRA empresa = %q tras el flanco de un Edge ajeno; quería pending|1 con su backoff sin vencer", got)
	}
	if n := len(other.Script.Calls("")) - otherCalls; n != 0 {
		t.Errorf("el Edge de la otra empresa recibió %d inferencias por el flanco de un Edge ajeno", n)
	}
	if errs := other.Edge.Errores(); len(errs) != 0 {
		t.Errorf("errores del núcleo del Edge de la otra empresa: %v", errs)
	}
}
