package gatewaygrpc

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-shared/envelope"
	"github.com/EduGoGroup/wapp-shared/logger"
	"google.golang.org/protobuf/proto"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
)

// ============================================================================
// El transporte de la inferencia local (T1.6-3). Son tests INTERNOS por la misma
// razón que los del ack: la respuesta se entrega por deliverInference, que es el
// camino exacto que recorre un frame llegado por el stream (route), y exponerlo en
// la API pública solo para el test sería peor.
// ============================================================================

// srvInfer arma un Server con su clave de cifrado y una sesión viva del tenant.
// responder recibe el frame empujado y decide qué contesta el Edge; si devuelve nil,
// el Edge se queda callado (que es un caso que hay que poder probar).
func srvInfer(t *testing.T, tenantID, sessionID string,
	responder func(*cloudlinkv1.InferenceRequest) *cloudlinkv1.InferenceResult,
) (*Server, func()) {
	t.Helper()
	reg := session.NewRegistry()
	srv := New(reg, logger.New(logger.WithWriter(io.Discard)), WithCloudEncPrivKey(testCloudPriv()))

	release := reg.Register(sessionID, senderFunc(func(msg *cloudlinkv1.CloudToEdge) error {
		req := msg.GetInferenceRequest()
		if req == nil {
			return nil
		}
		if res := responder(req); res != nil {
			go srv.deliverInference(res)
		}
		return nil
	}))
	srv.trackSession(connCtx{tenantID: tenantID, edgeID: "edge-1", sessionID: sessionID})
	return srv, release
}

// sellarSalida sella un InferenceOutput hacia la pública de la nube de test, tal
// como lo hace el Edge.
func sellarSalida(t *testing.T, rawJSON string) []byte {
	t.Helper()
	raw, err := proto.Marshal(&cloudlinkv1.InferenceOutput{RawJson: rawJSON})
	if err != nil {
		t.Fatalf("Marshal InferenceOutput: %v", err)
	}
	sealed, err := envelope.SealFor(testCloudPub(t), raw)
	if err != nil {
		t.Fatalf("SealFor: %v", err)
	}
	return sealed
}

// TestInfer_ElCaminoFeliz: prompt entra por el cable, JSON crudo sale. Comprueba
// además las TRES cosas del frame que el contrato exige y que compilan igual si se
// escriben mal:
//
//   - el prompt viaja VERBATIM (el Edge no lo recorta ni lo completa);
//   - la temperatura viaja CON PRESENCIA EXPLÍCITA aunque valga 0.0 — el campo del
//     proto es `optional` justo para que «quiero 0» y «no dije nada» no sean el mismo
//     byte, y un `Temperature: 0` sin puntero los fundiría en silencio;
//   - el session_id del PAYLOAD es el de origen y NO el de la sesión de empuje: son
//     datos distintos y copiarlos convertiría la trazabilidad en una tautología.
func TestInfer_ElCaminoFeliz(t *testing.T) {
	t.Parallel()
	var visto *cloudlinkv1.InferenceRequest
	srv, release := srvInfer(t, "tenant-1", "s-viva", func(req *cloudlinkv1.InferenceRequest) *cloudlinkv1.InferenceResult {
		visto = req
		return &cloudlinkv1.InferenceResult{
			CommandId: req.GetCommandId(),
			Result:    &cloudlinkv1.InferenceResult_EncOutput{EncOutput: sellarSalida(t, `{"version":1,"intent":"x"}`)},
		}
	})
	defer release()

	out, err := srv.Infer(context.Background(), "tenant-1", InferRequest{
		Prompt: "clasifica esto", Format: "json", Timeout: 2 * time.Second,
		OriginSessionID: "s-origen",
	})
	if err != nil {
		t.Fatalf("Infer: %v", err)
	}
	if out != `{"version":1,"intent":"x"}` {
		t.Fatalf("salida = %q", out)
	}
	if visto.GetPrompt() != "clasifica esto" {
		t.Fatalf("el prompt no viajó verbatim: %q", visto.GetPrompt())
	}
	if visto.Temperature == nil {
		t.Fatal("la temperatura viajó SIN presencia: el Edge no puede distinguir «quiero 0» de «no dije nada»")
	}
	if visto.GetSessionId() != "s-origen" {
		t.Fatalf("session_id del payload = %q, quiero la de ORIGEN", visto.GetSessionId())
	}
	if visto.GetTimeoutMs() != 2000 {
		t.Fatalf("timeout_ms = %d, quiero 2000", visto.GetTimeoutMs())
	}
}

