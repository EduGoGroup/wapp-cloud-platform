//go:build pendiente

package anclaje_test

// Trozo de anclaje_test.go (E-13): la REGLA 2, la mención textual.

import (
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/anclaje"
)

func TestDistribute_Mention_CaptionBeatsTheWindow(t *testing.T) {
	// photo4 llega TRES HORAS después del último mensaje: por proximidad iría a la
	// cabecera, que es donde acaba photo3 —misma hora, sin pie—. Lo que la salva es su
	// propio pie, «de vainilla»: la mención no mira ni la ventana ni el presupuesto.
	d := anclaje.Distribute(sampleTurns, sampleLines, []anclaje.MediaRef{photo3, photo4}, anclaje.Options{MaxMessagesBack: 1})
	requireLine(t, d, 1, "wapp/media/foto4.jpg")
	requireRequest(t, d, "wapp/media/foto3.jpg")
	requireOnlyLines(t, d, 1)
}

func TestDistribute_Mention_BeatsProximity(t *testing.T) {
	// El mensaje anterior sostiene la evidencia de la línea 0, y está cerca. Pero el pie
	// de la foto nombra a la línea 1: manda el pie. El par de control —la misma foto con
	// un pie que no nombra nada— cae en la línea 0 por proximidad.
	turns := []anclaje.Turn{
		{Seq: 1, At: at(9, 55, 30), Text: firstCakeText},
		{Seq: 2, At: at(9, 55, 40), Text: "y así de vainilla la otra"},
	}
	photo := image("wapp/media/m.jpg", 2, at(9, 55, 40))
	d := anclaje.Distribute(turns, sampleLines, []anclaje.MediaRef{photo}, anclaje.Options{})
	requireLine(t, d, 1, "wapp/media/m.jpg")
	requireOnlyLines(t, d, 1)

	turns[1].Text = "así la quiero"
	control := anclaje.Distribute(turns, sampleLines, []anclaje.MediaRef{photo}, anclaje.Options{})
	requireLine(t, control, 0, "wapp/media/m.jpg")
	requireOnlyLines(t, control, 0)
}

func TestDistribute_Mention_CaptionNamingTwoLinesAnchorsToNone(t *testing.T) {
	turns := []anclaje.Turn{
		{Seq: 1, At: at(9, 55, 0), Text: "Te mando fotos: la de chocolate y la de vainilla"},
	}
	photo := image("wapp/media/dos.jpg", 1, at(9, 55, 0))
	d := anclaje.Distribute(turns, sampleLines, []anclaje.MediaRef{photo}, anclaje.Options{})
	requireRequest(t, d, "wapp/media/dos.jpg")
	requireOnlyLines(t, d)
}

func TestDistribute_Mention_AmbiguousCaptionStillGoesThroughProximity(t *testing.T) {
	// El pie nombra a las DOS tortas, así que no hay mención. Pero ese mismo mensaje
	// sostiene la evidencia de UNA sola línea, y la proximidad —que empieza por el
	// mensaje del adjunto— la ancla.
	turns := []anclaje.Turn{
		{Seq: 1, At: at(9, 55, 0), Text: "No la de vainilla: una torta sería con decoración infantil, de bizcocho húmedo de chocolate"},
	}
	photo := image("wapp/media/amb.jpg", 1, at(9, 55, 0))
	d := anclaje.Distribute(turns, sampleLines, []anclaje.MediaRef{photo}, anclaje.Options{})
	requireLine(t, d, 0, "wapp/media/amb.jpg")
	requireRequest(t, d)
	requireOnlyLines(t, d, 0)
}

