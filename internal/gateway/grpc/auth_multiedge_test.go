package gatewaygrpc_test

// auth_multiedge_test.go — DOS EDGE A LA VEZ, Y CADA RESPUESTA POR SU CABLE
// (Plan 057 · Ola 1 · T1.4 y T1.5).
//
// 🔴 EL INCIDENTE QUE ESTOS DOS TESTS CONGELAN (2026-09-03, UAT). Con el Edge de la
// Mac y el del VPS conectados a la vez contra el mismo gateway, el login del operador
// en la consola del VPS devolvió, tras los 20 s de su relay, un HTTP 503
// `relay_offline`. La respuesta —CON LOS TOKENS DEL OPERADOR— había salido por el
// cable de la Mac, que la descartó por `command_id` desconocido.
//
// La causa no era una carrera ni un fallo de red: los frames de auth del Edge estampan
// `cltransport.ControlSessionID` (`__wapp_control__`), una constante IDÉNTICA en todos
// los Edge del planeta, y el gateway la registraba en `session.Registry`, que es un
// `map[session_id]` SIN tenant y con política última-gana. El segundo Edge que
// conectaba PISABA la entrada del primero.
//
// Los dos tests van EN PAREJA y ninguno vale solo:
//   - el primero demuestra que dos Edge del MISMO cliente no se pisan (disponibilidad);
//   - el segundo demuestra que tampoco se pisan dos Edge de CLIENTES DISTINTOS, que es
//     la severidad real del defecto y lo que el análisis de origen no vio: el Registry
//     no conoce tenants, así que el token de un operador viajaba a la máquina de otra
//     empresa. Sin el segundo, un arreglo que solo calificara la clave por tenant
//     pasaría el primero y seguiría filtrando.
//
// 🔬 MUTACIÓN que pone a los dos en rojo: devolver `pushAuthResponse` (auth.go) a
// `s.registry.Push(ctx, cc.sessionID, msg)`.

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-cloudlink/mtls"
	cltransport "github.com/EduGoGroup/wapp-cloudlink/transport"
	"github.com/EduGoGroup/wapp-shared/logger"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/enroll"
	gatewaygrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/grpc"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
)

const (
	tenantA = "aaaaaaaa-1111-1111-1111-111111111111"
	tenantB = "bbbbbbbb-2222-2222-2222-222222222222"
)

// --- authenticator que responde SEGÚN EL TENANT del canal ---

// authPorTenant emite un access token derivado del tenant que le llega, y un
// IdentityContext con ESE mismo tenant (si devolviera otro, el guard de tenant cruzado
// del gateway respondería `tenant_mismatch` y el test mediría otra cosa).
//
// Devolver un token distinto por tenant no es cosmético: es lo que permite al segundo
// test afirmar QUÉ token recibió cada Edge, y no solo que recibió algo.
type authPorTenant struct{}

func (authPorTenant) Login(_ context.Context, req in.LoginInput) (domain.AuthResult, error) {
	return domain.AuthResult{
		AccessToken:  "access-" + req.TenantID,
		RefreshToken: "refresh-" + req.TenantID,
		TokenType:    "Bearer",
		ExpiresAt:    time.Now().Add(time.Hour),
		Context:      domain.IdentityContext{TenantID: req.TenantID, UserID: "u-1"},
	}, nil
}

func (authPorTenant) Refresh(_ context.Context, _ in.RefreshInput) (domain.AuthResult, error) {
	return domain.AuthResult{}, nil
}
func (authPorTenant) Logout(_ context.Context, _ in.LogoutInput) error { return nil }
func (authPorTenant) Verify(_ context.Context, _ string) (in.VerifyResult, error) {
	return in.VerifyResult{}, nil
}

// --- harness de N Edge contra UN gateway ---

// multiEdgeHarness levanta UN servidor gRPC con mTLS real sobre bufconn y permite
// abrir CUANTOS clientes se quiera, cada uno con su propio certificado de Edge. Es la
// diferencia con newAuthHarness (auth_test.go), que ata el servidor a un solo cert: el
// defecto que estos tests miden SOLO existe con dos streams vivos a la vez.
type multiEdgeHarness struct {
	ca  *enroll.CA
	lis *bufconn.Listener
}

