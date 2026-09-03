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
	"strings"
	"sync"
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

// Refresh deriva el tenant del propio refresh token que Login emitió ("refresh-<tenant>"):
// el contrato de in.RefreshInput no lleva tenant, y el gateway compara el del resultado
// con el del canal mTLS. Sin esta derivación, todo refresh respondería tenant_mismatch y
// el test de la desconexión cruzada mediría otra cosa.
func (authPorTenant) Refresh(_ context.Context, req in.RefreshInput) (domain.AuthResult, error) {
	tenant := strings.TrimPrefix(req.RefreshToken, "refresh-")
	return domain.AuthResult{
		AccessToken:  "access-" + tenant,
		RefreshToken: "refresh-" + tenant,
		TokenType:    "Bearer",
		ExpiresAt:    time.Now().Add(time.Hour),
		Context:      domain.IdentityContext{TenantID: tenant, UserID: "u-1"},
	}, nil
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
	srv *gatewaygrpc.Server
}

func newMultiEdgeHarness(t *testing.T, opts ...gatewaygrpc.Option) *multiEdgeHarness {
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
		append([]gatewaygrpc.Option{
			gatewaygrpc.WithAuthenticator(authPorTenant{}),
			gatewaygrpc.WithAuthAuditor(&fakeAuditor{}),
		}, opts...)...,
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

	return &multiEdgeHarness{ca: ca, lis: lis, srv: srv}
}

// edgeVivo es un Edge conectado: su stream más un lector propio que va acumulando lo
// que el gateway le empuja, para poder preguntar tanto «¿recibí lo mío?» como
// «¿recibí algo que no era mío?».
type edgeVivo struct {
	nombre  string
	stream  grpc.BidiStreamingClient[cloudlinkv1.EdgeToCloud, cloudlinkv1.CloudToEdge]
	llegado chan *cloudlinkv1.CloudToEdge
	// cierra corta el stream como lo haría un Edge que se apaga: es lo que dispara el
	// closeStream del gateway y, con él, el release() de todo lo que ese stream tuviera
	// registrado.
	cierra func()
	// apartados guarda los frames que un helper sacó del canal buscando OTRA cosa.
	//
	// 🔴 Sin esto los helpers se pisan entre sí y el test miente en la dirección
	// PEOR: `esperaRespuesta` descartaría el ConfigUpdate inicial mientras busca la
	// respuesta del login, y el `esperaConfigCon` posterior daría por no entregada una
	// config que sí llegó. Se ordena por el lado del test, no relajando el aserto.
	// Todos los helpers corren en la goroutine del test, así que no necesita candado.
	apartados []*cloudlinkv1.CloudToEdge
}

