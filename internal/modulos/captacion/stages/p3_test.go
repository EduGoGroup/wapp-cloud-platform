//go:build pendiente

package stages_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
// Banco de pruebas de P3
//
// Aquí se llama al modelo VARIAS veces por job, así que el doble responde POR LLAMADA
// —según la idea que le toca y según si es el primer intento o el reintento— y guarda
// las entradas, las temperaturas y el ctx de cada una.
// ---------------------------------------------------------------------------

// p3Call es lo que el provider anotó de UNA llamada.
type p3Call struct {
	in          llm.ExtractItemSpecsInput
	temperature float64
	ctx         context.Context
	// previousClosed dice si, al empezar esta llamada, el ctx de la anterior ya estaba
	// cerrado: es lo que distingue un plazo POR LLAMADA de uno para el fan-out entero.
	previousClosed bool
}

// p3Reply es lo que el provider contesta a una llamada.
type p3Reply struct {
	raw json.RawMessage
	err error
}

// p3Bench son la etapa y sus dobles.
type p3Bench struct {
	stage    *stages.P3
	calls    []p3Call
	selector *fakeSelector
	store    *fakeStore
	log      bytes.Buffer
}

// newP3Bench arma P3 con un provider que contesta con `reply(idea, attempt)`: `attempt`
// es cuántas veces se había llamado YA con esa idea (0 en el primer intento).
func newP3Bench(t *testing.T, reply func(idea string, attempt int) p3Reply, opts ...stages.Option) *p3Bench {
	t.Helper()
	b := &p3Bench{store: &fakeStore{}}
	attempts := map[string]int{}
	b.selector = &fakeSelector{provider: &fakeProvider{
		onItemSpecs: func(ctx context.Context, in llm.ExtractItemSpecsInput, o llm.Options) (json.RawMessage, error) {
			call := p3Call{in: in, temperature: o.Temperature, ctx: ctx}
			if n := len(b.calls); n > 0 {
				call.previousClosed = b.calls[n-1].ctx.Err() != nil
			}
			b.calls = append(b.calls, call)
			attempt := attempts[in.Idea]
			attempts[in.Idea]++
			r := reply(in.Idea, attempt)
			return r.raw, r.err
		},
	}}
	stage, err := stages.NewP3(captureLog(&b.log), b.selector, b.store, opts...)
	if err != nil {
		t.Fatalf("NewP3: %v", err)
	}
	b.stage = stage
	return b
}

// ambarIdeas son las tres ideas tal como P3 las recibe de P2.
func ambarIdeas() []llm.Want {
	out := make([]llm.Want, 0, 3)
	for _, pair := range ambarWants() {
		out = append(out, llm.Want{Idea: pair[0], Evidence: pair[1]})
	}
	return out
}

// specOutput arma la respuesta del modelo para UN ítem, con el sobre de design §7.2.
func specOutput(t *testing.T, product, variant, evidence string, addons, customizations []string) json.RawMessage {
	t.Helper()
	item := map[string]any{"product": product, "evidence": evidence}
	if variant != "" {
		item["variant"] = variant
	}
	if len(addons) > 0 {
		item["addon_candidates"] = addons
	}
	if len(customizations) > 0 {
		item["customizations"] = customizations
	}
	return marshalSpecs(t, item)
}

// marshalSpecs envuelve los ítems en el artefacto que devuelve el modelo.
func marshalSpecs(t *testing.T, items ...any) json.RawMessage {
	t.Helper()
	if items == nil {
		items = []any{}
	}
	raw, err := json.Marshal(map[string]any{"version": llm.ArtifactVersion, "items": items})
	if err != nil {
		t.Fatalf("marshal del fixture: %v", err)
	}
	return raw
}

// ambarSpecOutput responde a cada idea de Ambar con una spec creíble anclada a SU
// evidencia: es la respuesta «todo va bien».
func ambarSpecOutput(t *testing.T, idea string) json.RawMessage {
	t.Helper()
	switch {
	case strings.Contains(idea, "decoración infantil"):
		return specOutput(t, "torta", "10 o 12 porciones", chocolateCakeEvidence,
			[]string{"decoración infantil"}, []string{"sin lactosa"})
	case strings.Contains(idea, "vainilla"):
		return specOutput(t, "torta", "25 o 30 porciones", vanillaCakeEvidence,
			[]string{"lluvia de colores"}, nil)
	default:
		return specOutput(t, "tequeños congelados", "paquete de 30", tequenosEvidence, nil, nil)
	}
}

