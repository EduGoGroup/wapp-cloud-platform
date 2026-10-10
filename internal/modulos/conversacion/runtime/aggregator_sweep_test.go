package runtime

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
)

// El barrido se conduce llamando a Sweep con el reloj movido, nunca esperando al ticker (eso es
// de aggregator_run_test.go).

// aggregatorSweepAt mueve el reloj hasta `at` segundos desde el arranque y barre.
func aggregatorSweepAt(rig *aggregatorRig, agg *IntakeAggregator, at int) int {
	target := aggregatorStart.Add(time.Duration(at) * time.Second)
	rig.clock.advance(target.Sub(rig.clock.now()))
	return agg.Sweep(context.Background())
}

// aggregatorObserveAt mueve el reloj hasta `at` segundos desde el arranque y observa un mensaje.
func aggregatorObserveAt(rig *aggregatorRig, agg *IntakeAggregator, key intake.WindowKey, waMessageID string, at int) {
	target := aggregatorStart.Add(time.Duration(at) * time.Second)
	rig.clock.advance(target.Sub(rig.clock.now()))
	agg.Observe(context.Background(), aggregatorRef(key, waMessageID, target))
}

// TestSweep_SilenceIsTheMainPath es AG-3: sin ningún intent, un mensaje y silencio cierran la
// ventana a los 45 s, ni un segundo antes. Y AG-6: un segundo barrido no cierra nada ni toca la
// fila, y la ventana cerrada lleva su Debug.
func TestSweep_SilenceIsTheMainPath(t *testing.T) {
	rig := newAggregatorRig()
	agg := rig.aggregator()
	key := aggregatorKey("event-1")
	aggregatorObserveAt(rig, agg, key, "wa-1", 0)

	if got := aggregatorSweepAt(rig, agg, 44); got != 0 {
		t.Fatalf("a los 44 s el barrido cerró %d ventanas, quería 0", got)
	}
	rig.requireStatus(t, "a los 44 s", intake.StatusAggregating)

	if got := aggregatorSweepAt(rig, agg, 45); got != 1 {
		t.Fatalf("a los 45 s el barrido cerró %d ventanas, quería 1", got)
	}
	rig.requireStatus(t, "a los 45 s", intake.StatusPending)
	closed := rig.jobs.Jobs()[0]
	rig.requireLine(t, "debug", "agregador: ventana cerrada",
		map[string]any{"tenant_id": aggregatorTenant, "session_id": "session-9", "job_id": closed.ID})

	if got := aggregatorSweepAt(rig, agg, 300); got != 0 {
		t.Errorf("un segundo barrido cerró %d ventanas, quería 0", got)
	}
	if again := rig.jobs.Jobs(); len(again) != 1 || !again[0].UpdatedAt.Equal(closed.UpdatedAt) || again[0].Status != intake.StatusPending {
		t.Errorf("el segundo barrido tocó la fila cerrada: %+v", again)
	}
	if len(rig.log.at("error"))+len(rig.log.at("warn"))+len(rig.log.at("info")) != 0 {
		t.Errorf("un cierre normal dejó algo más que Debug:\n%s", rig.log.dump())
	}
}

