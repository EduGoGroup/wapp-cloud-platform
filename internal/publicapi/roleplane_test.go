package publicapi_test

// roleplane_test.go — CONTRACT TESTS DE LA PUERTA AL PLANO DE ROLES
// (Plan 047 · Ola 1.0 · T1.0-4).
//
// Corren contra el registro REAL (publicapi.Register) y contra los usecases
// REALES sobre los dobles en memoria de infra/memory. No hay ni un fake de
// in.RoleAdmin: un doble del propio puerto que devolviera lo que le pidan
// probaría el mapeo de este fichero contra sí mismo, y lo que hay que demostrar
// —que un recurso ajeno da 404— nace DENTRO del usecase (visibleRole /
// requireMember) y solo cuenta si llega vivo hasta el cable.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/infra/memory"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
	iamusecase "github.com/EduGoGroup/wapp-cloud-platform/internal/iam/usecase"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
)

const (
	// keyARoles administra el plano de roles de la empresa A. Lleva los CUATRO
	// scopes a propósito: si le faltara alguno, el 404 cross-tenant que este
	// fichero exige podría estar saliendo de un 403 del middleware y el test
	// quedaría vacío sin que nadie lo note.
	keyARoles = "key-a-roles"
	// keyBRoles es su gemela en la empresa B: sirve para demostrar que lo que a
	// A le da 404 a B le funciona, es decir, que el recurso EXISTE.
	keyBRoles = "key-b-roles"
	// keyAViewer porta el glob del rol viewer ('*.read', migración 0015): alcanza
	// roles.read y NINGÚN .write. Es el criterio de T1.0-3 visto desde el cable.
	keyAViewer = "key-a-viewer"

	// Los sujetos de identity que se usan como miembros. Son UUID opacos: aquí no
	// hay padrón local (INV-02) y estas rutas nunca ven un email.
	userDeA = "11111111-1111-1111-1111-111111111111"
	userDeB = "22222222-2222-2222-2222-222222222222"
	// otroDeA es el SEGUNDO miembro de la empresa A. Existe para que el listado
	// de miembros se pruebe con más de una fila: con una sola, un listado que
	// devolviera «la primera que encuentre» pasaría igual.
	otroDeA = "44444444-4444-4444-4444-444444444444"
)

// planoDeRoles es el montaje completo de la puerta: la API con las rutas reales
// y los dos stores para sembrar el estado de partida.
type planoDeRoles struct {
	api      *testAPI
	roles    *memory.RoleStore
	members  *memory.MembershipStore
	rolAdeA  domain.Role // rol PROPIO de la empresa A
	rolBdeB  domain.Role // rol PROPIO de la empresa B (el "ajeno" para A)
	plantill domain.Role // plantilla GLOBAL (tenant_id NULL): visible, no editable
}

