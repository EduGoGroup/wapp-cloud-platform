//go:build pendiente

package llmvia

// Las promesas de notify.go (R4.5.d, R4.5.e), ejercidas por las tres puertas del Selector: la
// selección (For al construir), el pipeline (el provider que For devuelve) y el turno (Turno).
//
// Es paquete INTERNO a propósito (T4.8): el mapeo error → motivo es un auxiliar no exportado
// que nace en el verde, y su test de tabla nace con él en este mismo fichero (T-6). En rojo la
// tabla se afirma por lo que se VE: qué motivo queda escrito en el doble de degradation y qué
// recibe el observador.
//
// Sin red, sin BD y sin reloj real: el transporte es un doble, los dos almacenes son los
// dobles en memoria de sus paquetes y el instante del fallo lo pone WithClock.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/llm/api"
	"github.com/EduGoGroup/wapp-shared/logger"

	edgegrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/grpc"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/degradation"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/degradation/degradationhelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/tenantllm"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/tenantllm/tenantllmhelpertest"
)

const (
	// rigTenant es el tenant de todos los casos. Sin fila en el store ⇒ vía local (REQ-33).
	rigTenant = "tenant-notify"
	// rigOutput es una salida que llm.ExtractJSON acepta tal cual.
	rigOutput = `{"version":1}`
	// Los dos literales del log, byte a byte (diseno §4).
	logNoticeBorn   = "degradación: la vía LLM del tenant falló y se avisó al dueño"
	logNoticeFailed = "degradación: no se pudo escribir el aviso al dueño"
)

// rigClock es el instante en que «ocurre» todo fallo de estos tests: a mitad de una ventana
// de quince minutos, para que tres fallos seguidos caigan sin duda en la misma.
var rigClock = time.Date(2026, 10, 6, 10, 7, 0, 0, time.UTC)

// stubFrame es el transporte de la vía local: contesta lo sembrado y cuenta las llamadas.
type stubFrame struct {
	out   string
	err   error
	calls int
}

func (f *stubFrame) Infer(context.Context, string, edgegrpc.InferRequest) (string, error) {
	f.calls++
	return f.out, f.err
}

// reasonError, el error del transporte con Motivo(), vive en notify_reason_test.go.

// fallCounter recoge lo que sale por el observador: (origen, vía, motivo) por caída.
type fallCounter struct{ falls [][3]string }

func (c *fallCounter) count(origin, route, reason string) {
	c.falls = append(c.falls, [3]string{origin, route, reason})
}

// rig junta los colaboradores de un caso. El store de tenant_llm nace vacío (vía local).
type rig struct {
	store   *tenantllmhelpertest.Memoria
	frame   *stubFrame
	notices *degradationhelpertest.Memoria
	falls   *fallCounter
	logs    *bytes.Buffer
}

func newRig(frameErr error) *rig {
	return &rig{
		store:   tenantllmhelpertest.NewMemoria(),
		frame:   &stubFrame{out: rigOutput, err: frameErr},
		notices: degradationhelpertest.NewMemoria(),
		falls:   &fallCounter{},
		logs:    &bytes.Buffer{},
	}
}

// notifier es la opción que cablea el escritor REAL de avisos sobre el doble del almacén.
func (r *rig) notifier() SelectorOption {
	return WithNotifier(degradation.NewNotifier(r.notices, degradation.VentanaPorDefecto))
}

// selector arma el Selector con el frame, el observador y el reloj del rig, más opts.
func (r *rig) selector(t *testing.T, opts ...SelectorOption) *Selector {
	t.Helper()
	all := []SelectorOption{
		WithFrame(r.frame),
		WithDegradacionObservada(r.falls.count),
		WithClock(func() time.Time { return rigClock }),
	}
	s, err := NewSelector(r.store, logger.New(logger.WithWriter(r.logs), logger.WithJSON(true)), append(all, opts...)...)
	if err != nil {
		t.Fatalf("NewSelector: %v", err)
	}
	return s
}

