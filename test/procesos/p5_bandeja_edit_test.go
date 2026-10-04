//go:build integracion

package procesos

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// PUT /api/v1/intakes/{id}/items: la edición manual de las líneas del presupuesto por su dueña.

const (
	// p5FirstPn es el contacto del borrador que se corrige y se aprueba.
	p5FirstPn = "573005550001"

	// p5ItemsQuery resume las líneas guardadas de una solicitud, una por renglón y en orden de alta:
	// «sku|etiqueta|personalización|cantidad|precio».
	p5ItemsQuery = `SELECT coalesce(string_agg(sku || '|' || label || '|' || customization || '|' || qty::text || '|' || (unit_price::float8)::text,
		E'\n' ORDER BY id), '') FROM public.intake_items WHERE intake_id = $1::uuid`
)

// p5Items son las líneas que la dueña guarda en este proceso: las tres del caso Ámbar ya con precio
// y cuatro ADVERSARIAS, que el viejo acepta —
//
//   - `PAN@@1`: separador repetido en el SKU, y una personalización que empieza por `=` (lo que una
//     hoja de cálculo tomaría por fórmula);
//   - un SKU con U+00A0 en los extremos y en medio, y una etiqueta con U+00A0 y un salto de línea;
//   - `PAN-١٢٣`: dígitos no ASCII en el SKU y en la etiqueta;
//   - una línea de regalo (precio 0) cuya etiqueta empieza por `@`.
func p5Items() []map[string]any {
	return []map[string]any{
		{"sku": "TORTA-CHOC#10", "label": "Torta de chocolate 10 porciones", "customization": "decoración infantil", "qty": 1, "unit_price": 2100},
		{"sku": "TORTA-VAIN", "label": "Torta de vainilla", "qty": 1, "unit_price": 3900},
		{"sku": "TEQ-30", "label": "Tequeños congelados", "qty": 2, "unit_price": 490},
		{"sku": "PAN@@1", "label": "Pan de masa madre", "customization": "=SUM(A1)", "qty": 1, "unit_price": 120},
		{"sku": " PAN 2 ", "label": "Pan integral\ncon semillas", "qty": 1, "unit_price": 130},
		{"sku": "PAN-١٢٣", "label": "Pan ١٢٣", "qty": 1, "unit_price": 140},
		{"sku": "REGALO", "label": "@@cortesía", "qty": 1, "unit_price": 0},
	}
}

// p5StoredItems es lo que queda guardado de p5Items, en el formato de p5ItemsQuery: el SKU pierde
// los espacios de los extremos (U+00A0 incluido) y conserva el de en medio; la etiqueta queda en una
// línea, con el U+00A0 y el salto vueltos un espacio; el separador repetido, los dígitos no ASCII y
// el `=` inicial se guardan tal cual.
func p5StoredItems() []string {
	return []string{
		"TORTA-CHOC#10|Torta de chocolate 10 porciones|decoración infantil|1|2100",
		"TORTA-VAIN|Torta de vainilla||1|3900",
		"TEQ-30|Tequeños congelados||2|490",
		"PAN@@1|Pan de masa madre|=SUM(A1)|1|120",
		"PAN 2|Pan integral con semillas||1|130",
		"PAN-١٢٣|Pan ١٢٣||1|140",
		"REGALO|@@cortesía||1|0",
	}
}

// putItems guarda p5Items en la solicitud (con o sin `as_correction`) y devuelve el detalle que
// contesta el PUT. Falla (t.Fatalf) si no es un 200.
func (w *p5World) putItems(t *testing.T, id string, asCorrection bool) p5Detail {
	t.Helper()
	body := map[string]any{"items": p5Items()}
	if asCorrection {
		body["as_correction"] = true
	}
	return p5DecodeDetail(t, w.write(t, http.MethodPut, p5Path(id, "items"), body), "PUT …/items")
}

// expectStoredItems afirma que las líneas guardadas de la solicitud son las de p5StoredItems.
func (w *p5World) expectStoredItems(t *testing.T, id, what string) {
	t.Helper()
	want := strings.Join(p5StoredItems(), "\n")
	if got := p9Scalar(t, w.sc.DB, p5ItemsQuery, id); got != want {
		t.Errorf("%s: intake_items de %s =\n%s\nquería\n%s", what, id, got, want)
	}
}

