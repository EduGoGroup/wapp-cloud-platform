// Porta internal/iam/infra/memory/role_store.go @ 9a77307

package memory

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
)

// Assignment es una fila de iam_user_roles vista desde el doble: el rol y su
// ámbito (TenantID nil = global). La devuelve AssignmentsOf.
type Assignment struct {
	RoleID   string
	TenantID *string
}

// RoleStore es el doble en memoria de iam_roles/iam_role_grants/iam_user_roles.
// Pasa outhelpertest.ContratoRoleRepo, la misma suite que el adaptador Postgres.
type RoleStore struct {
	mu        sync.Mutex
	roles     map[string]domain.Role
	grants    map[string][]domain.Grant // roleID → grants
	userRoles map[string][]Assignment   // userID → asignaciones
	// now es el reloj de created_at. Por defecto time.Now; WithClock lo sustituye.
	now func() time.Time
}

// NewRoleStore crea un RoleStore vacío —sin plantillas globales: quien las
// necesite, el rol transversal incluido, las siembra con Seed— y con el reloj real.
func NewRoleStore() *RoleStore {
	return &RoleStore{
		roles:     make(map[string]domain.Role),
		grants:    make(map[string][]domain.Grant),
		userRoles: make(map[string][]Assignment),
		now:       time.Now,
	}
}

// WithClock sustituye el reloj del store y lo devuelve, para encadenarlo tras el constructor
// (antes de usarlo: no es seguro cambiar el reloj con el store en uso). Un now nil deja el que
// tenía.
func (s *RoleStore) WithClock(now func() time.Time) *RoleStore {
	if now != nil {
		s.now = now
	}
	return s
}

var _ out.RoleRepo = (*RoleStore)(nil)

// Seed inserta un rol con sus grants directamente (para sembrar plantillas
// globales o cadenas con parent en tests). ID vacío → se asigna uno; created_at
// cero → el del reloj. No comprueba la unicidad del nombre. Devuelve el rol
// insertado.
func (s *RoleStore) Seed(r domain.Role, grants []domain.Grant) domain.Role {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.ID == "" {
		r.ID = uuid.NewString()
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = s.now()
	}
	s.roles[r.ID] = r
	if len(grants) > 0 {
		s.grants[r.ID] = append(s.grants[r.ID], grants...)
	}
	return r
}

// Create implementa out.RoleRepo con la unicidad de los dos índices parciales de
// iam_roles: el nombre es único entre las globales y único por tenant.
func (s *RoleStore) Create(_ context.Context, r domain.Role) (domain.Role, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ex := range s.roles {
		if ex.Name != r.Name {
			continue
		}
		if ex.TenantID == nil && r.TenantID == nil {
			return domain.Role{}, fmt.Errorf("%w: rol=%s", domain.ErrConflict, r.Name)
		}
		if ex.TenantID != nil && r.TenantID != nil && *ex.TenantID == *r.TenantID {
			return domain.Role{}, fmt.Errorf("%w: rol=%s", domain.ErrConflict, r.Name)
		}
	}
	r.ID = uuid.NewString()
	r.CreatedAt = s.now()
	s.roles[r.ID] = r
	return r, nil
}

// GetByID implementa out.RoleRepo.
func (s *RoleStore) GetByID(_ context.Context, id string) (domain.Role, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.roles[id]
	if !ok {
		return domain.Role{}, domain.ErrNotFound
	}
	return r, nil
}

// List implementa out.RoleRepo (roles del tenant + plantillas globales), en el
// orden del adaptador (created_at) y con el id como desempate, para que el doble
// sea determinista pese al recorrido aleatorio del mapa.
func (s *RoleStore) List(_ context.Context, tenantID string) ([]domain.Role, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var res []domain.Role
	for _, r := range s.roles {
		if r.TenantID == nil || *r.TenantID == tenantID {
			res = append(res, r)
		}
	}
	slices.SortFunc(res, func(a, b domain.Role) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return res, nil
}

// ParentOf implementa out.RoleRepo: ok=false para un rol raíz y para uno que no
// existe.
func (s *RoleStore) ParentOf(_ context.Context, id string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.roles[id]
	if !ok || r.ParentRoleID == nil || *r.ParentRoleID == "" {
		return "", false, nil
	}
	return *r.ParentRoleID, true, nil
}

// GrantsOf implementa out.RoleRepo. Devuelve una copia.
func (s *RoleStore) GrantsOf(_ context.Context, roleID string) ([]domain.Grant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.Grant(nil), s.grants[roleID]...), nil
}

// AddGrant implementa out.RoleRepo (idempotente por pattern+effect).
func (s *RoleStore) AddGrant(_ context.Context, roleID string, g domain.Grant) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ex := range s.grants[roleID] {
		if ex == g {
			return nil
		}
	}
	s.grants[roleID] = append(s.grants[roleID], g)
	return nil
}

// RemoveGrant implementa out.RoleRepo (no-op si no estaba).
func (s *RoleStore) RemoveGrant(_ context.Context, roleID string, g domain.Grant) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.grants[roleID] = removeGrant(s.grants[roleID], g)
	return nil
}

