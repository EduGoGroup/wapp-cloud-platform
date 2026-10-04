//go:build integracion

package procesos

import (
	"bytes"
	"encoding/json"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"
)

// La IDA del puente: una revisión de una solicitud que escribe la dueña (PUT …/items, POST …/approve) se
// encola en webhook_outbox y el worker la entrega firmada al CRM falso. Re-expresa
// internal/flujos/runtime/webhook_sink_integration_test.go (el gate), payload_purge_integration_test.go
// (la purga) y crmpush/push_test.go + contrato_ast_test.go (los campos), sobre lo RECIBIDO por el doble.

const (
	// p6Quote es la cotización que la dueña manda al aprobar: el cliente la recibe byte a byte.
	p6Quote = "Tu presupuesto\u00a0: 2 tortas@@chocolate y tequeños ١٢. Total $4691"

	// p6OutboxRow resume TODAS las columnas mutables de una fila de webhook_outbox (el ciclo de vida de
	// una entrega): estado, intentos, último error, si hay claim vigente, si el payload está vaciado,
	// el verbo y la empresa.
	p6OutboxRow = `SELECT status || '|' || attempts::text || '|' || coalesce(last_error, 'sin error') || '|' ||
		CASE WHEN claimed_at IS NULL THEN 'sin claim' ELSE 'con claim' END || '|' ||
		CASE WHEN payload = '{}'::jsonb THEN 'payload vacío' ELSE 'con payload' END || '|' || kind || '|' || tenant_id
		FROM public.webhook_outbox WHERE id = $1::bigint`

	// p6BridgeFailure es el motivo que el worker deja en last_error cuando el puente contesta 500.
	p6BridgeFailure = "respuesta 500 del puente"
)

// p6Line es una línea de solicitud tal como la manda la dueña (PUT …/items) y tal como el contrato la
// entrega al puente: son los mismos cinco campos.
type p6Line struct {
	SKU           string  `json:"sku"`
	Label         string  `json:"label"`
	Customization string  `json:"customization"`
	Qty           int     `json:"qty"`
	UnitPrice     float64 `json:"unit_price"`
}

// p6ApprovedLines son las líneas con las que la dueña deja la solicitud que aprueba, tal como las
// MANDA. La personalización de la primera lleva un separador repetido, un espacio U+00A0 y dígitos no
// ASCII.
func p6ApprovedLines() []p6Line {
	return []p6Line{
		{SKU: "TORTA-CHOC", Label: "Torta de chocolate", Customization: "sin@@nueces" + p6NBSP + "١٢", Qty: 2, UnitPrice: 2100.5},
		{SKU: "TEQ-30", Label: "Tequeños congelados", Customization: "", Qty: 1, UnitPrice: 490},
	}
}

// p6ApprovedLinesStored son esas líneas tal como quedan GUARDADAS y como llegan al CRM: la
// personalización se sanea en origen y el U+00A0 pasa a ser un espacio corriente; el separador repetido
// y los dígitos no ASCII se quedan como vinieron.
func p6ApprovedLinesStored() []p6Line {
	lines := p6ApprovedLines()
	lines[0].Customization = "sin@@nueces ١٢"
	return lines
}

// p6DeadLines son las líneas de la solicitud cuya entrega se agota.
func p6DeadLines() []p6Line {
	return []p6Line{{SKU: "PAN@@1", Label: "Pan de masa madre", Customization: "", Qty: 3, UnitPrice: 120}}
}

// p6Variables son las variables de empresa del proceso: el worker las fotografía al ENTREGAR. wApp no
// interpreta claves ni valores, así que los adversarios viajan tal cual.
func p6Variables() map[string]string {
	return map[string]string{"moneda": "Bs", "tasa@@dia": "٣٦,٥٠", "pie\u00a0de nota": "Gracias\u00a0por su compra"}
}

// putItems manda las líneas de una solicitud por PUT /api/v1/intakes/{id}/items y exige el 200 con la
// solicitud aún por aprobar. Cada PUT escribe una revisión `corrected`, que es lo que se empuja al CRM.
func (w *p6World) putItems(t *testing.T, intakeID string, lines []p6Line) {
	t.Helper()
	r := w.call(t, w.admin, "", http.MethodPut, "/api/v1/intakes/"+intakeID+"/items", map[string]any{"items": lines})
	if got := p6Fields(t, r); r.Codigo != http.StatusOK || got["status"] != "pending_approval" {
		t.Fatalf("PUT intakes/%s/items: HTTP %d, estado %v; quería 200 en pending_approval\ncuerpo: %s",
			intakeID, r.Codigo, got["status"], recortar(r.Cuerpo))
	}
}

