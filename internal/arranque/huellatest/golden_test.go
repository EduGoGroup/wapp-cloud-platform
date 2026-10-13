package huellatest

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// goldenMinimal son los bytes exactos de WriteGolden para huellaMinima y huellaCompleta
// juntas: la cabecera y las líneas ordenadas, con la goroutine repetida dos veces.
const goldenMinimal = goldenHeader +
	"m2m\tentorno\t-\ta.go:1 os.Getenv\n" +
	"m2m\tgoroutine\t-\tp.T.Run\n" +
	"m2m\tgoroutine\t-\tp.T.Run\n" +
	"m2m\thook\t-\tInstrumentHTTP\n" +
	"m2m\tmetrica\t-\twapp_a\n" +
	"m2m\tmetrica\t-\twapp_z\n" +
	"m2m\trpc\t:8102\te/R\n" +
	"m2m\truta\t:8100\t/a\n" +
	"m2m\truta\t:8100\t/b\n" +
	"minimo\tmetrica\t-\twapp_x\n" +
	"minimo\trpc\t:8101\ts/M\n" +
	"minimo\truta\t:8100\t/healthz\n" +
	"minimo\truta\t:8103\tGET /api/v1/x\n"

// readFile lee un fichero de un directorio temporal del test.
func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := fs.ReadFile(os.DirFS(filepath.Dir(path)), filepath.Base(path))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestWriteGolden: bytes exactos (cabecera, una entrada por línea, todas ordenadas, las
// repetidas tantas veces como aparecen) y deterministas: ni el orden de las huellas ni el de
// sus listas cambia el fichero. Escribir trunca.
func TestWriteGolden(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.golden"), filepath.Join(dir, "b.golden")
	if err := WriteGolden(a, []Huella{huellaMinima(), huellaMinima2()}); err != nil {
		t.Fatalf("WriteGolden (primera): %v", err)
	}
	if err := WriteGolden(a, []Huella{huellaMinima(), huellaCompleta()}); err != nil {
		t.Fatalf("WriteGolden(a): %v", err)
	}
	if err := WriteGolden(b, []Huella{huellaCompleta(), huellaMinima()}); err != nil {
		t.Fatalf("WriteGolden(b): %v", err)
	}
	if got := readFile(t, a); got != goldenMinimal {
		t.Errorf("WriteGolden escribió\n%s\nquiere\n%s", got, goldenMinimal)
	}
	if readFile(t, a) != readFile(t, b) {
		t.Error("el orden de las huellas cambia los bytes de la dorada")
	}
}

// huellaMinima2 es otra huella, más larga que la de TestWriteGolden: sirve para ver que la
// segunda escritura trunca.
func huellaMinima2() Huella {
	return Huella{Perfil: "otro", Metricas: []string{"wapp_1", "wapp_2", "wapp_3", "wapp_4", "wapp_5", "wapp_6", "wapp_7", "wapp_8", "wapp_9", "wapp_10", "wapp_11", "wapp_12", "wapp_13", "wapp_14", "wapp_15", "wapp_16"}}
}

// TestReadGolden: ida y vuelta. Los perfiles vuelven en el orden del fichero y las listas
// ordenadas (multiconjuntos: la repetida, dos veces); una lista sin entradas vuelve nil; y
// las líneas vacías y los comentarios se ignoran.
func TestReadGolden(t *testing.T) {
	path := filepath.Join(t.TempDir(), "huella.golden")
	withEmpty := huellaMinima()
	withEmpty.Goroutines = []string{}
	if err := WriteGolden(path, []Huella{withEmpty, huellaCompleta()}); err != nil {
		t.Fatalf("WriteGolden: %v", err)
	}
	escribirFichero(t, path, readFile(t, path)+"\n# un comentario al final\n\n")
	got, err := ReadGolden(path)
	if err != nil {
		t.Fatalf("ReadGolden: %v", err)
	}
	want := []Huella{
		{
			Perfil:     "m2m",
			Rutas:      map[string][]string{":8100": {"/a", "/b"}},
			RPC:        map[string][]string{":8102": {"e/R"}},
			Metricas:   []string{"wapp_a", "wapp_z"},
			Goroutines: []string{"p.T.Run", "p.T.Run"},
			Hooks:      []string{"InstrumentHTTP"},
			Entorno:    []string{"a.go:1 os.Getenv"},
		},
		huellaMinima(),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ReadGolden(WriteGolden(hs)) = %#v\nquiere %#v", got, want)
	}
	for i, h := range []Huella{huellaCompleta(), huellaMinima()} {
		if d := Diferencia(h, got[i]); len(d) != 0 {
			t.Errorf("la huella %q leída difiere de la escrita: %q", h.Perfil, d)
		}
	}
}

// TestReadGoldenErrors: un fichero que no existe envuelve fs.ErrNotExist y nombra la ruta;
// una línea mal formada nombra la ruta y su número de línea.
func TestReadGoldenErrors(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "noexiste.golden")
	if got, err := ReadGolden(missing); !errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), "noexiste.golden") {
		t.Errorf("ReadGolden(no existe) = %v, %v; quiere fs.ErrNotExist nombrando la ruta", got, err)
	}
	cases := map[string]string{
		"tres campos":               "p\truta\t:8100\n",
		"cinco campos":              "p\truta\t:8100\t/a\t/b\n",
		"perfil vacío":              "\truta\t:8100\t/a\n",
		"valor vacío":               "p\truta\t:8100\t\n",
		"clase desconocida":         "p\tsocket\t-\tx\n",
		"ruta sin listener":         "p\truta\t-\t/a\n",
		"rpc sin listener":          "p\trpc\t-\ts/M\n",
		"métrica con listener":      "p\tmetrica\t:8100\twapp_x\n",
		"goroutine con listener":    "p\tgoroutine\t:8100\tp.T.Run\n",
		"separado por espacios":     "p ruta :8100 /a\n",
		"la línea mala es la terce": "p\truta\t:8100\t/a\n",
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "mala.golden")
			escribirFichero(t, path, "# cabecera\np\tmetrica\t-\twapp_ok\n"+bad)
			got, err := ReadGolden(path)
			if name == "la línea mala es la terce" {
				if err != nil || len(got) != 1 {
					t.Fatalf("ReadGolden(dorada válida) = %v, %v; quiere una huella y nil", got, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "mala.golden:3:") {
				t.Errorf("ReadGolden = %v, %v; quiere un error que nombre mala.golden:3", got, err)
			}
		})
	}
}