// allGood es el provider que contesta bien a la primera, siempre.
func allGood(t *testing.T) func(string, int) p3Reply {
	t.Helper()
	return func(idea string, _ int) p3Reply { return p3Reply{raw: ambarSpecOutput(t, idea)} }
}

// degenerateOutput es la salida que el modelo chico produce cuando se rompe: JSON válido
// con el relleno del esquema dentro, que `llm.ParseItemSpecs` rechaza como fallo de
// calidad. Es la degeneración REAL medida en campo; un `}{` a lo bruto probaría menos.
func degenerateOutput(t *testing.T) json.RawMessage {
	t.Helper()
	return marshalSpecs(t, map[string]any{"product": llm.PlaceholderEsquema, "evidence": llm.PlaceholderEsquema})
}

// readP3Artifact decodifica lo que se persistió, con la forma del Cloud.
func readP3Artifact(t *testing.T, a intake.Artifact) stages.P3Artifact {
	t.Helper()
	var out stages.P3Artifact
	if err := json.Unmarshal(a.Payload, &out); err != nil {
		t.Fatalf("el artefacto persistido no decodifica: %v", err)
	}
	return out
}

// assertSavedOnce comprueba el sobre del artefacto: uno solo, bajo el job, en la etapa
// `p3` y con las cuentas que se devolvieron.
func assertSavedOnce(t *testing.T, store *fakeStore, items, isolated int) intake.Artifact {
	t.Helper()
	if len(store.saved) != 1 || store.jobs[0] != jobID {
		t.Fatalf("artefactos persistidos = %d bajo %v; se esperaba uno, bajo el job", len(store.saved), store.jobs)
	}
	if store.saved[0].Stage != intake.StageP3 {
		t.Fatalf("etapa persistida = %q, se esperaba %q", store.saved[0].Stage, intake.StageP3)
	}
	got := readP3Artifact(t, store.saved[0])
	if got.Version != llm.ArtifactVersion || len(got.Items) != items || len(got.Isolated) != isolated {
		t.Fatalf("lo persistido no es lo devuelto: version=%d items=%d aislados=%d",
			got.Version, len(got.Items), len(got.Isolated))
	}
	return store.saved[0]
}

// assertNoCustomerText es ADR-0034 / INV-6 en dos sitios a la vez: ni las marcas del
// artefacto ni el log pueden llevar una frase de la conversación.
func assertNoCustomerText(t *testing.T, payload []byte, log, text string) {
	t.Helper()
	if strings.Contains(string(payload), text) {
		t.Fatal("la marca del ítem aislado copió el texto de la idea en el artefacto")
	}
	if strings.Contains(log, text) {
		t.Fatalf("el log volcó texto de la conversación: %q", log)
	}
}

// ---------------------------------------------------------------------------
// El fan-out
// ---------------------------------------------------------------------------

// TestP3Run_ThreeItems_ThreeCalls: el fan-out hace UNA llamada POR ÍTEM y no una por
// lote; cada llamada lleva SU idea, el hilo ENTERO como contexto y temperatura greedy.
func TestP3Run_ThreeItems_ThreeCalls(t *testing.T) {
	b := newP3Bench(t, allGood(t))
	ideas := ambarIdeas()

	art, err := b.stage.Run(context.Background(), ambarJob(), ambarText, ideas)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(b.calls) != 3 {
		t.Fatalf("llamadas al modelo = %d, se esperaban 3 (UNA por ítem)", len(b.calls))
	}
	for i, call := range b.calls {
		if call.in.Idea != ideas[i].Idea {
			t.Fatalf("la llamada %d pidió la idea %q; se esperaba %q", i, call.in.Idea, ideas[i].Idea)
		}
		if call.in.SourceText != ambarText {
			t.Fatalf("la llamada %d no recibió el hilo entero como contexto", i)
		}
		if call.temperature != llm.TemperatureGreedy {
			t.Fatalf("la llamada %d fue a temperatura %v; el primer intento es greedy", i, call.temperature)
		}
	}

	if len(art.Items) != 3 || len(art.Isolated) != 0 || art.Version != llm.ArtifactVersion {
		t.Fatalf("artefacto = %+v; se esperaban 3 ítems, ninguno aislado", art)
	}
	assertSavedOnce(t, b.store, 3, 0)

	log := b.log.String()
	for _, want := range []string{"p3: especificaciones por ítem extraídas y persistidas",
		"ideas=3", "items=3", "items_aislados=0", "items_sobre_tope=0"} {
		if !strings.Contains(log, want) {
			t.Fatalf("la línea de cierre no lleva %q: %q", want, log)
		}
	}
}

