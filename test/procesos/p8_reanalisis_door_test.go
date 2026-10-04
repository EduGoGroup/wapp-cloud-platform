//go:build integracion

package procesos

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
)

// La puerta del re-análisis de P8: qué pasa con el modelo caído (el job queda vivo y un segundo
// re-análisis del mismo evento se rechaza) y qué hace el viejo con una solicitud que ya no está por
// aprobar. La espera del «primero pendiente» es determinista: el guion falla P2 y el job vuelve a
// `pending` con su backoff, donde se queda hasta que el Edge vuelve a decir READY.

const (
	// p8Quote es la cotización con la que la dueña aprueba, y p8RejectedNotice el aviso que el
	// servidor manda al cliente cuando la dueña rechaza la solicitud.
	p8Quote          = "Tu presupuesto P8: una torta de vainilla, 3900."
	p8RejectedNotice = "No podemos tomar tu pedido en este momento. Si quieres, respóndenos y lo vemos."

	// p8HeldJob lee el job retenido, columna a columna.
	p8HeldJob = `SELECT status || '|' || coalesce(stage, '-') || '|' || attempts::text || '|' || intake_id::text
			|| '|' || (source_text_enc IS NOT NULL AND source_text_dek IS NOT NULL AND source_text_kek_id IS NOT NULL)::text
			|| '|' || (next_attempt_at > now())::text || '|' || (coalesce(artifacts, '{}'::jsonb) = '{}'::jsonb)::text
			|| '|' || coalesce(requested_by, '-') || '|' || coalesce(reanalysis_via, '-') || '|' || coalesce(reanalysis_source, '-')
			|| '|' || coalesce(reanalyzed_from::text, '-')
		FROM public.intake_jobs WHERE id = $1::uuid`
	// p8Notice lee el aviso de degradación de la empresa.
	p8Notice = `SELECT coalesce(string_agg(reason || '|' || via || '|' || occurrences::text || '|' || (read_at IS NULL)::text, ','), '')
		FROM public.owner_degradation_notices WHERE tenant_id = $1`
)

// p8HeldCase es una petición hecha MIENTRAS el primer re-análisis sigue vivo.
type p8HeldCase struct {
	name string
	id   func(id string) string // la forma en que se escribe el id de la solicitud
	body any
	code int
	// body esperado: «» = el 422 `reanalysis_in_progress` con el job retenido.
	want string
}

// p8HeldTable son los casos de la puerta con el primero pendiente, medidos contra el viejo. El id se
// resuelve con uuid.Parse, que admite más formas que la canónica: mayúsculas, llaves y sin guiones
// llegan a la MISMA solicitud (y por eso chocan con el job vivo); la forma `urn:uuid:` pasa el
// parseo y Postgres la rechaza: 500.
var p8HeldTable = []p8HeldCase{
	{name: "el mismo, otra vez", body: map[string]any{}},
	{name: "afirmando la vía local", body: map[string]any{"via": "local"}},
	{name: "con un campo provider que no existe", body: map[string]any{"provider": "api", "tenant_id": "otro"}},
	{name: "con transcripción nueva", body: map[string]any{"text": "esto no se guarda ZZP8NOENTRA"}},
	{name: "con transcripción de puros invisibles", body: map[string]any{"text": "\u200b\u200b   \n\t"}},
	{name: "id en mayúsculas", id: strings.ToUpper, body: map[string]any{}},
	{name: "id entre llaves", id: func(id string) string { return "{" + id + "}" }, body: map[string]any{}},
	{name: "id sin guiones", id: func(id string) string { return strings.ReplaceAll(id, "-", "") }, body: map[string]any{}},
	{name: "id con prefijo urn:uuid:", id: func(id string) string { return "urn:uuid:" + id }, body: map[string]any{},
		code: http.StatusInternalServerError, want: p8Failed},
	{name: "vía inválida: la forma gana al job vivo", body: map[string]any{"via": "chatgpt"},
		code: http.StatusBadRequest, want: `{"error":"invalid_via","via":"chatgpt"}`},
}

