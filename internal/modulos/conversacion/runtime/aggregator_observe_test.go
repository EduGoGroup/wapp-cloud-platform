//go:build pendiente

package runtime

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
)

// Observe es lo único del agregador que corre en línea con el mensaje del cliente: AG-1 (el
// presupuesto de I/O y el orden de las guardas), AG-2 (no devuelve error, no tumba el turno) y
// AG-5 (la memoria del último mensaje sobrevive al cierre de la ventana).

// TestObserve_BurstBecomesOneWindow es T1.1: una ráfaga produce UNA ventana viva, con las
// referencias en orden (el id del mensaje y, detrás, sus medias), la base de fechas del PRIMER
// mensaje y sin el texto en ningún sitio.
func TestObserve_BurstBecomesOneWindow(t *testing.T) {
	rig := newAggregatorRig()
	agg := rig.aggregator()
	key := aggregatorKey("event-1")
	first := aggregatorStart.Add(-3 * time.Minute) // el reloj del cliente no es el del servidor

	agg.Observe(context.Background(), aggregatorRef(key, "wa-1", first))
	rig.clock.advance(4 * time.Second)
	photo := aggregatorRef(key, "wa-2", first.Add(4*time.Second))
	photo.Text = ""
	photo.MediaRefs = []string{"media-a", "media-b"}
	agg.Observe(context.Background(), photo)
	rig.clock.advance(4 * time.Second)
	agg.Observe(context.Background(), aggregatorRef(key, "wa-3", first.Add(8*time.Second)))

	jobs := rig.jobs.Jobs()
	if len(jobs) != 1 {
		t.Fatalf("la ráfaga dejó %d filas, quería 1", len(jobs))
	}
	job := jobs[0]
	if job.Status != intake.StatusAggregating || job.Key != key {
		t.Errorf("fila = (status %q, key %+v), quería la ventana viva de la tupla", job.Status, job.Key)
	}
	if want := []string{"wa-1", "wa-2", "media-a", "media-b", "wa-3"}; !slices.Equal(job.SourceRefs, want) {
		t.Errorf("source_refs = %v, quería %v", job.SourceRefs, want)
	}
	if !job.MessageTS.Equal(first) {
		t.Errorf("message_ts = %v, quería el del PRIMER mensaje (%v)", job.MessageTS, first)
	}
	if job.SourceText.Complete() || len(job.SourceText.Enc) != 0 {
		t.Error("Observe escribió el sobre del literal: nace vacío y se llena al flush")
	}
	if len(rig.log.at("error"))+len(rig.log.at("warn")) != 0 {
		t.Errorf("una ráfaga normal dejó avisos:\n%s", rig.log.dump())
	}
	rig.requireNoSecret(t)
}

// TestObserve_Budget_OneWriteNoReads es AG-1 (D-044.26): por entrante admitido, UNA escritura
// (OpenOrAppend), cero lecturas de intake_jobs, cero de tenant_settings, cero composiciones y UNA
// pregunta al resolver — abriendo la ventana y ampliándola, con texto y sin él.
func TestObserve_Budget_OneWriteNoReads(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(agg *IntakeAggregator, key intake.WindowKey)
		ref     func(key intake.WindowKey) IncomingRef
	}{
		{
			name:    "opening the window",
			prepare: func(*IntakeAggregator, intake.WindowKey) {},
			ref:     func(key intake.WindowKey) IncomingRef { return aggregatorRef(key, "wa-1", aggregatorStart) },
		},
		{
			name: "extending the window",
			prepare: func(agg *IntakeAggregator, key intake.WindowKey) {
				agg.Observe(context.Background(), aggregatorRef(key, "wa-0", aggregatorStart))
			},
			ref: func(key intake.WindowKey) IncomingRef { return aggregatorRef(key, "wa-1", aggregatorStart) },
		},
		{
			name:    "media without text",
			prepare: func(*IntakeAggregator, intake.WindowKey) {},
			ref: func(key intake.WindowKey) IncomingRef {
				return IncomingRef{Key: key, WaMessageID: "wa-1", MediaRefs: []string{"media-a"}, MessageTS: aggregatorStart}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rig := newAggregatorRig()
			agg := rig.aggregator(WithSourceComposer(rig.composer), WithAheadRequester(rig.ahead))
			key := aggregatorKey("event-1")
			c.prepare(agg, key)
			rig.jobs.ResetCounters()
			asksBefore := len(rig.ents.asks())

			agg.Observe(context.Background(), c.ref(key))

			if got, want := rig.jobs.Counters(), (intake.Counters{OpenOrAppend: 1}); got != want {
				t.Errorf("presupuesto de intake_jobs = %+v, quería %+v", got, want)
			}
			if got := rig.settings.totalReads(); got != 0 {
				t.Errorf("Observe leyó tenant_settings %d veces: es el SELECT en línea que D-044.26 prohíbe", got)
			}
			if got := len(rig.composer.composed()); got != 0 {
				t.Errorf("Observe compuso %d literales: eso es del flush", got)
			}
			asks := rig.ents.asks()[asksBefore:]
			if want := []aggregatorAsk{{aggregatorTenant, "llm_intake"}}; !slices.Equal(asks, want) {
				t.Errorf("preguntas al resolver = %v, quería exactamente %v", asks, want)
			}
		})
	}
}