func newMultiEdgeHarness(t *testing.T) *multiEdgeHarness {
	t.Helper()
	ca := newDevCA(t)

	srvCertPEM, srvKeyPEM, err := ca.IssueServerCert("localhost", []string{"localhost"}, []net.IP{net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("IssueServerCert: %v", err)
	}
	serverCert, err := tls.X509KeyPair(srvCertPEM, srvKeyPEM)
	if err != nil {
		t.Fatalf("X509KeyPair server: %v", err)
	}

	// Sin lease, sin fleet y sin ConfigProvider: así el ÚNICO CloudToEdge que un Edge
	// puede recibir es un UserAuthResponse, y «no recibió nada» significa exactamente
	// «no recibió respuesta de auth ajena» (mismo criterio que newAuthHarness).
	srv := gatewaygrpc.New(session.NewRegistry(), logger.New(logger.WithWriter(io.Discard)),
		gatewaygrpc.WithAuthenticator(authPorTenant{}),
		gatewaygrpc.WithAuthAuditor(&fakeAuditor{}),
	)

	lis := bufconn.Listen(bufSize)
	gs := grpc.NewServer(grpc.Creds(mtls.ServerCreds(serverCert, ca.Pool())))
	srv.Register(gs)

	serveErrc := make(chan error, 1)
	go func() { serveErrc <- gs.Serve(lis) }()
	t.Cleanup(func() {
		gs.Stop()
		<-serveErrc
		if err := lis.Close(); err != nil {
			t.Logf("lis.Close: %v", err)
		}
	})

	return &multiEdgeHarness{ca: ca, lis: lis}
}

// edgeVivo es un Edge conectado: su stream más un lector propio que va acumulando lo
// que el gateway le empuja, para poder preguntar tanto «¿recibí lo mío?» como
// «¿recibí algo que no era mío?».
type edgeVivo struct {
	nombre  string
	stream  grpc.BidiStreamingClient[cloudlinkv1.EdgeToCloud, cloudlinkv1.CloudToEdge]
	llegado chan *cloudlinkv1.CloudToEdge
}

// conecta abre un stream Connect con el certificado de (tenant, edge) dado y arranca
// su lector.
func (h *multiEdgeHarness) conecta(t *testing.T, nombre, tenantID, edgeID string) *edgeVivo {
	t.Helper()
	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return h.lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(mtls.ClientCreds(issueEdgeCert(t, h.ca, tenantID, edgeID), h.ca.Pool(), "localhost")),
	)
	if err != nil {
		t.Fatalf("[%s] grpc.NewClient: %v", nombre, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		if err := conn.Close(); err != nil {
			t.Logf("[%s] conn.Close: %v", nombre, err)
		}
	})

	stream, err := cloudlinkv1.NewCloudLinkClient(conn).Connect(ctx)
	if err != nil {
		t.Fatalf("[%s] Connect: %v", nombre, err)
	}

	e := &edgeVivo{nombre: nombre, stream: stream, llegado: make(chan *cloudlinkv1.CloudToEdge, 8)}
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				close(e.llegado)
				return
			}
			e.llegado <- msg
		}
	}()
	return e
}

// pideLogin envía un UserLogin con el session_id de CONTROL —el literal real que el
// Edge estampa, no uno inventado para el test: es la clave compartida la que provoca
// el defecto— y el command_id dado.
func (e *edgeVivo) pideLogin(t *testing.T, cmdID string) {
	t.Helper()
	if err := e.stream.Send(loginFrame(cltransport.ControlSessionID, cmdID, "op@example.com", "pw")); err != nil {
		t.Fatalf("[%s] Send login %s: %v", e.nombre, cmdID, err)
	}
}

// esperaRespuesta exige que llegue un UserAuthResponse con ESE command_id.
func (e *edgeVivo) esperaRespuesta(t *testing.T, cmdID string, d time.Duration) *cloudlinkv1.UserAuthResponse {
	t.Helper()
	select {
	case msg, ok := <-e.llegado:
		if !ok {
			t.Fatalf("[%s] el stream se cerró esperando la respuesta a %s", e.nombre, cmdID)
		}
		resp := msg.GetUserAuthResponse()
		if resp == nil {
			t.Fatalf("[%s] llegó un CloudToEdge que no es UserAuthResponse: %+v", e.nombre, msg)
		}
		if resp.GetCommandId() != cmdID {
			t.Fatalf("[%s] recibió la respuesta de OTRA petición: command_id %q, esperaba %q",
				e.nombre, resp.GetCommandId(), cmdID)
		}
		return resp
	case <-time.After(d):
		t.Fatalf("[%s] NO recibió su respuesta a %s en %s. Es el defecto del 2026-09-03: la "+
			"respuesta salió por el cable de otro Edge porque `__wapp_control__` es una clave "+
			"compartida en un registro plano de última-gana", e.nombre, cmdID, d)
		return nil
	}
}

