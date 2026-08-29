package iampostgres_test

// canje_orden_ast_test.go — EL CANDADO DEL ORDEN DE LOS PASOS DEL CANJE
// (Plan 047 · Ola A · T-A5).
//
// 🔴 POR QUÉ ESTE TEST TUVO QUE EXISTIR, Y ES UN HALLAZGO, NO UNA PRECAUCIÓN.
// El criterio de T-A5 pide que un canje rechazado por «ya eres miembro de otra
// empresa» deje la invitación SIN MARCAR, y su mutación declarada es mover el
// marcado ANTES de la guarda. Esa mutación SE EJECUTÓ contra Postgres real
// (2026-08-28) y NO derribó ni un test — y no porque los tests fueran flojos:
// porque el desenlace es INOBSERVABLE desde fuera. Los cuatro pasos viven en UNA
// transacción, así que cuando GrantTenantAccess devuelve conflicto, el rollback
// deshace también el marcado. Los dos órdenes dejan la base EXACTAMENTE igual.
//
// O sea: lo que hoy cumple el criterio de T-A5 es la ATOMICIDAD, no el orden.
// Decirlo importa, porque quien lea «el marcado va después» y suponga que hay un
// test de conducta detrás, supondrá mal.
//
// ¿Y entonces por qué mantener el orden? Porque deja de ser inobservable en
// cuanto la transacción deje de cubrirlo todo, y eso es un cambio de una línea:
// pasarle `r.db` a GrantTenantAccess en vez de `tx`, meter un Commit entre
// medias, o partir el canje en dos funciones que abran cada una la suya. En
// cualquiera de esos escenarios el orden es lo ÚNICO que impide que un canje
// rechazado queme la invitación de alguien. El orden es la defensa que sobrevive
// al día que la transacción se rompa; este candado es lo que impide que se pierda
// mientras tanto, en silencio y sin que ningún test se entere.
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
	// canjeRedeem es la función que orquesta los cuatro pasos.
	canjeRedeem = "Redeem"
	// guardaDelAcceso es el paso (2): la guarda de «una sola empresa» va dentro.
	guardaDelAcceso = "GrantTenantAccess"
	// marcaDelCanje identifica el paso (3) por el SQL que escribe. Se busca por el
	// literal y no por el nombre de una variable porque el literal es lo que no se
	// puede renombrar sin cambiar lo que hace.
	marcaDelCanje = "SET redeemed_at"
)

// TestCanje_ElAccesoSeConcedeANTESDeMarcarLaInvitacion.
func TestCanje_ElAccesoSeConcedeANTESDeMarcarLaInvitacion(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	archivo, err := parser.ParseFile(fset, ficheroDelCanje, nil, 0)
	if err != nil {
		t.Fatalf("parseando %s: %v", ficheroDelCanje, err)
	}

	fn := buscarFuncion(archivo, canjeRedeem)
	if fn == nil {
		t.Fatalf("no encontré %s en %s: el candado estaría vigilando una pared", canjeRedeem, ficheroDelCanje)
	}

	posGuarda := posDeLlamada(fn, guardaDelAcceso)
	posMarca := posDeLiteral(fn, marcaDelCanje)

	if posGuarda == token.NoPos {
		t.Fatalf("%s no llama a %s.\n"+
			"Ese es un fallo peor que el del orden: sin esa llamada, o el canje no da acceso, o lo da "+
			"insertando en tenant_members por su cuenta — y entonces se salta la guarda de «una sola empresa» "+
			"y deja a esa persona sin poder volver a entrar.", canjeRedeem, guardaDelAcceso)
	}
	if posMarca == token.NoPos {
		t.Fatalf("no encontré el SQL que contiene %q en %s: si el marcado se mueve a otra función o el SQL "+
			"se compone por trozos, este candado deja de vigilar el orden y hay que reescribirlo, no borrarlo.",
			marcaDelCanje, canjeRedeem)
	}

	if posGuarda > posMarca {
		t.Errorf("el canje MARCA la invitación (línea %d) antes de conceder el acceso con %s (línea %d), y "+
			"tiene que ser al revés.\n"+
			"Hoy la transacción tapa la diferencia —el rollback deshace las dos escrituras— así que ningún "+
			"test de conducta puede verlo. Pero el día que GrantTenantAccess reciba `r.db` en vez de `tx`, o "+
			"que aparezca un Commit entre medias, este orden es lo único que impide que un canje RECHAZADO "+
			"queme la invitación: quedaría terminal, sin membresía detrás, y la dueña tendría que emitir otra "+
			"sin entender por qué.",
			fset.Position(posMarca).Line, guardaDelAcceso, fset.Position(posGuarda).Line)
	}
}

// posDeLlamada devuelve la posición de la PRIMERA llamada a esa función.
func posDeLlamada(fn *ast.FuncDecl, nombre string) token.Pos {
	pos := token.NoPos
	ast.Inspect(fn, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch f := call.Fun.(type) {
		case *ast.Ident:
			if f.Name == nombre {
				pos = call.Pos()
				return false
			}
		case *ast.SelectorExpr:
			if f.Sel.Name == nombre {
				pos = call.Pos()
				return false
			}
		}
		return true
	})
	return pos
}

// posDeLiteral devuelve la posición del PRIMER literal de cadena que contiene
// ese texto. Mira el AST y no el fichero en bruto para que un comentario que
// mencione el UPDATE —los hay, y explican justo esto— no cuente.
func posDeLiteral(fn *ast.FuncDecl, texto string) token.Pos {
	pos := token.NoPos
	ast.Inspect(fn, func(node ast.Node) bool {
		lit, ok := node.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		if strings.Contains(lit.Value, texto) {
			pos = lit.Pos()
			return false
		}
		return true
	})
	return pos
}