// busca devuelve el primer frame que cumpla `pred`, mirando primero entre los que otro
// helper apartó y luego el canal. Lo que no cumple se APARTA, no se tira.
//
// 🔴 Los que ya estaban apartados y no cumplen NO se vuelven a apartar: recorrerlos con
// índice y dejarlos donde están es lo que evita el bucle infinito de reinyectar el
// mismo frame que se acaba de sacar.
//
// Devuelve (frame, streamAbierto, encontrado).
func (e *edgeVivo) busca(d time.Duration, pred func(*cloudlinkv1.CloudToEdge) bool) (*cloudlinkv1.CloudToEdge, bool, bool) {
	for i, msg := range e.apartados {
		if pred(msg) {
			e.apartados = append(e.apartados[:i], e.apartados[i+1:]...)
			return msg, true, true
		}
	}
	fin := time.Now().Add(d)
	for {
		restante := time.Until(fin)
		if restante <= 0 {
			return nil, true, false
		}
		select {
		case msg, abierto := <-e.llegado:
			if !abierto {
				return nil, false, false
			}
			if pred(msg) {
				return msg, true, true
			}
			e.apartados = append(e.apartados, msg)
		case <-time.After(restante):
			return nil, true, false
		}
	}
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
	var unaVez sync.Once
	cerrar := func() {
		unaVez.Do(func() {
			cancel()
			if err := conn.Close(); err != nil {
				t.Logf("[%s] conn.Close: %v", nombre, err)
			}
		})
	}
	t.Cleanup(cerrar)

	stream, err := cloudlinkv1.NewCloudLinkClient(conn).Connect(ctx)
	if err != nil {
		t.Fatalf("[%s] Connect: %v", nombre, err)
	}

	e := &edgeVivo{
		nombre:  nombre,
		stream:  stream,
		llegado: make(chan *cloudlinkv1.CloudToEdge, 16),
		cierra:  cerrar,
	}
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

// pideRefresh envía un UserRefresh con el refresh token que este Edge tendría guardado.
func (e *edgeVivo) pideRefresh(t *testing.T, cmdID, refreshToken string) {
	t.Helper()
	frame := &cloudlinkv1.EdgeToCloud{
		CommandId: cmdID,
		SessionId: cltransport.ControlSessionID,
		Payload: &cloudlinkv1.EdgeToCloud_UserRefresh{UserRefresh: &cloudlinkv1.UserRefreshRequest{
			CommandId: cmdID, SessionId: cltransport.ControlSessionID, RefreshToken: refreshToken,
		}},
	}
	if err := e.stream.Send(frame); err != nil {
		t.Fatalf("[%s] Send refresh %s: %v", e.nombre, cmdID, err)
	}
}

// esperaRespuesta exige que llegue un UserAuthResponse con ESE command_id.
//
// ⚠️ IGNORA los frames que no son de auth, y no es laxitud: desde la Ola 2 el
// ConfigUpdate inicial del canal de control sale IN-BAND e INLINE (onControlChannel,
// en la goroutine del bucle Recv), mientras que la respuesta de auth se resuelve en el
// CARRIL. O sea que la config llega ANTES que la respuesta al login que la provocó. En
// producción da igual —el Edge correlaciona la auth por command_id y atiende el
// ConfigUpdate antes de resolver ninguna sesión—, pero un helper que exigiera que el
// PRIMER frame fuese la respuesta estaría midiendo un orden que nadie promete.
//
// Lo que NO se ignora es un UserAuthResponse con otro command_id: eso es exactamente
// el defecto que estos tests cazan, y tiene que ser rojo.
func (e *edgeVivo) esperaRespuesta(t *testing.T, cmdID string, d time.Duration) *cloudlinkv1.UserAuthResponse {
	t.Helper()
	msg, abierto, hallado := e.busca(d, func(m *cloudlinkv1.CloudToEdge) bool {
		return m.GetUserAuthResponse() != nil
	})
	if !abierto {
		t.Fatalf("[%s] el stream se cerró esperando la respuesta a %s", e.nombre, cmdID)
		return nil
	}
	if !hallado {
		t.Fatalf("[%s] NO recibió su respuesta a %s en %s. Es el defecto del 2026-09-03: la "+
			"respuesta salió por el cable de otro Edge porque `__wapp_control__` es una clave "+
			"compartida en un registro plano de última-gana", e.nombre, cmdID, d)
		return nil
	}
	resp := msg.GetUserAuthResponse()
	if resp.GetCommandId() != cmdID {
		t.Fatalf("[%s] recibió la respuesta de OTRA petición: command_id %q, esperaba %q",
			e.nombre, resp.GetCommandId(), cmdID)
		return nil
	}
	return resp
}

// exigeSilencio exige que a este Edge NO le llegue nada. Es el aserto de la fuga: no
// basta con que el Edge correcto reciba lo suyo, hace falta que el ajeno no reciba
// nada — el Edge de producción DESCARTA por command_id lo que no pidió, así que un
// token filtrado no deja rastro en el receptor.
func (e *edgeVivo) exigeSilencio(t *testing.T, d time.Duration) {
	t.Helper()
	msg, _, hallado := e.busca(d, func(*cloudlinkv1.CloudToEdge) bool { return true })
	if !hallado {
		return
	}
	if resp := msg.GetUserAuthResponse(); resp != nil {
		t.Fatalf("[%s] recibió un UserAuthResponse QUE NO PIDIÓ (command_id %q): es una fuga "+
			"de credenciales hacia el cable equivocado", e.nombre, resp.GetCommandId())
	}
	t.Fatalf("[%s] recibió un CloudToEdge que no le corresponde: %+v", e.nombre, msg)
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

// --- T2.5 (Ola 2) ---

// TestMultiEdge_DesconexionCruzadaNoAfectaCanalDeControl: apagar un Edge NO deja al
// otro sin poder autenticar.
//
// 🔴 EL EFECTO COLATERAL QUE CONGELA, que es la segunda mitad del incidente del
// 2026-09-03 y la que obligaba a REINICIAR el Edge del VPS. `Register` es última-gana y
// el `release()` que devuelve compara identidad: cuando la Mac se desconectaba, ELLA era
// la registrada bajo `__wapp_control__`, así que su release borraba la clave — y el VPS,
// perfectamente conectado, se quedaba sin canal de control. Todo login o refresh
// posterior moría con ErrSessionOffline hasta reiniciar el proceso.
//
// Tras la Ola 2 ese id no se registra en ningún sitio, así que no hay nada que borrar.
//
// 🔬 MUTACIÓN: devolver el `case` del control de Connect al registro perezoso (T2.1)
// CON el fallback a registry.Push en pushAuthResponse (T1.3) ⇒ rojo.
func TestMultiEdge_DesconexionCruzadaNoAfectaCanalDeControl(t *testing.T) {
	t.Parallel()
	h := newMultiEdgeHarness(t)

	vps := h.conecta(t, "vps", tenantA, "vmi3488280")
	vps.pideLogin(t, "cmd-vps-1")
	tokens := vps.esperaRespuesta(t, "cmd-vps-1", 5*time.Second).GetTokens()

	mac := h.conecta(t, "mac", tenantA, "MacBook-Pro-de-Jhoan.local")
	mac.pideLogin(t, "cmd-mac-1")
	mac.esperaRespuesta(t, "cmd-mac-1", 5*time.Second)

	// La Mac se va. Con el código anterior, esto se llevaba por delante el canal de
	// control del VPS.
	mac.cierra()

	vps.pideRefresh(t, "cmd-vps-refresh", tokens.GetRefreshToken())
	resp := vps.esperaRespuesta(t, "cmd-vps-refresh", 5*time.Second)
	if resp.GetError() != nil {
		t.Fatalf("el refresh del VPS falló tras apagarse la Mac: code=%q. Es el orphan release: "+
			"el release() del stream de la Mac borraba la clave de control COMPARTIDA",
			resp.GetError().GetCode())
	}
	if got := resp.GetTokens().GetAccessToken(); got != "access-"+tenantA {
		t.Fatalf("el VPS recibió el token %q, esperaba el de su tenant", got)
	}
}
