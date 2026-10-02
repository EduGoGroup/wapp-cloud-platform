// Package candados es la lógica de los candados de fichero de la reconstrucción modular
// (05 §5): fronteras entre módulos, un fichero un test, exportados cubiertos, sin BD viva y
// cobertura por fichero. Los tests del árbol real (internal/modulos/*_test.go,
// test/procesos/sin_bd_viva_test.go, make cobertura-ficheros) son finos: recorren el árbol
// con Recorrer, llaman a una función de este paquete y exigen cero violaciones. La lógica
// vive aquí para demostrar que MUERDE contra árboles de prueba en testdata/<candado>/muerde
// (≥ 1 violación nombrando fichero y motivo) y testdata/<candado>/pasa (0 violaciones).
//
// Leer código como texto (AST) está prohibido en los tests del árbol nuevo (05 E-7) salvo en
// los candados: este paquete y la huella del arranque son esa excepción, y ninguna otra.
//
// Promesas comunes a todos los candados: devuelven las violaciones ordenadas por Fichero y,
// a igual Fichero, por Motivo (salida determinista, diffable); cero violaciones es un slice
// de longitud 0; no leen el disco (trabajan sobre las Fuente ya parseadas), salvo Recorrer.
//
// Tres de ellos (un fichero un test, exportados cubiertos y cobertura por fichero) tratan
// aparte a los paquetes de suite de contrato y dobles (D-F1-3 y D-F1-6). Qué paquete es ese
// se decide en UN sitio, isHelperTestPackage: aquel cuyo nombre termina en el sufijo
// compuesto «helpertest» (D-F1-10), p. ej. contacthelpertest. Los dos primeros lo dejan fuera
// entero; la cobertura por fichero, solo sus ficheros de suite —contrato.go y *_contrato.go,
// isContractSuiteFile—, y mide sus dobles con lógica (D-F1-13).
package candados

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Violacion es un incumplimiento de un candado: el fichero culpable (ruta con barras,
// relativa a la raíz recorrida, igual que Fuente.Ruta) y el motivo legible por un humano.
type Violacion struct {
	Fichero string
	Motivo  string
}

// String devuelve la violación como "fichero: motivo" —exactamente Fichero, dos puntos, un
// espacio y Motivo—, el formato con el que los tests finos la imprimen.
func (v Violacion) String() string {
	return v.Fichero + ": " + v.Motivo
}

// Fuente es un fichero .go ya parseado por Recorrer.
//
//   - Ruta: ruta con barras «/» (también en Windows), relativa a la raíz recorrida; p. ej.
//     "internal/modulos/edge/lease/lease.go". Es la clave con la que los candados nombran
//     el fichero en sus violaciones.
//   - Archivo: el AST completo, con comentarios (parser.ParseComments).
//   - Fset: el FileSet con el que se parseó Archivo; todas las Fuente de una misma llamada
//     a Recorrer comparten el mismo.
//   - EsTest: true si el nombre del fichero termina en "_test.go".
//   - Paquete: el nombre de paquete de la cláusula `package` (p. ej. "lease", "lease_test",
//     "leasehelpertest"). Lo expone para que la exención de D-F1-3 y D-F1-6 (paquetes cuyo
//     nombre termina en "helpertest", D-F1-10: isHelperTestPackage) sea UNA condición en este
//     paquete y no una lista de rutas.
type Fuente struct {
	Ruta    string
	Archivo *ast.File
	Fset    *token.FileSet
	EsTest  bool
	Paquete string
}

// helperTestSuffix es el sufijo COMPUESTO del nombre de paquete que distingue al paquete de
// las suites de contrato y los dobles de un puerto. UnFicheroUnTest y ExportadosCubiertos lo
// dejan fuera entero; la cobertura por fichero (Evaluables y Cobertura), desde D-F1-13, solo
// sus ficheros de suite (isContractSuiteFile). La suite del paquete <paquete> vive en el
// paquete <paquete>helpertest, en <dir>/<paquete>helpertest (p. ej.
// internal/nucleo/contact/contacthelpertest).
//
// D-F1-10 (Jhoan, 2026-10-02). ESTRECHA D-F1-3 (exentos de UnFicheroUnTest y de
// ExportadosCubiertos) y D-F1-6 (exentos de la cobertura por fichero); no las deroga: lo que
// cambia es cómo se reconoce el paquete. Hasta D-F1-10 el sufijo era "test" a secas, y un
// paquete de PRODUCCIÓN cuyo nombre acabara en «test» por casualidad (latest, contest,
// attest…) quedaba exento de los tres sin que nadie lo hubiera decidido (hallazgo 21 de la
// revisión de S9–S11, medido con una sonda en internal/nucleo/latest).
const helperTestSuffix = "helpertest"

// isHelperTestPackage dice si name —el nombre de un paquete: la cláusula `package`
// (Fuente.Paquete), no su directorio— es el de un paquete de suite de contrato y dobles, el
// único al que los tres candados de fichero tratan aparte (D-F1-3, D-F1-6, estrechadas por
// D-F1-10; la cobertura por fichero, además, por D-F1-13: ver isContractSuiteFile).
//
// Lo es si y solo si termina en helperTestSuffix Y tiene al menos un carácter delante:
// "contacthelpertest" y "xhelpertest" sí. No lo son:
//   - un nombre que termina en "test" sin terminar en "helpertest": un paquete de producción
//     como "latest" o "contest", el nombre viejo de las suites ("cosatest", "fleettest") y
//     "huellatest", que conserva su nombre y por tanto se mide como cualquier otro;
//   - "helpertest" a secas: el sufijo nombra de QUIÉN es la suite (<paquete>helpertest), y
//     sin paquete delante no es la suite de nadie —haySuiteContrato nunca la buscaría—. Se
//     trata como producción: un candado, ante la duda, muerde;
//   - el sufijo en otro sitio ("helpertestcosa", "cosahelpertests") o con otra grafía
//     ("cosaHelperTest"): la comparación es exacta, con mayúsculas y minúsculas.
//
// Es la ÚNICA definición del criterio: UnFicheroUnTest, ExportadosCubiertos, haySuiteContrato
// e isContractSuiteFile (y por él la cobertura por fichero) la usan, para que no puedan
// divergir.
func isHelperTestPackage(name string) bool {
	return len(name) > len(helperTestSuffix) && strings.HasSuffix(name, helperTestSuffix)
}

// contractSuiteFile y contractSuiteSuffix son los dos nombres de un FICHERO DE SUITE de
// contrato: «contrato.go», donde vive `func Contrato(t *testing.T, …)`, y «<tema>_contrato.go»,
// los trozos por tema en que se parte una suite grande (p. ej. merge_contrato.go). «contrato»
// es vocabulario del método (05 E-11, excepción 3), y por eso no se traduce.
const (
	contractSuiteFile   = "contrato.go"
	contractSuiteSuffix = "_contrato.go"
)

// isContractSuiteFile dice si f es un fichero de suite de contrato: el ÚNICO que la cobertura
// por fichero (Evaluables y Cobertura) deja sin medir dentro de un paquete …helpertest
// (D-F1-13, Jhoan, 2026-10-02, que estrecha D-F1-6).
//
// Lo es si y solo si se cumplen LAS DOS:
//   - su paquete es de suite y dobles: isHelperTestPackage(f.Paquete), la cláusula `package`
//     y no el directorio. Un contrato.go o un x_contrato.go en un paquete que no lo es (uno de
//     producción, o uno con el sufijo viejo «test») NO es un fichero de suite: se mide;
//   - su nombre base (el último elemento de f.Ruta) es exactamente «contrato.go», o termina en
//     «_contrato.go» con al menos un carácter delante. No lo son «_contrato.go» a secas (la
//     toolchain de Go ni lo compila), «micontrato.go» (sin el guion bajo), «contrato_x.go»
//     (el sufijo en otro sitio), «Contrato.go» (otra grafía) ni un _test.go.
//
// El porqué de la exención es mecánico, no de confianza: la suite de un puerto solo la
// ejecutan los tests de sus implementaciones, que viven en OTROS paquetes, y `go test -cover`
// sin -coverpkg no cuenta lo que se ejecuta desde otro paquete: en el perfil de su propio
// paquete el fichero sale al 0 % aunque esté ejercitado entero. Los demás ficheros de un
// paquete …helpertest —los dobles con lógica, como contacthelpertest/estado.go— sí se
// ejecutan desde el test de su propio paquete, así que se miden con el umbral normal. Hasta
// D-F1-13 quedaba exento el paquete entero, y los dobles en memoria que 05 E-6 manda crear
// habrían nacido sin medir.
//
// Solo lo usa la cobertura por fichero. UnFicheroUnTest y ExportadosCubiertos siguen dejando
// fuera el paquete …helpertest ENTERO (D-F1-3), con isHelperTestPackage.
func isContractSuiteFile(f Fuente) bool {
	if !isHelperTestPackage(f.Paquete) {
		return false
	}
	base := path.Base(f.Ruta)
	return base == contractSuiteFile ||
		(len(base) > len(contractSuiteSuffix) && strings.HasSuffix(base, contractSuiteSuffix))
}

// Recorrer parsea, recursivamente, todos los ficheros .go bajo raiz/dir para cada dir de
// dirs (dir relativo a raiz, con barras) y devuelve una Fuente por fichero.
//
// Promesas:
//   - parsea con go/parser y parser.ParseComments; las etiquetas de compilación se ignoran
//     (un test con `//go:build pendiente` se devuelve como cualquier otro);
//   - salta todo directorio llamado "testdata" que encuentre al recorrer (a cualquier
//     profundidad), como la toolchain de Go; la propia raiz puede estar dentro de un
//     testdata —así se recorren los árboles de prueba—, lo que se salta es lo que cuelga
//     por debajo de raiz/dir;
//   - ignora los ficheros que no terminan en ".go";
//   - con incluirTests = false omite los "_test.go"; con true los incluye (EsTest = true);
//   - un dir que no existe aporta cero ficheros y NO es un error (el árbol nuevo aún está
//     vacío en F0); tampoco lo es una raiz inexistente;
//   - un fichero que no parsea es un error que nombra la ruta, y no se devuelve resultado
//     parcial (un fichero ilegible no se salta en silencio: sería un agujero del candado);
//   - el resultado está ordenado por Ruta y no repite ficheros aunque dos dirs se solapen.
func Recorrer(raiz string, dirs []string, incluirTests bool) ([]Fuente, error) {
	// Primero se reúnen las rutas (un conjunto: dos dirs solapados no repiten) y solo después
	// se parsea, en orden, cada fichero una vez.
	vistos := make(map[string]string) // Ruta con barras → ruta en disco
	for _, dir := range dirs {
		if err := listarGo(raiz, dir, incluirTests, vistos); err != nil {
			return nil, err
		}
	}
	claves := make([]string, 0, len(vistos))
	for ruta := range vistos {
		claves = append(claves, ruta)
	}
	sort.Strings(claves)

	fset := token.NewFileSet()
	fuentes := make([]Fuente, 0, len(claves))
	for _, ruta := range claves {
		// ParseFile no evalúa las etiquetas de compilación: un test con `//go:build pendiente`
		// se parsea como cualquier otro, que es justo lo que promete el contrato.
		archivo, err := parser.ParseFile(fset, vistos[ruta], nil, parser.ParseComments)
		if err != nil {
			// Sin resultado parcial: un fichero ilegible saltado en silencio sería un agujero
			// del candado.
			return nil, fmt.Errorf("candados: %s no parsea: %w", ruta, err)
		}
		fuentes = append(fuentes, Fuente{
			Ruta:    ruta,
			Archivo: archivo,
			Fset:    fset,
			EsTest:  strings.HasSuffix(ruta, "_test.go"),
			Paquete: archivo.Name.Name,
		})
	}
	return fuentes, nil
}

// listarGo añade a vistos los .go bajo raiz/dir, saltando los testdata que cuelgan por debajo
// y, si !incluirTests, los _test.go. Un raiz/dir inexistente no aporta nada y no es error: el
// árbol nuevo está vacío en F0 y el candado debe pasar igual.
func listarGo(raiz, dir string, incluirTests bool, vistos map[string]string) error {
	inicio := filepath.Join(raiz, filepath.FromSlash(dir))
	if _, err := os.Stat(inicio); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return filepath.WalkDir(inicio, func(ruta string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("candados: recorriendo %s: %w", ruta, err)
		}
		if d.IsDir() {
			// Como la toolchain de Go: testdata no es código del paquete. Solo por debajo del
			// inicio, que sí puede estar dentro de un testdata (así se recorren los árboles
			// de prueba de este mismo paquete).
			if ruta != inicio && d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		if !incluirTests && strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(raiz, ruta)
		if err != nil {
			return fmt.Errorf("candados: ruta relativa de %s: %w", ruta, err)
		}
		vistos[filepath.ToSlash(rel)] = ruta
		return nil
	})
}
