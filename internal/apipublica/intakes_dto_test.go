package apipublica_test

// intakes_dto_test.go — LOS CUERPOS de la bandeja del contrato de MountIntakes: la cabecera que
// comparten G1, G2 y G3, la página de G1 y el detalle que responden G2, G4, G5 y G6. Byte a byte,
// porque el orden de las claves y las que salen «siempre» son contrato. Los dobles están en
// intakes_test.go.

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica/apipublicahelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

// intakeContact es un contact_id opaco: un identificador sin número ni JID.
const intakeContact = "9f1c0a7e-0000-4000-8000-000000000abc"

// intakeZone es una zona que NO es UTC: los instantes tienen que salir normalizados.
var intakeZone = time.FixedZone("-03", -3*3600)

// intakeDue es una solicitud por aprobar cuyo plazo vence EXACTAMENTE en intakeNow (se tocó 24 h
// antes): el instante exacto ya cuenta como vencido.
func intakeDue() intakes.Intake {
	return intakes.Intake{
		ID: "in-1", ContactID: intakeContact, SessionID: "sess-a",
		Status: intakes.StatusPendingApproval, Total: 21000.5, CustomerNote: "dejar en portería",
		CreatedAt: time.Date(2026, 10, 1, 9, 0, 0, 0, intakeZone),
		UpdatedAt: time.Date(2026, 10, 7, 9, 0, 0, 0, intakeZone),
	}
}

// intakeFresh es su vecina: mismo estado, tocada un segundo después, sin nota. Todavía en plazo.
func intakeFresh() intakes.Intake {
	in := intakeDue()
	in.ID, in.Total, in.CustomerNote = "in-2", 9, ""
	in.UpdatedAt = in.UpdatedAt.Add(time.Second)
	return in
}

const (
	intakeDueHeader = `"id":"in-1","contact_id":"9f1c0a7e-0000-4000-8000-000000000abc","session_id":"sess-a",` +
		`"status":"pending_approval","total":21000.5,"customer_note":"dejar en portería","overdue":true,` +
		`"created_at":"2026-10-01T12:00:00Z","updated_at":"2026-10-07T12:00:00Z"`
	intakeFreshHeader = `"id":"in-2","contact_id":"9f1c0a7e-0000-4000-8000-000000000abc","session_id":"sess-a",` +
		`"status":"pending_approval","total":9,"customer_note":"","overdue":false,` +
		`"created_at":"2026-10-01T12:00:00Z","updated_at":"2026-10-07T12:00:01Z"`
)

// TestMountIntakes_ListBody: la página, byte a byte. Las dos solicitudes viajan en la MISMA
// respuesta para distinguir «marca lo que toca» de «marca todo»; page, page_size y total son los
// que devuelve el servicio, no los que pidió la query.
func TestMountIntakes_ListBody(t *testing.T) {
	svc := &intakeServiceSpy{page: intakes.Page{
		Intakes: []intakes.Intake{intakeDue(), intakeFresh()}, Page: 3, PageSize: 2, Total: 41,
	}}
	rec := intakeDo(t, svc, http.MethodGet, intakesTarget+"?page=7&page_size=100000", "")
	wantCode(t, "G1", rec, http.StatusOK)
	wantExactBody(t, "G1", rec, `{"intakes":[{`+intakeDueHeader+`},{`+intakeFreshHeader+`}],"page":3,"page_size":2,"total":41}`)
}

// TestMountIntakes_EmptyListIsAnArray: sin solicitudes responde [] y no null, también si el
// servicio devuelve un slice nil.
func TestMountIntakes_EmptyListIsAnArray(t *testing.T) {
	for name, rows := range map[string][]intakes.Intake{"nil": nil, "empty": {}} {
		svc := &intakeServiceSpy{page: intakes.Page{Intakes: rows, Page: 1, PageSize: 50}}
		rec := intakeDo(t, svc, http.MethodGet, intakesTarget, "")
		wantCode(t, name, rec, http.StatusOK)
		wantExactBody(t, name, rec, `{"intakes":[],"page":1,"page_size":50,"total":0}`)
	}
}

// intakeOverdueMarks devuelve la marca `overdue` de cada fila de la página que sirve d.
func intakeOverdueMarks(t *testing.T, d apipublica.IntakesDeps) []bool {
	t.Helper()
	h := apipublicahelpertest.New(t)
	rec := h.Call(intakeCara(h.Common(), d), h.With(tenantA, intakeReadPerm), http.MethodGet, intakesTarget, "")
	wantCode(t, "G1", rec, http.StatusOK)
	var body struct {
		Intakes []struct {
			Overdue *bool `json:"overdue"`
		} `json:"intakes"`
	}
	wantJSON(t, "G1", rec, &body)
	out := make([]bool, 0, len(body.Intakes))
	for i, in := range body.Intakes {
		if in.Overdue == nil {
			t.Fatalf("la fila %d no trae la clave overdue; viaja siempre", i)
		}
		out = append(out, *in.Overdue)
	}
	return out
}

