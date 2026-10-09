//go:build integracion

package procesos

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// P8 · Re-análisis (diseno.md §4 · T9.21): la dueña mira un borrador que la máquina interpretó y pide
// que lo vuelva a leer DESDE EL ORIGEN. `POST /api/v1/intakes/{id}/reanalyze` no espera al modelo:
// abre un job en `intake_jobs` con el contexto de la migración 0080 y contesta; el worker lo reclama,
// vuelve a pedir P2, P3 y P4 al Edge sobre el hilo cifrado del evento y cuelga UNA revisión más de la
// misma solicitud, firmada `owner`, con su fila `intake_reanalyzed` en flow_events. La solicitud no
// cambia de estado y al cliente no le llega nada.
//
// El proceso está partido por tema: este fichero trae el recorrido feliz (abrir y consumar un
// re-análisis, con todo lo que deja) y el cierre; el texto pegado va en p8_reanalisis_pasted_test.go,
// la puerta (modelo caído, segundo re-análisis, estados de la solicitud) en
// p8_reanalisis_door_test.go, los adversarios en p8_reanalisis_adversarial_test.go y el mundo y las
// ayudas en p8_reanalisis_helpers_test.go. Los borradores salen de createDraft, el helper de P4.

// TestP8_Reanalysis es el proceso P8. Los subtests comparten UN servidor y corren EN ORDEN (ninguno
// es paralelo): cada uno parte de lo que dejó el anterior. Se afirma lo que hace el binario viejo; el
// mismo test corre contra el nuevo sin distinguirlos.
//
//   - reanalisis: el recorrido de diseno.md: job de re-análisis → el guion responde otra cosa →
//     revisión 2 y `intake_reanalyzed`.
//   - texto_pegado: la transcripción de la dueña entra cifrada como una fila más del hilo, una sola
//     vez, y el origen pasa a `both`.
//   - puerta: con el modelo caído el job queda vivo (degradación) y un segundo re-análisis se
//     rechaza; al volver el modelo, la revisión sale.
//   - adversarios: ids, cuerpos y credenciales que la puerta rechaza sin escribir nada.
//   - aprobada / rechazada: qué hace el viejo con una solicitud que ya no está por aprobar.
func TestP8_Reanalysis(t *testing.T) {
	t.Parallel()
	w := p8NewWorld(t)
	w.main = w.newTarget(t, p8MainPn)

	t.Run("reanalisis", w.happyPath)
	t.Run("texto_pegado", w.pastedText)
	t.Run("puerta", w.door)
	t.Run("adversarios", w.adversarial)
	t.Run("aprobada", w.approved)
	t.Run("rechazada", w.rejected)
	t.Run("auditoria", w.auditTrail)
	t.Run("cierre", w.closing)
}

// p8Pending es un re-análisis aceptado y todavía sin consumar: el job abierto y las marcas tomadas
// ANTES de pedirlo.
type p8Pending struct {
	tgt    *p8Target
	jobID  string
	from   int    // la revisión vigente al pedirlo
	source string // event_thread | both

	intakeRow string            // la fila de la solicitud, todas sus columnas
	oldRevs   string            // las revisiones anteriores, todas sus columnas
	oldJobs   string            // los jobs anteriores, todas sus columnas
	static    map[string]string // las tablas que no puede tocar
	calls     int               // inferencias que el Edge llevaba atendidas
	events    int               // filas de flow_events de la empresa
}

const (
	// p8IntakeRow lee TODAS las columnas de la solicitud; p8OldRevs y p8OldJobs, la huella de todas
	// las columnas de sus revisiones hasta una dada y de los jobs de su evento salvo uno.
	p8IntakeRow = `SELECT row_to_json(i)::text FROM public.intakes i WHERE id = $1::uuid`
	p8OldRevs   = `SELECT count(*)::text || ':' || md5(coalesce(string_agg(md5(row_to_json(r)::text), '' ORDER BY revision_no), ''))
		FROM public.intake_revisions r WHERE intake_id = $1::uuid AND revision_no <= $2`
	p8OldJobs = `SELECT count(*)::text || ':' || md5(coalesce(string_agg(md5(row_to_json(j)::text), '' ORDER BY created_at), ''))
		FROM public.intake_jobs j WHERE event_id = $1::uuid AND id::text <> $2`
	p8EventCount = `SELECT count(*) FROM public.flow_events WHERE tenant_id = $1`
)

