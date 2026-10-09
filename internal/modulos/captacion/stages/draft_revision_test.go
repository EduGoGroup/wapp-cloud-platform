package stages_test

// draft_revision_test.go — el contrato de draft_revision.go: el payload §7.4 del caso
// Ambar, sus claves, las líneas, los adjuntos y los avisos. Quién firma, el rastro del
// análisis, las preguntas y la puerta única del literal (R-06) están en
// draft_revision_rules_test.go.

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/anclaje"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/stages"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

// El doble del store con cipher del literal satisface el puerto de la etapa, igual que
// `*intakes.Postgres` en producción: comprobado en compilación.
var _ stages.RevisionWriter = (*intakes.MemoryStore)(nil)

// TestDraftRun_Ambar_RevisionOneInterpretedBySystem es el criterio del plan: la revisión
// 1, `interpreted`, firmada por el sistema, con la cabecera del contrato §7.4 dentro.
//
// 🔴 CADA ASERCIÓN MIRA EL CONTENIDO, NO EL RECUENTO: «cuatro líneas» lo cumple también
// un payload con cuatro renglones vacíos (las líneas, en el test siguiente).
func TestDraftRun_Ambar_RevisionOneInterpretedBySystem(t *testing.T) {
	b := newDraftBench(t, ambarNow())
	art := b.runAmbar(t, ambarP4Job())

	if len(b.spy.got) != 1 {
		t.Fatalf("escrituras de revisión = %d, UNA por pasada", len(b.spy.got))
	}
	rev, p := b.lastRevision(t, art.IntakeID)
	type meta struct {
		intake, kind, author, rendered string
		no, artifactNo                 int
	}
	got := meta{rev.IntakeID, rev.Kind, rev.CreatedBy, rev.RenderedText, rev.RevisionNo, art.RevisionNo}
	if want := (meta{ambarIntakeID, intakes.RevisionKindInterpreted, intakes.RevisionBySystem, "", 1, 1}); got != want {
		t.Fatalf("revisión = %+v; se esperaba la 1, interpreted, de system y sin texto mandado al cliente", got)
	}

	if p.Version != 1 || intakes.RevisionPayloadVersion != 1 {
		t.Fatalf("version = %d", p.Version)
	}
	if p.SourceText != ambarText {
		t.Fatal("el original no viaja entero: el dueño lo compara con la interpretación (§7.6)")
	}
	if !p.MessageTS.Equal(ambarMessageTS) {
		t.Fatalf("message_ts = %v; es el del PRIMER mensaje, no el de la creación", p.MessageTS)
	}
	if p.DeliveryDate != "2026-07-22" {
		t.Fatalf("delivery_date = %q", p.DeliveryDate)
	}
	wantAnalysis := stages.Analysis{Provider: "api", Model: "claude-x", Source: stages.SourceEventThread}
	if !reflect.DeepEqual(p.Analysis, wantAnalysis) {
		t.Fatalf("analysis = %+v, se esperaba %+v", p.Analysis, wantAnalysis)
	}
	if len(p.MediaRefs) != 0 || len(p.Warnings) != 0 {
		t.Fatalf("el caso no trae adjuntos ni avisos: %+v %+v", p.MediaRefs, p.Warnings)
	}
}

// TestDraftRun_Ambar_FourLinesAndTwoQuestions es la otra mitad del criterio: las cuatro
// líneas son las del match, tal cual y sin adjuntos, y las dos preguntas llevan su texto
// exacto.
func TestDraftRun_Ambar_FourLinesAndTwoQuestions(t *testing.T) {
	b := newDraftBench(t, ambarNow())
	art := b.runAmbar(t, ambarP4Job())
	_, p := b.lastRevision(t, art.IntakeID)

	want := ambarMatch().Lines
	if len(p.Lines) != 4 {
		t.Fatalf("líneas = %d", len(p.Lines))
	}
	for i, l := range p.Lines {
		if !reflect.DeepEqual(l.Line, want[i]) || len(l.MediaRefs) != 0 {
			t.Fatalf("la línea %d llegó cambiada al borrador:\n%+v\n%+v", i, l, want[i])
		}
	}
	if p.Lines[0].UnitPrice != nil || len(p.Lines[0].VariantOptions) != 2 || p.Lines[2].UnitPrice == nil || *p.Lines[2].UnitPrice != 490 {
		t.Fatalf("los precios no sobrevivieron al viaje: %+v", p.Lines)
	}

	// Las dos preguntas, con su texto exacto: lo que SOLO el cliente puede contestar.
	wantQuestions := []string{
		"¿Confirmas el tamaño de «Torta chocolate húmedo + crema choc.»: 10 o 12 porciones?",
		"¿Zona de entrega para calcular el envío?",
	}
	if !reflect.DeepEqual(p.SuggestedQuestions, wantQuestions) {
		t.Fatalf("preguntas = %q\nse esperaban %q", p.SuggestedQuestions, wantQuestions)
	}
}