// provider pide el provider del tenant del rig y exige que For no falle.
func (r *rig) provider(t *testing.T, s *Selector) llm.LLMProvider {
	t.Helper()
	p, err := s.For(context.Background(), rigTenant, "")
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	return p
}

// useAPI deja al tenant del rig en la vía api con una clave FALSA (T-16) y ese proveedor y
// modelo. Con modelo vacío o proveedor desconocido, api.New falla al construir, sin red.
func (r *rig) useAPI(t *testing.T, provider, model string) {
	t.Helper()
	cfg := tenantllm.Config{TenantID: rigTenant, Via: tenantllm.ViaAPI, Provider: provider, Model: model}
	if err := r.store.Upsert(context.Background(), cfg, "clave-falsa-de-test", rigClock); err != nil {
		t.Fatalf("sembrando la fila api: %v", err)
	}
}

// written devuelve lo que quedó escrito en el doble de degradation para el tenant, como pares
// (motivo, vía). El motivo va con string(): Reason.String es contrato de otro fichero.
func (r *rig) written() [][2]string {
	rows := r.notices.Rows(rigTenant)
	out := make([][2]string, 0, len(rows))
	for _, n := range rows {
		out = append(out, [2]string{string(n.Reason), n.Via})
	}
	return out
}

// requireNothing afirma que el fallo ni se contó ni se escribió ni se intentó escribir.
func (r *rig) requireNothing(t *testing.T) {
	t.Helper()
	if len(r.falls.falls) != 0 {
		t.Errorf("caídas contadas = %v, quería ninguna: lo que no mapea no se cuenta", r.falls.falls)
	}
	if n := r.notices.Saves(); n != 0 {
		t.Errorf("el almacén de avisos se tocó %d veces (%v), quería 0: lo que no mapea no avisa", n, r.written())
	}
}

// requireOne afirma UNA caída contada y UN aviso escrito, con ese origen, esa vía y ese motivo.
func (r *rig) requireOne(t *testing.T, origin, route string, reason degradation.Reason) {
	t.Helper()
	wantFall := [3]string{origin, route, string(reason)}
	if len(r.falls.falls) != 1 || r.falls.falls[0] != wantFall {
		t.Errorf("caídas contadas = %v, quería exactamente %v", r.falls.falls, wantFall)
	}
	wantRow := [2]string{string(reason), route}
	if rows := r.written(); len(rows) != 1 || rows[0] != wantRow {
		t.Errorf("avisos escritos = %v, quería exactamente %v", rows, wantRow)
	}
}

// logMessages devuelve los mensajes del log con ese texto, cada uno con sus claves.
func (r *rig) logMessages(t *testing.T, msg string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(r.logs.Bytes()), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		entry := map[string]any{}
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatalf("línea de log ilegible %q: %v", line, err)
		}
		if entry["msg"] == msg {
			out = append(out, entry)
		}
	}
	return out
}

func rigInput() llm.ClassifyRequestInput {
	return llm.ClassifyRequestInput{
		Text:         "quiero tres pizzas",
		Catalog:      []llm.IntentSpec{{Name: "intake_request", Description: "pide productos"}},
		UnknownLabel: "desconocido",
	}
}

func rigTurn() TurnoRequest {
	return TurnoRequest{Prompt: "instrucciones + ejemplos + caso", Formato: `{"type":"object"}`}
}

// classify llama a la P1 del provider y devuelve solo el error.
func classify(ctx context.Context, p llm.LLMProvider) error {
	_, err := p.ClassifyRequest(ctx, rigInput(), llm.Options{})
	return err
}

// ---------------------------------------------------------------- la tabla de motivos