// open pide el re-análisis de tgt con el cuerpo dado y afirma el acuse: 200 con la MISMA solicitud,
// la revisión que tocará (la vigente más una), la vía efectiva `local` —sin fila en tenant_llm— y
// `processing`. Antes cambia la respuesta de P4 del guion para que la revisión nueva sea distinguible
// (la tercera línea llevará tantos paquetes como el número de la revisión) y toma las marcas. Falla
// (t.Fatalf) si la puerta no acepta.
func (w *p8World) open(t *testing.T, tgt *p8Target, body any, source string) p8Pending {
	t.Helper()
	sc := w.sc
	sc.Script.Respond(stageP4, p8ReplyP4(tgt.rev+1))
	p := p8Pending{
		tgt: tgt, from: tgt.rev, source: source,
		intakeRow: p9Scalar(t, sc.DB, p8IntakeRow, tgt.id),
		oldRevs:   p9Scalar(t, sc.DB, p8OldRevs, tgt.id, tgt.rev),
		oldJobs:   p9Scalar(t, sc.DB, p8OldJobs, tgt.eventID, ""),
		static:    p8Snapshot(t, sc, p8Static),
		calls:     len(sc.Script.Calls("")),
		events:    consultaEntero(t, sc.DB, p8EventCount, sc.Tenant),
	}
	r := w.post(t, w.owner, tgt.id, body)
	var ack p8Ack
	if r.Codigo != http.StatusOK {
		t.Fatalf("POST reanalyze de %s: HTTP %d, quería 200\ncuerpo: %s", tgt.id, r.Codigo, recortar(r.Cuerpo))
	}
	r.JSON(t, &ack)
	if ack.IntakeID != tgt.id || ack.RevisionNo != tgt.rev+1 || ack.Via != "local" || ack.Status != "processing" || !identidadUUID.MatchString(ack.JobID) {
		t.Fatalf("el acuse del re-análisis = %+v; quería la solicitud %s, la revisión %d, vía local y processing", ack, tgt.id, tgt.rev+1)
	}
	p.jobID = ack.JobID
	// El servidor escribe la línea antes de responder, pero el arnés lee su salida por otro lado: con la
	// máquina cargada el 200 llega antes que la línea al búfer. Se espera a que esté y luego se cuenta.
	opened := map[string]string{
		"job_id": p.jobID, "intake_id": tgt.id, "event_id": tgt.eventID, "tenant_id": sc.Tenant, "via": "local", "source": source,
	}
	p9WaitLogLines(t, sc.S, p8MsgOpened, opened, 1)
	if n := len(p9LogLines(sc.S, p8MsgOpened, opened)); n != 1 {
		t.Errorf("el log trae %d líneas %q de este job con vía local y origen %s, quería 1", n, p8MsgOpened, source)
	}
	return p
}

// settle espera a que el job del re-análisis termine y afirma TODO lo que deja: el job `done` con su
// contexto de la 0080 y sin sobre; UNA revisión más, `interpreted` y de `owner`, distinguible de la
// anterior; las revisiones y los jobs anteriores, la solicitud y las tablas vecinas sin un byte
// cambiado; las dos filas de telemetría; las inferencias que el Edge atendió (wantCalls, en orden); y
// ningún mensaje al cliente. attempts son los intentos que el job cobró (0 sin caídas).
func (w *p8World) settle(t *testing.T, p p8Pending, attempts int, wantCalls []string) {
	t.Helper()
	p8WaitJob(t, w.sc, p.jobID, fmt.Sprintf("done/draft/%d", attempts))
	p.tgt.rev = p.from + 1
	w.checkJob(t, p, attempts)
	w.checkRevisions(t, p)
	w.checkUntouched(t, p)
	w.checkEvents(t, p)
	w.checkCalls(t, p, wantCalls)
	w.checkDetail(t, p)
	w.sc.expectNoPendingText(t, fmt.Sprintf("tras el re-análisis que escribió la revisión %d", p.tgt.rev))
}

// p8FullRun son las inferencias de un re-análisis sin caídas: las mismas cinco de un borrador.
var p8FullRun = []string{"p2", "p3", "p3", "p3", "p4"}