// TestSweep_SlowBurstIsOneJob es T1.8-1 (b): tres mensajes separados 30 s son UNA petición. El
// silencio se mide desde el ÚLTIMO mensaje: el barrido de t=50 no cierra (con el ancla en el
// primero partiría el pedido en dos) y la ventana cierra a los 105 s, con la base de fechas del
// primero.
func TestSweep_SlowBurstIsOneJob(t *testing.T) {
	rig := newAggregatorRig()
	agg := rig.aggregator()
	key := aggregatorKey("event-1")
	aggregatorObserveAt(rig, agg, key, "wa-1", 0)
	aggregatorObserveAt(rig, agg, key, "wa-2", 30)

	if got := aggregatorSweepAt(rig, agg, 50); got != 0 {
		t.Fatalf("a los 50 s el barrido cerró %d ventanas: el silencio se mide desde el último mensaje (t=30)", got)
	}
	aggregatorObserveAt(rig, agg, key, "wa-3", 60)
	if got := aggregatorSweepAt(rig, agg, 104); got != 0 {
		t.Fatalf("a los 104 s el barrido cerró %d ventanas, quería 0", got)
	}
	if got := aggregatorSweepAt(rig, agg, 105); got != 1 {
		t.Fatalf("a los 105 s el barrido cerró %d ventanas, quería 1", got)
	}

	jobs := rig.jobs.Jobs()
	if len(jobs) != 1 || !slices.Equal(jobs[0].SourceRefs, []string{"wa-1", "wa-2", "wa-3"}) {
		t.Fatalf("filas = %+v, quería UN job con las tres referencias", jobs)
	}
	if !jobs[0].MessageTS.Equal(aggregatorStart) {
		t.Errorf("message_ts = %v, quería el del primer mensaje", jobs[0].MessageTS)
	}
}

// TestSweep_DripClosesAtTheCeiling es T1.8-1 (c): una conversación que gotea cada 40 s nunca
// alcanza 45 s de silencio; la cierra el techo, a los 120 s de nacer. El techo corta el pedido, no
// la conversación: el mensaje siguiente abre otra ventana.
func TestSweep_DripClosesAtTheCeiling(t *testing.T) {
	rig := newAggregatorRig()
	agg := rig.aggregator()
	key := aggregatorKey("event-1")
	for i, at := range []int{0, 40, 80} {
		aggregatorObserveAt(rig, agg, key, []string{"wa-1", "wa-2", "wa-3"}[i], at)
		if got := agg.Sweep(context.Background()); got != 0 {
			t.Fatalf("a los %d s el barrido cerró %d ventanas, quería 0", at, got)
		}
	}
	if got := aggregatorSweepAt(rig, agg, 119); got != 0 {
		t.Fatalf("a los 119 s el barrido cerró %d ventanas, quería 0", got)
	}

	if got := aggregatorSweepAt(rig, agg, 120); got != 1 {
		t.Fatalf("a los 120 s el barrido cerró %d ventanas, quería 1: sin techo el goteo no cierra nunca", got)
	}

	aggregatorObserveAt(rig, agg, key, "wa-4", 120)
	rig.requireStatus(t, "tras el mensaje que sigue al techo", intake.StatusPending, intake.StatusAggregating)
	jobs := rig.jobs.Jobs()
	if !slices.Equal(jobs[0].SourceRefs, []string{"wa-1", "wa-2", "wa-3"}) || !slices.Equal(jobs[1].SourceRefs, []string{"wa-4"}) {
		t.Errorf("refs = %v y %v, quería tres en el primer job y una en el segundo", jobs[0].SourceRefs, jobs[1].SourceRefs)
	}
}

// TestSweep_DeadlinesComeFromTenantSettings: los dos plazos se leen de la config del tenant y se
// respetan TAL CUAL. El 0 es un override («flush inmediato» / «vencido siempre»), no un «sin
// configurar» que se parchea con el default.
func TestSweep_DeadlinesComeFromTenantSettings(t *testing.T) {
	cases := []struct {
		name     string
		silence  time.Duration
		ceiling  time.Duration
		messages []int // segundos en que llega un mensaje
		openAt   int   // último segundo en que el barrido NO cierra (-1: no se comprueba)
		closedAt int   // segundo en que el barrido cierra
	}{
		{"short silence", 10 * time.Second, 120 * time.Second, []int{0}, 9, 10},
		{"long silence", 80 * time.Second, 120 * time.Second, []int{0}, 79, 80},
		{"zero silence is an immediate flush", 0, 120 * time.Second, []int{0}, -1, 0},
		{"tenant ceiling", 45 * time.Second, 90 * time.Second, []int{0, 40, 80}, 89, 90},
		{"zero ceiling is always due", 45 * time.Second, 0, []int{0}, -1, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rig := newAggregatorRig()
			agg := rig.aggregator()
			rig.settings.setDeadlines(aggregatorTenant, c.silence, c.ceiling)
			key := aggregatorKey("event-1")
			for i, at := range c.messages {
				aggregatorObserveAt(rig, agg, key, []string{"wa-1", "wa-2", "wa-3"}[i], at)
			}
			if c.openAt >= 0 {
				if got := aggregatorSweepAt(rig, agg, c.openAt); got != 0 {
					t.Fatalf("a los %d s el barrido cerró %d ventanas, quería 0", c.openAt, got)
				}
			}
			if got := aggregatorSweepAt(rig, agg, c.closedAt); got != 1 {
				t.Fatalf("a los %d s el barrido cerró %d ventanas, quería 1", c.closedAt, got)
			}
		})
	}
}

