//go:build pendiente

package runtime

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
)

// El adelanto por intent (T1.6-4, D-044.31: el Cloud PIDE la clasificación y la respuesta vuelve
// por OnClassified). AG-3: solo adelanta. AG-4: anota y después avisa, sin bloquear. AG-8: sin
// AheadRequester todo cierra por reloj.

// TestObserve_RequestsTheClassification: admitir un texto en ventana es lo que dispara la petición,
// UNA vez, con la clave y el texto del entrante, y después de que la ventana exista. Sin texto (un
// mensaje de solo media) no se pide.
func TestObserve_RequestsTheClassification(t *testing.T) {
	rig := newAggregatorRig()
	agg := rig.aggregator(WithAheadRequester(rig.ahead))
	key := aggregatorKey("event-1")

	agg.Observe(context.Background(), aggregatorRef(key, "wa-1", aggregatorStart))

	if want := []aggregatorRequest{{key, aggregatorClientText}}; !slices.Equal(rig.ahead.requested(), want) {
		t.Fatalf("peticiones = %+v, quería %+v", rig.ahead.requested(), want)
	}
	rig.requireStatus(t, "al pedir", intake.StatusAggregating)

	media := IncomingRef{Key: key, WaMessageID: "wa-2", MediaRefs: []string{"media-a"}, MessageTS: aggregatorStart}
	agg.Observe(context.Background(), media)
	if got := len(rig.ahead.requested()); got != 1 {
		t.Errorf("un mensaje sin texto pidió una clasificación (%d peticiones)", got)
	}
	if got := rig.jobs.Jobs()[0].SourceRefs; !slices.Equal(got, []string{"wa-1", "wa-2", "media-a"}) {
		t.Errorf("refs = %v: el mensaje sin texto entra en la ventana igual", got)
	}
	rig.requireNoSecret(t)
}

// TestWithAheadRequester_WithoutItEverythingClosesByTheClock es AG-8: sin quien pida —o con nil—
// Observe funciona igual y la ventana cierra a su hora, ni antes ni nunca.
func TestWithAheadRequester_WithoutItEverythingClosesByTheClock(t *testing.T) {
	for name, opts := range map[string][]AggregatorOption{
		"not wired":   nil,
		"nil ignored": {WithAheadRequester(nil)},
	} {
		rig := newAggregatorRig()
		agg := rig.aggregator(opts...)
		aggregatorObserveAt(rig, agg, aggregatorKey("event-1"), "wa-1", 0)

		if got := aggregatorSweepAt(rig, agg, 44); got != 0 {
			t.Errorf("%s: a los 44 s el barrido cerró %d ventanas, quería 0", name, got)
		}
		if got := aggregatorSweepAt(rig, agg, 45); got != 1 {
			t.Errorf("%s: a los 45 s el barrido cerró %d ventanas, quería 1", name, got)
		}
	}

	rig := newAggregatorRig()
	agg := rig.aggregator(WithAheadRequester(rig.ahead), WithAheadRequester(nil))
	agg.Observe(context.Background(), aggregatorRef(aggregatorKey("event-1"), "wa-1", aggregatorStart))
	if got := len(rig.ahead.requested()); got != 1 {
		t.Errorf("un nil posterior borró el AheadRequester ya cableado (%d peticiones)", got)
	}
}

