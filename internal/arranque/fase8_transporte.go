// Copia de internal/bootstrap/arranque/fase8_transporte.go @ 80807ba (F0 · 05 §6): cablea paquetes VIEJOS,
// salvo acceso (F2, T2.31, conmutar(acceso)), edge (F3, T3.28, conmutar(edge)) e inferencia (F4,
// T4.24, conmutar(inferencia)), que son internal/modulos/{acceso,edge,inferencia}: un solo gateway,
// el nuevo, que recibe acceso sin adaptador; de inferencia, aquí solo viajan sus almacenes hacia la cara nueva.
// Desde F6 (T6.25, conmutar(solicitudes)) las 18 rutas de solicitudes (G1–G18) las sirve la cara nueva
// con los objetos de internal/modulos/solicitudes que arma requestsDepsOfTheNewFace. Y desde F7 (T7.24,
// conmutar(captacion)) las 3 de captación (H1, E1, E2), con los de internal/modulos/captacion que arma
// captureDepsOfTheNewFace.
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

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	flowadmin "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/admin"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/platformadmin"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/reanalisis"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/enroll"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/filtercfg"
	edgegrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/grpc"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
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
	// SessionsDeps de la cara nueva, en edgeDepsOfTheNewFace, y el adminRouteDeps de más
	// abajo—. Encender solo uno deja la otra vía MUDA y no da ningún rojo: los tests de
	// cada vía pasan por separado. Si algún día se apaga, se apaga en los dos.
	c.filtersPusher = filtercfg.NewPusher(c.fleetRepo, c.gw)

	publicSrv, cara, compuesto, authMW, auditor, err := buildPublicAPIServer(publicAPIDeps{
		cfg:       c.cfg,
		db:        c.db,
		log:       c.log,
		mtx:       c.mtx,
		authStack: c.authStk,
		oldFace:   depsDeLaAPIPublica(c),
		// Un campo por módulo mudado; la fase que mude rutas añade aquí el suyo (newFaceDeps).
		newFace: newFaceDeps{
			edge:      edgeDepsOfTheNewFace(c),
			inference: inferenceDepsOfTheNewFace(c),
			requests:  requestsDepsOfTheNewFace(c),
			capture:   captureDepsOfTheNewFace(c),
		},
		platformRepo: c.platformRepo,
	})
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
		// SessionsDeps de edgeDepsOfTheNewFace: las dos vías llevan al MISMO handler y
		// encender solo una deja la otra muda sin dar ningún rojo (los tests de cada
		// vía pasan por separado). Si un día se apaga, se apagan las dos.
		//
		// 🔀 F3 · conmutar(edge) (D-FX-2, T-14): J16/J17 son los constructores que
		// exporta la cara nueva (apipublica/sessionadmin.go), los MISMOS que sirven
		// D3/D4 en el :8103, y se mudan en el mismo commit que ellas.
		sessionProfile: apipublica.SetSessionProfileHandler(c.fleetRepo, c.filtersPusher, c.log),
		sessionStatus:  apipublica.SetSessionStatusHandler(c.fleetRepo),
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
//
// 🔀 F3 · conmutar(edge) (FX TX.11): la cara VIEJA ya no sirve D1–D6. Sender, Sessions,
// SessionStatus, SessionProfiles, ProfilePush, Diagnostics y DiagnosticsRequester se quedan
// SIN asignar (nil) A PROPÓSITO: con nil, publicapi no registra D2–D6, que sirve la cara
// nueva con lo que arma edgeDepsOfTheNewFace; D1 la registra SIEMPRE (no tiene condición
// de montaje) pero queda tapada por la nueva en el compuesto. Con ellos se van los campos
// que solo leían esas rutas (Health, DiagnosticsBundleTTL). ConfigPush e Intents siguieron
// puestos hasta F7 (ver más abajo).
//
// 🔀 F4 · conmutar(inferencia) (FX TX.14): la cara VIEJA ya no sirve F1–F4. TenantLLM y
// DegradationNotices se quedan SIN asignar (nil) A PROPÓSITO: con nil, publicapi no registra
// ni las tres rutas de /api/v1/tenant-llm ni GET /api/v1/degradation-notices, que sirve la
// cara nueva con lo que arma inferenceDepsOfTheNewFace. No es solo orden: los almacenes del
// contenedor son ya los de internal/modulos/inferencia y no satisfacen los puertos viejos.
//
// 🔀 F6 · conmutar(solicitudes) (FX TX.18): la cara VIEJA ya no sirve G1–G18. Intakes,
// QuoteSuggestions, TenantVariables, Integrations, CRMSecrets, CRMGate, CRMReflect, CRMNotify y
// EventTelemetry se quedan SIN asignar (nil) A PROPÓSITO: con nil, publicapi no registra ni la
// cotización sugerida (G7), ni las variables (G11–G12), ni el puente CRM con su callback
// (G13–G17), ni la telemetría de eventos (G18), que sirve la cara nueva con lo que arma
// requestsDepsOfTheNewFace. Las 18 se mudan en el MISMO commit: G2 es un comodín
// (`…/intakes/{id}`) que, sola en la cara nueva, taparía los literales G9 y G10 de la vieja (son
// dos ServeMux; FX mapa §4.2). No es solo orden: el Service, los almacenes y el notificador del
// contenedor son ya los de internal/modulos/solicitudes y no satisfacen los puertos viejos.
// Entitlements y DBTimeout SIGUEN puestos: los leen las rutas que la vieja conserva.
//
// 🔀 F7 · conmutar(captacion) (FX TX.21): la cara VIEJA ya no sirve H1, E1 ni E2. Reanalysis,
// Intents y ConfigPush se quedan SIN asignar (nil) A PROPÓSITO: con nil, publicapi no registra ni
// `POST /api/v1/intakes/{id}/reanalyze` (H1) ni las dos de `/api/v1/intents` (E1–E2), que sirve la
// cara nueva con lo que arma captureDepsOfTheNewFace. ConfigPush solo lo leía E2 (medido: su
// único lector en publicapi es putIntentsHandler), así que se va con ella. No es solo orden: el
// servicio de re-análisis y el store de intenciones del contenedor son ya los de
// internal/modulos/captacion y no satisfacen los puertos viejos (Reanalizar ≠ Reanalyze; el Config
// de otro paquete).
//
// 🔴 Los tres campos se OMITEN, no se escriben con `nil`: el candado de cableado del re-análisis
// lee el AST de este paquete y da por rota cualquier clave `Reanalysis:` con valor nil, que es
// justo el modo de fallo que vigila (la ruta que no se monta y responde 404 sin un error).
//
// Y con H1 fuera, Intakes vuelve a nil DE VERDAD: murió oldFaceIntakesMountSentinel, el centinela
// sin implementación de D-F6-13 que existía solo para que la vieja siguiera registrando H1 (en
// publicapi, registerIntakes guarda toda su función tras `d.Intakes == nil`). Con él se van las
// nueve rutas de la bandeja que la vieja volvía a registrar tapadas por la nueva, y el pánico de
// puntero nil que había detrás de cada una si alguna quedaba destapada.
func depsDeLaAPIPublica(c *contenedor) publicapi.Deps {
	return publicapi.Deps{
		FlowDeps: publicapi.FlowDeps{
			Flows:   c.flowStore,
			Modules: c.flowReg,
			Starter: c.flowRuntime,
		},
		MediaDeps: publicapi.MediaDeps{
			Media:           c.flowDeps.presign,
			Content:         c.flowStore,
			ContentMaxBytes: c.cfg.TenantContent.MaxBytes,
			ContentVersions: c.flowStore,
			ImportMaxItems:  c.cfg.Import.MaxItems,
		},
		Triggers:            c.triggerStore,
		TriggersDurableFlow: c.durableFlowChecker,
		Entitlements:        c.entResolver,
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
		EventCanceller: c.flowRuntime,
		// El plazo de las consultas a BD de estos handlers (Plan 050 · Ola 3): un
		// solo valor de config para todos, porque lo que hay que respetar es la SUMA
		// con el reloj del Ack, no cada consulta por separado.
		DBTimeout: c.cfg.PublicAPIDBTimeout,
	}
}

