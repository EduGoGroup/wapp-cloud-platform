package modulos

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// Los dos restos del método «contrato sin lógica → test en rojo» que el relevo retiró (F10 ·
// T10.12, D-F10-3). Se COMPONEN por concatenación para que este fichero no contenga ninguno:
// el candado se recorre a sí mismo, y un `grep` de cualquiera de los dos debe dar cero en todo
// el repo.
const (
	pendingPackage = "pendiente"
	// pendingCall es el selector con el que un contrato sin lógica rellenaba su cuerpo.
	pendingCall = pendingPackage + "." + "Implementar"
)

// pendingTag reconoce la etiqueta de compilación tras la que nacía un test en rojo, sola o
// combinada con otras (`a && etiqueta`, `!etiqueta`), en la forma nueva y en la vieja.
var pendingTag = regexp.MustCompile(`^//\s*(go:build|\+build)\s.*\b` + pendingPackage + `\b`)

// noPendingDirs son TODOS los directorios con código Go del repo. Al contrario que los demás
// candados de este paquete, este NO salta `testdata` ni distingue producción de tests: un resto
// en un árbol de prueba también cuenta.
var noPendingDirs = []string{"internal", "cmd", "test"}

// findPending lee, línea a línea y como TEXTO (sin parsear: una etiqueta de compilación o un
// fichero que no compila no esconden nada), cada .go bajo root/dir y devuelve un hallazgo
// «ruta:línea: motivo» por cada línea con un resto, en el orden del recorrido, y las rutas de
// los ficheros que leyó. Las rutas van con barras y relativas a root.
func findPending(root string, dirs []string) (findings, read []string, err error) {
	tree := os.DirFS(root)
	for _, dir := range dirs {
		walkErr := fs.WalkDir(tree, dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(p, ".go") {
				return nil
			}
			content, err := fs.ReadFile(tree, p)
			if err != nil {
				return err
			}
			read = append(read, p)
			findings = append(findings, scanPending(p, string(content))...)
			return nil
		})
		if walkErr != nil {
			return nil, nil, walkErr
		}
	}
	return findings, read, nil
}

// scanPending devuelve los hallazgos del texto de un fichero; name es la ruta con la que se
// informan.
func scanPending(name, content string) []string {
	var found []string
	for i, line := range strings.Split(content, "\n") {
		if strings.Contains(line, pendingCall) {
			found = append(found, fmt.Sprintf("%s:%d: nombra %s", name, i+1, pendingCall))
		}
		if pendingTag.MatchString(strings.TrimSpace(line)) {
			found = append(found, fmt.Sprintf("%s:%d: etiqueta de compilación %s", name, i+1, pendingPackage))
		}
	}
	return found
}

// TestNoPending: tras el relevo no queda en el repo ningún contrato sin lógica ni ningún test
// tras la etiqueta del rojo, ni siquiera en un árbol de prueba (D-F10-3). El paquete que los
// sostenía se borró: quien reintroduzca uno de los dos restos rompe aquí antes que al compilar.
//
// Exige haber leído un fichero de producción, un test y uno de `testdata`: con la raíz o los
// directorios equivocados, o saltándose los árboles de prueba, no puede pasar en verde.
func TestNoPending(t *testing.T) {
	findings, read, err := findPending(raizRepo, noPendingDirs)
	if err != nil {
		t.Fatalf("findPending(%q, %v) = error %v", raizRepo, noPendingDirs, err)
	}
	for _, witness := range []string{
		"internal/candados/candados.go",                                  // producción
		"internal/modulos/no_pending_test.go",                            // test: este mismo fichero
		"internal/candados/testdata/recorrer/arbol/internal/a/a_test.go", // testdata
		"cmd/server/main.go",
		"test/procesos/doc.go",
	} {
		if !slices.Contains(read, witness) {
			t.Fatalf("el recorrido (%d ficheros) no leyó %s: raíz o directorios equivocados", len(read), witness)
		}
	}
	for _, f := range findings {
		t.Errorf("resto del método de contratos: %s", f)
	}
	t.Logf("recorridos=%d", len(read))
}

// TestNoPendingBites prueba el detector contra un árbol sintético: señala el selector y la
// etiqueta —en producción, en un test y dentro de `testdata`, sola o combinada, en la forma
// nueva y en la vieja— y no señala la palabra suelta, otra etiqueta ni un fichero que no es .go.
func TestNoPendingBites(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tag := "//go:build " + pendingPackage
	write("internal/a/a.go", "package a\n\nfunc A() {\n\tpanic("+pendingCall+"(\"a.A\"))\n}\n")
	write("internal/a/a_test.go", tag+"\n\npackage a\n")
	write("internal/a/testdata/x/x.go", "package x\n\n// "+pendingCall+" en un comentario también cuenta.\n")
	write("internal/b/b_test.go", "//go:build integracion && !"+pendingPackage+"\n// +build "+pendingPackage+"\n\npackage b\n")
	write("cmd/c/main.go", "this is not Go: "+pendingCall+"\n")
	// Lo que NO es un resto.
	write("internal/ok/ok.go", "//go:build otraetiqueta\n\npackage ok\n\n// La tarea queda "+pendingPackage+" de revisar.\nvar s = \""+pendingPackage+"\"\n")
	write("internal/ok/"+pendingPackage+"s.go", "//go:build "+pendingPackage+"s\n\npackage ok\n")
	write("internal/ok/notas.txt", tag+"\n"+pendingCall+"\n")
	write("fuera/f.go", "package f\n\nvar _ = "+pendingCall+"\n")

	got, read, err := findPending(root, []string{"internal", "cmd"})
	if err != nil {
		t.Fatalf("findPending: %v", err)
	}
	want := []string{
		"internal/a/a.go:4: nombra " + pendingCall,
		"internal/a/a_test.go:1: etiqueta de compilación " + pendingPackage,
		"internal/a/testdata/x/x.go:3: nombra " + pendingCall,
		"internal/b/b_test.go:1: etiqueta de compilación " + pendingPackage,
		"internal/b/b_test.go:2: etiqueta de compilación " + pendingPackage,
		"cmd/c/main.go:1: nombra " + pendingCall,
	}
	if !slices.Equal(got, want) {
		t.Errorf("hallazgos =\n  %s\nquiero\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	if len(read) != 7 {
		t.Errorf("ficheros leídos = %d (%v); quiero 7 (los .go de internal y cmd, testdata incluido)", len(read), read)
	}
}

// TestNoPendingPasses: un árbol sin restos no da hallazgos, y un directorio que no existe es un
// error, no un recorrido vacío en verde.
func TestNoPendingPasses(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "internal", "a")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	src := "//go:build integracion\n\npackage a\n\nfunc A() { panic(\"no implementado\") }\n"
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	got, read, err := findPending(root, []string{"internal"})
	if err != nil || len(got) != 0 || !slices.Equal(read, []string{"internal/a/a.go"}) {
		t.Errorf("findPending = %v, %v, %v; quiero ningún hallazgo, solo internal/a/a.go y sin error", got, read, err)
	}
	if _, _, err := findPending(root, []string{"noexiste"}); err == nil {
		t.Error("findPending sobre un directorio inexistente no dio error")
	}
}
