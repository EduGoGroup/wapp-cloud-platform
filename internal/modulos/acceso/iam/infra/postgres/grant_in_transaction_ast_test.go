package iampostgres_test

// grant_in_transaction_ast_test.go — EL CANDADO DE «EL ALTA DE ACCESO VA DENTRO DE UNA
// TRANSACCIÓN» (D-F2-12, Jhoan, 2026-10-04; hallazgo de F2-03).
//
// 🔴 LO QUE VIGILA, y por qué no basta un test de conducta. GrantTenantAccess toma un
// pg_advisory_xact_lock sobre la persona como primera operación, y ese cerrojo vive lo que viva la
// TRANSACCIÓN del ejecutor que recibe. Si quien la llama le pasa el pool (`r.db`) en vez de su
// `tx`, cada sentencia va en autocommit: el cerrojo se toma y se suelta en el acto, el conteo de
// «otras empresas» y el INSERT dejan de ser atómicos entre transacciones, y dos altas simultáneas
// de la misma persona en dos empresas cuentan cero las dos y escriben las dos. Nada da error: el
// desenlace es un usuario con dos membresías que nadie decidió darle, y solo bajo una carrera que
// no se puede provocar de forma determinista desde fuera. El defecto está en el ARGUMENTO, y a
// eso se llega preguntándole al AST.
//
// Los candados vecinos no lo ven: single_membership_writer vigila la función que ESCRIBE (cerrojo
// → guarda → INSERT) y redeem_order vigila el ORDEN de los pasos del canje; ninguno mira qué
// ejecutor recibe GrantTenantAccess.
//
// LA REGLA. En los ficheros de producción de este paquete, toda llamada a GrantTenantAccess:
//
//  1. está dentro de una función con nombre (no en una variable de paquete);
//  2. pasa como ejecutor —su segundo argumento— un IDENTIFICADOR, nunca un selector (`r.db`) ni
//     otra expresión;
//  3. y ese identificador se liga, en la MISMA función y ANTES de la llamada, al resultado de
//     BeginTx — y a nada más: una reasignación a otra cosa lo invalida.
//
// 🚨 GUARDA ANTI-HUECO: exige encontrar las dos llamadas conocidas (Add en memberships.go y Redeem
// en canje.go). Si mañana desaparecen o se mudan, el candado falla en vez de pasar vacío.
//
// ⚠️ LO QUE NO ALCANZA: las llamadas de OTROS paquetes (la aprobación del operador en
// platformadmin). Mira solo este directorio.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strings"
	"testing"
)

const (
	// txOpener es la única llamada cuyo resultado vale como ejecutor del alta.
	txOpener = "BeginTx"
	// grantExecutorArg es la posición del ejecutor en GrantTenantAccess(ctx, exec, …).
	grantExecutorArg = 1
)

// expectedGrantCallers son las llamadas que TIENEN que existir, como «fichero · función».
var expectedGrantCallers = []string{
	"canje.go · Redeem",
	"memberships.go · Add",
}

// TestGrantTenantAccess_ReceivesTheCallersTransaction.
func TestGrantTenantAccess_ReceivesTheCallersTransaction(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("leyendo el directorio del paquete: %v", err)
	}

	fset := token.NewFileSet()
	var found []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, perr := parser.ParseFile(fset, name, nil, 0)
		if perr != nil {
			t.Fatalf("parseando %s: %v", name, perr)
		}

		inFuncs := 0
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			calls := callsTo(fn, accessGrant)
			inFuncs += len(calls)
			for _, call := range calls {
				found = append(found, name+" · "+fn.Name.Name)
				checkGrantExecutor(t, fset, name, fn, call)
			}
		}
		if total := len(callsTo(file, accessGrant)); total != inFuncs {
			t.Errorf("%s llama a %s %d veces y solo %d están dentro de una función con nombre: una llamada "+
				"desde una variable de paquete no tiene transacción propia que este candado pueda comprobar.",
				name, accessGrant, total, inFuncs)
		}
	}

	slices.Sort(found)
	for _, want := range expectedGrantCallers {
		if !slices.Contains(found, want) {
			t.Errorf("no encontré la llamada a %s de «%s» (encontradas: %v).\n"+
				"Si la función se renombró o se mudó de fichero, actualiza expectedGrantCallers; si dejó de "+
				"llamar a %s, o ya no da acceso o inserta en tenant_members por su cuenta. En los dos casos "+
				"este candado estaría vigilando una pared.", accessGrant, want, found, accessGrant)
		}
	}
}

