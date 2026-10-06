package grpc

// Quién firma cada evento (R-G10): lo que hace una PERSONA en la consola del Edge lleva su
// `sub` y la etiqueta `operator`; lo que hace el PROCESO del Edge lleva su edge_id y la
// etiqueta `daemon`; y los dos se distinguen en la misma bitácora. Siempre con el tenant del
// canal, sin PII, y sin que la auditoría pueda cambiar la respuesta.

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/session"
)

// operatorEvent es el evento edge.auth.* esperado para una petición del operador que entró por
// el canal de control de (tenant-1, edge-1) con ese command_id.
func operatorEvent(cc connCtx, action, result, cmdID, actor string) in.AuditInput {
	return in.AuditInput{
		TenantID: cc.tenantID,
		Actor:    actor,
		Action:   action,
		Resource: "edge.auth",
		Result:   result,
		Meta: map[string]any{
			"actor_type": "operator",
			"edge_id":    cc.edgeID,
			"session_id": cc.sessionID,
			"command_id": cmdID,
			"channel":    "cloudlink",
		},
	}
}

// requireEvents afirma que la bitácora tiene exactamente esos eventos, campo a campo.
func requireEvents(t *testing.T, audit *auditLog, want ...in.AuditInput) {
	t.Helper()
	got := audit.recorded()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("bitácora:\n  got  %+v\n  want %+v", got, want)
	}
	requireNoSecrets(t, "la bitácora", fmt.Sprintf("%+v", got))
}

// Cada desenlace de login, refresh y logout deja UN evento del operador. El actor es su `sub`
// cuando llegó a resolverse —también en el tenant cruzado, que es justo a quien hay que poder
// nombrar— y vacío cuando el fallo fue antes; el logout nunca lo conoce.
func TestOperatorActionsAreAuditedWithTheirSub(t *testing.T) {
	t.Parallel()
	const sub = "7c9e6679-7425-40de-944b-e07fc1f90ae7"
	control := controlChannel("tenant-1", "edge-1", nil)
	anonymous := connCtx{sessionID: "s-1"}
	denied := func() *scriptedAuth {
		return &scriptedAuth{
			login: func(in.LoginInput) (domain.AuthResult, error) {
				return domain.AuthResult{}, domain.ErrInvalidCredentials
			},
			refresh: func(in.RefreshInput) (domain.AuthResult, error) { return domain.AuthResult{}, domain.ErrRefreshInvalid },
			logout:  func(in.LogoutInput) error { return errors.New("identity caído") },
		}
	}
	foreign := func() *scriptedAuth {
		other := func() (domain.AuthResult, error) { return tokensFor("tenant-ajeno", sub), nil }
		return &scriptedAuth{
			login:   func(in.LoginInput) (domain.AuthResult, error) { return other() },
			refresh: func(in.RefreshInput) (domain.AuthResult, error) { return other() },
		}
	}
	login := func(srv *Server, cc connCtx) { srv.handleUserLogin(context.Background(), cc, loginRequest("cmd-1")) }
	refresh := func(srv *Server, cc connCtx) {
		srv.handleUserRefresh(context.Background(), cc, refreshRequest("cmd-1", "refresh-tenant-1"))
	}
	logout := func(srv *Server, cc connCtx) {
		srv.handleUserLogout(context.Background(), cc, logoutRequest("cmd-1", "refresh-tenant-1", true))
	}
	cases := []struct {
		name    string
		authn   *scriptedAuth
		cc      connCtx
		request func(*Server, connCtx)
		action  string
		result  string
		actor   string
	}{
		{"login ok", echoTenantAuth(sub), control, login, "edge.auth.login", "ok", sub},
		{"login rejected by the port", denied(), control, login, "edge.auth.login", "error", ""},
		{"login from another tenant", foreign(), control, login, "edge.auth.login", "error", sub},
		{"login without channel identity", echoTenantAuth(sub), anonymous, login, "edge.auth.login", "error", ""},
		{"refresh ok", echoTenantAuth(sub), control, refresh, "edge.auth.refresh", "ok", sub},
		{"refresh rejected by the port", denied(), control, refresh, "edge.auth.refresh", "error", ""},
		{"refresh from another tenant", foreign(), control, refresh, "edge.auth.refresh", "error", sub},
		{"refresh without channel identity", echoTenantAuth(sub), anonymous, refresh, "edge.auth.refresh", "error", ""},
		{"logout ok", echoTenantAuth(sub), control, logout, "edge.auth.logout", "ok", ""},
		{"logout failed", denied(), control, logout, "edge.auth.logout", "error", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rig := newAuthRig(t, tc.authn)
			cc := tc.cc
			cc.sender = &liveSession{id: "el stream del Edge"}

			tc.request(rig.srv, cc)

			requireEvents(t, rig.audit, operatorEvent(cc, tc.action, tc.result, "cmd-1", tc.actor))
		})
	}
}

