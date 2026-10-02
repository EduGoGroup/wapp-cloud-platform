package candados

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
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

// D-F1-6 (Jhoan, 2026-10-01): los paquetes de suite y dobles (suites Contrato y dobles, p. ej.
// contacthelpertest) quedan exentos también de la cobertura por fichero (D-12). Su suite solo
// la ejecutan los tests de las implementaciones, que viven en otros paquetes, y
// `go test -cover` sin -coverpkg no cuenta lo que se ejecuta desde otro paquete: en el perfil
// del propio paquete el fichero sale al 0 % y rompería `make cobertura-ficheros`.
//
// D-F1-10 (Jhoan, 2026-10-02) ESTRECHA cómo se reconoce ese paquete: su nombre termina en el
// sufijo compuesto «helpertest» (…helpertest), no en «test» a secas. Con «test», un paquete de
// producción como latest quedaba sin medir; ahora latest y cosatest se miden y muerden.
//
// Los árboles de prueba están en testdata/cobertura/paquetes-test/{pasa,muerde} (aparte de
// testdata/cobertura/{pasa,muerde}, cuyas cifras exactas comprueba cmd/cobertura-ficheros);
// los casos sin árbol, en memoria.

const (
	arbolPaquetesTestPasa   = "testdata/cobertura/paquetes-test/pasa"
	arbolPaquetesTestMuerde = "testdata/cobertura/paquetes-test/muerde"
	dirPaquetesTest         = "internal/modulos/m/"
)

// exigeSinCubrirEn exige que el caso muerda de verdad: ruta es una Fuente de producción del
// paquete dado y el perfil le da sentencias y ninguna cubierta. Sin esto, un caso cuyo
// fichero no estuviera en el perfil, o estuviera cubierto, pasaría por no mirar.
func exigeSinCubrirEn(t *testing.T, perfil map[string]Fichero, fuentes []Fuente, ruta, paquete string) {
	t.Helper()
	if fi := perfil[prefijoPerfil+ruta]; fi.Sentencias == 0 || fi.Cubiertas != 0 {
		t.Fatalf("el perfil no deja %s al 0 %% con sentencias: %+v", ruta, fi)
	}
	for _, f := range fuentes {
		if f.Ruta != ruta {
			continue
		}
		if f.EsTest || f.Paquete != paquete {
			t.Fatalf("%s: EsTest=%v Paquete=%q; quiero producción del paquete %q", ruta, f.EsTest, f.Paquete, paquete)
		}
		return
	}
	t.Fatalf("%s no está entre las fuentes recorridas", ruta)
}

// TestCoberturaPaquetesTestEvaluables: Evaluables no devuelve ningún fichero de un paquete
// cuyo nombre termina en «helpertest», aunque el perfil le dé sentencias y ninguna cubierta;
// el paquete que no lo es sigue midiéndose —también el de producción que termina en «test»
// por casualidad (latest) y el del nombre viejo de las suites (cosatest), D-F1-10—; y el
// contrato en rojo y el adaptador Postgres legítimo siguen fuera, como antes.
func TestCoberturaPaquetesTestEvaluables(t *testing.T) {
	casos := []struct {
		nombre    string
		raiz      string
		sinCubrir map[string]string // fichero con sentencias y 0 cubiertas → su paquete
		quiero    []string
	}{
		{"pasa: la suite y el doble del paquete …helpertest no se miden; cosa.go sí", arbolPaquetesTestPasa,
			map[string]string{
				dirPaquetesTest + "cosahelpertest/doble.go":    "cosahelpertest",
				dirPaquetesTest + "cosahelpertest/contrato.go": "cosahelpertest",
				dirPaquetesTest + "pg/pg.go":                   "pg",
				dirPaquetesTest + "rojo/rojo.go":               "rojo",
			},
			[]string{dirPaquetesTest + "cosa/cosa.go"}},
		{"muerde: el MISMO doble se mide si su paquete no acaba en helpertest, y latest también", arbolPaquetesTestMuerde,
			map[string]string{
				dirPaquetesTest + "cosa/doble.go":           "cosa",
				dirPaquetesTest + "cosahelpertest/doble.go": "cosahelpertest",
				dirPaquetesTest + "cosatest/doble.go":       "cosatest",
				dirPaquetesTest + "latest/latest.go":        "latest",
				dirPaquetesTest + "rojo/rojo.go":            "rojo",
			},
			[]string{
				dirPaquetesTest + "cosa/doble.go",
				dirPaquetesTest + "cosatest/doble.go",
				dirPaquetesTest + "latest/latest.go",
			}},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			perfil, fuentes := casoCobertura(t, c.raiz)
			for ruta, paquete := range c.sinCubrir {
				exigeSinCubrirEn(t, perfil, fuentes, ruta, paquete)
			}
			if got := Evaluables(perfil, fuentes); !reflect.DeepEqual(got, c.quiero) {
				t.Errorf("Evaluables = %v; quiero %v", got, c.quiero)
			}
		})
	}
}

