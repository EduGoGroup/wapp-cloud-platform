package stages_test

// Trozo de p3_test.go (E-13): los DESENLACES DE UN ÍTEM — el reintento por calidad, el
// aislamiento con marca y el fallo de infraestructura, que ni se reintenta ni se aísla.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-shared/llm"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/stages"
)

// retriesOf cuenta las llamadas a TemperatureRetry y exige que todas sean de la idea que
// falló: reintentar una idea sana sería gastar la plaza única por gusto.
func retriesOf(t *testing.T, calls []p3Call, failedIdea string) int {
	t.Helper()
	n := 0
	for _, call := range calls {
		if call.temperature != llm.TemperatureRetry {
			continue
		}
		n++
		if call.in.Idea != failedIdea {
			t.Fatalf("se reintentó una idea que no falló: %q", call.in.Idea)
		}
	}
	return n
}

// TestP3Run_DegenerateOutput_RetriesOnceAndIsolatesTheItem lleva cuatro aserciones que
// no son la misma: hubo reintento; fue a TemperatureRetry y solo él; el ítem envenenado
// quedó AISLADO CON MARCA —posición y motivo—, no borrado; y los otros dos siguieron.
func TestP3Run_DegenerateOutput_RetriesOnceAndIsolatesTheItem(t *testing.T) {
	ideas := ambarIdeas()
	poisoned := ideas[1].Idea
	b := newP3Bench(t, func(idea string, _ int) p3Reply {
		if idea == poisoned {
			return p3Reply{raw: degenerateOutput(t)}
		}
		return p3Reply{raw: ambarSpecOutput(t, idea)}
	})

	art, err := b.stage.Run(context.Background(), ambarJob(), ambarText, ideas)
	if err != nil {
		t.Fatalf("un ítem envenenado NO puede tumbar el job, y Run devolvió: %v", err)
	}
	if len(b.calls) != 4 {
		t.Fatalf("llamadas = %d; se esperaban 4 (3 ítems + 1 reintento)", len(b.calls))
	}
	if n := retriesOf(t, b.calls, poisoned); n != 1 {
		t.Fatalf("llamadas a temperatura %v = %d; se esperaba exactamente 1", llm.TemperatureRetry, n)
	}
	if b.calls[2].in.SourceText != ambarText || b.calls[2].in.Idea != poisoned {
		t.Fatalf("el reintento no llevó la misma idea y el hilo entero: %+v", b.calls[2].in)
	}

	if len(art.Isolated) != 1 || art.Isolated[0] != (stages.IsolatedItem{IdeaPos: 1, Reason: stages.ReasonQuality}) {
		t.Fatalf("aislados = %+v; se esperaba exactamente {IdeaPos:1 Reason:%q}", art.Isolated, stages.ReasonQuality)
	}
	if len(art.Items) != 2 {
		t.Fatalf("items = %d; los otros dos ítems tenían que seguir", len(art.Items))
	}
	saved := assertSavedOnce(t, b.store, 2, 1)

	log := b.log.String()
	for _, want := range []string{"se reintenta UNA vez", "queda aislado", "idea_pos=1", "items_aislados=1"} {
		if !strings.Contains(log, want) {
			t.Fatalf("el aislamiento no dejó constancia (%q): %q", want, log)
		}
	}
	assertNoCustomerText(t, saved.Payload, log, poisoned)
}

// TestP3Run_RetryRecovers_TheItemIsNotIsolated: si el reintento sale bien, el ítem entra
// en el artefacto como cualquier otro.
func TestP3Run_RetryRecovers_TheItemIsNotIsolated(t *testing.T) {
	b := newP3Bench(t, func(idea string, attempt int) p3Reply {
		if attempt == 0 {
			return p3Reply{raw: degenerateOutput(t)}
		}
		return p3Reply{raw: ambarSpecOutput(t, idea)}
	})

	art, err := b.stage.Run(context.Background(), ambarJob(), ambarText, ambarIdeas()[:1])
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(b.calls) != 2 || b.calls[1].temperature != llm.TemperatureRetry {
		t.Fatalf("llamadas = %d; se esperaba el intento y UN reintento a %v", len(b.calls), llm.TemperatureRetry)
	}
	if len(art.Items) != 1 || len(art.Isolated) != 0 {
		t.Fatalf("artefacto = %+v; el reintento recuperó el ítem", art)
	}
}

