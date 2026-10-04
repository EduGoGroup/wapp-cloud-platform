//go:build integracion

package procesos

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// POST /api/v1/intakes/{id}/approve: la dueña manda su cotización, el cliente la recibe por WhatsApp
// y la solicitud queda `confirmed` con su revisión `approved`. Y 🔴 INV-1: la aprobación tiene UNA
// sola puerta y UN solo efecto.

const (
	// p5OwnerQuote es la cotización que escribe la dueña. Lleva emoji, saltos de línea, un U+00A0 y
	// espacios al final a propósito: «lo que se guarda es lo que se envía» solo se demuestra con un
	// texto que un recorte o una normalización estropearían.
	p5OwnerQuote = "Hola Ambar 👋\nTus tortas y los tequeños: $7.370\nEntrega el miércoles.  "

	// p5DepositTemplate es la plantilla de seña de la empresa de INV-1 y p5DepositRendered lo que el
	// servidor adjunta a la cotización, con el marcador {total} relleno.
	p5DepositTemplate = "Para reservar abona el 50 % de {total} a la cuenta 123-4."
	p5DepositRendered = "Para reservar abona el 50 % de $7370.00 a la cuenta 123-4."

	// p5ParallelApprovals es cuántas aprobaciones simultáneas de la misma solicitud lanza INV-1.
	p5ParallelApprovals = 8
)

// p5Approval es la aprobación dentro del proceso: primero lo que la puerta rechaza sin escribir
// (texto ausente, ids adversarios) y después UNA aprobación, en una empresa sin plantilla de seña: la
// cotización sale sola, tal cual. Deja en w.quote lo que salió, que es el ejemplo de la sugerencia.
func p5Approval(t *testing.T, w *p5World) {
	sc := w.sc
	before := p5Snapshot(t, sc.DB, sc.Tenant, w.first)
	const noText = "rendered_text es obligatorio: es el texto de la cotización que se le manda al cliente"
	for name, body := range map[string]any{
		"sin la clave":               map[string]any{},
		"texto vacío":                map[string]string{"rendered_text": ""},
		"solo espacios y U+00A0":     map[string]string{"rendered_text": "  \t\n"},
		"cuerpo que no es un objeto": "no soy un objeto",
		"texto que no es una cadena": map[string]any{"rendered_text": 7370},
		"sin cuerpo":                 nil,
	} {
		want := noText
		if name == "cuerpo que no es un objeto" || name == "texto que no es una cadena" || name == "sin cuerpo" {
			want = p5BadJSON
		}
		r := w.write(t, http.MethodPost, p5Path(w.first, "approve"), body)
		p5ExpectError(t, r, http.StatusBadRequest, want, "aprobar con "+name)
	}
	for name, id := range p5BadIDs(t, w.first) {
		r := w.write(t, http.MethodPost, p5Path(id, "approve"), map[string]string{"rendered_text": p5OwnerQuote})
		p5ExpectError(t, r, http.StatusNotFound, p5NotFound, "aprobar con un id "+name)
	}
	before = w.expectUntouched(t, before, w.first, "tras las aprobaciones rechazadas")
	sc.expectNoPendingText(t, "tras las aprobaciones rechazadas")

	r := w.write(t, http.MethodPost, p5Path(w.first, "approve"), map[string]string{"rendered_text": p5OwnerQuote})
	w.expectApproved(t, before, r, w.first, p5FirstPn, p5OwnerQuote)
	w.quote = p5OwnerQuote
	if n := len(p9LogLines(sc.S, p5MsgNoDeposit, map[string]string{"intake_id": w.first})); n != 1 {
		t.Errorf("el log tiene %d avisos de «sin plantilla de seña» para la aprobación, quería 1", n)
	}
}

