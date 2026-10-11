// Copia de internal/bootstrap/arranque/http.go @ 80807ba (F0 · 05 §6), con una desviación: el
// :8103 no lo sirve el publicapi viejo sino la cara única, internal/apipublica, que monta las
// 73 rutas (caraNueva, mudanzas.go). Va sola, sin nada detrás: lo que ella no registra recibe el
// 404 de ruta inexistente de su propio ServeMux (F10 · T10.9 · FX TX.25).
package arranque

import (
	"database/sql"
	"net/http"
	"time"

	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"
	"golang.org/x/time/rate"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/platformadmin"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/config"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/metrics"
)

const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 10 * time.Second
	writeTimeout      = 10 * time.Second
	idleTimeout       = 60 * time.Second
	shutdownTimeout   = 10 * time.Second
)

// publicAPIDeps es todo lo que buildPublicAPIServer recibe, con nombre. Existe porque la firma
// iba por nueve parámetros posicionales y cada fase que muda rutas a la cara nueva le añadía uno
// (edge en F3, inferencia en F4): con este struct la firma ya no crece. Y la fase tampoco añade
// un campo AQUÍ: lo añade en newFaceDeps (mudanzas.go), que es el único sitio donde se agrupan
// las dependencias de la cara nueva; este tipo solo lo transporta entero en newFace.
//
// Alternativa descartada: un campo por módulo en este struct (edge, inference, …) copiado luego a
// newFaceDeps. Serían dos structs paralelos que cada fase tendría que tocar a la vez, y olvidar la
// copia compila y deja las rutas del módulo sin montar (404 de ruta inexistente, sin ningún error).
type publicAPIDeps struct {
	cfg config.AppConfig
	db  *sql.DB
	log sharedlogger.Logger
	mtx *metrics.Metrics
	// authStack es el material de auth compartido con el gateway CloudLink (buildAuthStack).
	authStack *authStack
	// entResolver es el resolver de derechos del proceso (c.entResolver, el de acceso NUEVO): el
	// MISMO, con su caché, que gatea el resto de la plataforma (T-2). Aquí lo leen la guarda del
	// alta del plano de roles, el canje de invitaciones y C2.
	//
	entResolver entitlements.Resolver
	// newFace llega con las áreas de los MÓDULOS ya armadas por la fase 8 (edge, inference,
	// requests, capture y conversation). Las cinco de acceso (common, auth, rolePlane, audit,
	// entitlements) llegan vacías y las rellena buildPublicAPIServer, que es quien construye
	// sus servicios.
	newFace newFaceDeps
	// platformRepo sirve el alta self-service (A7) como almacén de solicitudes de acceso.
	platformRepo *platformadmin.Repository
}

