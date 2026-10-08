package apipublica

// intakes_dto_internal_test.go — las dos proyecciones de intakes_dto.go, vistas sin pasar por una
// ruta (05 E-4, P6: nacen con el verde). Los cuerpos enteros los prueba intakes_dto_test.go a
// través de la bandeja; aquí va lo que la proyección decide por su cuenta: contra qué instante
// se marca el plazo y qué claves desaparecen del JSON.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

// TestIntakeToDTO_OverdueIsDecidedAgainstTheGivenInstant: la marca sale de intakes.Overdue con
// el `at` que se le pasa —no de un reloj propio—, no cambia el estado, y los instantes salen en
// UTC aunque lleguen en otra zona.
func TestIntakeToDTO_OverdueIsDecidedAgainstTheGivenInstant(t *testing.T) {
	zone := time.FixedZone("+05:30", 5*3600+1800)
	touched := time.Date(2026, 10, 7, 17, 30, 0, 0, zone) // 12:00 UTC
	in := intakes.Intake{
		ID: "in-1", ContactID: "opaco", SessionID: "s", Status: intakes.StatusPendingApproval,
		Total: 9, CustomerNote: "portería", CreatedAt: touched.Add(-time.Hour), UpdatedAt: touched,
	}
	deadline := touched.Add(intakes.QuoteDeadline)

	for name, tc := range map[string]struct {
		at   time.Time
		want bool
	}{
		"before_the_deadline": {deadline.Add(-time.Nanosecond), false},
		"at_the_deadline":     {deadline, true},
		"long_after":          {deadline.AddDate(1, 0, 0), true},
		"zero_instant":        {time.Time{}, false},
	} {
		got := intakeToDTO(in, tc.at)
		if got.Overdue != tc.want {
			t.Errorf("%s: overdue = %v, quiero %v", name, got.Overdue, tc.want)
		}
		if got.Status != intakes.StatusPendingApproval {
			t.Errorf("%s: la marca cambió el estado a %q", name, got.Status)
		}
	}

	got := intakeToDTO(in, deadline)
	if got.CreatedAt != "2026-10-07T11:00:00Z" || got.UpdatedAt != "2026-10-07T12:00:00Z" {
		t.Errorf("instantes %s / %s; quiero 2026-10-07T11:00:00Z / 2026-10-07T12:00:00Z (en UTC)", got.CreatedAt, got.UpdatedAt)
	}
	if got.ID != "in-1" || got.ContactID != "opaco" || got.SessionID != "s" || got.Total != 9 || got.CustomerNote != "portería" {
		t.Errorf("la cabecera no salió tal cual: %+v", got)
	}

	// Lo que no espera la decisión de nadie no se marca, por viejo que sea.
	in.Status = intakes.StatusConfirmed
	if intakeToDTO(in, deadline.AddDate(1, 0, 0)).Overdue {
		t.Error("una solicitud confirmed salió marcada como overdue")
	}
}

// TestIntakeToDetailResponse_KeysThatDisappear: `literal_pruned_at`, `rendered_text` y
// `created_by` se omiten vacíos —un Format del instante cero publicaría «0001-01-01» en toda
// revisión sin podar y haría indistinguible el caso que el campo viene a separar—; las listas
// vacías son [] y no null; y la personalización vacía SÍ viaja.
func TestIntakeToDetailResponse_KeysThatDisappear(t *testing.T) {
	at := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	detail := intakes.Detail{
		Intake: intakes.Intake{ID: "in-1", Status: intakes.StatusSettled, CreatedAt: at, UpdatedAt: at},
		Items:  []intakes.Item{{SKU: "A", Label: "A", Qty: 1}},
		Revisions: []intakes.Revision{
			{RevisionNo: 1, Kind: intakes.RevisionKindCart, Payload: json.RawMessage(`{"version":1}`), CreatedAt: at},
			{RevisionNo: 2, Kind: intakes.RevisionKindInterpreted, Payload: json.RawMessage(`{"version":1}`), CreatedAt: at,
				RenderedText: "texto", CreatedBy: intakes.RevisionBySystem, LiteralPrunedAt: at.In(time.FixedZone("-03", -3*3600))},
		},
	}
	resp := intakeToDetailResponse(detail, at)
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("el detalle no se pudo serializar: %v", err)
	}
	body := string(raw)
	if n := strings.Count(body, "literal_pruned_at"); n != 1 || !strings.Contains(body, `"literal_pruned_at":"2026-10-08T12:00:00Z"`) {
		t.Errorf("literal_pruned_at aparece %d veces en %s; quiero una, en la revisión podada y en UTC", n, body)
	}
	if strings.Count(body, "rendered_text") != 1 || strings.Count(body, "created_by") != 1 {
		t.Errorf("rendered_text y created_by vacíos no se omitieron: %s", body)
	}
	if strings.Contains(body, "0001-01-01") {
		t.Errorf("el instante cero de una revisión sin podar se publicó: %s", body)
	}
	if !strings.Contains(body, `"customization":""`) || !strings.Contains(body, `"allowed_transitions":[]`) {
		t.Errorf("la personalización vacía o los destinos de un terminal no salieron como \"\" y []: %s", body)
	}

	empty, err := json.Marshal(intakeToDetailResponse(intakes.Detail{}, at))
	if err != nil {
		t.Fatalf("el detalle vacío no se pudo serializar: %v", err)
	}
	for _, want := range []string{`"items":[]`, `"revisions":[]`, `"allowed_transitions":[]`, `"buyer_data_present":false`, `"overdue":false`, `"customer_note":""`} {
		if !strings.Contains(string(empty), want) {
			t.Errorf("el detalle vacío no trae %s: %s", want, empty)
		}
	}
}
