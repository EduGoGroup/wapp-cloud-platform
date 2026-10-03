//go:build integracion

package procesos

import (
	"net/http"
	"strings"
	"testing"
)

// TestP2_RutasDePlataformaDenegadasAlCliente (R9.6.b, paso 6 de P2, invariante I-CP-5): las diez
// rutas del plano de plataforma —las que actúan sobre una empresa AJENA y piden un permiso «.any»—
// están cerradas para la administradora de una empresa cliente, aunque su rol tenant_admin tenga el
// comodín «*», y abiertas para el staff de plataforma. Es la regla de
// internal/platformadmin/platform_permissions_test.go del código viejo, vista por la puerta.

// p2PlatformRoute es una ruta del plano de plataforma (internal/bootstrap/arranque/rutas_admin.go).
type p2PlatformRoute struct {
	Metodo  string
	Ruta    string // con {tenant} y {request} por sustituir
	Permiso string // el permiso que exige y la acción con la que se audita
	Recurso string // el recurso con el que se audita
	Cuerpo  func(tenant string) any
	// Staff es el código que recibe el staff de plataforma con esa misma petición (medido contra el
	// binario viejo). Lo que afirma I-CP-5 es que NO es 403 ni 401; el valor exacto es el oráculo
	// para el binario nuevo.
	Staff int
}

// p2PlatformRoutes son las diez rutas «.any», en el orden en que se registran. El orden importa
// para el staff: la revocación comercial va antes que la restauración, que la deshace.
func p2PlatformRoutes() []p2PlatformRoute {
	sinCuerpo := func(string) any { return nil }
	delTenant := func(tenant string) any { return map[string]string{"tenant_id": tenant} }
	return []p2PlatformRoute{
		{http.MethodGet, "/admin/tenants", "tenants.read.any", "tenant", sinCuerpo, http.StatusOK},
		{http.MethodPost, "/admin/tenants", "tenants.create.any", "tenant", func(string) any {
			return map[string]string{"slug": "p2-plataforma-alta", "display_name": "Alta de P2", "plan_id": planTenantPorDefecto}
		}, http.StatusCreated},
		{http.MethodGet, "/admin/tenants/{tenant}", "tenants.read.any", "tenant", sinCuerpo, http.StatusOK},
		{http.MethodGet, "/admin/tenants/{tenant}/installations", "fleet.read.any", "fleet", sinCuerpo, http.StatusOK},
		{http.MethodPost, "/admin/tenants/{tenant}/enrollment-codes", "enrollment.issue.any", "enrollment", sinCuerpo, http.StatusCreated},
		{http.MethodGet, "/admin/access-requests", "users.provision.any", "user", sinCuerpo, http.StatusOK},
		{http.MethodPost, "/admin/access-requests/{request}/approve", "users.provision.any", "user", func(tenant string) any {
			return map[string]string{"tenant_id": tenant, "role": "tenant_admin"}
		}, http.StatusNotFound},
		{http.MethodPost, "/admin/access-requests/{request}/reject", "users.provision.any", "user", func(string) any {
			return map[string]string{"reason": "proceso P2"}
		}, http.StatusNotFound},
		{http.MethodPost, "/admin/tenants/revoke", "tenants.revoke.any", "tenant", delTenant, http.StatusNoContent},
		{http.MethodPost, "/admin/tenants/restore", "tenants.restore.any", "tenant", delTenant, http.StatusNoContent},
	}
}