// checkJob afirma la fila del job terminado, columna a columna: `done` en `draft`, sin error, atado
// desde el INSERT a la solicitud; las TRES piezas del sobre vaciadas (INV-13); ninguna referencia de
// mensaje (no hubo entrante); las cuatro columnas de la 0080; la misma ventana (sesión, contacto,
// evento) que la solicitud; el message_ts HEREDADO del primer job del evento; y los cinco artefactos,
// con el `draft` apuntando a la revisión recién escrita.
func (w *p8World) checkJob(t *testing.T, p p8Pending, attempts int) {
	t.Helper()
	const job = `SELECT status || '|' || coalesce(stage, '-') || '|' || attempts::text || '|' || coalesce(error, '-') || '|' || intake_id::text
			|| '|' || (source_text_enc IS NULL)::text || (source_text_dek IS NULL)::text || (source_text_kek_id IS NULL)::text
			|| '|' || source_refs::text
			|| '|' || coalesce(requested_by, '-') || '|' || coalesce(reanalysis_via, '-') || '|' || coalesce(reanalysis_source, '-')
			|| '|' || coalesce(reanalyzed_from::text, '-')
			|| '|' || session_id || '|' || contact_id || '|' || event_id::text || '|' || intake_type
			|| '|' || (message_ts = (SELECT message_ts FROM public.intake_jobs WHERE id = $2::uuid))::text
			|| '|' || (SELECT string_agg(k || ':' || coalesce(artifacts->k->>'version', '?'), ',' ORDER BY k) FROM jsonb_object_keys(artifacts) k)
			|| '|' || (artifacts->'draft'->>'intake_id') || '|' || (artifacts->'draft'->>'revision_no') || '|' || (artifacts->'draft'->>'lines')
		FROM public.intake_jobs WHERE id = $1::uuid AND tenant_id = $3`
	tgt := p.tgt
	want := fmt.Sprintf("done|draft|%d|-|%s|truetruetrue|[]|owner|local|%s|%d|%s|%s|%s|order|true|draft:1,match:1,p2:1,p3:1,p4:1|%s|%d|4",
		attempts, tgt.id, p.source, p.from, w.sc.Edge.SessionID, tgt.contactID, tgt.eventID, tgt.id, tgt.rev)
	if got := p9Scalar(t, w.sc.DB, job, p.jobID, tgt.firstJob, w.sc.Tenant); got != want {
		t.Errorf("el job del re-análisis = %q, quería %q", got, want)
	}
	if got := p9Scalar(t, w.sc.DB, p8OldJobs, tgt.eventID, p.jobID); got != p.oldJobs {
		t.Errorf("los jobs anteriores del evento cambiaron (filas:huella %s → %s)", p.oldJobs, got)
	}
}

// checkRevisions afirma las revisiones: las anteriores, sin un byte cambiado; y UNA más, la
// siguiente, `interpreted`, firmada `owner`, con su literal cifrado aparte (y fuera del payload, como
// las evidencias), el rastro del análisis —vía, origen y de qué revisión sale— y la tercera línea con
// la cantidad que el guion contestó ESTA vez. La base de fechas es la del mensaje original: la misma
// fecha de entrega que la revisión 1.
func (w *p8World) checkRevisions(t *testing.T, p p8Pending) {
	t.Helper()
	tgt := p.tgt
	if got := p9Scalar(t, w.sc.DB, p8OldRevs, tgt.id, p.from); got != p.oldRevs {
		t.Errorf("las revisiones anteriores cambiaron (filas:huella %s → %s)", p.oldRevs, got)
	}
	if n := consultaEntero(t, w.sc.DB, `SELECT count(*) FROM public.intake_revisions WHERE intake_id = $1::uuid`, tgt.id); n != tgt.rev {
		t.Errorf("intake_revisions tiene %d filas de la solicitud, quería %d", n, tgt.rev)
	}
	const revision = `SELECT kind || '|' || created_by || '|' || (rendered_text IS NULL)::text
			|| '|' || (length(literal_enc) > 0 AND length(literal_dek) > 0 AND literal_kek_id <> '' AND literal_pruned_at IS NULL)::text
			|| '|' || (payload ? 'source_text')::text || '|' || (payload::text LIKE '%evidence%')::text
			|| '|' || (payload->'analysis')::text
			|| '|' || jsonb_array_length(payload->'lines')::text || '|' || (payload->'lines'->2->>'sku') || '|' || (payload->'lines'->2->>'qty')
			|| '|' || (payload->>'delivery_date' = (SELECT r1.payload->>'delivery_date' FROM public.intake_revisions r1
				WHERE r1.intake_id = r.intake_id AND r1.revision_no = 1))::text
			|| '|' || (payload->>'message_ts' = (SELECT r1.payload->>'message_ts' FROM public.intake_revisions r1
				WHERE r1.intake_id = r.intake_id AND r1.revision_no = 1))::text
		FROM public.intake_revisions r WHERE intake_id = $1::uuid AND revision_no = $2`
	want := fmt.Sprintf(`interpreted|owner|true|true|false|false|{"source": "%s", "provider": "local", "reanalyzed_from": %d}|4|TEQ-30|%d|true|true`,
		p.source, p.from, tgt.rev)
	if got := p9Scalar(t, w.sc.DB, revision, tgt.id, tgt.rev); got != want {
		t.Errorf("la revisión %d = %q, quería %q", tgt.rev, got, want)
	}
}