// nuevoPlanoDeRoles arma la API con los usecases reales y siembra: un rol propio
// por empresa, una plantilla global y un miembro en cada empresa.
func nuevoPlanoDeRoles(t *testing.T) *planoDeRoles {
	t.Helper()
	st := memory.NewStore()

	// El MISMO CallerResolver que cablea bootstrap.buildRolePlane. Es la pieza que
	// hace que el tenant salga del token y no del cuerpo (INV-04): si este test
	// inventara aquí un tenant fijo, dejaría de probar lo que prueba.
	caller := in.CallerResolverFunc(func(ctx context.Context) (in.Caller, bool) {
		id, ok := httpapi.IdentityFromContext(ctx)
		return in.Caller{TenantID: id.TenantID, UserID: id.Subject}, ok
	})
	roleSvc, err := iamusecase.NewRoleService(caller, st.Roles, st.Grants, st.Memberships)
	if err != nil {
		t.Fatalf("NewRoleService: %v", err)
	}
	// El doble de identity acredita sin ruido: lo que este fichero prueba es el
	// plano de roles, no la acreditación (esa vive en membersalta_test.go).
	memberSvc, err := iamusecase.NewMembershipService(caller, st.Memberships, &identidadDeMentira{}, nil)
	if err != nil {
		t.Fatalf("NewMembershipService: %v", err)
	}

	tA, tB := tenantA, tenantB
	p := &planoDeRoles{
		roles:    st.Roles,
		members:  st.Memberships,
		rolAdeA:  st.Roles.Seed(domain.Role{TenantID: &tA, Name: "soporte-a"}, nil),
		rolBdeB:  st.Roles.Seed(domain.Role{TenantID: &tB, Name: "soporte-b"}, nil),
		plantill: st.Roles.Seed(domain.Role{Name: "viewer"}, nil),
	}
	st.Memberships.Seed(userDeA, tenantA)
	st.Memberships.Seed(otroDeA, tenantA)
	st.Memberships.Seed(userDeB, tenantB)

	p.api = newAPI(publicapi.Deps{Roles: roleSvc, Members: memberSvc}, map[string]testIdentity{
		keyARoles:  {TenantID: tenantA, Subject: "admin-a", Grants: []string{"roles.read", "roles.write", "members.read", "members.write"}},
		keyBRoles:  {TenantID: tenantB, Subject: "admin-b", Grants: []string{"roles.read", "roles.write", "members.read", "members.write"}},
		keyAViewer: {TenantID: tenantA, Subject: "viewer-a", Grants: []string{"*.read"}},
	})
	return p
}

// exigirCodigo compara el código EXACTO. Nada de `>= 400`: el criterio de T1.0-4
// distingue 404 de 403, y un assert por familia daría verde con el 403 que este
// fichero existe para prohibir.
func exigirCodigo(t *testing.T, rec *httptest.ResponseRecorder, quiero int, que string) {
	t.Helper()
	if rec.Code != quiero {
		t.Errorf("%s: code = %d, quiero %d (body %s)", que, rec.Code, quiero, rec.Body.String())
	}
}

// decodificar vuelca el cuerpo JSON en dst y aborta si no casa.
func decodificar(t *testing.T, body []byte, dst any) {
	t.Helper()
	if err := json.Unmarshal(body, dst); err != nil {
		t.Fatalf("decodificando la respuesta: %v; body=%s", err, body)
	}
}

// TestRolePlane_MetodoEquivocadoDa405 cubre el criterio del 405.
//
// El 405 no lo produce ningún `if r.Method` de los handlers —no lo tienen a
// propósito—: lo produce el http.ServeMux de Go 1.22 porque las rutas se
// registran con patrones método+ruta. Por eso el test va contra
// publicapi.Register y no contra un handler suelto: montado de otra forma, el
// mismo handler devolvería 200 al método equivocado.
func TestRolePlane_MetodoEquivocadoDa405(t *testing.T) {
	t.Parallel()
	p := nuevoPlanoDeRoles(t)

	casos := []struct {
		nombre string
		metodo string
		ruta   string
	}{
		{"DELETE_sobre_la_coleccion_de_roles", http.MethodDelete, "/api/v1/roles"},
		{"PUT_sobre_la_coleccion_de_roles", http.MethodPut, "/api/v1/roles"},
		{"PUT_sobre_los_grants_de_un_rol", http.MethodPut, "/api/v1/roles/" + p.rolAdeA.ID + "/grants"},
		{"GET_sobre_una_membresia", http.MethodGet, "/api/v1/members/" + userDeA},
		{"GET_sobre_los_roles_de_un_miembro", http.MethodGet, "/api/v1/members/" + userDeA + "/roles"},
		{"PUT_sobre_la_coleccion_de_miembros", http.MethodPut, "/api/v1/members"},
		{"GET_sobre_una_asignacion_de_rol", http.MethodGet, "/api/v1/members/" + userDeA + "/roles/" + p.rolAdeA.ID},
		{"GET_sobre_los_grants_de_un_miembro", http.MethodGet, "/api/v1/members/" + userDeA + "/grants"},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			rec := call(p.api, keyARoles, c.metodo, c.ruta, "")
			// EXACTAMENTE 405: un 404 aquí significaría que la ruta no se registró
			// (y entonces este test no estaría probando el método, sino su ausencia).
			exigirCodigo(t, rec, http.StatusMethodNotAllowed, c.metodo+" "+c.ruta)
			if allow := rec.Header().Get("Allow"); allow == "" {
				t.Errorf("%s %s: 405 sin cabecera Allow", c.metodo, c.ruta)
			}
		})
	}
}

