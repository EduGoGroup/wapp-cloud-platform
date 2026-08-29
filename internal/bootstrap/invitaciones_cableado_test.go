package bootstrap_test

// invitaciones_cableado_test.go — QUE LA PUERTA DE INVITACIONES ESTÉ ENCHUFADA
// (Plan 047 · Ola A · T-A2 y T-A8).
//
// Mismo modo de fallo MUDO que vigila roleplane_cableado_test.go, y por eso el
// mismo método: `registerRolePlane` monta las tres rutas SOLO si
// `Deps.Invitations` viene informado. Con nil no falla nada, no avisa nada, y los
// contract tests de publicapi siguen VERDES —construyen sus propias Deps— mientras
// en producción las rutas no existen y contestan 404 de ruta inexistente, que es
// indistinguible del 404 que estas mismas rutas dan a la invitación ajena.
//
// Es la trampa de «una ola cerrada no es una ola ENCENDIDA», que esta ola ya pagó
// dos veces. Se lee el AST, así que no hace falta base de datos y NO SE SALTA.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestCableado_LaPuertaDeInvitacionesEstaEnchufada exige las dos cosas que el
// arranque tiene que hacer para que T-A2 y T-A8 existan en producción: que
// buildRolePlane construya el servicio y que buildPublicAPIServer se lo pase a
// las Deps.
func TestCableado_LaPuertaDeInvitacionesEstaEnchufada(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()

	construido := false
	archivoAuth, err := parser.ParseFile(fset, "auth.go", nil, 0)
	if err != nil {
		t.Fatalf("parseando auth.go: %v", err)
	}
	ast.Inspect(archivoAuth, func(n ast.Node) bool {
		if llamada, ok := n.(*ast.CallExpr); ok && campoDe(llamada.Fun) == "iamusecase.NewInvitationService" {
			construido = true
		}
		return true
	})
	if !construido {
		t.Error("internal/bootstrap/auth.go NO construye el InvitationService.\n" +
			"Sin él no hay CallerResolver, que es lo ÚNICO que le da un tenant a este usecase (INV-04): " +
			"in.IssueInvitationInput no tiene campo TenantID, y esa ausencia es la regla escrita en el tipo.")
	}

	cableado := false
	archivoHTTP, err := parser.ParseFile(fset, "http.go", nil, 0)
	if err != nil {
		t.Fatalf("parseando http.go: %v", err)
	}
	ast.Inspect(archivoHTTP, func(n ast.Node) bool {
		asignacion, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, lhs := range asignacion.Lhs {
			if campoDe(lhs) != "pub.Invitations" {
				continue
			}
			// 🔴 Ver el campo escrito NO basta: es una interfaz, y `pub.Invitations =
			// nil` compila igual de bien que el cable bueno. Un nil desmonta las tres
			// rutas en silencio.
			if i < len(asignacion.Rhs) && campoDe(asignacion.Rhs[i]) == "nil" {
				t.Fatalf("pub.Invitations se cablea a nil: las rutas /api/v1/invitations NO se montarían (%s)",
					fset.Position(asignacion.Pos()))
			}
			cableado = true
		}
		return true
	})
	if !cableado {
		t.Error("buildPublicAPIServer NO le pasa a publicapi.Deps la administración de invitaciones " +
			"(pub.Invitations).\nConstruirla y no pasarla tiene el MISMO efecto que no construirla: las tres " +
			"rutas /api/v1/invitations no se montan, y la dueña se queda sin la ÚNICA vía para incorporar a " +
			"alguien a quien no puede buscar.")
	}
}

// TestCableado_LasTresRutasDeInvitacionesLlevanSuScope.
//
// Las tres formas de montarlas mal que NO darían ningún rojo por sí solas:
//
//   - el listado con `protect` en vez de `protectRead` (auditaría una lectura,
//     rompiendo el patrón vigente y ensuciando la bitácora con eventos sin efecto);
//   - la emisión o la revocación con `protectRead` (escribirían sin dejar rastro:
//     nadie podría saber después quién abrió o cerró esa puerta);
//   - el scope equivocado — y este es el peor de los tres: un `members.read` en la
//     emisión convertiría en administradora a quien solo tenía que mirar, porque
//     emitir una invitación ES meter gente en la empresa, en diferido.
//
// Cruza a ../publicapi/roleplane.go porque el registro vive ahí: un test de AST en
// el paquete del registro no podría decir nada de bootstrap, ni al revés.
func TestCableado_LasTresRutasDeInvitacionesLlevanSuScope(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	archivo, err := parser.ParseFile(fset, "../publicapi/roleplane.go", nil, 0)
	if err != nil {
		t.Fatalf("parseando ../publicapi/roleplane.go: %v", err)
	}

	type esperada struct {
		envoltorio string
		scope      string
	}
	quiero := map[string]esperada{
		`"GET /api/v1/invitations"`:         {"protectRead", "scopeMembersRead"},
		`"POST /api/v1/invitations"`:        {"protect", "scopeMembersWrite"},
		`"DELETE /api/v1/invitations/{id}"`: {"protect", "scopeMembersWrite"},
	}
	vistas := make(map[string]bool, len(quiero))

	ast.Inspect(archivo, func(n ast.Node) bool {
		llamada, ok := n.(*ast.CallExpr)
		if !ok || campoDe(llamada.Fun) != "mux.Handle" || len(llamada.Args) < 2 {
			return true
		}
		patron, ok := llamada.Args[0].(*ast.BasicLit)
		if !ok {
			return true
		}
		exigido, gobernada := quiero[patron.Value]
		if !gobernada {
			return true
		}
		vistas[patron.Value] = true
		envoltorio, ok := llamada.Args[1].(*ast.CallExpr)
		if !ok || campoDe(envoltorio.Fun) != exigido.envoltorio {
			t.Errorf("%s NO se monta con %s (%s)", patron.Value, exigido.envoltorio, fset.Position(llamada.Pos()))
			return true
		}
		if !argumentoPresente(envoltorio.Args, exigido.scope) {
			t.Errorf("%s no se protege con %s: el scope equivocado aquí no da un error, da un 403 a la dueña "+
				"—o, peor, deja emitir a quien solo podía mirar— (%s)",
				patron.Value, exigido.scope, fset.Position(llamada.Pos()))
		}
		return true
	})

	// 🚨 GUARDA ANTI-HUECO: un barrido que no encuentra nada pasa siempre. Si una
	// ruta se renombra o se monta desde otro fichero, este candado tiene que
	// fallar y enterarse alguien, en vez de vigilar una pared.
	for ruta := range quiero {
		if !vistas[ruta] {
			t.Errorf("internal/publicapi/roleplane.go NO monta %s: sin ella, la administración de "+
				"invitaciones no existe en el proceso y el síntoma es un 404 mudo", ruta)
		}
	}
}