// buildPublicAPIServer arma el :8103: la cara (internal/apipublica, con sus 73 rutas) envuelta
// UNA vez con rate-limit y métricas. Devuelve también la cara, que el contenedor guarda para que
// los candados miren sus Patrones() sobre el arranque real.
func buildPublicAPIServer(d publicAPIDeps) (*http.Server, *apipublica.Cara, *httpapi.Middleware, httpapi.AuditRecorder, error) {
	// Los nombres locales son los de los parámetros de antes: el cuerpo no cambia. face es una
	// COPIA (como lo era el parámetro por valor), así que lo que aquí se le escribe no vuelve al
	// que llama.
	cfg, db, log, mtx, as, platformRepo := d.cfg, d.db, d.log, d.mtx, d.authStack, d.platformRepo
	face := d.newFace

	// El material de auth (emisor/validador ES256, middleware, auditor) se
	// construye UNA vez en buildAuthStack y se COMPARTE con el gateway CloudLink
	// (Plan 033 · T2.2, ADR-0025): el mismo verificador acepta en el :8103
	// exactamente los tokens que acepta el relé del Edge.
	authMW := as.authMW
	auditor := as.auditor

	// PLANO DE ROLES Y MIEMBROS de la empresa (Plan 047 · Ola 1.0 · T1.0-4, plano 2
	// del ADR-0033), con las invitaciones (Plan 047 · Ola A · T-A2/T-A8). Se resuelve
	// AQUÍ porque es una dependencia que solo existe para esta API y que este
	// constructor puede armar entero.
	//
	// Los tres servicios van a la cara (apipublica.MountRolePlane, B1–B14). Ninguna ausencia da error: un
	// grupo sin su servicio NO existe y responde 404 de ruta inexistente —
	// indistinguible desde fuera del 404 que estas mismas rutas dan al recurso ajeno—.
	// Eso es lo que vigila roleplane_cableado_test.go.
	//
	// El cliente M2M viaja al plano porque el alta de un miembro acredita la
	// aplicación en identity antes de escribir la fila (Plan 047 · Ola B). Su
	// ausencia NO desmonta ninguna ruta: es la diferencia entre «esta
	// administración no existe» (404) y «existe y le falta configuración» (503),
	// y confundirlas mandaría a depurar el router en vez del entorno.
	//
	// d.entResolver es el MISMO resolver cacheado que gatea el resto de la
	// plataforma (fase3_almacenes.go: c.entResolver, el de acceso NUEVO). Llega al
	// plano por la guarda del alta, no por las rutas: ver buildRolePlane.
	rolesPlane, err := buildRolePlane(db, as.m2mClient, d.entResolver, log)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if as.m2mClient == nil {
		log.Warn("POST /api/v1/members: falta WAPP_IDENTITY_API_KEY; el alta de miembros responde 503 " +
			"(sin acreditar la aplicación en identity, la persona quedaría de alta y sin poder entrar)")
	}

	// EL CANJE DE UNA INVITACIÓN (Plan 047 · Ola A · T-A3/T-A4/T-A5) y LA ELECCIÓN
	// DE EMPRESA (Plan 047 · Ola 5 · T5.1, D-047.14): las rutas que un token SIN
	// EMPRESA atraviesa, con `Authenticate` a secas (la cadena y su porqué viven
	// ahora en apipublica.MountAuth, A4–A6). Con la BD cableada existen SIEMPRE: un
	// fallo de construcción es de cableado y aborta el arranque, no degrada la ruta.
	invitationRedeem, err := buildInvitationRedeem(db, d.entResolver)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	activeTenant, err := buildActiveTenantPlane(db)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	// El PRESUPUESTO DE LA PETICIÓN de envío (Plan 050 · Ola 5 · T5.4, REQ-050.19) se
	// DERIVA del mismo writeTimeout con el que se arma el http.Server unas líneas más
	// abajo. Las dos líneas están a la vista una de otra a propósito: el defecto que
	// esto cierra nació de una aritmética escrita a mano en otro fichero que nadie
	// rehizo al añadir un reloj. Aquí no hay aritmética que mantener — mover
	// writeTimeout arrastra el presupuesto solo. Ver apipublica.SendBudgetFrom.
	//
	// D1 la sirve la cara, así que el presupuesto va a SUS deps.
	face.edge.messages.SendBudget = apipublica.SendBudgetFrom(writeTimeout)

	// Operación pública (Plan 018 · T5): mensajes + flujos CRUD/arranque, cada ruta
	// autenticada por Context Token + grants (mismo authMW) y las escrituras
	// auditadas (mismo auditor). El tenant SIEMPRE sale del token (INV-8). T10
	// añade GET /api/v1/audit. Todo eso lo monta caraNueva (mudanzas.go), más abajo.

	// Blindaje transversal de la API pública (Plan 018 · T10, R11): rate-limit por
	// credencial + métricas de request/latencia. Envuelven la cara ENTERA. Orden de
	// ejecución: métricas (siempre cuenta, incluso un 429) → rate-limit → cara. NO
	// tocan /healthz/metrics (viven en el listener admin). El cubo por IP del
	// login se fue con el login (identity Plan 003 · Ola 5).
	//
	// La cara (internal/apipublica) lleva las 73 rutas del :8103 (caraNueva, mudanzas.go: 23 de
	// acceso, 6 de edge, 4 de inferencia, 18 de solicitudes, 3 de captación y 19 de
	// conversación) y se sirve DIRECTAMENTE, con el *http.Request que le llega, así que
	// r.Pattern sube hasta la métrica. Rate-limit y métricas la envuelven una sola vez (RX.2.c;
	// lo vigila cara_nueva_cableado_test.go). Se devuelve para que los candados miren sus
	// Patrones() sobre el arranque real.
	//
	// Common es el MISMO para todas las áreas: el mismo middleware, el mismo auditor (que
	// además sirve C1 como lector) y el mismo logger.
	//
	// Las áreas de los módulos (edge, inferencia, solicitudes, captación y conversación) ya
	// vienen en face, armadas por la fase 8;
	// aquí solo se rellenan las de acceso, cuyos servicios se construyen arriba.
	face.common = apipublica.Common{MW: authMW, Auditor: auditor, Log: log}
	face.auth = apipublica.AuthDeps{
		Verifier: as.contextTokens,
		// Un nil DE VERDAD con el modo dual apagado: A2 responde 503 (authStack.exchanger).
		Exchanger:      as.exchanger(),
		Redeemer:       invitationRedeem,
		TenantSelector: activeTenant,
		TenantLister:   activeTenant,
		// A7 (Plan 056 · T3.2): sin M2M (falta WAPP_IDENTITY_API_KEY) MountAuth la
		// cablea a un 503 fijo y avisa con un Warn (C-02); con M2M, al handler real
		// con su limitador propio por IP (A-06a).
		SignupRequests:   platformRepo,
		M2M:              as.m2mClient,
		SignupTrustProxy: cfg.RateLimit.TrustProxy,
	}
	face.rolePlane = apipublica.RolePlaneDeps{
		Roles:       rolesPlane.roles,
		Members:     rolesPlane.members,
		Invitations: rolesPlane.invitations,
	}
	face.audit = apipublica.AuditDeps{Audit: auditor}
	face.entitlements = apipublica.EntitlementsDeps{Entitlements: d.entResolver}
	cara := caraNueva(face)
	publicLim := httpapi.NewLimiter(rate.Limit(cfg.RateLimit.PublicRPS), cfg.RateLimit.PublicBurst)
	var handler http.Handler = cara
	handler = httpapi.PublicRateLimit(handler, publicLim, mtx, log)
	handler = mtx.InstrumentHTTP("public", handler)

	srv := &http.Server{
		Addr:              cfg.PublicHTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		// 🔴 El MISMO writeTimeout del que se derivó el SendBudget de D1 arriba. Si mueves
		// este valor no hay nada más que ajustar: el presupuesto lo sigue.
		WriteTimeout: writeTimeout,
		IdleTimeout:  idleTimeout,
	}
	return srv, cara, authMW, auditor, nil
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
