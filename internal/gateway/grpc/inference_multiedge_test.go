package gatewaygrpc_test

// inference_multiedge_test.go — EL CANAL DE CONTROL NO ATIENDE INFERENCIAS
// (Plan 057 · Ola 3 · T3.4, aserto 2).
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 POR QUÉ ESTE TEST MONTA UN STREAM ENTERO PARA UN ASERTO DE AUSENCIA
// ════════════════════════════════════════════════════════════════════════════
//
// El criterio dice «`inferenceSession` no devuelve nunca `__wapp_control__`». Escrito
// a lo fácil —crear un Server y comprobar que no aparece— sería VACUO: pasaría con el
// Server vacío, sin ejercitar nada.
//
// El montaje reproduce el escenario en el que ese id APARECERÍA si la guarda de la Ola
// 2 no existiera: un Edge REAL, con su mTLS, que manda un frame de auth —el único
// frame que un Edge sin ningún teléfono emparejado envía, y el que provocaba el
// registro perezoso— y espera su respuesta, de modo que cuando se pide la inferencia
// el frame ya está procesado del todo. Antes de la Ola 2, en ese punto el tenant tenía
// «una sesión viva» llamada `__wapp_control__`, y la inferencia salía por ella: hacia
// un Edge sin Ollama que la descartaría por session_id desconocido, cuando no hacia el
// Edge de OTRA EMPRESA que compartía esa misma clave global.
//
// 🔬 MUTACIÓN que lo pone rojo: quitar en connect.go el `case sessionID ==
// cltransport.ControlSessionID` para que el control vuelva a caer en el camino de
// sesión normal. Sale «inferencia … por la sesión __wapp_control__: timeout».
//
// ⚠️ Y LA MUTACIÓN QUE NO SIRVE, porque cuesta una hora descubrirlo: añadir solo
// `s.registry.Register(ControlSessionID, sender)` deja el test VERDE. `inferenceSession`
// no consulta el Registry, sino `edgeSessions`, el índice por tenant del Server, que
// puebla `trackSession` desde `registerSession`. Es decir: quien mete el canal de
// control en el conjunto de candidatos NO es el registro del cable, es el track.

import (
	"context"
	"errors"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	gatewaygrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/grpc"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
)

func TestInfer_ElCanalDeControlNoEsUnaSesionQueAtiendaInferencias(t *testing.T) {
	t.Parallel()
	h := newMultiEdgeHarness(t)

	// Un Edge con el operador logueado y CERO teléfonos emparejados: exactamente la
	// máquina que en UAT se llevó el token ajeno.
	e := h.conecta(t, "solo-control", tenantA, "edge-solo-control")
	e.pideLogin(t, "cmd-control-1")
	if resp := e.esperaRespuesta(t, "cmd-control-1", 3*time.Second); resp.GetTokens().GetAccessToken() == "" {
		t.Fatal("el login del montaje no respondió: sin él, el frame de auth no se habría procesado " +
			"y el aserto de abajo no probaría nada")
	}

	_, err := h.srv.Infer(context.Background(), tenantA, gatewaygrpc.InferRequest{
		Prompt: "clasifica esto", Format: "json", Timeout: 2 * time.Second,
	})
	if err == nil {
		t.Fatal("un Edge que solo tiene canal de control NO puede atender una inferencia")
	}
	var ie *gatewaygrpc.InferError
	if !errors.As(err, &ie) || ie.Motivo() != gatewaygrpc.MotivoEdgeOffline {
		t.Fatalf("quiero un InferError con motivo %q, llegó %v", gatewaygrpc.MotivoEdgeOffline, err)
	}
	if !errors.Is(err, session.ErrSessionOffline) {
		t.Fatalf("el error debe envolver ErrSessionOffline, llegó %v", err)
	}

	// Y la otra mitad, que es la que de verdad se rompía: no basta con que el gateway
	// devuelva error — el prompt no puede haber SALIDO por el cable del control.
	if msg, _, hallado := e.busca(300*time.Millisecond, func(m *cloudlinkv1.CloudToEdge) bool {
		return m.GetInferenceRequest() != nil
	}); hallado {
		t.Fatalf("[%s] recibió una InferenceRequest por el canal de control (session_id %q)",
			e.nombre, msg.GetSessionId())
	}
}
