package bootstrap_test

// roleplane_cableado_test.go — QUE LA PUERTA AL PLANO DE ROLES ESTÉ ENCHUFADA
// (Plan 047 · Ola 1.0 · T1.0-4).
//
// # POR QUÉ ESTE TEST EXISTE
//
// Por lo mismo que sus hermanos de al lado, y con un modo de fallo especialmente
// mudo: `publicapi.registerRolePlane` monta las rutas SOLO si `Deps.Roles` y
// `Deps.Members` vienen informados. Con cualquiera de los dos en nil:
//
//   - no falla nada y no avisa nada — ni un error de compilación, ni un log, ni un
//     rojo en los tests de publicapi, que construyen sus propias Deps y por tanto
//     seguirían verdes con el arranque entero desconectado;
//   - las rutas simplemente NO EXISTEN y responden 404 de ruta inexistente, que es
//     indistinguible desde fuera de «ese rol no es tuyo» — el mismo 404 que estas
//     rutas devuelven a propósito en el caso cross-tenant.
//
// Es la trampa de «una ola cerrada no es una ola encendida», que esta ola ya pagó
// dos veces. Mismo método que reanalisis/quotetext_cableado_test.go, con un fichero
// distinto: estas dos dependencias las resuelve buildPublicAPIServer (http.go), donde
// ya se resuelve pub.Audit, y no el literal de Deps de bootstrap.go. Se lee su AST,
// así que no hace falta base de datos y NO SE SALTA nunca.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestCableado_LaPuertaAlPlanoDeRolesEstaEnchufada exige las tres cosas que el
// arranque tiene que hacer para que T1.0-4 exista en producción.
func TestCableado_LaPuertaAlPlanoDeRolesEstaEnchufada(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	archivo, err := parser.ParseFile(fset, "http.go", nil, 0)
	if err != nil {
		t.Fatalf("parseando http.go: %v", err)
	}

	var visto cableadoDelPlanoDeRoles
	ast.Inspect(archivo, func(n ast.Node) bool {
		visto.anota(t, fset, n)
		return true
	})
	visto.exige(t)
}

// TestCableado_ElListadoDeMiembrosSeMontaComoLectura vigila la ruta que estrena
// `members.read`, el scope que la migración 0084 sembró y que hasta T1.0-4 no
// consumía NADIE.
//
// Un scope sin consumidor no da ningún rojo: la migración lo siembra, los tests
// pasan y la pantalla de «los miembros de mi empresa» se queda sin backend — que
// es el hueco por el que nació esta ola (D-047.8). Y hay dos formas de montarla
// mal que tampoco darían rojo: con `protect` (auditaría una LECTURA, rompiendo
// el patrón vigente y ensuciando la bitácora con eventos sin efecto) o con otro
// scope (un `members.write` de más convertiría en administradora a quien solo
// tenía que mirar). Las tres cosas se exigen aquí a la vez.
//
// Cruza a ../publicapi/roleplane.go a propósito: el registro de rutas vive ahí, y
// un test de AST en el paquete del registro no podría decir nada de bootstrap ni
// al revés. Sigue sin necesitar base de datos.
func TestCableado_ElListadoDeMiembrosSeMontaComoLectura(t *testing.T) {
	t.Parallel()
	const ruta = `"GET /api/v1/members"`
	fset := token.NewFileSet()
	archivo, err := parser.ParseFile(fset, "../publicapi/roleplane.go", nil, 0)
	if err != nil {
		t.Fatalf("parseando ../publicapi/roleplane.go: %v", err)
	}

	montada := false
	ast.Inspect(archivo, func(n ast.Node) bool {
		llamada, ok := n.(*ast.CallExpr)
		if !ok || campoDe(llamada.Fun) != "mux.Handle" || len(llamada.Args) < 2 {
			return true
		}
		patron, ok := llamada.Args[0].(*ast.BasicLit)
		if !ok || patron.Value != ruta {
			return true
		}
		montada = true
		envoltorio, ok := llamada.Args[1].(*ast.CallExpr)
		if !ok || campoDe(envoltorio.Fun) != "protectRead" {
			t.Fatalf("GET /api/v1/members NO se monta con protectRead: es una LECTURA y no debe auditarse (%s)",
				fset.Position(llamada.Pos()))
		}
		if !argumentoPresente(envoltorio.Args, "scopeMembersRead") {
			t.Fatalf("GET /api/v1/members no se protege con scopeMembersRead: sin ese scope, `members.read` "+
				"seguiría sembrado en la 0084 sin un solo consumidor (%s)", fset.Position(llamada.Pos()))
		}
		return true
	})
	if !montada {
		t.Error("internal/publicapi/roleplane.go NO monta GET /api/v1/members.\n" +
			"Sin esa ruta, `members.read` es un scope sembrado que no usa nadie y la pantalla de miembros " +
			"de la Ola 1 se queda sin backend al que llamar — el hueco exacto que esta ola existe para cerrar.")
	}
}

