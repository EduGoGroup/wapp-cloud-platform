//go:build pendiente

package cart_test

// orden_consulta_ast_test.go — EL ORDEN DENTRO DE Module.Step ES UN INVARIANTE, y se
// vigila sobre el AST (Plan 044 · Ola 3.5 · T3.5-2; trampa T-11 de la fase F8).
//
// Porta internal/flujos/modules/cart/orden_consulta_ast_test.go @ 9d5a4b6. Es uno de
// los DOS candados de invariante que pueden leer el código como texto (05 §3.2,
// plan/F8-conversacion/diseno.md §4.2); ningún otro test del paquete lo hace.
//
// RE-ANCLADO (E-13): el viejo abría `cart.go` a pelo. Aquí cart.go nace partido por
// tema y nada garantiza que Step se quede donde está, así que el candado busca el
// método Step de Module en TODOS los ficheros de producción del paquete y vigila el
// cuerpo que encuentre, esté donde esté. Si no encuentra UNO —y con cuerpo de
// verdad—, FALLA: un test estructural que no ve nada pasa siempre.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// Los nombres que el candado busca dentro de Step. Son no exportados y nacen con el
// verde; se fijan aquí para que el verde no pueda llamarlos de otra manera sin
// tocar el candado.
const (
	stepReceiver   = "Module"
	queryCallName  = "preresolveOrQuery" // `preresolveOConsulta` en el viejo (E-11)
	queryFieldName = "Query"             // modules.Result.Query (`Consulta` en el viejo)
	startedField   = "Started"
	advanceCall    = "advance"
)

// TestOrder_QueryIsRaisedBeforeAnyMutation comprueba que, dentro de Module.Step, la
// traducción de la entrada (m.preresolveOrQuery) y el return de la PETICIÓN preceden a
// las dos únicas mutaciones del método: `st.Started = true` —que declara cart_started
// EXACTAMENTE una vez— y la llamada a advance(), que es quien mueve el contador de
// inválidos.
//
// 🔴 QUÉ MUTACIÓN LO PONE ROJO, Y POR QUÉ NINGÚN TEST DE CONDUCTA BARATO LA CAZA.
// Mover el bloque `input, query := m.preresolveOrQuery(...)` + `if query != nil {
// return … }` por DEBAJO de `if !st.Started { st.Started = true; … }` rompe el
// mecanismo de la peor manera posible: la primera pasada declararía cart_started y el
// engine descartaría ese Result, la segunda pasada volvería a declararlo... y esa sí se
// despacha. Un efecto duplicado por cada mensaje que necesite consulta. Con el contador
// de inválidos pasa lo mismo: dos inválidos contados por UN mensaje del cliente, y el
// menú de salida armado a destiempo. El observable de las dos versiones es idéntico en
// todos los caminos que NO consultan (el 99 %); es la misma clase de defecto que llevó
// al invariante estructural de las rachas: la pregunta que de verdad falla en la
// práctica no es «¿se comporta bien este camino?» sino «¿alguien ha movido esta línea?».
//
// Lo que este test NO prueba: que la traducción sea correcta, ni que el módulo pregunte
// cuándo debe. Eso es consulta_test.go. Este solo vigila el ORDEN.
func TestOrder_QueryIsRaisedBeforeAnyMutation(t *testing.T) {
	fset := token.NewFileSet()
	files := parsePackageSources(t, fset)
	file, body, problem := findStep(fset, files)
	if problem != "" {
		t.Fatalf("%s. Sin el cuerpo de Step el candado no está mirando nada.", problem)
	}
	for _, violation := range checkOrder(piecesOf(fset, body)) {
		t.Errorf("%s: %s", file, violation)
	}
}