// TestFor_ReasonTableThroughTheDecorator fija el mapeo error → motivo (R4.5.d), incluido —y
// sobre todo— lo que NO notifica. Son los 17 casos de la tabla vieja, expresados como
// conducta: el frame falla con ese error, la P1 lo propaga, y se mira qué quedó escrito.
//
// 🔴 LA MITAD IMPORTANTE SON LOS `false`. Un canal que avisa de más deja de leerse
// (D-044.32), así que «esto no escribe fila» es una afirmación tan fuerte como su contraria.
func TestFor_ReasonTableThroughTheDecorator(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		err      error
		want     degradation.Reason
		notifies bool
	}{
		// --- Vía local: el motivo viaja dentro del error del transporte ---
		{"ollama down", &reasonError{edgegrpc.ReasonOllamaDown}, degradation.ReasonOllamaDown, true},
		{"breaker open", &reasonError{edgegrpc.ReasonBreakerOpen}, degradation.ReasonBreakerOpen, true},
		{"no live session", &reasonError{edgegrpc.ReasonEdgeOffline}, degradation.ReasonEdgeOffline, true},
		{"timeout", &reasonError{edgegrpc.ReasonTimeout}, degradation.ReasonTimeout, true},
		{"no lease", &reasonError{edgegrpc.ReasonLeaseInvalid}, degradation.ReasonLeaseInvalid, true},
		{"edge saturated", &reasonError{edgegrpc.ReasonEdgeSinCapacidad}, degradation.ReasonEdgeSinCapacidad, true},
		{"reason wrapped in another error", fmt.Errorf("contexto: %w", &reasonError{"timeout"}), degradation.ReasonTimeout, true},

		// --- Los centinelas de la vía API ---
		{"tenant has no credential", tenantllm.ErrNotConfigured, degradation.ReasonCredencial, true},
		{"api.New without credential", api.ErrInvalidConfig, degradation.ReasonCredencial, true},
		{"upstream provider failed", api.ErrUpstream, degradation.ReasonAPIError, true},

		// --- Lo que NO avisa, y por qué ---
		// El modelo RESPONDIÓ; su salida no era interpretable. El proveedor funciona, el
		// cable funciona, y el llamante tiene un reintento a 0,3 previsto para esto.
		{name: "output quality", err: llm.ErrLLMQuality},
		// Envuelto también: los providers lo meten dentro de errores más gordos y la rama
		// de calidad va la PRIMERA justo para que no se lo trague otra.
		{name: "wrapped quality", err: fmt.Errorf("clasificando: %w", llm.ErrLLMQuality)},
		// Una fila con `provider` fuera del CHECK. Nada se ha caído: la config está mal
		// escrita. Contarlo como `credencial` mandaría al dueño a rotar una clave buena.
		{name: "unsupported provider", err: api.ErrUnsupportedProvider},
		// El vocabulario es CERRADO: un motivo que el enum no conoce NO se escribe.
		{name: "reason invented by the transport", err: &reasonError{"se_rompio_algo"}},
		// Y un motivo SANO tampoco, aunque venga con la forma correcta.
		{name: "healthy reason shaped like a reason", err: &reasonError{"fastlane"}},
		{name: "any other error", err: errors.New("vete a saber")},
		{name: "no error", err: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(tc.err)
			got := classify(context.Background(), r.provider(t, r.selector(t, r.notifier())))
			if tc.err == nil && got != nil {
				t.Fatalf("P1 = %v, quería éxito", got)
			}
			if tc.err != nil && !errors.Is(got, tc.err) {
				t.Fatalf("P1 = %v, quería que el error del transporte llegara al llamante: %v", got, tc.err)
			}
			if !tc.notifies {
				r.requireNothing(t)
				return
			}
			r.requireOne(t, OrigenPipeline, tenantllm.ViaLocal, tc.want)
		})
	}
}

