// Nuevo (D-F2-3): el doble en memoria de los puertos de platformadmin. No tiene fichero viejo:
// el viejo no tenía puertos y sus tests iban contra Postgres. Se alinea con lo que hace el
// adaptador Postgres (Repository), que es lo que prometen los puertos, no con atajos de un doble.

package platformadminhelpertest

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/platformadmin"
)

// Fake implementa en memoria platformadmin.TenantStore, platformadmin.AccessRequestStore y
// State, sobre UN mismo estado (como Repository sobre una misma base). Es seguro para uso
// concurrente. Nace vacío: sin empresas, sin roles y sin solicitudes.
//
// Lo que reproduce de Postgres, y la suite Contrato afirma: el orden de los listados (empresas
// por created_at DESC, id DESC; solicitudes por created_at ASC; instalaciones por edge_id), la
// idempotencia de la solicitud pendiente, el «o todo o nada» de ExecuteApprovalTx y la regla de
// una sola empresa sin multi_empresa. Su reloj es propio y avanza un microsegundo por escritura
// (la precisión de timestamptz), empezando el 2026-01-01 UTC: dos altas seguidas nunca empatan.
//
// Lo que NO reproduce: la resolución de features de GetTenant (devuelve las que se le den con
// SetFeatures, ordenadas y sin repetir; ninguna por defecto) y los errores de codificación de un
// id que no es UUID (Postgres los da; el Fake contesta como a uno que no existe).
type Fake struct {
	mu        sync.Mutex
	clock     time.Time
	tenants   map[string]*fakeTenant
	features  map[string][]string
	multi     map[string]bool
	sessions  map[sessionKey]*time.Time
	leases    map[edgeKey]bool
	requests  []*RequestRow
	requestID map[*RequestRow]string
	roles     []Role
	members   map[accessKey]bool
	userRoles map[accessKey][]string
	failures  map[string]error
}

type fakeTenant struct {
	item platformadmin.TenantListItem
}

type sessionKey struct{ tenant, edge, session string }

type edgeKey struct{ tenant, edge string }

type accessKey struct{ user, tenant string }

// fakeEpoch es el instante en que arranca el reloj de un Fake.
var fakeEpoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// NewFake devuelve un Fake vacío.
func NewFake() *Fake {
	return &Fake{
		clock:     fakeEpoch,
		tenants:   map[string]*fakeTenant{},
		features:  map[string][]string{},
		multi:     map[string]bool{},
		sessions:  map[sessionKey]*time.Time{},
		leases:    map[edgeKey]bool{},
		requestID: map[*RequestRow]string{},
		members:   map[accessKey]bool{},
		userRoles: map[accessKey][]string{},
		failures:  map[string]error{},
	}
}

// AddRole registra un rol asignable (el equivalente de una fila de public.iam_roles). Un id o un
// nombre repetidos no se comprueban: ResolveRoleID devuelve el primero que case.
func (f *Fake) AddRole(id, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.roles = append(f.roles, Role{ID: id, Name: name})
}

// SetFeatures fija las features efectivas que GetTenant devuelve para la empresa tenantID.
func (f *Fake) SetFeatures(tenantID string, features ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.features[tenantID] = slices.Clone(features)
}

// GrantMultiCompany concede multi_empresa a la empresa tenantID: desde ahí, ExecuteApprovalTx
// deja entrar en ella a quien ya es miembro de otra.
func (f *Fake) GrantMultiCompany(tenantID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.multi[tenantID] = true
}

// Fail hace que, desde ahora, el método del puerto llamado method (p. ej. "ListTenants")
// devuelva err, con sus demás resultados a cero y sin tocar el estado. Con err nil deja de
// fallar. Es para probar las ramas de error de los consumidores; los métodos de State no fallan.
func (f *Fake) Fail(method string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err == nil {
		delete(f.failures, method)
		return
	}
	f.failures[method] = err
}

// now devuelve el instante del reloj propio y lo adelanta un microsegundo. Con f.mu tomado.
func (f *Fake) now() time.Time {
	at := f.clock
	f.clock = f.clock.Add(time.Microsecond)
	return at
}

// ── TenantStore ──────────────────────────────────────────────────────────────────────────────

