//go:build integracion

package procesos

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
)

// TestArnes_EdgeInferenceLeaseGate, contra el servidor real: el Edge de prueba obedece al lease
// también en la inferencia, provocada por el calentamiento que sigue a publicar el catálogo.
// Sale de edge_falso_test.go (D-F9-11: solo se movieron declaraciones).

// Los dos mensajes con los que el servidor dice, en su log de nivel debug, cómo acabó un
// calentamiento de la caché de prefijo de un Edge (internal/intakeahead/calentamiento.go, calentar):
// el Edge lo sirvió, o no; en el segundo caso el campo «error» trae el motivo del gateway.
const (
	edgeLogWarmupServed    = "calentamiento: emitido contra el Edge"
	edgeLogWarmupNotServed = "calentamiento: no se emitió"
)

// edgeIntentsCatalog arma un catálogo de intenciones mínimo y válido para PUT /api/v1/intents (una
// intención con un ejemplo), con la versión dada. Dos versiones distintas dan dos catálogos
// distintos: el servidor empuja un ConfigUpdate nuevo y pide otro calentamiento.
func edgeIntentsCatalog(version string) map[string]any {
	return map[string]any{
		"version": version,
		"intents": []map[string]any{{
			"name":        "solicitud_pedido",
			"descripcion": "El cliente pide productos o un presupuesto",
			"ejemplos":    []map[string]any{{"mensaje": "quiero 3 cajas de tornillos"}},
		}},
	}
}

// edgePublishIntents publica el catálogo de la versión dada por la puerta HTTP de la administradora
// (PUT /api/v1/intents) y espera a que el Edge reciba, por su efecto, una InferenceRequest más de
// las want-1 que ya tenía: el servidor persiste el catálogo, empuja el ConfigUpdate «intents» a las
// sesiones vivas y pide UN calentamiento por Edge. Devuelve esa petición. Falla (t.Fatalf) si el PUT
// no da 200 o si la petición no llega.
func edgePublishIntents(t *testing.T, esc edgeEscenario, e *edge, version string, want int) *cloudlinkv1.InferenceRequest {
	t.Helper()
	r := esc.S.Publica(esc.TokenAdmin).Put(t, "/api/v1/intents", edgeIntentsCatalog(version))
	if r.Codigo != http.StatusOK {
		t.Fatalf("publicar el catálogo %q: HTTP %d, quería 200\ncuerpo: %s", version, r.Codigo, recortar(r.Cuerpo))
	}
	edgeEsperar(t, edgeTopeFila, fmt.Sprintf("la InferenceRequest número %d en el Edge", want), func() bool { return len(e.Inferencias()) >= want })
	return e.Inferencias()[want-1]
}

// edgeWarmupLogLines devuelve las líneas del log del servidor con el mensaje msg (uno de los dos
// de arriba) cuya sesión es la del Edge, en orden.
func edgeWarmupLogLines(s *servidor, e *edge, msg string) []map[string]any {
	var lines []map[string]any
	for _, l := range s.LineasLog() {
		if l["msg"] == msg && l["session_id"] == e.SessionID {
			lines = append(lines, l)
		}
	}
	return lines
}

// edgeCheckWarmupRefused espera a que el servidor haya registrado want calentamientos NO servidos
// por el Edge y comprueba que el último lo fue por el lease: el campo «error» de la línea trae el
// motivo `lease_invalid` del gateway y el nombre del error del contrato,
// INFERENCE_ERROR_LEASE_INVALID. Recibe la etapa, para los mensajes. Falla el test (t.Fatalf) si las
// líneas no llegan, y con t.Errorf si el motivo es otro.
func edgeCheckWarmupRefused(t *testing.T, esc edgeEscenario, e *edge, stage string, want int) {
	t.Helper()
	var lines []map[string]any
	edgeEsperar(t, edgeTopeFila, fmt.Sprintf("%s: %d líneas %q en el log del servidor", stage, want, edgeLogWarmupNotServed), func() bool {
		lines = edgeWarmupLogLines(esc.S, e, edgeLogWarmupNotServed)
		return len(lines) >= want
	})
	reason := fmt.Sprint(lines[want-1]["error"])
	if !strings.Contains(reason, "lease_invalid") ||
		!strings.Contains(reason, cloudlinkv1.InferenceError_INFERENCE_ERROR_LEASE_INVALID.String()) {
		t.Errorf("%s: el servidor dice que el calentamiento no salió por %q; quería el motivo lease_invalid (INFERENCE_ERROR_LEASE_INVALID)", stage, reason)
	}
}

