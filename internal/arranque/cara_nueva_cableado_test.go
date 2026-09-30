package arranque

// cara_nueva_cableado_test.go — QUE LA CARA NUEVA ESTÉ DELANTE DE LA VIEJA, Y
// ENVUELTA UNA SOLA VEZ (F0 · T0.16/TX.3, RX.2.c).
//
// Test nuevo del arranque nuevo (no es copia del viejo). El :8103 sirve
// apipublica.Componer(cara nueva, publicMux) y el rate-limit y las métricas
// envuelven ese COMPUESTO una sola vez. Dos maneras de cablearlo mal que la huella
// no vería:
//
//   - envolver dos veces (la cara nueva con su propio InstrumentHTTP, y el compuesto
//     con el suyo): cada petición contaría doble en wapp_http_requests_total y el
//     cubo de rate-limit se gastaría dos veces por petición, sin que cambie ni una
//     ruta, ni una familia de /metrics en frío;
//   - envolver solo el mux viejo y poner la cara nueva POR FUERA: las rutas que mude
//     una fase saldrían sin métrica ni rate-limit, y en F0 (cara vacía) nadie lo
//     notaría.
//
// Se lee el AST de http.go, como sus hermanos *_cableado_test.go: sin BD, sin red,
// y NO SE SALTA nunca.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestCableado_LaCaraNuevaVaDelanteDeLaViejaYSeEnvuelveUnaVez exige, en
// buildPublicAPIServer: un único apipublica.Componer cuyo segundo argumento es
// publicMux (el mux al que publicapi.Register le da sus rutas); que el handler que
// se envuelve nazca de ese compuesto; y UNA sola llamada a httpapi.PublicRateLimit
// y a InstrumentHTTP("public", …) en todo el fichero.
func TestCableado_LaCaraNuevaVaDelanteDeLaViejaYSeEnvuelveUnaVez(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	archivo, err := parser.ParseFile(fset, "http.go", nil, 0)
	if err != nil {
		t.Fatalf("parseando http.go: %v", err)
	}

	var visto cableadoDeLaCaraNueva
	ast.Inspect(archivo, func(n ast.Node) bool {
		visto.anota(n)
		return true
	})
	visto.exige(t)
}

// cableadoDeLaCaraNueva acumula lo que el recorrido de http.go ve del montaje del :8103.
type cableadoDeLaCaraNueva struct {
	componer       int    // llamadas a apipublica.Componer
	varCompuesto   string // variable que recibe el Componer
	viejoDelCompon string // segundo argumento del Componer
	handlerDesde   string // de qué se inicializa `var handler http.Handler`
	rateLimit      int    // llamadas a httpapi.PublicRateLimit
	instrPublic    int    // llamadas a X.InstrumentHTTP("public", …)
	registerSobre  string // primer argumento de publicapi.Register
}

func (v *cableadoDeLaCaraNueva) anota(n ast.Node) {
	switch x := n.(type) {
	case *ast.AssignStmt:
		if len(x.Lhs) == 1 && len(x.Rhs) == 1 && esLlamada(x.Rhs[0], "apipublica", "Componer") {
			v.varCompuesto = nombre(x.Lhs[0])
		}
	case *ast.ValueSpec:
		if len(x.Names) == 1 && x.Names[0].Name == "handler" && len(x.Values) == 1 {
			v.handlerDesde = nombre(x.Values[0])
		}
	case *ast.CallExpr:
		v.anotaLlamada(x)
	}
}

func (v *cableadoDeLaCaraNueva) anotaLlamada(x *ast.CallExpr) {
	switch {
	case esLlamada(x, "apipublica", "Componer"):
		v.componer++
		if len(x.Args) == 2 {
			v.viejoDelCompon = nombre(x.Args[1])
		}
	case esLlamada(x, "httpapi", "PublicRateLimit"):
		v.rateLimit++
	case esLlamada(x, "publicapi", "Register"):
		if len(x.Args) > 0 {
			v.registerSobre = nombre(x.Args[0])
		}
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

func (v *cableadoDeLaCaraNueva) exige(t *testing.T) {
	t.Helper()
	if v.componer != 1 {
		t.Errorf("apipublica.Componer aparece %d veces en http.go; se espera exactamente 1 (una sola cara compuesta en el :8103)", v.componer)
	}
	if v.registerSobre == "" || v.viejoDelCompon != v.registerSobre {
		t.Errorf("la cara VIEJA del compuesto es %q, pero publicapi.Register monta sus rutas sobre %q: deben ser el mismo mux", v.viejoDelCompon, v.registerSobre)
	}
	if v.varCompuesto == "" || v.handlerDesde != v.varCompuesto {
		t.Errorf("`var handler http.Handler` nace de %q y el compuesto es %q: lo que se envuelve con rate-limit y métricas tiene que ser el COMPUESTO", v.handlerDesde, v.varCompuesto)
	}
	if v.rateLimit != 1 {
		t.Errorf("httpapi.PublicRateLimit aparece %d veces en http.go; se espera 1 (RX.2.c: el compuesto se envuelve una sola vez)", v.rateLimit)
	}
	if v.instrPublic != 1 {
		t.Errorf(`InstrumentHTTP("public", …) aparece %d veces en http.go; se espera 1 (RX.2.c)`, v.instrPublic)
	}
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
