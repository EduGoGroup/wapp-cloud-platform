package iampostgres_test

// redeem_single_query_ast_test.go — EL CANDADO DE LA LATENCIA SIMÉTRICA DEL CANJE (Plan 047 · Ola
// A · T-A3, requisito anti-oráculo). Porta el primer test de
// internal/iam/infra/postgres/canje_una_consulta_ast_test.go @ 9a77307 (D-F2-1, R-P6). El segundo
// —las cuatro NULLables cableadas— deja de ser AST: es el test de conducta de invitationFromRow
// (R-P7), en canje_test.go.
//
// 🔴 QUÉ VIGILA, Y POR QUÉ NO PUEDE SER UN TEST DE CONDUCTA. El criterio pide que «no existe»
// (404) y «caducada» (410) cuesten lo MISMO, para que nadie pueda sondear con un cronómetro qué
// tokens existieron alguna vez. Medir eso con relojes en un test es exactamente lo que no hay
// que hacer: la diferencia que importa son microsegundos, el ruido de una máquina compartida es
// de milisegundos, y el resultado sería un test que falla al azar y que alguien acaba borrando.
//
// Lo que SÍ se puede afirmar sin relojes es la causa: la asimetría solo puede aparecer si uno de
// los dos caminos hace trabajo que el otro no hace, y en este adaptador el único trabajo caro es
// ir a la base. Así que se cuenta cuántas veces va: si la rama de la ausencia (`sql.ErrNoRows`)
// dispara una segunda consulta —para «comprobar si existió», para registrar el intento, para lo
// que sea—, este candado se pone rojo antes de que nadie tenga que cronometrar nada.
//
// 🚨 GUARDA ANTI-HUECO: un barrido que no encuentra nada pasa siempre. Por eso exige encontrar el
// fichero, la función y AL MENOS una consulta antes de que su veredicto signifique algo.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strings"
	"testing"
)

// redeemFile es el adaptador cuyo coste tiene que ser simétrico. Conserva el nombre viejo (T-15).
const redeemFile = "canje.go"

// invitationReader (era leerInvitacion) es la función que va del digest al veredicto: la única
// del canje por la que pasan LOS DOS caminos indistinguibles.
const invitationReader = "readInvitation"

// dbRoundTrips son las llamadas que cuestan un viaje a Postgres. ExecContext no está: las
// escrituras del canje ocurren DESPUÉS del veredicto, o sea solo en el camino que ya se decidió
// válido, y por tanto no pueden desequilibrar el par.
var dbRoundTrips = []string{"QueryRowContext", "QueryContext"}

// TestRedeem_ReadInvitationMakesOneQuery.
func TestRedeem_ReadInvitationMakesOneQuery(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, redeemFile, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parseando %s: %v (¿se renombró el fichero? este candado estaría vigilando una pared)", redeemFile, err)
	}

	fn := findFunc(file, invitationReader)
	if fn == nil {
		t.Fatalf("no encontré la función %s en %s: el candado no puede afirmar nada.\n"+
			"Si la renombraste, actualiza la constante; si la disolviste dentro de Redeem, este test tiene que "+
			"pasar a contar las consultas de Redeem entero — pero NO lo borres: es lo único que impide que la "+
			"rama de «no existe» acabe costando distinto que la de «caducada».", invitationReader, redeemFile)
	}

	n := countDBRoundTrips(fn)
	if n == 0 {
		t.Fatalf("%s no consulta la base ni una vez: el candado está vigilando una pared", invitationReader)
	}
	if n != 1 {
		t.Errorf("%s hace %d consultas a la base y solo puede hacer UNA.\n"+
			"Con dos, «no existe» y «caducada» dejan de costar lo mismo y quien sondee tokens con un "+
			"cronómetro podrá saber cuáles existieron. Si necesitas otro dato, tráelo en la MISMA sentencia "+
			"—así llegó now(), que además evita comparar dos relojes—.", invitationReader, n)
	}

	// La segunda mitad: que la ausencia no se desvíe a otra función que sí consulte.
	// `sql.ErrNoRows` tiene que resolverse con un `return` y nada más.
	if callsInNoRowsBranch(fn) {
		t.Errorf("%s hace una llamada en la rama de sql.ErrNoRows: esa rama es la de «no existe» y "+
			"cualquier trabajo que solo ella haga es la asimetría que este candado vigila", invitationReader)
	}
}

// findFunc devuelve la declaración de primer nivel (función o método) con ese nombre.
func findFunc(file *ast.File, name string) *ast.FuncDecl {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == name {
			return fn
		}
	}
	return nil
}

// countDBRoundTrips cuenta las invocaciones a métodos que viajan a Postgres.
func countDBRoundTrips(fn *ast.FuncDecl) int {
	n := 0
	ast.Inspect(fn, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if ok && slices.Contains(dbRoundTrips, sel.Sel.Name) {
			n++
		}
		return true
	})
	return n
}

// callsInNoRowsBranch dice si el `case` que reconoce sql.ErrNoRows hace alguna llamada además de
// reconocerlo. La rama buena es un `return` pelado con valores literales; cualquier CallExpr ahí
// dentro es trabajo que el otro camino no paga.
func callsInNoRowsBranch(fn *ast.FuncDecl) bool {
	suspicious := false
	ast.Inspect(fn, func(node ast.Node) bool {
		clause, ok := node.(*ast.CaseClause)
		if !ok {
			return true
		}
		if !mentionsErrNoRows(clause.List) {
			return true
		}
		for _, stmt := range clause.Body {
			ast.Inspect(stmt, func(inner ast.Node) bool {
				if _, isCall := inner.(*ast.CallExpr); isCall {
					suspicious = true
				}
				return !suspicious
			})
		}
		return true
	})
	return suspicious
}

// mentionsErrNoRows dice si alguna expresión de la cláusula nombra ErrNoRows.
func mentionsErrNoRows(exprs []ast.Expr) bool {
	found := false
	for _, e := range exprs {
		ast.Inspect(e, func(node ast.Node) bool {
			sel, ok := node.(*ast.SelectorExpr)
			if ok && strings.Contains(sel.Sel.Name, "ErrNoRows") {
				found = true
			}
			return !found
		})
	}
	return found
}
