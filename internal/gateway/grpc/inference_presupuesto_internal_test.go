package gatewaygrpc

import (
	"context"
	"io"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	cltransport "github.com/EduGoGroup/wapp-cloudlink/transport"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
)

// TestInferToCloud_LosCamposDelPresupuestoYElRotulo comprueba lo que el transporte
// tiene que hacer con los tres campos nuevos del frame (7, 8 y 10).
//
// El caso que importa es el PRIMERO de la tabla: `max_output_tokens` es `optional`
// para que «quiero 0» y «no dije nada» no sean el mismo byte en el cable, y el Cloud
// nunca quiere 0 —una salida de cero tokens no es una respuesta—. Así que un valor no
// positivo tiene que dejar el campo AUSENTE para que el Edge aplique su default; si
// se escribiera un puntero a 0, el Edge recibiría la orden de generar nada.
//
// 🔬 MUTACIÓN: cambiar la guarda a `if req.MaxOutputTokens >= 0` ⇒ rojo en el primer
// caso (el campo pasa a estar presente valiendo 0).
func TestInferToCloud_LosCamposDelPresupuestoYElRotulo(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		nombre    string
		req       InferRequest
		presente  bool
		valor     int32
		claseWire string
		warmup    bool
	}{
		{
			nombre: "sin techo: el campo va AUSENTE y manda el default del Edge",
			req:    InferRequest{Prompt: "p"},
		},
		{
			nombre:    "un techo positivo viaja con presencia explícita",
			req:       InferRequest{Prompt: "p", MaxOutputTokens: 512, Class: ClaseLote},
			presente:  true,
			valor:     512,
			claseWire: ClaseLote,
		},
		{
			nombre:    "un calentamiento va marcado con warmup, NO con class",
			req:       InferRequest{Prompt: "p", MaxOutputTokens: 16, Class: ClaseLote, Warmup: true},
			presente:  true,
			valor:     16,
			claseWire: ClaseLote,
			warmup:    true,
		},
	} {
		t.Run(tc.nombre, func(t *testing.T) {
			t.Parallel()
			frame := inferToCloud("cmd-1", "sess-1", tc.req).GetInferenceRequest()
			if got := frame.MaxOutputTokens != nil; got != tc.presente {
				t.Fatalf("presencia de max_output_tokens = %v, quiero %v (valor recibido %v)",
					got, tc.presente, frame.GetMaxOutputTokens())
			}
			if tc.presente && frame.GetMaxOutputTokens() != tc.valor {
				t.Errorf("max_output_tokens = %d, quiero %d", frame.GetMaxOutputTokens(), tc.valor)
			}
			if frame.GetClass() != tc.claseWire {
				t.Errorf("class = %q, quiero %q", frame.GetClass(), tc.claseWire)
			}
			if frame.GetWarmup() != tc.warmup {
				t.Errorf("warmup = %v, quiero %v", frame.GetWarmup(), tc.warmup)
			}
		})
	}
}

// TestInfer_ElDESTINO_MandaSobreElOrigenYNoViajaEnElPayload es la propiedad que hace
// posible el calentamiento: se puede EXIGIR por qué Edge sale un frame sin afirmar
// que ninguna conversación lo originó.
//
// Las dos mitades importan y son distintas:
//
//   - el frame sale por el Edge que se pidió, aunque haya un origen vivo distinto (el
//     calentamiento tiene que llenar la caché de ESE Ollama y no la del vecino);
//   - el session_id del PAYLOAD sigue siendo el de origen —vacío cuando no lo hay—,
//     porque es trazabilidad de la conversación y no una copia del cable.
//
// 🔬 MUTACIÓN: que rutaPreferida devuelva siempre OriginSessionID ⇒ rojo en la primera
// mitad. Que inferToCloud escriba `SessionId: sessionID` ⇒ rojo en la segunda.
func TestInfer_ElDESTINO_MandaSobreElOrigenYNoViajaEnElPayload(t *testing.T) {
	t.Parallel()
	const salida = `{"version":1,"intent":"x"}`
	reg := session.NewRegistry()
	srv := New(reg, logger.New(logger.WithWriter(io.Discard)), WithCloudEncPrivKey(testCloudPriv()))
	sellada := sellarSalida(t, salida)

	var salioPor string
	var visto *cloudlinkv1.InferenceRequest
	for _, sid := range []string{"sess-B", "sess-A"} {
		defer reg.Register(sid, senderFunc(func(msg *cloudlinkv1.CloudToEdge) error {
			req := msg.GetInferenceRequest()
			if req == nil {
				return nil
			}
			salioPor, visto = sid, req
			go srv.deliverInference(&cloudlinkv1.InferenceResult{
				CommandId: req.GetCommandId(),
				Result:    &cloudlinkv1.InferenceResult_EncOutput{EncOutput: sellada},
			})
			return nil
		}))()
		srv.trackSession(connCtx{tenantID: "tenant-1", edgeID: "edge-" + sid, sessionID: sid})
	}

	if _, err := srv.Infer(context.Background(), "tenant-1", InferRequest{
		Prompt: "calienta", Timeout: 2 * time.Second,
		TargetSessionID: "sess-B",
		// Origen vivo y DISTINTO del destino: si el destino no mandara, el frame se
		// iría por sess-A y el calentamiento llenaría la caché equivocada.
		OriginSessionID: "sess-A",
		Warmup:          true,
	}); err != nil {
		t.Fatalf("Infer: %v", err)
	}
	if salioPor != "sess-B" {
		t.Fatalf("el frame salió por %q, quiero sess-B: TargetSessionID no gobernó el cable", salioPor)
	}
	if visto.GetSessionId() != "sess-A" {
		t.Fatalf("session_id del payload = %q, quiero sess-A (el ORIGEN, no el destino)", visto.GetSessionId())
	}
	if !visto.GetWarmup() {
		t.Error("el frame llegó sin warmup")
	}
}

