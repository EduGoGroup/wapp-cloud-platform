// Package platformadmin es el plano de administración de PLATAFORMA: la bandeja de empresas, sus
// instalaciones, los códigos de enrolamiento, la bandeja de solicitudes de acceso y el alta
// pública (POST /api/v1/signup). Contiene las únicas consultas cross-tenant del backend (no
// acotadas por un tenant_id de negocio), autorizadas exclusivamente para operadores con rol
// platform_admin (permisos *.any): todo handler de plataforma corta con
// httpapi.EnforcePlatformCaller ANTES de tocar el almacén.
//
// Los handlers reciben los PUERTOS de ports.go (TenantStore, AccessRequestStore), no el
// *Repository de Postgres (D-F2-3): así la lógica de la aprobación (validación, cerrojo de
// wapp.platform, reintento que converge, unión de systems) se prueba con el doble en memoria de
// platformadminhelpertest, sin BD.
//
// 🔒 I-CP-5: el arranque monta estos handlers INLINE e importa este paquete SIN alias: el candado
// de permisos de plataforma los reconoce por el texto "platformadmin." (trampa T-3 de F2).
//
// Porta internal/platformadmin/postgres.go @ 9a77307.
package platformadmin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	iampostgres "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/infra/postgres"
)

// Los centinelas de los adaptadores. Su texto es observable (los envuelven los errores que llegan
// a los logs) y se copia literal del viejo.
var (
	// ErrNotFound se devuelve cuando el recurso no existe: un tenant en GetTenant, una solicitud de
	// acceso en LookupAccessRequestStatus y RejectAccessRequest.
	ErrNotFound = errors.New("platformadmin: recurso no encontrado")
	// ErrConflict se devuelve ante colisión de unicidad (slug duplicado en CreateTenant) y ante
	// una solicitud que ya no está en el estado que la operación exige (ya resuelta, o la persona
	// ya pertenece a otra empresa).
	ErrConflict = errors.New("platformadmin: recurso ya existe o conflicto de unicidad")
	// ErrInvalidInput se devuelve ante datos de entrada no válidos (campos requeridos vacíos,
	// origin fuera de bff|edge, rol que no existe, motivo de rechazo en blanco).
	ErrInvalidInput = errors.New("platformadmin: entrada inválida")
)

