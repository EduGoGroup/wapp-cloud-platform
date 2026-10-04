// Parte de internal/arranque/auth.go (copia de internal/bootstrap/arranque/auth.go @ 80807ba), T2.34: el canje de una invitación.
package arranque

import (
	"context"
	"database/sql"
	"fmt"

	iampostgres "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/infra/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
	iamusecase "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/usecase"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// ---------------------------------------------------------------------------
// El CANJE de una invitación (Plan 047 · Ola A · T-A3/T-A4/T-A5)
// ---------------------------------------------------------------------------

// buildInvitationRedeem cablea el canje: la otra mitad de la invitación, la que
// usa el INVITADO.
//
// 🔴 SE CONSTRUYE APARTE DE rolePlane, Y NO ES DESORDEN. Aquellos tres servicios
// son el plano de administración de la empresa: los usa quien YA está dentro y
// todos exigen un token CON empresa (su CallerResolver resuelve el tenant y sin
// él fallan con domain.ErrNoTenant). El canje es lo contrario: lo usa quien
// todavía no está en ninguna, con un Context Token SIN empresa, y su ruta se
// monta fuera de registerRolePlane por eso mismo — ver el montaje en http.go.
// Meterlo en la misma pieza sugeriría que comparte esa precondición, y no la
// comparte.
//
// El CallerResolver es el MISMO puerto que el de buildRolePlane y se declara
// igual, con una diferencia que no está aquí sino en el usecase: RedeemService
// mira `UserID` y NO mira `TenantID`. La función de aquí no puede expresar esa
// diferencia (devuelve los dos campos, como la otra), así que no se intenta:
// donde vive la regla es en RedeemService.RedeemInvitation, con su porqué.
//
// No recibe `systems` ni `log`: el canje no llama a identity (quien canjea ya
// pasó su System Gate: si no, no tendría Context Token con el que llegar) y no
// tiene un fallo que el llamante no pueda ver, que era lo que el log salvaba en
// MembershipService.
//
// SÍ recibe `features`, y por la misma razón que el plano de roles: canjear DA DE
// ALTA, y desde el Plan 047 · Ola 5 · T5.2 el alta de quien ya es miembro de otra
// empresa depende del entitlement `multi_empresa` del tenant que invitó. El
// canje no lo consulta: solo lo lleva hasta GrantTenantAccess, que es quien
// pregunta.
func buildInvitationRedeem(db *sql.DB, features iampostgres.FeatureResolver) (in.InvitationRedeemer, error) {
	caller := in.CallerResolverFunc(func(ctx context.Context) (in.Caller, bool) {
		id, ok := httpapi.IdentityFromContext(ctx)
		return in.Caller{TenantID: id.TenantID, UserID: id.Subject}, ok
	})
	svc, err := iamusecase.NewRedeemService(caller, iampostgres.NewInvitationRedeemRepo(db, features))
	if err != nil {
		return nil, fmt.Errorf("construyendo RedeemService (IAM): %w", err)
	}
	return svc, nil
}
