//go:build integracion

package procesos

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"testing"
	"time"
)

// P2 · Canje de identidad y permisos (diseno.md §4 «P2», T9.14). Tres procesos, uno por fichero:
//
//   - TestP2_ExchangeAndPermissions (este fichero): pasos 1, 2, 4, 5 y 7. Sus tablas de casos
//     adversarios están en p2_canje_adversarial_test.go.
//   - TestP2_InvitacionUnSoloCanje (p2_canje_invitations_test.go): paso 3 y R9.6.a.
//   - TestP2_RutasDePlataformaDenegadasAlCliente (p2_canje_platform_routes_test.go): paso 6, R9.6.b.
//
// Todo lo que se afirma aquí se MIDIÓ contra el binario viejo (cmd/server); nada sale de un
// documento. Lo que comparten los tres ficheros (constantes, fixtures y lecturas) vive en este.

const (
	// p2RoleOperator y p2RoleViewer son las plantillas globales operator y viewer que siembra la
	// migración 0015 (tenant_admin es edgeRolTenantAdmin y platform_admin, rolPlataformaID).
	p2RoleOperator = "10000000-0000-0000-0000-000000000002"
	p2RoleViewer   = "10000000-0000-0000-0000-000000000003"

	p2RouteWhoAmI       = "/api/v1/auth/whoami"
	p2RouteTenants      = "/api/v1/auth/tenants"
	p2RouteActiveTenant = "/api/v1/auth/active-tenant"
	p2RouteRoles        = "/api/v1/roles"
	p2RouteMembers      = "/api/v1/members"
	p2RouteInvitations  = "/api/v1/invitations"
	p2RouteAccept       = "/api/v1/invitations/accept"

	// p2AuditExchange es la acción con la que el canje se anota en audit_events (recurso «auth»,
	// resultado «ok» o «error»). Las rutas con permiso anotan como acción su permiso y como
	// resultado «success» (< 400) o «failure».
	p2AuditExchange = "auth.exchange"
)

// p2AuditCount cuenta las filas de audit_events de un actor con esa acción, recurso, resultado y
// empresa («» = tenant_id NULL, que es como anota el canje de quien no tiene empresa).
const p2AuditCount = `SELECT count(*)::text FROM public.audit_events
	WHERE actor = $1 AND action = $2 AND resource = $3 AND result = $4 AND COALESCE(tenant_id::text, '') = $5`

// p2AddMember deja a usuario como miembro de la empresa y, si rol no es «», con ese rol acotado a
// ella. Es idempotente. Falla (t.Fatalf) si un id no es un UUID o el SQL falla.
//
// 🔧 POR QUÉ NO HAY PUERTA HTTP: el alta de un miembro por la puerta (POST /api/v1/members)
// acredita antes al usuario en identity-api con un cliente M2M que el arnés no levanta, y contesta
// 503 (es justo el paso 7 de este proceso). La otra puerta, la invitación, no sirve para una
// SEGUNDA empresa: la guarda de «una sola empresa por usuario» la rechaza salvo con la
// capacidad multi_empresa, que el plan de las empresas del arnés no trae. Para tenant_admin se usa
// edgeAltaAdminDelTenant; este fixture cubre los demás roles y el alta sin rol.
func p2AddMember(t *testing.T, db *sql.DB, usuario, tenant, rol string) {
	t.Helper()
	ids := map[string]string{"el usuario": usuario, "la empresa": tenant}
	if rol != "" {
		ids["el rol"] = rol
	}
	for nombre, valor := range ids {
		if err := exigirUUID(nombre, valor); err != nil {
			t.Fatalf("p2AddMember: %v", err)
		}
	}
	ctx, cancelar := context.WithTimeout(t.Context(), topeFixture)
	defer cancelar()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.tenant_members (user_id, tenant_id) VALUES ($1::uuid, $2::uuid)
		ON CONFLICT DO NOTHING`, usuario, tenant); err != nil {
		t.Fatalf("p2AddMember: insertar en tenant_members: %v", err)
	}
	if rol == "" {
		return
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.iam_user_roles (user_id, role_id, tenant_id) VALUES ($1::uuid, $2::uuid, $3::uuid)
		ON CONFLICT DO NOTHING`, usuario, rol, tenant); err != nil {
		t.Fatalf("p2AddMember: insertar en iam_user_roles: %v", err)
	}
}

