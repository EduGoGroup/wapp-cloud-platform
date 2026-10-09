package intakes_test

// inv1_aprobar_test.go — INV-1 SOBRE EL AST: NINGÚN CAMINO AUTOMÁTICO APRUEBA NI PREGUNTA.
//
// Porta internal/intakes/inv1_aprobar_ast_test.go e internal/intakes/inv1_pedirinfo_ast_test.go
// @ 5fd533e (candados de invariante, `05` §3.2: el AST está permitido aquí, E-7).
//
// INV-1 dice que el LLM jamás le responde al cliente y que nada se le manda sin que
// el dueño lo mande. `Service.Approve` es el punto exacto donde eso se puede romper
// sin ruido: es la única función del repo que ESCRIBE un estado, MANDA un WhatsApp y
// EMPUJA al CRM en un solo acto. `Service.RequestInfo` le manda un WhatsApp al cliente
// y mueve la solicitud a `needs_info`. Un llamante nuevo desde el motor de flujos,
// desde el pipeline o desde un barrido convertiría cualquiera de las dos en algo que
// ocurre solo.
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
// Nació tras `//go:build pendiente` (T6.9): el control positivo exige exactamente una
// llamada en `internal/apipublica`, y la cara HTTP nueva no servía todavía la aprobación
// ni la pregunta. Perdió la etiqueta en T6.25 (conmutar(solicitudes)), cuando G5 y G6
// pasaron a servirse por ella.
//
// Las listas de directorios son las de la fase F6 y se re-tocan en F7 y F8 (D-F6-2),
// con la guarda anti-hueco intacta.

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

// approveMethod y requestInfoMethod son los nombres que se persiguen (eran
// métodoAprobar y métodoPedirInfo en el viejo). Si se renombran, el control positivo
// se pone rojo y este fichero es el primero que hay que arreglar — que es exactamente
// lo que se quiere: la invariante no puede quedarse sin vigilancia por un refactor de
// nombres.
const (
	approveMethod     = "Approve"
	requestInfoMethod = "RequestInfo"
)

// ownersDoor es el ÚNICO directorio desde el que se puede aprobar o preguntar: la cara
// HTTP nueva (`internal/apipublica`, D-10), detrás del scope `intakes.write` y del gate
// `cart_basic`. Era puertaDelDueño (`../publicapi`) en el viejo. Se barre como CONTROL
// POSITIVO — si aquí no aparece la llamada, el barrido no está mirando lo que cree y
// su verde no vale nada.
var ownersDoor = filepath.Join("..", "..", "..", "apipublica")

// automaticFlows (era flujosAutomáticos) son los directorios del código que corre SIN
// un POST del dueño y que, por tanto, no puede aprobar ni preguntar nada. Desde F7
// (conmutar(captacion), T7.25) los de la captación son los NUEVOS de
// internal/modulos/captacion, porque son los que corren en el arranque nuevo; los del
// motor siguen siendo los paquetes VIEJOS hasta F8:
//
//   - flujos/runtime — el motor: lo que dispara un mensaje entrante del cliente, los
//     sinks de efectos y el agregador del 044. Es el sitio donde un «ya que estamos,
//     ciérralo» se colaría con la mejor de las intenciones.
//   - flujos/modules/cart — el proyector del carrito, que YA cierra solicitudes por su
//     cuenta (a `confirmed`) sin pasar por aquí: es el vecino más cercano al error.
//   - captacion/pipeline y captacion/stages — el pipeline LLM. INV-2 le prohíbe
//     responderle al cliente; aprobar es responderle al cliente con una cotización, y
//     preguntar («no entendí esta línea, pregúntale») es el llamante más plausible de
//     escribir sin mala intención.
//   - captacion/intake — la máquina de los jobs (worker, reintentos, despertar).
//   - captacion/reanalisis — la puerta del re-análisis. La abre el dueño, pero lo que
//     abre es un job que corre solo: «ya que lo regeneré, apruébalo» no puede salir
//     de aquí.
//   - captacion/intakeahead — el pool que pide las clasificaciones por adelantado,
//     fuera del camino del mensaje y sin nadie delante.
//   - «.» — EL PROPIO DOMINIO. `Approve` se DEFINE aquí, y aquí vive la primera
//     evaluación que ocurre POR EL PASO DEL TIEMPO —el plazo del presupuesto—, colgada
//     de una LECTURA. Un «ya que ha pasado el plazo, ciérralo» escrito aquí aprobaría
//     presupuestos con solo abrir la bandeja. Definir el método NO lo dispara:
//     callsTo persigue CallExpr sobre un selector (`x.Approve(…)`), y una declaración
//     no lo es.
//
// F7 sustituyó los tres de `intake` viejo por `../../captacion/{intake,pipeline,stages}`
// y añadió `../../captacion/{reanalisis,intakeahead}` (F7 `diseno.md` §6). F8 sustituirá
// los dos de `flujos` por `../../conversacion/{runtime,modules/cart}`.
//
// Si uno de ellos deja de existir o de tener ficheros, el barrido CORTA en vez de dar
// verde: significa que el código se mudó y este candado se quedó mirando a la pared.
var automaticFlows = []string{
	filepath.Join("..", "..", "..", "flujos", "runtime"),
	filepath.Join("..", "..", "..", "flujos", "modules", "cart"),
	filepath.Join("..", "..", "captacion", "intake"),
	filepath.Join("..", "..", "captacion", "pipeline"),
	filepath.Join("..", "..", "captacion", "stages"),
	filepath.Join("..", "..", "captacion", "reanalisis"),
	filepath.Join("..", "..", "captacion", "intakeahead"),
	".",
}

