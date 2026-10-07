package llmvia_test

// Porta internal/llmvia/c2_via_test.go @ ebf4eb7 (candado de invariante I-CP-3, D-F4-2).

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// ============================================================================
// C2 COMO TEST: «SI HAY UN `if via` FUERA DE LA SELECCIÓN, ES DEFECTO» (REQ-37,
// ADR-0044 §C2)
//
// # Por qué un test de AST y no N tests de conducta
//
// Porque lo que hay que custodiar NO es un comportamiento: es una REGLA sobre la forma del
// código, y una regla que se puede romper en un fichero que todavía no existe. Un test de
// conducta por cada sitio donde HOY no se pregunta por la vía es una lista que nace
// incompleta y que nadie amplía cuando escribe el fichero número N+1 — que es exactamente
// el sitio donde se colaría el `if via`.
//
// El fallo contra el que protege es concreto y barato de cometer: alguien, en el pipeline o
// en el runtime, escribe «si la vía es local, hago esto otro». Compila, pasa el vet, pasa
// el lint, y a partir de ahí hay DOS pipelines. C2 existe porque mantener dos es lo que
// hunde estos planes; el ADR lo prohíbe y esto es lo que lo hace comprobable.
//
// # Qué cuenta como «preguntar por la vía»
//
// Una comparación de igualdad (`==`/`!=`) o un `switch` cuyo sujeto se llame `via` —o
// empiece o termine por `via`, como `cfg.Via` o `tenantllm.ViaAPI`—. Es deliberadamente
// ANCHO (T-2: `envia` y `todavia` también cuentan): prefiere señalar de más y obligar a
// justificar la excepción en la lista de abajo a dejar pasar la forma que no se anticipó.
// 🔴 Prohibido esquivarlo renombrando la variable.
//
// # Qué árbol barre
//
// El de la reconstrucción modular: internal/{modulos,nucleo,arranque,apipublica}. El árbol
// viejo lo sigue vigilando el candado viejo (internal/llmvia/c2_via_test.go), que excluye
// este (D-F4-1). Desde F10, cuando el viejo muera, este barre todo internal/.
//
// # La lista de permitidos, y por qué cada uno
//
// NINGUNO de los permitidos decide CONDUCTA DE NEGOCIO por vía salvo la selección, que es
// de lo que trata C2: los demás son el validador del vocabulario y el almacén decidiendo
// qué columnas escribe. Son SOLO los del módulo inferencia; las dos entradas de fuera
// (apipublica/tenantllm.go y modulos/captacion/reanalisis/reanalisis.go) las añade quien
// introduzca esa comparación, en su commit `verde` (TX.13 y F7).
// ============================================================================

// scanRoots son los directorios de primer nivel de internal/ que se barren.
var scanRoots = []string{"modulos", "nucleo", "arranque", "apipublica"}

// internalDir es internal/ visto desde este paquete (internal/modulos/inferencia/llmvia).
const internalDir = "../../.."

// allowed mapea fichero → por qué se le permite mirar la vía. Añadir una entrada aquí es
// una decisión de diseño y hay que poder defenderla: si el motivo que escribes es «es que
// lo necesito para saber qué hacer», eso es justo el defecto.
var allowed = map[string]string{
	"internal/modulos/inferencia/llmvia/llmvia.go": "LA SELECCIÓN. El switch por vía de For y sus hermanos (Warm, PlazaDe y la " +
		"pregunta que Turno le hace): es la razón de ser de este paquete. llmvia_turno.go y notify.go NO están aquí a propósito.",

	"internal/modulos/inferencia/tenantllm/tenantllm.go": "ValidVia: el validador del vocabulario cerrado. No elige camino, dice si el valor existe.",

	"internal/modulos/inferencia/degradation/degradation.go": "ValidVia otra vez, en el paquete del aviso. El vocabulario está duplicado " +
		"a propósito (ver su comentario) y los dos lados necesitan validarlo.",

	"internal/modulos/inferencia/tenantllm/postgres.go": "El ALMACÉN: qué columnas escribe cada vía, y la negativa a entregar la credencial " +
		"de una fila `via='local'`. Es persistencia, no conducta: la fila de la vía local no tiene sobre que devolver.",

	"internal/modulos/inferencia/tenantllm/tenantllmhelpertest/memoria.go": "El DOBLE del almacén: aplica la misma regla de persistencia que " +
		"el adaptador Postgres (las guardas de Upsert y la negativa de APIKey), o los tests que se apoyan en él verían otra cosa.",
}