// expectApproved afirma TODO lo que una aprobación puede tocar (R9.5.c), partiendo de la foto de
// antes: el 200 con el detalle; el SendText al cliente con el texto exacto; en `intakes`, que cambian
// status y updated_at y NINGUNA otra columna (aprobar no cobra: ni seña, ni total, ni marca de plazo,
// ni reflejo del CRM); una revisión más, `approved` de `owner`, cuyo rendered_text es byte a byte lo
// enviado; un `intake_approved` más; y las líneas, el evento, los jobs, el estado del flujo, el hilo
// y webhook_outbox intactos. Devuelve la foto de después.
func (w *p5World) expectApproved(t *testing.T, before p5Snap, r respuesta, id, contact, text string) p5Snap {
	t.Helper()
	sc := w.sc
	revNo := len(before.Revisions) + 1
	p5ExpectApprovedResponse(t, r, id, text, revNo)
	sc.expectText(t, contact, text)
	p9WaitLogLines(t, sc.S, p5MsgSent, map[string]string{"intake_id": id, "accion": "approve"}, 1)

	after := p5Snapshot(t, sc.DB, sc.Tenant, id)
	p5ExpectIntakeChange(t, before, after, "aprobar", "status", "updated_at")
	p5ExpectSame(t, before, after, "aprobar", "items", "event", "jobs", "flow_state", "thread", "buyer", "outbox")
	if got := fmt.Sprintf("%v|%v|%d", after.Intake["status"], after.Event["status"], after.Outbox); got != "confirmed|open|0" {
		t.Errorf("tras aprobar, solicitud|evento|webhook_outbox = %s; quería confirmed|open|0 (aprobar no es cobrar ni cerrar el evento)", got)
	}
	p5ExpectApprovedRevision(t, before, after, text)
	p5ExpectApprovedEvent(t, before, after, revNo)
	return after
}

// p5ExpectApprovedResponse afirma el 200 de la aprobación: el detalle en `confirmed`, con sus líneas,
// la revisión `approved` de `owner` la última —con el texto exacto— y los destinos de `confirmed`.
func p5ExpectApprovedResponse(t *testing.T, r respuesta, id, text string, revNo int) {
	t.Helper()
	d := p5DecodeDetail(t, r, "POST …/approve")
	want := fmt.Sprintf("%s|confirmed|%d|7|%d", id, p5ItemsTotal, revNo)
	if got := fmt.Sprintf("%s|%s|%v|%d|%d", d.ID, d.Status, d.Total, len(d.Items), len(d.Revisions)); got != want || len(d.Revisions) == 0 {
		t.Fatalf("la aprobación contestó id|estado|total|líneas|revisiones = %s, quería %s", got, want)
	}
	last := d.Revisions[len(d.Revisions)-1]
	if got := fmt.Sprintf("%d:%s:%s", last.RevisionNo, last.Kind, last.CreatedBy); got != fmt.Sprintf("%d:approved:owner", revNo) || last.RenderedText != text {
		t.Errorf("la última revisión del detalle = %s con texto %q; quería la %d, approved, de owner, con %q", got, last.RenderedText, revNo, text)
	}
	if got := fmt.Sprint(d.AllowedTransitions); got != "[cancelled deposit_requested pending_approval settled]" {
		t.Errorf("allowed_transitions tras aprobar = %s", got)
	}
}

// p5ExpectApprovedEvent afirma la telemetría de la aprobación: UN flow_event más del contacto,
// `intake_approved`, de la bandeja, con el nº de la revisión y el tiempo desde el borrador, y nada más.
func p5ExpectApprovedEvent(t *testing.T, before, after p5Snap, revNo int) {
	t.Helper()
	if n := len(after.FlowEvents) - len(before.FlowEvents); n != 1 {
		t.Fatalf("aprobar dejó %d flow_events nuevos del contacto, quería 1", n)
	}
	event := after.FlowEvents[len(after.FlowEvents)-1]
	payload, isObject := event["payload"].(map[string]any)
	elapsed, isNumber := payload["elapsed_from_draft_ms"].(float64)
	got := fmt.Sprintf("%v|%v|%v|%v|%d", event["name"], event["flow_id"], event["kind"], payload["rev"], len(payload))
	if want := fmt.Sprintf("intake_approved|%s|event|%d|2", p5FlowInbox, revNo); got != want {
		t.Errorf("el flow_event de la aprobación = %s, quería %s", got, want)
	}
	if !isObject || !isNumber || elapsed < 0 {
		t.Errorf("el flow_event de la aprobación no trae elapsed_from_draft_ms como número no negativo: %v", event["payload"])
	}
}

