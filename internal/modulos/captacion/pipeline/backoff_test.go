//go:build pendiente

package pipeline_test

// backoff_test.go — la POLÍTICA DE REINTENTOS DEL JOB que backoff.go promete, vista desde
// donde se cumple: el worker. Qué causa le toca a cada error, cuántos intentos tiene cada
// causa y cuánto se empuja la marca.
//
// 🔴 Los valores se escriben LITERALES y no contra las constantes que protegen: un test que
// comparase el techo con DefaultMaxInfraAttempts pasaría con cualquier valor.

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/pipeline"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/stages"
)

// integrityViolation reproduce el fallo tal como sale de una etapa que escribe: el código
// SQLSTATE dentro del error del driver, envuelto por el store y otra vez por la etapa.
func integrityViolation(code, constraint string) error {
	fromDriver := &pgconn.PgError{Code: code, ConstraintName: constraint}
	fromStore := fmt.Errorf("store: upsert solicitud: %w", fromDriver)
	return fmt.Errorf("draft: crear la solicitud del job %s: %w", "6c5aac22", fromStore)
}

// TestCauses_AreAClosedVocabulary: las causas viajan como CAMPO del log y del motivo de
// muerte; sus valores son observables y no cambian.
func TestCauses_AreAClosedVocabulary(t *testing.T) {
	got := [3]string{pipeline.CauseQuality, pipeline.CauseInfra, pipeline.CauseInvalidJob}
	if want := [3]string{"calidad", "infra", "job_invalido"}; got != want {
		t.Fatalf("causas = %v, se esperaba %v", got, want)
	}
}

// TestDefaults_AreTheProductionOnes: la red del «nunca a cero», con los números de campo.
func TestDefaults_AreTheProductionOnes(t *testing.T) {
	if pipeline.DefaultCadence != 5*time.Second {
		t.Errorf("DefaultCadence = %s, se esperaban 5s", pipeline.DefaultCadence)
	}
	if pipeline.DefaultBackoffBase != 30*time.Second {
		t.Errorf("DefaultBackoffBase = %s, se esperaban 30s", pipeline.DefaultBackoffBase)
	}
	if pipeline.DefaultBackoffCap != 5*time.Minute {
		t.Errorf("DefaultBackoffCap = %s, se esperaban 5m", pipeline.DefaultBackoffCap)
	}
	if pipeline.DefaultMaxInfraAttempts != 10 {
		t.Errorf("DefaultMaxInfraAttempts = %d, se esperaban 10", pipeline.DefaultMaxInfraAttempts)
	}
	if pipeline.DefaultMaxQualityAttempts != 3 {
		t.Errorf("DefaultMaxQualityAttempts = %d, se esperaban 3", pipeline.DefaultMaxQualityAttempts)
	}
}

// TestPolicy_Quality_RetriesUpToItsOwnCeiling: el mismo fallo de calidad en cada etapa LLM
// se reintenta, y el techo que se aplica es el de CALIDAD (3) y no el de infra (10): a
// temperatura 0, repetir el prompt repite la respuesta. Es la política del JOB; la del
// ítem es de P3 y no llega hasta aquí.
func TestPolicy_Quality_RetriesUpToItsOwnCeiling(t *testing.T) {
	garbage := fmt.Errorf("la salida del modelo no es un artefacto legible: %w", llm.ErrLLMQuality)
	cases := map[string]func(*rig) *stageBase{
		intake.StageP2: func(r *rig) *stageBase { return &r.p2.stageBase },
		intake.StageP3: func(r *rig) *stageBase { return &r.p3.stageBase },
		intake.StageP4: func(r *rig) *stageBase { return &r.p4.stageBase },
	}
	for stage, pick := range cases {
		t.Run(stage, func(t *testing.T) {
			r := newRig(t, pipeline.Config{})
			failing := pick(r)
			failing.script = []step{{err: garbage}}
			id := r.seed("")

			row := r.drainUntilTerminal(t, id, 6)

			if row.Status != intake.StatusFailed {
				t.Fatalf("tras el techo de calidad el job debe quedar failed, quedó %q", row.Status)
			}
			if got := failing.count(); got != 3 {
				t.Errorf("la etapa se llamó %d veces, se esperaban 3 (el techo de calidad)", got)
			}
			if row.Attempts != 2 {
				t.Errorf("se cobraron %d reintentos antes de morir, se esperaban 2", row.Attempts)
			}
			want := "causa=calidad stage=" + stage + ": agotados los 3 intentos: " + garbage.Error()
			if row.Error != want {
				t.Errorf("motivo de muerte = %q\n               se esperaba %q", row.Error, want)
			}
		})
	}
}