// TestCoberturaPaquetesTestViolaciones: un árbol con un paquete …helpertest al 0 %, un
// contrato en rojo y un adaptador Postgres exento no da ninguna violación (el 80 % del resto
// sí se mide), y el adaptador sigue contando como exento.
func TestCoberturaPaquetesTestViolaciones(t *testing.T) {
	perfil, fuentes := casoCobertura(t, arbolPaquetesTestPasa)
	exigeSinCubrirEn(t, perfil, fuentes, dirPaquetesTest+"cosahelpertest/doble.go", "cosahelpertest")
	exigeSinCubrirEn(t, perfil, fuentes, dirPaquetesTest+"cosahelpertest/contrato.go", "cosahelpertest")

	exigeCero(t, Cobertura(perfil, fuentes, 80))
	if got, quiero := Exentos(fuentes), []string{dirPaquetesTest + "pg/pg.go"}; !reflect.DeepEqual(got, quiero) {
		t.Errorf("Exentos = %v; quiero %v", got, quiero)
	}
}

// TestCoberturaPaquetesTestContraste: el MISMO doble al 0 %, en un paquete cuyo nombre NO
// termina en «helpertest», es violación por umbral —en cosa, el gemelo de control, y en
// cosatest, el nombre viejo que D-F1-10 deja de eximir—; el de su vecino cosahelpertest no.
// Y latest, un paquete de producción que termina en «test» por casualidad, también muerde.
func TestCoberturaPaquetesTestContraste(t *testing.T) {
	perfil, fuentes := casoCobertura(t, arbolPaquetesTestMuerde)
	exigeSinCubrirEn(t, perfil, fuentes, dirPaquetesTest+"cosa/doble.go", "cosa")
	exigeSinCubrirEn(t, perfil, fuentes, dirPaquetesTest+"cosahelpertest/doble.go", "cosahelpertest")
	exigeSinCubrirEn(t, perfil, fuentes, dirPaquetesTest+"cosatest/doble.go", "cosatest")
	exigeSinCubrirEn(t, perfil, fuentes, dirPaquetesTest+"latest/latest.go", "latest")

	vs := Cobertura(perfil, fuentes, 80)
	exigeViolacion(t, vs, dirPaquetesTest+"cosa/doble.go", "0.0 %", "80")
	exigeViolacion(t, vs, dirPaquetesTest+"cosatest/doble.go", "0.0 %", "80")
	exigeViolacion(t, vs, dirPaquetesTest+"latest/latest.go", "0.0 %", "80")
	exigeNingunaEn(t, vs, dirPaquetesTest+"cosahelpertest/doble.go")
	exigeNingunaEn(t, vs, dirPaquetesTest+"rojo/rojo.go")
	if len(vs) != 3 {
		t.Errorf("se esperaban 3 violaciones; hay %d: %v", len(vs), vs)
	}
	exigeOrdenadas(t, vs)
}

