package runtime

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
)

// El cierre CON su sobre (D-F7-9, D-F8-13): el barrido compone ANTES de cerrar y cierra con el
// sobre en una sola sentencia. Trozo de aggregator_sweep_test.go (E-13).

// aggregatorSameEnvelope compara dos sobres pieza a pieza.
func aggregatorSameEnvelope(a, b intake.SourceText) bool {
	return bytes.Equal(a.Enc, b.Enc) && bytes.Equal(a.DEK, b.DEK) && a.KEKID == b.KEKID
}

// requireClosingBudget falla si los cierres no se hicieron por las vías esperadas.
func (r *aggregatorRig) requireClosingBudget(t *testing.T, withEnvelope, plain int) {
	t.Helper()
	got := r.jobs.Counters()
	if got.CloseWithSourceText != withEnvelope || got.Close != plain {
		t.Errorf("presupuesto = (cierres con sobre %d, cierres sin sobre %d), quería (%d, %d)",
			got.CloseWithSourceText, got.Close, withEnvelope, plain)
	}
}

// TestSweep_ComposesOncePerWindowItClosed: por cada ventana vencida se llama una vez al compositor
// con su clave y el job queda `pending` CON el sobre que devolvió; las que no vencen no se componen
// ni se tocan, y un segundo barrido no compone de nuevo.
func TestSweep_ComposesOncePerWindowItClosed(t *testing.T) {
	rig := newAggregatorRig()
	agg := rig.aggregator(WithSourceComposer(rig.composer))
	due, alsoDue, notDue := aggregatorKey("event-1"), aggregatorKey("event-2"), aggregatorKey("event-3")
	aggregatorObserveAt(rig, agg, due, "wa-1", 0)
	aggregatorObserveAt(rig, agg, alsoDue, "wa-1", 0)
	aggregatorObserveAt(rig, agg, notDue, "wa-1", 30)

	if got := aggregatorSweepAt(rig, agg, 45); got != 2 {
		t.Fatalf("el barrido cerró %d ventanas, quería 2", got)
	}
	if got := rig.composer.composed(); !slices.Equal(got, []intake.WindowKey{due, alsoDue}) {
		t.Errorf("ventanas compuestas = %v, quería las dos vencidas, una vez cada una", got)
	}
	rig.requireStatus(t, "tras el barrido", intake.StatusPending, intake.StatusPending, intake.StatusAggregating)
	jobs := rig.jobs.Jobs()
	for i, job := range jobs[:2] {
		if !aggregatorSameEnvelope(job.SourceText, aggregatorEnvelope()) {
			t.Errorf("job %d: sobre = %+v, quería el del compositor", i, job.SourceText)
		}
	}
	if !jobs[2].SourceText.Empty() {
		t.Errorf("la ventana que no venció lleva sobre: %+v", jobs[2].SourceText)
	}
	rig.requireClosingBudget(t, 2, 0)
	if got := agg.Sweep(context.Background()); got != 0 || len(rig.composer.composed()) != 2 {
		t.Errorf("un barrido sin nada que cerrar compuso de nuevo (cerró %d, composiciones %d)", got, len(rig.composer.composed()))
	}
}

// TestSweep_TheEnvelopeTravelsWithTheClose es el ORDEN de D-F8-13: mientras se compone, la fila
// sigue `aggregating` y nadie ha cerrado nada; y el cierre es UNA llamada que lleva el sobre. No
// existe el instante «`pending` sin sobre» en que el worker reclamaba el job (D-F7-9).
//
// Mata: «volver al orden viejo» (CloseWindow y después componer: la fila ya estaría `pending`
// durante Compose).
func TestSweep_TheEnvelopeTravelsWithTheClose(t *testing.T) {
	rig := newAggregatorRig()
	agg := rig.aggregator(WithSourceComposer(rig.composer))
	aggregatorObserveAt(rig, agg, aggregatorKey("event-1"), "wa-1", 0)
	var (
		during      intake.Job
		closesSoFar int
	)
	rig.composer.during = func(intake.WindowKey) {
		during = rig.jobs.Jobs()[0]
		cnt := rig.jobs.Counters()
		closesSoFar = cnt.Close + cnt.CloseWithSourceText
	}
	rig.jobs.ResetCounters()

	if got := aggregatorSweepAt(rig, agg, 45); got != 1 {
		t.Fatalf("el barrido cerró %d ventanas, quería 1", got)
	}

	if during.Status != intake.StatusAggregating || !during.SourceText.Empty() || closesSoFar != 0 {
		t.Errorf("durante la composición: (status %q, sobre vacío %v, cierres y escrituras ya hechos %d), quería aggregating, sin sobre y 0: se compone ANTES de cerrar",
			during.Status, during.SourceText.Empty(), closesSoFar)
	}
	rig.requireClosingBudget(t, 1, 0)
	if job := rig.jobs.Jobs()[0]; job.Status != intake.StatusPending || !aggregatorSameEnvelope(job.SourceText, aggregatorEnvelope()) {
		t.Errorf("job = (status %q, sobre %+v), quería pending con el sobre del compositor", job.Status, job.SourceText)
	}
}

