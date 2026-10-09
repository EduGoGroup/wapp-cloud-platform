package stages_test

// draft_revision_rules_test.go — las REGLAS de draft_revision.go (trozo de
// draft_revision_test.go): quién firma la revisión, el rastro del análisis, las
// preguntas sugeridas, el re-análisis que no pisa y la puerta única del literal (R-06).
//
// Casi todos son el mismo par —con la marca del job y sin ella—, y la mitad SIN marca es
// la que de verdad protege al pipeline normal.

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-shared/llm"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/stages"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

// ---------------------------------------------------------------------------
// QUIÉN FIRMA
// ---------------------------------------------------------------------------

// TestDraftRun_AuthorComesFromTheJob: `created_by` es un ROL y lo decide el JOB. Son dos
// paquetes con el mismo literal `"owner"` —la marca del job y el autor de la revisión—
// y se comparan las CONSTANTES. El `kind` NO cambia: un re-análisis sigue siendo una
// interpretación de la máquina.
func TestDraftRun_AuthorComesFromTheJob(t *testing.T) {
	stranger := ambarP4Job()
	stranger.Reanalysis.RequestedBy = "crm" // una marca que no es la de la dueña
	cases := []struct {
		name   string
		job    intake.ClaimedJob
		author string
	}{
		{"reanalysis asked by the owner", reanalysisJob(), intakes.RevisionByOwner},
		{"normal pipeline", ambarP4Job(), intakes.RevisionBySystem},
		{"a mark that is not the owner's", stranger, intakes.RevisionBySystem},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := newDraftBench(t, ambarNow())
			art := b.runAmbar(t, c.job)
			rev, _ := b.lastRevision(t, art.IntakeID)
			if rev.CreatedBy != c.author || rev.Kind != intakes.RevisionKindInterpreted {
				t.Fatalf("revisión firmada por %q con kind %q; se esperaba %q e interpreted", rev.CreatedBy, rev.Kind, c.author)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// EL RASTRO (`payload.analysis`)
// ---------------------------------------------------------------------------

// intPtr es el `reanalyzed_from` esperado.
func intPtr(n int) *int { return &n }

// TestDraftRun_Analysis: lo que trae el llamante NO se pisa, se COMPLETA; lo que falta
// sale del job SOLO si es un re-análisis de la dueña; `source` cae al hilo del evento; y
// la falta de vía se avisa sin tumbar nada.
func TestDraftRun_Analysis(t *testing.T) {
	noPrevious := reanalysisJob()
	noPrevious.Reanalysis.From = 0
	pasted := reanalysisJob()
	pasted.Reanalysis.Source = stages.SourcePastedText
	pasted.Reanalysis.Via = "api"
	pasted.Reanalysis.From = 4
	notOwner := reanalysisJob()
	notOwner.Reanalysis.RequestedBy = "crm"

	cases := []struct {
		name     string
		job      intake.ClaimedJob
		in       stages.Analysis
		want     stages.Analysis
		warnsVia bool
	}{
		{"normal pipeline without a via: warned, the draft lives", ambarP4Job(), stages.Analysis{},
			stages.Analysis{Source: stages.SourceEventThread}, true},
		{"normal pipeline with what the worker filled", ambarP4Job(), stages.Analysis{Provider: "api", Model: "claude-x"},
			stages.Analysis{Provider: "api", Model: "claude-x", Source: stages.SourceEventThread}, false},
		{"reanalysis: the three fields come from the job, the model from nobody", reanalysisJob(), stages.Analysis{},
			stages.Analysis{Provider: "local", Source: stages.SourceEventThread, ReanalyzedFrom: intPtr(1)}, false},
		{"reanalysis with pasted text over revision 4", pasted, stages.Analysis{},
			stages.Analysis{Provider: "api", Source: stages.SourcePastedText, ReanalyzedFrom: intPtr(4)}, false},
		{"what the caller brings wins over the job", pasted,
			stages.Analysis{Provider: "local", Model: "m", Source: stages.SourceBoth, ReanalyzedFrom: intPtr(9)},
			stages.Analysis{Provider: "local", Model: "m", Source: stages.SourceBoth, ReanalyzedFrom: intPtr(9)}, false},
		{"the caller brings half: the job completes the rest", reanalysisJob(), stages.Analysis{Provider: "api", Model: "claude-x"},
			stages.Analysis{Provider: "api", Model: "claude-x", Source: stages.SourceEventThread, ReanalyzedFrom: intPtr(1)}, false},
		{"reanalysis with no previous revision: null, never a zero", noPrevious, stages.Analysis{},
			stages.Analysis{Provider: "local", Source: stages.SourceEventThread}, false},
		{"a mark that is not the owner's completes nothing", notOwner, stages.Analysis{},
			stages.Analysis{Source: stages.SourceEventThread}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := newDraftBench(t, ambarNow())
			in := ambarDraftInput()
			in.Analysis = c.in
			art := b.run(t, c.job, in)

			_, p := b.lastRevision(t, art.IntakeID)
			if !reflect.DeepEqual(p.Analysis, c.want) {
				t.Fatalf("analysis = %+v (from %v), se esperaba %+v", p.Analysis, p.Analysis.ReanalyzedFrom, c.want)
			}
			const warning = "draft: la revisión sale SIN vía de análisis; no se podrá comparar local contra api (D-044.15)"
			if warned := strings.Contains(b.log.String(), warning); warned != c.warnsVia {
				t.Fatalf("aviso de la vía = %v, se esperaba %v:\n%s", warned, c.warnsVia, b.log.String())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// LAS PREGUNTAS SUGERIDAS
// ---------------------------------------------------------------------------

// TestDraftRun_SuggestedQuestions: se pregunta lo que SOLO el cliente puede contestar —la
// variante y la zona—, en el orden de las líneas y con los textos literales de §7.4.
func TestDraftRun_SuggestedQuestions(t *testing.T) {
	price := 3000.0
	options := []stages.VariantOption{{SKU: "X#10", Label: "10", Price: 900}, {SKU: "X#12", Label: "12", Price: 1000}}
	variant := func(label string, r *llm.Range) stages.Line {
		return stages.Line{Kind: stages.KindMatched, SKU: "X", Label: label, Qty: 1, Range: r, VariantOptions: options}
	}
	shipping := func(p *float64) stages.Line {
		return stages.Line{Kind: stages.KindShipping, SKU: intakes.ShippingSKU, Label: "Envío", Qty: 1, UnitPrice: p}
	}
	cases := []struct {
		name  string
		lines []stages.Line
		want  []string
	}{
		{"variants without a range: no numbers to quote", []stages.Line{variant("Torta de chocolate", nil)},
			[]string{"¿Cuál de las presentaciones de «Torta de chocolate» necesitas?"}},
		{"range with its unit", []stages.Line{variant("Torta", &llm.Range{Min: 10, Max: 12, Unit: "porciones"})},
			[]string{"¿Confirmas el tamaño de «Torta»: 10 o 12 porciones?"}},
		{"range without a unit: none is invented", []stages.Line{variant("Empanadas", &llm.Range{Min: 10, Max: 12})},
			[]string{"¿Confirmas el tamaño de «Empanadas»: 10 o 12?"}},
		{"a blank unit is no unit, a padded one is trimmed", []stages.Line{
			variant("A", &llm.Range{Min: 1, Max: 2, Unit: "   "}),
			variant("B", &llm.Range{Min: 1, Max: 2, Unit: "  kg "}),
		}, []string{"¿Confirmas el tamaño de «A»: 1 o 2?", "¿Confirmas el tamaño de «B»: 1 o 2 kg?"}},
		{"unpriced shipping asks for the zone", []stages.Line{shipping(nil)},
			[]string{"¿Zona de entrega para calcular el envío?"}},
		{"priced shipping is not asked: the zone was resolved", []stages.Line{shipping(&price)}, []string{}},
		{"unmatched is the owner's task, not a question", []stages.Line{
			{Kind: stages.KindUnmatched, Label: "torta de vainilla", Qty: 1},
		}, []string{}},
		{"a matched line with its price and no options asks nothing", []stages.Line{
			{Kind: stages.KindMatched, SKU: "TEQ-30", Label: "Tequeños", Qty: 1, UnitPrice: &price,
				Range: &llm.Range{Min: 1, Max: 2}},
		}, []string{}},
		{"the order is the order of the lines", []stages.Line{
			shipping(nil), variant("Z", nil), {Kind: stages.KindUnmatched, Label: "u"}, variant("A", nil),
		}, []string{
			"¿Zona de entrega para calcular el envío?",
			"¿Cuál de las presentaciones de «Z» necesitas?",
			"¿Cuál de las presentaciones de «A» necesitas?",
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := newDraftBench(t, ambarNow())
			art := b.run(t, ambarP4Job(), stages.DraftInput{Match: &stages.MatchArtifact{Version: 1, Lines: c.lines}})
			_, p := b.lastRevision(t, art.IntakeID)
			if p.SuggestedQuestions == nil || !reflect.DeepEqual(p.SuggestedQuestions, c.want) {
				t.Fatalf("preguntas = %#v\nse esperaban %#v", p.SuggestedQuestions, c.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// EL RE-ANÁLISIS NO PISA
// ---------------------------------------------------------------------------

// TestDraftRun_ReanalysisLeavesThePreviousRevisionIntact: el dueño pide re-análisis y
// aparece una revisión MÁS, sin que la anterior se toque: mismo número, mismo autor,
// mismo payload. La nueva se firma `owner`, sabe a quién sucede, y el artefacto publica
// el número REAL que le dio el store.
func TestDraftRun_ReanalysisLeavesThePreviousRevisionIntact(t *testing.T) {
	b := newDraftBench(t, ambarNow())
	const firstPayload = `{"version":1,"lines":[{"label":"hamburguesa con queso","qty":2}]}`
	first, err := b.revisions.InsertRevision(context.Background(), intakes.Revision{
		IntakeID:  ambarIntakeID,
		Kind:      intakes.RevisionKindInterpreted,
		Payload:   []byte(firstPayload),
		CreatedBy: intakes.RevisionBySystem,
	})
	if err != nil || first.RevisionNo != 1 {
		t.Fatalf("sembrar la revisión 1: %+v, %v", first, err)
	}

	art := b.runAmbar(t, reanalysisJob())

	revs := b.revisions.PersistedRevisions(art.IntakeID)
	if len(revs) != 2 {
		t.Fatalf("revisiones = %d; el re-análisis AÑADE una, no sustituye a la que había", len(revs))
	}
	if revs[0].RevisionNo != 1 || revs[0].CreatedBy != intakes.RevisionBySystem || string(revs[0].Payload) != string(first.Payload) {
		t.Fatalf("la revisión anterior se tocó: %+v", revs[0])
	}
	if revs[1].RevisionNo != 2 || revs[1].CreatedBy != intakes.RevisionByOwner || art.RevisionNo != 2 {
		t.Fatalf("la nueva = %+v (artefacto %d); se esperaba la 2, firmada por owner", revs[1], art.RevisionNo)
	}
	_, p := b.lastRevision(t, art.IntakeID)
	if p.Analysis.ReanalyzedFrom == nil || *p.Analysis.ReanalyzedFrom != 1 {
		t.Fatalf("reanalyzed_from = %v; sucede a la revisión que estaba vigente", p.Analysis.ReanalyzedFrom)
	}
}

// ---------------------------------------------------------------------------
// R-06 — LA PUERTA ÚNICA DEL LITERAL
// ---------------------------------------------------------------------------

// TestRevisionWriter_IsTheOnlyDoorForARevision fija R-06 por su mitad estructural: la
// revisión solo puede salir por el puerto de solicitudes —el store con cipher del
// literal—, porque los dos puertos del almacén de flujos no tienen por dónde. Cada
// puerto tiene EXACTAMENTE sus métodos: ampliarlos se ve aquí.
func TestRevisionWriter_IsTheOnlyDoorForARevision(t *testing.T) {
	ports := []struct {
		name    string
		port    reflect.Type
		methods []string
	}{
		{"RevisionWriter", reflect.TypeOf((*stages.RevisionWriter)(nil)).Elem(), []string{"InsertRevision"}},
		{"IntakeStore", reflect.TypeOf((*stages.IntakeStore)(nil)).Elem(), []string{"GetIntakeByEvent", "UpsertIntake"}},
		{"EventWriter", reflect.TypeOf((*stages.EventWriter)(nil)).Elem(), []string{"InsertFlowEvent"}},
	}
	for _, p := range ports {
		got := make([]string, 0, p.port.NumMethod())
		for i := range p.port.NumMethod() {
			got = append(got, p.port.Method(i).Name)
		}
		if !reflect.DeepEqual(got, p.methods) {
			t.Fatalf("métodos de %s = %v, se esperaban %v", p.name, got, p.methods)
		}
	}
	// El puerto de escritura del dominio de solicitudes cabe en el de la etapa.
	if !reflect.TypeOf((*intakes.RevisionWriter)(nil)).Elem().Implements(ports[0].port) {
		t.Fatal("intakes.RevisionWriter ya no satisface stages.RevisionWriter")
	}
}

// TestDraftRun_ClientLiteralIsNotPersistedInClear es R-06 por su mitad de conducta, sobre
// la etapa REAL y el doble del store con cipher: el payload que la etapa entrega lleva
// las claves que el store sabe sacar, así que lo GUARDADO no trae el literal.
//
// 🔴 LAS DOS MITADES, Y LA PRIMERA IMPIDE LA TAUTOLOGÍA: un barrido que solo comprueba
// «el texto no está» sale verde cuando el texto nunca llegó a escribirse. Primero se
// afirma que el literal SÍ está retenido (vuelve por la lectura); después, que NO está
// en claro en lo persistido.
//
// 🔴 QUÉ SE BARRE: el literal COMPLETO y las frases de evidencia (D-044.13). NO
// «cualquier subcadena»: la `customization` y el `label` de una línea `unmatched` son
// palabras del cliente y aun así NIVEL 1 por doctrina (ADR-0034), y se afirman abajo en
// positivo para que nadie los «arregle» cifrándolos.
func TestDraftRun_ClientLiteralIsNotPersistedInClear(t *testing.T) {
	b := newDraftBench(t, ambarNow())
	art := b.runAmbar(t, ambarP4Job())

	// (0) Lo que la etapa ENTREGA al puerto lleva el literal: quien lo saca es el store.
	delivered := string(b.spy.got[0].Payload)
	for _, key := range []string{`"source_text":`, `"evidence":`} {
		if !strings.Contains(delivered, key) {
			t.Fatalf("el payload entregado no trae %s: el store no tendría qué cifrar", key)
		}
	}

	// (1) EL TEXTO SÍ SE RETUVO: la lectura lo devuelve entero.
	_, read := b.lastRevision(t, art.IntakeID)
	if read.SourceText != ambarText {
		t.Fatal("la lectura no devuelve el literal: el barrido de abajo mediría CERO")
	}
	withEvidence := 0
	for _, l := range read.Lines {
		if l.Evidence != "" {
			withEvidence++
		}
	}
	if withEvidence != 3 {
		t.Fatalf("líneas con evidencia tras la lectura = %d, se esperaban 3", withEvidence)
	}

	// (2) Y NO ESTÁ EN CLARO EN LO PERSISTIDO.
	persisted := b.revisions.PersistedRevisions(art.IntakeID)
	if len(persisted) != 1 {
		t.Fatalf("revisiones persistidas = %d", len(persisted))
	}
	raw := string(persisted[0].Payload)
	for _, fragment := range []string{
		ambarText, "### MENSAJES DE LA CONVERSACIÓN", "Hola, buenas!", "Te quería pedir un presupuesto",
		"Me pasas precio porfa?", "Serían 2 tortas",
		chocolateCakeEvidence, vanillaCakeEvidence, tequenosEvidence, deliveryEvidence,
		`"source_text"`, `"evidence"`,
	} {
		if strings.Contains(raw, fragment) {
			t.Fatalf("literal del cliente EN CLARO en intake_revisions.payload: %q", fragment)
		}
	}

	// (3) Y LA INTERPRETACIÓN SIGUE EN CLARO: cifrar de más destruye el agregado de
	// negocio sin proteger a nadie.
	for _, fragment := range []string{
		`"lines"`, `"suggested_questions"`, "decoración infantil", "torta de vainilla con lluvia de colores",
	} {
		if !strings.Contains(raw, fragment) {
			t.Fatalf("la interpretación (nivel 1) no está en claro en lo persistido: falta %q", fragment)
		}
	}
}