// TestFor_NeverWritesAReasonOutsideTheVocabulary es la red estructural sobre la tabla de
// arriba: pase lo que pase, lo que se escribe tiene que ser un motivo del vocabulario cerrado.
// Uno de fuera haría que el escritor lo rechazara y el aviso se perdería justo cuando hacía
// falta.
func TestFor_NeverWritesAReasonOutsideTheVocabulary(t *testing.T) {
	t.Parallel()
	inputs := []error{
		&reasonError{"ollama_down"}, &reasonError{"basura"}, &reasonError{""},
		api.ErrUpstream, api.ErrInvalidConfig, api.ErrUnsupportedProvider,
		tenantllm.ErrNotConfigured, llm.ErrLLMQuality, errors.New("x"), nil,
	}
	r := newRig(nil)
	p := r.provider(t, r.selector(t, r.notifier()))
	for _, in := range inputs {
		r.frame.err = in
		if got := classify(context.Background(), p); !errors.Is(got, in) {
			t.Fatalf("P1 = %v, quería %v", got, in)
		}
	}
	for _, n := range r.notices.Rows(rigTenant) {
		if !n.Reason.Valid() {
			t.Errorf("quedó escrito el motivo %q, que NO es del vocabulario cerrado", string(n.Reason))
		}
	}
	// De los diez, avisan cuatro: ollama_down, api_error y credencial (dos veces, que el
	// dedupe colapsa en una fila). El resto no llega ni a intentarse.
	if saves, falls := r.notices.Saves(), len(r.falls.falls); saves != 4 || falls != 4 {
		t.Errorf("escrituras = %d y caídas = %d (%v), quería 4 y 4", saves, falls, r.falls.falls)
	}
}

// ---------------------------------------------------------------- el decorador

// TestDecorator_WrapsTheFiveStagesAndLeavesTheErrorIntact: los cinco métodos del puerto son
// la misma línea —llamar y, si falló, avisar— y NINGUNO altera el error: quien lo reciba
// tiene que poder seguir usando errors.Is y el duck-typing del motivo.
func TestDecorator_WrapsTheFiveStagesAndLeavesTheErrorIntact(t *testing.T) {
	t.Parallel()
	failure := &reasonError{edgegrpc.ReasonBreakerOpen}
	r := newRig(failure)
	p := r.provider(t, r.selector(t, r.notifier()))
	ctx := context.Background()

	stages := []struct {
		name string
		call func() (json.RawMessage, error)
	}{
		{"P1", func() (json.RawMessage, error) { return p.ClassifyRequest(ctx, rigInput(), llm.Options{}) }},
		{"P2", func() (json.RawMessage, error) {
			return p.ExtractMainIdeas(ctx, llm.ExtractMainIdeasInput{SourceText: "hola"}, llm.Options{})
		}},
		{"P3", func() (json.RawMessage, error) {
			return p.ExtractItemSpecs(ctx, llm.ExtractItemSpecsInput{SourceText: "hola", Idea: "pizza"}, llm.Options{})
		}},
		{"P4", func() (json.RawMessage, error) {
			return p.NormalizeQuantities(ctx, llm.NormalizeQuantitiesInput{SourceText: "hola", MessageTS: rigClock}, llm.Options{})
		}},
		{"P5", func() (json.RawMessage, error) {
			return p.GenerateQuoteText(ctx, llm.GenerateQuoteTextInput{Quote: json.RawMessage(`{"lines":[]}`)}, llm.Options{})
		}},
	}
	for i, st := range stages {
		_, err := st.call()
		// Identidad, no errors.Is: el decorador no envuelve.
		if err != error(failure) { //nolint:errorlint // se afirma la IDENTIDAD: sin envolver
			t.Fatalf("%s = %v, quería EL MISMO error del transporte, sin envolver", st.name, err)
		}
		var withReason interface{ Motivo() string }
		if !errors.As(err, &withReason) || withReason.Motivo() != edgegrpc.ReasonBreakerOpen {
			t.Fatalf("%s: el decorador alteró el error: %v", st.name, err)
		}
		want := [3]string{OrigenPipeline, tenantllm.ViaLocal, edgegrpc.ReasonBreakerOpen}
		if len(r.falls.falls) != i+1 || r.falls.falls[i] != want {
			t.Fatalf("tras %s, caídas = %v; quería %d, la última %v", st.name, r.falls.falls, i+1, want)
		}
	}
	if saves := r.notices.Saves(); saves != len(stages) {
		t.Errorf("escrituras de aviso = %d, quería una por etapa fallida (%d)", saves, len(stages))
	}

	// Y en el éxito no toca la salida ni avisa.
	r.frame.err = nil
	out, err := p.ClassifyRequest(ctx, rigInput(), llm.Options{})
	if err != nil || string(out) != rigOutput {
		t.Errorf("P1 = (%s, %v), quería la salida intacta %s", out, err, rigOutput)
	}
	if saves := r.notices.Saves(); saves != len(stages) {
		t.Errorf("una llamada que va bien escribió aviso: escrituras = %d", saves)
	}
}

