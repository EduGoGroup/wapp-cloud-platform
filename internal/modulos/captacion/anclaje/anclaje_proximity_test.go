//go:build pendiente

package anclaje_test

// Trozo de anclaje_test.go (E-13): la REGLA 3, la proximidad, con sus dos topes.

import (
	"slices"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/anclaje"
)

func TestDistribute_Proximity_TwoPhotosAfterTheFirstCakeAnchorToThatLine(t *testing.T) {
	// El turno SIN TEXTO no gasta presupuesto: photo2 tiene delante a photo1, y si los
	// adjuntos contaran como «mensajes hacia atrás», una ráfaga dejaría huérfana a la
	// última foto. Con presupuesto 1 —el mínimo posible— las dos siguen anclando, y a
	// ningún otro sitio.
	d := anclaje.Distribute(sampleTurns, sampleLines, []anclaje.MediaRef{photo1, photo2}, anclaje.Options{MaxMessagesBack: 1})
	requireLine(t, d, 0, "wapp/media/foto1.jpg", "wapp/media/foto2.jpg")
	requireRequest(t, d)
	requireOnlyLines(t, d, 0)
}

func TestDistribute_Proximity_OnlyLooksBackwards(t *testing.T) {
	t.Run("a later message does not attract an earlier attachment", func(t *testing.T) {
		// La foto llega con el saludo, ANTES de que se hable de ninguna torta.
		photo := image("wapp/media/pronto.jpg", 1, at(9, 55, 0))
		d := anclaje.Distribute(sampleTurns, sampleLines, []anclaje.MediaRef{photo}, anclaje.Options{})
		requireRequest(t, d, "wapp/media/pronto.jpg")
		requireOnlyLines(t, d)
	})
	t.Run("an attachment before every turn goes to the request", func(t *testing.T) {
		photo := image("wapp/media/primera.jpg", 0, at(9, 54, 0))
		d := anclaje.Distribute(sampleTurns, sampleLines, []anclaje.MediaRef{photo}, anclaje.Options{})
		requireRequest(t, d, "wapp/media/primera.jpg")
		requireOnlyLines(t, d)
	})
	t.Run("the nearest message wins over an older one", func(t *testing.T) {
		// Seq 6 tiene detrás la torta 2 (seq 5) y, más atrás, la torta 1 (seq 2).
		photo := image("wapp/media/seis.jpg", 6, at(9, 56, 30))
		d := anclaje.Distribute(sampleTurns, sampleLines, []anclaje.MediaRef{photo}, anclaje.Options{})
		requireLine(t, d, 1, "wapp/media/seis.jpg")
		requireOnlyLines(t, d, 1)
	})
}

func TestDistribute_Proximity_StartsAtTheAttachmentOwnMessage(t *testing.T) {
	// El pie no nombra ninguna etiqueta (no hay mención), pero sostiene la evidencia de
	// la línea: el mensaje del adjunto es el más cercano de todos.
	lines := []anclaje.Line{{Idx: 7, Evidence: "dos docenas de alfajores", Label: "Caja surtida"}}
	turns := []anclaje.Turn{{Seq: 1, At: at(9, 55, 0), Text: "Quiero DOS docenas\nde alfajores, como estos"}}
	d := anclaje.Distribute(turns, lines, []anclaje.MediaRef{image("wapp/media/alf.jpg", 1, at(9, 55, 0))}, anclaje.Options{MaxMessagesBack: 1})
	requireLine(t, d, 7, "wapp/media/alf.jpg")
	requireRequest(t, d)
}

func TestDistribute_Proximity_MessageHoldingTwoLinesStopsTheSearch(t *testing.T) {
	// El mensaje más cercano describe las dos tortas: la foto podría ser de cualquiera.
	// Elegir una es inventar, y NO se sigue hacia atrás aunque el mensaje anterior
	// sostenga una sola línea. El par de control —sin el mensaje ambiguo— ancla.
	both := "Serían 2 tortas: una torta sería con decoración infantil, de bizcocho húmedo de chocolate " +
		"con crema de chocolate; la otra de bizcocho de vainilla que tenga lluvia de colores, con dulce de leche"
	turns := []anclaje.Turn{
		{Seq: 1, At: at(9, 55, 20), Text: firstCakeText},
		{Seq: 2, At: at(9, 55, 30), Text: both},
		{Seq: 3, At: at(9, 55, 40), Text: ""},
	}
	photo := image("wapp/media/ambigua.jpg", 3, at(9, 55, 40))

	d := anclaje.Distribute(turns, sampleLines, []anclaje.MediaRef{photo}, anclaje.Options{})
	requireRequest(t, d, "wapp/media/ambigua.jpg")
	requireOnlyLines(t, d)

	control := anclaje.Distribute([]anclaje.Turn{turns[0], turns[2]}, sampleLines, []anclaje.MediaRef{photo}, anclaje.Options{})
	requireLine(t, control, 0, "wapp/media/ambigua.jpg")
}

