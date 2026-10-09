//go:build pendiente

package apipublica_test

// reanalyze_errors_test.go — la política de códigos de H1 del contrato de MountReanalyze: qué
// cuerpo y qué código le toca a cada error que el servicio ya decidió. El doble y los auxiliares
// están en reanalyze_test.go.

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/reanalisis"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

const (
	reanalyzeMsgNotFound = "solicitud no encontrada"
	reanalyzeMsgFailed   = "no se pudo pedir el re-análisis de la solicitud"

	// reanalyzeDSN es lo que un 500 no puede repetir: el error de un driver puede llevarlo dentro.
	//nolint:gosec // G101: no es una credencial, es el DSN inventado de un test
	reanalyzeDSN = "postgres://usuario:secreto@host/bd"
)

// reanalyzeOutcome es un desenlace con nombre: el error del servicio y lo que la cara contesta.
type reanalyzeOutcome struct {
	name string
	err  error
	code int
	body string
}

// reanalyzeOutcomes son los siete desenlaces con cuerpo propio, EN EL ORDEN del contrato (el del
// §8.1 y el de la cara vieja). TestMountReanalyze_FirstMatchWins se apoya en ese orden.
var reanalyzeOutcomes = []reanalyzeOutcome{
	{"invalid_via", reanalisis.InvalidViaError{Via: "chatgpt"},
		http.StatusBadRequest, `{"error":"invalid_via","via":"chatgpt"}`},
	{"text_too_long", intakes.NoteTooLongError{Runes: 312, Max: intakes.MaxNoteRunes},
		http.StatusBadRequest, `{"error":"text_too_long","runes":312,"max":280}`},
	{"feature_not_enabled", reanalisis.FeatureMissingError{Feature: "llm_intake"},
		http.StatusForbidden, `{"error":"feature_not_enabled","feature":"llm_intake"}`},
	{"llm_credentials_missing", reanalisis.CredentialsMissingError{Via: "api"},
		http.StatusUnprocessableEntity, `{"error":"llm_credentials_missing","via":"api"}`},
	{"not_found", intakes.ErrNotFound,
		http.StatusNotFound, `{"error":"` + reanalyzeMsgNotFound + `"}`},
	{"reanalysis_in_progress", reanalisis.InProgressError{JobID: reanalyzeJobID},
		http.StatusUnprocessableEntity, `{"error":"reanalysis_in_progress","job_id":"` + reanalyzeJobID + `"}`},
	{"source_unavailable", reanalisis.SourceUnavailableError{Reason: reanalisis.ReasonPurged},
		http.StatusUnprocessableEntity, `{"error":"source_unavailable","reason":"purged"}`},
}

// reanalyzeWantOutcome sirve una petición contra un servicio que falla con err y exige el código,
// el cuerpo byte a byte, UNA llamada al servicio y su registro de auditoría de fallo.
func reanalyzeWantOutcome(t *testing.T, err error, code int, body string) {
	t.Helper()
	svc := &reanalyzeServiceSpy{out: reanalyzeAck(), err: err}
	h, rec := reanalyzeDo(t, svc, reanalyzeTarget, `{"via":"api","text":"x"}`)
	wantCode(t, "H1", rec, code)
	wantExactBody(t, "H1", rec, body)
	reanalyzeWantCalls(t, "H1", svc, 1)
	reanalyzeWantOneAudit(t, "H1", h, "failure", code)
}

// TestMountReanalyze_Errors: cada desenlace con su código y su cuerpo, tal como lo da el servicio
// y también ENVUELTO (los tipos se leen con errors.As y el centinela con errors.Is: el texto
// demasiado largo llega siempre envuelto por el saneo).
func TestMountReanalyze_Errors(t *testing.T) {
	for _, o := range reanalyzeOutcomes {
		t.Run(o.name, func(t *testing.T) {
			reanalyzeWantOutcome(t, o.err, o.code, o.body)
		})
		t.Run(o.name+"/wrapped", func(t *testing.T) {
			reanalyzeWantOutcome(t, fmt.Errorf("reanalisis: el texto pegado no pasa el saneo: %w", o.err), o.code, o.body)
		})
	}
}