// TestInfer_CadaErrorDelFrameTraeSuMotivo: el enum del proto y el vocabulario de
// motivos son 1:1, y el mapeo se comprueba valor por valor.
//
// 🔬 MUTACIÓN: cambiar cualquier rama de motivoDeFrame por otro motivo ⇒ rojo aquí.
func TestInfer_CadaErrorDelFrameTraeSuMotivo(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		enum   cloudlinkv1.InferenceError
		motivo string
	}{
		{cloudlinkv1.InferenceError_INFERENCE_ERROR_OLLAMA_DOWN, MotivoOllamaDown},
		{cloudlinkv1.InferenceError_INFERENCE_ERROR_BREAKER_OPEN, MotivoBreakerOpen},
		{cloudlinkv1.InferenceError_INFERENCE_ERROR_TIMEOUT, MotivoTimeout},
		{cloudlinkv1.InferenceError_INFERENCE_ERROR_LEASE_INVALID, MotivoLeaseInvalid},
		{cloudlinkv1.InferenceError_INFERENCE_ERROR_EDGE_SIN_CAPACIDAD, MotivoEdgeSinCapacidad},
	} {
		t.Run(tc.enum.String(), func(t *testing.T) {
			t.Parallel()
			srv, release := srvInfer(t, "tenant-1", "s-viva", func(req *cloudlinkv1.InferenceRequest) *cloudlinkv1.InferenceResult {
				return &cloudlinkv1.InferenceResult{
					CommandId: req.GetCommandId(),
					Result:    &cloudlinkv1.InferenceResult_Error{Error: tc.enum},
				}
			})
			defer release()

			_, err := srv.Infer(context.Background(), "tenant-1", InferRequest{Prompt: "p", Timeout: time.Second})
			exigeMotivo(t, err, tc.motivo)
		})
	}
}

// exigeMotivo falla si el error no trae el motivo esperado por duck-typing. Se
// comprueba por la INTERFAZ ANÓNIMA y no por *InferError a propósito: es así como lo
// consume el escritor de notificaciones, y probarlo por el tipo concreto dejaría sin
// red el camino que de verdad se usa.
func exigeMotivo(t *testing.T, err error, quiero string) {
	t.Helper()
	if err == nil {
		t.Fatalf("quiero error con motivo %q, llegó nil", quiero)
	}
	var m interface{ Motivo() string }
	if !errors.As(err, &m) {
		t.Fatalf("el error no expone Motivo(): %v", err)
	}
	if m.Motivo() != quiero {
		t.Fatalf("motivo = %q, quiero %q", m.Motivo(), quiero)
	}
}

// TestInfer_SinSesionVivaEsEdgeOffline: el tenant no tiene ni un stream en esta
// réplica. Es el caso que la decisión de correlacionar en memoria hace posible y que
// se acepta A PROPÓSITO (ver el bloque de cabecera de inference.go): falla honesto,
// con motivo, y NO se empuja nada.
func TestInfer_SinSesionVivaEsEdgeOffline(t *testing.T) {
	t.Parallel()
	srv := New(session.NewRegistry(), logger.New(logger.WithWriter(io.Discard)))

	_, err := srv.Infer(context.Background(), "tenant-sin-edge", InferRequest{Prompt: "p"})
	exigeMotivo(t, err, MotivoEdgeOffline)
	if !errors.Is(err, session.ErrSessionOffline) {
		t.Fatalf("quiero que envuelva ErrSessionOffline: %v", err)
	}
}

// TestInfer_ElPresupuestoDelCloudVence: el Edge se queda callado y el Cloud NO se
// cuelga. El motivo es timeout, no edge_offline: hay stream, lo que no hay es
// respuesta.
func TestInfer_ElPresupuestoDelCloudVence(t *testing.T) {
	t.Parallel()
	srv, release := srvInfer(t, "tenant-1", "s-viva", func(*cloudlinkv1.InferenceRequest) *cloudlinkv1.InferenceResult {
		return nil // el Edge no contesta
	})
	defer release()
	srv.inferGrace = 10 * time.Millisecond

	inicio := time.Now()
	_, err := srv.Infer(context.Background(), "tenant-1", InferRequest{Prompt: "p", Timeout: 20 * time.Millisecond})
	exigeMotivo(t, err, MotivoTimeout)
	if time.Since(inicio) > 2*time.Second {
		t.Fatalf("la espera no respetó el presupuesto: %v", time.Since(inicio))
	}
}