// TestSweep_ReadsTheSettingsOncePerTenantPerPass: una pasada lee tenant_settings UNA vez por
// tenant aunque tenga varias ventanas, y la pasada siguiente vuelve a leer (un cambio de config se
// ve sin reiniciar).
func TestSweep_ReadsTheSettingsOncePerTenantPerPass(t *testing.T) {
	rig := newAggregatorRig()
	agg := rig.aggregator()
	other := intake.WindowKey{TenantID: "tenant-2", SessionID: "session-2", ContactID: "contact-2", EventID: "event-9"}
	for _, key := range []intake.WindowKey{aggregatorKey("event-1"), aggregatorKey("event-2"), aggregatorKey("event-3"), other} {
		aggregatorObserveAt(rig, agg, key, "wa-1", 0)
	}

	if got := aggregatorSweepAt(rig, agg, 10); got != 0 {
		t.Fatalf("a los 10 s el barrido cerró %d ventanas, quería 0", got)
	}
	if rig.settings.readsOf(aggregatorTenant) != 1 || rig.settings.readsOf("tenant-2") != 1 {
		t.Errorf("lecturas de tenant_settings = (tenant-1: %d, tenant-2: %d), quería una por tenant y por pasada",
			rig.settings.readsOf(aggregatorTenant), rig.settings.readsOf("tenant-2"))
	}

	rig.settings.setDeadlines(aggregatorTenant, 5*time.Second, 120*time.Second)
	if got := agg.Sweep(context.Background()); got != 3 {
		t.Fatalf("tras bajar el silencio del tenant a 5 s, el barrido cerró %d ventanas, quería sus 3", got)
	}
	if rig.settings.readsOf(aggregatorTenant) != 2 {
		t.Errorf("la segunda pasada no volvió a leer la config: %d lecturas", rig.settings.readsOf(aggregatorTenant))
	}
	rig.requireStatus(t, "tras la segunda pasada", intake.StatusPending, intake.StatusPending, intake.StatusPending, intake.StatusAggregating)
}