// TestObserve_CheapGuardsComeFirst: un entrante sin identificador o sin evento vivo (el LIMBO), o
// un agregador al que le falta una dependencia, no escribe nada, no pide nada y NO le cuesta al
// sistema ni una pregunta al resolver.
func TestObserve_CheapGuardsComeFirst(t *testing.T) {
	full := aggregatorKey("event-1")
	refs := map[string]IncomingRef{
		"no wa_message_id": aggregatorRef(full, "", aggregatorStart),
		"no live event":    aggregatorRef(intake.WindowKey{TenantID: full.TenantID, SessionID: full.SessionID, ContactID: full.ContactID}, "wa-1", aggregatorStart),
		"no tenant":        aggregatorRef(intake.WindowKey{SessionID: full.SessionID, ContactID: full.ContactID, EventID: full.EventID}, "wa-1", aggregatorStart),
		"no session":       aggregatorRef(intake.WindowKey{TenantID: full.TenantID, ContactID: full.ContactID, EventID: full.EventID}, "wa-1", aggregatorStart),
		"no contact":       aggregatorRef(intake.WindowKey{TenantID: full.TenantID, SessionID: full.SessionID, EventID: full.EventID}, "wa-1", aggregatorStart),
	}
	for name, ref := range refs {
		t.Run(name, func(t *testing.T) {
			rig := newAggregatorRig()
			rig.aggregator(WithAheadRequester(rig.ahead)).Observe(context.Background(), ref)

			if got := rig.jobs.Counters(); got != (intake.Counters{}) {
				t.Errorf("se tocó intake_jobs: %+v", got)
			}
			if got := rig.ents.asks(); len(got) != 0 {
				t.Errorf("la guarda barata llegó tarde: se preguntó al resolver %v", got)
			}
			if got := rig.ahead.requested(); len(got) != 0 {
				t.Errorf("se pidió una clasificación sin ventana: %v", got)
			}
		})
	}

	t.Run("missing dependency", func(t *testing.T) {
		ref := aggregatorRef(full, "wa-1", aggregatorStart)
		var nilAggregator *IntakeAggregator
		nilAggregator.Observe(context.Background(), ref)
		rig := newAggregatorRig()
		NewIntakeAggregator(nil, rig.jobs, rig.settings, rig.ents).Observe(context.Background(), ref)
		NewIntakeAggregator(rig.log, nil, rig.settings, rig.ents).Observe(context.Background(), ref)
		NewIntakeAggregator(rig.log, rig.jobs, rig.settings, nil).Observe(context.Background(), ref)
		if got := rig.jobs.Counters(); got != (intake.Counters{}) {
			t.Errorf("un agregador incompleto tocó intake_jobs: %+v", got)
		}
		if got := rig.ents.asks(); len(got) != 0 {
			t.Errorf("un agregador incompleto preguntó al resolver: %v", got)
		}
	})

	t.Run("nil settings does not switch Observe off", func(t *testing.T) {
		rig := newAggregatorRig()
		NewIntakeAggregator(rig.log, rig.jobs, nil, rig.ents, WithAggregatorClock(rig.now)).
			Observe(context.Background(), aggregatorRef(full, "wa-1", aggregatorStart))
		rig.requireStatus(t, "sin settings", intake.StatusAggregating)
	})
}

