package bootstrap

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
)

// TestSendBudgetCableado fija, contra el ÁRBOL DE SINTAXIS de http.go, que el
// presupuesto de la petición de envío se cablea de verdad en las Deps de la API
// pública (Plan 050 · Ola 5 · T5.4, REQ-050.19).
//
// POR QUÉ UN TEST DE AST, igual que TestFlowRuntimeOptionsCableadas y por el mismo
// motivo: Deps.SendBudget es un campo con cero-valor útil —<=0 significa «sin plazo»,
// el comportamiento anterior a esta tarea—, así que **olvidar la asignación compila,
// pasa el vet, pasa el lint y deja TODOS los tests en verde**. Los tests de
// internal/publicapi cablean el presupuesto ellos mismos para poder medirlo, de modo
// que no pueden notar que producción no lo cablea. Sin esta red, el arreglo entero
// quedaría inerte en el binario real y el único síntoma volvería a ser un POST que
// cuelga 88 s contra un Edge saturado.
//
// Es exactamente el modo en que falló WithOpeningBuilder: construido, probado y sin
// enchufar durante meses.
func TestSendBudgetCableado(t *testing.T) {
	fset := token.NewFileSet()
	archivo, err := parser.ParseFile(fset, "http.go", nil, 0)
	if err != nil {
		t.Fatalf("parseando http.go: %v", err)
	}

	var asignado bool
	ast.Inspect(archivo, func(n ast.Node) bool {
		asig, ok := n.(*ast.AssignStmt)
		if !ok || len(asig.Lhs) != 1 || len(asig.Rhs) != 1 {
			return true
		}
		if campo(asig.Lhs[0]) != "pub.SendBudget" {
			return true
		}
		// No basta con que se asigne ALGO: tiene que ser la derivación. Una constante
		// suelta aquí reintroduciría el número que hay que mantener a mano —justo el
		// mecanismo por el que la aritmética de config.go se desincronizó— y este test
		// pasaría sin decir nada.
		llamada, ok := asig.Rhs[0].(*ast.CallExpr)
		if !ok || campo(llamada.Fun) != "publicapi.SendBudgetFrom" {
			t.Fatalf("pub.SendBudget se asigna con algo que NO es publicapi.SendBudgetFrom: "+
				"el presupuesto tiene que DERIVARSE del writeTimeout, no ser un número suelto "+
				"(%s)", fset.Position(asig.Pos()))
		}
		if len(llamada.Args) != 1 || campo(llamada.Args[0]) != "writeTimeout" {
			t.Fatalf("SendBudgetFrom no recibe writeTimeout: si se deriva de otra cosa, mover "+
				"el WriteTimeout del http.Server deja de arrastrar el presupuesto y la "+
				"aritmética vuelve a poder mentir (%s)", fset.Position(llamada.Pos()))
		}
		asignado = true
		return false
	})

	if !asignado {
		t.Fatal("internal/bootstrap/http.go NO cablea pub.SendBudget.\n" +
			"Sin esa línea, Deps.SendBudget queda en cero, sendCtx no pone plazo y el " +
			"handler de envío vuelve a poder pasarse del WriteTimeout: el cliente se queda " +
			"con la conexión cerrada y sin cuerpo (incidente del 2026-08-06). Todos los " +
			"demás tests siguen verdes, por eso existe este.")
	}
}

// TestSendBudgetDejaMargenConElWriteTimeoutReal comprueba la RELACIÓN con el valor que
// de verdad corre en producción, que este paquete es el único que puede leer
// (writeTimeout es privado de bootstrap). No se compara contra ningún número escrito a
// mano: se afirma que hay presupuesto y que cabe por debajo del deadline de escritura,
// que son las dos propiedades de las que depende que exista respuesta.
func TestSendBudgetDejaMargenConElWriteTimeoutReal(t *testing.T) {
	presupuesto := publicapi.SendBudgetFrom(writeTimeout)
	if presupuesto <= 0 {
		t.Fatalf("con writeTimeout=%v no hay presupuesto (%v): el handler de envío se "+
			"quedaría sin plazo", writeTimeout, presupuesto)
	}
	if presupuesto >= writeTimeout {
		t.Fatalf("presupuesto %v >= writeTimeout %v: el handler se rendiría con el deadline "+
			"de escritura ya vencido y el cliente seguiría sin recibir nada",
			presupuesto, writeTimeout)
	}
	t.Logf("writeTimeout=%v ⇒ presupuesto=%v (margen de escritura %v)",
		writeTimeout, presupuesto, writeTimeout-presupuesto)
	// El margen tiene que ser holgado para escribir ~350 B en una conexión abierta y a
	// la vez estrecho, porque es EXACTAMENTE la franja de envíos que cambian de
	// desenlace: los que tardan entre el presupuesto y el writeTimeout hoy alcanzaban a
	// responder y a partir de ahora se cortan.
	if margen := writeTimeout - presupuesto; margen > 2*time.Second {
		t.Fatalf("el margen es de %v: la franja de envíos que pasan a cortarse mide lo "+
			"mismo, y ensancharla sin motivo cambia el desenlace de envíos que iban bien",
			margen)
	}
}

// campo devuelve la representación textual de un selector o identificador ("pub.SendBudget",
// "writeTimeout"). Cualquier otra cosa devuelve "".
func campo(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		if base, ok := v.X.(*ast.Ident); ok {
			return base.Name + "." + v.Sel.Name
		}
	}
	return ""
}