// TestP2_RutasDePlataformaDenegadasAlCliente recorre las diez rutas con el token de la
// administradora de una empresa cliente (403 en todas, sin efecto y sin auditoría), sin token (401)
// y con el del staff de plataforma (ni 403 ni 401). Necesita Docker.
func TestP2_RutasDePlataformaDenegadasAlCliente(t *testing.T) {
	t.Parallel()
	esc := edgeEscenarioNuevo(t, "p2_platform", "p2-plataforma", false)
	admin := uuidAleatorio(t)
	edgeAltaAdminDelTenant(t, esc.DB, admin, esc.Tenant)
	resAdmin := p2Exchange(t, esc.S, admin)
	p2WantContext(t, "canje de la administradora de cliente", resAdmin, esc.Tenant, "tenant_admin")
	sinEmpresa := p2NewGuest(t, esc.S)

	rutas := p2PlatformRoutes()
	if len(rutas) != 10 {
		t.Fatalf("la tabla tiene %d rutas de plataforma, I-CP-5 habla de 10", len(rutas))
	}
	solicitud := uuidAleatorio(t) // una solicitud de acceso que no existe
	pedir := func(t *testing.T, token string, ruta p2PlatformRoute) respuesta {
		t.Helper()
		destino := strings.NewReplacer("{tenant}", esc.Tenant, "{request}", solicitud).Replace(ruta.Ruta)
		return esc.S.Admin(token).hacer(t, ruta.Metodo, destino, ruta.Cuerpo(esc.Tenant))
	}
	const estado = `SELECT (SELECT count(*) FROM public.tenants)::text || '|' ||
		(SELECT count(*) FROM public.enrollment_codes WHERE tenant_id = $1::uuid)::text || '|' ||
		(SELECT (revoked_at IS NOT NULL)::text FROM public.tenants WHERE id = $1::uuid)`
	antes := p2Text(t, esc.DB, estado, esc.Tenant)

	t.Run("la administradora de cliente recibe 403 en las diez", func(t *testing.T) {
		for _, ruta := range rutas {
			t.Run(ruta.Metodo+" "+ruta.Ruta, func(t *testing.T) {
				p2WantCode(t, "administradora de cliente", pedir(t, resAdmin.ContextToken, ruta), http.StatusForbidden)
				p2WantCode(t, "usuario sin empresa", pedir(t, sinEmpresa.Token, ruta), http.StatusForbidden)
				p2WantCode(t, "sin token", pedir(t, "", ruta), http.StatusUnauthorized)
			})
		}
		// El 403 es de verdad: no creó la empresa, no emitió códigos y no revocó nada.
		if despues := p2Text(t, esc.DB, estado, esc.Tenant); despues != antes {
			t.Errorf("los 403 cambiaron el estado (empresas|códigos|revocada): %q → %q", antes, despues)
		}
		// Y lo corta el middleware de permisos, antes de la auditoría: de la administradora y del
		// usuario sin empresa solo quedan sus canjes en la bitácora.
		for _, actor := range []string{admin, sinEmpresa.ID} {
			if n := consultaEntero(t, esc.DB, `SELECT count(*) FROM public.audit_events WHERE actor = $1 AND action <> $2`, actor, p2AuditExchange); n != 0 {
				t.Errorf("audit_events de %s: %d filas que no son canjes, quería 0", actor, n)
			}
		}
		// No es que el token de la administradora no valga: su propio plano le contesta.
		p2WantCode(t, "la administradora en su plano", esc.S.Publica(resAdmin.ContextToken).Get(t, p2RouteRoles, nil), http.StatusOK)
	})

	t.Run("el staff de plataforma no recibe 403 en ninguna", func(t *testing.T) {
		for _, ruta := range rutas {
			t.Run(ruta.Metodo+" "+ruta.Ruta, func(t *testing.T) {
				r := pedir(t, esc.TokenStaff, ruta)
				if r.Codigo == http.StatusForbidden || r.Codigo == http.StatusUnauthorized {
					t.Fatalf("staff de plataforma: HTTP %d, I-CP-5 pide que no sea 403 ni 401\ncuerpo: %s", r.Codigo, recortar(r.Cuerpo))
				}
				p2WantCode(t, "staff de plataforma", r, ruta.Staff)
			})
		}
		// Las rutas del staff sí pasaron por la auditoría, con el permiso como acción.
		if n := consultaEntero(t, esc.DB, `SELECT count(DISTINCT action) FROM public.audit_events WHERE action LIKE '%.any' AND tenant_id = $1::uuid`, tenantPlataformaID); n != 7 {
			t.Errorf("audit_events tiene %d permisos «.any» distintos anotados para el staff, quería los 7 de la tabla", n)
		}
		// La revocación comercial y su restauración dejaron la empresa como estaba.
		if revocada := p2Text(t, esc.DB, `SELECT (revoked_at IS NOT NULL)::text FROM public.tenants WHERE id = $1::uuid`, esc.Tenant); revocada != "false" {
			t.Errorf("tras revocar y restaurar, tenants.revoked_at IS NOT NULL = %s", revocada)
		}
	})

	edgeSinErrores(t, esc.S, nil)
}
