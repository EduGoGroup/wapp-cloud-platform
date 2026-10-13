package candados

import (
	"strings"
	"testing"
)

// TestUnFicheroUnTestMuerde: sin test y sin excepción verificada, cada x.go es una
// violación que nombra el x_test.go que falta. Desde D-F1-10 eso incluye a todo paquete cuyo
// nombre termina en «test» sin terminar en «helpertest»: uno de producción (latest), uno con
// el nombre viejo de las suites (cosatest) y el puerto cuya suite vive en <paquete>test.
func TestUnFicheroUnTestMuerde(t *testing.T) {
	fuentes := recorrerCaso(t, "testdata/unfichero/muerde", []string{"internal"}, true)
	vs := UnFicheroUnTest(fuentes)

	const m = "internal/modulos/m/"
	casos := []struct {
		nombre  string
		fichero string
		falta   string
	}{
		{"fichero sin test al lado", m + "huerfano/huerfano.go", "falta huerfano_test.go"},
		{"un «puerto» con una función ya no es solo de interfaces, aunque tenga suite", m + "puerto/puerto.go", "falta puerto_test.go"},
		{"un puerto sin suite Contrato en <paquete>helpertest necesita test", m + "sinsuite/sinsuite.go", "falta sinsuite_test.go"},
		{"un doc.go con una declaración necesita test", m + "documento/doc.go", "falta doc_test.go"},
		{"un fichero de //go:embed con otra var necesita test", m + "embebido/embed.go", "falta embed_test.go"},
		{"a production package whose name merely ends in test (latest) is not exempt", m + "latest/latest.go", "falta latest_test.go"},
		{"the old bare test suffix (cosatest) no longer exempts", m + "cosatest/doble.go", "falta doble_test.go"},
		{"control twin of the helpertest double, in a regular package", m + "cosa/doble.go", "falta doble_test.go"},
		{"a port whose suite lives in <package>test, the old name, needs a test", m + "legacysuite/legacysuite.go", "falta legacysuite_test.go"},
		{"a suite in a package with the old bare test suffix needs a test itself", m + "legacysuite/legacysuitetest/contrato.go", "falta contrato_test.go"},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			exigeViolacion(t, vs, c.fichero, c.falta)
		})
	}
	if len(vs) != len(casos) {
		t.Errorf("se esperaban %d violaciones; hay %d: %v", len(casos), len(vs), vs)
	}
	// La suite y los dobles de un paquete …helpertest no necesitan test (D-F1-3 = sí, con el
	// sufijo compuesto de D-F1-10): ni la suite del puerto, ni el MISMO doble que en cosa y
	// en cosatest sí muerde.
	exigeNingunaEn(t, vs, m+"puerto/puertohelpertest/contrato.go")
	exigeNingunaEn(t, vs, m+"cosahelpertest/doble.go")
	exigeOrdenadas(t, vs)
}

// TestUnFicheroUnTestPasa: con test al lado, doc.go solo con su comentario, ficheros solo de
// //go:embed (también en grupo), un puerto con su suite Contrato en <paquete>helpertest, y un
// paquete …helpertest entero (incluido un doble con lógica, D-F1-3 = sí; el sufijo, D-F1-10):
// cero violaciones.
func TestUnFicheroUnTestPasa(t *testing.T) {
	fuentes := recorrerCaso(t, "testdata/unfichero/pasa", []string{"internal"}, true)
	exigeCero(t, UnFicheroUnTest(fuentes))
}

// TestUnFicheroUnTestSoloEntreFuentes: el test se busca solo entre fuentes; sin los tests
// (incluirTests = false), el fichero que sí lo tiene en disco muerde.
func TestUnFicheroUnTestSoloEntreFuentes(t *testing.T) {
	fuentes := recorrerCaso(t, "testdata/unfichero/pasa", []string{"internal"}, false)
	vs := UnFicheroUnTest(fuentes)
	exigeViolacion(t, vs, "internal/modulos/m/con/con.go", "falta con_test.go")
}

