package stages_test

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake/anclaje"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake/stages"
)

// audio_jamas_al_llm_test.go — EL CANDADO DE T5.3: el audio del cliente JAMÁS
// entra en un prompt.
//
// ════════════════════════════════════════════════════════════════════════════
// QUÉ MITAD DE T5.3 ES ÉSTA, Y QUÉ MITAD **NO** SE CONSTRUYE
// ════════════════════════════════════════════════════════════════════════════
//
// El enunciado de T5.3 pide dos cosas del audio: «adjunto reproducible/descargable
// vía URL prefirmada (infra 017), JAMÁS enviado al LLM». La primera NO se
// construye, y no por falta de tiempo: la deroga **D-044.52 §1**, que es POSTERIOR
// y ya está implementada — el audio se pinta como RÓTULO SIN ENLACE
// (`anclaje.EtiquetaAudio`), porque hoy no hay dato que servir (`Media` va
// `anclaje.Reparto{}` vacío a propósito, `pipeline.go:914`) ni destino que sirva
// (la única ruta de media presigna SUBIDA). El export de audio tiene dueño
// nombrado desde antes: Plan 045 O3 (doc 14 D-11).
//
// La SEGUNDA mitad sigue viva entera y es la que se prueba aquí.
//
// ════════════════════════════════════════════════════════════════════════════
// POR QUÉ ESTA SEPARACIÓN NO SE CAE SOLA, Y QUÉ LA PODRÍA ROMPER
// ════════════════════════════════════════════════════════════════════════════
//
// Hoy la separación es ESTRUCTURAL y no una comprobación: `literalDe`
// (`pipeline.go:712`) abre el sobre de `job.SourceText` y ese literal es lo ÚNICO
// que viaja a P2/P3/P4; `SourceRefs` —donde vive la referencia del audio— es un
// campo aparte del `ClaimedJob` que ninguna etapa concatena.
//
// 🔴 «Estructural» no es «imposible». Lo que la rompería es de una línea: que
// alguien añada un campo de refs a `llm.ExtractMainIdeasInput` (o a las otras
// dos) y lo rellene «para dar más contexto al modelo». Por eso aquí hay DOS tests
// y no uno:
//
//   - el de CONDUCTA corre las tres etapas de verdad contra un provider espía y
//     mira el PROMPT QUE SE CONSTRUIRÍA — la forma que viaja por el cable, no la
//     cómoda del struct;
//   - el ESTRUCTURAL congela los campos de los tres tipos de entrada, así que un
//     campo nuevo pone el test rojo aunque nadie lo haya rellenado todavía. Un
//     «jamás» que solo mira lo que hoy se rellena no es un jamás.

// refAudioDeAmbar es la referencia del audio que Ambar mandó en el hilo (doc 08).
// Tiene la forma de una key del Plan 017 y NO es una URL: `anclaje.MediaRef.Ref`
// es un identificador opaco, y ese contrato también se vigila aquí de refilón —si
// alguien metiera una URL firmada en `SourceRefs`, este fixture dejaría de
// parecerse a lo que corre.
const refAudioDeAmbar = "wapp/media/t-p2/2026/07/13/audio-0af3c9d1.ogg"

// jobConAudio es el job del caso Ambar CON el audio entre sus referencias.
//
// 🔴 SIN ESTO EL TEST SERÍA VACUO: un job sin ref de audio pasaría la aserción
// «ningún prompt contiene la ref» sin haber probado nada. La ref tiene que estar
// de verdad en el job, y el propio test lo comprueba antes de mirar los prompts.
func jobConAudio() intake.ClaimedJob {
	j := jobAmbarP4()
	j.SourceRefs = []string{"wa-msg-9955-01", refAudioDeAmbar}
	return j
}

// ---------------------------------------------------------------------------
// EL ESPÍA
// ---------------------------------------------------------------------------

// llamadaEspiada es UNA petición al modelo, con las DOS formas: el struct de
// entrada serializado y el PROMPT que ese struct produce.
type llamadaEspiada struct {
	etapa  string
	campos json.RawMessage
	prompt string
}

// provEspia recuerda todo lo que se le pidió al modelo y construye el prompt REAL
// con los `Build…Prompt` del paquete compartido —los mismos que usa cualquier
// implementación del puerto (contrato de `llm.LLMProvider`)—.
//
// ⚠️ Un espía que solo guardara el struct dejaría un hueco: el prompt lo compone
// la plantilla, y si algún día la plantilla imprimiera algo derivado del job que
// el struct no enseña, el struct saldría limpio y el cable no. Por eso se guardan
// los dos.
type provEspia struct {
	t         *testing.T
	llamadas  []llamadaEspiada
	respP2    json.RawMessage
	respP3    func(idea string) json.RawMessage
	respP4    json.RawMessage
	llamadaP3 int
}