// TestRolePlane_CrossTenantDa404YNo403 es el criterio con trampa: pedir con el
// token de la empresa A un recurso de la empresa B tiene que dar 404 y NO 403.
// La diferencia no es cosmética — un 403 dice «existe, pero no puedes», y eso ya
// es filtrar que ese rol o esa persona existen en otra empresa.
//
// 🔴 LO QUE HACE QUE ESTE TEST NO SEA VACUO son dos cosas, y las dos están
// comprobadas abajo:
//
//  1. el token de A trae `roles.write`, así que el middleware NO puede ser el que
//     corta: si el mapeo cambiara a 403, el test se pondría rojo; y
//  2. la MISMA petición con el token de B responde 2xx, así que el recurso EXISTE
//     y el 404 no es «ruta inexistente» ni «id que no está en ninguna parte».
func TestRolePlane_CrossTenantDa404YNo403(t *testing.T) {
	t.Parallel()
	p := nuevoPlanoDeRoles(t)

	grant := `{"pattern":"sessions.read","effect":"allow"}`
	casos := []struct {
		nombre string
		metodo string
		ruta   string
		cuerpo string
	}{
		{"conceder_grant_a_un_rol_ajeno", http.MethodPost, "/api/v1/roles/" + p.rolBdeB.ID + "/grants", grant},
		{"revocar_grant_de_un_rol_ajeno", http.MethodDelete, "/api/v1/roles/" + p.rolBdeB.ID + "/grants?pattern=sessions.read&effect=allow", ""},
		{"asignar_rol_a_una_persona_ajena", http.MethodPost, "/api/v1/members/" + userDeB + "/roles", `{"role_id":"` + p.rolAdeA.ID + `"}`},
		{"quitar_rol_a_una_persona_ajena", http.MethodDelete, "/api/v1/members/" + userDeB + "/roles/" + p.rolAdeA.ID, ""},
		{"conceder_override_a_una_persona_ajena", http.MethodPost, "/api/v1/members/" + userDeB + "/grants", grant},
		{"revocar_override_de_una_persona_ajena", http.MethodDelete, "/api/v1/members/" + userDeB + "/grants?pattern=sessions.read&effect=allow", ""},
		{"heredar_de_un_rol_padre_ajeno", http.MethodPost, "/api/v1/roles", `{"name":"nuevo-a","parent_role_id":"` + p.rolBdeB.ID + `"}`},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			rec := call(p.api, keyARoles, c.metodo, c.ruta, c.cuerpo)
			if rec.Code == http.StatusForbidden {
				t.Fatalf("%s %s: devolvió 403 — un 'prohibido' CONFIRMA que el recurso ajeno existe; "+
					"el contrato de T1.0-4 es 404 (body %s)", c.metodo, c.ruta, rec.Body.String())
			}
			exigirCodigo(t, rec, http.StatusNotFound, c.metodo+" "+c.ruta)
		})
	}

	// El control que impide que lo de arriba sea vacuo: las MISMAS operaciones,
	// con el token de la empresa dueña, sí surten efecto.
	t.Run("control_el_dueno_si_puede", func(t *testing.T) {
		rec := call(p.api, keyBRoles, http.MethodPost, "/api/v1/roles/"+p.rolBdeB.ID+"/grants", grant)
		exigirCodigo(t, rec, http.StatusNoContent, "POST grants del rol propio de B")

		rec = call(p.api, keyBRoles, http.MethodPost, "/api/v1/members/"+userDeB+"/roles",
			`{"role_id":"`+p.rolBdeB.ID+`"}`)
		exigirCodigo(t, rec, http.StatusNoContent, "POST rol a un miembro propio de B")
	})
}

