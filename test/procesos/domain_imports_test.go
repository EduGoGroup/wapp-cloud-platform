package procesos

import (
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/candados"
)

// goModulePath es la ruta del módulo Go: candados.ProcessImports relativiza con ella los imports.
const goModulePath = "github.com/EduGoGroup/wapp-cloud-platform"

// TestProcessImports es el candado de R9.4.d (plan/F9-procesos/requisitos.md): un proceso entra
// por las puertas reales y no importa paquetes de dominio. Va sin etiqueta integracion, como
// TestSinBDViva, para correr en `make test` (ci-local) sin Docker, y recorre todos los .go del
// paquete, con o sin etiqueta. Las reglas las define candados.ProcessImports, FICHERO A FICHERO:
// nada de internal/ en un proceso; internal/candados solo en los dos ficheros-candado (este y
// sin_bd_viva_test.go); y en un *_contrato_test.go, solo su suite …helpertest, el constructor
// del puerto que esa suite prueba e internal/platform/crypto. Sus casos que muerden viven en
// internal/candados/testdata/processimports/muerde.
//
// Hasta F1-06 esto era un comando de la spec que miraba el paquete entero y no estaba en ningún
// gate (hallazgos 36 y 37 de F1).
func TestProcessImports(t *testing.T) {
	fuentes, err := candados.Recorrer("../..", []string{"test/procesos"}, true)
	if err != nil {
		t.Fatalf("recorrer test/procesos: %v", err)
	}
	// Un recorrido vacío pasaría sin mirar nada: hoy hay al menos doc.go y este fichero.
	if len(fuentes) == 0 {
		t.Fatal("recorridos = 0: el candado no vio ningún fichero de test/procesos")
	}
	t.Logf("recorridos = %d", len(fuentes))
	for _, v := range candados.ProcessImports(goModulePath, fuentes) {
		t.Errorf("%s: %s", v.Fichero, v.Motivo)
	}
}