// TestInfer_ElLlamanteQueSeRindeNoTieneMotivo: la distinción que sostiene REQ-38.
// Si el ctx del llamante muere —la ventana de agregación se cerró, el proceso se
// apaga— eso NO dice nada sobre la salud de la vía del tenant, así que el error sale
// SIN motivo y nadie avisa al dueño.
//
// 🔬 MUTACIÓN: fundir los dos relojes en un solo context.WithTimeout dentro de
// awaitInference ⇒ este caso devolvería motivo `timeout` y el dueño recibiría un
// aviso por su propio reloj.
func TestInfer_ElLlamanteQueSeRindeNoTieneMotivo(t *testing.T) {
	t.Parallel()
	srv, release := srvInfer(t, "tenant-1", "s-viva", func(*cloudlinkv1.InferenceRequest) *cloudlinkv1.InferenceResult {
		return nil
	})
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err := srv.Infer(ctx, "tenant-1", InferRequest{Prompt: "p", Timeout: 30 * time.Second})
	if !errors.Is(err, ErrInferenceAbandonada) {
		t.Fatalf("quiero ErrInferenceAbandonada, llegó: %v", err)
	}
	var m interface{ Motivo() string }
	if errors.As(err, &m) {
		t.Fatalf("el abandono del llamante NO puede traer motivo, trajo %q", m.Motivo())
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("quiero que siga envolviendo el ctx.Err(): %v", err)
	}
}

// TestCancelSessionInfers_DespiertaEnElActo: cuando el stream cae, la inferencia en
// vuelo NO espera su presupuesto entero (que aquí son 30 s) contra un Edge que ya no
// está. Es la misma lección medida del Plan 050 · Ola 2, aplicada a un plazo mucho
// más largo que el del ack.
func TestCancelSessionInfers_DespiertaEnElActo(t *testing.T) {
	t.Parallel()
	empujado := make(chan struct{}, 1)
	srv, release := srvInfer(t, "tenant-1", "s-viva", func(*cloudlinkv1.InferenceRequest) *cloudlinkv1.InferenceResult {
		empujado <- struct{}{}
		return nil
	})
	defer release()

	type res struct {
		err error
		dur time.Duration
	}
	hecho := make(chan res, 1)
	go func() {
		inicio := time.Now()
		_, err := srv.Infer(context.Background(), "tenant-1", InferRequest{Prompt: "p", Timeout: 30 * time.Second})
		hecho <- res{err: err, dur: time.Since(inicio)}
	}()

	<-empujado
	if n := srv.cancelSessionInfers("s-viva"); n != 1 {
		t.Fatalf("cancelados = %d, quiero 1", n)
	}

	select {
	case r := <-hecho:
		exigeMotivo(t, r.err, MotivoEdgeOffline)
		if !errors.Is(r.err, ErrStreamClosed) {
			t.Fatalf("quiero que envuelva ErrStreamClosed: %v", r.err)
		}
		if r.dur > 5*time.Second {
			t.Fatalf("esperó %v: la cancelación no despertó al llamante", r.dur)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("la inferencia siguió esperando después de cancelar la sesión")
	}
}

// TestInfer_LaEntradaPendienteNoSeFuga: al salir de Infer —por el camino que sea— el
// mapa de correlación queda vacío. Es la propiedad que hace que un mapa en memoria
// sea aceptable aquí, así que se comprueba en vez de suponerse.
func TestInfer_LaEntradaPendienteNoSeFuga(t *testing.T) {
	t.Parallel()
	srv, release := srvInfer(t, "tenant-1", "s-viva", func(req *cloudlinkv1.InferenceRequest) *cloudlinkv1.InferenceResult {
		return &cloudlinkv1.InferenceResult{
			CommandId: req.GetCommandId(),
			Result:    &cloudlinkv1.InferenceResult_Error{Error: cloudlinkv1.InferenceError_INFERENCE_ERROR_OLLAMA_DOWN},
		}
	})
	defer release()

	for range 3 {
		// El error es el ESPERADO (el Edge contesta ollama_down); lo que se mira aquí
		// es que la entrada pendiente se retire por ese camino igual que por los otros.
		if _, err := srv.Infer(context.Background(), "tenant-1", InferRequest{Prompt: "p", Timeout: time.Second}); err == nil {
			t.Fatal("quiero el error del Edge")
		}
	}
	srv.infersMu.Lock()
	n := len(srv.infers)
	srv.infersMu.Unlock()
	if n != 0 {
		t.Fatalf("quedan %d entradas pendientes", n)
	}
}

// TestInfer_ConDosSesionesVivasElORIGEN_DecidePorDondeSale (T1.7-8): el criterio de
// selección visto DESDE EL CABLE, con dos Edges del mismo tenant.
//
// El test de inferenceSession de más abajo prueba la FUNCIÓN; este prueba la CADENA
// entera —InferRequest.OriginSessionID → inferenceSession → Registry.Push→ y es la
// que estaba rota: el selector recibía la sesión de origen y no se la pasaba al
// adaptador, así que este campo llegaba SIEMPRE vacío y el frame salía siempre por la
// primera alfabética. Con un solo Edge no se nota nada; con dos, el segundo no
// calienta nunca su caché de prefijos.
//
// Las dos sesiones se registran en orden INVERSO al alfabético a propósito: si el
// código empujara por «la última registrada» o por el recorrido del map, el caso del
// fallback pasaría por casualidad.
func TestInfer_ConDosSesionesVivasElORIGEN_DecidePorDondeSale(t *testing.T) {
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
		// edgeID distinto por sesión: son DOS instalaciones del mismo tenant, cada una
		// con su Ollama. Es el único escenario donde la elección cambia algo.
		srv.trackSession(connCtx{tenantID: "tenant-1", edgeID: "edge-" + sid, sessionID: sid})
	}

	for _, tc := range []struct {
		nombre, origen, quieroStream, quieroPayload string
	}{
		{"el origen vivo manda: sale por SU Edge, no por el primero alfabético",
			"sess-B", "sess-B", "sess-B"},
		{"sin origen sale por el primero alfabético (no-regresión del fallback)",
			"", "sess-A", ""},
		{"un origen que no está vivo cae al alfabético y NO es un error",
			"sess-muerta", "sess-A", "sess-muerta"},
	} {
		t.Run(tc.nombre, func(t *testing.T) {
			salioPor, visto = "", nil
			out, err := srv.Infer(context.Background(), "tenant-1", InferRequest{
				Prompt: "clasifica esto", Format: "json", Timeout: 2 * time.Second,
				OriginSessionID: tc.origen,
			})
			if err != nil {
				t.Fatalf("Infer: %v", err)
			}
			if out != salida {
				t.Fatalf("salida = %q", out)
			}
			if salioPor != tc.quieroStream {
				t.Fatalf("el frame salió por %q, quiero %q", salioPor, tc.quieroStream)
			}
			// Y el session_id del PAYLOAD sigue siendo el de origen tal cual llegó,
			// también cuando no coincide con el stream: es trazabilidad de la
			// conversación, no una copia de por dónde salió.
			if visto.GetSessionId() != tc.quieroPayload {
				t.Fatalf("session_id del payload = %q, quiero %q", visto.GetSessionId(), tc.quieroPayload)
			}
		})
	}
}