// checkUntouched afirma lo que el re-análisis NO toca: la solicitud (todas sus columnas: ni el estado,
// ni el importe, ni updated_at), sus líneas, el evento y el outbox del puente CRM; y el hilo, que solo
// crece con una transcripción pegada. El `draft` lo dice en el log: cuelga la revisión de la solicitud
// que ya existía.
func (w *p8World) checkUntouched(t *testing.T, p p8Pending) {
	t.Helper()
	if got := p9Scalar(t, w.sc.DB, p8IntakeRow, p.tgt.id); got != p.intakeRow {
		t.Errorf("la solicitud cambió con el re-análisis:\nantes   %s\ndespués %s", p.intakeRow, got)
	}
	p8Unchanged(t, w.sc, p.static, fmt.Sprintf("tras el re-análisis que escribió la revisión %d", p.tgt.rev))
	if got, want := p8Thread(t, w.sc, p.tgt.eventID), p.tgt.wantThread(); got != want {
		t.Errorf("el hilo del evento = %q, quería %q", got, want)
	}
	if n := len(p9LogLines(w.sc.S, p8MsgAttached, map[string]string{"job_id": p.jobID, "intake_id": p.tgt.id})); n != 1 {
		t.Errorf("el log trae %d líneas %q de este job, quería 1", n, p8MsgAttached)
	}
}

// checkEvents afirma la telemetría: DOS filas más de la empresa, en este orden. La del borrador, con
// los mismos contadores que la primera pasada más `requested_by` (lo que permite sacar del KPI un
// `elapsed_ms` que aquí mide desde el mensaje ORIGINAL), y `intake_reanalyzed` con la vía, el origen
// y las dos revisiones. Ni una palabra del cliente.
func (w *p8World) checkEvents(t *testing.T, p p8Pending) {
	t.Helper()
	sc, tgt := w.sc, p.tgt
	if n := consultaEntero(t, sc.DB, p8EventCount, sc.Tenant); n != p.events+2 {
		t.Errorf("flow_events pasó de %d a %d filas, quería dos más", p.events, n)
	}
	const events = `SELECT coalesce(string_agg(flow_id || '|' || flow_version::text || '|' || kind || '|' || name || '|' || contact_id
			|| '|' || (payload - 'elapsed_ms')::text, E'\n' ORDER BY id), '')
		FROM (SELECT * FROM public.flow_events WHERE tenant_id = $1 ORDER BY id DESC LIMIT 2) x`
	prefix := p4DraftFlow + "|1|event|"
	want := prefix + `intake_draft_created|` + tgt.contactID + `|{"lines": 4, "matched": 3, "unmatched": 0, "requested_by": "owner"}` + "\n" +
		prefix + `intake_reanalyzed|` + tgt.contactID + fmt.Sprintf(`|{"via": "local", "source": "%s", "to_rev": %d, "from_rev": %d}`, p.source, tgt.rev, p.from)
	if got := p9Scalar(t, sc.DB, events, sc.Tenant); got != want {
		t.Errorf("las dos últimas filas de flow_events =\n%s\nquería\n%s", got, want)
	}
	const elapsed = `SELECT ((e.payload->>'elapsed_ms')::bigint = (j.artifacts->'draft'->>'elapsed_ms')::bigint)::text
			|| '|' || ((j.artifacts->'draft'->>'elapsed_ms')::bigint > (f.artifacts->'draft'->>'elapsed_ms')::bigint)::text
		FROM public.flow_events e, public.intake_jobs j, public.intake_jobs f
		WHERE e.id = (SELECT max(id) FROM public.flow_events WHERE tenant_id = $1 AND name = 'intake_draft_created')
		  AND j.id = $2::uuid AND f.id = $3::uuid`
	if got := p9Scalar(t, sc.DB, elapsed, sc.Tenant, p.jobID, tgt.firstJob); got != "true|true" {
		t.Errorf("elapsed_ms de la telemetría = %q; quería el del artefacto del job, y mayor que el del primer borrador (mide desde el mensaje original)", got)
	}
}

