//go:build pendiente

package apipublica_test

// flows_start_test.go — cubre de MountFlows la ruta I4, POST /api/v1/flows/{id}/start: la
// petición, la identidad del contacto, el acuse y la tabla de errores del arranque. Los dobles y
// los auxiliares del área viven en flows_test.go.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica/apipublicahelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/session"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

const (
	flowsMsgBadJSON     = "cuerpo JSON inválido"
	flowsMsgNoSession   = "session_id es requerido"
	flowsMsgNoContact   = "se requiere contact_ref {kind,value} o contact (alias phone_e164)"
	flowsMsgExists      = "ya existe una conversación viva para la clave"
	flowsMsgAckTimeout  = "timeout esperando el ack del Edge"
	flowsMsgStartFailed = "no se pudo iniciar la conversación"
	flowsMsgDurable     = "el flujo tiene contenido durable (cart/survey): su evento nace en la conversación, no por " +
		"esta API. Configura una regla event_start para este flujo (POST /api/v1/triggers) para que el " +
		"cliente lo arranque escribiendo su palabra clave; no reintentes esta llamada, seguirá devolviendo 409"
	flowsMsgStreamClosed = "el stream del Edge se cerró antes del ack: la conversación YA quedó abierta y el " +
		"comando de su primer mensaje viajó al Edge, así que no se sabe si el cliente llegó a recibirlo. " +
		"NO reintentes este arranque —devolverá 409—: comprueba la conversación y, si el primer mensaje " +
		"no salió, continúala sobre la que ya existe"
)

// flowsStart pide I4 sobre target con un token del tenant A que trae SOLO flows.start.
func flowsStart(t *testing.T, starter *flowsStarterSpy, target, body string) (*apipublicahelpertest.Harness, *httptest.ResponseRecorder) {
	t.Helper()
	return flowsDo(t, apipublica.FlowsDeps{Starter: starter}, flowsPermStart, http.MethodPost, target, body)
}

// flowsOKAck es el Ack del camino feliz.
func flowsOKAck() *cloudlinkv1.Ack {
	return &cloudlinkv1.Ack{AckedCommandId: "cmd-1", Ok: true}
}

// TestMountFlows_StartCallsTheEngine: el motor recibe el tenant del TOKEN, el flow_id de la RUTA,
// el session_id tal cual y la Ref normalizada, con el contexto de la petición sin plazo propio; un
// tenant_id o un flow_id en el cuerpo o en la query no cuentan.
func TestMountFlows_StartCallsTheEngine(t *testing.T) {
	starter := &flowsStarterSpy{ack: flowsOKAck()}
	other := apipublicahelpertest.TenantB
	h, rec := flowsStart(t, starter, flowsTargetStart+"?tenant_id="+other+"&flow_id=otro",
		`{"tenant_id":"`+other+`","flow_id":"otro","session_id":" Sess-A ","contact":"+1 555 123-4567"}`)
	wantCode(t, "I4", rec, http.StatusOK)
	wantExactBody(t, "I4", rec, `{"acked_command_id":"cmd-1","ok":true}`)
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("I4: Content-Type %q, quiero application/json", got)
	}
	if starter.calls != 1 {
		t.Fatalf("I4: el motor recibió %d arranques, quiero 1", starter.calls)
	}
	wantRef := contact.Ref{Kind: contact.KindPhoneE164, Value: "15551234567"}
	if starter.gotTenant != tenantA || starter.gotFlowID != flowsFlowID || starter.gotSession != " Sess-A " || starter.gotRef != wantRef {
		t.Errorf("I4: el motor recibió (%q, %q, %q, %+v); quiero (%s, %s, \" Sess-A \", %+v)",
			starter.gotTenant, starter.gotFlowID, starter.gotSession, starter.gotRef, tenantA, flowsFlowID, wantRef)
	}
	if starter.hasDeadline {
		t.Error("I4: el contexto del motor trae plazo; la puerta no pone uno propio")
	}
	if body := rec.Body.String(); strings.Contains(body, tenantA) || strings.Contains(body, other) {
		t.Errorf("I4: la respuesta lleva un tenant (%s); es el del token y no viaja", body)
	}
	flowsWantAudit(t, "I4", h, flowsPermStart, flowsResource, "success", http.StatusOK)

	// El id de la ruta no se valida ni se normaliza aquí.
	starter = &flowsStarterSpy{ack: flowsOKAck()}
	_, rec = flowsStart(t, starter, flowsTarget+"/Menu_SOPORTE.v2/start", flowsStartBody)
	wantCode(t, "id libre", rec, http.StatusOK)
	if starter.gotFlowID != "Menu_SOPORTE.v2" {
		t.Errorf("id libre: el motor recibió el flujo %q, quiero \"Menu_SOPORTE.v2\"", starter.gotFlowID)
	}
}