// TestMountIntakes_OverdueUsesTheInjectedClock: la marca se decide contra d.Now. Con el reloj un
// nanosegundo antes del plazo nadie está vencido; en el instante exacto, solo la que toca.
func TestMountIntakes_OverdueUsesTheInjectedClock(t *testing.T) {
	page := intakes.Page{Intakes: []intakes.Intake{intakeDue(), intakeFresh()}, Page: 1, PageSize: 50, Total: 2}
	for name, tc := range map[string]struct {
		now  time.Time
		want [2]bool
	}{
		"one_nanosecond_before_the_deadline": {intakeNow.Add(-time.Nanosecond), [2]bool{false, false}},
		"the_exact_instant_already_counts":   {intakeNow, [2]bool{true, false}},
		"one_second_later_both_are_overdue":  {intakeNow.Add(time.Second), [2]bool{true, true}},
	} {
		d := intakeDeps(&intakeServiceSpy{page: page}, entitlements.FeatureCartBasic)
		d.Now = func() time.Time { return tc.now }
		if got := intakeOverdueMarks(t, d); len(got) != 2 || [2]bool{got[0], got[1]} != tc.want {
			t.Errorf("%s: overdue = %v, quiero %v", name, got, tc.want)
		}
	}
}

// TestMountIntakes_OneInstantPerResponse: dos solicitudes con el MISMO plazo salen con la misma
// marca aunque el reloj cruce el plazo entre una lectura y la siguiente.
func TestMountIntakes_OneInstantPerResponse(t *testing.T) {
	twin := intakeDue()
	twin.ID = "in-1b"
	ticks := 0
	d := intakeDeps(&intakeServiceSpy{page: intakes.Page{Intakes: []intakes.Intake{intakeDue(), twin}}}, entitlements.FeatureCartBasic)
	d.Now = func() time.Time {
		ticks++
		return intakeNow.Add(time.Duration(ticks-2) * time.Hour) // 1ª lectura: antes del plazo; 2ª: justo
	}
	got := intakeOverdueMarks(t, d)
	if len(got) != 2 || got[0] != got[1] {
		t.Errorf("overdue = %v con el mismo plazo; quiero la misma marca en las dos (un instante por respuesta)", got)
	}
	if ticks != 1 {
		t.Errorf("la página leyó el reloj %d veces, quiero 1: una lectura por fila puede cruzar el plazo a media respuesta", ticks)
	}
}

// TestMountIntakes_NilClockIsTheWallClock: sin reloj inyectado manda time.Now. Las fechas están
// tan lejos del presente que la marca no depende del día en que corra el test.
func TestMountIntakes_NilClockIsTheWallClock(t *testing.T) {
	past, future := intakeDue(), intakeDue()
	past.UpdatedAt = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	future.UpdatedAt = time.Date(2999, 1, 1, 0, 0, 0, 0, time.UTC)
	d := intakeDeps(&intakeServiceSpy{page: intakes.Page{Intakes: []intakes.Intake{past, future}}}, entitlements.FeatureCartBasic)
	d.Now = nil
	if got := intakeOverdueMarks(t, d); len(got) != 2 || !got[0] || got[1] {
		t.Errorf("overdue = %v sin reloj inyectado, quiero [true false]", got)
	}
}

// intakeDetailFixture es una solicitud completa: dos líneas (una personalizada) y los TRES casos
// del literal del cliente — hay texto, nunca lo hubo, se podó.
func intakeDetailFixture() intakes.Detail {
	at := time.Date(2026, 10, 5, 12, 0, 0, 0, intakeZone)
	return intakes.Detail{
		Intake: intakeDue(),
		Items: []intakes.Item{
			{SKU: "HAMB", Label: "Hamburguesa", Customization: "sin cebolla", Qty: 2, UnitPrice: 4.5, AddedAt: at},
			{SKU: "BEB", Label: "Bebida", Qty: 1, UnitPrice: 0},
		},
		Revisions: []intakes.Revision{
			{IntakeID: "in-1", RevisionNo: 1, Kind: intakes.RevisionKindCart, CreatedAt: at,
				Payload: json.RawMessage(`{"version":1,"total":9,"lines":[{"sku":"HAMB","qty":2}]}`)},
			{IntakeID: "in-1", RevisionNo: 2, Kind: intakes.RevisionKindInterpreted, CreatedAt: at.Add(time.Minute),
				Payload:      json.RawMessage(`{"version":1,"source_text":"dos hamburguesas sin cebolla","lines":[]}`),
				RenderedText: "Hamburguesa · 2 × $4.50", CreatedBy: intakes.RevisionBySystem},
			{IntakeID: "in-1", RevisionNo: 3, Kind: intakes.RevisionKindInterpreted, CreatedAt: at.Add(2 * time.Minute),
				Payload:   json.RawMessage(`{"version":1,"lines":[]}`),
				CreatedBy: intakes.RevisionBySystem, LiteralPrunedAt: at.Add(72 * time.Hour)},
		},
		BuyerDataPresent: true,
	}
}

