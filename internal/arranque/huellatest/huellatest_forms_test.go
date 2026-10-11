package huellatest

// Las formas que el contrato de huellatest.go nombra y que ningún árbol de testdata traía
// (D-42): la llamada que no es Handle, el patrón con host y con {$}, el candidato que no se
// puede sondear, los receptores y las funciones genéricas, el valor de función, el método
// del universo y el paquete que no es "os". Los árboles nacen en t.TempDir(): un paquete de
// Goroutines necesita además su go.mod, porque el importador corre `go list` dentro.

import (
	"net/http"
	"path/filepath"
	"slices"
	"testing"
)

// ── Candidatos ───────────────────────────────────────────────────────────────

// TestCandidatosIgnoresCallsThatAreNotHandle: candidato es solo el primer argumento de
// X.Handle o X.HandleFunc. Otro método con un literal delante (mux.Get), una función suelta
// y un Handle sin receptor no lo son, aunque su argumento parezca una ruta.
func TestCandidatosIgnoresCallsThatAreNotHandle(t *testing.T) {
	raiz := t.TempDir()
	escribirFichero(t, filepath.Join(raiz, "p", "p.go"), `package p

func montar(mux *enrutador) {
	mux.Handle("/si", nil)
	mux.HandleFunc("GET /tambien", nil)
	mux.Get("/otro-metodo", nil)
	registrar("/funcion-suelta")
	Handle("/sin-receptor", nil)
}
`)
	got, err := Candidatos(raiz, "p")
	if err != nil {
		t.Fatalf("Candidatos: error %v", err)
	}
	if want := []string{"/si", "GET /tambien"}; !slices.Equal(got, want) {
		t.Errorf("Candidatos = %q, quiere %q (sin mux.Get, sin registrar(…) y sin el Handle sin receptor)", got, want)
	}
}

// ── Rutas ────────────────────────────────────────────────────────────────────

// probeLog envuelve h y apunta, por sonda, "<método> <host> <ruta>".
func probeLog(h http.Handler, sondas *[]string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*sondas = append(*sondas, r.Method+" "+r.Host+" "+r.URL.Path)
		h.ServeHTTP(w, r)
	})
}