// TestOnClassified_TriggerPolicy es AG-3 y D-044.20: adelanta solo intake_request con confianza
// >= 0.7. Cuando adelanta, el barrido siguiente cierra sin esperar al silencio; cuando no, la
// ventana sigue VIVA y no queda ni una línea de log.
func TestOnClassified_TriggerPolicy(t *testing.T) {
	cases := []struct {
		name       string
		intent     string
		confidence float64
		brings     bool
	}{
		{"intake_request at the threshold", "intake_request", 0.7, true},
		{"intake_request above", IntentIntakeRequest, 0.95, true},
		{"intake_request just below", "intake_request", 0.69, false},
		{"intake_request with zero confidence", "intake_request", 0, false},
		{"another intent, certain", "greeting", 1, false},
		{"different casing", "Intake_Request", 1, false},
		{"empty intent", "", 1, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rig := newAggregatorRig()
			agg := rig.aggregator()
			key := aggregatorKey("event-1")
			aggregatorObserveAt(rig, agg, key, "wa-1", 0)

			agg.OnClassified(key, c.intent, c.confidence)

			rig.requireStatus(t, "antes del barrido (OnClassified no cierra)", intake.StatusAggregating)
			if got := rig.jobs.Counters(); got.Close != 0 || got.Reads != 0 {
				t.Errorf("OnClassified tocó intake_jobs: %+v", got)
			}
			got := aggregatorSweepAt(rig, agg, 1)
			if c.brings {
				if got != 1 {
					t.Fatalf("el barrido cerró %d ventanas, quería 1: el intent adelanta el flush", got)
				}
				rig.requireStatus(t, "tras el adelanto", intake.StatusPending)
				if again := agg.Sweep(context.Background()); again != 0 {
					t.Errorf("un segundo barrido cerró %d ventanas, quería 0", again)
				}
				return
			}
			if got != 0 {
				t.Fatalf("el barrido cerró %d ventanas, quería 0: esa clasificación no adelanta", got)
			}
			rig.requireStatus(t, "sin adelanto", intake.StatusAggregating)
			if len(rig.log.all()) != 0 {
				t.Errorf("una clasificación que no dispara no es noticia:\n%s", rig.log.dump())
			}
		})
	}
}

// TestWithIntentConfidence_ReplacesThePlatformDefault: el umbral inyectado SUSTITUYE al 0.7 (una
// confianza entre los dos números lo delata); <= 0 se ignora.
func TestWithIntentConfidence_ReplacesThePlatformDefault(t *testing.T) {
	cases := []struct {
		name       string
		opts       []AggregatorOption
		confidence float64
		want       int
	}{
		{"lowered: 0.6 clears 0.5", []AggregatorOption{WithIntentConfidence(0.5)}, 0.6, 1},
		{"lowered: 0.49 does not clear 0.5", []AggregatorOption{WithIntentConfidence(0.5)}, 0.49, 0},
		{"raised: 0.8 does not clear 0.9", []AggregatorOption{WithIntentConfidence(0.9)}, 0.8, 0},
		{"zero is ignored", []AggregatorOption{WithIntentConfidence(0)}, 0.6, 0},
		{"negative is ignored", []AggregatorOption{WithIntentConfidence(-1)}, 0.6, 0},
		{"ignored value keeps the default", []AggregatorOption{WithIntentConfidence(0)}, 0.7, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rig := newAggregatorRig()
			agg := rig.aggregator(c.opts...)
			key := aggregatorKey("event-1")
			aggregatorObserveAt(rig, agg, key, "wa-1", 0)
			agg.OnClassified(key, IntentIntakeRequest, c.confidence)
			if got := agg.Sweep(context.Background()); got != c.want {
				t.Errorf("con confianza %v el barrido cerró %d ventanas, quería %d", c.confidence, got, c.want)
			}
		})
	}
}

// TestOnClassified_TheJobIsIndistinguishable es T1.7 (d): el job que cierra un intent y el que
// cierra el silencio son iguales —mismas referencias, misma base de fechas, mismo estado, sobre
// vacío—. No hay marca de por qué se disparó.
func TestOnClassified_TheJobIsIndistinguishable(t *testing.T) {
	run := func(withIntent bool) intake.Job {
		rig := newAggregatorRig()
		agg := rig.aggregator()
		key := aggregatorKey("event-1")
		aggregatorObserveAt(rig, agg, key, "wa-1", 0)
		aggregatorObserveAt(rig, agg, key, "wa-2", 3)
		at := 48
		if withIntent {
			agg.OnClassified(key, IntentIntakeRequest, 0.9)
			at = 3
		}
		if got := aggregatorSweepAt(rig, agg, at); got != 1 {
			t.Fatalf("conIntent=%v: el barrido cerró %d ventanas, quería 1", withIntent, got)
		}
		return rig.jobs.Jobs()[0]
	}

	byIntent, bySilence := run(true), run(false)

	if byIntent.Status != bySilence.Status || byIntent.Key != bySilence.Key ||
		!slices.Equal(byIntent.SourceRefs, bySilence.SourceRefs) || !byIntent.MessageTS.Equal(bySilence.MessageTS) ||
		byIntent.SourceText.Complete() != bySilence.SourceText.Complete() || !byIntent.CreatedAt.Equal(bySilence.CreatedAt) {
		t.Errorf("los dos caminos dejan jobs distintos:\n por intent:   %+v\n por silencio: %+v", byIntent, bySilence)
	}
}

