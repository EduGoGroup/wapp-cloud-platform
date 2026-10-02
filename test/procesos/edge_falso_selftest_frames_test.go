//go:build integracion

package procesos

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-shared/envelope"
	"google.golang.org/protobuf/proto"
)

// Los tests propios, sin servidor, de lo que el Edge de prueba manda y de cómo se le espera: los
// frames salientes (TestArnes_EdgeSaliente), el envío serializado y las esperas.
// Sale de edge_falso_test.go (D-F9-11: solo se movieron declaraciones).

// TestArnes_EdgeSaliente prueba, sin servidor, los frames que el Edge manda por iniciativa del
// test: el latido (contador, READY, sin número propio), el entrante SELLADO (en claro solo from y
// wa_message_id; lo sensible solo se abre con la privada de la nube y reproduce el texto, con el
// número cuando el remitente es un número y sin él cuando es un LID), el acuse en sus dos estados
// y el frame del canal de control.
func TestArnes_EdgeSaliente(t *testing.T) {
	t.Parallel()
	e, c, k := edgeDePrueba(t)

	e.latir(t, 3)
	e.entrante(t, "573001110000@s.whatsapp.net", "quiero 3 cajas de tornillos", "WA-1")
	e.entrante(t, "abc123@lid", "desde un LID", "WA-2")
	e.acuse(t, "WA-OUT-1", false)
	e.acuse(t, "WA-OUT-2", true)
	e.usarCanalControl(t)
	frames := c.esperar(t, 6)

	hb := frames[0].GetHeartbeat()
	if frames[0].GetSessionId() != "sesion-prueba" || hb.GetLeaseCounter() != 3 ||
		hb.GetInferenceReadiness() != cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_READY ||
		hb.GetSelfPn() != "" || hb.GetState() != cloudlinkv1.SessionState_SESSION_STATE_UNSPECIFIED {
		t.Errorf("latido = %+v en %q", hb, frames[0].GetSessionId())
	}

	edgeVerificarEntrante(t, frames[1], k, "573001110000@s.whatsapp.net", "quiero 3 cajas de tornillos", "573001110000", "WA-1")
	edgeVerificarEntrante(t, frames[2], k, "abc123@lid", "desde un LID", "", "WA-2")

	for i, quiere := range []cloudlinkv1.ReceiptStatus{
		cloudlinkv1.ReceiptStatus_RECEIPT_STATUS_DELIVERED, cloudlinkv1.ReceiptStatus_RECEIPT_STATUS_READ,
	} {
		r := frames[3+i].GetReceipt()
		if r.GetStatus() != quiere || r.GetSessionId() != "sesion-prueba" || len(r.GetMessageIds()) != 1 ||
			r.GetMessageIds()[0] != fmt.Sprintf("WA-OUT-%d", i+1) || r.GetTimestamp() == 0 || frames[3+i].GetSessionId() != "sesion-prueba" {
			t.Errorf("acuse %d = %+v", i, r)
		}
	}
	if frames[5].GetSessionId() != edgeSesionControl || frames[5].GetPong() == nil {
		t.Errorf("canal de control: %T en la sesión %q", frames[5].GetPayload(), frames[5].GetSessionId())
	}
}

// edgeVerificarEntrante comprueba un frame de entrante sellado: lo único que viaja en claro es from,
// wa_message_id y el instante (los planos sensibles, vacíos), trae enc_payload, y abierto con la
// privada de la nube reproduce el texto y el número esperados.
func edgeVerificarEntrante(t *testing.T, f *cloudlinkv1.EdgeToCloud, k claves, de, texto, numero, waID string) {
	t.Helper()
	m := f.GetIncoming()
	if m == nil {
		t.Fatalf("el frame es %T, quería un IncomingMessage", f.GetPayload())
	}
	if m.GetFrom() != de || m.GetWaMessageId() != waID || m.GetTsUnix() == 0 {
		t.Errorf("campos en claro = from %q, wa_message_id %q, ts %d", m.GetFrom(), m.GetWaMessageId(), m.GetTsUnix())
	}
	if m.GetText() != "" || m.GetPushName() != "" || m.GetFromPn() != "" || m.GetFromLid() != "" {
		t.Errorf("lo sensible viaja en claro: text %q push_name %q from_pn %q from_lid %q",
			m.GetText(), m.GetPushName(), m.GetFromPn(), m.GetFromLid())
	}
	if len(m.GetEncPayload()) == 0 || strings.Contains(string(m.GetEncPayload()), texto) {
		t.Fatalf("enc_payload vacío o con el texto a la vista")
	}
	sp := edgeAbrirSensible(t, m.GetEncPayload(), k)
	if sp.GetText() != texto || sp.GetFromPn() != numero || sp.GetPushName() != "" || sp.GetFromLid() != "" {
		t.Errorf("SensitivePayload = %+v, quería texto %q y número %q", sp, texto, numero)
	}
}

