//go:build integracion

package procesos

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// POST /api/v1/intakes/{id}/request-info: la dueña le manda al cliente SU pregunta y la solicitud
// queda en `needs_info`. El candado que pasa a aserción es internal/intakes/inv1_pedirinfo_ast_test.go:
// la pregunta sale por el POST de la dueña, una vez, y con su texto.

const (
	// p5AskedPn es el contacto del borrador sobre el que se pide información.
	p5AskedPn = "573005550002"
	// p5QuestionNeedle es una aguja irrepetible dentro de la pregunta: lo que se busca en el log.
	p5QuestionNeedle = "ZZP5PREGUNTA9K"
	// p5Question es la pregunta que escribe la dueña: espacios en los extremos, un U+00A0, un separador
	// repetido, dígitos no ASCII y un salto de línea final. Sale byte a byte.
	p5Question = "  ¿De 10 o 12 porciones? a@@b ١٢٣ " + p5QuestionNeedle + "\n"
)

// p5RequestInfo es el recorrido de «pedir más información» sobre un borrador recién nacido (sin
// líneas guardadas: preguntar no exige presupuesto): lo que la puerta rechaza sin escribir, la
// pregunta buena y lo que una solicitud en `needs_info` ya no admite.
func p5RequestInfo(t *testing.T, w *p5World) {
	sc := w.sc
	w.asked = createDraft(t, sc, p5AskedPn)
	before := p5Snapshot(t, sc.DB, sc.Tenant, w.asked)
	path := p5Path(w.asked, "request-info")

	const noQuestion = "question es obligatoria: es la pregunta que se le manda al cliente, y jamás sale sola"
	for name, body := range map[string]any{
		"sin la clave":           map[string]any{},
		"pregunta vacía":         map[string]string{"question": ""},
		"solo espacios y U+00A0": map[string]string{"question": "  \t\n"},
	} {
		p5ExpectError(t, w.write(t, http.MethodPost, path, body), http.StatusBadRequest, noQuestion, "pedir información con "+name)
	}
	for name, body := range map[string]any{
		"un cuerpo que no es un objeto":     "no soy un objeto",
		"una pregunta que no es una cadena": map[string]any{"question": []string{"¿?"}},
		"sin cuerpo":                        nil,
	} {
		p5ExpectError(t, w.write(t, http.MethodPost, path, body), http.StatusBadRequest, p5BadJSON, "pedir información con "+name)
	}
	for name, id := range p5BadIDs(t, w.asked) {
		r := w.write(t, http.MethodPost, p5Path(id, "request-info"), map[string]string{"question": p5Question})
		p5ExpectError(t, r, http.StatusNotFound, p5NotFound, "pedir información con un id "+name)
	}
	before = w.expectUntouched(t, before, w.asked, "tras las peticiones de información rechazadas")
	sc.expectNoPendingText(t, "tras las peticiones de información rechazadas")

	asked := p5AskQuestion(t, w, before)
	p5AfterQuestion(t, w, asked)
}