// checkCalls afirma lo que el Edge atendió por este re-análisis: las etapas de want, en orden, todas
// con el rótulo `lote` y el techo de su etapa; y que cada prompt lleva el hilo literal del evento.
func (w *p8World) checkCalls(t *testing.T, p p8Pending, want []string) {
	t.Helper()
	calls := w.sc.Script.Calls("")[p.calls:]
	got := make([]string, 0, len(calls))
	for _, c := range calls {
		got = append(got, string(c.Stage))
		if c.Class != "lote" || c.MaxOutputTokens != scriptCeilings[c.Stage] {
			t.Errorf("una inferencia de %s del re-análisis viajó con class %q y techo %d", c.Stage, c.Class, c.MaxOutputTokens)
		}
		for _, text := range draftBurst() {
			if !strings.Contains(c.Prompt, "cliente: "+text+"\n") {
				t.Errorf("el prompt de %s del re-análisis no lleva el mensaje del cliente del hilo del evento", c.Stage)
			}
		}
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("las inferencias del re-análisis = %v, quería %v", got, want)
	}
}

// checkDetail afirma lo que ve la dueña por GET /api/v1/intakes/{id}: una revisión más, la última
// `interpreted` y de `owner` con su rastro, la cantidad nueva en la tercera línea y —descifrados en
// el borde— el hilo literal y las evidencias. El estado de la solicitud es el de su fila.
func (w *p8World) checkDetail(t *testing.T, p p8Pending) {
	t.Helper()
	tgt := p.tgt
	status, revs := w.detail(t, tgt.id)
	if want := p9Scalar(t, w.sc.DB, `SELECT status FROM public.intakes WHERE id = $1::uuid`, tgt.id); status != want {
		t.Errorf("el detalle dice estado %q y la fila %q", status, want)
	}
	if len(revs) != tgt.rev {
		t.Fatalf("el detalle trae %d revisiones, quería %d", len(revs), tgt.rev)
	}
	last := revs[len(revs)-1]
	a := last.Payload.Analysis
	if last.RevisionNo != tgt.rev || last.Kind != "interpreted" || last.CreatedBy != "owner" ||
		a.Source != p.source || a.Provider != "local" || a.ReanalyzedFrom == nil || *a.ReanalyzedFrom != p.from {
		t.Errorf("la última revisión del detalle = %d/%s/%s con análisis %+v; quería la %d, interpreted, de owner, %s por local desde la %d",
			last.RevisionNo, last.Kind, last.CreatedBy, a, tgt.rev, p.source, p.from)
	}
	if len(last.Payload.Lines) != 4 || last.Payload.Lines[2].Qty != tgt.rev || last.Payload.Lines[2].Evidence != scriptEvidenceTequenos {
		t.Errorf("la tercera línea de la última revisión no trae la cantidad %d con su evidencia", tgt.rev)
	}
	if !strings.Contains(last.Payload.SourceText, "cliente: "+draftBurst()[0]+"\n") {
		t.Errorf("el source_text de la última revisión no trae el hilo literal del evento")
	}
}

