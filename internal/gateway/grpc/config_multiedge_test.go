package gatewaygrpc_test

// config_multiedge_test.go — LA CONFIGURACIÓN DE UNA EMPRESA NO ENTRA EN EL EDGE DE
// OTRA (Plan 057 · Ola 2 · T2.3, REQ-057.5).
//
// 🔴 POR QUÉ ESTE ES EL PEOR DE LOS CUATRO DEFECTOS, aunque no sea el que se observó.
// El del login (Ola 1) filtraba tokens, pero el Edge receptor los DESCARTABA: no
// reconocía el `command_id`. Este no se descarta. `handleConfigUpdate` del Edge
// (internal/adapters/cloudlink/adapter.go) se atiende ANTES de resolver la sesión —es
// config del EDGE por kind, no de una sesión—, se aplica GLOBAL y se ack-ea SIEMPRE,
// aunque el `session_id` que la etiqueta no esté registrado. Así que el catálogo de
// intenciones de una empresa podía APLICARSE DE VERDAD en la máquina de otra.
//
// El camino: `trackSession` metía `__wapp_control__` en `edgeSessions`, de donde
// `sessionsForTenant` lo saca, y `PushConfig` hace fan-out sobre esa lista. Como la
// clave es global y el Registry es última-gana, el `Push` a `__wapp_control__` salía
// por el stream del ÚLTIMO Edge que hubiera conectado, fuera de quien fuera.
//
// 🔬 MUTACIÓN que lo pone en rojo: devolver el `case sessionID == ControlSessionID` de
// Connect al registro perezoso de siempre (T2.1).

import (
	"bytes"
	"context"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	gatewaygrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/grpc"
)

// cfgPorTenant entrega una config cuyo PAYLOAD DICE DE QUIÉN ES. Que el payload sea
// identificable no es cosmético: es lo único que permite afirmar «este Edge recibió
// configuración AJENA» en vez del aserto débil «recibió algo».
type cfgPorTenant struct{}

func (cfgPorTenant) ConfigsForConnect(_ context.Context, tenantID string) ([]gatewaygrpc.ConfigPayload, error) {
	return []gatewaygrpc.ConfigPayload{{
		Kind:    "intents",
		Version: "v1",
		Payload: []byte("catalogo-de-" + tenantID),
	}}, nil
}

// frameHeartbeat es lo que abre una sesión REAL (con un session_id de teléfono, no la
// constante de control): el registro del gateway es perezoso al primer frame.
func frameHeartbeat(sessionID string) *cloudlinkv1.EdgeToCloud {
	return &cloudlinkv1.EdgeToCloud{
		SessionId: sessionID,
		Payload:   &cloudlinkv1.EdgeToCloud_Heartbeat{Heartbeat: &cloudlinkv1.Heartbeat{}},
	}
}

// esperaConfigCon exige que llegue un ConfigUpdate cuyo payload contenga la marca dada.
func (e *edgeVivo) esperaConfigCon(t *testing.T, marca string, d time.Duration) {
	t.Helper()
	_, abierto, hallado := e.busca(d, configCon(marca))
	if !abierto {
		t.Fatalf("[%s] el stream se cerró esperando la config %q", e.nombre, marca)
	}
	if !hallado {
		t.Fatalf("[%s] no recibió la config %q en %s", e.nombre, marca, d)
	}
}

// exigeNingunaConfigCon falla si a este Edge le llegó alguna config con la marca de
// otra empresa. Es el aserto de la fuga, y mira TAMBIÉN lo que otros helpers apartaron.
func (e *edgeVivo) exigeNingunaConfigCon(t *testing.T, marcaAjena string, d time.Duration) {
	t.Helper()
	msg, _, hallado := e.busca(d, configCon(marcaAjena))
	if !hallado {
		return
	}
	t.Fatalf("[%s] recibió y habría APLICADO configuración de otra empresa (%q). "+
		"El Edge no descarta un ConfigUpdate ajeno: lo aplica global y lo ack-ea",
		e.nombre, msg.GetConfigUpdate().GetPayload())
}