// p2Exchange canjea un Identity Token de wapp.bff del usuario y devuelve la respuesta entera del
// canje. Falla (t.Fatalf) si no es un 200 con context_token.
func p2Exchange(t *testing.T, s *servidor, usuario string) resultadoCanje {
	t.Helper()
	r := canje(t, s, s.Identidad.TokenDe(usuario, "wapp.bff"))
	var res resultadoCanje
	if r.Codigo == http.StatusOK {
		r.JSON(t, &res)
	}
	if r.Codigo != http.StatusOK || res.ContextToken == "" || res.Context.UserID != usuario {
		t.Fatalf("canje de %s: HTTP %d, quería 200 con context_token de ese usuario\ncuerpo: %s", usuario, r.Codigo, recortar(r.Cuerpo))
	}
	return res
}

// p2ErrorOf devuelve el campo «error» del cuerpo JSON de una respuesta de rechazo, o «» si el
// cuerpo no es un objeto JSON con ese campo.
func p2ErrorOf(r respuesta) string {
	var cuerpo struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(r.Cuerpo, &cuerpo); err != nil {
		return ""
	}
	return cuerpo.Error
}

// p2Text lee una consulta de una fila y una columna de texto. Sin filas devuelve «». Falla
// (t.Fatalf) si la consulta da otro error.
func p2Text(t *testing.T, db *sql.DB, consulta string, args ...any) string {
	t.Helper()
	var valor string
	switch err := db.QueryRowContext(t.Context(), consulta, args...).Scan(&valor); err {
	case nil, sql.ErrNoRows:
		return valor
	default:
		t.Fatalf("consulta %q: %v", consulta, err)
		return ""
	}
}

// p2WantCode falla el test (t.Errorf) si la respuesta no trae el código esperado.
func p2WantCode(t *testing.T, que string, r respuesta, quiere int) {
	t.Helper()
	if r.Codigo != quiere {
		t.Errorf("%s: HTTP %d, quería %d\ncuerpo: %s", que, r.Codigo, quiere, recortar(r.Cuerpo))
	}
}

// p2WantAudit espera a que audit_events tenga exactamente n filas del actor con esa acción,
// recurso, resultado y empresa («» = sin empresa).
func p2WantAudit(t *testing.T, db *sql.DB, n int, actor, accion, recurso, resultado, tenant string) {
	t.Helper()
	edgeEsperarValor(t, db, strconv.Itoa(n),
		fmt.Sprintf("audit_events de %s con %s/%s/%s en la empresa %q", actor, accion, recurso, resultado, tenant),
		p2AuditCount, actor, accion, recurso, resultado, tenant)
}

// p2WantContext falla el test si el contexto de un canje no es el de esa empresa («» = sin
// empresa) con exactamente esos roles.
func p2WantContext(t *testing.T, que string, res resultadoCanje, tenant string, roles ...string) {
	t.Helper()
	tiene := slices.Sorted(slices.Values(res.Context.Roles))
	if res.Context.TenantID != tenant || !slices.Equal(tiene, slices.Sorted(slices.Values(roles))) {
		t.Errorf("%s: contexto %+v, quería la empresa %q con los roles %v", que, res.Context, tenant, roles)
	}
}

// p2TenantOption es una fila de GET /api/v1/auth/tenants.
type p2TenantOption struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Active      bool   `json:"active"`
}

// p2TenantsOf lee GET /api/v1/auth/tenants con ese token y devuelve las empresas indexadas por id.
// Falla (t.Fatalf) si no es un 200 o trae la clave «tenants» ausente (nula).
func p2TenantsOf(t *testing.T, s *servidor, token string) map[string]p2TenantOption {
	t.Helper()
	r := s.Publica(token).Get(t, p2RouteTenants, nil)
	var lista struct {
		Tenants []p2TenantOption `json:"tenants"`
	}
	if r.Codigo == http.StatusOK {
		r.JSON(t, &lista)
	}
	if r.Codigo != http.StatusOK || lista.Tenants == nil {
		t.Fatalf("GET %s: HTTP %d %s, quería 200 con la lista «tenants»", p2RouteTenants, r.Codigo, recortar(r.Cuerpo))
	}
	porID := map[string]p2TenantOption{}
	for _, o := range lista.Tenants {
		porID[o.ID] = o
	}
	return porID
}