// TestPolicy_QualityGoesFirst: los adaptadores envuelven `llm.ErrLLMQuality` dentro de
// errores más gordos. Envuelto dos veces sigue siendo calidad, y si el mismo error trae
// DENTRO una violación de integridad, manda la calidad: se reintenta, no muere.
func TestPolicy_QualityGoesFirst(t *testing.T) {
	cases := map[string]error{
		"wrapped twice": fmt.Errorf("p3: la salida del modelo no es legible: %w",
			fmt.Errorf("campo `product` vacío: %w", llm.ErrLLMQuality)),
		"together with an integrity violation": fmt.Errorf("p3: %w; y además %w",
			integrityViolation("23505", "intakes_event_id_uidx"), llm.ErrLLMQuality),
		"together with a missing literal": fmt.Errorf("p3: %w; y además %w", stages.ErrNoLiteral, llm.ErrLLMQuality),
	}
	for name, failure := range cases {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, pipeline.Config{})
			r.p3.script = []step{{err: failure}}
			id := r.seed("")
			r.run(t, id, intake.StatusPending)

			line := r.log.one(t, "WARN", "la etapa falló; el job vuelve a la cola con backoff")
			if line.fields["causa"] != "calidad" || line.fields["tope"] != 3 {
				t.Errorf("el tropiezo lleva causa=%v tope=%v, se esperaba calidad y 3", line.fields["causa"], line.fields["tope"])
			}
		})
	}
}

// TestPolicy_QualityThatRecovers_EndsWell: el control del techo. Si el segundo intento sale
// bien, el job llega a `done` y queda constancia del intento fallido.
func TestPolicy_QualityThatRecovers_EndsWell(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	r.p2.script = []step{{err: fmt.Errorf("basura: %w", llm.ErrLLMQuality)}, {}}
	id := r.seed("")

	row := r.drainUntilTerminal(t, id, 4)
	if row.Status != intake.StatusDone || row.Attempts != 1 {
		t.Fatalf("el job quedó (%q, attempts=%d, error=%q), se esperaba done con un intento cobrado", row.Status, row.Attempts, row.Error)
	}
	if row.SourceText.Complete() {
		t.Error("INV-13: un job terminal no puede conservar el sobre del literal")
	}
}

// TestPolicy_Infra_RetriesUpToTheInfraCeiling: con el proveedor caído el job NUNCA llega a
// `done`; recorre la curva entera y muere al décimo intento, diciendo cuál era la causa.
func TestPolicy_Infra_RetriesUpToTheInfraCeiling(t *testing.T) {
	cases := map[string]error{
		"transport":       errors.New("dial tcp 127.0.0.1:1: connect: connection refused"),
		"deadline":        fmt.Errorf("p2: pedir las ideas principales: %w", context.DeadlineExceeded),
		"anything at all": errors.New("algo que nadie clasificó"),
	}
	for name, failure := range cases {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, pipeline.Config{})
			r.p2.script = []step{{err: failure}}
			id := r.seed("")

			for round := 1; round <= 12; round++ {
				r.drain(context.Background())
				if got := r.row(t, id).Status; got == intake.StatusDone {
					t.Fatalf("vuelta %d: el job llegó a done con el proveedor caído", round)
				}
				r.clock.Advance(10 * time.Minute)
			}

			row := r.row(t, id)
			if row.Status != intake.StatusFailed || row.Attempts != 9 {
				t.Fatalf("el job quedó (%q, attempts=%d), se esperaba failed tras 9 reintentos cobrados", row.Status, row.Attempts)
			}
			if got := r.p2.count(); got != 10 {
				t.Errorf("P2 se llamó %d veces, se esperaban 10 (el techo de infra)", got)
			}
			if want := "causa=infra stage=p2: agotados los 10 intentos: " + failure.Error(); row.Error != want {
				t.Errorf("motivo de muerte = %q\n               se esperaba %q", row.Error, want)
			}
		})
	}
}