// TestP3Run_RetryIsExactlyOne cuenta las llamadas de un job de UN ítem que falla
// SIEMPRE: aquí no se comprueba que el reintento existe, sino que se PARA. Cada intento
// son 22–32 s de la plaza única del Edge.
func TestP3Run_RetryIsExactlyOne(t *testing.T) {
	b := newP3Bench(t, func(string, int) p3Reply { return p3Reply{raw: degenerateOutput(t)} })

	art, err := b.stage.Run(context.Background(), ambarJob(), ambarText, ambarIdeas()[:1])
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(b.calls) != 2 {
		t.Fatalf("llamadas = %d para UN ítem; se esperaban exactamente 2 (intento + UN reintento)", len(b.calls))
	}
	if b.calls[0].temperature != llm.TemperatureGreedy || b.calls[1].temperature != llm.TemperatureRetry {
		t.Fatalf("temperaturas = %v, %v; se esperaba greedy y luego %v",
			b.calls[0].temperature, b.calls[1].temperature, llm.TemperatureRetry)
	}
	// Cero ítems válidos tampoco es fatal: el artefacto se persiste vacío, con la marca.
	if len(art.Items) != 0 || len(art.Isolated) != 1 {
		t.Fatalf("artefacto = %+v; se esperaba 0 items y 1 aislado", art)
	}
	assertSavedOnce(t, b.store, 0, 1)
}

// TestP3Run_InfrastructureError_IsNeitherRetriedNorIsolated: el reintento es SOLO por
// calidad. Una caída no se reintenta aquí —la reintenta el job, con su backoff— y no se
// aísla: dejaría al cliente sin un ítem que el sistema nunca llegó a preguntar.
func TestP3Run_InfrastructureError_IsNeitherRetriedNorIsolated(t *testing.T) {
	errInfra := errors.New("edge sin capacidad")
	b := newP3Bench(t, func(string, int) p3Reply { return p3Reply{err: errInfra} })

	art, err := b.stage.Run(context.Background(), ambarJob(), ambarText, ambarIdeas())
	if !errors.Is(err, errInfra) || errors.Is(err, llm.ErrLLMQuality) {
		t.Fatalf("error = %v; el fallo de infraestructura sale con su familia intacta, y no como calidad", err)
	}
	if want := "p3: especificar el ítem en la posición 0: " + errInfra.Error(); err.Error() != want {
		t.Fatalf("texto del error = %q; se esperaba %q", err.Error(), want)
	}
	if art != nil {
		t.Fatalf("Run devolvió artefacto con un fallo de infraestructura: %+v", art)
	}
	if len(b.calls) != 1 {
		t.Fatalf("llamadas = %d; ni se reintenta ni se sigue con los demás ítems", len(b.calls))
	}
	if len(b.store.saved) != 0 {
		t.Fatalf("se persistió un artefacto pese al fallo de infraestructura: %d", len(b.store.saved))
	}
}

// TestP3Run_InfrastructureErrorOnTheRetry_AlsoLeavesWithItsFamily: la caída puede llegar
// en el reintento; tampoco entonces se aísla el ítem, y el error dice que fue ahí.
func TestP3Run_InfrastructureErrorOnTheRetry_AlsoLeavesWithItsFamily(t *testing.T) {
	errInfra := errors.New("socket caído")
	ideas := ambarIdeas()
	b := newP3Bench(t, func(idea string, attempt int) p3Reply {
		switch {
		case idea != ideas[1].Idea:
			return p3Reply{raw: ambarSpecOutput(t, idea)}
		case attempt == 0:
			return p3Reply{raw: degenerateOutput(t)}
		default:
			return p3Reply{err: errInfra}
		}
	})

	art, err := b.stage.Run(context.Background(), ambarJob(), ambarText, ideas)
	if !errors.Is(err, errInfra) {
		t.Fatalf("error = %v; el fallo del reintento tiene que salir con su familia intacta", err)
	}
	if want := "p3: especificar el ítem en la posición 1 (reintento): " + errInfra.Error(); err.Error() != want {
		t.Fatalf("texto del error = %q; se esperaba %q", err.Error(), want)
	}
	if art != nil || len(b.calls) != 3 || len(b.store.saved) != 0 {
		t.Fatalf("art=%v llamadas=%d guardados=%d; el job se suelta entero", art, len(b.calls), len(b.store.saved))
	}
}

