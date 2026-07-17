package gatewaygrpc_test

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-cloudlink/mtls"
	"github.com/EduGoGroup/wapp-shared/logger"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/enroll"
	gatewaygrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/grpc"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
)

// --- fakes del IAM (Plan 033 · T2.4) ---

// fakeAuthenticator implementa in.Authenticator con respuestas programadas, para
// ejercitar el gateway sin BD (Login/Refresh/Logout).
type fakeAuthenticator struct {
	loginResult   domain.AuthResult
	loginErr      error
	loginGotInput in.LoginInput

	refreshResult domain.AuthResult
	refreshErr    error

	logoutErr    error
	logoutGotIn  in.LogoutInput
	logoutCalled bool
}

func (f *fakeAuthenticator) Login(_ context.Context, req in.LoginInput) (domain.AuthResult, error) {
	f.loginGotInput = req
	return f.loginResult, f.loginErr
}

func (f *fakeAuthenticator) Refresh(_ context.Context, _ in.RefreshInput) (domain.AuthResult, error) {
	return f.refreshResult, f.refreshErr
}

func (f *fakeAuthenticator) Logout(_ context.Context, req in.LogoutInput) error {
	f.logoutCalled = true
	f.logoutGotIn = req
	return f.logoutErr
}

func (f *fakeAuthenticator) Verify(_ context.Context, _ string) (in.VerifyResult, error) {
	return in.VerifyResult{}, nil
}

// fakeAuditor implementa in.Auditor y captura los eventos registrados (thread-safe).
type fakeAuditor struct {
	mu     sync.Mutex
	events []in.AuditInput
}

func (f *fakeAuditor) Record(_ context.Context, e in.AuditInput) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, e)
	return nil
}

func (f *fakeAuditor) ListAudit(_ context.Context, _ string, _, _ int) ([]domain.AuditEvent, error) {
	return nil, nil
}

func (f *fakeAuditor) snapshot() []in.AuditInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]in.AuditInput, len(f.events))
	copy(out, f.events)
	return out
}

// --- harness de auth (mTLS real ⇒ peerIdentity extrae tenant/edge del cert) ---

type authHarness struct {
	authn   *fakeAuthenticator
	auditor *fakeAuditor
	client  cloudlinkv1.CloudLinkClient
}

// newAuthHarness levanta el Server SOLO con el authenticator+auditor (sin lease/
// fleet/config): así el ÚNICO CloudToEdge que el cliente recibe es el
// UserAuthResponse (no hay push de lease ni de jwks que filtrar). La identidad
// (tenant/edge) sale del cert mTLS real, igual que en producción.
func newAuthHarness(t *testing.T, ca *enroll.CA, edgeCert tls.Certificate, authn *fakeAuthenticator, auditor *fakeAuditor) *authHarness {
	t.Helper()

	reg := session.NewRegistry()
	log := logger.New(logger.WithWriter(io.Discard))
	srv := gatewaygrpc.New(reg, log,
		gatewaygrpc.WithAuthenticator(authn),
		gatewaygrpc.WithAuthAuditor(auditor),
	)

	srvCertPEM, srvKeyPEM, err := ca.IssueServerCert("localhost", []string{"localhost"}, []net.IP{net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("IssueServerCert: %v", err)
	}
	serverCert, err := tls.X509KeyPair(srvCertPEM, srvKeyPEM)
	if err != nil {
		t.Fatalf("X509KeyPair server: %v", err)
	}

	lis := bufconn.Listen(bufSize)
	gs := grpc.NewServer(grpc.Creds(mtls.ServerCreds(serverCert, ca.Pool())))
	srv.Register(gs)

	serveErrc := make(chan error, 1)
	go func() { serveErrc <- gs.Serve(lis) }()

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(mtls.ClientCreds(edgeCert, ca.Pool(), "localhost")),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}

	t.Cleanup(func() {
		if closeErr := conn.Close(); closeErr != nil {
			t.Logf("conn.Close: %v", closeErr)
		}
		gs.Stop()
		<-serveErrc
		if closeErr := lis.Close(); closeErr != nil {
			t.Logf("lis.Close: %v", closeErr)
		}
	})

	return &authHarness{
		authn:   authn,
		auditor: auditor,
		client:  cloudlinkv1.NewCloudLinkClient(conn),
	}
}

