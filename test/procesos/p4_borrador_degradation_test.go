//go:build integracion

package procesos

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
)

// La degradación de P4 (diseno.md §4, paso 4) y la puerta de la ventana: qué pasa cuando el Ollama
// del cliente no contesta, y qué primer mensaje abre una ventana de captación.

const (
	// p4DegradedPn es el contacto cuya ráfaga encuentra el modelo caído, y p4BystanderPn el de otro
	// cliente que escribe al flujo estático mientras tanto.
	p4DegradedPn  = "573004440003"
	p4BystanderPn = "573004440004"

	// p4MetricDegradation es el contador de caídas de la vía LLM. Es un CounterVec: no aparece en
	// /metrics hasta su primer incremento (trampa T-10), así que se mira DESPUÉS de provocar la caída.
	p4MetricDegradation = "wapp_llm_degradacion_total"

	// p4MsgRetry es la línea WARN del worker cuando una etapa falla y el job vuelve a la cola, y
	// p4MsgWoken la INFO de cuando el Edge vuelve a decir READY y sus jobs se reanudan.
	p4MsgRetry = "pipeline: la etapa falló; el job vuelve a la cola con backoff"
	p4MsgWoken = "pipeline: el Edge acaba de poder servir inferencia; se reanudan sus jobs sin esperar al backoff"
	// p4MsgNotified es la línea WARN del aviso al dueño.
	p4MsgNotified = "degradación: la vía LLM del tenant falló y se avisó al dueño"
)

// p4DegradationCounter devuelve el valor de wapp_llm_degradacion_total{origen="pipeline",
// reason="ollama_down",via="local"}, o 0 si la serie aún no existe.
func p4DegradationCounter(t *testing.T, sc *draftScene) float64 {
	t.Helper()
	r := sc.S.Admin("").Get(t, "/metrics", nil)
	if r.Codigo != http.StatusOK {
		t.Fatalf("GET /metrics = %d, quería 200", r.Codigo)
	}
	exp, err := p0ParsearExposicion(string(r.Cuerpo))
	if err != nil {
		t.Fatalf("/metrics no se puede interpretar: %v", err)
	}
	for _, m := range exp.Muestras {
		if m.Nombre == p4MetricDegradation && m.Etiquetas["origen"] == "pipeline" && m.Etiquetas["reason"] == "ollama_down" && m.Etiquetas["via"] == "local" {
			return m.Valor
		}
	}
	return 0
}

