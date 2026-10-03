//go:build integracion

package procesos

import (
	"fmt"
	"strings"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-shared/envelope"
	"google.golang.org/protobuf/proto"
)

// Los frames que el Edge de prueba manda al servidor: latido, entrante sellado, acuse, bundle de
// diagnóstico y el frame del canal de control, todos por una salida serializada.
// Sale de edge_falso_test.go (D-F9-11: solo se movieron declaraciones).

// ---------------------------------------------------------------------------------------------
// Frames que el Edge manda
// ---------------------------------------------------------------------------------------------

// emitir manda un frame por la salida del Edge, serializado con los demás envíos. Devuelve
// errEdgeSinSalida si no hay enlace, o el error de la salida.
func (e *edge) emitir(msg *cloudlinkv1.EdgeToCloud) error {
	e.salidaMu.Lock()
	defer e.salidaMu.Unlock()
	if e.salida == nil {
		return errEdgeSinSalida
	}
	return e.salida(msg)
}

// emitirOAnotar es emitir para el bucle de recepción, que no tiene a quién devolver el error: lo
// anota en Errores, salvo que el cierre ya haya empezado (entonces es lo esperado).
func (e *edge) emitirOAnotar(msg *cloudlinkv1.EdgeToCloud) {
	if err := e.emitir(msg); err != nil && !e.estaCerrando() {
		e.anotarError(fmt.Errorf("emitir %T: %w", msg.GetPayload(), err))
	}
}

// edgeLatido arma el frame de latido: el contador de lease dado y la inferencia declarada READY.
// Sin SelfPn a propósito: con un número propio, el servidor mandaría un SendText de saludo a la
// sesión recién emparejada. READY desde el primer latido evita que el servidor pida un
// calentamiento por compatibilidad con los Edges que no dicen nada.
func edgeLatido(sesion string, contador int64) *cloudlinkv1.EdgeToCloud {
	return &cloudlinkv1.EdgeToCloud{
		SessionId: sesion,
		Payload: &cloudlinkv1.EdgeToCloud_Heartbeat{Heartbeat: &cloudlinkv1.Heartbeat{
			LeaseCounter:       contador,
			InferenceReadiness: cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_READY,
		}},
	}
}

// latir manda un latido con el contador de lease dado. El servidor renueva el lease con contador+1,
// y el Validator solo acepta contadores que superen al último aplicado: tras conectar (contadores 1
// y 2) el siguiente latido útil usa 2 o más. Falla (t.Fatalf) si no hay enlace o el envío falla.
func (e *edge) latir(t *testing.T, contador int64) {
	t.Helper()
	if err := e.emitir(edgeLatido(e.SessionID, contador)); err != nil {
		t.Fatalf("latir(%d): %v", contador, err)
	}
}

// entrante manda un IncomingMessage SELLADO, como el Edge real: el texto y el número viajan
// dentro de un SensitivePayload marshalado y sellado con la pública de la nube (enc_payload), y
// los planos sensibles (text, push_name, from_pn, from_lid) van VACÍOS. En claro solo van from
// (de), wa_message_id (waID), el instante y el modo de direccionamiento. Si de es un número (con o
// sin «+» y con o sin el sufijo «@…»), ese número va como from_pn dentro del sobre. Falla
// (t.Fatalf) si no puede sellar o emitir.
func (e *edge) entrante(t *testing.T, de, texto, waID string) {
	t.Helper()
	msg, err := e.armarEntrante(de, texto, waID)
	if err != nil {
		t.Fatalf("entrante %s: %v", waID, err)
	}
	if err := e.emitir(msg); err != nil {
		t.Fatalf("entrante %s: %v", waID, err)
	}
}

// armarEntrante construye el frame de entrante sellado. Devuelve error si el sellado falla.
func (e *edge) armarEntrante(de, texto, waID string) (*cloudlinkv1.EdgeToCloud, error) {
	numero := edgeNumeroDe(de)
	plano, err := proto.Marshal(&cloudlinkv1.SensitivePayload{Text: texto, FromPn: numero})
	if err != nil {
		return nil, fmt.Errorf("serializar el SensitivePayload: %w", err)
	}
	sellado, err := envelope.SealFor(e.CloudEncPub, plano)
	if err != nil {
		return nil, fmt.Errorf("sellar el SensitivePayload: %w", err)
	}
	modo := ""
	if numero != "" {
		modo = "pn"
	}
	return &cloudlinkv1.EdgeToCloud{
		SessionId: e.SessionID,
		Payload: &cloudlinkv1.EdgeToCloud_Incoming{Incoming: &cloudlinkv1.IncomingMessage{
			From:           de,
			TsUnix:         time.Now().Unix(),
			WaMessageId:    waID,
			AddressingMode: modo,
			EncPayload:     sellado,
		}},
	}, nil
}

// sealedIncoming describe un entrante sellado campo a campo (D-F1-11): lo que entrante y
// armarEntrante no saben mandar. From y WaID viajan en claro, como en el Edge real; Text, FromPn,
// FromLid y PushName viajan SOLO dentro del sobre. Ningún campo se normaliza ni se deriva de otro:
// lo que el test escribe es lo que llega, para poder mandar identidades parciales (solo FromLid,
// solo FromPn) y valores adversarios (separadores repetidos, dígitos no ASCII, espacios Unicode).
type sealedIncoming struct {
	From     string // en claro: el JID del remitente («…@s.whatsapp.net», «…@lid»)
	WaID     string // en claro: wa_message_id
	Text     string // sellado
	FromPn   string // sellado; vacío = el entrante no trae número
	FromLid  string // sellado; vacío = el entrante no trae LID
	PushName string // sellado; vacío = el entrante no trae nombre de perfil
}