// TestP2_ExchangeAndPermissions recorre el canje de identidad y los permisos por la puerta HTTP:
// quien no tiene empresa (paso 1), la administradora de una empresa (paso 2), quien tiene dos y
// elige (paso 4), el RBAC de roles y grants (paso 5), el alta de miembro sin identity (paso 7) y
// los canjes que se rechazan. Necesita Docker.
func TestP2_ExchangeAndPermissions(t *testing.T) {
	t.Parallel()
	esc := edgeEscenarioNuevo(t, "p2", "p2-canje", false)
	admin := uuidAleatorio(t)
	edgeAltaAdminDelTenant(t, esc.DB, admin, esc.Tenant)
	esc.TokenAdmin = canjear(t, esc.S, esc.S.Identidad.TokenDe(admin, "wapp.bff"))

	t.Run("sin membresías: token sin empresa y 403 en el negocio", func(t *testing.T) { p2NoMembership(t, esc) })
	t.Run("la administradora canjea con su empresa", func(t *testing.T) { p2AdminExchange(t, esc, admin) })
	t.Run("dos empresas: no elige por él hasta que elige", func(t *testing.T) { p2TwoTenants(t, esc) })
	t.Run("RBAC de roles y grants", func(t *testing.T) { p2RBAC(t, esc, admin) })
	t.Run("nombres de rol adversarios", func(t *testing.T) { p2AdversarialRoleNames(t, esc) })
	t.Run("patrones de grant adversarios", func(t *testing.T) { p2AdversarialGrantPatterns(t, esc) })
	t.Run("alta de miembro sin identity: 503, nunca 404", func(t *testing.T) { p2AddMemberWithoutIdentity(t, esc, admin) })
	t.Run("canjes rechazados", func(t *testing.T) { p2RejectedExchanges(t, esc) })

	// El alta de miembro sin credencial M2M no deja línea ERROR: se contesta 503 y nada más.
	edgeSinErrores(t, esc.S, nil)
}

// p2Grants devuelve la lista clave («allow» o «deny») de los grants de un token; nil si el token no
// los trae con esa forma (que falten es un valor legítimo: es lo que se afirma del token sin empresa).
func p2Grants(claims map[string]any, clave string) []any {
	if grants, ok := claims["grants"].(map[string]any); ok {
		if lista, ok := grants[clave].([]any); ok {
			return lista
		}
	}
	return nil
}