// TestMountFlows_StartContactIdentity: manda contact_ref si trae value; si falta o viene con
// value vacío vale el alias `contact`, que es un phone_e164.
func TestMountFlows_StartContactIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       contact.Ref
	}{
		{"contact_alias_is_phone_e164", `{"session_id":"s","contact":"+58 (412) 555-0001"}`,
			contact.Ref{Kind: contact.KindPhoneE164, Value: "584125550001"}},
		{"contact_ref_phone", `{"session_id":"s","contact_ref":{"kind":"phone_e164","value":"+15550001"}}`,
			contact.Ref{Kind: contact.KindPhoneE164, Value: "15550001"}},
		{"contact_ref_lid_is_normalized", `{"session_id":"s","contact_ref":{"kind":"wa_lid","value":"123:2@lid"}}`,
			contact.Ref{Kind: contact.KindWALID, Value: "123"}},
		{"contact_ref_username_is_lowercased", `{"session_id":"s","contact_ref":{"kind":"wa_username","value":" Ana.Perez "}}`,
			contact.Ref{Kind: contact.KindWAUsername, Value: "ana.perez"}},
		{"contact_ref_wins_over_contact", `{"session_id":"s","contact_ref":{"kind":"wa_lid","value":"77"},"contact":"+15550001"}`,
			contact.Ref{Kind: contact.KindWALID, Value: "77"}},
		{"empty_contact_ref_value_falls_back_to_contact", `{"session_id":"s","contact_ref":{"kind":"wa_lid","value":""},"contact":"+15550001"}`,
			contact.Ref{Kind: contact.KindPhoneE164, Value: "15550001"}},
		{"null_contact_ref_falls_back_to_contact", `{"session_id":"s","contact_ref":null,"contact":"15550001"}`,
			contact.Ref{Kind: contact.KindPhoneE164, Value: "15550001"}},
		{"only_the_first_json_value_is_read", `{"session_id":"s","contact":"15550001"} basura`,
			contact.Ref{Kind: contact.KindPhoneE164, Value: "15550001"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			starter := &flowsStarterSpy{ack: flowsOKAck()}
			_, rec := flowsStart(t, starter, flowsTargetStart, tc.body)
			wantCode(t, tc.name, rec, http.StatusOK)
			if starter.calls != 1 || starter.gotRef != tc.want {
				t.Errorf("%s: el motor recibió %d arranques con la referencia %+v; quiero 1 con %+v", tc.name, starter.calls, starter.gotRef, tc.want)
			}
		})
	}
}

// TestMountFlows_StartBadRequestNeverReachesTheEngine: los cuatro 400, en su orden, sin arrancar
// nada y auditados como fallo.
func TestMountFlows_StartBadRequestNeverReachesTheEngine(t *testing.T) {
	for _, tc := range []struct{ name, body, msg string }{
		{"empty_body", ``, flowsMsgBadJSON},
		{"broken_json", `{no es json`, flowsMsgBadJSON},
		{"array_body", `[]`, flowsMsgBadJSON},
		{"wrong_type_session", `{"session_id":7,"contact":"15550001"}`, flowsMsgBadJSON},
		{"wrong_type_contact_ref", `{"session_id":"s","contact_ref":"15550001"}`, flowsMsgBadJSON},
		{"empty_object", `{}`, flowsMsgNoSession},
		{"null_body", `null`, flowsMsgNoSession},
		{"session_before_contact", `{"contact_ref":{"kind":"email","value":"a@b.c"}}`, flowsMsgNoSession},
		{"empty_session", `{"session_id":"","contact":"15550001"}`, flowsMsgNoSession},
		{"no_identity", `{"session_id":"s"}`, flowsMsgNoContact},
		{"empty_identities", `{"session_id":"s","contact_ref":{"kind":"wa_lid","value":""},"contact":""}`, flowsMsgNoContact},
		{"unknown_kind_doubles_the_prefix", `{"session_id":"s","contact_ref":{"kind":"email","value":"a@b.c"}}`,
			`contact_ref inválida: contact_ref inválida: kind desconocido "email"`},
		{"missing_kind", `{"session_id":"s","contact_ref":{"value":"15550001"}}`,
			`contact_ref inválida: contact_ref inválida: kind desconocido ""`},
		{"phone_without_digits", `{"session_id":"s","contact":"sin numero"}`,
			`contact_ref inválida: contact_ref inválida: phone_e164 sin dígitos`},
		{"invalid_contact_ref_does_not_fall_back", `{"session_id":"s","contact_ref":{"kind":"wa_lid","value":"abc"},"contact":"15550001"}`,
			`contact_ref inválida: contact_ref inválida: wa_lid con parte de usuario no numérica (longitud 3)`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			starter := &flowsStarterSpy{ack: flowsOKAck()}
			h, rec := flowsStart(t, starter, flowsTargetStart, tc.body)
			wantCode(t, tc.name, rec, http.StatusBadRequest)
			wantErrorBody(t, tc.name, rec, tc.msg)
			if starter.calls != 0 {
				t.Errorf("%s: el motor recibió %d arranques, quiero 0", tc.name, starter.calls)
			}
			flowsWantAudit(t, tc.name, h, flowsPermStart, flowsResource, "failure", http.StatusBadRequest)
		})
	}
}