// ListTenants implementa platformadmin.TenantStore.
func (f *Fake) ListTenants(_ context.Context, limit, offset int) ([]platformadmin.TenantListItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failures["ListTenants"]; err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	} else if limit > 500 {
		limit = 500
	}
	offset = max(offset, 0)
	all := make([]platformadmin.TenantListItem, 0, len(f.tenants))
	for _, tn := range f.tenants {
		all = append(all, copyTenant(tn.item))
	}
	slices.SortFunc(all, func(a, b platformadmin.TenantListItem) int {
		if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(b.ID, a.ID)
	})
	if offset >= len(all) {
		return []platformadmin.TenantListItem{}, nil
	}
	return all[offset:min(offset+limit, len(all))], nil
}

// GetTenant implementa platformadmin.TenantStore.
func (f *Fake) GetTenant(_ context.Context, id string) (platformadmin.TenantDetail, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failures["GetTenant"]; err != nil {
		return platformadmin.TenantDetail{}, err
	}
	tn, ok := f.tenants[id]
	if !ok {
		return platformadmin.TenantDetail{}, platformadmin.ErrNotFound
	}
	it := copyTenant(tn.item)
	edges := map[string]bool{}
	for k := range f.sessions {
		if k.tenant == id {
			edges[k.edge] = true
		}
	}
	features := slices.Clone(f.features[id])
	slices.Sort(features)
	features = slices.Compact(features)
	if features == nil {
		features = []string{}
	}
	return platformadmin.TenantDetail{
		ID: it.ID, Slug: it.Slug, DisplayName: it.DisplayName, PlanID: it.PlanID, RevokedAt: it.RevokedAt,
		CreatedAt: it.CreatedAt, UpdatedAt: it.UpdatedAt, InstallationsCount: len(edges), Features: features,
	}, nil
}

// ExistsTenant implementa platformadmin.TenantStore.
func (f *Fake) ExistsTenant(_ context.Context, id string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failures["ExistsTenant"]; err != nil {
		return false, err
	}
	_, ok := f.tenants[id]
	return ok, nil
}

// CreateTenant implementa platformadmin.TenantStore.
func (f *Fake) CreateTenant(_ context.Context, slug, displayName string, planID *string) (platformadmin.CreatedTenant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failures["CreateTenant"]; err != nil {
		return platformadmin.CreatedTenant{}, err
	}
	if slug == "" || displayName == "" {
		return platformadmin.CreatedTenant{}, fmt.Errorf("%w: slug y display_name son requeridos", platformadmin.ErrInvalidInput)
	}
	for _, tn := range f.tenants {
		if tn.item.Slug == slug {
			return platformadmin.CreatedTenant{}, fmt.Errorf("%w: slug=%s", platformadmin.ErrConflict, slug)
		}
	}
	var plan *string
	if planID != nil && *planID != "" {
		plan = new(string)
		*plan = *planID
	}
	id := f.addTenantLocked(slug, displayName, plan, f.now())
	return platformadmin.CreatedTenant{ID: id, Slug: slug}, nil
}

// addTenantLocked da de alta una empresa con id nuevo. Con f.mu tomado.
func (f *Fake) addTenantLocked(slug, displayName string, plan *string, at time.Time) string {
	id := uuid.NewString()
	f.tenants[id] = &fakeTenant{item: platformadmin.TenantListItem{
		ID: id, Slug: slug, DisplayName: displayName, PlanID: plan, CreatedAt: at, UpdatedAt: at,
	}}
	return id
}

// ListInstallations implementa platformadmin.TenantStore.
func (f *Fake) ListInstallations(_ context.Context, tenantID string) ([]platformadmin.InstallationItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failures["ListInstallations"]; err != nil {
		return nil, err
	}
	byEdge := map[string]*platformadmin.InstallationItem{}
	for k, seen := range f.sessions {
		if k.tenant != tenantID {
			continue
		}
		it, ok := byEdge[k.edge]
		if !ok {
			it = &platformadmin.InstallationItem{EdgeID: k.edge, LeaseRevoked: f.leases[edgeKey{tenantID, k.edge}]}
			byEdge[k.edge] = it
		}
		it.Sessions++
		if seen != nil && (it.LastSeenAt == nil || seen.After(*it.LastSeenAt)) {
			at := *seen
			it.LastSeenAt = &at
		}
	}
	items := make([]platformadmin.InstallationItem, 0, len(byEdge))
	for _, it := range byEdge {
		items = append(items, *it)
	}
	slices.SortFunc(items, func(a, b platformadmin.InstallationItem) int { return cmp.Compare(a.EdgeID, b.EdgeID) })
	return items, nil
}