// door es la puerta con el modelo caído. La dueña pide el re-análisis y la puerta contesta 200 igual
// (no espera al modelo); P2 falla con OLLAMA_DOWN y el job NO muere: vuelve a `pending` con un
// intento cobrado, su backoff y su sobre compuesto, la revisión vigente sigue siendo la última y la
// dueña recibe su aviso de degradación. Con ese job vivo, un segundo re-análisis del mismo evento se
// rechaza con `422 reanalysis_in_progress` y el id del job que estorba, sin escribir nada. Al volver
// el modelo (latido DOWN y READY del Edge) el job se reanuda sin esperar al backoff y la revisión
// sale, con el origen `event_thread`: la transcripción pegada antes ya es parte del hilo.
func (w *p8World) door(t *testing.T) {
	sc, tgt := w.sc, w.main
	counter := p4DegradationCounter(t, sc)
	p2Calls := len(sc.Script.Calls(stageP2))

	sc.Script.Fail(stageP2, cloudlinkv1.InferenceError_INFERENCE_ERROR_OLLAMA_DOWN)
	defer sc.Script.Clear(stageP2)
	p := w.open(t, tgt, map[string]any{}, "event_thread")
	p8WaitJob(t, sc, p.jobID, "pending/-/1")
	p9WaitLogLines(t, sc.S, p4MsgRetry, map[string]string{"job_id": p.jobID, "stage": "p2", "causa": "infra"}, 1)

	want := fmt.Sprintf("pending|-|1|%s|true|true|true|owner|local|event_thread|%d", tgt.id, p.from)
	if got := p9Scalar(t, sc.DB, p8HeldJob, p.jobID); got != want {
		t.Errorf("el job con el modelo caído = %q, quería %q", got, want)
	}
	nextAttempt := p9Scalar(t, sc.DB, `SELECT next_attempt_at::text FROM public.intake_jobs WHERE id = $1::uuid`, p.jobID)
	if got := p9Scalar(t, sc.DB, p8Notice, sc.Tenant); got != "ollama_down|local|1|true" {
		t.Errorf("owner_degradation_notices = %q, quería un aviso ollama_down|local con una ocurrencia y sin leer", got)
	}
	if after := p4DegradationCounter(t, sc); after != counter+1 {
		t.Errorf("%s{origen=pipeline,reason=ollama_down,via=local} pasó de %v a %v, quería +1", p4MetricDegradation, counter, after)
	}
	if got := p9Scalar(t, sc.DB, p8OldRevs, tgt.id, p.from); got != p.oldRevs {
		t.Errorf("con el modelo caído las revisiones cambiaron (filas:huella %s → %s)", p.oldRevs, got)
	}
	if got := p9Scalar(t, sc.DB, p8IntakeRow, tgt.id); got != p.intakeRow {
		t.Errorf("con el modelo caído la solicitud cambió:\nantes   %s\ndespués %s", p.intakeRow, got)
	}

	w.whileHeld(t, tgt, p.jobID)
	if n := len(sc.Script.Calls(stageP2)) - p2Calls; n != 1 {
		t.Errorf("el worker pidió P2 %d veces con el modelo caído, quería 1 (el backoff lo retiene)", n)
	}

	sc.Script.Clear(stageP2)
	sc.beatReadiness(t, cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_DOWN)
	sc.beatReadiness(t, cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_READY)
	w.settle(t, p, 1, append([]string{"p2"}, p8FullRun...))
	w.checkPastedPrompts(t, p, 1)
	p9WaitLogLines(t, sc.S, p4MsgWoken, map[string]string{"tenant_id": sc.Tenant, "edge_id": sc.Edge.EdgeID}, 1)
	if got := p9Scalar(t, sc.DB, `SELECT (updated_at < $2::timestamptz)::text FROM public.intake_jobs WHERE id = $1::uuid`, p.jobID, nextAttempt); got != "true" {
		t.Errorf("el job reanudado no terminó antes de vencer su backoff (%s)", nextAttempt)
	}
	if got := p9Scalar(t, sc.DB, p8Notice, sc.Tenant); got != "ollama_down|local|1|true" {
		t.Errorf("tras la reanudación owner_degradation_notices = %q, quería el mismo aviso con una ocurrencia", got)
	}
}

