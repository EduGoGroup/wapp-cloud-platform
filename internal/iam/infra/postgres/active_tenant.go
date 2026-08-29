package iampostgres

// active_tenant.go — EL ADAPTADOR DE public.user_active_tenant (migración 0086;
// Plan 047 · Ola 5 · T5.1).
//
// 🔴 AQUÍ NO SE DECIDE NADA. Este fichero guarda y devuelve una preferencia; que
// esa preferencia valga lo decide quien la lee, contrastándola contra las
// membresías vivas (usecase.ExchangeService.tenantActivo). Si algún día aparece
// aquí un JOIN contra tenant_members «para asegurar», habrá DOS sitios donde vive
// la misma regla y el día que discrepen ganará el que nadie está mirando.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/out"
)

// ActiveTenantRepo implementa out.ActiveTenantRepo sobre
// public.user_active_tenant. La tabla no lleva FK hacia el usuario: esa
// identidad vive en identity, en otra base de datos.
type ActiveTenantRepo struct {
	db *sql.DB
}

// NewActiveTenantRepo construye el repositorio sobre el pool dado.
func NewActiveTenantRepo(db *sql.DB) *ActiveTenantRepo { return &ActiveTenantRepo{db: db} }

var _ out.ActiveTenantRepo = (*ActiveTenantRepo)(nil)

// ActiveTenantOf implementa out.ActiveTenantRepo.
//
// La AUSENCIA de fila sale como ok=false y err=nil, y NO como
// domain.ErrNotFound: no haber elegido todavía no es un fallo, es el estado
// normal de quien acaba de recibir su segunda membresía. Traducirlo a un error
// obligaría al canje a distinguir ese error de uno real, que es justo la
// confusión que se quiere evitar (ver el comentario del puerto).
func (r *ActiveTenantRepo) ActiveTenantOf(ctx context.Context, userID string) (string, bool, error) {
	var tenantID string
	err := r.db.QueryRowContext(ctx, `
		SELECT tenant_id::text
		FROM public.user_active_tenant
		WHERE user_id = $1
	`, userID).Scan(&tenantID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, fmt.Errorf("iam: leyendo la empresa activa: %w", err)
	}
	return tenantID, true, nil
}

// SetActiveTenant implementa out.ActiveTenantRepo.
//
// Es un UPSERT por `user_id` porque la tabla tiene PK por `user_id`: elegir
// empresa REEMPLAZA la elección anterior. Un INSERT a secas fallaría con
// unique_violation en la segunda elección de la misma persona, y un DELETE +
// INSERT dejaría una ventana sin fila que un canje concurrente leería como «no ha
// elegido nunca» — y le emitiría un token sin empresa a alguien que sí eligió.
//
// `updated_at` se reescribe EXPLÍCITAMENTE en el DO UPDATE: el DEFAULT now() solo
// alcanza al INSERT, así que sin esta línea la columna congelaría la fecha de la
// PRIMERA elección y diría algo falso.
func (r *ActiveTenantRepo) SetActiveTenant(ctx context.Context, userID, tenantID string) error {
	if _, err := r.db.ExecContext(ctx, `
		INSERT INTO public.user_active_tenant (user_id, tenant_id)
		VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE
		SET tenant_id = EXCLUDED.tenant_id, updated_at = now()
	`, userID, tenantID); err != nil {
		return fmt.Errorf("iam: guardando la empresa activa: %w", err)
	}
	return nil
}
