//go:build integracion

package procesos

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// P5 · La bandeja de la dueña (diseno.md §4 · T9.18): lo que la dueña hace con un borrador que el
// pipeline dejó en `pending_approval`. Entra SIEMPRE por la API pública, con el token de la
// administradora, sobre borradores creados con createDraft (P4). El proceso está partido por tema:
//
//   - este fichero: el mundo del proceso, la foto de una solicitud (p5Snapshot) y el orden;
//   - p5_bandeja_edit_test.go: PUT …/items;
//   - p5_bandeja_approve_test.go: POST …/approve y TestP5_AprobarDosVecesUnSoloEfecto (INV-1);
//   - p5_bandeja_requestinfo_test.go: POST …/request-info;
//   - p5_bandeja_quote_test.go: POST …/quote-suggestion y su plazo propio (T-12);
//   - p5_bandeja_expiry_test.go: el plazo del presupuesto y la poda del literal;
//   - p5_bandeja_discard_test.go: POST /api/v1/intakes/discard;
//   - p5_bandeja_export_test.go: export, summary.json y la feature `intakes_export`.
//
// Se afirma lo que hace el binario viejo; el mismo test corre contra el nuevo sin distinguirlos.

const (
	// p5FlowInbox es el flow_id con el que la bandeja publica su telemetría en flow_events.
	p5FlowInbox = "_intake_inbox"
	// p5ItemsTotal es el total de las líneas de p5Items.
	p5ItemsTotal = 7370

	// Los mensajes del log del servidor que este proceso cuenta.
	p5MsgSent      = "notificación de cambio de estado enviada al cliente"
	p5MsgNoDeposit = "notificación: el tenant no tiene plantilla de seña (tenant_settings.deposit_template); " +
		"se le manda la cotización del dueño sola, sin instrucciones de pago"
	p5MsgReminder       = "recordatorio de plazo: el presupuesto lleva más del plazo esperando al dueño"
	p5MsgPruned         = "retención: literal de la revisión podado por TTL vencido"
	p5MsgQuoteMismatch  = "quotetext: el texto del modelo NO cuadra con las líneas (INV-2); sale el texto determinista"
	p5MsgQuoteIllegible = "quotetext: la salida del modelo no es un artefacto P5 legible; sale el texto determinista"
	p5MsgQuoteFailed    = "quotetext: el proveedor no redactó la cotización; sale el texto determinista"

	// p5NotFound es el cuerpo del 404 de toda la bandeja: una solicitud ajena, inexistente o con un id
	// que no es un UUID contestan lo mismo (INV-8).
	p5NotFound = "solicitud no encontrada"
	// p5BadJSON es el cuerpo del 400 de un cuerpo que no es el objeto esperado.
	p5BadJSON = "cuerpo JSON inválido"
)

// p5World es lo que comparten los subtests de un proceso de P5: el escenario de P4, el cliente con
// reintento del 429 (hallazgo 32) y lo que cada paso deja apuntado para el cierre.
type p5World struct {
	sc    *draftScene
	calls *p9World
	owner p9Caller

	// audit cuenta las filas de audit_events que las ESCRITURAS del proceso tienen que dejar:
	// «resource|código HTTP» → filas con action `intakes.write`.
	audit map[string]int

	first string // borrador que la dueña corrige y aprueba
	asked string // borrador sobre el que la dueña pide más información
	open  string // borrador que la dueña deja esperando: sugerencia, plazo, poda y descarte

	quote string // la cotización que salió al aprobar `first`: el ejemplo de la sugerencia
}

// p5NewWorld arranca el escenario de P4 para el proceso dado. Hay que llamarlo con el `t` del
// proceso (el Edge se ata a él).
func p5NewWorld(t *testing.T, proceso, slug string) *p5World {
	t.Helper()
	sc := draftScenario(t, proceso, slug)
	return &p5World{
		sc:    sc,
		calls: &p9World{root: t, audit: map[string]int{}},
		owner: p9Caller{client: sc.Pub, tenant: sc.Tenant},
		audit: map[string]int{},
	}
}

