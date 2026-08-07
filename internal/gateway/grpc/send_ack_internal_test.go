package gatewaygrpc

import (
	"context"
	"io"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
)

// senderFunc adapta una función al contrato session.Sender.
type senderFunc func(*cloudlinkv1.CloudToEdge) error

func (f senderFunc) Send(msg *cloudlinkv1.CloudToEdge) error { return f(msg) }

// TestAwaitAckDevuelveElAckDelEdge: el reloj del ack NO puede comerse un acuse
// legítimo. Es la otra mitad del test de regresión del cuelgue: acotar la espera
// solo sirve si el camino feliz sigue intacto (en el e2e real, 0,88s).
//
// Es un test INTERNO porque necesita entregar el Ack por deliverAck, que es el
// mismo camino que recorre un frame que llega por el stream (connect.go, route);
// exponerlo en la API pública solo para el test sería peor.
func TestAwaitAckDevuelveElAckDelEdge(t *testing.T) {
	t.Parallel()
	reg := session.NewRegistry()
	srv := New(reg, logger.New(logger.WithWriter(io.Discard)), WithAckTimeout(5*time.Second))

	// El Edge acusa en cuanto recibe, correlacionando por command_id como el real.
	release := reg.Register("s-viva", senderFunc(func(msg *cloudlinkv1.CloudToEdge) error {
		go srv.deliverAck(&cloudlinkv1.Ack{AckedCommandId: msg.GetCommandId(), Ok: true})
		return nil
	}))
	defer release()

	ack, err := srv.SendText(context.Background(), "s-viva", "57301", "hola")
	if err != nil {
		t.Fatalf("SendText contra un Edge que acusa: %v", err)
	}
	if !ack.GetOk() || ack.GetAckedCommandId() == "" {
		t.Fatalf("ack = %+v, quiero ok=true con acked_command_id", ack)
	}
}

// TestNewMaterializaElAckTimeout: el camino caliente NUNCA queda sin reloj. Un
// Server construido sin la opción —o con un valor absurdo— cae al default en vez
// de quedarse en cero, que es lo que reintroduciría la espera infinita.
func TestNewMaterializaElAckTimeout(t *testing.T) {
	t.Parallel()
	log := logger.New(logger.WithWriter(io.Discard))

	for _, tc := range []struct {
		nombre string
		opts   []Option
		quiero time.Duration
	}{
		{"sin opción", nil, defaultAckTimeout},
		{"cero explícito", []Option{WithAckTimeout(0)}, defaultAckTimeout},
		{"negativo", []Option{WithAckTimeout(-time.Second)}, defaultAckTimeout},
		{"configurado", []Option{WithAckTimeout(2 * time.Second)}, 2 * time.Second},
	} {
		t.Run(tc.nombre, func(t *testing.T) {
			t.Parallel()
			if got := New(session.NewRegistry(), log, tc.opts...).ackTimeout; got != tc.quiero {
				t.Fatalf("ackTimeout = %v, quiero %v", got, tc.quiero)
			}
		})
	}
}

// TestAckTimeoutPorDebajoDelWriteTimeoutHTTP fija la INVARIANTE que hace que todo
// esto sirva: el 504 tiene que poder ESCRIBIRSE. En Go el WriteTimeout del
// http.Server (10s, internal/bootstrap/http.go) no interrumpe al handler ni cancela
// su contexto — solo hace fallar el Write posterior. Si el default del ack lo
// igualara o superara, el 504 se generaría con el deadline de escritura ya vencido
// y el cliente seguiría viendo una conexión cerrada sin cuerpo: exactamente el
// síntoma del 2026-08-06, pero con más código.
//
// Si alguien sube este default, tiene que subir el WriteTimeout en el mismo cambio.
func TestAckTimeoutPorDebajoDelWriteTimeoutHTTP(t *testing.T) {
	t.Parallel()
	// Valor de internal/bootstrap/http.go (no importable: es otro paquete y la
	// constante no está exportada; se replica aquí como aserción explícita).
	const httpWriteTimeout = 10 * time.Second
	if defaultAckTimeout >= httpWriteTimeout {
		t.Fatalf("defaultAckTimeout=%v >= WriteTimeout HTTP=%v: el 504 no llegaría a escribirse",
			defaultAckTimeout, httpWriteTimeout)
	}
}