// p5ExpectApprovedRevision afirma la revisión que deja la aprobación: UNA más, la última, `approved`
// de `owner`, con el texto exacto, sin sobre de literal, y con la foto de las líneas (total incluido);
// las anteriores, idénticas.
func p5ExpectApprovedRevision(t *testing.T, before, after p5Snap, text string) {
	t.Helper()
	if len(after.Revisions) != len(before.Revisions)+1 {
		t.Fatalf("aprobar dejó %d revisiones, quería %d", len(after.Revisions), len(before.Revisions)+1)
	}
	for i := range before.Revisions {
		if got := p5Changed(before.Revisions[i], after.Revisions[i]); len(got) != 0 {
			t.Errorf("aprobar tocó la revisión %d: columnas %v", i+1, got)
		}
	}
	rev := after.Revisions[len(after.Revisions)-1]
	got := fmt.Sprintf("%v|%v|%v|%v|%v", rev["kind"], rev["created_by"], rev["revision_no"], rev["literal_enc"], rev["literal_pruned_at"])
	if want := fmt.Sprintf("approved|owner|%d|<nil>|<nil>", len(after.Revisions)); got != want || rev["rendered_text"] != text {
		t.Errorf("la revisión de la aprobación = %s con texto %q; quería %s con %q", got, rev["rendered_text"], want, text)
	}
	payload, err := json.Marshal(rev["payload"])
	if err != nil {
		t.Fatalf("el payload de la revisión approved no serializa: %v", err)
	}
	var photo struct {
		Version int      `json:"version"`
		Total   float64  `json:"total"`
		Items   []p5Item `json:"items"`
	}
	if err := json.Unmarshal(payload, &photo); err != nil {
		t.Fatalf("el payload de la revisión approved no decodifica: %v\n%s", err, payload)
	}
	skus := make([]string, 0, len(photo.Items))
	for _, it := range photo.Items {
		skus = append(skus, it.SKU)
	}
	want := fmt.Sprintf("1|%d|TORTA-CHOC#10,TORTA-VAIN,TEQ-30,PAN@@1,PAN\u00a02,PAN-١٢٣,REGALO", p5ItemsTotal)
	if got := fmt.Sprintf("%d|%v|%s", photo.Version, photo.Total, strings.Join(skus, ",")); got != want {
		t.Errorf("la foto de la revisión approved = %s, quería %s", got, want)
	}
}

