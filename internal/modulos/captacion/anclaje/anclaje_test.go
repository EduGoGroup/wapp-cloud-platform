//go:build pendiente

package anclaje_test

import (
	"encoding/json"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/anclaje"
)

// ════════════════════════════════════════════════════════════════════════════
// EL FIXTURE
//
// 🔴 El texto de la conversación NO es el real de Ambar: se redactó a partir de la
// descripción del caso (Plan 044 · Ola 3 · T3.3). Esto es un DETECTOR DE REGRESIÓN y
// no acredita acierto: ninguna medida de calidad del reparto puede salir de aquí.
//
// Los instantes son del reloj del CLIENTE y están fijados: ni un time.Now() en los
// tests de este paquete.
//
// Los valores esperados se comprobaron además contra el paquete viejo
// (internal/intake/anclaje @ 8d875ab) con estos mismos casos: es la equivalencia
// viejo ↔ nuevo, fijada a mano porque el paquete nuevo no importa el viejo.
// ════════════════════════════════════════════════════════════════════════════

// at builds an instant of 2026-07-13, the day of the request.
func at(h, m, s int) time.Time {
	return time.Date(2026, 7, 13, h, m, s, 0, time.UTC)
}

// Las cuatro líneas del borrador. La de envío va con evidencia VACÍA a propósito: no
// sale de ninguna frase del cliente, y tiene que ser incapaz de atraer una foto.
var sampleLines = []anclaje.Line{
	{Idx: 0, Evidence: "una torta sería con decoración infantil, de bizcocho húmedo de chocolate",
		Label: "Torta chocolate húmedo + crema choc."},
	{Idx: 1, Evidence: "otra de bizcocho de vainilla que tenga lluvia de colores",
		Label: "Torta vainilla, lluvia de colores, dulce de leche y merengue"},
	{Idx: 2, Evidence: "un paquete de tequeños congelados de 30",
		Label: "Tequeños congelados paquete x30"},
	{Idx: 3, Evidence: "", Label: "Envío"},
}

const (
	firstCakeText  = "Serían 2 tortas. Una torta sería con decoración infantil, de bizcocho húmedo de chocolate con crema de chocolate, de 10 o 12 porciones"
	secondCakeText = "Y la otra de bizcocho de vainilla que tenga lluvia de colores, con dulce de leche y merengue, de 25 o 30 porciones"
)

// La conversación. Los turnos 3, 4, 6 y 8 son los adjuntos: llegan SIN texto, que es
// como llega una foto suelta por WhatsApp.
var sampleTurns = []anclaje.Turn{
	{Seq: 1, At: at(9, 55, 0), Text: "Hola, buenas! Te quería pedir un presupuesto para el miércoles de la semana que viene"},
	{Seq: 2, At: at(9, 55, 30), Text: firstCakeText},
	{Seq: 3, At: at(9, 55, 40), Text: ""},
	{Seq: 4, At: at(9, 55, 45), Text: ""},
	{Seq: 5, At: at(9, 56, 10), Text: secondCakeText},
	{Seq: 6, At: at(9, 56, 30), Text: ""},
	{Seq: 7, At: at(9, 56, 50), Text: "También quería un paquete de tequeños congelados de 30"},
	{Seq: 8, At: at(13, 10, 0), Text: ""},
	{Seq: 9, At: at(13, 11, 0), Text: "Ah, y así de vainilla la quiero"},
}

// Las CINCO referencias del caso, una por cada destino posible del reparto:
//
//	photo1, photo2 → línea 0 por PROXIMIDAD (llegan pegadas al mensaje de la torta 1)
//	audio1         → SOLICITUD por ser audio (regla 1, sin excepción)
//	photo3         → SOLICITUD por quedar FUERA de la ventana (3 h después)
//	photo4         → línea 1 por MENCIÓN («de vainilla» en su propio pie)
var (
	photo1 = anclaje.MediaRef{Ref: "wapp/media/foto1.jpg", Kind: anclaje.KindImage, Seq: 3, At: at(9, 55, 40)}
	photo2 = anclaje.MediaRef{Ref: "wapp/media/foto2.jpg", Kind: anclaje.KindImage, Seq: 4, At: at(9, 55, 45)}
	audio1 = anclaje.MediaRef{Ref: "wapp/media/audio1.ogg", Kind: anclaje.KindAudio, Seq: 6, At: at(9, 56, 30)}
	photo3 = anclaje.MediaRef{Ref: "wapp/media/foto3.jpg", Kind: anclaje.KindImage, Seq: 8, At: at(13, 10, 0)}
	photo4 = anclaje.MediaRef{Ref: "wapp/media/foto4.jpg", Kind: anclaje.KindImage, Seq: 9, At: at(13, 11, 0)}
)