// TestRolePlane_ElViewerLeeYNoEscribe ratifica en el CABLE el criterio de T1.0-3:
// el glob '*.read' del rol viewer alcanza roles.read y ningún .write. Se
// comprueba aquí y no solo en la migración porque lo que la dueña sufre es el
// código HTTP, no la fila de la tabla.
func TestRolePlane_ElViewerLeeYNoEscribe(t *testing.T) {
	t.Parallel()
	p := nuevoPlanoDeRoles(t)

	rec := call(p.api, keyAViewer, http.MethodGet, "/api/v1/roles", "")
	exigirCodigo(t, rec, http.StatusOK, "GET /api/v1/roles con el glob del viewer")

	// El gemelo: members.read también lo alcanza el glob '*.read'. Sin esta
	// comprobación, la mitad «un viewer SÍ mira quién está en la empresa» del
	// criterio de T1.0-3 no la tocaría ningún cable.
	rec = call(p.api, keyAViewer, http.MethodGet, "/api/v1/members", "")
	exigirCodigo(t, rec, http.StatusOK, "GET /api/v1/members con el glob del viewer")

	rec = call(p.api, keyAViewer, http.MethodPost, "/api/v1/roles", `{"name":"no-deberia"}`)
	exigirCodigo(t, rec, http.StatusForbidden, "POST /api/v1/roles con el glob del viewer")

	rec = call(p.api, keyAViewer, http.MethodPost, "/api/v1/members", `{"user_id":"`+userDeA+`"}`)
	exigirCodigo(t, rec, http.StatusForbidden, "POST /api/v1/members con el glob del viewer")
}

// TestRolePlane_ListarYCrearRoles cubre la lectura y el alta, y con ellas el
// aislamiento por el otro lado: A ve su rol y la plantilla global, y NO ve el rol
// de B.
func TestRolePlane_ListarYCrearRoles(t *testing.T) {
	t.Parallel()
	p := nuevoPlanoDeRoles(t)

	rec := call(p.api, keyARoles, http.MethodGet, "/api/v1/roles", "")
	exigirCodigo(t, rec, http.StatusOK, "GET /api/v1/roles")
	var listado []struct {
		RoleID   string `json:"role_id"`
		Name     string `json:"name"`
		TenantID string `json:"tenant_id"`
		Global   bool   `json:"global"`
	}
	decodificar(t, rec.Body.Bytes(), &listado)

	vistos := map[string]bool{}
	for _, r := range listado {
		vistos[r.RoleID] = true
		if r.RoleID == p.plantill.ID && !r.Global {
			t.Error("la plantilla global no viene marcada como global: la UI ofrecería editarla y recibiría 422")
		}
	}
	if !vistos[p.rolAdeA.ID] || !vistos[p.plantill.ID] {
		t.Errorf("el listado de A no trae su rol propio y la plantilla global: %+v", listado)
	}
	if vistos[p.rolBdeB.ID] {
		t.Error("el listado de A trae el rol de la empresa B: fuga de aislamiento (INV-04)")
	}

	// Alta: 201 con el rol creado, acotado a la empresa del token aunque el cuerpo
	// no la mencione.
	rec = call(p.api, keyARoles, http.MethodPost, "/api/v1/roles", `{"name":"turno-noche"}`)
	exigirCodigo(t, rec, http.StatusCreated, "POST /api/v1/roles")
	var creado struct {
		RoleID   string `json:"role_id"`
		TenantID string `json:"tenant_id"`
		Global   bool   `json:"global"`
	}
	decodificar(t, rec.Body.Bytes(), &creado)
	if creado.TenantID != tenantA || creado.Global {
		t.Errorf("el rol no nació acotado a la empresa del token: %+v", creado)
	}

	// Y el nombre repetido dentro de la misma empresa es 409, no 500.
	rec = call(p.api, keyARoles, http.MethodPost, "/api/v1/roles", `{"name":"turno-noche"}`)
	exigirCodigo(t, rec, http.StatusConflict, "POST /api/v1/roles con nombre repetido")
}

