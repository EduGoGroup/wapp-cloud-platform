// Copia de internal/bootstrap/arranque/fase8_transporte.go @ 80807ba (F0 · 05 §6): cablea paquetes VIEJOS,
// salvo acceso, que desde F2 (T2.31, conmutar(acceso)) es internal/modulos/acceso (el
// gateway viejo lo recibe detrás de bridge_iam.go).
package arranque

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/EduGoGroup/wapp-cloudlink/mtls"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/filtercfg"
	flowadmin "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/admin"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/enroll"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/platformadmin"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/tenantvars"
)

// faseTransporte levanta los CUATRO listeners del proceso y les monta sus rutas:
//
//	:8102  gRPC Enrollment  — TLS de servidor SOLAMENTE (el Edge enrola sin cert)
//	:8101  gRPC CloudLink   — mTLS estricto contra la MISMA CA
//	:8103  HTTP API pública — /api/v1/* para consolas y BFF
//	:8100  HTTP admin       — /admin/*, /healthz, /metrics
//
// Aquí NO se sirve todavía: se abren los sockets y se arman los servidores. Servir es
// lo último que hace el arranque, después de la fase de fondo, y lo hace servir.go.
//
// Los dos `net.Listen` son los últimos errores de arranque que puede ver un operador,
// y los más fáciles de leer: «address already in use» con el puerto delante.
type faseTransporte struct{}

func (faseTransporte) nombre() string { return "transporte" }

func (faseTransporte) requiere() []string {
	return []string{"gateway", "flujos", "auth", "pki", "almacenes"}
}

func (faseTransporte) ejecutar(_ context.Context, c *contenedor) error {
	// --- Servidor Enrollment: TLS de servidor SOLAMENTE (sin cert de cliente). ---
	c.enrollGS = grpc.NewServer(grpc.Creds(EnrollServerCreds(c.serverCert)))
	c.enrollSrv.Register(c.enrollGS)
	enrollLis, err := net.Listen("tcp", c.cfg.GRPCEnrollAddr)
	if err != nil {
		return fmt.Errorf("escuchando enrollment en %s: %w", c.cfg.GRPCEnrollAddr, err)
	}
	c.enrollLis = enrollLis

	// --- Servidor CloudLink: mTLS estricto contra la MISMA CA. ---
	c.connectGS = grpc.NewServer(
		grpc.Creds(mtls.ServerCreds(c.serverCert, c.ca.Pool())),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    30 * time.Second,
			Timeout: 10 * time.Second,
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             15 * time.Second,
			PermitWithoutStream: true,
		}),
	)
	c.gw.Register(c.connectGS)
	connectLis, err := net.Listen("tcp", c.cfg.GRPCConnectAddr)
	if err != nil {
		return fmt.Errorf("escuchando cloudlink en %s: %w", c.cfg.GRPCConnectAddr, err)
	}
	c.connectLis = connectLis

	// entResolver viaja a la bandeja del operador porque APROBAR una solicitud es
	// dar de alta, y el alta pregunta por multi_empresa (Plan 047 · Ola 5 · T5.2).
	c.platformRepo = platformadmin.NewRepository(c.db, c.entResolver)
	// Hook del push de filtros EN CALIENTE (Plan 046 · T2.1): traduce «cambió el
	// perfil de esta sesión» a un ConfigUpdate kind:"filters" con la foto COMPLETA del
	// tenant, hacia sus sesiones vivas.
	//
	// 🔴 Se cablea en LOS DOS SITIOS que montan la ruta /sessions/{id}/profile —el
	// SessionDeps de la API pública, en depsDeLaAPIPublica, y el adminRouteDeps de más
	// abajo—. Encender solo uno deja la otra vía MUDA y no da ningún rojo: los tests de
	// cada vía pasan por separado. Si algún día se apaga, se apaga en los dos.
	c.filtersPusher = filtercfg.NewPusher(c.fleetRepo, c.gw)

	publicSrv, cara, compuesto, authMW, auditor, err := buildPublicAPIServer(c.cfg, c.db, c.log, c.mtx, c.authStk,
		depsDeLaAPIPublica(c), c.platformRepo)
	if err != nil {
		return err
	}
	c.publicSrv, c.publicCara, c.publicCompuesto, c.authMW, c.auditor = publicSrv, cara, compuesto, authMW, auditor

	c.httpSrv = servidorAdmin(c)

	c.marca("transporte")
	return nil
}

