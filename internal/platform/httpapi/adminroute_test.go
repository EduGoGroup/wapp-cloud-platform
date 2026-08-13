package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	identityrbac "github.com/EduGoGroup/identity-shared/auth/rbac"
)

// Este fichero prueba el CERROJO DE ARRIBA del plano de plataforma (ADR-0039):
// el middleware RBAC REAL, con tokens firmados de verdad, no fakes.
//
// POR QUÉ HACE FALTA, Y POR QUÉ NO BASTABA admin_test.go. Los tests de handler
// inyectan la Identity a mano, así que pasan igual con cualquier permiso en la
// ruta: nunca habrían notado que 'tenants.revoke' quedaba cubierto por el grant
// '*' de tenant_admin — es decir, que TODO administrador de TODO cliente podía
// disparar el kill-switch comercial. Esa fue exactamente la creencia que dejó el
// agujero abierto ("el '*' ya lo cubre, no hace falta migración de IAM"): era
// cierta, y por eso era el problema.
//
// Lo que se afirma aquí es el comportamiento COMPUESTO de tres piezas que viven
// en repos distintos y que ningún test tocaba a la vez: el patrón de permiso que
// registra la ruta (bootstrap.go), los grants sembrados (0059_platform_admin.sql)
// y el matcher glob de identity-shared. Los grants se escriben literales, calcados
// del seed, para que un cambio en la migración que rompa esta lógica aparezca
// aquí como un rojo y no en producción como un cliente cortando a otro.

// grantsTenantAdmin reproduce los grants EFECTIVOS del rol canónico
// tenant_admin tras la migración 0059: su '*' de siempre (0015) más el deny
// nuevo.
var grantsTenantAdmin = identityrbac.Grants{
	Allow: []string{"*"},
	Deny:  []string{"*.any"},
}

// grantsPlatformAdmin reproduce los del rol platform_admin (0059): los dos
// permisos del plano de plataforma, y nada más.
var grantsPlatformAdmin = identityrbac.Grants{
	Allow: []string{"tenants.revoke.any", "tenants.restore.any"},
}

// runWithGrants monta Authenticate → RequirePermission(perm) sobre un handler
// terminal que responde 200, firma un token con los grants dados y devuelve el
// código resultante. Es el mismo montaje que usa el bootstrap para cada ruta
// admin, sin el auditor.
func runWithGrants(t *testing.T, grants identityrbac.Grants, perm string) int {
	t.Helper()
	mw, jwt := newMW()
	tok := userToken(t, jwt, grants)

	final := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := mw.Authenticate(mw.RequirePermission(perm)(final))

	req := httptest.NewRequest(http.MethodPost, "/admin/tenants/revoke", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

// TestAdminRBAC_TenantAdminWildcardCannotRevokeAny es el corazón del arreglo:
// el administrador de una empresa cliente, con su comodín total, NO alcanza el
// kill-switch comercial. Si este test se pone verde por accidente (por ejemplo
// si alguien borra el deny '*.any' de la 0059 por "limpiar"), cualquier cliente
// podría cortar a cualquier otro.
func TestAdminRBAC_TenantAdminWildcardCannotRevokeAny(t *testing.T) {
	t.Parallel()
	if code := runWithGrants(t, grantsTenantAdmin, "tenants.revoke.any"); code != http.StatusForbidden {
		t.Fatalf("tenants.revoke.any con grants de tenant_admin: code = %d, want 403.\n"+
			"El deny '*.any' (0059_platform_admin.sql) es lo único que impide que el grant "+
			"'*' de tenant_admin (0015) alcance el kill-switch COMERCIAL de un tenant ajeno.", code)
	}
	if code := runWithGrants(t, grantsTenantAdmin, "tenants.restore.any"); code != http.StatusForbidden {
		t.Fatalf("tenants.restore.any con grants de tenant_admin: code = %d, want 403", code)
	}
}

// TestAdminRBAC_PlatformAdminCanRevokeAny: el rol que sí es dueño del plano
// pasa. Sin este caso, el test de arriba se satisfaría con un deny global que
// dejara el kill-switch inservible para todo el mundo, wApp incluida.
func TestAdminRBAC_PlatformAdminCanRevokeAny(t *testing.T) {
	t.Parallel()
	if code := runWithGrants(t, grantsPlatformAdmin, "tenants.revoke.any"); code != http.StatusOK {
		t.Fatalf("tenants.revoke.any con grants de platform_admin: code = %d, want 200", code)
	}
	if code := runWithGrants(t, grantsPlatformAdmin, "tenants.restore.any"); code != http.StatusOK {
		t.Fatalf("tenants.restore.any con grants de platform_admin: code = %d, want 200", code)
	}
}

// TestAdminRBAC_TenantAdminStillPassesLeasesRevoke acota el daño colateral del
// deny. '*.any' es un patrón de FORMA, no una lista: cubre cualquier permiso
// terminado en '.any', presente o futuro. Lo que NO debe tocar es lo que ya
// existía — y en particular el otro kill-switch, el anti-clon por instalación
// (ADR-0007), que es del tenant y tiene que seguir siéndolo. Sin este caso, el
// arreglo del plano de plataforma podría haber dejado a los clientes sin poder
// cortar sus propias máquinas comprometidas.
func TestAdminRBAC_TenantAdminStillPassesLeasesRevoke(t *testing.T) {
	t.Parallel()
	for _, perm := range []string{"leases.revoke", "flows.create", "messages.send"} {
		if code := runWithGrants(t, grantsTenantAdmin, perm); code != http.StatusOK {
			t.Errorf("%s con grants de tenant_admin: code = %d, want 200 (el deny '*.any' no debe rozar lo existente)", perm, code)
		}
	}
}