// p5Editing es el recorrido de la edición sobre un borrador recién nacido: lo que el borrador NO
// deja hacer antes de guardar líneas, la tabla de cuerpos e ids adversarios (ninguno escribe), el
// vaciado, la edición buena y la corrección.
func p5Editing(t *testing.T, w *p5World) {
	sc := w.sc
	w.first = createDraft(t, sc, p5FirstPn)
	born := p5Snapshot(t, sc.DB, sc.Tenant, w.first)
	if born.Intake["status"] != "pending_approval" || len(born.Items) != 0 || len(born.Revisions) != 1 {
		t.Fatalf("el borrador de createDraft nace con estado %v, %d líneas y %d revisiones; quería pending_approval, 0 y 1",
			born.Intake["status"], len(born.Items), len(born.Revisions))
	}

	p5BeforeItems(t, w)
	p5ItemsRejected(t, w)
	w.expectUntouched(t, born, w.first, "tras los cuerpos e ids rechazados de PUT …/items")
	sc.expectNoPendingText(t, "tras los PUT …/items rechazados")

	emptied := p5EmptyItems(t, w, born)
	edited := p5GoodEdit(t, w, emptied)
	p5Correction(t, w, edited)
	sc.expectNoPendingText(t, "tras editar las líneas: corregir no le habla al cliente")
	if n := len(sc.Script.Calls("")); n != 5 {
		t.Errorf("tras la edición el Edge lleva %d inferencias, quería 5 (las del borrador): editar no usa el modelo", n)
	}
}

// p5BeforeItems afirma lo que un borrador SIN líneas guardadas rechaza: el pipeline deja sus líneas
// en la revisión y no en intake_items, así que no hay nada que cotizar ni que aprobar todavía.
func p5BeforeItems(t *testing.T, w *p5World) {
	t.Helper()
	const noLines = "la solicitud no tiene líneas que cotizar: guarda primero las líneas del borrador con PUT /api/v1/intakes/{id}/items"
	r := w.read(t, http.MethodPost, p5Path(w.first, "quote-suggestion"), nil)
	p5ExpectError(t, r, http.StatusBadRequest, noLines, "quote-suggestion de un borrador sin líneas guardadas")

	r = w.write(t, http.MethodPost, p5Path(w.first, "approve"), map[string]string{"rendered_text": "Hola"})
	var pending struct {
		Error string `json:"error"`
		Lines []struct {
			Index int    `json:"index"`
			Label string `json:"label"`
		} `json:"lines"`
	}
	r.JSON(t, &pending)
	if got := fmt.Sprint(pending.Lines); r.Codigo != http.StatusBadRequest || pending.Error != "lines_without_price" ||
		got != "[{0 Torta de chocolate} {3 Envío por confirmar}]" {
		t.Errorf("aprobar un borrador con líneas sin precio: HTTP %d %s; quería 400 lines_without_price con las líneas 0 y 3",
			r.Codigo, recortar(r.Cuerpo))
	}
}

// p5ItemsRejected es la tabla adversaria de PUT …/items: cuerpos que no llegan a escribir y los ids
// que no son una solicitud de la empresa. El 400 de líneas trae TODOS los defectos, con su posición.
func p5ItemsRejected(t *testing.T, w *p5World) {
	t.Helper()
	line := func(sku, label string, qty any, price float64) map[string]any {
		return map[string]any{"sku": sku, "label": label, "qty": qty, "unit_price": price}
	}
	long := strings.Repeat("ñ", 281)
	cases := []struct {
		name    string
		body    any
		message string   // campo `error` exacto; «invalid_items» si el rechazo trae defectos
		defects []string // «posición:campo» de cada defecto, en orden
	}{
		{"sin la clave items", map[string]any{}, "items es obligatorio (manda [] para dejar la solicitud sin líneas)", nil},
		{"items nulo", map[string]any{"items": nil}, "items es obligatorio (manda [] para dejar la solicitud sin líneas)", nil},
		{"cuerpo que no es un objeto", "no soy un objeto", p5BadJSON, nil},
		{"items que no es una lista", map[string]any{"items": "TORTA"}, p5BadJSON, nil},
		{"cantidad en dígitos no ASCII", map[string]any{"items": []any{line("A", "a", "١", 1)}}, p5BadJSON, nil},
		{"sku reservado de la plataforma", map[string]any{"items": []any{line("_shipping", "Envío", 1, 1)}}, "invalid_items", []string{"0:sku"}},
		{"todos los defectos de una línea", map[string]any{"items": []any{line("  ", " \n", 0, -1), line("A", "bien", 1, 1)}},
			"invalid_items", []string{"0:sku", "0:label", "0:qty", "0:unit_price"}},
		{"etiqueta y personalización demasiado largas", map[string]any{"items": []any{
			line("A", "bien", 1, 1),
			map[string]any{"sku": "B", "label": long, "customization": long, "qty": 1, "unit_price": 1},
		}}, "invalid_items", []string{"1:label", "1:customization", "1:label"}},
	}
	for _, c := range cases {
		r := w.write(t, http.MethodPut, p5Path(w.first, "items"), c.body)
		var got struct {
			Error  string `json:"error"`
			Errors []struct {
				Index int    `json:"index"`
				Field string `json:"field"`
			} `json:"errors"`
		}
		r.JSON(t, &got)
		defects := make([]string, 0, len(got.Errors))
		for _, d := range got.Errors {
			defects = append(defects, fmt.Sprintf("%d:%s", d.Index, d.Field))
		}
		if r.Codigo != http.StatusBadRequest || got.Error != c.message || strings.Join(defects, ",") != strings.Join(c.defects, ",") {
			t.Errorf("PUT …/items con %s: HTTP %d %s; quería 400 %q con los defectos %v",
				c.name, r.Codigo, recortar(r.Cuerpo), c.message, c.defects)
		}
	}
	for name, id := range p5BadIDs(t, w.first) {
		r := w.write(t, http.MethodPut, p5Path(id, "items"), map[string]any{"items": p5Items()})
		p5ExpectError(t, r, http.StatusNotFound, p5NotFound, "PUT …/items con un id "+name)
	}
}

