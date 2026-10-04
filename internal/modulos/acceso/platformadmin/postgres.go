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
	"errors"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
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
type Repository struct{}

// ListTenants implementa TenantStore.ListTenants: ORDER BY created_at DESC, id DESC, con limit
// acotado a [1, 500] (≤0 ⇒ 50) y offset negativo ⇒ 0. Nunca devuelve nil.
func (r *Repository) ListTenants(ctx context.Context, limit, offset int) ([]TenantListItem, error) {
	panic(pendiente.Implementar("platformadmin.Repository.ListTenants"))
}

// GetTenant implementa TenantStore.GetTenant: tres consultas (fila, COUNT(DISTINCT edge_id) de
// fleet_sessions y features efectivas). ErrNotFound si no existe.
func (r *Repository) GetTenant(ctx context.Context, id string) (TenantDetail, error) {
	panic(pendiente.Implementar("platformadmin.Repository.GetTenant"))
}

// ExistsTenant implementa TenantStore.ExistsTenant con UNA consulta ligera (SELECT EXISTS).
func (r *Repository) ExistsTenant(ctx context.Context, id string) (bool, error) {
	panic(pendiente.Implementar("platformadmin.Repository.ExistsTenant"))
}

// CreateTenant implementa TenantStore.CreateTenant. Valida ANTES de tocar la base: slug o
// display_name vacíos ⇒ ErrInvalidInput sin consulta. La violación de unicidad del slug (23505)
// ⇒ ErrConflict envuelto con el slug.
func (r *Repository) CreateTenant(ctx context.Context, slug, displayName string, planID *string) (CreatedTenant, error) {
	panic(pendiente.Implementar("platformadmin.Repository.CreateTenant"))
}

// ListInstallations implementa TenantStore.ListInstallations: fleet_sessions agrupadas por
// edge_id con LEFT JOIN a leases, ORDER BY edge_id ASC. Nunca devuelve nil.
func (r *Repository) ListInstallations(ctx context.Context, tenantID string) ([]InstallationItem, error) {
	panic(pendiente.Implementar("platformadmin.Repository.ListInstallations"))
}
