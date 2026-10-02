//go:build integracion

package procesos

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	cllease "github.com/EduGoGroup/wapp-cloudlink/lease"
)

// Los casos de TestArnes_EdgeNucleo sobre los envíos, sin servidor: SendText y SendMedia con lease
// vigente, y el gate de lease que los bloquea cuando no lo hay, se rechazó, venció o se revocó.
// Sale de edge_falso_test.go (D-F9-11: solo se movieron declaraciones).

// edgeProbarSendText comprueba que, con un lease vigente, el SendText llega al canal con destino,
// texto y command_id, que el Ack sale con ok=true, sin error, con el mismo command_id y el
// session_id del comando, y que cuando el Ack sale el texto ya está en el canal.
func edgeProbarSendText(t *testing.T) {
	t.Parallel()
	e, c, k := edgeDePrueba(t)
	edgeGrantLease(t, e, k)
	var textoYaEnElCanal atomic.Bool
	e.salida = func(m *cloudlinkv1.EdgeToCloud) error {
		textoYaEnElCanal.Store(len(e.textos) == 1)
		return c.recoger(m)
	}
	e.manejar(edgeComando("cmd-1", "sesion-x", func(cmd *cloudlinkv1.CloudToEdge) {
		cmd.Payload = &cloudlinkv1.CloudToEdge_SendText{SendText: &cloudlinkv1.SendText{To: "573001110000", Text: "hola"}}
	}))

	select {
	case txt := <-e.Textos():
		if (txt != textoRecibido{A: "573001110000", Texto: "hola", ComandoID: "cmd-1"}) {
			t.Errorf("texto recibido = %+v", txt)
		}
	default:
		t.Fatalf("el SendText no llegó a Textos()")
	}
	frames := c.todos()
	if len(frames) != 1 {
		t.Fatalf("frames emitidos = %d, quería 1 (el Ack)", len(frames))
	}
	ack := edgeAckDe(t, frames[0])
	if ack.GetAckedCommandId() != "cmd-1" || !ack.GetOk() || ack.GetError() != "" || frames[0].GetSessionId() != "sesion-x" {
		t.Errorf("Ack = %+v en la sesión %q, quería ok de cmd-1, sin error, en sesion-x", ack, frames[0].GetSessionId())
	}
	if !textoYaEnElCanal.Load() {
		t.Errorf("el Ack salió antes de publicar el texto: un 200 HTTP no garantizaría verlo en el canal")
	}
}

// edgeOperateCommands son los dos comandos «de operar» del contrato, los que el gate de lease
// cubre, cada uno con un constructor de su payload.
var edgeOperateCommands = []struct {
	name    string
	payload func(*cloudlinkv1.CloudToEdge)
}{
	{"SendText", func(cmd *cloudlinkv1.CloudToEdge) {
		cmd.Payload = &cloudlinkv1.CloudToEdge_SendText{SendText: &cloudlinkv1.SendText{To: "573001110000", Text: "hola"}}
	}},
	{"SendMedia", func(cmd *cloudlinkv1.CloudToEdge) {
		cmd.Payload = &cloudlinkv1.CloudToEdge_SendMedia{SendMedia: &cloudlinkv1.SendMedia{To: "573001110000"}}
	}},
}

// edgeCheckBlocked manda al Edge un SendText y un SendMedia con command_id y comprueba que NINGUNO
// «sale»: cada uno se contesta con un solo Ack{ok=false, error=«lease no vigente»} de su command_id
// en la sesión del comando, y el canal de textos sigue vacío. Recibe la etapa (para los mensajes y
// para que los command_id no se repitan). Falla el test con t.Errorf por cada incumplimiento.
func edgeCheckBlocked(t *testing.T, e *edge, c *edgeColector, stage string) {
	t.Helper()
	for _, oc := range edgeOperateCommands {
		before := len(c.todos())
		id := stage + "-" + oc.name
		e.manejar(edgeComando(id, "sesion-x", oc.payload))
		frames := c.todos()[before:]
		if len(frames) != 1 {
			t.Errorf("%s, %s: %d frames emitidos, quería 1 (el Ack de rechazo)", stage, oc.name, len(frames))
			continue
		}
		ack := edgeAckDe(t, frames[0])
		if ack.GetAckedCommandId() != id || ack.GetOk() || ack.GetError() != edgeLeaseNotValidText || frames[0].GetSessionId() != "sesion-x" {
			t.Errorf("%s, %s: Ack = %+v en la sesión %q; quería ok=false con error %q de %s en sesion-x",
				stage, oc.name, ack, frames[0].GetSessionId(), edgeLeaseNotValidText, id)
		}
	}
	if n := len(e.Textos()); n != 0 {
		t.Errorf("%s: hay %d textos en el canal; sin lease vigente el Edge no entrega ninguno", stage, n)
	}
}

