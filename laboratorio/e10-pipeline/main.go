//go:build laboratorio

// e10-pipeline es el escalón E10 del laboratorio (Plan 058 · Bloque C): EL PIPELINE DE CAPTACIÓN,
// etapa por etapa, con texto directo.
//
// Toma una frase (o una ráfaga) del banco E03 y la pasa por las CINCO etapas REALES del pipeline
// —P2 (ideas) → P3 (especificación por ítem) → P4 (cantidades y fecha) → Match (catálogo) → Draft
// (borrador)— construidas como las construye el arranque. Antes, como referencia, pide P1 (la
// intención) igual que el adelanto de ventana. Al final compara con lo que el banco espera.
//
//   - REAL: las cinco etapas, sus prompts (wapp-shared/llm), el parseo, el anclaje de evidencias, la
//     aritmética de fechas de Go, la cascada del match y el índice del catálogo Ámbar.
//
//   - DOBLE: la máquina de intake_jobs (intakehelpertest.MachineMemory), los almacenes del borrador
//     (en memoria) y el viaje al Edge (el frame: guion u Ollama).
//
//   - FUERA: la ventana (E09) y el worker que encadena y reintenta (E11). Aquí las etapas se llaman a
//     mano; los reintentos de calidad del worker se IMITAN (-attempts), con su misma temperatura.
//
//     GOTOOLCHAIN=go1.26.5 GOWORK=off go run -tags laboratorio ./laboratorio/e10-pipeline
//     … -phrase e02-long -frame ollama       (un caso del banco, narrado paso a paso)
//     … -all -frame script                   (el banco entero: tabla de aciertos)
//     … -all -frame ollama                   (lo mismo con Gemma)
//     … -prompts /tmp/prompts-wapp           (prompts editados, como WAPP_LLM_PROMPTS_DIR)
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake/intakehelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/pipeline"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo/indice"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/llmvia"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/llmvia/local"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/prompts"

	"github.com/EduGoGroup/wapp-cloud-platform/laboratorio/common"
)

type options struct {
	flags    *common.Flags
	phraseID string
	all      bool
	bank     string
	prompts  string
	attempts int
	today    string
	bare     bool
}

func main() {
	o := options{flags: common.RegisterFlags(nil)}
	flag.StringVar(&o.phraseID, "phrase", "amber-burst", "id del caso del banco E03 que se narra paso a paso")
	flag.BoolVar(&o.all, "all", false, "pasa el banco entero y saca la tabla de aciertos")
	flag.StringVar(&o.bank, "bank", common.DefaultPhrasesPath, "ruta del banco de frases")
	flag.StringVar(&o.prompts, "prompts", "", "directorio de prompts editados (lo mismo que WAPP_LLM_PROMPTS_DIR); vacío = los compilados")
	flag.IntVar(&o.attempts, "attempts", pipeline.DefaultMaxQualityAttempts, "intentos por etapa ante un fallo de CALIDAD, como el worker (3 de fábrica)")
	flag.StringVar(&o.today, "today", "", "día de la prueba AAAA-MM-DD (base de las fechas relativas); vacío = hoy")
	flag.BoolVar(&o.bare, "bare", false, "pasa a las etapas el texto PELADO, sin el formato del compositor (como debug_inferencia en E02)")
	flag.Parse()
	if err := run(o); err != nil {
		os.Exit(1)
	}
}

// lab es el montaje compartido por todos los casos.
type lab struct {
	n        *common.Narrator
	spy      *common.SpyFrame
	sel      *llmvia.Selector
	machine  *intakehelpertest.MachineMemory
	stages   *common.IntakeStages
	index    *indice.Indice
	base     time.Time
	attempts int
	verbose  bool
	bare     bool
}