// TenantListItem es un elemento de la lista resumida de empresas (GET /admin/tenants). Los tags
// JSON son contrato con la consola de plataforma. PlanID y RevokedAt son null cuando la columna es
// NULL.
type TenantListItem struct {
	ID          string     `json:"id"`
	Slug        string     `json:"slug"`
	DisplayName string     `json:"display_name"`
	PlanID      *string    `json:"plan_id"`
	RevokedAt   *time.Time `json:"revoked_at"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// TenantDetail es el detalle de una empresa (GET /admin/tenants/{id}): la fila, el número de
// instalaciones (edges distintos con sesión) y sus features EFECTIVAS (plan ∪ overrides
// encendidos, menos overrides apagados; plan NULL ⇒ 'basic'), ordenadas y nunca null.
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

// CreatedTenant es la respuesta de la creación de una empresa: su id (UUID que genera la base) y
// su slug.
type CreatedTenant struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
}

// InstallationItem es una instalación (un Edge) de una empresa: cuántas sesiones tiene, la última
// señal de cualquiera de ellas (null si ninguna dio señal) y si su lease está revocado (false si
// no hay lease).
type InstallationItem struct {
	EdgeID       string     `json:"edge_id"`
	Sessions     int        `json:"sessions"`
	LastSeenAt   *time.Time `json:"last_seen_at"`
	LeaseRevoked bool       `json:"lease_revoked"`
}

// Repository es el adaptador Postgres de TenantStore (este fichero) y de AccessRequestStore
// (access_requests_postgres.go): SQL crudo sobre database/sql, sin ORM. Las promesas de cada
// método son las del puerto; aquí solo se añade lo propio del adaptador.
type Repository struct {
	db *sql.DB
	// features es el resolver de derechos que ExecuteApprovalTx pasa a GrantTenantAccess (ver
	// NewRepository).
	features iampostgres.FeatureResolver
}

// NewRepository construye el repositorio sobre el pool db. No valida sus argumentos ni toca la
// base: con db nil devuelve igualmente un Repository (que fallará en la primera consulta).
//
// features es el resolver de derechos comerciales, y el repositorio lo usa para UNA sola cosa:
// viaja tal cual hasta iampostgres.GrantTenantAccess cuando ExecuteApprovalTx da de alta (Plan
// 047 · Ola 5 · T5.2): aprobar es DAR DE ALTA, y el desenlace de un alta depende del derecho
// multi_empresa de la empresa de destino. nil no desactiva la regla: la deja contestando que no
// (la persona que ya es de otra empresa no entra), el extremo fail-closed de entitlements.
func NewRepository(db *sql.DB, features iampostgres.FeatureResolver) *Repository {
	return &Repository{db: db, features: features}
}

// ListTenants implementa TenantStore.ListTenants: ORDER BY created_at DESC, id DESC, con limit
// acotado a [1, 500] (≤0 ⇒ 50) y offset negativo ⇒ 0. Nunca devuelve nil.
//
// El ORDER BY lleva `id DESC` como desempate: `created_at` NO es único (el seed de varios tenants
// en la misma transacción comparte `now()`), así que sin un segundo criterio estable, dos páginas
// consecutivas (limit=1&offset=0 y offset=1) pueden devolver la MISMA fila empatada y omitir la
// otra -- el orden entre empates de un ORDER BY de una sola columna no está garantizado entre
// ejecuciones.
func (r *Repository) ListTenants(ctx context.Context, limit, offset int) ([]TenantListItem, error) {
	limit, offset = clampPage(limit, offset)

	rows, err := r.db.QueryContext(ctx, `
		SELECT id::text, slug, display_name, plan_id, revoked_at, created_at, updated_at
		FROM public.tenants
		ORDER BY created_at DESC, id DESC
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

// GetTenant implementa TenantStore.GetTenant: tres consultas (fila, COUNT(DISTINCT edge_id) de
// fleet_sessions y features efectivas). ErrNotFound si no existe.
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

// ExistsTenant implementa TenantStore.ExistsTenant con UNA consulta ligera (SELECT EXISTS).
// Está pensada para los handlers que solo necesitan decidir un 404 antes de una operación
// (listar instalaciones, emitir un código de enrolamiento): llamar a GetTenant para eso paga tres
// consultas (fila + COUNT(DISTINCT edge_id) + features efectivas) que nadie usa.
func (r *Repository) ExistsTenant(ctx context.Context, id string) (bool, error) {
	var exists bool
	err := r.db.QueryRowContext(ctx, `
		SELECT EXISTS(SELECT 1 FROM public.tenants WHERE id = $1)
	`, id).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("platformadmin: comprobar existencia de tenant: %w", err)
	}
	return exists, nil
}

// CreateTenant implementa TenantStore.CreateTenant. Valida ANTES de tocar la base: slug o
// display_name vacíos ⇒ ErrInvalidInput sin consulta. La violación de unicidad del slug (23505)
// ⇒ ErrConflict envuelto con el slug.
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

// ListInstallations implementa TenantStore.ListInstallations: fleet_sessions agrupadas por
// edge_id con LEFT JOIN a leases, ORDER BY edge_id ASC. Nunca devuelve nil.
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

// maxLimit es el tope de una página de empresas.
const maxLimit = 500

// defaultLimit es la página de empresas cuando no se pide una válida.
const defaultLimit = 50

// pgUniqueViolation es el SQLSTATE de una violación de unicidad.
const pgUniqueViolation = "23505"

// clampPage acota una página de empresas como la promete TenantStore.ListTenants: limit ≤ 0 ⇒ 50,
// limit > 500 ⇒ 500, offset < 0 ⇒ 0. La usan el adaptador (la página que pide) y
// ListTenantsHandler (la que responde): en el viejo eran dos copias de la misma regla.
func clampPage(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = defaultLimit
	} else if limit > maxLimit {
		limit = maxLimit
	}
	return limit, max(offset, 0)
}

// isUniqueViolation informa si err es (o envuelve) una violación de unicidad de Postgres.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation
}
