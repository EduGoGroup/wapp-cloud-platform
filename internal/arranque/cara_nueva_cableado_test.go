package arranque

// cara_nueva_cableado_test.go — QUE LA CARA NUEVA ESTÉ DELANTE DE UN MUX VACÍO, Y
// ENVUELTA UNA SOLA VEZ (F0 · T0.16/TX.3, RX.2.c; F8 · FX TX.24).
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
//   - envolver solo el mux de detrás y poner la cara nueva POR FUERA: las rutas
//     saldrían sin métrica ni rate-limit.
//
// 🔀 F8 · conmutar(conversacion): hasta F8 el mux de detrás era el del publicapi viejo y
// este test exigía que publicapi.Register montara sobre él. Con las 73 rutas en la cara
// nueva la regla se invierte: el mux de detrás es un http.NewServeMux() local y NADIE
// registra nada en él. Una ruta montada ahí saldría por la cara vieja, fuera del mapa.
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

// TestCableado_LaCaraNuevaVaDelanteDeLaViejaYSeEnvuelveUnaVez exige, en
// buildPublicAPIServer: un único apipublica.Componer cuyo segundo argumento es la
// variable local que nace de http.NewServeMux(); que NADIE monte nada sobre ese mux
// (ni Handle/HandleFunc como método, ni pasándolo a una función Register…: la cara vieja
// no sirve ninguna ruta); que el handler que se envuelve nazca del compuesto; y UNA sola
// llamada a httpapi.PublicRateLimit y a InstrumentHTTP("public", …) en todo el fichero.
func TestCableado_LaCaraNuevaVaDelanteDeLaViejaYSeEnvuelveUnaVez(t *testing.T) {
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
// fallo. Va en dos pasadas porque «nadie monta sobre el mux de detrás» necesita saber antes
// cuál es ese mux.
func newFaceWiringProblems(archivo *ast.File) []string {
	var visto cableadoDeLaCaraNueva
	ast.Inspect(archivo, func(n ast.Node) bool {
		visto.anota(n)
		return true
	})
	ast.Inspect(archivo, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			visto.newFaceNotesUseOfBackMux(call)
		}
		return true
	})
	return visto.newFaceProblems()
}

// cableadoDeLaCaraNueva acumula lo que el recorrido de http.go ve del montaje del :8103.
type cableadoDeLaCaraNueva struct {
	componer       int             // llamadas a apipublica.Componer
	varCompuesto   string          // variable que recibe el Componer
	viejoDelCompon string          // segundo argumento del Componer
	handlerDesde   string          // de qué se inicializa `var handler http.Handler`
	rateLimit      int             // llamadas a httpapi.PublicRateLimit
	instrPublic    int             // llamadas a X.InstrumentHTTP("public", …)
	muxesNuevos    map[string]bool // variables que nacen de http.NewServeMux()
	usosDelMux     []string        // quién monta algo sobre el mux de detrás
}

func (v *cableadoDeLaCaraNueva) anota(n ast.Node) {
	switch x := n.(type) {
	case *ast.AssignStmt:
		if len(x.Lhs) != 1 || len(x.Rhs) != 1 {
			return
		}
		if esLlamada(x.Rhs[0], "apipublica", "Componer") {
			v.varCompuesto = nombre(x.Lhs[0])
		}
		if esLlamada(x.Rhs[0], "http", "NewServeMux") && nombre(x.Lhs[0]) != "" {
			if v.muxesNuevos == nil {
				v.muxesNuevos = map[string]bool{}
			}
			v.muxesNuevos[nombre(x.Lhs[0])] = true
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

// newFaceNotesUseOfBackMux anota toda llamada que toque el mux de detrás y no sea el Componer:
// un método sobre él (publicMux.Handle, publicMux.HandleFunc…) o una función que lo reciba como
// argumento (X.Register(publicMux, …), registrar(publicMux)…). Cualquiera de las dos le da rutas.
func (v *cableadoDeLaCaraNueva) newFaceNotesUseOfBackMux(x *ast.CallExpr) {
	mux := v.viejoDelCompon
	if mux == "" || esLlamada(x, "apipublica", "Componer") {
		return
	}
	if sel, ok := x.Fun.(*ast.SelectorExpr); ok && nombre(sel.X) == mux {
		v.usosDelMux = append(v.usosDelMux, mux+"."+sel.Sel.Name)
	}
	for _, arg := range x.Args {
		if nombre(arg) == mux {
			v.usosDelMux = append(v.usosDelMux, textoDe(x.Fun)+"(… "+mux+" …)")
		}
	}
}

func (v *cableadoDeLaCaraNueva) newFaceProblems() []string {
	var p []string
	if v.componer != 1 {
		p = append(p, fmt.Sprintf("apipublica.Componer aparece %d veces en http.go; se espera exactamente 1 (una sola cara compuesta en el :8103)", v.componer))
	}
	if v.viejoDelCompon == "" || !v.muxesNuevos[v.viejoDelCompon] {
		p = append(p, fmt.Sprintf("el segundo argumento de apipublica.Componer es %q y no una variable local nacida de http.NewServeMux(): desde F8 detrás de la cara nueva va un mux VACÍO", v.viejoDelCompon))
	}
	for _, uso := range v.usosDelMux {
		p = append(p, fmt.Sprintf("http.go monta rutas sobre el mux de detrás con %s: desde F8 la cara vieja no sirve NINGUNA ruta (las 73 son de la cara nueva)", uso))
	}
	if v.varCompuesto == "" || v.handlerDesde != v.varCompuesto {
		p = append(p, fmt.Sprintf("`var handler http.Handler` nace de %q y el compuesto es %q: lo que se envuelve con rate-limit y métricas tiene que ser el COMPUESTO", v.handlerDesde, v.varCompuesto))
	}
	if v.rateLimit != 1 {
		p = append(p, fmt.Sprintf("httpapi.PublicRateLimit aparece %d veces en http.go; se espera 1 (RX.2.c: el compuesto se envuelve una sola vez)", v.rateLimit))
	}
	if v.instrPublic != 1 {
		p = append(p, fmt.Sprintf(`InstrumentHTTP("public", …) aparece %d veces en http.go; se espera 1 (RX.2.c)`, v.instrPublic))
	}
	return p
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
