package bootstrap_test

import (
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

type routeCheck struct {
	pattern    string
	permission string
	handlerExp string
	line       int
}

func extractPlatformRoute(fset *token.FileSet, call *ast.CallExpr) *routeCheck {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Handle" || len(call.Args) < 2 {
		return nil
	}

	patternLit, ok := call.Args[0].(*ast.BasicLit)
	if !ok {
		return nil
	}
	pattern := strings.Trim(patternLit.Value, "\"")

	adminCall, ok := call.Args[1].(*ast.CallExpr)
	if !ok {
		return nil
	}
	adminFun, ok := adminCall.Fun.(*ast.Ident)
	if !ok || adminFun.Name != "adminHandler" || len(adminCall.Args) < 6 {
		return nil
	}

	permLit, ok := adminCall.Args[3].(*ast.BasicLit)
	if !ok {
		return nil
	}
	perm := strings.Trim(permLit.Value, "\"")

	handlerStr := formatNode(fset, adminCall.Args[5])

	isPlatform := strings.Contains(handlerStr, "platformadmin.") ||
		strings.Contains(pattern, "/admin/tenants") ||
		strings.Contains(pattern, "/admin/access-requests")

	if !isPlatform {
		return nil
	}

	pos := fset.Position(call.Pos())
	return &routeCheck{
		pattern:    pattern,
		permission: perm,
		handlerExp: handlerStr,
		line:       pos.Line,
	}
}

// TestINV056_1_PlatformPermissionsMustEndInDotAny asegura el invariante INV-056.1:
// Toda ruta o handler perteneciente al plano de administración de plataforma
// (paquete internal/platformadmin o rutas /admin/tenants, /admin/access-requests)
// DEBE exigir un permiso RBAC que termine estrictamente en ".any".
func TestINV056_1_PlatformPermissionsMustEndInDotAny(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	bootstrapPath := filepath.Join(".", "bootstrap.go")
	node, err := parser.ParseFile(fset, bootstrapPath, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parser.ParseFile(%s): %v", bootstrapPath, err)
	}

	var foundRoutes []routeCheck

	ast.Inspect(node, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if r := extractPlatformRoute(fset, call); r != nil {
			foundRoutes = append(foundRoutes, *r)
		}
		return true
	})

	if len(foundRoutes) == 0 {
		t.Fatal("no se encontraron rutas de plataforma en bootstrap.go; ¿cambió la estructura de registro?")
	}

	for _, r := range foundRoutes {
		if !strings.HasSuffix(r.permission, ".any") {
			t.Errorf("INV-056.1 VIOLADO en bootstrap.go:%d: ruta %q con handler %q exige permiso %q (DEBE terminar en '.any')",
				r.line, r.pattern, r.handlerExp, r.permission)
		}
	}
}

// TestINV056_1_DetectsUnguardedPlatformRoute es el caso NEGATIVO que T1.4 exige
// explícitamente («comprobar ambas direcciones, no solo el verde», A-01). No basta
// con que el test de arriba esté en verde contra el bootstrap.go de HOY: hay que
// probar que la MAQUINARIA de detección (extractPlatformRoute + formatNode) de
// verdad reconoce una ruta de plataforma mal cercada, para que mañana, si alguien
// escribe algo así en bootstrap.go de verdad, TestINV056_1_PlatformPermissionsMustEndInDotAny
// se ponga en ROJO en vez de quedarse callado (el bug original: la rama de
// detección por texto de handler estaba muerta y solo sobrevivían dos prefijos de
// pattern escritos a mano — un handler platformadmin.* bajo CUALQUIER OTRO pattern
// pasaba desapercibido).
//
// El fixture es intencionadamente ajeno a /admin/tenants y /admin/access-requests
// (los dos prefijos hardcodeados) para que la ÚNICA vía de detección posible sea
// el texto "platformadmin." del handler — exactamente la rama que A-01 reparó.
func TestINV056_1_DetectsUnguardedPlatformRoute(t *testing.T) {
	t.Parallel()

	const src = `package bootstrap

func wireBadRoute() {
	mux.Handle("GET /admin/fleet/sessions", adminHandler(authMW, auditor, log,
		"tenants.list", "fleet", platformadmin.ListSessionsHandler(platformRepo, cfg.PlatformTenantID)))
}
`

	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, "fixture_ruta_sin_cerca.go", src, 0)
	if err != nil {
		t.Fatalf("parser.ParseFile del fixture: %v", err)
	}

	var found *routeCheck
	ast.Inspect(node, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if r := extractPlatformRoute(fset, call); r != nil {
			found = r
		}
		return true
	})

	if found == nil {
		t.Fatal("extractPlatformRoute NO detectó la ruta platformadmin del fixture: " +
			"la detección por texto de handler sigue muerta (isPlatform nunca da true " +
			"para un handler platformadmin.* bajo un pattern que no sea /admin/tenants " +
			"ni /admin/access-requests)")
	}
	// Mismo assert que TestINV056_1_PlatformPermissionsMustEndInDotAny aplica a
	// cada ruta encontrada: con este fixture DEBE fallar (el caso negativo existe
	// justo para violar INV-056.1). Si esto NO falla, el fixture está mal armado.
	if strings.HasSuffix(found.permission, ".any") {
		t.Fatalf("fixture mal construido: el permiso %q ya termina en '.any' (el caso negativo debe violar INV-056.1)", found.permission)
	}
}

// formatNode imprime el NODO como CÓDIGO FUENTE (go/printer), no como el volcado
// del árbol que da ast.Fprint. A-01: con ast.Fprint, un selector como
// platformadmin.ListTenantsHandler se parte en dos nodos Ident separados
// ("platformadmin" y "ListTenantsHandler") y la subcadena literal
// "platformadmin." (con punto) NUNCA aparece en el volcado — la detección de
// isPlatform() por texto de handler queda muerta y solo sobrevive el prefijo de
// pattern hardcodeado, que es justo la lista-que-alguien-se-olvida-de-actualizar
// que T1.4 quería evitar. go/printer sí reproduce el código fuente tal cual
// (p. ej. "platformadmin.ListTenantsHandler(platformRepo, cfg.PlatformTenantID)"),
// así que la subcadena vuelve a aparecer.
func formatNode(fset *token.FileSet, n ast.Node) string {
	var sb strings.Builder
	if err := printer.Fprint(&sb, fset, n); err != nil {
		return ""
	}
	return sb.String()
}
