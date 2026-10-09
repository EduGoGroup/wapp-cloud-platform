//go:build pendiente

package stages_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/stages"
)

// ---------------------------------------------------------------------------
// Banco de pruebas de P2
// ---------------------------------------------------------------------------

// p2Call es lo que el provider anotó de UNA llamada de P2.
type p2Call struct {
	in          llm.ExtractMainIdeasInput
	temperature float64
	bounded     bool
	remaining   time.Duration
}

// p2Bench son la etapa y sus dobles.
type p2Bench struct {
	stage    *stages.P2
	calls    []p2Call
	selector *fakeSelector
	store    *fakeStore
	log      bytes.Buffer
}

// newP2Bench arma P2 con un provider que contesta `response` (o `providerErr`).
func newP2Bench(t *testing.T, response json.RawMessage, providerErr error, opts ...stages.Option) *p2Bench {
	t.Helper()
	b := &p2Bench{store: &fakeStore{}}
	b.selector = &fakeSelector{provider: &fakeProvider{
		onMainIdeas: func(ctx context.Context, in llm.ExtractMainIdeasInput, o llm.Options) (json.RawMessage, error) {
			call := p2Call{in: in, temperature: o.Temperature}
			if deadline, ok := ctx.Deadline(); ok {
				call.bounded, call.remaining = true, time.Until(deadline)
			}
			b.calls = append(b.calls, call)
			return response, providerErr
		},
	}}
	stage, err := stages.NewP2(captureLog(&b.log), b.selector, b.store, opts...)
	if err != nil {
		t.Fatalf("NewP2: %v", err)
	}
	b.stage = stage
	return b
}

