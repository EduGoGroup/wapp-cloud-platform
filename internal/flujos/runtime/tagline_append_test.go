package runtime

import (
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
)

// Tests INTERNOS de appendTagline (Plan 043 · T3.8 punto 2). Van en `package
// runtime` —como webhook_sink_test.go— porque lo que hay que fijar son los BORDES
// de una función pura, y desde fuera solo se llegaría a ellos fabricando flujos que
// emitan justo esa forma de salida: mucho montaje para probar cuatro ramas.
//
// El borde que importa es el del adjunto: un nodo media emite un Output SIN texto, y
// pegarle una coletilla a eso produciría un mensaje que el cliente no puede leer.

// TestAppendTagline_PegaAlUltimoTextoYDevuelveQueLoHizo es el caso normal, con DOS
// salidas para que se vea que va al ÚLTIMO. Con una sola salida, un bug que pegara
// al primero pasaría desapercibido.
func TestAppendTagline_PegaAlUltimoTextoYDevuelveQueLoHizo(t *testing.T) {
	outs := []engine.Output{{Text: "primero"}, {Text: "segundo"}}

	got, pegada := appendTagline(outs, "coletilla")

	if !pegada {
		t.Fatalf("debe informar de que la pegó")
	}
	if got[0].Text != "primero" {
		t.Fatalf("no debe tocar los salientes anteriores: %q", got[0].Text)
	}
	if got[1].Text != "segundo\n\ncoletilla" {
		t.Fatalf("debe pegarse al ÚLTIMO texto: %q", got[1].Text)
	}
	if len(got) != 2 {
		t.Fatalf("no debe inventar salientes: hay %d", len(got))
	}
}

// TestAppendTagline_SinColetillaNoTocaNada: el caso vacío no debe dejar rastro, ni
// siquiera los dos saltos de línea.
func TestAppendTagline_SinColetillaNoTocaNada(t *testing.T) {
	got, pegada := appendTagline([]engine.Output{{Text: "solo"}}, "")

	if pegada {
		t.Fatalf("sin coletilla no se pega nada")
	}
	if got[0].Text != "solo" {
		t.Fatalf("el texto debe quedar intacto: %q", got[0].Text)
	}
}

// TestAppendTagline_SinSalidasNoInventaNada: una respuesta sin salientes no gana uno
// por tener coletilla. Inventarlo convertiría un turno mudo en un mensaje suelto que
// habla de algo que el cliente no ha visto.
func TestAppendTagline_SinSalidasNoInventaNada(t *testing.T) {
	got, pegada := appendTagline(nil, "coletilla")

	if pegada || len(got) != 0 {
		t.Fatalf("sin salidas no se inventa un saliente: pegada=%v got=%+v", pegada, got)
	}
}

// TestAppendTagline_UltimaSalidaSinTextoNoRecibeColetilla es el borde del ADJUNTO: la
// última salida es un media (sin texto), así que no hay dónde pegarla.
//
// El `false` es la mitad que importa y la razón de que la función informe en vez de
// devolver solo las salidas: quien llama tiene que poder NO marcar la conversación.
// Marcarla aquí dejaría el estado diciendo «ya se le ofreció» sobre algo que el
// cliente nunca leyó, y esa conversación no volvería a recibirla jamás.
func TestAppendTagline_UltimaSalidaSinTextoNoRecibeColetilla(t *testing.T) {
	outs := []engine.Output{{Text: "aquí tienes el catálogo"}, {Media: &model.MediaRef{}}}

	got, pegada := appendTagline(outs, "coletilla")

	if pegada {
		t.Fatalf("sin un último texto donde pegarla, NO se pega — y hay que decirlo")
	}
	if got[1].Text != "" {
		t.Fatalf("no se le inventa texto al adjunto: %q", got[1].Text)
	}
	if got[0].Text != "aquí tienes el catálogo" {
		t.Fatalf("y tampoco se cuela en un saliente anterior: %q", got[0].Text)
	}
	if len(got) != 2 {
		t.Fatalf("no debe inventar salientes: hay %d", len(got))
	}
}