// recvAuthResponse envía el frame dado y devuelve el UserAuthResponse recibido.
func recvAuthResponse(t *testing.T, client cloudlinkv1.CloudLinkClient, frame *cloudlinkv1.EdgeToCloud) *cloudlinkv1.UserAuthResponse {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	stream, err := client.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := stream.Send(frame); err != nil {
		t.Fatalf("Send: %v", err)
	}
	cmd, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	resp := cmd.GetUserAuthResponse()
	if resp == nil {
		t.Fatalf("primer CloudToEdge no es UserAuthResponse: %+v", cmd)
	}
	return resp
}

func loginFrame(sessionID, cmdID, email, pass string) *cloudlinkv1.EdgeToCloud {
	return &cloudlinkv1.EdgeToCloud{
		CommandId: cmdID,
		SessionId: sessionID,
		Payload: &cloudlinkv1.EdgeToCloud_UserLogin{UserLogin: &cloudlinkv1.UserLoginRequest{
			CommandId: cmdID, SessionId: sessionID, Email: email, Password: pass,
		}},
	}
}

// findAudit devuelve el primer evento con la acción dada (o falla).
func findAudit(t *testing.T, events []in.AuditInput, action string) in.AuditInput {
	t.Helper()
	for _, e := range events {
		if e.Action == action {
			return e
		}
	}
	t.Fatalf("no se registró auditoría con acción %q (eventos: %+v)", action, events)
	return in.AuditInput{}
}

// --- tests ---

// TestUserLoginTenantMismatch: el AuthResult del IAM trae un tenant DISTINTO al del
// canal mTLS ⇒ UserAuthError{tenant_mismatch}, SIN tokens, y auditado como error
// (guard tenant cruzado, ADR-0025).
func TestUserLoginTenantMismatch(t *testing.T) {
	t.Parallel()
	ca := newDevCA(t)
	authn := &fakeAuthenticator{loginResult: domain.AuthResult{
		AccessToken: "should-not-be-delivered",
		Context:     domain.IdentityContext{TenantID: "otro-tenant", UserID: "u-1"},
	}}
	auditor := &fakeAuditor{}
	h := newAuthHarness(t, ca, issueEdgeCert(t, ca, testTenantID, testEdgeID), authn, auditor)

	resp := recvAuthResponse(t, h.client, loginFrame("s1", "cmd-1", "op@example.com", "pw"))

	if resp.GetTokens() != nil {
		t.Fatalf("tenant cruzado NO debe entregar tokens: %+v", resp.GetTokens())
	}
	if got := resp.GetError().GetCode(); got != "tenant_mismatch" {
		t.Fatalf("code: got %q, want tenant_mismatch", got)
	}
	if resp.GetCommandId() != "cmd-1" || resp.GetSessionId() != "s1" {
		t.Fatalf("correlación incorrecta: %+v", resp)
	}
	// El login se acotó al tenant del canal (no al del cuerpo).
	if authn.loginGotInput.TenantID != testTenantID {
		t.Fatalf("Login debía acotarse al tenant del canal %q, got %q", testTenantID, authn.loginGotInput.TenantID)
	}
	ev := findAudit(t, auditor.snapshot(), "edge.auth.login")
	if ev.Result != "error" {
		t.Fatalf("auditoría: result got %q, want error", ev.Result)
	}
	if ev.TenantID != testTenantID {
		t.Fatalf("auditoría: tenant debe ser el del canal, got %q", ev.TenantID)
	}
	if ev.Meta["edge_id"] != testEdgeID {
		t.Fatalf("auditoría: edge_id en meta got %v", ev.Meta["edge_id"])
	}
}

// TestUserLoginInvalidCredentials: el IAM devuelve ErrInvalidCredentials ⇒
// UserAuthError{invalid_credentials}, sin tokens.
func TestUserLoginInvalidCredentials(t *testing.T) {
	t.Parallel()
	ca := newDevCA(t)
	authn := &fakeAuthenticator{loginErr: domain.ErrInvalidCredentials}
	auditor := &fakeAuditor{}
	h := newAuthHarness(t, ca, issueEdgeCert(t, ca, testTenantID, testEdgeID), authn, auditor)

	resp := recvAuthResponse(t, h.client, loginFrame("s1", "cmd-2", "op@example.com", "bad"))

	if resp.GetTokens() != nil {
		t.Fatal("credenciales inválidas NO deben entregar tokens")
	}
	if got := resp.GetError().GetCode(); got != "invalid_credentials" {
		t.Fatalf("code: got %q, want invalid_credentials", got)
	}
	ev := findAudit(t, auditor.snapshot(), "edge.auth.login")
	if ev.Result != "error" {
		t.Fatalf("auditoría: result got %q, want error", ev.Result)
	}
}

