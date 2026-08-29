package iampostgres_test

// canje_una_consulta_ast_test.go — EL CANDADO DE LA LATENCIA SIMÉTRICA DEL CANJE
// (Plan 047 · Ola A · T-A3, requisito anti-oráculo).
//
// 🔴 QUÉ VIGILA, Y POR QUÉ NO PUEDE SER UN TEST DE CONDUCTA. El criterio pide
// que «no existe» (404) y «caducada» (410) cuesten lo MISMO, para que nadie
// pueda sondear con un cronómetro qué tokens existieron alguna vez. Medir eso
// con relojes en un test es exactamente lo que no hay que hacer: la diferencia
// que importa son microsegundos, el ruido de una máquina compartida es de
// milisegundos, y el resultado sería un test que falla al azar y que alguien
// acaba borrando.
//
// Lo que SÍ se puede afirmar sin relojes es la causa: la asimetría solo puede
// aparecer si uno de los dos caminos hace trabajo que el otro no hace, y en este
// adaptador el único trabajo caro es ir a la base. Así que se cuenta cuántas
// veces va: si la rama de la ausencia (`sql.ErrNoRows`) dispara una segunda
// consulta —para «comprobar si existió», para registrar el intento, para lo que
// sea—, este candado se pone rojo antes de que nadie tenga que cronometrar nada.
//
// 🚨 GUARDA ANTI-HUECO: un barrido que no encuentra nada pasa siempre. Por eso
// exige encontrar el fichero, la función y AL MENOS una consulta antes de que su
// veredicto signifique algo.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strings"
	"testing"
)

// ficheroDelCanje es el adaptador cuyo coste tiene que ser simétrico.
const ficheroDelCanje = "canje.go"

// lecturaDelCanje es la función que va del digest al veredicto: la única del
// canje por la que pasan LOS DOS caminos indistinguibles.
const lecturaDelCanje = "leerInvitacion"

// metodosQueVanALaBase son las llamadas que cuestan un viaje a Postgres.
// ExecContext no está: las escrituras del canje ocurren DESPUÉS del veredicto,
// o sea solo en el camino que ya se decidió válido, y por tanto no pueden
// desequilibrar el par.
var metodosQueVanALaBase = []string{"QueryRowContext", "QueryContext"}

// TestCanje_LaLecturaHaceUnaSolaConsulta.
func TestCanje_LaLecturaHaceUnaSolaConsulta(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	archivo, err := parser.ParseFile(fset, ficheroDelCanje, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parseando %s: %v (¿se renombró el fichero? este candado estaría vigilando una pared)", ficheroDelCanje, err)
	}

	fn := buscarFuncion(archivo, lecturaDelCanje)
	if fn == nil {
		t.Fatalf("no encontré la función %s en %s: el candado no puede afirmar nada.\n"+
			"Si la renombraste, actualiza la constante; si la disolviste dentro de Redeem, este test tiene que "+
			"pasar a contar las consultas de Redeem entero — pero NO lo borres: es lo único que impide que la "+
			"rama de «no existe» acabe costando distinto que la de «caducada».", lecturaDelCanje, ficheroDelCanje)
	}

	n := contarLlamadasABase(fn)
	if n == 0 {
		t.Fatalf("%s no consulta la base ni una vez: el candado está vigilando una pared", lecturaDelCanje)
	}
	if n != 1 {
		t.Errorf("%s hace %d consultas a la base y solo puede hacer UNA.\n"+
			"Con dos, «no existe» y «caducada» dejan de costar lo mismo y quien sondee tokens con un "+
			"cronómetro podrá saber cuáles existieron. Si necesitas otro dato, tráelo en la MISMA sentencia "+
			"—así llegó now(), que además evita comparar dos relojes—.", lecturaDelCanje, n)
	}

	// La segunda mitad: que la ausencia no se desvíe a otra función que sí
	// consulte. `sql.ErrNoRows` tiene que resolverse con un `return` y nada más.
	if llamaAAlgunaFuncionTrasErrNoRows(fn) {
		t.Errorf("%s hace una llamada en la rama de sql.ErrNoRows: esa rama es la de «no existe» y "+
			"cualquier trabajo que solo ella haga es la asimetría que este candado vigila", lecturaDelCanje)
	}
}

// buscarFuncion devuelve la declaración de la función con ese nombre.
func buscarFuncion(archivo *ast.File, nombre string) *ast.FuncDecl {
	for _, decl := range archivo.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == nombre {
			return fn
		}
	}
	return nil
}

// contarLlamadasABase cuenta las invocaciones a métodos que viajan a Postgres.
func contarLlamadasABase(fn *ast.FuncDecl) int {
	n := 0
	ast.Inspect(fn, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if ok && slices.Contains(metodosQueVanALaBase, sel.Sel.Name) {
			n++
		}
		return true
	})
	return n
}