// TestOrderLock_SeesStepAndJudgesItsOrder prueba el propio candado contra cuerpos de
// Step sintéticos: que VE Step cuando tiene cuerpo —esté en el fichero que esté—, que
// distingue el orden bueno de las dos mutaciones que vigila, y que no pasa en verde
// cuando no ve lo que tiene que ver.
func TestOrderLock_SeesStepAndJudgesItsOrder(t *testing.T) {
	const (
		header  = "package cart\n"
		ask     = "\tinput, query := m.preresolveOrQuery(cat, st, vars, input)\n\tif query != nil {\n\t\treturn modules.Result{Vars: vars, Query: query}\n\t}\n"
		start   = "\tif !st.Started {\n\t\tst.Started = true\n\t}\n"
		move    = "\tnewSt, outs, effs := advance(cat, st, input, size, fields)\n"
		tail    = "\treturn modules.Result{Vars: vars, Outputs: outs, Effects: effs}\n}\n"
		open    = "func (m Module) Step(_ model.Node, conv model.Conversation, input string) modules.Result {\n"
		pointer = "func (m *Module) Step(_ model.Node, conv model.Conversation, input string) modules.Result {\n"
	)
	cases := []struct {
		name    string
		sources map[string]string
		problem string // fragmento del motivo por el que no hay cuerpo que vigilar
		want    []string
	}{
		{name: "right order", sources: map[string]string{"cart.go": header + open + ask + start + move + tail}},
		{name: "right order in another file", sources: map[string]string{
			"cart.go": header + "type Module struct{}\n", "cart_step.go": header + open + ask + start + move + tail}},
		{name: "pointer receiver", sources: map[string]string{"cart.go": header + pointer + ask + start + move + tail}},
		{name: "started before the query", sources: map[string]string{"cart.go": header + open + start + ask + move + tail},
			want: []string{"muta st.Started ANTES"}},
		{name: "advance before the query", sources: map[string]string{"cart.go": header + open + move + ask + start + tail},
			want: []string{"llama a advance() ANTES"}},
		{name: "everything before the query", sources: map[string]string{"cart.go": header + open + start + move + ask + tail},
			want: []string{"muta st.Started ANTES", "llama a advance() ANTES"}},
		{name: "no query at all", sources: map[string]string{"cart.go": header + open + start + move + tail},
			want: []string{"0 apariciones de la llamada a m.preresolveOrQuery", "0 apariciones de el return con la PETICIÓN"}},
		{name: "query never returned", sources: map[string]string{
			"cart.go": header + open + "\tinput, query := m.preresolveOrQuery(cat, st, vars, input)\n\t_ = query\n" + start + move + tail},
			want: []string{"0 apariciones de el return con la PETICIÓN"}},
		{name: "started twice", sources: map[string]string{"cart.go": header + open + ask + start + start + move + tail},
			want: []string{"2 apariciones de la asignación st.Started = true"}},
		{name: "no advance", sources: map[string]string{"cart.go": header + open + ask + start + "\tvar outs []string\n\tvar effs []modules.Effect\n" + tail},
			want: []string{"0 apariciones de la llamada a advance("}},
		{name: "step still in contract", problem: "sigue en contrato", sources: map[string]string{
			"cart.go": header + open + "\tpanic(pendiente.Implementar(\"cart.Module.Step\"))\n}\n"}},
		{name: "no step anywhere", problem: "no se encontró", sources: map[string]string{"cart.go": header + "type Module struct{}\n"}},
		{name: "step of another type does not count", problem: "no se encontró", sources: map[string]string{
			"cart.go": header + strings.Replace(open, "(m Module)", "(p *Projector)", 1) + ask + start + move + tail}},
		{name: "plain function does not count", problem: "no se encontró", sources: map[string]string{
			"cart.go": header + strings.Replace(open, "(m Module) ", "", 1) + ask + start + move + tail}},
		{name: "two steps", problem: "más de un", sources: map[string]string{
			"cart.go": header + open + ask + start + move + tail, "otro.go": header + open + ask + start + move + tail}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fset := token.NewFileSet()
			files := map[string]*ast.File{}
			for name, src := range tc.sources {
				f, err := parser.ParseFile(fset, name, src, 0)
				if err != nil {
					t.Fatalf("fuente de prueba %s ilegible: %v", name, err)
				}
				files[name] = f
			}
			_, body, problem := findStep(fset, files)
			if tc.problem != "" {
				if !strings.Contains(problem, tc.problem) {
					t.Fatalf("motivo = %q, quiero uno que diga %q", problem, tc.problem)
				}
				return
			}
			if problem != "" {
				t.Fatalf("el candado no vio Step: %s", problem)
			}
			got := checkOrder(piecesOf(fset, body))
			if len(got) != len(tc.want) {
				t.Fatalf("violaciones = %q, quiero %d: %q", got, len(tc.want), tc.want)
			}
			for i, want := range tc.want {
				if !strings.Contains(got[i], want) {
					t.Errorf("violación %d = %q, quiero una que diga %q", i, got[i], want)
				}
			}
		})
	}
}

