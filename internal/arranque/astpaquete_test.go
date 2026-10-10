// Copia de internal/bootstrap/arranque/astpaquete_test.go @ 80807ba (F0 · 05 §6). Desde F8
// (conmutar(conversacion)) el arranque que este test ejercita no cablea ningún paquete viejo.
package arranque

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
	"testing"
)

// astDelArranque parsea EL PAQUETE ENTERO y devuelve su árbol de sintaxis.
//
// # POR QUÉ EL PAQUETE Y NO UN FICHERO
//
// Los tests de cableado de este directorio custodian invariantes de PRODUCCIÓN —que
// `gw.OnEdgeReady` esté enchufado, que el worker del pipeline se arranque UNA sola
// vez, que toda ruta de plataforma exija un permiso `.any`—. Esos invariantes son del
// ARRANQUE, no del fichero donde hoy cae la línea.
//
// 🔴 HASTA EL 2026-09-04 ESTOS TESTS DECÍAN `parser.ParseFile(fset, "bootstrap.go")`, y
// eso los ataba a un nombre de fichero: partir el arranque en fases los tumbó a los
// nueve de golpe, sin que ni un solo invariante hubiera cambiado. Falló ruidosamente,
// que fue una suerte — pero el modo de fallo que de verdad hay que temer es el otro: si
// alguien hubiera movido la línea vigilada a otro fichero DEJANDO `bootstrap.go` en su
// sitio, el test habría seguido en VERDE mirando un fichero donde ya no está lo que
// busca. Un test de cableado que se queda ciego no avisa de nada.
//
// Recorrer el paquete cierra las dos puertas: el invariante se sigue exigiendo aunque
// la línea cambie de fichero, y la próxima división no cuesta nueve rojos.
//
// Se excluyen los `_test.go` a propósito: lo que se acredita es lo que corre en
// producción. Un cable escrito en un fichero de test no cablea nada.
func astDelArranque(t *testing.T) (*token.FileSet, []*ast.File) {
	t.Helper()

	entradas, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("leyendo el directorio del arranque: %v", err)
	}
	// Orden estable por nombre de fichero. os.ReadDir ya ordena, pero se deja dicho:
	// un test que cuenta apariciones y reporta la posición de la primera daría un
	// mensaje distinto en cada corrida si el orden fuera el de un mapa de Go.
	nombres := make([]string, 0, len(entradas))
	for _, e := range entradas {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		nombres = append(nombres, n)
	}
	sort.Strings(nombres)
	if len(nombres) == 0 {
		t.Fatal("el paquete del arranque no tiene ni un fichero de producción")
	}

	fset := token.NewFileSet()
	ficheros := make([]*ast.File, 0, len(nombres))
	for _, n := range nombres {
		f, err := parser.ParseFile(fset, n, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parseando %s: %v", n, err)
		}
		ficheros = append(ficheros, f)
	}
	return fset, ficheros
}

// sinContenedor pela el receptor del contenedor del arranque: `c.gw.OnWarmup` se lee
// como `gw.OnWarmup`.
//
// Existe porque desde que el arranque se construye por fases, los objetos ya no son
// variables locales de una función de 990 líneas sino campos de un contenedor
// compartido. El CABLE es el mismo —el mismo hook, el mismo objeto, la misma línea— y
// por eso los criterios de todos estos tests siguen escritos como se escribieron: si
// esta función no existiera, habría que reescribir nueve invariantes para decir
// exactamente lo mismo con un prefijo delante, y una reescritura así es justo la
// ocasión en la que uno cambia sin querer lo que afirma un test.
//
// ⚠️ Solo pela el receptor `c` inicial. `otraCosa.gw.OnWarmup` NO se convierte en
// `gw.OnWarmup`: seguiría sin reconocerse, que es lo correcto — no es el cable que se
// exige.
func sinContenedor(ruta string) string {
	return strings.TrimPrefix(ruta, "c.")
}

// inspecciona recorre el AST de TODOS los ficheros de producción del paquete con la
// misma semántica que ast.Inspect: devolver false corta el descenso por esa rama.
//
// Sustituye al `ast.Inspect(archivo, …)` de un solo fichero que usaban estos tests, y
// la sustitución es de UNA línea a propósito: el cuerpo del visitante —o sea, lo que
// cada test AFIRMA— se queda exactamente como estaba.
func inspecciona(ficheros []*ast.File, visita func(ast.Node) bool) {
	for _, fichero := range ficheros {
		ast.Inspect(fichero, visita)
	}
}
