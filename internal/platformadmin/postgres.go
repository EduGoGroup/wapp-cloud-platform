// Package platformadmin implementa el repositorio y handlers del plano de administración
// de plataforma. Este paquete contiene las únicas consultas cross-tenant del backend
// (no acotadas por un tenant_id de negocio), autorizadas exclusivamente para operadores
// con rol platform_admin (permisos *.any).
package platformadmin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

const maxLimit = 500
const pgUniqueViolation = "23505"

var (
	// ErrNotFound se devuelve cuando el recurso no existe.
	ErrNotFound = errors.New("platformadmin: recurso no encontrado")
	// ErrConflict se devuelve ante colisión de unicidad (p. ej. slug duplicado).
	ErrConflict = errors.New("platformadmin: recurso ya existe o conflicto de unicidad")
	// ErrInvalidInput se devuelve ante datos de entrada no válidos.
	ErrInvalidInput = errors.New("platformadmin: entrada inválida")
)

// TenantListItem representa un elemento en la lista resumida de empresas.
type TenantListItem struct {
	ID          string     `json:"id"`
	Slug        string     `json:"slug"`
	DisplayName string     `json:"display_name"`
	PlanID      *string    `json:"plan_id"`
	RevokedAt   *time.Time `json:"revoked_at"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// TenantDetail contiene toda la información de una empresa, sus estadísticas y features efectivas.
type TenantDetail struct {
	ID                 string     `json:"id"`
	Slug               string     `json:"slug"`
	DisplayName        string     `json:"display_name"`
	PlanID             *string    `json:"plan_id"`
	RevokedAt          *time.Time `json:"revoked_at"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	InstallationsCount int        `json:"installations_count"`
	Features           []string   `json:"features"`
}

// CreatedTenant representa la respuesta de creación de una empresa.
type CreatedTenant struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
}

// InstallationItem representa una instalación/Edge vinculada a un tenant.
type InstallationItem struct {
	EdgeID       string     `json:"edge_id"`
	Sessions     int        `json:"sessions"`
	LastSeenAt   *time.Time `json:"last_seen_at"`
	LeaseRevoked bool       `json:"lease_revoked"`
}

// Repository expone las operaciones cross-tenant sobre la base de datos PostgreSQL.
type Repository struct {
	db *sql.DB
}

// NewRepository construye el repositorio sobre el pool dado.
func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

