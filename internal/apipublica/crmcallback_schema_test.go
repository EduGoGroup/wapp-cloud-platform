//go:build pendiente

package apipublica_test

// crmcallback_schema_test.go — el CUERPO de G17 del contrato de MountCRMCallback: los motivos
// del 422 y, sobre todo, que la frontera y el schema PUBLICADO del verbo intake.status dan el
// mismo veredicto (D-F6-3: el esquema wapp-crm-v1 se valida desde tres tests; este es el de la
// vuelta). Es un trozo de crmcallback_test.go, partido por tema (05 E-13).

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// crmContractDir apunta al contrato PUBLICADO, no a una copia: si alguien edita el schema, este
// test lo ve sin que nadie tenga que sincronizar nada.
const crmContractDir = "../../docs/contracts/wapp-crm-v1"

// crmStatusBody arma un intake.status a partir de pares clave → JSON crudo del valor, en el
// orden dado (el orden y las repeticiones importan en algún caso adversario).
func crmStatusBody(pairs ...string) string {
	body := "{"
	for i := 0; i < len(pairs); i += 2 {
		if i > 0 {
			body += ","
		}
		body += `"` + pairs[i] + `":` + pairs[i+1]
	}
	return body + "}"
}

// crmValidPairs son los pares de un intake.status válido mínimo.
func crmValidPairs(status string) []string {
	return []string{"contract_version", `"1"`, "verb", `"intake.status"`, "intake_id", `"` + crmIntakeID + `"`,
		"status", `"` + status + `"`, "occurred_at", `"2026-08-08T12:00:00Z"`}
}

// crmWith devuelve los pares válidos con una clave cambiada (value "" = quitarla) o añadida.
func crmWith(key, value string) []string {
	base := crmValidPairs("paid")
	out := make([]string, 0, len(base)+2)
	replaced := false
	for i := 0; i < len(base); i += 2 {
		if base[i] != key {
			out = append(out, base[i], base[i+1])
			continue
		}
		replaced = true
		if value != "" {
			out = append(out, key, value)
		}
	}
	if !replaced && value != "" {
		out = append(out, key, value)
	}
	return out
}

