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

// TestAyudasComunes: las ayudas que comparten los tests de los candados aciertan sobre
// violaciones conocidas. Son el oráculo de los demás tests: si contieneTodos aceptara un
// trozo ausente, un caso `muerde` pasaría sin morder.
func TestAyudasComunes(t *testing.T) {
	vs := []Violacion{
		{Fichero: "internal/x/a.go", Motivo: "falta a_test.go"},
		{Fichero: "internal/x/a.go", Motivo: "importa internal/viejo"},
		{Fichero: "internal/x/b.go", Motivo: "falta b_test.go"},
	}
	exigeViolacion(t, vs, "internal/x/a.go", "falta", "a_test.go")
	exigeNingunaEn(t, vs, "internal/x/c.go")
	exigeOrdenadas(t, vs)
	exigeCero(t, nil)
	exigeCero(t, []Violacion{})
	if contieneTodos("falta a_test.go", []string{"falta", "b_test.go"}) {
		t.Error("contieneTodos aceptó un trozo ausente")
	}
	if !contieneTodos("cualquier motivo", nil) {
		t.Error("contieneTodos sin trozos debe aceptar")
	}
}

// ── La exención de suites y dobles: el sufijo compuesto «helpertest» (D-F1-10) ───────────

// helperTestDoubleBody es lo que sigue a la cláusula `package` en el fichero con el que los
// tres candados prueban cada nombre de helperTestSuffixCases: una función exportada con
// lógica. Es el MISMO código para todos los nombres: lo único que cambia entre un caso exento
// y uno que muerde es el nombre del paquete.
const helperTestDoubleBody = "\n\n// Next devuelve el siguiente.\nfunc Next(n int) int { return n + 1 }\n"

// helperTestSuffixCases son los nombres de paquete que fijan el borde de D-F1-10 (Jhoan,
// 2026-10-02) en los TRES candados (UnFicheroUnTest, ExportadosCubiertos y la cobertura por
// fichero): es de suite y dobles el paquete cuyo nombre (la cláusula `package`, pkg; no el
// directorio, dir) termina en «helpertest» y tiene algo delante. Un nombre que termina en
// «test» a secas —uno de producción como latest, o el nombre viejo de las suites, cosatest—
// ya no lo es, y «helpertest» sin prefijo tampoco (no es la suite de ningún paquete). Los dos
// primeros candados dejan fuera ese paquete entero; la cobertura por fichero, solo sus
// ficheros de suite (D-F1-13), y por eso prueba estos nombres con un contrato.go.
var helperTestSuffixCases = []struct {
	name   string
	dir    string
	pkg    string
	exempt bool
}{
	{"cosahelpertest is the suite or the doubles of package cosa", "cosahelpertest", "cosahelpertest", true},
	{"contacthelpertest is the suite of the pilot", "contacthelpertest", "contacthelpertest", true},
	{"a one-letter prefix before helpertest is enough", "xhelpertest", "xhelpertest", true},
	{"a helpertest package in a directory that is not one stays exempt", "other", "otherhelpertest", true},
	{"a helpertest directory holding a regular package is not exempt", "cosahelpertest", "cosa", false},
	{"cosa is the control twin", "cosa", "cosa", false},
	{"latest is a production package ending in test by chance", "latest", "latest", false},
	{"contest is another production package ending in test", "contest", "contest", false},
	{"cosatest carries the old bare suffix", "cosatest", "cosatest", false},
	{"huellatest keeps its name and loses the exemption", "huellatest", "huellatest", false},
	{"wrappertest ends in pertest, not in helpertest", "wrappertest", "wrappertest", false},
	{"yelpertest ends in elpertest, not in helpertest", "yelpertest", "yelpertest", false},
	{"helpertest alone has no package before the suffix", "helpertest", "helpertest", false},
	{"helpertestcosa has the suffix at the wrong end", "helpertestcosa", "helpertestcosa", false},
	{"cosahelpertests is a plural, not the suffix", "cosahelpertests", "cosahelpertests", false},
	{"test alone is not exempt either", "test", "test", false},
}

