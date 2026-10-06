// Copia de internal/bootstrap/arranque/fase4_gateway.go @ 80807ba (F0 · 05 §6). Desde F3 (T3.28,
// conmutar(edge)) construye el gateway NUEVO (internal/modulos/edge/grpc), el ÚNICO del proceso,
// que recibe los servicios de internal/modulos/acceso directamente, sin adaptador.
package arranque

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
	iamusecase "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/usecase"
	edgegrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/grpc"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/inferstats"
	edgesession "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/session"
)

// faseGateway construye el gateway gRPC que termina el túnel de cada Edge: el objeto
// del que cuelga TODA la comunicación con el equipo del cliente.
//
// 🔴 SE CONSTRUYE AQUÍ, EN MEDIO, Y NO PUEDE SUBIR NI BAJAR. Necesita casi todo lo de
// las tres fases anteriores (lease, enrolamiento, flota, cipher, auth), y casi todo lo
// de las cinco siguientes lo necesita a él: el selector de vía LLM usa su método Infer
// como frame de la vía local, el notificador de solicitudes lo usa como salida hacia
// WhatsApp, y el motor de flujos le cuelga tres hooks.
//
// Los hooks (OnIncoming, OnHeartbeat, OnWarmup, OnEdgeReady) NO se cablean aquí: se
// cablean en la fase que construye a quien responde a cada uno. Es la contrapartida de
// que el gateway nazca antes que ellos.
type faseGateway struct{}

func (faseGateway) nombre() string { return "gateway" }

func (faseGateway) requiere() []string {
	return []string{"lease", "enroll", "cipher", "almacenes", "auth", "metricas"}
}

func (faseGateway) ejecutar(_ context.Context, c *contenedor) error {
	// EL ALMACÉN DEL PARTE DE INFERENCIA (Plan 044 · Ola 1.7 · T1.7-9). Se crea aquí,
	// antes del Gateway, porque tiene DOS extremos: el Gateway lo escribe con lo que
	// llega en cada latido y /metrics lo lee en el scrape. Es memoria pura —ni base ni
	// goroutine— y por eso puede vivir todo el arranque sin condiciones.
	c.inferStats = inferstats.New()
	// El lado LECTOR, con el molde de RegisterDBStats: se registra DESPUÉS de New()
	// porque la fuente no existía al construir las métricas. Se aborta el arranque si
	// falla, por el mismo motivo que allí: el único error posible es un choque de
	// nombres en el registry, o sea un bug que se ve al arrancar o no se ve nunca.
	if err := c.mtx.RegisterInferenceStats(c.inferStats.Aggregated); err != nil {
		return err
	}

	c.gw = edgegrpc.New(
		// Deadline por Send hacia el Edge (Plan 027 · Ola 1 · T5, cierra H6): un Edge
		// lento no retiene al llamante ni atasca el kill-switch (env WAPP_GRPC_PUSH_TIMEOUT).
		edgesession.NewRegistry(edgesession.WithSendTimeout(c.cfg.GRPCPushTimeout)),
		c.log,
		// Deadline por ESPERA DEL ACK (env WAPP_GRPC_ACK_TIMEOUT): el hermano del
		// anterior. Aquel acota el empuje; este, la respuesta. Sin él, un Edge
		// saturado cuelga al llamante HTTP indefinidamente (2026-08-06).
		edgegrpc.WithAckTimeout(c.cfg.GRPCAckTimeout),
		// Carril de trabajo del stream (Plan 050 · Ola 1, ADR-0040): tope de cola POR
		// SESIÓN (env WAPP_GATEWAY_WORK_QUEUE, default 64 = el techo de entrantes
		// concurrentes) y presupuesto de pared por trabajo (env WAPP_GATEWAY_WORK_TIMEOUT,
		// default 5s). Aquí solo se materializan; quien los consume es el carril.
		edgegrpc.WithWorkQueue(c.cfg.GatewayWorkQueue),
		edgegrpc.WithWorkTimeout(c.cfg.GatewayWorkTimeout),
		edgegrpc.WithLease(c.leaseMgr),
		edgegrpc.WithFleet(c.fleetRepo),
		edgegrpc.WithCloudEncPrivKey(c.cloudEncPriv),
		edgegrpc.WithReceiptSink(c.receiptSink),
		// Push de config al conectar (ADR-0021). TRES eslabones —jwks, intents y
		// filters— armados por buildConfigProvider, que es donde vive el porqué de
		// cada regla (y, sobre todo, el único sitio desde el que un test puede
		// ejercer la cadena REAL: es el criterio (d) de T2.1).
		edgegrpc.WithConfigProvider(
			buildConfigProvider(c.jwksCfg, c.intentStore, c.entResolver, c.fleetRepo, c.log),
		),
		// Recepción del DiagnosticsBundle (Plan 031 · T5, ADR-0023): el demux correlaciona
		// el bundle con su solicitud pendiente por command_id y lo almacena.
		// El lado ESCRITOR del mismo almacén: sin este cable, el parte de inferencia
		// sigue subiendo y muriendo en la base, que es justo lo que T1.7-9 arregla.
		edgegrpc.WithInferenceStats(c.inferStats),
		edgegrpc.WithDiagnosticsSink(c.diagStore),
		// Auth de usuario del plano de control del Edge (Plan 033 · T2.2, ADR-0025): el
		// gateway delega UserLogin/Refresh/Logout en un puerto de autenticación y audita
		// edge.auth.* (CERO PII) con el mismo auditor del :8103. Detrás del puerto está
		// identity-core si la delegación de la Ola 3 está encendida (WAPP_IDENTITY_URL),
		// o el IAM local si no; el gateway no distingue los dos casos.
		//
		// 🔀 F3 · conmutar(edge): los dos servicios son los de acceso NUEVO y entran sin
		// adaptador (murió bridge_iam.go); solo pasan por la costura que conserva el nil
		// de verdad (edgeAuthenticatorPort, edgeAuditorPort).
		edgegrpc.WithAuthenticator(edgeAuthenticatorPort(c.authStk.edgeAuthSvc)),
		edgegrpc.WithAuthAuditor(edgeAuditorPort(c.authStk.auditor)),
	)

	c.marca("gateway")
	return nil
}

// edgeAuthenticatorPort entrega el autenticador delegado como el puerto que pide el gateway.
// Con svc nil devuelve un nil DE VERDAD (interfaz nil, no un puntero nil dentro de una
// interfaz), el mismo cuidado que authStack.exchanger: el gateway compara el puerto con nil
// para responder «auth no disponible», y un *iamusecase.DelegatedAuthService nil metido en la
// interfaz le haría creer que tiene autenticador (y reventaría al primer login).
func edgeAuthenticatorPort(svc *iamusecase.DelegatedAuthService) in.Authenticator {
	if svc == nil {
		return nil
	}
	return svc
}

// edgeAuditorPort entrega el auditor como el puerto que pide el gateway. Con svc nil devuelve
// un nil DE VERDAD, por la misma razón que edgeAuthenticatorPort: sin auditor, el gateway
// funciona pero no audita, y lo decide comparando con nil.
func edgeAuditorPort(svc *iamusecase.AuditService) in.Auditor {
	if svc == nil {
		return nil
	}
	return svc
}
