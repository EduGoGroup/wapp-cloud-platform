// Copia de internal/bootstrap/arranque/http.go @ 80807ba (F0 · 05 §6): cablea paquetes VIEJOS,
// salvo acceso (F2, T2.31) y edge (F3, T3.28), que son internal/modulos/{acceso,edge}: sus
// rutas del :8103 (A–C y D1–D6) las sirve la cara nueva, internal/apipublica.
package arranque

import (
	"database/sql"
	"net/http"
	"time"

	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"
	"golang.org/x/time/rate"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/platformadmin"
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

// edgeFaceDeps es lo que la cara nueva necesita para D1–D6 (F3 · conmutar(edge)). Lo arma la
// fase 8 (edgeDepsOfTheNewFace) con el gateway, la flota y el almacén de diagnóstico del
// contenedor; buildPublicAPIServer solo le añade el presupuesto de envío, que deriva de su
// propio writeTimeout.
type edgeFaceDeps struct {
	// messages enciende D1.
	messages apipublica.MessagesDeps
	// sessions enciende D2–D4.
	sessions apipublica.SessionsDeps
	// diagnostics enciende D5–D6.
	diagnostics apipublica.DiagnosticsDeps
}

// buildPublicAPIServer arma el :8103: la cara NUEVA (internal/apipublica, con las rutas de las
// fases ≤ FaseActual) delante del mux VIEJO (publicapi), compuestas una vez y envueltas una vez
// con rate-limit y métricas. Devuelve también la cara y el compuesto, que el contenedor guarda
// para el candado de mudanzas.
func buildPublicAPIServer(cfg config.AppConfig, db *sql.DB, log sharedlogger.Logger, mtx *metrics.Metrics, as *authStack, pub publicapi.Deps, edge edgeFaceDeps, platformRepo *platformadmin.Repository) (*http.Server, *apipublica.Cara, *apipublica.Compuesto, *httpapi.Middleware, httpapi.AuditRecorder, error) {
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
	// 🔀 F2 · conmutar(acceso): los tres servicios ya no van a publicapi.Deps sino a la
	// cara nueva (apipublica.MountRolePlane, B1–B14). Ninguna ausencia da error: un
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
	// pub.Entitlements es el MISMO resolver cacheado que gatea el resto de la
	// plataforma (fase3_almacenes.go: c.entResolver, el de acceso NUEVO). Llega al
	// plano por la guarda del alta, no por las rutas: ver buildRolePlane.
	rolesPlane, err := buildRolePlane(db, as.m2mClient, pub.Entitlements, log)
	if err != nil {
		return nil, nil, nil, nil, nil, err
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
	invitationRedeem, err := buildInvitationRedeem(db, pub.Entitlements)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	activeTenant, err := buildActiveTenantPlane(db)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}

	// 🔀 F2 · conmutar(acceso) (FX TX.7): la cara VIEJA ya no sirve el plano de roles
	// ni la bitácora. Sus cuatro campos van a nil A PROPÓSITO: con nil, publicapi no
	// registra B1–B14 ni C1, que sirve la cara nueva. pub.Entitlements SIGUE puesto
	// (la vieja lo necesita para E2 y para los gates de feature de sus rutas): es el
	// MISMO resolver que recibe la cara nueva para C2 —una sola caché (T-5)—.
	pub.Roles, pub.Members, pub.Invitations, pub.Audit = nil, nil, nil, nil

	// El mux de la cara VIEJA. Desde F2 ya no registra A1–A7 (verify, exchange,
	// whoami, canje, empresa activa ×2 y signup): los monta apipublica.MountAuth.
	publicMux := http.NewServeMux()

	// El PRESUPUESTO DE LA PETICIÓN de envío (Plan 050 · Ola 5 · T5.4, REQ-050.19) se
	// DERIVA del mismo writeTimeout con el que se arma el http.Server unas líneas más
	// abajo. Las dos líneas están a la vista una de otra a propósito: el defecto que
	// esto cierra nació de una aritmética escrita a mano en otro fichero que nadie
	// rehizo al añadir un reloj. Aquí no hay aritmética que mantener — mover
	// writeTimeout arrastra el presupuesto solo. Ver apipublica.SendBudgetFrom.
	//
	// 🔀 F3 · conmutar(edge): D1 la sirve la cara nueva, así que el presupuesto va a SUS
	// deps. La D1 del mux viejo sigue registrada (no tiene condición de montaje) pero
	// queda tapada por la nueva y nunca atiende: no se le cablea nada.
	edge.messages.SendBudget = apipublica.SendBudgetFrom(writeTimeout)

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
	//
	// 🔀 F0 · desviación de la copia (T0.16/TX.3, D-10): delante del mux viejo va la
	// cara NUEVA (internal/apipublica) con las rutas de las fases ≤ FaseActual
	// (caraNueva, mudanzas.go; desde F3, las 23 de acceso y las 6 de edge). El Compuesto sirve por la
	// nueva lo que ella registre y delega el resto en publicMux con el MISMO
	// *http.Request, así que r.Pattern sigue llegando a la métrica. Rate-limit y métricas
	// envuelven el COMPUESTO una sola vez (RX.2.c; lo vigila cara_nueva_cableado_test.go).
	// La cara y el compuesto se devuelven para que el candado de mudanzas los mire patrón
	// a patrón (Cara.Patrones, Compuesto.Resolver).
	//
	// Common es el MISMO para todas las áreas y para las dos caras: el mismo middleware,
	// el mismo auditor (que además sirve C1 como lector) y el mismo logger.
	cara := caraNueva(newFaceDeps{
		common: apipublica.Common{MW: authMW, Auditor: auditor, Log: log},
		auth: apipublica.AuthDeps{
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
		},
		rolePlane: apipublica.RolePlaneDeps{
			Roles:       rolesPlane.roles,
			Members:     rolesPlane.members,
			Invitations: rolesPlane.invitations,
		},
		audit:        apipublica.AuditDeps{Audit: auditor},
		entitlements: apipublica.EntitlementsDeps{Entitlements: pub.Entitlements},
		// 🔀 F3 · conmutar(edge) (FX TX.11): D1–D6, con lo que hasta F3 iba a la vieja.
		messages:    edge.messages,
		sessions:    edge.sessions,
		diagnostics: edge.diagnostics,
	})
	compuesto := apipublica.Componer(cara, publicMux)
	publicLim := httpapi.NewLimiter(rate.Limit(cfg.RateLimit.PublicRPS), cfg.RateLimit.PublicBurst)
	var handler http.Handler = compuesto
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
	return srv, cara, compuesto, authMW, auditor, nil
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