// servidorAdmin arma el listener :8100: health, /metrics y todo /admin/*.
func servidorAdmin(c *contenedor) *http.Server {
	checker := httpapi.NewHealthChecker()
	checker.Register(postgres.NewHealthCheck(c.db))
	codeStore := enroll.NewPostgresCodeStore(c.db)
	mux := http.NewServeMux()
	// registerAdminRoutes es la ÚNICA función que cablea patrón→permiso→handler
	// contra el mux admin (Plan 056 · A-02): TestMuxRegistration_NoPanic llama a
	// esta MISMA función con deps de prueba, así que un conflicto de patrones que
	// hoy provoca panic en producción también lo provoca en el test — no una
	// copia a mano que solo se prueba a sí misma. Ver el docstring del tipo.
	registerAdminRoutes(mux, adminRouteDeps{
		authMW:  c.authMW,
		auditor: c.auditor,
		log:     c.log,

		health:  httpapi.HealthHandler(checker),
		metrics: c.mtx.PromHandler(),
		// Kill-switch COMERCIAL por tenant (Plan 055 · T3.3, D-055.2): mismo
		// patrón adminHandler (auth + permiso + auditoría). Aquí el objetivo es
		// un tenant AJENO (ADR-0039), así que el permiso lleva el sufijo '.any'.
		revokeLease: httpapi.RevokeLeaseHandler(c.gw),
		sendMessage: httpapi.SendMessageHandler(c.gw, c.log),
		cryptoRekey: httpapi.CryptoRekeyHandler(
			func(ctx context.Context, batch int) (crypto.Report, error) {
				return crypto.Rekey(ctx, c.db, c.flowDeps.cipher, c.flowDeps.kp, batch)
			},
		),
		flowsCreate:    flowadmin.DefinitionHandler(c.flowStore, c.flowReg),
		flowsStart:     flowadmin.StartHandler(c.flowRuntime),
		triggersCreate: flowadmin.CreateTriggerHandler(c.triggerStore, c.durableFlowChecker),
		triggersList:   flowadmin.ListTriggersHandler(c.triggerStore, c.durableFlowChecker),
		triggersDelete: flowadmin.DeleteTriggerHandler(c.triggerStore, c.durableFlowChecker),
		// 🔴 SITIO 2 DE 2 del hook de filtros (Plan 046 · T2.1). El tercer argumento
		// era nil desde T1.2 y aquí se enciende, EN PAREJA con el ProfilePush del
		// SessionDeps de depsDeLaAPIPublica: las dos vías llevan al MISMO handler y
		// encender solo una deja la otra muda sin dar ningún rojo (los tests de cada
		// vía pasan por separado). Si un día se apaga, se apagan las dos.
		sessionProfile: flowadmin.SetSessionProfileHandler(c.fleetRepo, c.filtersPusher, c.log),
		sessionStatus:  flowadmin.SetSessionStatusHandler(c.fleetRepo),
		revokeTenant:   httpapi.RevokeTenantHandler(c.gw, c.cfg.PlatformTenantID),
		restoreTenant:  httpapi.RestoreTenantHandler(c.gw, c.cfg.PlatformTenantID),

		// Rutas de plataforma (Plan 056 · T1.3, T3.4): platformRepo/codeStore/
		// m2mClient/platformTenantID viajan CRUDOS (no como http.Handler ya
		// armado) porque registerAdminRoutes construye los platformadmin.*Handler
		// INLINE — es justo el texto que INV-056.1 (platform_permissions_test.go)
		// busca para reconocer una ruta de plataforma. Ver el docstring del tipo.
		platformRepo:     c.platformRepo,
		platformTenantID: c.cfg.PlatformTenantID,
		codeStore:        codeStore,
		m2mClient:        c.authStk.m2mClient,
	})

	return &http.Server{
		Addr:              c.cfg.HTTPAddr,
		Handler:           c.mtx.InstrumentHTTP("admin", mux),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}
}

