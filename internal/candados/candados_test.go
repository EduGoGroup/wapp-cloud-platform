//go:build pendiente

package candados

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// ── Ayudas comunes a los tests de todos los candados ─────────────────────────────────────

// recorrerCaso recorre un árbol de prueba y exige que haya ficheros: un caso `muerde` o
// `pasa` que no recorre nada pasaría por no mirar (tareas.md T0.6).
func recorrerCaso(t *testing.T, raiz string, dirs []string, incluirTests bool) []Fuente {
	t.Helper()
	fuentes, err := Recorrer(raiz, dirs, incluirTests)
	if err != nil {
		t.Fatalf("Recorrer(%q, %v) = error %v", raiz, dirs, err)
	}
	if len(fuentes) == 0 {
		t.Fatalf("Recorrer(%q, %v) recorrió 0 ficheros: el caso no mira nada", raiz, dirs)
	}
	return fuentes
}

// exigeViolacion exige al menos una violación en fichero cuyo Motivo contenga cada trozo.
func exigeViolacion(t *testing.T, vs []Violacion, fichero string, trozos ...string) {
	t.Helper()
	for _, v := range vs {
		if v.Fichero == fichero && contieneTodos(v.Motivo, trozos) {
			return
		}
	}
	t.Errorf("falta una violación en %s con motivo que contenga %q; hay: %v", fichero, trozos, vs)
}

// exigeNingunaEn exige que ninguna violación nombre fichero.
func exigeNingunaEn(t *testing.T, vs []Violacion, fichero string) {
	t.Helper()
	for _, v := range vs {
		if v.Fichero == fichero {
			t.Errorf("violación inesperada: %v", v)
		}
	}
}

// exigeCero exige cero violaciones.
func exigeCero(t *testing.T, vs []Violacion) {
	t.Helper()
	if len(vs) != 0 {
		t.Errorf("se esperaban 0 violaciones; hay %d: %v", len(vs), vs)
	}
}

// exigeOrdenadas exige el orden común de los candados: Fichero y, a igual Fichero, Motivo.
func exigeOrdenadas(t *testing.T, vs []Violacion) {
	t.Helper()
	ok := sort.SliceIsSorted(vs, func(i, j int) bool {
		if vs[i].Fichero != vs[j].Fichero {
			return vs[i].Fichero < vs[j].Fichero
		}
		return vs[i].Motivo < vs[j].Motivo
	})
	if !ok {
		t.Errorf("violaciones fuera de orden (Fichero, Motivo): %v", vs)
	}
}

func contieneTodos(s string, trozos []string) bool {
	for _, tr := range trozos {
		if !strings.Contains(s, tr) {
			return false
		}
	}
	return true
}

func rutas(fuentes []Fuente) []string {
	out := make([]string, 0, len(fuentes))
	for _, f := range fuentes {
		out = append(out, f.Ruta)
	}
	return out
}

// ── Violacion ────────────────────────────────────────────────────────────────────────────

// TestViolacionString: el formato es exactamente "fichero: motivo".
func TestViolacionString(t *testing.T) {
	v := Violacion{Fichero: "internal/x/y.go", Motivo: "falta y_test.go"}
	if got, quiero := v.String(), "internal/x/y.go: falta y_test.go"; got != quiero {
		t.Errorf("String() = %q; quiero %q", got, quiero)
	}
}

// ── Recorrer ─────────────────────────────────────────────────────────────────────────────

const arbolRecorrer = "testdata/recorrer/arbol"

