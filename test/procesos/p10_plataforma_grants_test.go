//go:build integracion

package procesos

import (
	"database/sql"
	"net/http"
	"strings"
	"testing"
)

// Los grants que siembran las migraciones (0015, 0030, 0040, 0042, 0057, 0059, 0060, 0084): la
// parte de P10 que re-expresa migrations/grants_integration_test.go. El test viejo leía las filas
// de iam_role_grants y las pasaba por el evaluador de identity-shared; aquí la fila se mira igual
// (p10CheckGrantSeeds) y la CONDUCTA se mira por la puerta: el servidor resuelve los grants en el
// canje y su middleware de permisos decide, que es el evaluador de producción con el seed real.
//
// Un 403 del middleware de permisos se distingue de cualquier otro 403 por lo que deja: nada. El
// permiso se comprueba ANTES de la auditoría (hallazgo 38 de F9), así que el rechazo por permiso no
// escribe fila en audit_events.

// p10Grant es una fila de public.iam_role_grants que una migración siembra.
type p10Grant struct{ role, pattern, effect, origin string }

// Los scopes de los dos planos, como los nombra grants_integration_test.go.
var (
	// p10RolePlane son los cuatro scopes del plano de roles de la empresa (0084).
	p10RolePlane = []string{"roles.read", "roles.write", "members.read", "members.write"}
	// p10PlatformPlane son los siete del plano de plataforma: dos del corte comercial (0059) y
	// cinco de la consola (0060).
	p10PlatformPlane = []string{
		"tenants.revoke.any", "tenants.restore.any",
		"tenants.read.any", "tenants.create.any", "fleet.read.any", "users.provision.any", "enrollment.issue.any",
	}
)

// p10SeededGrants devuelve las filas que el seed tiene que traer, cada una con su migración.
func p10SeededGrants() []p10Grant {
	out := make([]p10Grant, 0, 7+len(p10PlatformPlane)+len(p10RolePlane))
	out = append(out,
		// 0015: los globs de las plantillas. El «*» de tenant_admin y el «*.read» de viewer.
		p10Grant{edgeRolTenantAdmin, "*", "allow", "0015"},
		p10Grant{p2RoleViewer, "*.read", "allow", "0015"},
		// Un scope nuevo no lo tiene nadie hasta que una migración se lo concede al rol operator:
		// sin la fila, la ruta queda montada y contestando 403 a quien la necesita.
		p10Grant{p2RoleOperator, "sessions.read", "allow", "0030"},
		p10Grant{p2RoleOperator, "entitlements.read", "allow", "0040"},
		p10Grant{p2RoleOperator, "intakes.read", "allow", "0042"},
		p10Grant{p2RoleOperator, "events_telemetry.read", "allow", "0057"},
		// 0059: el deny por FORMA que saca al «*» de tenant_admin del plano de plataforma.
		p10Grant{edgeRolTenantAdmin, "*.any", "deny", "0059"},
	)
	for _, scope := range p10PlatformPlane {
		out = append(out, p10Grant{rolPlataformaID, scope, "allow", "0059/0060"})
	}
	// 0084: el deny POR NOMBRE que saca a platform_admin del plano de roles de una empresa.
	for _, scope := range p10RolePlane {
		out = append(out, p10Grant{rolPlataformaID, scope, "deny", "0084"})
	}
	return out
}

