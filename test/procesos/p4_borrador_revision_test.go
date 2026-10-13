//go:build integracion

package procesos

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Lo que el borrador de P4 deja: la solicitud, su revisión `interpreted` y la fila de telemetría en
// Postgres, y lo que la dueña ve por GET /api/v1/intakes y GET /api/v1/intakes/{id}.

// p4Line es una línea del presupuesto tal como va en el payload de la revisión.
type p4Line struct {
	Kind          string   `json:"kind"`
	SKU           string   `json:"sku"`
	Label         string   `json:"label"`
	Qty           int      `json:"qty"`
	UnitPrice     *float64 `json:"unit_price"`
	UnitKind      string   `json:"unit_kind"`
	PackageSize   int      `json:"package_size"`
	Customization string   `json:"customization"`
	Evidence      string   `json:"evidence"`
	Range         *struct {
		Min  int    `json:"min"`
		Max  int    `json:"max"`
		Unit string `json:"unit"`
	} `json:"range"`
	Match *struct {
		Strategy   string  `json:"strategy"`
		Confidence float64 `json:"confidence"`
	} `json:"match"`
	VariantOptions []struct {
		SKU   string  `json:"sku"`
		Label string  `json:"label"`
		Price float64 `json:"price"`
	} `json:"variant_options"`
}

// p4Payload es el payload de la revisión `interpreted`. SourceText solo viene en la API (la base lo
// guarda cifrado aparte).
type p4Payload struct {
	Version            int       `json:"version"`
	Lines              []p4Line  `json:"lines"`
	DeliveryDate       string    `json:"delivery_date"`
	MessageTS          time.Time `json:"message_ts"`
	SuggestedQuestions []string  `json:"suggested_questions"`
	SourceText         string    `json:"source_text"`
	Analysis           struct {
		Source   string `json:"source"`
		Provider string `json:"provider"`
	} `json:"analysis"`
}

// summary resume una línea para compararla de un vistazo: clase, sku, etiqueta, cantidad, precio
// («-» si no tiene), paquete y estrategia del match.
func (l p4Line) summary() string {
	price := "-"
	if l.UnitPrice != nil {
		price = fmt.Sprint(*l.UnitPrice)
	}
	strategy := "-"
	if l.Match != nil {
		strategy = fmt.Sprintf("%s/%v", l.Match.Strategy, l.Match.Confidence)
	}
	return fmt.Sprintf("%s|%s|%s|%d|%s|%s:%d|%s", l.Kind, l.SKU, l.Label, l.Qty, price, l.UnitKind, l.PackageSize, strategy)
}

// p4Summaries resume todas las líneas de un payload.
func p4Summaries(p p4Payload) []string {
	out := make([]string, len(p.Lines))
	for i, l := range p.Lines {
		out[i] = l.summary()
	}
	return out
}

// p4RevisionPayload lee de Postgres el payload de la revisión 1 de una solicitud. Falla (t.Fatalf) si
// no decodifica.
func p4RevisionPayload(t *testing.T, sc *draftScene, intakeID string) p4Payload {
	t.Helper()
	var payload p4Payload
	raw := p9Scalar(t, sc.DB, `SELECT payload::text FROM public.intake_revisions WHERE intake_id = $1::uuid AND revision_no = 1`, intakeID)
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("el payload de la revisión 1 de %s no decodifica: %v\n%s", intakeID, err, raw)
	}
	return payload
}

