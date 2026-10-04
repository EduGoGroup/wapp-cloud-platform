// Parte de internal/arranque/auth.go (copia de internal/bootstrap/arranque/auth.go @ 80807ba), T2.34: la elección de empresa.
package arranque

import (
	"context"
	"database/sql"
	"fmt"

	iampostgres "github.com/EduGoGroup/wapp-cloud-platform/internal/iam/infra/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
	iamusecase "github.com/EduGoGroup/wapp-cloud-platform/internal/iam/usecase"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// ---------------------------------------------------------------------------
// La ELECCIÓN de empresa (Plan 047 · Ola 5 · T5.1)
// ---------------------------------------------------------------------------

// buildActiveTenantPlane cablea las DOS puertas de la empresa del sujeto: LEER
// entre cuáles puede elegir (GET /api/v1/auth/tenants) y ESCRIBIR cuál elige
// (POST /api/v1/auth/active-tenant).
//
// 🔴 DEVUELVE EL SERVICIO CONCRETO Y NO UN PUERTO, y aquí es lo correcto:
// satisface DOS puertos (in.TenantLister e in.ActiveTenantSelector) y devolver
// uno solo obligaría a un type-assert o a construirlo dos veces. Quien recibe
// sigue tomando interfaces —el handler declara los dos puertos por separado—, que
// es donde la frontera importa; este helper es privado y de cableado.
//
// 🔴 SE CONSTRUYE APARTE DE rolePlane POR LA MISMA RAZÓN QUE EL CANJE, y es la
// razón entera de la tarea: aquellos servicios son el plano de administración de
// una empresa y todos exigen un token CON empresa (su CallerResolver resuelve el
// tenant y sin él fallan con domain.ErrNoTenant). Ésta es lo contrario: la usa
// quien todavía no tiene ninguna empresa en su token —dos membresías y ninguna
// elegida ⇒ token sin tenant y sin grants— y su ruta se monta fuera de
// registerRolePlane por eso mismo (ver el montaje en http.go).
//
// El MembershipRepo es el mismo adaptador que usan el canje y el plano de roles:
// la comprobación «¿es miembro de esta empresa?» tiene que dar el mismo veredicto
// que la que hace el canje al leer la empresa activa. Con dos adaptadores
// distintos, un día darían dos.
//
// No recibe `systems` ni `log`: elegir empresa no llama a identity (quien elige
// ya pasó su System Gate: si no, no tendría Context Token con el que llegar) y no
// tiene ningún fallo que el llamante no pueda ver.
func buildActiveTenantPlane(db *sql.DB) (*iamusecase.ActiveTenantService, error) {
	caller := in.CallerResolverFunc(func(ctx context.Context) (in.Caller, bool) {
		id, ok := httpapi.IdentityFromContext(ctx)
		return in.Caller{TenantID: id.TenantID, UserID: id.Subject}, ok
	})
	// nil de resolver por lo mismo que en el canje del exchange: elegir empresa
	// LEE membresías (UserTenants) y no da de alta a nadie.
	svc, err := iamusecase.NewActiveTenantService(caller, iampostgres.NewMembershipRepo(db, nil), iampostgres.NewActiveTenantRepo(db))
	if err != nil {
		return nil, fmt.Errorf("construyendo ActiveTenantService (IAM): %w", err)
	}
	return svc, nil
}
