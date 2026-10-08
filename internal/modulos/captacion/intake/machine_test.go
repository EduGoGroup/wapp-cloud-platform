//go:build pendiente

package intake

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestStages_AreTheClosedVocabularyInMachineOrder: las cinco etapas son, byte a byte, las del
// CHECK `intake_jobs_stage_check` de la 0072, y StageIndex las conoce a las cinco, en el orden
// de la máquina, y a ninguna más.
func TestStages_AreTheClosedVocabularyInMachineOrder(t *testing.T) {
	got := []string{StageP2, StageP3, StageP4, StageMatch, StageDraft}
	want := []string{"p2", "p3", "p4", "match", "draft"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("etapa %d = %q, quería %q", i, got[i], want[i])
		}
		if idx := StageIndex(want[i]); idx != i {
			t.Errorf("StageIndex(%q) = %d, quería %d", want[i], idx, i)
		}
	}
	for _, unknown := range []string{"", "p1", "p5", "P2", " p2", "p2 ", "done", "pending", "ｐ２", "p 2"} {
		if idx := StageIndex(unknown); idx != -1 {
			t.Errorf("StageIndex(%q) = %d, quería -1: no es una etapa", unknown, idx)
		}
	}
}

// TestIsTerminal_OnlyDoneAndFailed: los terminales son los que disparan INV-13. Un estado que
// se creyera terminal sin serlo dejaría el literal puesto.
func TestIsTerminal_OnlyDoneAndFailed(t *testing.T) {
	cases := map[string]bool{
		StatusAggregating: false, StatusPending: false, StatusProcessing: false,
		StatusDone: true, StatusFailed: true,
		"": false, "DONE": false, "done ": false, "cancelled": false,
	}
	for status, want := range cases {
		if got := IsTerminal(status); got != want {
			t.Errorf("IsTerminal(%q) = %v, quería %v", status, got, want)
		}
	}
}

// TestArtifact_Validate: la puerta de «un artefacto inválido JAMÁS se persiste», con sus cinco
// causas y el texto de cada una byte a byte.
//
// 🔴 EL CASO QUE MUERDE es el objeto JSON válido SIN `version`: los rotos los rechazaría también
// el `::jsonb` de Postgres; ese no, y es el único que prueba que esta puerta existe.
func TestArtifact_Validate(t *testing.T) {
	cases := []struct {
		name    string
		a       Artifact
		wantErr string // "" = válido; si termina en ": " es el prefijo de un error envuelto
	}{
		{"minimal object with version", Artifact{StageP2, json.RawMessage(`{"version":1}`)}, ""},
		{"every stage is accepted", Artifact{StageDraft, json.RawMessage(`{"version":3,"lines":[]}`)}, ""},
		{"version with surrounding whitespace", Artifact{StageP4, json.RawMessage(" {\n\t\"version\" : 2 } ")}, ""},
		{"unknown stage", Artifact{"p5", json.RawMessage(`{"version":1}`)},
			`intake: etapa "p5" fuera del vocabulario [p2 p3 p4 match draft]`},
		{"empty stage", Artifact{"", json.RawMessage(`{"version":1}`)},
			`intake: etapa "" fuera del vocabulario [p2 p3 p4 match draft]`},
		{"upper-case stage", Artifact{"P2", json.RawMessage(`{"version":1}`)},
			`intake: etapa "P2" fuera del vocabulario [p2 p3 p4 match draft]`},
		{"nil payload", Artifact{StageP2, nil}, `intake: artefacto de la etapa "p2" vacío`},
		{"empty payload", Artifact{StageP3, json.RawMessage{}}, `intake: artefacto de la etapa "p3" vacío`},
		{"broken JSON", Artifact{StageP2, json.RawMessage(`{"version":1`)},
			`intake: artefacto de la etapa "p2" no es un objeto JSON válido: `},
		{"an array", Artifact{StageP2, json.RawMessage(`[{"version":1}]`)},
			`intake: artefacto de la etapa "p2" no es un objeto JSON válido: `},
		{"a scalar", Artifact{StageP2, json.RawMessage(`3`)},
			`intake: artefacto de la etapa "p2" no es un objeto JSON válido: `},
		{"JSON null", Artifact{StageP2, json.RawMessage(`null`)},
			"intake: artefacto de la etapa \"p2\" sin campo `version` (los artefactos son versionados, design §3.2)"},
		{"object without version", Artifact{StageP2, json.RawMessage(`{"ideas":[]}`)},
			"intake: artefacto de la etapa \"p2\" sin campo `version` (los artefactos son versionados, design §3.2)"},
		{"version key in another case", Artifact{StageP2, json.RawMessage(`{"Version":1}`)},
			"intake: artefacto de la etapa \"p2\" sin campo `version` (los artefactos son versionados, design §3.2)"},
		{"version zero", Artifact{StageP2, json.RawMessage(`{"version":0}`)},
			"intake: artefacto de la etapa \"p2\" con `version` inválida: se espera un entero >= 1"},
		{"negative version", Artifact{StageP2, json.RawMessage(`{"version":-1}`)},
			"intake: artefacto de la etapa \"p2\" con `version` inválida: se espera un entero >= 1"},
		{"version as string", Artifact{StageP2, json.RawMessage(`{"version":"1"}`)},
			"intake: artefacto de la etapa \"p2\" con `version` inválida: se espera un entero >= 1"},
		{"fractional version", Artifact{StageP2, json.RawMessage(`{"version":1.5}`)},
			"intake: artefacto de la etapa \"p2\" con `version` inválida: se espera un entero >= 1"},
		{"null version", Artifact{StageP2, json.RawMessage(`{"version":null}`)},
			"intake: artefacto de la etapa \"p2\" con `version` inválida: se espera un entero >= 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.a.Validate()
			switch {
			case tc.wantErr == "":
				if err != nil {
					t.Errorf("Validate() = %v, quería nil", err)
				}
			case err == nil:
				t.Errorf("Validate() = nil, quería %q", tc.wantErr)
			case strings.HasSuffix(tc.wantErr, ": "):
				if !strings.HasPrefix(err.Error(), tc.wantErr) || len(err.Error()) == len(tc.wantErr) {
					t.Errorf("Validate() = %q, quería el prefijo %q y su causa", err, tc.wantErr)
				}
			case err.Error() != tc.wantErr:
				t.Errorf("Validate() = %q, quería %q", err, tc.wantErr)
			}
		})
	}
}