// edgeDepsOfTheNewFace reúne lo que la cara NUEVA necesita para servir D1–D6 (F3 ·
// conmutar(edge), FX TX.11), con los MISMOS valores que hasta F3 iban a la cara vieja en
// depsDeLaAPIPublica: el gateway como Sender y DiagnosticsRequester, la flota por sus tres
// ejes, el almacén de diagnóstico y los umbrales de salud. Alerter no se cablea (como hasta
// ahora): nil ⇒ NoopAlerter. messages.SendBudget lo pone buildPublicAPIServer, que es quien
// conoce el writeTimeout del que se deriva.
//
// 🔴 Las condiciones de montaje no cambian: los tres punteros (c.gw, c.fleetRepo,
// c.diagStore) se construyen siempre en sus fases, y viajan a las interfaces tal como
// viajaban a las de la cara vieja.
func edgeDepsOfTheNewFace(c *contenedor) edgeFaceDeps {
	return edgeFaceDeps{
		messages: apipublica.MessagesDeps{
			Sender:   c.gw,
			Sessions: c.fleetRepo,
			// El plazo de las consultas a BD de estos handlers (Plan 050 · Ola 3): el
			// mismo valor de config que el resto de la API.
			DBTimeout: c.cfg.PublicAPIDBTimeout,
		},
		sessions: apipublica.SessionsDeps{
			Sessions:  c.fleetRepo,
			Health:    apipublica.HealthRules{DegradedAfter: c.cfg.Health.DegradedAfter, StaleAfter: c.cfg.Health.StaleAfter},
			DBTimeout: c.cfg.PublicAPIDBTimeout,
			// Plan 046 · T1.2: el mismo repo por el eje del perfil (SetProfile).
			SessionProfiles: c.fleetRepo,
			// 🔴 SITIO 1 DE 2 del hook de filtros (Plan 046 · T2.1). El otro es el
			// `sessionProfile` de adminRouteDeps: son DOS vías distintas hacia el MISMO
			// handler y encender solo una deja la otra muda sin dar ningún rojo.
			// Best-effort: un fallo del push NO cambia el 200 del POST.
			ProfilePush:   c.filtersPusher,
			SessionStatus: c.fleetRepo,
		},
		diagnostics: apipublica.DiagnosticsDeps{
			Diagnostics:          c.diagStore,
			DiagnosticsRequester: c.gw,
			Sessions:             c.fleetRepo,
			BundleTTL:            c.cfg.Diagnostics.BundleTTL,
			DBTimeout:            c.cfg.PublicAPIDBTimeout,
		},
	}
}

