package apipublica

// intakes_llm_gate_internal_test.go — el filtro de intakes_llm_gate.go visto sin pasar por una
// ruta (05 E-4, P6: nace con el verde). Lo que se ve por el cable lo prueba
// intakes_llm_gate_test.go; aquí va lo que el cable no deja ver: que un payload sin las claves
// vuelve SIN reserializar (los mismos bytes, no unos equivalentes) y que el gate pregunta por la
// feature que toca y al tenant que toca.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

// TestIntakeHideLLMFields: lo que no lleva ninguna de las dos claves en SU nivel vuelve intacto
// —con sus espacios y su orden: ni siquiera compactado—, y lo que sí, sin ellas y con todo lo
// demás.
func TestIntakeHideLLMFields(t *testing.T) {
	untouched := []string{
		``,
		`null`,
		`7`,
		`"suggested_questions"`,
		`[{"suggested_questions":[1]}]`,
		`{ "zeta" : 1,  "alfa" : [ 2 ] }`,
		`{"lines" : [ {"sku":"B"} , {"sku":"A"} ], "version":1}`,
		`{"lines":"variant_options"}`,
		`{"lines":null}`,
		`{"lines":[null,7,"variant_options",[{"variant_options":1}]]}`,
		`{"variant_options":[1],"lines":[{"suggested_questions":[2]}]}`,
		`{"Suggested_Questions":[1],"lines":[{"VARIANT_OPTIONS":[2]}]}`,
		`{no es json`,
	}
	for _, in := range untouched {
		out, err := intakeHideLLMFields(json.RawMessage(in))
		if err != nil || string(out) != in {
			t.Errorf("intakeHideLLMFields(%s) = %s, %v; quiero el original, byte a byte", in, out, err)
		}
	}

	touched := map[string]string{
		`{"suggested_questions":["q"]}`:                                                            `{}`,
		`{"suggested_questions":[],"version":1}`:                                                   `{"version":1}`,
		`{"version":1,"suggested_questions":null,"lines":[]}`:                                      `{"lines":[],"version":1}`,
		`{"lines":[{"variant_options":[{"sku":"A#1"}],"sku":"A"}]}`:                                `{"lines":[{"sku":"A"}]}`,
		`{"lines":[{"sku":"A"},{"variant_options":null},7]}`:                                       `{"lines":[{"sku":"A"},{},7]}`,
		`{"suggested_questions":["q"],"lines":"no-es-lista","zeta":{"b":1}}`:                       `{"lines":"no-es-lista","zeta":{"b":1}}`,
		`{"suggested_questions":["q"],"lines":[{"variant_options":[1],"note":"variant_options"}]}`: `{"lines":[{"note":"variant_options"}]}`,
	}
	for in, want := range touched {
		out, err := intakeHideLLMFields(json.RawMessage(in))
		if err != nil || string(out) != want {
			t.Errorf("intakeHideLLMFields(%s) = %s, %v; quiero %s", in, out, err, want)
		}
	}
}

// intakeGateResolver es un entitlements.Resolver que apunta por qué le preguntan.
type intakeGateResolver struct {
	has     bool
	err     error
	tenant  string
	feature string
}

func (r *intakeGateResolver) Has(_ context.Context, tenantID, feature string) (bool, error) {
	r.tenant, r.feature = tenantID, feature
	return r.has, r.err
}

func (*intakeGateResolver) ListEffective(context.Context, string) (string, []string, error) {
	return "", nil, nil
}

func (*intakeGateResolver) CacheTTL() time.Duration { return 0 }