// p4Degradation es el paso 4: con P2 fallando con INFERENCE_ERROR_OLLAMA_DOWN, la ráfaga de un cliente
// deja UN aviso al dueño (owner_degradation_notices y GET /api/v1/degradation-notices), el contador
// sube en uno, y el job NO muere ni termina: vuelve a `pending` con un intento cobrado y su backoff, y
// el worker no lo martillea. Mientras tanto el flujo estático responde igual a otro cliente (INV-10).
// Al volver el modelo, un latido DOWN y otro READY del Edge reanudan el job sin esperar a su backoff,
// y el borrador sale completo.
func p4Degradation(t *testing.T, sc *draftScene) {
	counter := p4DegradationCounter(t, sc)
	p2Calls := len(sc.Script.Calls(stageP2))
	intakes := consultaEntero(t, sc.DB, `SELECT count(*) FROM public.intakes WHERE tenant_id = $1`, sc.Tenant)
	if n := consultaEntero(t, sc.DB, `SELECT count(*) FROM public.owner_degradation_notices`); n != 0 {
		t.Fatalf("owner_degradation_notices ya tiene %d filas antes de provocar la caída", n)
	}

	sc.Script.Fail(stageP2, cloudlinkv1.InferenceError_INFERENCE_ERROR_OLLAMA_DOWN)
	defer sc.Script.Clear(stageP2)
	ids := sendDraftBurst(t, sc, p4DegradedPn, draftBurst())
	flushDraftWindow(t, sc)

	const notice = `SELECT reason || '|' || via || '|' || occurrences::text || '|' || (read_at IS NULL)::text
			|| '|' || (window_end - window_start = interval '15 minutes' AND created_at >= window_start AND created_at < window_end)::text
		FROM public.owner_degradation_notices WHERE tenant_id = $1`
	got := ""
	if !draftPoll(t, draftTimeout, func() bool {
		got = p9Scalar(t, sc.DB, notice, sc.Tenant)
		return got != ""
	}) {
		t.Fatalf("el aviso de degradación no llegó en %s", draftTimeout)
	}
	if got != "ollama_down|local|1|true|true" {
		t.Errorf("owner_degradation_notices = %q, quería ollama_down|local|1 sin leer, en su ventana de 15 minutos", got)
	}
	p9WaitLogLines(t, sc.S, p4MsgRetry, map[string]string{"stage": "p2", "causa": "infra"}, 1)
	p4NoticeAPI(t, sc)
	if after := p4DegradationCounter(t, sc); after != counter+1 {
		t.Errorf("%s{origen=pipeline,reason=ollama_down,via=local} pasó de %v a %v, quería +1", p4MetricDegradation, counter, after)
	}

	const job = `SELECT status || '|' || coalesce(stage, '-') || '|' || attempts::text || '|' || (intake_id IS NULL)::text
			|| '|' || (next_attempt_at > now())::text || '|' || (source_text_enc IS NOT NULL AND source_text_dek IS NOT NULL AND source_text_kek_id IS NOT NULL)::text
		FROM public.intake_jobs WHERE tenant_id = $1 AND source_refs ? $2`
	if got := p9Scalar(t, sc.DB, job, sc.Tenant, ids[0]); got != "pending|-|1|true|true|true" {
		t.Errorf("el job con el modelo caído = %q, quería pending, un intento, sin solicitud, con backoff y con su sobre", got)
	}
	nextAttempt := p9Scalar(t, sc.DB, `SELECT next_attempt_at::text FROM public.intake_jobs WHERE tenant_id = $1 AND source_refs ? $2`, sc.Tenant, ids[0])
	if n := len(sc.Script.Calls(stageP2)) - p2Calls; n != 1 {
		t.Errorf("el worker pidió P2 %d veces con el modelo caído, quería 1 (el backoff lo retiene)", n)
	}
	if n := consultaEntero(t, sc.DB, `SELECT count(*) FROM public.intakes WHERE tenant_id = $1`, sc.Tenant); n != intakes {
		t.Errorf("intakes pasó de %d a %d filas con el modelo caído", intakes, n)
	}

	p4StaticFlowDuringOutage(t, sc)

	sc.Script.Clear(stageP2)
	sc.beatReadiness(t, cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_DOWN)
	sc.beatReadiness(t, cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_READY)
	jobID, intakeID := waitDraft(t, sc, p4DegradedPn)
	p9WaitLogLines(t, sc.S, p4MsgWoken, map[string]string{"tenant_id": sc.Tenant, "edge_id": sc.Edge.EdgeID}, 1)
	const done = `SELECT status || '|' || stage || '|' || attempts::text || '|' || (updated_at < $2::timestamptz)::text FROM public.intake_jobs WHERE id = $1::uuid`
	if got := p9Scalar(t, sc.DB, done, jobID, nextAttempt); got != "done|draft|1|true" {
		t.Errorf("el job reanudado = %q, quería done|draft con su intento cobrado y terminado ANTES de vencer su backoff (%s)", got, nextAttempt)
	}
	if n := len(sc.Script.Calls(stageP2)) - p2Calls; n != 2 {
		t.Errorf("P2 se pidió %d veces en total para la ráfaga degradada, quería 2 (la caída y la reanudación)", n)
	}
	if got := p9Scalar(t, sc.DB, notice, sc.Tenant); got != "ollama_down|local|1|true|true" {
		t.Errorf("tras la reanudación owner_degradation_notices = %q, quería el mismo aviso con una ocurrencia", got)
	}
	if after := p4DegradationCounter(t, sc); after != counter+1 {
		t.Errorf("tras la reanudación el contador vale %v, quería %v", after, counter+1)
	}
	if n := len(p9LogLines(sc.S, p4MsgNotified, map[string]string{"tenant_id": sc.Tenant, "reason": "ollama_down", "via": "local"})); n != 1 {
		t.Errorf("el log trae %d avisos de degradación, quería 1", n)
	}
	if got := p9Scalar(t, sc.DB, `SELECT status || '|' || (SELECT count(*) FROM public.intake_revisions r WHERE r.intake_id = i.id)::text
		FROM public.intakes i WHERE id = $1::uuid`, intakeID); got != "pending_approval|1" {
		t.Errorf("el borrador de la ráfaga degradada = %q, quería pending_approval con una revisión", got)
	}
}

