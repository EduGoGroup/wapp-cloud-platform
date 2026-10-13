package admin_test

// handlers_start_test.go — POST /admin/flows/start: qué identidad de contacto llega al
// Starter, cómo se refleja el Ack y a qué código y texto se traduce cada error de
// Start. Trozo de handlers_test.go, partido por tema (E-13).

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/admin"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/session"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

type fakeStarter struct {
	ack *cloudlinkv1.Ack
	err error

	calls       int
	gotIdentity httpapi.Identity
	gotTenant   string
	gotFlowID   string
	gotSession  string
	gotRef      contact.Ref
}

func (f *fakeStarter) Start(ctx context.Context, tenantID, flowID, sessionID string, ref contact.Ref) (*cloudlinkv1.Ack, error) {
	f.calls++
	f.gotIdentity, _ = httpapi.IdentityFromContext(ctx)
	f.gotTenant, f.gotFlowID, f.gotSession, f.gotRef = tenantID, flowID, sessionID, ref
	return f.ack, f.err
}

// startBody usa el alias `contact` y un tenant_id intruso.
const startBody = `{"tenant_id":"tenant-del-cuerpo","flow_id":"menu-soporte","session_id":"s1","contact":"573001112233"}`

func mustRef(t *testing.T, kind, value string) contact.Ref {
	t.Helper()
	ref, err := contact.NewRef(kind, value)
	if err != nil {
		t.Fatalf("fixture: NewRef(%q, %q): %v", kind, value, err)
	}
	return ref
}

func invalidRefMessage(t *testing.T, kind, value string) string {
	t.Helper()
	_, err := contact.NewRef(kind, value)
	if err == nil {
		t.Fatalf("fixture: NewRef(%q, %q) debería fallar", kind, value)
	}
	return "contact_ref inválida: " + err.Error()
}

func TestStartHandler_ResolvesTheContact(t *testing.T) {
	t.Parallel()
	const head = `{"tenant_id":"tenant-del-cuerpo","flow_id":" menu-soporte","session_id":"s1 ",`
	cases := []struct {
		name string
		body string
		kind string
		val  string
	}{
		{"plain contact alias is a phone", head + `"contact":"573001112233"}`, contact.KindPhoneE164, "573001112233"},
		{"contact_ref is normalized", head + `"contact_ref":{"kind":"wa_lid","value":"88887777@lid"}}`, contact.KindWALID, "88887777@lid"},
		{"contact_ref wins over the alias", head + `"contact":"573001112233","contact_ref":{"kind":"wa_lid","value":"88887777"}}`,
			contact.KindWALID, "88887777"},
		{"contact_ref with empty value falls back to the alias", head + `"contact":"573001112233","contact_ref":{"kind":"wa_lid","value":""}}`,
			contact.KindPhoneE164, "573001112233"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := &fakeStarter{ack: &cloudlinkv1.Ack{AckedCommandId: "cmd-1", Ok: true}}

			rec := serve(admin.StartHandler(st), operator(), http.MethodPost, "/admin/flows/start", tc.body)

			wantJSON(t, rec, http.StatusOK, `{"acked_command_id":"cmd-1","ok":true}`)
			if st.calls != 1 {
				t.Fatalf("Start se llamó %d veces, quiero 1", st.calls)
			}
			if want := mustRef(t, tc.kind, tc.val); st.gotRef != want {
				t.Errorf("ref = %+v, quiero %+v", st.gotRef, want)
			}
			if st.gotTenant != tokenTenant || st.gotIdentity.TenantID != tokenTenant {
				t.Errorf("tenant = %q (identidad del ctx %+v), quiero el del token", st.gotTenant, st.gotIdentity)
			}
			// flow_id y session_id viajan TAL CUAL, sin recortar.
			if st.gotFlowID != " menu-soporte" || st.gotSession != "s1 " {
				t.Errorf("flow_id=%q session_id=%q, quiero los del cuerpo sin tocar", st.gotFlowID, st.gotSession)
			}
		})
	}
}