// TestIntakeApplyLLMGate: pregunta por `llm_intake` al tenant que se le da; abre SOLO con
// (true, nil) y en cualquier otro caso tapa las revisiones, una a una, sin tocar el resto de la
// respuesta.
func TestIntakeApplyLLMGate(t *testing.T) {
	const payload = `{"version":1,"suggested_questions":["q"]}`
	for name, tc := range map[string]struct {
		resolver *intakeGateResolver
		hidden   bool
	}{
		"has_the_feature":       {&intakeGateResolver{has: true}, false},
		"does_not_have_it":      {&intakeGateResolver{}, true},
		"resolver_fails":        {&intakeGateResolver{err: errors.New("bd caída")}, true},
		"true_with_an_error":    {&intakeGateResolver{has: true, err: errors.New("a medias")}, true},
		"cancelled_is_an_error": {&intakeGateResolver{has: true, err: context.Canceled}, true},
	} {
		resp := intakeDetailResponse{
			intakeDTO: intakeDTO{ID: "in-1"},
			Revisions: []intakeRevisionDTO{
				{RevisionNo: 1, Payload: json.RawMessage(payload), LiteralPrunedAt: "2026-09-01T12:00:00Z"},
				{RevisionNo: 2, Payload: json.RawMessage(`{"zeta":1,"alfa":2}`)},
			},
		}
		if err := intakeApplyLLMGate(context.Background(), tc.resolver, "tenant-x", &resp); err != nil {
			t.Fatalf("%s: el gate falló: %v", name, err)
		}
		if tc.resolver.tenant != "tenant-x" || tc.resolver.feature != entitlements.FeatureLLMIntake {
			t.Errorf("%s: preguntó por %q al tenant %q; quiero llm_intake a tenant-x", name, tc.resolver.feature, tc.resolver.tenant)
		}
		want := payload
		if tc.hidden {
			want = `{"version":1}`
		}
		if got := string(resp.Revisions[0].Payload); got != want {
			t.Errorf("%s: payload %s, quiero %s", name, got, want)
		}
		if resp.Revisions[0].LiteralPrunedAt == "" || string(resp.Revisions[1].Payload) != `{"zeta":1,"alfa":2}` || resp.ID != "in-1" {
			t.Errorf("%s: el gate tocó algo más que las dos claves: %+v", name, resp)
		}
	}
}

// TestIntakeWriteDetail: la única salida del detalle responde 200 en JSON con la solicitud ya
// filtrada y con la marca del plazo decidida contra el instante que se le pasa.
func TestIntakeWriteDetail(t *testing.T) {
	touched := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	detail := intakes.Detail{
		Intake: intakes.Intake{ID: "in-1", Status: intakes.StatusPendingApproval, CreatedAt: touched, UpdatedAt: touched},
		Revisions: []intakes.Revision{{RevisionNo: 1, Kind: intakes.RevisionKindInterpreted, CreatedAt: touched,
			Payload: json.RawMessage(`{"suggested_questions":["q"],"version":1}`)}},
	}
	body := func(overdue bool, payload string) string {
		return fmt.Sprintf(`{"id":"in-1","contact_id":"","session_id":"","status":"pending_approval","total":0,"customer_note":"",`+
			`"overdue":%t,"created_at":"2026-10-07T12:00:00Z","updated_at":"2026-10-07T12:00:00Z","items":[],"revisions":[`+
			`{"revision_no":1,"kind":"interpreted","payload":%s,"created_at":"2026-10-07T12:00:00Z"}],`+
			`"allowed_transitions":["cancelled","confirmed","needs_info","rejected"],"buyer_data_present":false}`, overdue, payload)
	}

	for name, tc := range map[string]struct {
		resolver *intakeGateResolver
		at       time.Time
		want     string
	}{
		"without_the_feature_in_time": {&intakeGateResolver{}, touched.Add(time.Hour), body(false, `{"version":1}`)},
		"with_the_feature_overdue": {&intakeGateResolver{has: true}, touched.Add(intakes.QuoteDeadline),
			body(true, `{"suggested_questions":["q"],"version":1}`)},
	} {
		rec := httptest.NewRecorder()
		intakeWriteDetail(context.Background(), rec, tc.resolver, "tenant-x", detail, tc.at)
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" {
			t.Errorf("%s: código %d y Content-Type %q; quiero 200 y application/json", name, rec.Code, rec.Header().Get("Content-Type"))
		}
		if got := rec.Body.String(); got != tc.want {
			t.Errorf("%s: cuerpo\n  %s\nquiero\n  %s", name, got, tc.want)
		}
	}
}