// TestP3Run_KeepsRangesTextualAndCandidatesApartFromCustomizations: «10 o 12 porciones»
// llega TEXTUAL a P4 —partirlo es suyo y elegir un número es de nadie—, y los candidatos
// no se mezclan con las personalizaciones: el match mira unos y no las otras. Los ítems
// salen en el orden de las ideas.
func TestP3Run_KeepsRangesTextualAndCandidatesApartFromCustomizations(t *testing.T) {
	b := newP3Bench(t, allGood(t))
	art, err := b.stage.Run(context.Background(), ambarJob(), ambarText, ambarIdeas())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(art.Items) != 3 {
		t.Fatalf("items = %d, se esperaban 3", len(art.Items))
	}
	first := art.Items[0]
	if first.Variant != "10 o 12 porciones" {
		t.Fatalf("variant = %q; el rango tiene que llegar textual a P4", first.Variant)
	}
	if !reflect.DeepEqual(first.AddonCandidates, []string{"decoración infantil"}) ||
		!reflect.DeepEqual(first.Customizations, []string{"sin lactosa"}) {
		t.Fatalf("addon_candidates=%v customizations=%v; viajan en campos SEPARADOS",
			first.AddonCandidates, first.Customizations)
	}
	if art.Items[2].Product != "tequeños congelados" {
		t.Fatalf("items[2] = %+v; los ítems salen en el orden de las ideas", art.Items[2])
	}
}

