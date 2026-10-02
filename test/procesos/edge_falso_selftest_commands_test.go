//go:build integracion

package procesos

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	cllease "github.com/EduGoGroup/wapp-cloudlink/lease"
)

// TestArnes_EdgeNucleo, sin servidor: qué hace el Edge de prueba con cada comando que no es un envío
// (config, diagnóstico, ping, leases, comando desconocido, fallo de la salida, canal lleno, copias).
// Los casos de envío están en edge_falso_selftest_send_test.go.
// Sale de edge_falso_test.go (D-F9-11: solo se movieron declaraciones).

// TestArnes_EdgeNucleo prueba, sin servidor, qué hace el Edge con cada comando del servidor y qué
// emite: con lease vigente el SendText se publica y se acusa (en ese orden) y el SendMedia se
// acusa; sin lease vigente —ninguno todavía, rechazado, vencido o revocado— ninguno de los dos
// «sale» y se contesta Ack{ok=false, «lease no vigente»}, como el Edge real; el ConfigUpdate se
// registra y se acusa (sin gate), el DiagnosticsRequest solo se registra y se contesta con bundle,
// el Ping se contesta con Pong, los leases pasan por el Validator (vigente, revocado, firma ajena,
// contador viejo), un comando desconocido se acusa si trae command_id, y un fallo de la salida
// queda en Errores.
func TestArnes_EdgeNucleo(t *testing.T) {
	t.Parallel()
	t.Run("SendText: se publica y se acusa, en ese orden", edgeProbarSendText)
	t.Run("gate de lease: sin lease vigente no se entrega y se acusa ok=false", edgeCheckLeaseGate)
	t.Run("gate de lease: un lease vencido tampoco deja entregar", edgeCheckLeaseGateExpired)
	t.Run("SendMedia: con lease vigente se acusa", edgeCheckSendMediaWithLease)
	t.Run("ConfigUpdate: se registra y se acusa", edgeProbarConfig)
	t.Run("DiagnosticsRequest: se registra, no responde y bundle contesta", edgeProbarDiagnosticoNucleo)
	t.Run("Ping: Pong con el mismo nonce", edgeProbarPing)
	t.Run("LeaseUpdate vigente y revocación", edgeProbarLeases)
	t.Run("LeaseUpdate de firma ajena y de contador viejo", edgeProbarLeasesRechazados)
	t.Run("comando desconocido: se acusa solo con command_id", edgeProbarDesconocido)
	t.Run("un fallo de la salida queda en Errores", edgeProbarFalloDeSalida)
	t.Run("el canal de textos lleno se anota y no bloquea", edgeProbarCanalLleno)
	t.Run("las listas devueltas son copias", edgeProbarCopias)
}

// edgeProbarConfig comprueba que el ConfigUpdate se registra entero (kind, versión, contenido, sesión
// y command_id), que se acusa, y que uno dirigido a todas las sesiones (session_id vacío) se
// acusa con la sesión del Edge. El Edge de este test NO tiene lease: el ConfigUpdate no pasa por
// el gate (como en el Edge real, no es una operación de WhatsApp).
func edgeProbarConfig(t *testing.T) {
	t.Parallel()
	e, c, _ := edgeDePrueba(t)
	e.manejar(edgeComando("cfg-1", "sesion-x", func(cmd *cloudlinkv1.CloudToEdge) {
		cmd.Payload = &cloudlinkv1.CloudToEdge_ConfigUpdate{ConfigUpdate: &cloudlinkv1.ConfigUpdate{
			CommandId: "cfg-1", SessionId: "sesion-x", Kind: "jwks", Version: "kid-1", Payload: []byte(`{"keys":[]}`),
		}}
	}))
	e.manejar(edgeComando("cfg-2", "", func(cmd *cloudlinkv1.CloudToEdge) {
		cmd.Payload = &cloudlinkv1.CloudToEdge_ConfigUpdate{ConfigUpdate: &cloudlinkv1.ConfigUpdate{
			CommandId: "cfg-2", Kind: "filters", Version: "7", Payload: []byte(`{}`),
		}}
	}))

	configs := e.Configs()
	if len(configs) != 2 {
		t.Fatalf("configs = %d, quería 2", len(configs))
	}
	quiere := configRecibida{Kind: "jwks", Version: "kid-1", Payload: []byte(`{"keys":[]}`), ComandoID: "cfg-1", Sesion: "sesion-x"}
	if configs[0].Kind != quiere.Kind || configs[0].Version != quiere.Version || string(configs[0].Payload) != string(quiere.Payload) ||
		configs[0].ComandoID != quiere.ComandoID || configs[0].Sesion != quiere.Sesion {
		t.Errorf("config 0 = %+v, quería %+v", configs[0], quiere)
	}
	frames := c.todos()
	if len(frames) != 2 {
		t.Fatalf("frames emitidos = %d, quería 2 Ack", len(frames))
	}
	if a := edgeAckDe(t, frames[0]); a.GetAckedCommandId() != "cfg-1" || !a.GetOk() {
		t.Errorf("Ack 0 = %+v", a)
	}
	if a := edgeAckDe(t, frames[1]); a.GetAckedCommandId() != "cfg-2" || frames[1].GetSessionId() != "sesion-prueba" {
		t.Errorf("Ack 1 = %+v en la sesión %q, quería cfg-2 en sesion-prueba", a, frames[1].GetSessionId())
	}
}