// p2NoMembership es el paso 1: quien no pertenece a ninguna empresa canjea y recibe un token sin
// empresa, sin roles y sin un solo grant; se puede mirar (whoami) y listar sus empresas (ninguna),
// pero toda ruta de negocio le contesta 403 y el 403 no deja rastro en la bitácora.
func p2NoMembership(t *testing.T, esc edgeEscenario) {
	usuario := uuidAleatorio(t)
	res := p2Exchange(t, esc.S, usuario)
	p2WantContext(t, "canje sin membresías", res, "")
	claims := segmentoJWT(t, res.ContextToken, 1)
	if tenant, hay := claims["tenant_id"]; hay && tenant != "" {
		t.Errorf("el token sin empresa trae tenant_id=%v", tenant)
	}
	for _, clave := range []string{"allow", "deny"} {
		if lista := p2Grants(claims, clave); len(lista) != 0 {
			t.Errorf("el token sin empresa trae grants.%s=%v, quería ninguno", clave, lista)
		}
	}
	p2WantAudit(t, esc.DB, 1, usuario, p2AuditExchange, "auth", "ok", "")

	pub := esc.S.Publica(res.ContextToken)
	var yo struct {
		TenantID string   `json:"tenant_id"`
		Subject  string   `json:"subject"`
		Roles    []string `json:"roles"`
	}
	r := pub.Get(t, p2RouteWhoAmI, nil)
	p2WantCode(t, "whoami sin empresa", r, http.StatusOK)
	r.JSON(t, &yo)
	if yo.TenantID != "" || yo.Subject != usuario || len(yo.Roles) != 0 {
		t.Errorf("whoami sin empresa: %+v, quería subject=%s sin empresa ni roles", yo, usuario)
	}
	if empresas := p2TenantsOf(t, esc.S, res.ContextToken); len(empresas) != 0 {
		t.Errorf("auth/tenants sin membresías: %v, quería la lista vacía", empresas)
	}

	for _, ruta := range []string{p2RouteRoles, p2RouteMembers, p2RouteInvitations} {
		p2WantCode(t, "GET "+ruta+" sin empresa", pub.Get(t, ruta, nil), http.StatusForbidden)
	}
	p2WantCode(t, "POST roles sin empresa", pub.Post(t, p2RouteRoles, map[string]string{"name": "no-debe-existir"}), http.StatusForbidden)
	p2WantCode(t, "POST invitations sin empresa", pub.Post(t, p2RouteInvitations, nil), http.StatusForbidden)
	if n := consultaEntero(t, esc.DB, `SELECT count(*) FROM public.iam_roles WHERE name = 'no-debe-existir'`); n != 0 {
		t.Errorf("el 403 dejó %d rol(es) creados", n)
	}
	// Un 403 de middleware no llega a la bitácora: del usuario solo queda su canje.
	if n := consultaEntero(t, esc.DB, `SELECT count(*) FROM public.audit_events WHERE actor = $1`, usuario); n != 1 {
		t.Errorf("audit_events del usuario sin empresa: %d filas, quería 1 (solo el canje)", n)
	}
	p2WantCode(t, "whoami sin token", esc.S.Publica("").Get(t, p2RouteWhoAmI, nil), http.StatusUnauthorized)
}

// p2AdminExchange es el paso 2: la administradora de la empresa que creó el staff canjea y su token
// lleva esa empresa y el rol tenant_admin; whoami y auth/tenants lo dicen por la puerta.
func p2AdminExchange(t *testing.T, esc edgeEscenario, admin string) {
	res := p2Exchange(t, esc.S, admin)
	p2WantContext(t, "canje de la administradora", res, esc.Tenant, "tenant_admin")
	claims := segmentoJWT(t, res.ContextToken, 1)
	if claims["tenant_id"] != esc.Tenant || claims["sub"] != admin {
		t.Errorf("claims del token de la administradora: tenant_id=%v sub=%v", claims["tenant_id"], claims["sub"])
	}
	// Los grants efectivos viajan en el token: el comodín de tenant_admin y el deny de lo «.any»
	// (la pieza de I-CP-5). Es también lo que da sentido al «sin un solo grant» del paso 1.
	permitidos, negados := p2Grants(claims, "allow"), p2Grants(claims, "deny")
	if !slices.Contains(permitidos, any("*")) || !slices.Contains(negados, any("*.any")) {
		t.Errorf("grants del token de la administradora: allow=%v deny=%v, quería «*» permitido y «*.any» negado", permitidos, negados)
	}
	// Dos canjes de la administradora hasta aquí: el del escenario y este.
	p2WantAudit(t, esc.DB, 2, admin, p2AuditExchange, "auth", "ok", esc.Tenant)

	var yo struct {
		TenantID string   `json:"tenant_id"`
		Subject  string   `json:"subject"`
		Roles    []string `json:"roles"`
	}
	r := esc.S.Publica(res.ContextToken).Get(t, p2RouteWhoAmI, nil)
	p2WantCode(t, "whoami de la administradora", r, http.StatusOK)
	r.JSON(t, &yo)
	if yo.TenantID != esc.Tenant || yo.Subject != admin || !slices.Equal(yo.Roles, []string{"tenant_admin"}) {
		t.Errorf("whoami de la administradora: %+v", yo)
	}
	empresas := p2TenantsOf(t, esc.S, res.ContextToken)
	// Con una sola empresa no hay fila en user_active_tenant y aun así la lista la marca: «activa»
	// es la empresa con la que saldría el canje, no la fila de la elección.
	if e, ok := empresas[esc.Tenant]; len(empresas) != 1 || !ok || e.DisplayName != "Procesos p2-canje" || !e.Active {
		t.Errorf("auth/tenants de la administradora: %v, quería solo %s («Procesos p2-canje», marcada)", empresas, esc.Tenant)
	}
	if n := consultaEntero(t, esc.DB, `SELECT count(*) FROM public.user_active_tenant WHERE user_id = $1::uuid`, admin); n != 0 {
		t.Errorf("user_active_tenant de la administradora: %d filas, quería 0 (no eligió nada)", n)
	}
	p2WantCode(t, "GET roles de la administradora", esc.S.Publica(res.ContextToken).Get(t, p2RouteRoles, nil), http.StatusOK)
	p2WantCode(t, "GET members de la administradora", esc.S.Publica(res.ContextToken).Get(t, p2RouteMembers, nil), http.StatusOK)
}

