package reanalisis_test

// reanalisis_checks_test.go — los escalones de LECTURA 1–8 de Reanalyze, uno por
// promesa: la forma, los dos gates, la vía que afirma y no conmuta, la credencial, la
// solicitud y el job vivo. Cada rechazo se mira dos veces: por el error con nombre y
// por lo que NO se llegó a preguntar ni a escribir (la bitácora). Un error correcto
// después de haber leído lo que no tocaba sigue siendo una fuga.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/reanalisis"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

// --- escalones 1-2 · la forma ------------------------------------------------

// TestReanalyze_ViaOutsideVocabulary_IsRejectedBeforeAnyPort: el rechazo de VOCABULARIO
// no habla de la vía configurada y no toca ningún puerto.
func TestReanalyze_ViaOutsideVocabulary_IsRejectedBeforeAnyPort(t *testing.T) {
	t.Parallel()
	b := newBench(t)

	_, err := b.ask(reanalisis.Request{Via: "chatgpt"})

	var via reanalisis.InvalidViaError
	if !errors.As(err, &via) {
		t.Fatalf("error = %v; se esperaba InvalidViaError", err)
	}
	if via.Via != "chatgpt" || via.Configured != "" {
		t.Errorf("InvalidViaError = %+v; lleva la vía tal cual y Configured vacío", via)
	}
	b.requireSteps(t)
}

// TestReanalyze_ShapeBeatsTheGate es el orden del §8.1 escrito como test: «400,
// validación de forma, ANTES de cualquier gate». Es la razón por la que el gate no es un
// middleware de la ruta: con él delante, este caso respondería 403.
func TestReanalyze_ShapeBeatsTheGate(t *testing.T) {
	t.Parallel()
	b := newBench(t, withoutFeatures)

	_, err := b.ask(reanalisis.Request{Via: "chatgpt"})

	var via reanalisis.InvalidViaError
	if !errors.As(err, &via) {
		t.Fatalf("error = %v; un tenant SIN llm_intake y con vía inválida recibe InvalidViaError", err)
	}
	if len(b.features.asked) != 0 {
		t.Errorf("se preguntó por %v; la forma se rechaza sin mirar las features", b.features.asked)
	}
}

// TestReanalyze_TextTooLong_IsRejectedNotTruncated: el saneo comparte tope con la
// indicación del cliente (280 runas) y RECHAZA en vez de truncar —recortar «…y sin
// maní» pierde el final, y el final es donde va el alérgeno—. Es forma: ningún puerto.
func TestReanalyze_TextTooLong_IsRejectedNotTruncated(t *testing.T) {
	t.Parallel()
	b := newBench(t)

	_, err := b.ask(reanalisis.Request{Text: strings.Repeat("a", 281)})

	var tooLong intakes.NoteTooLongError
	if !errors.As(err, &tooLong) {
		t.Fatalf("error = %v; se esperaba poder leer el NoteTooLongError", err)
	}
	if tooLong.Runes != 281 || tooLong.Max != intakes.MaxNoteRunes {
		t.Errorf("NoteTooLongError = %+v; se esperaban 281 runas sobre %d", tooLong, intakes.MaxNoteRunes)
	}
	if want := "reanalisis: el texto pegado no pasa el saneo: " + tooLong.Error(); err.Error() != want {
		t.Errorf("texto = %q; se esperaba %q", err.Error(), want)
	}
	b.requireSteps(t)
}

// TestReanalyze_TextAtTheLimit_Passes: 280 runas ya saneadas caben.
func TestReanalyze_TextAtTheLimit_Passes(t *testing.T) {
	t.Parallel()
	b := newBench(t)
	text := strings.Repeat("ñ", intakes.MaxNoteRunes)

	b.mustAsk(t, reanalisis.Request{Text: text})

	if len(b.thread.written) != 1 || b.thread.written[0] != text {
		t.Errorf("filas pegadas = %d; se esperaba el texto entero, sin recortar", len(b.thread.written))
	}
}

// --- escalón 3 · el gate del nivel --------------------------------------------