func run(o options) error {
	n := common.NewNarrator(nil)
	n.Title("E10 · El pipeline, etapa por etapa",
		"P2 → P3 → P4 → Match → Draft reales sobre una frase del banco E03; P1 delante, como referencia.",
		"Frame: "+o.flags.Describe())
	phrases, err := common.LoadPhrases(o.bank)
	if err != nil {
		n.Fail(err, "banco de frases")
		return err
	}
	l, err := setup(n, o)
	if err != nil {
		return err
	}
	if o.all {
		return l.runAll(phrases)
	}
	p, ok := common.FindPhrase(phrases, o.phraseID)
	if !ok {
		err := fmt.Errorf("-phrase %q no está en el banco", o.phraseID)
		n.Fail(err, "banderas")
		return err
	}
	l.verbose = true
	res := l.runPhrase(context.Background(), p)
	l.verdict(p, res)
	return nil
}

func setup(n *common.Narrator, o options) (*lab, error) {
	n.Step("El montaje")
	spy, err := common.FrameFromFlags(o.flags)
	if err != nil {
		n.Fail(err, "banderas")
		return nil, err
	}
	if f, ok := spy.Inner().(*common.OllamaFrame); ok {
		if _, err := f.ModelThinks(context.Background()); err != nil {
			n.Fail(err, "Ollama no contesta")
			return nil, err
		}
	}
	var selOpts []llmvia.SelectorOption
	if o.prompts != "" {
		loaded, err := prompts.Cargar(o.prompts)
		if err != nil {
			n.Fail(err, "prompts.Cargar(%s)", o.prompts)
			return nil, err
		}
		selOpts = append(selOpts, llmvia.WithLocalOptions(local.ConPlantillas(loaded.Plantillas)))
		for stage, origin := range loaded.Origen {
			n.Note("Prompt %s: %s", stage, origin)
		}
		n.Note("Es la palanca WAPP_LLM_PROMPTS_DIR: el arranque llama a prompts.Cargar y pasa el resultado al selector")
		n.Note("  con llmvia.WithLocalOptions(local.ConPlantillas(…)). P1 no está aquí: lo gobierna el catálogo de intenciones.")
	}
	log := common.NewLog(o.flags.Verbose)
	sel, err := common.NewSelector(spy, log, selOpts...)
	if err != nil {
		n.Fail(err, "selector")
		return nil, err
	}
	machine := intakehelpertest.NewMachineMemory(nil)
	st, err := common.NewIntakeStages(log, sel, machine, nil)
	if err != nil {
		n.Fail(err, "etapas")
		return nil, err
	}
	idx, err := common.AmberIndex()
	if err != nil {
		n.Fail(err, "catálogo")
		return nil, err
	}
	base, err := baseDay(o.today)
	if err != nil {
		n.Fail(err, "-today")
		return nil, err
	}
	n.Note("Cinco etapas reales armadas como en internal/arranque/fase5_captacion.go: 48 s por llamada, P4 en UTC, match sin zona gris.")
	n.Note("Catálogo Ámbar indexado (%d artículos) por catalogo.ParseCatalog + indice.Construir, como la caché de producción.", idx.Articulos())
	n.Note("message_ts de cada job: %s. Es la base de las fechas (D-044.9) y el «día de la prueba» del banco.", base.Format(time.RFC3339))
	n.Note("Reintentos por calidad: hasta %d intentos por etapa, como el worker. Cada intento a temperatura %.1f (la etapa no la sube).", o.attempts, 0.0)
	if o.bare {
		n.Note("-bare: las etapas reciben el texto PELADO (los mensajes unidos por saltos de línea), sin cabeceras ni")
		n.Note("  «cliente:». Es lo que manda debug_inferencia (E02), NO lo que manda producción.")
	}
	return &lab{n: n, spy: spy, sel: sel, machine: machine, stages: st, index: idx, base: base, attempts: o.attempts, bare: o.bare}, nil
}

// baseDay es el instante de los mensajes: el día pedido (o el de hoy en la hora local de esta
// máquina) a las 10:00 UTC, que es la zona en la que trabaja P4 (stages.DefaultZone).
func baseDay(today string) (time.Time, error) {
	d := time.Now()
	if today != "" {
		var err error
		if d, err = time.Parse(time.DateOnly, today); err != nil {
			return time.Time{}, fmt.Errorf("-today %q: formato AAAA-MM-DD", today)
		}
	}
	return time.Date(d.Year(), d.Month(), d.Day(), 10, 0, 0, 0, time.UTC), nil
}
