package catalogo_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// CRITERIO (d) — EL PARSEO SE QUEDA FUERA DEL CAMINO DEL ENTRANTE
// ---------------------------------------------------------------------------

// rutaDelIndice es el import path de este paquete.
const rutaDelIndice = "github.com/EduGoGroup/wapp-cloud-platform/internal/intake/catalogo"

// TestFrontera_NingunFicheroDeFlujosImportaElIndice.
//
// 🔴 POR QUÉ ESTO ES UN TEST Y NO UN COMENTARIO. El criterio (d) dice que el índice
// no puede reabrir INV-02/T1.5 «por la puerta de atrás»: el turno conversacional
// contesta al cliente sin esperar a parsear catálogos. Esa promesa no la puede
// sostener una nota en la cabecera de un fichero, porque quien la rompería es
// justamente alguien que no la ha leído — un día que `flujos/modules/cart` necesite
// una búsqueda por tag y este paquete la tenga hecha.
//
// Se comprueba sobre el AST, que es lo que ve el compilador, y no con un `grep`:
// una cadena "internal/intake/catalogo" dentro de un comentario o de un mensaje de
// error no es un import, y un grep la contaría.
//
// Alcance: `internal/flujos/**`, ficheros de producción Y de test. Los de test
// cuentan: un test del motor conversacional que importara el índice significaría
// que alguien está a un paso de cablearlo ahí.
func TestFrontera_NingunFicheroDeFlujosImportaElIndice(t *testing.T) {
	raiz := filepath.Join("..", "..", "flujos")
	fset := token.NewFileSet()

	revisados := 0
	err := filepath.WalkDir(raiz, func(ruta string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		f, err := parser.ParseFile(fset, ruta, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		revisados++
		for _, imp := range f.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			require.NoError(t, err)
			require.NotEqualf(t, rutaDelIndice, path,
				"🔴 %s importa el índice del catálogo. El índice vive en el WORKER del pipeline: "+
					"llevarlo al turno conversacional devuelve el parseo del catálogo al camino del "+
					"entrante, que es justo lo que INV-02/T1.5 prohíbe.", ruta)
		}
		return nil
	})
	require.NoError(t, err)

	// Sin esto, un `raiz` mal escrito recorrería cero ficheros y el test saldría
	// verde sin haber mirado nada.
	require.Greater(t, revisados, 100, "el barrido tiene que haber leído los ficheros de internal/flujos de verdad")
}

// TestFrontera_ElIndiceNoPuedeLeer es la otra mitad del criterio (d), dicha sobre
// los tipos: una búsqueda por ítem no tiene con qué ir a Postgres.
//
// `indiceSatisfaceFuente` (dobles_test.go) es una aserción de tipo evaluada al
// cargar el paquete de test: hoy vale false, y el día que alguien le cuelgue un
// `LeerCatalogo` al índice pasará a true y este test se pondrá rojo. Es la forma de
// que «el índice no puede leer» sea una comprobación y no una promesa.
func TestFrontera_ElIndiceNoPuedeLeer(t *testing.T) {
	require.False(t, indiceSatisfaceFuente, "el *Indice NO debe poder leer: si empieza a poder, la garantía del criterio (a) deja de ser estructural")
}