// p6ForIntake es el filtro de las entregas del CRM falso de una solicitud.
func p6ForIntake(intakeID string) func(crmFakeDelivery) bool {
	return func(d crmFakeDelivery) bool { return d.Text("intake_id") == intakeID }
}

// pushGateClosed: sin puente ACTIVO, una revisión de la dueña no se encola. Las tres formas de no
// tenerlo: sin fila de integración, con el puente apagado, y encendido pero con los eventos en `local`.
// El empuje ocurre dentro de la petición, así que tras el 200 la cola vacía es definitiva.
func (w *p6World) pushGateClosed(t *testing.T) {
	w.approved = createDraft(t, w.sc, p6PnApproved)
	// El borrador que deja el pipeline tampoco se empuja, con o sin puente: solo salen las revisiones
	// posteriores, las que escribe la dueña (y el re-análisis, que es de P8).
	steps := []struct {
		name   string
		config any
	}{
		{"sin fila de integración", nil},
		{"con el puente apagado", p6Bridge(w.endpoint(), w.secret, false)},
		{"encendido con events_adapter local", map[string]any{"events_adapter": "local", "endpoint_url": w.endpoint(), "enabled": true}},
	}
	for _, step := range steps {
		if step.config != nil {
			if r := w.call(t, w.admin, p6ActWrite, http.MethodPut, p6PathIntegration, step.config); r.Codigo != http.StatusOK {
				t.Fatalf("PUT integrations (%s): HTTP %d %s", step.name, r.Codigo, recortar(r.Cuerpo))
			}
		}
		w.putItems(t, w.approved, p6DeadLines())
		p6WantMark(t, w.sc, "tras una revisión "+step.name, "sin filas", p6OutboxMark)
	}
	if r := w.call(t, w.admin, p6ActWrite, http.MethodDelete, p6PathIntegration, nil); r.Codigo != http.StatusNoContent {
		t.Fatalf("DELETE integrations: HTTP %d %s", r.Codigo, recortar(r.Cuerpo))
	}
	if n := len(w.crm.Deliveries()); n != 0 {
		t.Errorf("el CRM falso recibió %d entregas sin puente activo", n)
	}
	// T-10: el contador del worker no existe hasta su primer incremento.
	if got := p3Counter(t, w.sc.S, p6MetricDeliveries, p6MetricLabel, "delivered"); got != 0 {
		t.Errorf("%s{status=delivered} = %v sin ninguna entrega", p6MetricDeliveries, got)
	}
}