// TestRecorrerRutasYOrden: rutas con barras relativas a raiz, ordenadas, sin testdata ni
// ficheros que no son .go, sin repetir aunque los dirs se solapen, y con o sin tests.
func TestRecorrerRutasYOrden(t *testing.T) {
	casos := []struct {
		nombre       string
		dirs         []string
		incluirTests bool
		quiero       []string
	}{
		{"sin tests se omiten los _test.go y se salta testdata", []string{"internal"}, false,
			[]string{"internal/a/a.go", "internal/a/b/b.go"}},
		{"con tests se incluyen los _test.go, también los de etiqueta pendiente", []string{"internal"}, true,
			[]string{"internal/a/a.go", "internal/a/a_test.go", "internal/a/b/b.go"}},
		{"dirs solapados no repiten ficheros", []string{"internal", "internal/a"}, false,
			[]string{"internal/a/a.go", "internal/a/b/b.go"}},
		{"varios dirs dan un resultado ordenado por Ruta", []string{"otro", "internal/a/b"}, false,
			[]string{"internal/a/b/b.go", "otro/c.go"}},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			got := rutas(recorrerCaso(t, arbolRecorrer, c.dirs, c.incluirTests))
			if !reflect.DeepEqual(got, c.quiero) {
				t.Errorf("Rutas = %v; quiero %v", got, c.quiero)
			}
		})
	}
}

// TestRecorrerCamposDeFuente: EsTest, Paquete, Archivo con comentarios y un Fset compartido.
func TestRecorrerCamposDeFuente(t *testing.T) {
	fuentes := recorrerCaso(t, arbolRecorrer, []string{"internal"}, true)
	quiero := map[string]struct {
		esTest  bool
		paquete string
	}{
		"internal/a/a.go":      {false, "a"},
		"internal/a/a_test.go": {true, "a"},
		"internal/a/b/b.go":    {false, "b"},
	}
	for _, f := range fuentes {
		q, ok := quiero[f.Ruta]
		if !ok {
			t.Errorf("Ruta inesperada %q", f.Ruta)
			continue
		}
		if f.EsTest != q.esTest || f.Paquete != q.paquete {
			t.Errorf("%s: EsTest=%v Paquete=%q; quiero %v %q", f.Ruta, f.EsTest, f.Paquete, q.esTest, q.paquete)
		}
		if f.Archivo == nil || f.Fset == nil {
			t.Fatalf("%s: Archivo o Fset nil", f.Ruta)
		}
		if len(f.Archivo.Comments) == 0 {
			t.Errorf("%s: sin comentarios; Recorrer debe parsear con ParseComments", f.Ruta)
		}
		if f.Fset != fuentes[0].Fset {
			t.Errorf("%s: Fset distinto; todas las Fuente de una llamada comparten FileSet", f.Ruta)
		}
	}
}

// TestRecorrerDirInexistente: un dir o una raiz que no existen dan cero ficheros sin error.
func TestRecorrerDirInexistente(t *testing.T) {
	casos := []struct {
		nombre string
		raiz   string
		dirs   []string
	}{
		{"dir inexistente", arbolRecorrer, []string{"no/existe"}},
		{"raiz inexistente", "testdata/recorrer/no-existe", []string{"internal"}},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			fuentes, err := Recorrer(c.raiz, c.dirs, true)
			if err != nil || len(fuentes) != 0 {
				t.Errorf("Recorrer = (%v, %v); quiero (0 ficheros, nil)", rutas(fuentes), err)
			}
		})
	}
}

// TestRecorrerNoParsea: un fichero que no parsea es un error que nombra la ruta, sin
// resultado parcial. Se escribe en un directorio temporal: un .go roto en testdata rompería
// gofmt y los barridos AST viejos.
func TestRecorrerNoParsea(t *testing.T) {
	raiz := t.TempDir()
	dir := filepath.Join(raiz, "internal", "x")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bien.go"), []byte("package x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "roto.go"), []byte("package x\nfunc {\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fuentes, err := Recorrer(raiz, []string{"internal"}, false)
	if err == nil {
		t.Fatalf("Recorrer sobre un fichero roto no dio error; devolvió %v", rutas(fuentes))
	}
	if !strings.Contains(err.Error(), "roto.go") {
		t.Errorf("el error %q no nombra roto.go", err)
	}
	if len(fuentes) != 0 {
		t.Errorf("con error no hay resultado parcial; devolvió %v", rutas(fuentes))
	}
}
