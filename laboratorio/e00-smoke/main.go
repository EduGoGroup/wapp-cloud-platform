//go:build laboratorio

// e00-smoke es la prueba de humo del andamio del laboratorio (Plan 058 · Ola 1): pide UNA inferencia a través
// del llmvia.Selector REAL —prompt real de wapp-shared/llm, techo y plazo reales de la vía local,
// ExtractJSON y Parse… reales— y la contesta el frame que elijas. Si esto sale, el andamio está sano.
//
//	GOTOOLCHAIN=go1.26.5 GOWORK=off go run -tags laboratorio ./laboratorio/e00-smoke -frame script
//	GOTOOLCHAIN=go1.26.5 GOWORK=off go run -tags laboratorio ./laboratorio/e00-smoke -frame ollama
//	… -stage p2                       (P2 sobre la ráfaga del caso Ámbar)
//	… -phrase "¿a qué hora cierran?"   (otra frase para P1)
//
// Sale con rc=0 si la inferencia viajó y su salida pasó el validador de la etapa; rc=1 si no.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/EduGoGroup/wapp-shared/llm"

	"github.com/EduGoGroup/wapp-cloud-platform/laboratorio/common"
)

const defaultPhrase = "quiero encargar 2 tortas de chocolate y un paquete de 30 tequeños para el sábado"

func main() {
	flags := common.RegisterFlags(nil)
	stage := flag.String("stage", "p1", "etapa a pedir: p1 (clasificar una frase) o p2 (ideas de la ráfaga Ámbar)")
	phrase := flag.String("phrase", defaultPhrase, "frase del cliente para P1")
	flag.Parse()
	if err := run(flags, common.Stage(*stage), *phrase); err != nil {
		os.Exit(1)
	}
}

func run(flags *common.Flags, stage common.Stage, phrase string) error {
	n := common.NewNarrator(nil)
	n.Title("E00 · Humo del andamio",
		"Una inferencia por el selector real de la vía local; solo el viaje al Edge es del laboratorio.",
		"Frame: "+flags.Describe())
	ctx := context.Background()

	n.Step("Se arma el frame")
	spy, err := common.FrameFromFlags(flags)
	if err != nil {
		n.Fail(err, "banderas")
		return err
	}
	switch f := spy.Inner().(type) {
	case *common.ScriptFrame:
		n.Note("ScriptFrame reconoce la etapa por el rótulo del constructor del prompt y contesta el JSON del caso Ámbar.")
	case *common.OllamaFrame:
		think, err := f.ModelThinks(ctx)
		if err != nil {
			n.Fail(err, "Ollama no contesta a /api/show")
			return err
		}
		n.Note("OllamaFrame hace POST %s/api/chat como el cajero del Edge.", flags.OllamaURL)
		if think {
			n.OK("%s declara «thinking»: se mandará think:false (si no, piensa en voz alta y no cabe en el techo).", f.Model())
		} else {
			n.Note("%s no declara «thinking»: el campo think no viaja (mandarlo sería un error de Ollama).", f.Model())
		}
	}

	n.Step("Se arma el selector real y se pide el provider del tenant")
	sel, err := common.NewSelector(spy, common.NewLog(flags.Verbose))
	if err != nil {
		n.Fail(err, "llmvia.NewSelector")
		return err
	}
	prov, err := sel.For(ctx, common.LabTenant, "")
	if err != nil {
		n.Fail(err, "Selector.For")
		return err
	}
	n.Note("La configuración LLM está vacía: sin fila, la vía es LOCAL (REQ-33). Provider: %T.", prov)

	var raw json.RawMessage
	var validate func(json.RawMessage) (any, error)
	switch stage {
	case common.StageP1:
		n.Step("P1 · ClassifyRequest")
		in := common.AmberP1Input(phrase)
		n.Show("frase", phrase)
		n.Note("Catálogo: %s, más la etiqueta reservada %q.", intentNames(in.Catalog), in.UnknownLabel)
		raw, err = prov.ClassifyRequest(ctx, in, llm.Options{Temperature: llm.TemperatureGreedy})
		validate = func(r json.RawMessage) (any, error) { return llm.ParseClassification(r, in) }
	case common.StageP2:
		n.Step("P2 · ExtractMainIdeas")
		in := llm.ExtractMainIdeasInput{SourceText: strings.Join(common.AmberBurst(), "\n")}
		n.Show("hilo", in.SourceText)
		raw, err = prov.ExtractMainIdeas(ctx, in, llm.Options{Temperature: llm.TemperatureGreedy})
		validate = func(r json.RawMessage) (any, error) { return llm.ParseMainIdeas(r) }
	default:
		err = fmt.Errorf("-stage %q: vale p1 o p2", stage)
		n.Fail(err, "banderas")
		return err
	}
	trip, calls := spy.Last()
	if calls > 0 {
		req := trip.Request
		n.Note("Viajó por el cable: etapa reconocida %q · class %q · techo %d tokens · format %q · temperatura %.1f · plazo %s · prompt de %d bytes.",
			trip.Stage, req.Class, req.MaxOutputTokens, req.Format, req.Temperature, req.Timeout, len(req.Prompt))
		n.Raw("salida cruda del modelo", trip.Output)
		n.Note("Tardó %s de pared.", trip.Duration.Round(1e6))
		if f, ok := spy.Inner().(*common.OllamaFrame); ok {
			n.Note("Desglose de Ollama: %s.", f.Last())
		}
	}
	if err != nil {
		n.Fail(err, "la inferencia falló")
		return err
	}

	n.Step("Validación con el Parse… de la etapa (wapp-shared/llm)")
	art, err := validate(raw)
	if err != nil {
		n.Fail(err, "el artefacto NO pasa el validador")
		n.Note("En producción el llamante reintentaría UNA vez a temperatura %.1f (llm.TemperatureRetry).", llm.TemperatureRetry)
		return err
	}
	n.Show("artefacto validado", art)
	if c, ok := art.(*llm.Classification); ok {
		n.OK("intención %q con confianza %.2f; evidencia %q.", c.Intent, c.Confidence, c.Evidence)
		n.Note("El adelanto de ventana solo cierra con %q y confianza ≥ 0,7 (lo estudia E12).", common.IntentIntakeRequest)
	}
	if m, ok := art.(*llm.MainIdeas); ok {
		n.OK("%d ideas extraídas.", len(m.Wants))
	}
	n.Step("Fin")
	n.OK("El andamio funciona: frame → local.Provider → ExtractJSON → Parse.")
	return nil
}

func intentNames(specs []llm.IntentSpec) string {
	names := make([]string, 0, len(specs))
	for _, s := range specs {
		names = append(names, s.Name)
	}
	return strings.Join(names, ", ")
}
