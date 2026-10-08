//go:build pendiente

package intakes

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
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
