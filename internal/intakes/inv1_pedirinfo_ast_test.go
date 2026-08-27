package intakes_test

// inv1_pedirinfo_ast_test.go — INV-1 SOBRE EL AST: NINGÚN CAMINO AUTOMÁTICO PREGUNTA
// (Plan 044 · Ola 4 · T4.4).
//
// `Service.RequestInfo` le manda un WhatsApp al cliente y mueve la solicitud a
// `needs_info`. INV-1 dice que nada le sale al cliente sin que lo mande el dueño, e
// INV-2 le prohíbe al LLM responder: un llamante nuevo desde el pipeline —«no entendí
// esta línea, pregúntale»— sería exactamente el sistema que estos dos invariantes
// existen para impedir, y encima el más plausible de escribir sin mala intención.
//
// Reusa entero el barrido de inv1_aprobar_ast_test.go (mismo paquete): los mismos
// directorios, el mismo control positivo y la misma guarda anti-hueco. Copiar el
// barrido habría dejado dos listas de directorios que divergen en cuanto alguien mueva
// un paquete, y la que no se actualice se quedaría vigilando una pared.

import (
	"strings"
	"testing"
)

// métodoPedirInfo es el nombre que se persigue. Si se renombra, el control positivo se
// pone rojo y este fichero es lo primero que hay que arreglar.
const métodoPedirInfo = "RequestInfo"

// TestINV1_SoloElPOSTDelDueñoPregunta exige que la ÚNICA llamada a
// Service.RequestInfo del repo sea la del handler HTTP.
func TestINV1_SoloElPOSTDelDueñoPregunta(t *testing.T) {
	// (1) Control POSITIVO, primero: si el barrido no encuentra la llamada que SÍ
	// existe, ninguna de sus ausencias significa nada.
	legítimas := llamadasA(t, puertaDelDueño, métodoPedirInfo)
	switch len(legítimas) {
	case 0:
		t.Fatalf("el barrido no encontró NI UNA llamada a %s en %s. O el método se renombró, o el "+
			"handler se mudó de paquete: arregla este candado ANTES de fiarte de su verde",
			métodoPedirInfo, puertaDelDueño)
	case 1: // lo esperado: una puerta, una llamada
	default:
		t.Fatalf("hay %d llamadas a %s en %s (%s). Una puerta, un camino: dos handlers son dos sitios "+
			"donde comprobar el scope, el gate y el estado de origen",
			len(legítimas), métodoPedirInfo, puertaDelDueño, strings.Join(legítimas, ", "))
	}

	// (2) La invariante. Ningún flujo automático puede llamarla.
	for _, dir := range flujosAutomáticos {
		if sitios := llamadasA(t, dir, métodoPedirInfo); len(sitios) > 0 {
			t.Errorf("🔴 %s llama a %s en %s. INV-1/INV-2: pedirle un dato al cliente es RESPONDERLE al "+
				"cliente, y eso no puede ocurrir por un mensaje entrante, por un tick ni por lo que el "+
				"LLM no haya entendido. Las `suggested_questions` las prepara el sistema y las manda el "+
				"DUEÑO, con su POST y después de editarlas (D-044.49 §2)",
				dir, métodoPedirInfo, strings.Join(sitios, ", "))
		}
	}
}