// TestReanalyze_WithoutLLMIntake_NothingElseIsAsked son las dos caras de la fuga de
// existencia: sin `llm_intake` la respuesta es FeatureMissingError aunque el evento no
// tenga material (con el orden invertido: `source_unavailable`) o la solicitud sea de
// otro tenant (con el orden invertido: 404, un oráculo de qué solicitudes existen en
// otros tenants, INV-8). El assert fuerte es la bitácora: ni se preguntó.
func TestReanalyze_WithoutLLMIntake_NothingElseIsAsked(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*bench){
		"event without material":   func(b *bench) { b.thread.entries = nil },
		"intake of another tenant": func(b *bench) { b.intakes.err = intakes.ErrNotFound },
		"live job on the event":    func(b *bench) { b.seedLiveJob(intake.StatusAggregating) },
	}
	for name, tweak := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			b := newBench(t, withoutFeatures, tweak)

			_, err := b.ask(reanalisis.Request{})

			var feature reanalisis.FeatureMissingError
			if !errors.As(err, &feature) || feature.Feature != entitlements.FeatureLLMIntake {
				t.Fatalf("error = %v; se esperaba FeatureMissingError{llm_intake}", err)
			}
			if errors.Is(err, intakes.ErrNotFound) {
				t.Error("sin la capacidad no se contesta si la solicitud existe")
			}
			b.requireSteps(t, stepLevelGate)
			b.requireNoWrites(t)
		})
	}
}

// TestReanalyze_ResolverDown_FailsClosed: la política del middleware, aplicada en
// código, en los DOS gates. Un resolver caído responde «no la tienes» y su error NO se
// propaga: un 5xx invita a reintentar hasta colarse.
func TestReanalyze_ResolverDown_FailsClosed(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		tweaks []func(*bench)
		key    string
	}{
		"level gate": {nil, entitlements.FeatureLLMIntake},
		"via gate":   {[]func(*bench){withAPIConfig, withAPILLM}, entitlements.FeatureAPILLM},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			b := newBench(t, append(c.tweaks, func(b *bench) {
				b.features.errFor = map[string]error{c.key: errInfra}
			})...)

			_, err := b.ask(reanalisis.Request{})

			var feature reanalisis.FeatureMissingError
			if !errors.As(err, &feature) || feature.Feature != c.key {
				t.Fatalf("error = %v; se esperaba FeatureMissingError{%s}", err, c.key)
			}
			if errors.Is(err, errInfra) {
				t.Error("el fallo de infraestructura se propagó: sería un 500")
			}
			b.requireNoWrites(t)
		})
	}
}

// --- escalón 4 · la vía afirma, no conmuta --------------------------------------

// TestReanalyze_WithoutLLMIntake_GateBeatsViaMismatch fija la ÚNICA desviación del orden
// del §8.1: la mitad de COINCIDENCIA del 400 va después del gate base, porque su cuerpo
// publica la configuración LLM del tenant.
func TestReanalyze_WithoutLLMIntake_GateBeatsViaMismatch(t *testing.T) {
	t.Parallel()
	b := newBench(t, withAPIConfig, withoutFeatures)

	// `local` contradice la vía configurada (`api`): con la feature sería InvalidViaError.
	_, err := b.ask(reanalisis.Request{Via: "local"})

	var feature reanalisis.FeatureMissingError
	if !errors.As(err, &feature) || feature.Feature != entitlements.FeatureLLMIntake {
		t.Fatalf("error = %v; sin llm_intake no se contesta la configuración del tenant", err)
	}
	b.requireSteps(t, stepLevelGate)
}

// TestReanalyze_ViaThatContradictsTheEffectiveOne_IsInvalid: `via` tiene que coincidir
// con la vía EFECTIVA, siempre — una regla, no dos. 🔴 «Sin fila» es el caso que corre
// en campo y `local` es ahí un valor REAL, no una ausencia: `{"via":"api"}` lo
// contradice igual (el defecto del `hayFila &&`, 2026-08-27). La petición muere ANTES
// del gate de la vía y de la solicitud, aunque el tenant lo tenga todo.
func TestReanalyze_ViaThatContradictsTheEffectiveOne_IsInvalid(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		tweak      func(*bench)
		via        string
		configured string
	}{
		"no row, asserting api":             {func(*bench) {}, "api", "local"},
		"local row, asserting api":          {withLocalConfig, "api", "local"},
		"api row, asserting local":          {withAPIConfig, "local", "api"},
		"row with empty via, asserting api": {func(b *bench) { b.config.found = true }, "api", "local"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			b := newBench(t, c.tweak, withAPILLM)

			_, err := b.ask(reanalisis.Request{Via: c.via})

			var via reanalisis.InvalidViaError
			if !errors.As(err, &via) {
				t.Fatalf("error = %v; se esperaba InvalidViaError", err)
			}
			if via.Via != c.via || via.Configured != c.configured {
				t.Errorf("InvalidViaError = %+v; se esperaba {%s %s}: el cuerpo dice cuál SÍ vale", via, c.via, c.configured)
			}
			b.requireSteps(t, stepLevelGate, stepVia)
			b.requireNoWrites(t)
		})
	}
}