func (p *provEspia) anota(etapa string, in any, prompt string) {
	p.t.Helper()
	raw, err := json.Marshal(in)
	if err != nil {
		p.t.Fatalf("el espía no pudo serializar la entrada de %s: %v", etapa, err)
	}
	p.llamadas = append(p.llamadas, llamadaEspiada{etapa: etapa, campos: raw, prompt: prompt})
}

func (p *provEspia) ExtractMainIdeas(_ context.Context, in llm.ExtractMainIdeasInput, _ llm.Options) (json.RawMessage, error) {
	p.anota(intake.StageP2, in, llm.BuildExtractMainIdeasPrompt(in))
	return p.respP2, nil
}

func (p *provEspia) ExtractItemSpecs(_ context.Context, in llm.ExtractItemSpecsInput, _ llm.Options) (json.RawMessage, error) {
	p.anota(intake.StageP3, in, llm.BuildExtractItemSpecsPrompt(in))
	p.llamadaP3++
	return p.respP3(in.Idea), nil
}

func (p *provEspia) NormalizeQuantities(_ context.Context, in llm.NormalizeQuantitiesInput, _ llm.Options) (json.RawMessage, error) {
	p.anota(intake.StageP4, in, llm.BuildNormalizeQuantitiesPrompt(in))
	return p.respP4, nil
}

func (p *provEspia) ClassifyRequest(_ context.Context, in llm.ClassifyRequestInput, _ llm.Options) (json.RawMessage, error) {
	p.anota("classify", in, llm.BuildClassifyRequestPrompt(in))
	return nil, errNoLlamar
}

func (p *provEspia) GenerateQuoteText(_ context.Context, in llm.GenerateQuoteTextInput, _ llm.Options) (json.RawMessage, error) {
	p.anota("quote", in, llm.BuildGenerateQuoteTextPrompt(in))
	return nil, errNoLlamar
}

// ---------------------------------------------------------------------------
// EL TEST DE CONDUCTA
// ---------------------------------------------------------------------------

