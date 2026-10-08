package intakes

// vencimiento_lock_test.go — LOS BARRIDOS de los candados del plazo (R-06, R6.2.c).
// Los dos tests que los usan viven en vencimiento_test.go (§candados); aquí está solo la
// maquinaria, apartada por tamaño (E-13). Porta internal/intakes/inv_vencimiento_ast_test.go
// @ 64c181a.
//
// 🚨 LA GUARDA ANTI-HUECO ES LA MITAD DEL CANDADO. Un barrido que no encuentra nada pasa
// siempre, y ese es su modo de fallo natural: se cambia la ruta, se rompe el filtro de
// extensiones, y el candado sigue verde vigilando una pared. Por eso cada barrido cuenta
// los ficheros que leyó y trae un CONTROL POSITIVO —un literal que SÍ está y que tiene que
// aparecer— antes de que su silencio signifique algo.
//
// LOS DOS BARRIDOS MIRAN COSAS DISTINTAS, Y NO ES INCONSISTENCIA:
//
//   - el del evento es de TEXTO CRUDO sobre el repo entero, comentarios incluidos. Lo que
//     se prohíbe ahí es el NOMBRE, porque el literal es la puerta: primero aparece en un
//     comentario, luego en una constante «por si acaso» y al final en un `emit`;
//   - el del TTL derogado es sobre el AST del paquete, sin comentarios. Ahí lo que se
//     prohíbe es OBEDECER la columna, y el comentario de vencimiento.go que explica por qué
//     no se reusa es justamente lo que hay que conservar.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// forbiddenEvent es el nombre del evento de telemetría que R-06 prohíbe. Se compone por
// CONCATENACIÓN y no se escribe entero en ninguna parte del repo (T-4): los dos candados
// —éste y el viejo— miran texto crudo, así que un literal suelto en cualquier fichero los
// pondría rojos. Era eventoProhibido en el viejo.
//
// Que el nombre no se pueda escribir es el punto. Detrás no hay una cadena sino una
// doctrina: los objetos de negocio no mueren por tiempo, mueren por acción humana
// (ADR-0029 Enmienda 2, D-041.16). La solicitud pasada de plazo se MARCA, y nadie emite
// que expiró.
var forbiddenEvent = "intake_" + "expired"

// positiveControlText es un literal que SÍ existe en el repo y que el mismo barrido tiene
// que encontrar. Se elige `deposit_reminded_at` porque está en varios ficheros y de VARIAS
// extensiones —.go (dominio y stores) y .sql (la migración 0045)—, así que caza tanto una
// raíz mal puesta como un filtro de extensiones roto. Era controlPositivoTexto en el viejo.
const positiveControlText = "deposit_reminded_at"

// sweptExtensions son los ficheros donde un evento podría emitirse o declararse: código,
// esquema, plantillas y contratos. Se enumeran en vez de barrer todo para no leer binarios
// ni el contenido de .git. Era extensionesBarridas en el viejo.
var sweptExtensions = map[string]bool{
	".go": true, ".sql": true, ".html": true, ".json": true, ".yml": true, ".yaml": true,
}

// repoRoot es la raíz de wapp-cloud-platform vista desde
// internal/modulos/solicitudes/intakes. Era raízDelRepo ("../..") en el viejo.
const repoRoot = "../../../.."

// lockFiles son los DOS ficheros del candado nuevo, por su ruta desde la raíz: el barrido
// de texto no los lee, para que el control positivo que ellos mismos escriben no se
// satisfaga solo. El viejo excluía por nombre base (esteCandado); aquí va la ruta entera
// porque `vencimiento_test.go` también existe en internal/intakes y ese SÍ hay que leerlo.
var lockFiles = map[string]bool{
	"internal/modulos/solicitudes/intakes/vencimiento_lock_test.go": true,
	"internal/modulos/solicitudes/intakes/vencimiento_test.go":      true,
}

// sweepText recorre `root` y devuelve dónde aparece cada uno de los dos literales
// (`fichero:línea`) y cuántos ficheros llegó a leer de verdad. Era barrerTexto en el viejo.
//
// El conteo de ficheros leídos es el anti-hueco: sin él, un filtro de extensiones roto o
// una raíz mal puesta darían cero apariciones y verde.
func sweepText(t *testing.T, root, forbidden, positive string) (whereForbidden, wherePositive []string, read int) {
	t.Helper()

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "certs":
				return filepath.SkipDir
			}
			return nil
		}
		if !sweptExtensions[filepath.Ext(d.Name())] {
			return nil
		}
		if rel, rerr := filepath.Rel(root, path); rerr == nil && lockFiles[filepath.ToSlash(rel)] {
			return nil
		}
		//nolint:gosec // G304: la ruta sale del propio WalkDir sobre el repo, no de entrada externa
		content, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		read++
		for i, line := range strings.Split(string(content), "\n") {
			if strings.Contains(line, forbidden) {
				whereForbidden = append(whereForbidden, path+":"+strconv.Itoa(i+1))
			}
			if strings.Contains(line, positive) {
				wherePositive = append(wherePositive, path+":"+strconv.Itoa(i+1))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("barriendo %s: %v. Si el repo se reorganizó, este candado se quedó vigilando un "+
			"sitio que no existe: arregla la raíz", root, err)
	}
	return whereForbidden, wherePositive, read
}

// sweepAST parsea los ficheros de PRODUCCIÓN del directorio y busca los literales en lo
// que el compilador ve —cadenas e identificadores—, NO en los comentarios: el parser se
// invoca con modo 0, que los descarta. Era barrerAST en el viejo.
//
// Se excluyen los _test.go: un test que nombre la columna derogada para comprobar que
// nadie la usa no es código que la use. Se leen los ficheros a mano porque parser.ParseDir
// está deprecado desde Go 1.22.
func sweepAST(t *testing.T, dir string, forbidden []string, positive string) (whereForbidden, wherePositive []string, read int) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("no se pudo listar %s: %v. Si el paquete se movió, este candado se queda vigilando "+
			"un sitio que no existe", dir, err)
	}

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		read++
		path := filepath.Join(dir, name)
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			t.Fatalf("no se pudo parsear %s: %v", path, perr)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			text := ""
			switch node := n.(type) {
			case *ast.BasicLit:
				text = node.Value
			case *ast.Ident:
				text = node.Name
			default:
				return true
			}
			site := path + ":" + strconv.Itoa(fset.Position(n.Pos()).Line)
			for _, p := range forbidden {
				if strings.Contains(text, p) {
					whereForbidden = append(whereForbidden, site)
				}
			}
			if strings.Contains(text, positive) {
				wherePositive = append(wherePositive, site)
			}
			return true
		})
	}
	return whereForbidden, wherePositive, read
}
