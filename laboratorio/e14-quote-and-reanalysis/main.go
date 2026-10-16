//go:build laboratorio

// e14-quote-and-reanalysis es el escalón E14 del laboratorio (Plan 058 · Bloque D): P5 Y
// RE-ANÁLISIS, las dos puertas de la bandeja que SÍ llaman al modelo.
//
//  1. La sugerencia de cotización (quotetext.NewService): el modelo redacta con la voz de la dueña
//     —P5— y el verificador de precios (INV-2) decide si ese texto se puede mandar o si sale el
//     determinista. Se mide lo que tarda Gemma.
//
//  2. El re-análisis (reanalisis.NewService): la dueña pide reinterpretar; la puerta compone y
//     sella el literal, abre un job NUEVO sobre el mismo evento y el worker lo recorre hasta una
//     revisión nueva de la MISMA solicitud.
//
//     - REAL: los dos servicios, el selector y los prompts, el verificador, el compositor, el worker,
//     las cinco etapas, intakes.Service.
//     - DOBLE: la inferencia (-frame), los almacenes, el lector de la semilla de estilo
//     (tenant_content), los derechos, la config LLM del tenant (vacía ⇒ vía local) y el reloj.
//     - COSTURA: common.IntakeTable, que además responde ReanalysisTargetOf (intakes.MemoryStore no
//     lo implementa; solo *intakes.Postgres).
//
//     GOTOOLCHAIN=go1.26.5 GOWORK=off go run -tags laboratorio ./laboratorio/e14-quote-and-reanalysis
//     … -frame hybrid                    (P5 con Gemma; P2–P4 por guion para que el re-análisis no muera)
//     … -frame hybrid -p5-runs 5         (cinco P5 para medir el tiempo de Gemma)
//     … -p5-script swap                  (con guion: precios cruzados; el verificador los para)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"

	"github.com/EduGoGroup/wapp-cloud-platform/laboratorio/common"
)

const (
	session  = "lab-session-e14"
	phoneAna = "56 9 0000 0014"
)

var errUnexpected = errors.New("e14: el montaje no se comportó como se esperaba")

// Lo que contesta el GUION en P5, según -p5-script. Los importes de good son los del presupuesto
// corregido, en el orden del borrador: 2100, 3900, 490 y el total 6490.
var p5Scripts = map[string]string{
	"good": "¡Hola! Te paso el presupuesto: torta de chocolate de 10 porciones $2100, torta de vainilla $3900 " +
		"y un paquete de 30 tequeños $490. Total $6490. ¡Quedo atenta!",
	"swap": "¡Hola! Te paso el presupuesto: torta de chocolate de 10 porciones $3900, torta de vainilla $2100 " +
		"y un paquete de 30 tequeños $490. Total $6490.",
	"amber": common.AmberQuoteText,
}

// lab es el montaje del escalón.
type lab struct {
	n        *common.Narrator
	ctx      context.Context
	box      *common.Inbox
	spy      *common.SpyFrame
	flags    *common.Flags
	runs     int
	p5Kind   string
	anaEvent string
}

func main() {
	flags := common.RegisterFlags(nil)
	runs := flag.Int("p5-runs", 3, "cuántas sugerencias P5 pedir con ejemplos (para medir el tiempo del modelo)")
	p5Kind := flag.String("p5-script", "good", "qué contesta el GUION en P5: good (importes en su sitio), swap (precios cruzados) o amber (sin importes)")
	flag.Parse()
	if _, ok := p5Scripts[*p5Kind]; !ok {
		fmt.Fprintf(os.Stderr, "-p5-script %q: vale good, swap o amber\n", *p5Kind)
		os.Exit(2)
	}
	l := &lab{n: common.NewNarrator(nil), ctx: context.Background(), flags: flags, runs: max(*runs, 1), p5Kind: *p5Kind}
	if err := l.run(); err != nil {
		os.Exit(1)
	}
}

func (l *lab) run() error {
	l.n.Title("E14 · P5 y re-análisis",
		"La sugerencia de cotización con la voz de la dueña, y la reinterpretación de una solicitud a petición suya.",
		"Frame: "+l.flags.Describe())
	if err := l.setup(); err != nil {
		return err
	}
	id, err := l.draftAndCorrect()
	if err != nil {
		return err
	}
	if err := l.suggestWithoutExamples(id); err != nil {
		return err
	}
	if err := l.suggestWithExamples(id); err != nil {
		return err
	}
	if err := l.reanalyze(id); err != nil {
		return err
	}
	if h, ok := l.spy.Inner().(*common.HybridFrame); ok {
		l.n.Note("-frame hybrid · quién contestó: %s.", h.Tally())
	}
	return nil
}

