package reanalisis_test

// reanalisis_errors_test.go — el contrato de reanalisis_errors.go: los TEXTOS de los
// desenlaces con nombre (observables: acaban en el log y, en el caso de las razones, en
// el cuerpo del 422) y la forma en que se leen.

import (
	"errors"
	"fmt"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/reanalisis"
)

// TestErrors_TextsAreTheLiteralOnes: cada desenlace rinde el texto del viejo, byte a byte.
func TestErrors_TextsAreTheLiteralOnes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"invalid via, vocabulary", reanalisis.InvalidViaError{Via: "chatgpt"},
			`reanalisis: "chatgpt" no es una vía (local|api)`},
		{"invalid via, contradicts the configured one", reanalisis.InvalidViaError{Via: "api", Configured: "local"},
			`reanalisis: la vía "api" no es la configurada por el tenant ("local")`},
		{"feature missing", reanalisis.FeatureMissingError{Feature: "llm_intake"},
			`reanalisis: el tenant no tiene la capacidad "llm_intake"`},
		{"credentials missing", reanalisis.CredentialsMissingError{Via: "api"},
			`reanalisis: la vía "api" exige credencial y consentimiento en tenant_llm`},
		{"source unavailable", reanalisis.SourceUnavailableError{Reason: reanalisis.ReasonPurged},
			`reanalisis: no hay literal original que re-analizar (purged)`},
		{"in progress", reanalisis.InProgressError{JobID: "job-7"},
			`reanalisis: el evento ya tiene un job vivo (job-7)`},
		{"not wired", reanalisis.ErrNotWired,
			`reanalisis: el servicio necesita log, solicitudes, hilo, jobs, compositor, features y config LLM`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := c.err.Error(); got != c.want {
				t.Fatalf("texto = %q; se esperaba %q", got, c.want)
			}
		})
	}
}

// TestErrors_EachMethodRendersItsOwnText llama al método de cada tipo por su nombre: el
// texto es de CADA tipo, no de un formateador común.
func TestErrors_EachMethodRendersItsOwnText(t *testing.T) {
	t.Parallel()
	got := []string{
		reanalisis.InvalidViaError{Via: ""}.Error(),
		reanalisis.FeatureMissingError{Feature: "api_llm"}.Error(),
		reanalisis.CredentialsMissingError{Via: "api"}.Error(),
		reanalisis.SourceUnavailableError{Reason: reanalisis.ReasonNeverStored}.Error(),
		reanalisis.InProgressError{JobID: ""}.Error(),
	}
	want := []string{
		`reanalisis: "" no es una vía (local|api)`,
		`reanalisis: el tenant no tiene la capacidad "api_llm"`,
		`reanalisis: la vía "api" exige credencial y consentimiento en tenant_llm`,
		`reanalisis: no hay literal original que re-analizar (never_stored)`,
		`reanalisis: el evento ya tiene un job vivo ()`,
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("texto %d = %q; se esperaba %q", i, got[i], want[i])
		}
	}
}

// TestReasons_AreTheContractVocabulary: las dos razones viajan al cliente en el cuerpo
// del 422; sus valores son del contrato §8.1.
func TestReasons_AreTheContractVocabulary(t *testing.T) {
	t.Parallel()
	if reanalisis.ReasonPurged != "purged" {
		t.Errorf("ReasonPurged = %q; se esperaba %q", reanalisis.ReasonPurged, "purged")
	}
	if reanalisis.ReasonNeverStored != "never_stored" {
		t.Errorf("ReasonNeverStored = %q; se esperaba %q", reanalisis.ReasonNeverStored, "never_stored")
	}
}

// TestErrors_AreReadByValueThroughAWrap: son tipos VALOR y la cara HTTP los lee con
// errors.As aunque lleguen envueltos, con su dato dentro.
func TestErrors_AreReadByValueThroughAWrap(t *testing.T) {
	t.Parallel()
	wrap := func(err error) error { return fmt.Errorf("capa de arriba: %w", err) }

	var via reanalisis.InvalidViaError
	if !errors.As(wrap(reanalisis.InvalidViaError{Via: "api", Configured: "local"}), &via) ||
		via.Via != "api" || via.Configured != "local" {
		t.Errorf("InvalidViaError no se leyó entero: %+v", via)
	}
	var feature reanalisis.FeatureMissingError
	if !errors.As(wrap(reanalisis.FeatureMissingError{Feature: "api_llm"}), &feature) || feature.Feature != "api_llm" {
		t.Errorf("FeatureMissingError no se leyó entero: %+v", feature)
	}
	var cred reanalisis.CredentialsMissingError
	if !errors.As(wrap(reanalisis.CredentialsMissingError{Via: "api"}), &cred) || cred.Via != "api" {
		t.Errorf("CredentialsMissingError no se leyó entero: %+v", cred)
	}
	var source reanalisis.SourceUnavailableError
	if !errors.As(wrap(reanalisis.SourceUnavailableError{Reason: reanalisis.ReasonPurged}), &source) ||
		source.Reason != reanalisis.ReasonPurged {
		t.Errorf("SourceUnavailableError no se leyó entero: %+v", source)
	}
	var busy reanalisis.InProgressError
	if !errors.As(wrap(reanalisis.InProgressError{JobID: "job-7"}), &busy) || busy.JobID != "job-7" {
		t.Errorf("InProgressError no se leyó entero: %+v", busy)
	}
	if !errors.Is(wrap(reanalisis.ErrNotWired), reanalisis.ErrNotWired) {
		t.Error("ErrNotWired no se reconoce envuelto")
	}
}
