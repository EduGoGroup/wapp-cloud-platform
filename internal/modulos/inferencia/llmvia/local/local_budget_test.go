package local_test

// Los dos presupuestos del adaptador: el de TIEMPO (el plazo se hereda del llamante,
// R4.6.a y R4.6.d) y el de SALIDA (techo de tokens y rótulo por etapa, R4.6.b). Los dobles
// y la tabla de etapas viven en local_test.go.
//
// ⚠️ El plazo se mide con el reloj REAL (time.Until, T-17): por eso estos tests usan
// contextos holgados y comparan POR RANGO, nunca por igualdad.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"

	edgegrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/grpc"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/llmvia/local"
)

// clockSlack es lo que puede pasar, como mucho, entre crear el ctx y que el adaptador lea
// su deadline. Solo afloja la cota de ABAJO: la de arriba es exacta.
const clockSlack = 2 * time.Second

// assertInherited afirma que el plazo del frame es «presupuesto − MargenVeredicto»: nunca
// mayor, y como mucho clockSlack menor.
func assertInherited(t *testing.T, got, budget time.Duration) {
	t.Helper()
	want := budget - local.MargenVeredicto
	if got > want || got < want-clockSlack {
		t.Fatalf("timeout del frame = %v, quiero entre %v y %v (presupuesto %v − margen %v)",
			got, want-clockSlack, want, budget, local.MargenVeredicto)
	}
}

// El mecanismo: con deadline, el `timeout_ms` del frame es LO QUE QUEDA menos el margen,
// no una constante de este paquete. Vale para las cinco etapas.
func TestFrameTimeoutIsInheritedFromTheCallerDeadline(t *testing.T) {
	t.Parallel()
	const budget = 45 * time.Second
	for _, sc := range stageCalls() {
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			f := &fakeFrame{out: validOutput}
			ctx, cancel := context.WithTimeout(context.Background(), budget)
			defer cancel()
			if _, err := sc.call(ctx, newProvider(t, f), llm.Options{}); err != nil {
				t.Fatalf("%s: %v", sc.name, err)
			}
			assertInherited(t, f.last(t, 1).Timeout, budget)
		})
	}
}

// El caso RARO: sin deadline no hay nada que heredar y manda la red de seguridad. Va junto
// al de arriba a propósito: sin este, «heredar» podría implementarse dejando sin techo al
// llamante descuidado.
func TestWithoutDeadlineTheSafetyNetApplies(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		opts []local.Option
		want time.Duration
	}{
		{"default", nil, local.DefaultTimeout},
		{"WithTimeout sets it", []local.Option{local.WithTimeout(90 * time.Second)}, 90 * time.Second},
		{"zero is ignored", []local.Option{local.WithTimeout(0)}, local.DefaultTimeout},
		{"negative is ignored", []local.Option{local.WithTimeout(-time.Second)}, local.DefaultTimeout},
		{"zero does not erase a previous one",
			[]local.Option{local.WithTimeout(90 * time.Second), local.WithTimeout(0)}, 90 * time.Second},
	} {
		for _, sc := range stageCalls() {
			t.Run(tc.name+"/"+sc.name, func(t *testing.T) {
				t.Parallel()
				f := &fakeFrame{out: validOutput}
				if _, err := sc.call(context.Background(), newProvider(t, f, tc.opts...), llm.Options{}); err != nil {
					t.Fatalf("%s: %v", sc.name, err)
				}
				if got := f.last(t, 1).Timeout; got != tc.want {
					t.Fatalf("timeout del frame = %v, quiero %v", got, tc.want)
				}
			})
		}
	}
}

