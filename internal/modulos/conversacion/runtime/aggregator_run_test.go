package runtime

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
)

// Run es la única goroutine del agregador. Sus tests corren en una burbuja de synctest: el reloj
// es simulado (time.Now es a la vez el reloj inyectado del agregador y el del almacén) y solo
// avanza cuando todas las goroutines de la burbuja están bloqueadas, así que «a los 45 s» es
// exacto y no cuesta 45 segundos.

// aggregatorElapse deja pasar d de tiempo SIMULADO y espera a que el agregador vuelva a quedarse
// quieto. Solo vale dentro de una burbuja.
func aggregatorElapse(d time.Duration) {
	<-time.After(d)
	synctest.Wait()
}

// aggregatorStartRun arranca Run en su goroutine. Quien llama difiere el cancel: sin él, un fallo
// del test dejaría a Run barriendo para siempre dentro de la burbuja.
func aggregatorStartRun(agg *IntakeAggregator) (cancel context.CancelFunc, done <-chan struct{}) {
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		agg.Run(ctx)
	}()
	synctest.Wait() // el RecoverAtBoot del arranque ya ocurrió
	return cancel, finished
}

// aggregatorRequireStopped cancela y exige que Run haya vuelto.
func aggregatorRequireStopped(t *testing.T, cancel context.CancelFunc, done <-chan struct{}) {
	t.Helper()
	cancel()
	synctest.Wait()
	select {
	case <-done:
	default:
		t.Fatal("Run no volvió tras cancelar el contexto")
	}
}

// TestRun_TheTickClosesTheWindow: en producción nadie llama a Sweep a mano; lo llama el tick. La
// ventana nace con su plazo sin cumplir (el RecoverAtBoot del arranque no puede cerrarla) y el
// tick la cierra a su hora, con el intervalo por defecto de 5 s.
func TestRun_TheTickClosesTheWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rig := newAggregatorRigAt(time.Now)
		agg := rig.aggregator()
		agg.Observe(context.Background(), aggregatorRef(aggregatorKey("event-1"), "wa-1", time.Now()))
		cancel, done := aggregatorStartRun(agg)
		defer cancel()
		rig.requireStatus(t, "al arrancar Run", intake.StatusAggregating)

		aggregatorElapse(44 * time.Second)
		rig.requireStatus(t, "a los 44 s", intake.StatusAggregating)

		aggregatorElapse(time.Second)
		rig.requireStatus(t, "a los 45 s", intake.StatusPending)

		aggregatorRequireStopped(t, cancel, done)
		if len(rig.log.at("error")) != 0 {
			t.Errorf("una vida normal de Run dejó errores:\n%s", rig.log.dump())
		}
	})
}

// TestWithSweepInterval: el intervalo inyectado fija el grano del cierre —la ventana de 45 s cierra
// en el primer tick que la ve vencida— y un valor <= 0 deja los 5 s de plataforma.
func TestWithSweepInterval(t *testing.T) {
	cases := []struct {
		name     string
		interval time.Duration
		openAt   time.Duration // todavía viva
		closedAt time.Duration // ya cerrada
	}{
		{"seven seconds: ticks at 42 and 49", 7 * time.Second, 48 * time.Second, 49 * time.Second},
		{"twenty seconds: ticks at 40 and 60", 20 * time.Second, 59 * time.Second, 60 * time.Second},
		{"zero is ignored", 0, 44 * time.Second, 45 * time.Second},
		{"negative is ignored", -time.Second, 44 * time.Second, 45 * time.Second},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				interval := WithSweepInterval(c.interval)
				rig := newAggregatorRigAt(time.Now)
				agg := rig.aggregator(interval)
				agg.Observe(context.Background(), aggregatorRef(aggregatorKey("event-1"), "wa-1", time.Now()))
				cancel, done := aggregatorStartRun(agg)
				defer cancel()

				aggregatorElapse(c.openAt)
				rig.requireStatus(t, "antes del tick que la cierra", intake.StatusAggregating)
				aggregatorElapse(c.closedAt - c.openAt)
				rig.requireStatus(t, "en el tick que la cierra", intake.StatusPending)

				aggregatorRequireStopped(t, cancel, done)
			})
		})
	}
}