// edgeAbrirSensible abre un enc_payload con la privada de la nube y lo interpreta como
// SensitivePayload. Falla el test (t.Fatalf) si no se abre o no se interpreta.
func edgeAbrirSensible(t *testing.T, sellado []byte, k claves) *cloudlinkv1.SensitivePayload {
	t.Helper()
	plano, err := envelope.OpenWith(k.NubePriv, sellado)
	if err != nil {
		t.Fatalf("abrir enc_payload con la privada de la nube: %v", err)
	}
	sp := &cloudlinkv1.SensitivePayload{}
	if err := proto.Unmarshal(plano, sp); err != nil {
		t.Fatalf("enc_payload abierto no es un SensitivePayload: %v", err)
	}
	return sp
}

// TestArnes_EdgeEnvioSerializado prueba que el Edge nunca llama a la salida desde dos goroutines a
// la vez (el Send de gRPC no lo admite): 16 goroutines emiten 50 frames cada una y la salida, que
// detecta el solape, no ve ninguno. Bajo -race caza además cualquier acceso sin candado.
func TestArnes_EdgeEnvioSerializado(t *testing.T) {
	t.Parallel()
	e, _, _ := edgeDePrueba(t)
	var dentro, solapes, total atomic.Int64
	e.salida = func(*cloudlinkv1.EdgeToCloud) error {
		if dentro.Add(1) > 1 {
			solapes.Add(1)
		}
		total.Add(1)
		dentro.Add(-1)
		return nil
	}
	var espera sync.WaitGroup
	for range 16 {
		espera.Go(func() {
			for n := range 50 {
				if err := e.emitir(edgeLatido("s", int64(n))); err != nil {
					t.Errorf("emitir: %v", err)
					return
				}
			}
		})
	}
	espera.Wait()
	if solapes.Load() != 0 || total.Load() != 16*50 {
		t.Errorf("solapes = %d, emitidos = %d; quería 0 y %d", solapes.Load(), total.Load(), 16*50)
	}
}

// TestArnes_EdgeEsperas prueba las esperas del Edge sin servidor: el texto que ya está llega, el que
// no llega agota el tope con un error, un contexto cancelado corta la espera, esperarConfig y
// esperarLeases encuentran lo que ya está (un lease rechazado también cuenta), y edgeSondear
// devuelve falso al agotar su contexto. El Edge tiene un lease vigente: sin él no habría texto que
// esperar.
func TestArnes_EdgeEsperas(t *testing.T) {
	t.Parallel()
	e, _, k := edgeDePrueba(t)
	edgeGrantLease(t, e, k)

	if cap(e.Textos()) != edgeBuferTextos {
		t.Errorf("el canal de textos tiene capacidad %d, quería %d", cap(e.Textos()), edgeBuferTextos)
	}
	if _, err := e.recibirTexto(t.Context(), 30*time.Millisecond); err == nil {
		t.Errorf("sin texto, recibirTexto debía fallar por el tope")
	}
	cancelado, cancelar := context.WithCancel(t.Context())
	cancelar()
	if _, err := e.recibirTexto(cancelado, time.Minute); !errors.Is(err, context.Canceled) {
		t.Errorf("con el contexto cancelado, recibirTexto = %v, quería context.Canceled", err)
	}

	e.manejar(edgeComando("c-1", "s", func(cmd *cloudlinkv1.CloudToEdge) {
		cmd.Payload = &cloudlinkv1.CloudToEdge_SendText{SendText: &cloudlinkv1.SendText{To: "x", Text: "y"}}
	}))
	e.manejar(edgeComando("cfg-1", "s", func(cmd *cloudlinkv1.CloudToEdge) {
		cmd.Payload = &cloudlinkv1.CloudToEdge_ConfigUpdate{ConfigUpdate: &cloudlinkv1.ConfigUpdate{CommandId: "cfg-1", Kind: "jwks", Version: "v"}}
	}))
	if txt := e.esperarTexto(t, time.Second); txt.Texto != "y" {
		t.Errorf("esperarTexto = %+v", txt)
	}
	if c := e.esperarConfig(t, "jwks", time.Second); c.Version != "v" {
		t.Errorf("esperarConfig = %+v", c)
	}
	e.esperarLeases(t, 1, time.Second) // el vigente de edgeGrantLease
	e.alRecibirLease(&cloudlinkv1.LeaseUpdate{})
	e.esperarLeases(t, 2, time.Second) // y el vacío, que el Validator rechaza pero cuenta

	corto, cancelarCorto := context.WithTimeout(t.Context(), 40*time.Millisecond)
	defer cancelarCorto()
	if edgeSondear(corto, func() bool { return false }) {
		t.Errorf("edgeSondear devolvió verdadero con una condición siempre falsa")
	}
}