// edgeProbarDiagnosticoNucleo comprueba que el DiagnosticsRequest se registra sin emitir nada, y que bundle
// manda un DiagnosticsBundle con el command_id y la cola de log dados.
func edgeProbarDiagnosticoNucleo(t *testing.T) {
	t.Parallel()
	e, c, _ := edgeDePrueba(t)
	e.manejar(edgeComando("diag-1", "sesion-x", func(cmd *cloudlinkv1.CloudToEdge) {
		cmd.Payload = &cloudlinkv1.CloudToEdge_DiagnosticsRequest{DiagnosticsRequest: &cloudlinkv1.DiagnosticsRequest{
			CommandId: "diag-1", SessionId: "sesion-x", Scope: "full",
		}}
	}))
	if pedidos := e.Diagnosticos(); len(pedidos) != 1 || pedidos[0] != (diagnosticoPedido{ComandoID: "diag-1", Scope: "full", Sesion: "sesion-x"}) {
		t.Errorf("diagnósticos = %+v", pedidos)
	}
	if n := len(c.todos()); n != 0 {
		t.Fatalf("el DiagnosticsRequest emitió %d frames; el Edge no responde solo", n)
	}

	e.bundle(t, "diag-1", "línea 1\nlínea 2")
	frames := c.todos()
	if len(frames) != 1 || frames[0].GetSessionId() != "sesion-prueba" {
		t.Fatalf("frames tras bundle = %d, sesión %q", len(frames), frames[0].GetSessionId())
	}
	b := frames[0].GetDiagnosticsBundle()
	if b.GetCommandId() != "diag-1" || b.GetLogTail() != "línea 1\nlínea 2" || b.GetGoroutineDump() == "" || b.GetSubsystemsJson() == "" {
		t.Errorf("bundle = %+v", b)
	}
}

// edgeProbarPing comprueba que el Ping se contesta con un Pong del mismo nonce en la sesión del
// comando.
func edgeProbarPing(t *testing.T) {
	t.Parallel()
	e, c, _ := edgeDePrueba(t)
	e.manejar(edgeComando("", "sesion-x", func(cmd *cloudlinkv1.CloudToEdge) {
		cmd.Payload = &cloudlinkv1.CloudToEdge_Ping{Ping: &cloudlinkv1.Ping{Nonce: 42}}
	}))
	frames := c.todos()
	if len(frames) != 1 || frames[0].GetPong().GetNonce() != 42 || frames[0].GetSessionId() != "sesion-x" {
		t.Errorf("frames tras Ping = %+v", frames)
	}
}

// edgeProbarLeases comprueba que un lease vigente deja operar al Edge, que uno de revocación lo
// corta y marca el kill-switch, y que después ningún lease vigente lo revive.
func edgeProbarLeases(t *testing.T) {
	t.Parallel()
	e, _, k := edgeDePrueba(t)
	iss := edgeEmisorLease(t, k)
	aplicar := func(lu *cloudlinkv1.LeaseUpdate, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("emitir el lease: %v", err)
		}
		e.manejar(edgeComando("", e.SessionID, func(cmd *cloudlinkv1.CloudToEdge) {
			cmd.Payload = &cloudlinkv1.CloudToEdge_LeaseUpdate{LeaseUpdate: lu}
		}))
	}
	if e.puedeOperar() || e.revocado() {
		t.Fatalf("sin lease, puedeOperar=%v revocado=%v; quería falso y falso", e.puedeOperar(), e.revocado())
	}
	aplicar(iss.Issue(e.EdgeID, e.TenantID, time.Hour, 1))
	if !e.puedeOperar() || e.revocado() || e.Leases() != 1 {
		t.Errorf("con un lease vigente: puedeOperar=%v revocado=%v leases=%d", e.puedeOperar(), e.revocado(), e.Leases())
	}
	aplicar(iss.Revoke(e.EdgeID, e.TenantID))
	if e.puedeOperar() || !e.revocado() {
		t.Errorf("tras la revocación: puedeOperar=%v revocado=%v; quería falso y verdadero", e.puedeOperar(), e.revocado())
	}
	aplicar(iss.Issue(e.EdgeID, e.TenantID, time.Hour, 9))
	if e.puedeOperar() || !e.revocado() {
		t.Errorf("un lease vigente revivió al Edge revocado: puedeOperar=%v revocado=%v", e.puedeOperar(), e.revocado())
	}
	if errs := e.Errores(); len(errs) != 0 {
		t.Errorf("Errores = %v, quería ninguno", errs)
	}
}