// TestRolePlane_LaPlantillaGlobalNoSeEdita cubre el 422: una plantilla global es
// VISIBLE y asignable desde cualquier empresa, y justo por eso no es editable
// desde ninguna — sus grants valen para todas a la vez.
//
// 422 y no 400: el cuerpo está bien formado y se entiende perfectamente; lo que
// no se puede procesar es lo que pide.
func TestRolePlane_LaPlantillaGlobalNoSeEdita(t *testing.T) {
	t.Parallel()
	p := nuevoPlanoDeRoles(t)

	rec := call(p.api, keyARoles, http.MethodPost, "/api/v1/roles/"+p.plantill.ID+"/grants",
		`{"pattern":"sessions.read","effect":"allow"}`)
	exigirCodigo(t, rec, http.StatusUnprocessableEntity, "POST grants sobre la plantilla global")

	// Pero SÍ se puede asignar a un miembro: visible y asignable, no editable.
	rec = call(p.api, keyARoles, http.MethodPost, "/api/v1/members/"+userDeA+"/roles",
		`{"role_id":"`+p.plantill.ID+`"}`)
	exigirCodigo(t, rec, http.StatusNoContent, "asignar la plantilla global a un miembro propio")
}

// TestRolePlane_AltaYBajaDeMiembro cubre members.write: el alta es idempotente
// (204, no 201) y la segunda EMPRESA para la misma persona es 409.
//
// 🔧 El porqué del 409 cambió el 2026-08-29: hasta entonces era que «le rompería
// el canje de su token». Desde el Plan 047 · Ola 5 · T5.1 el canje resuelve con
// varias membresías (empresa activa, D-047.14), así que lo que lo sostiene es que
// el alta en una segunda empresa sea una decisión y no un efecto colateral —
// MD-055.2 decide cuándo se levanta.
func TestRolePlane_AltaYBajaDeMiembro(t *testing.T) {
	t.Parallel()
	p := nuevoPlanoDeRoles(t)
	const nuevo = "33333333-3333-3333-3333-333333333333"

	rec := call(p.api, keyARoles, http.MethodPost, "/api/v1/members", `{"user_id":"`+nuevo+`"}`)
	exigirCodigo(t, rec, http.StatusNoContent, "POST /api/v1/members")
	rec = call(p.api, keyARoles, http.MethodPost, "/api/v1/members", `{"user_id":"`+nuevo+`"}`)
	exigirCodigo(t, rec, http.StatusNoContent, "POST /api/v1/members repetido (idempotente)")

	// La misma persona en la empresa B: 409.
	rec = call(p.api, keyBRoles, http.MethodPost, "/api/v1/members", `{"user_id":"`+nuevo+`"}`)
	exigirCodigo(t, rec, http.StatusConflict, "POST /api/v1/members de alguien que ya es de otra empresa")

	// user_id vacío es 400 (entrada inválida), no 500.
	rec = call(p.api, keyARoles, http.MethodPost, "/api/v1/members", `{"user_id":""}`)
	exigirCodigo(t, rec, http.StatusBadRequest, "POST /api/v1/members sin user_id")

	// Baja: 204, e idempotente. Y la baja de alguien de OTRA empresa no borra nada
	// y responde 204 igual — el DELETE lleva el tenant del contexto, así que no hay
	// nada que 404ear ni nada que filtrar.
	rec = call(p.api, keyARoles, http.MethodDelete, "/api/v1/members/"+nuevo, "")
	exigirCodigo(t, rec, http.StatusNoContent, "DELETE /api/v1/members/{user_id}")
	tenants, err := p.members.TenantsOfUser(context.Background(), nuevo)
	if err != nil {
		t.Fatalf("TenantsOfUser: %v", err)
	}
	if len(tenants) != 0 {
		t.Errorf("la baja no retiró la membresía: %v", tenants)
	}
}

