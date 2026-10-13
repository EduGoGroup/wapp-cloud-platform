//go:build integracion

package procesos

import (
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	cllease "github.com/EduGoGroup/wapp-cloudlink/lease"
)

// Los casos de TestArnes_EdgeInferencia sobre el gate de lease, sin servidor: sin lease vigente el
// Edge de prueba contesta LEASE_INVALID agotada la gracia, y sirve si el lease llega dentro de ella.
// Sale de edge_falso_test.go (D-F9-11: solo se movieron declaraciones).

// edgeInferenceTestGrace es la gracia corta con la que los tests del núcleo recorren el gate de
// lease de la inferencia sin pagar los 2 s del Edge real en cada bloqueo.
const edgeInferenceTestGrace = 100 * time.Millisecond

// edgeCheckInferenceBlocked manda al Edge una inferencia normal y un calentamiento y comprueba que
// NINGUNO se sirve: cada uno se contesta con un solo frame, un InferenceResult con el command_id de
// la petición, INFERENCE_ERROR_LEASE_INVALID y sin salida sellada, en la sesión del comando; no es
// un Ack. Recibe la etapa (para los mensajes y para que los command_id no se repitan). Falla el
// test con t.Errorf por cada incumplimiento.
func edgeCheckInferenceBlocked(t *testing.T, e *edge, c *edgeColector, stage string) {
	t.Helper()
	for _, warmup := range []bool{false, true} {
		before := len(c.todos())
		id := fmt.Sprintf("%s-warmup-%v", stage, warmup)
		e.manejar(edgePeticion(id, "p", warmup, nil))
		frames := c.todos()[before:]
		if len(frames) != 1 {
			t.Errorf("%s, warmup=%v: %d frames emitidos, quería 1 (el InferenceResult de rechazo)", stage, warmup, len(frames))
			continue
		}
		res := frames[0].GetInferenceResult()
		if res == nil {
			t.Errorf("%s, warmup=%v: el frame es %T, quería un InferenceResult (la inferencia no se acusa con Ack)", stage, warmup, frames[0].GetPayload())
			continue
		}
		if res.GetCommandId() != id || res.GetError() != cloudlinkv1.InferenceError_INFERENCE_ERROR_LEASE_INVALID ||
			len(res.GetEncOutput()) != 0 || frames[0].GetSessionId() != "sesion-x" {
			t.Errorf("%s, warmup=%v: InferenceResult de %q con error %v y %d bytes de salida sellada, en la sesión %q; quería LEASE_INVALID de %s, sin salida, en sesion-x",
				stage, warmup, res.GetCommandId(), res.GetError(), len(res.GetEncOutput()), frames[0].GetSessionId(), id)
		}
	}
}

// edgeCheckInferenceLeaseGate recorre la vida del lease y comprueba el gate de la inferencia en cada
// etapa, igual que el Edge real (carrilInferencia.leaseVigente de wapp-edge-agent): sin ningún lease
// todavía, bloquea; con uno que el Validator rechazó por firma ajena, bloquea; con uno vigente, la
// inferencia consulta el guion y sale sellada, y el calentamiento sale; tras la revocación, bloquea;
// y un lease vigente posterior no lo reabre (la revocación es pegajosa). El guion solo se consulta
// la vez que se sirvió, las peticiones bloqueadas quedan registradas, y los bloqueos no se anotan
// en Errores. La gracia y el sondeo por defecto son literalmente los del Edge real.
func edgeCheckInferenceLeaseGate(t *testing.T) {
	t.Parallel()
	e, c, k := edgeDePrueba(t)
	e.inferenceLeaseGrace = edgeInferenceTestGrace
	iss := edgeEmisorLease(t, k)
	foreign := edgeEmisorLease(t, nuevasClaves(t))
	apply := func(lu *cloudlinkv1.LeaseUpdate, err error) {
		t.Helper()
		e.manejar(edgeLeaseCommand(t, e, lu, err))
	}
	if edgeInferenceLeaseGrace != 2000*time.Millisecond || edgeInferenceLeasePoll != 50*time.Millisecond {
		t.Fatalf("la gracia y el sondeo por defecto son %s y %s: deben ser los del Edge real, 2 s y 50 ms",
			edgeInferenceLeaseGrace, edgeInferenceLeasePoll)
	}
	var calls atomic.Int64
	e.Inferir = func(*cloudlinkv1.InferenceRequest) (string, *cloudlinkv1.InferenceError) {
		calls.Add(1)
		return `{"servida":true}`, nil
	}

	edgeCheckInferenceBlocked(t, e, c, "sin-lease")
	apply(foreign.Issue(e.EdgeID, e.TenantID, time.Hour, 1))
	edgeCheckInferenceBlocked(t, e, c, "lease-rechazado")

	apply(iss.Issue(e.EdgeID, e.TenantID, time.Hour, 2))
	before := len(c.todos())
	e.manejar(edgePeticion("vigente", "p", false, nil))
	e.manejar(edgePeticion("vigente-calentamiento", "p", true, nil))
	frames := c.todos()[before:]
	if len(frames) != 2 {
		t.Fatalf("con lease vigente: %d frames, quería 2 InferenceResult", len(frames))
	}
	if id, raw := edgeAbrirSalida(t, frames[0], k); id != "vigente" || raw != `{"servida":true}` {
		t.Errorf("con lease vigente, la inferencia = (%q, %q); quería la salida del guion", id, raw)
	}
	if id, raw := edgeAbrirSalida(t, frames[1], k); id != "vigente-calentamiento" || raw != edgeSalidaCalentamiento {
		t.Errorf("con lease vigente, el calentamiento = (%q, %q); quería %q", id, raw, edgeSalidaCalentamiento)
	}

	apply(iss.Revoke(e.EdgeID, e.TenantID))
	edgeCheckInferenceBlocked(t, e, c, "revocado")
	apply(iss.Issue(e.EdgeID, e.TenantID, time.Hour, 9))
	edgeCheckInferenceBlocked(t, e, c, "revocado-y-renovado")

	if n := calls.Load(); n != 1 {
		t.Errorf("el guion se consultó %d veces, quería 1: una inferencia bloqueada no llega al guion", n)
	}
	if n := len(e.Inferencias()); n != 10 {
		t.Errorf("Inferencias registra %d peticiones, quería 10 (las 8 bloqueadas también: la nube las pidió)", n)
	}
	if errs := e.Errores(); len(errs) != 1 || !errors.Is(errs[0], cllease.ErrBadSignature) {
		t.Errorf("Errores = %v, quería solo el lease de firma ajena (un bloqueo no es un error del núcleo)", errs)
	}
}

