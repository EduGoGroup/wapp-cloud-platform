// Copia de internal/bootstrap/arranque/roleplane_cableado_test.go @ 80807ba (F0 · 05 §6), retargeteada
// en F2 (T2.31, conmutar(acceso)): el plano de roles lo sirve la cara NUEVA (apipublica).
package arranque

// roleplane_cableado_test.go — QUE LA PUERTA AL PLANO DE ROLES ESTÉ ENCHUFADA
// (Plan 047 · Ola 1.0 · T1.0-4).
//
// # POR QUÉ ESTE TEST EXISTE
//
// Por lo mismo que sus hermanos de al lado, y con un modo de fallo especialmente
// mudo: `apipublica.MountRolePlane` (antes `publicapi.registerRolePlane`) monta las
// rutas SOLO si `RolePlaneDeps.Roles` y `RolePlaneDeps.Members` vienen informados. Con
// cualquiera de los dos en nil:
//
//   - no falla nada y no avisa nada — ni un error de compilación, ni un log, ni un
//     rojo en los tests de apipublica, que construyen sus propias Deps y por tanto
//     seguirían verdes con el arranque entero desconectado;
//   - las rutas simplemente NO EXISTEN y responden 404 de ruta inexistente, que es
//     indistinguible desde fuera de «ese rol no es tuyo» — el mismo 404 que estas
//     rutas devuelven a propósito en el caso cross-tenant.
//
// Es la trampa de «una ola cerrada no es una ola encendida», que esta ola ya pagó
// dos veces. Mismo método que reanalisis/quotetext_cableado_test.go, con un fichero
// distinto: estas dos dependencias las resuelve buildPublicAPIServer (http.go) y se
// las da a la cara nueva (caraNueva, mudanzas.go). Se lee su AST, así que no hace
// falta base de datos y NO SE SALTA nunca.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"
)

// TestCableado_LaPuertaAlPlanoDeRolesEstaEnchufada exige las tres cosas que el
// arranque tiene que hacer para que T1.0-4 exista en producción, retargeteadas en F2
// (T2.31, conmutar(acceso)) a la cara NUEVA: que http.go llame a buildRolePlane, que
// apipublica.RolePlaneDeps reciba Roles y Members DE ESE plano (no nil, no otra cosa) y
// que la cara VIEJA ya no los reciba (pub.Roles y pub.Members solo a nil: con un
// valor, publicapi registraría B1–B11 también en el mux viejo, tapadas por la nueva y
// sin que ni la huella ni el candado de mudanzas lo vieran).
func TestCableado_LaPuertaAlPlanoDeRolesEstaEnchufada(t *testing.T) {
	t.Parallel()
	visto := cableadoDelPlanoDeRolesEn(t)
	if visto.planoDe == "" {
		t.Fatal("internal/arranque/http.go NO llama a buildRolePlane.\n" +
			"Sin él no hay CallerResolver, que es lo ÚNICO que le da un tenant a estos usecases " +
			"(INV-04): ningún Input de in.* tiene campo TenantID.")
	}
	for _, campo := range []string{"Roles", "Members"} {
		visto.exigeDelPlano(t, campo)
		visto.exigeViejoANil(t, campo)
	}
}

// cableadoDelPlanoDeRoles es lo que el recorrido de http.go ve del plano de roles.
type cableadoDelPlanoDeRoles struct {
	fset *token.FileSet
	// planoDe es la variable que recibe buildRolePlane(...).
	planoDe string
	// nuevo es el literal apipublica.RolePlaneDeps: campo → expresión (texto) y posición.
	nuevo map[string]ast.Expr
	// viejo son las asignaciones a pub.<campo>: campo → expresiones asignadas.
	viejo map[string][]ast.Expr
}

// cableadoDelPlanoDeRolesEn recorre el AST de http.go y anota el plano de roles.
func cableadoDelPlanoDeRolesEn(t *testing.T) cableadoDelPlanoDeRoles {
	t.Helper()
	v := cableadoDelPlanoDeRoles{fset: token.NewFileSet(), nuevo: map[string]ast.Expr{}, viejo: map[string][]ast.Expr{}}
	archivo, err := parser.ParseFile(v.fset, "http.go", nil, 0)
	if err != nil {
		t.Fatalf("parseando http.go: %v", err)
	}
	ast.Inspect(archivo, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			v.anotaAsignacion(x)
		case *ast.CompositeLit:
			if types.ExprString(x.Type) != "apipublica.RolePlaneDeps" {
				return true
			}
			for _, el := range x.Elts {
				if kv, ok := el.(*ast.KeyValueExpr); ok {
					v.nuevo[types.ExprString(kv.Key)] = kv.Value
				}
			}
		}
		return true
	})
	return v
}