// TestInferenceSession_PrefiereElOrigenYSiNoEsDeterminista: los dos pasos del
// criterio de selección.
//
// El segundo importa más de lo que parece: el recorrido de un map de Go está
// ALEATORIZADO, así que sin ordenar, dos peticiones seguidas del mismo tenant se
// irían a Edges distintos y el breaker del Edge (ADR-0042) y el modelo ya cargado
// dejarían de significar nada. Por eso se pide DIEZ veces y se exige el mismo
// resultado: una sola llamada pasaría por casualidad.
func TestInferenceSession_PrefiereElOrigenYSiNoEsDeterminista(t *testing.T) {
	t.Parallel()
	reg := session.NewRegistry()
	srv := New(reg, logger.New(logger.WithWriter(io.Discard)))
	for _, sid := range []string{"s-ccc", "s-aaa", "s-bbb"} {
		defer reg.Register(sid, senderFunc(func(*cloudlinkv1.CloudToEdge) error { return nil }))()
		srv.trackSession(connCtx{tenantID: "t", edgeID: "e", sessionID: sid})
	}

	if got, ok := srv.inferenceSession("t", "s-bbb"); !ok || got != "s-bbb" {
		t.Fatalf("con origen vivo quiero s-bbb, salió %q (ok=%v)", got, ok)
	}
	// Un origen que NO está vivo no manda: se cae al criterio general.
	if got, ok := srv.inferenceSession("t", "s-muerta"); !ok || got != "s-aaa" {
		t.Fatalf("con origen muerto quiero la primera alfabética, salió %q (ok=%v)", got, ok)
	}
	for range 10 {
		if got, _ := srv.inferenceSession("t", ""); got != "s-aaa" {
			t.Fatalf("la selección no es determinista: salió %q", got)
		}
	}
	if _, ok := srv.inferenceSession("otro-tenant", ""); ok {
		t.Fatal("el tenant sin sesiones no puede devolver una sesión de otro (INV-8)")
	}
}

