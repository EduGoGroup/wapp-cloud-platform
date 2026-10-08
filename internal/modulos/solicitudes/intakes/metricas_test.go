package intakes

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// metricSpy es un doble de MetricsPublisher que retiene lo que se le pidió publicar
// y, si se le pone un `err`, lo rechaza todo.
type metricSpy struct {
	err   error
	calls []metricCall
}

// metricCall es UNA publicación, tal como la vio el puerto.
type metricCall struct {
	tenantID  string
	contactID string
	name      string
	payload   map[string]any
}

func (s *metricSpy) PublishMetric(_ context.Context, tenantID, contactID, name string, payload map[string]any) error {
	if s.err != nil {
		return s.err
	}
	s.calls = append(s.calls, metricCall{tenantID: tenantID, contactID: contactID, name: name, payload: payload})
	return nil
}

// TestMetricEvents_WireNames: los tres `flow_events.name` son contrato de wire (design §10) y se
// conservan byte a byte aunque las constantes hayan cambiado de nombre (E-11).
func TestMetricEvents_WireNames(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		got  string
		want string
	}{
		{name: "line corrected", got: EventLineCorrected, want: "intake_line_corrected"},
		{name: "approved", got: EventApproved, want: "intake_approved"},
		{name: "info requested", got: EventInfoRequested, want: "intake_info_requested"},
	}
	seen := map[string]bool{}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, quería %q", c.name, c.got, c.want)
		}
		if seen[c.got] {
			t.Errorf("%s repite el nombre %q: cada evento tiene el suyo", c.name, c.got)
		}
		seen[c.got] = true
	}
}

// TestMetricsPublisher_SpeaksTheDomainLanguage: el puerto recibe un tenant, el contacto opaco, el
// nombre del evento y sus contadores —nada de la fila de flow_events— y devuelve el error del
// emisor tal cual, para que quien llama lo avise sin propagarlo.
func TestMetricsPublisher_SpeaksTheDomainLanguage(t *testing.T) {
	t.Parallel()
	spy := &metricSpy{}
	var port MetricsPublisher = spy

	// Las tres formas de design §10, tal como viajan por el puerto.
	payloads := []struct {
		event   string
		payload map[string]any
		want    string
	}{
		{event: EventLineCorrected, payload: map[string]any{"lines_corrected": 2, "lines_total": 4}, want: `{"lines_corrected":2,"lines_total":4}`},
		{event: EventApproved, payload: map[string]any{"rev": 3, "elapsed_from_draft_ms": int64(1_900_000)}, want: `{"elapsed_from_draft_ms":1900000,"rev":3}`},
		{event: EventInfoRequested, payload: map[string]any{"questions": 1}, want: `{"questions":1}`},
	}
	for _, p := range payloads {
		if err := port.PublishMetric(context.Background(), "tenant-a", "contacto-opaco-1", p.event, p.payload); err != nil {
			t.Fatalf("PublishMetric(%s) devolvió %v, quería nil", p.event, err)
		}
	}
	if len(spy.calls) != len(payloads) {
		t.Fatalf("el doble vio %d publicaciones, quería %d", len(spy.calls), len(payloads))
	}
	for i, p := range payloads {
		call := spy.calls[i]
		if call.tenantID != "tenant-a" || call.contactID != "contacto-opaco-1" || call.name != p.event {
			t.Errorf("publicación %d = (%q, %q, %q), quería (tenant-a, contacto-opaco-1, %q)",
				i, call.tenantID, call.contactID, call.name, p.event)
		}
		raw, err := json.Marshal(call.payload)
		if err != nil {
			t.Fatalf("serializar el payload de %s: %v", p.event, err)
		}
		if string(raw) != p.want {
			t.Errorf("payload de %s = %s, quería %s", p.event, raw, p.want)
		}
	}

	failure := errors.New("la base no responde")
	port = &metricSpy{err: failure}
	if err := port.PublishMetric(context.Background(), "tenant-a", "contacto-opaco-1", EventApproved, nil); !errors.Is(err, failure) {
		t.Errorf("PublishMetric con el emisor caído = %v, quería el error del emisor", err)
	}
}

// Los vectores de los dos cálculos puros salen del fichero viejo
// (internal/intakes/metricas.go @ 64c181a), casos adversarios incluidos.

// twoLines son las dos líneas de cliente de la escena del test viejo.
func twoLines() []Item {
	return []Item{
		{SKU: "torta-v1", Label: "Torta 10-12 porciones", Qty: 1, UnitPrice: 18000},
		{SKU: "hamb-v1", Label: "Hamburguesa", Customization: "sin cebolla, bien cocida", Qty: 2, UnitPrice: 4000},
	}
}