const intakeDetailBody = `{` + intakeDueHeader + `,"items":[` +
	`{"sku":"HAMB","label":"Hamburguesa","customization":"sin cebolla","qty":2,"unit_price":4.5},` +
	`{"sku":"BEB","label":"Bebida","customization":"","qty":1,"unit_price":0}],"revisions":[` +
	`{"revision_no":1,"kind":"cart","payload":{"version":1,"total":9,"lines":[{"sku":"HAMB","qty":2}]},"created_at":"2026-10-05T15:00:00Z"},` +
	`{"revision_no":2,"kind":"interpreted","payload":{"version":1,"source_text":"dos hamburguesas sin cebolla","lines":[]},` +
	`"rendered_text":"Hamburguesa · 2 × $4.50","created_by":"system","created_at":"2026-10-05T15:01:00Z"},` +
	`{"revision_no":3,"kind":"interpreted","payload":{"version":1,"lines":[]},"created_by":"system",` +
	`"created_at":"2026-10-05T15:02:00Z","literal_pruned_at":"2026-10-08T15:00:00Z"}],` +
	`"allowed_transitions":["cancelled","confirmed","needs_info","rejected"],"buyer_data_present":true}`

// TestMountIntakes_DetailBody: el detalle, byte a byte, y el MISMO por las cuatro rutas que lo
// responden. Fija de una vez: el contacto opaco, la personalización que viaja también vacía, el
// payload crudo, las claves que se omiten vacías y los tres casos del literal (source_text
// presente · ausente sin sello · ausente con literal_pruned_at).
func TestMountIntakes_DetailBody(t *testing.T) {
	for _, r := range intakeRoutes {
		if r.call != "Get" && r.call != "ReplaceItems" && r.call != "Approve" && r.call != "RequestInfo" {
			continue
		}
		svc := &intakeServiceSpy{detail: intakeDetailFixture()}
		rec := intakeDo(t, svc, r.method, r.target, r.body)
		wantCode(t, r.id, rec, http.StatusOK)
		wantExactBody(t, r.id, rec, intakeDetailBody)
	}
}

// TestMountIntakes_DetailEmptyCollectionsAreArrays: sin líneas ni revisiones, y en un estado
// terminal, las tres listas salen [] y nunca null — «no hay acciones» y «no sé» se pintan distinto.
func TestMountIntakes_DetailEmptyCollectionsAreArrays(t *testing.T) {
	in := intakeDue()
	in.Status = intakes.StatusCancelled
	rec := intakeDo(t, &intakeServiceSpy{detail: intakes.Detail{Intake: in}}, http.MethodGet, intakeTarget, "")
	wantCode(t, "G2", rec, http.StatusOK)
	var body map[string]json.RawMessage
	wantJSON(t, "G2", rec, &body)
	for _, key := range []string{"items", "revisions", "allowed_transitions"} {
		if string(body[key]) != "[]" {
			t.Errorf("%s = %s, quiero []", key, body[key])
		}
	}
	if string(body["overdue"]) != "false" || string(body["buyer_data_present"]) != "false" {
		t.Errorf("overdue = %s y buyer_data_present = %s; quiero las dos claves presentes y en false (una cancelada no espera a nadie)",
			body["overdue"], body["buyer_data_present"])
	}
}

// TestMountIntakes_AllowedTransitionsFollowTheStatus: los destinos salen de la máquina de estados
// del dominio, en su orden.
func TestMountIntakes_AllowedTransitionsFollowTheStatus(t *testing.T) {
	for status, want := range map[string]string{
		intakes.StatusOpen:      `["abandoned","cancelled","confirmed","pending_approval"]`,
		intakes.StatusConfirmed: `["cancelled","deposit_requested","pending_approval","settled"]`,
		intakes.StatusNeedsInfo: `["cancelled","pending_approval"]`,
		intakes.StatusAbandoned: `[]`,
		intakes.StatusExpired:   `[]`,
	} {
		in := intakeDue()
		in.Status = status
		rec := intakeDo(t, &intakeServiceSpy{detail: intakes.Detail{Intake: in}}, http.MethodGet, intakeTarget, "")
		var body map[string]json.RawMessage
		wantJSON(t, status, rec, &body)
		if string(body["allowed_transitions"]) != want {
			t.Errorf("%s: allowed_transitions = %s, quiero %s", status, body["allowed_transitions"], want)
		}
	}
}

// TestMountIntakes_StatusAnswersTheSameHeader: G3 responde la cabecera —la misma proyección que
// la fila de G1—, sin líneas ni revisiones.
func TestMountIntakes_StatusAnswersTheSameHeader(t *testing.T) {
	svc := &intakeServiceSpy{header: intakeDue()}
	rec := intakeDo(t, svc, http.MethodPost, intakeTarget+"/status", `{"status":"confirmed"}`)
	wantCode(t, "G3", rec, http.StatusOK)
	wantExactBody(t, "G3", rec, `{`+intakeDueHeader+`}`)
}