// TestPolicy_IntegrityViolation_DiesWithoutASingleRetry (D-044.46): la clase 23 ENTERA. El
// dato que la provoca no cambia entre intentos: repetir la escritura solo alarga la espera
// del cliente (el job `6c5aac22` se reintentó 10 veces en 29 minutos para morir igual).
func TestPolicy_IntegrityViolation_DiesWithoutASingleRetry(t *testing.T) {
	cases := []struct{ name, code, constraint string }{
		{"unique_violation 23505", "23505", "intakes_event_id_uidx"},
		{"not_null_violation 23502", "23502", ""},
		{"foreign_key_violation 23503", "23503", "intakes_event_id_fkey"},
		{"check_violation 23514", "23514", "intakes_event_id_required_chk"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, pipeline.Config{})
			r.draft.script = []step{{err: integrityViolation(c.code, c.constraint)}}
			id := r.seed("")
			// Varias vueltas con el reloj avanzando: si se hubiera reencolado, se vería.
			for range 3 {
				r.drain(context.Background())
				r.clock.Advance(10 * time.Minute)
			}

			row := r.row(t, id)
			if row.Status != intake.StatusFailed || row.Attempts != 0 {
				t.Fatalf("el job quedó (%q, attempts=%d), se esperaba failed sin reintentos", row.Status, row.Attempts)
			}
			if got := r.draft.count(); got != 1 {
				t.Errorf("la etapa se llamó %d veces; un %s no se reintenta NI UNA VEZ", got, c.code)
			}
			want := "causa=job_invalido stage=draft: " + integrityViolation(c.code, c.constraint).Error()
			if row.Error != want {
				t.Errorf("motivo de muerte = %q\n               se esperaba %q", row.Error, want)
			}
			if got := r.store.closesOf(opRetry); len(got) != 0 {
				t.Errorf("hubo %d reencolados", len(got))
			}
		})
	}
}

// TestPolicy_PostgresConcurrencyConflict_IsStillInfra: la mitad que impide que el arreglo
// se pase de ancho. La clase 40 es el caso OPUESTO: el conflicto es de concurrencia, no de
// dato, y reejecutar converge.
func TestPolicy_PostgresConcurrencyConflict_IsStillInfra(t *testing.T) {
	for _, code := range []string{"40P01", "40001"} {
		t.Run(code, func(t *testing.T) {
			r := newRig(t, pipeline.Config{})
			r.draft.script = []step{{err: fmt.Errorf("store: cerrar solicitud: %w", &pgconn.PgError{Code: code})}}
			id := r.seed("")
			r.run(t, id, intake.StatusPending)
			if got := r.row(t, id).Attempts; got != 1 {
				t.Errorf("el intento debe quedar cobrado; attempts=%d", got)
			}
		})
	}
}

// TestPolicy_AStageWithoutLiteral_IsAnInvalidJob: `stages.ErrNoLiteral` devuelto por una
// etapa también es un job que no puede salir bien.
func TestPolicy_AStageWithoutLiteral_IsAnInvalidJob(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	r.p2.script = []step{{err: fmt.Errorf("p2: %w", stages.ErrNoLiteral)}}
	id := r.seed("")
	r.run(t, id, intake.StatusFailed)

	row := r.row(t, id)
	if want := "causa=job_invalido stage=p2: p2: stages: el job no trae literal que analizar"; row.Error != want || row.Attempts != 0 {
		t.Errorf("el job quedó (error=%q, attempts=%d)\n       se esperaba error=%q sin reintentos", row.Error, row.Attempts, want)
	}
}

// within afirma que la marca del reintento N-ésimo cae en `[0,8·d; 1,2·d)` desde `from`.
func within(t *testing.T, what string, from, mark time.Time, d time.Duration) {
	t.Helper()
	low, high := time.Duration(float64(d)*0.8), time.Duration(float64(d)*1.2)
	if got := mark.Sub(from); got < low || got >= high {
		t.Errorf("%s: la marca se empujó %s, se esperaba dentro de [%s, %s)", what, got, low, high)
	}
}

