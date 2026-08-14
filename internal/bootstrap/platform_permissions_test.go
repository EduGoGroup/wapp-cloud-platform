package bootstrap_test

import (
	"go/ast"
	"go/parser"
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

func formatNode(fset *token.FileSet, n ast.Node) string {
	var sb strings.Builder
	if err := ast.Fprint(&sb, fset, n, nil); err != nil {
		return ""
	}
	return sb.String()
}