// p5SetDepositTemplate deja la plantilla de seña de la empresa (tenant_settings.deposit_template),
// que el servidor adjunta a la cotización al aprobar. Es un upsert que no toca el resto de ajustes.
// Falla (t.Fatalf) si la empresa no es un UUID o el SQL falla.
//
// 🔧 POR QUÉ NO HAY PUERTA HTTP: ninguna ruta de /api/v1 ni de /admin escribe deposit_template; hoy
// la pone el operador por SQL (la misma situación que ventanaInmediata).
func p5SetDepositTemplate(t *testing.T, db *sql.DB, tenant, template string) {
	t.Helper()
	if err := exigirUUID("la empresa", tenant); err != nil {
		t.Fatalf("p5SetDepositTemplate: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), topeFixture)
	defer cancel()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.tenant_settings (tenant_id, deposit_template) VALUES ($1, $2)
		ON CONFLICT (tenant_id) DO UPDATE SET deposit_template = $2`, tenant, template); err != nil {
		t.Fatalf("p5SetDepositTemplate: escribir tenant_settings de %s: %v", tenant, err)
	}
}

// TestP5_AprobarDosVecesUnSoloEfecto es 🔴 INV-1 como conducta (R9.6.c; el candado que pasa a
// aserción es internal/intakes/inv1_aprobar_ast_test.go): la aprobación tiene una sola puerta —el
// POST de la dueña— y un solo efecto. En su propio servidor, con una empresa CON plantilla de seña:
//
//   - nadie_aprueba_solo: un borrador con sus líneas guardadas sigue `pending_approval` y el cliente no
//     ha recibido nada mientras la dueña no aprueba.
//   - dos_veces: la primera aprobación manda UN mensaje (la cotización de la dueña con la plantilla de
//     seña adjunta) y deja UNA revisión `approved` y UN `intake_approved`; la segunda y la tercera se
//     rechazan y no cambian una sola columna de ninguna tabla, ni mandan nada. Tampoco las otras
//     puertas tocan una solicitud ya aprobada.
//   - simultaneas: p5ParallelApprovals aprobaciones a la vez, cada una con su texto → gana UNA; lo
//     guardado y lo enviado son el texto de la que ganó.
func TestP5_AprobarDosVecesUnSoloEfecto(t *testing.T) {
	t.Parallel()
	w := p5NewWorld(t, "p5_inv1", "p5-inv1")
	p5SetDepositTemplate(t, w.sc.DB, w.sc.Tenant, p5DepositTemplate)

	var before p5Snap
	t.Run("nadie_aprueba_solo", func(t *testing.T) {
		w.first = createDraft(t, w.sc, "573005560001")
		w.putItems(t, w.first, false)
		before = p5Snapshot(t, w.sc.DB, w.sc.Tenant, w.first)
		if before.Intake["status"] != "pending_approval" || len(before.Revisions) != 2 {
			t.Errorf("el borrador con sus líneas está %v con %d revisiones; quería pending_approval y 2", before.Intake["status"], len(before.Revisions))
		}
		w.sc.expectNoPendingText(t, "antes de que la dueña apruebe")
	})
	t.Run("dos_veces", func(t *testing.T) { p5ApproveTwice(t, w, before) })
	t.Run("simultaneas", func(t *testing.T) { p5ApproveInParallel(t, w) })
	t.Run("cierre", func(t *testing.T) {
		const approved = `SELECT count(*)::text FROM public.flow_events WHERE tenant_id = $1 AND name = 'intake_approved'`
		if got := p9Scalar(t, w.sc.DB, approved, w.sc.Tenant); got != "2" {
			t.Errorf("flow_events tiene %s intake_approved de la empresa, quería 2 (uno por solicitud)", got)
		}
		if n := len(p9LogLines(w.sc.S, p5MsgSent, map[string]string{"accion": "approve"})); n != 2 {
			t.Errorf("el log tiene %d envíos de cotización, quería 2 (uno por solicitud)", n)
		}
		w.closing(t, nil)
	})
}

// p5ApproveTwice aprueba, y vuelve a aprobar: un solo efecto.
func p5ApproveTwice(t *testing.T, w *p5World, before p5Snap) {
	sc := w.sc
	sent := p5OwnerQuote + "\n\n" + p5DepositRendered
	r := w.write(t, http.MethodPost, p5Path(w.first, "approve"), map[string]string{"rendered_text": p5OwnerQuote})
	approved := w.expectApproved(t, before, r, w.first, "573005560001", sent)

	for _, text := range []string{p5OwnerQuote, "Otra cotización: $1"} {
		r = w.write(t, http.MethodPost, p5Path(w.first, "approve"), map[string]string{"rendered_text": text})
		var rejected struct {
			Error        string   `json:"error"`
			Status       string   `json:"status"`
			ApprovableIn []string `json:"approvable_in"`
		}
		r.JSON(t, &rejected)
		if r.Codigo != http.StatusUnprocessableEntity || rejected.Error != "not_approvable" || rejected.Status != "confirmed" ||
			fmt.Sprint(rejected.ApprovableIn) != "[pending_approval]" {
			t.Errorf("aprobar una solicitud ya aprobada: HTTP %d %s; quería 422 not_approvable desde confirmed", r.Codigo, recortar(r.Cuerpo))
		}
	}
	approved = w.expectUntouched(t, approved, w.first, "INV-1: tras aprobar otra vez una solicitud ya aprobada")

	// Las otras puertas de la bandeja tampoco tocan lo aprobado.
	r = w.write(t, http.MethodPut, p5Path(w.first, "items"), map[string]any{"items": p5Items()})
	if !p9ErrorIs(r, http.StatusUnprocessableEntity, `"error":"not_editable","status":"confirmed","editable_in":["pending_approval"]`) {
		t.Errorf("editar una solicitud aprobada: HTTP %d %s; quería 422 not_editable", r.Codigo, recortar(r.Cuerpo))
	}
	r = w.write(t, http.MethodPost, p5Path(w.first, "request-info"), map[string]string{"question": "¿Sigue en pie?"})
	if !p9ErrorIs(r, http.StatusUnprocessableEntity, `"error":"invalid_transition","status":"confirmed","requested":"needs_info"`) {
		t.Errorf("pedir información de una solicitud aprobada: HTTP %d %s; quería 422 invalid_transition", r.Codigo, recortar(r.Cuerpo))
	}
	r = w.write(t, http.MethodPost, "/api/v1/intakes/discard", map[string]any{"intake_ids": []string{w.first}})
	if !p9ErrorIs(r, http.StatusOK, `{"discarded":[],"skipped":[{"intake_id":"`+w.first+`","reason":"not_open"}]}`) {
		t.Errorf("descartar una solicitud aprobada: HTTP %d %s; quería 200 con not_open", r.Codigo, recortar(r.Cuerpo))
	}
	w.expectUntouched(t, approved, w.first, "INV-1: tras las otras puertas sobre una solicitud ya aprobada")
	sc.expectNoPendingText(t, "INV-1: tras los intentos sobre una solicitud ya aprobada")
	if n := len(p9LogLines(sc.S, p5MsgSent, map[string]string{"intake_id": w.first})); n != 1 {
		t.Errorf("INV-1: el log tiene %d envíos al cliente de la solicitud, quería 1", n)
	}
}

// p5ApproveInParallel lanza p5ParallelApprovals aprobaciones simultáneas de OTRA solicitud, cada una
// con un texto distinto, y afirma que gana UNA: un 200 (el resto, 409 o 422), una revisión `approved`,
// un `intake_approved` y un SendText, y que lo guardado y lo enviado son el texto de la que ganó.
func p5ApproveInParallel(t *testing.T, w *p5World) {
	sc := w.sc
	const contact = "573005560002"
	id := createDraft(t, sc, contact)
	w.putItems(t, id, false)
	before := p5Snapshot(t, sc.DB, sc.Tenant, id)

	texts := make([]string, p5ParallelApprovals)
	bodies := make([][]byte, p5ParallelApprovals)
	for i := range texts {
		texts[i] = fmt.Sprintf("Cotización nº %d de la dueña. Total: $7.370", i)
		raw, err := json.Marshal(map[string]string{"rendered_text": texts[i]})
		if err != nil {
			t.Fatalf("serializar la aprobación %d: %v", i, err)
		}
		bodies[i] = raw
	}
	responses := make([]respuesta, p5ParallelApprovals)
	errs := make([]error, p5ParallelApprovals)
	var group sync.WaitGroup
	start := make(chan struct{})
	for i := range bodies {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			responses[i], errs[i] = sc.Pub.despachar(t.Context(), http.MethodPost, p5Path(id, "approve"), bodies[i])
		}()
	}
	close(start)
	group.Wait()

	var winner respuesta
	winnerText, winners := "", 0
	for i, r := range responses {
		if errs[i] != nil {
			t.Fatalf("la aprobación simultánea %d no llegó: %v", i, errs[i])
		}
		w.audit[fmt.Sprintf("intake|%d", r.Codigo)]++
		switch r.Codigo {
		case http.StatusOK:
			winner, winnerText = r, texts[i]
			winners++
		case http.StatusConflict, http.StatusUnprocessableEntity:
		default:
			t.Errorf("la aprobación simultánea %d contestó HTTP %d %s; quería 200, 409 o 422", i, r.Codigo, recortar(r.Cuerpo))
		}
	}
	if winners != 1 {
		t.Fatalf("INV-1: de %d aprobaciones simultáneas ganaron %d, quería exactamente 1", p5ParallelApprovals, winners)
	}
	after := w.expectApproved(t, before, winner, id, contact, winnerText+"\n\n"+p5DepositRendered)
	sc.expectNoPendingText(t, "INV-1: tras las aprobaciones simultáneas")
	if n := len(p9LogLines(sc.S, p5MsgSent, map[string]string{"intake_id": id})); n != 1 {
		t.Errorf("INV-1: el log tiene %d envíos al cliente de la solicitud aprobada a la vez, quería 1", n)
	}
	w.expectUntouched(t, after, id, "INV-1: releída tras las aprobaciones simultáneas")
}