// TestBackoff_TheCurveDoublesFromTheBaseAndIsCapped: el castigo del intento N es
// `30 s × 2^(N−1)` topado en 5 min, con un jitter de ±20 %. Se recorre la curva ENTERA de
// infra, y la marca se mide contra el reloj inyectado.
func TestBackoff_TheCurveDoublesFromTheBaseAndIsCapped(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	r.p2.script = []step{{err: errEdgeDown}}
	id := r.seed("")
	want := []time.Duration{
		30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute,
		5 * time.Minute, 5 * time.Minute, 5 * time.Minute, 5 * time.Minute, 5 * time.Minute,
	}
	for i, d := range want {
		from := r.clock.Now()
		r.drain(context.Background())
		row := r.row(t, id)
		if row.Status != intake.StatusPending || row.Attempts != i+1 {
			t.Fatalf("intento %d: el job quedó (%q, attempts=%d)", i+1, row.Status, row.Attempts)
		}
		within(t, fmt.Sprintf("intento %d", i+1), from, row.NextAttemptAt, d)
		r.clock.Advance(10 * time.Minute)
	}
}

// TestBackoff_TheExponentIsClamped: con un techo de intentos alto el desplazamiento no
// desborda: del intento 13 en adelante el castigo es `base × 2^12`.
func TestBackoff_TheExponentIsClamped(t *testing.T) {
	for _, consumed := range []int{11, 12, 13, 40} {
		t.Run(fmt.Sprintf("attempt %d", consumed+1), func(t *testing.T) {
			r := newRig(t, pipeline.Config{MaxInfraAttempts: 100, BackoffBase: time.Second, BackoffCap: 1000 * time.Hour})
			r.p2.script = []step{{err: errEdgeDown}}
			row := r.healthyRow("")
			row.Attempts = consumed
			id := r.mem.Seed(row)
			from := r.clock.Now()
			r.run(t, id, intake.StatusPending)

			shift := min(consumed, 12)
			within(t, "exponente acotado", from, r.row(t, id).NextAttemptAt, time.Second*time.Duration(1<<shift))
		})
	}
}

// TestBackoff_HasJitter: sin jitter, N jobs castigados por la MISMA caída de Edge volverían
// exactamente a la vez contra una plaza que solo atiende a uno.
func TestBackoff_HasJitter(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	r.p2.script = []step{{err: errEdgeDown}}
	const jobs = 40
	for range jobs {
		r.seed("")
	}
	if n := r.drain(context.Background()); n != jobs {
		t.Fatalf("Drain procesó %d jobs, se esperaban %d", n, jobs)
	}
	marks := map[time.Time]bool{}
	for _, retry := range r.store.closesOf(opRetry) {
		marks[retry.mark] = true
	}
	if len(marks) < 5 {
		t.Fatalf("el jitter no dispersa: %d castigos por la misma caída dieron %d marcas distintas", jobs, len(marks))
	}
}

// TestBackoff_ConfigMovesTheCurveAndTheCeilings: las perillas de Config son las que mandan.
func TestBackoff_ConfigMovesTheCurveAndTheCeilings(t *testing.T) {
	cfg := pipeline.Config{MaxQualityAttempts: 1, MaxInfraAttempts: 3, BackoffBase: time.Minute, BackoffCap: 90 * time.Second}

	t.Run("curve", func(t *testing.T) {
		r := newRig(t, cfg)
		r.p2.script = []step{{err: errEdgeDown}}
		id := r.seed("")
		for i, d := range []time.Duration{time.Minute, 90 * time.Second} {
			from := r.clock.Now()
			r.run(t, id, intake.StatusPending)
			within(t, fmt.Sprintf("intento %d", i+1), from, r.row(t, id).NextAttemptAt, d)
			r.clock.Advance(10 * time.Minute)
		}
		r.run(t, id, intake.StatusFailed)
		if want := "causa=infra stage=p2: agotados los 3 intentos: el Edge no contesta"; r.row(t, id).Error != want {
			t.Errorf("motivo de muerte = %q, se esperaba %q", r.row(t, id).Error, want)
		}
	})
	t.Run("quality ceiling of one", func(t *testing.T) {
		r := newRig(t, cfg)
		r.p2.script = []step{{err: fmt.Errorf("basura: %w", llm.ErrLLMQuality)}}
		id := r.seed("")
		r.run(t, id, intake.StatusFailed)
		if row := r.row(t, id); row.Attempts != 0 || r.p2.count() != 1 {
			t.Errorf("con techo 1 el job muere al primer tropiezo; attempts=%d, llamadas=%d", row.Attempts, r.p2.count())
		}
	})
}