// p2TwoTenants es el paso 4: quien pertenece a dos empresas y no eligió recibe un token SIN empresa
// (el canje no elige por él); elegir una de las suyas la deja en user_active_tenant y el siguiente
// canje sale con ella y con SUS roles en ella; pedir una de la que no es miembro es 404 y no
// escribe nada.
func p2TwoTenants(t *testing.T, esc edgeEscenario) {
	segunda := crearTenant(t, esc.S, esc.TokenStaff, "p2-segunda")
	ajena := crearTenant(t, esc.S, esc.TokenStaff, "p2-ajena")
	usuario := uuidAleatorio(t)
	p2AddMember(t, esc.DB, usuario, esc.Tenant, p2RoleViewer)
	p2AddMember(t, esc.DB, usuario, segunda, p2RoleOperator)
	const activa = `SELECT tenant_id::text FROM public.user_active_tenant WHERE user_id = $1::uuid`

	res := p2Exchange(t, esc.S, usuario)
	p2WantContext(t, "canje con dos empresas y sin activa", res, "")
	p2WantAudit(t, esc.DB, 1, usuario, p2AuditExchange, "auth", "ok", "")
	pub := esc.S.Publica(res.ContextToken)
	p2WantCode(t, "GET roles con dos empresas y sin activa", pub.Get(t, p2RouteRoles, nil), http.StatusForbidden)
	empresas := p2TenantsOf(t, esc.S, res.ContextToken)
	if len(empresas) != 2 || empresas[esc.Tenant].Active || empresas[segunda].Active || empresas[segunda].DisplayName != "Procesos p2-segunda" {
		t.Errorf("auth/tenants con dos empresas: %v, quería las dos sin marcar", empresas)
	}

	elegir := func(tenant any) respuesta {
		return pub.Post(t, p2RouteActiveTenant, map[string]any{"tenant_id": tenant})
	}
	p2WantCode(t, "elegir una empresa de la que no es miembro", elegir(ajena), http.StatusNotFound)
	p2WantCode(t, "elegir una empresa que no existe", elegir(uuidAleatorio(t)), http.StatusNotFound)
	p2WantCode(t, "elegir sin decir cuál", elegir(""), http.StatusBadRequest)
	p2WantCode(t, "elegir sin token", esc.S.Publica("").Post(t, p2RouteActiveTenant, map[string]string{"tenant_id": segunda}), http.StatusUnauthorized)
	if fila := p2Text(t, esc.DB, activa, usuario); fila != "" {
		t.Errorf("los rechazos dejaron la empresa activa %q", fila)
	}

	p2WantCode(t, "elegir la segunda empresa", elegir(segunda), http.StatusNoContent)
	if fila := p2Text(t, esc.DB, activa, usuario); fila != segunda {
		t.Errorf("user_active_tenant = %q, quería %s", fila, segunda)
	}
	res = p2Exchange(t, esc.S, usuario)
	p2WantContext(t, "canje tras elegir la segunda", res, segunda, "operator")
	if empresas = p2TenantsOf(t, esc.S, res.ContextToken); !empresas[segunda].Active || empresas[esc.Tenant].Active {
		t.Errorf("auth/tenants tras elegir la segunda: %v, quería marcada solo %s", empresas, segunda)
	}

	// Cambiar de empresa reemplaza la fila (una por usuario) y el canje sale con los roles de la otra.
	p2WantCode(t, "volver a la primera empresa", esc.S.Publica(res.ContextToken).Post(t, p2RouteActiveTenant, map[string]string{"tenant_id": esc.Tenant}), http.StatusNoContent)
	if n := consultaEntero(t, esc.DB, `SELECT count(*) FROM public.user_active_tenant WHERE user_id = $1::uuid`, usuario); n != 1 {
		t.Errorf("user_active_tenant tiene %d filas del usuario, quería 1", n)
	}
	p2WantContext(t, "canje tras volver a la primera", p2Exchange(t, esc.S, usuario), esc.Tenant, "viewer")
	// La elección no se audita con el middleware (quien elige puede no traer empresa): del usuario
	// solo hay canjes en la bitácora.
	if n := consultaEntero(t, esc.DB, `SELECT count(*) FROM public.audit_events WHERE actor = $1 AND action <> $2`, usuario, p2AuditExchange); n != 0 {
		t.Errorf("audit_events del usuario de dos empresas: %d filas que no son canjes, quería 0", n)
	}
}

