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
// internal/arranque NO entra entero (D-F0-1: es el arranque copiado y su test es el paquete):
// de él entran dos cosas. Una, sus adaptadores bridge_<x>.go, y SOLO esos, que
// TestUnFicheroUnTest suma al alcance con candados.WalkBridges (bridgeDirs; D-F1-16, P5,
// Jhoan, 2026-10-03; 05 §4.2): un adaptador sin su bridge_<x>_test.go muerde. Otra, su
// huellatest, que desde D-F1-10 (Jhoan, 2026-10-02) NO está exento de ningún candado: su nombre termina en
// «test», no en el sufijo compuesto «helpertest» que exime a suites y dobles
// (candados.UnFicheroUnTest), y no se puede renombrar porque lo importa el test del arranque
// viejo. Su huellatest.go lleva su huellatest_test.go, con todos sus exportados.
var alcance = []string{
	"internal/modulos",
	"internal/nucleo",
	"internal/apipublica",
	"internal/pendiente",
	"internal/candados",
	"internal/arranque/huellatest",
}

// bridgeDirs son los directorios de los que un_fichero_un_test mira SOLO los adaptadores de
// arranque bridge_<x>.go (hijos directos, con su test), sin meter el directorio en alcance.
// Es la misma lista que COBERTURA_BRIDGE_DIRS del Makefile.
var bridgeDirs = []string{"internal/arranque"}

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

// walkBridges devuelve, con tests, los adaptadores bridge_<x>.go de bridgeDirs.
func walkBridges(t *testing.T) []candados.Fuente {
	t.Helper()
	var bridges []candados.Fuente
	for _, dir := range bridgeDirs {
		fuentes, err := candados.WalkBridges(raizRepo, dir, true)
		if err != nil {
			t.Fatalf("WalkBridges(%q, %q) = error %v", raizRepo, dir, err)
		}
		bridges = append(bridges, fuentes...)
	}
	return bridges
}

// vio dice si ruta está entre las fuentes recorridas.
func vio(fuentes []candados.Fuente, ruta string) bool {
	return slices.ContainsFunc(fuentes, func(f candados.Fuente) bool { return f.Ruta == ruta })
}

// TestUnFicheroUnTest: cada x.go de producción del alcance tiene su x_test.go (05 E-3), con
// las excepciones verificadas por candados.UnFicheroUnTest. El alcance es `alcance` MÁS los
// adaptadores bridge_<x>.go de bridgeDirs (D-F1-16), y ningún otro fichero de internal/arranque.
func TestUnFicheroUnTest(t *testing.T) {
	fuentes := recorrerAlcance(t, alcance)
	const testigo = "internal/pendiente/pendiente.go"
	if !vio(fuentes, testigo) {
		t.Fatalf("el recorrido (%d ficheros) no vio %s: raíz o alcance equivocados", len(fuentes), testigo)
	}
	bridges := walkBridges(t)
	// Testigo de los adaptadores: hoy existe bridge_contact.go, que muere en F8 (05 §4.2). Si
	// el recorrido no lo ve, el candado no está mirando los adaptadores. Cuando muera el último
	// adaptador, este testigo se retira con él.
	const bridgeWitness = "internal/arranque/bridge_contact.go"
	if !vio(bridges, bridgeWitness) {
		t.Fatalf("el recorrido de adaptadores (%d ficheros) no vio %s: bridgeDirs equivocado", len(bridges), bridgeWitness)
	}
	// Solo adaptadores: un fichero del arranque que no lo sea no puede colarse en el alcance.
	const notABridge = "internal/arranque/orquestador.go"
	if vio(bridges, notABridge) {
		t.Fatalf("el recorrido de adaptadores vio %s, que no es un bridge_<x>.go", notABridge)
	}
	fuentes = append(fuentes, bridges...)
	for _, v := range candados.UnFicheroUnTest(fuentes) {
		t.Errorf("un fichero, un test: %s", v)
	}
	t.Logf("recorridos=%d (adaptadores=%d)", len(fuentes), len(bridges))
}