// ---------------------------------------------------------------- las tres puertas

// TestSelection_BuildFailureNotifiesWithItsOrigin: el fallo al CONSTRUIR el adaptador es un
// fallo de la vía tanto como el fallo al consumirlo, y entra por la puerta `seleccion` con la
// vía de la fila. La vía api se prueba solo con api.New fallando: sin red y sin clave real.
func TestSelection_BuildFailureNotifiesWithItsOrigin(t *testing.T) {
	t.Parallel()

	t.Run("config the constructor rejects counts as credential", func(t *testing.T) {
		t.Parallel()
		r := newRig(nil)
		r.useAPI(t, tenantllm.ProviderAnthropic, "") // sin modelo ⇒ api.ErrInvalidConfig
		p, err := r.selector(t, r.notifier()).For(context.Background(), rigTenant, "")
		if !errors.Is(err, api.ErrInvalidConfig) || p != nil {
			t.Fatalf("For = (%v, %v), quería (nil, api.ErrInvalidConfig)", p, err)
		}
		r.requireOne(t, OrigenSeleccion, tenantllm.ViaAPI, degradation.ReasonCredencial)
		if r.frame.calls != 0 {
			t.Errorf("se tocó el cable %d veces para un tenant en vía api", r.frame.calls)
		}
	})

	// 🔴 La trampa 7: ErrUnsupportedProvider ENVUELVE ErrInvalidConfig. Si las ramas
	// fueran en el otro orden, esto se contaría como credencial.
	t.Run("unsupported provider is not a degradation", func(t *testing.T) {
		t.Parallel()
		r := newRig(nil)
		r.useAPI(t, "vertex", "un-modelo")
		_, err := r.selector(t, r.notifier()).For(context.Background(), rigTenant, "")
		if !errors.Is(err, api.ErrUnsupportedProvider) {
			t.Fatalf("For = %v, quería api.ErrUnsupportedProvider", err)
		}
		r.requireNothing(t)
	})
}

// TestTurno_FailureNotifiesWithItsOrigin: armar el frame por nuestra cuenta NO se salta el
// aviso al dueño (ADR-0044 §5), y la misma llamada produce el dato de campo de D-044.41 con
// el origen que lo hace legible: `turno` es alguien esperando delante del teléfono.
func TestTurno_FailureNotifiesWithItsOrigin(t *testing.T) {
	t.Parallel()
	failure := &reasonError{edgegrpc.ReasonTimeout}
	r := newRig(failure)
	if _, err := r.selector(t, r.notifier()).Turno(context.Background(), rigTenant, "s-1", rigTurn()); err != error(failure) { //nolint:errorlint // se afirma la IDENTIDAD: sin envolver
		t.Fatalf("Turno = %v, quería el error del transporte intacto", err)
	}
	r.requireOne(t, OrigenTurno, tenantllm.ViaLocal, degradation.ReasonTimeout)
}

// ---------------------------------------------------------------- el observador