// whileHeld recorre p8HeldTable con el job retenido: cada petición recibe su rechazo y ninguna
// escribe nada (ni un job, ni una fila del hilo aunque traiga transcripción, ni una inferencia). La
// solicitud vista desde OTRA empresa sigue siendo un 404: la solicitud se busca antes que el job vivo.
func (w *p8World) whileHeld(t *testing.T, tgt *p8Target, heldJob string) {
	t.Helper()
	before := p8Snapshot(t, w.sc, p8Tables)
	calls := len(w.sc.Script.Calls(""))
	inProgress := `{"error":"reanalysis_in_progress","job_id":"` + heldJob + `"}`
	for _, tc := range p8HeldTable {
		id, code, want := tgt.id, tc.code, tc.want
		if tc.id != nil {
			id = tc.id(tgt.id)
		}
		if want == "" {
			code, want = http.StatusUnprocessableEntity, inProgress
		}
		if r := w.post(t, w.owner, id, tc.body); !p8Is(r, code, want) {
			t.Errorf("%s: HTTP %d %s; quería %d %s", tc.name, r.Codigo, recortar(r.Cuerpo), code, want)
		}
	}
	if r := w.post(t, w.outsider, tgt.id, map[string]any{}); !p8Is(r, http.StatusNotFound, p8NotFound) {
		t.Errorf("otra empresa con el job vivo: HTTP %d %s; quería 404", r.Codigo, recortar(r.Cuerpo))
	}
	p8Unchanged(t, w.sc, before, "tras los rechazos con el primer re-análisis pendiente")
	if n := len(w.sc.Script.Calls("")) - calls; n != 0 {
		t.Errorf("los rechazos con el primer re-análisis pendiente provocaron %d inferencias", n)
	}
	w.sc.expectNoPendingText(t, "tras los rechazos con el primer re-análisis pendiente")
}

// approved afirma lo que hace el viejo con una solicitud APROBADA. La dueña corrige las líneas (PUT
// …/items: revisión 2, `corrected`), aprueba (POST …/approve: revisión 3, `approved`; el cliente
// recibe la cotización) y después pide el re-análisis: la puerta NO lo rechaza. Abre su job y sale la
// revisión 4, `interpreted` y de `owner`; la solicitud sigue `confirmed` con su importe y sus líneas,
// y al cliente no le llega nada más.
func (w *p8World) approved(t *testing.T) {
	sc := w.sc
	tgt := w.newTarget(t, p8ApprovedPn)
	r := w.write(t, http.MethodPost, "/api/v1/intakes/"+tgt.id+"/approve", map[string]any{"rendered_text": p8Quote})
	if r.Codigo != http.StatusBadRequest || !strings.Contains(string(r.Cuerpo), `"error":"lines_without_price"`) {
		t.Fatalf("aprobar el borrador sin precificar: HTTP %d, quería 400 lines_without_price\ncuerpo: %s", r.Codigo, recortar(r.Cuerpo))
	}
	r = w.write(t, http.MethodPut, "/api/v1/intakes/"+tgt.id+"/items", map[string]any{"items": []any{
		map[string]any{"sku": "TORTA-VAIN", "label": "Torta de vainilla", "qty": 1, "unit_price": 3900},
	}})
	if r.Codigo != http.StatusOK {
		t.Fatalf("corregir las líneas: HTTP %d, quería 200\ncuerpo: %s", r.Codigo, recortar(r.Cuerpo))
	}
	r = w.write(t, http.MethodPost, "/api/v1/intakes/"+tgt.id+"/approve", map[string]any{"rendered_text": p8Quote})
	if r.Codigo != http.StatusOK {
		t.Fatalf("aprobar: HTTP %d, quería 200\ncuerpo: %s", r.Codigo, recortar(r.Cuerpo))
	}
	sc.expectText(t, tgt.pn, p8Quote)
	tgt.rev = 3
	const state = `SELECT status || '|' || total::text || '|' || (SELECT string_agg(kind || ':' || created_by, ',' ORDER BY revision_no)
			FROM public.intake_revisions r WHERE r.intake_id = i.id) || '|' || (SELECT count(*) FROM public.intake_items x WHERE x.intake_id = i.id)::text
		FROM public.intakes i WHERE id = $1::uuid`
	if got := p9Scalar(t, sc.DB, state, tgt.id); got != "confirmed|3900|interpreted:system,corrected:owner,approved:owner|1" {
		t.Fatalf("la solicitud aprobada = %q, quería confirmed con sus tres revisiones y una línea", got)
	}

	p := w.open(t, tgt, map[string]any{}, "event_thread")
	w.settle(t, p, 0, p8FullRun)
	if got := p9Scalar(t, sc.DB, state, tgt.id); got != "confirmed|3900|interpreted:system,corrected:owner,approved:owner,interpreted:owner|1" {
		t.Errorf("la solicitud aprobada tras el re-análisis = %q; quería confirmed, el mismo importe y una revisión interpreted más", got)
	}
	if n := consultaEntero(t, sc.DB, `SELECT count(*) FROM public.webhook_outbox`); n != 0 {
		t.Errorf("webhook_outbox tiene %d filas: la empresa no tiene puente CRM configurado", n)
	}
}