// configCon construye el predicado «es un ConfigUpdate cuyo payload lleva esta marca».
func configCon(marca string) func(*cloudlinkv1.CloudToEdge) bool {
	return func(m *cloudlinkv1.CloudToEdge) bool {
		cu := m.GetConfigUpdate()
		return cu != nil && bytes.Contains(cu.GetPayload(), []byte(marca))
	}
}

// TestPushConfig_NoAlcanzaElCanalDeControl: el fan-out de config de un tenant llega a
// sus sesiones REALES y a nadie más.
func TestPushConfig_NoAlcanzaElCanalDeControl(t *testing.T) {
	t.Parallel()
	h := newMultiEdgeHarness(t, gatewaygrpc.WithConfigProvider(cfgPorTenant{}))

	// Empresa A: un Edge con el operador logueado Y un teléfono emparejado.
	edgeA := h.conecta(t, "edge-de-A", tenantA, "edge-A")
	edgeA.pideLogin(t, "cmd-A-1")
	edgeA.esperaRespuesta(t, "cmd-A-1", 5*time.Second)
	if err := edgeA.stream.Send(frameHeartbeat("aaaa1111-2222-3333-4444-555555555555")); err != nil {
		t.Fatalf("heartbeat de A: %v", err)
	}
	edgeA.esperaConfigCon(t, "catalogo-de-"+tenantA, 5*time.Second)

	// Empresa B: conecta DESPUÉS, así que con el código anterior era ELLA la registrada
	// bajo la clave de control compartida.
	edgeB := h.conecta(t, "edge-de-B", tenantB, "edge-B")
	edgeB.pideLogin(t, "cmd-B-1")
	edgeB.esperaRespuesta(t, "cmd-B-1", 5*time.Second)

	// La empresa A publica su catálogo.
	if err := h.srv.PushConfig(context.Background(), tenantA, "intents", "v2", []byte("publicado-de-"+tenantA)); err != nil {
		t.Fatalf("PushConfig: %v", err)
	}

	edgeA.esperaConfigCon(t, "publicado-de-"+tenantA, 5*time.Second)
	edgeB.exigeNingunaConfigCon(t, "-de-"+tenantA, 800*time.Millisecond)
}

// TestConfigAlConectar_LlegaAlEdgeSinNingunTelefono es el CONTROL del anterior, y sin él
// la Ola 2 podría cerrarse habiendo roto algo en silencio (REQ-057.7).
//
// 🔴 QUÉ CUSTODIA. El push de config al conectar (ADR-0021) colgaba del registro de una
// sesión. Para un Edge recién arrancado SIN NINGÚN TELÉFONO, el frame de auth era el
// único que provocaba registro: recibía su catálogo POR EL CANAL DE CONTROL y por
// ningún otro sitio. Al dejar de registrar ese id (T2.1), ese Edge se habría quedado
// con el catálogo VACÍO — sin test rojo y sin una sola línea de error en ningún log.
// `pushConfigsInBand` (T2.2) es lo que lo repone, y esto es lo que lo demuestra.
//
// 🔬 MUTACIÓN: borrar la llamada a `s.pushConfigsInBand` de `onControlChannel`.
func TestConfigAlConectar_LlegaAlEdgeSinNingunTelefono(t *testing.T) {
	t.Parallel()
	h := newMultiEdgeHarness(t, gatewaygrpc.WithConfigProvider(cfgPorTenant{}))

	// Un Edge que solo se ha logueado: cero sesiones de WhatsApp, cero heartbeats.
	edge := h.conecta(t, "edge-recien-instalado", tenantA, "edge-sin-telefonos")
	edge.pideLogin(t, "cmd-1")
	edge.esperaRespuesta(t, "cmd-1", 5*time.Second)

	edge.esperaConfigCon(t, "catalogo-de-"+tenantA, 5*time.Second)
}
