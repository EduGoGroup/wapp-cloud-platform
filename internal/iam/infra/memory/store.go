// Package memory provee implementaciones EN MEMORIA de los puertos out del IAM
// (RoleRepo, GrantRepo, AuditRepo, MembershipRepo), seguras para concurrencia.
// Pensadas para tests unitarios CI-safe de los usecases (sin BD), imitando la
// semántica de la implementación Postgres: unicidad → domain.ErrConflict,
// ausencia → domain.ErrNotFound, filtrado por tenant.
//
// Cada tabla tiene su propio store (no un único tipo): los puertos declaran
// métodos homónimos con firmas distintas (Create/GetByID/List), que Go no
// permite convivir en un mismo tipo. Store los agrega para un wiring cómodo en
// los tests.
//
// Ya no hay dobles de usuarios, refresh ni api-keys: esos puertos murieron con
// el IAM propio de wApp (identity Plan 003 · Ola 5).
package memory

import "github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"

// Store agrega los repositorios en memoria para el wiring de tests.
type Store struct {
	Roles       *RoleStore
	Grants      *GrantStore
	Audit       *AuditStore
	Memberships *MembershipStore
	Invitations *InvitationStore
}

// NewStore crea el agregado con todos los repositorios vacíos.
func NewStore() *Store {
	return &Store{
		Roles:       NewRoleStore(),
		Grants:      NewGrantStore(),
		Audit:       NewAuditStore(),
		Memberships: NewMembershipStore(),
		Invitations: NewInvitationStore(),
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