func TestDistribute_Proximity_EvidenceAcrossTwoMessagesIsHeldByNone(t *testing.T) {
	turns := []anclaje.Turn{
		{Seq: 1, At: at(9, 55, 30), Text: "una torta sería con decoración infantil,"},
		{Seq: 2, At: at(9, 55, 35), Text: "de bizcocho húmedo"},
		{Seq: 3, At: at(9, 55, 40), Text: ""},
	}
	lines := []anclaje.Line{{Idx: 0, Evidence: "una torta sería con decoración infantil, de bizcocho húmedo", Label: "Bizcochuelo"}}
	d := anclaje.Distribute(turns, lines, []anclaje.MediaRef{image("wapp/media/x.jpg", 3, at(9, 55, 40))}, anclaje.Options{})
	requireRequest(t, d, "wapp/media/x.jpg")
	requireOnlyLines(t, d)
}

func TestDistribute_Proximity_Window(t *testing.T) {
	// El turno 2 (la torta 1) es de las 9:55:30. La misma foto, en el turno 3; lo único
	// que cambia es CUÁNDO llegó y qué ventana se pide.
	base := at(9, 55, 30)
	cases := []struct {
		name     string
		elapsed  time.Duration
		opts     anclaje.Options
		anchored bool
	}{
		{"inside the default window", 10 * time.Second, anclaje.Options{}, true},
		{"exactly the default window is still inside", 5 * time.Minute, anclaje.Options{}, true},
		{"one nanosecond past the default window", 5*time.Minute + 1, anclaje.Options{}, false},
		{"an hour later", time.Hour, anclaje.Options{}, false},
		{"custom window keeps it", time.Hour, anclaje.Options{Window: time.Hour}, true},
		{"custom window drops it", 10 * time.Second, anclaje.Options{Window: 9 * time.Second}, false},
		{"zero window is the default", 5 * time.Minute, anclaje.Options{Window: 0}, true},
		{"negative window is the default, inside", 5 * time.Minute, anclaje.Options{Window: -time.Hour}, true},
		{"negative window is the default, outside", 5*time.Minute + 1, anclaje.Options{Window: -time.Hour}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			photo := image("wapp/media/x.jpg", 3, base.Add(c.elapsed))
			d := anclaje.Distribute(sampleTurns[:2], sampleLines, []anclaje.MediaRef{photo}, c.opts)
			if c.anchored {
				requireLine(t, d, 0, "wapp/media/x.jpg")
				requireRequest(t, d)
				return
			}
			requireRequest(t, d, "wapp/media/x.jpg")
			requireOnlyLines(t, d)
		})
	}
}

func TestDistribute_Proximity_WindowNeedsBothInstants(t *testing.T) {
	// Un llamante que todavía no tenga los `ts_unix` pasa el instante en cero. La
	// ventana no descarta nada y el orden (`Seq`) hace el trabajo: no hay un modo
	// «adivina la hora».
	untimed := make([]anclaje.Turn, 0, len(sampleTurns))
	for _, turn := range sampleTurns {
		untimed = append(untimed, anclaje.Turn{Seq: turn.Seq, Text: turn.Text})
	}
	t.Run("nothing has an instant", func(t *testing.T) {
		refs := []anclaje.MediaRef{
			{Ref: "wapp/media/foto1.jpg", Kind: anclaje.KindImage, Seq: 3},
			{Ref: "wapp/media/foto2.jpg", Kind: anclaje.KindImage, Seq: 4},
		}
		d := anclaje.Distribute(untimed, sampleLines, refs, anclaje.Options{})
		requireLine(t, d, 0, "wapp/media/foto1.jpg", "wapp/media/foto2.jpg")
		requireRequest(t, d)
	})
	t.Run("only the ref has an instant", func(t *testing.T) {
		d := anclaje.Distribute(untimed, sampleLines, []anclaje.MediaRef{image("wapp/media/x.jpg", 3, at(23, 0, 0))}, anclaje.Options{})
		requireLine(t, d, 0, "wapp/media/x.jpg")
	})
	t.Run("only the turns have an instant", func(t *testing.T) {
		ref := anclaje.MediaRef{Ref: "wapp/media/x.jpg", Kind: anclaje.KindImage, Seq: 3}
		d := anclaje.Distribute(sampleTurns, sampleLines, []anclaje.MediaRef{ref}, anclaje.Options{})
		requireLine(t, d, 0, "wapp/media/x.jpg")
	})
}

