package llmvia_test

// EL TURNO ACOTADO DEL NIVEL B (llmvia_turno.go · R4.6.c).
//
// Corre SIN RED: el transporte es un doble y no se levanta ningún Ollama. Lo que se ejerce
// es exactamente lo que este método decide —el plazo, los campos del frame y el paso por el
// aviso—; la vía la contesta llmvia.go, y aquí se afirma su consecuencia.

import (
	"context"
	"errors"
	"testing"
	"time"

	edgegrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/grpc"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/degradation"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/llmvia"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/llmvia/local"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/tenantllm"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/tenantllm/tenantllmhelpertest"
)

// El resolutor del carrito recibe el selector POR INTERFAZ, con esta firma.
var _ interface {
	Turno(ctx context.Context, tenantID, originSessionID string, t llmvia.TurnoRequest) (string, error)
} = (*llmvia.Selector)(nil)

func turnRequest() llmvia.TurnoRequest {
	return llmvia.TurnoRequest{Prompt: "instrucciones + ejemplos + caso", Formato: `{"type":"object"}`}
}

// TestTurno_BuildsTheFrameWithTheMeasuredParameters: los campos que este método pone en el
// frame no son gustos, son la medición del 2026-08-26 contra Ollama real, y cada uno
// protege algo distinto:
//
//	Timeout = 12 s          → 0,8 × 12 s = 9,6 s queda POR ENCIMA del peor caso caliente
//	                          medido (7,9 s), así que las respuestas SANAS no envenenan el
//	                          breaker del tenant —COMPARTIDO con el pipeline—; y a la vez
//	                          corta el caso frío (18 s).
//	Temperature = 0         → esto elige entre opciones que ya existen, no redacta.
//	Format = el esquema     → un JSON Schema, no la cadena "json": viaja verbatim.
//	MaxOutputTokens = 128   → la salida real son 18-20 tokens; el techo acota al modelo
//	                          degenerado sin estorbar al legítimo.
//	Class = interactivo     → SOLO rótulo, pero separa en el parte lo que alguien estaba
//	                          esperando de lo que corría de fondo.
func TestTurno_BuildsTheFrameWithTheMeasuredParameters(t *testing.T) {
	t.Parallel()
	// La salida NO es JSON a propósito: el texto del modelo vuelve crudo, sin validar.
	frame := &fakeFrame{out: "dos, creo"}
	store := tenantllmhelpertest.NewMemoria() // sin fila ⇒ vía local
	s := newSelector(t, store, llmvia.WithFrame(frame),
		// Las opciones del adaptador local NO son de este camino: el turno arma su frame.
		llmvia.WithLocalOptions(local.WithFormat("json_schema"), local.WithMaxOutputTokens(false)))

	raw, err := s.Turno(context.Background(), testTenant, "sesion-7", turnRequest())
	if err != nil {
		t.Fatalf("Turno: %v", err)
	}
	if raw != frame.out {
		t.Fatalf("raw = %q: el texto del modelo se devuelve SIN interpretar", raw)
	}
	call := frame.only(t)
	want := edgegrpc.InferRequest{
		Prompt:          turnRequest().Prompt,
		Format:          turnRequest().Formato,
		Temperature:     0,
		Timeout:         12 * time.Second,
		OriginSessionID: "sesion-7",
		MaxOutputTokens: 128,
		Class:           edgegrpc.ClassInteractive,
	}
	if call.req != want {
		t.Errorf("frame = %+v\nquería  %+v", call.req, want)
	}
	if call.tenant != testTenant {
		t.Errorf("tenant del frame = %q, quería %q", call.tenant, testTenant)
	}
	if llmvia.PlazoTurno != 12*time.Second || llmvia.TechoTurno != 128 {
		t.Errorf("PlazoTurno = %v y TechoTurno = %d; los números medidos son 12 s y 128", llmvia.PlazoTurno, llmvia.TechoTurno)
	}
	if n := store.APIKeyCalls(); n != 0 {
		t.Errorf("un turno por la vía local pidió la credencial %d veces", n)
	}
}

// TestTurno_AShortCallerBudgetStillGetsTheWholeTurn es la regresión del motivo por el que
// este método existe en vez de entrar por local.Provider: aquel descuenta MargenVeredicto
// (7 s) del deadline del llamante SIEMPRE —con un ctx de 12 s dejaría 5, y con uno de 5
// devolvería ErrSinPresupuesto sin tocar el cable—. Aquí el margen se SUMA a nuestra
// espera en vez de restarse del plazo del Edge.
//
// Los relojes son reales (time.Until): se compara por rango, no por igualdad.
func TestTurno_AShortCallerBudgetStillGetsTheWholeTurn(t *testing.T) {
	t.Parallel()
	ceiling := llmvia.PlazoTurno + local.MargenVeredicto // 19 s: lo que esperamos nosotros
	for _, tc := range []struct {
		name   string
		budget time.Duration // 0 ⇒ el llamante no trae deadline
		// El ctx que ve el frame vence entre min y max.
		min, max time.Duration
	}{
		{name: "caller without deadline waits the turn plus the verdict margin", min: ceiling - 5*time.Second, max: ceiling},
		{name: "caller with more than we wait", budget: time.Minute, min: ceiling - 5*time.Second, max: ceiling},
		{name: "caller with exactly what we wait", budget: ceiling, min: ceiling - 5*time.Second, max: ceiling},
		{name: "caller with exactly the turn", budget: llmvia.PlazoTurno, min: llmvia.PlazoTurno - 5*time.Second, max: llmvia.PlazoTurno},
		{name: "caller with less than the verdict margin", budget: 5 * time.Second, min: time.Millisecond, max: 5 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			if tc.budget > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.budget)
				defer cancel()
			}
			frame := &fakeFrame{out: "{}"}
			s := newSelector(t, tenantllmhelpertest.NewMemoria(), llmvia.WithFrame(frame))

			if _, err := s.Turno(ctx, testTenant, "s-1", turnRequest()); err != nil {
				t.Fatalf("Turno con un ctx de %v: %v (aquí no existe «sin presupuesto»)", tc.budget, err)
			}
			call := frame.only(t)
			if call.req.Timeout != llmvia.PlazoTurno {
				t.Errorf("timeout_ms = %v, quería el plazo entero (%v): el margen del veredicto no se resta "+
					"del presupuesto del Edge, se suma a lo que esperamos nosotros", call.req.Timeout, llmvia.PlazoTurno)
			}
			if !call.hasBudget || call.budget < tc.min || call.budget > tc.max {
				t.Errorf("plazo del ctx que ve el frame = (%v, %v), quería entre %v y %v",
					call.budget, call.hasBudget, tc.min, tc.max)
			}
		})
	}
	// T-9, visto desde aquí: lo que esperamos nosotros tiene que vencer DESPUÉS del timer
	// del gateway, o el veredicto lo decidiría el azar.
	if gateway := llmvia.PlazoTurno + edgegrpc.DefaultInferGrace; ceiling <= gateway {
		t.Errorf("esperamos %v y el gateway %v: nuestro ctx no puede vencer antes", ceiling, gateway)
	}
}

