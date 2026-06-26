package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrNotFound se devuelve cuando una búsqueda no encuentra la fila pedida.
var ErrNotFound = errors.New("postgres: registro no encontrado")

// Tenant es la entidad raíz de multi-tenancy. Refleja public.tenants.
type Tenant struct {
	ID          string
	Slug        string
	DisplayName string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// TenantRepository es el contrato de acceso a tenants. Define la semilla del
// patrón de repositorios SQL raw; los repos de enroll/fleet/lease (T3/T4) lo
// replican. La implementación concreta es SQLTenantRepository.
type TenantRepository interface {
	// Create inserta un tenant a partir de su slug y display_name y devuelve la
	// fila completa (con id y timestamps generados por la BD).
	Create(ctx context.Context, slug, displayName string) (Tenant, error)
	// FindBySlug devuelve el tenant con el slug dado, o ErrNotFound.
	FindBySlug(ctx context.Context, slug string) (Tenant, error)
}

// SQLTenantRepository implementa TenantRepository con SQL raw sobre *sql.DB.
type SQLTenantRepository struct {
	db *sql.DB
}

// NewTenantRepository construye el repositorio sobre el pool dado.
func NewTenantRepository(db *sql.DB) *SQLTenantRepository {
	return &SQLTenantRepository{db: db}
}

// Create inserta un tenant y devuelve la fila resultante.
func (r *SQLTenantRepository) Create(ctx context.Context, slug, displayName string) (Tenant, error) {
	var t Tenant
	err := r.db.QueryRowContext(ctx, `
		INSERT INTO public.tenants (slug, display_name)
		VALUES ($1, $2)
		RETURNING id::text, slug, display_name, created_at, updated_at
	`, slug, displayName).Scan(&t.ID, &t.Slug, &t.DisplayName, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return Tenant{}, fmt.Errorf("postgres: creando tenant: %w", err)
	}
	return t, nil
}

// FindBySlug devuelve el tenant con el slug dado, o ErrNotFound si no existe.
func (r *SQLTenantRepository) FindBySlug(ctx context.Context, slug string) (Tenant, error) {
	var t Tenant
	err := r.db.QueryRowContext(ctx, `
		SELECT id::text, slug, display_name, created_at, updated_at
		FROM public.tenants
		WHERE slug = $1
	`, slug).Scan(&t.ID, &t.Slug, &t.DisplayName, &t.CreatedAt, &t.UpdatedAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Tenant{}, ErrNotFound
	case err != nil:
		return Tenant{}, fmt.Errorf("postgres: buscando tenant por slug: %w", err)
	}
	return t, nil
}