// rejected afirma lo que hace el viejo con una solicitud RECHAZADA. El descarte por lotes (POST
// /api/v1/intakes/discard) no alcanza a un borrador `pending_approval`: lo salta con `not_open` y no
// lo toca. La dueña lo rechaza (POST …/status; el cliente recibe el aviso) y después pide el
// re-análisis: tampoco se rechaza. Sale la revisión 2 y la solicitud sigue `rejected`.
func (w *p8World) rejected(t *testing.T) {
	sc := w.sc
	tgt := w.newTarget(t, p8RejectedPn)
	before := p8Snapshot(t, sc, p8Tables)
	r := w.write(t, http.MethodPost, "/api/v1/intakes/discard", map[string]any{"intake_ids": []string{tgt.id}})
	if !p8Is(r, http.StatusOK, `{"discarded":[],"skipped":[{"intake_id":"`+tgt.id+`","reason":"not_open"}]}`) {
		t.Fatalf("descartar un borrador por aprobar: HTTP %d %s; quería 200 con la solicitud saltada por not_open", r.Codigo, recortar(r.Cuerpo))
	}
	p8Unchanged(t, sc, before, "tras el descarte que no aplica")

	r = w.write(t, http.MethodPost, "/api/v1/intakes/"+tgt.id+"/status", map[string]any{"status": "rejected"})
	if r.Codigo != http.StatusOK {
		t.Fatalf("rechazar la solicitud: HTTP %d, quería 200\ncuerpo: %s", r.Codigo, recortar(r.Cuerpo))
	}
	sc.expectText(t, tgt.pn, p8RejectedNotice)
	const state = `SELECT status || '|' || (SELECT string_agg(kind || ':' || created_by, ',' ORDER BY revision_no)
			FROM public.intake_revisions r WHERE r.intake_id = i.id)
		FROM public.intakes i WHERE id = $1::uuid`
	if got := p9Scalar(t, sc.DB, state, tgt.id); got != "rejected|interpreted:system" {
		t.Fatalf("la solicitud rechazada = %q, quería rejected con su revisión 1", got)
	}

	p := w.open(t, tgt, map[string]any{}, "event_thread")
	w.settle(t, p, 0, p8FullRun)
	if got := p9Scalar(t, sc.DB, state, tgt.id); got != "rejected|interpreted:system,interpreted:owner" {
		t.Errorf("la solicitud rechazada tras el re-análisis = %q; quería rejected y una revisión interpreted más", got)
	}
}
