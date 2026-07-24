// Package memory provee implementaciones EN MEMORIA de los puertos out del IAM
// (UserRepo, RoleRepo, GrantRepo, RefreshRepo, APIKeyRepo, AuditRepo), seguras
// para concurrencia. Pensadas para tests unitarios CI-safe de los usecases (sin
// BD), imitando la semántica de la implementación Postgres: unicidad →
// domain.ErrConflict, ausencia → domain.ErrNotFound, filtrado por tenant.
//
// Cada tabla tiene su propio store (no un único tipo): los seis puertos declaran
// métodos homónimos con firmas distintas (Create/GetByID/List/Revoke/GetByHash),
// que Go no permite convivir en un mismo tipo. Store agrega los seis para un
// wiring cómodo en los tests.
package memory

import "github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"

// Store agrega los seis repositorios en memoria para el wiring de tests.
type Store struct {
	Users   *UserStore
	Roles   *RoleStore
	Grants  *GrantStore
	Refresh *RefreshStore
	APIKeys *APIKeyStore
	Audit   *AuditStore
}

// NewStore crea el agregado con los seis repositorios vacíos.
func NewStore() *Store {
	return &Store{
		Users:   NewUserStore(),
		Roles:   NewRoleStore(),
		Grants:  NewGrantStore(),
		Refresh: NewRefreshStore(),
		APIKeys: NewAPIKeyStore(),
		Audit:   NewAuditStore(),
	}
}

// removeGrant devuelve la lista sin el grant dado (comparación por valor).
func removeGrant(list []domain.Grant, g domain.Grant) []domain.Grant {
	out := make([]domain.Grant, 0, len(list))
	for _, ex := range list {
		if ex != g {
			out = append(out, ex)
		}
	}
	return out
}