// buildSealedIncoming construye el frame de un sealedIncoming: un SensitivePayload con text,
// push_name, from_pn y from_lid, marshalado y sellado con la pública de la nube (enc_payload), y
// los cuatro planos sensibles VACÍOS. addressing_mode es «pn» si trae número, «lid» si solo trae
// LID y vacío si no trae ninguno. Devuelve error si el sellado falla.
func (e *edge) buildSealedIncoming(in sealedIncoming) (*cloudlinkv1.EdgeToCloud, error) {
	plain, err := proto.Marshal(&cloudlinkv1.SensitivePayload{
		Text: in.Text, PushName: in.PushName, FromPn: in.FromPn, FromLid: in.FromLid,
	})
	if err != nil {
		return nil, fmt.Errorf("serializar el SensitivePayload: %w", err)
	}
	sealed, err := envelope.SealFor(e.CloudEncPub, plain)
	if err != nil {
		return nil, fmt.Errorf("sellar el SensitivePayload: %w", err)
	}
	mode := ""
	switch {
	case in.FromPn != "":
		mode = "pn"
	case in.FromLid != "":
		mode = "lid"
	}
	return &cloudlinkv1.EdgeToCloud{
		SessionId: e.SessionID,
		Payload: &cloudlinkv1.EdgeToCloud_Incoming{Incoming: &cloudlinkv1.IncomingMessage{
			From:           in.From,
			TsUnix:         time.Now().Unix(),
			WaMessageId:    in.WaID,
			AddressingMode: mode,
			EncPayload:     sealed,
		}},
	}, nil
}

// sendSealedIncoming manda un sealedIncoming por la salida del Edge. Falla (t.Fatalf) si no puede
// sellar o emitir.
func (e *edge) sendSealedIncoming(t *testing.T, in sealedIncoming) {
	t.Helper()
	msg, err := e.buildSealedIncoming(in)
	if err != nil {
		t.Fatalf("sendSealedIncoming %s: %v", in.WaID, err)
	}
	if err := e.emitir(msg); err != nil {
		t.Fatalf("sendSealedIncoming %s: %v", in.WaID, err)
	}
}

// edgeNumeroDe saca el número de un remitente: la parte anterior a «@» sin el «+» inicial, si son
// solo dígitos. Devuelve "" si no lo es (un LID, un texto cualquiera).
func edgeNumeroDe(de string) string {
	numero, _, _ := strings.Cut(de, "@")
	numero = strings.TrimPrefix(numero, "+")
	if numero == "" || strings.Trim(numero, "0123456789") != "" {
		return ""
	}
	return numero
}

// acuse manda un acuse de recibo del mensaje waID: «leído» si leido, «entregado» si no. Es el
// Receipt de CloudLink, con el session_id del Edge dentro del propio acuse (el servidor persiste
// ese) y sin command_id. Falla (t.Fatalf) si no hay enlace o el envío falla.
func (e *edge) acuse(t *testing.T, waID string, leido bool) {
	t.Helper()
	estado := cloudlinkv1.ReceiptStatus_RECEIPT_STATUS_DELIVERED
	if leido {
		estado = cloudlinkv1.ReceiptStatus_RECEIPT_STATUS_READ
	}
	err := e.emitir(&cloudlinkv1.EdgeToCloud{
		SessionId: e.SessionID,
		Payload: &cloudlinkv1.EdgeToCloud_Receipt{Receipt: &cloudlinkv1.MessageReceipt{
			SessionId:  e.SessionID,
			MessageIds: []string{waID},
			Status:     estado,
			Timestamp:  time.Now().Unix(),
		}},
	})
	if err != nil {
		t.Fatalf("acuse de %s: %v", waID, err)
	}
}

// bundle responde a un DiagnosticsRequest: manda un DiagnosticsBundle correlacionado por
// comandoID, con logTail como cola del log y un volcado de goroutines y un estado de subsistemas
// de relleno (sin llaves ni contenido). Falla (t.Fatalf) si no hay enlace o el envío falla.
func (e *edge) bundle(t *testing.T, comandoID, logTail string) {
	t.Helper()
	err := e.emitir(&cloudlinkv1.EdgeToCloud{
		SessionId: e.SessionID,
		Payload: &cloudlinkv1.EdgeToCloud_DiagnosticsBundle{DiagnosticsBundle: &cloudlinkv1.DiagnosticsBundle{
			CommandId:      comandoID,
			LogTail:        logTail,
			GoroutineDump:  "goroutine 1 [running]:\nmain.main()\n\t(volcado de prueba)",
			SubsystemsJson: `{"subsistemas":[{"nombre":"prueba","estado":"ok"}]}`,
		}},
	})
	if err != nil {
		t.Fatalf("bundle %s: %v", comandoID, err)
	}
}

// usarCanalControl manda un frame por el canal de control (session_id edgeSesionControl), como
// hace el Edge real al autenticar a su operador antes de emparejar ningún teléfono. El servidor no
// lo registra como sesión; solo le empuja la config inicial por ese mismo stream (jwks y filters),
// que llega a Configs con Sesion = edgeSesionControl. El frame es un Pong sin efecto. Falla
// (t.Fatalf) si no hay enlace o el envío falla.
func (e *edge) usarCanalControl(t *testing.T) {
	t.Helper()
	err := e.emitir(&cloudlinkv1.EdgeToCloud{
		SessionId: edgeSesionControl,
		Payload:   &cloudlinkv1.EdgeToCloud_Pong{Pong: &cloudlinkv1.Pong{}},
	})
	if err != nil {
		t.Fatalf("usarCanalControl: %v", err)
	}
}