// TestWriteGoldenErrors: lo que no se podría volver a leer no se escribe; y WriteGolden no
// crea directorios.
func TestWriteGoldenErrors(t *testing.T) {
	dir := t.TempDir()
	cases := map[string][]Huella{
		"perfil vacío":         {{Metricas: []string{"wapp_x"}}},
		"perfil repetido":      {huellaMinima(), huellaMinima()},
		"perfil comentario":    {{Perfil: "#p", Metricas: []string{"wapp_x"}}},
		"perfil con TAB":       {{Perfil: "a\tb", Metricas: []string{"wapp_x"}}},
		"valor con TAB":        {{Perfil: "p", Metricas: []string{"wapp\tx"}}},
		"valor con salto":      {{Perfil: "p", Hooks: []string{"a\nb"}}},
		"valor vacío":          {{Perfil: "p", Goroutines: []string{""}}},
		"listener vacío":       {{Perfil: "p", Rutas: map[string][]string{"": {"/a"}}}},
		"listener con TAB":     {{Perfil: "p", RPC: map[string][]string{":81\t01": {"s/M"}}}},
		"ruta con TAB":         {{Perfil: "p", Rutas: map[string][]string{":8100": {"GET\t/a"}}}},
		"entorno con salto":    {{Perfil: "p", Entorno: []string{"a.go:1\nos.Getenv"}}},
		"segundo perfil vacío": {huellaMinima(), {Metricas: []string{"wapp_x"}}},
	}
	for name, hs := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(name, " ", "-")+".golden")
			if err := WriteGolden(path, hs); err == nil {
				t.Error("WriteGolden = nil; quiere error")
			}
			if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("WriteGolden con error dejó el fichero escrito (Stat: %v)", err)
			}
		})
	}
	if err := WriteGolden(filepath.Join(dir, "sin", "padre.golden"), nil); err == nil {
		t.Error("WriteGolden(padre inexistente) = nil; quiere error (no crea directorios)")
	}
}
