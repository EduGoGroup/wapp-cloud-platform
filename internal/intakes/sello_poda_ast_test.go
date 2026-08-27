package intakes_test

// sello_poda_ast_test.go — EL CANDADO DEL CABLEADO DE LA PODA
// (Plan 044 · Ola 4 · Tanda 4).
//
// sello_poda_test.go prueba la DECISIÓN (sellarPodada, pura, sin BD). Esto prueba
// que la decisión SE LLAMA, que es la otra mitad y la que ningún test de conducta
// puede sostener sin Postgres: la ruta que la ejecuta es revisionsOf, y sus tests
// son de integración y se SALTAN sin WAPP_TEST_DB_DSN (`make test-integration` la
// exporta contra un postgres:16 efímero; una corrida pelada de `go test` no).
//
// 🔴 EL DEFECTO QUE VIGILA ES EXACTAMENTE EL QUE HUBO. Durante meses la línea era
// `p.ejecutarPoda(ctx, q, intakeID, poda)` a secas: la poda ocurría, sellaba la
// columna, y su instante se TIRABA. La lectura que podaba devolvía cero en
// LiteralPrunedAt y decía «esta revisión nunca tuvo texto» de la que acababa de
// destruir. Un resultado descartado no da error, no lo caza el compilador y no lo
// caza `vet`: hay que preguntarle al AST.
//
// 🚨 GUARDA ANTI-HUECO, igual que en inv_vencimiento_ast_test.go: un barrido que no
// encuentra nada pasa siempre. Si mañana la función se renombra o se muda de
// fichero, este candado vigilaría una pared en silencio — por eso exige encontrar
// las DOS llamadas antes de que su veredicto signifique algo.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

const (
	ficheroDelStore  = "postgres.go"
	funcDeLaLectura  = "revisionsOf"
	llamadaQuePoda   = "ejecutarPoda"
	llamadaQueSePubl = "sellarPodada"
)

// nombreLlamado devuelve el identificador final de una llamada: `f(...)` → "f",
// `p.f(...)` → "f". El receptor no importa aquí y mirarlo ataría el candado a que la
// variable se siga llamando `p`.
func nombreLlamado(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		return fn.Sel.Name
	}
	return ""
}

// TestPoda_ElInstanteSelladoNoSeDESCARTA es el candado: dentro de revisionsOf, el
// resultado de ejecutarPoda no puede quedar en un statement suelto.
//
// La regla es «no se descarta», no «se compone en una línea», a propósito: partirlo
// en `sellado := p.ejecutarPoda(…)` y `sellarPodada(out, n, sellado)` es un refactor
// legítimo y tiene que seguir pasando. Lo único prohibido es tirar el valor.
func TestPoda_ElInstanteSelladoNoSeDescarta(t *testing.T) {
	fset := token.NewFileSet()
	archivo, err := parser.ParseFile(fset, ficheroDelStore, nil, 0)
	if err != nil {
		t.Fatalf("parseando %s: %v", ficheroDelStore, err)
	}

	var cuerpo *ast.FuncDecl
	for _, decl := range archivo.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == funcDeLaLectura {
			cuerpo = fn
			break
		}
	}
	if cuerpo == nil {
		t.Fatalf("no se encontró %s en %s: el candado estaría vigilando una pared", funcDeLaLectura, ficheroDelStore)
	}

	var vistaPoda, vistaPublicacion bool
	var descartada token.Pos
	ast.Inspect(cuerpo, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			switch nombreLlamado(call) {
			case llamadaQuePoda:
				vistaPoda = true
			case llamadaQueSePubl:
				vistaPublicacion = true
			}
		}
		// Un ExprStmt es una llamada cuyo valor NO va a ninguna parte.
		stmt, ok := n.(*ast.ExprStmt)
		if !ok {
			return true
		}
		if call, ok := stmt.X.(*ast.CallExpr); ok && nombreLlamado(call) == llamadaQuePoda {
			descartada = stmt.Pos()
		}
		return true
	})

	// Control positivo ANTES del veredicto: sin las dos llamadas, el silencio de
	// abajo no significaría nada.
	if !vistaPoda {
		t.Fatalf("%s no llama a %s: ¿se movió la poda de sitio?", funcDeLaLectura, llamadaQuePoda)
	}
	if !vistaPublicacion {
		t.Fatalf("%s no llama a %s: el instante de la poda no llega a la respuesta, "+
			"así que la lectura que poda vuelve a decir «nunca hubo texto»", funcDeLaLectura, llamadaQueSePubl)
	}
	if descartada.IsValid() {
		t.Fatalf("%s: el resultado de %s se descarta en %s. Ese valor es el "+
			"literal_pruned_at que la respuesta tiene que publicar en ESTA lectura, "+
			"porque la columna todavía es NULL cuando el cursor la lee",
			funcDeLaLectura, llamadaQuePoda, fset.Position(descartada))
	}
}