var sampleRefs = []anclaje.MediaRef{photo1, photo2, audio1, photo3, photo4}

// ---------------------------------------------------------------------------
// Ayudas de aserción. Ninguna cuenta: TODAS dicen QUÉ ref está DÓNDE.
// ---------------------------------------------------------------------------

// refIDs devuelve los identificadores de un tramo del reparto, EN ORDEN. Comparar
// identificadores y no longitudes es la diferencia entre «hay dos fotos en la línea
// 0» y «las dos fotos que hay en la línea 0 son la 1 y la 2».
func refIDs(refs []anclaje.MediaRef) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		out = append(out, r.Ref)
	}
	return out
}

func requireLine(t *testing.T, d anclaje.Distribution, idx int, want ...string) {
	t.Helper()
	if got := refIDs(d.ByLine[idx]); !slices.Equal(got, want) {
		t.Fatalf("línea %d: esperaba %v, obtuve %v", idx, want, got)
	}
}

func requireRequest(t *testing.T, d anclaje.Distribution, want ...string) {
	t.Helper()
	if got := refIDs(d.Request); !slices.Equal(got, want) {
		t.Fatalf("solicitud: esperaba %v, obtuve %v", want, got)
	}
}

// requireOnlyLines comprueba que NINGUNA línea fuera de las permitidas recibió
// adjuntos, y que ninguna aparece como clave sin tenerlos. Sin esto, «la foto está en
// la línea 0» sería compatible con «y también en la 1».
func requireOnlyLines(t *testing.T, d anclaje.Distribution, allowed ...int) {
	t.Helper()
	for _, idx := range slices.Sorted(maps.Keys(d.ByLine)) {
		if len(d.ByLine[idx]) == 0 {
			t.Fatalf("la línea %d aparece en ByLine sin adjuntos", idx)
		}
		if !slices.Contains(allowed, idx) {
			t.Fatalf("la línea %d no debía recibir nada y recibió %v", idx, refIDs(d.ByLine[idx]))
		}
	}
}

// image builds a photo ref with a known instant.
func image(ref string, seq int, when time.Time) anclaje.MediaRef {
	return anclaje.MediaRef{Ref: ref, Kind: anclaje.KindImage, Seq: seq, At: when}
}

// ---------------------------------------------------------------------------
// Las constantes: texto observable
// ---------------------------------------------------------------------------

func TestConstants_AreTheObservableLiterals(t *testing.T) {
	kinds := map[string]string{
		anclaje.KindImage:    "image",
		anclaje.KindAudio:    "audio",
		anclaje.KindPTT:      "ptt",
		anclaje.KindVoice:    "voice",
		anclaje.KindVideo:    "video",
		anclaje.KindDocument: "document",
	}
	if len(kinds) != 6 {
		t.Fatalf("las seis clases deben ser distintas, hay %d", len(kinds))
	}
	for got, want := range kinds {
		if got != want {
			t.Errorf("clase %q: se esperaba el literal %q", got, want)
		}
	}
	// La constante y el literal tienen que ser la MISMA cosa. Comparar solo contra la
	// constante sería el test tautológico que pasa con cualquier valor.
	if anclaje.AudioLabel != "🎙️ audio del cliente — escúchalo" {
		t.Errorf("AudioLabel dejó de ser el texto de T3.3/REQ-29: %q", anclaje.AudioLabel)
	}
	if anclaje.DefaultMaxMessagesBack != 3 {
		t.Errorf("DefaultMaxMessagesBack = %d, se esperaba 3", anclaje.DefaultMaxMessagesBack)
	}
	if anclaje.DefaultWindow != 5*time.Minute {
		t.Errorf("DefaultWindow = %v, se esperaba 5m", anclaje.DefaultWindow)
	}
}

