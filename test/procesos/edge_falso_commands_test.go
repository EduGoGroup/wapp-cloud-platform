//go:build integracion

package procesos

import (
	"fmt"
	"slices"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
)

// El núcleo del Edge de prueba: qué hace con cada comando que recibe del servidor (lease, SendText,
// SendMedia, config, diagnóstico, ping) y el gate de lease de los envíos. La inferencia va aparte,
// en edge_falso_inference_test.go. Sale de edge_falso_test.go (D-F9-11: solo se movieron declaraciones).

// ---------------------------------------------------------------------------------------------
// Comandos que el Edge recibe (el núcleo)
// ---------------------------------------------------------------------------------------------

// despachar es lo que hace el bucle de recepción con cada comando. Las peticiones de inferencia
// van a su propia goroutine, para que ni un guion lento ni la gracia del gate de lease frenen los
// acuses, los leases ni los pings (las inferencias entre sí sí van de una en una); el resto se
// atiende en línea.
func (e *edge) despachar(cmd *cloudlinkv1.CloudToEdge) {
	if cmd.GetInferenceRequest() != nil {
		e.enVuelo.Go(func() { e.manejar(cmd) })
		return
	}
	e.manejar(cmd)
}

// manejar atiende UN comando del servidor, sin transporte: lo que responda lo emite por la salida
// del Edge. SendText: si el lease está vigente publica el texto y lo acusa; si no, lo rechaza.
// SendMedia: si el lease está vigente lo acusa; si no, lo rechaza. LeaseUpdate: lo aplica al
// Validator. ConfigUpdate: lo registra y lo acusa. DiagnosticsRequest: lo registra (no responde
// solo). InferenceRequest: si el lease está vigente contesta con el guion (o con un error); si no,
// tras la gracia, con INFERENCE_ERROR_LEASE_INVALID. Ping: Pong. Cualquier otro comando con
// command_id se acusa como correcto.
//
// El gate de lease cubre lo que en el Edge real es «operar», igual que wapp-edge-agent: los dos
// comandos que despachan a WhatsApp (blockedByLease) y la inferencia, que allí tiene su propia
// regla y aquí también (inferenceBlockedByLease: con gracia y con otra respuesta, un
// InferenceResult y no un Ack). ConfigUpdate, DiagnosticsRequest y Ping no pasan por ninguno.
func (e *edge) manejar(cmd *cloudlinkv1.CloudToEdge) {
	switch p := cmd.GetPayload().(type) {
	case *cloudlinkv1.CloudToEdge_LeaseUpdate:
		e.alRecibirLease(p.LeaseUpdate)
	case *cloudlinkv1.CloudToEdge_SendText:
		e.alRecibirTexto(cmd, p.SendText)
	case *cloudlinkv1.CloudToEdge_SendMedia:
		e.handleSendMedia(cmd)
	case *cloudlinkv1.CloudToEdge_ConfigUpdate:
		e.alRecibirConfig(cmd, p.ConfigUpdate)
	case *cloudlinkv1.CloudToEdge_DiagnosticsRequest:
		e.alRecibirDiagnostico(cmd, p.DiagnosticsRequest)
	case *cloudlinkv1.CloudToEdge_InferenceRequest:
		e.alRecibirInferencia(cmd, p.InferenceRequest)
	case *cloudlinkv1.CloudToEdge_Ping:
		e.alRecibirPing(cmd, p.Ping)
	case *cloudlinkv1.CloudToEdge_UserAuthResponse:
		e.handleUserAuthResponse(cmd, p.UserAuthResponse) // edge_falso_auth_test.go: se guarda, no se acusa
	default:
		e.acusar(cmd)
	}
}

// sesionDe devuelve el session_id con el que se contesta a un comando: el del comando, o el del
// Edge si el comando iba a todas las sesiones (vacío).
func (e *edge) sesionDe(cmd *cloudlinkv1.CloudToEdge) string {
	if sid := cmd.GetSessionId(); sid != "" {
		return sid
	}
	return e.SessionID
}

// acusar contesta a un comando con Ack{ok=true}, correlacionado por su command_id. Un comando sin
// command_id no se acusa (no habría a qué correlacionarlo).
func (e *edge) acusar(cmd *cloudlinkv1.CloudToEdge) {
	e.reply(cmd, true, "")
}

// reply contesta a un comando con Ack{ok, error}, correlacionado por su command_id y en la sesión
// del comando. errText es el motivo cuando ok es falso (vacío si ok). Un comando sin command_id no
// se contesta (no habría a qué correlacionarlo).
func (e *edge) reply(cmd *cloudlinkv1.CloudToEdge, ok bool, errText string) {
	if cmd.GetCommandId() == "" {
		return
	}
	e.emitirOAnotar(&cloudlinkv1.EdgeToCloud{
		SessionId: e.sesionDe(cmd),
		Payload: &cloudlinkv1.EdgeToCloud_Ack{Ack: &cloudlinkv1.Ack{
			AckedCommandId: cmd.GetCommandId(),
			Ok:             ok,
			Error:          errText,
		}},
	})
}