// TestInfer_LoQueNoMapeaNoTraeMotivo: los tres fallos de la NUBE —sin clave, sobre
// ilegible, oneof vacío— no son degradaciones de la vía del tenant y salen SIN
// motivo, de modo que el decorador de llmvia no escribe aviso.
//
// 🔬 MUTACIÓN: devolver cualquiera de los tres como *InferError con motivo
// ollama_down ⇒ el dueño recibiría un aviso mandándolo a mirar su Ollama, que está
// perfectamente.
func TestInfer_LoQueNoMapeaNoTraeMotivo(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		nombre   string
		conClave bool
		res      func(cmdID string) *cloudlinkv1.InferenceResult
		quiero   error
	}{
		{
			nombre: "sin clave de cifrado en la nube",
			res: func(id string) *cloudlinkv1.InferenceResult {
				return &cloudlinkv1.InferenceResult{CommandId: id,
					Result: &cloudlinkv1.InferenceResult_EncOutput{EncOutput: []byte("lo que sea")}}
			},
			quiero: ErrInferenceSinClaveDeCifrado,
		},
		{
			nombre: "sobre corrupto", conClave: true,
			res: func(id string) *cloudlinkv1.InferenceResult {
				return &cloudlinkv1.InferenceResult{CommandId: id,
					Result: &cloudlinkv1.InferenceResult_EncOutput{EncOutput: []byte("no es un sobre")}}
			},
			quiero: ErrInferenceSelladoIlegible,
		},
		{
			nombre: "oneof vacío", conClave: true,
			res:    func(id string) *cloudlinkv1.InferenceResult { return &cloudlinkv1.InferenceResult{CommandId: id} },
			quiero: ErrInferenceSinSalida,
		},
	} {
		t.Run(tc.nombre, func(t *testing.T) {
			t.Parallel()
			reg := session.NewRegistry()
			opts := []Option{}
			if tc.conClave {
				opts = append(opts, WithCloudEncPrivKey(testCloudPriv()))
			}
			srv := New(reg, logger.New(logger.WithWriter(io.Discard)), opts...)
			defer reg.Register("s", senderFunc(func(msg *cloudlinkv1.CloudToEdge) error {
				go srv.deliverInference(tc.res(msg.GetCommandId()))
				return nil
			}))()
			srv.trackSession(connCtx{tenantID: "t", edgeID: "e", sessionID: "s"})

			_, err := srv.Infer(context.Background(), "t", InferRequest{Prompt: "p", Timeout: time.Second})
			if !errors.Is(err, tc.quiero) {
				t.Fatalf("err = %v, quiero %v", err, tc.quiero)
			}
			var m interface{ Motivo() string }
			if errors.As(err, &m) {
				t.Fatalf("un fallo de la NUBE no puede traer motivo, trajo %q", m.Motivo())
			}
		})
	}
}

// TestInfer_ResultadoHuerfanoNoRompeNada: un InferenceResult sin petición pendiente
// —llegó tarde, duplicado, o su llamante ya se rindió— se ignora. Mismo criterio que
// el bundle de diagnóstico huérfano: jamás tumba el stream.
func TestInfer_ResultadoHuerfanoNoRompeNada(t *testing.T) {
	t.Parallel()
	srv := New(session.NewRegistry(), logger.New(logger.WithWriter(io.Discard)))
	srv.deliverInference(&cloudlinkv1.InferenceResult{CommandId: "no-existe"})
	srv.deliverInference(nil)
}

// TestNewMaterializaElMargenDeInferencia: la espera de la inferencia NUNCA queda sin
// margen. Sin él los dos relojes vencerían a la vez y se perdería el error NOMBRADO
// del Edge, que es el único que dice qué pasó.
func TestNewMaterializaElMargenDeInferencia(t *testing.T) {
	t.Parallel()
	srv := New(session.NewRegistry(), logger.New(logger.WithWriter(io.Discard)))
	if srv.inferGrace != DefaultInferGrace {
		t.Fatalf("inferGrace = %v, quiero %v", srv.inferGrace, DefaultInferGrace)
	}
	if got := inferTimeout(0); got != defaultInferTimeout {
		t.Fatalf("inferTimeout(0) = %v, quiero %v", got, defaultInferTimeout)
	}
}
