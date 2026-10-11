package arranque

// cara_nueva_cableado_test.go — QUE EL :8103 SIRVA LA CARA SOLA, ENVUELTA UNA SOLA VEZ
// (F0 · T0.16/TX.3, RX.2.c; F10 · T10.9 · FX TX.25).
//
// Test nuevo del arranque nuevo (no es copia del viejo). El :8103 sirve la cara que devuelve
// caraNueva, y el rate-limit y las métricas la envuelven una sola vez. Tres maneras de
// cablearlo mal que la huella no vería:
//
//   - envolver dos veces: cada petición contaría doble en wapp_http_requests_total y el
//     cubo de rate-limit se gastaría dos veces por petición, sin que cambie ni una ruta,
//     ni una familia de /metrics en frío;
//   - envolver otra cosa y poner la cara POR FUERA: las rutas saldrían sin métrica ni
//     rate-limit;
//   - volver a poner un mux al lado de la cara (un http.NewServeMux() en http.go, o el
//     apipublica.Componer que los unía): una ruta montada ahí se serviría sin estar en
//     Cara.Patrones(), fuera de lo que los candados de rutas miran.
//
// Se lee el AST de http.go, como sus hermanos *_cableado_test.go: sin BD, sin red,
// y NO SE SALTA nunca.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestCableado_TheFaceIsServedAloneAndWrappedOnce exige, en http.go: ninguna llamada a
// apipublica.Componer ni a http.NewServeMux() (la cara va sola, sin mux al lado); que el
// handler que se envuelve nazca de la variable que recibe caraNueva(…); y UNA sola llamada a
// httpapi.PublicRateLimit y a InstrumentHTTP("public", …) en todo el fichero.
func TestCableado_TheFaceIsServedAloneAndWrappedOnce(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	archivo, err := parser.ParseFile(fset, "http.go", nil, 0)
	if err != nil {
		t.Fatalf("parseando http.go: %v", err)
	}
	for _, p := range newFaceWiringProblems(archivo) {
		t.Error(p)
	}
}

// newFaceWiringProblems aplica el candado a un http.go ya parseado y devuelve una línea por
// fallo.
func newFaceWiringProblems(archivo *ast.File) []string {
	var seen newFaceWiring
	ast.Inspect(archivo, func(n ast.Node) bool {
		seen.note(n)
		return true
	})
	return seen.problems()
}

// newFaceWiring acumula lo que el recorrido de http.go ve del montaje del :8103.
type newFaceWiring struct {
	compose     int    // llamadas a apipublica.Componer
	newMuxes    int    // llamadas a http.NewServeMux()
	faceVar     string // variable que recibe caraNueva(…)
	handlerFrom string // de qué se inicializa `var handler http.Handler`
	rateLimit   int    // llamadas a httpapi.PublicRateLimit
	instrPublic int    // llamadas a X.InstrumentHTTP("public", …)
}

func (v *newFaceWiring) note(n ast.Node) {
	switch x := n.(type) {
	case *ast.AssignStmt:
		if len(x.Lhs) == 1 && len(x.Rhs) == 1 && isLocalCall(x.Rhs[0], "caraNueva") {
			v.faceVar = nombre(x.Lhs[0])
		}
	case *ast.ValueSpec:
		if len(x.Names) == 1 && x.Names[0].Name == "handler" && len(x.Values) == 1 {
			v.handlerFrom = nombre(x.Values[0])
		}
	case *ast.CallExpr:
		v.noteCall(x)
	}
}

func (v *newFaceWiring) noteCall(x *ast.CallExpr) {
	switch {
	case esLlamada(x, "apipublica", "Componer"):
		v.compose++
	case esLlamada(x, "http", "NewServeMux"):
		v.newMuxes++
	case esLlamada(x, "httpapi", "PublicRateLimit"):
		v.rateLimit++
	default:
		sel, ok := x.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "InstrumentHTTP" || len(x.Args) != 2 {
			return
		}
		if lit, ok := x.Args[0].(*ast.BasicLit); ok && lit.Value == `"public"` {
			v.instrPublic++
		}
	}
}

func (v *newFaceWiring) problems() []string {
	var p []string
	if v.compose != 0 {
		p = append(p, fmt.Sprintf("apipublica.Componer aparece %d veces en http.go; se espera 0: el :8103 sirve la cara sola", v.compose))
	}
	if v.newMuxes != 0 {
		p = append(p, fmt.Sprintf("http.NewServeMux() aparece %d veces en http.go; se espera 0: una ruta montada en un mux al lado de la cara se serviría fuera de Cara.Patrones()", v.newMuxes))
	}
	if v.faceVar == "" || v.handlerFrom != v.faceVar {
		p = append(p, fmt.Sprintf("`var handler http.Handler` nace de %q y la cara de caraNueva(…) es %q: lo que se envuelve con rate-limit y métricas tiene que ser la CARA", v.handlerFrom, v.faceVar))
	}
	if v.rateLimit != 1 {
		p = append(p, fmt.Sprintf("httpapi.PublicRateLimit aparece %d veces en http.go; se espera 1 (RX.2.c: la cara se envuelve una sola vez)", v.rateLimit))
	}
	if v.instrPublic != 1 {
		p = append(p, fmt.Sprintf(`InstrumentHTTP("public", …) aparece %d veces en http.go; se espera 1 (RX.2.c)`, v.instrPublic))
	}
	return p
}

// isLocalCall dice si e es una llamada funcion(…) a una función del propio paquete.
func isLocalCall(e ast.Expr, funcion string) bool {
	call, ok := e.(*ast.CallExpr)
	return ok && nombre(call.Fun) == funcion
}

// nombre devuelve el nombre de e si es un identificador, o "" si no lo es.
func nombre(e ast.Expr) string {
	if id, ok := e.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

// esLlamada dice si e es una llamada paquete.Funcion(…).
func esLlamada(e ast.Expr, paquete, funcion string) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != funcion {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == paquete
}