// edgeProbarLeasesRechazados comprueba que un lease firmado por otra clave y uno de contador no
// superior al aplicado se anotan en Errores con su causa (errors.Is) y no cambian el estado.
func edgeProbarLeasesRechazados(t *testing.T) {
	t.Parallel()
	e, _, k := edgeDePrueba(t)
	iss := edgeEmisorLease(t, k)
	ajeno := edgeEmisorLease(t, nuevasClaves(t))
	aplicar := func(lu *cloudlinkv1.LeaseUpdate, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("emitir el lease: %v", err)
		}
		e.manejar(edgeComando("", e.SessionID, func(cmd *cloudlinkv1.CloudToEdge) {
			cmd.Payload = &cloudlinkv1.CloudToEdge_LeaseUpdate{LeaseUpdate: lu}
		}))
	}
	aplicar(ajeno.Issue(e.EdgeID, e.TenantID, time.Hour, 1))
	if e.puedeOperar() {
		t.Errorf("un lease de firma ajena dejó operar al Edge")
	}
	aplicar(iss.Issue(e.EdgeID, e.TenantID, time.Hour, 5))
	aplicar(iss.Issue(e.EdgeID, e.TenantID, time.Hour, 5))
	aplicar(iss.Issue(e.EdgeID, e.TenantID, time.Hour, 3))
	if !e.puedeOperar() || e.Leases() != 4 {
		t.Errorf("puedeOperar=%v leases=%d; quería verdadero y 4 recibidos", e.puedeOperar(), e.Leases())
	}
	errs := e.Errores()
	if len(errs) != 3 || !errors.Is(errs[0], cllease.ErrBadSignature) ||
		!errors.Is(errs[1], cllease.ErrStaleCounter) || !errors.Is(errs[2], cllease.ErrStaleCounter) {
		t.Errorf("Errores = %v, quería firma ajena y dos contadores viejos", errs)
	}
}

// edgeProbarDesconocido comprueba que un comando que el Edge no interpreta (un UserAuthResponse, la
// respuesta al login del operador) se acusa como correcto si trae command_id, y no se acusa si no
// lo trae. El Edge de este test NO tiene lease: lo que no es «de operar» no pasa por el gate. (El
// SendMedia, que hacía de «desconocido» aquí, dejó de serlo: tiene su gate y sus propios tests.)
func edgeProbarDesconocido(t *testing.T) {
	t.Parallel()
	e, c, _ := edgeDePrueba(t)
	unknown := func(cmd *cloudlinkv1.CloudToEdge) {
		cmd.Payload = &cloudlinkv1.CloudToEdge_UserAuthResponse{UserAuthResponse: &cloudlinkv1.UserAuthResponse{}}
	}
	e.manejar(edgeComando("", "sesion-x", unknown))
	if n := len(c.todos()); n != 0 {
		t.Fatalf("un comando sin command_id emitió %d frames", n)
	}
	e.manejar(edgeComando("auth-1", "sesion-x", unknown))
	frames := c.todos()
	if len(frames) != 1 {
		t.Fatalf("frames = %d, quería 1", len(frames))
	}
	if a := edgeAckDe(t, frames[0]); a.GetAckedCommandId() != "auth-1" || !a.GetOk() || a.GetError() != "" {
		t.Errorf("Ack = %+v", a)
	}
}

