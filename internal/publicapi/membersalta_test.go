package publicapi_test

// membersalta_test.go — LOS DESENLACES DE POST /api/v1/members CONTRA EL CABLE
// (Plan 047 · Ola B, T-B5 y T-B6).
//
// Corre contra el registro REAL (publicapi.Register) y el usecase REAL: lo que
// se prueba es qué código y qué cuerpo ve la consola, y eso solo cuenta si nace
// dentro del usecase y llega vivo hasta el HTTP. Un doble de in.MembershipAdmin
// probaría el mapeo contra sí mismo.

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/infra/memory"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/out"
	iamusecase "github.com/EduGoGroup/wapp-cloud-platform/internal/iam/usecase"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
)

// identidadDeMentira es el doble de out.UserSystemsClient para el plano público.
// Programable por fallo: cada campo `err*` produce uno de los desenlaces de la
// tabla de T-B6.
type identidadDeMentira struct {
	mu       sync.Mutex
	vigentes []string
	errGet   error
	errPut   error
	puts     [][]string
}

var _ out.UserSystemsClient = (*identidadDeMentira)(nil)

func (f *identidadDeMentira) GetUserSystems(_ context.Context, _ string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.errGet != nil {
		return nil, f.errGet
	}
	return append([]string{}, f.vigentes...), nil
}

func (f *identidadDeMentira) ReplaceUserSystems(_ context.Context, _ string, systems []string) (domain.IdentitySystemsDiff, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts = append(f.puts, append([]string{}, systems...))
	if f.errPut != nil {
		return domain.IdentitySystemsDiff{}, f.errPut
	}
	f.vigentes = append([]string{}, systems...)
	return domain.IdentitySystemsDiff{Systems: f.vigentes, Granted: []string{}, Revoked: []string{}}, nil
}

// planoDeAltas monta la API con el plano de miembros REAL sobre el cliente de
// identity que se le pase. `identity` puede ser nil de verdad: es el despliegue
// sin WAPP_IDENTITY_API_KEY.
type planoDeAltas struct {
	api     *testAPI
	members *memory.MembershipStore
}

func nuevoPlanoDeAltas(t *testing.T, identity out.UserSystemsClient) *planoDeAltas {
	t.Helper()
	st := memory.NewStore()
	caller := in.CallerResolverFunc(func(ctx context.Context) (in.Caller, bool) {
		id, ok := httpapi.IdentityFromContext(ctx)
		return in.Caller{TenantID: id.TenantID, UserID: id.Subject}, ok
	})
	memberSvc, err := iamusecase.NewMembershipService(caller, st.Memberships, identity, nil)
	if err != nil {
		t.Fatalf("NewMembershipService: %v", err)
	}
	roleSvc, err := iamusecase.NewRoleService(caller, st.Roles, st.Grants, st.Memberships)
	if err != nil {
		t.Fatalf("NewRoleService: %v", err)
	}
	api := newAPI(publicapi.Deps{Roles: roleSvc, Members: memberSvc}, map[string]testIdentity{
		keyARoles: {TenantID: tenantA, Subject: "admin-a", Grants: []string{"roles.read", "roles.write", "members.read", "members.write"}},
	})
	return &planoDeAltas{api: api, members: st.Memberships}
}

