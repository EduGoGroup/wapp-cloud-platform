//go:build integracion

package procesos

import (
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-shared/envelope"
	"google.golang.org/protobuf/proto"
)

// TestArnes_EdgeInferencia, sin servidor: cómo contesta el Edge de prueba a una InferenceRequest con
// lease vigente (guion, fallo, sin guion, calentamiento, guion lento, de una en una). Los casos del
// gate de lease están en edge_falso_selftest_inference_lease_test.go.
// Sale de edge_falso_test.go (D-F9-11: solo se movieron declaraciones).

// TestArnes_EdgeInferencia prueba, sin servidor, cómo contesta el Edge a una InferenceRequest. Con
// lease vigente: con guion, el JSON sale sellado y se abre con la privada de la nube; si el guion
// falla, sale el error nombrado; sin guion, OLLAMA_DOWN; un calentamiento no consulta el guion; las
// peticiones quedan registradas; un guion lento no frena el resto de comandos; y las inferencias
// van de una en una. Sin lease vigente —ninguno todavía, rechazado, vencido o revocado—, y como el
// Edge real: agotada la gracia contesta INFERENCE_ERROR_LEASE_INVALID sin consultar el guion; si el
// lease llega dentro de la gracia, sirve; y cerrar el enlace corta la espera.
func TestArnes_EdgeInferencia(t *testing.T) {
	t.Parallel()
	t.Run("gate de lease: sin lease vigente no se infiere y se contesta LEASE_INVALID", edgeCheckInferenceLeaseGate)
	t.Run("gate de lease: un lease vencido tampoco deja inferir", edgeCheckInferenceLeaseGateExpired)
	t.Run("gate de lease: la gracia espera al lease y el cierre la corta", edgeCheckInferenceLeaseGrace)
	t.Run("con guion: la salida va sellada", edgeProbarInferenciaConGuion)
	t.Run("el guion falla: error nombrado", edgeProbarInferenciaFalla)
	t.Run("sin guion: OLLAMA_DOWN", edgeProbarInferenciaSinGuion)
	t.Run("el calentamiento no consulta el guion", edgeProbarCalentamiento)
	t.Run("un guion lento no frena los demás comandos", edgeProbarInferenciaLenta)
	t.Run("las inferencias van de una en una", edgeProbarInferenciasSerializadas)
	t.Run("UNSPECIFIED no viaja", edgeProbarErrorNoEspecificado)
}

// edgePeticion arma un CloudToEdge con una InferenceRequest.
func edgePeticion(id, prompt string, warmup bool, tope *int32) *cloudlinkv1.CloudToEdge {
	return edgeComando(id, "sesion-x", func(cmd *cloudlinkv1.CloudToEdge) {
		cmd.Payload = &cloudlinkv1.CloudToEdge_InferenceRequest{InferenceRequest: &cloudlinkv1.InferenceRequest{
			CommandId: id, Prompt: prompt, Class: "lote", Warmup: warmup, MaxOutputTokens: tope,
		}}
	})
}

// edgeAbrirSalida abre el InferenceResult de un frame con la privada de la nube y devuelve el JSON
// crudo, o falla el test si el frame no es una salida sellada.
func edgeAbrirSalida(t *testing.T, f *cloudlinkv1.EdgeToCloud, k claves) (id, crudo string) {
	t.Helper()
	res := f.GetInferenceResult()
	if res == nil {
		t.Fatalf("el frame es %T, quería un InferenceResult", f.GetPayload())
	}
	sellado := res.GetEncOutput()
	if len(sellado) == 0 {
		t.Fatalf("el InferenceResult no trae salida sellada (error %v)", res.GetError())
	}
	plano, err := envelope.OpenWith(k.NubePriv, sellado)
	if err != nil {
		t.Fatalf("abrir la salida con la privada de la nube: %v", err)
	}
	var salida cloudlinkv1.InferenceOutput
	if err := proto.Unmarshal(plano, &salida); err != nil {
		t.Fatalf("la salida abierta no es un InferenceOutput: %v", err)
	}
	return res.GetCommandId(), salida.GetRawJson()
}

