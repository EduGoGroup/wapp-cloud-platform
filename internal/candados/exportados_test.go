package candados

import (
	"strings"
	"testing"
)

// TestExportadosCubiertosMuerde: un exportado citado solo en un comentario, o solo en un
// literal de cadena, no está cubierto; un método exportado se nombra "Tipo.Metodo".
func TestExportadosCubiertosMuerde(t *testing.T) {
	fuentes := recorrerCaso(t, "testdata/exportados/muerde", []string{"internal"}, true)
	vs := ExportadosCubiertos(fuentes)

	const fichero = "internal/modulos/m/cosa/cosa.go"
	casos := []struct {
		nombre  string
		simbolo string
	}{
		{"constante citada solo en un comentario del test", "Limite"},
		{"método exportado citado solo en un literal", "Cosa.Medir"},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			exigeViolacion(t, vs, fichero, c.simbolo, "cosa_test.go")
		})
	}
	if len(vs) != len(casos) {
		t.Errorf("se esperaban %d violaciones; hay %d: %v", len(casos), len(vs), vs)
	}
	for _, v := range vs {
		if strings.Contains(v.Motivo, "Hacer") {
			t.Errorf("Hacer aparece como identificador en el test y no debe violar: %v", v)
		}
	}
	exigeOrdenadas(t, vs)
}

// TestExportadosCubiertosPasa: un test en rojo (etiqueta pendiente) del paquete externo que
// nombra todo por selector cuenta; campos y no exportados no se exigen; un x.go sin test no
// es asunto de este candado; un paquete …test queda fuera (D-F1-3 = sí).
func TestExportadosCubiertosPasa(t *testing.T) {
	fuentes := recorrerCaso(t, "testdata/exportados/pasa", []string{"internal"}, true)
	exigeCero(t, ExportadosCubiertos(fuentes))
}

// TestExportadosCubiertosBordes: receptores puntero, genéricos y entre paréntesis se nombran
// por su tipo; los métodos de tipos no exportados (o de receptores que no son un tipo con
// nombre) no se exigen; var y const en grupo exigen cada nombre exportado; un Ident suelto
// del mismo paquete cuenta; varios ficheros salen ordenados por Fichero y Motivo.
func TestExportadosCubiertosBordes(t *testing.T) {
	fuentes := []Fuente{
		fuenteEnMemoria(t, "internal/modulos/m/b/b.go", `package b

import "strings"

var _ = strings.ToUpper

type (
	Pila[T any]       struct{ v []T }
	Par[K comparable, V any] struct{}
	oculto            struct{}
)

var Uno, dos, Tres = 1, 2, 3

const (
	Alfa = iota
	beta
)

func (p *Pila[T]) Meter(x T)  { p.v = append(p.v, x) }
func (p Par[K, V]) Clave() K  { var k K; return k }
func (o *oculto) Visible()    {}
func (m mapa) Suelto()        {}
func (m map[string]int) Raro() {}
func ((Pila[int])) Sacar()    {}
func (Pila[T]) quieto()       {}

type mapa map[string]int
`),
		fuenteEnMemoria(t, "internal/modulos/m/b/b_test.go", `package b

func usar() {
	var p Pila[int]
	p.Meter(Uno)
	_ = Alfa
}
`),
		fuenteEnMemoria(t, "internal/modulos/m/a/a.go", "package a\n\nfunc Z() {}\n\nfunc Y() {}\n"),
		fuenteEnMemoria(t, "internal/modulos/m/a/a_test.go", "package a_test\n"),
	}
	for i := range fuentes {
		fuentes[i].EsTest = strings.HasSuffix(fuentes[i].Ruta, "_test.go")
	}
	vs := ExportadosCubiertos(fuentes)

	casos := []struct {
		nombre  string
		fichero string
		simbolo string
	}{
		{"función sin mencionar (a)", "internal/modulos/m/a/a.go", "Y"},
		{"otra función sin mencionar (a)", "internal/modulos/m/a/a.go", "Z"},
		{"tipo genérico de dos parámetros", "internal/modulos/m/b/b.go", "Par"},
		{"método de receptor genérico por valor", "internal/modulos/m/b/b.go", "Par.Clave"},
		{"método de receptor entre paréntesis", "internal/modulos/m/b/b.go", "Pila.Sacar"},
		{"variable exportada de un grupo", "internal/modulos/m/b/b.go", "Tres"},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			base := strings.TrimSuffix(c.fichero[strings.LastIndex(c.fichero, "/")+1:], ".go") + "_test.go"
			exigeViolacion(t, vs, c.fichero, c.simbolo, base)
		})
	}
	if len(vs) != len(casos) {
		t.Errorf("se esperaban %d violaciones; hay %d: %v", len(casos), len(vs), vs)
	}
	for _, v := range vs {
		for _, no := range []string{"Visible", "Suelto", "Raro", "quieto", "dos", "beta", "oculto"} {
			if strings.Contains(v.Motivo, no) {
				t.Errorf("%s no debe exigirse: %v", no, v)
			}
		}
	}
	exigeOrdenadas(t, vs)
}