// TestSweep_TheClosedJobCarriesExactlyTheComposedEnvelope: tras el cierre feliz, el sobre de la fila
// es EXACTAMENTE el que devolvió el compositor, pieza a pieza.
//
// Mata: «cerrar sin sobre» (llamar a CloseWithSourceText con el sobre vacío aunque haya uno).
func TestSweep_TheClosedJobCarriesExactlyTheComposedEnvelope(t *testing.T) {
	rig := newAggregatorRig()
	agg := rig.aggregator(WithSourceComposer(rig.composer))
	want := intake.SourceText{Enc: []byte("literal cifrado de event-1"), DEK: []byte("dek envuelta"), KEKID: "K7"}
	rig.composer.env = &want
	aggregatorObserveAt(rig, agg, aggregatorKey("event-1"), "wa-1", 0)

	if got := aggregatorSweepAt(rig, agg, 45); got != 1 {
		t.Fatalf("el barrido cerró %d ventanas, quería 1", got)
	}
	job := rig.jobs.Jobs()[0]
	if job.Status != intake.StatusPending || !job.SourceText.Complete() || !aggregatorSameEnvelope(job.SourceText, want) {
		t.Errorf("job = (status %q, sobre %+v), quería pending con %+v", job.Status, job.SourceText, want)
	}
}

// TestSweep_AMessageDuringTheCompositionKeepsTheWindowOpen es la CARRERA que D-F8-13 cierra por el
// otro lado: un mensaje que entra mientras se compone deja el sobre viejo (compuesto sin él). El
// cierre solo vale si la ventana no cambió: aquí no cierra, la fila sigue viva con los DOS mensajes
// y sin sobre, y no es un error. Cuando el silencio vence de nuevo, se compone otra vez y cierra.
//
// Mata: «quitar la guarda de que la ventana no cambió». La guarda vive en el store (updated_at =
// el leído); el test la ejerce de punta a punta con el gemelo real.
func TestSweep_AMessageDuringTheCompositionKeepsTheWindowOpen(t *testing.T) {
	rig := newAggregatorRig()
	agg := rig.aggregator(WithSourceComposer(rig.composer))
	key := aggregatorKey("event-1")
	aggregatorObserveAt(rig, agg, key, "wa-1", 0)
	rig.composer.during = func(k intake.WindowKey) {
		rig.clock.advance(time.Second) // el mensaje entra en el segundo 46, con la composición a medias
		if err := rig.jobs.OpenOrAppend(context.Background(), intake.Append{Key: k, MessageTS: rig.clock.now(), Refs: []string{"wa-2"}}); err != nil {
			t.Errorf("no se pudo meter el mensaje durante la composición: %v", err)
		}
	}

	if got := aggregatorSweepAt(rig, agg, 45); got != 0 {
		t.Fatalf("el barrido cerró %d ventanas, quería 0: la ventana cambió mientras se componía", got)
	}
	rig.requireStatus(t, "tras el mensaje tardío", intake.StatusAggregating)
	job := rig.jobs.Jobs()[0]
	if !job.SourceText.Empty() || !slices.Equal(job.SourceRefs, []string{"wa-1", "wa-2"}) {
		t.Errorf("job = (sobre %+v, refs %v), quería sin sobre y con los dos mensajes", job.SourceText, job.SourceRefs)
	}
	if len(rig.log.at("error"))+len(rig.log.at("debug")) != 0 {
		t.Errorf("una ventana que cambió no es un error ni un cierre:\n%s", rig.log.dump())
	}

	rig.composer.during = nil
	if got := aggregatorSweepAt(rig, agg, 90); got != 0 {
		t.Fatalf("a los 90 s el barrido cerró %d ventanas, quería 0: el silencio cuenta desde el segundo 46", got)
	}
	if got := aggregatorSweepAt(rig, agg, 91); got != 1 {
		t.Fatalf("a los 91 s el barrido cerró %d ventanas, quería 1", got)
	}
	job = rig.jobs.Jobs()[0]
	if job.Status != intake.StatusPending || !aggregatorSameEnvelope(job.SourceText, aggregatorEnvelope()) ||
		!slices.Equal(job.SourceRefs, []string{"wa-1", "wa-2"}) {
		t.Errorf("job = (status %q, sobre %+v, refs %v), quería UN job pending con sobre y los dos mensajes",
			job.Status, job.SourceText, job.SourceRefs)
	}
	if got := len(rig.composer.composed()); got != 2 {
		t.Errorf("composiciones = %d, quería 2: la que se tiró y la que cerró", got)
	}
	rig.requireClosingBudget(t, 2, 0)
}