// TestUnFicheroUnTestBordes: cada excepción se verifica a fondo, en memoria. Un doc.go con
// solo un import, un fichero vacío que no es doc.go, un `type` que no es interfaz, una var
// con otra directiva, y una suite cuyo Contrato no casa (método, sin parámetros, primer
// parámetro que no es *testing.T, paquete o directorio equivocados) no eximen; el alias del
// import "testing" sí se respeta; dos violaciones del mismo directorio salen ordenadas. La
// suite vive en <paquete>helpertest (D-F1-10): aquí, phelpertest en p/phelpertest.
func TestUnFicheroUnTestBordes(t *testing.T) {
	const d = "internal/modulos/m/p/"
	const puerto = "package p\n\n// R es el puerto.\ntype R interface{ Leer() int }\n"
	suite := func(cuerpo string) string {
		return "package phelpertest\n\nimport tst \"testing\"\n\n" + cuerpo + "\n"
	}
	casos := []struct {
		nombre string
		ruta   string
		src    string
		extra  []Fuente
		muerde bool
	}{
		{"doc.go con solo un import necesita test", d + "doc.go", "package p\n\nimport _ \"embed\"\n", nil, true},
		{"fichero vacío que no es doc.go necesita test", d + "vacio.go", "package p\n", nil, true},
		{"type que no es interfaz no es puerto", d + "tipo.go", "package p\n\ntype T struct{}\n", nil, true},
		{"var con otra directiva no es de //go:embed", d + "var.go", "package p\n\n//go:generate x\nvar V string\n", nil, true},
		{"const no es //go:embed", d + "const.go", "package p\n\nconst C = 1\n", nil, true},
		{"var sin comentario no es de //go:embed", d + "suelta.go", "package p\n\nvar V = 1\n", nil, true},
		{"puerto con suite que importa testing con alias", d + "p.go", puerto,
			[]Fuente{fuenteEnMemoria(t, d+"phelpertest/c.go", suite("func Contrato(t *tst.T, n func() int) {}"))}, false},
		{"Contrato como método no es suite", d + "p.go", puerto,
			[]Fuente{fuenteEnMemoria(t, d+"phelpertest/c.go", suite("type S struct{}\n\nfunc (S) Contrato(t *tst.T) {}"))}, true},
		{"Contrato sin parámetros no es suite", d + "p.go", puerto,
			[]Fuente{fuenteEnMemoria(t, d+"phelpertest/c.go", suite("var _ tst.T\n\nfunc Contrato() {}"))}, true},
		{"Contrato con testing.T por valor no es suite", d + "p.go", puerto,
			[]Fuente{fuenteEnMemoria(t, d+"phelpertest/c.go", suite("func Contrato(t tst.T) {}"))}, true},
		{"Contrato con *testing.B no es suite", d + "p.go", puerto,
			[]Fuente{fuenteEnMemoria(t, d+"phelpertest/c.go", suite("func Contrato(b *tst.B) {}"))}, true},
		{"Contrato con *T sin paquete no es suite", d + "p.go", puerto,
			[]Fuente{fuenteEnMemoria(t, d+"phelpertest/c.go", suite("type T struct{}\n\nvar _ tst.T\n\nfunc Contrato(t *T) {}"))}, true},
		{"*testing.T sin importar testing no es suite", d + "p.go", puerto,
			[]Fuente{fuenteEnMemoria(t, d+"phelpertest/c.go", "package phelpertest\n\nimport \"errors\"\n\nvar _ = errors.New\n\nfunc Contrato(t *testing.T) {}\n")}, true},
		{"suite en otro directorio no cuenta", d + "p.go", puerto,
			[]Fuente{fuenteEnMemoria(t, "internal/modulos/m/phelpertest/c.go", suite("func Contrato(t *tst.T) {}"))}, true},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			fuentes := append([]Fuente{fuenteEnMemoria(t, c.ruta, c.src)}, c.extra...)
			vs := UnFicheroUnTest(fuentes)
			if !c.muerde {
				exigeCero(t, vs)
				return
			}
			base := strings.TrimSuffix(c.ruta[len(d):], ".go")
			exigeViolacion(t, vs, c.ruta, "falta "+base+"_test.go")
			if len(vs) != 1 {
				t.Errorf("se esperaba 1 violación; hay %d: %v", len(vs), vs)
			}
		})
	}

	// Dos huérfanos: salen ordenados por Fichero aunque lleguen al revés.
	vs := UnFicheroUnTest([]Fuente{
		fuenteEnMemoria(t, d+"z.go", "package p\n"),
		fuenteEnMemoria(t, d+"a.go", "package p\n"),
	})
	if len(vs) != 2 || vs[0].Fichero != d+"a.go" {
		t.Errorf("se esperaban 2 violaciones, primero a.go; hay %v", vs)
	}
	exigeOrdenadas(t, vs)
}