func statusOK(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

// TestRutasProbesAHostPattern: un candidato "[MÉTODO ][HOST]/RUTA" se sondea con ESE host y
// con la ruta sin él. El mux solo casa un patrón con host si la petición lo trae: el mismo
// camino bajo otro host no está montado.
func TestRutasProbesAHostPattern(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("admin.wapp.test/h", statusOK)
	var sondas []string
	got := Rutas(probeLog(mux, &sondas), []string{"admin.wapp.test/h", "GET otro.wapp.test/h"})
	if want := []string{"admin.wapp.test/h"}; !slices.Equal(got, want) {
		t.Errorf("Rutas = %q, quiere %q (el patrón con host, montado; el mismo camino bajo otro host, no)", got, want)
	}
	if want := []string{"GET admin.wapp.test /h", "GET otro.wapp.test /h"}; !slices.Equal(sondas, want) {
		t.Errorf("sondas = %q, quiere %q (el host va en Host, no en la ruta)", sondas, want)
	}
}

// TestRutasProbesTheEndAnchor: el comodín {$} se sustituye por nada, no por su nombre: la
// sonda de "GET /raiz/{$}" es GET /raiz/, que es lo único que ese patrón casa.
func TestRutasProbesTheEndAnchor(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /raiz/{$}", statusOK)
	var sondas []string
	got := Rutas(probeLog(mux, &sondas), []string{"GET /raiz/{$}"})
	if want := []string{"GET /raiz/{$}"}; !slices.Equal(got, want) {
		t.Errorf("Rutas = %q, quiere %q", got, want)
	}
	if want := []string{"GET example.com /raiz/"}; !slices.Equal(sondas, want) {
		t.Errorf("sondas = %q, quiere %q ({$} → nada)", sondas, want)
	}
}

// TestRutasSkipsACandidateItCannotProbe: un candidato que no da una URL válida no se puede
// sondear, así que no está montado; no tumba la huella y el handler ni se entera, aunque
// respondiera 200 a todo. El candidato sano de detrás se sondea igual.
func TestRutasSkipsACandidateItCannotProbe(t *testing.T) {
	var sondas []string
	h := probeLog(http.HandlerFunc(statusOK), &sondas)
	got := Rutas(h, []string{"GET /a%zz", "/sano"})
	if want := []string{"/sano"}; !slices.Equal(got, want) {
		t.Errorf("Rutas = %q, quiere %q (el escape %%zz no es una URL: sin sonda, no montada)", got, want)
	}
	if want := []string{"GET example.com /sano"}; !slices.Equal(sondas, want) {
		t.Errorf("sondas = %q, quiere %q (una sola: la del candidato sano)", sondas, want)
	}
}

// ── Goroutines ───────────────────────────────────────────────────────────────

// goModule escribe en un directorio temporal un módulo de un solo paquete, sin dependencias
// fuera de la biblioteca estándar, y devuelve su ruta.
func goModule(t *testing.T, nombre, fuente string) string {
	t.Helper()
	dir := t.TempDir()
	escribirFichero(t, filepath.Join(dir, "go.mod"), "module "+nombre+"\n\ngo 1.26\n")
	escribirFichero(t, filepath.Join(dir, nombre+".go"), fuente)
	return dir
}

// TestGoroutinesNormalizesTheRemainingForms: el receptor se escribe sin puntero, sin
// paréntesis y sin parámetros de tipo (T, *T, T[K], T[K, V]); una función genérica
// instanciada se nombra como la función; un valor de función no tiene nombre estático y se
// escribe la expresión, atribuida a su contenedora; y un método del universo (error.Error)
// sale sin paquete.
func TestGoroutinesNormalizesTheRemainingForms(t *testing.T) {
	dir := goModule(t, "formas", `package formas

type Cola[T any] struct{}

func (c *Cola[T]) Vaciar() { go func() {}() }

type Par[K comparable, V any] struct{}

func (p Par[K, V]) Lanzar() { go func() {}() }

type Suelto struct{}

func (s (*Suelto)) Lanzar() { go func() {}() }

func repetir[T any](T) {}

func emparejar[K, V any](K, V) {}

func Instanciadas() {
	go repetir[int](1)
	go emparejar[int, string](1, "")
}

func Valor(fn func()) { go fn() }

func Universo(err error) { go err.Error() }
`)
	got, err := Goroutines(dir)
	if err != nil {
		t.Fatalf("Goroutines: error %v", err)
	}
	want := []string{
		"error.Error",               // método del universo: sin paquete
		"formas.Cola.Vaciar.func",   // receptor *Cola[T]
		"formas.Par.Lanzar.func",    // receptor Par[K, V]
		"formas.Suelto.Lanzar.func", // receptor (*Suelto)
		"formas.Valor.fn",           // valor de función: la expresión, en su contenedora
		"formas.emparejar",          // emparejar[int, string]
		"formas.repetir",            // repetir[int]
	}
	if !slices.Equal(got, want) {
		t.Errorf("Goroutines = %q\nquiere     %q", got, want)
	}
}

// TestGoroutinesTypeErrorInsideAModule: un paquete que no comprueba tipos es un error aunque
// viva en un módulo válido. El caso «no comprueba tipos» de TestGoroutinesError escribe su
// fichero sin go.mod, y ahí `go list` falla por el módulo que falta antes de mirar un solo
// tipo: aquí lo único que está mal es el tipo.
func TestGoroutinesTypeErrorInsideAModule(t *testing.T) {
	sano := goModule(t, "sano", "package sano\n\nfunc g() {}\n\nfunc F() { go g() }\n")
	if got, err := Goroutines(sano); err != nil || !slices.Equal(got, []string{"sano.g"}) {
		t.Fatalf("Goroutines(módulo sano) = %q, %v; quiere [sano.g]: el arnés no vale si lo sano falla", got, err)
	}
	roto := goModule(t, "roto", "package roto\n\nfunc F() { go g() }\n")
	if got, err := Goroutines(roto); err == nil {
		t.Errorf("Goroutines(g sin declarar) = %q, nil; quiere error, no una huella parcial", got)
	}
}

// ── Entorno ──────────────────────────────────────────────────────────────────

// TestEntornoIgnoresPackagesThatAreNotOs: el paquete se reconoce por su import "os". Un
// Getenv o un LookupEnv de otro paquete, con alias o sin él, no es una lectura del entorno
// que el contrato cuente; la del "os" de verdad, en el mismo fichero, sí.
func TestEntornoIgnoresPackagesThatAreNotOs(t *testing.T) {
	raiz := t.TempDir()
	escribirFichero(t, filepath.Join(raiz, "x", "x.go"), `package x

import (
	"os"
	"syscall"

	ajeno "example.com/config"
)

func leer() {
	_, _ = syscall.Getenv("A")
	_ = ajeno.Getenv("B")
	_, _ = ajeno.LookupEnv("C")
	_ = os.Getenv("D")
}
`)
	got, err := Entorno(raiz, "x")
	if err != nil {
		t.Fatalf("Entorno: error %v", err)
	}
	if want := []string{"x/x.go:14 os.Getenv"}; !slices.Equal(got, want) {
		t.Errorf("Entorno = %q, quiere %q (ni syscall.Getenv ni los del alias ajeno)", got, want)
	}
}
