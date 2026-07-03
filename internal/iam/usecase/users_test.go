package usecase_test

import (
	"context"
	"errors"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/infra/memory"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
)

func TestCreateUser_DuplicateEmailConflict(t *testing.T) {
	t.Parallel()
	store := memory.NewStore()
	users := mustUserSvc(t, store)
	ctx := context.Background()

	if _, err := users.CreateUser(ctx, in.CreateUserInput{TenantID: testTenant, Email: "dup@x.example", Password: testLoginPhrase}); err != nil {
		t.Fatalf("primer CreateUser: %v", err)
	}
	_, err := users.CreateUser(ctx, in.CreateUserInput{TenantID: testTenant, Email: "dup@x.example", Password: testLoginPhrase})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("se esperaba ErrConflict, got %v", err)
	}
}

func TestGetUser_TenantIsolation(t *testing.T) {
	t.Parallel()
	store := memory.NewStore()
	users := mustUserSvc(t, store)
	ctx := context.Background()

	u, err := users.CreateUser(ctx, in.CreateUserInput{TenantID: testTenant, Email: "iso@x.example", Password: testLoginPhrase})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	// El tenant dueño lo ve.
	if _, err := users.GetUser(ctx, testTenant, u.ID); err != nil {
		t.Fatalf("GetUser (dueño): %v", err)
	}
	// Otro tenant NO lo ve (aislamiento → ErrNotFound).
	if _, err := users.GetUser(ctx, testTenantB, u.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("se esperaba ErrNotFound cross-tenant, got %v", err)
	}
}

func TestRole_CreateAndOwnershipGuard(t *testing.T) {
	t.Parallel()
	store := memory.NewStore()
	roles := mustRoleSvc(t, store)
	ctx := context.Background()

	role, err := roles.CreateRole(ctx, in.CreateRoleInput{
		TenantID: testTenant, Name: "custom",
		Grants: []domain.Grant{{Pattern: "contacts.read", Effect: domain.EffectAllow}},
	})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	// Otro tenant no puede mutar el rol ajeno.
	if err := roles.AddGrant(ctx, testTenantB, role.ID, domain.Grant{Pattern: "*", Effect: domain.EffectAllow}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("se esperaba ErrNotFound al mutar rol ajeno, got %v", err)
	}
	// El dueño sí.
	if err := roles.AddGrant(ctx, testTenant, role.ID, domain.Grant{Pattern: "flows.read", Effect: domain.EffectAllow}); err != nil {
		t.Fatalf("AddGrant (dueño): %v", err)
	}
}