// Abrir una sesión CloudLink es una acción del PROCESO del Edge: el actor es su edge_id (el CN
// de su certificado) y la etiqueta es `daemon`. Sin command_id: nadie lo pidió.
func TestSessionOpenIsAuditedAsTheDaemon(t *testing.T) {
	t.Parallel()
	rig := newAuthRig(t, echoTenantAuth("user-1"))

	rig.srv.recordEdgeSession(context.Background(), phone("tenant-1", "edge-1", "s-1"))

	requireEvents(t, rig.audit, in.AuditInput{
		TenantID: "tenant-1",
		Actor:    "edge-1",
		Action:   "edge.session.open",
		Resource: "edge.session",
		Result:   "ok",
		Meta:     map[string]any{"actor_type": "daemon", "edge_id": "edge-1", "session_id": "s-1", "channel": "cloudlink"},
	})
}

// Sin identidad mTLS no hay Edge al que atribuir la apertura: no se registra.
func TestSessionOpenWithoutIdentityIsNotAudited(t *testing.T) {
	t.Parallel()
	rig := newAuthRig(t, echoTenantAuth("user-1"))
	rig.srv.recordEdgeSession(context.Background(), connCtx{sessionID: "s-1"})
	requireEvents(t, rig.audit)
}

// La pregunta que R-G10 exige poder contestar: de todo lo que pasó por un canal, qué lo hizo
// la persona y qué lo hizo la máquina. Ningún evento queda sin etiqueta.
func TestOperatorAndDaemonAreToldApartInTheSameLog(t *testing.T) {
	t.Parallel()
	const sub = "7c9e6679-7425-40de-944b-e07fc1f90ae7"
	rig := newAuthRig(t, echoTenantAuth(sub))
	cc := phone("tenant-1", "edge-1", "s-1")
	cc.sender = &liveSession{id: "el stream del Edge"}

	rig.srv.recordEdgeSession(context.Background(), cc)
	rig.srv.handleUserLogin(context.Background(), cc, loginRequest("cmd-1"))

	byType := map[any][]string{}
	for _, ev := range rig.audit.recorded() {
		byType[ev.Meta["actor_type"]] = append(byType[ev.Meta["actor_type"]], ev.Actor)
	}
	want := map[any][]string{"daemon": {"edge-1"}, "operator": {sub}}
	if !reflect.DeepEqual(byType, want) {
		t.Errorf("actores por tipo = %v, se esperaba %v", byType, want)
	}
}

// La auditoría es best-effort: sin auditor la auth funciona igual, y un auditor que falla no
// cambia la respuesta; solo deja su rastro en debug, con la acción.
func TestAuditNeverChangesTheAnswer(t *testing.T) {
	t.Parallel()
	t.Run("without auditor", func(t *testing.T) {
		t.Parallel()
		srv := New(session.NewRegistry(), quietLog(), WithAuthenticator(echoTenantAuth("user-1")))
		stream := &liveSession{id: "el stream del Edge"}
		cc := controlChannel("tenant-1", "edge-1", stream)

		srv.recordEdgeSession(context.Background(), cc)
		srv.handleUserLogin(context.Background(), cc, loginRequest("cmd-1"))

		requireTokens(t, onlyAuthResponse(t, stream, cc, "cmd-1"), tokensFor("tenant-1", "user-1"))
	})
	t.Run("failing auditor", func(t *testing.T) {
		t.Parallel()
		rig := newAuthRig(t, echoTenantAuth("user-1"))
		rig.audit.err = errors.New("base caída")
		stream := &liveSession{id: "el stream del Edge"}
		cc := controlChannel("tenant-1", "edge-1", stream)

		rig.srv.recordEdgeSession(context.Background(), cc)
		rig.srv.handleUserLogin(context.Background(), cc, loginRequest("cmd-1"))

		requireTokens(t, onlyAuthResponse(t, stream, cc, "cmd-1"), tokensFor("tenant-1", "user-1"))
		for _, want := range []string{
			"level=DEBUG", `msg="auth: registrar auditoría de sesión"`,
			`msg="auth: registrar auditoría"`, "action=edge.auth.login", "base caída",
		} {
			if !rig.log.contains(want) {
				t.Errorf("al log le falta %q: %q", want, rig.log.String())
			}
		}
		requireNoSecrets(t, "el log", rig.log.String())
	})
}