// TestReanalyze_ViaThatMatches_Passes: afirmar la vía correcta no es un rechazo, y
// omitirla usa la efectiva — `local` sin fila o con la vía de la fila vacía.
func TestReanalyze_ViaThatMatches_Passes(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		tweaks []func(*bench)
		via    string
		want   string
	}{
		"no row, asserting local":        {nil, "local", "local"},
		"local row, asserting local":     {[]func(*bench){withLocalConfig}, "local", "local"},
		"local row, omitted":             {[]func(*bench){withLocalConfig}, "", "local"},
		"row with empty via, omitted":    {[]func(*bench){func(b *bench) { b.config.found = true }}, "", "local"},
		"api row, asserting api":         {[]func(*bench){withAPIConfig, withAPILLM}, "api", "api"},
		"api row, omitted":               {[]func(*bench){withAPIConfig, withAPILLM}, "", "api"},
		"api config but no row, omitted": {[]func(*bench){withAPIConfig, func(b *bench) { b.config.found = false }}, "", "local"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			b := newBench(t, c.tweaks...)

			out := b.mustAsk(t, reanalisis.Request{Via: c.via})

			if out.Via != c.want {
				t.Errorf("vía efectiva = %q; se esperaba %q", out.Via, c.want)
			}
		})
	}
}

// TestReanalyze_ConfigReadFails_IsWrapped: no saber la vía no es «vía local».
func TestReanalyze_ConfigReadFails_IsWrapped(t *testing.T) {
	t.Parallel()
	b := newBench(t, func(b *bench) { b.config.err = errInfra })

	_, err := b.ask(reanalisis.Request{})

	if !errors.Is(err, errInfra) {
		t.Fatalf("error = %v; se esperaba el del almacén envuelto", err)
	}
	if want := "reanalisis: leer la configuración LLM del tenant: " + errInfra.Error(); err.Error() != want {
		t.Errorf("texto = %q; se esperaba %q", err.Error(), want)
	}
	b.requireSteps(t, stepLevelGate, stepVia)
}

// --- escalones 5-6 · el gate de la vía y la credencial ----------------------------

// TestReanalyze_APIViaWithoutAPILLM_FeatureMissing es el 403 de la VÍA: un tenant que
// configuró la API y después perdió el add-on.
func TestReanalyze_APIViaWithoutAPILLM_FeatureMissing(t *testing.T) {
	t.Parallel()
	b := newBench(t, withAPIConfig)

	_, err := b.ask(reanalisis.Request{})

	var feature reanalisis.FeatureMissingError
	if !errors.As(err, &feature) || feature.Feature != entitlements.FeatureAPILLM {
		t.Fatalf("error = %v; se esperaba FeatureMissingError{api_llm}", err)
	}
	b.requireSteps(t, stepLevelGate, stepVia, stepViaGate)
	b.requireNoWrites(t)
}

// TestReanalyze_LocalVia_NeverAsksForAPILLM es el INVARIANTE de D-044.28 / ADR-0044:
// `api_llm` gatea LA VÍA, no la capacidad. Un tenant con `llm_intake` y sin `api_llm` es
// VÁLIDO en vía local y su re-análisis funciona entero, sin preguntar por esa clave.
func TestReanalyze_LocalVia_NeverAsksForAPILLM(t *testing.T) {
	t.Parallel()
	for name, tweak := range map[string]func(*bench){
		"no row in tenant_llm": func(*bench) {},
		"row with via=local":   withLocalConfig,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			b := newBench(t, tweak)

			b.mustAsk(t, reanalisis.Request{})

			if !b.features.askedFor(entitlements.FeatureLLMIntake) {
				t.Error("no se preguntó por llm_intake")
			}
			if b.features.askedFor(entitlements.FeatureAPILLM) {
				t.Error("la vía local preguntó por api_llm (ADR-0044 · D-044.28)")
			}
		})
	}
}