// TestIsHelperTestPackage: la única definición del criterio de D-F1-10. Exento es el nombre
// que termina en «helpertest» con algo delante; ni «test» a secas (latest, cosatest,
// huellatest), ni un trozo del sufijo (pertest, elpertest), ni «helpertest» sin prefijo, ni
// el sufijo en otro sitio, ni con otra grafía, ni el nombre vacío.
func TestIsHelperTestPackage(t *testing.T) {
	if helperTestSuffix != "helpertest" {
		t.Errorf("helperTestSuffix = %q; D-F1-10 fija %q", helperTestSuffix, "helpertest")
	}
	for _, c := range helperTestSuffixCases {
		t.Run(c.name, func(t *testing.T) {
			if got := isHelperTestPackage(c.pkg); got != c.exempt {
				t.Errorf("isHelperTestPackage(%q) = %v; quiero %v", c.pkg, got, c.exempt)
			}
		})
	}
	for _, name := range []string{"", "cosaHelperTest", "cosaHELPERTEST", "cosa_helper_test", "cosahelpertest_test"} {
		if isHelperTestPackage(name) {
			t.Errorf("isHelperTestPackage(%q) = true; quiero false", name)
		}
	}
}

// ── El fichero de suite: contrato.go y *_contrato.go de un paquete …helpertest (D-F1-13) ──

// contractSuiteFileCases son los nombres base que fijan el borde de D-F1-13 (Jhoan,
// 2026-10-02): dentro de un paquete …helpertest, es fichero de suite —y por eso queda fuera de
// la cobertura por fichero— el que se llama exactamente «contrato.go» o termina en
// «_contrato.go» con algo delante. Todo lo demás es un doble o una ayuda, y se mide.
var contractSuiteFileCases = []struct {
	name  string
	base  string
	suite bool
}{
	{"contrato.go holds the Contrato suite", "contrato.go", true},
	{"merge_contrato.go is a themed piece of the suite", "merge_contrato.go", true},
	{"a one-letter theme is enough", "x_contrato.go", true},
	{"a theme with underscores", "push_name_contrato.go", true},
	{"estado.go is a double with logic", "estado.go", false},
	{"memory.go is an in-memory double", "memory.go", false},
	{"doble.go is a double", "doble.go", false},
	{"_contrato.go has nothing before the suffix", "_contrato.go", false},
	{"micontrato.go lacks the underscore", "micontrato.go", false},
	{"contrato_merge.go has the word at the wrong end", "contrato_merge.go", false},
	{"contratos.go is a plural", "contratos.go", false},
	{"xcontrato.go is not contrato.go", "xcontrato.go", false},
	{"Contrato.go has another spelling", "Contrato.go", false},
	{"merge_Contrato.go has another spelling", "merge_Contrato.go", false},
	{"merge_contrato.go.go ends in something else", "merge_contrato.go.go", false},
	{"contract.go is the translation, not the method word", "contract.go", false},
}

// TestIsContractSuiteFile: la única definición de «fichero de suite» (D-F1-13). Lo es el
// fichero de un paquete …helpertest (por su cláusula `package`, no por su directorio) cuyo
// nombre base es contrato.go o <algo>_contrato.go. El MISMO nombre en un paquete que no es
// …helpertest no lo es, y lo que cuenta es el nombre base: un directorio llamado como un
// fichero de suite no convierte en suite lo que contiene.
func TestIsContractSuiteFile(t *testing.T) {
	if contractSuiteFile != "contrato.go" || contractSuiteSuffix != "_contrato.go" {
		t.Errorf("contractSuiteFile = %q, contractSuiteSuffix = %q; D-F1-13 fija contrato.go y _contrato.go",
			contractSuiteFile, contractSuiteSuffix)
	}
	const d = "internal/nucleo/cosa/cosahelpertest/"
	for _, c := range contractSuiteFileCases {
		t.Run(c.name, func(t *testing.T) {
			if got := isContractSuiteFile(Fuente{Ruta: d + c.base, Paquete: "cosahelpertest"}); got != c.suite {
				t.Errorf("isContractSuiteFile(%s, package cosahelpertest) = %v; quiero %v", c.base, got, c.suite)
			}
			for _, pkg := range []string{"cosa", "cosatest", "latest", "helpertest", "cosahelpertest_test"} {
				if isContractSuiteFile(Fuente{Ruta: d + c.base, Paquete: pkg}) {
					t.Errorf("isContractSuiteFile(%s, package %s) = true; quiero false: no es un paquete …helpertest", c.base, pkg)
				}
			}
		})
	}
	// Sin directorio, el nombre base es la ruta entera; y el directorio no cuenta.
	edges := []struct {
		path  string
		suite bool
	}{
		{"contrato.go", true},
		{"merge_contrato.go", true},
		{"a/b/c/contrato.go", true},
		{"internal/contrato.go/estado.go", false},
		{"internal/merge_contrato.go/estado.go", false},
		{"", false},
	}
	for _, b := range edges {
		if got := isContractSuiteFile(Fuente{Ruta: b.path, Paquete: "cosahelpertest"}); got != b.suite {
			t.Errorf("isContractSuiteFile(%q) = %v; quiero %v", b.path, got, b.suite)
		}
	}
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