// ── AccessRequestStore ───────────────────────────────────────────────────────────────────────

// ListAccessRequests implementa platformadmin.AccessRequestStore.
func (f *Fake) ListAccessRequests(_ context.Context, status string) ([]platformadmin.AccessRequestItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failures["ListAccessRequests"]; err != nil {
		return nil, err
	}
	if status == "" {
		status = "pending"
	}
	items := []platformadmin.AccessRequestItem{}
	for _, r := range f.requests { // f.requests va en orden de alta: created_at ascendente
		if r.Status != status {
			continue
		}
		items = append(items, platformadmin.AccessRequestItem{
			ID: f.requestID[r], UserID: r.UserID, Email: r.Email, Origin: r.Origin, Status: r.Status,
			CreatedAt: r.CreatedAt, Systems: []string{}, SystemsKnown: false,
		})
	}
	return items, nil
}

// CreateAccessRequest implementa platformadmin.AccessRequestStore.
func (f *Fake) CreateAccessRequest(_ context.Context, userID, email, origin string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failures["CreateAccessRequest"]; err != nil {
		return err
	}
	if userID == "" || email == "" || (origin != "bff" && origin != "edge") {
		return platformadmin.ErrInvalidInput
	}
	for _, r := range f.requests {
		if r.UserID == userID && r.Status == "pending" {
			return nil // ON CONFLICT (user_id) WHERE status = 'pending' DO NOTHING
		}
	}
	r := &RequestRow{UserID: userID, Email: email, Origin: origin, Status: "pending", CreatedAt: f.now()}
	f.requests = append(f.requests, r)
	f.requestID[r] = uuid.NewString()
	return nil
}

// RejectAccessRequest implementa platformadmin.AccessRequestStore.
func (f *Fake) RejectAccessRequest(_ context.Context, requestID, reason, operatorID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failures["RejectAccessRequest"]; err != nil {
		return err
	}
	if requestID == "" || strings.TrimSpace(reason) == "" {
		return platformadmin.ErrInvalidInput
	}
	r := f.findRequestLocked(requestID)
	switch {
	case r == nil:
		return platformadmin.ErrNotFound
	case r.Status != "pending":
		return platformadmin.ErrConflict
	}
	r.Status = "rejected"
	r.Reason = &reason
	f.decideLocked(r, operatorID)
	return nil
}

// LookupAccessRequestStatus implementa platformadmin.AccessRequestStore.
func (f *Fake) LookupAccessRequestStatus(_ context.Context, requestID string) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failures["LookupAccessRequestStatus"]; err != nil {
		return "", "", err
	}
	r := f.findRequestLocked(requestID)
	if r == nil {
		return "", "", platformadmin.ErrNotFound
	}
	return r.UserID, r.Status, nil
}

// ResolveRoleID implementa platformadmin.AccessRequestStore.
func (f *Fake) ResolveRoleID(_ context.Context, role string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failures["ResolveRoleID"]; err != nil {
		return "", err
	}
	for _, r := range f.roles {
		if r.Name == role || r.ID == role {
			return r.ID, nil
		}
	}
	return "", platformadmin.ErrInvalidInput
}

// CheckRetryApproved implementa platformadmin.AccessRequestStore.
func (f *Fake) CheckRetryApproved(_ context.Context, userID, tenantID, roleID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failures["CheckRetryApproved"]; err != nil {
		return err
	}
	k := accessKey{userID, tenantID}
	if !f.members[k] {
		return platformadmin.ErrConflict
	}
	if !slices.Contains(f.userRoles[k], roleID) {
		return platformadmin.ErrRetryRoleMismatch
	}
	return nil
}