// TestSweep_UnreadableSettingsFallBackToPlatformDefaults: si la config no se puede leer —o no hay
// de dónde leerla— valen 45 s y 120 s, no «no cerrar»; el fallo queda en Warn.
func TestSweep_UnreadableSettingsFallBackToPlatformDefaults(t *testing.T) {
	t.Run("settings fail", func(t *testing.T) {
		rig := newAggregatorRig()
		agg := rig.aggregator()
		rig.settings.setDeadlines(aggregatorTenant, 10*time.Second, 20*time.Second)
		boom := errors.New("config ilegible")
		rig.settings.failWith(boom)
		aggregatorObserveAt(rig, agg, aggregatorKey("event-1"), "wa-1", 0)

		if got := aggregatorSweepAt(rig, agg, 44); got != 0 {
			t.Fatalf("a los 44 s el barrido cerró %d ventanas, quería 0 (default de plataforma: 45 s)", got)
		}
		rig.requireLine(t, "warn",
			"agregador: no se pudieron leer los plazos de la ventana (aggregation_window_seconds/aggregation_max_seconds); se usan los defaults de plataforma",
			map[string]any{"error": boom, "tenant_id": aggregatorTenant})
		if got := aggregatorSweepAt(rig, agg, 45); got != 1 {
			t.Fatalf("a los 45 s el barrido cerró %d ventanas, quería 1", got)
		}
	})

	t.Run("no settings at all", func(t *testing.T) {
		rig := newAggregatorRig()
		agg := NewIntakeAggregator(rig.log, rig.jobs, nil, rig.ents, WithAggregatorClock(rig.now))
		key := aggregatorKey("event-1")
		for i, at := range []int{0, 40, 80} {
			aggregatorObserveAt(rig, agg, key, []string{"wa-1", "wa-2", "wa-3"}[i], at)
		}
		if got := aggregatorSweepAt(rig, agg, 119); got != 0 {
			t.Fatalf("a los 119 s el barrido cerró %d ventanas, quería 0 (techo de plataforma: 120 s)", got)
		}
		if got := aggregatorSweepAt(rig, agg, 120); got != 1 {
			t.Fatalf("a los 120 s el barrido cerró %d ventanas, quería 1", got)
		}
		if len(rig.log.at("warn")) != 0 {
			t.Errorf("no tener settings no es un fallo de lectura:\n%s", rig.log.dump())
		}
	})
}

// TestWithSweepBatch_IsACeilingPerPass: el batch acota el trabajo de UNA pasada, no el negocio: con
// tres ventanas vencidas y un batch de 1 hacen falta tres pasadas y no se pierde ninguna. <= 0 se
// ignora.
func TestWithSweepBatch_IsACeilingPerPass(t *testing.T) {
	open := func(rig *aggregatorRig, agg *IntakeAggregator) {
		for _, event := range []string{"event-1", "event-2", "event-3"} {
			aggregatorObserveAt(rig, agg, aggregatorKey(event), "wa-1", 0)
		}
	}

	t.Run("batch of one", func(t *testing.T) {
		batch := WithSweepBatch(1)
		rig := newAggregatorRig()
		agg := rig.aggregator(batch)
		open(rig, agg)
		passes := make([]int, 0, 4)
		for range 4 {
			passes = append(passes, aggregatorSweepAt(rig, agg, 45))
		}
		if !slices.Equal(passes, []int{1, 1, 1, 0}) {
			t.Errorf("cierres por pasada = %v, quería [1 1 1 0]", passes)
		}
		rig.requireStatus(t, "tras cuatro pasadas", intake.StatusPending, intake.StatusPending, intake.StatusPending)
	})

	for _, ignored := range []int{0, -3} {
		batch := WithSweepBatch(ignored)
		rig := newAggregatorRig()
		agg := rig.aggregator(batch)
		open(rig, agg)
		if got := aggregatorSweepAt(rig, agg, 45); got != 3 {
			t.Errorf("WithSweepBatch(%d): la pasada cerró %d ventanas, quería las 3 (el valor se ignora)", ignored, got)
		}
	}
}

// TestSweep_LostRaceIsNotAClose es la otra mitad de AG-6: si el almacén dice que otro cerró antes
// (false sin error), la ventana no se cuenta y no es un error. Se compuso —una vez: el sobre se
// arma ANTES de saber quién cierra— pero ese sobre se tira: no se escribe por ninguna otra vía.
func TestSweep_LostRaceIsNotAClose(t *testing.T) {
	rig := newAggregatorRig()
	agg := rig.aggregator(WithSourceComposer(rig.composer))
	key := aggregatorKey("event-1")
	aggregatorObserveAt(rig, agg, key, "wa-1", 0)
	rig.jobs.set(func(j *aggregatorJobs) { j.lostRace = true })

	if got := aggregatorSweepAt(rig, agg, 45); got != 0 {
		t.Errorf("el barrido contó %d cierres que no hizo él", got)
	}
	if got := rig.composer.composed(); !slices.Equal(got, []intake.WindowKey{key}) {
		t.Errorf("ventanas compuestas = %v, quería la vencida, una sola vez", got)
	}
	if job := rig.jobs.Jobs()[0]; job.Status != intake.StatusAggregating || !job.SourceText.Empty() {
		t.Errorf("job = (status %q, sobre vacío %v): quien pierde la carrera no toca la fila", job.Status, job.SourceText.Empty())
	}
	if got := rig.jobs.Counters(); got.PutSourceText != 0 || got.Close != 0 {
		t.Errorf("presupuesto = %+v: el sobre de una carrera perdida no se escribe por otra vía", got)
	}
	if len(rig.log.at("error"))+len(rig.log.at("debug")) != 0 {
		t.Errorf("perder la carrera no se loguea:\n%s", rig.log.dump())
	}
}