// edgeProbarInferenciaConGuion comprueba que el guion recibe la petición, que su JSON vuelve sellado
// (se abre con NubePriv y reproduce el JSON), con el command_id de la petición y en la sesión del
// comando, y que la petición queda registrada entera.
func edgeProbarInferenciaConGuion(t *testing.T) {
	t.Parallel()
	e, c, k := edgeDePrueba(t)
	edgeGrantLease(t, e, k)
	var visto atomic.Pointer[cloudlinkv1.InferenceRequest]
	e.Inferir = func(req *cloudlinkv1.InferenceRequest) (string, *cloudlinkv1.InferenceError) {
		visto.Store(req)
		return `{"intencion":"presupuesto","ñandú":"€"}`, nil
	}
	tope := int32(512)
	e.manejar(edgePeticion("inf-1", "clasifica esto", false, &tope))

	frames := c.todos()
	if len(frames) != 1 || frames[0].GetSessionId() != "sesion-x" {
		t.Fatalf("frames = %d", len(frames))
	}
	id, crudo := edgeAbrirSalida(t, frames[0], k)
	if id != "inf-1" || crudo != `{"intencion":"presupuesto","ñandú":"€"}` {
		t.Errorf("salida = (%q, %q)", id, crudo)
	}
	if v := visto.Load(); v == nil || v.GetPrompt() != "clasifica esto" || v.GetMaxOutputTokens() != 512 {
		t.Errorf("el guion vio %+v", v)
	}
	reg := e.Inferencias()
	if len(reg) != 1 || reg[0].GetPrompt() != "clasifica esto" || reg[0].GetClass() != "lote" || reg[0].MaxOutputTokens == nil {
		t.Errorf("Inferencias = %+v", reg)
	}
}

// edgeProbarInferenciaFalla comprueba que el error del guion sale como error nombrado, sin salida
// sellada.
func edgeProbarInferenciaFalla(t *testing.T) {
	t.Parallel()
	e, c, k := edgeDePrueba(t)
	edgeGrantLease(t, e, k)
	e.Inferir = func(*cloudlinkv1.InferenceRequest) (string, *cloudlinkv1.InferenceError) {
		return "ignorado", cloudlinkv1.InferenceError_INFERENCE_ERROR_TIMEOUT.Enum()
	}
	e.manejar(edgePeticion("inf-2", "p", false, nil))
	res := c.todos()[0].GetInferenceResult()
	if res.GetCommandId() != "inf-2" || res.GetError() != cloudlinkv1.InferenceError_INFERENCE_ERROR_TIMEOUT || len(res.GetEncOutput()) != 0 {
		t.Errorf("resultado = %+v, quería TIMEOUT sin salida", res)
	}
}

// edgeProbarInferenciaSinGuion comprueba que sin guion se contesta OLLAMA_DOWN.
func edgeProbarInferenciaSinGuion(t *testing.T) {
	t.Parallel()
	e, c, k := edgeDePrueba(t)
	edgeGrantLease(t, e, k)
	e.manejar(edgePeticion("inf-3", "p", false, nil))
	res := c.todos()[0].GetInferenceResult()
	if res.GetCommandId() != "inf-3" || res.GetError() != cloudlinkv1.InferenceError_INFERENCE_ERROR_OLLAMA_DOWN || len(res.GetEncOutput()) != 0 {
		t.Errorf("resultado = %+v, quería OLLAMA_DOWN", res)
	}
}

// edgeProbarCalentamiento comprueba que una petición de calentamiento se contesta con la salida
// vacía sellada SIN llamar al guion (no gasta un paso), y que queda registrada como calentamiento.
func edgeProbarCalentamiento(t *testing.T) {
	t.Parallel()
	e, c, k := edgeDePrueba(t)
	edgeGrantLease(t, e, k)
	var llamadas atomic.Int64
	e.Inferir = func(*cloudlinkv1.InferenceRequest) (string, *cloudlinkv1.InferenceError) {
		llamadas.Add(1)
		return `{"no":"debe verse"}`, nil
	}
	e.manejar(edgePeticion("cal-1", "prefijo", true, nil))
	_, crudo := edgeAbrirSalida(t, c.todos()[0], k)
	if crudo != edgeSalidaCalentamiento || llamadas.Load() != 0 {
		t.Errorf("calentamiento: salida %q con %d llamadas al guion; quería %q y 0", crudo, llamadas.Load(), edgeSalidaCalentamiento)
	}
	if reg := e.Inferencias(); len(reg) != 1 || !reg[0].GetWarmup() {
		t.Errorf("Inferencias = %+v, quería un calentamiento", reg)
	}
}