// TestINV1_OnlyTheOwnersPOSTApproves (era TestINV1_SoloElPOSTDelDueñoAprueba) barre el
// código de producción y exige que la ÚNICA llamada a Service.Approve sea la del
// handler HTTP.
func TestINV1_OnlyTheOwnersPOSTApproves(t *testing.T) {
	// (1) Control POSITIVO. Va primero a propósito: si el barrido no sabe encontrar
	// la llamada que SÍ existe, ninguna de sus ausencias significa nada.
	legitimate := callsTo(t, ownersDoor, approveMethod)
	switch len(legitimate) {
	case 0:
		t.Fatalf("el barrido no encontró NI UNA llamada a %s en %s. O el método se renombró, o el "+
			"handler se mudó de paquete: arregla este candado ANTES de fiarte de su verde, porque "+
			"tal como está no está mirando nada", approveMethod, ownersDoor)
	case 1: // lo esperado: una puerta, una llamada
	default:
		t.Fatalf("hay %d llamadas a %s en %s (%s). INV-1 se sostiene sobre que la aprobación tenga "+
			"UNA sola puerta: dos handlers son dos sitios donde comprobar el scope, el gate y las "+
			"precondiciones, y el hallazgo #24 de este plan ya se pagó una vez por duplicar una puerta",
			len(legitimate), approveMethod, ownersDoor, strings.Join(legitimate, ", "))
	}

	// (2) La invariante. Ningún flujo automático puede llamarla.
	for _, dir := range automaticFlows {
		if sites := callsTo(t, dir, approveMethod); len(sites) > 0 {
			t.Errorf("🔴 %s llama a %s en %s. INV-1: ningún camino de código llega a la aprobación sin "+
				"el POST del dueño — aprobar MANDA UN WHATSAPP con precio al cliente, deja la solicitud "+
				"en `confirmed` y empuja al CRM. Nada de eso puede ocurrir por un mensaje entrante, por "+
				"un tick ni por lo que entienda el LLM (INV-2). Si de verdad hace falta un camino nuevo, "+
				"la decisión es de producto y va escrita antes que el código",
				dir, approveMethod, strings.Join(sites, ", "))
		}
	}
}

// TestINV1_OnlyTheOwnersPOSTAsks (era TestINV1_SoloElPOSTDelDueñoPregunta) exige que la
// ÚNICA llamada a Service.RequestInfo sea la del handler HTTP. Reusa entero el barrido
// de arriba: los mismos directorios, el mismo control positivo y la misma guarda
// anti-hueco. Copiar el barrido dejaría dos listas de directorios que divergen en
// cuanto alguien mueva un paquete, y la que no se actualice se quedaría vigilando una
// pared.
func TestINV1_OnlyTheOwnersPOSTAsks(t *testing.T) {
	// (1) Control POSITIVO, primero: si el barrido no encuentra la llamada que SÍ
	// existe, ninguna de sus ausencias significa nada.
	legitimate := callsTo(t, ownersDoor, requestInfoMethod)
	switch len(legitimate) {
	case 0:
		t.Fatalf("el barrido no encontró NI UNA llamada a %s en %s. O el método se renombró, o el "+
			"handler se mudó de paquete: arregla este candado ANTES de fiarte de su verde",
			requestInfoMethod, ownersDoor)
	case 1: // lo esperado: una puerta, una llamada
	default:
		t.Fatalf("hay %d llamadas a %s en %s (%s). Una puerta, un camino: dos handlers son dos sitios "+
			"donde comprobar el scope, el gate y el estado de origen",
			len(legitimate), requestInfoMethod, ownersDoor, strings.Join(legitimate, ", "))
	}

	// (2) La invariante. Ningún flujo automático puede llamarla.
	for _, dir := range automaticFlows {
		if sites := callsTo(t, dir, requestInfoMethod); len(sites) > 0 {
			t.Errorf("🔴 %s llama a %s en %s. INV-1/INV-2: pedirle un dato al cliente es RESPONDERLE al "+
				"cliente, y eso no puede ocurrir por un mensaje entrante, por un tick ni por lo que el "+
				"LLM no haya entendido. Las `suggested_questions` las prepara el sistema y las manda el "+
				"DUEÑO, con su POST y después de editarlas (D-044.49 §2)",
				dir, requestInfoMethod, strings.Join(sites, ", "))
		}
	}
}

// callsTo (era llamadasA) parsea los ficheros de PRODUCCIÓN del directorio y devuelve
// los `fichero:línea` donde se invoca el método dado sobre algo (`x.Approve(…)`).
//
// Se excluyen los _test.go: un doble de test puede tener un método con ese nombre y
// llamarlo, y eso no es un camino automático. Se leen los ficheros a mano porque
// parser.ParseDir está deprecado desde Go 1.22.
//
// NO baja a subdirectorios, y por eso automaticFlows los enumera uno a uno: un
// barrido recursivo silencioso convertiría «este paquete ya no existe» en «no
// encontré nada», que es justo el verde falso que este fichero existe para impedir.
func callsTo(t *testing.T, dir, method string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("no se pudo listar %s: %v. Si el paquete se movió, este candado se queda vigilando "+
			"un sitio que no existe: arregla la lista de directorios", dir, err)
	}

	var out []string
	read := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		read++
		path := filepath.Join(dir, name)
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			t.Fatalf("no se pudo parsear %s: %v", path, perr)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != method {
				return true
			}
			p := fset.Position(call.Pos())
			out = append(out, path+":"+strconv.Itoa(p.Line))
			return true
		})
	}
	if read == 0 {
		t.Fatalf("el barrido no leyó ni un fichero de producción de %s: no está mirando nada, así que "+
			"su silencio no prueba nada", dir)
	}
	return out
}