// TestReanalyze_APIViaWithoutCredentials_IsItsOwnOutcome recorre los DOS estados de fila
// incompleta: sin clave, y con clave sin consentimiento (que cuenta igual: ADR-0030).
//
// 🔴 FABRICA UN ESTADO QUE LA BASE NO PUEDE PRODUCIR, y se dice: una fila `via='api'` la
// garantiza completa el CHECK de la 0073. Es defensa en profundidad —un restore parcial,
// un `UPDATE` a mano— y tiene que salir por un error con nombre, que NUNCA es el del
// paywall: el tenant ya pagó.
func TestReanalyze_APIViaWithoutCredentials_IsItsOwnOutcome(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*bench){
		"api row without key":     func(b *bench) { b.config.cfg.HasAPIKey = false },
		"api row without consent": func(b *bench) { b.config.cfg.ConsentedAt = time.Time{} },
	}
	for name, breakRow := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			b := newBench(t, withAPIConfig, withAPILLM, breakRow)

			_, err := b.ask(reanalisis.Request{})

			var cred reanalisis.CredentialsMissingError
			if !errors.As(err, &cred) || cred.Via != "api" {
				t.Fatalf("error = %v; se esperaba CredentialsMissingError{api}", err)
			}
			var feature reanalisis.FeatureMissingError
			if errors.As(err, &feature) {
				t.Error("es el desenlace de credencial, NUNCA el del paywall")
			}
			b.requireSteps(t, stepLevelGate, stepVia, stepViaGate)
			b.requireNoWrites(t)
		})
	}
}

// --- escalones 7-8 · la solicitud y el job vivo -----------------------------------

// TestReanalyze_IntakeOfAnotherTenant_ErrorAsIs: el error de la lectura sale TAL CUAL
// (es el 404 de la cara HTTP) y no se pregunta por jobs ni por el hilo.
func TestReanalyze_IntakeOfAnotherTenant_ErrorAsIs(t *testing.T) {
	t.Parallel()
	b := newBench(t, func(b *bench) { b.intakes.err = intakes.ErrNotFound })

	_, err := b.ask(reanalisis.Request{})

	if !isUnwrapped(err, intakes.ErrNotFound) {
		t.Fatalf("error = %v; se esperaba intakes.ErrNotFound tal cual", err)
	}
	b.requireSteps(t, stepLevelGate, stepVia, stepIntake)
	b.requireNoWrites(t)
}

// TestReanalyze_LiveJob_InProgress cubre a la vez la concurrencia de D-044.15 y la
// CARRERA con la ventana viva del cliente: `aggregating` es un estado no terminal, así
// que un re-análisis pedido en mitad de una ráfaga encuentra ese job y sale por aquí.
// Un job terminal del mismo evento no estorba.
func TestReanalyze_LiveJob_InProgress(t *testing.T) {
	t.Parallel()
	for _, status := range []string{intake.StatusAggregating, intake.StatusPending, intake.StatusProcessing} {
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			b := newBench(t)
			live := b.seedLiveJob(status)

			_, err := b.ask(reanalisis.Request{})

			var busy reanalisis.InProgressError
			if !errors.As(err, &busy) {
				t.Fatalf("error = %v; se esperaba InProgressError", err)
			}
			if busy.JobID != live {
				t.Errorf("job = %q; el cuerpo lleva el job vivo (%s) para poder seguirlo", busy.JobID, live)
			}
			b.requireSteps(t, stepLevelGate, stepVia, stepIntake, stepLiveJob)
			b.requireNoWrites(t)
		})
	}
	for _, status := range []string{intake.StatusDone, intake.StatusFailed} {
		t.Run(status+" does not block", func(t *testing.T) {
			t.Parallel()
			b := newBench(t)
			b.seedLiveJob(status)

			b.mustAsk(t, reanalisis.Request{})
		})
	}
}

// TestReanalyze_LiveJobLookupFails_ErrorAsIs: no saber si hay un job vivo no es «no lo
// hay»; el error de la cola sale tal cual y no se abre nada.
func TestReanalyze_LiveJobLookupFails_ErrorAsIs(t *testing.T) {
	t.Parallel()
	b := newBench(t, func(b *bench) { b.jobs.liveErr = errInfra })

	_, err := b.ask(reanalisis.Request{})

	if !isUnwrapped(err, errInfra) {
		t.Fatalf("error = %v; se esperaba el de la cola tal cual", err)
	}
	b.requireSteps(t, stepLevelGate, stepVia, stepIntake, stepLiveJob)
	b.requireNoWrites(t)
}