func TestStartHandler_ReflectsTheAck(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		ack  *cloudlinkv1.Ack
		want string
	}{
		{"ack ok omits error", &cloudlinkv1.Ack{AckedCommandId: "cmd-1", Ok: true}, `{"acked_command_id":"cmd-1","ok":true}`},
		{"edge error is still a 200", &cloudlinkv1.Ack{AckedCommandId: "cmd-2", Error: "edge: sin sesión"},
			`{"acked_command_id":"cmd-2","ok":false,"error":"edge: sin sesión"}`},
		{"nil ack", nil, `{"acked_command_id":"","ok":false}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := serve(admin.StartHandler(&fakeStarter{ack: tc.ack}), operator(), http.MethodPost, "/admin/flows/start", startBody)
			wantJSON(t, rec, http.StatusOK, tc.want)
		})
	}
}

func TestStartHandler_Rejections(t *testing.T) {
	t.Parallel()
	const (
		msgFields  = "flow_id y session_id son requeridos"
		msgContact = "se requiere contact_ref {kind,value} o contact (alias phone_e164)"
	)
	cases := []struct {
		name     string
		id       *httpapi.Identity
		method   string
		body     string
		wantCode int
		wantMsg  string
	}{
		{"no identity", nil, http.MethodPost, startBody, http.StatusUnauthorized, msgAuthRequired},
		{"identity without tenant", &httpapi.Identity{Subject: "user-1"}, http.MethodPost, startBody, http.StatusUnauthorized, msgAuthRequired},
		{"malformed json", operator(), http.MethodPost, `{`, http.StatusBadRequest, msgInvalidJSON},
		{"missing flow_id", operator(), http.MethodPost, `{"session_id":"s1","contact":"573001112233"}`, http.StatusBadRequest, msgFields},
		{"missing session_id", operator(), http.MethodPost, `{"flow_id":"f","contact":"573001112233"}`, http.StatusBadRequest, msgFields},
		{"fields are checked before the contact", operator(), http.MethodPost, `{"flow_id":"f"}`, http.StatusBadRequest, msgFields},
		{"no contact at all", operator(), http.MethodPost, `{"flow_id":"f","session_id":"s1"}`, http.StatusBadRequest, msgContact},
		{"unknown contact_ref kind", operator(), http.MethodPost,
			`{"flow_id":"f","session_id":"s1","contact_ref":{"kind":"telegram","value":"x"}}`,
			http.StatusBadRequest, invalidRefMessage(t, "telegram", "x")},
		{"alias that is not a phone", operator(), http.MethodPost, `{"flow_id":"f","session_id":"s1","contact":"no-es-un-numero"}`,
			http.StatusBadRequest, invalidRefMessage(t, contact.KindPhoneE164, "no-es-un-numero")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := &fakeStarter{ack: &cloudlinkv1.Ack{Ok: true}}
			rec := serve(admin.StartHandler(st), tc.id, tc.method, "/admin/flows/start", tc.body)
			wantPlainError(t, rec, tc.wantCode, tc.wantMsg)
			if st.calls != 0 {
				t.Errorf("Start se llamó %d veces en una petición rechazada", st.calls)
			}
		})
	}
}

// El texto de contact.NewRef ya empieza por "contact_ref inválida": sale dos veces.
func TestStartHandler_InvalidRefRepeatsThePrefix(t *testing.T) {
	t.Parallel()
	rec := serve(admin.StartHandler(&fakeStarter{}), operator(), http.MethodPost, "/admin/flows/start",
		`{"flow_id":"f","session_id":"s1","contact_ref":{"kind":"telegram","value":"x"}}`)
	const twice = "contact_ref inválida: contact_ref inválida"
	if !strings.HasPrefix(rec.Body.String(), twice) {
		t.Errorf("cuerpo = %q, quiero que empiece por %q", rec.Body.String(), twice)
	}
}

// streamDownError imita el error del Gateway: la causa envuelta y el método del
// duck-typing, cuyo VALOR —no su mera presencia— es lo que mira el handler.
type streamDownError struct {
	cause error
	down  bool
}

func (e *streamDownError) Error() string     { return fmt.Sprintf("runtime: enviar texto: %v", e.cause) }
func (e *streamDownError) Unwrap() error     { return e.cause }
func (e *streamDownError) StreamCaido() bool { return e.down }

func TestStartHandler_TranslatesStartErrors(t *testing.T) {
	t.Parallel()
	const (
		msgExists  = "ya existe una conversación viva para la clave"
		msgDurable = "el flujo tiene contenido durable (cart/survey): su evento nace en la conversación, no por " +
			"esta API. Configura una regla event_start para este flujo (POST /api/v1/triggers) para que el " +
			"cliente lo arranque escribiendo su palabra clave; no reintentes esta llamada, seguirá devolviendo 409"
		msgOffline    = "sesión offline: no hay stream vivo para el Edge"
		msgStreamDown = "el stream del Edge se cerró antes del ack: la conversación YA quedó abierta y el " +
			"comando de su primer mensaje viajó al Edge, así que no se sabe si el cliente llegó a recibirlo. " +
			"NO reintentes este arranque —devolverá 409—: comprueba la conversación y, si el primer mensaje " +
			"no salió, continúala sobre la que ya existe"
		msgTimeout = "timeout esperando el ack del Edge"
		msgGeneric = "no se pudo iniciar la conversación"
	)
	wrap := func(err error) error { return fmt.Errorf("runtime: enviar texto: %w", err) }
	errOther := errors.New("otra cosa")
	cases := []struct {
		name     string
		err      error
		wantCode int
		wantMsg  string
	}{
		{"conversation exists", runtime.ErrConversationExists, http.StatusConflict, msgExists},
		{"conversation exists, wrapped", wrap(runtime.ErrConversationExists), http.StatusConflict, msgExists},
		{"durable flow needs an event", wrap(runtime.ErrDurableFlowNeedsEvent), http.StatusConflict, msgDurable},
		{"session offline, edge sentinel", wrap(session.ErrSessionOffline), http.StatusBadGateway, msgOffline},
		{"session offline, httpapi sentinel is the same variable", wrap(httpapi.ErrSessionOffline), http.StatusBadGateway, msgOffline},
		{"stream down", &streamDownError{cause: errOther, down: true}, http.StatusGatewayTimeout, msgStreamDown},
		{"stream down, wrapped", wrap(&streamDownError{cause: errOther, down: true}), http.StatusGatewayTimeout, msgStreamDown},
		{"stream down wins over the deadline", &streamDownError{cause: context.DeadlineExceeded, down: true},
			http.StatusGatewayTimeout, msgStreamDown},
		{"existing conversation wins over stream down", &streamDownError{cause: runtime.ErrConversationExists, down: true},
			http.StatusConflict, msgExists},
		{"session offline wins over stream down", &streamDownError{cause: session.ErrSessionOffline, down: true},
			http.StatusBadGateway, msgOffline},
		{"stream not down falls to its cause: deadline", &streamDownError{cause: context.DeadlineExceeded},
			http.StatusGatewayTimeout, msgTimeout},
		{"stream not down falls to its cause: other", &streamDownError{cause: errOther}, http.StatusInternalServerError, msgGeneric},
		{"deadline exceeded", wrap(context.DeadlineExceeded), http.StatusGatewayTimeout, msgTimeout},
		{"canceled", wrap(context.Canceled), http.StatusGatewayTimeout, msgTimeout},
		{"anything else", errOther, http.StatusInternalServerError, msgGeneric},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := serve(admin.StartHandler(&fakeStarter{err: tc.err}), operator(), http.MethodPost, "/admin/flows/start", startBody)
			wantPlainError(t, rec, tc.wantCode, tc.wantMsg)
		})
	}
}