// p4NoticeAPI afirma que GET /api/v1/degradation-notices devuelve a la dueña el aviso de la tabla, y
// solo ese: el mismo id, el motivo, la vía, una ocurrencia y sin leer.
func p4NoticeAPI(t *testing.T, sc *draftScene) {
	t.Helper()
	r := sc.Pub.Get(t, "/api/v1/degradation-notices", nil)
	var body struct {
		Notices []struct {
			ID, Reason, Via string
			Occurrences     int
			Read            bool
			ReadAt          string `json:"read_at"`
			WindowStart     string `json:"window_start"`
			WindowEnd       string `json:"window_end"`
		}
		Limit, Offset int
	}
	r.JSON(t, &body)
	if r.Codigo != http.StatusOK || len(body.Notices) != 1 || body.Offset != 0 || body.Limit < 1 {
		t.Fatalf("GET /api/v1/degradation-notices: HTTP %d con %d avisos\ncuerpo: %s", r.Codigo, len(body.Notices), recortar(r.Cuerpo))
	}
	n := body.Notices[0]
	id := p9Scalar(t, sc.DB, `SELECT id::text FROM public.owner_degradation_notices WHERE tenant_id = $1`, sc.Tenant)
	if n.ID != id || n.Reason != "ollama_down" || n.Via != "local" || n.Occurrences != 1 || n.Read || n.ReadAt != "" {
		t.Errorf("el aviso de la API = %+v, quería el %s: ollama_down, local, 1 ocurrencia, sin leer", n, id)
	}
	start, errStart := time.Parse(time.RFC3339, n.WindowStart)
	end, errEnd := time.Parse(time.RFC3339, n.WindowEnd)
	if errStart != nil || errEnd != nil || end.Sub(start) != 15*time.Minute {
		t.Errorf("la ventana del aviso de la API = %q a %q, quería 15 minutos", n.WindowStart, n.WindowEnd)
	}
}

// p4StaticFlowDuringOutage es la mitad de INV-10 que se ve desde fuera: con el modelo caído y un job
// en cola, otro cliente que escribe la palabra clave de un flujo estático recibe la bienvenida, el
// menú y la respuesta a su opción, igual que sin pipeline. Ese flujo no abre evento, así que tampoco
// abre ventana: ni un job más.
func p4StaticFlowDuringOutage(t *testing.T, sc *draftScene) {
	t.Helper()
	jobs := consultaEntero(t, sc.DB, `SELECT count(*) FROM public.intake_jobs WHERE tenant_id = $1`, sc.Tenant)
	sc.installMenu(t)
	from := p4BystanderPn + "@s.whatsapp.net"
	sc.Edge.sendSealedIncoming(t, sealedIncoming{From: from, WaID: draftWaID(p4BystanderPn, 0), Text: p3Keyword, FromPn: p4BystanderPn})
	sc.expectText(t, p4BystanderPn, p3Welcome)
	sc.expectText(t, p4BystanderPn, p3MenuPrompt)
	sc.Edge.sendSealedIncoming(t, sealedIncoming{From: from, WaID: draftWaID(p4BystanderPn, 1), Text: "1", FromPn: p4BystanderPn})
	sc.expectText(t, p4BystanderPn, p3SalesText)
	if n := consultaEntero(t, sc.DB, `SELECT count(*) FROM public.intake_jobs WHERE tenant_id = $1`, sc.Tenant); n != jobs {
		t.Errorf("un flujo sin evento abrió ventana: intake_jobs pasó de %d a %d filas", jobs, n)
	}
}