// blockedByLease aplica el gate de lease del Edge real a un comando «de operar» (ADR-0007, el
// kill-switch): si el Edge NO puede operar —aún no tiene lease, el Validator rechazó el que llegó,
// venció o está revocado—, contesta Ack{ok=false, error=«lease no vigente»} y devuelve true, y
// quien llama no debe despachar nada. Si puede operar devuelve false sin emitir nada. Es la regla
// de handleSendText y handleSendMedia de wapp-edge-agent
// (internal/adapters/cloudlink/adapter.go: `!validator.CanOperate(hasDEK)`), sin su modo sombra.
// Un bloqueo no es un error del núcleo: no se anota en Errores.
func (e *edge) blockedByLease(cmd *cloudlinkv1.CloudToEdge) bool {
	if e.puedeOperar() {
		return false
	}
	e.reply(cmd, false, edgeLeaseNotValidText)
	return true
}

// alRecibirLease se lo da al Validator y DESPUÉS lo cuenta: cuando Leases() dice n, los n ya están
// aplicados (o rechazados), así que quien espera un lease puede mirar puedeOperar() sin carrera. Un
// lease que el Validator rechaza (firma ajena, contador viejo) se anota en Errores y no cambia el
// estado; si además es el PRIMERO de la conexión, el rechazo queda para conectarErr
// (initialLeaseRejection). Una revocación se acepta: no es un rechazo.
func (e *edge) alRecibirLease(lu *cloudlinkv1.LeaseUpdate) {
	e.mu.Lock()
	v := e.validador
	e.mu.Unlock()
	err := v.Apply(lu)

	e.mu.Lock()
	defer e.mu.Unlock()
	e.leases++
	if err == nil {
		return
	}
	if e.leases == 1 {
		e.initialLeaseErr = err
	}
	e.errores = append(e.errores, fmt.Errorf("el Validator rechazó un LeaseUpdate: %w", err))
}

// alRecibirTexto aplica el gate de lease y, si el Edge puede operar, publica el SendText en el
// canal de textos y después lo acusa. Sin lease vigente NO lo publica y lo rechaza con
// Ack{ok=false, «lease no vigente»} (blockedByLease): para el servidor, y para la API de envío, ese
// texto no se entregó. El orden publicar → acusar importa para quien llama por HTTP: el servidor
// devuelve su 200 al recibir el Ack, así que cuando el test ve una respuesta con ok=true el texto
// ya está en el canal, y cuando la ve con ok=false sabe que no lo estará.
func (e *edge) alRecibirTexto(cmd *cloudlinkv1.CloudToEdge, st *cloudlinkv1.SendText) {
	if e.blockedByLease(cmd) {
		return
	}
	txt := textoRecibido{A: st.GetTo(), Texto: st.GetText(), ComandoID: cmd.GetCommandId()}
	select {
	case e.textos <- txt:
	default:
		e.anotarError(fmt.Errorf("el canal de textos está lleno (%d): se descarta el SendText %s", edgeBuferTextos, txt.ComandoID))
	}
	e.acusar(cmd)
}

// handleSendMedia aplica el gate de lease a un SendMedia, como el Edge real: sin lease vigente lo
// rechaza con Ack{ok=false, «lease no vigente»}; con lease lo acusa como correcto. El doble no
// descarga ni registra el archivo: de un SendMedia solo decide si «sale» o no.
func (e *edge) handleSendMedia(cmd *cloudlinkv1.CloudToEdge) {
	if e.blockedByLease(cmd) {
		return
	}
	e.acusar(cmd)
}

// alRecibirConfig registra el ConfigUpdate (con copia del contenido) y lo acusa. No pasa por el
// gate de lease: no es una operación de WhatsApp (igual que en el Edge real).
func (e *edge) alRecibirConfig(cmd *cloudlinkv1.CloudToEdge, cu *cloudlinkv1.ConfigUpdate) {
	e.mu.Lock()
	e.configs = append(e.configs, configRecibida{
		Kind:      cu.GetKind(),
		Version:   cu.GetVersion(),
		Payload:   slices.Clone(cu.GetPayload()),
		ComandoID: cu.GetCommandId(),
		Sesion:    cu.GetSessionId(),
	})
	e.mu.Unlock()
	e.acusar(cmd)
}

// alRecibirDiagnostico registra el DiagnosticsRequest. No responde: el bundle lo manda el test.
func (e *edge) alRecibirDiagnostico(_ *cloudlinkv1.CloudToEdge, dr *cloudlinkv1.DiagnosticsRequest) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.diagnosticos = append(e.diagnosticos, diagnosticoPedido{
		ComandoID: dr.GetCommandId(),
		Scope:     dr.GetScope(),
		Sesion:    dr.GetSessionId(),
	})
}

// alRecibirPing contesta con Pong y el mismo nonce.
func (e *edge) alRecibirPing(cmd *cloudlinkv1.CloudToEdge, p *cloudlinkv1.Ping) {
	e.emitirOAnotar(&cloudlinkv1.EdgeToCloud{
		SessionId: e.sesionDe(cmd),
		Payload:   &cloudlinkv1.EdgeToCloud_Pong{Pong: &cloudlinkv1.Pong{Nonce: p.GetNonce()}},
	})
}

// anotarError guarda un error del núcleo (un lease rechazado, un envío que falló, un canal lleno)
// para que Errores lo muestre.
func (e *edge) anotarError(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.errores = append(e.errores, err)
}