// edgeCheckInferenceLeaseGateExpired comprueba que el gate de la inferencia mira la vigencia y no
// solo «hubo un lease»: con un lease bien firmado y aceptado por el Validator, pero ya vencido, no
// se infiere.
func edgeCheckInferenceLeaseGateExpired(t *testing.T) {
	t.Parallel()
	e, c, k := edgeDePrueba(t)
	e.inferenceLeaseGrace = edgeInferenceTestGrace
	lu, err := edgeEmisorLease(t, k).Issue(e.EdgeID, e.TenantID, -time.Minute, edgeContadorInicial)
	e.manejar(edgeLeaseCommand(t, e, lu, err))
	if errs := e.Errores(); len(errs) != 0 || e.puedeOperar() {
		t.Fatalf("el lease vencido debía aceptarse y no dejar operar: errores %v, puedeOperar %v", errs, e.puedeOperar())
	}
	edgeCheckInferenceBlocked(t, e, c, "vencido")
}

// edgeCheckInferenceLeaseGrace comprueba la gracia del gate, con el camino real del bucle
// (despachar) y sin depender de cuánto tarde la máquina: (a) el rechazo no sale antes de agotar la
// gracia; (b) una petición que llega ANTES que el lease —la ventana del arranque— espera y, en
// cuanto el lease es vigente, se sirve (con una gracia de 30 s: si el gate no sondeara, el test
// agotaría su espera en vez de pasar por suerte); y (c) cerrar el enlace corta la espera, y la
// petición se contesta LEASE_INVALID sin anotar errores.
func edgeCheckInferenceLeaseGrace(t *testing.T) {
	t.Parallel()
	const long = 30 * time.Second

	t.Run("el rechazo espera la gracia entera", func(t *testing.T) {
		t.Parallel()
		e, c, _ := edgeDePrueba(t)
		e.inferenceLeaseGrace = 3 * edgeInferenceTestGrace
		start := time.Now()
		e.manejar(edgePeticion("espera", "p", false, nil))
		if d := time.Since(start); d < e.inferenceLeaseGrace {
			t.Errorf("el rechazo salió a los %s, antes de agotar la gracia de %s", d, e.inferenceLeaseGrace)
		}
		if res := c.esperar(t, 1)[0].GetInferenceResult(); res.GetError() != cloudlinkv1.InferenceError_INFERENCE_ERROR_LEASE_INVALID {
			t.Errorf("resultado = %+v, quería LEASE_INVALID", res)
		}
	})

	t.Run("el lease llega dentro de la gracia: se sirve", func(t *testing.T) {
		t.Parallel()
		e, c, k := edgeDePrueba(t)
		e.inferenceLeaseGrace = long
		e.Inferir = func(*cloudlinkv1.InferenceRequest) (string, *cloudlinkv1.InferenceError) {
			return `{"a tiempo":true}`, nil
		}
		e.despachar(edgePeticion("temprana", "p", false, nil))
		edgeEsperar(t, 5*time.Second, "la petición registrada", func() bool { return len(e.Inferencias()) == 1 })
		if n := len(c.todos()); n != 0 {
			t.Fatalf("sin lease y dentro de la gracia el Edge ya emitió %d frames", n)
		}
		edgeGrantLease(t, e, k)
		if id, raw := edgeAbrirSalida(t, c.esperar(t, 1)[0], k); id != "temprana" || raw != `{"a tiempo":true}` {
			t.Errorf("salida = (%q, %q); quería la del guion", id, raw)
		}
		if !edgeEsperarGrupo(&e.enVuelo, 5*time.Second) {
			t.Errorf("la inferencia en vuelo no terminó")
		}
	})

	t.Run("cerrar el enlace corta la espera", func(t *testing.T) {
		t.Parallel()
		e, c, _ := edgeDePrueba(t)
		e.inferenceLeaseGrace = long
		e.despachar(edgePeticion("al-cerrar", "p", false, nil))
		edgeEsperar(t, 5*time.Second, "la petición registrada", func() bool { return len(e.Inferencias()) == 1 })
		if err := e.cerrarEnlace(); err != nil {
			t.Fatalf("cerrarEnlace: %v", err)
		}
		if !edgeEsperarGrupo(&e.enVuelo, 5*time.Second) {
			t.Fatalf("la inferencia siguió esperando su gracia de %s tras empezar el cierre", long)
		}
		if res := c.esperar(t, 1)[0].GetInferenceResult(); res.GetCommandId() != "al-cerrar" || res.GetError() != cloudlinkv1.InferenceError_INFERENCE_ERROR_LEASE_INVALID {
			t.Errorf("resultado = %+v, quería LEASE_INVALID de al-cerrar", res)
		}
		if errs := e.Errores(); len(errs) != 0 {
			t.Errorf("Errores = %v; cortar la gracia al cerrar no es un error del núcleo", errs)
		}
	})
}