// TestTurno_TheRouteDecidesWhetherThereIsATurn: las tres respuestas que no llegan al cable.
// El tenant en vía API se queda sin ESTE escalón —su carrito sigue con el reprompt de
// siempre— y sobre todo NO se le manda la pregunta a un Edge. El error es NOMBRADO para que
// quien lo reciba pueda decir «aquí no hay nada que hacer» en vez de «falló», que es lo que
// distingue una degradación de una avería: ni se avisa ni se cuenta.
func TestTurno_TheRouteDecidesWhetherThereIsATurn(t *testing.T) {
	t.Parallel()
	boom := errors.New("la base no está")
	for _, tc := range []struct {
		name  string
		store llmvia.Store
		want  error
		text  string
	}{
		{
			name: "api route", store: rowStore(t, apiRow()), want: llmvia.ErrViaSinTurnoAcotado,
			text: "llmvia: la vía del tenant no sabe servir un turno acotado",
		},
		{
			name: "route outside the vocabulary", store: &stubStore{found: true, row: tenantllm.Config{Via: "vertex"}},
			want: llmvia.ErrViaDesconocida,
			text: `llmvia: vía fuera del vocabulario cerrado (local|api): "vertex" (tenant ` + testTenant + `)`,
		},
		{name: "the row cannot be read", store: &stubStore{getErr: boom}, want: boom, text: storeReadPrefix + boom.Error()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			frame := &fakeFrame{out: "{}"}
			book, falls := newNoticeBook(), &fallCounter{}
			s := newSelector(t, tc.store, llmvia.WithFrame(frame), book.option(), llmvia.WithDegradacionObservada(falls.count))

			raw, err := s.Turno(context.Background(), testTenant, "s-1", turnRequest())
			if !errors.Is(err, tc.want) || raw != "" {
				t.Fatalf("Turno = (%q, %v), quería (\"\", %v)", raw, err, tc.want)
			}
			if err.Error() != tc.text {
				t.Errorf("texto = %q, quería %q", err, tc.text)
			}
			frame.requireUntouched(t)
			requireSilence(t, book, falls)
		})
	}
}

// TestTurno_ARouteFailureNotifiesTheOwnerAndCountsAsTurn es el corazón del encargo: armar el
// frame por nuestra cuenta NO puede saltarse el aviso al dueño (ADR-0044 §5). Un Ollama
// caído tiene que llegarle al cliente igual lo pida el presupuesto que lo pida el carrito.
// Y la MISMA llamada produce el dato de campo de D-044.41, con el origen `turno`.
func TestTurno_ARouteFailureNotifiesTheOwnerAndCountsAsTurn(t *testing.T) {
	t.Parallel()
	failure := &transportError{"timeout"}
	book, falls := newNoticeBook(), &fallCounter{}
	s := newSelector(t, tenantllmhelpertest.NewMemoria(),
		llmvia.WithFrame(&fakeFrame{err: failure}), book.option(), llmvia.WithDegradacionObservada(falls.count))

	raw, err := s.Turno(context.Background(), testTenant, "s-1", turnRequest())
	if err != error(failure) || raw != "" { //nolint:errorlint // se afirma la IDENTIDAD: sin envolver
		t.Fatalf("Turno = (%q, %v), quería (\"\", el error del transporte intacto)", raw, err)
	}
	requireOneNotice(t, book, falls, llmvia.OrigenTurno, tenantllm.ViaLocal, degradation.ReasonTimeout)
}

// TestTurno_WithoutFrameFailsWithTheUsualError: un selector sin frame es un bug de arranque.
// Se dice con el vocabulario que ya existe (local.ErrSinTransporte) en vez de estrenar un
// tercer nombre para el mismo problema, y no es una degradación de la vía.
func TestTurno_WithoutFrameFailsWithTheUsualError(t *testing.T) {
	t.Parallel()
	book, falls := newNoticeBook(), &fallCounter{}
	s := newSelector(t, tenantllmhelpertest.NewMemoria(), book.option(), llmvia.WithDegradacionObservada(falls.count))

	raw, err := s.Turno(context.Background(), testTenant, "s-1", turnRequest())
	if !errors.Is(err, local.ErrSinTransporte) || raw != "" {
		t.Fatalf("Turno = (%q, %v), quería (\"\", local.ErrSinTransporte)", raw, err)
	}
	requireSilence(t, book, falls)
}