// argumentoPresente reporta si alguno de los argumentos es el identificador dado.
func argumentoPresente(args []ast.Expr, nombre string) bool {
	for _, a := range args {
		if campoDe(a) == nombre {
			return true
		}
	}
	return false
}

type cableadoDelPlanoDeRoles struct {
	construido bool // (a) buildRolePlane(...)
	roles      bool // (b) pub.Roles = ...
	miembros   bool // (c) pub.Members = ...
}

func (c *cableadoDelPlanoDeRoles) anota(t *testing.T, fset *token.FileSet, n ast.Node) {
	t.Helper()
	switch v := n.(type) {
	case *ast.CallExpr:
		if campoDe(v.Fun) == "buildRolePlane" {
			c.construido = true
		}
	case *ast.AssignStmt:
		for i, lhs := range v.Lhs {
			campo := campoDe(lhs)
			if campo != "pub.Roles" && campo != "pub.Members" {
				continue
			}
			// 🔴 Ver el campo escrito NO basta: los dos son interfaces y `pub.Roles =
			// nil` compila igual de bien que el cable bueno. Un nil aquí desmonta las
			// rutas en silencio, así que se rechaza en el acto.
			if i < len(v.Rhs) && campoDe(v.Rhs[i]) == "nil" {
				t.Fatalf("%s se cablea a nil: las rutas del plano de roles NO se montarían (%s)",
					campo, fset.Position(v.Pos()))
			}
			if campo == "pub.Roles" {
				c.roles = true
			} else {
				c.miembros = true
			}
		}
	}
}

func (c *cableadoDelPlanoDeRoles) exige(t *testing.T) {
	t.Helper()
	if !c.construido {
		t.Error("internal/bootstrap/http.go NO llama a buildRolePlane.\n" +
			"Sin él no hay CallerResolver, que es lo ÚNICO que le da un tenant a estos usecases " +
			"(INV-04): ningún Input de in.* tiene campo TenantID.")
	}
	if !c.roles {
		t.Error("buildPublicAPIServer NO le pasa a publicapi.Deps la administración de roles (pub.Roles).\n" +
			"Construirla y no pasarla tiene el MISMO efecto que no construirla: GET/POST /api/v1/roles " +
			"y las rutas de rol/grant bajo /api/v1/members no se montan y responden 404 de ruta " +
			"inexistente — el mismo 404 que estas rutas dan al recurso ajeno, así que nadie lo nota.")
	}
	if !c.miembros {
		t.Error("buildPublicAPIServer NO le pasa a publicapi.Deps la administración de membresía (pub.Members).\n" +
			"POST /api/v1/members y DELETE /api/v1/members/{user_id} no se montan: la dueña de la " +
			"empresa no tendría por dónde dar de alta a nadie, y el síntoma sería un 404 mudo.")
	}
}
