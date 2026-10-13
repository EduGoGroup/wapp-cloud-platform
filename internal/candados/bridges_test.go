package candados

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// bridgesTree es el árbol de prueba de WalkBridges: un directorio con un adaptador y su test
// (bridge_x.go), un fichero sin test que no es adaptador (otro.go), dos nombres parecidos que
// no lo son (bridge.go y su bridge_test.go), un directorio llamado bridge_dir.go y un
// subdirectorio con otro adaptador (sub/bridge_y.go).
const (
	bridgesTree = "testdata/bridges/arbol"
	bridgesDir  = "internal/arranque"
)

// walkBridgesCase llama a WalkBridges y exige que no falle.
func walkBridgesCase(t *testing.T, raiz, dir string, incluirTests bool) []Fuente {
	t.Helper()
	fuentes, err := WalkBridges(raiz, dir, incluirTests)
	if err != nil {
		t.Fatalf("WalkBridges(%q, %q, %v) = error %v", raiz, dir, incluirTests, err)
	}
	return fuentes
}

// TestWalkBridgesOnlyDirectBridges: salen SOLO los hijos directos bridge_<x>.go, con su test si
// se pide; ni otro.go, ni bridge.go, ni bridge_test.go, ni el directorio bridge_dir.go, ni el
// adaptador del subdirectorio.
func TestWalkBridgesOnlyDirectBridges(t *testing.T) {
	casos := []struct {
		nombre       string
		incluirTests bool
		quiero       []string
	}{
		{"sin tests: solo el adaptador de producción", false,
			[]string{"internal/arranque/bridge_x.go"}},
		{"con tests: el adaptador y su test, ordenados", true,
			[]string{"internal/arranque/bridge_x.go", "internal/arranque/bridge_x_test.go"}},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			got := rutas(walkBridgesCase(t, bridgesTree, bridgesDir, c.incluirTests))
			if !reflect.DeepEqual(got, c.quiero) {
				t.Errorf("Rutas = %v; quiero %v", got, c.quiero)
			}
		})
	}
}

// TestWalkBridgesTreeBites: el árbol de prueba contiene de verdad lo que WalkBridges debe
// callar. Sin esto, el caso anterior pasaría también contra un árbol al que le faltaran los
// ficheros trampa.
func TestWalkBridgesTreeBites(t *testing.T) {
	todo := rutas(recorrerCaso(t, bridgesTree, []string{bridgesDir}, true))
	for _, trampa := range []string{
		"internal/arranque/otro.go",
		"internal/arranque/bridge.go",
		"internal/arranque/bridge_test.go",
		"internal/arranque/bridge_dir.go/dentro.go",
		"internal/arranque/sub/bridge_y.go",
		"internal/arranque/sub/bridge_y_test.go",
	} {
		if !contieneRuta(todo, trampa) {
			t.Errorf("el árbol de prueba no tiene %s: el caso no demuestra nada (hay %v)", trampa, todo)
		}
	}
	// Y el directorio entero SÍ mordería: por eso no puede entrar en el alcance (D-F0-1).
	exigeViolacion(t, UnFicheroUnTest(recorrerCaso(t, bridgesTree, []string{bridgesDir}, true)),
		"internal/arranque/otro.go", "falta otro_test.go")
}

// contieneRuta dice si ruta está en rutas.
func contieneRuta(rutas []string, ruta string) bool {
	for _, r := range rutas {
		if r == ruta {
			return true
		}
	}
	return false
}

// TestWalkBridgesNotRecursive: llamada sobre el subdirectorio, devuelve SU adaptador; es la
// prueba de que sub/bridge_y.go no sale arriba por no ser hijo directo, no por su nombre.
func TestWalkBridgesNotRecursive(t *testing.T) {
	got := rutas(walkBridgesCase(t, bridgesTree, bridgesDir+"/sub", true))
	quiero := []string{"internal/arranque/sub/bridge_y.go", "internal/arranque/sub/bridge_y_test.go"}
	if !reflect.DeepEqual(got, quiero) {
		t.Errorf("Rutas = %v; quiero %v", got, quiero)
	}
}

// TestWalkBridgesSourceFields: las Fuente tienen la forma de las de Recorrer: EsTest, Paquete,
// Archivo con comentarios y un Fset común a la llamada.
func TestWalkBridgesSourceFields(t *testing.T) {
	fuentes := walkBridgesCase(t, bridgesTree, bridgesDir, true)
	if len(fuentes) != 2 {
		t.Fatalf("se esperaban 2 fuentes; hay %v", rutas(fuentes))
	}
	prod, test := fuentes[0], fuentes[1]
	if prod.EsTest || !test.EsTest {
		t.Errorf("EsTest = %v, %v; quiero false, true", prod.EsTest, test.EsTest)
	}
	for _, f := range fuentes {
		if f.Paquete != "arranque" {
			t.Errorf("%s: Paquete = %q; quiero \"arranque\"", f.Ruta, f.Paquete)
		}
		if f.Archivo == nil || len(f.Archivo.Comments) == 0 {
			t.Errorf("%s: sin comentarios; WalkBridges debe parsear con ParseComments", f.Ruta)
		}
		if f.Fset == nil || f.Fset != prod.Fset {
			t.Errorf("%s: el Fset no es el común a la llamada", f.Ruta)
		}
	}
}

