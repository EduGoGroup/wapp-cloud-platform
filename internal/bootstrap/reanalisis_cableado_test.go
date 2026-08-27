package bootstrap_test

// reanalisis_cableado_test.go — QUE EL RE-ANÁLISIS ESTÉ ENCHUFADO (Plan 044 · Ola 4 ·
// T4.6, cierre de T4.10 mitad 2).
//
// # POR QUÉ ESTE TEST EXISTE, Y NO ES CEREMONIA
//
// Esta ola ya pagó DOS VECES el mismo defecto: la Ola 3 escribió `stages.NewMatch` y
// `stages.NewDraft` con sus tests y sin un solo call-site de producción, y antes la
// Ola 2 tuvo que cerrar el mismo agujero con T2.9. Los tests de las piezas seguían
// verdes en los dos casos, porque una pieza que nadie construye pasa igual sus
// propios tests.
//
// T4.6 deja DOS cables que fallan EN SILENCIO si faltan:
//
//   - `stages.ConEmpujeCRM`: sin él, un re-análisis escribe su revisión y el CRM se
//     queda con la versión vieja del pedido. La etapa lo grita con un log.Error, pero
//     nadie mira el log hasta que el integrador reclama.
//   - `reanalisis.NewServicio` + `Deps.Reanalysis`: sin ellos la ruta NO SE MONTA y
//     `POST /api/v1/intakes/{id}/reanalyze` responde 404 de ruta inexistente. Nada
//     falla, nada avisa: el botón «Regenerar» de la consola simplemente no hace nada.
//
// Mismo método que pipeline_captacion_cableado_test.go: se lee el AST de bootstrap.go
// y se exige ver las llamadas. No monta el proceso — no hay base de datos aquí — y por
// eso NO SE SALTA nunca.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestCableado_ElReanalisisEstaEnchufado exige las cuatro cosas de T4.6 que
// bootstrap.go tiene que hacer.
func TestCableado_ElReanalisisEstaEnchufado(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	archivo, err := parser.ParseFile(fset, "bootstrap.go", nil, 0)
	if err != nil {
		t.Fatalf("parseando bootstrap.go: %v", err)
	}

	var visto cableadoDelReanalisis
	ast.Inspect(archivo, func(n ast.Node) bool {
		visto.anota(t, fset, n)
		return true
	})
	visto.exige(t)
}

type cableadoDelReanalisis struct {
	servicio  bool // (a) reanalisis.NewServicio(...)
	enDeps    bool // (b) publicapi.Deps{... Reanalysis: ...}
	empujeCRM bool // (c) stages.NewDraft(..., stages.ConEmpujeCRM(...))
	clausura  bool // (d) la clausura llama a PushRevisionByID del Service
}

func (c *cableadoDelReanalisis) anota(t *testing.T, fset *token.FileSet, n ast.Node) {
	t.Helper()
	switch v := n.(type) {
	case *ast.CallExpr:
		switch campoDe(v.Fun) {
		case "reanalisis.NewServicio":
			c.servicio = true
		case "stages.NewDraft":
			c.anotaEmpuje(t, fset, v)
		case "intakeService.PushRevisionByID":
			// La clausura del nudo de construcción: el `draft` nace ANTES que el Service
			// de solicitudes, así que el cable no se puede resolver al construir. Lo que
			// se exige es que la función que se pasa llame de verdad al Service — una
			// clausura vacía compilaría igual y no empujaría nada.
			c.clausura = true
		}
	case *ast.KeyValueExpr:
		if campoDe(v.Key) == "Reanalysis" {
			if campoDe(v.Value) == "nil" {
				t.Fatalf("publicapi.Deps.Reanalysis se cablea a nil: la ruta no se montaría (%s)",
					fset.Position(v.Pos()))
			}
			c.enDeps = true
		}
	}
}

// anotaEmpuje exige que la etapa `draft` nazca con el puente CRM.
//
// 🔴 NO BASTA CON VER LA OPCIÓN ESCRITA: `ConEmpujeCRM(nil)` compila y la propia
// opción se traga el nil (es nil-safe a propósito, como ConAforo y ConZonasDeEnvio),
// así que el empuje desaparecería sin que nada lo dijera hasta que un re-análisis real
// no llegara al CRM. Por eso se mira también el argumento.
func (c *cableadoDelReanalisis) anotaEmpuje(t *testing.T, fset *token.FileSet, llamada *ast.CallExpr) {
	t.Helper()
	for _, arg := range llamada.Args {
		opt, ok := arg.(*ast.CallExpr)
		if !ok || campoDe(opt.Fun) != "stages.ConEmpujeCRM" {
			continue
		}
		if len(opt.Args) != 1 || campoDe(opt.Args[0]) == "nil" {
			t.Fatalf("stages.ConEmpujeCRM no recibe un empujador utilizable: la opción se traga el nil "+
				"y el re-análisis dejaría de salir al CRM en silencio (%s)", fset.Position(opt.Pos()))
		}
		c.empujeCRM = true
	}
}

func (c *cableadoDelReanalisis) exige(t *testing.T) {
	t.Helper()
	if !c.servicio {
		t.Error("internal/bootstrap/bootstrap.go NO llama a reanalisis.NewServicio.\n" +
			"Sin el caso de uso construido, Deps.Reanalysis queda nil y la ruta " +
			"POST /api/v1/intakes/{id}/reanalyze NO SE MONTA: responde 404 de ruta inexistente. " +
			"Nada falla y nada avisa — el botón «Regenerar» de la consola simplemente no hace nada.")
	}
	if !c.enDeps {
		t.Error("publicapi.Deps NO recibe el servicio de re-análisis (campo Reanalysis).\n" +
			"Construirlo y no pasarlo tiene el MISMO efecto que no construirlo: la ruta no se monta.")
	}
	if !c.empujeCRM {
		t.Error("stages.NewDraft se construye SIN stages.ConEmpujeCRM.\n" +
			"El borrador sale igual, así que ningún otro test se pone rojo. Lo que se pierde es el " +
			"cierre de T4.10 mitad 2: un re-análisis pedido por el dueño escribe su revisión y el " +
			"CRM se queda con la versión vieja del pedido, que es exactamente lo que D-044.19 " +
			"existe para impedir. La etapa lo grita con un log.Error y nadie mira el log hasta que " +
			"el integrador reclama.")
	}
	if !c.clausura {
		t.Error("la clausura de stages.ConEmpujeCRM no llama a intakeService.PushRevisionByID.\n" +
			"Una función que no empuja nada compila igual y deja el cable puesto de mentira.")
	}
}

// campoDe rinde `pkg.Fun` o `x.Sel` como texto plano. Es el gemelo del `campo` de
// pipeline_captacion_cableado_test.go; se duplica porque aquel es privado de su
// fichero y unificarlos ataría dos tests que hoy pueden cambiar por separado.
func campoDe(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		if base := campoDe(v.X); base != "" {
			return base + "." + v.Sel.Name
		}
		return v.Sel.Name
	}
	return ""
}
