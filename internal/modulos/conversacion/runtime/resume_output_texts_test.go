package runtime

import (
	"slices"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
)

// TestOutputTexts_SkipsOutputsWithoutText: del turno fuera de turno solo llega al hilo lo que
// tiene literal. Una salida sin texto —un adjunto sin caption— se salta, y las demás conservan
// su orden. Es interno porque ningún flujo del guion reinicia sobre un adjunto mudo.
func TestOutputTexts_SkipsOutputsWithoutText(t *testing.T) {
	outs := []engine.Output{
		{Text: "aviso-zzq"},
		{Media: &model.MediaRef{}},
		{Text: "pantalla-zzq"},
	}

	got := outputTexts(outs)

	if want := []string{"aviso-zzq", "pantalla-zzq"}; !slices.Equal(got, want) {
		t.Errorf("outputTexts = %q, quería %q: la salida sin texto no es una fila del hilo", got, want)
	}
	if got := outputTexts(nil); len(got) != 0 {
		t.Errorf("outputTexts(nil) = %q, quería ninguna", got)
	}
}
