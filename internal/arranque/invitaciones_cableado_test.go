// Copia de internal/bootstrap/arranque/invitaciones_cableado_test.go @ 80807ba (F0 · 05 §6), retargeteada
// en F2 (T2.31, conmutar(acceso)): las invitaciones las sirve la cara NUEVA (apipublica).
package arranque

// invitaciones_cableado_test.go — QUE LA PUERTA DE INVITACIONES ESTÉ ENCHUFADA
// (Plan 047 · Ola A · T-A2 y T-A8).
//
// Mismo modo de fallo MUDO que vigila roleplane_cableado_test.go, y por eso el
// mismo método: `apipublica.MountRolePlane` monta las tres rutas SOLO si
// `RolePlaneDeps.Invitations` viene informado. Con nil no falla nada, no avisa nada, y
// los tests de apipublica siguen VERDES —construyen sus propias Deps— mientras
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
// arranque tiene que hacer para que T-A2 y T-A8 existan en producción, retargeteadas en
// F2 (T2.31, conmutar(acceso)): que buildRolePlane construya el servicio con el usecase
// NUEVO (internal/modulos/acceso/iam/usecase) y que buildPublicAPIServer se lo pase a la
// cara nueva (apipublica.RolePlaneDeps.Invitations), dejando a nil el de la vieja.
func TestCableado_LaPuertaDeInvitacionesEstaEnchufada(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()

	archivoAuth, err := parser.ParseFile(fset, "auth_roleplane.go", nil, 0)
	if err != nil {
		t.Fatalf("parseando auth_roleplane.go: %v", err)
	}
	const usecaseNuevo = `"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/usecase"`
	aliasNuevo := false
	for _, imp := range archivoAuth.Imports {
		if imp.Path.Value == usecaseNuevo && imp.Name != nil && imp.Name.Name == "iamusecase" {
			aliasNuevo = true
		}
	}
	if !aliasNuevo {
		t.Errorf("auth_roleplane.go no importa %s como iamusecase: el InvitationService tiene que ser el de acceso NUEVO", usecaseNuevo)
	}

	construido := false
	ast.Inspect(archivoAuth, func(n ast.Node) bool {
		if llamada, ok := n.(*ast.CallExpr); ok && campoDe(llamada.Fun) == "iamusecase.NewInvitationService" {
			construido = true
		}
		return true
	})
	if !construido {
		t.Error("internal/arranque/auth_roleplane.go NO construye el InvitationService.\n" +
			"Sin él no hay CallerResolver, que es lo ÚNICO que le da un tenant a este usecase (INV-04): " +
			"in.IssueInvitationInput no tiene campo TenantID, y esa ausencia es la regla escrita en el tipo.")
	}

	// La otra mitad: construirla y no pasarla tiene el MISMO efecto que no construirla —las
	// tres rutas /api/v1/invitations no se montan y la dueña se queda sin la ÚNICA vía para
	// incorporar a alguien a quien no puede buscar—. Y dársela también a la vieja la
	// registraría dos veces, tapada por la nueva.
	visto := cableadoDelPlanoDeRolesEn(t)
	if visto.planoDe == "" {
		t.Fatal("internal/arranque/http.go NO llama a buildRolePlane")
	}
	visto.exigeDelPlano(t, "Invitations")
	visto.exigeViejoANil(t, "Invitations")
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
// Cruza a ../apipublica/roleplane.go porque el registro vive ahí: un test de AST en
// el paquete del registro no podría decir nada del arranque, ni al revés.
func TestCableado_LasTresRutasDeInvitacionesLlevanSuScope(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	archivo, err := parser.ParseFile(fset, "../apipublica/roleplane.go", nil, 0)
	if err != nil {
		t.Fatalf("parseando ../apipublica/roleplane.go: %v", err)
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
		if !ok || campoCompletoDe(llamada.Fun) != "c.Handle" || len(llamada.Args) < 2 {
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
			t.Errorf("internal/apipublica/roleplane.go NO monta %s: sin ella, la administración de "+
				"invitaciones no existe en el proceso y el síntoma es un 404 mudo", ruta)
		}
	}
}