// TestSweep_EmptyThreadClosesWithAnEmptyEnvelope: un hilo sin mensajes (sobre vacío, error nil) no
// deja la ventana abierta: cierra por la MISMA vía, con el sobre a NULL, y no es un error.
//
// Mata: «el sobre vacío no cierra» y «el sobre vacío se trata como un fallo del compositor».
func TestSweep_EmptyThreadClosesWithAnEmptyEnvelope(t *testing.T) {
	rig := newAggregatorRig()
	agg := rig.aggregator(WithSourceComposer(rig.composer))
	rig.composer.empty = true
	aggregatorObserveAt(rig, agg, aggregatorKey("event-1"), "wa-1", 0)
	rig.jobs.ResetCounters()

	if got := aggregatorSweepAt(rig, agg, 45); got != 1 {
		t.Fatalf("el barrido cerró %d ventanas, quería 1", got)
	}
	if job := rig.jobs.Jobs()[0]; job.Status != intake.StatusPending || !job.SourceText.Empty() {
		t.Errorf("job = (status %q, sobre %+v), quería pending y sin sobre", job.Status, job.SourceText)
	}
	rig.requireClosingBudget(t, 1, 0)
	if len(rig.log.at("error")) != 0 {
		t.Errorf("un hilo sin mensajes no es un error:\n%s", rig.log.dump())
	}
}

// TestSweep_ComposerFailureDoesNotRevertTheClose: si el compositor falla, la ventana se cierra
// igual —SIN sobre, por el CloseWindow de siempre—, cuenta como cerrada y el fallo va a Error. Con
// el contexto cancelado cierra igual pero no es una avería (D-F9-10).
//
// Mata: «el fallo de composición no cierra», «no se loguea» y «se loguea con el ctx cancelado».
func TestSweep_ComposerFailureDoesNotRevertTheClose(t *testing.T) {
	boom := errors.New("hilo ilegible")
	open := func() (*aggregatorRig, *IntakeAggregator) {
		rig := newAggregatorRig()
		agg := rig.aggregator(WithSourceComposer(rig.composer))
		rig.composer.err = boom
		aggregatorObserveAt(rig, agg, aggregatorKey("event-1"), "wa-1", 0)
		rig.jobs.ResetCounters()
		rig.clock.advance(45 * time.Second)
		return rig, agg
	}

	t.Run("live context", func(t *testing.T) {
		rig, agg := open()
		if got := agg.Sweep(context.Background()); got != 1 {
			t.Fatalf("el barrido cerró %d ventanas, quería 1: el fallo del compositor no deja la ventana abierta", got)
		}
		rig.requireStatus(t, "tras el fallo del compositor", intake.StatusPending)
		job := rig.jobs.Jobs()[0]
		if !job.SourceText.Empty() {
			t.Error("quedó un sobre escrito tras un compositor fallido")
		}
		rig.requireClosingBudget(t, 0, 1)
		rig.requireLine(t, "error", "agregador: la ventana se cerró pero el literal no se pudo componer (T1.4)",
			map[string]any{"error": boom, "tenant_id": aggregatorTenant, "job_id": job.ID})
		rig.requireLine(t, "debug", "agregador: ventana cerrada",
			map[string]any{"tenant_id": aggregatorTenant, "session_id": "session-9", "job_id": job.ID})
	})

	t.Run("cancelled context", func(t *testing.T) {
		rig, agg := open()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if got := agg.Sweep(ctx); got != 1 {
			t.Fatalf("el barrido cerró %d ventanas, quería 1", got)
		}
		rig.requireClosingBudget(t, 0, 1)
		if got := rig.log.at("error"); len(got) != 0 {
			t.Errorf("con el contexto cancelado el fallo del compositor dejó %d líneas a ERROR:\n%s", len(got), rig.log.dump())
		}
	})
}