func TestDistribute_Mention_SharedTokenDoesNotDistinguish(t *testing.T) {
	// «torta» está en las etiquetas de LAS DOS tortas, así que no señala a ninguna.
	turns := []anclaje.Turn{{Seq: 1, At: at(9, 55, 0), Text: "Mirá esta torta"}}
	photo := image("wapp/media/torta.jpg", 1, at(9, 55, 0))
	d := anclaje.Distribute(turns, sampleLines, []anclaje.MediaRef{photo}, anclaje.Options{})
	requireRequest(t, d, "wapp/media/torta.jpg")
	requireOnlyLines(t, d)
}

func TestDistribute_Mention_ComparesByTokenNotBySubstring(t *testing.T) {
	// La trampa de «sin sal» dentro de «salsa», aquí con un par MEDIDO: «paquetería»
	// contiene literalmente «paquete», que es un token distintivo de la línea de
	// tequeños. Y el falso positivo no es cosmético —la frase habla del ENVÍO—: una
	// comparación por subcadena colgaría la foto de la línea de tequeños.
	//
	// ⚠️ La palabra está comprobada carácter a carácter. «empaquetado» NO vale como
	// trampa: lleva «paquet-A-do», no contiene «paquete», y ninguna implementación
	// caería en ella.
	//
	// El par de control es lo que hace que el verde signifique algo: el mismo montaje
	// con «el paquete» SÍ ancla.
	lines := []anclaje.Line{sampleLines[2], sampleLines[0]}
	photo := image("wapp/media/p.jpg", 1, at(9, 55, 0))

	trap := []anclaje.Turn{{Seq: 1, At: at(9, 55, 0), Text: "¿me lo podés mandar por paquetería?"}}
	dTrap := anclaje.Distribute(trap, lines, []anclaje.MediaRef{photo}, anclaje.Options{})
	requireRequest(t, dTrap, "wapp/media/p.jpg")
	requireOnlyLines(t, dTrap)

	good := []anclaje.Turn{{Seq: 1, At: at(9, 55, 0), Text: "¿y el paquete cómo viene?"}}
	dGood := anclaje.Distribute(good, lines, []anclaje.MediaRef{photo}, anclaje.Options{})
	requireLine(t, dGood, 2, "wapp/media/p.jpg")
	requireRequest(t, dGood)
}

func TestDistribute_Mention_OnlyReadsTheTurnThatBroughtTheAttachment(t *testing.T) {
	t.Run("no turn with the ref seq means no mention", func(t *testing.T) {
		// «de vainilla» está en el turno 1, pero la foto es del turno 2, que no existe:
		// no se toma prestado el texto de otro mensaje.
		turns := []anclaje.Turn{{Seq: 1, At: at(9, 55, 0), Text: "así de vainilla la quiero"}}
		d := anclaje.Distribute(turns, sampleLines, []anclaje.MediaRef{image("wapp/media/x.jpg", 2, at(9, 55, 5))}, anclaje.Options{})
		requireRequest(t, d, "wapp/media/x.jpg")
		requireOnlyLines(t, d)
	})
	t.Run("without its turn proximity still decides", func(t *testing.T) {
		turns := []anclaje.Turn{{Seq: 1, At: at(9, 55, 30), Text: firstCakeText}}
		d := anclaje.Distribute(turns, sampleLines, []anclaje.MediaRef{image("wapp/media/x.jpg", 2, at(9, 55, 40))}, anclaje.Options{})
		requireLine(t, d, 0, "wapp/media/x.jpg")
		requireRequest(t, d)
	})
	t.Run("two turns with the same seq: the first in input order", func(t *testing.T) {
		turns := []anclaje.Turn{
			{Seq: 1, At: at(9, 55, 0), Text: "la de vainilla"},
			{Seq: 1, At: at(9, 55, 0), Text: "la de chocolate"},
		}
		d := anclaje.Distribute(turns, sampleLines, []anclaje.MediaRef{image("wapp/media/x.jpg", 1, at(9, 55, 0))}, anclaje.Options{})
		requireLine(t, d, 1, "wapp/media/x.jpg")
		requireOnlyLines(t, d, 1)
	})
}
