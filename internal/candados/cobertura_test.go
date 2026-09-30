package candados

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const prefijoPerfil = "github.com/EduGoGroup/wapp-cloud-platform/"

// TestMarcaPostgres: la marca es la línea exacta de 05 E-6.
func TestMarcaPostgres(t *testing.T) {
	if MarcaPostgres != "// cobertura: adaptador postgres (05 E-6)" {
		t.Errorf("MarcaPostgres = %q", MarcaPostgres)
	}
}

// TestAgregarSuma: agrega por fichero; Cubiertas suma las sentencias con cuenta > 0; las
// líneas vacías se ignoran; la clave es la ruta del perfil y coincide con Fichero.Ruta.
func TestAgregarSuma(t *testing.T) {
	perfil := "mode: set\n" +
		"m/a.go:1.1,2.2 3 1\n" +
		"m/a.go:3.1,4.2 1 0\n" +
		"\n" +
		"m/b.go:1.1,2.2 2 0\n"
	got, err := Agregar(strings.NewReader(perfil))
	if err != nil {
		t.Fatalf("Agregar: %v", err)
	}
	quiero := map[string]Fichero{
		"m/a.go": {Ruta: "m/a.go", Sentencias: 4, Cubiertas: 3},
		"m/b.go": {Ruta: "m/b.go", Sentencias: 2, Cubiertas: 0},
	}
	if !reflect.DeepEqual(got, quiero) {
		t.Errorf("Agregar = %v; quiero %v", got, quiero)
	}
}

// TestAgregarBloqueRepetido: un bloque repetido cuenta una vez, con la cuenta máxima.
func TestAgregarBloqueRepetido(t *testing.T) {
	casos := []struct {
		nombre string
		perfil string
		quiero Fichero
	}{
		{"cero y luego cinco: cubierto una vez", "mode: count\na.go:1.1,2.2 3 0\na.go:1.1,2.2 3 5\n",
			Fichero{Ruta: "a.go", Sentencias: 3, Cubiertas: 3}},
		{"cinco y luego cero: sigue cubierto", "mode: count\na.go:1.1,2.2 3 5\na.go:1.1,2.2 3 0\n",
			Fichero{Ruta: "a.go", Sentencias: 3, Cubiertas: 3}},
		{"dos veces cero: sin cubrir, sin duplicar", "mode: atomic\na.go:1.1,2.2 3 0\na.go:1.1,2.2 3 0\n",
			Fichero{Ruta: "a.go", Sentencias: 3, Cubiertas: 0}},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			got, err := Agregar(strings.NewReader(c.perfil))
			if err != nil {
				t.Fatalf("Agregar: %v", err)
			}
			if got["a.go"] != c.quiero {
				t.Errorf("a.go = %+v; quiero %+v", got["a.go"], c.quiero)
			}
		})
	}
}

// TestAgregarErrores: perfil sin cabecera, segunda cabecera o línea mal formada dan error
// con "línea N" y sin resultado parcial.
func TestAgregarErrores(t *testing.T) {
	casos := []struct {
		nombre string
		perfil string
		linea  string
	}{
		{"perfil vacío", "", "línea 1"},
		{"sin cabecera mode", "a.go:1.1,2.2 3 1\n", "línea 1"},
		{"segunda cabecera", "mode: set\nmode: set\n", "línea 2"},
		{"faltan campos", "mode: set\na.go:1.1,2.2 3\n", "línea 2"},
		{"sin dos puntos antes del rango", "mode: set\na.go 3 1\n", "línea 2"},
		{"rango mal formado", "mode: set\na.go:1.1,2.2 3 1\na.go:1.1-2.2 3 1\n", "línea 3"},
		{"sentencias no enteras", "mode: set\na.go:1.1,2.2 x 1\n", "línea 2"},
		{"cuenta negativa", "mode: set\na.go:1.1,2.2 3 -1\n", "línea 2"},
		{"solo líneas en blanco", "\n\n", "línea 1"},
		{"cabecera tras líneas en blanco mal formada", "\nmodo: set\n", "línea 2"},
		{"fichero vacío antes del rango", "mode: set\n:1.1,2.2 3 1\n", "línea 2"},
		{"extremo del rango sin columna", "mode: set\na.go:1,2.2 3 1\n", "línea 2"},
		{"columna no entera", "mode: set\na.go:1.x,2.2 3 1\n", "línea 2"},
		{"sentencias negativas", "mode: set\na.go:1.1,2.2 -3 1\n", "línea 2"},
		{"cuenta no entera", "mode: set\na.go:1.1,2.2 3 uno\n", "línea 2"},
		{"cuatro campos", "mode: set\na.go:1.1,2.2 3 1 1\n", "línea 2"},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			got, err := Agregar(strings.NewReader(c.perfil))
			if err == nil {
				t.Fatalf("Agregar(%q) sin error; devolvió %v", c.perfil, got)
			}
			if !strings.Contains(err.Error(), c.linea) {
				t.Errorf("el error %q no contiene %q", err, c.linea)
			}
			if len(got) != 0 {
				t.Errorf("con error no hay resultado parcial; devolvió %v", got)
			}
		})
	}
}

