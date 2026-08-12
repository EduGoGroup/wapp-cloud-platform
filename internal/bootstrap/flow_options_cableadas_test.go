package bootstrap

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestFlowRuntimeOptionsCableadas fija, contra el ÁRBOL DE SINTAXIS de bootstrap.go, que las
// Options del motor de flujos que no tienen otra red están efectivamente cableadas.
//
// POR QUÉ UN TEST DE AST Y NO UNO NORMAL: `flowruntime.New(...)` se invoca inline dentro del
// arranque, con dependencias que exigen Postgres, R2 y el gateway gRPC vivos — no hay costura
// que permita construir las Options y mirarlas. Y la comprobación importa lo suficiente como
// para no dejarla sin red: las Options son variádicas, así que **omitir una compila, pasa el
// vet, pasa el lint y deja el paquete entero en verde**. Ese es exactamente el modo en que
// falló `WithOpeningBuilder`.
//
// LA HISTORIA, para que no se repita: `WithOpeningBuilder` llegó CONSTRUIDA y PROBADA con el
// Plan 043 (T3.8) y se quedó SIN ENCHUFAR hasta el 2026-08-12. Con `opening` a nil, la rama
// Fallback de `handleTrigger` cae SIEMPRE a `startPlainFlow` —el camino que la enmienda E-9 del
// ADR-0029 vino a reemplazar—, que arranca el flujo del tenant SIN evento padre. En un tenant
// cuyo flujo lleva un nodo `cart`, eso es una comanda perdida en silencio contra el NOT NULL de
// `intakes.event_id` (migración 0054). Se midió DOS veces en UAT el mismo día (hallazgos #001 y
// #003 de docs/runbooks/bitacora-errores-uat.md) antes de que nadie mirara el cableado, porque
// no había una sola señal roja que apuntara aquí.
//
// Este test es esa señal. Si mañana alguien reordena el arranque y una de estas Options se cae
// por el camino, el rojo sale aquí y no en la bandeja de solicitudes de un cliente.
func TestFlowRuntimeOptionsCableadas(t *testing.T) {
	// Cada entrada es una Option cuya ausencia NO rompe nada visible: el motor arranca, los
	// tests pasan y la capacidad simplemente no existe en producción.
	requeridas := map[string]string{
		"WithOpeningBuilder": "sin ella el fallback cae a startPlainFlow (arranque SIN evento): " +
			"comanda perdida en silencio si el flujo del tenant lleva un nodo cart (E-9, hallazgos #001/#003)",
		"WithDispatcher": "sin él no hay menú del despachador: la elección explícita de tipo — la " +
			"TERCERA puerta del nacimiento del evento (T2.5/REQ-01b) — deja de existir",
		"WithFlowForKind": "sin él el salto por tipo no sabe qué flujo arrancar para un event_kind",
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "bootstrap.go", nil, 0)
	if err != nil {
		t.Fatalf("parsear bootstrap.go: %v", err)
	}

	vistas := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		// Interesa la forma `flowruntime.WithX(...)`: un selector sobre el paquete.
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "flowruntime" {
			return true
		}
		vistas[sel.Sel.Name] = true
		return true
	})

	for opcion, porQue := range requeridas {
		if !vistas[opcion] {
			t.Errorf("flowruntime.%s NO está cableada en bootstrap.go — %s", opcion, porQue)
		}
	}
}