// TestCoberturaPaquetesTestBordes: la exención de D-F1-6 es por el nombre del PAQUETE (la
// cláusula `package`, la misma condición que D-F1-3: el sufijo «helpertest» de D-F1-10), no
// por el directorio ni por la nota: un …helpertest al 100 % tampoco se mide. No cambia lo
// demás: un …helpertest en rojo o con adaptador Postgres legítimo sigue fuera de Evaluables, y
// una marca de Postgres ilegítima en un …helpertest sigue siendo violación de marca (nadie se
// exime por decreto, 05 E-6) pero no de umbral.
func TestCoberturaPaquetesTestBordes(t *testing.T) {
	const fuenteRoja = "package cosahelpertest\n\nimport \"mod/pendiente\"\n\nfunc R() { panic(pendiente.Implementar(\"R\")) }\n"
	casos := []struct {
		nombre    string
		ruta      string
		src       string
		cubiertas int // de las 2 sentencias que el perfil le da
		medido    bool
	}{
		{"paquete que no es …helpertest: se mide", "x/cosa/cosa.go", "package cosa\n\nfunc A() {}\n", 0, true},
		{"paquete …helpertest al 0 %: no se mide", "x/cosahelpertest/mal.go", "package cosahelpertest\n\nfunc C() {}\n", 0, false},
		{"paquete …helpertest al 100 %: tampoco, la exención no es por nota", "x/cosahelpertest/bien.go", "package cosahelpertest\n\nfunc B() {}\n", 2, false},
		{"directorio …helpertest con package que no lo es: se mide", "y/cosahelpertest/dir.go", "package cosa\n\nfunc D() {}\n", 0, true},
		{"package …helpertest en un directorio que no lo es: no se mide", "z/otro/otro.go", "package otrohelpertest\n\nfunc E() {}\n", 0, false},
		{"paquete …helpertest en rojo: fuera, como antes", "x/cosahelpertest/rojo.go", fuenteRoja, 0, false},
		{"paquete …helpertest con adaptador Postgres legítimo: fuera, como antes", "x/cosahelpertest/pg.go",
			MarcaPostgres + "\n\npackage cosahelpertest\n\nimport \"database/sql\"\n\nvar _ *sql.DB\n", 0, false},
		{"paquete …helpertest con marca ilegítima: fuera del umbral", "x/cosahelpertest/falsa.go",
			MarcaPostgres + "\n\npackage cosahelpertest\n\nfunc F() {}\n", 0, false},
	}
	fuentes := make([]Fuente, 0, len(casos))
	perfil := make(map[string]Fichero, len(casos))
	for _, c := range casos {
		fuentes = append(fuentes, fuenteEnMemoria(t, c.ruta, c.src))
		perfil["mod/"+c.ruta] = Fichero{Sentencias: 2, Cubiertas: c.cubiertas}
	}

	evaluables := Evaluables(perfil, fuentes)
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			if got := slices.Contains(evaluables, c.ruta); got != c.medido {
				t.Errorf("%s en Evaluables = %v; quiero %v (Evaluables = %v)", c.ruta, got, c.medido, evaluables)
			}
		})
	}
	if got, quiero := Exentos(fuentes), []string{"x/cosahelpertest/pg.go"}; !reflect.DeepEqual(got, quiero) {
		t.Errorf("Exentos = %v; quiero %v", got, quiero)
	}

	vs := Cobertura(perfil, fuentes, 80)
	exigeViolacion(t, vs, "x/cosa/cosa.go", "0.0 %", "80")
	exigeViolacion(t, vs, "y/cosahelpertest/dir.go", "0.0 %", "80")
	exigeViolacion(t, vs, "x/cosahelpertest/falsa.go", "marca", "postgres")
	if len(vs) != 3 {
		t.Errorf("se esperaban 3 violaciones (dos por umbral, una por marca); hay %d: %v", len(vs), vs)
	}
	exigeOrdenadas(t, vs)
}

// TestCoberturaHelperTestSuffix: la exención de suites y dobles es por el sufijo COMPUESTO
// «helpertest» del nombre del paquete (D-F1-10), la cláusula `package` y no el directorio.
// Cada nombre de helperTestSuffixCases se prueba con el MISMO fichero al 0 %: exento = fuera
// de Evaluables y sin violación; no exento = en Evaluables y con violación por umbral.
func TestCoberturaHelperTestSuffix(t *testing.T) {
	for _, c := range helperTestSuffixCases {
		t.Run(c.name, func(t *testing.T) {
			ruta := "internal/modulos/m/" + c.dir + "/double.go"
			fuentes := []Fuente{fuenteEnMemoria(t, ruta, "package "+c.pkg+helperTestDoubleBody)}
			perfil := map[string]Fichero{"mod/" + ruta: {Sentencias: 1, Cubiertas: 0}}

			evaluables, vs := Evaluables(perfil, fuentes), Cobertura(perfil, fuentes, 80)
			if c.exempt {
				if len(evaluables) != 0 {
					t.Errorf("Evaluables = %v; quiero ninguno (paquete %s exento)", evaluables, c.pkg)
				}
				exigeCero(t, vs)
				return
			}
			if !reflect.DeepEqual(evaluables, []string{ruta}) {
				t.Errorf("Evaluables = %v; quiero [%s] (paquete %s no exento)", evaluables, ruta, c.pkg)
			}
			exigeViolacion(t, vs, ruta, "0.0 %", "80")
			if len(vs) != 1 {
				t.Errorf("se esperaba 1 violación; hay %d: %v", len(vs), vs)
			}
		})
	}
}