// p4DoorCase es un primer mensaje de un contacto nuevo y lo que hace con él el disparo `event_start`
// (palabra «presupuesto», match `contains`).
type p4DoorCase struct {
	name  string
	text  string
	opens bool // true: abre evento y ventana de captación; false: no casa el disparo
}

// p4DoorTable son los casos de la puerta, medidos contra el viejo. La comparación normaliza las dos
// partes igual: minúsculas, sin diacríticos, espacios (también U+00A0) colapsados.
var p4DoorTable = []p4DoorCase{
	{"mayúsculas", "PRESUPUESTO", true},
	{"con tilde donde no va", "quiero un presupuésto ya", true},
	{"rodeada de U+00A0, separador repetido y dígitos no ASCII", "necesito  un\u00a0presupuesto@@ ١٢٣", true},
	{"partida por un U+00A0", "pre\u00a0supuesto", false},
	{"partida por un separador repetido", "presu@@puesto", false},
	{"con un cero por la o", "presupuest0", false},
}

// p4Door recorre p4DoorTable con los plazos de la ventana retenidos: cada caso es un contacto nuevo.
// Si su primer mensaje casa el disparo, recibe la bienvenida y el menú del flujo, y ese mensaje abre
// una ventana (`aggregating`, con su referencia). Si no casa, recibe solo la bienvenida; para ver que
// de verdad no abrió nada, el mismo contacto manda después la palabra exacta —el servidor atiende los
// entrantes de una conversación en serie— y es ESE mensaje el que abre el evento y la ventana, con
// una sola referencia: la suya. El primero no está en ninguna.
func p4Door(t *testing.T, sc *draftScene) {
	holdDraftWindow(t, sc.DB, sc.Tenant)
	const withRef = `SELECT count(*) FROM public.intake_jobs WHERE tenant_id = $1 AND source_refs ? $2`
	for i, tc := range p4DoorTable {
		t.Run(tc.name, func(t *testing.T) {
			pn := fmt.Sprintf("5730044401%02d", i)
			from, first, second := pn+"@s.whatsapp.net", draftWaID(pn, 0), draftWaID(pn, 1)
			events := consultaEntero(t, sc.DB, `SELECT count(*) FROM public.conversation_events WHERE tenant_id = $1::uuid`, sc.Tenant)
			sc.Edge.sendSealedIncoming(t, sealedIncoming{From: from, WaID: first, Text: tc.text, FromPn: pn})
			sc.expectText(t, pn, p3Welcome)
			opener := first
			if !tc.opens {
				sc.Edge.sendSealedIncoming(t, sealedIncoming{From: from, WaID: second, Text: draftKeyword, FromPn: pn})
				opener = second
			}
			sc.expectText(t, pn, draftMenuPrompt)
			draftWaitJob(t, sc, opener, "aggregating|1", "la ventana del caso «"+tc.name+"»")
			if n := consultaEntero(t, sc.DB, withRef, sc.Tenant, first); (n == 1) != tc.opens {
				t.Errorf("el primer mensaje está en %d ventanas; abre=%v", n, tc.opens)
			}
			if n := consultaEntero(t, sc.DB, `SELECT count(*) FROM public.conversation_events WHERE tenant_id = $1::uuid`, sc.Tenant); n != events+1 {
				t.Errorf("conversation_events pasó de %d a %d, quería un evento más", events, n)
			}
			if strings.Contains(sc.S.Log(), tc.text) {
				t.Errorf("el log del servidor contiene el texto del cliente %q", tc.text)
			}
		})
	}
}
