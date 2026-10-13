package pipeline_test

// pipeline_test.go — el contrato de pipeline.go: los puertos, ErrNotWired, CallTimeoutFloor,
// Config, las opciones y NewWorker. El bucle está en pipeline_loop_test.go; lo que le pasa
// a un job, en pipeline_chain_test.go, pipeline_capacity_test.go y pipeline_outcome_test.go.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/pipeline"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/stages"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo/indice"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

// Los puertos del worker los satisfacen las piezas de PRODUCCIÓN (lo que el arranque
// cablea) y los dobles de estos tests. Comprobado en compilación: si una etapa cambia de
// firma, esto deja de compilar antes que el arranque.
var (
	_ pipeline.IdeasStage         = (*stages.P2)(nil)
	_ pipeline.SpecsStage         = (*stages.P3)(nil)
	_ pipeline.NormalizationStage = (*stages.P4)(nil)
	_ pipeline.MatchStage         = (*stages.Match)(nil)
	_ pipeline.DraftStage         = (*stages.Draft)(nil)
	_ pipeline.Catalogs           = (*indice.Cache)(nil)
	_ pipeline.ShippingZones      = (*intakes.Postgres)(nil)
	_ pipeline.ShippingZones      = (*intakes.MemoryStore)(nil)

	_ pipeline.IdeasStage         = (*fakeP2)(nil)
	_ pipeline.SpecsStage         = (*fakeP3)(nil)
	_ pipeline.NormalizationStage = (*fakeP4)(nil)
	_ pipeline.MatchStage         = (*fakeMatch)(nil)
	_ pipeline.DraftStage         = (*fakeDraft)(nil)
	_ pipeline.Decrypter          = (*fakeDecrypter)(nil)
)

// startupLine arranca Run, espera a que esté en su select, lo para y devuelve la línea del
// arranque.
func startupLine(t *testing.T, r *rig) logLine {
	t.Helper()
	stop := r.start(t)
	r.ticker.tick(t)
	stop()
	return r.log.one(t, "INFO", "pipeline: worker arrancado")
}

// TestNewWorker_MissingPiece_ReturnsErrNotWired: como las etapas, un worker sin una pieza
// se niega a nacer. Las NUEVE son obligatorias: con `match`, `draft` o el catálogo como
// opción, un worker sin borrador terminaría jobs en `done` sin `intake_id`.
func TestNewWorker_MissingPiece_ReturnsErrNotWired(t *testing.T) {
	r := newParts(t)
	cfg := pipeline.Config{}
	cases := map[string]func() (*pipeline.Worker, error){
		"no log": func() (*pipeline.Worker, error) {
			return pipeline.NewWorker(nil, r.store, r.p2, r.p3, r.p4, r.match, r.draft, r.catalogs, r.decrypter, cfg)
		},
		"no store": func() (*pipeline.Worker, error) {
			return pipeline.NewWorker(r.log, nil, r.p2, r.p3, r.p4, r.match, r.draft, r.catalogs, r.decrypter, cfg)
		},
		"no p2": func() (*pipeline.Worker, error) {
			return pipeline.NewWorker(r.log, r.store, nil, r.p3, r.p4, r.match, r.draft, r.catalogs, r.decrypter, cfg)
		},
		"no p3": func() (*pipeline.Worker, error) {
			return pipeline.NewWorker(r.log, r.store, r.p2, nil, r.p4, r.match, r.draft, r.catalogs, r.decrypter, cfg)
		},
		"no p4": func() (*pipeline.Worker, error) {
			return pipeline.NewWorker(r.log, r.store, r.p2, r.p3, nil, r.match, r.draft, r.catalogs, r.decrypter, cfg)
		},
		"no match": func() (*pipeline.Worker, error) {
			return pipeline.NewWorker(r.log, r.store, r.p2, r.p3, r.p4, nil, r.draft, r.catalogs, r.decrypter, cfg)
		},
		"no draft": func() (*pipeline.Worker, error) {
			return pipeline.NewWorker(r.log, r.store, r.p2, r.p3, r.p4, r.match, nil, r.catalogs, r.decrypter, cfg)
		},
		"no catalogs": func() (*pipeline.Worker, error) {
			return pipeline.NewWorker(r.log, r.store, r.p2, r.p3, r.p4, r.match, r.draft, nil, r.decrypter, cfg)
		},
		"no decrypter": func() (*pipeline.Worker, error) {
			return pipeline.NewWorker(r.log, r.store, r.p2, r.p3, r.p4, r.match, r.draft, r.catalogs, nil, cfg)
		},
	}
	for name, construct := range cases {
		t.Run(name, func(t *testing.T) {
			w, err := construct()
			if !errors.Is(err, pipeline.ErrNotWired) || w != nil {
				t.Fatalf("NewWorker = (%v, %v), se esperaba (nil, ErrNotWired)", w, err)
			}
		})
	}
}