// TestSweep_StoreFailuresAreLoggedAndSkipped: con el contexto VIVO, un fallo al listar o al cerrar
// va a Error (es el control positivo de D-F9-10). Listar: la pasada devuelve 0. Cerrar: esa
// ventana no cuenta y la pasada sigue con las demás.
func TestSweep_StoreFailuresAreLoggedAndSkipped(t *testing.T) {
	t.Run("listing fails", func(t *testing.T) {
		rig := newAggregatorRig()
		agg := rig.aggregator()
		aggregatorObserveAt(rig, agg, aggregatorKey("event-1"), "wa-1", 0)
		boom := errors.New("base caída")
		rig.jobs.set(func(j *aggregatorJobs) { j.listErr = boom })

		if got := aggregatorSweepAt(rig, agg, 45); got != 0 {
			t.Errorf("con el listado caído el barrido devolvió %d, quería 0", got)
		}
		rig.requireLine(t, "error", "agregador: no se pudieron listar las ventanas vivas", map[string]any{"error": boom})
		rig.requireStatus(t, "con el listado caído", intake.StatusAggregating)
	})

	t.Run("closing one window fails", func(t *testing.T) {
		rig := newAggregatorRig()
		agg := rig.aggregator(WithSourceComposer(rig.composer))
		broken, healthy := aggregatorKey("event-1"), aggregatorKey("event-2")
		aggregatorObserveAt(rig, agg, broken, "wa-1", 0)
		aggregatorObserveAt(rig, agg, healthy, "wa-1", 0)
		boom := errors.New("update rechazado")
		rig.jobs.set(func(j *aggregatorJobs) { j.closeErr[broken] = boom })

		if got := aggregatorSweepAt(rig, agg, 45); got != 1 {
			t.Fatalf("el barrido cerró %d ventanas, quería 1: la que falla no cuenta y la otra sí", got)
		}
		rig.requireStatus(t, "tras el fallo de un cierre", intake.StatusAggregating, intake.StatusPending)
		rig.requireLine(t, "error", "agregador: no se pudo cerrar la ventana de captación",
			map[string]any{"error": boom, "tenant_id": aggregatorTenant, "session_id": "session-9", "job_id": rig.jobs.Jobs()[0].ID})
		if got := rig.composer.composed(); !slices.Equal(got, []intake.WindowKey{broken, healthy}) {
			t.Errorf("ventanas compuestas = %v, quería las dos vencidas: se compone antes de cerrar", got)
		}
		jobs := rig.jobs.Jobs()
		if !jobs[0].SourceText.Empty() || !aggregatorSameEnvelope(jobs[1].SourceText, aggregatorEnvelope()) {
			t.Errorf("sobres = (%+v, %+v), quería ninguno en la que falló y el del compositor en la que cerró",
				jobs[0].SourceText, jobs[1].SourceText)
		}
	})
}