// p2RBAC es el paso 5: un viewer no crea roles (403, sin fila y sin auditoría); la administradora
// sí (201, fila de la empresa y auditoría); un grant por usuario se guarda en iam_user_grants y
// SURTE EFECTO en el siguiente canje de esa persona; y los grants de un rol propio se guardan en
// iam_role_grants, mientras que una plantilla global no se toca desde una empresa.
func p2RBAC(t *testing.T, esc edgeEscenario, admin string) {
	viewer := uuidAleatorio(t)
	p2AddMember(t, esc.DB, viewer, esc.Tenant, p2RoleViewer)
	tokenViewer := p2Exchange(t, esc.S, viewer).ContextToken
	adm := esc.S.Publica(esc.TokenAdmin)
	const rolPorNombre = `SELECT id::text FROM public.iam_roles WHERE tenant_id = $1::uuid AND name = $2`

	p2WantCode(t, "viewer crea un rol", esc.S.Publica(tokenViewer).Post(t, p2RouteRoles, map[string]string{"name": "cajera"}), http.StatusForbidden)
	p2WantCode(t, "viewer lee los roles", esc.S.Publica(tokenViewer).Get(t, p2RouteRoles, nil), http.StatusOK)
	if id := p2Text(t, esc.DB, rolPorNombre, esc.Tenant, "cajera"); id != "" {
		t.Errorf("el 403 del viewer dejó el rol %s", id)
	}
	p2WantAudit(t, esc.DB, 0, viewer, "roles.write", "role", "failure", esc.Tenant)

	r := adm.Post(t, p2RouteRoles, map[string]string{"name": "cajera"})
	p2WantCode(t, "la administradora crea un rol", r, http.StatusCreated)
	var rol struct {
		RoleID   string `json:"role_id"`
		Name     string `json:"name"`
		TenantID string `json:"tenant_id"`
		Global   bool   `json:"global"`
	}
	r.JSON(t, &rol)
	if rol.Name != "cajera" || rol.TenantID != esc.Tenant || rol.Global || rol.RoleID != p2Text(t, esc.DB, rolPorNombre, esc.Tenant, "cajera") {
		t.Errorf("rol creado: %+v, quería «cajera» de la empresa %s y el id de su fila en iam_roles", rol, esc.Tenant)
	}
	p2WantAudit(t, esc.DB, 1, admin, "roles.write", "role", "success", esc.Tenant)
	p2WantCode(t, "repetir el nombre del rol", adm.Post(t, p2RouteRoles, map[string]string{"name": "cajera"}), http.StatusConflict)
	p2WantAudit(t, esc.DB, 1, admin, "roles.write", "role", "failure", esc.Tenant)

	// Grants del rol: en el propio se guardan; una plantilla global no se modifica desde una empresa.
	concesion := map[string]string{"pattern": "members.read", "effect": "allow"}
	p2WantCode(t, "grant a un rol propio", adm.Post(t, p2RouteRoles+"/"+rol.RoleID+"/grants", concesion), http.StatusNoContent)
	if n := consultaEntero(t, esc.DB, `SELECT count(*) FROM public.iam_role_grants WHERE role_id = $1::uuid AND pattern = 'members.read' AND effect = 'allow'`, rol.RoleID); n != 1 {
		t.Errorf("iam_role_grants del rol propio: %d filas, quería 1", n)
	}
	antes := consultaEntero(t, esc.DB, `SELECT count(*) FROM public.iam_role_grants WHERE role_id = $1::uuid`, p2RoleViewer)
	p2WantCode(t, "grant a una plantilla global", adm.Post(t, p2RouteRoles+"/"+p2RoleViewer+"/grants", concesion), http.StatusUnprocessableEntity)
	if n := consultaEntero(t, esc.DB, `SELECT count(*) FROM public.iam_role_grants WHERE role_id = $1::uuid`, p2RoleViewer); n != antes {
		t.Errorf("la plantilla viewer pasó de %d a %d grants", antes, n)
	}

	// Grant por usuario: solo a un miembro de la empresa, y con un efecto que exista.
	rutaGrants := p2RouteMembers + "/" + viewer + "/grants"
	permiso := map[string]string{"pattern": "roles.write", "effect": "allow"}
	p2WantCode(t, "viewer se concede un grant", esc.S.Publica(tokenViewer).Post(t, rutaGrants, permiso), http.StatusForbidden)
	p2WantCode(t, "grant a quien no es miembro", adm.Post(t, p2RouteMembers+"/"+uuidAleatorio(t)+"/grants", permiso), http.StatusNotFound)
	p2WantCode(t, "grant con un efecto que no existe", adm.Post(t, rutaGrants, map[string]string{"pattern": "roles.write", "effect": "quizá"}), http.StatusBadRequest)
	const grantsDe = `SELECT count(*) FROM public.iam_user_grants WHERE user_id = $1::uuid`
	if n := consultaEntero(t, esc.DB, grantsDe, viewer); n != 0 {
		t.Errorf("los rechazos dejaron %d grant(s) al viewer", n)
	}
	p2WantCode(t, "grant por usuario", adm.Post(t, rutaGrants, permiso), http.StatusNoContent)
	if n := consultaEntero(t, esc.DB, grantsDe+` AND pattern = 'roles.write' AND effect = 'allow'`, viewer); n != 1 {
		t.Errorf("iam_user_grants del viewer: %d filas con roles.write/allow, quería 1", n)
	}
	p2WantAudit(t, esc.DB, 1, admin, "roles.write", "user_grant", "success", esc.Tenant)
	p2WantAudit(t, esc.DB, 2, admin, "roles.write", "user_grant", "failure", esc.Tenant)

	// El token viejo del viewer sigue sin el permiso; el del siguiente canje ya lo trae.
	p2WantCode(t, "viewer crea un rol con su token de antes del grant", esc.S.Publica(tokenViewer).Post(t, p2RouteRoles, map[string]string{"name": "repartidora"}), http.StatusForbidden)
	nuevo := p2Exchange(t, esc.S, viewer)
	p2WantContext(t, "canje del viewer tras el grant", nuevo, esc.Tenant, "viewer")
	p2WantCode(t, "viewer crea un rol tras el grant", esc.S.Publica(nuevo.ContextToken).Post(t, p2RouteRoles, map[string]string{"name": "repartidora"}), http.StatusCreated)
	p2WantAudit(t, esc.DB, 1, viewer, "roles.write", "role", "success", esc.Tenant)
}