// p10CheckGrantSeeds mira el SEED real, fila a fila: cada grant de p10SeededGrants está una vez;
// platform_admin no tiene más grants que sus siete allow y sus cuatro deny, y tenant_admin sus dos;
// operator no tiene ninguna fila de los cuatro scopes del plano de roles; y la empresa operadora
// existe con el id fijo que la plataforma trae por defecto y el slug «wapp-platform». Solo necesita
// la base: lo llaman el proceso del servidor y, tras cada réplica, el de las migraciones.
func p10CheckGrantSeeds(t *testing.T, db *sql.DB) {
	t.Helper()
	const one = `SELECT count(*) FROM public.iam_role_grants WHERE role_id = $1::uuid AND pattern = $2 AND effect = $3`
	for _, g := range p10SeededGrants() {
		if n := consultaEntero(t, db, one, g.role, g.pattern, g.effect); n != 1 {
			t.Errorf("iam_role_grants: %d filas (%s, %q, %s), quería 1 (migración %s)", n, g.role, g.pattern, g.effect, g.origin)
		}
	}
	const total = `SELECT count(*) FROM public.iam_role_grants WHERE role_id = $1::uuid`
	for role, want := range map[string]int{rolPlataformaID: len(p10PlatformPlane) + len(p10RolePlane), edgeRolTenantAdmin: 2} {
		if n := consultaEntero(t, db, total, role); n != want {
			t.Errorf("iam_role_grants: el rol %s tiene %d grants, quería %d: su perímetro cambió", role, n, want)
		}
	}
	// operator queda fuera del plano de roles A PROPÓSITO (0084): quien asigna roles se asigna
	// tenant_admin. Ni la fila exacta ni un glob que la cubra.
	if n := consultaEntero(t, db, total+` AND effect = 'allow'
		AND pattern IN ('*', '*.read', '*.write', 'roles.*', 'members.*', 'roles.read', 'roles.write', 'members.read', 'members.write')`, p2RoleOperator); n != 0 {
		t.Errorf("iam_role_grants: operator tiene %d grants que alcanzan el plano de roles, quería 0", n)
	}
	if slug := p2Text(t, db, `SELECT slug FROM public.tenants WHERE id = $1::uuid`, tenantPlataformaID); slug != "wapp-platform" {
		t.Errorf("la empresa operadora %s tiene slug %q, quería «wapp-platform» (0059)", tenantPlataformaID, slug)
	}
}

// p10Access es una petición por una puerta y el código que el binario viejo contesta.
type p10Access struct {
	who    string // quién llama, para el mensaje
	token  string
	admin  bool // true = listener admin (:8100); false = API pública (:8103)
	method string
	path   string
	body   any
	want   int
}

// checkAccess hace cada petición de la tabla y exige su código. Un 429 se reintenta (hallazgo 32).
func (w *p10World) checkAccess(t *testing.T, table []p10Access) {
	t.Helper()
	for _, a := range table {
		client := w.sc.S.Publica(a.token)
		if a.admin {
			client = w.sc.S.Admin(a.token)
		}
		r := w.calls.call(t, p9Caller{client: client}, "", a.method, a.path, a.body)
		if r.Codigo != a.want {
			t.Errorf("%s · %s %s: HTTP %d, quería %d\ncuerpo: %s", a.who, a.method, a.path, r.Codigo, a.want, recortar(r.Cuerpo))
		}
	}
}

// rolePlaneRequests son las cuatro peticiones del plano de roles, una por scope, con el código que
// recibe quien SÍ tiene el permiso.
func (w *p10World) rolePlaneRequests(who, token string, codes [4]int) []p10Access {
	return []p10Access{
		{who, token, false, http.MethodGet, p2RouteRoles, nil, codes[0]},
		{who, token, false, http.MethodPost, p2RouteRoles, map[string]string{"name": "p10-" + who}, codes[1]},
		{who, token, false, http.MethodGet, p2RouteMembers, nil, codes[2]},
		{who, token, false, http.MethodPost, p2RouteMembers, map[string]string{"user_id": w.viewer.ID}, codes[3]},
	}
}