// TestRevisionPayload_JSONKeysAreTheContract fija las etiquetas JSON de §7.4 con un
// payload escrito a mano: las claves, su orden, el `null` de `reanalyzed_from`, el
// `unit_price` nulo, y las claves aplanadas de la línea embebida junto a `media_refs`.
func TestRevisionPayload_JSONKeysAreTheContract(t *testing.T) {
	from := 2
	p := stages.RevisionPayload{
		Version:      1,
		SourceText:   "hola",
		MessageTS:    time.Date(2026, 7, 13, 12, 55, 0, 0, time.UTC),
		Analysis:     stages.Analysis{Provider: "local", Model: "m", Source: stages.SourceBoth, ReanalyzedFrom: &from},
		DeliveryDate: "2026-07-22",
		MediaRefs:    []anclaje.MediaRef{{Ref: "a.ogg", Kind: anclaje.KindAudio, Label: "audio"}},
		Lines: []stages.RevisionLine{{
			Line:      stages.Line{Kind: stages.KindUnmatched, Label: "L", Qty: 2, Evidence: "ev"},
			MediaRefs: []anclaje.MediaRef{{Ref: "f.jpg", Kind: anclaje.KindImage}},
		}},
		SuggestedQuestions: []string{"q"},
		Warnings:           []stages.Warning{{ItemPos: 3, Reason: stages.WarningInvalidQty}},
	}
	const golden = `{"version":1,"source_text":"hola","message_ts":"2026-07-13T12:55:00Z",` +
		`"analysis":{"provider":"local","model":"m","source":"both","reanalyzed_from":2},` +
		`"delivery_date":"2026-07-22","media_refs":[{"ref":"a.ogg","kind":"audio","label":"audio"}],` +
		`"lines":[{"kind":"unmatched","label":"L","qty":2,"unit_price":null,"evidence":"ev",` +
		`"media_refs":[{"ref":"f.jpg","kind":"image"}]}],` +
		`"suggested_questions":["q"],"warnings":[{"item_pos":3,"reason":"cantidad_invalida"}]}`
	raw, err := json.Marshal(p)
	if err != nil || string(raw) != golden {
		t.Fatalf("payload = %s (%v)\nse esperaba %s", raw, err, golden)
	}

	// Lo que se omite y lo que NO: la primera lectura dice `"reanalyzed_from":null`.
	const minimal = `{"version":0,"message_ts":"0001-01-01T00:00:00Z",` +
		`"analysis":{"provider":"","source":"","reanalyzed_from":null},"lines":null,"suggested_questions":null}`
	raw, err = json.Marshal(stages.RevisionPayload{})
	if err != nil || string(raw) != minimal {
		t.Fatalf("payload vacío = %s (%v)\nse esperaba %s", raw, err, minimal)
	}
}

// TestSourceVocabulary_ValuesAreLiteral: los tres valores de `analysis.source` viajan en
// el payload y en `intake_reanalyzed`; no se tocan.
func TestSourceVocabulary_ValuesAreLiteral(t *testing.T) {
	got := []string{stages.SourceEventThread, stages.SourcePastedText, stages.SourceBoth}
	if want := []string{"event_thread", "pasted_text", "both"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("vocabulario de source = %q, se esperaba %q", got, want)
	}
}

