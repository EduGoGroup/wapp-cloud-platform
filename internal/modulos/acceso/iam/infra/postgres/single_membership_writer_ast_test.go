//go:build pendiente

package iampostgres_test

// single_membership_writer_ast_test.go — EL CANDADO DE «UNA SOLA EMPRESA POR USUARIO» (Plan 047 ·
// Ola 1.0 · T1.0-2; endurecido en Ola 5 · T5.2). Porta el candado de invariante
// internal/iam/infra/postgres/membresia_unica_ast_test.go @ 9a77307 (D-F2-1, diseño F2 §6).
//
// El criterio de T1.0-2 pedía UN solo sitio que insertara en public.tenant_members, «o dos, con
// justificación escrita». Mientras convivan los dos árboles (hasta F10) son DOS, y esta es la
// justificación: el GrantTenantAccess viejo (internal/iam/infra/postgres/memberships.go, lo que
// corre en UAT) y su reconstrucción en este paquete. Los dos son el mismo caso de uso; cuando
// muera el viejo, la lista se queda en uno.
//
// 🔴 LO QUE VIGILA, y por qué no basta un test de conducta. Un segundo INSERT escrito en otro
// sitio no da error: da un usuario con dos filas en tenant_members que nadie decidió darle. El
// defecto está en el código que NO llama a la guarda, y a eso no se llega ejercitando el que sí;
// hay que preguntarle al AST.
//
// 🔴 A DIFERENCIA DEL VIEJO, ESTE BARRE TODO internal/, árbol nuevo incluido. El viejo salta
// internal/{modulos,nucleo,arranque,apipublica,pendiente,candados} desde F0 (T0.27, D-F4-1): el
// árbol nuevo trae sus propios candados, y este es el de acceso.
//
// A cada función que contenga el INSERT se le exigen CUATRO cosas, y las tres últimas son de
// ORDEN y de ANIDAMIENTO, que es lo que un test de conducta no puede ver (una transacción que hace
// rollback borra la diferencia entre «contó y escribió» y «escribió sin contar»):
//
//  1. LLAMA a la guarda contable (countOtherMemberships).
//  2. La llama en el CUERPO de la función, no dentro de un if/for/switch ni de un func literal:
//     una guarda condicionada es una guarda que alguien puede rodear con una condición nueva.
//  3. La llamada va ANTES —por posición en el fichero— de cualquier INSERT.
//  4. Y el CERROJO (pg_advisory_xact_lock) va antes que la guarda: contar sin haber tomado el
//     cerrojo es contar un estado que otra transacción puede estar cambiando (TOCTOU de T5.2).
//
// 🚨 GUARDA ANTI-HUECO: exige encontrar EXACTAMENTE los escritores conocidos antes de que su
// veredicto signifique algo — si la tabla se renombra o el SQL se compone por trozos (T-2), este
// candado falla y se entera alguien, en vez de vigilar una pared.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	// sweepRoot es internal/ visto desde este paquete.
	sweepRoot = "../../../../.."
	// membershipInsert es el literal que marca a un escritor de la tabla.
	membershipInsert = "INSERT INTO public.tenant_members"
	// sharedGuard es la única función que decide si esa alta puede pasar.
	sharedGuard = "countOtherMemberships"
	// personLock es el literal del advisory lock que serializa las altas de un mismo usuario.
	personLock = "pg_advisory_xact_lock"
)

// expectedWriters son los ÚNICOS ficheros que pueden dar de alta una membresía, con la ruta
// relativa a sweepRoot: el viejo y el nuevo, hasta F10. Añadir uno aquí sin la guarda no engaña
// al test: la guarda se comprueba aparte, función por función.
var expectedWriters = []string{
	"iam/infra/postgres/memberships.go",
	"modulos/acceso/iam/infra/postgres/memberships.go",
}

// TestSingleMembershipWriter_LockThenGuardThenInsert.
func TestSingleMembershipWriter_LockThenGuardThenInsert(t *testing.T) {
	fset := token.NewFileSet()
	var found []string

	err := filepath.WalkDir(sweepRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		if !hasStringLiteral(file, membershipInsert) {
			return nil
		}

		rel, rerr := filepath.Rel(sweepRoot, path)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		found = append(found, rel)

		for _, fn := range funcsWithLiteral(file, membershipInsert) {
			checkWriter(t, fset, rel, fn)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("barriendo %s: %v", sweepRoot, err)
	}

	slices.Sort(found)
	want := slices.Clone(expectedWriters)
	slices.Sort(want)
	if !slices.Equal(found, want) {
		t.Fatalf("escritores de tenant_members = %v, esperaba %v.\n"+
			"Si falta alguno, el candado está vigilando una pared (¿se renombró la tabla, o el SQL se "+
			"compone por trozos?). Si encontró uno de más, alguien duplicó el alta en vez de llamar a "+
			"GrantTenantAccess: o lo reconduce ahí, o documenta por qué existe y se asegura de que llama "+
			"a %s.", found, want, sharedGuard)
	}
}

// hasStringLiteral dice si algún literal de cadena del fichero contiene el texto dado. Mira el
// AST y no el fichero en bruto a propósito: así un comentario que mencione el INSERT —los hay, y
// explican justo esto— no cuenta como escritor.
func hasStringLiteral(file *ast.File, text string) bool {
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		if strings.Contains(lit.Value, text) {
			found = true
			return false
		}
		return true
	})
	return found
}