// p5EmptyItems afirma que `{"items": []}` SE APLICA —vaciar el presupuesto es una edición— y deja su
// revisión, y que un presupuesto vacío no se puede aprobar. Devuelve la foto que queda.
func p5EmptyItems(t *testing.T, w *p5World, born p5Snap) p5Snap {
	t.Helper()
	d := p5DecodeDetail(t, w.write(t, http.MethodPut, p5Path(w.first, "items"), map[string]any{"items": []any{}}), "PUT …/items con la lista vacía")
	if len(d.Items) != 0 || d.Total != 0 || p5Kinds(d.Revisions) != "1:interpreted:system 2:corrected:owner" {
		t.Errorf("vaciar las líneas contestó %d líneas, total %v y revisiones %q", len(d.Items), d.Total, p5Kinds(d.Revisions))
	}
	emptied := p5Snapshot(t, w.sc.DB, w.sc.Tenant, w.first)
	p5ExpectIntakeChange(t, born, emptied, "vaciar las líneas", "updated_at")
	p5ExpectSame(t, born, emptied, "vaciar las líneas", "items", "event", "jobs", "flow_state", "thread", "buyer", "outbox")
	if got := p5LastInboxEvent(emptied); got != `intake_line_corrected {"lines_corrected":0,"lines_total":0}` {
		t.Errorf("vaciar las líneas publicó %q", got)
	}

	const noLines = "la solicitud no tiene líneas que cotizar: guarda primero las líneas del borrador con PUT /api/v1/intakes/{id}/items"
	r := w.write(t, http.MethodPost, p5Path(w.first, "approve"), map[string]string{"rendered_text": "Hola"})
	p5ExpectError(t, r, http.StatusBadRequest, noLines, "aprobar un presupuesto vacío")
	return w.expectUntouched(t, emptied, w.first, "tras negarse a aprobar un presupuesto vacío")
}

// p5LastInboxEvent resume el último flow_event del contacto de la foto: «nombre payload» (el payload
// con las claves en orden alfabético), y exige que lo publique la bandeja como telemetría (`event`).
func p5LastInboxEvent(s p5Snap) string {
	if len(s.FlowEvents) == 0 {
		return "(ninguno)"
	}
	last := s.FlowEvents[len(s.FlowEvents)-1]
	if last["flow_id"] != p5FlowInbox || last["kind"] != "event" || last["contact_id"] != s.Intake["contact_id"] {
		return fmt.Sprintf("(de %v/%v, contacto %v)", last["flow_id"], last["kind"], last["contact_id"])
	}
	payload, err := json.Marshal(last["payload"])
	if err != nil {
		return "(payload ilegible)"
	}
	return fmt.Sprintf("%v %s", last["name"], payload)
}