// inferenceDepsOfTheNewFace reúne lo que la cara NUEVA necesita para servir F1–F4 (F4 ·
// conmutar(inferencia), FX TX.14), con los MISMOS objetos del contenedor que usa el resto del
// arranque: el almacén de tenant_llm del que lee el selector de vía, el almacén de avisos en el
// que escribe su notificador, y el único resolver de derechos (una sola caché).
//
// La configuración de la vía LLM API (Plan 044 · T0.3) entra por el puerto RECORTADO
// apipublica.TenantLLMStore, que NO tiene el método APIKey: la capa HTTP no puede pedir la
// credencial ni por error. Y la lectura de los avisos de degradación (Plan 044 · T1.5-4,
// REQ-38) entra por un puerto de solo lectura: la capa HTTP no puede escribir un aviso.
//
// 🔴 Las condiciones de montaje no cambian respecto a la cara vieja: los tres punteros
// (c.tenantLLMStore, c.degradationStore, c.entResolver) se construyen siempre en la fase 3.
func inferenceDepsOfTheNewFace(c *contenedor) inferenceFaceDeps {
	return inferenceFaceDeps{
		tenantLLM: apipublica.TenantLLMDeps{
			TenantLLM:    c.tenantLLMStore,
			Entitlements: c.entResolver,
		},
		degradationNotices: apipublica.DegradationNoticesDeps{
			DegradationNotices: c.degradationStore,
			Entitlements:       c.entResolver,
			// El plazo de las consultas a BD de estos handlers (Plan 050 · Ola 3): el
			// mismo valor de config que el resto de la API.
			DBTimeout: c.cfg.PublicAPIDBTimeout,
		},
	}
}

