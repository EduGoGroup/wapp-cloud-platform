// Porta internal/iam/infra/postgres/active_tenant.go @ 9a77307

package iampostgres

// active_tenant.go — EL ADAPTADOR DE public.user_active_tenant (migración 0086; Plan 047 · Ola 5
// · T5.1).
//
// 🔴 AQUÍ NO SE DECIDE NADA. Este fichero guarda y devuelve una preferencia; que esa preferencia
// valga lo decide quien la lee, contrastándola contra las membresías vivas (el canje del
// usecase). Si algún día aparece aquí un JOIN contra tenant_members «para asegurar», habrá DOS
// sitios donde vive la misma regla y el día que discrepen ganará el que nadie está mirando.

import (
	"context"
	"database/sql"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// ActiveTenantRepo implementa out.ActiveTenantRepo sobre public.user_active_tenant. La tabla no
// lleva FK hacia el usuario: esa identidad vive en identity, en otra base de datos.
type ActiveTenantRepo struct{}

// NewActiveTenantRepo construye el repositorio sobre el pool dado. No valida el pool ni lo toca:
// el primer uso es la primera consulta.
func NewActiveTenantRepo(db *sql.DB) *ActiveTenantRepo {
	panic(pendiente.Implementar("iampostgres.NewActiveTenantRepo"))
}

var _ out.ActiveTenantRepo = (*ActiveTenantRepo)(nil)

// ActiveTenantOf implementa out.ActiveTenantRepo: la empresa que userID eligió, (tenantID, true,
// nil).
//
// La AUSENCIA de fila sale como ok=false y err=nil, y NO como domain.ErrNotFound: no haber
// elegido todavía no es un fallo, es el estado normal de quien acaba de recibir su segunda
// membresía. Un fallo de la base, en cambio, NO se disfraza de ausencia: sale ("", false, err),
// con err envolviendo la causa y el texto "iam: leyendo la empresa activa: …".
func (r *ActiveTenantRepo) ActiveTenantOf(ctx context.Context, userID string) (string, bool, error) {
	panic(pendiente.Implementar("iampostgres.ActiveTenantRepo.ActiveTenantOf"))
}

// SetActiveTenant implementa out.ActiveTenantRepo: UPSERT por `user_id`, porque elegir empresa
// REEMPLAZA la elección anterior (un valor por usuario, nunca dos filas ni una ventana sin fila).
// `updated_at` se reescribe en cada elección, no solo en la primera. Un fallo de la base sale
// envolviendo la causa con el texto "iam: guardando la empresa activa: …".
func (r *ActiveTenantRepo) SetActiveTenant(ctx context.Context, userID, tenantID string) error {
	panic(pendiente.Implementar("iampostgres.ActiveTenantRepo.SetActiveTenant"))
}
