package bootstrap

import (
	"net/http"
	"time"

	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"
	"golang.org/x/time/rate"

	iamhttp "github.com/EduGoGroup/wapp-cloud-platform/internal/iam/transport/http"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/config"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/metrics"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
)

const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 10 * time.Second
	writeTimeout      = 10 * time.Second
	idleTimeout       = 60 * time.Second
	shutdownTimeout   = 10 * time.Second
)

func buildPublicAPIServer(cfg config.AppConfig, log sharedlogger.Logger, mtx *metrics.Metrics, as *authStack, pub publicapi.Deps) (*http.Server, *httpapi.Middleware, httpapi.AuditRecorder, error) {
	// El material de auth (emisor/validador ES256, AuthService, M2M, middleware,
	// auditor) se construye UNA vez en buildAuthStack y se COMPARTE con el gateway
	// CloudLink (Plan 033 · T2.2, ADR-0025): el mismo AuthService atiende tanto el
	// :8103 como las RPCs UserLogin/Refresh/Logout del Edge.
	authMW := as.authMW
	auditor := as.auditor
	// El mismo AuditService sirve la consulta GET /api/v1/audit (Plan 018 · T10):
	// lee la bitácora del tenant del token (audit.read). CERO PII (eventos opacos).
	pub.Audit = auditor

	publicMux := http.NewServeMux()
	iamhttp.Register(publicMux, as.authSvc, as.m2mSvc, as.exchanger(), log)
	// Ruta protegida de referencia: ejercita el middleware de extremo a extremo y
	// documenta el contrato de identidad para T4/T5 (tenant/subject del token).
	publicMux.Handle("/api/v1/auth/whoami", authMW.Authenticate(httpapi.WhoAmIHandler()))

	// Operación pública (Plan 018 · T5): mensajes + flujos CRUD/arranque, cada ruta
	// autenticada por api-key/scope (mismo authMW) y las escrituras auditadas (mismo
	// auditor). El tenant SIEMPRE sale del token (INV-8). T10 añade GET /api/v1/audit.
	publicapi.Register(publicMux, pub, authMW, auditor, log)

	// Blindaje transversal de la API pública (Plan 018 · T10, R11): rate-limit por
	// credencial (api-key/tenant) y por IP en el login (anti fuerza bruta) +
	// métricas de request/latencia. Envuelven el mux ENTERO. Orden de ejecución:
	// métricas (siempre cuenta, incluso un 429) → rate-limit → mux. NO tocan
	// /healthz/metrics (viven en el listener admin).
	publicLim := httpapi.NewLimiter(rate.Limit(cfg.RateLimit.PublicRPS), cfg.RateLimit.PublicBurst)
	loginLim := httpapi.NewLimiter(rate.Limit(float64(cfg.RateLimit.LoginPerMin)/60.0), cfg.RateLimit.LoginBurst)
	var handler http.Handler = publicMux
	handler = httpapi.PublicRateLimit(handler, publicLim, loginLim, mtx, log)
	handler = mtx.InstrumentHTTP("public", handler)

	srv := &http.Server{
		Addr:              cfg.PublicHTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
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