// TestC2_TheRouteIsOnlyAskedInTheSelection recorre el AST del árbol nuevo y exige que la
// lista de ficheros que comparan por vía sea EXACTAMENTE la de arriba.
//
// Falla en los dos sentidos, y las dos mitades importan:
//
//   - un fichero NUEVO que pregunta por la vía ⇒ rojo, que es el defecto que C2 persigue;
//   - un permitido que YA NO pregunta ⇒ también rojo, para que la lista no se convierta en
//     un cementerio de excepciones que nadie se atreve a tocar.
//
// Y afirma que recorrió algo: un candado que no encuentra su árbol pasaría en verde sin
// haber mirado nada.
func TestC2_TheRouteIsOnlyAskedInTheSelection(t *testing.T) {
	t.Parallel()

	found := map[string][]string{}
	scanned := 0
	for _, root := range scanRoots {
		n := scanTree(t, root, found)
		if n == 0 {
			t.Errorf("internal/%s: recorridos = 0; el candado no encontró código que mirar ahí", root)
		}
		scanned += n
	}
	if scanned == 0 {
		t.Fatal("recorridos = 0: el candado no miró ningún fichero")
	}
	t.Logf("recorridos=%d", scanned)

	for file, sites := range found {
		if _, ok := allowed[file]; !ok {
			t.Errorf("🔴 C2 ROTO: %s pregunta por la VÍA en %v.\n"+
				"La vía se pregunta en UN solo sitio (llmvia.Selector, en llmvia.go). Si necesitas saberla aquí, "+
				"lo que necesitas de verdad es otro método en el puerto —o un dato que el selector le ate al "+
				"adaptador al construirlo—. Si de verdad es una excepción legítima, añádela a `allowed` con su "+
				"motivo y que alguien lo revise.", file, sites)
		}
	}
	for file, why := range allowed {
		if _, ok := found[file]; !ok {
			t.Errorf("%s está en la lista de permitidos y NO pregunta por la vía; quita la entrada.\n"+
				"Motivo que tenía: %s", file, why)
		}
	}
	if t.Failed() {
		files := make([]string, 0, len(found))
		for file := range found {
			files = append(files, file)
		}
		slices.Sort(files)
		t.Logf("ficheros que hoy preguntan por la vía: %v", files)
	}
}

// scanTree recorre internal/<root>, apunta en found cada sitio que compara por vía y
// devuelve cuántos ficheros de producción miró.
func scanTree(t *testing.T, root string, found map[string][]string) int {
	t.Helper()
	scanned := 0
	err := filepath.WalkDir(filepath.Join(internalDir, root), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" {
				return filepath.SkipDir // como la toolchain: no es código del árbol
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		// 🔴 UN FICHERO QUE NO PARSEA NO SE SALTA EN SILENCIO. Saltarlo dejaría un
		// agujero por el que un `if via` podría esconderse: bastaría un error de
		// sintaxis para que este test dijera «todo bien».
		f := parseFile(t, fset, p)
		if f == nil {
			return nil
		}
		scanned++
		rel := "internal/" + strings.TrimPrefix(filepath.ToSlash(p), internalDir+"/")
		ast.Inspect(f, func(n ast.Node) bool {
			if site := asksForTheRoute(n); site != "" {
				found[rel] = append(found[rel], site+" (línea "+strconv.Itoa(fset.Position(n.Pos()).Line)+")")
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("recorriendo internal/%s: %v", root, err)
	}
	return scanned
}

// parseFile devuelve el AST del fichero, o nil habiendo reportado el fallo. No devuelve
// error a propósito: el llamante es un WalkDir cuyo error abortaría el recorrido entero, y
// un fichero ilegible no debe impedir revisar los demás.
func parseFile(t *testing.T, fset *token.FileSet, p string) *ast.File {
	t.Helper()
	f, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Errorf("no se pudo parsear %s (C2 no se pudo comprobar ahí): %v", p, err)
		return nil
	}
	return f
}

// asksForTheRoute devuelve una descripción del sitio si el nodo compara por vía, o cadena
// vacía. Reconoce las dos formas en que se escribe un «si la vía es…»: la comparación
// suelta y el switch.
func asksForTheRoute(n ast.Node) string {
	switch v := n.(type) {
	case *ast.BinaryExpr:
		if v.Op != token.EQL && v.Op != token.NEQ {
			return ""
		}
		if isRouteName(finalName(v.X)) || isRouteName(finalName(v.Y)) {
			return "comparación " + finalName(v.X) + " " + v.Op.String() + " " + finalName(v.Y)
		}
	case *ast.SwitchStmt:
		if v.Tag != nil && isRouteName(finalName(v.Tag)) {
			return "switch sobre " + finalName(v.Tag)
		}
	}
	return ""
}

// finalName saca el nombre que identifica a una expresión: `via`, `cfg.Via`,
// `tenantllm.ViaAPI`, `leer().Via` ⇒ `via`, `Via`, `ViaAPI`, `Via`.
func finalName(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return v.Sel.Name
	case *ast.CallExpr:
		return finalName(v.Fun)
	}
	return ""
}

// isRouteName reconoce `via`, `Via`, `cfg.Via`, `ViaLocal`, `ViaAPI`… Es ANCHO a propósito:
// ver la cabecera del fichero.
func isRouteName(name string) bool {
	lower := strings.ToLower(name)
	return lower == "via" || strings.HasPrefix(lower, "via") || strings.HasSuffix(lower, "via")
}
