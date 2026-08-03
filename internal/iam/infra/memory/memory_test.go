package memory

import (
	"context"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
)

func TestRoleStore_CRUD(t *testing.T) {
	ctx := context.Background()
	s := NewRoleStore()

	tID := "tenant-a"
	r := domain.Role{
		TenantID: &tID,
		Name:     "admin",
	}

	// Seed & Create
	seeded := s.Seed(r, []domain.Grant{{Pattern: "users.create", Effect: domain.EffectAllow}})
	if seeded.ID == "" {
		t.Fatal("Seed ID no debe ser vacío")
	}

	got, err := s.GetByID(ctx, seeded.ID)
	if err != nil || got.Name != "admin" {
		t.Fatalf("GetByID err=%v, name=%s", err, got.Name)
	}

	// GrantsOf & AddGrant & RemoveGrant
	grants, err := s.GrantsOf(ctx, seeded.ID)
	if err != nil || len(grants) != 1 {
		t.Fatalf("GrantsOf err=%v, len=%d", err, len(grants))
	}

	g2 := domain.Grant{Pattern: "users.delete", Effect: domain.EffectAllow}
	if err := s.AddGrant(ctx, seeded.ID, g2); err != nil {
		t.Fatalf("AddGrant err: %v", err)
	}
	if err := s.RemoveGrant(ctx, seeded.ID, g2); err != nil {
		t.Fatalf("RemoveGrant err: %v", err)
	}

	// AssignToUser / RolesOfUser / UnassignFromUser
	if err := s.AssignToUser(ctx, "user-1", seeded.ID); err != nil {
		t.Fatalf("AssignToUser err: %v", err)
	}

	roles, err := s.RolesOfUser(ctx, "user-1")
	if err != nil || len(roles) != 1 {
		t.Fatalf("RolesOfUser err=%v, len=%d", err, len(roles))
	}

	if err := s.UnassignFromUser(ctx, "user-1", seeded.ID); err != nil {
		t.Fatalf("UnassignFromUser err: %v", err)
	}
}

func TestRoleStore_ParentOf(t *testing.T) {
	ctx := context.Background()
	s := NewRoleStore()

	tID := "tenant-a"
	parentID := "role-parent"
	rChild := domain.Role{
		TenantID:     &tID,
		Name:         "child",
		ParentRoleID: &parentID,
	}
	childCreated, err := s.Create(ctx, rChild)
	if err != nil {
		t.Fatalf("Create rol hijo err: %v", err)
	}
	pID, hasParent, err := s.ParentOf(ctx, childCreated.ID)
	if err != nil || !hasParent || pID != "role-parent" {
		t.Fatalf("ParentOf err=%v, has=%v, pID=%s", err, hasParent, pID)
	}
}

func TestGrantStore_CRUD(t *testing.T) {
	ctx := context.Background()
	s := NewGrantStore()

	grant := domain.Grant{
		Pattern: "messages.send",
		Effect:  domain.EffectAllow,
	}

	if err := s.AddUserGrant(ctx, "user-1", grant); err != nil {
		t.Fatalf("AddUserGrant err: %v", err)
	}
	// Idempotencia
	if err := s.AddUserGrant(ctx, "user-1", grant); err != nil {
		t.Fatalf("AddUserGrant re-add err: %v", err)
	}

	grants, err := s.GrantsOfUser(ctx, "user-1")
	if err != nil || len(grants) != 1 {
		t.Fatalf("GrantsOfUser err=%v, len=%d", err, len(grants))
	}

	if err := s.RemoveUserGrant(ctx, "user-1", grant); err != nil {
		t.Fatalf("RemoveUserGrant err: %v", err)
	}
	grantsAfter, err := s.GrantsOfUser(ctx, "user-1")
	if err != nil {
		t.Fatalf("GrantsOfUser tras remove err: %v", err)
	}
	if len(grantsAfter) != 0 {
		t.Fatalf("len tras remove = %d, quiero 0", len(grantsAfter))
	}
}

func TestAuditStore_CRUD(t *testing.T) {
	ctx := context.Background()
	s := NewAuditStore()

	tenant := "tenant-a"
	ev := domain.AuditEvent{
		TenantID: &tenant,
		Action:   "flows.create",
		Resource: "flow",
	}

	if err := s.Record(ctx, ev); err != nil {
		t.Fatalf("Record audit event err: %v", err)
	}

	events := s.Events()
	if len(events) != 1 || events[0].ID != 1 {
		t.Fatalf("Events len=%d, id=%d", len(events), events[0].ID)
	}

	list, err := s.List(ctx, "tenant-a", 10, 0)
	if err != nil || len(list) != 1 {
		t.Fatalf("List audit events err=%v, len=%d", err, len(list))
	}
}

func TestStoreAggregator(t *testing.T) {
	st := NewStore()
	if st.Roles == nil || st.Grants == nil || st.Audit == nil || st.Memberships == nil {
		t.Fatal("NewStore debe inicializar los 4 repositorios")
	}
}