// checkGrantExecutor aplica las exigencias 2 y 3 de la cabecera a UNA llamada a GrantTenantAccess
// de la función fn.
func checkGrantExecutor(t *testing.T, fset *token.FileSet, file string, fn *ast.FuncDecl, call *ast.CallExpr) {
	t.Helper()
	where := fset.Position(call.Pos())

	if len(call.Args) <= grantExecutorArg {
		t.Errorf("%s · %s llama a %s (%s) con %d argumentos: no hay ejecutor que comprobar. Si la firma "+
			"cambió, este candado hay que reescribirlo, no borrarlo.",
			file, fn.Name.Name, accessGrant, where, len(call.Args))
		return
	}

	exec, ok := call.Args[grantExecutorArg].(*ast.Ident)
	if !ok {
		t.Errorf("%s · %s pasa a %s (%s) un ejecutor que no es una variable local —un selector como "+
			"`r.db`, u otra expresión—, y tiene que ser la transacción que abrió con %s.\n"+
			"Fuera de una transacción el pg_advisory_xact_lock de la persona se suelta en el acto: dos altas "+
			"simultáneas de la misma persona cuentan cero las dos y escriben las dos, y ningún test de "+
			"conducta lo ve.", file, fn.Name.Name, accessGrant, where, txOpener)
		return
	}

	bindings := bindingsOf(fn, exec.Name)
	if len(bindings) == 0 {
		t.Errorf("%s · %s pasa `%s` a %s (%s) y esa variable no se liga en la función al resultado de %s "+
			"(¿es un parámetro, o un campo?). El cerrojo de la persona solo protege si el ejecutor es una "+
			"transacción abierta por quien llama.", file, fn.Name.Name, exec.Name, accessGrant, where, txOpener)
		return
	}
	if first := bindings[0]; first.pos > call.Pos() {
		t.Errorf("%s · %s liga `%s` (%s) DESPUÉS de pasársela a %s (%s): la transacción tiene que estar "+
			"abierta antes del alta.", file, fn.Name.Name, exec.Name, fset.Position(first.pos), accessGrant, where)
	}
	for _, b := range bindings {
		if b.opener != txOpener {
			t.Errorf("%s · %s liga `%s` (%s) a algo que no es el resultado de %s, y luego se la pasa a %s "+
				"(%s). Si deja de ser una transacción, el cerrojo de la persona se suelta en el acto y se "+
				"reabre la carrera de las dos altas simultáneas (TOCTOU de T5.2).",
				file, fn.Name.Name, exec.Name, fset.Position(b.pos), txOpener, accessGrant, where)
		}
	}
}

// binding es una asignación a una variable: dónde y de qué llamada viene ("" si el lado derecho no
// es una llamada).
type binding struct {
	pos    token.Pos
	opener string
}

// bindingsOf devuelve, en orden de posición, las asignaciones de fn (`:=` y `=`) y sus
// declaraciones `var` que tienen a name en el lado izquierdo.
func bindingsOf(fn *ast.FuncDecl, name string) []binding {
	var res []binding
	ast.Inspect(fn, func(n ast.Node) bool {
		switch st := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range st.Lhs {
				if id, ok := lhs.(*ast.Ident); ok && id.Name == name {
					res = append(res, binding{pos: st.Pos(), opener: openerOf(st.Rhs, i)})
				}
			}
		case *ast.ValueSpec:
			for i, id := range st.Names {
				if id.Name == name {
					res = append(res, binding{pos: st.Pos(), opener: openerOf(st.Values, i)})
				}
			}
		}
		return true
	})
	slices.SortFunc(res, func(a, b binding) int { return int(a.pos - b.pos) })
	return res
}

// openerOf devuelve el nombre de la llamada que da valor a la variable de índice i: la única del
// lado derecho en `a, b := f()`, o la de su misma posición en `a, b := f(), g()`. "" si no es una
// llamada.
func openerOf(rhs []ast.Expr, i int) string {
	var expr ast.Expr
	switch {
	case len(rhs) == 1:
		expr = rhs[0]
	case i < len(rhs):
		expr = rhs[i]
	default:
		return ""
	}
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return ""
	}
	return calleeName(call)
}

// callsTo devuelve todas las llamadas a name que cuelgan de root, anidadas o no.
func callsTo(root ast.Node, name string) []*ast.CallExpr {
	var res []*ast.CallExpr
	ast.Inspect(root, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok && calleeName(call) == name {
			res = append(res, call)
		}
		return true
	})
	return res
}
