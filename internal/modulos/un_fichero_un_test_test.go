package modulos

import (
	"slices"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/candados"
)

// raizRepo es la raíz del repositorio vista desde este paquete (internal/modulos).
const raizRepo = "../.."

// alcance son los directorios que recorren un_fichero_un_test y exportados_cubiertos
// (plan/F0-andamiaje/diseno.md §4, «Alcance»). internal/nucleo está vacío hasta F1 y aun así
// va cubierto (el piloto lo necesita, F1 entrada E2); internal/apipublica, cuando exista.
// internal/arranque queda FUERA (D-F0-1: su test es el paquete); solo entra su huellatest.
var alcance = []string{
	"internal/modulos",
	"internal/nucleo",
	"internal/apipublica",
	"internal/pendiente",
	"internal/candados",
	"internal/arranque/huellatest",
}

// alcanceExportados es alcance más internal/arranque: diseno.md §4 lo deja dentro de
// exportados_cubiertos (Ejecutar en orquestador_test.go, EnrollServerCreds en pki_test.go).
var alcanceExportados = append(slices.Clone(alcance), "internal/arranque")

// recorrerAlcance recorre dirs desde la raíz del repo, con tests, y falla si no ve nada: un
// candado que no mira ningún fichero pasaría en verde sin proteger nada.
func recorrerAlcance(t *testing.T, dirs []string) []candados.Fuente {
	t.Helper()
	fuentes, err := candados.Recorrer(raizRepo, dirs, true)
	if err != nil {
		t.Fatalf("Recorrer(%q, %v) = error %v", raizRepo, dirs, err)
	}
	if len(fuentes) == 0 {
		t.Fatalf("Recorrer(%q, %v) recorrió 0 ficheros: el candado no mira nada", raizRepo, dirs)
	}
	return fuentes
}

// vio dice si ruta está entre las fuentes recorridas.
func vio(fuentes []candados.Fuente, ruta string) bool {
	return slices.ContainsFunc(fuentes, func(f candados.Fuente) bool { return f.Ruta == ruta })
}

// TestUnFicheroUnTest: cada x.go de producción del alcance tiene su x_test.go (05 E-3), con
// las excepciones verificadas por candados.UnFicheroUnTest.
func TestUnFicheroUnTest(t *testing.T) {
	fuentes := recorrerAlcance(t, alcance)
	const testigo = "internal/pendiente/pendiente.go"
	if !vio(fuentes, testigo) {
		t.Fatalf("el recorrido (%d ficheros) no vio %s: raíz o alcance equivocados", len(fuentes), testigo)
	}
	for _, v := range candados.UnFicheroUnTest(fuentes) {
		t.Errorf("un fichero, un test: %s", v)
	}
	t.Logf("recorridos=%d", len(fuentes))
}