// TestArnes_EdgeInferenceLeaseGate prueba contra el servidor real que el Edge de prueba obedece al
// lease también en la INFERENCIA, como el Edge real (ADR-0007: servir inferencia es operar). La
// inferencia se provoca por la única puerta que hoy no exige montar la canalización LLM: publicar
// el catálogo de intenciones, tras lo cual el servidor pide al Edge un calentamiento
// (InferenceRequest con warmup=true).
//
//  1. Con lease vigente (el control: sin él, lo de abajo pasaría en falso): el Edge recibe el
//     ConfigUpdate «intents» y el calentamiento, lo sirve, y el servidor lo registra como emitido.
//  2. Tras POST /admin/leases/revoke, otro catálogo provoca otro calentamiento: el Edge lo recibe y,
//     agotada su gracia, contesta INFERENCE_ERROR_LEASE_INVALID; el servidor registra que no salió,
//     con el motivo lease_invalid.
//  3. Reconectar no lo arregla: el servidor vuelve a pedir el calentamiento al Edge revocado, y
//     vuelve a recibir lease_invalid.
//
// En ningún momento el servidor da un calentamiento por servido después de la revocación. Necesita
// Docker.
func TestArnes_EdgeInferenceLeaseGate(t *testing.T) {
	t.Parallel()
	esc := edgeEscenarioNuevo(t, "edge_inference_gate", "edge-inference-gate", true)
	e := enrolar(t, esc.S, edgeEmitirCodigo(t, esc.S, esc.TokenStaff, esc.Tenant))
	e.conectar(t)
	// El latido de conectar ya provocó la renovación del lease y el aviso de calentamiento del
	// arranque, que sin catálogo publicado termina sin pedir nada: se espera a esa renovación para
	// que el calentamiento del primer PUT no coincida con aquel (el servidor lleva uno en vuelo por
	// Edge y descartaría el segundo).
	e.esperarLeases(t, 2, edgeTopeFila)
	if n := len(e.Inferencias()); n != 0 {
		t.Fatalf("sin catálogo publicado el Edge ya recibió %d peticiones de inferencia", n)
	}

	req := edgePublishIntents(t, esc, e, "1", 1)
	if !req.GetWarmup() || req.GetPrompt() == "" {
		t.Errorf("la petición tras publicar el catálogo: warmup=%v, prompt de %d bytes; quería un calentamiento con prompt", req.GetWarmup(), len(req.GetPrompt()))
	}
	if cfg := e.esperarConfig(t, "intents", edgeTopeFila); cfg.Sesion != e.SessionID || !json.Valid(cfg.Payload) {
		t.Errorf("el ConfigUpdate intents = sesión %q, payload válido %v", cfg.Sesion, json.Valid(cfg.Payload))
	}
	edgeEsperar(t, edgeTopeFila, "el calentamiento servido, en el log del servidor", func() bool {
		return len(edgeWarmupLogLines(esc.S, e, edgeLogWarmupServed)) == 1
	})
	if lines := edgeWarmupLogLines(esc.S, e, edgeLogWarmupNotServed); len(lines) != 0 {
		t.Fatalf("con lease vigente el servidor registró calentamientos no servidos: %v", lines)
	}

	r := esc.S.Admin(esc.TokenAdmin).Post(t, "/admin/leases/revoke", map[string]string{"edge_id": e.EdgeID})
	if r.Codigo != http.StatusNoContent {
		t.Fatalf("revocar el lease: HTTP %d, quería 204\ncuerpo: %s", r.Codigo, recortar(r.Cuerpo))
	}
	edgeEsperar(t, edgeTopeFila, "que el Edge quede revocado", e.revocado)

	if req := edgePublishIntents(t, esc, e, "2", 2); !req.GetWarmup() {
		t.Errorf("la petición tras publicar el segundo catálogo no es un calentamiento: %+v", req)
	}
	edgeCheckWarmupRefused(t, esc, e, "tras revocar", 1)

	e.conectar(t) // no falla: una revocación aceptada por el Validator no es un rechazo
	if e.puedeOperar() || !e.revocado() {
		t.Errorf("tras reconectar el Edge revocado: puedeOperar=%v revocado=%v; quería falso y verdadero", e.puedeOperar(), e.revocado())
	}
	edgeEsperar(t, edgeTopeFila, "el calentamiento del arranque en el Edge reconectado", func() bool { return len(e.Inferencias()) >= 3 })
	edgeCheckWarmupRefused(t, esc, e, "tras reconectar revocado", 2)

	if lines := edgeWarmupLogLines(esc.S, e, edgeLogWarmupServed); len(lines) != 1 {
		t.Errorf("el servidor dio por servidos %d calentamientos, quería 1 (el de antes de revocar): %v", len(lines), lines)
	}
	if errs := e.Errores(); len(errs) != 0 {
		t.Errorf("el núcleo del Edge anotó errores: %v", errs)
	}
	edgeSinErrores(t, esc.S, nil)
}
