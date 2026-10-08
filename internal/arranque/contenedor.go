// Copia de internal/bootstrap/arranque/contenedor.go @ 80807ba (F0 · 05 §6): cablea paquetes VIEJOS,
// salvo acceso (F2, T2.31, conmutar(acceso)), edge (F3, T3.28, conmutar(edge)) e inferencia (F4,
// T4.24, conmutar(inferencia)), que son internal/modulos/{acceso,edge,inferencia}: un solo gateway,
// el nuevo, que recibe acceso sin adaptador, y un solo selector de vía, el nuevo. Y solicitudes (F6,
// T6.24, conmutar(solicitudes)), que es internal/modulos/solicitudes salvo intakeStoreViejo.
package arranque

import (
	"crypto/tls"
	"database/sql"
	"net"
	"net/http"

	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"
	"google.golang.org/grpc"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	flowadmin "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/admin"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	flowruntime "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	flowstore "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake/pipeline"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakeahead"
	intakesviejo "github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intentcfg"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/platformadmin"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/diagnostics"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/enroll"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/filtercfg"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/fleet"
	edgegrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/grpc"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/inferstats"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/lease"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/receipts"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/degradation"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/llmvia"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/tenantllm"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes/quotetext"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/integrations"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/tenantvars"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/config"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/metrics"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/ratelimit"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/reanalisis"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/turnoacotado"
)