// p5AskQuestion manda la pregunta y afirma todo lo que puede tocar: el 200 con el detalle en
// `needs_info`; UN SendText al cliente con la pregunta byte a byte (sin plantilla, sin aviso
// genérico delante); en `intakes`, status y updated_at y ninguna otra columna; NINGUNA revisión nueva
// (preguntar no cambia el presupuesto); un `intake_info_requested` que cuenta la pregunta y no la
// lleva; y el log sin una palabra de ella. Devuelve la foto de después.
func p5AskQuestion(t *testing.T, w *p5World, before p5Snap) p5Snap {
	t.Helper()
	sc := w.sc
	d := p5DecodeDetail(t, w.write(t, http.MethodPost, p5Path(w.asked, "request-info"), map[string]string{"question": p5Question}),
		"POST …/request-info")
	if d.ID != w.asked || d.Status != "needs_info" || len(d.Items) != 0 || p5Kinds(d.Revisions) != "1:interpreted:system" {
		t.Errorf("pedir información contestó %s/%s con %d líneas y revisiones %q", d.ID, d.Status, len(d.Items), p5Kinds(d.Revisions))
	}
	if got := fmt.Sprint(d.AllowedTransitions); got != "[cancelled pending_approval]" {
		t.Errorf("allowed_transitions en needs_info = %s", got)
	}
	sc.expectText(t, p5AskedPn, p5Question)
	p9WaitLogLines(t, sc.S, p5MsgSent, map[string]string{"intake_id": w.asked, "accion": "request_info"}, 1)

	after := p5Snapshot(t, sc.DB, sc.Tenant, w.asked)
	p5ExpectIntakeChange(t, before, after, "pedir información", "status", "updated_at")
	if after.Intake["status"] != "needs_info" {
		t.Errorf("intakes.status tras pedir información = %v, quería needs_info", after.Intake["status"])
	}
	p5ExpectSame(t, before, after, "pedir información", "items", "revisions", "event", "jobs", "flow_state", "thread", "buyer", "outbox")
	if n := len(after.FlowEvents) - len(before.FlowEvents); n != 1 {
		t.Errorf("pedir información dejó %d flow_events nuevos del contacto, quería 1", n)
	}
	if got := p5LastInboxEvent(after); got != `intake_info_requested {"questions":1}` {
		t.Errorf("pedir información publicó %q", got)
	}
	if strings.Contains(sc.S.Log(), p5QuestionNeedle) {
		t.Errorf("el log del servidor lleva el texto de la pregunta de la dueña (%s)", p5QuestionNeedle)
	}
	if strings.Contains(after.raw, p5QuestionNeedle) {
		t.Errorf("la pregunta de la dueña quedó escrita en Postgres: va solo al hilo de WhatsApp del cliente")
	}
	if n := len(sc.Script.Calls(stageP5)); n != 0 {
		t.Errorf("pedir información provocó %d inferencias P5", n)
	}
	return after
}

// p5AfterQuestion afirma lo que una solicitud en `needs_info` rechaza: una segunda pregunta (un solo
// mensaje por pregunta), la aprobación y la edición. Nada de eso escribe ni le habla al cliente.
func p5AfterQuestion(t *testing.T, w *p5World, asked p5Snap) {
	t.Helper()
	sc := w.sc
	r := w.write(t, http.MethodPost, p5Path(w.asked, "request-info"), map[string]string{"question": "¿Y la entrega?"})
	if !p9ErrorIs(r, http.StatusUnprocessableEntity,
		`{"error":"invalid_transition","status":"needs_info","requested":"needs_info","allowed":["cancelled","pending_approval"]}`) {
		t.Errorf("preguntar dos veces: HTTP %d %s; quería 422 invalid_transition desde needs_info", r.Codigo, recortar(r.Cuerpo))
	}
	r = w.write(t, http.MethodPost, p5Path(w.asked, "approve"), map[string]string{"rendered_text": p5OwnerQuote})
	if !p9ErrorIs(r, http.StatusUnprocessableEntity, `{"error":"not_approvable","status":"needs_info","approvable_in":["pending_approval"]}`) {
		t.Errorf("aprobar en needs_info: HTTP %d %s; quería 422 not_approvable", r.Codigo, recortar(r.Cuerpo))
	}
	r = w.write(t, http.MethodPut, p5Path(w.asked, "items"), map[string]any{"items": p5Items()})
	if !p9ErrorIs(r, http.StatusUnprocessableEntity, `{"error":"not_editable","status":"needs_info","editable_in":["pending_approval"]}`) {
		t.Errorf("editar en needs_info: HTTP %d %s; quería 422 not_editable", r.Codigo, recortar(r.Cuerpo))
	}
	w.expectUntouched(t, asked, w.asked, "tras los intentos sobre una solicitud en needs_info")
	sc.expectNoPendingText(t, "tras los intentos sobre una solicitud en needs_info")
	if n := len(p9LogLines(sc.S, p5MsgSent, map[string]string{"intake_id": w.asked})); n != 1 {
		t.Errorf("el log tiene %d envíos al cliente de la solicitud en needs_info, quería 1", n)
	}
}