// casoCobertura agrega el perfil.out de un árbol de prueba y recorre el árbol.
func casoCobertura(t *testing.T, raiz string) (map[string]Fichero, []Fuente) {
	t.Helper()
	datos, err := os.ReadFile(filepath.Join(raiz, "perfil.out")) //nolint:gosec // G304: ruta fija de testdata del propio test
	if err != nil {
		t.Fatal(err)
	}
	perfil, err := Agregar(strings.NewReader(string(datos)))
	if err != nil {
		t.Fatalf("Agregar(%s/perfil.out): %v", raiz, err)
	}
	if _, ok := perfil[prefijoPerfil+"internal/modulos/m/rojo/rojo.go"]; !ok {
		t.Fatalf("el perfil de %s no está en forma de import: %v", raiz, perfil)
	}
	return perfil, recorrerCaso(t, raiz, []string{"internal"}, false)
}

// TestCoberturaMuerde: un fichero al 50 %, una marca ilegítima y una marca fuera de la
// cabecera muerden; el fichero en rojo y el adaptador legítimo no se evalúan.
func TestCoberturaMuerde(t *testing.T) {
	perfil, fuentes := casoCobertura(t, "testdata/cobertura/muerde")
	vs := Cobertura(perfil, fuentes, 80)

	const m = "internal/modulos/m/"
	casos := []struct {
		nombre  string
		fichero string
		trozos  []string
	}{
		{"fichero en verde al 50 % (bloque repetido contado una vez)", m + "medio/medio.go", []string{"50.0 %", "80"}},
		{"marca postgres sin importar database/sql ni pgx", m + "falso/falso.go", []string{"marca", "postgres"}},
		{"marca fuera de la cabecera no exime", m + "tarde/tarde.go", []string{"0.0 %", "80"}},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			exigeViolacion(t, vs, c.fichero, c.trozos...)
		})
	}
	if len(vs) != len(casos) {
		t.Errorf("se esperaban %d violaciones; hay %d: %v", len(casos), len(vs), vs)
	}
	exigeNingunaEn(t, vs, m+"rojo/rojo.go")
	exigeNingunaEn(t, vs, m+"pg/pg.go")
	exigeOrdenadas(t, vs)
}

// TestCoberturaPasa: 80.0 % en umbral 80 pasa; adaptador legítimo, rojo, fichero sin
// sentencias y entradas del perfil fuera de fuentes no dan violación.
func TestCoberturaPasa(t *testing.T) {
	perfil, fuentes := casoCobertura(t, "testdata/cobertura/pasa")
	exigeCero(t, Cobertura(perfil, fuentes, 80))
}

// TestCoberturaUmbral: el umbral es un parámetro: el mismo 80.0 % muerde con umbral 90.
func TestCoberturaUmbral(t *testing.T) {
	perfil, fuentes := casoCobertura(t, "testdata/cobertura/pasa")
	vs := Cobertura(perfil, fuentes, 90)
	exigeViolacion(t, vs, "internal/modulos/m/bien/bien.go", "80.0 %", "90")
}

// TestExentos: solo los marcados en cabecera que importan database/sql o pgx, ordenados.
func TestExentos(t *testing.T) {
	casos := []struct {
		raiz   string
		quiero []string
	}{
		{"testdata/cobertura/muerde", []string{"internal/modulos/m/pg/pg.go"}},
		{"testdata/cobertura/pasa", []string{"internal/modulos/m/pg/pg.go"}},
	}
	for _, c := range casos {
		t.Run(c.raiz, func(t *testing.T) {
			got := Exentos(recorrerCaso(t, c.raiz, []string{"internal"}, false))
			if !reflect.DeepEqual(got, c.quiero) {
				t.Errorf("Exentos = %v; quiero %v", got, c.quiero)
			}
		})
	}
}