// p4DraftRows afirma lo que el borrador deja en Postgres: UNA solicitud `pending_approval` sin
// importe, atada al evento; NINGUNA fila en intake_items (las líneas viven en la revisión hasta que
// la dueña corrige); UNA revisión `interpreted` firmada por `system` con el literal cifrado aparte; y
// la fila de telemetría `intake_draft_created`.
func p4DraftRows(t *testing.T, sc *draftScene, run p4Run) {
	t.Helper()
	const intake = `SELECT status || '|' || total::text || '|' || intake_type || '|' || event_id::text || '|' || contact_id || '|' || session_id
			|| '|' || customer_note || '|' || (crm_status IS NULL AND crm_synced_at IS NULL AND expires_at IS NULL AND deposit_due_at IS NULL)::text
		FROM public.intakes WHERE id = $1::uuid AND tenant_id = $2`
	want := "pending_approval|0|order|" + run.eventID + "|" + run.contactID + "|" + sc.Edge.SessionID + "||true"
	if got := p9Scalar(t, sc.DB, intake, run.intakeID, sc.Tenant); got != want {
		t.Errorf("la solicitud = %q, quería %q", got, want)
	}
	if n := consultaEntero(t, sc.DB, `SELECT count(*) FROM public.intakes WHERE tenant_id = $1`, sc.Tenant); n != 1 {
		t.Errorf("intakes tiene %d filas de la empresa, quería 1", n)
	}
	if n := consultaEntero(t, sc.DB, `SELECT count(*) FROM public.intake_items WHERE intake_id = $1::uuid`, run.intakeID); n != 0 {
		t.Errorf("intake_items tiene %d filas del borrador; el viejo no escribe ninguna hasta que la dueña corrige", n)
	}

	const revision = `SELECT revision_no::text || '|' || kind || '|' || created_by || '|' || (rendered_text IS NULL)::text
			|| '|' || (length(literal_enc) > 0 AND length(literal_dek) > 0 AND literal_kek_id <> '' AND literal_pruned_at IS NULL)::text
			|| '|' || (payload ? 'source_text')::text || '|' || (payload::text LIKE '%evidence%')::text
		FROM public.intake_revisions WHERE intake_id = $1::uuid`
	if got, want := p9Scalar(t, sc.DB, revision, run.intakeID), "1|interpreted|system|true|true|false|false"; got != want {
		t.Errorf("la revisión = %q, quería %q (el literal y las evidencias NO van en el payload)", got, want)
	}
	if n := consultaEntero(t, sc.DB, `SELECT count(*) FROM public.intake_revisions WHERE intake_id = $1::uuid`, run.intakeID); n != 1 {
		t.Errorf("intake_revisions tiene %d filas del borrador, quería 1", n)
	}
	p4CheckAmbarPayload(t, p4RevisionPayload(t, sc, run.intakeID), "la revisión en Postgres")

	const event = `SELECT flow_id || '|' || kind || '|' || contact_id || '|' || (payload->>'lines') || '|' || (payload->>'matched') || '|' || (payload->>'unmatched')
			|| '|' || ((payload->>'elapsed_ms')::bigint = (SELECT (artifacts->'draft'->>'elapsed_ms')::bigint FROM public.intake_jobs WHERE id = $3::uuid))::text
		FROM public.flow_events WHERE tenant_id = $1 AND name = 'intake_draft_created' AND contact_id = $2`
	if got, want := p9Scalar(t, sc.DB, event, sc.Tenant, run.contactID, run.jobID), p4DraftFlow+"|event|"+run.contactID+"|4|3|0|true"; got != want {
		t.Errorf("flow_events intake_draft_created = %q, quería %q", got, want)
	}
	const names = `SELECT coalesce(string_agg(name, ',' ORDER BY id), '') FROM public.flow_events WHERE tenant_id = $1`
	if got := p9Scalar(t, sc.DB, names, sc.Tenant); got != "event_started,intake_draft_created" {
		t.Errorf("flow_events de la empresa = %q, quería event_started,intake_draft_created", got)
	}
}