// TestMountReanalyze_ErrorBodiesCarryTheServiceData: el dato de cada cuerpo es el del error, no
// uno fijo. `via` sale SIEMPRE en el invalid_via —vacío incluido: «no mandaste vía» no es «mandaste
// una que no existe»— y `configured_via` solo cuando el rechazo es por contradecir la del tenant.
// Los dos 403 llevan cada uno su clave, y las dos razones de la fuente, la suya.
func TestMountReanalyze_ErrorBodiesCarryTheServiceData(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code int
		body string
	}{
		{"invalid_via_with_empty_via", reanalisis.InvalidViaError{},
			http.StatusBadRequest, `{"error":"invalid_via","via":""}`},
		{"invalid_via_against_the_configured_one", reanalisis.InvalidViaError{Via: "api", Configured: "local"},
			http.StatusBadRequest, `{"error":"invalid_via","via":"api","configured_via":"local"}`},
		{"text_too_long_with_its_two_figures", intakes.NoteTooLongError{Runes: 9000, Max: 7},
			http.StatusBadRequest, `{"error":"text_too_long","runes":9000,"max":7}`},
		{"feature_api_llm", reanalisis.FeatureMissingError{Feature: "api_llm"},
			http.StatusForbidden, `{"error":"feature_not_enabled","feature":"api_llm"}`},
		{"credentials_with_its_via", reanalisis.CredentialsMissingError{Via: "otra"},
			http.StatusUnprocessableEntity, `{"error":"llm_credentials_missing","via":"otra"}`},
		{"source_never_stored", reanalisis.SourceUnavailableError{Reason: reanalisis.ReasonNeverStored},
			http.StatusUnprocessableEntity, `{"error":"source_unavailable","reason":"never_stored"}`},
		{"in_progress_without_job_id", reanalisis.InProgressError{},
			http.StatusUnprocessableEntity, `{"error":"reanalysis_in_progress","job_id":""}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reanalyzeWantOutcome(t, tc.err, tc.code, tc.body)
		})
	}
}

// TestMountReanalyze_FirstMatchWins: si un error casa con dos desenlaces gana el primero de la
// lista del contrato, lo junte quien lo junte y en el orden que sea.
func TestMountReanalyze_FirstMatchWins(t *testing.T) {
	for i := 0; i+1 < len(reanalyzeOutcomes); i++ {
		first, second := reanalyzeOutcomes[i], reanalyzeOutcomes[i+1]
		t.Run(first.name+"_over_"+second.name, func(t *testing.T) {
			reanalyzeWantOutcome(t, errors.Join(second.err, first.err), first.code, first.body)
		})
	}
}

// TestMountReanalyze_NotFoundIsNever403: una solicitud ajena y una inexistente son la MISMA
// respuesta (INV-8); un 403 confirmaría que el id existe.
func TestMountReanalyze_NotFoundIsNever403(t *testing.T) {
	svc := &reanalyzeServiceSpy{err: intakes.ErrNotFound}
	_, rec := reanalyzeDo(t, svc, reanalyzeTarget, `{}`)
	wantCode(t, "solicitud ajena", rec, http.StatusNotFound)
	wantErrorBody(t, "solicitud ajena", rec, reanalyzeMsgNotFound)
}

// TestMountReanalyze_UnknownErrorIs500WithoutRepeatingIt: lo que no tiene nombre —también el
// servicio sin cablear— es un 500 con SU texto, que no repite el error (puede llevar el DSN).
func TestMountReanalyze_UnknownErrorIs500WithoutRepeatingIt(t *testing.T) {
	for name, err := range map[string]error{
		"driver_error": errors.New(reanalyzeDSN + ": conexión rechazada"),
		"wrapped":      fmt.Errorf("reanalisis: leer el hilo del evento e1: %w", errors.New(reanalyzeDSN)),
		"not_wired":    reanalisis.ErrNotWired,
		"pointer_to_a_named_error_is_not_the_named_error": &reanalisis.InProgressError{JobID: reanalyzeJobID},
	} {
		t.Run(name, func(t *testing.T) {
			svc := &reanalyzeServiceSpy{out: reanalyzeAck(), err: err}
			h, rec := reanalyzeDo(t, svc, reanalyzeTarget, `{}`)
			wantCode(t, name, rec, http.StatusInternalServerError)
			wantErrorBody(t, name, rec, reanalyzeMsgFailed)
			reanalyzeWantCalls(t, name, svc, 1)
			reanalyzeWantOneAudit(t, name, h, "failure", http.StatusInternalServerError)
		})
	}
}