// p2AddMemberWithoutIdentity es el paso 7: sin credencial M2M de identity, el alta de un miembro
// contesta 503 «identity_no_configurado» (la ruta EXISTE: su GET hermano da 200), no escribe la
// membresía y queda auditada como fallo.
func p2AddMemberWithoutIdentity(t *testing.T, esc edgeEscenario, admin string) {
	adm := esc.S.Publica(esc.TokenAdmin)
	nuevo := uuidAleatorio(t)
	r := adm.Post(t, p2RouteMembers, map[string]string{"user_id": nuevo})
	p2WantCode(t, "alta de miembro sin identity", r, http.StatusServiceUnavailable)
	if msg := p2ErrorOf(r); msg != "identity_no_configurado" {
		t.Errorf("alta de miembro sin identity: error %q, quería identity_no_configurado", msg)
	}
	if n := consultaEntero(t, esc.DB, `SELECT count(*) FROM public.tenant_members WHERE user_id = $1::uuid`, nuevo); n != 0 {
		t.Errorf("el 503 dejó %d membresía(s)", n)
	}
	p2WantAudit(t, esc.DB, 1, admin, "members.write", "member", "failure", esc.Tenant)
	if meta := p2Text(t, esc.DB, `SELECT meta->>'status' FROM public.audit_events WHERE actor = $1 AND action = 'members.write' AND resource = 'member'`, admin); meta != "503" {
		t.Errorf("meta.status de la auditoría del alta: %q, quería 503", meta)
	}
	p2WantCode(t, "alta de miembro sin user_id", adm.Post(t, p2RouteMembers, map[string]string{"user_id": " "}), http.StatusBadRequest)
	p2WantCode(t, "GET members (la ruta existe)", adm.Get(t, p2RouteMembers, nil), http.StatusOK)
}

