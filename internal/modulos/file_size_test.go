package modulos

import (
	"slices"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/candados"
)

// alcanceTamano es el árbol nuevo entero: alcanceExportados (que ya incluye internal/arranque)
// más los procesos de F9 y el binario nuevo. El código viejo no entra: muere en F10, y la
// regla se le aplica cuando se reconstruye (05 E-13).
var alcanceTamano = append(slices.Clone(alcanceExportados), "test/procesos", "cmd/server-modular")

// TestFileSize: ningún .go del árbol nuevo pasa de 600 líneas (500 + 20 %), salvo los de la
// lista cerrada candados.OversizedFiles, que no pueden crecer (05 E-13, D-R-7, decisión de
// Jhoan del 2026-10-04).
func TestFileSize(t *testing.T) {
	fuentes := recorrerAlcance(t, alcanceTamano)
	for _, v := range candados.FileSize(fuentes) {
		t.Errorf("tamaño de fichero: %s", v)
	}
	t.Logf("recorridos=%d", len(fuentes))
}