// TestP3Run_AsksForTheProviderOnceForTheWholeFanOut: la vía es del tenant y de la sesión
// de origen, no de la idea. Pedirla dentro del bucle serían N lecturas y N oportunidades
// de que la vía cambie a mitad de un pedido.
func TestP3Run_AsksForTheProviderOnceForTheWholeFanOut(t *testing.T) {
	b := newP3Bench(t, allGood(t))

	if _, err := b.stage.Run(context.Background(), ambarJob(), ambarText, ambarIdeas()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !reflect.DeepEqual(b.selector.asked, []string{originRoute}) {
		t.Fatalf("el provider se pidió como %v; se esperaba una sola vez, [%q]", b.selector.asked, originRoute)
	}
}

// TestP3Run_NoIdeas_CallsNothingAndPersistsAnEmptyArtifact: un P2 que se quedó sin ideas
// vivas no es un fallo. P3 no gasta la plaza única y deja el artefacto vacío escrito,
// que es lo que hace que la reanudación no repita la etapa.
func TestP3Run_NoIdeas_CallsNothingAndPersistsAnEmptyArtifact(t *testing.T) {
	b := newP3Bench(t, allGood(t))

	art, err := b.stage.Run(context.Background(), ambarJob(), ambarText, nil)
	if err != nil {
		t.Fatalf("cero ideas NO es un fallo, y Run devolvió: %v", err)
	}
	if len(b.calls) != 0 || len(b.selector.asked) != 0 {
		t.Fatalf("llamadas=%d provider=%d; sin ideas no hay nada que preguntar", len(b.calls), len(b.selector.asked))
	}
	if len(art.Items) != 0 || art.Version != llm.ArtifactVersion {
		t.Fatalf("artefacto = %+v", art)
	}
	saved := assertSavedOnce(t, b.store, 0, 0)
	if !strings.Contains(string(saved.Payload), `"items":[]`) {
		t.Fatalf("el artefacto vacío no lleva `items` como lista vacía: %s", saved.Payload)
	}
	if strings.Contains(string(saved.Payload), "isolated") {
		t.Fatalf("el artefacto sin aislados trae la clave de las marcas: %s", saved.Payload)
	}
}

// TestP3Run_SavedArtifactIsStillReadableByTheSharedParser custodia el precio de haber
// extendido el contrato de §7.2 con la clave `isolated`: el lector compartido —el que
// usa P4— sigue leyendo el artefacto sin enterarse.
func TestP3Run_SavedArtifactIsStillReadableByTheSharedParser(t *testing.T) {
	ideas := ambarIdeas()
	b := newP3Bench(t, func(idea string, _ int) p3Reply {
		if idea == ideas[1].Idea {
			return p3Reply{raw: degenerateOutput(t)}
		}
		return p3Reply{raw: ambarSpecOutput(t, idea)}
	})

	if _, err := b.stage.Run(context.Background(), ambarJob(), ambarText, ideas); err != nil {
		t.Fatalf("Run: %v", err)
	}
	saved := assertSavedOnce(t, b.store, 2, 1)
	if !strings.Contains(string(saved.Payload), `"isolated":[{"idea_pos":1,"reason":"quality"}]`) {
		t.Fatalf("la marca no tiene la forma del contrato: %s", saved.Payload)
	}

	read, err := llm.ParseItemSpecs(saved.Payload)
	if err != nil {
		t.Fatalf("el parser compartido no lee el artefacto de P3: %v", err)
	}
	if read.Version != llm.ArtifactVersion || len(read.Items) != 2 {
		t.Fatalf("el parser compartido leyó version=%d items=%d", read.Version, len(read.Items))
	}
}

// TestIsolationReasons_AreTheClosedVocabulary: los tres valores se serializan al
// artefacto y los lee la bandeja del dueño. Van LITERALES: comparar una constante
// consigo misma dejaría pasar un renombrado que rompería al lector.
func TestIsolationReasons_AreTheClosedVocabulary(t *testing.T) {
	got := []string{stages.ReasonQuality, stages.ReasonEvidence, stages.ReasonOverLimit}
	if want := []string{"quality", "evidence", "over_limit"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("motivos de aislamiento = %v; el contrato dice %v", got, want)
	}

	raw, err := json.Marshal(stages.IsolatedItem{IdeaPos: 4, Reason: stages.ReasonOverLimit})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(raw) != `{"idea_pos":4,"reason":"over_limit"}` {
		t.Fatalf("la marca se serializa como %s", raw)
	}
}

// ---------------------------------------------------------------------------
// Los caminos que no persisten
// ---------------------------------------------------------------------------

// TestP3Run_NoLiteral_NeitherAsksForAProviderNorCallsTheModel: la misma guarda que P2, y
// con más razón —P3 gastaría N plazas, no una—.
func TestP3Run_NoLiteral_NeitherAsksForAProviderNorCallsTheModel(t *testing.T) {
	b := newP3Bench(t, allGood(t))

	art, err := b.stage.Run(context.Background(), ambarJob(), "", ambarIdeas())
	if !errors.Is(err, stages.ErrNoLiteral) {
		t.Fatalf("error = %v; se esperaba ErrNoLiteral", err)
	}
	if art != nil || len(b.selector.asked) != 0 || len(b.calls) != 0 || len(b.store.saved) != 0 {
		t.Fatalf("art=%v provider=%d llamadas=%d guardados=%d; sin literal no se hace nada",
			art, len(b.selector.asked), len(b.calls), len(b.store.saved))
	}
}

// TestP3Run_SelectorFails_WrapsTheErrorAndCallsNothing: sin provider no hay fan-out.
func TestP3Run_SelectorFails_WrapsTheErrorAndCallsNothing(t *testing.T) {
	errVia := errors.New("el tenant no tiene vía configurada")
	b := newP3Bench(t, allGood(t))
	b.selector.err = errVia

	art, err := b.stage.Run(context.Background(), ambarJob(), ambarText, ambarIdeas())
	if !errors.Is(err, errVia) {
		t.Fatalf("error = %v; el fallo del selector tiene que salir envuelto con %%w", err)
	}
	if want := "p3: elegir el proveedor del tenant: " + errVia.Error(); err.Error() != want {
		t.Fatalf("texto del error = %q; se esperaba %q", err.Error(), want)
	}
	if art != nil || len(b.calls) != 0 || len(b.store.saved) != 0 {
		t.Fatalf("art=%v llamadas=%d guardados=%d", art, len(b.calls), len(b.store.saved))
	}
}

// TestP3Run_StoreDidNotSave_TellsTheTwoWaysApart: las dos formas de «no se guardó»
// siguen siendo distinguibles desde P3.
func TestP3Run_StoreDidNotSave_TellsTheTwoWaysApart(t *testing.T) {
	t.Run("the store fails", func(t *testing.T) {
		errDB := errors.New("conexión perdida")
		b := newP3Bench(t, allGood(t))
		b.store.err = errDB

		art, err := b.stage.Run(context.Background(), ambarJob(), ambarText, ambarIdeas())
		if !errors.Is(err, errDB) || errors.Is(err, stages.ErrJobNotProcessing) {
			t.Fatalf("error = %v; se esperaba el fallo del store envuelto", err)
		}
		if want := "p3: persistir el artefacto: " + errDB.Error(); err.Error() != want {
			t.Fatalf("texto del error = %q; se esperaba %q", err.Error(), want)
		}
		if art != nil {
			t.Fatal("Run devolvió artefacto sin haberlo persistido")
		}
	})

	t.Run("the job left processing", func(t *testing.T) {
		b := newP3Bench(t, allGood(t))
		b.store.lost = true

		art, err := b.stage.Run(context.Background(), ambarJob(), ambarText, ambarIdeas())
		if !errors.Is(err, stages.ErrJobNotProcessing) {
			t.Fatalf("error = %v; se esperaba ErrJobNotProcessing", err)
		}
		if art != nil {
			t.Fatal("Run devolvió artefacto sin haberlo persistido")
		}
	})
}

// ---------------------------------------------------------------------------
// El plazo por llamada (R-03)
// ---------------------------------------------------------------------------

// TestP3Run_CallTimeout_BoundsEachCallAndNotTheWholeFanOut: cada ítem lleva SU plazo —el
// de una llamada, no el de las N—, también el reintento, y el plazo no alcanza a la
// persistencia. Que el ctx de una llamada ya esté cerrado cuando empieza la siguiente es
// lo que distingue «por llamada» de «un plazo para todo el fan-out».
func TestP3Run_CallTimeout_BoundsEachCallAndNotTheWholeFanOut(t *testing.T) {
	const limit = 48 * time.Second
	ideas := ambarIdeas()
	b := newP3Bench(t, func(idea string, attempt int) p3Reply {
		if idea == ideas[0].Idea && attempt == 0 {
			return p3Reply{raw: degenerateOutput(t)} // fuerza un reintento
		}
		return p3Reply{raw: ambarSpecOutput(t, idea)}
	}, stages.WithCallTimeout(limit))

	if _, err := b.stage.Run(context.Background(), ambarJob(), ambarText, ideas); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(b.calls) != 4 {
		t.Fatalf("llamadas = %d; se esperaban 4 (3 ítems + 1 reintento)", len(b.calls))
	}
	for i, call := range b.calls {
		deadline, ok := call.ctx.Deadline()
		if !ok {
			t.Fatalf("la llamada %d llegó SIN deadline (temperatura %v)", i, call.temperature)
		}
		if got := time.Until(deadline); got > limit || got < limit-5*time.Second {
			t.Fatalf("la llamada %d tiene %v de plazo; se esperaba ≈ %v", i, got, limit)
		}
		if i > 0 && !call.previousClosed {
			t.Fatalf("al empezar la llamada %d el plazo de la anterior seguía abierto: es un plazo por FAN-OUT, no por llamada", i)
		}
	}
	if len(b.store.bounded) != 1 || b.store.bounded[0] {
		t.Fatal("la persistencia heredó el plazo de una llamada")
	}
}

// TestP3Run_WithoutCallTimeout_InheritsTheCallerContext: el default es compatible.
func TestP3Run_WithoutCallTimeout_InheritsTheCallerContext(t *testing.T) {
	b := newP3Bench(t, allGood(t))
	if _, err := b.stage.Run(context.Background(), ambarJob(), ambarText, ambarIdeas()[:1]); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, ok := b.calls[0].ctx.Deadline(); ok {
		t.Fatal("sin la opción la llamada hereda el ctx del llamante, que aquí no trae deadline")
	}
}

// ---------------------------------------------------------------------------
// El constructor
// ---------------------------------------------------------------------------

// TestNewP3_NotWired: una etapa a medio construir no nace.
func TestNewP3_NotWired(t *testing.T) {
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
			stage, err := stages.NewP3(c.log, c.selector, c.store)
			if !errors.Is(err, stages.ErrNotWired) {
				t.Fatalf("error = %v; se esperaba ErrNotWired", err)
			}
			if stage != nil {
				t.Fatal("se construyó la etapa a medias")
			}
		})
	}
}