// llamaAAlgunaFuncionTrasErrNoRows dice si el `case` que reconoce sql.ErrNoRows
// hace alguna llamada además de reconocerlo. La rama buena es un `return` pelado
// con valores literales; cualquier CallExpr ahí dentro es trabajo que el otro
// camino no paga.
func llamaAAlgunaFuncionTrasErrNoRows(fn *ast.FuncDecl) bool {
	sospechosa := false
	ast.Inspect(fn, func(node ast.Node) bool {
		clausula, ok := node.(*ast.CaseClause)
		if !ok {
			return true
		}
		if !mencionaErrNoRows(clausula.List) {
			return true
		}
		for _, sentencia := range clausula.Body {
			ast.Inspect(sentencia, func(interior ast.Node) bool {
				if _, esLlamada := interior.(*ast.CallExpr); esLlamada {
					sospechosa = true
				}
				return !sospechosa
			})
		}
		return true
	})
	return sospechosa
}

// mencionaErrNoRows dice si alguna expresión de la cláusula nombra ErrNoRows.
func mencionaErrNoRows(exprs []ast.Expr) bool {
	encontrado := false
	for _, e := range exprs {
		ast.Inspect(e, func(node ast.Node) bool {
			sel, ok := node.(*ast.SelectorExpr)
			if ok && strings.Contains(sel.Sel.Name, "ErrNoRows") {
				encontrado = true
			}
			return !encontrado
		})
	}
	return encontrado
}

// camposNULLablesDeLaFila son las cuatro columnas NULLables de
// tenant_invitations que leerInvitacion tiene que trasladar a la entidad.
//
// 🔴 POR QUÉ ESTE CANDADO EXISTE, Y ES UN HUECO MEDIDO, NO IMAGINADO. El canje
// rechaza una invitación revocada por DOS guardas independientes: la
// clasificación (domain.EvaluarCanje, que mira inv.RevokedAt) y el
// `revoked_at IS NULL` del UPDATE. Se midió cada una por separado contra
// Postgres real (2026-08-28) y salió esto:
//
//   - romper la FUNCIÓN de dominio          ⇒ ROJO (TestEvaluarCanje_...)
//   - quitar la condición del UPDATE        ⇒ VERDE (la tapa la clasificación)
//   - NO CABLEAR revoked_at aquí, en el scan ⇒ 🔴 VERDE, y ese es el hueco
//
// El tercero es el peligroso y es el que este test cierra. Tirar `revAt` en esta
// función deja la entidad diciendo «no revocada» sobre una fila que SÍ lo está:
// la clasificación da vía libre, y la única protección que queda es el UPDATE.
// Desde fuera no se ve NADA —el UPDATE afecta cero filas, sale el mismo
// domain.ErrConflict y el rollback borra la membresía que se llegó a escribir—,
// así que ningún test de conducta puede distinguirlo. La defensa en profundidad
// se habría quedado en una sola capa sin que nadie se entere, que es exactamente
// la forma en que estas cosas se pierden.
//
// Las otras tres van con ella por el mismo motivo: un scan que lee una columna y
// no la traslada es un dato perdido en silencio. `redeemed_at` decide el «ya se
// usó» y `role_id` es el rol que la persona recibe al entrar.
var camposNULLablesDeLaFila = []string{"RoleID", "RedeemedBy", "RedeemedAt", "RevokedAt"}

// TestCanje_LaLecturaCableaLasCuatroColumnasNULLables.
func TestCanje_LaLecturaCableaLasCuatroColumnasNULLables(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	archivo, err := parser.ParseFile(fset, ficheroDelCanje, nil, 0)
	if err != nil {
		t.Fatalf("parseando %s: %v", ficheroDelCanje, err)
	}
	fn := buscarFuncion(archivo, lecturaDelCanje)
	if fn == nil {
		t.Fatalf("no encontré %s en %s: el candado estaría vigilando una pared", lecturaDelCanje, ficheroDelCanje)
	}

	asignados := camposAsignados(fn)
	for _, campo := range camposNULLablesDeLaFila {
		if !asignados[campo] {
			t.Errorf("%s NO traslada %s a la entidad: la fila se lee y ese dato se pierde.\n"+
				"Con RevokedAt perdido, una invitación REVOCADA se clasifica como pendiente y la única "+
				"protección que queda es el `revoked_at IS NULL` del UPDATE — y ningún test de conducta puede "+
				"verlo, porque el desenlace desde fuera es idéntico (mismo ErrConflict, mismo rollback).",
				lecturaDelCanje, campo)
		}
	}
}

// camposAsignados devuelve los nombres de campo que la función asigna sobre
// alguna estructura (`x.Campo = …`). No mira sobre CUÁL, y no hace falta: en
// esta función solo hay una entidad que rellenar.
func camposAsignados(fn *ast.FuncDecl) map[string]bool {
	asignados := map[string]bool{}
	ast.Inspect(fn, func(node ast.Node) bool {
		asig, ok := node.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for _, izq := range asig.Lhs {
			if sel, esSel := izq.(*ast.SelectorExpr); esSel {
				asignados[sel.Sel.Name] = true
			}
		}
		return true
	})
	return asignados
}