// pushFirstAttemptFails: el CRM contesta 500 a la entrega de una solicitud. La fila vuelve a `pending`
// con un intento contado, el motivo y el siguiente intento a 30 s ± 20 %, y CONSERVA su payload —la
// plantilla, sin los tres campos que el worker completa al entregar—. El resumen de la cola lo enseña.
func (w *p6World) pushFirstAttemptFails(t *testing.T) {
	w.dead = createDraft(t, w.sc, p6PnDead)
	dead := w.dead
	w.crm.RespondWith(func(d crmFakeDelivery) int {
		if d.Text("intake_id") == dead {
			return http.StatusInternalServerError
		}
		return http.StatusOK
	})
	w.putItems(t, w.dead, p6DeadLines())

	first := w.crm.Wait(t, p6Timeout, "el primer intento de la entrega que el CRM rechaza", 1, p6ForIntake(w.dead))[0]
	want := "pending|1|" + p6BridgeFailure + "|sin claim|con payload|intake.push|" + w.sc.Tenant
	p6WaitScalar(t, w.sc, want, "la fila de la entrega tras el primer 500", p6OutboxRow, first.ID)

	// El backoff del primer fallo es 30 s con ±20 % de jitter, contado desde el fallo.
	next, err := strconv.ParseFloat(p9Scalar(t, w.sc.DB,
		`SELECT extract(epoch FROM next_attempt_at)::text FROM public.webhook_outbox WHERE id = $1::bigint`, first.ID), 64)
	if err != nil {
		t.Fatalf("leer next_attempt_at: %v", err)
	}
	if wait := next - float64(first.At.UnixMilli())/1000; wait < 22 || wait > 38 {
		t.Errorf("el reintento quedó a %.1f s del primer intento; quería 30 s ± 20 %% (24–36 s)", wait)
	}
	// La plantilla encolada no congela buyer_data, variables ni customer_note (PII y coste: los pone el
	// worker al entregar), y sí lleva lo demás.
	if got := p9Scalar(t, w.sc.DB, `SELECT (payload ?| array['buyer_data','variables','customer_note'])::text || '|' ||
		(payload ?& array['contract_version','verb','tenant','contact','intake_id','lifecycle_status','revision_no','items','total','timestamp'])::text
		FROM public.webhook_outbox WHERE id = $1::bigint`, first.ID); got != "false|true" {
		t.Errorf("la plantilla encolada: (lleva lo que completa el worker | lleva los campos estables) = %s, quería false|true", got)
	}

	w.metrics["failed"] = 1
	p3WaitCounter(t, w.sc.S, p6MetricDeliveries, p6MetricLabel, "failed", 1)

	r := w.call(t, w.admin, "", http.MethodGet, p6PathOutbox, nil)
	oldest := p9Scalar(t, w.sc.DB, `SELECT to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
		FROM public.webhook_outbox WHERE id = $1::bigint`, first.ID)
	wantQueue := map[string]any{"pending": 1.0, "delivering": 0.0, "delivered": 0.0, "dead": 0.0, "oldest_pending_at": oldest}
	if got := p6Fields(t, r); r.Codigo != http.StatusOK || !reflect.DeepEqual(got, wantQueue) {
		t.Errorf("GET integrations/outbox con una entrega en reintento: HTTP %d %v, quería %v", r.Codigo, got, wantQueue)
	}
}

// pushDelivered: la dueña corrige las líneas y aprueba. Cada revisión llega al CRM falso firmada, en
// ventana y válida contra el schema publicado; la fila queda `delivered` con el payload VACIADO
// (migración 0050), y aprobar dos veces no empuja dos veces.
func (w *p6World) pushDelivered(t *testing.T) {
	r := w.call(t, w.admin, "", http.MethodPut, p6PathVariables, map[string]any{"variables": p6Variables()})
	if r.Codigo != http.StatusOK {
		t.Fatalf("PUT tenant-variables: HTTP %d %s", r.Codigo, recortar(r.Cuerpo))
	}
	w.putItems(t, w.approved, p6ApprovedLines())
	w.crm.Wait(t, p6Timeout, "la revisión corregida", 2, p6ForIntake(w.approved))

	approve := "/api/v1/intakes/" + w.approved + "/approve"
	r = w.call(t, w.admin, "", http.MethodPost, approve, map[string]string{"rendered_text": p6Quote})
	if got := p6Fields(t, r); r.Codigo != http.StatusOK || got["status"] != "confirmed" {
		t.Fatalf("POST approve: HTTP %d, estado %v; quería 200 en confirmed\ncuerpo: %s", r.Codigo, got["status"], recortar(r.Cuerpo))
	}
	w.sc.expectText(t, p6PnApproved, p6Quote)
	got := w.crm.Wait(t, p6Timeout, "la revisión aprobada", 3, p6ForIntake(w.approved))[1:3]

	// Aprobar otra vez no es una transición: 422, y ni una fila ni una entrega más.
	r = w.call(t, w.admin, "", http.MethodPost, approve, map[string]string{"rendered_text": p6Quote})
	p6WantError(t, "segundo approve", r, http.StatusUnprocessableEntity, "not_approvable")
	w.reanalyzeApproved(t)

	ids := make([]string, 0, len(got))
	for i, d := range got {
		if d.Method != http.MethodPost || d.Path != "/hook" || d.Header.Get("Content-Type") != "application/json" {
			t.Errorf("entrega %d: llegó por %s %s con Content-Type %q; quería POST /hook en JSON", i, d.Method, d.Path, d.Header.Get("Content-Type"))
		}
		if !d.SignatureOK || !d.InWindow || d.SchemaErr != nil || d.Status != http.StatusOK {
			t.Errorf("entrega %d: firma=%v ventana=%v schema=%v respuesta=%d", i, d.SignatureOK, d.InWindow, d.SchemaErr, d.Status)
		}
		want := "delivered|0|sin error|sin claim|payload vacío|intake.push|" + w.sc.Tenant
		p6WaitScalar(t, w.sc, want, "la fila de la entrega "+d.ID, p6OutboxRow, d.ID)
		ids = append(ids, d.ID)
	}
	if len(got) != 2 || ids[0] == ids[1] {
		t.Errorf("la solicitud aprobada produjo las entregas %v; quería dos, cada una con su X-Wapp-Delivery", ids)
	}
	if rows := p9Scalar(t, w.sc.DB, `SELECT count(*)::text || '|' || count(*) FILTER (WHERE status = 'delivered' AND payload <> '{}'::jsonb)::text
		FROM public.webhook_outbox`); rows != "5|0" {
		t.Errorf("webhook_outbox: (filas | entregadas con payload) = %s, quería 5|0", rows)
	}
	w.metrics["delivered"] = 4
	p3WaitCounter(t, w.sc.S, p6MetricDeliveries, p6MetricLabel, "delivered", 4)
}

