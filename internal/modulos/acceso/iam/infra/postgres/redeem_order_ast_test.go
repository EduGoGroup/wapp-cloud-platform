package iampostgres_test

// redeem_order_ast_test.go — EL CANDADO DEL ORDEN DE LOS PASOS DEL CANJE (Plan 047 · Ola A ·
// T-A5). Porta internal/iam/infra/postgres/canje_orden_ast_test.go @ 9a77307 (D-F2-1, R-P5).
//
// 🔴 POR QUÉ ESTE TEST TUVO QUE EXISTIR, Y ES UN HALLAZGO, NO UNA PRECAUCIÓN. El criterio de T-A5
// pide que un canje rechazado por «ya eres miembro de otra empresa» deje la invitación SIN
// MARCAR, y su mutación declarada es mover el marcado ANTES de la guarda. Esa mutación SE EJECUTÓ
// contra Postgres real (2026-08-28) y NO derribó ni un test — porque el desenlace es INOBSERVABLE
// desde fuera: los cuatro pasos viven en UNA transacción, así que cuando GrantTenantAccess
// devuelve conflicto, el rollback deshace también el marcado.
//
// O sea: lo que hoy cumple el criterio de T-A5 es la ATOMICIDAD, no el orden. ¿Y entonces por qué
// mantener el orden? Porque deja de ser inobservable en cuanto la transacción deje de cubrirlo
// todo, y eso es un cambio de una línea: pasarle `r.db` a GrantTenantAccess en vez de `tx`, meter
// un Commit entre medias, o partir el canje en dos funciones que abran cada una la suya. En
// cualquiera de esos escenarios el orden es lo ÚNICO que impide que un canje rechazado queme la
// invitación de alguien. Este candado es lo que impide que se pierda mientras tanto.
//
// 🚨 GUARDA ANTI-HUECO: exige encontrar las DOS piezas antes de opinar.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

const (
	// redeemFunc es la función que orquesta los cuatro pasos.
	redeemFunc = "Redeem"
	// accessGrant es el paso (2): la guarda de «una sola empresa» va dentro.
	accessGrant = "GrantTenantAccess"
	// redeemMark identifica el paso (3) por el SQL que escribe. Se busca por el literal y no por
	// el nombre de una variable porque el literal es lo que no se puede renombrar sin cambiar lo
	// que hace (T-2).
	redeemMark = "SET redeemed_at"
)

// TestRedeem_GrantsAccessBeforeMarkingInvitation.
func TestRedeem_GrantsAccessBeforeMarkingInvitation(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, redeemFile, nil, 0)
	if err != nil {
		t.Fatalf("parseando %s: %v", redeemFile, err)
	}

	fn := findFunc(file, redeemFunc)
	if fn == nil {
		t.Fatalf("no encontré %s en %s: el candado estaría vigilando una pared", redeemFunc, redeemFile)
	}

	grantPos := firstCallPos(fn, accessGrant)
	markPos := firstLiteralPos(fn, redeemMark)

	if grantPos == token.NoPos {
		t.Fatalf("%s no llama a %s.\n"+
			"Ese es un fallo peor que el del orden: sin esa llamada, o el canje no da acceso, o lo da "+
			"insertando en tenant_members por su cuenta — y entonces se salta la guarda de «una sola empresa».",
			redeemFunc, accessGrant)
	}
	if markPos == token.NoPos {
		t.Fatalf("no encontré el SQL que contiene %q en %s: si el marcado se mueve a otra función o el SQL "+
			"se compone por trozos, este candado deja de vigilar el orden y hay que reescribirlo, no borrarlo.",
			redeemMark, redeemFunc)
	}

	if grantPos > markPos {
		t.Errorf("el canje MARCA la invitación (línea %d) antes de conceder el acceso con %s (línea %d), y "+
			"tiene que ser al revés.\n"+
			"Hoy la transacción tapa la diferencia —el rollback deshace las dos escrituras— así que ningún "+
			"test de conducta puede verlo. Pero el día que GrantTenantAccess reciba `r.db` en vez de `tx`, o "+
			"que aparezca un Commit entre medias, este orden es lo único que impide que un canje RECHAZADO "+
			"queme la invitación: quedaría terminal, sin membresía detrás, y la dueña tendría que emitir otra "+
			"sin entender por qué.",
			fset.Position(markPos).Line, accessGrant, fset.Position(grantPos).Line)
	}
}

// firstCallPos devuelve la posición de la PRIMERA llamada a esa función (o método).
func firstCallPos(fn *ast.FuncDecl, name string) token.Pos {
	pos := token.NoPos
	ast.Inspect(fn, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch f := call.Fun.(type) {
		case *ast.Ident:
			if f.Name == name {
				pos = call.Pos()
				return false
			}
		case *ast.SelectorExpr:
			if f.Sel.Name == name {
				pos = call.Pos()
				return false
			}
		}
		return true
	})
	return pos
}

// firstLiteralPos devuelve la posición del PRIMER literal de cadena que contiene ese texto. Mira
// el AST y no el fichero en bruto para que un comentario que mencione el UPDATE —los hay, y
// explican justo esto— no cuente.
func firstLiteralPos(fn *ast.FuncDecl, text string) token.Pos {
	pos := token.NoPos
	ast.Inspect(fn, func(node ast.Node) bool {
		lit, ok := node.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		if strings.Contains(lit.Value, text) {
			pos = lit.Pos()
			return false
		}
		return true
	})
	return pos
}