// TestObserve_GateIsLLMIntakeOnly: el único gate es llm_intake y es fail-closed. Sin la feature no
// se escribe ni se pide nada, en silencio; tener api_llm o llm_intent no la sustituye; y si el
// resolver falla, tampoco se escribe y queda un aviso sin contenido.
func TestObserve_GateIsLLMIntakeOnly(t *testing.T) {
	key := intake.WindowKey{TenantID: aggregatorTenantWithout, SessionID: "session-9", ContactID: "contact-opaque", EventID: "event-1"}

	t.Run("tenant without the feature", func(t *testing.T) {
		rig := newAggregatorRig()
		rig.ents.Enable(aggregatorTenantWithout, "api_llm")
		rig.ents.Enable(aggregatorTenantWithout, entitlements.FeatureLLMIntent)
		rig.aggregator(WithAheadRequester(rig.ahead)).Observe(context.Background(), aggregatorRef(key, "wa-1", aggregatorStart))

		if got := rig.jobs.Counters(); got != (intake.Counters{}) {
			t.Errorf("un tenant sin llm_intake tocó intake_jobs: %+v", got)
		}
		if len(rig.ahead.requested()) != 0 {
			t.Error("un tenant sin llm_intake gastó una inferencia")
		}
		if want := []aggregatorAsk{{aggregatorTenantWithout, entitlements.FeatureLLMIntake}}; !slices.Equal(rig.ents.asks(), want) {
			t.Errorf("preguntas al resolver = %v, quería solo %v (ni api_llm ni llm_intent)", rig.ents.asks(), want)
		}
		if len(rig.log.all()) != 0 {
			t.Errorf("no tener la feature no es noticia:\n%s", rig.log.dump())
		}
	})

	t.Run("resolver failure is fail-closed", func(t *testing.T) {
		rig := newAggregatorRig()
		boom := errors.New("resolver caído")
		rig.ents.Err = boom
		rig.aggregator(WithAheadRequester(rig.ahead)).Observe(context.Background(), aggregatorRef(aggregatorKey("event-1"), "wa-1", aggregatorStart))

		if got := rig.jobs.Counters(); got != (intake.Counters{}) {
			t.Errorf("con el resolver caído se tocó intake_jobs: %+v", got)
		}
		if len(rig.ahead.requested()) != 0 {
			t.Error("con el resolver caído se pidió una clasificación")
		}
		rig.requireLine(t, "warn", "agregador: no se pudo resolver la feature llm_intake; el entrante no entra en ninguna ventana",
			map[string]any{"error": boom, "tenant_id": aggregatorTenant, "session_id": "session-9"})
		rig.requireNoSecret(t)
	})
}

// TestObserve_StoreFailureDoesNotBreakTheTurn es AG-2 (INV-10): si la sentencia falla, Observe
// vuelve con normalidad —la escritura se INTENTÓ—, lo deja en Error sin contenido y no pide
// clasificación para una ventana que no existe.
func TestObserve_StoreFailureDoesNotBreakTheTurn(t *testing.T) {
	rig := newAggregatorRig()
	agg := rig.aggregator(WithAheadRequester(rig.ahead))
	boom := errors.New("base caída")
	rig.jobs.FailOpenWith(boom)
	key := aggregatorKey("event-1")

	agg.Observe(context.Background(), aggregatorRef(key, "wa-1", aggregatorStart))

	if got, want := rig.jobs.Counters(), (intake.Counters{OpenOrAppend: 1}); got != want {
		t.Errorf("presupuesto = %+v, quería %+v: la escritura tiene que haberse intentado, y nada más", got, want)
	}
	if len(rig.jobs.Jobs()) != 0 {
		t.Error("quedó una fila a medias tras el fallo")
	}
	if len(rig.ahead.requested()) != 0 {
		t.Error("se pidió la clasificación de una ventana que no llegó a abrirse")
	}
	rig.requireLine(t, "error", "agregador: no se pudo abrir/ampliar la ventana de captación; el turno sigue",
		map[string]any{"error": boom, "tenant_id": aggregatorTenant, "session_id": "session-9", "wa_message_id": "wa-1"})
	rig.requireNoSecret(t)

	// Rareza portada: el id quedó anotado como visto ANTES de la sentencia, así que su re-entrega
	// inmediata se descarta sin reintentar; un mensaje distinto sí abre la ventana.
	rig.jobs.FailOpenWith(nil)
	agg.Observe(context.Background(), aggregatorRef(key, "wa-1", aggregatorStart))
	if got := rig.jobs.Counters().OpenOrAppend; got != 1 {
		t.Errorf("la re-entrega del id fallido llegó a la sentencia (%d escrituras): el viejo la descarta", got)
	}
	agg.Observe(context.Background(), aggregatorRef(key, "wa-2", aggregatorStart))
	rig.requireStatus(t, "tras un mensaje distinto", intake.StatusAggregating)
}