// read hace una LECTURA por la API pública (GET, y quote-suggestion, que se audita como lectura):
// reintenta el 429 y no deja fila de auditoría.
func (w *p5World) read(t *testing.T, method, path string, body any) respuesta {
	t.Helper()
	return w.calls.call(t, w.owner, "", method, path, body)
}

// write hace una ESCRITURA de la bandeja (`intakes.write`) y apunta la fila de audit_events que
// tiene que dejar: el recurso (`intake`, o `conversation_event` para la cancelación de un evento) y
// el código HTTP con el que contestó.
func (w *p5World) write(t *testing.T, method, path string, body any) respuesta {
	t.Helper()
	r := w.calls.call(t, w.owner, "", method, path, body)
	resource := "intake"
	if strings.Contains(path, "/conversation-events/") {
		resource = "conversation_event"
	}
	w.audit[fmt.Sprintf("%s|%d", resource, r.Codigo)]++
	return r
}

// p5Path es la ruta de una acción sobre una solicitud; el id va tal cual (los adversarios lo traen
// ya escapado).
func p5Path(id, action string) string {
	if action == "" {
		return "/api/v1/intakes/" + id
	}
	return "/api/v1/intakes/" + id + "/" + action
}

// p5Snap es LA FOTO de una solicitud en Postgres: su fila entera, sus líneas, sus revisiones, su
// evento, sus jobs, los flow_events de su contacto, el estado del flujo del contacto, cuántos turnos
// tiene el hilo del evento y cuántas filas hay en webhook_outbox. Es la marca de estado de P5
// (R9.5.c): se compara ENTERA, columna a columna, antes y después de cada operación.
type p5Snap struct {
	Intake     map[string]any   `json:"intake"`
	Items      []map[string]any `json:"items"`
	Revisions  []map[string]any `json:"revisions"`
	Event      map[string]any   `json:"event"`
	Jobs       []map[string]any `json:"jobs"`
	FlowEvents []map[string]any `json:"flow_events"`
	FlowState  []map[string]any `json:"flow_state"`
	Thread     int              `json:"thread"`
	Buyer      int              `json:"buyer"`
	Outbox     int              `json:"outbox"`

	raw string
}

// p5SnapshotQuery arma la foto en una sola consulta, para que sea un instante y no seis.
const p5SnapshotQuery = `
	WITH i AS (SELECT * FROM public.intakes WHERE id = $1::uuid AND tenant_id = $2)
	SELECT jsonb_build_object(
		'intake', (SELECT to_jsonb(i) FROM i),
		'items', (SELECT coalesce(jsonb_agg(to_jsonb(it) ORDER BY it.id), '[]'::jsonb)
			FROM public.intake_items it WHERE it.intake_id = $1::uuid),
		'revisions', (SELECT coalesce(jsonb_agg(to_jsonb(r) ORDER BY r.revision_no), '[]'::jsonb)
			FROM public.intake_revisions r WHERE r.intake_id = $1::uuid),
		'event', (SELECT to_jsonb(e) FROM public.conversation_events e, i WHERE e.id = i.event_id),
		'jobs', (SELECT coalesce(jsonb_agg(to_jsonb(j) ORDER BY j.created_at, j.id), '[]'::jsonb)
			FROM public.intake_jobs j WHERE j.intake_id = $1::uuid),
		'flow_events', (SELECT coalesce(jsonb_agg(to_jsonb(f) ORDER BY f.id), '[]'::jsonb)
			FROM public.flow_events f, i WHERE f.tenant_id = $2 AND f.contact_id = i.contact_id),
		'flow_state', (SELECT coalesce(jsonb_agg(to_jsonb(s) ORDER BY s.session_id), '[]'::jsonb)
			FROM public.flow_state s, i WHERE s.tenant_id::text = $2 AND s.contact_id::text = i.contact_id),
		'thread', (SELECT count(*) FROM public.conversation_event_messages m, i WHERE m.event_id = i.event_id),
		'buyer', (SELECT count(*) FROM public.intake_buyer_data b WHERE b.intake_id = $1::uuid),
		'outbox', (SELECT count(*) FROM public.webhook_outbox)
	)::text`