// TestWalkBridgesFeedsTheLocks: lo que devuelve sirve a los candados tal cual. Con el test,
// UnFicheroUnTest no dice nada; sin él (incluirTests = false), nombra el adaptador y el test
// que falta, que es exactamente lo que D-F1-16 quiere que muerda.
func TestWalkBridgesFeedsTheLocks(t *testing.T) {
	exigeCero(t, UnFicheroUnTest(walkBridgesCase(t, bridgesTree, bridgesDir, true)))
	vs := UnFicheroUnTest(walkBridgesCase(t, bridgesTree, bridgesDir, false))
	exigeViolacion(t, vs, "internal/arranque/bridge_x.go", "falta bridge_x_test.go")
	if len(vs) != 1 {
		t.Errorf("se esperaba 1 violación; hay %d: %v", len(vs), vs)
	}
}

// TestWalkBridgesMissingDir: un dir o una raiz que no existen dan cero ficheros sin error, y
// también un directorio sin adaptadores.
func TestWalkBridgesMissingDir(t *testing.T) {
	casos := []struct {
		nombre, raiz, dir string
	}{
		{"dir inexistente", bridgesTree, "no/existe"},
		{"raiz inexistente", "testdata/no-existe", bridgesDir},
		{"directorio sin adaptadores", bridgesTree, bridgesDir + "/bridge_dir.go"},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			fuentes, err := WalkBridges(c.raiz, c.dir, true)
			if err != nil || fuentes == nil || len(fuentes) != 0 {
				t.Errorf("WalkBridges = (%v, %v); quiero (0 ficheros no nil, nil)", rutas(fuentes), err)
			}
		})
	}
}

// TestWalkBridgesErrors: un adaptador que no parsea es un error que nombra la ruta, sin
// resultado parcial; un fichero roto que NO es adaptador no molesta; y un dir que es un
// fichero es un error de lectura, no «cero adaptadores».
func TestWalkBridgesErrors(t *testing.T) {
	raiz := t.TempDir()
	dir := filepath.Join(raiz, "internal", "arranque")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	escribir := func(nombre, src string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, nombre), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	escribir("bridge_a.go", "package arranque\n")
	escribir("roto.go", "package arranque\nfunc {")

	got := rutas(walkBridgesCase(t, raiz, bridgesDir, true))
	if quiero := []string{"internal/arranque/bridge_a.go"}; !reflect.DeepEqual(got, quiero) {
		t.Errorf("Rutas = %v; quiero %v (roto.go no es un adaptador y no se parsea)", got, quiero)
	}

	escribir("bridge_roto.go", "package arranque\nfunc {")
	fuentes, err := WalkBridges(raiz, bridgesDir, true)
	if err == nil {
		t.Fatalf("WalkBridges sobre un adaptador roto no dio error; devolvió %v", rutas(fuentes))
	}
	if !strings.Contains(err.Error(), "internal/arranque/bridge_roto.go") {
		t.Errorf("el error %q no nombra el adaptador roto", err)
	}
	if len(fuentes) != 0 {
		t.Errorf("con error no hay resultado parcial; devolvió %v", rutas(fuentes))
	}

	fuentes, err = WalkBridges(raiz, bridgesDir+"/bridge_a.go", true)
	if err == nil || len(fuentes) != 0 {
		t.Errorf("WalkBridges sobre un fichero = (%v, %v); quiero error", rutas(fuentes), err)
	}
}

// TestIsBridgeFile: el criterio del nombre, exacto.
func TestIsBridgeFile(t *testing.T) {
	casos := []struct {
		name      string
		withTests bool
		quiero    bool
	}{
		{"bridge_contact.go", false, true},
		{"bridge_contact.go", true, true},
		{"bridge_contact_test.go", true, true},
		{"bridge_contact_test.go", false, false},
		{"bridge_a_b.go", false, true},
		{"bridge.go", true, false},
		{"bridge_.go", true, false},
		{"bridge_test.go", true, false},
		{"bridge__test.go", true, false},
		{"Bridge_x.go", true, false},
		{"mibridge_x.go", true, false},
		{"bridge_x.txt", true, false},
		{"bridge_x", true, false},
		{"otro.go", true, false},
	}
	for _, c := range casos {
		if got := isBridgeFile(c.name, c.withTests); got != c.quiero {
			t.Errorf("isBridgeFile(%q, %v) = %v; quiero %v", c.name, c.withTests, got, c.quiero)
		}
	}
}