// TestRolePlane_GrantInvalidoEs400 comprueba que el efecto NO toma "allow" por
// defecto: un campo olvidado no puede convertirse en un permiso concedido.
func TestRolePlane_GrantInvalidoEs400(t *testing.T) {
	t.Parallel()
	p := nuevoPlanoDeRoles(t)

	casos := map[string]string{
		"sin_effect":      `{"pattern":"sessions.read"}`,
		"effect_inventad": `{"pattern":"sessions.read","effect":"quizas"}`,
		"sin_pattern":     `{"effect":"allow"}`,
	}
	for nombre, cuerpo := range casos {
		t.Run(nombre, func(t *testing.T) {
			rec := call(p.api, keyARoles, http.MethodPost, "/api/v1/roles/"+p.rolAdeA.ID+"/grants", cuerpo)
			exigirCodigo(t, rec, http.StatusBadRequest, "POST grants con "+nombre)
		})
	}

	// Y el grant que sí es válido entra: sin este control, los tres de arriba
	// podrían estar saliendo verdes por una ruta rota.
	rec := call(p.api, keyARoles, http.MethodPost, "/api/v1/roles/"+p.rolAdeA.ID+"/grants",
		`{"pattern":"sessions.read","effect":"allow"}`)
	exigirCodigo(t, rec, http.StatusNoContent, "POST grants válido")
	grants, err := p.roles.GrantsOf(context.Background(), p.rolAdeA.ID)
	if err != nil {
		t.Fatalf("GrantsOf: %v", err)
	}
	if len(grants) != 1 || grants[0].Pattern != "sessions.read" || grants[0].Effect != domain.EffectAllow {
		t.Errorf("el grant no llegó al repositorio tal cual: %+v", grants)
	}
}

// TestRolePlane_ElListadoDeMiembrosSoloTraeLosDeSuEmpresa cubre `members.read`,
// el scope que la migración 0084 sembró y que hasta ahora no consumía nadie.
//
// Se prueba con DOS empresas pobladas y desde LOS DOS lados, no con una sola:
// un listado que devolviera la tabla entera —el fallo clásico de una consulta a
// la que se le olvida el WHERE— pasaría un test de una sola empresa sin
// despeinarse, porque todo lo que hay es suyo.
func TestRolePlane_ElListadoDeMiembrosSoloTraeLosDeSuEmpresa(t *testing.T) {
	t.Parallel()
	p := nuevoPlanoDeRoles(t)

	leer := func(credencial string) []memberWire {
		t.Helper()
		rec := call(p.api, credencial, http.MethodGet, "/api/v1/members", "")
		exigirCodigo(t, rec, http.StatusOK, "GET /api/v1/members con "+credencial)
		var miembros []memberWire
		decodificar(t, rec.Body.Bytes(), &miembros)
		return miembros
	}

	deA := leer(keyARoles)
	if ids := idsDeMiembros(deA); !mismosIDs(ids, []string{userDeA, otroDeA}) {
		t.Errorf("la empresa A ve %v; esperaba exactamente sus dos miembros (%s, %s)", ids, userDeA, otroDeA)
	}
	for _, m := range deA {
		if m.TenantID != tenantA {
			t.Errorf("una fila del listado de A dice tenant_id=%q: %+v", m.TenantID, m)
		}
		if m.CreatedAt == "" {
			t.Errorf("la fila %s viene sin created_at: es lo único, además del id, que la dueña puede ver", m.UserID)
		}
	}

	// Y desde el otro lado: B ve al suyo y solo al suyo. Este segundo sentido es
	// lo que descarta que A viera «lo primero de la tabla» por casualidad.
	if ids := idsDeMiembros(leer(keyBRoles)); !mismosIDs(ids, []string{userDeB}) {
		t.Errorf("la empresa B ve %v; esperaba solo a %s", ids, userDeB)
	}

	// El listado refleja las escrituras del propio plano: dar de alta a alguien lo
	// hace aparecer, y darlo de baja lo hace desaparecer. Sin esto, el endpoint
	// podría estar leyendo de un sitio distinto del que escriben POST/DELETE.
	const recien = "55555555-5555-5555-5555-555555555555"
	exigirCodigo(t, call(p.api, keyARoles, http.MethodPost, "/api/v1/members", `{"user_id":"`+recien+`"}`),
		http.StatusNoContent, "alta previa al listado")
	if ids := idsDeMiembros(leer(keyARoles)); !mismosIDs(ids, []string{userDeA, otroDeA, recien}) {
		t.Errorf("tras el alta, la empresa A ve %v; esperaba a los tres", ids)
	}
	exigirCodigo(t, call(p.api, keyARoles, http.MethodDelete, "/api/v1/members/"+recien, ""),
		http.StatusNoContent, "baja previa al listado")
	if ids := idsDeMiembros(leer(keyARoles)); !mismosIDs(ids, []string{userDeA, otroDeA}) {
		t.Errorf("tras la baja, la empresa A sigue viendo %v", ids)
	}
}