// parsePackageSources parsea los ficheros de PRODUCCIÓN del paquete (el directorio
// del test), sin los _test.go.
func parsePackageSources(t *testing.T, fset *token.FileSet) map[string]*ast.File {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("no se pudo listar el paquete: %v", err)
	}
	files := map[string]*ast.File{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("no se pudo parsear %s: %v", name, err)
		}
		files[name] = f
	}
	if len(files) == 0 {
		t.Fatal("el paquete no tiene ficheros de producción: el candado no está mirando nada")
	}
	return files
}

// findStep localiza el método Step de Module entre los ficheros dados y devuelve el
// fichero donde vive y su cuerpo. `problem` no vacío ⇒ no hay UN cuerpo que vigilar, y
// dice por qué: no existe, hay más de uno, o sigue en contrato (su cuerpo es solo el
// panic de pendiente.Implementar).
func findStep(fset *token.FileSet, files map[string]*ast.File) (file string, body *ast.BlockStmt, problem string) {
	found := 0
	for name, f := range files {
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "Step" || fn.Body == nil || !isMethodOf(fn, stepReceiver) {
				continue
			}
			found++
			file, body = name, fn.Body
		}
	}
	switch {
	case found == 0:
		return "", nil, "no se encontró el método Step de " + stepReceiver + " con cuerpo en ningún fichero del paquete"
	case found > 1:
		return "", nil, "hay más de un método Step de " + stepReceiver + " en el paquete"
	case isContractBody(body):
		pos := fset.Position(body.Pos())
		return "", nil, stepReceiver + ".Step sigue en contrato (" + pos.Filename + ": su cuerpo es el panic de pendiente.Implementar)"
	}
	return file, body, ""
}

// isMethodOf dice si fn es un método cuyo receptor es `typeName` o `*typeName`.
func isMethodOf(fn *ast.FuncDecl, typeName string) bool {
	if fn.Recv == nil || len(fn.Recv.List) != 1 {
		return false
	}
	expr := fn.Recv.List[0].Type
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	id, ok := expr.(*ast.Ident)
	return ok && id.Name == typeName
}