// TestRun_TheHintWakesTheSweep es AG-4 (T1.8-1 (h)): el adelanto por intent NO espera al tick. El
// ticker va a una hora y el reloj no avanza: lo único capaz de cerrar la ventana es el despertador.
func TestRun_TheHintWakesTheSweep(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hourly := WithSweepInterval(time.Hour)
		rig := newAggregatorRigAt(time.Now)
		agg := rig.aggregator(hourly)
		key := aggregatorKey("event-1")
		agg.Observe(context.Background(), aggregatorRef(key, "wa-1", time.Now()))
		cancel, done := aggregatorStartRun(agg)
		defer cancel()
		start := time.Now()

		agg.OnClassified(key, IntentIntakeRequest, 0.9)
		synctest.Wait()

		rig.requireStatus(t, "tras el aviso", intake.StatusPending)
		if elapsed := time.Since(start); elapsed != 0 {
			t.Errorf("pasaron %v de reloj: la ventana tenía que cerrarse por el aviso, sin esperar a nada", elapsed)
		}
		aggregatorRequireStopped(t, cancel, done)
	})
}

// TestRun_RecoversAtBootAndKeepsNoTimerPerWindow es AG-6: sin Run corriendo, una ventana vencida
// hace diez minutos sigue viva (no hay un timer por ventana que la cierre); al arrancar, Run la
// cierra en el acto, sin esperar a ningún tick, y lo dice en Info.
func TestRun_RecoversAtBootAndKeepsNoTimerPerWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hourly := WithSweepInterval(time.Hour)
		rig := newAggregatorRigAt(time.Now)
		agg := rig.aggregator(hourly)
		agg.Observe(context.Background(), aggregatorRef(aggregatorKey("event-1"), "wa-1", time.Now()))

		aggregatorElapse(10 * time.Minute)
		rig.requireStatus(t, "diez minutos después, sin Run", intake.StatusAggregating)

		start := time.Now()
		cancel, done := aggregatorStartRun(agg)
		defer cancel()

		rig.requireStatus(t, "al arrancar Run", intake.StatusPending)
		if elapsed := time.Since(start); elapsed != 0 {
			t.Errorf("pasaron %v de reloj: la recuperación es del arranque, no de un tick", elapsed)
		}
		rig.requireLine(t, "info", "agregador: ventanas vencidas cerradas al arrancar", map[string]any{"jobs": 1})
		aggregatorRequireStopped(t, cancel, done)
	})
}

// TestRun_ReturnsAtOnceWithoutAStore: sobre un receptor nil o un agregador sin almacén, Run vuelve
// en el acto, sin esperar a que nadie cancele.
func TestRun_ReturnsAtOnceWithoutAStore(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var nilAggregator *IntakeAggregator
		nilAggregator.Run(context.Background())

		rig := newAggregatorRigAt(time.Now)
		start := time.Now()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel() // si Run no vuelve solo, que el fallo no lo deje barriendo en la burbuja
		done := make(chan struct{})
		go func() {
			defer close(done)
			NewIntakeAggregator(rig.log, nil, rig.settings, rig.ents).Run(ctx)
		}()
		synctest.Wait()

		select {
		case <-done:
		default:
			t.Fatal("Run sin almacén se quedó esperando al ticker en vez de volver en el acto")
		}
		if elapsed := time.Since(start); elapsed != 0 {
			t.Errorf("Run sin almacén se quedó %v esperando", elapsed)
		}
	})
}