// TestNewWorker_AllPieces_BuildsWithoutOptions: con las nueve piezas y sin opciones el
// worker nace; las opciones cablean lo que sirve peor sin ellas, no lo que hace falta.
func TestNewWorker_AllPieces_BuildsWithoutOptions(t *testing.T) {
	r := newParts(t)
	w, err := pipeline.NewWorker(r.log, r.store, r.p2, r.p3, r.p4, r.match, r.draft,
		r.catalogs, r.decrypter, pipeline.Config{})
	if err != nil || w == nil {
		t.Fatalf("NewWorker = (%v, %v), se esperaba un worker y nil", w, err)
	}
	// Y es un worker que trabaja: sin opciones, un job sano sale adelante.
	id := r.seed("")
	if found, err := w.RunOnce(context.Background()); !found || err != nil {
		t.Fatalf("RunOnce = (%v, %v), se esperaba (true, nil)", found, err)
	}
	if row := r.row(t, id); row.Status != intake.StatusDone {
		t.Fatalf("el job quedó %q (%s), se esperaba done", row.Status, r.log.dump())
	}
}

// TestErrNotWired_Text: el texto es observable (sale en el log del arranque si falta una
// pieza) y se conserva literal.
func TestErrNotWired_Text(t *testing.T) {
	const want = "pipeline: el worker necesita log, store, las CINCO etapas, el catálogo y el descifrador"
	if got := pipeline.ErrNotWired.Error(); got != want {
		t.Fatalf("ErrNotWired = %q, se esperaba %q", got, want)
	}
}

// TestCallTimeoutFloor_IsFortyEightSecondsAndMeetsBothConditions: el valor es 48 s (T-13:
// de él se deriva el plazo de G7) y cumple las dos desigualdades con los números medidos.
// Los valores van LITERALES: comparar contra la propia constante pasaría con cualquiera.
func TestCallTimeoutFloor_IsFortyEightSecondsAndMeetsBothConditions(t *testing.T) {
	if pipeline.CallTimeoutFloor != 48*time.Second {
		t.Fatalf("CallTimeoutFloor = %s, se esperaban 48s", pipeline.CallTimeoutFloor)
	}
	const (
		verdictMargin = 7 * time.Second  // el margen de veredicto del adaptador local
		maxObservedP3 = 32 * time.Second // la mayor de las DOS observaciones
		slowThreshold = 0.8              // el breaker llama lento a plazo × 0,8
	)
	useful := pipeline.CallTimeoutFloor - verdictMargin
	if useful <= maxObservedP3 {
		t.Fatalf("una P3 del máximo observado (%s) moriría por timeout: al Edge le llegan %s", maxObservedP3, useful)
	}
	if float64(useful)*slowThreshold <= float64(maxObservedP3) {
		t.Fatalf("una P3 sana del máximo observado (%s) contaría como LENTA: el umbral queda en %s",
			maxObservedP3, time.Duration(float64(useful)*slowThreshold))
	}
}

// TestConfig_Zero_IsTheProductionPolicy (R-02): Config{} es la configuración de producción.
// Los valores van literales, y se leen de lo que el worker HACE con ellos: el log del
// arranque y la cadencia con la que pide su ticker.
func TestConfig_Zero_IsTheProductionPolicy(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	line := startupLine(t, r)
	want := map[string]any{
		"cadencia":             "5s",
		"max_intentos_calidad": 3,
		"max_intentos_infra":   10,
		"backoff_base":         "30s",
		"backoff_tope":         "5m0s",
	}
	for key, value := range want {
		if got := line.fields[key]; got != value {
			t.Errorf("%s = %v, se esperaba %v", key, got, value)
		}
	}
	if cadences, _ := r.ticker.asked(); len(cadences) != 1 || cadences[0] != 5*time.Second {
		t.Errorf("el ticker se pidió con %v, se esperaba una vez con 5s", cadences)
	}
}