// requestsDepsOfTheNewFace reúne lo que la cara NUEVA necesita para servir G1–G18 (F6 ·
// conmutar(solicitudes), FX TX.18), con los MISMOS objetos del contenedor que usa el resto del
// arranque: el único Service de solicitudes (el que también recibe el motor para abandonar), el
// generador de cotización de la fase 5, los almacenes de la fase 3, el gate y el notificador de la
// fase 6 y el único resolver de derechos (una sola caché). Aquí no se construye dominio: la única
// construcción es el adaptador de lectura de G18, que solo esta API usa.
//
// 🔴 Las condiciones de montaje no cambian respecto a la cara vieja: todos estos punteros se
// construyen siempre en sus fases, así que las 18 rutas se montan siempre. Una ausencia NO da
// error: la ruta no se monta y responde el 404 de ruta inexistente (desde F7 ya no hay centinela
// en la cara vieja, D-F6-13, detrás del que cayera una de la bandeja). Lo vigilan el candado de
// mudanzas y solicitudes_cableado_test.go. Los Now se dejan en nil (time.Now).
func requestsDepsOfTheNewFace(c *contenedor) requestsFaceDeps {
	return requestsFaceDeps{
		// G1–G6, G8 · la bandeja. El recordatorio de la seña y el del plazo (D-041.12 · T4.4,
		// D-044.50) no entran por aquí: cuelgan del Service, que los dispara en las LECTURAS del
		// dueño (listado y detalle), que es lo que en esta plataforma hace de reloj.
		intakes: apipublica.IntakesDeps{
			Intakes:      c.intakeService,
			Entitlements: c.entResolver,
		},
		// G7, G9, G10 · cotización sugerida, export y resumen: el MISMO Service por su puerto de
		// lectura en bloque, y el generador de la fase 5.
		//
		// 🔴 QuoteWriteDeadline es quoteWriteDeadline (fase5_captacion.go): sale del MISMO valor
		// que recibe quotetext.WithTimeout, más su margen. No se escribe aquí ningún número.
		intakeReports: apipublica.IntakeReportsDeps{
			Intakes:            c.intakeService,
			Entitlements:       c.entResolver,
			QuoteSuggestions:   c.quoteSvc,
			QuoteWriteDeadline: quoteWriteDeadline,
		},
		// G11–G12 · las variables del tenant: el MISMO almacén que lee el worker del puente CRM
		// (c.tenantVars, fase 3). El plazo de la lectura es el de config (Plan 050 · Ola 3).
		tenantVariables: apipublica.TenantVariablesDeps{
			TenantVariables: c.tenantVars,
			DBTimeout:       c.cfg.PublicAPIDBTimeout,
		},
		// G13–G16 · la CONFIGURACIÓN del puente CRM (Plan 042 · T5.1): el MISMO store que guarda
		// el secreto de la ida. El secreto solo entra (write-only) y sale como huella.
		integrations: apipublica.IntegrationsDeps{
			Integrations: c.integrationsStore,
			Entitlements: c.entResolver,
		},
		// G17 · la VUELTA del puente CRM (Plan 042 · T4.2/T4.3/T4.4). Las cuatro piezas ya
		// existen: el MISMO store que guarda el secreto, el MISMO gate que decide si se encola,
		// el almacén de solicitudes como reflector y el notificador de la fase 6 (una sola salida
		// hacia WhatsApp).
		crmCallback: apipublica.CRMCallbackDeps{
			CRMSecrets: c.integrationsStore,
			CRMGate:    c.webhookGate,
			CRMReflect: c.intakeStore,
			CRMNotify:  crmStatusNotifierPort(c.intakeNotifier),
		},
		// G18 · telemetría de ciclo de vida del evento conversacional (Plan 043 · T6.5, cierra
		// MD-043.17): SQL directo sobre el MISMO *sql.DB que comparte toda la plataforma, no un
		// segundo pool. El adaptador se queda en la cara (D-FX-4): ver
		// internal/apipublica/eventstelemetry_store.go.
		eventTelemetry: apipublica.EventTelemetryDeps{
			EventTelemetry: apipublica.NewPostgresEventTelemetryStore(c.db),
		},
	}
}