// TestRun_ContextCancelled_ReturnsWithoutLoggingAtError es D-F9-10: la parada no es un error. Si el
// contexto se cancela antes de entrar, mientras Run espera, o con una llamada al almacén o al
// compositor a medias, Run vuelve y el log no tiene NI UNA línea a ERROR. (El control positivo es
// TestSweep_StoreFailuresAreLoggedAndSkipped, TestSweep_ComposerFailureDoesNotRevertTheClose y
// TestSweep_CloseWithEnvelopeFailure_IsAnErrorOnlyWhileTheContextLives: con el contexto vivo, esos
// mismos fallos sí van a ERROR.)
//
// La composición ocurre ANTES del cierre (D-F8-13): una parada que la corta deja la ventana VIVA
// —el compositor devuelve el error del contexto y el cierre sin sobre que le sigue falla contra una
// base que, como Postgres, rechaza un contexto cancelado—. La recoge el arranque siguiente.
func TestRun_ContextCancelled_ReturnsWithoutLoggingAtError(t *testing.T) {
	sites := []struct {
		name    string
		prepare func(rig *aggregatorRig)
		want    string // estado de la ventana tras la parada
	}{
		{"while idle", func(*aggregatorRig) {}, intake.StatusPending},
		{"during the listing", func(rig *aggregatorRig) {
			rig.jobs.set(func(j *aggregatorJobs) { j.blockList = true })
		}, intake.StatusAggregating},
		{"during a close", func(rig *aggregatorRig) {
			rig.jobs.set(func(j *aggregatorJobs) { j.blockClose = true })
		}, intake.StatusAggregating},
		{"during the composition", func(rig *aggregatorRig) {
			rig.composer.block = true
			rig.jobs.set(func(j *aggregatorJobs) { j.ctxAware = true })
		}, intake.StatusAggregating},
	}
	for _, site := range sites {
		t.Run(site.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				rig := newAggregatorRigAt(time.Now)
				rig.settings.setDeadlines(aggregatorTenant, 0, 0) // vencida desde que nace
				agg := rig.aggregator(WithSourceComposer(rig.composer))
				agg.Observe(context.Background(), aggregatorRef(aggregatorKey("event-1"), "wa-1", time.Now()))
				site.prepare(rig)

				cancel, done := aggregatorStartRun(agg) // Run queda en su espera o con la llamada a medias
				defer cancel()
				aggregatorRequireStopped(t, cancel, done)

				if got := rig.log.at("error"); len(got) != 0 {
					t.Errorf("la parada dejó %d líneas a ERROR:\n%s", len(got), rig.log.dump())
				}
				rig.requireStatus(t, "tras la parada", site.want)
				if site.name == "during the composition" && len(rig.composer.composed()) != 1 {
					t.Errorf("composiciones = %d, quería 1: la parada tenía que cortar la composición", len(rig.composer.composed()))
				}
			})
		})
	}

	t.Run("already cancelled before Run", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			rig := newAggregatorRigAt(time.Now)
			rig.jobs.set(func(j *aggregatorJobs) { j.ctxAware = true }) // como Postgres: con el ctx cancelado, falla
			agg := rig.aggregator()
			agg.Observe(context.Background(), aggregatorRef(aggregatorKey("event-1"), "wa-1", time.Now()))
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			agg.Run(ctx)

			if got := rig.log.at("error"); len(got) != 0 {
				t.Errorf("arrancar con el contexto ya cancelado dejó %d líneas a ERROR:\n%s", len(got), rig.log.dump())
			}
			rig.requireStatus(t, "tras la parada", intake.StatusAggregating)
		})
	})
}

// aggregatorGatedComposer retiene cada composición hasta que el test abre la compuerta: es la
// forma de tener a Run DENTRO de un barrido el tiempo que haga falta.
type aggregatorGatedComposer struct{ open chan struct{} }

func (g aggregatorGatedComposer) Compose(ctx context.Context, _ intake.WindowKey) (intake.SourceText, error) {
	select {
	case <-g.open:
	case <-ctx.Done():
	}
	return aggregatorEnvelope(), nil
}

// TestRun_AHintDuringASweepIsNotLost es la promesa del despertador («no se pierde ningún
// despertar»): un aviso que llega mientras Run está OCUPADO en un barrido queda pendiente en el
// buffer y Run lo atiende al terminar, sin esperar al tick. El ticker va a una hora y el reloj no
// avanza: solo el aviso guardado puede cerrar la segunda ventana.
func TestRun_AHintDuringASweepIsNotLost(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hourly := WithSweepInterval(time.Hour)
		gate := aggregatorGatedComposer{open: make(chan struct{})}
		rig := newAggregatorRigAt(time.Now)
		agg := rig.aggregator(hourly, WithSourceComposer(gate))
		first, second := aggregatorKey("event-1"), aggregatorKey("event-2")
		agg.Observe(context.Background(), aggregatorRef(first, "wa-1", time.Now()))
		agg.Observe(context.Background(), aggregatorRef(second, "wa-1", time.Now()))
		cancel, done := aggregatorStartRun(agg)
		defer cancel()
		start := time.Now()

		agg.OnClassified(first, IntentIntakeRequest, 0.9)
		synctest.Wait() // Run está dentro del barrido, retenido en la composición de la primera
		// La composición va ANTES del cierre (D-F8-13): con ella retenida, la primera sigue viva.
		rig.requireStatus(t, "con el barrido a medias", intake.StatusAggregating, intake.StatusAggregating)

		agg.OnClassified(second, IntentIntakeRequest, 0.9) // llega con Run ocupado: nadie escucha
		close(gate.open)
		synctest.Wait()

		rig.requireStatus(t, "al terminar el barrido en curso", intake.StatusPending, intake.StatusPending)
		if elapsed := time.Since(start); elapsed != 0 {
			t.Errorf("pasaron %v de reloj: el aviso pendiente tenía que atenderse sin esperar al tick", elapsed)
		}
		aggregatorRequireStopped(t, cancel, done)
	})
}