// isContractBody reconoce el cuerpo de un contrato sin lógica: una sola sentencia,
// `panic(pendiente.Implementar(…))`.
func isContractBody(body *ast.BlockStmt) bool {
	if len(body.List) != 1 {
		return false
	}
	stmt, ok := body.List[0].(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := stmt.X.(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return false
	}
	if fn, ok := call.Fun.(*ast.Ident); !ok || fn.Name != "panic" {
		return false
	}
	inner, ok := call.Args[0].(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := inner.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Implementar" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "pendiente"
}

// pieces guarda el OFFSET en el fichero de cada una de las cuatro cosas que este
// invariante ordena. Slices y no un int por pieza: encontrar DOS de algo es tan
// significativo como no encontrar ninguna.
type pieces struct{ query, request, started, advance []int }

// piecesOf recorre el cuerpo de Step y localiza las cuatro piezas.
func piecesOf(fset *token.FileSet, body *ast.BlockStmt) pieces {
	var p pieces
	offset := func(n ast.Node) int { return fset.Position(n.Pos()).Offset }
	ast.Inspect(body, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.CallExpr:
			switch fn := v.Fun.(type) {
			case *ast.SelectorExpr:
				if fn.Sel.Name == queryCallName {
					p.query = append(p.query, offset(n))
				}
			case *ast.Ident:
				if fn.Name == advanceCall {
					p.advance = append(p.advance, offset(n))
				}
			}
		case *ast.ReturnStmt:
			if returnsQuery(v) {
				p.request = append(p.request, offset(n))
			}
		case *ast.AssignStmt:
			if assignsStartedTrue(v) {
				p.started = append(p.started, offset(n))
			}
		}
		return true
	})
	return p
}

// checkOrder devuelve las violaciones del invariante, en un orden fijo. Primero exige
// EXACTAMENTE una de cada pieza —si Step se reorganiza y el candado deja de VER alguna
// se volvería verde sin comprobar nada, el modo de fallo clásico de un test
// estructural—, y solo con las cuatro a la vista juzga el orden.
func checkOrder(p pieces) []string {
	var out []string
	exactlyOne := func(name string, pos []int) {
		if len(pos) != 1 {
			out = append(out, "se encontraron "+strconv.Itoa(len(pos))+" apariciones de "+name+" dentro de Module.Step y se esperaba 1. "+
				"Si has reorganizado el método, arregla este parseo ANTES de fiarte del verde")
		}
	}
	exactlyOne("la llamada a m."+queryCallName, p.query)
	exactlyOne("el return con la PETICIÓN de consulta (modules.Result{… "+queryFieldName+": …})", p.request)
	exactlyOne("la asignación st."+startedField+" = true", p.started)
	exactlyOne("la llamada a "+advanceCall+"(", p.advance)
	if len(out) > 0 {
		return out
	}
	if p.query[0] > p.request[0] {
		out = append(out, "el return de la petición está ANTES de la llamada que la produce")
	}
	if p.request[0] > p.started[0] {
		out = append(out, "🔴 el carrito muta st.Started ANTES de poder devolver la petición de consulta. "+
			"La primera pasada declararía cart_started, el engine la descartaría y la segunda volvería a "+
			"declararlo: efecto DUPLICADO por cada mensaje que necesite consulta. Devuelve la petición antes de tocar nada")
	}
	if p.request[0] > p.advance[0] {
		out = append(out, "🔴 el carrito llama a advance() ANTES de poder devolver la petición de consulta. "+
			"advance mueve el contador de inválidos: con las dos pasadas se contarían DOS inválidos por UN "+
			"mensaje del cliente y el menú de salida se armaría a destiempo")
	}
	return out
}

// returnsQuery reconoce el `return modules.Result{… Query: …}` de la petición. Se
// identifica por la CLAVE del literal y no por su posición: da igual cómo se llame la
// variable o en qué orden estén los campos.
func returnsQuery(r *ast.ReturnStmt) bool {
	for _, res := range r.Results {
		lit, ok := res.(*ast.CompositeLit)
		if !ok {
			continue
		}
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			if id, ok := kv.Key.(*ast.Ident); ok && id.Name == queryFieldName {
				return true
			}
		}
	}
	return false
}

// assignsStartedTrue reconoce `st.Started = true` (cualquier receptor: lo que importa
// es el campo).
func assignsStartedTrue(a *ast.AssignStmt) bool {
	if len(a.Lhs) != 1 || len(a.Rhs) != 1 {
		return false
	}
	sel, ok := a.Lhs[0].(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != startedField {
		return false
	}
	id, ok := a.Rhs[0].(*ast.Ident)
	return ok && id.Name == "true"
}