// p2Output arma la salida del modelo. `wants` son pares idea/evidencia; `hint` es el
// par texto/evidencia de la pista, o vacío para omitirla.
func p2Output(version int, wants [][2]string, hint [2]string) json.RawMessage {
	var b strings.Builder
	fmt.Fprintf(&b, `{"version":%d,"wants":[`, version)
	for i, w := range wants {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"idea":%q,"evidence":%q}`, w[0], w[1])
	}
	b.WriteString("]")
	if hint[0] != "" {
		fmt.Fprintf(&b, `,"delivery_hint":{"text":%q,"evidence":%q}`, hint[0], hint[1])
	}
	b.WriteString("}")
	return json.RawMessage(b.String())
}

// ambarP2Output es la salida de un modelo bien portado para el caso.
func ambarP2Output() json.RawMessage {
	return p2Output(llm.ArtifactVersion, ambarWants(), [2]string{ambarHintText, deliveryEvidence})
}

// readP2Artifact decodifica lo que se persistió.
func readP2Artifact(t *testing.T, a intake.Artifact) llm.MainIdeas {
	t.Helper()
	var out llm.MainIdeas
	if err := json.Unmarshal(a.Payload, &out); err != nil {
		t.Fatalf("el artefacto persistido no decodifica: %v", err)
	}
	return out
}

// ---------------------------------------------------------------------------
// El camino feliz
// ---------------------------------------------------------------------------

// TestP2Run_AmbarText_ThreeIdeasAndTheDeliveryHint: las tres ideas y la pista se
// sostienen sobre el literal, y el artefacto queda persistido bajo la etapa `p2` con lo
// mismo que se devolvió.
func TestP2Run_AmbarText_ThreeIdeasAndTheDeliveryHint(t *testing.T) {
	b := newP2Bench(t, ambarP2Output(), nil)

	ideas, err := b.stage.Run(context.Background(), ambarJob(), ambarText)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(ideas.Wants) != 3 {
		t.Fatalf("ideas devueltas = %d, se esperaban 3", len(ideas.Wants))
	}
	for i, pair := range ambarWants() {
		if ideas.Wants[i].Idea != pair[0] {
			t.Fatalf("idea %d = %q; el orden de las ideas vivas es el del modelo", i, ideas.Wants[i].Idea)
		}
	}
	if ideas.DeliveryHint == nil || ideas.DeliveryHint.Text != ambarHintText {
		t.Fatalf("pista de entrega = %+v; su evidencia SÍ está en el literal", ideas.DeliveryHint)
	}

	// Persistido una vez, bajo el job y la etapa p2, con lo mismo que se devolvió.
	if len(b.store.saved) != 1 || b.store.jobs[0] != jobID {
		t.Fatalf("artefactos persistidos = %d bajo %v", len(b.store.saved), b.store.jobs)
	}
	if b.store.saved[0].Stage != intake.StageP2 {
		t.Fatalf("etapa persistida = %q, se esperaba %q", b.store.saved[0].Stage, intake.StageP2)
	}
	saved := readP2Artifact(t, b.store.saved[0])
	if saved.Version != llm.ArtifactVersion || len(saved.Wants) != 3 || saved.DeliveryHint == nil {
		t.Fatalf("lo persistido no es lo devuelto: version=%d wants=%d pista=%v",
			saved.Version, len(saved.Wants), saved.DeliveryHint != nil)
	}
}

// TestP2Run_MakesOneGreedyCallWithTheWholeLiteral: el provider se pide UNA vez, con la
// sesión que enruta la inferencia al Edge de origen; la llamada es UNA, greedy y con el
// literal ENTERO; y el cierre deja su línea con las cuentas.
func TestP2Run_MakesOneGreedyCallWithTheWholeLiteral(t *testing.T) {
	b := newP2Bench(t, ambarP2Output(), nil)
	if _, err := b.stage.Run(context.Background(), ambarJob(), ambarText); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !reflect.DeepEqual(b.selector.asked, []string{originRoute}) {
		t.Fatalf("el provider se pidió como %v; se esperaba [%q]", b.selector.asked, originRoute)
	}
	// UNA llamada, greedy, con el literal ENTERO.
	if len(b.calls) != 1 {
		t.Fatalf("llamadas al modelo = %d, se esperaba 1 (el reintento es del worker)", len(b.calls))
	}
	if b.calls[0].in.SourceText != ambarText {
		t.Fatalf("el prompt no recibió el literal del job: %q", b.calls[0].in.SourceText)
	}
	if b.calls[0].temperature != llm.TemperatureGreedy {
		t.Fatalf("temperatura = %v; P2 llama a %v", b.calls[0].temperature, llm.TemperatureGreedy)
	}

	log := b.log.String()
	for _, want := range []string{"p2: ideas principales extraídas y persistidas", "ideas=3", "ideas_descartadas=0", "con_pista_de_entrega=true"} {
		if !strings.Contains(log, want) {
			t.Fatalf("la línea de cierre no lleva %q: %q", want, log)
		}
	}
}

// ---------------------------------------------------------------------------
// El anclaje
// ---------------------------------------------------------------------------

// TestP2Run_InventedEvidence_DropsTheIdeaAndTheJobLives lleva dos aserciones distintas:
// la idea inventada no está —ni en lo devuelto ni en lo persistido—, y el job sigue vivo
// (Run devuelve nil y el artefacto se guarda igual).
func TestP2Run_InventedEvidence_DropsTheIdeaAndTheJobLives(t *testing.T) {
	const inventedIdea = "dos bandejas de pasapalos surtidos"
	wants := ambarWants()
	wants[1] = [2]string{inventedIdea, inventedEvidence}
	b := newP2Bench(t, p2Output(llm.ArtifactVersion, wants, [2]string{ambarHintText, deliveryEvidence}), nil)

	ideas, err := b.stage.Run(context.Background(), ambarJob(), ambarText)
	if err != nil {
		t.Fatalf("descartar una idea NO puede tumbar el job, y Run devolvió: %v", err)
	}
	if len(ideas.Wants) != 2 {
		t.Fatalf("ideas vivas = %d, se esperaban 2 (la inventada se descarta)", len(ideas.Wants))
	}
	for _, w := range ideas.Wants {
		if w.Idea == inventedIdea {
			t.Fatal("la idea sin respaldo en el literal sobrevivió al anclaje")
		}
	}
	if len(b.store.saved) != 1 {
		t.Fatalf("artefactos persistidos = %d: el job tenía que seguir su curso", len(b.store.saved))
	}
	if strings.Contains(string(b.store.saved[0].Payload), inventedIdea) {
		t.Fatal("la idea descartada se persistió igual: se está guardando la salida cruda del modelo")
	}
	if len(readP2Artifact(t, b.store.saved[0]).Wants) != 2 {
		t.Fatal("lo persistido no coincide con lo devuelto")
	}

	// Y quedó DICHO en el log, con la posición y sin una palabra del cliente.
	log := b.log.String()
	if !strings.Contains(log, "la idea se descarta") || !strings.Contains(log, "idea_pos=1") {
		t.Fatalf("el descarte no dejó log con la posición de la idea: %q", log)
	}
	if !strings.Contains(log, "ideas_descartadas=1") {
		t.Fatalf("la línea de cierre no cuenta la idea descartada: %q", log)
	}
	if strings.Contains(log, inventedEvidence) || strings.Contains(log, inventedIdea) {
		t.Fatalf("el log volcó texto de la conversación (ADR-0034, INV-6): %q", log)
	}
}

// TestP2Run_InventedDeliveryHint_DropsOnlyTheHint: la pista se ancla con la misma regla.
// Una fecha inventada es peor que ninguna, porque P4 la convertiría en una fecha
// absoluta con toda la apariencia de ser cierta.
func TestP2Run_InventedDeliveryHint_DropsOnlyTheHint(t *testing.T) {
	const inventedHint = "lo necesito para el sábado por la mañana"
	b := newP2Bench(t, p2Output(llm.ArtifactVersion, ambarWants(), [2]string{"el sábado", inventedHint}), nil)

	ideas, err := b.stage.Run(context.Background(), ambarJob(), ambarText)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if ideas.DeliveryHint != nil {
		t.Fatalf("la pista inventada sobrevivió: %+v", *ideas.DeliveryHint)
	}
	if len(ideas.Wants) != 3 {
		t.Fatalf("ideas vivas = %d: la pista se llevó por delante a las ideas", len(ideas.Wants))
	}
	if len(b.store.saved) != 1 || readP2Artifact(t, b.store.saved[0]).DeliveryHint != nil {
		t.Fatal("la pista descartada se persistió igual, o no se persistió el artefacto")
	}
	log := b.log.String()
	if !strings.Contains(log, "la pista se descarta") || !strings.Contains(log, "con_pista_de_entrega=false") {
		t.Fatalf("el descarte de la pista no dejó log: %q", log)
	}
	if strings.Contains(log, inventedHint) || strings.Contains(log, "el sábado") {
		t.Fatalf("el log volcó la pista del cliente: %q", log)
	}
}

// TestP2Run_EveryIdeaInvented_PersistsAnEmptyArtifact: cero ideas vivas tampoco es
// fatal. El artefacto con `wants` vacío es válido y se persiste igual.
func TestP2Run_EveryIdeaInvented_PersistsAnEmptyArtifact(t *testing.T) {
	wants := [][2]string{{"bandejas de pasapalos", inventedEvidence}}
	b := newP2Bench(t, p2Output(llm.ArtifactVersion, wants, [2]string{}), nil)

	ideas, err := b.stage.Run(context.Background(), ambarJob(), ambarText)
	if err != nil {
		t.Fatalf("quedarse sin ideas NO es un fallo, y Run devolvió: %v", err)
	}
	if len(ideas.Wants) != 0 {
		t.Fatalf("ideas vivas = %d, se esperaban 0", len(ideas.Wants))
	}
	if len(b.store.saved) != 1 {
		t.Fatalf("artefactos persistidos = %d; el artefacto vacío TAMBIÉN se persiste", len(b.store.saved))
	}
	if !strings.Contains(string(b.store.saved[0].Payload), `"wants":[]`) {
		t.Fatalf("el artefacto vacío no lleva `wants` como lista vacía: %s", b.store.saved[0].Payload)
	}
}

// ---------------------------------------------------------------------------
// Los caminos que no persisten
// ---------------------------------------------------------------------------

// TestP2Run_UnreadableOutput_IsAQualityFailureAndNothingIsPersisted: una salida sin
// `version`, o que ni es JSON, es un fallo de CALIDAD; no se construye artefacto y el
// error no cita la salida del modelo, que lleva frases del cliente.
func TestP2Run_UnreadableOutput_IsAQualityFailureAndNothingIsPersisted(t *testing.T) {
	withoutVersion := strings.Replace(string(ambarP2Output()), fmt.Sprintf(`{"version":%d,`, llm.ArtifactVersion), `{`, 1)
	cases := map[string]json.RawMessage{
		"output without version":  json.RawMessage(withoutVersion),
		"output that is not JSON": json.RawMessage(`}{ ` + tequenosEvidence),
	}
	for name, output := range cases {
		t.Run(name, func(t *testing.T) {
			b := newP2Bench(t, output, nil)

			ideas, err := b.stage.Run(context.Background(), ambarJob(), ambarText)
			if !errors.Is(err, llm.ErrLLMQuality) {
				t.Fatalf("error = %v; se esperaba un fallo de CALIDAD (el proveedor respondió, mal)", err)
			}
			if !strings.HasPrefix(err.Error(), "p2: la salida del modelo no es un artefacto P2 legible: ") {
				t.Fatalf("texto del error = %q", err.Error())
			}
			if strings.Contains(err.Error(), tequenosEvidence) {
				t.Fatalf("el error cita la salida del modelo: %q", err.Error())
			}
			if ideas != nil || len(b.store.saved) != 0 {
				t.Fatalf("ideas=%v guardados=%d; con una salida ilegible no hay artefacto", ideas, len(b.store.saved))
			}
			if len(b.calls) != 1 {
				t.Fatalf("llamadas = %d; P2 no reintenta: el reintento es del worker", len(b.calls))
			}
		})
	}
}

// TestP2Run_NoLiteral_NeitherAsksForAProviderNorCallsTheModel: un job cuyo sobre no se
// llegó a escribir no gasta la plaza única ni manda un prompt sin texto del cliente.
func TestP2Run_NoLiteral_NeitherAsksForAProviderNorCallsTheModel(t *testing.T) {
	b := newP2Bench(t, ambarP2Output(), nil)

	ideas, err := b.stage.Run(context.Background(), ambarJob(), "")
	if !errors.Is(err, stages.ErrNoLiteral) {
		t.Fatalf("error = %v; se esperaba ErrNoLiteral", err)
	}
	if err.Error() != "stages: el job no trae literal que analizar" {
		t.Fatalf("texto de ErrNoLiteral = %q", err.Error())
	}
	if ideas != nil || len(b.selector.asked) != 0 || len(b.calls) != 0 || len(b.store.saved) != 0 {
		t.Fatalf("ideas=%v provider=%d llamadas=%d guardados=%d; sin literal no se hace nada",
			ideas, len(b.selector.asked), len(b.calls), len(b.store.saved))
	}
}

// TestP2Run_SelectorFails_WrapsTheErrorAndCallsNothing: sin provider no hay llamada.
func TestP2Run_SelectorFails_WrapsTheErrorAndCallsNothing(t *testing.T) {
	errVia := errors.New("el tenant no tiene vía configurada")
	b := newP2Bench(t, ambarP2Output(), nil)
	b.selector.err = errVia

	_, err := b.stage.Run(context.Background(), ambarJob(), ambarText)
	if !errors.Is(err, errVia) {
		t.Fatalf("error = %v; el fallo del selector tiene que salir envuelto con %%w", err)
	}
	if want := "p2: elegir el proveedor del tenant: " + errVia.Error(); err.Error() != want {
		t.Fatalf("texto del error = %q; se esperaba %q", err.Error(), want)
	}
	if len(b.calls) != 0 || len(b.store.saved) != 0 {
		t.Fatalf("llamadas=%d guardados=%d; sin provider no hay nada que hacer", len(b.calls), len(b.store.saved))
	}
}

// TestP2Run_ProviderFails_KeepsTheErrorFamilyAndPersistsNothing: el error sale hacia
// arriba con su familia intacta, que es lo que el worker necesita para decidir.
func TestP2Run_ProviderFails_KeepsTheErrorFamilyAndPersistsNothing(t *testing.T) {
	errInfra := errors.New("edge sin capacidad")
	b := newP2Bench(t, nil, errInfra)

	ideas, err := b.stage.Run(context.Background(), ambarJob(), ambarText)
	if !errors.Is(err, errInfra) || errors.Is(err, llm.ErrLLMQuality) {
		t.Fatalf("error = %v; se esperaba el fallo de infraestructura, y no uno de calidad", err)
	}
	if want := "p2: pedir las ideas principales: " + errInfra.Error(); err.Error() != want {
		t.Fatalf("texto del error = %q; se esperaba %q", err.Error(), want)
	}
	if ideas != nil || len(b.calls) != 1 || len(b.store.saved) != 0 {
		t.Fatalf("ideas=%v llamadas=%d guardados=%d; una llamada, sin reintento y sin artefacto",
			ideas, len(b.calls), len(b.store.saved))
	}
}

// TestP2Run_StoreDidNotSave_TellsTheTwoWaysApart distingue las dos formas de «no se
// guardó»: la base caída (error envuelto) y la transición que no aplicó (centinela).
func TestP2Run_StoreDidNotSave_TellsTheTwoWaysApart(t *testing.T) {
	t.Run("the store fails", func(t *testing.T) {
		errDB := errors.New("conexión perdida")
		b := newP2Bench(t, ambarP2Output(), nil)
		b.store.err = errDB

		ideas, err := b.stage.Run(context.Background(), ambarJob(), ambarText)
		if !errors.Is(err, errDB) || errors.Is(err, stages.ErrJobNotProcessing) {
			t.Fatalf("error = %v; se esperaba el fallo del store envuelto", err)
		}
		if want := "p2: persistir el artefacto: " + errDB.Error(); err.Error() != want {
			t.Fatalf("texto del error = %q; se esperaba %q", err.Error(), want)
		}
		if ideas != nil {
			t.Fatal("Run devolvió artefacto sin haberlo persistido")
		}
	})

	t.Run("the job left processing", func(t *testing.T) {
		b := newP2Bench(t, ambarP2Output(), nil)
		b.store.lost = true

		ideas, err := b.stage.Run(context.Background(), ambarJob(), ambarText)
		if !errors.Is(err, stages.ErrJobNotProcessing) {
			t.Fatalf("error = %v; se esperaba ErrJobNotProcessing", err)
		}
		if err.Error() != "stages: el job ya no estaba en processing; el artefacto no se guardó" {
			t.Fatalf("texto de ErrJobNotProcessing = %q", err.Error())
		}
		if ideas != nil {
			t.Fatal("Run devolvió artefacto sin haberlo persistido")
		}
	})
}

// ---------------------------------------------------------------------------
// El plazo por llamada (R-03)
// ---------------------------------------------------------------------------

// TestP2Run_CallTimeout_BoundsTheModelCallAndNotThePersistence: con la opción, el ctx de
// LA llamada llega acotado; el plazo acaba donde acaba la llamada y la escritura del
// artefacto no lo hereda. Sin la opción, la llamada hereda el ctx del llamante.
func TestP2Run_CallTimeout_BoundsTheModelCallAndNotThePersistence(t *testing.T) {
	const limit = 48 * time.Second

	t.Run("with the option", func(t *testing.T) {
		b := newP2Bench(t, ambarP2Output(), nil, stages.WithCallTimeout(limit))
		if _, err := b.stage.Run(context.Background(), ambarJob(), ambarText); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if len(b.calls) != 1 || !b.calls[0].bounded {
			t.Fatal("la llamada al modelo llegó SIN deadline: el adaptador caería a su default de 30 s")
		}
		if got := b.calls[0].remaining; got > limit || got < limit-5*time.Second {
			t.Fatalf("plazo que llegó a la llamada = %v; se esperaba ≈ %v", got, limit)
		}
		if len(b.store.bounded) != 1 || b.store.bounded[0] {
			t.Fatal("la persistencia heredó el plazo de la llamada: una escritura a la base moriría a mitad")
		}
	})

	t.Run("without the option", func(t *testing.T) {
		b := newP2Bench(t, ambarP2Output(), nil)
		if _, err := b.stage.Run(context.Background(), ambarJob(), ambarText); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if len(b.calls) != 1 || b.calls[0].bounded {
			t.Fatal("sin la opción la llamada hereda el ctx del llamante, que aquí no trae deadline")
		}
	})
}

// ---------------------------------------------------------------------------
// Los puertos y el constructor
// ---------------------------------------------------------------------------

// TestStageStore_CanOnlySaveAnArtifact es la mitad ESTRUCTURAL de «descartar una idea no
// tumba el job»: ninguna etapa puede matar un job porque el puerto no tiene con qué. Si
// alguien añade `Fail` a StageStore, la decisión pasa por la revisión.
func TestStageStore_CanOnlySaveAnArtifact(t *testing.T) {
	port := reflect.TypeOf((*stages.StageStore)(nil)).Elem()
	if port.NumMethod() != 1 || port.Method(0).Name != "SaveStage" {
		names := make([]string, 0, port.NumMethod())
		for i := range port.NumMethod() {
			names = append(names, port.Method(i).Name)
		}
		t.Fatalf("StageStore tiene los métodos %v: una etapa solo puede GUARDAR su artefacto", names)
	}
}

// TestNewP2_NotWired: una etapa a medio construir no nace.
func TestNewP2_NotWired(t *testing.T) {
	log := captureLog(&bytes.Buffer{})
	cases := map[string]struct {
		log      logger.Logger
		selector stages.ProviderSelector
		store    stages.StageStore
	}{
		"without log":      {nil, &fakeSelector{}, &fakeStore{}},
		"without selector": {log, nil, &fakeStore{}},
		"without store":    {log, &fakeSelector{}, nil},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			stage, err := stages.NewP2(c.log, c.selector, c.store)
			if !errors.Is(err, stages.ErrNotWired) {
				t.Fatalf("error = %v; se esperaba ErrNotWired", err)
			}
			if err.Error() != "stages: la etapa necesita log, selector de vía y store" {
				t.Fatalf("texto de ErrNotWired = %q", err.Error())
			}
			if stage != nil {
				t.Fatal("se construyó la etapa a medias")
			}
		})
	}
}
