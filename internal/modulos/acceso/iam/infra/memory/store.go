// Porta internal/iam/infra/memory/store.go @ 9a77307

// Package memory provee implementaciones EN MEMORIA de los puertos out del IAM
// (MembershipRepo, RoleRepo, GrantRepo, AuditRepo, InvitationRepo,
// ActiveTenantRepo e InvitationRedeemRepo), seguras para concurrencia. Pensadas
// para tests unitarios CI-safe de los usecases (sin BD), imitando la semántica de
// la implementación Postgres: unicidad → domain.ErrConflict, ausencia →
// domain.ErrNotFound, filtrado por tenant, los mismos órdenes. Cada doble pasa la
// suite de su puerto (iam/ports/out/outhelpertest), la misma que pasará el
// adaptador Postgres en F9; ningún código de producción los usa.
//
// Cada tabla tiene su propio store (no un único tipo): los puertos declaran
// métodos homónimos con firmas distintas (Create/GetByID/List), que Go no
// permite convivir en un mismo tipo. Store los agrega para un wiring cómodo en
// los tests. RedeemStore (nuevo) es el canje: no tiene tabla propia que no sea la
// bandeja de solicitudes, y escribe en las de los otros tres.
//
// Los stores que fechan algo (AuditStore, InvitationStore, MembershipStore,
// RoleStore) usan time.Now salvo que se les inyecte otro reloj con WithClock: los
// tests no dependen del reloj real.
//
// Ya no hay dobles de usuarios, refresh ni api-keys: esos puertos murieron con
// el IAM propio de wApp (identity Plan 003 · Ola 5).
package memory

import "time"

// Store agrega los repositorios en memoria para el wiring de tests. Redeem
// escribe sobre los MISMOS Invitations, Memberships y Roles del agregado: un canje
// se ve después en ellos.
type Store struct {
	Roles       *RoleStore
	Grants      *GrantStore
	Audit       *AuditStore
	Memberships *MembershipStore
	Invitations *InvitationStore
	// ActiveTenants es la empresa ACTIVA por usuario (tabla user_active_tenant,
	// Plan 047 · Ola 5 · T5.1). La consume el canje cuando hay dos o más
	// membresías.
	ActiveTenants *ActiveTenantStore
	// Redeem es el canje de invitaciones (out.InvitationRedeemRepo) sobre los
	// stores de arriba.
	Redeem *RedeemStore
}

// NewStore crea el agregado con todos los repositorios vacíos, el reloj real y
// sin resolver de derechos (para multi_empresa: Memberships.WithFeatures).
func NewStore() *Store {
	s := &Store{
		Roles:         NewRoleStore(),
		Grants:        NewGrantStore(),
		Audit:         NewAuditStore(),
		Memberships:   NewMembershipStore(),
		Invitations:   NewInvitationStore(),
		ActiveTenants: NewActiveTenantStore(),
	}
	s.Redeem = NewRedeemStore(s.Invitations, s.Memberships, s.Roles)
	return s
}

// WithClock inyecta el mismo reloj en todos los stores que fechan algo y devuelve
// el agregado, para encadenarlo tras NewStore (antes de usarlo). Un now nil deja
// los que tenían.
func (s *Store) WithClock(now func() time.Time) *Store {
	s.Roles.WithClock(now)
	s.Audit.WithClock(now)
	s.Memberships.WithClock(now)
	s.Invitations.WithClock(now)
	return s
}