func (l *lab) setup() error {
	l.n.Step("El montaje")
	var err error
	if l.spy, err = common.FrameFromFlags(l.flags); err != nil {
		l.n.Fail(err, "banderas")
		return err
	}
	if s := l.script(); s != nil {
		s.Respond(common.StageP5, common.P5Reply(p5Scripts[l.p5Kind]))
		l.n.Note("El guion contestará en P5 la variante %q: %q.", l.p5Kind, p5Scripts[l.p5Kind])
	}
	if l.box, err = common.NewInbox(l.spy, l.flags.Verbose); err != nil {
		l.n.Fail(err, "bandeja")
		return err
	}
	l.n.Note("La bandeja de E13 (common.NewInbox) más los dos servicios de este escalón, cableados como en")
	l.n.Note("  internal/arranque/fase5_captacion.go: el MISMO selector que las etapas, y el plazo de P5 = %s.", quoteTimeout)
	return nil
}

// script es el guion que contesta P5 si P5 va por el guion: el frame entero con -frame script, o
// el de dentro del híbrido si P5 está en -script-stages. nil si P5 va al modelo.
func (l *lab) script() *common.ScriptFrame {
	switch f := l.spy.Inner().(type) {
	case *common.ScriptFrame:
		return f
	case *common.HybridFrame:
		for _, s := range f.Scripted() {
			if s == common.StageP5 {
				return f.Script()
			}
		}
	}
	return nil
}

// model es el frame de Ollama si P5 va al modelo; nil si va al guion.
func (l *lab) model() *common.OllamaFrame {
	switch f := l.spy.Inner().(type) {
	case *common.OllamaFrame:
		return f
	case *common.HybridFrame:
		if l.script() == nil {
			return f.Model()
		}
	}
	return nil
}

// draftAndCorrect deja el presupuesto en el punto en que se puede sugerir: el borrador del pipeline
// y la corrección de la dueña con precios (E13, deprisa).
func (l *lab) draftAndCorrect() (string, error) {
	l.n.Step("El presupuesto: borrador del pipeline + corrección de la dueña (E13, deprisa)")
	contactID, err := l.box.NewCustomer(l.ctx, phoneAna, "Ana")
	if err != nil {
		l.n.Fail(err, "contacto")
		return "", err
	}
	d, err := l.box.DraftFromMessages(l.ctx, session, contactID, common.AmberBurst(), nil)
	if err != nil {
		l.n.Fail(err, "pipeline")
		return "", err
	}
	if d.IntakeID == "" {
		l.n.Fail(nil, "el job murió %q sin borrador: %s", d.Row.Status, d.Row.Error)
		l.n.Note("Con -frame ollama es lo esperable (E10: Gemma tumba P2). Usa -frame hybrid para seguir.")
		return "", errUnexpected
	}
	l.n.OK("Borrador %s · revisión 1 interpreted.", d.IntakeID[:8])
	l.showIsolated(d.Row)
	l.box.Clock.Advance(10 * time.Minute)
	det, err := l.box.Service.ReplaceItems(l.ctx, common.LabTenant, d.IntakeID, correctedItems(), intakes.EditAsCorrection)
	if err != nil {
		l.n.Fail(err, "ReplaceItems")
		return "", err
	}
	l.n.OK("ReplaceItems: %d líneas, total %.2f · revisión 2 corrected.", len(det.Items), det.Total)
	l.anaEvent = d.EventID
	return d.IntakeID, nil
}

// correctedItems son las líneas que pone la dueña: las del catálogo, con la de chocolate en 10
// porciones.
func correctedItems() []intakes.Item {
	return []intakes.Item{
		{SKU: "TORTA-CHOC", Label: "Torta de chocolate 10 porciones", Customization: "decoración infantil", Qty: 1, UnitPrice: 2100},
		{SKU: "TORTA-VAIN", Label: "Torta de vainilla", Customization: "lluvia de colores", Qty: 1, UnitPrice: 3900},
		{SKU: "TEQ-30", Label: "Tequeños congelados (paquete de 30)", Qty: 1, UnitPrice: 490},
	}
}