// TestUnFicheroUnTestHelperTestSuffix: la exención de suites y dobles es por el sufijo
// COMPUESTO «helpertest» del nombre del paquete (D-F1-10), la cláusula `package` y no el
// directorio. Cada nombre de helperTestSuffixCases se prueba con el MISMO fichero sin test:
// exento = cero violaciones; no exento = «falta double_test.go».
func TestUnFicheroUnTestHelperTestSuffix(t *testing.T) {
	for _, c := range helperTestSuffixCases {
		t.Run(c.name, func(t *testing.T) {
			ruta := "internal/modulos/m/" + c.dir + "/double.go"
			vs := UnFicheroUnTest([]Fuente{fuenteEnMemoria(t, ruta, "package "+c.pkg+helperTestDoubleBody)})
			if c.exempt {
				exigeCero(t, vs)
				return
			}
			exigeViolacion(t, vs, ruta, "falta double_test.go")
			if len(vs) != 1 {
				t.Errorf("se esperaba 1 violación; hay %d: %v", len(vs), vs)
			}
		})
	}
}

// TestUnFicheroUnTestSuiteLookup: la suite Contrato de un puerto (fichero solo de interfaces)
// se busca en el paquete <paquete>helpertest del subdirectorio <dir>/<paquete>helpertest
// (D-F1-10). La MISMA suite con el nombre viejo <paquete>test ya no exime al puerto, ni
// mezclando nombre nuevo y viejo entre paquete y directorio, ni un paquete llamado
// «helpertest» a secas; y la suite que no está en un paquete …helpertest necesita su test.
func TestUnFicheroUnTestSuiteLookup(t *testing.T) {
	const d = "internal/modulos/m/p/"
	const port = "package p\n\n// R es el puerto.\ntype R interface{ Leer() int }\n"
	suite := func(pkg string) string {
		return "package " + pkg + "\n\nimport \"testing\"\n\nfunc Contrato(t *testing.T, n func() int) {}\n"
	}
	cases := []struct {
		name     string
		suiteDir string
		suitePkg string
		missing  []string // ficheros a los que les falta test; vacío = cero violaciones
	}{
		{"suite in p/phelpertest, package phelpertest: the port is exempt", "phelpertest", "phelpertest", nil},
		{"suite in p/ptest, package ptest (old name): port and suite need a test", "ptest", "ptest",
			[]string{d + "p.go", d + "ptest/c.go"}},
		{"new directory, old package name: port and suite need a test", "phelpertest", "ptest",
			[]string{d + "p.go", d + "phelpertest/c.go"}},
		{"old directory, new package name: the suite is exempt, the port is not", "ptest", "phelpertest",
			[]string{d + "p.go"}},
		{"bare helpertest is nobody's suite: port and suite need a test", "helpertest", "helpertest",
			[]string{d + "p.go", d + "helpertest/c.go"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			vs := UnFicheroUnTest([]Fuente{
				fuenteEnMemoria(t, d+"p.go", port),
				fuenteEnMemoria(t, d+c.suiteDir+"/c.go", suite(c.suitePkg)),
			})
			for _, fichero := range c.missing {
				base := strings.TrimSuffix(fichero[strings.LastIndex(fichero, "/")+1:], ".go")
				exigeViolacion(t, vs, fichero, "falta "+base+"_test.go")
			}
			if len(vs) != len(c.missing) {
				t.Errorf("se esperaban %d violaciones; hay %d: %v", len(c.missing), len(vs), vs)
			}
		})
	}
}