// ExecuteApprovalTx implementa platformadmin.AccessRequestStore: comprueba TODO antes de
// escribir nada, que es como el Fake da el «o todo o nada» de la transacción de Postgres.
func (f *Fake) ExecuteApprovalTx(_ context.Context, requestID, tenantID, userID, roleID, operatorID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failures["ExecuteApprovalTx"]; err != nil {
		return err
	}
	// Lo que en Postgres es una clave foránea: la empresa y el rol existen.
	if _, ok := f.tenants[tenantID]; !ok {
		return fmt.Errorf("platformadmin fake: la empresa %s no existe (tenant_members.tenant_id)", tenantID)
	}
	if !slices.ContainsFunc(f.roles, func(r Role) bool { return r.ID == roleID }) {
		return fmt.Errorf("platformadmin fake: el rol %s no existe (iam_user_roles.role_id)", roleID)
	}
	// Una sola empresa salvo multi_empresa en la de destino (GrantTenantAccess).
	for k, member := range f.members {
		if member && k.user == userID && k.tenant != tenantID && !f.multi[tenantID] {
			return platformadmin.ErrConflict
		}
	}
	r := f.findRequestLocked(requestID)
	if r == nil || r.Status != "pending" {
		return platformadmin.ErrConflict
	}
	k := accessKey{userID, tenantID}
	f.members[k] = true
	if !slices.Contains(f.userRoles[k], roleID) {
		f.userRoles[k] = append(f.userRoles[k], roleID)
		slices.Sort(f.userRoles[k])
	}
	r.Status = "approved"
	f.decideLocked(r, operatorID)
	return nil
}

// findRequestLocked busca la solicitud por id. Con f.mu tomado.
func (f *Fake) findRequestLocked(requestID string) *RequestRow {
	for _, r := range f.requests {
		if f.requestID[r] == requestID {
			return r
		}
	}
	return nil
}

// decideLocked anota quién y cuándo resolvió: decided_by solo si el operador es un UUID (si no,
// NULL), como el adaptador Postgres. Con f.mu tomado.
func (f *Fake) decideLocked(r *RequestRow, operatorID string) {
	r.DecidedBy = nil
	if op, err := uuid.Parse(operatorID); err == nil {
		s := op.String()
		r.DecidedBy = &s
	}
	at := f.now()
	r.DecidedAt = &at
}

// ── State ────────────────────────────────────────────────────────────────────────────────────

// SeedTenantsCreatedAt implementa State.
func (f *Fake) SeedTenantsCreatedAt(t *testing.T, n int, at time.Time) []string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, n)
	for range n {
		slug := "seed-" + strings.ReplaceAll(uuid.NewString(), "-", "")
		out = append(out, f.addTenantLocked(slug, "Sembrada", nil, at))
	}
	return out
}

// SeedFleetSession implementa State. Repetir la misma sesión reemplaza su última señal. Falla el
// test si la empresa no existe (en Postgres, la clave foránea).
func (f *Fake) SeedFleetSession(t *testing.T, tenantID, edgeID, sessionID string, lastSeenAt *time.Time) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.tenants[tenantID]; !ok {
		t.Fatalf("Fake.SeedFleetSession: la empresa %s no existe", tenantID)
	}
	var seen *time.Time
	if lastSeenAt != nil {
		at := *lastSeenAt
		seen = &at
	}
	f.sessions[sessionKey{tenantID, edgeID, sessionID}] = seen
}

// SeedLease implementa State. Falla el test si la empresa no existe.
func (f *Fake) SeedLease(t *testing.T, tenantID, edgeID string, revoked bool) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.tenants[tenantID]; !ok {
		t.Fatalf("Fake.SeedLease: la empresa %s no existe", tenantID)
	}
	f.leases[edgeKey{tenantID, edgeID}] = revoked
}

// Request implementa State: devuelve una COPIA de la fila.
func (f *Fake) Request(t *testing.T, requestID string) RequestRow {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.findRequestLocked(requestID)
	if r == nil {
		t.Fatalf("Fake.Request: la solicitud %s no existe", requestID)
	}
	row := *r
	row.Reason = clonePtr(r.Reason)
	row.DecidedBy = clonePtr(r.DecidedBy)
	row.DecidedAt = clonePtr(r.DecidedAt)
	return row
}

// Access implementa State.
func (f *Fake) Access(t *testing.T, userID, tenantID string) (bool, []string) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	k := accessKey{userID, tenantID}
	roles := slices.Clone(f.userRoles[k])
	if roles == nil {
		roles = []string{}
	}
	return f.members[k], roles
}

// copyTenant copia la fila, punteros incluidos: quien la recibe no puede tocar el estado.
func copyTenant(it platformadmin.TenantListItem) platformadmin.TenantListItem {
	it.PlanID = clonePtr(it.PlanID)
	it.RevokedAt = clonePtr(it.RevokedAt)
	return it
}

func clonePtr[V any](p *V) *V {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
