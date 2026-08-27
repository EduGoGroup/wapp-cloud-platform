package intakes_test

// inv1_aprobar_ast_test.go — INV-1 SOBRE EL AST: NINGÚN CAMINO AUTOMÁTICO APRUEBA
// (Plan 044 · Ola 4 · T4.3, criterio explícito de la tarea).
//
// INV-1 dice que el LLM jamás le responde al cliente y que nada se le manda sin que
// el dueño lo mande. `Service.Approve` es el punto exacto donde eso se puede romper
// sin ruido: es la única función del repo que ESCRIBE un estado, MANDA un WhatsApp y
// EMPUJA al CRM en un solo acto. Un llamante nuevo desde el motor de flujos, desde el
// pipeline o desde un barrido convertiría la aprobación en algo que ocurre solo.
//
// Un test de conducta no puede cubrir esto: comprobaría que el camino que YO llamo
// hace lo correcto, no que nadie más lo llame. La pregunta es sobre el CÓDIGO —«¿quién
// invoca esto?»— y se contesta mirando el código.
//
// 🚨 LA GUARDA ANTI-HUECO ES LA MITAD DEL TEST. Un barrido estructural que no
// encuentra nada pasa siempre, y ese es su modo de fallo natural: se renombra el
// método, se muda el handler de paquete, y el candado sigue verde vigilando una
// pared. Por eso este test exige TRES cosas antes de fiarse de su propio verde:
// que cada directorio barrido tenga ficheros de producción que leer, que el control
// POSITIVO encuentre la llamada legítima, y que la encuentre UNA sola vez.
//
// Molde: internal/integrations/crmpush/contrato_ast_test.go (T4.10 mitad 2).

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// métodoAprobar es el nombre que se persigue. Si se renombra, el control positivo de
// abajo se pone rojo y este fichero es el primero que hay que arreglar — que es
// exactamente lo que se quiere: la invariante no puede quedarse sin vigilancia por un
// refactor de nombres.
const métodoAprobar = "Approve"

// puertaDelDueño es el ÚNICO directorio desde el que se puede aprobar: el transporte
// HTTP, detrás del scope `intakes.write` y del gate `cart_basic`. Se barre como
// CONTROL POSITIVO — si aquí no aparece la llamada, el barrido no está mirando lo que
// cree y su verde no vale nada.
var puertaDelDueño = filepath.Join("..", "publicapi")

// flujosAutomáticos son los directorios del código que corre SIN un POST del dueño y
// que, por tanto, no puede aprobar nada:
//
//   - flujos/runtime — el motor: lo que dispara un mensaje entrante del cliente, los
//     sinks de efectos y el agregador del 044. Es el sitio donde un «ya que estamos,
//     ciérralo» se colaría con la mejor de las intenciones.
//   - flujos/modules/cart — el proyector del carrito, que YA cierra solicitudes por su
//     cuenta (a `confirmed`) sin pasar por aquí: es el vecino más cercano al error.
//   - intake/pipeline y intake/stages — el pipeline LLM. INV-2 le prohíbe responderle
//     al cliente; aprobar es responderle al cliente con una cotización.
//   - intake — la máquina de los jobs (worker, reintentos, despertar).
//
// Si uno de ellos deja de existir o de tener ficheros, el barrido CORTA en vez de dar
// verde: significa que el código se mudó y este candado se quedó mirando a la pared.
var flujosAutomáticos = []string{
	filepath.Join("..", "flujos", "runtime"),
	filepath.Join("..", "flujos", "modules", "cart"),
	filepath.Join("..", "intake", "pipeline"),
	filepath.Join("..", "intake", "stages"),
	filepath.Join("..", "intake"),
}

// TestINV1_SoloElPOSTDelDueñoAprueba barre el código de producción y exige que la
// ÚNICA llamada a Service.Approve del repo sea la del handler HTTP.
func TestINV1_SoloElPOSTDelDueñoAprueba(t *testing.T) {
	// (1) Control POSITIVO. Va primero a propósito: si el barrido no sabe encontrar
	// la llamada que SÍ existe, ninguna de sus ausencias significa nada.
	legítimas := llamadasA(t, puertaDelDueño, métodoAprobar)
	switch len(legítimas) {
	case 0:
		t.Fatalf("el barrido no encontró NI UNA llamada a %s en %s. O el método se renombró, o el "+
			"handler se mudó de paquete: arregla este candado ANTES de fiarte de su verde, porque "+
			"tal como está no está mirando nada", métodoAprobar, puertaDelDueño)
	case 1: // lo esperado: una puerta, una llamada
	default:
		t.Fatalf("hay %d llamadas a %s en %s (%s). INV-1 se sostiene sobre que la aprobación tenga "+
			"UNA sola puerta: dos handlers son dos sitios donde comprobar el scope, el gate y las "+
			"precondiciones, y el hallazgo #24 de este plan ya se pagó una vez por duplicar una puerta",
			len(legítimas), métodoAprobar, puertaDelDueño, strings.Join(legítimas, ", "))
	}

	// (2) La invariante. Ningún flujo automático puede llamarla.
	for _, dir := range flujosAutomáticos {
		if sitios := llamadasA(t, dir, métodoAprobar); len(sitios) > 0 {
			t.Errorf("🔴 %s llama a %s en %s. INV-1: ningún camino de código llega a la aprobación sin "+
				"el POST del dueño — aprobar MANDA UN WHATSAPP con precio al cliente, deja la solicitud "+
				"en `confirmed` y empuja al CRM. Nada de eso puede ocurrir por un mensaje entrante, por "+
				"un tick ni por lo que entienda el LLM (INV-2). Si de verdad hace falta un camino nuevo, "+
				"la decisión es de producto y va escrita antes que el código",
				dir, métodoAprobar, strings.Join(sitios, ", "))
		}
	}
}

// llamadasA parsea los ficheros de PRODUCCIÓN del directorio y devuelve los
// `fichero:línea` donde se invoca el método dado sobre algo (`x.Approve(…)`).
//
// Se excluyen los _test.go: un doble de test puede tener un método con ese nombre y
// llamarlo, y eso no es un camino automático. Se leen los ficheros a mano porque
// parser.ParseDir está deprecado desde Go 1.22.
//
// NO baja a subdirectorios, y por eso flujosAutomáticos los enumera uno a uno: un
// barrido recursivo silencioso convertiría «este paquete ya no existe» en «no
// encontré nada», que es justo el verde falso que este fichero existe para impedir.
func llamadasA(t *testing.T, dir, método string) []string {
	t.Helper()
	entradas, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("no se pudo listar %s: %v. Si el paquete se movió, este candado se queda vigilando "+
			"un sitio que no existe: arregla la lista de directorios", dir, err)
	}

	var out []string
	leídos := 0
	for _, e := range entradas {
		nombre := e.Name()
		if e.IsDir() || !strings.HasSuffix(nombre, ".go") || strings.HasSuffix(nombre, "_test.go") {
			continue
		}
		leídos++
		ruta := filepath.Join(dir, nombre)
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, ruta, nil, 0)
		if perr != nil {
			t.Fatalf("no se pudo parsear %s: %v", ruta, perr)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != método {
				return true
			}
			p := fset.Position(call.Pos())
			out = append(out, ruta+":"+strconv.Itoa(p.Line))
			return true
		})
	}
	if leídos == 0 {
		t.Fatalf("el barrido no leyó ni un fichero de producción de %s: no está mirando nada, así que "+
			"su silencio no prueba nada", dir)
	}
	return out
}