// ListTenants devuelve los tenants paginados. El parámetro limit se acota entre 1 y 500 (default 50).
func (r *Repository) ListTenants(ctx context.Context, limit, offset int) ([]TenantListItem, error) {
	if limit <= 0 {
		limit = 50
	} else if limit > maxLimit {
		limit = maxLimit
	}
	if offset < 0 {
		offset = 0
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT id::text, slug, display_name, plan_id, revoked_at, created_at, updated_at
		FROM public.tenants
		ORDER BY created_at DESC
		LIMIT $1 OFFSET $2
	`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("platformadmin: listar tenants: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			_ = cerr
		}
	}()

	items := []TenantListItem{}
	for rows.Next() {
		var (
			item      TenantListItem
			planID    sql.NullString
			revokedAt sql.NullTime
		)
		if err := rows.Scan(&item.ID, &item.Slug, &item.DisplayName, &planID, &revokedAt, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, fmt.Errorf("platformadmin: escanear tenant: %w", err)
		}
		if planID.Valid {
			item.PlanID = &planID.String
		}
		if revokedAt.Valid {
			item.RevokedAt = &revokedAt.Time
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("platformadmin: iterar tenants: %w", err)
	}
	return items, nil
}

// GetTenant devuelve el detalle de un tenant, su conteo de instalaciones y features activas.
func (r *Repository) GetTenant(ctx context.Context, id string) (TenantDetail, error) {
	var (
		detail    TenantDetail
		planID    sql.NullString
		revokedAt sql.NullTime
	)
	err := r.db.QueryRowContext(ctx, `
		SELECT id::text, slug, display_name, plan_id, revoked_at, created_at, updated_at
		FROM public.tenants
		WHERE id = $1
	`, id).Scan(&detail.ID, &detail.Slug, &detail.DisplayName, &planID, &revokedAt, &detail.CreatedAt, &detail.UpdatedAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return TenantDetail{}, ErrNotFound
	case err != nil:
		return TenantDetail{}, fmt.Errorf("platformadmin: leer tenant: %w", err)
	}

	if planID.Valid {
		detail.PlanID = &planID.String
	}
	if revokedAt.Valid {
		detail.RevokedAt = &revokedAt.Time
	}

	// Conteo de instalaciones (edges distintos en fleet_sessions)
	err = r.db.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT edge_id)
		FROM public.fleet_sessions
		WHERE tenant_id = $1
	`, id).Scan(&detail.InstallationsCount)
	if err != nil {
		return TenantDetail{}, fmt.Errorf("platformadmin: contar instalaciones: %w", err)
	}

	// Resolver features efectivas (plan + tenant overrides)
	plan := "basic"
	if detail.PlanID != nil && *detail.PlanID != "" {
		plan = *detail.PlanID
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT f.feature
		FROM (
			SELECT pf.feature
			FROM public.plan_features pf
			WHERE pf.plan_id = $2
			UNION
			SELECT tf.feature
			FROM public.tenant_features tf
			WHERE tf.tenant_id = $1 AND tf.enabled
		) AS f
		WHERE NOT EXISTS (
			SELECT 1
			FROM public.tenant_features apagada
			WHERE apagada.tenant_id = $1
			  AND apagada.feature = f.feature
			  AND NOT apagada.enabled
		)
	`, id, plan)
	if err != nil {
		return TenantDetail{}, fmt.Errorf("platformadmin: leer features efectivas: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			_ = cerr
		}
	}()

	detail.Features = []string{}
	for rows.Next() {
		var feat string
		if err := rows.Scan(&feat); err != nil {
			return TenantDetail{}, fmt.Errorf("platformadmin: escanear feature: %w", err)
		}
		detail.Features = append(detail.Features, feat)
	}
	if err := rows.Err(); err != nil {
		return TenantDetail{}, fmt.Errorf("platformadmin: iterar features: %w", err)
	}
	slices.Sort(detail.Features)

	return detail, nil
}

// CreateTenant inserta una nueva empresa.
func (r *Repository) CreateTenant(ctx context.Context, slug, displayName string, planID *string) (CreatedTenant, error) {
	if slug == "" || displayName == "" {
		return CreatedTenant{}, fmt.Errorf("%w: slug y display_name son requeridos", ErrInvalidInput)
	}

	var pID *string
	if planID != nil && *planID != "" {
		pID = planID
	}

	var created CreatedTenant
	err := r.db.QueryRowContext(ctx, `
		INSERT INTO public.tenants (id, slug, display_name, plan_id)
		VALUES (gen_random_uuid(), $1, $2, $3)
		RETURNING id::text, slug
	`, slug, displayName, pID).Scan(&created.ID, &created.Slug)
	if err != nil {
		if isUniqueViolation(err) {
			return CreatedTenant{}, fmt.Errorf("%w: slug=%s", ErrConflict, slug)
		}
		return CreatedTenant{}, fmt.Errorf("platformadmin: crear tenant: %w", err)
	}
	return created, nil
}

// ListInstallations lista las instalaciones (edges) de un tenant con sus sesiones y estado del lease.
func (r *Repository) ListInstallations(ctx context.Context, tenantID string) ([]InstallationItem, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT 
			f.edge_id,
			COUNT(f.session_id) as sessions,
			MAX(f.last_seen_at) as last_seen_at,
			COALESCE(bool_or(l.revoked), false) as lease_revoked
		FROM public.fleet_sessions f
		LEFT JOIN public.leases l ON l.tenant_id = f.tenant_id AND l.edge_id = f.edge_id
		WHERE f.tenant_id = $1
		GROUP BY f.edge_id
		ORDER BY f.edge_id ASC
	`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("platformadmin: listar instalaciones: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			_ = cerr
		}
	}()

	items := []InstallationItem{}
	for rows.Next() {
		var (
			item       InstallationItem
			lastSeenAt sql.NullTime
		)
		if err := rows.Scan(&item.EdgeID, &item.Sessions, &lastSeenAt, &item.LeaseRevoked); err != nil {
			return nil, fmt.Errorf("platformadmin: escanear instalacion: %w", err)
		}
		if lastSeenAt.Valid {
			item.LastSeenAt = &lastSeenAt.Time
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("platformadmin: iterar instalaciones: %w", err)
	}
	return items, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation
}