// p6PushDoc es el `intake.push` recibido, decodificado por NOMBRE DE CABLE.
type p6PushDoc struct {
	ContractVersion string            `json:"contract_version"`
	Verb            string            `json:"verb"`
	Tenant          string            `json:"tenant"`
	Contact         string            `json:"contact"`
	IntakeID        string            `json:"intake_id"`
	LifecycleStatus string            `json:"lifecycle_status"`
	RevisionNo      int               `json:"revision_no"`
	Variables       map[string]string `json:"variables"`
	BuyerData       map[string]string `json:"buyer_data"`
	CustomerNote    *string           `json:"customer_note"`
	Items           []p6Line          `json:"items"`
	Total           float64           `json:"total"`
	Timestamp       string            `json:"timestamp"`
}

// p6SchemaRequired lee del contrato publicado la lista `required` del schema de intake.push.
func p6SchemaRequired(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(crmFakeContractDir, crmFakePushSchemaFile))
	if err != nil {
		t.Fatalf("leer el schema publicado: %v", err)
	}
	var schema struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil || len(schema.Required) == 0 {
		t.Fatalf("el schema publicado no trae `required`: %v", err)
	}
	slices.Sort(schema.Required)
	return schema.Required
}

// pushContractFields es el candado de internal/integrations/crmpush/contrato_ast_test.go vuelto
// aserción sobre lo RECIBIDO: `revision_no` y `lifecycle_status` no son constantes —cada entrega lleva
// el número que la base le dio a SU revisión y el estado real de la solicitud en ese momento—, y el
// resto del documento es el de la solicitud, con el contacto opaco y sin un dato del canal.
func (w *p6World) pushContractFields(t *testing.T) {
	revisions := p9Scalar(t, w.sc.DB, `SELECT max(revision_no) FILTER (WHERE kind = 'corrected')::text || '|' ||
		max(revision_no) FILTER (WHERE kind = 'approved')::text FROM public.intake_revisions WHERE intake_id = $1::uuid`, w.approved)
	if revisions != "6|7" {
		t.Fatalf("la solicitud aprobada tiene (última corregida | aprobada) = %s, quería 6|7", revisions)
	}
	// La primera entrega de la solicitud aprobada es la del re-análisis (pushReanalysis).
	approved := w.crm.Select(p6ForIntake(w.approved))
	dead := w.crm.Select(p6ForIntake(w.dead))
	if len(approved) != 4 || len(dead) == 0 {
		t.Fatalf("el CRM falso tiene %d entregas de la solicitud aprobada y %d de la otra", len(approved), len(dead))
	}
	// Las variables son las del instante de la ENTREGA: el primer intento de la otra solicitud salió
	// antes de que la empresa las tuviera.
	cases := []struct {
		name      string
		d         crmFakeDelivery
		intake    string
		phone     string
		revision  int
		lifecycle string
		lines     []p6Line
		variables map[string]string
	}{
		{"la corrección", approved[1], w.approved, p6PnApproved, 6, "pending_approval", p6ApprovedLinesStored(), p6Variables()},
		{"la aprobación", approved[2], w.approved, p6PnApproved, 7, "confirmed", p6ApprovedLinesStored(), p6Variables()},
		{"la corrección de la otra solicitud", dead[0], w.dead, p6PnDead, 2, "pending_approval", p6DeadLines(), map[string]string{}},
	}
	required := p6SchemaRequired(t)
	for _, c := range cases {
		var doc p6PushDoc
		if err := json.Unmarshal(c.d.Body, &doc); err != nil {
			t.Fatalf("%s: el cuerpo recibido no se puede leer: %v", c.name, err)
		}
		if doc.RevisionNo != c.revision || doc.LifecycleStatus != c.lifecycle {
			t.Errorf("%s: revision_no %d y lifecycle_status %q; quería %d y %q (los REALES, no un literal)",
				c.name, doc.RevisionNo, doc.LifecycleStatus, c.revision, c.lifecycle)
		}
		w.checkPushDoc(t, c.name, c.d, doc, c.intake, c.lines)
		if !reflect.DeepEqual(doc.Variables, c.variables) {
			t.Errorf("%s: variables = %v, quería %v (las de la empresa al entregar)", c.name, doc.Variables, c.variables)
		}
		if keys := slices.Sorted(maps.Keys(c.d.Doc)); !slices.Equal(keys, required) {
			t.Errorf("%s: el documento trae las claves %v; quería exactamente las obligatorias del schema %v", c.name, keys, required)
		}
		if bytes.Contains(c.d.Body, []byte(c.phone)) {
			t.Errorf("%s: FUGA: el teléfono del cliente viaja en el intake.push", c.name)
		}
	}
}

