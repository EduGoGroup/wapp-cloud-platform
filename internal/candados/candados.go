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
	"go/ast"
	"go/token"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
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
	panic(pendiente.Implementar("candados.Violacion.String"))
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
	panic(pendiente.Implementar("candados.Recorrer"))
}