// TestUserRefreshRotates: el IAM rota y devuelve un par nuevo (tenant coherente con
// el canal) ⇒ UserTokens con el par nuevo.
func TestUserRefreshRotates(t *testing.T) {
	t.Parallel()
	ca := newDevCA(t)
	exp := time.Now().Add(15 * time.Minute).Truncate(time.Second)
	authn := &fakeAuthenticator{refreshResult: domain.AuthResult{
		AccessToken:  "new-access",
		RefreshToken: "new-refresh",
		TokenType:    "Bearer",
		ExpiresAt:    exp,
		Context:      domain.IdentityContext{TenantID: testTenantID, UserID: "u-1"},
	}}
	auditor := &fakeAuditor{}
	h := newAuthHarness(t, ca, issueEdgeCert(t, ca, testTenantID, testEdgeID), authn, auditor)

	frame := &cloudlinkv1.EdgeToCloud{
		CommandId: "cmd-3",
		SessionId: "s1",
		Payload: &cloudlinkv1.EdgeToCloud_UserRefresh{UserRefresh: &cloudlinkv1.UserRefreshRequest{
			CommandId: "cmd-3", SessionId: "s1", RefreshToken: "old-refresh",
		}},
	}
	resp := recvAuthResponse(t, h.client, frame)

	tk := resp.GetTokens()
	if tk == nil {
		t.Fatalf("refresh ok debe entregar tokens: %+v", resp)
	}
	if tk.GetAccessToken() != "new-access" || tk.GetRefreshToken() != "new-refresh" {
		t.Fatalf("par rotado incorrecto: %+v", tk)
	}
	if tk.GetExpiresAt() != exp.Unix() {
		t.Fatalf("expires_at: got %d, want %d", tk.GetExpiresAt(), exp.Unix())
	}
	ev := findAudit(t, auditor.snapshot(), "edge.auth.refresh")
	if ev.Result != "ok" || ev.Actor != "u-1" {
		t.Fatalf("auditoría refresh inesperada: %+v", ev)
	}
}

// TestUserLogout: el IAM revoca OK ⇒ rama Tokens con UserTokens VACÍO (convención
// del contrato) y el refresh token del request llega al IAM.
func TestUserLogout(t *testing.T) {
	t.Parallel()
	ca := newDevCA(t)
	authn := &fakeAuthenticator{}
	auditor := &fakeAuditor{}
	h := newAuthHarness(t, ca, issueEdgeCert(t, ca, testTenantID, testEdgeID), authn, auditor)

	frame := &cloudlinkv1.EdgeToCloud{
		CommandId: "cmd-4",
		SessionId: "s1",
		Payload: &cloudlinkv1.EdgeToCloud_UserLogout{UserLogout: &cloudlinkv1.UserLogoutRequest{
			CommandId: "cmd-4", SessionId: "s1", RefreshToken: "rt-xyz",
		}},
	}
	resp := recvAuthResponse(t, h.client, frame)

	tk := resp.GetTokens()
	if tk == nil {
		t.Fatalf("logout ok debe usar la rama Tokens (vacía): %+v", resp)
	}
	if tk.GetAccessToken() != "" || tk.GetRefreshToken() != "" || tk.GetTokenType() != "" || tk.GetExpiresAt() != 0 {
		t.Fatalf("logout ok: UserTokens debe ir VACÍO, got %+v", tk)
	}
	if !authn.logoutCalled || authn.logoutGotIn.RefreshToken != "rt-xyz" {
		t.Fatalf("el refresh token no llegó al IAM: %+v", authn.logoutGotIn)
	}
	ev := findAudit(t, auditor.snapshot(), "edge.auth.logout")
	if ev.Result != "ok" {
		t.Fatalf("auditoría logout: result got %q, want ok", ev.Result)
	}
}