// p5GoodEdit guarda p5Items y afirma lo que deja: las líneas saneadas, el total cuadrado, la
// revisión `corrected` de `owner` con la foto de las líneas, `intake_line_corrected` y NADA más (ni
// línea de envío, ni fila en webhook_outbox: la empresa no tiene puente). Devuelve la foto.
func p5GoodEdit(t *testing.T, w *p5World, before p5Snap) p5Snap {
	t.Helper()
	d := w.putItems(t, w.first, false)
	if d.ID != w.first || d.Status != "pending_approval" || d.Total != p5ItemsTotal || d.Overdue || len(d.Items) != 7 {
		t.Errorf("PUT …/items contestó %s/%s total %v vencida=%v con %d líneas", d.ID, d.Status, d.Total, d.Overdue, len(d.Items))
	}
	if p5Kinds(d.Revisions) != "1:interpreted:system 2:corrected:owner 3:corrected:owner" {
		t.Errorf("PUT …/items contestó las revisiones %q", p5Kinds(d.Revisions))
	}
	if got := fmt.Sprint(d.AllowedTransitions); got != "[cancelled confirmed needs_info rejected]" {
		t.Errorf("allowed_transitions tras editar = %s", got)
	}
	w.expectStoredItems(t, w.first, "tras la edición")

	edited := p5Snapshot(t, w.sc.DB, w.sc.Tenant, w.first)
	p5ExpectIntakeChange(t, before, edited, "editar las líneas", "total", "updated_at")
	p5ExpectSame(t, before, edited, "editar las líneas", "event", "jobs", "flow_state", "thread", "buyer", "outbox")
	if edited.Intake["total"] != float64(p5ItemsTotal) || edited.Outbox != 0 {
		t.Errorf("tras editar, intakes.total = %v y webhook_outbox tiene %d filas; quería %d y 0", edited.Intake["total"], edited.Outbox, p5ItemsTotal)
	}
	const revision = `SELECT kind || '|' || created_by || '|' || (rendered_text IS NULL)::text || '|' || (literal_enc IS NULL AND literal_pruned_at IS NULL)::text
			|| '|' || (payload->>'version') || '|' || (payload->>'total') || '|' || jsonb_array_length(payload->'items')::text
			|| '|' || (payload ? 'as_correction')::text || '|' || (payload::text LIKE '%customization%')::text
			|| '|' || (SELECT string_agg(i->>'sku', ',' ORDER BY n) FROM jsonb_array_elements(payload->'items') WITH ORDINALITY x(i, n))
		FROM public.intake_revisions WHERE intake_id = $1::uuid AND revision_no = 3`
	want := "corrected|owner|true|true|1|7370|7|false|false|TORTA-CHOC#10,TORTA-VAIN,TEQ-30,PAN@@1,PAN 2,PAN-١٢٣,REGALO"
	if got := p9Scalar(t, w.sc.DB, revision, w.first); got != want {
		t.Errorf("la revisión de la edición = %q, quería %q", got, want)
	}
	if got := p5LastInboxEvent(edited); got != `intake_line_corrected {"lines_corrected":7,"lines_total":7}` {
		t.Errorf("la edición publicó %q", got)
	}
	return edited
}

// p5Correction repite el MISMO cuerpo como corrección (`as_correction: true`): las líneas quedan
// igual, pero cada PUT deja su revisión (la auditoría no es idempotente), la revisión lleva la señal
// de qué corrige, la telemetría cuenta cero líneas cambiadas y updated_at se mueve (reinicia el plazo).
func p5Correction(t *testing.T, w *p5World, before p5Snap) {
	t.Helper()
	d := w.putItems(t, w.first, true)
	if p5Kinds(d.Revisions) != "1:interpreted:system 2:corrected:owner 3:corrected:owner 4:corrected:owner" || d.Total != p5ItemsTotal {
		t.Errorf("la corrección contestó total %v y revisiones %q", d.Total, p5Kinds(d.Revisions))
	}
	w.expectStoredItems(t, w.first, "tras la corrección")
	after := p5Snapshot(t, w.sc.DB, w.sc.Tenant, w.first)
	p5ExpectIntakeChange(t, before, after, "repetir la edición como corrección", "updated_at")
	const signal = `SELECT (payload->>'as_correction') || '|' || (payload->>'corrects_revision_no') || '|' || (payload->>'corrects_kind')
		FROM public.intake_revisions WHERE intake_id = $1::uuid AND revision_no = 4`
	if got := p9Scalar(t, w.sc.DB, signal, w.first); got != "true|3|corrected" {
		t.Errorf("la señal de la corrección = %q, quería true|3|corrected", got)
	}
	if got := p5LastInboxEvent(after); got != `intake_line_corrected {"lines_corrected":0,"lines_total":7}` {
		t.Errorf("la corrección sin cambios publicó %q", got)
	}
	if n := len(after.Items); n != 7 {
		t.Errorf("tras la corrección hay %d líneas, quería 7", n)
	}
}