// grantsRolePlane: los roles de la EMPRESA frente al plano de roles (0084) y los scopes de lectura
// del operator. tenant_admin alcanza los cuatro por su «*» —el alta de miembro contesta 503 porque
// el arnés no levanta identity, que no es un 403—; viewer lee por «*.read» y no escribe; operator
// no alcanza ninguno, y sí las cuatro lecturas que las migraciones le concedieron una a una.
func (w *p10World) grantsRolePlane(t *testing.T) {
	const denied = http.StatusForbidden
	w.checkAccess(t, w.rolePlaneRequests("tenant_admin", w.sc.TokenAdmin,
		[4]int{http.StatusOK, http.StatusCreated, http.StatusOK, http.StatusServiceUnavailable}))
	w.checkAccess(t, w.rolePlaneRequests("viewer", w.viewer.Token, [4]int{http.StatusOK, denied, http.StatusOK, denied}))
	w.checkAccess(t, w.rolePlaneRequests("operator", w.operator.Token, [4]int{denied, denied, denied, denied}))

	op := w.operator.Token
	w.checkAccess(t, []p10Access{
		{"operator", op, false, http.MethodGet, "/api/v1/sessions", nil, http.StatusOK},
		{"operator", op, false, http.MethodGet, "/api/v1/entitlements", nil, http.StatusOK},
		{"operator", op, false, http.MethodGet, "/api/v1/intakes", nil, http.StatusOK},
		{"operator", op, false, http.MethodGet, "/api/v1/events/telemetry", nil, http.StatusOK},
	})

	// Solo la administradora dejó un rol; los 403 no escribieron nada ni se auditaron.
	if n := consultaEntero(t, w.sc.DB, `SELECT count(*) FROM public.iam_roles WHERE tenant_id = $1::uuid`, w.sc.Tenant); n != 1 {
		t.Errorf("iam_roles de la empresa: %d filas, quería 1 (la de la administradora)", n)
	}
	w.wantNoRolePlaneAudit(t, "viewer u operator", `actor = ANY($2::text[]) AND result = 'failure'`,
		"{"+w.viewer.ID+","+w.operator.ID+"}")
}

// wantNoRolePlaneAudit exige que no haya filas de audit_events de los cuatro scopes del plano de
// roles que cumplan el predicado ($1 son los scopes; $2, el argumento del predicado).
func (w *p10World) wantNoRolePlaneAudit(t *testing.T, who, predicate, arg string) {
	t.Helper()
	query := `SELECT count(*) FROM public.audit_events WHERE action = ANY($1::text[]) AND ` + predicate
	if n := consultaEntero(t, w.sc.DB, query, "{"+strings.Join(p10RolePlane, ",")+"}", arg); n != 0 {
		t.Errorf("audit_events tiene %d filas del plano de roles de %s: el 403 no lo cortó el middleware de permisos", n, who)
	}
}

// grantsPerimeters es la cerca INV-10 en sus cuatro cruces, contra el seed real:
//
//	(1) tenant_admin   → plano de PLATAFORMA = 403   (2) platform_admin → plano de EMPRESA    = 403
//	(3) tenant_admin   → plano de EMPRESA    = pasa  (4) platform_admin → plano de PLATAFORMA = pasa
//
// Los dos que pasan no son decoración: son lo que impide «arreglar» un cruce amputando un plano.
func (w *p10World) grantsPerimeters(t *testing.T) {
	routes := p2PlatformRoutes()
	request := uuidAleatorio(t) // una solicitud de acceso que no existe
	target := func(r p2PlatformRoute) string {
		return strings.NewReplacer("{tenant}", w.otherTenant, "{request}", request).Replace(r.Ruta)
	}

	// (1) y (4): las diez rutas «.any» sobre OTRA empresa (así el corte comercial y su restauración
	// no tocan al Edge del proceso). El orden de la tabla deja a esa empresa como estaba.
	scopes := map[string]bool{}
	for _, r := range routes {
		scopes[r.Permiso] = true
		w.checkAccess(t, []p10Access{{"tenant_admin", w.sc.TokenAdmin, true, r.Metodo, target(r), r.Cuerpo(w.otherTenant), http.StatusForbidden}})
		got := w.calls.call(t, w.staff, "", r.Metodo, target(r), r.Cuerpo(w.otherTenant))
		if got.Codigo == http.StatusForbidden || got.Codigo == http.StatusUnauthorized {
			t.Errorf("CRUCE 4 ROTO: platform_admin · %s %s: HTTP %d", r.Metodo, r.Ruta, got.Codigo)
		}
	}
	for _, scope := range p10PlatformPlane {
		if !scopes[scope] {
			t.Errorf("ninguna ruta de la tabla pide %q: el cruce no se midió para ese scope", scope)
		}
	}
	if revoked := p2Text(t, w.sc.DB, `SELECT (revoked_at IS NOT NULL)::text FROM public.tenants WHERE id = $1::uuid`, w.otherTenant); revoked != "false" {
		t.Errorf("tras revocar y restaurar, la otra empresa quedó con revoked_at IS NOT NULL = %s", revoked)
	}

	// (2) El staff de plataforma no entra en la administración de una empresa.
	const denied = http.StatusForbidden
	w.checkAccess(t, w.rolePlaneRequests("platform_admin", w.sc.TokenStaff, [4]int{denied, denied, denied, denied}))
	w.wantNoRolePlaneAudit(t, "el staff de plataforma", `tenant_id = $2::uuid`, tenantPlataformaID)

	// (3) La administradora sigue en lo suyo: el deny «*.any» es por forma y no se llevó por
	// delante ni el plano de roles (grants_role_plane) ni lo de siempre. Aquí basta con que el
	// permiso deje pasar: cada petición llega a su handler, que la rechaza por el cuerpo.
	admin := w.sc.TokenAdmin
	w.checkAccess(t, []p10Access{
		{"tenant_admin · leases.revoke", admin, true, http.MethodPost, "/admin/leases/revoke", map[string]string{}, http.StatusBadRequest},
		{"tenant_admin · flows.create", admin, false, http.MethodPost, "/api/v1/flows", map[string]string{}, http.StatusBadRequest},
		{"tenant_admin · messages.send", admin, false, http.MethodPost, "/api/v1/messages", map[string]string{}, http.StatusBadRequest},
		{"tenant_admin · intakes.write", admin, false, http.MethodPost, "/api/v1/intakes/discard", map[string]string{}, http.StatusBadRequest},
	})
}

