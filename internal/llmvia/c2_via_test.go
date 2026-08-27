package llmvia_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// ============================================================================
// C2 COMO TEST: «SI HAY UN `if via` FUERA DE LA SELECCIÓN, ES DEFECTO» (REQ-37,
// ADR-0044 §C2, criterio literal de T1.6-3)
//
// # Por qué un test de AST y no N tests de conducta
//
// Porque lo que hay que custodiar NO es un comportamiento: es una REGLA sobre la
// forma del código, y una regla que se puede romper en un fichero que todavía no
// existe. Un test de conducta por cada sitio donde HOY no se pregunta por la vía es
// una lista que nace incompleta y que nadie amplía cuando escribe el fichero número
// N+1 — que es exactamente el sitio donde se colaría el `if via`.
//
// El fallo contra el que protege es concreto y barato de cometer: alguien, en el
// pipeline de la O2 o en el runtime, escribe «si la vía es local, hago esto otro».
// Compila, pasa el vet, pasa el lint, y a partir de ahí hay DOS pipelines. C2 existe
// porque mantener dos es lo que hunde estos plantes; el ADR lo prohíbe y esto es lo
// que lo hace comprobable.
//
// # Qué cuenta como «preguntar por la vía»
//
// Una comparación de igualdad (`==`/`!=`) o un `switch` cuyo sujeto se llame `via`
// —o termine en `Via`, como `cfg.Via`—. Es deliberadamente ANCHO: prefiere señalar
// de más y obligar a justificar la excepción en la lista de abajo a dejar pasar la
// forma que no se anticipó.
//
// # La lista de permitidos, y por qué cada uno
//
// NINGUNO de los permitidos decide CONDUCTA DE NEGOCIO por vía: son el validador del
// vocabulario, la traducción del contrato HTTP a la fila, y el store decidiendo qué
// columnas escribe. La única entrada que elige camino es la selección, que es de lo
// que trata C2.
// ============================================================================

// permitidos mapea fichero → por qué se le permite mirar la vía. Añadir una entrada
// aquí es una decisión de diseño y hay que poder defenderla: si el motivo que
// escribes es «es que lo necesito para saber qué hacer», eso es justo el defecto.
var permitidos = map[string]string{
	"internal/llmvia/llmvia.go": "LA SELECCIÓN. Es el único switch por vía del repo y la razón de ser de este paquete.",

	"internal/tenantllm/tenantllm.go": "ValidVia: el validador del vocabulario cerrado. No elige camino, dice si el valor existe.",
	"internal/degradation/degradation.go": "ValidVia otra vez, en el paquete del aviso. El vocabulario está duplicado a propósito " +
		"(ver su docstring) y los dos lados necesitan validarlo.",

	"internal/tenantllm/postgres.go": "El STORE: qué columnas escribe cada vía, y la negativa a entregar la credencial de una " +
		"fila `via='local'`. Es persistencia, no conducta: la fila de la vía local no tiene sobre que devolver.",

	"internal/publicapi/tenantllm.go": "El CONTRATO HTTP del PUT: qué campos exige cada vía (REQ-33: elegir local no exige nada) y " +
		"cómo se traduce el cuerpo a los argumentos del store. Valida la petición; no decide quién ejecuta la inferencia.",

	// 🔧 ENTRADA DE T4.6 (Plan 044 · Ola 4), y merece leerse entera antes de copiarla
	// para otro sitio.
	//
	// Es el gemelo del permitido de arriba, un endpoint más allá: el CONTRATO HTTP de
	// `/reanalyze` (design §8.1) obliga a saber la vía EFECTIVA para tres cosas, y las
	// tres son de la PETICIÓN, no de la inferencia:
	//
	//   1. resolverla (cuerpo → `tenant_llm.via` → `local` si no hay fila, D-044.48 §4);
	//   2. rechazar con 400 la que contradice la que el tenant eligió (REQ-33);
	//   3. exigir `api_llm` y credencial SOLO en la rama `api` — que es literalmente
	//      la tabla del §8.1 y el invariante de D-044.28 («`api_llm` gatea LA VÍA»).
	//
	// 🔴 LO QUE AQUÍ NO SE HACE, Y ES LO QUE C2 PROTEGE: elegir provider. Este fichero
	// no construye ningún adaptador, no llama al modelo y no toca `llm.LLMProvider`. La
	// vía que resuelve viaja al job como DATO (`intake_jobs.reanalysis_via`, 0080) y
	// acaba en `payload.analysis.provider` — telemetría, no rama.
	//
	// Y no puede DIVERGIR de la selección, que es la pregunta que de verdad importa:
	// cuando el worker tome ese job, `Selector.For` resolverá la vía por su cuenta desde
	// `tenant_llm`, con el MISMO default (`local` si no hay fila, llmvia.go:206).
	//
	// 🔴 LA COINCIDENCIA ES POR CONSTRUCCIÓN, Y LA SOSTIENE UNA SOLA REGLA: la `via` del
	// cuerpo tiene que COINCIDIR con la efectiva o la petición muere en un 400
	// `invalid_via` (D-044.51, ratificado por Jhoan). Así que lo único que puede llegar
	// a abrir un job es exactamente la vía que el selector va a resolver — sin fila,
	// `local`; con fila, la de la fila—. El endpoint AFIRMA la vía; no la conmuta y no
	// puede conmutarla.
	//
	// 🔧 ESTE PÁRRAFO DECÍA OTRA COSA Y ERA FALSA: «sin fila, una petición con
	// `via:"api"` muere en el 422 de credencial». Eso describía un DEFECTO —un
	// `hayFila &&` que desactivaba la comparación justo cuando no había fila, que es el
	// estado de los tres tenants de UAT—. Corregido el 2026-08-27: muere en el 400, y
	// antes de preguntar por ninguna feature. Se deja escrito porque el comentario que
	// justifica un permiso de este candado no puede describir una conducta que el
	// código ya no tiene.
	"internal/reanalisis/reanalisis.go": "El CONTRATO HTTP del re-análisis (§8.1): resuelve la vía EFECTIVA de la petición y " +
		"decide QUÉ EXIGE cada una —`api` pide `api_llm` y credencial, `local` no pide nada—. No construye " +
		"provider ni llama al modelo: la vía viaja al job como DATO y la selección la sigue haciendo el Selector.",
}