// edgeProbarInferenciaLenta comprueba, con el camino real del bucle (despachar), que mientras un
// guion está bloqueado el Edge sigue contestando Pings, y que al soltarlo la salida sale.
func edgeProbarInferenciaLenta(t *testing.T) {
	t.Parallel()
	e, c, k := edgeDePrueba(t)
	edgeGrantLease(t, e, k)
	entro, suelta := make(chan struct{}), make(chan struct{})
	e.Inferir = func(*cloudlinkv1.InferenceRequest) (string, *cloudlinkv1.InferenceError) {
		close(entro)
		<-suelta
		return `{"tarde":true}`, nil
	}
	e.despachar(edgePeticion("lenta", "p", false, nil))
	select {
	case <-entro:
	case <-time.After(5 * time.Second):
		t.Fatalf("el guion no llegó a ejecutarse")
	}
	e.despachar(edgeComando("", "sesion-x", func(cmd *cloudlinkv1.CloudToEdge) {
		cmd.Payload = &cloudlinkv1.CloudToEdge_Ping{Ping: &cloudlinkv1.Ping{Nonce: 7}}
	}))
	frames := c.todos()
	if len(frames) != 1 || frames[0].GetPong().GetNonce() != 7 {
		t.Fatalf("con el guion bloqueado, frames = %d; quería solo el Pong", len(frames))
	}
	close(suelta)
	frames = c.esperar(t, 2)
	if _, crudo := edgeAbrirSalida(t, frames[1], k); crudo != `{"tarde":true}` {
		t.Errorf("salida tras soltar el guion = %q", crudo)
	}
	if !edgeEsperarGrupo(&e.enVuelo, 5*time.Second) {
		t.Errorf("la inferencia en vuelo no terminó")
	}
}

// edgeProbarInferenciasSerializadas comprueba que dos peticiones simultáneas no ejecutan el guion a
// la vez (una plaza única, como el Ollama real) y que las dos se contestan.
func edgeProbarInferenciasSerializadas(t *testing.T) {
	t.Parallel()
	e, c, k := edgeDePrueba(t)
	edgeGrantLease(t, e, k)
	var dentro, maximo atomic.Int64
	e.Inferir = func(*cloudlinkv1.InferenceRequest) (string, *cloudlinkv1.InferenceError) {
		n := dentro.Add(1)
		for {
			m := maximo.Load()
			if n <= m || maximo.CompareAndSwap(m, n) {
				break
			}
		}
		// Cede el procesador varias veces dentro del guion, como lo haría el modelo trabajando: si
		// otra inferencia pudiera entrar a la vez, aquí tendría tiempo de hacerlo.
		for range 200 {
			runtime.Gosched()
		}
		dentro.Add(-1)
		return "{}", nil
	}
	for _, id := range []string{"a", "b", "c"} {
		e.despachar(edgePeticion(id, "p", false, nil))
	}
	c.esperar(t, 3)
	if m := maximo.Load(); m != 1 {
		t.Errorf("el guion llegó a correr %d veces a la vez, quería 1", m)
	}
}

// edgeProbarErrorNoEspecificado comprueba que un error UNSPECIFIED, que en el contrato significa
// «no hay error», se manda como OLLAMA_DOWN y no como un resultado sin salida ni error.
func edgeProbarErrorNoEspecificado(t *testing.T) {
	t.Parallel()
	res := edgeErrorInferencia("x", cloudlinkv1.InferenceError_INFERENCE_ERROR_UNSPECIFIED)
	if res.GetError() != cloudlinkv1.InferenceError_INFERENCE_ERROR_OLLAMA_DOWN {
		t.Errorf("error = %v, quería OLLAMA_DOWN", res.GetError())
	}
}
