// Copia de internal/bootstrap/arranque/send_budget_cableado_test.go @ 80807ba (F0 · 05 §6). Desde
// F3 (T3.28, conmutar(edge)) D1 la sirve la cara nueva: lo que se vigila es el SendBudget de
// apipublica.MessagesDeps, derivado con apipublica.SendBudgetFrom.
package arranque

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
)

// TestSendBudgetCableado fija, contra el ÁRBOL DE SINTAXIS de http.go, que el
// presupuesto de la petición de envío se cablea de verdad en las deps de D1 de la cara
// NUEVA (Plan 050 · Ola 5 · T5.4, REQ-050.19; desde F3, apipublica.MessagesDeps).
//
// POR QUÉ UN TEST DE AST, igual que TestFlowRuntimeOptionsCableadas y por el mismo
// motivo: MessagesDeps.SendBudget es un campo con cero-valor útil —<=0 significa «sin plazo»,
// el comportamiento anterior a esta tarea—, así que **olvidar la asignación compila,
// pasa el vet, pasa el lint y deja TODOS los tests en verde**. Los tests de
// internal/apipublica cablean el presupuesto ellos mismos para poder medirlo, de modo
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
		// ruta y no campo: el destino tiene dos niveles (edge.messages.SendBudget).
		if ruta(asig.Lhs[0]) != "edge.messages.SendBudget" {
			return true
		}
		// No basta con que se asigne ALGO: tiene que ser la derivación. Una constante
		// suelta aquí reintroduciría el número que hay que mantener a mano —justo el
		// mecanismo por el que la aritmética de config.go se desincronizó— y este test
		// pasaría sin decir nada.
		llamada, ok := asig.Rhs[0].(*ast.CallExpr)
		if !ok || campo(llamada.Fun) != "apipublica.SendBudgetFrom" {
			t.Fatalf("edge.messages.SendBudget se asigna con algo que NO es apipublica.SendBudgetFrom: "+
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
		t.Fatal("internal/arranque/http.go NO cablea edge.messages.SendBudget.\n" +
			"Sin esa línea, MessagesDeps.SendBudget queda en cero, sendCtx no pone plazo y el " +
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
	presupuesto := apipublica.SendBudgetFrom(writeTimeout)
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
//
// Rinde UN SOLO nivel a propósito: varios tests comparan contra su resultado y
// ensancharlo cambiaría lo que ellos afirman. Para selectores anidados está `ruta`.
//
// El receptor del contenedor del arranque no cuenta como nivel: `c.gw.OnWarmup` se lee
// como `gw.OnWarmup`, que es el cable que estos tests exigen desde antes de que el
// arranque se partiera en fases. Ver sinContenedor.
func campo(e ast.Expr) string {
	s := ruta(e)
	if strings.Count(s, ".") > 1 {
		return ""
	}
	return s
}