// checkWriter aplica las CUATRO exigencias de la cabecera a una función que escribe en
// tenant_members, nombrando posiciones en el mensaje: un candado que dice «está mal» sin decir
// dónde manda a leer el fichero entero.
func checkWriter(t *testing.T, fset *token.FileSet, rel string, fn *ast.FuncDecl) {
	t.Helper()

	calls := topLevelCalls(fn, sharedGuard)
	if len(calls) == 0 {
		if nested := firstCall(fn, sharedGuard); nested != token.NoPos {
			t.Errorf("%s · %s llama a %s DENTRO de un if/for/switch (%s): la guarda tiene que evaluarse "+
				"SIEMPRE. Condicionarla es exactamente cómo se rodea sin ponerse en rojo.",
				rel, fn.Name.Name, sharedGuard, fset.Position(nested))
			return
		}
		t.Errorf("%s · %s inserta en tenant_members y NO llama a %s: puede escribir una segunda empresa "+
			"a espaldas de la guarda (MD-055.2, Plan 047 · Ola 5 · T5.2)", rel, fn.Name.Name, sharedGuard)
		return
	}
	guardPos := calls[0]

	for _, insertPos := range literalPositions(fn, membershipInsert) {
		if insertPos < guardPos {
			t.Errorf("%s · %s tiene un INSERT en tenant_members (%s) ANTES de la guarda %s (%s): hay un "+
				"camino que escribe sin contar. Que la llamada exista más abajo no la evalúa.",
				rel, fn.Name.Name, fset.Position(insertPos), sharedGuard, fset.Position(guardPos))
		}
	}

	locks := literalPositions(fn, personLock)
	if len(locks) == 0 {
		t.Errorf("%s · %s cuenta y escribe SIN tomar %s: vuelve a abrirse la ventana TOCTOU que T5.2 "+
			"cerró (dos altas simultáneas de la misma persona cuentan cero las dos y escriben las dos).",
			rel, fn.Name.Name, personLock)
		return
	}
	if locks[0] > guardPos {
		t.Errorf("%s · %s toma %s (%s) DESPUÉS de la guarda %s (%s): el cerrojo tiene que preceder al "+
			"conteo o no protege el conteo.",
			rel, fn.Name.Name, personLock, fset.Position(locks[0]), sharedGuard, fset.Position(guardPos))
	}
}

// funcsWithLiteral devuelve las funciones del fichero que contienen el texto dado en alguno de
// sus literales de cadena.
func funcsWithLiteral(file *ast.File, text string) []*ast.FuncDecl {
	var res []*ast.FuncDecl
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		if len(literalPositions(fn, text)) > 0 {
			res = append(res, fn)
		}
	}
	return res
}

// literalPositions devuelve, en orden, las posiciones de los literales de cadena de fn que
// contienen el texto dado.
func literalPositions(fn *ast.FuncDecl, text string) []token.Pos {
	var res []token.Pos
	ast.Inspect(fn, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if ok && lit.Kind == token.STRING && strings.Contains(lit.Value, text) {
			res = append(res, lit.Pos())
		}
		return true
	})
	slices.Sort(res)
	return res
}

// topLevelCalls devuelve las posiciones de las llamadas a name que son SENTENCIAS del cuerpo de
// la función —incluida la forma `x, err := name(...)`, que es la que usa el código real—, y NO
// las que cuelgan de un if, un for, un switch o un func literal.
//
// La distinción es el corazón del endurecimiento de T5.2: una guarda dentro de un `if` se salta
// cambiando la condición.
func topLevelCalls(fn *ast.FuncDecl, name string) []token.Pos {
	var res []token.Pos
	for _, stmt := range fn.Body.List {
		var exprs []ast.Expr
		switch st := stmt.(type) {
		case *ast.AssignStmt:
			exprs = st.Rhs
		case *ast.ExprStmt:
			exprs = []ast.Expr{st.X}
		default:
			continue
		}
		for _, e := range exprs {
			if call, ok := e.(*ast.CallExpr); ok && calleeName(call) == name {
				res = append(res, call.Pos())
			}
		}
	}
	slices.Sort(res)
	return res
}

// firstCall devuelve la posición de la PRIMERA llamada a name en cualquier punto de fn (anidada o
// no), o token.NoPos. Distingue «no la llama» de «la llama condicionada».
func firstCall(fn *ast.FuncDecl, name string) token.Pos {
	pos := token.NoPos
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || calleeName(call) != name {
			return true
		}
		pos = call.Pos()
		return false
	})
	return pos
}

// calleeName devuelve el identificador final de la función llamada: `f(...)` → "f" y
// `pkg.F(...)` → "F". El receptor o el paquete no importan.
func calleeName(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		return fn.Sel.Name
	}
	return ""
}
