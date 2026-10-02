//go:build integracion

package procesos

import (
	"crypto/ed25519"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	cllease "github.com/EduGoGroup/wapp-cloudlink/lease"
)

// Lo que comparten los tests propios del Edge de prueba que corren SIN servidor: el colector que
// hace de stream, el Edge armado a mano y las ayudas para emitirle leases y comandos.
// Sale de edge_falso_test.go (D-F9-11: solo se movieron declaraciones).

// ---------------------------------------------------------------------------------------------
// Tests del núcleo (sin servidor)
// ---------------------------------------------------------------------------------------------

// edgeColector recoge los frames que el núcleo emite, en lugar del stream. Un fallo programado
// hace que la salida devuelva ese error.
type edgeColector struct {
	mu     sync.Mutex
	frames []*cloudlinkv1.EdgeToCloud
	fallo  error
}

// recoger es la salida del núcleo en los tests: guarda el frame o devuelve el fallo programado.
func (c *edgeColector) recoger(m *cloudlinkv1.EdgeToCloud) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fallo != nil {
		return c.fallo
	}
	c.frames = append(c.frames, m)
	return nil
}

// todos devuelve una copia de los frames recogidos, en orden.
func (c *edgeColector) todos() []*cloudlinkv1.EdgeToCloud {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.frames)
}

// esperar espera, con tope de 5 s, a que haya al menos n frames y devuelve todos los recogidos.
func (c *edgeColector) esperar(t *testing.T, n int) []*cloudlinkv1.EdgeToCloud {
	t.Helper()
	edgeEsperar(t, 5*time.Second, fmt.Sprintf("%d frames emitidos por el Edge", n), func() bool { return len(c.todos()) >= n })
	return c.todos()
}

// edgeDePrueba arma un Edge sin servidor: con las claves de una corrida y una salida que apunta a un
// colector. Devuelve el Edge, el colector y las claves (para emitir leases y abrir sobres).
func edgeDePrueba(t *testing.T) (*edge, *edgeColector, claves) {
	t.Helper()
	k := nuevasClaves(t)
	e := nuevoEdge("11111111-1111-4111-8111-111111111111", "edge-prueba", "sesion-prueba", k.NubePub, k.LeasePub)
	c := &edgeColector{}
	e.salida = c.recoger
	return e, c, k
}

// edgeEmisorLease devuelve el emisor de leases del servidor de las claves dadas (el que firma con
// la semilla Ed25519).
func edgeEmisorLease(t *testing.T, k claves) *cllease.Issuer {
	t.Helper()
	semilla := clavesDecodificar(t, "LeaseSeedB64", k.LeaseSeedB64, ed25519.SeedSize)
	iss, err := cllease.NewIssuer(ed25519.NewKeyFromSeed(semilla))
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	return iss
}

// edgeLeaseCommand envuelve un LeaseUpdate en el CloudToEdge con el que el servidor lo manda a la
// sesión del Edge (sin command_id). Falla el test (t.Fatalf) si err, el de haberlo emitido, no es
// nil: está pensada para recibir directamente lo que devuelve Issuer.Issue o Issuer.Revoke.
func edgeLeaseCommand(t *testing.T, e *edge, lu *cloudlinkv1.LeaseUpdate, err error) *cloudlinkv1.CloudToEdge {
	t.Helper()
	if err != nil {
		t.Fatalf("emitir el lease: %v", err)
	}
	return edgeComando("", e.SessionID, func(cmd *cloudlinkv1.CloudToEdge) {
		cmd.Payload = &cloudlinkv1.CloudToEdge_LeaseUpdate{LeaseUpdate: lu}
	})
}

// edgeGrantLease deja a un Edge sin servidor como lo deja conectar contra uno de verdad: con un
// lease vigente (una hora, contador 1) firmado con las claves dadas y aplicado por el Validator.
// Sin esto el Edge no «entrega» nada (blockedByLease) ni sirve inferencia
// (inferenceBlockedByLease). Falla el test (t.Fatalf) si tras aplicarlo el Edge no puede operar.
func edgeGrantLease(t *testing.T, e *edge, k claves) {
	t.Helper()
	lu, err := edgeEmisorLease(t, k).Issue(e.EdgeID, e.TenantID, time.Hour, edgeContadorInicial)
	e.manejar(edgeLeaseCommand(t, e, lu, err))
	if !e.puedeOperar() {
		t.Fatalf("edgeGrantLease: con un lease vigente recién aplicado el Edge no puede operar (errores: %v)", e.Errores())
	}
}

// edgeComando envuelve un payload en un CloudToEdge con el command_id y el session_id dados.
func edgeComando(id, sesion string, payload func(*cloudlinkv1.CloudToEdge)) *cloudlinkv1.CloudToEdge {
	cmd := &cloudlinkv1.CloudToEdge{CommandId: id, SessionId: sesion}
	payload(cmd)
	return cmd
}

// edgeAckDe extrae el Ack de un frame, o falla el test si el frame es de otro tipo.
func edgeAckDe(t *testing.T, f *cloudlinkv1.EdgeToCloud) *cloudlinkv1.Ack {
	t.Helper()
	ack := f.GetAck()
	if ack == nil {
		t.Fatalf("el frame es %T, quería un Ack", f.GetPayload())
	}
	return ack
}