// edgeProbarFalloDeSalida comprueba que un envío que falla se anota en Errores con su causa, y que
// con el cierre ya empezado deja de anotarse.
func edgeProbarFalloDeSalida(t *testing.T) {
	t.Parallel()
	e, c, _ := edgeDePrueba(t)
	causa := errors.New("tubería rota")
	c.fallo = causa
	e.manejar(edgeComando("p-1", "sesion-x", func(cmd *cloudlinkv1.CloudToEdge) {
		cmd.Payload = &cloudlinkv1.CloudToEdge_Ping{Ping: &cloudlinkv1.Ping{Nonce: 1}}
	}))
	if errs := e.Errores(); len(errs) != 1 || !errors.Is(errs[0], causa) {
		t.Fatalf("Errores = %v, quería uno que envuelva %v", errs, causa)
	}

	e.mu.Lock()
	e.cerrando = true
	e.mu.Unlock()
	e.manejar(edgeComando("p-2", "sesion-x", func(cmd *cloudlinkv1.CloudToEdge) {
		cmd.Payload = &cloudlinkv1.CloudToEdge_Ping{Ping: &cloudlinkv1.Ping{Nonce: 2}}
	}))
	if n := len(e.Errores()); n != 1 {
		t.Errorf("con el cierre empezado se anotó otro error: %d", n)
	}
	e.salida = nil
	if err := e.emitir(edgeLatido("s", 1)); !errors.Is(err, errEdgeSinSalida) {
		t.Errorf("emitir sin salida = %v, quería errEdgeSinSalida", err)
	}
}

// edgeProbarCanalLleno comprueba que, con lease vigente y el canal de textos lleno, el siguiente
// SendText no bloquea al bucle de recepción: se descarta, se anota en Errores y aun así se acusa.
func edgeProbarCanalLleno(t *testing.T) {
	t.Parallel()
	e, c, k := edgeDePrueba(t)
	edgeGrantLease(t, e, k)
	texto := func(cmd *cloudlinkv1.CloudToEdge) {
		cmd.Payload = &cloudlinkv1.CloudToEdge_SendText{SendText: &cloudlinkv1.SendText{To: "x", Text: "y"}}
	}
	for i := range edgeBuferTextos + 1 {
		e.manejar(edgeComando(fmt.Sprintf("t-%d", i), "s", texto))
	}
	if len(e.Textos()) != edgeBuferTextos {
		t.Errorf("textos en el canal = %d, quería %d", len(e.Textos()), edgeBuferTextos)
	}
	if errs := e.Errores(); len(errs) != 1 || !strings.Contains(errs[0].Error(), "t-256") {
		t.Errorf("Errores = %v, quería uno que nombre el texto t-256 descartado", errs)
	}
	if n := len(c.todos()); n != edgeBuferTextos+1 {
		t.Errorf("acuses = %d, quería %d (también el del descartado)", n, edgeBuferTextos+1)
	}
}

// edgeProbarCopias comprueba que modificar lo que devuelven Configs, Diagnosticos y Errores no
// altera el registro del Edge.
func edgeProbarCopias(t *testing.T) {
	t.Parallel()
	e, _, _ := edgeDePrueba(t)
	e.manejar(edgeComando("cfg-1", "s", func(cmd *cloudlinkv1.CloudToEdge) {
		cmd.Payload = &cloudlinkv1.CloudToEdge_ConfigUpdate{ConfigUpdate: &cloudlinkv1.ConfigUpdate{CommandId: "cfg-1", Kind: "k", Payload: []byte("abc")}}
	}))
	e.manejar(edgeComando("d-1", "s", func(cmd *cloudlinkv1.CloudToEdge) {
		cmd.Payload = &cloudlinkv1.CloudToEdge_DiagnosticsRequest{DiagnosticsRequest: &cloudlinkv1.DiagnosticsRequest{CommandId: "d-1"}}
	}))
	e.anotarError(errors.New("uno"))

	cfg := e.Configs()
	cfg[0].Payload[0] = 'X'
	cfg[0].Kind = "otro"
	diag := e.Diagnosticos()
	diag[0].ComandoID = "otro"
	errs := e.Errores()
	errs[0] = errors.New("otro")

	if again := e.Configs(); string(again[0].Payload) != "abc" || again[0].Kind != "k" {
		t.Errorf("Configs devolvió una vista del registro: %+v", again[0])
	}
	if again := e.Diagnosticos(); again[0].ComandoID != "d-1" {
		t.Errorf("Diagnosticos devolvió una vista del registro: %+v", again[0])
	}
	if again := e.Errores(); again[0].Error() != "uno" {
		t.Errorf("Errores devolvió una vista del registro: %v", again[0])
	}
}
