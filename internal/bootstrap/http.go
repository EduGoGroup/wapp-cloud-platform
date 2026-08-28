package bootstrap

import (
	"database/sql"
	"net/http"
	"time"

	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"
	"golang.org/x/time/rate"

	iamhttp "github.com/EduGoGroup/wapp-cloud-platform/internal/iam/transport/http"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/config"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/metrics"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/ratelimit"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platformadmin"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
)

const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 10 * time.Second
	writeTimeout      = 10 * time.Second
	idleTimeout       = 60 * time.Second
	shutdownTimeout   = 10 * time.Second
)

func buildPublicAPIServer(cfg config.AppConfig, db *sql.DB, log sharedlogger.Logger, mtx *metrics.Metrics, as *authStack, pub publicapi.Deps, platformRepo *platformadmin.Repository) (*http.Server, *httpapi.Middleware, httpapi.AuditRecorder, error) {
	// El material de auth (emisor/validador ES256, middleware, auditor) se
	// construye UNA vez en buildAuthStack y se COMPARTE con el gateway CloudLink
	// (Plan 033 · T2.2, ADR-0025): el mismo verificador acepta en el :8103
	// exactamente los tokens que acepta el relé del Edge.
	authMW := as.authMW
	auditor := as.auditor
	// El mismo AuditService sirve la consulta GET /api/v1/audit (Plan 018 · T10):
	// lee la bitácora del tenant del token (audit.read). CERO PII (eventos opacos).
	pub.Audit = auditor

	// PLANO DE ROLES Y MIEMBROS de la empresa (Plan 047 · Ola 1.0 · T1.0-4, plano 2
	// del ADR-0033). Se resuelve AQUÍ y no en el literal de Deps de bootstrap.go por
	// la misma razón que pub.Audit justo encima: es una dependencia que solo existe
	// para esta API y que este constructor puede armar entero.
	//
	// 🔴 Los DOS campos son necesarios y ninguna ausencia da error: con Roles nil no
	// se montan /api/v1/roles ni las rutas de rol/grant bajo /api/v1/members, y con
	// Members nil no se monta el alta/baja de membresía. Las rutas simplemente NO
	// EXISTEN y responden 404 de ruta inexistente — indistinguible desde fuera del
	// 404 que estas mismas rutas dan al recurso ajeno. Eso es lo que vigila
	// roleplane_cableado_test.go.
	//
	// El cliente M2M viaja al plano porque el alta de un miembro acredita la
	// aplicación en identity antes de escribir la fila (Plan 047 · Ola B). Su
	// ausencia NO desmonta ninguna ruta: es la diferencia entre «esta
	// administración no existe» (404) y «existe y le falta configuración» (503),
	// y confundirlas mandaría a depurar el router en vez del entorno.
	rolesPlane, err := buildRolePlane(db, as.m2mClient, log)
	if err != nil {
		return nil, nil, nil, err
	}
	if as.m2mClient == nil {
		log.Warn("POST /api/v1/members: falta WAPP_IDENTITY_API_KEY; el alta de miembros responde 503 " +
			"(sin acreditar la aplicación en identity, la persona quedaría de alta y sin poder entrar)")
	}
	pub.Roles = rolesPlane.roles
	pub.Members = rolesPlane.members

	publicMux := http.NewServeMux()
	iamhttp.Register(publicMux, as.contextTokens, as.exchanger(), log)
	// Ruta protegida de referencia: ejercita el middleware de extremo a extremo y
	// documenta el contrato de identidad para T4/T5 (tenant/subject del token).
	publicMux.Handle("/api/v1/auth/whoami", authMW.Authenticate(httpapi.WhoAmIHandler()))

	// Ruta pública de alta de usuario (Plan 056 · T3.2). A-06a: el freno era
	// 5 rps/burst 10 por IP —432 000 altas/día para un formulario que una
	// persona rellena una vez—; baja a 1 cada 60s con ráfaga de 5. C-02
	// (defensa en profundidad): sin cliente M2M (falta WAPP_IDENTITY_API_KEY)
	// esta ruta NO se cablea al handler real —que necesita el M2M para
	// operar—, sino a un 503 fijo. La guarda `m2m == nil` dentro de
	// SignupHandler es la SEGUNDA capa, por si algún día alguien lo registra
	// desde otro sitio.
	if as.m2mClient != nil {
		signupLimiter := ratelimit.NewLimiter(rate.Every(time.Minute), 5)
		publicMux.Handle("POST /api/v1/signup", platformadmin.SignupHandler(platformRepo, as.m2mClient, signupLimiter, cfg.RateLimit.TrustProxy, log))
	} else {
		log.Warn("POST /api/v1/signup: falta WAPP_IDENTITY_API_KEY; el registro público responde 503 (servicio no disponible)")
		publicMux.Handle("POST /api/v1/signup", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "registro no disponible", http.StatusServiceUnavailable)
		}))
	}

	// El PRESUPUESTO DE LA PETICIÓN de envío (Plan 050 · Ola 5 · T5.4, REQ-050.19) se
	// DERIVA del mismo writeTimeout con el que se arma el http.Server unas líneas más
	// abajo. Las dos líneas están a la vista una de otra a propósito: el defecto que
	// esto cierra nació de una aritmética escrita a mano en otro fichero que nadie
	// rehizo al añadir un reloj. Aquí no hay aritmética que mantener — mover
	// writeTimeout arrastra el presupuesto solo. Ver publicapi.SendBudgetFrom.
	pub.SendBudget = publicapi.SendBudgetFrom(writeTimeout)

	// Operación pública (Plan 018 · T5): mensajes + flujos CRUD/arranque, cada ruta
	// autenticada por Context Token + grants (mismo authMW) y las escrituras
	// auditadas (mismo auditor). El tenant SIEMPRE sale del token (INV-8). T10
	// añade GET /api/v1/audit.
	publicapi.Register(publicMux, pub, authMW, auditor, log)

	// Blindaje transversal de la API pública (Plan 018 · T10, R11): rate-limit por
	// credencial + métricas de request/latencia. Envuelven el mux ENTERO. Orden de
	// ejecución: métricas (siempre cuenta, incluso un 429) → rate-limit → mux. NO
	// tocan /healthz/metrics (viven en el listener admin). El cubo por IP del
	// login se fue con el login (identity Plan 003 · Ola 5).
	publicLim := httpapi.NewLimiter(rate.Limit(cfg.RateLimit.PublicRPS), cfg.RateLimit.PublicBurst)
	var handler http.Handler = publicMux
	handler = httpapi.PublicRateLimit(handler, publicLim, mtx, log)
	handler = mtx.InstrumentHTTP("public", handler)

	srv := &http.Server{
		Addr:              cfg.PublicHTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		// 🔴 El MISMO writeTimeout del que se derivó pub.SendBudget arriba. Si mueves
		// este valor no hay nada más que ajustar: el presupuesto lo sigue.
		WriteTimeout: writeTimeout,
		IdleTimeout:  idleTimeout,
	}
	return srv, authMW, auditor, nil
}

// adminHandler blinda un endpoint /admin/* con la cadena de la fase IAM (Plan
// 018 · T4): Authenticate (identidad del token) → RequirePermission(perm) →
// AuditMiddleware(action=perm, resource) → handler. El tenant SIEMPRE sale del
// token (INV-8, lo lee el handler con IdentityFromContext) y la operación queda
// auditada sin PII (actor/resource opacos). El nombre del permiso se reutiliza
// como `action` de la bitácora (p. ej. "flows.create").
func adminHandler(mw *httpapi.Middleware, auditor httpapi.AuditRecorder, log sharedlogger.Logger, perm, resource string, h http.Handler) http.Handler {
	h = httpapi.AuditMiddleware(auditor, perm, resource, log)(h)
	h = mw.RequirePermission(perm)(h)
	return mw.Authenticate(h)
}