// TestOnClassified_LateAnswerIsHarmless: la inferencia tarda segundos y puede contestar DESPUÉS de
// que la ventana cerrara por su reloj. No reabre, no vuelve a cerrar y no toca la fila; y la pista
// se consume en esa pasada, así que no espera a la ventana siguiente.
func TestOnClassified_LateAnswerIsHarmless(t *testing.T) {
	rig := newAggregatorRig()
	agg := rig.aggregator()
	key := aggregatorKey("event-1")
	aggregatorObserveAt(rig, agg, key, "wa-1", 0)
	if got := aggregatorSweepAt(rig, agg, 45); got != 1 {
		t.Fatalf("el barrido cerró %d ventanas, quería 1", got)
	}
	closed := rig.jobs.Jobs()[0]

	agg.OnClassified(key, IntentIntakeRequest, 0.95)

	if got := aggregatorSweepAt(rig, agg, 50); got != 0 {
		t.Errorf("la respuesta tardía cerró %d ventanas, quería 0", got)
	}
	after := rig.jobs.Jobs()
	if len(after) != 1 || after[0].Status != intake.StatusPending || !after[0].UpdatedAt.Equal(closed.UpdatedAt) {
		t.Errorf("la respuesta tardía tocó la fila cerrada: %+v", after)
	}

	aggregatorObserveAt(rig, agg, key, "wa-2", 51)
	if got := agg.Sweep(context.Background()); got != 0 {
		t.Errorf("la pista tardía, ya consumida, cerró la ventana siguiente (%d cierres)", got)
	}
	rig.requireStatus(t, "tras el mensaje nuevo", intake.StatusPending, intake.StatusAggregating)
}

// TestOnClassified_LateAnswerClosesTheNextWindowEarly fija lo ACEPTADO: la pista es por tupla, no
// por job. Si la respuesta tardía llega cuando el cliente ya abrió OTRA ventana sobre el mismo
// evento, la cierra antes de tiempo. Lo que no puede pasar es que pierda mensajes o duplique jobs.
func TestOnClassified_LateAnswerClosesTheNextWindowEarly(t *testing.T) {
	rig := newAggregatorRig()
	agg := rig.aggregator()
	key := aggregatorKey("event-1")
	aggregatorObserveAt(rig, agg, key, "wa-1", 0)
	if got := aggregatorSweepAt(rig, agg, 45); got != 1 {
		t.Fatalf("el barrido cerró %d ventanas, quería 1", got)
	}
	aggregatorObserveAt(rig, agg, key, "wa-2", 50)

	agg.OnClassified(key, IntentIntakeRequest, 0.95) // la respuesta de la petición de wa-1

	if got := aggregatorSweepAt(rig, agg, 51); got != 1 {
		t.Fatalf("el barrido cerró %d ventanas, quería 1 (la nueva, adelantada)", got)
	}
	jobs := rig.jobs.Jobs()
	if len(jobs) != 2 || jobs[0].Status != intake.StatusPending || jobs[1].Status != intake.StatusPending {
		t.Fatalf("filas = %+v, quería dos jobs cerrados", jobs)
	}
	if !slices.Equal(jobs[0].SourceRefs, []string{"wa-1"}) || !slices.Equal(jobs[1].SourceRefs, []string{"wa-2"}) {
		t.Errorf("refs = %v y %v: se perdió o se duplicó un mensaje", jobs[0].SourceRefs, jobs[1].SourceRefs)
	}
}

