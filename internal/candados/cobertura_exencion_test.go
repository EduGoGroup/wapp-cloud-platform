//go:build pendiente

package candados

import (
	"reflect"
	"slices"
	"testing"
)

// D-F1-6 (Jhoan, 2026-10-01): los paquetes …test (suites Contrato y dobles, p. ej.
// contacttest) quedan exentos también de la cobertura por fichero (D-12). Su suite solo la
// ejecutan los tests de las implementaciones, que viven en otros paquetes, y `go test -cover`
// sin -coverpkg no cuenta lo que se ejecuta desde otro paquete: en el perfil del propio
// paquete …test el fichero sale al 0 % y rompería `make cobertura-ficheros`.
//
// Rojo: Evaluables (y con él Cobertura) todavía los mide. Los árboles de prueba están en
// testdata/cobertura/paquetes-test/{pasa,muerde}; los casos sin árbol, en memoria.

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
// cuyo nombre termina en «test», aunque el perfil le dé sentencias y ninguna cubierta; el
// paquete que no lo es sigue midiéndose; y el contrato en rojo y el adaptador Postgres
// legítimo siguen fuera, como antes.
func TestCoberturaPaquetesTestEvaluables(t *testing.T) {
	casos := []struct {
		nombre    string
		raiz      string
		sinCubrir map[string]string // fichero con sentencias y 0 cubiertas → su paquete
		quiero    []string
	}{
		{"pasa: la suite y el doble del paquete …test no se miden; cosa.go sí", arbolPaquetesTestPasa,
			map[string]string{
				dirPaquetesTest + "cosatest/doble.go":    "cosatest",
				dirPaquetesTest + "cosatest/contrato.go": "cosatest",
				dirPaquetesTest + "pg/pg.go":             "pg",
				dirPaquetesTest + "rojo/rojo.go":         "rojo",
			},
			[]string{dirPaquetesTest + "cosa/cosa.go"}},
		{"muerde: el MISMO doble se mide si su paquete no acaba en test", arbolPaquetesTestMuerde,
			map[string]string{
				dirPaquetesTest + "cosa/doble.go":     "cosa",
				dirPaquetesTest + "cosatest/doble.go": "cosatest",
				dirPaquetesTest + "rojo/rojo.go":      "rojo",
			},
			[]string{dirPaquetesTest + "cosa/doble.go"}},
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

// TestCoberturaPaquetesTestViolaciones: un árbol con un paquete …test al 0 %, un contrato en
// rojo y un adaptador Postgres exento no da ninguna violación (el 80 % del resto sí se
// mide), y el adaptador sigue contando como exento.
func TestCoberturaPaquetesTestViolaciones(t *testing.T) {
	perfil, fuentes := casoCobertura(t, arbolPaquetesTestPasa)
	exigeSinCubrirEn(t, perfil, fuentes, dirPaquetesTest+"cosatest/doble.go", "cosatest")
	exigeSinCubrirEn(t, perfil, fuentes, dirPaquetesTest+"cosatest/contrato.go", "cosatest")

	exigeCero(t, Cobertura(perfil, fuentes, 80))
	if got, quiero := Exentos(fuentes), []string{dirPaquetesTest + "pg/pg.go"}; !reflect.DeepEqual(got, quiero) {
		t.Errorf("Exentos = %v; quiero %v", got, quiero)
	}
}

// TestCoberturaPaquetesTestContraste: el MISMO doble al 0 %, en un paquete cuyo nombre NO
// termina en «test», sigue siendo violación por umbral; el de su vecino cosatest no.
func TestCoberturaPaquetesTestContraste(t *testing.T) {
	perfil, fuentes := casoCobertura(t, arbolPaquetesTestMuerde)
	exigeSinCubrirEn(t, perfil, fuentes, dirPaquetesTest+"cosa/doble.go", "cosa")
	exigeSinCubrirEn(t, perfil, fuentes, dirPaquetesTest+"cosatest/doble.go", "cosatest")

	vs := Cobertura(perfil, fuentes, 80)
	exigeViolacion(t, vs, dirPaquetesTest+"cosa/doble.go", "0.0 %", "80")
	exigeNingunaEn(t, vs, dirPaquetesTest+"cosatest/doble.go")
	exigeNingunaEn(t, vs, dirPaquetesTest+"rojo/rojo.go")
	if len(vs) != 1 {
		t.Errorf("se esperaba 1 violación; hay %d: %v", len(vs), vs)
	}
}

// TestCoberturaPaquetesTestBordes: la exención de D-F1-6 es por el nombre del PAQUETE (la
// cláusula `package`, la misma condición que D-F1-3), no por el directorio ni por la nota:
// un …test al 100 % tampoco se mide. No cambia lo demás: un …test en rojo o con adaptador
// Postgres legítimo sigue fuera de Evaluables, y una marca de Postgres ilegítima en un …test
// sigue siendo violación de marca (nadie se exime por decreto, 05 E-6) pero ya no de umbral.
func TestCoberturaPaquetesTestBordes(t *testing.T) {
	const fuenteRoja = "package cosatest\n\nimport \"mod/pendiente\"\n\nfunc R() { panic(pendiente.Implementar(\"R\")) }\n"
	casos := []struct {
		nombre    string
		ruta      string
		src       string
		cubiertas int // de las 2 sentencias que el perfil le da
		medido    bool
	}{
		{"paquete que no es …test: se mide", "x/cosa/cosa.go", "package cosa\n\nfunc A() {}\n", 0, true},
		{"paquete …test al 0 %: no se mide", "x/cosatest/mal.go", "package cosatest\n\nfunc C() {}\n", 0, false},
		{"paquete …test al 100 %: tampoco, la exención no es por nota", "x/cosatest/bien.go", "package cosatest\n\nfunc B() {}\n", 2, false},
		{"directorio …test con package que no lo es: se mide", "y/cosatest/dir.go", "package cosa\n\nfunc D() {}\n", 0, true},
		{"package …test en un directorio que no lo es: no se mide", "z/otro/otro.go", "package otrotest\n\nfunc E() {}\n", 0, false},
		{"paquete …test en rojo: fuera, como antes", "x/cosatest/rojo.go", fuenteRoja, 0, false},
		{"paquete …test con adaptador Postgres legítimo: fuera, como antes", "x/cosatest/pg.go",
			MarcaPostgres + "\n\npackage cosatest\n\nimport \"database/sql\"\n\nvar _ *sql.DB\n", 0, false},
		{"paquete …test con marca ilegítima: fuera del umbral", "x/cosatest/falsa.go",
			MarcaPostgres + "\n\npackage cosatest\n\nfunc F() {}\n", 0, false},
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
	if got, quiero := Exentos(fuentes), []string{"x/cosatest/pg.go"}; !reflect.DeepEqual(got, quiero) {
		t.Errorf("Exentos = %v; quiero %v", got, quiero)
	}

	vs := Cobertura(perfil, fuentes, 80)
	exigeViolacion(t, vs, "x/cosa/cosa.go", "0.0 %", "80")
	exigeViolacion(t, vs, "y/cosatest/dir.go", "0.0 %", "80")
	exigeViolacion(t, vs, "x/cosatest/falsa.go", "marca", "postgres")
	if len(vs) != 3 {
		t.Errorf("se esperaban 3 violaciones (dos por umbral, una por marca); hay %d: %v", len(vs), vs)
	}
	exigeOrdenadas(t, vs)
}
