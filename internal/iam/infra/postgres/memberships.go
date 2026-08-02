package iampostgres

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/out"
)

// MembershipRepo implementa out.MembershipRepo sobre public.tenant_members
// (migración 0037). La tabla no lleva FK hacia el usuario: su identidad vive en
// identity, en otra base de datos.
type MembershipRepo struct {
	db *sql.DB
}

// NewMembershipRepo construye el repositorio sobre el pool dado.
func NewMembershipRepo(db *sql.DB) *MembershipRepo { return &MembershipRepo{db: db} }

var _ out.MembershipRepo = (*MembershipRepo)(nil)

// TenantsOfUser implementa out.MembershipRepo. Ordena por created_at para que
// dos llamadas devuelvan siempre lo mismo; el orden NO se usa para elegir un
// tenant cuando hay varios (eso es un error explícito del exchange, no una
// preferencia).
func (r *MembershipRepo) TenantsOfUser(ctx context.Context, userID string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT tenant_id::text
		FROM public.tenant_members
		WHERE user_id = $1
		ORDER BY created_at, tenant_id
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("iam: leer membresías: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			_ = cerr
		}
	}()

	var tenants []string
	for rows.Next() {
		var tenantID string
		if scanErr := rows.Scan(&tenantID); scanErr != nil {
			return nil, fmt.Errorf("iam: leer membresías: %w", scanErr)
		}
		tenants = append(tenants, tenantID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iam: leer membresías: %w", err)
	}
	return tenants, nil
}