// contenedor es el estado compartido del arranque: todo lo que una fase construye
// y una fase posterior consume.
//
// # POR QUÉ UN STRUCT PRIVADO Y NO NUEVE PAQUETES
//
// Porque los campos son PRIVADOS y las nueve fases viven en este mismo paquete, así
// que la fase 7 lee `c.flowDeps.cipher` sin que `cipher` tenga que exportarse. Un
// subpaquete por fase obligaría a publicar ~40 campos que hoy no son contrato de
// nadie —y publicar un campo es prometer que seguirá ahí—. La frontera que este
// diseño sí levanta es la de fuera: el paquete `bootstrap` que envuelve a éste
// expone DOS símbolos, y son los mismos dos de siempre.
//
// # NADIE LEE UN CAMPO QUE SU FASE NO HAYA DECLARADO
//
// El orden de construcción es load-bearing y hasta hoy solo estaba escrito en los
// comentarios («adelantarlo es seguro: solo depende de ctx/cfg/db»). Ahora además se
// comprueba: cada fase declara en `requiere()` lo que necesita ya construido, y
// `marca()`/`listo()` llevan la cuenta. Reordenar la lista de fases deja de ser un
// nil-pointer a los diez minutos de vuelo y pasa a ser un error de arranque con el
// nombre de la fase que se adelantó.
type contenedor struct {
	// ─── Prólogo: existen antes de la primera fase porque el resto se construye
	// con ellos (y porque un fallo de config no tiene dónde loguearse todavía).
	cfg config.AppConfig
	log sharedlogger.Logger

	// ─── Fase 1 · infraestructura ───────────────────────────────────────────
	mtx          *metrics.Metrics
	db           *sql.DB
	ca           *enroll.CA
	serverCert   tls.Certificate
	leaseMgr     *lease.Manager
	enrollSrv    *enroll.Server
	cloudEncPriv []byte

	// ─── Fase 2 · autenticación ─────────────────────────────────────────────
	authStk *authStack
	jwksCfg edgegrpc.ConfigPayload

	// ─── Fase 3 · almacenes ─────────────────────────────────────────────────
	flowDeps          flowRuntimeDeps
	receiptSink       *receipts.Sink
	entResolver       *entitlements.Postgres
	intentStore       *intentcfg.PostgresStore
	diagStore         *diagnostics.Postgres
	fleetRepo         *fleet.PostgresRepository
	flowStore         *flowstore.PostgresRepository
	flowResolver      *flowruntime.PostgresTenantResolver
	triggerStore      *trigger.PostgresStore
	intakeStore       *intakes.Postgres
	buyerDataStore    *intakes.PostgresBuyerData
	integrationsStore *integrations.Postgres
	// intakeStoreViejo es la SEGUNDA instancia, VIEJA, del almacén de solicitudes (D-F6-1,
	// mantenida por Jhoan el 2026-10-07): internal/intakes sobre el mismo pool y el mismo cipher.
	// No tiene estado propio. Existe porque cuatro puertos de paquetes viejos nombran tipos del
	// intakes viejo en su firma y el almacén nuevo no los satisface: el proyector del carrito
	// (cart.NewProjector, args 2 y 3), la etapa draft (stages.NewDraft), las zonas de envío del
	// pipeline (pipeline.ConZonasDeEnvio) y el re-análisis (reanalisis.NewServicio). Muere en F7
	// (los tres de captación) y F8 (el carrito). Nadie más lo lee: todo lo demás usa intakeStore.
	intakeStoreViejo *intakesviejo.Postgres
	// tenantVars es el único almacén de variables del tenant: lo leen G11–G12 de la cara nueva
	// (fase 8) y el worker del puente CRM (fase 9), que antes construían uno cada una.
	tenantVars          *tenantvars.Postgres
	tenantLLMStore      *tenantllm.Postgres
	degradationStore    *degradation.Postgres
	degradationNotifier *degradation.Notifier
	intakeJobStore      *intake.Postgres
	eventStore          *events.Store

	// ─── Fase 4 · gateway ───────────────────────────────────────────────────
	inferStats *inferstats.Store
	gw         *edgegrpc.Server

	// ─── Fase 5 · captación (el stack LLM P2→P5) ────────────────────────────
	intakeComposer   *flowruntime.SourceTextComposer
	llmSelector      *llmvia.Selector
	intakePipeline   *pipeline.Worker
	consultaResolver *turnoacotado.Resolver
	reanalysisSvc    *reanalisis.Servicio
	quoteSvc         *quotetext.Service

	// ─── Fase 6 · solicitudes ───────────────────────────────────────────────
	webhookGate     *integrations.EntitlementsGate
	intakeNotifier  *intakes.Notifier
	depositReminder *intakes.DepositReminder
	expiryReminder  *intakes.ExpiryReminder
	// 🔴 CAMPO DIFERIDO. La etapa `draft` de la fase 5 recibe una clausura que lee
	// ESTE campo, y la fase 5 corre ANTES que la 6: cuando la clausura se construye,
	// el valor es nil. Se resuelve AL LLAMAR y no al construir, que es lo que rompe
	// el ciclo (la etapa necesita el Service; el Service necesita el notificador, que
	// necesita el gateway). Cuando el pipeline llegue a empujar algo, el proceso lleva
	// rato arrancado; y aunque siguiera nil, PushRevisionByID es nil-safe y calla.
	intakeService *intakes.Service

	// ─── Fase 7 · flujos ────────────────────────────────────────────────────
	flowReg            *modules.Registry
	flowEngine         *engine.Engine
	durableFlowChecker *flowadmin.EngineDurableFlowChecker
	replyLimiter       *ratelimit.Limiter
	intakeAhead        *intakeahead.Pool
	// 🔴 EL OTRO CAMPO DIFERIDO, y el mismo patrón por el mismo motivo: el agregador
	// PIDE por el pool (intakeAhead) y el pool RESPONDE al agregador, así que se
	// necesitan mutuamente. La clausura SinkFunc que recibe el pool lee este campo al
	// llamar. La alternativa era un setter público sobre el agregador, o sea dejar el
	// cable mutable en caliente para arreglar un problema que solo existe durante el
	// arranque.
	intakeAggregator *flowruntime.IntakeAggregator
	dispatcher       *events.Dispatcher
	flowRuntime      *flowruntime.Runtime

	// ─── Fase 8 · transporte ────────────────────────────────────────────────
	platformRepo  *platformadmin.Repository
	filtersPusher *filtercfg.Pusher
	authMW        *httpapi.Middleware
	auditor       httpapi.AuditRecorder
	httpSrv       *http.Server
	publicSrv     *http.Server
	enrollGS      *grpc.Server
	connectGS     *grpc.Server
	enrollLis     net.Listener
	connectLis    net.Listener

	// publicCompuesto es el estrangulador que sirve publicSrv (cara nueva delante del
	// publicapi viejo). F0 · desviación de la copia (TX.4): se guarda para que el
	// candado de mudanzas resuelva las 73 rutas del :8103 por Compuesto.Resolver.
	publicCompuesto *apipublica.Compuesto
	// publicCara es la cara nueva que va dentro de publicCompuesto. F2 · conmutar(acceso):
	// se guarda para que el candado de mudanzas compare sus Patrones() con el mapa sobre
	// el arranque REAL, no sobre una cara rearmada en el test.
	publicCara *apipublica.Cara

	// hitos son las precondiciones ya cumplidas. No es un mapa de «objetos
	// construidos» —eso son los campos de arriba— sino de ETAPAS alcanzadas: lo que
	// una fase promete dejar listo para las siguientes.
	hitos map[string]bool
}

func nuevoContenedor(cfg config.AppConfig, log sharedlogger.Logger) *contenedor {
	return &contenedor{cfg: cfg, log: log, hitos: map[string]bool{}}
}

// marca deja constancia de que un hito quedó cumplido. Lo llama cada fase al final,
// con lo que promete en su documentación.
func (c *contenedor) marca(hitos ...string) {
	for _, h := range hitos {
		c.hitos[h] = true
	}
}

// falta devuelve el primer hito exigido que todavía no se ha cumplido, o "" si están
// todos. Es lo que convierte el orden de `fases` en un contrato comprobado.
func (c *contenedor) falta(exigidos []string) string {
	for _, h := range exigidos {
		if !c.hitos[h] {
			return h
		}
	}
	return ""
}

// cerrar libera lo que el arranque abrió y que sobrevive a un fallo de fase. Hoy es
// solo el pool de PostgreSQL: los listeners los cierra el apagado gracioso y el resto
// es memoria.
//
// Va con guarda de nil a propósito: si la fase 1 muere ANTES de abrir la base, este
// defer corre igual y no debe llevarse por delante el error de verdad con un pánico.
func (c *contenedor) cerrar() {
	if c.db != nil {
		closeDB(c.db, c.log)
	}
}