// TestSweep_NilSafe: sobre un receptor nil, o sin almacén o sin logger, devuelve 0 sin tocar nada.
func TestSweep_NilSafe(t *testing.T) {
	rig := newAggregatorRig()
	rig.jobs.Seed(intake.Job{Key: aggregatorKey("event-1"), Status: intake.StatusAggregating, CreatedAt: aggregatorStart.Add(-time.Hour)})
	rig.jobs.ResetCounters()
	var nilAggregator *IntakeAggregator
	aggregators := map[string]*IntakeAggregator{
		"nil receiver": nilAggregator,
		"no jobs":      NewIntakeAggregator(rig.log, nil, rig.settings, rig.ents),
		"no logger":    NewIntakeAggregator(nil, rig.jobs, rig.settings, rig.ents),
	}
	for name, agg := range aggregators {
		if got := agg.Sweep(context.Background()); got != 0 {
			t.Errorf("%s: Sweep devolvió %d, quería 0", name, got)
		}
		if got := agg.RecoverAtBoot(context.Background()); got != 0 {
			t.Errorf("%s: RecoverAtBoot devolvió %d, quería 0", name, got)
		}
	}
	if got := rig.jobs.Counters(); got != (intake.Counters{}) {
		t.Errorf("un agregador incompleto tocó intake_jobs: %+v", got)
	}
	rig.requireStatus(t, "tras los barridos vacíos", intake.StatusAggregating)
}

// TestRecoverAtBoot_ClosesWhatExpiredWhileDown es AG-6 (T1.1): el estado de la ventana vive en la
// tabla, no en el proceso. Un agregador NUEVO sobre la misma base —un reinicio— cierra al arrancar
// lo que venció mientras no había nadie, y lo dice en Info; si no venció nada, no dice nada.
func TestRecoverAtBoot_ClosesWhatExpiredWhileDown(t *testing.T) {
	rig := newAggregatorRig()
	before := rig.aggregator()
	aggregatorObserveAt(rig, before, aggregatorKey("event-1"), "wa-1", 0)
	aggregatorObserveAt(rig, before, aggregatorKey("event-2"), "wa-1", 0)

	fresh := rig.aggregator()
	if got := fresh.RecoverAtBoot(context.Background()); got != 0 {
		t.Fatalf("un arranque inmediato cerró %d ventanas, quería 0", got)
	}
	if len(rig.log.at("info")) != 0 {
		t.Errorf("un arranque sin nada vencido logueó:\n%s", rig.log.dump())
	}

	rig.clock.advance(10 * time.Minute)
	restarted := rig.aggregator()
	if got := restarted.RecoverAtBoot(context.Background()); got != 2 {
		t.Fatalf("el arranque tras 10 minutos cerró %d ventanas, quería 2", got)
	}
	rig.requireStatus(t, "tras la recuperación", intake.StatusPending, intake.StatusPending)
	rig.requireLine(t, "info", "agregador: ventanas vencidas cerradas al arrancar", map[string]any{"jobs": 2})
}

// TestSweep_ZeroDeadlineIsExpiredWhateverTheClocksSay: un plazo a 0 es «vencido siempre» —flush
// inmediato para el silencio, y lo mismo para el techo—, no «vencido cuando el reloj alcance a la
// fila». Las fechas de la fila las pone el reloj de la base y el barrido usa el del proceso: con
// la base 5 s por delante, la ventana se cierra igual en el primer barrido que la ve.
func TestSweep_ZeroDeadlineIsExpiredWhateverTheClocksSay(t *testing.T) {
	cases := []struct {
		name             string
		silence, ceiling time.Duration
	}{
		{"zero silence", 0, 120 * time.Second},
		{"zero ceiling", 45 * time.Second, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rig := newAggregatorRig()
			rig.settings.setDeadlines(aggregatorTenant, c.silence, c.ceiling)
			agg := rig.aggregator()
			aggregatorObserveAt(rig, agg, aggregatorKey("event-1"), "wa-1", 10) // la fila nace en el segundo 10

			if got := aggregatorSweepAt(rig, agg, 5); got != 1 { // el proceso va por el segundo 5
				t.Fatalf("el barrido cerró %d ventanas, quería 1: un plazo a 0 está vencido siempre", got)
			}
			rig.requireStatus(t, "tras el barrido", intake.StatusPending)
		})
	}
}
