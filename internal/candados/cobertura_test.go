package candados

import (
	"errors"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const prefijoPerfil = "github.com/EduGoGroup/wapp-cloud-platform/"

// oldPostgresMark es la línea que, hasta P2 (Jhoan, 2026-10-03; 05 E-9), eximía a un
// adaptador Postgres del umbral de cobertura. Hoy es un comentario inerte: los tests la ponen
// en la cabecera de un fichero para afirmar que ya NO lo saca del informe ni da violación.
const oldPostgresMark = "// cobertura: adaptador postgres (05 E-6)"

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

// TestCoberturaMuerde: quedan por debajo un fichero al 50 %, uno al 0 % y el adaptador
// Postgres marcado al 0 %, que desde P2 se mide como los demás. El fichero con la marca vieja
// sin import de Postgres está al 90 %: ni está por debajo ni hay ya «violación de marca». El
// fichero en rojo no se evalúa.
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
		{"adaptador postgres marcado en la cabecera: ya no está exento", m + "pg/pg.go", []string{"0.0 %", "80", "0 de 10"}},
		{"marca fuera de la cabecera: igual de inerte", m + "tarde/tarde.go", []string{"0.0 %", "80"}},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			exigeViolacion(t, vs, c.fichero, c.trozos...)
		})
	}
	if len(vs) != len(casos) {
		t.Errorf("se esperaban %d ficheros por debajo; hay %d: %v", len(casos), len(vs), vs)
	}
	exigeNingunaEn(t, vs, m+"rojo/rojo.go")
	exigeNingunaEn(t, vs, m+"falso/falso.go")
	exigeSinMotivoDeMarca(t, vs)
	exigeOrdenadas(t, vs)
}

// exigeSinMotivoDeMarca exige que ningún motivo hable de la marca de Postgres: la «violación
// por marca ilegítima» se retiró con P2 y Cobertura solo informa de porcentajes.
func exigeSinMotivoDeMarca(t *testing.T, vs []Violacion) {
	t.Helper()
	for _, v := range vs {
		if strings.Contains(v.Motivo, "marca") || strings.Contains(v.Motivo, "postgres") {
			t.Errorf("la marca de Postgres ya no da motivo alguno; hay %q", v)
		}
	}
}

// TestCoberturaPasa: 80.0 % con referencia 80 no está por debajo; tampoco el adaptador Postgres
// marcado (medido, al 100 %), el rojo, el fichero sin sentencias ni las entradas del perfil
// fuera de fuentes.
func TestCoberturaPasa(t *testing.T) {
	perfil, fuentes := casoCobertura(t, "testdata/cobertura/pasa")
	exigeCero(t, Cobertura(perfil, fuentes, 80))
}

// TestCoberturaUmbral: el umbral es un parámetro: el mismo 80.0 % queda por debajo con
// referencia 90, y solo él (el adaptador marcado está al 100 %).
func TestCoberturaUmbral(t *testing.T) {
	perfil, fuentes := casoCobertura(t, "testdata/cobertura/pasa")
	vs := Cobertura(perfil, fuentes, 90)
	exigeViolacion(t, vs, "internal/modulos/m/bien/bien.go", "80.0 %", "90")
	if len(vs) != 1 {
		t.Errorf("se esperaba 1 fichero por debajo; hay %d: %v", len(vs), vs)
	}
}