// TestMountCRMCallback_MatchesThePublishedSchema ata las dos representaciones del contrato que
// conviven en el repo: el `intake.status.schema.json` publicado —lo que el autor de un puente
// lee— y la frontera —lo que de verdad decide el 422—. No compara implementaciones: les da los
// MISMOS cuerpos, bien firmados, y exige el MISMO veredicto (válido ⇔ no es un 422).
func TestMountCRMCallback_MatchesThePublishedSchema(t *testing.T) {
	sch, err := jsonschema.NewCompiler().Compile(filepath.Join(crmContractDir, "intake.status.schema.json"))
	if err != nil {
		t.Fatalf("compilando el schema publicado: %v", err)
	}

	cases := map[string]string{
		"valid_minimal":             crmStatusBody(crmValidPairs("paid")...),
		"valid_preparing":           crmStatusBody(crmValidPairs("preparing")...),
		"valid_delivered":           crmStatusBody(crmValidPairs("delivered")...),
		"valid_rejected":            crmStatusBody(crmValidPairs("rejected")...),
		"valid_with_external_ref":   crmStatusBody(crmWith("external_ref", `"F-2026-0001"`)...),
		"valid_empty_external_ref":  crmStatusBody(crmWith("external_ref", `""`)...),
		"valid_offset_occurred_at":  crmStatusBody(crmWith("occurred_at", `"2026-08-08T09:00:00-03:00"`)...),
		"valid_fractional_seconds":  crmStatusBody(crmWith("occurred_at", `"2026-08-08T12:00:00.123Z"`)...),
		"status_not_canonical":      crmStatusBody(crmValidPairs("shipped")...),
		"status_uppercase":          crmStatusBody(crmValidPairs("PAID")...),
		"status_with_space":         crmStatusBody(crmValidPairs("paid ")...),
		"status_lifecycle_word":     crmStatusBody(crmValidPairs("approved")...),
		"status_empty":              crmStatusBody(crmValidPairs("")...),
		"status_number":             crmStatusBody(crmWith("status", `1`)...),
		"status_null":               crmStatusBody(crmWith("status", `null`)...),
		"status_array":              crmStatusBody(crmWith("status", `["paid"]`)...),
		"tenant_in_the_body":        crmStatusBody(crmWith("tenant", `"acme"`)...),
		"tenant_id_in_the_body":     crmStatusBody(crmWith("tenant_id", `"`+tenantB+`"`)...),
		"unknown_field":             crmStatusBody(crmWith("total", `12.5`)...),
		"missing_occurred_at":       crmStatusBody(crmWith("occurred_at", "")...),
		"missing_intake_id":         crmStatusBody(crmWith("intake_id", "")...),
		"missing_status":            crmStatusBody(crmWith("status", "")...),
		"missing_verb":              crmStatusBody(crmWith("verb", "")...),
		"missing_contract_version":  crmStatusBody(crmWith("contract_version", "")...),
		"empty_intake_id":           crmStatusBody(crmWith("intake_id", `""`)...),
		"intake_id_number":          crmStatusBody(crmWith("intake_id", `42`)...),
		"verb_of_another_message":   crmStatusBody(crmWith("verb", `"intake.push"`)...),
		"verb_uppercase":            crmStatusBody(crmWith("verb", `"INTAKE.STATUS"`)...),
		"contract_version_future":   crmStatusBody(crmWith("contract_version", `"2"`)...),
		"contract_version_number":   crmStatusBody(crmWith("contract_version", `1`)...),
		"contract_version_1_dot_0":  crmStatusBody(crmWith("contract_version", `"1.0"`)...),
		"occurred_at_number":        crmStatusBody(crmWith("occurred_at", `1788263200`)...),
		"external_ref_number":       crmStatusBody(crmWith("external_ref", `2026`)...),
		"external_ref_object":       crmStatusBody(crmWith("external_ref", `{"folio":"F-1"}`)...),
		"empty_object":              `{}`,
		"array_instead_of_object":   `[` + crmStatusBody(crmValidPairs("paid")...) + `]`,
		"string_instead_of_object":  `"intake.status"`,
		"number_instead_of_object":  `42`,
		"field_name_in_other_case_": crmStatusBody(crmWith("Tenant", `"acme"`)...),
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			// Veredicto del schema publicado.
			var inst any
			if err := json.Unmarshal([]byte(body), &inst); err != nil {
				t.Fatalf("el caso no es JSON: %v", err)
			}
			schemaOK := sch.Validate(inst) == nil

			// Veredicto de la frontera: firma buena, puente activo y un reflector que encuentra
			// cualquier solicitud; lo único que puede dar 422 es el cuerpo.
			rig := newCRMRig(t)
			rig.reflector.anyIntake = true
			rec := crmSend(rig.cara, crmGood(body))
			if rec.Code != http.StatusOK && rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("la frontera respondió %d (%s); este test solo espera 200 o 422", rec.Code, rec.Body.String())
			}
			if faceOK := rec.Code == http.StatusOK; schemaOK != faceOK {
				t.Fatalf("VEREDICTOS DISTINTOS para el mismo cuerpo — el contrato publicado y el endpoint se han separado:\n"+
					"  schema=%v  frontera=%v (%s)\n  %s", schemaOK, faceOK, rec.Body.String(), body)
			}
			if rec.Code == http.StatusUnprocessableEntity && len(rig.reflector.calls) != 0 {
				t.Errorf("un cuerpo rechazado llegó al reflector: %+v", rig.reflector.calls)
			}
		})
	}
}