// TestPushConfig_CalientaUNAVezPorEDGE_NoPorSesion.
//
// 🔴 ES LA DIFERENCIA ENTRE UNA MEJORA Y UNA AVERÍA. Un Edge multiplexa todas las
// sesiones del tenant sobre un stream y tiene UN Ollama con UNA plaza. Si el
// calentamiento se disparara por sesión, un tenant con tres teléfonos en una máquina
// pagaría tres prefills fríos seguidos (~150 s) por publicar su catálogo, y durante
// ese rato ninguna inferencia real cabría.
//
// 🔬 MUTACIÓN: que calentarEdges recorra sessionsForTenant en vez de unaSesionPorEdge
// ⇒ rojo (tres avisos en vez de dos).
func TestPushConfig_CalientaUNAVezPorEDGE_NoPorSesion(t *testing.T) {
	t.Parallel()
	reg := session.NewRegistry()
	srv := New(reg, logger.New(logger.WithWriter(io.Discard)))

	// edge-1 con TRES sesiones (tres teléfonos, una máquina) y edge-2 con una.
	for _, s := range []struct{ edge, sess string }{
		{"edge-1", "s-1"}, {"edge-1", "s-2"}, {"edge-1", "s-3"}, {"edge-2", "s-4"},
	} {
		defer reg.Register(s.sess, senderFunc(func(*cloudlinkv1.CloudToEdge) error { return nil }))()
		srv.trackSession(connCtx{tenantID: "t", edgeID: s.edge, sessionID: s.sess})
	}

	avisos := map[string]string{}
	srv.OnWarmup = func(tenantID, edgeID, sessionID, _ string) {
		if tenantID != "t" {
			t.Errorf("tenant = %q", tenantID)
		}
		if _, repetido := avisos[edgeID]; repetido {
			t.Errorf("el Edge %s recibió DOS avisos de calentamiento por un solo ConfigUpdate", edgeID)
		}
		avisos[edgeID] = sessionID
	}

	if err := srv.PushConfig(context.Background(), "t", "intents", "v1", []byte("{}")); err != nil {
		t.Fatalf("PushConfig: %v", err)
	}
	if len(avisos) != 2 {
		t.Fatalf("avisos de calentamiento = %d (%v), quiero 2: uno por EDGE", len(avisos), avisos)
	}
	if s := avisos["edge-1"]; s != "s-1" && s != "s-2" && s != "s-3" {
		t.Errorf("el aviso de edge-1 trae la sesión %q, que no es suya", s)
	}
	if avisos["edge-2"] != "s-4" {
		t.Errorf("el aviso de edge-2 trae %q, quiero s-4", avisos["edge-2"])
	}
}

// TestOnSessionRegistered_CalientaSalvoElCanalDeControl.
//
// La segunda mitad no es una guarda defensiva: un Edge con el operador logueado y
// CERO teléfonos no va a recibir ningún mensaje que clasificar, así que calentarlo
// gastaría ~50 s de la CPU del cliente y ~250 MB de su caché por un prefijo que nadie
// va a pedir.
//
// 🔬 MUTACIÓN: quitar la condición `cc.sessionID != cltransport.ControlSessionID` ⇒
// rojo en el segundo caso.
func TestOnSessionRegistered_CalientaSalvoElCanalDeControl(t *testing.T) {
	t.Parallel()
	srv := New(session.NewRegistry(), logger.New(logger.WithWriter(io.Discard)))
	var vistos []string
	srv.OnWarmup = func(_, _, sessionID, kind string) {
		if kind != "" {
			t.Errorf("el aviso del handshake trae kind %q; tiene que ir vacío: no se publicó ninguna config, "+
				"es que este Edge no tiene NADA cacheado", kind)
		}
		vistos = append(vistos, sessionID)
	}

	srv.onSessionRegistered(context.Background(), ccDePrueba("s-normal"))
	if len(vistos) != 1 || vistos[0] != "s-normal" {
		t.Fatalf("una sesión normal tiene que disparar el calentamiento; vistos=%v", vistos)
	}

	cc := ccDePrueba("s-normal")
	cc.sessionID = cltransport.ControlSessionID
	srv.onSessionRegistered(context.Background(), cc)
	if len(vistos) != 1 {
		t.Fatalf("el canal de control NO se calienta (no hay teléfono detrás); vistos=%v", vistos)
	}
}