// TestConfig_NonPositiveValues_FallToDefaults: un valor `<= 0` cae al valor por defecto,
// nunca a cero. Una cadencia a cero sería un bucle de CPU; un techo a cero mataría el
// primer job que tropezara.
func TestConfig_NonPositiveValues_FallToDefaults(t *testing.T) {
	r := newRig(t, pipeline.Config{
		Cadence: -time.Second, MaxQualityAttempts: -1, MaxInfraAttempts: -1,
		BackoffBase: -time.Second, BackoffCap: -time.Second,
	})
	line := startupLine(t, r)
	want := map[string]any{
		"cadencia": "5s", "max_intentos_calidad": 3, "max_intentos_infra": 10,
		"backoff_base": "30s", "backoff_tope": "5m0s",
	}
	for key, value := range want {
		if got := line.fields[key]; got != value {
			t.Errorf("%s = %v, se esperaba el valor por defecto %v", key, got, value)
		}
	}
}

// TestConfig_ExplicitValues_AreRespected: lo que el llamante fija no se pisa.
func TestConfig_ExplicitValues_AreRespected(t *testing.T) {
	r := newRig(t, pipeline.Config{
		Cadence: 7 * time.Second, MaxQualityAttempts: 2, MaxInfraAttempts: 4,
		BackoffBase: time.Minute, BackoffCap: 2 * time.Minute,
	})
	line := startupLine(t, r)
	want := map[string]any{
		"cadencia": "7s", "max_intentos_calidad": 2, "max_intentos_infra": 4,
		"backoff_base": "1m0s", "backoff_tope": "2m0s",
	}
	for key, value := range want {
		if got := line.fields[key]; got != value {
			t.Errorf("%s = %v, se esperaba %v", key, got, value)
		}
	}
	if cadences, _ := r.ticker.asked(); len(cadences) != 1 || cadences[0] != 7*time.Second {
		t.Errorf("el ticker se pidió con %v, se esperaba una vez con 7s", cadences)
	}
}

// TestWithCapacity_BothPiecesOrNothing: con el aforo Y quien resuelve la plaza, el worker
// arranca con aforo; con uno solo a nil la opción no hace nada y el arranque lo grita.
func TestWithCapacity_BothPiecesOrNothing(t *testing.T) {
	routes := &fakeSlots{edges: map[string]string{sessionID: "edge-1"}}
	cases := []struct {
		name    string
		option  pipeline.Option
		enabled bool
	}{
		{"both pieces", pipeline.WithCapacity(pipeline.NewCapacity(pipeline.KPerSlot), routes), true},
		{"nil capacity", pipeline.WithCapacity(nil, routes), false},
		{"nil slots", pipeline.WithCapacity(pipeline.NewCapacity(pipeline.KPerSlot), nil), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, pipeline.Config{}, c.option)
			line := startupLine(t, r)
			if got := line.fields["aforo_por_edge"]; got != c.enabled {
				t.Errorf("aforo_por_edge = %v, se esperaba %v", got, c.enabled)
			}
			warned := len(r.log.find("worker SIN aforo por Edge")) == 1
			if warned == c.enabled {
				t.Errorf("aviso de «SIN aforo» = %v con aforo = %v:\n%s", warned, c.enabled, r.log.dump())
			}
		})
	}
}

// TestWithShippingZones_Nil_DoesNothing: con nil el worker sigue sin lector de zonas (y el
// arranque lo grita); con un lector, no hay aviso.
func TestWithShippingZones_Nil_DoesNothing(t *testing.T) {
	t.Run("nil reader", func(t *testing.T) {
		r := newParts(t)
		r.build(t, pipeline.Config{}, pipeline.WithShippingZones(nil), pipeline.WithTicker(r.ticker.start))
		startupLine(t, r)
		r.log.one(t, "WARN", "worker SIN lector de zonas de envío").requireKeys(t, "consecuencia")
	})
	t.Run("with a reader", func(t *testing.T) {
		r := newRig(t, pipeline.Config{})
		startupLine(t, r)
		if got := r.log.find("worker SIN lector de zonas de envío"); len(got) != 0 {
			t.Fatalf("con lector de zonas no debe haber aviso:\n%s", r.log.dump())
		}
	})
}