func (v *cableadoDelPlanoDeRoles) anotaAsignacion(x *ast.AssignStmt) {
	if len(x.Rhs) == 1 && len(x.Lhs) >= 1 {
		if llamada, ok := x.Rhs[0].(*ast.CallExpr); ok && campoDe(llamada.Fun) == "buildRolePlane" {
			v.planoDe = nombre(x.Lhs[0])
		}
	}
	if len(x.Lhs) != len(x.Rhs) {
		return
	}
	for i, lhs := range x.Lhs {
		if campo, ok := strings.CutPrefix(types.ExprString(lhs), "pub."); ok {
			v.viejo[campo] = append(v.viejo[campo], x.Rhs[i])
		}
	}
}

// exigeDelPlano: el campo del literal apipublica.RolePlaneDeps es <plano>.<campo en
// minúscula>, el servicio que construyó buildRolePlane.
func (v cableadoDelPlanoDeRoles) exigeDelPlano(t *testing.T, campo string) {
	t.Helper()
	quiero := v.planoDe + "." + strings.ToLower(campo)
	got, ok := v.nuevo[campo]
	switch {
	case !ok:
		t.Errorf("apipublica.RolePlaneDeps no recibe %s en http.go: sus rutas NO se montan en la cara nueva "+
			"y responden 404 de ruta inexistente — el mismo 404 que dan al recurso ajeno, así que nadie lo nota", campo)
	case types.ExprString(got) != quiero:
		// 🔴 Ver el campo escrito NO basta: es una interfaz, y `Roles: nil` compila igual
		// de bien que el cable bueno. Tiene que ser el servicio del plano.
		t.Errorf("apipublica.RolePlaneDeps.%s = %s (%s); se espera %s, el servicio que construye buildRolePlane",
			campo, types.ExprString(got), v.fset.Position(got.Pos()), quiero)
	}
}

// exigeViejoANil: la cara vieja ya no recibe el servicio. pub.<campo> se asigna, y solo a
// nil, para que quede escrito en el código que la vieja deja de servir esas rutas.
func (v cableadoDelPlanoDeRoles) exigeViejoANil(t *testing.T, campo string) {
	t.Helper()
	asignaciones := v.viejo[campo]
	if len(asignaciones) == 0 {
		t.Errorf("http.go no deja pub.%s a nil de forma explícita: desde F2 la cara vieja no sirve esas rutas "+
			"y tiene que quedar escrito (FX TX.7)", campo)
	}
	for _, e := range asignaciones {
		if types.ExprString(e) != "nil" {
			t.Errorf("pub.%s = %s (%s): la cara VIEJA volvería a registrar las rutas que ya sirve la nueva, "+
				"tapadas por ella y con OTRO servicio detrás", campo, types.ExprString(e), v.fset.Position(e.Pos()))
		}
	}
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
// Cruza a ../apipublica/roleplane.go a propósito: el registro de rutas vive ahí, y
// un test de AST en el paquete del registro no podría decir nada del arranque ni
// al revés. Sigue sin necesitar base de datos.
func TestCableado_ElListadoDeMiembrosSeMontaComoLectura(t *testing.T) {
	t.Parallel()
	const ruta = `"GET /api/v1/members"`
	fset := token.NewFileSet()
	archivo, err := parser.ParseFile(fset, "../apipublica/roleplane.go", nil, 0)
	if err != nil {
		t.Fatalf("parseando ../apipublica/roleplane.go: %v", err)
	}

	montada := false
	ast.Inspect(archivo, func(n ast.Node) bool {
		llamada, ok := n.(*ast.CallExpr)
		if !ok || campoCompletoDe(llamada.Fun) != "c.Handle" || len(llamada.Args) < 2 {
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
		t.Error("internal/apipublica/roleplane.go NO monta GET /api/v1/members.\n" +
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
