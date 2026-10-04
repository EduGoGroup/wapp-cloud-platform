// Parte de internal/arranque/auth.go (copia de internal/bootstrap/arranque/auth.go @ 80807ba), T2.34: el plano de roles del tenant (roles, miembros e invitaciones).
package arranque

import (
	"context"
	"database/sql"
	"fmt"

	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"

	iampostgres "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/infra/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
	iamusecase "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/usecase"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// ---------------------------------------------------------------------------
// Plano de roles del tenant (Plan 047 · Ola 1.0 · T1.0-4)
// ---------------------------------------------------------------------------

// rolePlane agrupa los dos casos de uso que abren el plano 2 del ADR-0033 —la
// administración de RBAC y la de membresía de la PROPIA empresa— para que
// bootstrap.go los pase a publicapi.Deps con una sola pieza. Sin ellos, las
// rutas /api/v1/roles y /api/v1/members no se montan y responden 404 de ruta
// inexistente: es exactamente el modo de fallo mudo que vigila
// roleplane_cableado_test.go.
type rolePlane struct {
	roles   in.RoleAdmin
	members in.MembershipAdmin
	// invitations es la incorporación POR CÓDIGO (Plan 047 · Ola A). Va en la
	// misma pieza que las otras dos porque comparte con ellas el CallerResolver y
	// los repositorios, y porque las tres abren el mismo plano: quién está en la
	// empresa y quién puede entrar.
	invitations in.InvitationAdmin
}

// buildRolePlane cablea los casos de uso del plano de roles sobre el *sql.DB ya
// abierto.
//
// 🔑 EL CALLERRESOLVER ES LA PIEZA QUE IMPORTA, y es de aquí de donde tenía que
// salir. Los usecases no llaman a httpapi.IdentityFromContext ellos mismos por
// dirección de dependencias (internal/platform/httpapi ya importa iam/ports/in;
// que el usecase importara el transporte invertiría la flecha), así que reciben
// este puerto. Es el ÚNICO origen del tenant_id en todo el plano: ningún Input de
// in.* tiene campo TenantID, y esa ausencia es INV-04 escrita en el tipo. Un
// contexto sin Identity devuelve ok=false y el usecase falla con
// domain.ErrNoTenant antes de tocar un repositorio.
//
// Los cuatro repositorios son los MISMOS adaptadores que ya usa el canje
// (buildAuthStack): no hay una segunda implementación de estas tablas. El
// MembershipRepo se construye UNA vez y se comparte entre los dos servicios —
// RoleService lo necesita para requireMember (acotar las operaciones sobre
// personas) y MembershipService para el alta y la baja.
//
// `systems` es el cliente M2M de identity (authStack.m2mClient) y ADMITE nil: en
// un despliegue sin WAPP_IDENTITY_API_KEY el plano se construye igual, la
// lectura de miembros sigue sirviendo y solo el alta contesta 503 (ver
// iamusecase.NewMembershipService). Llega como interfaz y no como puntero
// concreto por la misma razón que el campo del que sale: un *M2MClient nil
// metido en un parámetro de interfaz produce un valor NO nil, y entonces la
// guarda del usecase daría siempre false.
//
// `features` es el resolver de derechos comerciales, y llega hasta aquí por UNA
// sola razón: el alta de un miembro pregunta por el entitlement `multi_empresa`
// antes de decidir si alguien que ya está en otra empresa puede entrar en ésta
// (Plan 047 · Ola 5 · T5.2). 🔴 NO gatea las rutas de este plano — administrar
// miembros y roles sigue siendo capacidad base de cualquier empresa (D-047.10):
// lo que la feature decide es el desenlace de un alta concreta, no si la puerta
// existe. Si algún día aparece aquí un RequireFeature, es un defecto.
//
// `log` es el MISMO del proceso, y no es decorativo: cuando identity rechaza la
// credencial M2M de wApp el llamante se lleva un 500 genérico —es un fallo del
// servidor y no le incumbe—, así que el rastro es el único sitio donde queda
// escrito qué scope hay que reemitir.
func buildRolePlane(db *sql.DB, systems out.UserSystemsClient, features iampostgres.FeatureResolver, log sharedlogger.Logger) (rolePlane, error) {
	caller := in.CallerResolverFunc(func(ctx context.Context) (in.Caller, bool) {
		id, ok := httpapi.IdentityFromContext(ctx)
		return in.Caller{TenantID: id.TenantID, UserID: id.Subject}, ok
	})
	members := iampostgres.NewMembershipRepo(db, features)
	roles := iampostgres.NewRoleRepo(db)
	roleSvc, err := iamusecase.NewRoleService(
		caller,
		roles,
		iampostgres.NewGrantRepo(db),
		members,
	)
	if err != nil {
		return rolePlane{}, fmt.Errorf("construyendo RoleService (IAM): %w", err)
	}
	memberSvc, err := iamusecase.NewMembershipService(caller, members, systems, log)
	if err != nil {
		return rolePlane{}, fmt.Errorf("construyendo MembershipService (IAM): %w", err)
	}
	// El servicio de invitaciones comparte el MISMO RoleRepo que RoleService, y no
	// es reutilización por comodidad: lo usa para una sola cosa —comprobar que el
	// rol prometido en la invitación es visible para la empresa que la emite— y
	// esa comprobación tiene que dar el mismo veredicto que la de RoleService.
	// Con dos adaptadores distintos, un día darían dos.
	invitationSvc, err := iamusecase.NewInvitationService(caller, iampostgres.NewInvitationRepo(db), roles)
	if err != nil {
		return rolePlane{}, fmt.Errorf("construyendo InvitationService (IAM): %w", err)
	}
	return rolePlane{roles: roleSvc, members: memberSvc, invitations: invitationSvc}, nil
}