func TestDistribute_Proximity_TextlessTurnOutOfWindowAlsoStopsTheSearch(t *testing.T) {
	// El turno con la evidencia NO tiene instante, así que por sí solo no se descarta.
	// Lo que corta es el adjunto intermedio, una hora anterior a la foto: la ventana se
	// mira en cada turno del camino, tenga texto o no. El par de control —el mismo
	// camino con el intermedio dentro de la ventana— ancla.
	turns := []anclaje.Turn{
		{Seq: 1, Text: firstCakeText},
		{Seq: 2, At: at(9, 0, 0), Text: ""},
		{Seq: 3, At: at(10, 0, 0), Text: ""},
	}
	photo := image("wapp/media/x.jpg", 3, at(10, 0, 0))
	d := anclaje.Distribute(turns, sampleLines, []anclaje.MediaRef{photo}, anclaje.Options{})
	requireRequest(t, d, "wapp/media/x.jpg")
	requireOnlyLines(t, d)

	turns[1].At = at(9, 59, 0)
	control := anclaje.Distribute(turns, sampleLines, []anclaje.MediaRef{photo}, anclaje.Options{})
	requireLine(t, control, 0, "wapp/media/x.jpg")
}

func TestDistribute_Proximity_MessageBudget(t *testing.T) {
	// La misma foto, la misma distancia temporal, y lo único que cambia es cuántos
	// mensajes de charla se metieron por medio y qué presupuesto se pide. El turno del
	// adjunto (seq 9) no existe: no gasta.
	base := []anclaje.Turn{{Seq: 1, At: at(9, 55, 30), Text: firstCakeText}}
	chat := []anclaje.Turn{
		{Seq: 2, At: at(9, 55, 35), Text: "bueno"},
		{Seq: 3, At: at(9, 55, 36), Text: "dale"},
		{Seq: 4, At: at(9, 55, 37), Text: "gracias"},
	}
	photo := image("wapp/media/lejana.jpg", 9, at(9, 55, 50))
	cases := []struct {
		name     string
		chat     int
		opts     anclaje.Options
		anchored bool
	}{
		{"two chat messages fit the default budget of three", 2, anclaje.Options{}, true},
		{"three chat messages exhaust it right before the evidence", 3, anclaje.Options{}, false},
		{"custom budget reaches further", 3, anclaje.Options{MaxMessagesBack: 4}, true},
		{"custom budget of one", 1, anclaje.Options{MaxMessagesBack: 1}, false},
		{"custom budget of one with nothing in between", 0, anclaje.Options{MaxMessagesBack: 1}, true},
		{"negative budget is the default, inside", 2, anclaje.Options{MaxMessagesBack: -5}, true},
		{"negative budget is the default, outside", 3, anclaje.Options{MaxMessagesBack: -5}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			turns := append(slices.Clone(base), chat[:c.chat]...)
			d := anclaje.Distribute(turns, sampleLines, []anclaje.MediaRef{photo}, c.opts)
			if c.anchored {
				requireLine(t, d, 0, "wapp/media/lejana.jpg")
				requireRequest(t, d)
				return
			}
			requireRequest(t, d, "wapp/media/lejana.jpg")
			requireOnlyLines(t, d)
		})
	}
}

func TestDistribute_Proximity_TheAttachmentOwnTextSpendsBudget(t *testing.T) {
	// El pie «mirá» no nombra nada ni sostiene nada, pero es un mensaje con texto: con
	// presupuesto 1 se lo gasta él y la evidencia del turno anterior queda fuera.
	turns := []anclaje.Turn{
		{Seq: 1, At: at(9, 55, 30), Text: firstCakeText},
		{Seq: 2, At: at(9, 55, 40), Text: "mirá"},
	}
	photo := image("wapp/media/x.jpg", 2, at(9, 55, 40))

	d := anclaje.Distribute(turns, sampleLines, []anclaje.MediaRef{photo}, anclaje.Options{MaxMessagesBack: 1})
	requireRequest(t, d, "wapp/media/x.jpg")
	requireOnlyLines(t, d)

	control := anclaje.Distribute(turns, sampleLines, []anclaje.MediaRef{photo}, anclaje.Options{MaxMessagesBack: 2})
	requireLine(t, control, 0, "wapp/media/x.jpg")
}