// TestMountFlows_StartAckIsMirrored: el 200 refleja el Ack; un Ack con ok=false sigue siendo un
// 200 y un Ack nil no rompe.
func TestMountFlows_StartAckIsMirrored(t *testing.T) {
	for _, tc := range []struct {
		name string
		ack  *cloudlinkv1.Ack
		want string
	}{
		{"ok_without_error_omits_it", flowsOKAck(), `{"acked_command_id":"cmd-1","ok":true}`},
		{"not_ok_is_still_200", &cloudlinkv1.Ack{AckedCommandId: "cmd-2", Error: "whatsapp: no conectado"},
			`{"acked_command_id":"cmd-2","ok":false,"error":"whatsapp: no conectado"}`},
		{"nil_ack", nil, `{"acked_command_id":"","ok":false}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, rec := flowsStart(t, &flowsStarterSpy{ack: tc.ack}, flowsTargetStart, flowsStartBody)
			wantCode(t, tc.name, rec, http.StatusOK)
			wantExactBody(t, tc.name, rec, tc.want)
			flowsWantAudit(t, tc.name, h, flowsPermStart, flowsResource, "success", http.StatusOK)
		})
	}
}

// TestMountFlows_StartErrors es la tabla de errores del arranque, con su orden: gana el primer
// caso que case. fakeSendError (messages_senderror_test.go) es el doble del error del gateway,
// con StreamCaido configurable.
func TestMountFlows_StartErrors(t *testing.T) {
	wrap := func(err error) error { return fmt.Errorf("runtime: enviar texto: %w", err) }
	for _, tc := range []struct {
		name string
		err  error
		code int
		msg  string
	}{
		{"conversation_exists_is_409", runtime.ErrConversationExists, http.StatusConflict, flowsMsgExists},
		{"wrapped_conversation_exists_is_409", wrap(runtime.ErrConversationExists), http.StatusConflict, flowsMsgExists},
		{"durable_flow_is_409_with_its_own_text", runtime.ErrDurableFlowNeedsEvent, http.StatusConflict, flowsMsgDurable},
		{"wrapped_durable_flow_is_409", wrap(runtime.ErrDurableFlowNeedsEvent), http.StatusConflict, flowsMsgDurable},
		{"exists_wins_over_durable", errors.Join(runtime.ErrDurableFlowNeedsEvent, runtime.ErrConversationExists), http.StatusConflict, flowsMsgExists},
		{"offline_is_502", wrap(fmt.Errorf("%w: %q", session.ErrSessionOffline, "sess-a")), http.StatusBadGateway, msgOffline},
		{"platform_sentinel_is_the_same_offline", wrap(httpapi.ErrSessionOffline), http.StatusBadGateway, msgOffline},
		{"offline_wins_over_stream_closed", wrap(&fakeSendError{cmdID: "c", cause: session.ErrSessionOffline, closed: true}), http.StatusBadGateway, msgOffline},
		{"durable_wins_over_offline", errors.Join(session.ErrSessionOffline, runtime.ErrDurableFlowNeedsEvent), http.StatusConflict, flowsMsgDurable},
		{"stream_closed_is_504_with_its_own_text", wrap(&fakeSendError{cmdID: "c", cause: errors.New("EOF"), closed: true}), http.StatusGatewayTimeout, flowsMsgStreamClosed},
		{"stream_closed_wins_over_deadline", &fakeSendError{cmdID: "c", cause: context.DeadlineExceeded, closed: true}, http.StatusGatewayTimeout, flowsMsgStreamClosed},
		{"stream_not_closed_follows_its_cause_deadline", &fakeSendError{cmdID: "c", cause: context.DeadlineExceeded}, http.StatusGatewayTimeout, flowsMsgAckTimeout},
		{"stream_not_closed_follows_its_cause_other", &fakeSendError{cmdID: "c", cause: errors.New("boom")}, http.StatusInternalServerError, flowsMsgStartFailed},
		{"deadline_is_504", wrap(context.DeadlineExceeded), http.StatusGatewayTimeout, flowsMsgAckTimeout},
		{"canceled_is_504", wrap(context.Canceled), http.StatusGatewayTimeout, flowsMsgAckTimeout},
		{"anything_else_is_500_without_the_cause", errors.New("pgx: dsn=secreto"), http.StatusInternalServerError, flowsMsgStartFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			starter := &flowsStarterSpy{ack: flowsOKAck(), err: tc.err}
			h, rec := flowsStart(t, starter, flowsTargetStart, flowsStartBody)
			wantCode(t, tc.name, rec, tc.code)
			wantErrorBody(t, tc.name, rec, tc.msg)
			if starter.calls != 1 {
				t.Errorf("%s: el motor recibió %d arranques, quiero 1", tc.name, starter.calls)
			}
			flowsWantAudit(t, tc.name, h, flowsPermStart, flowsResource, "failure", tc.code)
		})
	}
}