// p4CheckAmbarPayload afirma la interpretación del caso Ámbar contra el catálogo del escenario: tres
// líneas casadas por el escalón EXACTO, en el orden del cliente, y el envío el último; el paquete de
// 30 es UN paquete. El detalle de las dos tortas, las preguntas y la fecha van en sus ayudas.
func p4CheckAmbarPayload(t *testing.T, p p4Payload, where string) {
	t.Helper()
	want := []string{
		"matched|TORTA-CHOC|Torta de chocolate|1|-|:0|exact/1",
		"matched|TORTA-VAIN|Torta de vainilla|1|3900|:0|exact/1",
		"matched|TEQ-30|Tequeños congelados|1|490|package:30|exact/1",
		"shipping|" + p4ShippingSKU + "|Envío por confirmar|1|-|:0|-",
	}
	if got := p4Summaries(p); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("%s: las líneas son\n%s\nquería\n%s", where, strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	p4CheckAmbarCakes(t, p.Lines[0], p.Lines[1], where)
	questions := []string{"¿Confirmas el tamaño de «Torta de chocolate»: 10 o 12 porciones?", "¿Zona de entrega para calcular el envío?"}
	if strings.Join(p.SuggestedQuestions, "|") != strings.Join(questions, "|") {
		t.Errorf("%s: las preguntas preparadas = %q, quería %q", where, p.SuggestedQuestions, questions)
	}
	if p.Version != 1 || p.Analysis.Source != "event_thread" || p.Analysis.Provider != "" {
		t.Errorf("%s: version %d, análisis %+v; quería 1 y event_thread sin vía", where, p.Version, p.Analysis)
	}
	p4CheckDeliveryDate(t, p, where)
}

// p4CheckAmbarCakes afirma las dos tortas: la de chocolate se vende por presentaciones y el cliente
// dio un rango, así que sale sin precio, con el rango SIN colapsar y con las dos presentaciones que
// caben en él; el añadido que el catálogo no tiene («decoración infantil») cae a personalización. La
// de vainilla conserva su rango y su personalización.
func p4CheckAmbarCakes(t *testing.T, choc, vanilla p4Line, where string) {
	t.Helper()
	if choc.Range == nil || choc.Range.Min != 10 || choc.Range.Max != 12 || choc.Range.Unit != "porciones" {
		t.Errorf("%s: el rango de la torta de chocolate = %+v, quería 10 a 12 porciones", where, choc.Range)
	}
	if choc.Customization != "decoración infantil" {
		t.Errorf("%s: la personalización de la torta de chocolate = %q", where, choc.Customization)
	}
	options := make([]string, 0, len(choc.VariantOptions))
	for _, o := range choc.VariantOptions {
		options = append(options, fmt.Sprintf("%s=%v", o.SKU, o.Price))
	}
	if strings.Join(options, ",") != "TORTA-CHOC#10=2100,TORTA-CHOC#12=2400" {
		t.Errorf("%s: las opciones de la torta de chocolate = %v, quería las presentaciones de 10 y 12", where, options)
	}
	if vanilla.Range == nil || vanilla.Range.Min != 25 || vanilla.Range.Max != 30 {
		t.Errorf("%s: el rango de la torta de vainilla = %+v, quería 25 a 30", where, vanilla.Range)
	}
	if vanilla.Customization != "lluvia de colores, dulce de leche y merengue" {
		t.Errorf("%s: la personalización de la torta de vainilla = %q", where, vanilla.Customization)
	}
}

// p4CheckDeliveryDate afirma la fecha de entrega: «el miércoles de la semana que viene», contra la
// fecha del MENSAJE en UTC, es un miércoles de 3 a 9 días después. La calcula Go; la que propone el
// modelo (scriptStaleDate) no se usa (D-044.9).
func p4CheckDeliveryDate(t *testing.T, p p4Payload, where string) {
	t.Helper()
	date, err := time.Parse(time.DateOnly, p.DeliveryDate)
	if err != nil || p.DeliveryDate == scriptStaleDate {
		t.Errorf("%s: delivery_date = %q (error %v); quería una fecha calculada, no la del modelo", where, p.DeliveryDate, err)
		return
	}
	days := date.Sub(p.MessageTS.UTC().Truncate(24*time.Hour)) / (24 * time.Hour)
	if date.Weekday() != time.Wednesday || days < 3 || days > 9 {
		t.Errorf("%s: delivery_date = %s (%s) con message_ts %s; quería el miércoles de la semana siguiente al mensaje",
			where, p.DeliveryDate, date.Weekday(), p.MessageTS)
	}
}

// p4DraftAPI afirma lo que ve la dueña: GET /api/v1/intakes lista la solicitud, y GET
// /api/v1/intakes/{id} trae su revisión con las líneas casadas con el catálogo y —solo aquí,
// descifrado en el borde— el hilo literal y la evidencia de cada línea. `items` va vacío. Una
// solicitud que no existe, o un id con dígitos no ASCII, da 404.
func p4DraftAPI(t *testing.T, sc *draftScene, run p4Run, texts []string) {
	t.Helper()
	p4DraftList(t, sc, run)
	payload := p4DraftDetail(t, sc, run)
	p4CheckAmbarPayload(t, payload, "la revisión en la API")
	evidences := []string{scriptEvidenceChoc, scriptEvidenceVanilla, scriptEvidenceTequenos, ""}
	for i, l := range payload.Lines {
		if l.Evidence != evidences[i] {
			t.Errorf("la evidencia de la línea %d en la API = %q, quería %q", i, l.Evidence, evidences[i])
		}
	}
	for _, text := range texts {
		if !strings.Contains(payload.SourceText, "cliente: "+text+"\n") {
			t.Errorf("el source_text de la API no trae el mensaje del cliente %q", text)
		}
	}
	for name, id := range map[string]string{"desconocida": uuidAleatorio(t), "dígitos no ASCII": "١" + run.intakeID[1:]} {
		if r := sc.Pub.Get(t, "/api/v1/intakes/"+id, nil); r.Codigo != http.StatusNotFound {
			t.Errorf("GET /api/v1/intakes/{id} con una solicitud %s: HTTP %d, quería 404", name, r.Codigo)
		}
	}
}

// p4DraftList afirma GET /api/v1/intakes: 200 con UNA solicitud, la del borrador, en
// `pending_approval`, sin importe y sin vencer.
func p4DraftList(t *testing.T, sc *draftScene, run p4Run) {
	t.Helper()
	r := sc.Pub.Get(t, "/api/v1/intakes", nil)
	var list struct {
		Intakes []struct {
			ID, Status string
			ContactID  string  `json:"contact_id"`
			SessionID  string  `json:"session_id"`
			Total      float64 `json:"total"`
			Overdue    bool    `json:"overdue"`
		} `json:"intakes"`
		Page, Total int
	}
	r.JSON(t, &list)
	if r.Codigo != http.StatusOK || list.Total != 1 || list.Page != 1 || len(list.Intakes) != 1 {
		t.Fatalf("GET /api/v1/intakes: HTTP %d con %d solicitudes (total %d)\ncuerpo: %s", r.Codigo, len(list.Intakes), list.Total, recortar(r.Cuerpo))
	}
	in := list.Intakes[0]
	if in.ID != run.intakeID || in.Status != "pending_approval" || in.ContactID != run.contactID || in.SessionID != sc.Edge.SessionID {
		t.Errorf("la solicitud de la lista = %+v", in)
	}
	if in.Total != 0 || in.Overdue {
		t.Errorf("la solicitud de la lista sale con total %v y vencida=%v; quería 0 y sin vencer", in.Total, in.Overdue)
	}
}

// p4DraftDetail afirma GET /api/v1/intakes/{id}: 200 con la solicitud, `items` vacío y UNA revisión
// (la 1, `interpreted`, de `system`), y devuelve su payload.
func p4DraftDetail(t *testing.T, sc *draftScene, run p4Run) p4Payload {
	t.Helper()
	r := sc.Pub.Get(t, "/api/v1/intakes/"+run.intakeID, nil)
	var detail struct {
		ID, Status string
		Total      float64
		Items      []json.RawMessage
		Revisions  []struct {
			RevisionNo int       `json:"revision_no"`
			Kind       string    `json:"kind"`
			CreatedBy  string    `json:"created_by"`
			Payload    p4Payload `json:"payload"`
		}
	}
	r.JSON(t, &detail)
	if r.Codigo != http.StatusOK || detail.ID != run.intakeID || detail.Status != "pending_approval" || detail.Total != 0 {
		t.Fatalf("GET /api/v1/intakes/{id}: HTTP %d %s/%s total %v\ncuerpo: %s", r.Codigo, detail.ID, detail.Status, detail.Total, recortar(r.Cuerpo))
	}
	if len(detail.Items) != 0 {
		t.Errorf("el detalle trae %d items; el viejo no devuelve ninguno en un borrador sin corregir", len(detail.Items))
	}
	if len(detail.Revisions) != 1 {
		t.Fatalf("el detalle trae %d revisiones, quería 1", len(detail.Revisions))
	}
	if rev := detail.Revisions[0]; rev.RevisionNo != 1 || rev.Kind != "interpreted" || rev.CreatedBy != "system" {
		t.Errorf("la revisión del detalle = %d/%s/%s, quería la 1, interpreted, de system", rev.RevisionNo, rev.Kind, rev.CreatedBy)
	}
	return detail.Revisions[0].Payload
}