// TestRolePlane_ElListadoDeMiembrosNoDelataAIdentity fija en un test lo que hoy
// es una decisión escrita: el listado devuelve lo que guarda tenant_members —id
// opaco, empresa y fecha— y NADA de la persona, que vive en identity (INV-02).
//
// Es un candado sobre el WIRE, no sobre el DTO: quien mañana añada `name` o
// `email` «porque la UI lo necesita» tiene que pasar por aquí, y esa es una
// decisión de producto (¿el listado de una empresa consulta el padrón del
// grupo?) que no se toma por comodidad de una pantalla.
func TestRolePlane_ElListadoDeMiembrosNoDelataAIdentity(t *testing.T) {
	t.Parallel()
	p := nuevoPlanoDeRoles(t)

	rec := call(p.api, keyARoles, http.MethodGet, "/api/v1/members", "")
	exigirCodigo(t, rec, http.StatusOK, "GET /api/v1/members")

	var crudo []map[string]any
	decodificar(t, rec.Body.Bytes(), &crudo)
	if len(crudo) == 0 {
		t.Fatal("el listado vino vacío: el test no puede comprobar los campos de una fila que no existe")
	}
	const permitidos = "user_id, tenant_id, created_at"
	for _, fila := range crudo {
		for campo := range fila {
			if campo != "user_id" && campo != "tenant_id" && campo != "created_at" {
				t.Errorf("el listado de miembros expone el campo %q; los únicos admitidos son %s "+
					"(la persona vive en identity-core, INV-02: sacarla de ahí es decisión de producto)",
					campo, permitidos)
			}
		}
	}
}

// memberWire es el cuerpo de una fila de GET /api/v1/members.
type memberWire struct {
	UserID    string `json:"user_id"`
	TenantID  string `json:"tenant_id"`
	CreatedAt string `json:"created_at"`
}

// idsDeMiembros proyecta los user_id del listado.
func idsDeMiembros(miembros []memberWire) []string {
	ids := make([]string, 0, len(miembros))
	for _, m := range miembros {
		ids = append(ids, m.UserID)
	}
	return ids
}

// mismosIDs compara dos conjuntos de ids SIN mirar el orden (el orden del
// listado lo garantiza el repositorio y lo prueban sus propios tests; aquí lo
// que importa es QUIÉNES salen).
func mismosIDs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	pendientes := make(map[string]int, len(want))
	for _, id := range want {
		pendientes[id]++
	}
	for _, id := range got {
		if pendientes[id] == 0 {
			return false
		}
		pendientes[id]--
	}
	return true
}