// TestArtifact_Validate_ErrorNeverQuotesThePayload: el payload de una etapa puede llevar literal
// del cliente (P2 guarda `evidence`, que son frases suyas). Un error que lo vuelque acaba en el
// log, y ADR-0034 lo prohíbe.
func TestArtifact_Validate_ErrorNeverQuotesThePayload(t *testing.T) {
	const secret = "quiero-dos-tortas-para-el-sabado"
	payloads := []string{
		`{"evidence":"` + secret + `"`,              // JSON roto
		`["` + secret + `"]`,                        // un array
		`{"evidence":"` + secret + `"}`,             // sin version
		`{"version":"` + secret + `"}`,              // version inválida
		`{"version":0,"evidence":"` + secret + `"}`, // version cero
	}
	for _, p := range payloads {
		err := Artifact{Stage: StageP2, Payload: json.RawMessage(p)}.Validate()
		if err == nil {
			t.Fatalf("Validate(%s) = nil, quería error", p)
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("el error cita el payload: %q", err)
		}
	}
}

// TestReanalysis_IsFromOwner: LA pregunta que gatea las diferencias de conducta del re-análisis
// es «¿lo pidió el dueño?», y el único valor que la contesta con sí es RequestedByOwner, "owner".
func TestReanalysis_IsFromOwner(t *testing.T) {
	if RequestedByOwner != "owner" {
		t.Errorf("RequestedByOwner = %q, quería \"owner\"", RequestedByOwner)
	}
	cases := map[string]bool{RequestedByOwner: true, "": false, "system": false, "Owner": false, "owner ": false}
	for role, want := range cases {
		r := Reanalysis{RequestedBy: role, Via: "api", Source: "both", From: 2}
		if got := r.IsFromOwner(); got != want {
			t.Errorf("Reanalysis{RequestedBy: %q}.IsFromOwner() = %v, quería %v", role, got, want)
		}
	}
	if (Reanalysis{}).IsFromOwner() {
		t.Error("el Reanalysis cero (pipeline normal) dice ser del dueño")
	}
}

// TestClaimedJob_ZeroValueIsANormalPipelineJob: un job reclamado sin contexto de re-análisis es
// del pipeline normal, y lleva en el claim los intentos consumidos y el sobre.
func TestClaimedJob_ZeroValueIsANormalPipelineJob(t *testing.T) {
	job := ClaimedJob{ID: "job-1", Attempts: 2, MessageTS: time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)}
	if job.Reanalysis.IsFromOwner() {
		t.Error("un ClaimedJob sin contexto dice ser un re-análisis del dueño")
	}
	if job.SourceText.Complete() || job.Stage != "" || len(job.Artifacts) != 0 || len(job.SourceRefs) != 0 {
		t.Errorf("ClaimedJob sin rellenar = %+v, quería sobre vacío, sin etapa y sin artefactos", job)
	}
}

// TestPipelineStore_IsSevenOperations_AndTheAdapterSatisfiesIt: el puerto del worker son los dos
// reclamos y las cinco transiciones, y lo satisface *Postgres (el mismo objeto que JobStore: una
// tabla, un pool).
func TestPipelineStore_IsSevenOperations_AndTheAdapterSatisfiesIt(t *testing.T) {
	type seven interface {
		ClaimNext(ctx context.Context) (ClaimedJob, bool, error)
		ClaimNextIgnoringBackoff(ctx context.Context, tenantID string) (ClaimedJob, bool, error)
		SaveStage(ctx context.Context, jobID string, a Artifact) (bool, error)
		Release(ctx context.Context, jobID string) (bool, error)
		Retry(ctx context.Context, jobID string, next time.Time) (bool, error)
		Finish(ctx context.Context, jobID, intakeID string) (bool, error)
		Fail(ctx context.Context, jobID, reason string) (bool, error)
	}
	var port PipelineStore = (*Postgres)(nil)
	var narrow seven = port
	port = narrow
	if _, isQueue := port.(JobStore); !isQueue {
		t.Error("el adaptador que satisface PipelineStore no satisface JobStore: tienen que ser el mismo objeto")
	}
}
