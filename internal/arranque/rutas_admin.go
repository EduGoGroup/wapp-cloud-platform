// Copia de internal/bootstrap/arranque/rutas_admin.go @ 80807ba (F0 · 05 §6): cablea paquetes VIEJOS,
// salvo acceso (F2, T2.31, conmutar(acceso)) y edge (F3, T3.28, conmutar(edge)), que son
// internal/modulos/{acceso,edge}: un solo gateway, el nuevo, que recibe acceso sin adaptador.
package arranque

import (
	"net/http"

	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/platformadmin"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// adminRouteDeps agrupa lo que registerAdminRoutes necesita para cablear el mux
// admin/health (:8100): las tres piezas transversales de adminHandler
// (autenticación, auditoría, log) y un handler por ruta.
//
// Las rutas de plataforma (/admin/tenants*, /admin/access-requests*) son la
// excepción: en vez de un http.Handler ya armado, viajan las piezas CRUDAS
// (platformRepo, platformTenantID, codeStore, m2mClient) y registerAdminRoutes
// construye el platformadmin.*Handler(...) INLINE, dentro de la propia llamada a
// mux.Handle. Eso no es un capricho de estilo: TestINV056_1_PlatformPermissionsMustEndInDotAny
// (platform_permissions_test.go, A-01) detecta una ruta de plataforma leyendo el
// TEXTO fuente del argumento handler de cada adminHandler(...) en busca de
// "platformadmin." — si esas llamadas se pre-construyeran en un campo http.Handler
// como las demás, el detector se quedaría ciego ante una ruta de plataforma nueva
// registrada bajo un patrón que no empiece por /admin/tenants o /admin/access-requests
// (justo el escenario que A-01 documenta). Cualquier handler platformadmin.* nuevo
// debe seguir este mismo patrón: pieza cruda en el struct, constructor inline abajo.
type adminRouteDeps struct {
	authMW  *httpapi.Middleware
	auditor httpapi.AuditRecorder
	log     sharedlogger.Logger

	health         http.Handler
	metrics        http.Handler
	revokeLease    http.Handler
	sendMessage    http.Handler
	cryptoRekey    http.Handler
	flowsCreate    http.Handler
	flowsStart     http.Handler
	triggersCreate http.Handler
	triggersList   http.Handler
	triggersDelete http.Handler
	sessionProfile http.Handler
	sessionStatus  http.Handler
	revokeTenant   http.Handler
	restoreTenant  http.Handler

	platformRepo     *platformadmin.Repository
	platformTenantID string
	codeStore        platformadmin.CodeIssuer
	m2mClient        out.IdentityM2MClient
}

// registerAdminRoutes cablea TODAS las rutas del listener admin/health (:8100)
// contra mux: patrón HTTP → adminHandler(auth + permiso + auditoría) → handler.
// Es la ÚNICA función que hace este cableado (Plan 056 · A-02): la fase de
// transporte y el test de registro (mux_registration_test.go,
// TestMuxRegistration_NoPanic) llaman a esta MISMA función — un test que
// reconstruyera esta lista a mano solo probaría su propia copia, no lo que corre en
// producción (el conflicto de patrones que http.ServeMux resuelve con panic AL
// REGISTRAR, no en caliente).
func registerAdminRoutes(mux *http.ServeMux, d adminRouteDeps) {
	mux.Handle("/healthz", d.health)
	mux.Handle("/metrics", d.metrics)
	mux.Handle("/admin/leases/revoke", adminHandler(d.authMW, d.auditor, d.log,
		"leases.revoke", "lease", d.revokeLease))
	// Kill-switch COMERCIAL por tenant (Plan 055 · T3.3, D-055.2) y rutas de
	// plataforma (Plan 056 · T1.3, T3.4): mismo patrón adminHandler (auth + permiso + auditoría).
	// Aquí el objetivo es un tenant AJENO (ADR-0039), así que el permiso lleva el
	// sufijo '.any': la migración 0059/0060 se lo da SOLO a platform_admin y se lo
	// niega al '*' de tenant_admin con un deny '*.any'. Ese deny es la pieza que
	// convierte esto en un permiso de verdad — sin él, el '*' de cualquier
	// administrador de cliente ya cubriría estas rutas. El handler además exige,
	// por su cuenta, que el token sea del tenant de plataforma.
	mux.Handle("GET /admin/tenants", adminHandler(d.authMW, d.auditor, d.log,
		"tenants.read.any", "tenant", platformadmin.ListTenantsHandler(d.platformRepo, d.platformTenantID)))
	mux.Handle("POST /admin/tenants", adminHandler(d.authMW, d.auditor, d.log,
		"tenants.create.any", "tenant", platformadmin.CreateTenantHandler(d.platformRepo, d.platformTenantID)))
	mux.Handle("GET /admin/tenants/{id}", adminHandler(d.authMW, d.auditor, d.log,
		"tenants.read.any", "tenant", platformadmin.GetTenantHandler(d.platformRepo, d.platformTenantID)))
	mux.Handle("GET /admin/tenants/{id}/installations", adminHandler(d.authMW, d.auditor, d.log,
		"fleet.read.any", "fleet", platformadmin.ListInstallationsHandler(d.platformRepo, d.platformTenantID)))
	mux.Handle("POST /admin/tenants/{id}/enrollment-codes", adminHandler(d.authMW, d.auditor, d.log,
		"enrollment.issue.any", "enrollment", platformadmin.IssueEnrollmentCodeHandler(d.platformRepo, d.codeStore, d.platformTenantID)))
	mux.Handle("GET /admin/access-requests", adminHandler(d.authMW, d.auditor, d.log,
		"users.provision.any", "user", platformadmin.ListAccessRequestsHandler(d.platformRepo, d.platformTenantID)))
	mux.Handle("POST /admin/access-requests/{id}/approve", adminHandler(d.authMW, d.auditor, d.log,
		"users.provision.any", "user", platformadmin.ApproveAccessRequestHandler(d.platformRepo, d.platformRepo, d.m2mClient, d.platformTenantID)))
	mux.Handle("POST /admin/access-requests/{id}/reject", adminHandler(d.authMW, d.auditor, d.log,
		"users.provision.any", "user", platformadmin.RejectAccessRequestHandler(d.platformRepo, d.platformTenantID)))
	mux.Handle("POST /admin/tenants/revoke", adminHandler(d.authMW, d.auditor, d.log,
		"tenants.revoke.any", "tenant", d.revokeTenant))
	mux.Handle("POST /admin/tenants/restore", adminHandler(d.authMW, d.auditor, d.log,
		"tenants.restore.any", "tenant", d.restoreTenant))
	mux.Handle("/admin/messages/send", adminHandler(d.authMW, d.auditor, d.log,
		"messages.send", "message", d.sendMessage))
	mux.Handle("/admin/crypto/rekey", adminHandler(d.authMW, d.auditor, d.log,
		"crypto.rekey", "kek", d.cryptoRekey))
	mux.Handle("/admin/flows", adminHandler(d.authMW, d.auditor, d.log,
		"flows.create", "flow", d.flowsCreate))
	mux.Handle("/admin/flows/start", adminHandler(d.authMW, d.auditor, d.log,
		"flows.start", "flow", d.flowsStart))
	mux.Handle("POST /admin/triggers", adminHandler(d.authMW, d.auditor, d.log,
		"triggers.create", "trigger", d.triggersCreate))
	mux.Handle("GET /admin/triggers", adminHandler(d.authMW, d.auditor, d.log,
		"triggers.read", "trigger", d.triggersList))
	mux.Handle("DELETE /admin/triggers/{id}", adminHandler(d.authMW, d.auditor, d.log,
		"triggers.delete", "trigger", d.triggersDelete))
	mux.Handle("POST /admin/sessions/{id}/profile", adminHandler(d.authMW, d.auditor, d.log,
		"sessions.write", "session", d.sessionProfile))
	mux.Handle("POST /admin/sessions/{id}/status", adminHandler(d.authMW, d.auditor, d.log,
		"sessions.write", "session", d.sessionStatus))
}
