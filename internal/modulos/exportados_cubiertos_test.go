package modulos

import (
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/candados"
)

// TestExportadosCubiertos: cada exportado de x.go aparece como identificador en su x_test.go
// (05 E-9, «se cumple ya en rojo»), sobre alcanceExportados (el alcance más internal/arranque).
func TestExportadosCubiertos(t *testing.T) {
	fuentes := recorrerAlcance(t, alcanceExportados)
	for _, v := range candados.ExportadosCubiertos(fuentes) {
		t.Errorf("exportados cubiertos: %s", v)
	}
	t.Logf("recorridos=%d", len(fuentes))
}