// TestObserver_CountsEvenWithoutNotifier custodia una colocación, y la colocación es la
// decisión (T-8): contar va ANTES de mirar si hay notificador. Colgarlo de él ataría el dato
// que desbloquea D-044.41 a que haya base de datos cableada.
func TestObserver_CountsEvenWithoutNotifier(t *testing.T) {
	t.Parallel()

	t.Run("turn door", func(t *testing.T) {
		t.Parallel()
		r := newRig(&reasonError{edgegrpc.ReasonOllamaDown})
		if _, err := r.selector(t).Turno(context.Background(), rigTenant, "s-1", rigTurn()); err == nil {
			t.Fatal("quería el error del transporte")
		}
		want := [3]string{OrigenTurno, tenantllm.ViaLocal, edgegrpc.ReasonOllamaDown}
		if len(r.falls.falls) != 1 || r.falls.falls[0] != want {
			t.Fatalf("caídas = %v, quería %v aunque no haya tabla de avisos", r.falls.falls, want)
		}
	})

	t.Run("selection door", func(t *testing.T) {
		t.Parallel()
		r := newRig(nil)
		r.useAPI(t, tenantllm.ProviderAnthropic, "")
		if _, err := r.selector(t).For(context.Background(), rigTenant, ""); err == nil {
			t.Fatal("quería el error del constructor")
		}
		want := [3]string{OrigenSeleccion, tenantllm.ViaAPI, string(degradation.ReasonCredencial)}
		if len(r.falls.falls) != 1 || r.falls.falls[0] != want {
			t.Fatalf("caídas = %v, quería %v aunque no haya tabla de avisos", r.falls.falls, want)
		}
	})

	// ⚠️ La consecuencia que el paquete viejo ya tenía: sin notificador, el provider de For
	// va SIN envoltura, así que un fallo del pipeline no pasa por el aviso y no se cuenta.
	t.Run("pipeline door is not wrapped without a notifier", func(t *testing.T) {
		t.Parallel()
		r := newRig(&reasonError{edgegrpc.ReasonOllamaDown})
		if err := classify(context.Background(), r.provider(t, r.selector(t))); err == nil {
			t.Fatal("quería el error del transporte")
		}
		if len(r.falls.falls) != 0 {
			t.Fatalf("caídas = %v: sin notificador el provider no va envuelto", r.falls.falls)
		}
	})
}

// TestObserver_IsNotUndercountedByTheDedupe: diez timeouts de la misma ventana escriben UN
// aviso y son DIEZ caídas a Nivel A. Son dos preguntas distintas —«qué le cuento al dueño» y
// «cuánto pasa»— y se responden por separado.
func TestObserver_IsNotUndercountedByTheDedupe(t *testing.T) {
	t.Parallel()
	r := newRig(&reasonError{edgegrpc.ReasonTimeout})
	s := r.selector(t, r.notifier())
	const failures = 3
	for range failures {
		if _, err := s.Turno(context.Background(), rigTenant, "s-1", rigTurn()); err == nil {
			t.Fatal("quería el error del transporte")
		}
	}
	if got := len(r.falls.falls); got != failures {
		t.Errorf("caídas contadas = %d, quería %d: el contador no pasa por el dedupe", got, failures)
	}
	rows := r.notices.Rows(rigTenant)
	if len(rows) != 1 || rows[0].Occurrences != failures {
		t.Errorf("avisos = %v, quería UNO con %d ocurrencias", r.written(), failures)
	}
}

// TestObserver_WhatHasNoReasonIsNotCounted: la regla dura del mapeo («lo que no mapea, no
// avisa») gobierna también el contador. Un fallo sin motivo, o uno de CALIDAD, no es una
// degradación de la vía y no puede ensuciar la serie.
func TestObserver_WhatHasNoReasonIsNotCounted(t *testing.T) {
	t.Parallel()
	for name, failure := range map[string]error{
		"plain error": errors.New("json roto"),
		"quality":     fmt.Errorf("turno: %w", llm.ErrLLMQuality),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newRig(failure)
			if _, err := r.selector(t, r.notifier()).Turno(context.Background(), rigTenant, "s-1", rigTurn()); !errors.Is(err, failure) {
				t.Fatalf("Turno = %v, quería %v", err, failure)
			}
			r.requireNothing(t)
		})
	}
}

// TestWithDegradacionObservada_NilIsIgnored: nil no apaga el observador que ya estaba.
func TestWithDegradacionObservada_NilIsIgnored(t *testing.T) {
	t.Parallel()
	r := newRig(&reasonError{edgegrpc.ReasonTimeout})
	var none ObservadorDegradacion
	s := r.selector(t, WithDegradacionObservada(none)) // después del observador del rig
	if _, err := s.Turno(context.Background(), rigTenant, "s-1", rigTurn()); err == nil {
		t.Fatal("quería el error del transporte")
	}
	if len(r.falls.falls) != 1 {
		t.Fatalf("caídas = %v, quería 1: un observador nil se ignora, no sustituye al anterior", r.falls.falls)
	}
}