// checkPushDoc comprueba los campos del documento que no dependen de la revisión: la identidad de la
// solicitud, las líneas y el total, lo que el worker completa al entregar y el instante.
func (w *p6World) checkPushDoc(t *testing.T, name string, d crmFakeDelivery, doc p6PushDoc, intakeID string, lines []p6Line) {
	t.Helper()
	if doc.ContractVersion != "1" || doc.Verb != "intake.push" || doc.Tenant != w.sc.Tenant || doc.IntakeID != intakeID {
		t.Errorf("%s: contract_version %q, verb %q, tenant %q, intake_id %q", name, doc.ContractVersion, doc.Verb, doc.Tenant, doc.IntakeID)
	}
	w.checkPushLines(t, name, doc, intakeID, lines)
	if doc.Variables == nil || len(doc.BuyerData) != 0 || doc.BuyerData == nil || doc.CustomerNote == nil || *doc.CustomerNote != "" {
		t.Errorf("%s: variables %v, buyer_data %v, customer_note %v; quería un objeto de variables, buyer_data {} y customer_note «»",
			name, doc.Variables, doc.BuyerData, doc.CustomerNote)
	}
	built, err := time.Parse(time.RFC3339, doc.Timestamp)
	if err != nil || built.Location() != time.UTC || built.After(d.At) || d.At.Sub(built) > p6Timeout {
		t.Errorf("%s: timestamp %q (%v); quería el instante UTC en que se armó el push, antes de la entrega", name, doc.Timestamp, err)
	}
}

// checkPushLines comprueba el contacto, las líneas y el total del documento contra la solicitud: el
// contacto es el id OPACO de la fila, las líneas las guardadas y el total su suma.
func (w *p6World) checkPushLines(t *testing.T, name string, doc p6PushDoc, intakeID string, lines []p6Line) {
	t.Helper()
	row := p9Scalar(t, w.sc.DB, `SELECT contact_id::text || '|' || total::float8::text FROM public.intakes WHERE id = $1::uuid`, intakeID)
	total := 0.0
	for _, l := range lines {
		total += float64(l.Qty) * l.UnitPrice
	}
	if got := doc.Contact + "|" + strconv.FormatFloat(doc.Total, 'f', -1, 64); got != row || doc.Total != total || !identidadUUID.MatchString(doc.Contact) {
		t.Errorf("%s: (contact | total) = %s; quería %s, lo de la solicitud (contacto opaco, total %v)", name, got, row, total)
	}
	if !reflect.DeepEqual(doc.Items, lines) {
		t.Errorf("%s: items = %+v, quería %+v", name, doc.Items, lines)
	}
}