// p5Snapshot toma la foto de la solicitud id de la empresa. Falla (t.Fatalf) si la consulta falla,
// la solicitud no existe o la foto no decodifica.
func p5Snapshot(t *testing.T, db *sql.DB, tenant, id string) p5Snap {
	t.Helper()
	snap := p5Snap{raw: p9Scalar(t, db, p5SnapshotQuery, id, tenant)}
	if err := json.Unmarshal([]byte(snap.raw), &snap); err != nil {
		t.Fatalf("la foto de la solicitud %s no decodifica: %v\n%s", id, err, snap.raw)
	}
	if snap.Intake == nil {
		t.Fatalf("la solicitud %s no existe en la empresa %s", id, tenant)
	}
	return snap
}

// p5Changed devuelve, ordenadas, las claves cuyo valor difiere entre dos filas (o que solo están en
// una de las dos). Es lo que permite afirmar «esta operación tocó ESTAS columnas y ninguna más».
func p5Changed(before, after map[string]any) []string {
	var out []string
	for k, v := range before {
		if w, ok := after[k]; !ok || fmt.Sprint(v) != fmt.Sprint(w) {
			out = append(out, k)
		}
	}
	for k := range after {
		if _, ok := before[k]; !ok {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

// p5Sections dice qué partes de la foto difieren entre dos fotos, para el mensaje de fallo.
func p5Sections(before, after p5Snap) []string {
	var parts map[string]json.RawMessage
	var others map[string]json.RawMessage
	if json.Unmarshal([]byte(before.raw), &parts) != nil || json.Unmarshal([]byte(after.raw), &others) != nil {
		return []string{"(fotos ilegibles)"}
	}
	var out []string
	for k, v := range parts {
		if string(v) != string(others[k]) {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

// expectUntouched afirma que la solicitud está EXACTAMENTE como en la foto before: ni una columna
// de ninguna de sus tablas cambió. Devuelve la foto nueva. Falla con t.Errorf.
func (w *p5World) expectUntouched(t *testing.T, before p5Snap, id, what string) p5Snap {
	t.Helper()
	after := p5Snapshot(t, w.sc.DB, w.sc.Tenant, id)
	if after.raw != before.raw {
		t.Errorf("%s: la solicitud %s cambió y no debía: difieren %v (columnas de intakes: %v)",
			what, id, p5Sections(before, after), p5Changed(before.Intake, after.Intake))
	}
	return after
}

// p5ExpectIntakeChange afirma que, entre las dos fotos, la fila de `intakes` cambió EXACTAMENTE en
// las columnas dadas (ordenadas) y en ninguna otra.
func p5ExpectIntakeChange(t *testing.T, before, after p5Snap, what string, columns ...string) {
	t.Helper()
	if got := p5Changed(before.Intake, after.Intake); strings.Join(got, ",") != strings.Join(columns, ",") {
		t.Errorf("%s: en intakes cambiaron las columnas %v, quería exactamente %v", what, got, columns)
	}
}

// p5ExpectSame afirma que las partes nombradas de la foto son idénticas en las dos.
func p5ExpectSame(t *testing.T, before, after p5Snap, what string, sections ...string) {
	t.Helper()
	changed := p5Sections(before, after)
	for _, s := range sections {
		if slices.Contains(changed, s) {
			t.Errorf("%s: cambió %q de la solicitud y no debía (difieren %v)", what, s, changed)
		}
	}
}

// p5Detail es el cuerpo de GET /api/v1/intakes/{id} y de las acciones que lo devuelven.
type p5Detail struct {
	ID                 string       `json:"id"`
	Status             string       `json:"status"`
	Total              float64      `json:"total"`
	Overdue            bool         `json:"overdue"`
	UpdatedAt          string       `json:"updated_at"`
	Items              []p5Item     `json:"items"`
	Revisions          []p5Revision `json:"revisions"`
	AllowedTransitions []string     `json:"allowed_transitions"`
}

// p5Item es una línea de cliente al wire.
type p5Item struct {
	SKU           string  `json:"sku"`
	Label         string  `json:"label"`
	Customization string  `json:"customization"`
	Qty           int     `json:"qty"`
	UnitPrice     float64 `json:"unit_price"`
}

// p5Revision es una revisión al wire. LiteralPrunedAt viene «» si la clave no está.
type p5Revision struct {
	RevisionNo      int             `json:"revision_no"`
	Kind            string          `json:"kind"`
	Payload         json.RawMessage `json:"payload"`
	RenderedText    string          `json:"rendered_text"`
	CreatedBy       string          `json:"created_by"`
	LiteralPrunedAt string          `json:"literal_pruned_at"`
}

// p5DecodeDetail exige un 200 y decodifica el detalle. Falla (t.Fatalf) si no lo es.
func p5DecodeDetail(t *testing.T, r respuesta, what string) p5Detail {
	t.Helper()
	if r.Codigo != http.StatusOK {
		t.Fatalf("%s: HTTP %d, quería 200\ncuerpo: %s", what, r.Codigo, recortar(r.Cuerpo))
	}
	var d p5Detail
	r.JSON(t, &d)
	return d
}

// p5Kinds resume las revisiones de un detalle: «nº:clase:autor», en orden.
func p5Kinds(revs []p5Revision) string {
	out := make([]string, 0, len(revs))
	for _, r := range revs {
		out = append(out, fmt.Sprintf("%d:%s:%s", r.RevisionNo, r.Kind, r.CreatedBy))
	}
	return strings.Join(out, " ")
}

// p5ExpectError exige el código y el campo `error` EXACTO de una respuesta de rechazo.
func p5ExpectError(t *testing.T, r respuesta, code int, text, what string) {
	t.Helper()
	var got struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(r.Cuerpo, &got); err != nil || r.Codigo != code || got.Error != text {
		t.Errorf("%s: HTTP %d %s; quería %d con error %q", what, r.Codigo, recortar(r.Cuerpo), code, text)
	}
}

// p5BadIDs son los ids adversarios de solicitud, ya escapados para la ruta: uno desconocido, uno
// con dígitos no ASCII, uno con separador repetido y el id bueno con un espacio U+00A0 delante.
func p5BadIDs(t *testing.T, good string) map[string]string {
	t.Helper()
	return map[string]string{
		"desconocido":      uuidAleatorio(t),
		"dígitos no ASCII": "%D9%A1" + good[1:],
		"separador":        "a@@b",
		"U+00A0 delante":   "%C2%A0" + good,
	}
}

// TestP5_OwnerInbox es el proceso P5. Los subtests comparten UN servidor y corren EN ORDEN: cada uno
// parte de lo que dejó el anterior. La aprobación repetida y simultánea (INV-1) tiene su propio
// test, TestP5_AprobarDosVecesUnSoloEfecto.
//
//   - edicion: PUT …/items sobre un borrador → revisión `corrected` e `intake_line_corrected`.
//   - sugerencia_sin_historial: sin cotizaciones aprobadas la sugerencia es determinista y no gasta
//     inferencia.
//   - aprobacion: las precondiciones de POST …/approve y una aprobación.
//   - pedir_info: POST …/request-info → el cliente recibe la pregunta; `intake_info_requested`.
//   - sugerencia_con_plazo: quote-suggestion con el modelo tardando 12 s responde, pasado el
//     WriteTimeout global de 10 s.
//   - vencimiento: un presupuesto envejecido se marca al leerlo y el recordatorio sale una vez.
//   - poda: el literal del cliente se destruye al leer una revisión vencida, y la lectura lo dice.
//   - descarte: POST /api/v1/intakes/discard.
//   - export: GET …/export y summary.json, y la empresa sin `intakes_export`.
func TestP5_OwnerInbox(t *testing.T) {
	t.Parallel()
	w := p5NewWorld(t, "p5", "p5-bandeja")

	t.Run("edicion", func(t *testing.T) { p5Editing(t, w) })
	t.Run("sugerencia_sin_historial", func(t *testing.T) { p5QuoteWithoutHistory(t, w) })
	t.Run("aprobacion", func(t *testing.T) { p5Approval(t, w) })
	t.Run("pedir_info", func(t *testing.T) { p5RequestInfo(t, w) })
	t.Run("sugerencia_con_plazo", func(t *testing.T) { p5SlowQuote(t, w) })
	t.Run("vencimiento", func(t *testing.T) { p5Expiry(t, w) })
	t.Run("poda", func(t *testing.T) { p5Pruning(t, w) })
	t.Run("descarte", func(t *testing.T) { p5Discard(t, w) })
	t.Run("export", func(t *testing.T) { p5Export(t, w) })
	t.Run("cierre", func(t *testing.T) { w.closing(t, nil) })
}

// closing es el cierre de un proceso de P5, ANTES de parar el servidor: cada escritura dejó su fila
// de audit_events (y los 429 reintentados, ninguna), la bandeja no publicó más telemetría que la
// suya, el guion atendió todo lo que se le pidió, el núcleo del Edge no anotó errores, ningún texto
// quedó sin leer y el log del servidor no trae más ERROR que los esperados.
func (w *p5World) closing(t *testing.T, errors map[string]int) {
	sc := w.sc
	const audit = `SELECT coalesce(string_agg(k || '=' || n::text, ' ' ORDER BY k), '') FROM (
		SELECT resource || '|' || (meta->>'status') AS k, count(*) AS n FROM public.audit_events
		WHERE tenant_id = $1::uuid AND action = 'intakes.write' GROUP BY 1) x`
	keys := make([]string, 0, len(w.audit))
	for k, n := range w.audit {
		keys = append(keys, fmt.Sprintf("%s=%d", k, n))
	}
	slices.Sort(keys)
	edgeEsperarValor(t, sc.DB, strings.Join(keys, " "), "audit_events de las escrituras de la bandeja", audit, sc.Tenant)
	if w.calls.throttled > 0 {
		t.Logf("P5 reintentó %d respuestas 429 del límite de la API pública (20 rps, ráfaga de 40)", w.calls.throttled)
	}

	const names = `SELECT coalesce(string_agg(DISTINCT name, ',' ORDER BY name), '') FROM public.flow_events
		WHERE tenant_id = $1 AND flow_id = $2`
	if got := p9Scalar(t, sc.DB, names, sc.Tenant, p5FlowInbox); !p5OnlyInboxEvents(got) {
		t.Errorf("la bandeja publicó en flow_events %q: solo puede publicar correcciones, aprobaciones y preguntas", got)
	}
	if problems := sc.Script.Problems(); len(problems) != 0 {
		t.Errorf("el guion no supo atender: %v", problems)
	}
	if errs := sc.Edge.Errores(); len(errs) != 0 {
		t.Errorf("errores del núcleo del Edge: %v", errs)
	}
	sc.expectNoPendingText(t, "al cerrar el proceso")
	edgeSinErrores(t, sc.S, errors)
}

// p5OnlyInboxEvents dice si una lista de nombres de flow_events separados por comas trae solo los
// tres que la bandeja publica. El plazo vencido NO publica nada: nada muere por tiempo.
func p5OnlyInboxEvents(names string) bool {
	if names == "" {
		return true
	}
	for _, name := range strings.Split(names, ",") {
		if !slices.Contains([]string{"intake_approved", "intake_info_requested", "intake_line_corrected"}, name) {
			return false
		}
	}
	return true
}