// EL INVARIANTE, y el caso exacto que falló en campo el 2026-08-23: el llamante estaba
// dispuesto a esperar 40 s y el adaptador cortó a los 30 s por su cuenta, por debajo del
// máximo real del fierro (36,5 s). Un llamante con MÁS presupuesto que la red de seguridad
// sale por el cable con MÁS, o el defecto está de vuelta.
func TestAdapterIsNeverStricterThanItsCaller(t *testing.T) {
	t.Parallel()
	budget := local.DefaultTimeout + 3*local.MargenVeredicto

	t.Run("the default safety net does not cap", func(t *testing.T) {
		t.Parallel()
		f := &fakeFrame{out: validOutput}
		ctx, cancel := context.WithTimeout(context.Background(), budget)
		defer cancel()
		if _, err := newProvider(t, f).ClassifyRequest(ctx, catalogInput(), llm.Options{}); err != nil {
			t.Fatalf("ClassifyRequest: %v", err)
		}
		got := f.last(t, 1).Timeout
		if got <= local.DefaultTimeout {
			t.Fatalf("el adaptador recortó a su propio plazo: mandó %v teniendo %v de presupuesto "+
				"(su red de seguridad es %v). Ese es el defecto de campo del 2026-08-23",
				got, budget, local.DefaultTimeout)
		}
		assertInherited(t, got, budget)
	})

	// La tentación evidente: `min(restante, p.timeout)`. WithTimeout NO es un techo.
	t.Run("a short WithTimeout does not cap either", func(t *testing.T) {
		t.Parallel()
		f := &fakeFrame{out: validOutput}
		ctx, cancel := context.WithTimeout(context.Background(), budget)
		defer cancel()
		p := newProvider(t, f, local.WithTimeout(10*time.Second))
		if _, err := p.ClassifyRequest(ctx, catalogInput(), llm.Options{}); err != nil {
			t.Fatalf("ClassifyRequest: %v", err)
		}
		assertInherited(t, f.last(t, 1).Timeout, budget)
	})

	// Y tampoco es un suelo: con deadline, la red de seguridad no se lee.
	t.Run("a long WithTimeout does not stretch", func(t *testing.T) {
		t.Parallel()
		f := &fakeFrame{out: validOutput}
		ctx, cancel := context.WithTimeout(context.Background(), budget)
		defer cancel()
		p := newProvider(t, f, local.WithTimeout(10*time.Minute))
		if _, err := p.ClassifyRequest(ctx, catalogInput(), llm.Options{}); err != nil {
			t.Fatalf("ClassifyRequest: %v", err)
		}
		assertInherited(t, f.last(t, 1).Timeout, budget)
	})
}

// T-9 · R4.6.d: la desigualdad de la que depende que el veredicto sea determinista. Con el
// margen justo, el timer del gateway y el ctx del llamante vencerían a la vez y el `select`
// elegiría al azar entre `timeout` (con motivo, con aviso al dueño) y
// ErrInferenceAbandoned (sin motivo, sin aviso).
func TestVerdictMarginCoversTheGatewayGrace(t *testing.T) {
	t.Parallel()
	if local.MargenVeredicto <= edgegrpc.DefaultInferGrace {
		t.Fatalf("MargenVeredicto (%v) tiene que ser ESTRICTAMENTE mayor que DefaultInferGrace (%v): "+
			"con el margen justo, quién emite el veredicto lo decide una carrera",
			local.MargenVeredicto, edgegrpc.DefaultInferGrace)
	}
}

// Sin presupuesto no se toca el cable: ni command_id, ni viaje por el stream, ni plaza del
// Ollama del cliente. La comprobación que importa es la de CERO llamadas: sin ella pasaría
// una implementación que llama al Edge y luego tira la respuesta.
func TestWithoutBudgetTheFrameIsNeverCalled(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		budget time.Duration
	}{
		{"half the margin left", local.MargenVeredicto / 2},
		{"just under the margin", local.MargenVeredicto - 500*time.Millisecond},
		{"deadline already past", -time.Second},
	} {
		for _, sc := range stageCalls() {
			t.Run(tc.name+"/"+sc.name, func(t *testing.T) {
				t.Parallel()
				f := &fakeFrame{out: validOutput}
				ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(tc.budget))
				defer cancel()

				got, err := sc.call(ctx, newProvider(t, f), llm.Options{})
				if !errors.Is(err, local.ErrSinPresupuesto) {
					t.Fatalf("quiero ErrSinPresupuesto, llegó: %v", err)
				}
				if got != nil {
					t.Errorf("sin presupuesto la salida debe ser nil, llegó %q", got)
				}
				if n := f.calls(); n != 0 {
					t.Fatalf("se mandaron %d inferencia(s) al Edge sin plazo para esperarlas", n)
				}
			})
		}
	}
}

// El error de «sin presupuesto» es PELADO: no trae Motivo(), así que el escritor de avisos
// no le cuenta al dueño una degradación que no existe. Y su texto dice cuánto quedaba.
func TestNoBudgetErrorCarriesNoReason(t *testing.T) {
	t.Parallel()
	f := &fakeFrame{out: validOutput}
	ctx, cancel := context.WithTimeout(context.Background(), local.MargenVeredicto/2)
	defer cancel()

	_, err := newProvider(t, f).ClassifyRequest(ctx, catalogInput(), llm.Options{})
	if !errors.Is(err, local.ErrSinPresupuesto) {
		t.Fatalf("quiero ErrSinPresupuesto, llegó: %v", err)
	}
	var withReason interface{ Motivo() string }
	if errors.As(err, &withReason) {
		t.Fatalf("ErrSinPresupuesto trae motivo %q: se avisaría al dueño de un equipo sano", withReason.Motivo())
	}
	text := err.Error()
	prefix := local.ErrSinPresupuesto.Error() + ": quedan "
	suffix := ", y el margen del veredicto es 7s"
	if !strings.HasPrefix(text, prefix) || !strings.HasSuffix(text, suffix) {
		t.Fatalf("texto = %q, quiero %q…%q", text, prefix, suffix)
	}
	left, perr := time.ParseDuration(strings.TrimSuffix(strings.TrimPrefix(text, prefix), suffix))
	if perr != nil {
		t.Fatalf("lo que queda no es una duración legible en %q: %v", text, perr)
	}
	// Por rango (T-17): quedaba medio margen, menos lo que tardó en leerse el reloj.
	if half := local.MargenVeredicto / 2; left > half || left < half-clockSlack {
		t.Fatalf("el texto dice que quedaban %v, quiero ~%v", left, half)
	}
	if left != left.Round(time.Millisecond) {
		t.Fatalf("lo que queda (%v) no viene redondeado al milisegundo", left)
	}
}