// edgeCheckLeaseGate recorre la vida del lease y comprueba el gate en cada etapa, igual que el Edge
// real (handleSendText/handleSendMedia de wapp-edge-agent): sin ningún lease todavía, bloquea; con
// uno que el Validator rechazó por firma ajena, bloquea; con uno vigente, el SendText se publica y
// los dos comandos se acusan ok=true; tras la revocación, bloquea; y un lease vigente posterior no
// lo reabre (la revocación es pegajosa). El texto del rechazo es literalmente «lease no vigente», y
// un comando bloqueado sin command_id no emite nada. Los bloqueos no se anotan en Errores.
func edgeCheckLeaseGate(t *testing.T) {
	t.Parallel()
	e, c, k := edgeDePrueba(t)
	iss := edgeEmisorLease(t, k)
	foreign := edgeEmisorLease(t, nuevasClaves(t))
	apply := func(lu *cloudlinkv1.LeaseUpdate, err error) {
		t.Helper()
		e.manejar(edgeLeaseCommand(t, e, lu, err))
	}
	if edgeLeaseNotValidText != "lease no vigente" {
		t.Fatalf("el texto del rechazo es %q: debe ser el del Edge real, «lease no vigente»", edgeLeaseNotValidText)
	}

	edgeCheckBlocked(t, e, c, "sin-lease")
	before := len(c.todos())
	e.manejar(edgeComando("", "sesion-x", edgeOperateCommands[0].payload))
	if n := len(c.todos()) - before; n != 0 || len(e.Textos()) != 0 {
		t.Errorf("un SendText bloqueado sin command_id emitió %d frames y dejó %d textos; quería 0 y 0", n, len(e.Textos()))
	}

	apply(foreign.Issue(e.EdgeID, e.TenantID, time.Hour, 1))
	edgeCheckBlocked(t, e, c, "lease-rechazado")

	apply(iss.Issue(e.EdgeID, e.TenantID, time.Hour, 2))
	before = len(c.todos())
	for _, oc := range edgeOperateCommands {
		e.manejar(edgeComando("vigente-"+oc.name, "sesion-x", oc.payload))
	}
	frames := c.todos()[before:]
	if len(frames) != len(edgeOperateCommands) {
		t.Fatalf("con lease vigente: %d frames, quería %d Ack", len(frames), len(edgeOperateCommands))
	}
	for i, oc := range edgeOperateCommands {
		if ack := edgeAckDe(t, frames[i]); ack.GetAckedCommandId() != "vigente-"+oc.name || !ack.GetOk() || ack.GetError() != "" {
			t.Errorf("con lease vigente, %s: Ack = %+v, quería ok=true sin error", oc.name, ack)
		}
	}
	if txt, err := e.recibirTexto(t.Context(), time.Second); err != nil || txt.ComandoID != "vigente-SendText" {
		t.Errorf("con lease vigente el SendText debía llegar al canal: %+v, %v", txt, err)
	}

	apply(iss.Revoke(e.EdgeID, e.TenantID))
	edgeCheckBlocked(t, e, c, "revocado")
	apply(iss.Issue(e.EdgeID, e.TenantID, time.Hour, 9))
	edgeCheckBlocked(t, e, c, "revocado-y-renovado")

	if errs := e.Errores(); len(errs) != 1 || !errors.Is(errs[0], cllease.ErrBadSignature) {
		t.Errorf("Errores = %v, quería solo el lease de firma ajena (un bloqueo no es un error del núcleo)", errs)
	}
}

// edgeCheckLeaseGateExpired comprueba que el gate mira la vigencia y no solo «hubo un lease»: un
// lease bien firmado y aceptado por el Validator, pero ya vencido, no deja entregar.
func edgeCheckLeaseGateExpired(t *testing.T) {
	t.Parallel()
	e, c, k := edgeDePrueba(t)
	lu, err := edgeEmisorLease(t, k).Issue(e.EdgeID, e.TenantID, -time.Minute, edgeContadorInicial)
	e.manejar(edgeLeaseCommand(t, e, lu, err))
	if errs := e.Errores(); len(errs) != 0 || e.Leases() != 1 || e.revocado() {
		t.Fatalf("el lease vencido debía aceptarse sin más: errores %v, leases %d, revocado %v", errs, e.Leases(), e.revocado())
	}
	if e.puedeOperar() {
		t.Fatalf("con un lease vencido el Edge dice que puede operar")
	}
	edgeCheckBlocked(t, e, c, "vencido")
}

// edgeCheckSendMediaWithLease comprueba el SendMedia con lease vigente: se acusa ok=true, sin
// error, con su command_id y en la sesión del comando; sin command_id no se acusa; y no deja nada
// en el canal de textos (el doble no registra el archivo).
func edgeCheckSendMediaWithLease(t *testing.T) {
	t.Parallel()
	e, c, k := edgeDePrueba(t)
	edgeGrantLease(t, e, k)
	media := edgeOperateCommands[1].payload
	e.manejar(edgeComando("", "sesion-x", media))
	if n := len(c.todos()); n != 0 {
		t.Fatalf("un SendMedia sin command_id emitió %d frames", n)
	}
	e.manejar(edgeComando("media-1", "sesion-x", media))
	frames := c.todos()
	if len(frames) != 1 {
		t.Fatalf("frames = %d, quería 1", len(frames))
	}
	if a := edgeAckDe(t, frames[0]); a.GetAckedCommandId() != "media-1" || !a.GetOk() || a.GetError() != "" || frames[0].GetSessionId() != "sesion-x" {
		t.Errorf("Ack = %+v en la sesión %q, quería ok de media-1 en sesion-x", a, frames[0].GetSessionId())
	}
	if n := len(e.Textos()); n != 0 {
		t.Errorf("el SendMedia dejó %d textos en el canal", n)
	}
}