// TestWithClock_DrivesTheRetryMark: la marca del reintento sale del reloj inyectado, no
// del reloj del proceso.
func TestWithClock_DrivesTheRetryMark(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	r.p2.script = []step{{err: errors.New("el Edge no contesta")}}
	id := r.seed("")
	t0 := r.clock.Now() // 2026-08-24: lejos de hoy, así que no puede confundirse con time.Now

	r.drain(context.Background())

	mark := r.row(t, id).NextAttemptAt
	if mark.Before(t0.Add(24*time.Second)) || !mark.Before(t0.Add(36*time.Second)) {
		t.Fatalf("la marca es %s; con el reloj inyectado en %s se esperaba dentro de [+24s, +36s)", mark, t0)
	}
}

// TestWithClock_Nil_KeepsTheProcessClock: con nil la opción no hace nada y el worker usa
// `time.Now`.
func TestWithClock_Nil_KeepsTheProcessClock(t *testing.T) {
	r := newParts(t)
	r.build(t, pipeline.Config{}, pipeline.WithClock(nil))
	r.p2.script = []step{{err: errors.New("el Edge no contesta")}}
	// El reclamo en memoria mira el reloj falso; el worker, el del proceso.
	id := r.seed("")

	before := time.Now()
	if found, err := r.w.RunOnce(context.Background()); !found || err != nil {
		t.Fatalf("RunOnce = (%v, %v), se esperaba (true, nil)", found, err)
	}
	after := time.Now()

	mark := r.row(t, id).NextAttemptAt
	if mark.Before(before.Add(24*time.Second)) || !mark.Before(after.Add(36*time.Second)) {
		t.Fatalf("la marca es %s; con el reloj del proceso se esperaba dentro de [%s+24s, %s+36s)", mark, before, after)
	}
}

// TestWithTicker_Nil_KeepsTheRealTicker: con nil la opción no hace nada y Run usa un
// ticker de verdad con la cadencia de Config: sigue preguntando por trabajo.
func TestWithTicker_Nil_KeepsTheRealTicker(t *testing.T) {
	r := newParts(t)
	r.build(t, pipeline.Config{Cadence: time.Millisecond}, pipeline.WithTicker(nil))
	r.start(t)
	// Un reclamo es el drenaje del arranque; los demás solo pueden venir de tics reales.
	eventually(t, "el worker preguntó por trabajo en varios tics", func() bool { return r.mem.Claims() >= 3 })
}

// TestOptions_AppliedInOrder_TheLastOneWins: dos opciones del mismo tipo, manda la última.
func TestOptions_AppliedInOrder_TheLastOneWins(t *testing.T) {
	r := newParts(t)
	discarded := newFakeTicker()
	r.build(t, pipeline.Config{}, pipeline.WithTicker(discarded.start), pipeline.WithTicker(r.ticker.start))
	startupLine(t, r)
	if cadences, _ := discarded.asked(); len(cadences) != 0 {
		t.Errorf("la primera opción se usó (%v); debía mandar la última", cadences)
	}
	if cadences, _ := r.ticker.asked(); len(cadences) != 1 {
		t.Errorf("la última opción se usó %d veces, se esperaba 1", len(cadences))
	}
}

// TestOptions_ANilOptionDoesNotUndoAnEarlierOne: «con nil la opción no hace nada» también
// cuando delante hay una que sí cableó la pieza: no la descablea.
func TestOptions_ANilOptionDoesNotUndoAnEarlierOne(t *testing.T) {
	routes := &fakeSlots{edges: map[string]string{sessionID: "edge-1"}}
	capacity := pipeline.NewCapacity(pipeline.KPerSlot)
	cases := map[string]pipeline.Option{
		"nil capacity": pipeline.WithCapacity(nil, routes),
		"nil slots":    pipeline.WithCapacity(capacity, nil),
		"nil zones":    pipeline.WithShippingZones(nil),
		"nil clock":    pipeline.WithClock(nil),
		"nil ticker":   pipeline.WithTicker(nil),
	}
	for name, undo := range cases {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, pipeline.Config{}, pipeline.WithCapacity(capacity, routes), undo)
			line := startupLine(t, r)
			if got := line.fields["aforo_por_edge"]; got != true {
				t.Errorf("aforo_por_edge = %v; la opción a nil descableó el aforo", got)
			}
			if got := r.log.at("WARN"); len(got) != 0 {
				t.Errorf("el arranque avisa de una pieza que SÍ estaba cableada:\n%s", r.log.dump())
			}
		})
	}
}