// happyPath es el recorrido de diseno.md §4 P8 sobre el borrador de createDraft: el re-análisis sin
// cuerpo útil (`{}`: «regenera según el origen») abre su job, el guion contesta otra cantidad y sale
// la revisión 2 con `intake_reanalyzed`. La revisión 1 sigue diciendo lo que decía.
func (w *p8World) happyPath(t *testing.T) {
	p := w.open(t, w.main, map[string]any{}, "event_thread")
	w.settle(t, p, 0, p8FullRun)
	_, revs := w.detail(t, w.main.id)
	if first := revs[0]; first.RevisionNo != 1 || first.CreatedBy != "system" || first.Payload.Lines[2].Qty != 1 ||
		first.Payload.Analysis.Provider != "" || first.Payload.Analysis.ReanalyzedFrom != nil {
		t.Errorf("la revisión 1 del detalle cambió: %d de %s, cantidad %d, análisis %+v",
			first.RevisionNo, first.CreatedBy, first.Payload.Lines[2].Qty, first.Payload.Analysis)
	}
	if n := consultaEntero(t, w.sc.DB, `SELECT count(*) FROM public.owner_degradation_notices`); n != 0 {
		t.Errorf("owner_degradation_notices tiene %d filas tras un re-análisis sin caídas", n)
	}
}

// auditTrail compara audit_events con lo que el proceso apuntó llamada a llamada: toda petición que
// llega a un handler de la bandeja deja su fila `intakes.write` (`success` por debajo de 400,
// `failure` desde 400) bajo la empresa del token; las que corta el middleware (401, 403 de permiso)
// no dejan ninguna. La fila se escribe después de responder: la última se espera sondeando.
func (w *p8World) auditTrail(t *testing.T) {
	const query = `SELECT count(*)::text FROM public.audit_events WHERE tenant_id = $1::uuid AND action = $2 AND result = $3`
	total := 0
	for key, n := range w.calls.audit {
		parts := strings.Split(key, "|")
		edgeEsperarValor(t, w.sc.DB, fmt.Sprint(n), "audit_events de "+key, query, parts[0], parts[1], parts[2])
		total += n
	}
	if got := consultaEntero(t, w.sc.DB, `SELECT count(*) FROM public.audit_events WHERE action = $1`, p8Action); got != total {
		t.Errorf("audit_events tiene %d filas %s, quería %d (%v)", got, p8Action, total, w.calls.audit)
	}
	if got := consultaEntero(t, w.sc.DB, `SELECT count(*) FROM public.audit_events
		WHERE action = $1 AND (actor = '' OR resource <> 'intake' OR NOT (meta ? 'status'))`, p8Action); got != 0 {
		t.Errorf("hay %d filas de auditoría de la bandeja sin actor, con otro recurso o sin meta.status", got)
	}
}

// closing es el cierre del proceso, ANTES de parar el servidor: la solicitud principal acabó con sus
// seis revisiones y en el estado en que nació; el guion atendió todo lo que se le pidió; el Edge no
// recibió calentamientos ni anotó errores ni tiene textos sin leer; el único aviso de degradación es
// el de la puerta; el literal del cliente y la transcripción de la dueña no están en claro en
// ninguna tabla ni en el log; y el log del servidor no trae ERROR.
func (w *p8World) closing(t *testing.T) {
	sc := w.sc
	if got := p9Scalar(t, sc.DB, `SELECT status || '|' || (SELECT count(*) FROM public.intake_revisions r WHERE r.intake_id = i.id)::text
		FROM public.intakes i WHERE id = $1::uuid`, w.main.id); got != fmt.Sprintf("pending_approval|%d", w.main.rev) {
		t.Errorf("la solicitud principal acabó en %q, quería pending_approval con %d revisiones", got, w.main.rev)
	}
	if problems := sc.Script.Problems(); len(problems) != 0 {
		t.Errorf("el guion no supo atender: %v", problems)
	}
	if n := len(sc.Script.Calls(stageP1)) + len(sc.Script.Calls(stageP5)); n != 0 {
		t.Errorf("el Edge recibió %d inferencias de P1 o P5: el re-análisis solo pide P2, P3 y P4", n)
	}
	for _, req := range sc.Edge.Inferencias() {
		if req.GetWarmup() {
			t.Errorf("el Edge recibió un calentamiento sin catálogo de intenciones publicado")
		}
	}
	if errs := sc.Edge.Errores(); len(errs) != 0 {
		t.Errorf("errores del núcleo del Edge: %v", errs)
	}
	w.noLiteralInClear(t)
	sc.expectNoPendingText(t, "al cerrar el proceso")
	edgeSinErrores(t, sc.S, nil)
	t.Logf("peticiones reintentadas por 429 (límite por credencial de la API pública): %d", w.calls.throttled)
}