// TestEvaluables: en verde, no exentos, con sentencias en el perfil; ordenados por Ruta.
func TestEvaluables(t *testing.T) {
	casos := []struct {
		raiz   string
		quiero []string
	}{
		{"testdata/cobertura/muerde", []string{
			"internal/modulos/m/falso/falso.go",
			"internal/modulos/m/medio/medio.go",
			"internal/modulos/m/tarde/tarde.go",
		}},
		{"testdata/cobertura/pasa", []string{"internal/modulos/m/bien/bien.go"}},
	}
	for _, c := range casos {
		t.Run(c.raiz, func(t *testing.T) {
			perfil, fuentes := casoCobertura(t, c.raiz)
			got := Evaluables(perfil, fuentes)
			if !reflect.DeepEqual(got, c.quiero) {
				t.Errorf("Evaluables = %v; quiero %v", got, c.quiero)
			}
		})
	}
}

// lectorRoto falla al leer: un perfil que no se puede leer entero es un error, no un
// perfil corto.
type lectorRoto struct{}

func (lectorRoto) Read([]byte) (int, error) { return 0, errors.New("disco roto") }

// TestAgregarLectorRoto: un error de lectura se propaga con "línea N" y sin resultado.
func TestAgregarLectorRoto(t *testing.T) {
	got, err := Agregar(lectorRoto{})
	if err == nil || !strings.Contains(err.Error(), "línea 1") {
		t.Fatalf("Agregar(lectorRoto) = %v, %v; quiero error con \"línea 1\"", got, err)
	}
	if len(got) != 0 {
		t.Errorf("con error no hay resultado parcial; devolvió %v", got)
	}
}

// TestCoberturaBordes: cruce exacto y por sufijo con la Ruta más larga ganando; pgx por
// subpaquete exime; una mención de pendiente.Implementar en un comentario no pone el
// fichero en rojo; los tests no se evalúan ni se eximen; el porcentaje se trunca al decimal.
func TestCoberturaBordes(t *testing.T) {
	fuentes := []Fuente{
		fuenteEnMemoria(t, "x/a.go", "package a\n\nfunc A() {}\n"),
		fuenteEnMemoria(t, "a.go", "package a\n\nfunc A() {}\n"),
		fuenteEnMemoria(t, "p/pool.go", "// cobertura: adaptador postgres (05 E-6)\n\n"+
			"package p\n\nimport \"github.com/jackc/pgx/v5/pgxpool\"\n\nvar _ *pgxpool.Pool\n"),
		fuenteEnMemoria(t, "c/c.go", "package c\n\n// No es rojo: pendiente.Implementar solo se cita aquí.\nfunc C() {}\n"),
		fuenteEnMemoria(t, "t/t_test.go", "// cobertura: adaptador postgres (05 E-6)\n\npackage t\n"),
	}
	fuentes[4].EsTest = true
	perfil := map[string]Fichero{
		"mod/x/a.go":      {Ruta: "mod/x/a.go", Sentencias: 10000, Cubiertas: 7996},
		"a.go":            {Ruta: "a.go", Sentencias: 4, Cubiertas: 4},
		"mod/p/pool.go":   {Ruta: "mod/p/pool.go", Sentencias: 5, Cubiertas: 0},
		"mod/c/c.go":      {Ruta: "mod/c/c.go", Sentencias: 2, Cubiertas: 0},
		"mod/t/t_test.go": {Ruta: "mod/t/t_test.go", Sentencias: 2, Cubiertas: 0},
	}
	if got, quiero := Exentos(fuentes), []string{"p/pool.go"}; !reflect.DeepEqual(got, quiero) {
		t.Errorf("Exentos = %v; quiero %v", got, quiero)
	}
	if got, quiero := Evaluables(perfil, fuentes), []string{"a.go", "c/c.go", "x/a.go"}; !reflect.DeepEqual(got, quiero) {
		t.Errorf("Evaluables = %v; quiero %v", got, quiero)
	}
	vs := Cobertura(perfil, fuentes, 80)
	exigeViolacion(t, vs, "x/a.go", "79.9 %", "80")
	exigeViolacion(t, vs, "c/c.go", "0.0 %", "80")
	exigeNingunaEn(t, vs, "a.go")
	exigeNingunaEn(t, vs, "p/pool.go")
	exigeNingunaEn(t, vs, "t/t_test.go")
	if len(vs) != 2 {
		t.Errorf("se esperaban 2 violaciones; hay %d: %v", len(vs), vs)
	}
	exigeOrdenadas(t, vs)
}
