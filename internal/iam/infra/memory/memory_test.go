package memory

import (
	"context"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
)

func TestUserStore_CRUD(t *testing.T) {
	ctx := context.Background()
	s := NewUserStore()

	u := domain.User{
		TenantID:     "tenant-a",
		Email:        "user@example.com",
		PasswordHash: "secret-hash",
		IsActive:     true,
	}

	created, err := s.Create(ctx, u)
	if err != nil || created.ID == "" {
		t.Fatalf("Create user err=%v, id=%s", err, created.ID)
	}

	// Duplicado email -> ErrConflict
	if _, err := s.Create(ctx, u); err != domain.ErrConflict {
		t.Fatalf("Create email duplicado err=%v, quiero ErrConflict", err)
	}

	// GetByID
	got, err := s.GetByID(ctx, created.ID)
	if err != nil || got.Email != u.Email {
		t.Fatalf("GetByID err=%v, email=%s", err, got.Email)
	}

	// FindByEmail
	gotFind, err := s.FindByEmail(ctx, "user@example.com")
	if err != nil || gotFind.ID != created.ID {
		t.Fatalf("FindByEmail err=%v, id=%s", err, gotFind.ID)
	}

	// GetByEmail
	gotEmail, err := s.GetByEmail(ctx, "tenant-a", "user@example.com")
	if err != nil || gotEmail.ID != created.ID {
		t.Fatalf("GetByEmail err=%v, id=%s", err, gotEmail.ID)
	}

	// Inexistente -> ErrNotFound
	if _, err := s.GetByID(ctx, "nonexistent"); err != domain.ErrNotFound {
		t.Fatalf("GetByID inexistente err=%v, quiero ErrNotFound", err)
	}

	// List
	list, err := s.List(ctx, "tenant-a")
	if err != nil || len(list) != 1 {
		t.Fatalf("List err=%v, len=%d", err, len(list))
	}

	// SoftDelete
	if err := s.SoftDelete(ctx, "tenant-a", created.ID); err != nil {
		t.Fatalf("SoftDelete err: %v", err)
	}
	gotDeact, err := s.GetByID(ctx, created.ID)
	if err != domain.ErrNotFound {
		t.Fatalf("tras SoftDelete, GetByID debió dar ErrNotFound, dio err=%v, u=%+v", err, gotDeact)
	}
}

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

	// ParentOf
	parentID := "role-parent"
	rChild := domain.Role{
		TenantID:     &tID,
		Name:         "child",
		ParentRoleID: &parentID,
	}
	childCreated, _ := s.Create(ctx, rChild)
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
	grantsAfter, _ := s.GrantsOfUser(ctx, "user-1")
	if len(grantsAfter) != 0 {
		t.Fatalf("len tras remove = %d, quiero 0", len(grantsAfter))
	}
}

func TestRefreshStore_CRUD(t *testing.T) {
	ctx := context.Background()
	s := NewRefreshStore()

	rt := domain.RefreshToken{
		UserID:    "user-1",
		TokenHash: "hash-123",
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}

	if err := s.Save(ctx, rt); err != nil {
		t.Fatalf("Save refresh token err: %v", err)
	}

	got, err := s.GetByHash(ctx, "hash-123")
	if err != nil || got.UserID != "user-1" {
		t.Fatalf("GetByHash err=%v, userID=%s", err, got.UserID)
	}

	// Revoke single
	if err := s.Revoke(ctx, "hash-123"); err != nil {
		t.Fatalf("Revoke err: %v", err)
	}
	gotRev, _ := s.GetByHash(ctx, "hash-123")
	if gotRev.RevokedAt == nil {
		t.Fatal("RevokedAt no debe ser nil tras Revoke")
	}

	// RevokeAllForUser
	rt2 := domain.RefreshToken{
		UserID:    "user-2",
		TokenHash: "hash-456",
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
	s.Save(ctx, rt2)
	if err := s.RevokeAllForUser(ctx, "user-2"); err != nil {
		t.Fatalf("RevokeAllForUser err: %v", err)
	}
}

func TestAPIKeyStore_CRUD(t *testing.T) {
	ctx := context.Background()
	s := NewAPIKeyStore()

	key := domain.APIKey{
		TenantID: "tenant-a",
		ClientID: "client-1",
		KeyHash:  "hash-key-1",
		Scopes:   []string{"flows.read"},
		IsActive: true,
	}

	created, err := s.Create(ctx, key)
	if err != nil || created.ID == "" {
		t.Fatalf("Create APIKey err=%v", err)
	}

	// Duplicado -> ErrConflict
	if _, err := s.Create(ctx, key); err != domain.ErrConflict {
		t.Fatalf("Create duplicado err=%v, quiero ErrConflict", err)
	}

	got, err := s.GetByHash(ctx, "hash-key-1")
	if err != nil || got.ID != created.ID {
		t.Fatalf("GetByHash err=%v, id=%s", err, got.ID)
	}

	list, err := s.List(ctx, "tenant-a")
	if err != nil || len(list) != 1 {
		t.Fatalf("List APIKey err=%v, len=%d", err, len(list))
	}

	if err := s.TouchLastUsed(ctx, created.ID); err != nil {
		t.Fatalf("TouchLastUsed err: %v", err)
	}

	if err := s.Revoke(ctx, "tenant-a", created.ID); err != nil {
		t.Fatalf("Revoke APIKey err: %v", err)
	}

	// Revoke inexistente -> ErrNotFound
	if err := s.Revoke(ctx, "tenant-a", "nonexistent"); err != domain.ErrNotFound {
		t.Fatalf("Revoke inexistente err=%v, quiero ErrNotFound", err)
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
	if st.Users == nil || st.Roles == nil || st.Grants == nil || st.Refresh == nil || st.APIKeys == nil || st.Audit == nil {
		t.Fatal("NewStore debe inicializar los 6 repositorios")
	}
}