// crmStatusNotifierPort entrega el notificador a G17 como su puerto, o un nil DE INTERFAZ si no
// hay notificador. G17 decide «no aviso» comparando CRMNotify con nil, y un *intakes.Notifier nil
// metido en la interfaz NO es nil: el aviso se intentaría sobre un receptor nil. Hoy la fase 6
// construye siempre el notificador; la costura existe para que el día que sea opcional el cable
// no cambie de significado sin que nadie lo vea.
func crmStatusNotifierPort(n *intakes.Notifier) apipublica.CRMStatusNotifier {
	if n == nil {
		return nil
	}
	return n
}

// captureDepsOfTheNewFace reúne lo que la cara NUEVA necesita para servir H1, E1 y E2 (F7 ·
// conmutar(captacion), FX TX.21), con los MISMOS objetos del contenedor que usa el resto del
// arranque: el único servicio de re-análisis (fase 5), el store de intenciones del que también
// leen el pool de clasificaciones y el push de config al conectar (fase 3), el único resolver de
// derechos (una sola caché) y el único gateway del proceso. Aquí no se construye nada.
//
// 🔴 Las condiciones de montaje no cambian respecto a la cara vieja: H1 se monta si hay servicio
// de re-análisis, y ya NO depende de que haya bandeja (era el accidente de publicapi que D-F6-13
// tuvo que rodear con un centinela); E1–E2, si hay store de intenciones y resolver de derechos.
// Los cuatro punteros se construyen siempre en sus fases, así que las tres se montan siempre. Una
// ausencia NO da error: la ruta no se monta y responde 404. Lo vigilan el candado de mudanzas y
// reanalisis_cableado_test.go.
func captureDepsOfTheNewFace(c *contenedor) captureFaceDeps {
	return captureFaceDeps{
		// H1 · el re-análisis va APARTE de la bandeja y no como método suyo: cruza cinco
		// fronteras que la bandeja no cruza (T4.6, ver captacion/reanalisis).
		reanalyze: apipublica.ReanalyzeDeps{
			Reanalysis: reanalysisServicePort(c.reanalysisSvc),
		},
		// E1–E2 · la config de intenciones (Plan 029). El gate `llm_intent` vive dentro del
		// handler de E2, y por eso viaja el resolver. El PUT empuja el ConfigUpdate por el
		// gateway NUEVO, el único del proceso, best-effort: sin sesiones vivas no es un error.
		// El plazo de la lectura es el de config (Plan 050 · Ola 3), el mismo que recibía E1 en
		// la cara vieja.
		intents: apipublica.IntentsDeps{
			Intents:      c.intentStore,
			Entitlements: c.entResolver,
			ConfigPush:   configPusherPort(c.gw),
			DBTimeout:    c.cfg.PublicAPIDBTimeout,
		},
	}
}

// reanalysisServicePort entrega el servicio de re-análisis a H1 como su puerto, o un nil DE
// INTERFAZ si no hay servicio. MountReanalyze decide «no monto la ruta» comparando Reanalysis con
// nil, y un *reanalisis.Service nil metido en la interfaz NO es nil: la ruta se montaría y el
// primer POST reventaría sobre un receptor nil. Hoy la fase 5 construye siempre el servicio (o
// aborta el arranque); la costura existe para que el día que sea opcional el cable no cambie de
// significado sin que nadie lo vea. Mismo cuidado que crmStatusNotifierPort.
func reanalysisServicePort(svc *reanalisis.Service) apipublica.ReanalysisService {
	if svc == nil {
		return nil
	}
	return svc
}

// configPusherPort entrega el gateway a E2 como su puerto de push, o un nil DE INTERFAZ si no hay
// gateway. E2 decide «no empujo» comparando ConfigPush con nil (el push es best-effort y opcional),
// y un *edgegrpc.Server nil metido en la interfaz NO es nil: el PUT guardaría la config y luego
// reventaría al empujarla. Hoy la fase 4 construye siempre el gateway; por la misma razón que
// reanalysisServicePort.
func configPusherPort(gw *edgegrpc.Server) apipublica.ConfigPusher {
	if gw == nil {
		return nil
	}
	return gw
}