// TestSweep_CloseWithEnvelopeFailure_IsAnErrorOnlyWhileTheContextLives es D-F9-10 en la rama nueva:
// si la sentencia que cierra con el sobre falla, la ventana sigue viva y entera (no hay medio
// cierre); con el contexto vivo es un Error, con el contexto cancelado es la parada y calla.
//
// Mata: «el fallo del cierre con sobre no se loguea» y «se loguea aunque el ctx esté cancelado».
func TestSweep_CloseWithEnvelopeFailure_IsAnErrorOnlyWhileTheContextLives(t *testing.T) {
	boom := errors.New("update rechazado")
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for name, c := range map[string]struct {
		ctx        context.Context
		wantErrors int
	}{
		"live context":      {context.Background(), 1},
		"cancelled context": {cancelled, 0},
	} {
		t.Run(name, func(t *testing.T) {
			rig := newAggregatorRig()
			agg := rig.aggregator(WithSourceComposer(rig.composer))
			aggregatorObserveAt(rig, agg, aggregatorKey("event-1"), "wa-1", 0)
			rig.jobs.FailCloseWithSourceTextWith(boom)
			rig.clock.advance(45 * time.Second)

			if got := agg.Sweep(c.ctx); got != 0 {
				t.Fatalf("el barrido contó %d cierres con la sentencia fallando", got)
			}
			job := rig.jobs.Jobs()[0]
			if job.Status != intake.StatusAggregating || !job.SourceText.Empty() {
				t.Errorf("job = (status %q, sobre %+v), quería la ventana viva y sin sobre", job.Status, job.SourceText)
			}
			if got := rig.log.at("error"); len(got) != c.wantErrors {
				t.Fatalf("líneas a ERROR = %d, quería %d:\n%s", len(got), c.wantErrors, rig.log.dump())
			}
			if c.wantErrors == 1 {
				rig.requireLine(t, "error", "agregador: no se pudo cerrar la ventana de captación",
					map[string]any{"error": boom, "tenant_id": aggregatorTenant, "session_id": "session-9", "job_id": job.ID})
			}
		})
	}
}

// TestWithSourceComposer_DefaultsToAnEmptyComposer es AG-8: sin compositor —o con nil— las
// ventanas cierran igual y el job queda `pending` SIN texto; nadie escribe el sobre. Un nil detrás
// de un compositor no lo quita: el caso «nil after ok» cablea uno que devuelve el sobre vacío.
func TestWithSourceComposer_DefaultsToAnEmptyComposer(t *testing.T) {
	kept := &aggregatorComposer{empty: true}
	defer func() {
		if got := len(kept.composed()); got != 1 {
			t.Errorf("nil after ok: el compositor cableado antes del nil se llamó %d veces, quería 1", got)
		}
	}()
	for name, opts := range map[string][]AggregatorOption{
		"not wired":    nil,
		"nil ignored":  {WithSourceComposer(nil)},
		"nil after ok": {WithSourceComposer(kept), WithSourceComposer(nil)},
	} {
		rig := newAggregatorRig()
		agg := rig.aggregator(opts...)
		aggregatorObserveAt(rig, agg, aggregatorKey("event-1"), "wa-1", 0)
		rig.jobs.ResetCounters()

		if got := aggregatorSweepAt(rig, agg, 45); got != 1 {
			t.Fatalf("%s: el barrido cerró %d ventanas, quería 1", name, got)
		}
		if job := rig.jobs.Jobs()[0]; job.Status != intake.StatusPending || !job.SourceText.Empty() {
			t.Errorf("%s: job = (status %q, sobre %+v), quería pending y sin texto", name, job.Status, job.SourceText)
		}
		rig.requireClosingBudget(t, 1, 0)
		if len(rig.log.at("error")) != 0 {
			t.Errorf("%s: cerrar sin compositor no es un error:\n%s", name, rig.log.dump())
		}
	}
}