// editedLines devuelve las dos líneas tras aplicarles una edición.
func editedLines(edit func(lines []Item)) []Item {
	lines := twoLines()
	edit(lines)
	return lines
}

// TestCorrectionCount: lines_total es el lado grande y lines_corrected lo que no sobrevivió igual,
// por multiconjunto de los cinco campos editables; la línea de plataforma no cuenta.
func TestCorrectionCount(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	shipping := Item{SKU: "_shipping", Label: "Envío — Centro", Qty: 1, UnitPrice: 1500}
	cake := twoLines()[0]
	cases := []struct {
		name          string
		before, after []Item
		wantCorrected int
		wantTotal     int
	}{
		{name: "nothing touched", before: twoLines(), after: twoLines(), wantCorrected: 0, wantTotal: 2},
		{name: "quantity corrected", before: twoLines(), after: editedLines(func(l []Item) { l[1].Qty = 3 }), wantCorrected: 1, wantTotal: 2},
		{name: "one removed", before: twoLines(), after: twoLines()[:1], wantCorrected: 1, wantTotal: 2},
		{name: "all removed never exceeds the total", before: twoLines(), after: nil, wantCorrected: 2, wantTotal: 2},
		{
			name: "one added", before: twoLines(),
			after:         append(twoLines(), Item{SKU: "gaseosa-v1", Label: "Gaseosa", Qty: 1, UnitPrice: 2000}),
			wantCorrected: 1, wantTotal: 3,
		},
		{name: "from empty", before: nil, after: twoLines(), wantCorrected: 2, wantTotal: 2},
		{name: "both empty", before: nil, after: nil},
		{name: "customization only", before: twoLines(), after: editedLines(func(l []Item) { l[1].Customization = "con cebolla" }), wantCorrected: 1, wantTotal: 2},
		{name: "label only", before: twoLines(), after: editedLines(func(l []Item) { l[0].Label = "Torta" }), wantCorrected: 1, wantTotal: 2},
		{name: "price only", before: twoLines(), after: editedLines(func(l []Item) { l[0].UnitPrice = 18000.01 }), wantCorrected: 1, wantTotal: 2},
		{name: "sku case is another line", before: twoLines(), after: editedLines(func(l []Item) { l[0].SKU = "TORTA-V1" }), wantCorrected: 1, wantTotal: 2},
		{name: "sku trailing space is another line", before: twoLines(), after: editedLines(func(l []Item) { l[0].SKU = "torta-v1 " }), wantCorrected: 1, wantTotal: 2},
		{
			name:   "added_at is not part of the identity",
			before: editedLines(func(l []Item) { l[0].AddedAt, l[1].AddedAt = t0, t0.Add(time.Hour) }), after: twoLines(),
			wantCorrected: 0, wantTotal: 2,
		},
		{name: "reordering is not a correction", before: twoLines(), after: editedLines(func(l []Item) { l[0], l[1] = l[1], l[0] }), wantCorrected: 0, wantTotal: 2},
		{name: "duplicate removed", before: []Item{cake, cake}, after: []Item{cake}, wantCorrected: 1, wantTotal: 2},
		{name: "duplicate added", before: []Item{cake}, after: []Item{cake, cake}, wantCorrected: 1, wantTotal: 2},
		{
			name: "one of two duplicates edited", before: []Item{cake, cake},
			after:         []Item{cake, {SKU: cake.SKU, Label: cake.Label, Qty: 5, UnitPrice: cake.UnitPrice}},
			wantCorrected: 1, wantTotal: 2,
		},
		{name: "all replaced by more lines", before: twoLines(), after: []Item{{SKU: "x", Qty: 1}, {SKU: "y", Qty: 1}, {SKU: "z", Qty: 1}}, wantCorrected: 3, wantTotal: 3},
		{name: "shipping only before", before: append(twoLines(), shipping), after: editedLines(func(l []Item) { l[1].Qty = 3 }), wantCorrected: 1, wantTotal: 2},
		{name: "shipping on both sides", before: append(twoLines(), shipping), after: append(twoLines(), shipping), wantCorrected: 0, wantTotal: 2},
		{
			name: "a changed shipping line is still not counted", before: append(twoLines(), shipping),
			after:         append(twoLines(), Item{SKU: "_shipping", Label: "otro", Qty: 1, UnitPrice: 9}),
			wantCorrected: 0, wantTotal: 2,
		},
		{name: "only shipping", before: []Item{shipping}, after: []Item{shipping}},
		{name: "any reserved prefix is platform", before: []Item{{SKU: "_", Qty: 1}}, after: []Item{{SKU: "_x", Qty: 2}}},
		{name: "space before the prefix is a customer line", before: []Item{{SKU: " _shipping", Qty: 1}}, after: nil, wantCorrected: 1, wantTotal: 1},
		{name: "underscore suffix is a customer line", before: []Item{{SKU: "PAN_", Qty: 1}}, after: []Item{{SKU: "PAN_", Qty: 1}}, wantCorrected: 0, wantTotal: 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			corrected, total := correctionCount(c.before, c.after)
			if corrected != c.wantCorrected || total != c.wantTotal {
				t.Errorf("correctionCount = (%d, %d), quería (%d, %d)", corrected, total, c.wantCorrected, c.wantTotal)
			}
			if corrected > total {
				t.Errorf("lines_corrected (%d) supera a lines_total (%d)", corrected, total)
			}
		})
	}
}