// TestMembersAlta_SinClienteM2MElAltaEs503YElListadoSigueEn200 — T-B5.
//
// 🔴 La distinción que fija este test es 503 vs 404, y es semántica. El 404 del
// plano de roles significa «esta administración no existe en este proceso»
// (publicapi.registerRolePlane con d.Members == nil). Aquí la administración SÍ
// existe —lo demuestra el GET, que contesta 200 en la misma corrida— y lo que
// falta es la credencial M2M de identity. Desmontar la ruta daría un 404 que
// manda a depurar el router cuando lo que hay que tocar es el entorno.
//
// El GET en 200 no es adorno: es la mitad que prueba que la ausencia de M2M
// degrada SOLO la escritura. Sin él, un montaje que tirara el plano entero
// también pasaría la mitad del 503.
func TestMembersAlta_SinClienteM2MElAltaEs503YElListadoSigueEn200(t *testing.T) {
	t.Parallel()
	// nil de verdad: un puntero nil metido en la interfaz daría un valor NO nil y
	// la guarda del usecase no dispararía (el mismo trapiche que documenta
	// authStack.m2mClient).
	p := nuevoPlanoDeAltas(t, nil)

	rec := call(p.api, keyARoles, http.MethodPost, "/api/v1/members", `{"user_id":"`+userDeA+`"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST /api/v1/members sin M2M: code = %d, quiero 503 (body %s).\n"+
			"Un 404 significaría que la ruta no se montó, y eso manda a depurar el router "+
			"en vez de la configuración del despliegue.", rec.Code, rec.Body.String())
	}
	if cuerpo := rec.Body.String(); !strings.Contains(cuerpo, "identity_no_configurado") {
		t.Errorf("cuerpo = %s, quiero {\"error\":\"identity_no_configurado\"}: el otro 503 "+
			"—identity caído— se arregla esperando, y este no", cuerpo)
	}

	rec = call(p.api, keyARoles, http.MethodGet, "/api/v1/members", "")
	if rec.Code != http.StatusOK {
		t.Errorf("GET /api/v1/members sin M2M: code = %d, quiero 200: la lectura de miembros no "+
			"necesita identity y no puede caerse con ella (body %s)", rec.Code, rec.Body.String())
	}

	// Y nadie quedó de alta: el 503 se devuelve ANTES de escribir.
	tenants, err := p.members.TenantsOfUser(context.Background(), userDeA)
	if err != nil {
		t.Fatalf("TenantsOfUser: %v", err)
	}
	if len(tenants) != 0 {
		t.Errorf("el 503 dejó membresía escrita: %v", tenants)
	}
}

// TestMembersAlta_LosSeisDesenlaces — T-B6.
//
// Un caso por desenlace y el código EXACTO, nunca por familia: 502 y 503 son los
// dos «no fue culpa tuya» y significan cosas distintas —uno es identity
// rechazando, el otro identity ausente o caído—, y quien esté de guardia mira
// primero el número.
func TestMembersAlta_LosSeisDesenlaces(t *testing.T) {
	t.Parallel()

	const nuevo = "77777777-7777-7777-7777-777777777777"

	casos := []struct {
		nombre  string
		monta   func(t *testing.T) *planoDeAltas
		userID  string
		codigo  int
		cuerpo  string // subcadena exigida; vacío = no se mira
		sinBody bool
	}{
		{
			nombre: "alta correcta",
			monta:  func(t *testing.T) *planoDeAltas { return nuevoPlanoDeAltas(t, &identidadDeMentira{}) },
			userID: nuevo, codigo: http.StatusNoContent, sinBody: true,
		},
		{
			// El MISMO 204 sin escribir en identity: la persona ya tenía la
			// aplicación. Desde fuera es indistinguible del anterior a propósito.
			nombre: "alta correcta de quien ya tenía la aplicación",
			monta: func(t *testing.T) *planoDeAltas {
				return nuevoPlanoDeAltas(t, &identidadDeMentira{vigentes: []string{"wapp.bff"}})
			},
			userID: nuevo, codigo: http.StatusNoContent, sinBody: true,
		},
		{
			nombre: "sin cliente M2M configurado",
			monta:  func(t *testing.T) *planoDeAltas { return nuevoPlanoDeAltas(t, nil) },
			userID: nuevo, codigo: http.StatusServiceUnavailable, cuerpo: "identity_no_configurado",
		},
		{
			nombre: "identity caído",
			monta: func(t *testing.T) *planoDeAltas {
				return nuevoPlanoDeAltas(t, &identidadDeMentira{errGet: domain.ErrIdentityUnavailable})
			},
			userID: nuevo, codigo: http.StatusServiceUnavailable, cuerpo: "identity no está disponible",
		},
		{
			nombre: "identity no acredita la aplicación",
			monta: func(t *testing.T) *planoDeAltas {
				return nuevoPlanoDeAltas(t, &identidadDeMentira{errPut: domain.ErrSystemNotAllowed})
			},
			userID: nuevo, codigo: http.StatusBadGateway, cuerpo: "system_no_acreditable",
		},
		{
			nombre: "el UUID no existe en identity",
			monta: func(t *testing.T) *planoDeAltas {
				return nuevoPlanoDeAltas(t, &identidadDeMentira{errGet: domain.ErrNotFound})
			},
			userID: nuevo, codigo: http.StatusNotFound,
		},
		{
			nombre: "ya es miembro de otra empresa",
			monta: func(t *testing.T) *planoDeAltas {
				p := nuevoPlanoDeAltas(t, &identidadDeMentira{})
				p.members.Seed(nuevo, tenantB)
				return p
			},
			userID: nuevo, codigo: http.StatusConflict,
		},
	}

	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			t.Parallel()
			p := c.monta(t)
			rec := call(p.api, keyARoles, http.MethodPost, "/api/v1/members", `{"user_id":"`+c.userID+`"}`)
			if rec.Code != c.codigo {
				t.Fatalf("code = %d, quiero %d (body %s)", rec.Code, c.codigo, rec.Body.String())
			}
			if c.sinBody && rec.Body.Len() != 0 {
				t.Errorf("el 204 no lleva cuerpo, llevó %s", rec.Body.String())
			}
			if c.cuerpo != "" && !strings.Contains(rec.Body.String(), c.cuerpo) {
				t.Errorf("cuerpo = %s, quiero que contenga %q", rec.Body.String(), c.cuerpo)
			}
		})
	}
}