// TestOnClassified_NeverBlocks es AG-4: sin Run escuchando, el despertador no tiene lector. Mil
// avisos vuelven igual (que el test acabe ES la aserción) y no se pierde la pista: el barrido
// siguiente cierra la ventana. Sobre un receptor nil no hace nada.
func TestOnClassified_NeverBlocks(t *testing.T) {
	rig := newAggregatorRig()
	agg := rig.aggregator()
	key := aggregatorKey("event-1")
	aggregatorObserveAt(rig, agg, key, "wa-1", 0)
	aggregatorObserveAt(rig, agg, aggregatorKey("event-2"), "wa-1", 0)

	for range 1000 {
		agg.OnClassified(key, IntentIntakeRequest, 0.9)
	}
	agg.OnClassified(aggregatorKey("event-2"), IntentIntakeRequest, 0.9)
	var nilAggregator *IntakeAggregator
	nilAggregator.OnClassified(key, IntentIntakeRequest, 0.9)

	if got := agg.Sweep(context.Background()); got != 2 {
		t.Fatalf("el barrido cerró %d ventanas, quería las 2: descartar avisos no descarta pistas", got)
	}
	if got := agg.Sweep(context.Background()); got != 0 {
		t.Errorf("mil avisos dejaron trabajo repetido: el segundo barrido cerró %d", got)
	}
}

// TestSweep_ConsumesEveryHintEachPass: una pasada se lleva TODAS las pistas, también la de una
// ventana que el batch dejó fuera. Esa ventana no se pierde: cierra por su reloj.
func TestSweep_ConsumesEveryHintEachPass(t *testing.T) {
	batch := WithSweepBatch(1)
	rig := newAggregatorRig()
	agg := rig.aggregator(batch)
	immediate := intake.WindowKey{TenantID: "tenant-2", SessionID: "session-2", ContactID: "contact-2", EventID: "event-9"}
	rig.settings.setDeadlines("tenant-2", 0, 120*time.Second)
	hinted := aggregatorKey("event-1")
	aggregatorObserveAt(rig, agg, immediate, "wa-1", 0) // la más antigua: es la única que ve la pasada
	aggregatorObserveAt(rig, agg, hinted, "wa-1", 0)
	agg.OnClassified(hinted, IntentIntakeRequest, 0.9)

	if got := agg.Sweep(context.Background()); got != 1 {
		t.Fatalf("la primera pasada cerró %d ventanas, quería 1 (la de flush inmediato)", got)
	}
	if got := agg.Sweep(context.Background()); got != 0 {
		t.Fatalf("la segunda pasada cerró %d ventanas: la pista de la ventana que quedó fuera del batch debía haberse consumido", got)
	}
	rig.requireStatus(t, "antes del plazo", intake.StatusPending, intake.StatusAggregating)
	if got := aggregatorSweepAt(rig, agg, 45); got != 1 {
		t.Errorf("a los 45 s el barrido cerró %d ventanas, quería 1: sin pista, cierra por su reloj", got)
	}
}

// TestSweep_KeepsTheHintsWhenListingFails: si la pasada no llega a ver ninguna ventana porque el
// listado falla, las pistas no se gastan: el barrido siguiente adelanta igual.
func TestSweep_KeepsTheHintsWhenListingFails(t *testing.T) {
	rig := newAggregatorRig()
	agg := rig.aggregator()
	key := aggregatorKey("event-1")
	aggregatorObserveAt(rig, agg, key, "wa-1", 0)
	agg.OnClassified(key, IntentIntakeRequest, 0.9)
	rig.jobs.set(func(j *aggregatorJobs) { j.listErr = context.DeadlineExceeded })

	if got := agg.Sweep(context.Background()); got != 0 {
		t.Fatalf("con el listado caído el barrido cerró %d ventanas", got)
	}
	rig.jobs.set(func(j *aggregatorJobs) { j.listErr = nil })

	if got := agg.Sweep(context.Background()); got != 1 {
		t.Errorf("tras recuperarse el listado el barrido cerró %d ventanas, quería 1: la pista seguía ahí", got)
	}
}