// TestRolePlane_LasOnceRutasEstanMontadas es el criterio «las rutas nuevas
// aparecen en el inventario», EJECUTADO en vez de grepeado.
//
// Un grep sobre el código encuentra el literal aunque el `mux.Handle` esté
// dentro de un `if` que nunca se cumple; esto pide las once contra el mux REAL y
// exige 2xx. Todas se piden sobre recursos PROPIOS de la empresa A y con los
// cuatro scopes, así que cualquier 404 aquí solo puede significar «esa ruta no
// existe» — no «eso no es tuyo».
//
// Cada caso arma su propio plano: el último DELETE da de baja a userDeA, y si
// compartieran estado el orden de ejecución decidiría el resultado.
func TestRolePlane_LasOnceRutasEstanMontadas(t *testing.T) {
	t.Parallel()

	grant := `{"pattern":"sessions.read","effect":"allow"}`
	rutas := []struct {
		metodo string
		// ruta se compone con el plano recién sembrado (los ids cambian en cada uno).
		ruta   func(p *planoDeRoles) string
		cuerpo func(p *planoDeRoles) string
	}{
		{http.MethodGet, func(*planoDeRoles) string { return "/api/v1/roles" }, nil},
		{http.MethodPost, func(*planoDeRoles) string { return "/api/v1/roles" }, func(*planoDeRoles) string { return `{"name":"nuevo"}` }},
		{http.MethodPost, func(p *planoDeRoles) string { return "/api/v1/roles/" + p.rolAdeA.ID + "/grants" }, func(*planoDeRoles) string { return grant }},
		{http.MethodDelete, func(p *planoDeRoles) string {
			return "/api/v1/roles/" + p.rolAdeA.ID + "/grants?pattern=sessions.read&effect=allow"
		}, nil},
		{http.MethodGet, func(*planoDeRoles) string { return "/api/v1/members" }, nil},
		{http.MethodPost, func(*planoDeRoles) string { return "/api/v1/members" }, func(*planoDeRoles) string {
			return `{"user_id":"` + userDeA + `"}`
		}},
		{http.MethodDelete, func(*planoDeRoles) string { return "/api/v1/members/" + userDeA }, nil},
		{http.MethodPost, func(*planoDeRoles) string { return "/api/v1/members/" + userDeA + "/roles" }, func(p *planoDeRoles) string {
			return `{"role_id":"` + p.rolAdeA.ID + `"}`
		}},
		{http.MethodDelete, func(p *planoDeRoles) string {
			return "/api/v1/members/" + userDeA + "/roles/" + p.rolAdeA.ID
		}, nil},
		{http.MethodPost, func(*planoDeRoles) string { return "/api/v1/members/" + userDeA + "/grants" }, func(*planoDeRoles) string { return grant }},
		{http.MethodDelete, func(*planoDeRoles) string {
			return "/api/v1/members/" + userDeA + "/grants?pattern=sessions.read&effect=allow"
		}, nil},
	}
	if len(rutas) != 11 {
		t.Fatalf("el inventario del plano de roles son 11 rutas, no %d: si añades o quitas una, dilo aquí", len(rutas))
	}

	for _, r := range rutas {
		p := nuevoPlanoDeRoles(t)
		ruta := r.ruta(p)
		cuerpo := ""
		if r.cuerpo != nil {
			cuerpo = r.cuerpo(p)
		}
		t.Run(r.metodo+"_"+ruta, func(t *testing.T) {
			rec := call(p.api, keyARoles, r.metodo, ruta, cuerpo)
			if rec.Code == http.StatusNotFound {
				t.Fatalf("%s %s: 404 — la ruta NO ESTÁ MONTADA (se pidió sobre un recurso propio y con scope, "+
					"así que no puede ser el 404 de aislamiento)", r.metodo, ruta)
			}
			if rec.Code < 200 || rec.Code > 299 {
				t.Errorf("%s %s: code = %d, esperaba 2xx (body %s)", r.metodo, ruta, rec.Code, rec.Body.String())
			}
		})
	}
}