// grantsDenyRowsHold es el bloque (2b) del test viejo: el cruce 2 no se sostiene solo en que
// platform_admin no tenga ningún allow que case. Se le añade a su rol el «*» que un día podría
// ganar la consola y se canjea de nuevo: con las cuatro filas de deny de la 0084, el plano de roles
// sigue cerrado, mientras que una lectura cualquiera de empresa —que antes del «*» era 403— se
// abre. El grant se retira al terminar; los tokens ya emitidos no cambian.
func (w *p10World) grantsDenyRowsHold(t *testing.T) {
	const denied = http.StatusForbidden
	staff := uuidAleatorio(t)
	altaStaffPlataforma(t, w.sc.DB, staff)
	before := p2Exchange(t, w.sc.S, staff).ContextToken
	w.checkAccess(t, []p10Access{{"platform_admin", before, false, http.MethodGet, "/api/v1/entitlements", nil, denied}})

	if n := p10Exec(t, w.sc.DB, `INSERT INTO public.iam_role_grants (role_id, pattern, effect) VALUES ($1::uuid, '*', 'allow')`, rolPlataformaID); n != 1 {
		t.Fatalf("añadir el «*» a platform_admin tocó %d filas, quería 1", n)
	}
	defer func() {
		if n := p10Exec(t, w.sc.DB, `DELETE FROM public.iam_role_grants WHERE role_id = $1::uuid AND pattern = '*'`, rolPlataformaID); n != 1 {
			t.Errorf("retirar el «*» de platform_admin tocó %d filas, quería 1", n)
		}
		p10CheckGrantSeeds(t, w.sc.DB)
	}()

	wide := p2Exchange(t, w.sc.S, staff).ContextToken
	w.checkAccess(t, []p10Access{{"platform_admin con «*»", wide, false, http.MethodGet, "/api/v1/entitlements", nil, http.StatusOK}})
	w.checkAccess(t, w.rolePlaneRequests("platform_admin con «*»", wide, [4]int{denied, denied, denied, denied}))
	// El token de ANTES del grant sigue sin el permiso: los grants viajan en el token.
	w.checkAccess(t, []p10Access{{"platform_admin, token previo", before, false, http.MethodGet, "/api/v1/entitlements", nil, denied}})
	w.wantNoRolePlaneAudit(t, "el staff con «*»", `actor = $2`, staff)
}