// ---------------------------------------------------------------------------
// MediaRef: lo que se persiste en el payload del borrador
// ---------------------------------------------------------------------------

func TestMediaRef_JSONNeverCarriesSeqNorAt(t *testing.T) {
	cases := []struct {
		name string
		ref  anclaje.MediaRef
		want string
	}{
		{"audio with label",
			anclaje.MediaRef{Ref: "wapp/media/a.ogg", Kind: anclaje.KindAudio, Label: anclaje.AudioLabel, Seq: 6, At: at(9, 56, 30)},
			`{"ref":"wapp/media/a.ogg","kind":"audio","label":"🎙️ audio del cliente — escúchalo"}`},
		{"photo without label omits it",
			anclaje.MediaRef{Ref: "wapp/media/f.jpg", Kind: anclaje.KindImage, Seq: 3, At: at(9, 55, 40)},
			`{"ref":"wapp/media/f.jpg","kind":"image"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := json.Marshal(c.ref)
			if err != nil {
				t.Fatalf("Marshal falló: %v", err)
			}
			if string(got) != c.want {
				t.Fatalf("JSON = %s, se esperaba %s", got, c.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// REGLA 1 — el audio va SIEMPRE a nivel de solicitud, con su etiqueta
// ---------------------------------------------------------------------------

func TestDistribute_AudioGoesToRequestWithItsLabel(t *testing.T) {
	d := anclaje.Distribute(sampleTurns, sampleLines, []anclaje.MediaRef{audio1}, anclaje.Options{})

	requireRequest(t, d, "wapp/media/audio1.ogg")
	requireOnlyLines(t, d)
	if got := d.Request[0].Label; got != "🎙️ audio del cliente — escúchalo" {
		t.Fatalf("etiqueta del audio: esperaba %q, obtuve %q", "🎙️ audio del cliente — escúchalo", got)
	}
}

func TestDistribute_AudioIsNeverAnchoredEvenWhenItsMessageNamesTheProduct(t *testing.T) {
	// 🔴 EL BLINDAJE DE LA REGLA 1. El audio llega con un pie que nombra a UNA sola
	// línea sin ambigüedad —«de chocolate»— y que además sostiene su evidencia: por la
	// mención o por la proximidad, anclaría. El par de control (la misma ref como foto)
	// demuestra que el montaje ancla, y que lo único que lo impide es ser audio.
	turns := []anclaje.Turn{
		{Seq: 1, At: at(9, 55, 30), Text: firstCakeText},
		{Seq: 2, At: at(9, 55, 40), Text: "Acá te explico la de chocolate"},
	}
	voice := anclaje.MediaRef{Ref: "wapp/media/nota.ogg", Kind: anclaje.KindAudio, Seq: 2, At: at(9, 55, 40), Label: "otra etiqueta"}

	d := anclaje.Distribute(turns, sampleLines, []anclaje.MediaRef{voice}, anclaje.Options{})
	requireRequest(t, d, "wapp/media/nota.ogg")
	requireOnlyLines(t, d)
	if d.Request[0].Label != anclaje.AudioLabel {
		t.Fatalf("la etiqueta que traía el audio no se pisó: %q", d.Request[0].Label)
	}

	control := anclaje.Distribute(turns, sampleLines, []anclaje.MediaRef{image("wapp/media/nota.ogg", 2, at(9, 55, 40))}, anclaje.Options{})
	requireLine(t, control, 0, "wapp/media/nota.ogg")
}

func TestDistribute_AudioKinds(t *testing.T) {
	// WhatsApp manda la nota de voz como `ptt`, no como `audio`. Seq 3 es un turno
	// pegado a la torta 1: una ref que NO sea audio ancla ahí a la línea 0.
	cases := []struct {
		name  string
		kind  string
		audio bool
	}{
		{"audio", anclaje.KindAudio, true},
		{"ptt", anclaje.KindPTT, true},
		{"voice", anclaje.KindVoice, true},
		{"uppercase", "PTT", true},
		{"mixed case with ASCII blanks", " Audio ", true},
		{"tab and newline around", "\tVOICE\n", true},
		{"unicode blanks around", "\u00a0ptt\u2003", true},
		{"image", anclaje.KindImage, false},
		{"video", anclaje.KindVideo, false},
		{"document", anclaje.KindDocument, false},
		{"empty kind", "", false},
		{"unknown kind", "sticker", false},
		{"mime type is not a kind", "audio/ogg", false},
		{"accented lookalike", "áudio", false},
		{"blank inside the word", "au dio", false},
		{"zero width space is not trimmed", "\u200baudio", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ref := anclaje.MediaRef{Ref: "wapp/media/x", Kind: c.kind, Seq: 3, At: at(9, 55, 40)}
			d := anclaje.Distribute(sampleTurns, sampleLines, []anclaje.MediaRef{ref}, anclaje.Options{})
			if c.audio {
				requireRequest(t, d, "wapp/media/x")
				requireOnlyLines(t, d)
				if d.Request[0].Label != anclaje.AudioLabel {
					t.Fatalf("%+q no recibió la etiqueta de audio", c.kind)
				}
				return
			}
			requireLine(t, d, 0, "wapp/media/x")
			requireRequest(t, d)
			if got := d.ByLine[0][0].Label; got != "" {
				t.Fatalf("%+q no es audio y recibió la etiqueta %q", c.kind, got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// EL INVARIANTE CONTABLE: ni se pierde ni se duplica
// ---------------------------------------------------------------------------

func TestDistribute_AccountingInvariant(t *testing.T) {
	// El caso con VARIAS refs y con los TRES destinos ocupados: dos anclajes por
	// proximidad, uno por mención, un audio en la cabecera y una foto que se fue a la
	// cabecera por falta de certeza.
	d := anclaje.Distribute(sampleTurns, sampleLines, sampleRefs, anclaje.Options{})

	// LA CUENTA ES SOBRE EL MULTICONJUNTO DE IDENTIFICADORES, no sobre el tamaño: así
	// una ref que aparece dos veces y otra que desaparece se ven igual que una pérdida.
	out := make([]string, 0, len(sampleRefs))
	for _, idx := range slices.Sorted(maps.Keys(d.ByLine)) {
		out = append(out, refIDs(d.ByLine[idx])...)
	}
	out = append(out, refIDs(d.Request)...)
	slices.Sort(out)
	in := refIDs(sampleRefs)
	slices.Sort(in)
	if !slices.Equal(in, out) {
		t.Fatalf("el reparto no conserva las refs:\n entrada %v\n salida  %v", in, out)
	}

	// Y el reparto CONCRETO, que es lo que hace que el invariante no sea satisfacible
	// mandándolo todo a la cabecera.
	requireLine(t, d, 0, "wapp/media/foto1.jpg", "wapp/media/foto2.jpg")
	requireLine(t, d, 1, "wapp/media/foto4.jpg")
	requireRequest(t, d, "wapp/media/audio1.ogg", "wapp/media/foto3.jpg")
	requireOnlyLines(t, d, 0, 1)
}

func TestDistribute_OutputKeepsInputOrderWithinEachDestination(t *testing.T) {
	// Las mismas refs, en el orden inverso: cada destino sale también invertido.
	reversed := slices.Clone(sampleRefs)
	slices.Reverse(reversed)

	d := anclaje.Distribute(sampleTurns, sampleLines, reversed, anclaje.Options{})
	requireLine(t, d, 0, "wapp/media/foto2.jpg", "wapp/media/foto1.jpg")
	requireLine(t, d, 1, "wapp/media/foto4.jpg")
	requireRequest(t, d, "wapp/media/foto3.jpg", "wapp/media/audio1.ogg")
	requireOnlyLines(t, d, 0, 1)
}

func TestDistribute_IsStable(t *testing.T) {
	// Dos ejecuciones sobre la misma entrada dan el MISMO reparto. Importa porque la
	// mención recorre un mapa por dentro: si el resultado dependiera de ese orden, el
	// fallo sería intermitente y solo saldría en producción.
	first := anclaje.Distribute(sampleTurns, sampleLines, sampleRefs, anclaje.Options{})
	for i := range 50 {
		other := anclaje.Distribute(sampleTurns, sampleLines, sampleRefs, anclaje.Options{})
		if !slices.Equal(refIDs(other.Request), refIDs(first.Request)) {
			t.Fatalf("vuelta %d: la solicitud cambió", i)
		}
		if len(other.ByLine) != len(first.ByLine) {
			t.Fatalf("vuelta %d: cambió el número de líneas con adjuntos", i)
		}
		for idx := range first.ByLine {
			if !slices.Equal(refIDs(other.ByLine[idx]), refIDs(first.ByLine[idx])) {
				t.Fatalf("vuelta %d: la línea %d cambió", i, idx)
			}
		}
	}
}

func TestDistribute_DoesNotMutateItsInputs(t *testing.T) {
	turns := []anclaje.Turn{sampleTurns[4], sampleTurns[1], sampleTurns[2], sampleTurns[0], sampleTurns[3]}
	turnsBefore := slices.Clone(turns)
	refs := []anclaje.MediaRef{audio1, photo1}
	refsBefore := slices.Clone(refs)
	lines := slices.Clone(sampleLines)

	// Los turnos llegan DESORDENADOS: se ordenan por Seq en una copia y la foto ancla.
	d := anclaje.Distribute(turns, lines, refs, anclaje.Options{})
	requireLine(t, d, 0, "wapp/media/foto1.jpg")
	requireRequest(t, d, "wapp/media/audio1.ogg")

	if !slices.Equal(turns, turnsBefore) {
		t.Fatalf("Distribute reordenó o cambió los turnos del llamante: %v", turns)
	}
	// El audio sale etiquetado en el reparto, pero la ref del llamante no se toca.
	if !slices.Equal(refs, refsBefore) {
		t.Fatalf("Distribute cambió las refs del llamante: %v", refs)
	}
	if !slices.Equal(lines, sampleLines) {
		t.Fatalf("Distribute cambió las líneas del llamante: %v", lines)
	}
}

func TestDistribute_EmptyInputs(t *testing.T) {
	t.Run("no refs gives an empty usable distribution", func(t *testing.T) {
		d := anclaje.Distribute(sampleTurns, sampleLines, nil, anclaje.Options{})
		if d.ByLine == nil {
			t.Fatal("ByLine llegó nil: el llamante no debería tener que comprobarlo")
		}
		requireRequest(t, d)
		requireOnlyLines(t, d)
	})
	t.Run("no turns and no lines sends everything to the request", func(t *testing.T) {
		d := anclaje.Distribute(nil, nil, sampleRefs, anclaje.Options{})
		if d.ByLine == nil {
			t.Fatal("ByLine llegó nil sin líneas")
		}
		requireRequest(t, d, refIDs(sampleRefs)...)
		requireOnlyLines(t, d)
		for _, r := range d.Request {
			want := ""
			if r.Kind == anclaje.KindAudio {
				want = anclaje.AudioLabel
			}
			if r.Label != want {
				t.Fatalf("%s: etiqueta %q, se esperaba %q", r.Ref, r.Label, want)
			}
		}
	})
	t.Run("no lines", func(t *testing.T) {
		d := anclaje.Distribute(sampleTurns, nil, []anclaje.MediaRef{photo1, photo4}, anclaje.Options{})
		requireRequest(t, d, "wapp/media/foto1.jpg", "wapp/media/foto4.jpg")
		requireOnlyLines(t, d)
	})
	t.Run("lines without evidence nor label attract nothing", func(t *testing.T) {
		blank := []anclaje.Line{{Idx: 0}, {Idx: 1, Evidence: "   ", Label: " \t "}}
		d := anclaje.Distribute(sampleTurns, blank, []anclaje.MediaRef{photo1, photo4}, anclaje.Options{})
		requireRequest(t, d, "wapp/media/foto1.jpg", "wapp/media/foto4.jpg")
		requireOnlyLines(t, d)
	})
}

func TestDistribute_ShippingLineAttractsNothing(t *testing.T) {
	// La línea de envío nace sin evidencia y su etiqueta, «Envío», no aparece en ningún
	// mensaje. Tiene que ser IMPOSIBLE que le cuelgue una foto.
	d := anclaje.Distribute(sampleTurns, sampleLines, sampleRefs, anclaje.Options{})
	if refs, ok := d.ByLine[3]; ok {
		t.Fatalf("la línea de envío recibió %v", refIDs(refs))
	}
}