// TestLineKeyOf_FiveEditableFields: la clave lleva los cinco campos que el dueño toca y nada más.
func TestLineKeyOf_FiveEditableFields(t *testing.T) {
	t.Parallel()
	it := Item{SKU: "hamb-v1", Label: "Hamburguesa", Customization: "sin cebolla", Qty: 2, UnitPrice: 4000, AddedAt: time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)}
	want := lineKey{sku: "hamb-v1", label: "Hamburguesa", customization: "sin cebolla", qty: 2, unitPrice: 4000}
	if got := lineKeyOf(it); got != want {
		t.Errorf("lineKeyOf = %+v, quería %+v", got, want)
	}
}

// TestIsPlatformLine: es de plataforma el sku que EMPIEZA por el prefijo reservado, sin recortar.
func TestIsPlatformLine(t *testing.T) {
	t.Parallel()
	cases := []struct {
		sku  string
		want bool
	}{
		{sku: "_shipping", want: true},
		{sku: "_", want: true},
		{sku: "_x", want: true},
		{sku: " _shipping"},
		{sku: "PAN_"},
		{sku: ""},
		{sku: "pan"},
	}
	for _, c := range cases {
		if got := isPlatformLine(Item{SKU: c.sku}); got != c.want {
			t.Errorf("isPlatformLine(%q) = %v, quería %v", c.sku, got, c.want)
		}
	}
}

// TestDraftInstant: manda la revisión `interpreted` de número más bajo —no la posición ni la fecha—
// y una sin fecha no cuenta.
func TestDraftInstant(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	rev := func(no int, kind string, at time.Time) Revision {
		return Revision{RevisionNo: no, Kind: kind, CreatedAt: at}
	}
	cases := []struct {
		name      string
		revisions []Revision
		want      time.Time
		wantFound bool
	}{
		{name: "no revisions"},
		{name: "no interpreted revision", revisions: []Revision{rev(1, "corrected", t0), rev(2, "approved", t0.Add(time.Hour))}},
		{name: "single draft", revisions: []Revision{rev(1, RevisionKindInterpreted, t0)}, want: t0, wantFound: true},
		{
			name: "reanalysis does not restart the clock",
			revisions: []Revision{
				rev(1, "interpreted", t0), rev(2, "corrected", t0.Add(time.Minute)), rev(3, "interpreted", t0.Add(30*time.Minute)),
			},
			want: t0, wantFound: true,
		},
		{
			name:      "slice order does not matter",
			revisions: []Revision{rev(3, "interpreted", t0.Add(30*time.Minute)), rev(1, "interpreted", t0)},
			want:      t0, wantFound: true,
		},
		{
			name:      "lowest number wins over earliest time",
			revisions: []Revision{rev(2, "interpreted", t0), rev(1, "interpreted", t0.Add(time.Hour))},
			want:      t0.Add(time.Hour), wantFound: true,
		},
		{
			name:      "undated draft is skipped",
			revisions: []Revision{rev(1, "interpreted", time.Time{}), rev(2, "interpreted", t0.Add(5*time.Minute))},
			want:      t0.Add(5 * time.Minute), wantFound: true,
		},
		{name: "only an undated draft", revisions: []Revision{rev(1, "interpreted", time.Time{})}},
		{
			name:      "same number keeps the first seen",
			revisions: []Revision{rev(1, "interpreted", t0), rev(1, "interpreted", t0.Add(time.Hour))},
			want:      t0, wantFound: true,
		},
		{name: "kind is matched exactly", revisions: []Revision{rev(1, "Interpreted", t0), rev(2, "interpreted ", t0)}},
		{
			name:      "revision number zero is the lowest",
			revisions: []Revision{rev(5, "interpreted", t0.Add(time.Hour)), rev(0, "interpreted", t0)},
			want:      t0, wantFound: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got, found := draftInstant(c.revisions)
			if found != c.wantFound || !got.Equal(c.want) {
				t.Errorf("draftInstant = (%v, %v), quería (%v, %v)", got, found, c.want, c.wantFound)
			}
		})
	}
}