// TestEvaluables: en verde y con sentencias en el perfil, ordenados por Ruta. El adaptador
// Postgres marcado (pg/pg.go) ESTÁ en los dos árboles: ya no hay exentos por umbral (P2).
func TestEvaluables(t *testing.T) {
	casos := []struct {
		raiz   string
		quiero []string
	}{
		{"testdata/cobertura/muerde", []string{
			"internal/modulos/m/falso/falso.go",
			"internal/modulos/m/medio/medio.go",
			"internal/modulos/m/pg/pg.go",
			"internal/modulos/m/tarde/tarde.go",
		}},
		{"testdata/cobertura/pasa", []string{
			"internal/modulos/m/bien/bien.go",
			"internal/modulos/m/pg/pg.go",
		}},
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

// TestCoberturaBordes: cruce exacto y por sufijo con la Ruta más larga ganando; un adaptador
// marcado que importa pgx por subpaquete se mide (antes quedaba exento); una mención de
// pendiente.Implementar en un comentario no pone el fichero en rojo; los tests no se evalúan,
// lleven o no la marca; el porcentaje se trunca al decimal.
func TestCoberturaBordes(t *testing.T) {
	fuentes := []Fuente{
		fuenteEnMemoria(t, "x/a.go", "package a\n\nfunc A() {}\n"),
		fuenteEnMemoria(t, "a.go", "package a\n\nfunc A() {}\n"),
		fuenteEnMemoria(t, "p/pool.go", oldPostgresMark+"\n\n"+
			"package p\n\nimport \"github.com/jackc/pgx/v5/pgxpool\"\n\nvar _ *pgxpool.Pool\n"),
		fuenteEnMemoria(t, "c/c.go", "package c\n\n// No es rojo: pendiente.Implementar solo se cita aquí.\nfunc C() {}\n"),
		fuenteEnMemoria(t, "t/t_test.go", oldPostgresMark+"\n\npackage t\n"),
	}
	fuentes[4].EsTest = true
	perfil := map[string]Fichero{
		"mod/x/a.go":      {Ruta: "mod/x/a.go", Sentencias: 10000, Cubiertas: 7996},
		"a.go":            {Ruta: "a.go", Sentencias: 4, Cubiertas: 4},
		"mod/p/pool.go":   {Ruta: "mod/p/pool.go", Sentencias: 5, Cubiertas: 0},
		"mod/c/c.go":      {Ruta: "mod/c/c.go", Sentencias: 2, Cubiertas: 0},
		"mod/t/t_test.go": {Ruta: "mod/t/t_test.go", Sentencias: 2, Cubiertas: 0},
	}
	if got, quiero := Evaluables(perfil, fuentes), []string{"a.go", "c/c.go", "p/pool.go", "x/a.go"}; !reflect.DeepEqual(got, quiero) {
		t.Errorf("Evaluables = %v; quiero %v", got, quiero)
	}
	vs := Cobertura(perfil, fuentes, 80)
	exigeViolacion(t, vs, "x/a.go", "79.9 %", "80")
	exigeViolacion(t, vs, "c/c.go", "0.0 %", "80")
	exigeViolacion(t, vs, "p/pool.go", "0.0 %", "80", "0 de 5")
	exigeNingunaEn(t, vs, "a.go")
	exigeNingunaEn(t, vs, "t/t_test.go")
	if len(vs) != 3 {
		t.Errorf("se esperaban 3 ficheros por debajo; hay %d: %v", len(vs), vs)
	}
	exigeSinMotivoDeMarca(t, vs)
	exigeOrdenadas(t, vs)
}

// D-F1-6 (Jhoan, 2026-10-01): las suites de contrato quedan fuera de la cobertura por
// fichero. Solo las ejecutan los tests de las implementaciones, que viven en otros paquetes, y
// `go test -cover` sin -coverpkg no cuenta lo que se ejecuta desde otro paquete: en el perfil
// del propio paquete el fichero sale al 0 % y ensuciaría el informe de `make cobertura-ficheros`
// con un cero que no es verdad. No es una exención por umbral (esas se retiraron con P2,
// 2026-10-03): es que el fichero no es medible.
//
// D-F1-10 (Jhoan, 2026-10-02) ESTRECHA cómo se reconoce el paquete de suite y dobles: su nombre
// termina en el sufijo compuesto «helpertest» (…helpertest), no en «test» a secas. Con «test»,
// un paquete de producción como latest quedaba sin medir; ahora latest y cosatest se miden.
//
// D-F1-13 (Jhoan, 2026-10-02) ESTRECHA qué queda exento DENTRO de ese paquete: solo los
// ficheros de suite, contrato.go y *_contrato.go (isContractSuiteFile). Los demás —los dobles
// con lógica, como contacthelpertest/estado.go— los ejecuta el test de su propio paquete, así
// que se miden: uno por debajo sale en la lista, uno por encima no. Y un fichero
// que se llama contrato.go o x_contrato.go en un paquete que NO es …helpertest no es de suite.
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

// TestCoberturaPaquetesTestEvaluables: de un paquete cuyo nombre termina en «helpertest»,
// Evaluables deja fuera SOLO los ficheros de suite (contrato.go y *_contrato.go), aunque el
// perfil les dé sentencias y ninguna cubierta; su doble con lógica se mide (D-F1-13). Fuera
// de un paquete …helpertest, llamarse contrato.go o merge_contrato.go no exime: se miden en
// cosa, y en cosatest, el nombre viejo de las suites. Siguen midiéndose el paquete de
// producción que termina en «test» por casualidad (latest, D-F1-10), sigue fuera el contrato
// en rojo, y el adaptador Postgres marcado ahora SE MIDE (P2).
func TestCoberturaPaquetesTestEvaluables(t *testing.T) {
	casos := []struct {
		nombre    string
		raiz      string
		sinCubrir map[string]string // fichero con sentencias y 0 cubiertas → su paquete
		quiero    []string
	}{
		{"pasa: los ficheros de suite del paquete …helpertest no se miden; su doble, cosa.go y el adaptador marcado sí", arbolPaquetesTestPasa,
			map[string]string{
				dirPaquetesTest + "cosahelpertest/contrato.go":       "cosahelpertest",
				dirPaquetesTest + "cosahelpertest/merge_contrato.go": "cosahelpertest",
				dirPaquetesTest + "rojo/rojo.go":                     "rojo",
			},
			[]string{
				dirPaquetesTest + "cosa/cosa.go",
				dirPaquetesTest + "cosahelpertest/doble.go",
				dirPaquetesTest + "pg/pg.go",
			}},
		{"muerde: el doble de …helpertest se mide; contrato.go y merge_contrato.go, solo fuera de …helpertest", arbolPaquetesTestMuerde,
			map[string]string{
				dirPaquetesTest + "cosa/contrato.go":                 "cosa",
				dirPaquetesTest + "cosa/doble.go":                    "cosa",
				dirPaquetesTest + "cosa/merge_contrato.go":           "cosa",
				dirPaquetesTest + "cosahelpertest/contrato.go":       "cosahelpertest",
				dirPaquetesTest + "cosahelpertest/doble.go":          "cosahelpertest",
				dirPaquetesTest + "cosahelpertest/merge_contrato.go": "cosahelpertest",
				dirPaquetesTest + "cosatest/contrato.go":             "cosatest",
				dirPaquetesTest + "cosatest/doble.go":                "cosatest",
				dirPaquetesTest + "latest/latest.go":                 "latest",
				dirPaquetesTest + "rojo/rojo.go":                     "rojo",
			},
			[]string{
				dirPaquetesTest + "cosa/contrato.go",
				dirPaquetesTest + "cosa/doble.go",
				dirPaquetesTest + "cosa/merge_contrato.go",
				dirPaquetesTest + "cosahelpertest/doble.go",
				dirPaquetesTest + "cosatest/contrato.go",
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

// TestCoberturaPaquetesTestViolaciones: un árbol con los dos ficheros de suite de un paquete
// …helpertest al 0 %, su doble con lógica POR ENCIMA del umbral (medido, y pasa), un contrato
// en rojo y un adaptador Postgres marcado y cubierto no deja ningún fichero por debajo; el
// adaptador no pasa por exento sino por medido: está en Evaluables.
func TestCoberturaPaquetesTestViolaciones(t *testing.T) {
	perfil, fuentes := casoCobertura(t, arbolPaquetesTestPasa)
	exigeSinCubrirEn(t, perfil, fuentes, dirPaquetesTest+"cosahelpertest/contrato.go", "cosahelpertest")
	exigeSinCubrirEn(t, perfil, fuentes, dirPaquetesTest+"cosahelpertest/merge_contrato.go", "cosahelpertest")
	// El doble pasa porque está cubierto, no porque no se mire: tiene sentencias, todas cubiertas.
	if fi := perfil[prefijoPerfil+dirPaquetesTest+"cosahelpertest/doble.go"]; fi.Sentencias == 0 || fi.Cubiertas != fi.Sentencias {
		t.Fatalf("el perfil no deja cosahelpertest/doble.go cubierto entero: %+v", fi)
	}

	exigeCero(t, Cobertura(perfil, fuentes, 80))
	if evaluables := Evaluables(perfil, fuentes); !slices.Contains(evaluables, dirPaquetesTest+"pg/pg.go") {
		t.Errorf("el adaptador Postgres marcado no está en Evaluables: %v", evaluables)
	}
}

// TestCoberturaPaquetesTestContraste: el doble con lógica al 0 % muerde en su paquete
// …helpertest igual que sus gemelos de cosa y de cosatest (D-F1-13: POR DEBAJO del umbral,
// muerde). El MISMO contrato.go y el MISMO merge_contrato.go, al 0 %, no muerden en
// cosahelpertest —son ficheros de suite— y sí en cosa y en cosatest, que no son paquetes
// …helpertest. Y latest, un paquete de producción que termina en «test» por casualidad,
// sigue mordiendo (D-F1-10).
func TestCoberturaPaquetesTestContraste(t *testing.T) {
	perfil, fuentes := casoCobertura(t, arbolPaquetesTestMuerde)
	biting := []string{
		"cosa/contrato.go", "cosa/doble.go", "cosa/merge_contrato.go",
		"cosahelpertest/doble.go",
		"cosatest/contrato.go", "cosatest/doble.go",
		"latest/latest.go",
	}
	exempt := []string{"cosahelpertest/contrato.go", "cosahelpertest/merge_contrato.go"}
	for _, f := range append(slices.Clone(biting), exempt...) {
		exigeSinCubrirEn(t, perfil, fuentes, dirPaquetesTest+f, path.Dir(f)) // el directorio es el paquete
	}

	vs := Cobertura(perfil, fuentes, 80)
	for _, f := range biting {
		exigeViolacion(t, vs, dirPaquetesTest+f, "0.0 %", "80")
	}
	for _, f := range exempt {
		exigeNingunaEn(t, vs, dirPaquetesTest+f)
	}
	exigeNingunaEn(t, vs, dirPaquetesTest+"rojo/rojo.go")
	if len(vs) != len(biting) {
		t.Errorf("se esperaban %d violaciones; hay %d: %v", len(biting), len(vs), vs)
	}
	exigeOrdenadas(t, vs)
}

// TestCoberturaPaquetesTestBordes: la exención es del FICHERO DE SUITE de un paquete
// …helpertest (D-F1-13), y el paquete se reconoce por su nombre (la cláusula `package`, el
// sufijo «helpertest» de D-F1-10), no por el directorio. No es por nota: un fichero de suite
// al 100 % tampoco se mide, y un doble se mide esté al 0 % (muerde), al 79,9 % (muerde) o al
// 80 % justo (pasa). Un doble en rojo sigue fuera de Evaluables. La marca vieja de Postgres es
// inerte (P2): con import de Postgres o sin él, el fichero se mide como cualquier otro —y un
// fichero de suite que la lleve sigue fuera por ser de suite—, y nunca da motivo de «marca».
func TestCoberturaPaquetesTestBordes(t *testing.T) {
	const fuenteRoja = "package cosahelpertest\n\nimport \"mod/pendiente\"\n\nfunc R() { panic(pendiente.Implementar(\"R\")) }\n"
	casos := []struct {
		nombre     string
		ruta       string
		src        string
		statements int
		cubiertas  int
		medido     bool
		below      bool // da violación por umbral (80)
	}{
		{"paquete que no es …helpertest: se mide", "x/cosa/cosa.go", "package cosa\n\nfunc A() {}\n", 2, 0, true, true},
		{"contrato.go de un …helpertest al 0 %: no se mide", "x/cosahelpertest/contrato.go", "package cosahelpertest\n\nfunc C() {}\n", 2, 0, false, false},
		{"x_contrato.go de un …helpertest al 0 %: no se mide", "x/cosahelpertest/merge_contrato.go", "package cosahelpertest\n\nfunc M() {}\n", 2, 0, false, false},
		{"fichero de suite al 100 %: tampoco, la exención no es por nota", "x/cosahelpertest/resolve_contrato.go", "package cosahelpertest\n\nfunc B() {}\n", 2, 2, false, false},
		{"doble de un …helpertest al 0 %: se mide y muerde", "x/cosahelpertest/doble.go", "package cosahelpertest\n\nfunc D() {}\n", 2, 0, true, true},
		{"doble de un …helpertest justo por debajo del umbral: muerde", "x/cosahelpertest/casi.go", "package cosahelpertest\n\nfunc K() {}\n", 1000, 799, true, true},
		{"doble de un …helpertest justo en el umbral: se mide y pasa", "x/cosahelpertest/justo.go", "package cosahelpertest\n\nfunc J() {}\n", 1000, 800, true, false},
		{"doble de un …helpertest al 100 %: se mide y pasa", "x/cosahelpertest/memory.go", "package cosahelpertest\n\nfunc G() {}\n", 2, 2, true, false},
		{"contrato.go en un directorio …helpertest con package que no lo es: se mide", "y/cosahelpertest/contrato.go", "package cosa\n\nfunc D() {}\n", 2, 0, true, true},
		{"contrato.go de un package …helpertest en un directorio que no lo es: no se mide", "z/otro/contrato.go", "package otrohelpertest\n\nfunc E() {}\n", 2, 0, false, false},
		{"doble de un package …helpertest en un directorio que no lo es: se mide", "z/otro/otro.go", "package otrohelpertest\n\nfunc E() {}\n", 2, 0, true, true},
		{"doble de un …helpertest en rojo: fuera, como antes", "x/cosahelpertest/rojo.go", fuenteRoja, 2, 0, false, false},
		{"adaptador Postgres marcado en un …helpertest: la marca ya no lo saca, se mide", "x/cosahelpertest/pg.go",
			oldPostgresMark + "\n\npackage cosahelpertest\n\nimport \"database/sql\"\n\nvar _ *sql.DB\n", 2, 0, true, true},
		{"doble de un …helpertest con la marca y sin import de Postgres: se mide igual", "x/cosahelpertest/falsa.go",
			oldPostgresMark + "\n\npackage cosahelpertest\n\nfunc F() {}\n", 2, 0, true, true},
		{"fichero de suite con la marca: fuera por ser de suite, sin motivo de marca", "x/cosahelpertest/falsa_contrato.go",
			oldPostgresMark + "\n\npackage cosahelpertest\n\nfunc F() {}\n", 2, 0, false, false},
	}
	fuentes := make([]Fuente, 0, len(casos))
	perfil := make(map[string]Fichero, len(casos))
	for _, c := range casos {
		fuentes = append(fuentes, fuenteEnMemoria(t, c.ruta, c.src))
		perfil["mod/"+c.ruta] = Fichero{Sentencias: c.statements, Cubiertas: c.cubiertas}
	}

	evaluables := Evaluables(perfil, fuentes)
	vs := Cobertura(perfil, fuentes, 80)
	belowCount := 0
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			if got := slices.Contains(evaluables, c.ruta); got != c.medido {
				t.Errorf("%s en Evaluables = %v; quiero %v (Evaluables = %v)", c.ruta, got, c.medido, evaluables)
			}
			got := slices.ContainsFunc(vs, func(v Violacion) bool {
				return v.Fichero == c.ruta && strings.Contains(v.Motivo, "< umbral 80 %")
			})
			if got != c.below {
				t.Errorf("%s da violación por umbral = %v; quiero %v (%v)", c.ruta, got, c.below, vs)
			}
		})
		if c.below {
			belowCount++
		}
	}

	exigeViolacion(t, vs, "x/cosahelpertest/casi.go", "79.9 %", "80")
	exigeNingunaEn(t, vs, "x/cosahelpertest/falsa_contrato.go")
	if len(vs) != belowCount {
		t.Errorf("se esperaban %d ficheros por debajo; hay %d: %v", belowCount, len(vs), vs)
	}
	exigeSinMotivoDeMarca(t, vs)
	exigeOrdenadas(t, vs)
}

// TestCoberturaHelperTestSuffix: el paquete de suite y dobles se reconoce por el sufijo
// COMPUESTO «helpertest» de su nombre (D-F1-10), la cláusula `package` y no el directorio.
// Cada nombre de helperTestSuffixCases se prueba con el MISMO fichero de suite, contrato.go,
// al 0 %: en un paquete …helpertest queda fuera de Evaluables y sin violación; en cualquier
// otro, contrato.go es un fichero más: está en Evaluables y da violación por umbral. Y con el
// MISMO código en un fichero que no es de suite (double.go), se mide en TODOS (D-F1-13): el
// paquete …helpertest ya no exime por sí solo.
func TestCoberturaHelperTestSuffix(t *testing.T) {
	for _, c := range helperTestSuffixCases {
		t.Run(c.name, func(t *testing.T) {
			ruta := "internal/modulos/m/" + c.dir + "/contrato.go"
			fuentes := []Fuente{fuenteEnMemoria(t, ruta, "package "+c.pkg+helperTestDoubleBody)}
			perfil := map[string]Fichero{"mod/" + ruta: {Sentencias: 1, Cubiertas: 0}}

			evaluables, vs := Evaluables(perfil, fuentes), Cobertura(perfil, fuentes, 80)
			if c.exempt {
				if len(evaluables) != 0 {
					t.Errorf("Evaluables = %v; quiero ninguno (contrato.go del paquete %s es de suite)", evaluables, c.pkg)
				}
				exigeCero(t, vs)
			} else {
				if !reflect.DeepEqual(evaluables, []string{ruta}) {
					t.Errorf("Evaluables = %v; quiero [%s] (paquete %s no exento)", evaluables, ruta, c.pkg)
				}
				exigeViolacion(t, vs, ruta, "0.0 %", "80")
				if len(vs) != 1 {
					t.Errorf("se esperaba 1 violación; hay %d: %v", len(vs), vs)
				}
			}

			double := "internal/modulos/m/" + c.dir + "/double.go"
			fuentes = []Fuente{fuenteEnMemoria(t, double, "package "+c.pkg+helperTestDoubleBody)}
			perfil = map[string]Fichero{"mod/" + double: {Sentencias: 1, Cubiertas: 0}}
			if got := Evaluables(perfil, fuentes); !reflect.DeepEqual(got, []string{double}) {
				t.Errorf("Evaluables = %v; quiero [%s]: un doble se mide en cualquier paquete", got, double)
			}
			exigeViolacion(t, Cobertura(perfil, fuentes, 80), double, "0.0 %", "80")
		})
	}
}

// TestCoberturaContractSuiteFile: dentro de un paquete …helpertest, la cobertura deja fuera
// exactamente los ficheros que contractSuiteFileCases marca como de suite (D-F1-13). Cada
// nombre se prueba con el MISMO código al 0 %: de suite = fuera de Evaluables y sin
// violación; no de suite = en Evaluables y con violación por umbral. Y el MISMO nombre en un
// paquete que no es …helpertest se mide siempre.
func TestCoberturaContractSuiteFile(t *testing.T) {
	for _, c := range contractSuiteFileCases {
		t.Run(c.name, func(t *testing.T) {
			file := "internal/modulos/m/cosa/cosahelpertest/" + c.base
			sources := []Fuente{fuenteEnMemoria(t, file, "package cosahelpertest"+helperTestDoubleBody)}
			profile := map[string]Fichero{"mod/" + file: {Sentencias: 1, Cubiertas: 0}}

			measured, vs := Evaluables(profile, sources), Cobertura(profile, sources, 80)
			if c.suite {
				if len(measured) != 0 {
					t.Errorf("Evaluables = %v; quiero ninguno (%s es un fichero de suite)", measured, c.base)
				}
				exigeCero(t, vs)
			} else {
				if !reflect.DeepEqual(measured, []string{file}) {
					t.Errorf("Evaluables = %v; quiero [%s] (%s no es un fichero de suite)", measured, file, c.base)
				}
				exigeViolacion(t, vs, file, "0.0 %", "80")
				if len(vs) != 1 {
					t.Errorf("se esperaba 1 violación; hay %d: %v", len(vs), vs)
				}
			}

			plain := "internal/modulos/m/cosa/" + c.base
			sources = []Fuente{fuenteEnMemoria(t, plain, "package cosa"+helperTestDoubleBody)}
			profile = map[string]Fichero{"mod/" + plain: {Sentencias: 1, Cubiertas: 0}}
			if got := Evaluables(profile, sources); !reflect.DeepEqual(got, []string{plain}) {
				t.Errorf("Evaluables = %v; quiero [%s]: fuera de un paquete …helpertest todo se mide", got, plain)
			}
			exigeViolacion(t, Cobertura(profile, sources, 80), plain, "0.0 %", "80")
		})
	}
}