// RolesOfUser implementa out.RoleRepo: primero los acotados al tenant y luego los
// globales, sin repetir un rol (el adaptador también pone primero los acotados).
// Con tenantID "" solo los globales.
func (s *RoleStore) RolesOfUser(_ context.Context, userID, tenantID string) ([]domain.Role, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var res []domain.Role
	seen := make(map[string]bool)

	// Primero los asignados específicamente al tenant
	if tenantID != "" {
		for _, a := range s.userRoles[userID] {
			if a.TenantID != nil && *a.TenantID == tenantID {
				if r, ok := s.roles[a.RoleID]; ok && !seen[r.ID] {
					seen[r.ID] = true
					res = append(res, r)
				}
			}
		}
	}
	// Luego los asignados globalmente (tenantID == nil)
	for _, a := range s.userRoles[userID] {
		if a.TenantID == nil {
			if r, ok := s.roles[a.RoleID]; ok && !seen[r.ID] {
				seen[r.ID] = true
				res = append(res, r)
			}
		}
	}
	return res, nil
}

// checkAssignmentScope es LA MISMA GUARDA DE ÁMBITO que el adaptador Postgres
// (validarAmbitoDeAsignacion, Plan 047 · Ola 5 · T5.6): el ámbito global (tenant
// nil o "") es del rol transversal y de ningún otro. Si el doble no la tuviera,
// un test unitario daría por buena una asignación global que la base rechaza
// —que es exactamente el defecto que T5.6 vino a cerrar, visto desde el otro
// lado del espejo—. La usan AssignToUser y el canje (RedeemStore), las dos vías
// que escriben en iam_user_roles.
func checkAssignmentScope(roleID string, tenantID *string) error {
	if (tenantID == nil || *tenantID == "") && roleID != domain.TransversalRoleID {
		return fmt.Errorf("%w: rol=%s", domain.ErrRoleScopeInvalid, roleID)
	}
	return nil
}

// AssignToUser implementa out.RoleRepo, opcionalmente acotado a un tenant
// (D-056.11): tenantID nil asigna GLOBAL; tenantID no nil acota la
// asignación a esa empresa. Idempotente por (roleID, tenantID), igual que las
// dos UNIQUE de iam_user_roles que emula. Un rol de empresa con ámbito global →
// domain.ErrRoleScopeInvalid sin escribir (checkAssignmentScope).
func (s *RoleStore) AssignToUser(_ context.Context, userID, roleID string, tenantID *string) error {
	if err := checkAssignmentScope(roleID, tenantID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.assignLocked(userID, roleID, tenantID)
	return nil
}

// assignLocked escribe la asignación si no estaba (sin guarda). El llamante tiene
// el candado.
func (s *RoleStore) assignLocked(userID, roleID string, tenantID *string) {
	for _, a := range s.userRoles[userID] {
		if a.RoleID == roleID && samePtrValue(a.TenantID, tenantID) {
			return
		}
	}
	var scope *string
	if tenantID != nil {
		v := *tenantID
		scope = &v
	}
	s.userRoles[userID] = append(s.userRoles[userID], Assignment{RoleID: roleID, TenantID: scope})
}

// SeedAssignment (era SeedAsignacion) escribe una asignación de rol SIN pasar
// por la guarda de ámbito (helper de tests). Es a AssignToUser lo que
// MembershipStore.Seed es a Add, y existe por la misma razón: fabricar el estado
// de partida, incluido el que la guarda ya no dejaría escribir.
//
// 🔴 El estado que fabrica es REAL, no imaginario: en la base de UAT hay filas
// con tenant_id NULL de un rol que no es el transversal, escritas a mano por SQL
// directo antes de que existiera la guarda (Plan 047 · Ola 5 · T5.6). Un doble
// que no supiera representarlas dejaría sin poder probar qué hace el sistema
// cuando se las encuentra.
//
// Repetir la misma terna no la duplica, igual que AssignToUser.
func (s *RoleStore) SeedAssignment(userID, roleID string, tenantID *string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.assignLocked(userID, roleID, tenantID)
}

// AssignmentsOf devuelve una copia de las asignaciones del usuario, en el orden
// en que se escribieron (helper de tests): es como se ve el ÁMBITO de cada una,
// que RolesOfUser no distingue.
func (s *RoleStore) AssignmentsOf(userID string) []Assignment {
	s.mu.Lock()
	defer s.mu.Unlock()
	res := make([]Assignment, 0, len(s.userRoles[userID]))
	for _, a := range s.userRoles[userID] {
		if a.TenantID != nil {
			v := *a.TenantID
			a.TenantID = &v
		}
		res = append(res, a)
	}
	return res
}

// samePtrValue compara dos *string por VALOR: nil == nil, y dos no-nil son
// iguales si sus valores lo son (nunca por dirección de memoria).
func samePtrValue(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// UnassignFromUser implementa out.RoleRepo, simétrico a AssignToUser: retira
// SOLO la asignación cuyo tenant coincide por VALOR con el pedido (nil retira la
// global), igual que el `IS NOT DISTINCT FROM` del adaptador Postgres.
func (s *RoleStore) UnassignFromUser(_ context.Context, userID, roleID string, tenantID *string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	filtered := make([]Assignment, 0, len(s.userRoles[userID]))
	for _, a := range s.userRoles[userID] {
		if a.RoleID != roleID || !samePtrValue(a.TenantID, tenantID) {
			filtered = append(filtered, a)
		}
	}
	s.userRoles[userID] = filtered
	return nil
}