// TestMountCRMCallback_422Reasons: el motivo le dice al autor del puente QUÉ corregir, con el
// texto exacto, y el primero que falla decide.
func TestMountCRMCallback_422Reasons(t *testing.T) {
	const (
		notAStatus = "el cuerpo no es un intake.status válido"
		badStatus  = "status debe ser uno de: paid, preparing, delivered, rejected"
	)
	cases := []struct {
		name, body, reason string
	}{
		{"extra_field_is_named", crmStatusBody(crmWith("tenant", `"acme"`)...), `el cuerpo trae un campo que el contrato no admite: "tenant"`},
		{"extra_field_wins_over_bad_values", `{"tenant":"acme","status":"shipped"}`, `el cuerpo trae un campo que el contrato no admite: "tenant"`},
		{"wrong_type_names_the_field", crmStatusBody(crmWith("status", `7`)...), "el campo status tiene un tipo que el contrato no admite"},
		{"not_json", `esto no es json`, notAStatus},
		{"empty_body", ``, notAStatus},
		{"truncated_object", `{"contract_version":"1"`, notAStatus},
		// Sin campo que nombrar, el motivo del viejo sale con el hueco: se conserva literal.
		{"array_body_has_no_field_to_name", `[]`, "el campo  tiene un tipo que el contrato no admite"},
		{"string_body_has_no_field_to_name", `"paid"`, "el campo  tiene un tipo que el contrato no admite"},
		{"contract_version", crmStatusBody(crmWith("contract_version", `"2"`)...), `contract_version debe ser "1"`},
		{"empty_object_fails_on_the_first_rule", `{}`, `contract_version debe ser "1"`},
		{"json_null_fails_on_the_first_rule", `null`, `contract_version debe ser "1"`},
		{"verb", crmStatusBody(crmWith("verb", `"intake.push"`)...), `verb debe ser "intake.status"`},
		{"intake_id_missing", crmStatusBody(crmWith("intake_id", "")...), "intake_id es obligatorio"},
		{"intake_id_blank", crmStatusBody(crmWith("intake_id", `" \t "`)...), "intake_id es obligatorio"},
		{"status_missing", crmStatusBody(crmWith("status", "")...), "status es obligatorio"},
		{"status_unknown", crmStatusBody(crmValidPairs("shipped")...), badStatus},
		{"status_uppercase", crmStatusBody(crmValidPairs("Paid")...), badStatus},
		{"status_with_fullwidth_letters", crmStatusBody(crmValidPairs("ｐａｉｄ")...), badStatus},
		{"occurred_at_missing", crmStatusBody(crmWith("occurred_at", "")...), "occurred_at es obligatorio"},
		{"occurred_at_blank", crmStatusBody(crmWith("occurred_at", `"  "`)...), "occurred_at es obligatorio"},
		{"occurred_at_date_only", crmStatusBody(crmWith("occurred_at", `"2026-08-08"`)...), "occurred_at debe ser una marca RFC3339"},
		{"occurred_at_unix", crmStatusBody(crmWith("occurred_at", `"1788263200"`)...), "occurred_at debe ser una marca RFC3339"},
		{"occurred_at_without_zone", crmStatusBody(crmWith("occurred_at", `"2026-08-08T12:00:00"`)...), "occurred_at debe ser una marca RFC3339"},
		{"occurred_at_with_spaces", crmStatusBody(crmWith("occurred_at", `" 2026-08-08T12:00:00Z "`)...), "occurred_at debe ser una marca RFC3339"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rig := newCRMRig(t)
			rig.reflector.anyIntake = true
			rec := crmSend(rig.cara, crmGood(tc.body))
			wantCode(t, tc.name, rec, http.StatusUnprocessableEntity)
			wantErrorBody(t, tc.name, rec, tc.reason)
			if len(rig.reflector.calls) != 0 || len(rig.notifier.notices) != 0 {
				t.Errorf("%s: un cuerpo rechazado llegó al reflector o al notificador", tc.name)
			}
		})
	}
}

// TestMountCRMCallback_OldQuirksKept: cuatro rarezas de la cara vieja, conservadas y dichas en
// el contrato. Solo se decodifica el primer valor JSON, una clave repetida toma su última
// aparición, y —en estas dos la frontera es más laxa que el schema publicado— un campo a null
// cuenta como ausente y las claves casan sin distinguir mayúsculas.
func TestMountCRMCallback_OldQuirksKept(t *testing.T) {
	valid := crmStatusBody(crmValidPairs("paid")...)
	cases := []struct {
		name, body, status string
	}{
		{"trailing_garbage_is_not_looked_at", valid + ` basura {"status":"rejected"}`, "paid"},
		{"duplicate_key_last_wins", crmStatusBody(append(crmValidPairs("paid"), "status", `"delivered"`)...), "delivered"},
		{"null_external_ref_counts_as_absent", crmStatusBody(crmWith("external_ref", `null`)...), "paid"},
		{"keys_match_case_insensitively", strings.Replace(valid, `"status"`, `"STATUS"`, 1), "paid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rig := newCRMRig(t)
			rig.reflector.anyIntake = true
			rec := crmSend(rig.cara, crmGood(tc.body))
			wantCode(t, tc.name, rec, http.StatusOK)
			if len(rig.reflector.calls) != 1 || rig.reflector.calls[0].status != tc.status || rig.reflector.calls[0].externalRef != "" {
				t.Errorf("%s: el reflector recibió %+v, quiero el estado %q y external_ref vacío", tc.name, rig.reflector.calls, tc.status)
			}
		})
	}
}