// TestDraftRun_LinesAreCopiedAsIs: el borrador no decide cuántas líneas hay —eso lo
// decidió el match— y su trabajo es no perder ninguna ni cambiarles nada. Con una quinta
// línea (el añadido facturable) salen CINCO, en su orden.
func TestDraftRun_LinesAreCopiedAsIs(t *testing.T) {
	decoPrice := 800.0
	in := ambarDraftInput()
	deco := stages.Line{
		Kind: stages.KindMatched, SKU: "DECO-INF", Label: "Decoración infantil", Qty: 1, UnitPrice: &decoPrice,
		Match: &stages.MatchProvenance{Strategy: "ngrama", Confidence: 1}, Evidence: chocolateCakeEvidence,
	}
	lines := in.Match.Lines
	in.Match.Lines = append([]stages.Line{lines[0], deco}, lines[1:]...)

	b := newDraftBench(t, ambarNow())
	art := b.run(t, ambarP4Job(), in)

	_, p := b.lastRevision(t, art.IntakeID)
	if len(p.Lines) != 5 || art.Lines != 5 {
		t.Fatalf("líneas = %d (artefacto %d), se esperaban 5", len(p.Lines), art.Lines)
	}
	for i, l := range p.Lines {
		if !reflect.DeepEqual(l.Line, in.Match.Lines[i]) {
			t.Fatalf("la línea %d llegó cambiada:\n%+v\n%+v", i, l.Line, in.Match.Lines[i])
		}
	}
}

// TestDraftRun_EmptyMatchIsALegitEmptyDraft: un artefacto con CERO líneas no es
// ErrNoMatch: es un borrador vacío, y sus listas se serializan `[]`, no `null`.
func TestDraftRun_EmptyMatchIsALegitEmptyDraft(t *testing.T) {
	b := newDraftBench(t, ambarNow())
	art := b.run(t, ambarP4Job(), stages.DraftInput{Match: &stages.MatchArtifact{Version: llm.ArtifactVersion}})

	if art.Lines != 0 || art.RevisionNo != 1 {
		t.Fatalf("artefacto = %+v", *art)
	}
	raw := string(b.spy.got[0].Payload)
	for _, fragment := range []string{`"lines":[]`, `"suggested_questions":[]`, `"reanalyzed_from":null`} {
		if !strings.Contains(raw, fragment) {
			t.Fatalf("el payload del borrador vacío no trae %s: %s", fragment, raw)
		}
	}
	for _, absent := range []string{`"source_text"`, `"delivery_date"`, `"media_refs"`, `"warnings"`} {
		if strings.Contains(raw, absent) {
			t.Fatalf("el payload del borrador vacío trae %s y es omitempty: %s", absent, raw)
		}
	}
}

// TestDraftRun_MediaIsNeitherLostNorDuplicated: el reparto entra por líneas y por
// cabecera y se copia sin inventar. Un adjunto anclado a una línea que NO EXISTE sube a
// la cabecera, detrás de los que ya estaban: perder un audio del cliente es peor que
// enseñarlo en el sitio genérico.
func TestDraftRun_MediaIsNeitherLostNorDuplicated(t *testing.T) {
	photo := anclaje.MediaRef{Ref: "wapp/foto-torta.jpg", Kind: anclaje.KindImage}
	orphan := anclaje.MediaRef{Ref: "wapp/huerfana.jpg", Kind: anclaje.KindImage}
	audio := anclaje.MediaRef{Ref: "wapp/audio1.ogg", Kind: anclaje.KindAudio, Label: anclaje.AudioLabel}

	for _, orphanIdx := range []int{99, 4, -1} {
		b := newDraftBench(t, ambarNow())
		in := ambarDraftInput()
		in.Media = anclaje.Distribution{
			ByLine:  map[int][]anclaje.MediaRef{0: {photo}, orphanIdx: {orphan}},
			Request: []anclaje.MediaRef{audio},
		}
		art := b.run(t, ambarP4Job(), in)
		_, p := b.lastRevision(t, art.IntakeID)

		if !reflect.DeepEqual(p.Lines[0].MediaRefs, []anclaje.MediaRef{photo}) {
			t.Fatalf("índice %d: adjuntos de la línea 0 = %+v", orphanIdx, p.Lines[0].MediaRefs)
		}
		total := len(p.MediaRefs)
		for i, l := range p.Lines {
			total += len(l.MediaRefs)
			if i > 0 && len(l.MediaRefs) != 0 {
				t.Fatalf("índice %d: a la línea %d le aparecieron adjuntos: %+v", orphanIdx, i, l.MediaRefs)
			}
		}
		if !reflect.DeepEqual(p.MediaRefs, []anclaje.MediaRef{audio, orphan}) {
			t.Fatalf("índice %d: cabecera = %+v; la huérfana sube detrás de las que ya estaban", orphanIdx, p.MediaRefs)
		}
		if total != 3 {
			t.Fatalf("índice %d: entraron tres refs y salen %d", orphanIdx, total)
		}
		b.assertLogHas(t, "draft: adjuntos anclados a una línea que no existe; suben a la cabecera")
		b.assertLogLacks(t, "huerfana")
	}

	// Sin huérfanas no hay aviso.
	b := newDraftBench(t, ambarNow())
	in := ambarDraftInput()
	in.Media = anclaje.Distribution{ByLine: map[int][]anclaje.MediaRef{3: {photo}}}
	art := b.run(t, ambarP4Job(), in)
	_, p := b.lastRevision(t, art.IntakeID)
	if !reflect.DeepEqual(p.Lines[3].MediaRefs, []anclaje.MediaRef{photo}) || len(p.MediaRefs) != 0 {
		t.Fatalf("la última línea es una línea más: %+v / cabecera %+v", p.Lines[3].MediaRefs, p.MediaRefs)
	}
	b.assertLogLacks(t, "una línea que no existe")
}