// depsDeLaAPIPublica reúne lo que el listener :8103 sirve. Todo lo de aquí YA EXISTE
// en el contenedor: esta función no construye dominio, solo lo enchufa — con la única
// excepción de los dos adaptadores de lectura que solo esta API usa.
//
// 🔴 UNA AUSENCIA AQUÍ NO DA ERROR: la ruta simplemente no se monta y responde 404 de
// ruta inexistente, que desde fuera es indistinguible del 404 al recurso ajeno. Es el
// modo de fallo que vigilan los tests de cableado de este paquete.
func depsDeLaAPIPublica(c *contenedor) publicapi.Deps {
	return publicapi.Deps{
		Sender: c.gw,
		FlowDeps: publicapi.FlowDeps{
			Flows:   c.flowStore,
			Modules: c.flowReg,
			Starter: c.flowRuntime,
		},
		SessionDeps: publicapi.SessionDeps{
			Sessions:      c.fleetRepo,
			SessionStatus: c.fleetRepo,
			// Plan 046 · T1.2: el mismo repo por el eje NUEVO (SetProfile).
			SessionProfiles: c.fleetRepo,
			// 🔴 SITIO 1 DE 2 del hook de filtros (Plan 046 · T2.1). El otro es el
			// `sessionProfile` de adminRouteDeps: son DOS vías distintas hacia el MISMO
			// handler y encender solo una deja la otra muda sin dar ningún rojo.
			// Best-effort: un fallo del push NO cambia el 200 del POST.
			ProfilePush: c.filtersPusher,
		},
		MediaDeps: publicapi.MediaDeps{
			Media:           c.flowDeps.presign,
			Content:         c.flowStore,
			ContentMaxBytes: c.cfg.TenantContent.MaxBytes,
			ContentVersions: c.flowStore,
			ImportMaxItems:  c.cfg.Import.MaxItems,
		},
		DiagDeps: publicapi.DiagDeps{
			Diagnostics:          c.diagStore,
			DiagnosticsRequester: c.gw,
			DiagnosticsBundleTTL: c.cfg.Diagnostics.BundleTTL,
		},
		Triggers:            c.triggerStore,
		TriggersDurableFlow: c.durableFlowChecker,
		Intents:             c.intentStore,
		Entitlements:        c.entResolver,
		// El notificador (D-041.14 · T4.2) usa las MISMAS tres piezas que ya usa el
		// motor para hablarle a un contacto: el Gateway como sender, el resolver
		// custodiado de PII para el destino y el store de solicitudes para la config
		// del tenant. No hay un segundo camino de salida hacia WhatsApp.
		//
		// El recordatorio de la seña (D-041.12 · T4.4) entra por su propia opción
		// porque cuelga de otro sitio: no de la transición, sino de las LECTURAS del
		// dueño (listado y detalle), que es lo que en esta plataforma hace de reloj.
		Intakes: c.intakeService,
		// El re-análisis va APARTE de Intakes y no como método suyo: cruza cinco
		// fronteras que la bandeja no cruza (T4.6, ver internal/reanalisis).
		Reanalysis:       c.reanalysisSvc,
		QuoteSuggestions: c.quoteSvc,
		// La bandeja de EVENTOS conversacionales (Plan 043 · T3.9b) lee del MISMO
		// store que el motor y el despachador: es la misma consulta de rescatables
		// leída desde el lado del dueño, y una segunda instancia sería un segundo
		// reloj opinando sobre qué está vencido.
		ConversationEvents: c.eventStore,
		// La cancelación de esa bandeja (Plan 043 · T4.2/T4.3) la sirve el MISMO
		// runtime del motor —no el store— porque cancelar orquesta tres efectos:
		// guard del evento, puntero del flow_state y solicitud colgante. El runtime
		// ya viene armado con WithEventStore y WithIntakeAbandoner; sin este cable la
		// ruta POST …/{id}/cancel no se monta.
		EventCanceller:  c.flowRuntime,
		TenantVariables: tenantvars.NewPostgres(c.db),
		// La VUELTA del puente CRM (Plan 042 · T4.2/T4.3/T4.4). Las cuatro piezas ya
		// existen y se reutilizan tal cual: el MISMO store que guarda el secreto
		// de la ida, el MISMO gate que decide si se encola, el store de solicitudes y el
		// notificador del Plan 041. Nada de esto es nuevo salvo el cable.
		// La CONFIGURACIÓN del puente (Plan 042 · T5.1): el MISMO store, otra
		// pregunta. El CRUD lee y escribe tenant_integrations; el secreto solo
		// entra (write-only) y sale como huella.
		// Y la CONFIGURACIÓN de la vía LLM API (Plan 044 · T0.3): entra por el
		// puerto RECORTADO publicapi.TenantLLMStore, que NO tiene el método APIKey
		// — la capa HTTP no puede pedir la credencial ni por error.
		Integrations: c.integrationsStore,
		TenantLLM:    c.tenantLLMStore,
		// Y la LECTURA de los avisos de degradación (Plan 044 · T1.5-4, REQ-38),
		// por un puerto de solo lectura: la capa HTTP no puede escribir un aviso ni
		// por error. En esta ola la tabla está vacía y una lista `[]` es la
		// respuesta sana — significa que el LLM no se ha degradado.
		DegradationNotices: c.degradationStore,
		// La VUELTA del puente CRM y lo que cuelga de ella (Plan 042 · T4.2/T4.3/
		// T4.4) más los umbrales de salud: piezas ya armadas, aquí solo el cable.
		CRMSecrets: c.integrationsStore,
		CRMGate:    c.webhookGate,
		CRMReflect: c.intakeStore,
		CRMNotify:  c.intakeNotifier,
		ConfigPush: c.gw,
		Health:     publicapi.HealthRules{DegradedAfter: c.cfg.Health.DegradedAfter, StaleAfter: c.cfg.Health.StaleAfter},
		// El plazo de las consultas a BD de estos handlers (Plan 050 · Ola 3): un
		// solo valor de config para todos, porque lo que hay que respetar es la SUMA
		// con el reloj del Ack, no cada consulta por separado.
		DBTimeout: c.cfg.PublicAPIDBTimeout,
		// Telemetría de ciclo de vida del evento conversacional (Plan 043 ·
		// T6.5, cierra MD-043.17): SQL directo sobre el MISMO *sql.DB que ya
		// comparte toda la plataforma — no una segunda conexión ni un segundo
		// pool. Ver el comentario de propiedad en
		// internal/publicapi/eventstelemetry_store.go.
		EventTelemetry: publicapi.NewPostgresEventTelemetryStore(c.db),
	}
}