// exigeSilencio exige que a este Edge NO le llegue nada. Es el aserto de la fuga: no
// basta con que el Edge correcto reciba lo suyo, hace falta que el ajeno no reciba
// nada — el Edge de producción DESCARTA por command_id lo que no pidió, así que un
// token filtrado no deja rastro en el receptor.
func (e *edgeVivo) exigeSilencio(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case msg, ok := <-e.llegado:
		if !ok {
			return // stream cerrado: tampoco recibió nada
		}
		if resp := msg.GetUserAuthResponse(); resp != nil {
			t.Fatalf("[%s] recibió un UserAuthResponse QUE NO PIDIÓ (command_id %q): es una fuga "+
				"de credenciales hacia el cable equivocado", e.nombre, resp.GetCommandId())
		}
		t.Fatalf("[%s] recibió un CloudToEdge que no le corresponde: %+v", e.nombre, msg)
	case <-time.After(d):
	}
}

// --- T1.4 ---

// TestMultiEdge_LoginConcurrenteNoColisiona: dos Edge del MISMO tenant, conectados a
// la vez, cada uno recibe SU token por SU cable.
//
// El orden es deliberado y hace el fallo DETERMINISTA, no probabilístico: el Edge B se
// conecta y hace su login DESPUÉS del primer login de A (así B queda como último
// registrado bajo la clave compartida), y solo entonces A vuelve a pedir. Con el
// código anterior a esta ola, esa segunda petición de A se la lleva B.
func TestMultiEdge_LoginConcurrenteNoColisiona(t *testing.T) {
	t.Parallel()
	h := newMultiEdgeHarness(t)

	vps := h.conecta(t, "vps", tenantA, "vmi3488280")
	vps.pideLogin(t, "cmd-vps-1")
	vps.esperaRespuesta(t, "cmd-vps-1", 5*time.Second)

	mac := h.conecta(t, "mac", tenantA, "MacBook-Pro-de-Jhoan.local")
	mac.pideLogin(t, "cmd-mac-1")
	mac.esperaRespuesta(t, "cmd-mac-1", 5*time.Second)

	// El VPS vuelve a pedir con la Mac ya registrada bajo la MISMA clave de control.
	vps.pideLogin(t, "cmd-vps-2")
	vps.esperaRespuesta(t, "cmd-vps-2", 5*time.Second)
	mac.exigeSilencio(t, 500*time.Millisecond)
}

// --- T1.5 ---

// TestMultiEdge_LoginDeOtroTenantNoLlegaAlEdgeAjeno: lo mismo, pero con los dos Edge
// en EMPRESAS DISTINTAS.
//
// 🔴 NO es un duplicado del anterior. Aquel demuestra disponibilidad; este demuestra
// que no hay FUGA DE CREDENCIALES ENTRE INQUILINOS, que es la severidad real del
// defecto: `session.Registry` indexa por session_id a secas, sin tenant, así que la
// colisión nunca estuvo acotada a un cliente. El aserto que lo prueba es el de
// silencio sobre el Edge ajeno, más la comprobación de que cada token es el de SU
// tenant.
func TestMultiEdge_LoginDeOtroTenantNoLlegaAlEdgeAjeno(t *testing.T) {
	t.Parallel()
	h := newMultiEdgeHarness(t)

	empresaA := h.conecta(t, "empresa-A", tenantA, "edge-de-A")
	empresaA.pideLogin(t, "cmd-A-1")
	if got := empresaA.esperaRespuesta(t, "cmd-A-1", 5*time.Second).GetTokens().GetAccessToken(); got != "access-"+tenantA {
		t.Fatalf("empresa-A recibió el token %q, esperaba el de su tenant", got)
	}

	empresaB := h.conecta(t, "empresa-B", tenantB, "edge-de-B")
	empresaB.pideLogin(t, "cmd-B-1")
	if got := empresaB.esperaRespuesta(t, "cmd-B-1", 5*time.Second).GetTokens().GetAccessToken(); got != "access-"+tenantB {
		t.Fatalf("empresa-B recibió el token %q, esperaba el de su tenant", got)
	}

	// A pide de nuevo con B registrado bajo la misma clave global.
	empresaA.pideLogin(t, "cmd-A-2")
	if got := empresaA.esperaRespuesta(t, "cmd-A-2", 5*time.Second).GetTokens().GetAccessToken(); got != "access-"+tenantA {
		t.Fatalf("empresa-A recibió el token %q, esperaba el de su tenant", got)
	}
	empresaB.exigeSilencio(t, 500*time.Millisecond)
}
