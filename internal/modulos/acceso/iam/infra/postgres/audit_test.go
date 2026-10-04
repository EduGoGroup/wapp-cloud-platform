//go:build pendiente

package iampostgres

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
)

var _ func(*sql.DB) *AuditRepo = NewAuditRepo

// TestNewAuditRepo_DoesNotTouchPool: construir no consulta nada.
func TestNewAuditRepo_DoesNotTouchPool(t *testing.T) {
	if NewAuditRepo(downPool(t)) == nil {
		t.Fatal("NewAuditRepo devolvió nil")
	}
}

// TestAuditRecord_UnserializableMetaRejectedBeforeDB: un Meta que JSON no sabe serializar se
// rechaza con su texto y SIN llegar a la base (el error no es el del pool caído).
func TestAuditRecord_UnserializableMetaRejectedBeforeDB(t *testing.T) {
	tenant := testTenantID
	err := NewAuditRepo(downPool(t)).Record(t.Context(), domain.AuditEvent{
		TenantID: &tenant,
		Actor:    testUserID,
		Action:   "role.create",
		Meta:     map[string]any{"canal": make(chan int)},
	})
	if err == nil {
		t.Fatal("Record con un Meta no serializable no dio error")
	}
	if errors.Is(err, errPoolDown) {
		t.Fatalf("Record llegó a la base con un Meta no serializable: %v", err)
	}
	var jsonErr *json.UnsupportedTypeError
	if !errors.As(err, &jsonErr) {
		t.Errorf("Record = %v; quiere un error que envuelva el de JSON", err)
	}
	if prefix := "iam: serializar meta de auditoría: "; !strings.HasPrefix(err.Error(), prefix) {
		t.Errorf("texto %q; quiere el prefijo %q", err.Error(), prefix)
	}
}

// TestAuditRecord_InfraError: un fallo de la base al registrar se propaga envuelto.
func TestAuditRecord_InfraError(t *testing.T) {
	tenant := testTenantID
	err := NewAuditRepo(downPool(t)).Record(t.Context(), domain.AuditEvent{TenantID: &tenant, Actor: testUserID, Action: "role.create"})
	wantInfraError(t, "Record", err, "iam: registrar auditoría: ")
}

// TestAuditList_InfraErrorNoPartialList: un fallo de la base al listar sale (nil, err).
func TestAuditList_InfraErrorNoPartialList(t *testing.T) {
	events, err := NewAuditRepo(downPool(t)).List(t.Context(), testTenantID, 10, 0)
	wantInfraError(t, "List", err, "iam: listar auditoría: ")
	if events != nil {
		t.Errorf("List con error devolvió %d eventos; quiere nil", len(events))
	}
}