// La tabla de R4.6.b, fila a fila: cada etapa viaja con SU techo y SU rótulo. Se afirma el
// número (es contrato) y la PROPIEDAD que lo justifica: que cubra la salida más grande
// medida de esa etapa. Sin el campo, el Edge aplica 256 y P2/P3 (265–293 tokens) salen
// truncadas, con un reintento que vuelve a truncar en el mismo sitio.
func TestEachStageSetsItsOutputBudgetAndClass(t *testing.T) {
	t.Parallel()
	for _, sc := range stageCalls() {
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			f := &fakeFrame{out: validOutput}
			if _, err := sc.call(context.Background(), newProvider(t, f), llm.Options{}); err != nil {
				t.Fatalf("%s: %v", sc.name, err)
			}
			req := f.last(t, 1)
			if req.MaxOutputTokens != sc.maxTokens {
				t.Errorf("%s: max_output_tokens = %d, quiero %d", sc.name, req.MaxOutputTokens, sc.maxTokens)
			}
			if req.MaxOutputTokens <= sc.measured {
				t.Errorf("%s: max_output_tokens = %d no cubre la salida más grande medida/estimada (%d): "+
					"la respuesta se truncaría y el reintento volvería a truncar", sc.name, req.MaxOutputTokens, sc.measured)
			}
			if req.Class != sc.class {
				t.Errorf("%s: class = %q, quiero %q", sc.name, req.Class, sc.class)
			}
		})
	}
}

// La otra mitad del rótulo: `class` no es un adorno uniforme. P1 es el turno que alguien
// espera en WhatsApp; P2–P5 corren de fondo. Si las cinco salieran igual, el conteo por
// `class` del Edge no distinguiría nada.
func TestOnlyP1IsInteractive(t *testing.T) {
	t.Parallel()
	interactive := 0
	for _, sc := range stageCalls() {
		f := &fakeFrame{out: validOutput}
		if _, err := sc.call(context.Background(), newProvider(t, f), llm.Options{}); err != nil {
			t.Fatalf("%s: %v", sc.name, err)
		}
		switch class := f.last(t, 1).Class; class {
		case edgegrpc.ClassInteractive:
			interactive++
			if sc.stage != "" {
				t.Errorf("%s salió como interactiva y es trabajo de lote", sc.name)
			}
		case edgegrpc.ClassBatch:
		default:
			t.Errorf("%s: class = %q está fuera del vocabulario del cable", sc.name, class)
		}
	}
	if interactive != 1 {
		t.Fatalf("etapas interactivas = %d, quiero exactamente 1 (P1)", interactive)
	}
}

// El interruptor de campo: apagado, el campo 7 viaja AUSENTE (0), no valiendo 256. Lo que
// se reproduce es la conducta anterior —«el Cloud no dice nada y el Edge aplica su
// default»—, no «el Cloud pide 256». El rótulo no depende del interruptor.
func TestSwitchingTheBudgetOffSendsZero(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		opts []local.Option
		on   bool
	}{
		{"on by default", nil, true},
		{"explicitly on", []local.Option{local.WithMaxOutputTokens(true)}, true},
		{"off", []local.Option{local.WithMaxOutputTokens(false)}, false},
		{"on then off", []local.Option{local.WithMaxOutputTokens(true), local.WithMaxOutputTokens(false)}, false},
	} {
		for _, sc := range stageCalls() {
			t.Run(tc.name+"/"+sc.name, func(t *testing.T) {
				t.Parallel()
				f := &fakeFrame{out: validOutput}
				if _, err := sc.call(context.Background(), newProvider(t, f, tc.opts...), llm.Options{}); err != nil {
					t.Fatalf("%s: %v", sc.name, err)
				}
				req := f.last(t, 1)
				want := int32(0)
				if tc.on {
					want = sc.maxTokens
				}
				if req.MaxOutputTokens != want {
					t.Fatalf("max_output_tokens = %d, quiero %d", req.MaxOutputTokens, want)
				}
				if req.Class != sc.class {
					t.Errorf("class = %q, quiero %q: el interruptor es del techo, no del rótulo", req.Class, sc.class)
				}
			})
		}
	}
}