// p2RejectedExchanges son los negativos del canje: caducado, de otro emisor y de un system que wApp
// no acepta se rechazan con 401 antes de saber quién es (no dejan fila en la bitácora); un token
// bueno al que le queda menos de un minuto se rechaza con 401 y SÍ queda anotado como «error».
func p2RejectedExchanges(t *testing.T, esc edgeEscenario) {
	id := esc.S.Identidad
	usuario := uuidAleatorio(t)
	p2AddMember(t, esc.DB, usuario, esc.Tenant, p2RoleViewer)
	for nombre, token := range map[string]string{
		"caducado":                id.TokenCaducado(usuario, "wapp.bff"),
		"de otro emisor":          id.TokenDeOtroEmisor(usuario, "wapp.bff"),
		"de un system no wApp":    id.TokenDe(usuario, "edugo.kmp"),
		"de un system con @@":     id.TokenDe(usuario, "wapp@@bff"),
		"de un system con U+00A0": id.TokenDe(usuario, "wapp.bff "),
	} {
		r := canje(t, esc.S, token)
		p2WantCode(t, "canje de un token "+nombre, r, http.StatusUnauthorized)
		if msg := p2ErrorOf(r); msg != "identity token inválido" {
			t.Errorf("canje de un token %s: error %q, quería «identity token inválido»", nombre, msg)
		}
	}
	if n := consultaEntero(t, esc.DB, `SELECT count(*) FROM public.audit_events WHERE actor = $1`, usuario); n != 0 {
		t.Errorf("los canjes rechazados antes de identificar dejaron %d fila(s) de auditoría", n)
	}

	// Firma, emisor y system buenos, pero con 20 s de vida: menos que el mínimo de un Context Token.
	ahora := time.Now()
	apurado := id.firmarES256(map[string]any{
		"iss": identidadEmisor, "sub": usuario, "aud": []string{"wapp.bff"}, "system": "wapp.bff",
		"email": usuario + "@procesos.test", "token_use": "identity", "token_version": 0,
		"jti": uuidAleatorio(t), "iat": ahora.Add(-time.Minute).Unix(), "nbf": ahora.Add(-time.Minute).Unix(),
		"exp": ahora.Add(20 * time.Second).Unix(),
	})
	r := canje(t, esc.S, apurado)
	p2WantCode(t, "canje de un token al que le quedan 20 s", r, http.StatusUnauthorized)
	p2WantAudit(t, esc.DB, 1, usuario, p2AuditExchange, "auth", "error", esc.Tenant)
	p2WantAudit(t, esc.DB, 0, usuario, p2AuditExchange, "auth", "ok", esc.Tenant)
}