// TestP3Run_TwoItemsIsolatedForDifferentReasons_TheRestIsPersisted junta en un job los
// DOS motivos del fan-out —la salida ilegible y la evidencia inventada— y afirma que el
// artefacto de los demás LLEGA A LA BASE. La evidencia inventada NO se reintenta: es una
// salida bien formada que miente, y subir la temperatura no la vuelve honesta.
func TestP3Run_TwoItemsIsolatedForDifferentReasons_TheRestIsPersisted(t *testing.T) {
	ideas := ambarIdeas()
	byQuality, byEvidence := ideas[0].Idea, ideas[1].Idea
	b := newP3Bench(t, func(idea string, _ int) p3Reply {
		switch idea {
		case byQuality:
			return p3Reply{raw: degenerateOutput(t)}
		case byEvidence:
			return p3Reply{raw: specOutput(t, "bandeja de pasapalos", "", inventedEvidence, nil, nil)}
		default:
			return p3Reply{raw: ambarSpecOutput(t, idea)}
		}
	})

	art, err := b.stage.Run(context.Background(), ambarJob(), ambarText, ideas)
	if err != nil {
		t.Fatalf("dos ítems aislados NO pueden tumbar el job, y Run devolvió: %v", err)
	}
	if len(art.Items) != 1 || art.Items[0].Product != "tequeños congelados" {
		t.Fatalf("items = %+v; solo el tercero tenía que sobrevivir", art.Items)
	}
	want := []stages.IsolatedItem{
		{IdeaPos: 0, Reason: stages.ReasonQuality},
		{IdeaPos: 1, Reason: stages.ReasonEvidence},
	}
	if len(art.Isolated) != 2 || art.Isolated[0] != want[0] || art.Isolated[1] != want[1] {
		t.Fatalf("marcas = %+v; se esperaba %+v, en el orden de las ideas", art.Isolated, want)
	}
	// 2 llamadas por la ilegible + 1 por la inventada (sin reintento) + 1 por la sana.
	if len(b.calls) != 4 || retriesOf(t, b.calls, byQuality) != 1 {
		t.Fatalf("llamadas = %d; una evidencia inventada no se reintenta", len(b.calls))
	}

	saved := assertSavedOnce(t, b.store, 1, 2)
	if strings.Contains(string(saved.Payload), "bandeja de pasapalos") {
		t.Fatal("la spec sin respaldo en el literal se persistió igual: se guarda la salida cruda del modelo")
	}
	if !strings.Contains(b.log.String(), "la evidencia del ítem no aparece en el literal del cliente") {
		t.Fatalf("la evidencia inventada no dejó log: %q", b.log.String())
	}
	assertNoCustomerText(t, nil, b.log.String(), inventedEvidence)
}

// TestP3Run_EveryItemIsolated_StillPersistsAndReturnsNil: ningún ítem aislado tumba el
// job, tampoco cuando lo son todos.
func TestP3Run_EveryItemIsolated_StillPersistsAndReturnsNil(t *testing.T) {
	b := newP3Bench(t, func(string, int) p3Reply {
		return p3Reply{raw: specOutput(t, "bandeja de pasapalos", "", inventedEvidence, nil, nil)}
	})

	art, err := b.stage.Run(context.Background(), ambarJob(), ambarText, ambarIdeas())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(art.Items) != 0 || len(art.Isolated) != 3 {
		t.Fatalf("artefacto = %+v; se esperaban los tres ítems aislados", art)
	}
	assertSavedOnce(t, b.store, 0, 3)
}

// TestP3Run_SeveralSpecsInOneCall_KeepsOnlyTheFirst cubre la degeneración más cara: el
// modelo tiene el HILO ENTERO en el prompt, así que puede ignorar el «especifica UN SOLO
// ítem» y devolver varios. En N llamadas serían N² specs y el mismo producto cobrado N
// veces. Se conserva la PRIMERA: perder una repetición es recuperable, duplicar una
// línea con precio no.
func TestP3Run_SeveralSpecsInOneCall_KeepsOnlyTheFirst(t *testing.T) {
	two := marshalSpecs(t,
		map[string]any{"product": "torta", "evidence": chocolateCakeEvidence},
		map[string]any{"product": "tequeños", "evidence": tequenosEvidence})
	b := newP3Bench(t, func(string, int) p3Reply { return p3Reply{raw: two} })

	art, err := b.stage.Run(context.Background(), ambarJob(), ambarText, ambarIdeas()[:1])
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(art.Items) != 1 || art.Items[0].Product != "torta" {
		t.Fatalf("items = %+v; se esperaba solo el primero", art.Items)
	}
	if len(b.calls) != 1 {
		t.Fatalf("llamadas = %d; una spec de más no es un fallo de calidad y no se reintenta", len(b.calls))
	}
	if !strings.Contains(b.log.String(), "descartadas=1") {
		t.Fatalf("descartar una spec de más no dejó constancia: %q", b.log.String())
	}
}

// TestP3Run_ZeroSpecs_IsAQualityFailure: se pidió UN ítem y el modelo devolvió un
// artefacto bien formado con la lista vacía. El parser compartido lo acepta, así que sin
// esta regla el ítem desaparecería sin marca y sin reintento.
func TestP3Run_ZeroSpecs_IsAQualityFailure(t *testing.T) {
	empty := marshalSpecs(t)
	b := newP3Bench(t, func(string, int) p3Reply { return p3Reply{raw: empty} })

	art, err := b.stage.Run(context.Background(), ambarJob(), ambarText, ambarIdeas()[:1])
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(b.calls) != 2 || b.calls[1].temperature != llm.TemperatureRetry {
		t.Fatalf("llamadas = %d; cero specs es un fallo de calidad y se reintenta UNA vez", len(b.calls))
	}
	if len(art.Items) != 0 || len(art.Isolated) != 1 || art.Isolated[0].Reason != stages.ReasonQuality {
		t.Fatalf("artefacto = %+v; se esperaba el ítem aislado por calidad", art)
	}
}