// TestObserve_SameMessageTwice: el mismo wa_message_id observado dos veces seguidas sobre la misma
// ventana no duplica la referencia ni pide dos clasificaciones; en otra ventana sí entra.
func TestObserve_SameMessageTwice(t *testing.T) {
	rig := newAggregatorRig()
	agg := rig.aggregator(WithAheadRequester(rig.ahead))
	key := aggregatorKey("event-1")

	agg.Observe(context.Background(), aggregatorRef(key, "wa-1", aggregatorStart))
	agg.Observe(context.Background(), aggregatorRef(key, "wa-1", aggregatorStart))

	jobs := rig.jobs.Jobs()
	if len(jobs) != 1 || !slices.Equal(jobs[0].SourceRefs, []string{"wa-1"}) {
		t.Fatalf("filas = %+v, quería una ventana con la referencia una sola vez", jobs)
	}
	if got := rig.jobs.Counters().OpenOrAppend; got != 1 {
		t.Errorf("el mensaje repetido llegó a la sentencia: %d escrituras", got)
	}
	if got := len(rig.ahead.requested()); got != 1 {
		t.Errorf("se pidieron %d clasificaciones del mismo mensaje, quería 1", got)
	}

	agg.Observe(context.Background(), aggregatorRef(aggregatorKey("event-2"), "wa-1", aggregatorStart))
	rig.requireStatus(t, "el mismo id en otra ventana", intake.StatusAggregating, intake.StatusAggregating)
}

// TestObserve_SeenSurvivesTheFlush es AG-5 (trampa T-10): cerrar la ventana NO borra la memoria
// del último mensaje. Una re-entrega tras el flush no reabre nada; un mensaje distinto sí abre la
// ventana siguiente sobre el mismo evento; y la memoria es del proceso, no de la base.
func TestObserve_SeenSurvivesTheFlush(t *testing.T) {
	rig := newAggregatorRig()
	agg := rig.aggregator()
	key := aggregatorKey("event-1")
	agg.Observe(context.Background(), aggregatorRef(key, "wa-1", aggregatorStart))
	rig.clock.advance(45 * time.Second)
	if got := agg.Sweep(context.Background()); got != 1 {
		t.Fatalf("el barrido cerró %d ventanas, quería 1", got)
	}
	rig.jobs.ResetCounters()

	agg.Observe(context.Background(), aggregatorRef(key, "wa-1", aggregatorStart))

	if got := rig.jobs.Counters(); got != (intake.Counters{}) {
		t.Errorf("la re-entrega tras el flush tocó intake_jobs: %+v", got)
	}
	rig.requireStatus(t, "tras la re-entrega", intake.StatusPending)

	agg.Observe(context.Background(), aggregatorRef(key, "wa-2", aggregatorStart))

	rig.requireStatus(t, "tras un mensaje nuevo", intake.StatusPending, intake.StatusAggregating)
	jobs := rig.jobs.Jobs()
	if !slices.Equal(jobs[0].SourceRefs, []string{"wa-1"}) || !slices.Equal(jobs[1].SourceRefs, []string{"wa-2"}) {
		t.Errorf("refs = %v y %v, quería [wa-1] y [wa-2]: el job cerrado no se toca", jobs[0].SourceRefs, jobs[1].SourceRefs)
	}

	// Un proceso nuevo sobre la misma base no recuerda nada: la red primaria contra el reenvío es
	// el dedupe persistente de ingesta, no esta memoria.
	rig.aggregator().Observe(context.Background(), aggregatorRef(key, "wa-2", aggregatorStart))
	if got := rig.jobs.Jobs()[1].SourceRefs; !slices.Equal(got, []string{"wa-2", "wa-2"}) {
		t.Errorf("refs de la ventana viva = %v, quería [wa-2 wa-2]: la memoria vive en el proceso", got)
	}
}