// TestC2_LaViaSoloSePreguntaEnLaSeleccion recorre el AST de todo internal/ y exige
// que la lista de ficheros que comparan por vía sea EXACTAMENTE la de arriba.
//
// Falla en los dos sentidos, y las dos mitades importan:
//
//   - un fichero NUEVO que pregunta por la vía ⇒ rojo, que es el defecto que C2
//     persigue;
//   - un permitido que YA NO pregunta ⇒ también rojo, para que la lista no se
//     convierta en un cementerio de excepciones que nadie se atreve a tocar.
func TestC2_LaViaSoloSePreguntaEnLaSeleccion(t *testing.T) {
	t.Parallel()

	// El test corre desde internal/llmvia; la raíz a recorrer es internal/.
	encontrados := map[string][]string{}
	err := filepath.WalkDir("..", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		fset := token.NewFileSet()
		// 🔴 UN FICHERO QUE NO PARSEA NO SE SALTA EN SILENCIO. Saltarlo dejaría un
		// agujero por el que un `if via` podría esconderse: bastaría con que el fichero
		// tuviera un error de sintaxis para que este test dijera «todo bien». Se reporta,
		// y ya decidirá quien lo lea si es su problema o el del compilador.
		f := parsear(t, fset, p)
		if f == nil {
			return nil
		}
		rel := "internal/" + strings.TrimPrefix(filepath.ToSlash(p), "../")
		ast.Inspect(f, func(n ast.Node) bool {
			if sitio := preguntaPorLaVia(n); sitio != "" {
				encontrados[rel] = append(encontrados[rel],
					sitio+" (línea "+itoa(fset.Position(n.Pos()).Line)+")")
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("recorriendo internal/: %v", err)
	}

	for fichero, sitios := range encontrados {
		if _, ok := permitidos[fichero]; !ok {
			t.Errorf("🔴 C2 ROTO: %s pregunta por la VÍA en %v.\n"+
				"La vía se pregunta en UN solo sitio (llmvia.Selector.For). Si necesitas saberla aquí, "+
				"lo que necesitas de verdad es otro método en el puerto llm.LLMProvider —o un dato "+
				"que el selector le ate al adaptador al construirlo—. Si de verdad es una excepción "+
				"legítima, añádela a `permitidos` con su motivo y que alguien lo revise.", fichero, sitios)
		}
	}
	for fichero, porQue := range permitidos {
		if _, ok := encontrados[fichero]; !ok {
			t.Errorf("%s está en la lista de permitidos y YA NO pregunta por la vía; quita la entrada.\n"+
				"Motivo que tenía: %s", fichero, porQue)
		}
	}
	if t.Failed() {
		claves := make([]string, 0, len(encontrados))
		for k := range encontrados {
			claves = append(claves, k)
		}
		sort.Strings(claves)
		t.Logf("ficheros que hoy preguntan por la vía: %v", claves)
	}
}

// parsear devuelve el AST del fichero, o nil habiendo reportado el fallo. No devuelve
// error a propósito: el llamante es un WalkDir cuyo error abortaría el recorrido
// entero, y un fichero ilegible no debe impedir revisar los demás.
func parsear(t *testing.T, fset *token.FileSet, p string) *ast.File {
	t.Helper()
	f, err := parser.ParseFile(fset, p, nil, 0)
	if err != nil {
		t.Errorf("no se pudo parsear %s (C2 no se pudo comprobar ahí): %v", p, err)
		return nil
	}
	return f
}

// preguntaPorLaVia devuelve una descripción del sitio si el nodo compara por vía, o
// cadena vacía. Reconoce las dos formas en que se escribe un «si la vía es…»: la
// comparación suelta y el switch.
func preguntaPorLaVia(n ast.Node) string {
	switch v := n.(type) {
	case *ast.BinaryExpr:
		if v.Op != token.EQL && v.Op != token.NEQ {
			return ""
		}
		if esNombreDeVia(nombreFinal(v.X)) || esNombreDeVia(nombreFinal(v.Y)) {
			return "comparación " + nombreFinal(v.X) + " " + v.Op.String() + " " + nombreFinal(v.Y)
		}
	case *ast.SwitchStmt:
		if v.Tag != nil && esNombreDeVia(nombreFinal(v.Tag)) {
			return "switch sobre " + nombreFinal(v.Tag)
		}
	}
	return ""
}

// nombreFinal saca el nombre que identifica a una expresión: `via`, `cfg.Via`,
// `tenantllm.ViaAPI` ⇒ `via`, `Via`, `ViaAPI`.
func nombreFinal(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return v.Sel.Name
	case *ast.CallExpr:
		return nombreFinal(v.Fun)
	}
	return ""
}

// esNombreDeVia reconoce `via`, `Via`, `cfg.Via`, `ViaLocal`, `ViaAPI`… Es ANCHO a
// propósito: ver la cabecera del fichero.
func esNombreDeVia(n string) bool {
	l := strings.ToLower(n)
	return l == "via" || strings.HasPrefix(l, "via") || strings.HasSuffix(l, "via")
}

// itoa evita importar strconv para una línea de mensaje.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