// TestDraftRun_DegradedItemsTravelAsWarningsAndTheDraftLives (DEUDA-044.16): un ítem
// malo NO tira el borrador. Llega como línea más su aviso, el aviso viaja en la revisión
// —que es lo que lee la bandeja— y no quita ni añade líneas.
func TestDraftRun_DegradedItemsTravelAsWarningsAndTheDraftLives(t *testing.T) {
	b := newDraftBench(t, ambarNow())
	in := ambarDraftInput()
	warnings := []stages.Warning{
		{ItemPos: 1, Reason: stages.WarningRangeWithoutVariant},
		{ItemPos: 2, Reason: stages.WarningInvalidQty},
		{ItemPos: 5, Reason: stages.WarningNoProduct},
	}
	in.Match.Warnings = warnings
	in.Match.Lines[2].Qty = -3 // el ítem degradado conserva su cantidad inválida

	art := b.run(t, ambarP4Job(), in)

	_, p := b.lastRevision(t, art.IntakeID)
	if !reflect.DeepEqual(p.Warnings, warnings) {
		t.Fatalf("warnings = %+v, se esperaban %+v", p.Warnings, warnings)
	}
	if len(p.Lines) != 4 || p.Lines[2].Qty != -3 {
		t.Fatalf("un aviso no quita ni añade líneas ni maquilla la cantidad: %+v", p.Lines)
	}
	if len(p.SuggestedQuestions) != 2 {
		t.Fatalf("los avisos no generan preguntas: %q", p.SuggestedQuestions)
	}
}

// TestDraftRun_RevisionFailure: sin revisión no hay entrega. No hay empuje, ni eventos,
// ni marca en el job; la solicitud ya creada se queda.
func TestDraftRun_RevisionFailure(t *testing.T) {
	boom := errors.New("el store no responde")
	pusher := &pushSpy{}
	b := newDraftBench(t, ambarNow(), stages.WithCRMPush(pusher))
	b.spy.err = boom

	art, err := b.stage.Run(context.Background(), reanalysisJob(), ambarDraftInput())
	if !errors.Is(err, boom) || art != nil {
		t.Fatalf("Run = (%v, %v)", art, err)
	}
	if want := "draft: revisión interpretada de la solicitud " + ambarIntakeID + ": el store no responde"; err.Error() != want {
		t.Fatalf("texto = %q, se esperaba %q", err, want)
	}
	if len(pusher.received()) != 0 || len(b.events.calls) != 0 || len(b.jobs.saved) != 0 {
		t.Fatalf("sin revisión no hay nada detrás: empujes=%v eventos=%v artefactos=%d",
			pusher.received(), b.events.calls, len(b.jobs.saved))
	}
	if len(b.intakes.rows) != 1 {
		t.Fatalf("solicitudes = %d; la cabecera ya creada se queda", len(b.intakes.rows))
	}
}
