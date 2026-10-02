package candados

import (
	"strings"
	"testing"
)

// TestExportadosCubiertosMuerde: un exportado citado solo en un comentario, o solo en un
// literal de cadena, no está cubierto; un método exportado se nombra "Tipo.Metodo". Desde
// D-F1-10 muerde también el paquete cuyo nombre termina en «test» sin terminar en
// «helpertest»: uno de producción (latest) y uno con el nombre viejo de las suites (cosatest);
// el MISMO doble no muerde en cosahelpertest y sí en su gemelo de control, cosa.
func TestExportadosCubiertosMuerde(t *testing.T) {
	fuentes := recorrerCaso(t, "testdata/exportados/muerde", []string{"internal"}, true)
	vs := ExportadosCubiertos(fuentes)

	const m = "internal/modulos/m/"
	casos := []struct {
		nombre  string
		fichero string
		simbolo string
		test    string
	}{
		{"constante citada solo en un comentario del test", m + "cosa/cosa.go", "Limite", "cosa_test.go"},
		{"método exportado citado solo en un literal", m + "cosa/cosa.go", "Cosa.Medir", "cosa_test.go"},
		{"exported function of a production package ending in test (latest)", m + "latest/latest.go", "Version", "latest_test.go"},
		{"type of a package with the old bare test suffix (cosatest)", m + "cosatest/doble.go", "Doble no aparece", "doble_test.go"},
		{"method of a package with the old bare test suffix (cosatest)", m + "cosatest/doble.go", "Doble.Leer", "doble_test.go"},
		{"type of the control twin of the helpertest double", m + "cosa/doble.go", "Doble no aparece", "doble_test.go"},
		{"method of the control twin of the helpertest double", m + "cosa/doble.go", "Doble.Leer", "doble_test.go"},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			exigeViolacion(t, vs, c.fichero, c.simbolo, c.test)
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
	// El paquete …helpertest queda fuera (D-F1-3 = sí, con el sufijo compuesto de D-F1-10).
	exigeNingunaEn(t, vs, m+"cosahelpertest/doble.go")
	exigeOrdenadas(t, vs)
}

// TestExportadosCubiertosPasa: un test en rojo (etiqueta pendiente) del paquete externo que
// nombra todo por selector cuenta; campos y no exportados no se exigen; un x.go sin test no
// es asunto de este candado; un paquete …helpertest queda fuera (D-F1-3 = sí; el sufijo,
// D-F1-10).
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

// TestExportadosCubiertosHelperTestSuffix: la exención de suites y dobles es por el sufijo
// COMPUESTO «helpertest» del nombre del paquete (D-F1-10), la cláusula `package` y no el
// directorio. Cada nombre de helperTestSuffixCases se prueba con el MISMO fichero y un test
// que no nombra su exportado: exento = cero violaciones; no exento = una, que nombra Next.
func TestExportadosCubiertosHelperTestSuffix(t *testing.T) {
	for _, c := range helperTestSuffixCases {
		t.Run(c.name, func(t *testing.T) {
			ruta := "internal/modulos/m/" + c.dir + "/double.go"
			test := fuenteEnMemoria(t, "internal/modulos/m/"+c.dir+"/double_test.go", "package "+c.pkg+"\n")
			test.EsTest = true
			vs := ExportadosCubiertos([]Fuente{
				fuenteEnMemoria(t, ruta, "package "+c.pkg+helperTestDoubleBody),
				test,
			})
			if c.exempt {
				exigeCero(t, vs)
				return
			}
			exigeViolacion(t, vs, ruta, "Next", "double_test.go")
			if len(vs) != 1 {
				t.Errorf("se esperaba 1 violación; hay %d: %v", len(vs), vs)
			}
		})
	}
}