// TestElAudioJamasEntraEnUnPrompt es el criterio literal de T5.3: «test del
// pipeline: `source_refs` de audio nunca aparece en ningún prompt (assert sobre
// el provider fake)».
//
// Corre las TRES etapas que hablan con el modelo —P2, P3 (una llamada por idea) y
// P4— con el MISMO espía, sobre un job cuyas `source_refs` llevan de verdad la
// referencia del audio.
//
// 💥 MUTACIÓN que lo pone rojo: añadir `SourceRefs` a `llm.ExtractMainIdeasInput`
// y rellenarlo en `P2.pedirIdeas` con `job.SourceRefs` ⇒ la ref aparece en el
// struct serializado y el test cae. (La plantilla no la imprimiría, así que el
// prompt seguiría limpio: por eso se miran LAS DOS formas y no solo una.)
func TestElAudioJamasEntraEnUnPrompt(t *testing.T) {
	job := jobConAudio()

	// (0) EL FIXTURE LLEVA EL AUDIO. Sin esta comprobación, el resto del test
	// pasaría también con un job sin adjuntos y no probaría nada.
	if !contiene(job.SourceRefs, refAudioDeAmbar) {
		t.Fatalf("el fixture no lleva la ref del audio en source_refs (%v): el test sería VACUO", job.SourceRefs)
	}

	espia := &provEspia{
		t:      t,
		respP2: artefactoP2(t, llm.ArtifactVersion, wantsDeAmbar(), hintDeAmbar()),
		respP3: func(idea string) json.RawMessage { return specDeLaIdea(t, idea) },
		respP4: json.RawMessage(respuestaP4Ambar),
	}
	sel := &selFake{prov: espia}
	store := &storeFake{}
	var buf bytes.Buffer
	log := logger.New(logger.WithWriter(&buf))
	ctx := context.Background()

	p2, err := stages.NewP2(log, sel, store)
	if err != nil {
		t.Fatalf("NewP2: %v", err)
	}
	ideas, err := p2.Run(ctx, job, textoAmbar)
	if err != nil {
		t.Fatalf("P2.Run: %v", err)
	}

	p3, err := stages.NewP3(log, sel, store)
	if err != nil {
		t.Fatalf("NewP3: %v", err)
	}
	if _, err := p3.Run(ctx, job, textoAmbar, ideas.Wants); err != nil {
		t.Fatalf("P3.Run: %v", err)
	}

	p4, err := stages.NewP4(log, sel, store, stages.ZonaPorDefecto)
	if err != nil {
		t.Fatalf("NewP4: %v", err)
	}
	if _, err := p4.Run(ctx, job, textoAmbar, specsDeAmbar(), pistaDeAmbar()); err != nil {
		t.Fatalf("P4.Run: %v", err)
	}

	// (1) HUBO LLAMADAS DE VERDAD. Un espía con cero llamadas pasaría cualquier
	// aserción de ausencia — que es la forma más común de que un test así mienta.
	const minimo = 5 // 1 de P2 + 3 de P3 (una por idea) + 1 de P4
	if len(espia.llamadas) < minimo {
		t.Fatalf("el espía recogió %d llamadas; se esperaban al menos %d (P2 + una P3 por idea + P4)",
			len(espia.llamadas), minimo)
	}

	// (2) EL ESPÍA SÍ VE EL CONTENIDO. Si no viera nada, la aserción de ausencia
	// sería trivial: se comprueba que el literal del cliente SÍ llega.
	if !algunaLlamadaContiene(espia.llamadas, "tequeños congelados") {
		t.Fatal("ninguna llamada contiene el literal del cliente: el espía no está viendo lo que viaja")
	}

	// (3) LA AUSENCIA, sobre las dos formas: el struct y el prompt.
	prohibidos := []string{
		refAudioDeAmbar,       // la ref completa
		"audio-0af3c9d1",      // el nombre del objeto suelto
		"wapp/media",          // el prefijo de la key del Plan 017
		".ogg",                // la extensión
		anclaje.EtiquetaAudio, // el rótulo que el dueño ve en la bandeja
		anclaje.KindAudio,     // "audio"
		anclaje.KindPTT,       // "ptt", la nota de voz de WhatsApp
		"wa-msg-9955-01",      // y de paso: NINGUNA source_ref viaja al modelo
	}
	for _, l := range espia.llamadas {
		for _, mal := range prohibidos {
			if strings.Contains(string(l.campos), mal) {
				t.Errorf("la llamada de %s lleva %q en su ENTRADA: %s", l.etapa, mal, l.campos)
			}
			if strings.Contains(l.prompt, mal) {
				t.Errorf("el PROMPT de %s contiene %q", l.etapa, mal)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// EL CANDADO ESTRUCTURAL
// ---------------------------------------------------------------------------

// TestLasEntradasDelModeloNoTienenDondeMeterUnAdjunto congela los campos de los
// tres tipos de entrada del puerto LLM.
//
// 🔴 POR QUÉ HACE FALTA ADEMÁS DEL DE CONDUCTA: aquél solo puede afirmar sobre lo
// que HOY se rellena. El día que alguien añada `MediaRefs []string` a
// `ExtractItemSpecsInput` y todavía no lo rellene, el test de conducta seguiría
// verde y el agujero ya estaría abierto — con el campo puesto, rellenarlo es una
// línea que nadie revisaría dos veces. Esto lo pone rojo EN EL COMMIT QUE ABRE EL
// HUECO, que es cuando la decisión todavía se puede tomar.
//
// No es una prohibición eterna: es una PUERTA CON TIMBRE. Si algún día el diseño
// decide que el modelo tiene que ver una transcripción del audio (el `stt_audio`
// que el plan deja como follow-up), se añade el campo, se actualiza esta lista y
// la decisión queda escrita aquí en vez de colarse.
//
// 💥 Mutación: añadir un campo cualquiera a uno de los tres structs ⇒ rojo.
func TestLasEntradasDelModeloNoTienenDondeMeterUnAdjunto(t *testing.T) {
	casos := []struct {
		tipo   any
		quiero []string
	}{
		{llm.ExtractMainIdeasInput{}, []string{"SourceText"}},
		{llm.ExtractItemSpecsInput{}, []string{"SourceText", "Idea"}},
		{llm.NormalizeQuantitiesInput{}, []string{"SourceText", "Items", "MessageTS"}},
	}
	for _, c := range casos {
		tipo := reflect.TypeOf(c.tipo)
		t.Run(tipo.Name(), func(t *testing.T) {
			got := make([]string, 0, tipo.NumField())
			for i := range tipo.NumField() {
				got = append(got, tipo.Field(i).Name)
			}
			if !reflect.DeepEqual(got, c.quiero) {
				t.Errorf("%s tiene los campos %v; se esperaban %v.\n"+
					"Si el campo nuevo transporta un adjunto —o cualquier cosa derivada de "+
					"`source_refs`—, el audio acaba de dejar de estar fuera del prompt (T5.3, D-044.52).",
					tipo.Name(), got, c.quiero)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func contiene(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func algunaLlamadaContiene(ls []llamadaEspiada, s string) bool {
	for _, l := range ls {
		if strings.Contains(l.prompt, s) || strings.Contains(string(l.campos), s) {
			return true
		}
	}
	return false
}
