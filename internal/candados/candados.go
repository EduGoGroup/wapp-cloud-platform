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
package candados

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
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
//     "leasetest"). Lo expone para que la exención de D-F1-3 (paquetes cuyo nombre termina
//     en "test") sea UNA condición en este paquete y no una lista de rutas.
type Fuente struct {
	Ruta    string
	Archivo *ast.File
	Fset    *token.FileSet
	EsTest  bool
	Paquete string
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
