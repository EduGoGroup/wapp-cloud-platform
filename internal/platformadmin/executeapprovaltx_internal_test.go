package platformadmin

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
	"github.com/google/uuid"
)

// openInternalTestDB es LA MISMA conexión de pruebas de integración que
// postgres_test.go (package platformadmin_test) abre bajo WAPP_TEST_DB_DSN,
// duplicada aquí porque este fichero necesita `package platformadmin` (no
// platformadmin_test): executeApprovalTx no está exportado, y probar su
// atomicidad exige llamarlo DIRECTAMENTE, saltándose resolveRoleID a
// propósito -- resolveRoleID rechazaría cualquier role_id que no exista ANTES
// de entrar en la tx, así que a través de la API pública (ApproveAccessRequest)
// es IMPOSIBLE llegar a un INSERT de rol que falle sin fingirlo desde dentro.
func openInternalTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("WAPP_TEST_DB_DSN")
	if dsn == "" {
		if os.Getenv("WAPP_TEST_REQUIRE_DB") != "" {
			t.Fatalf("WAPP_TEST_DB_DSN no definido pero WAPP_TEST_REQUIRE_DB exige BD")
		}
		t.Skipf("WAPP_TEST_DB_DSN no definido: se omiten los tests de integración con BD")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := postgres.Open(ctx, postgres.Config{DSN: dsn})
	if err != nil {
		if os.Getenv("WAPP_TEST_REQUIRE_DB") != "" {
			t.Fatalf("BD no disponible en WAPP_TEST_DB_DSN (%v) pero WAPP_TEST_REQUIRE_DB exige BD", err)
		}
		t.Skipf("BD no disponible en WAPP_TEST_DB_DSN (%v): se omiten", err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Logf("cerrando BD de test: %v", cerr)
		}
	})
	if _, err := migrations.Migrate(ctx, db); err != nil {
		t.Fatalf("migrando BD de test: %v", err)
	}
	return db
}

// TestExecuteApprovalTx_RoleInsertFailureRollsBackEverything es el criterio
// (2) de T3.4: «si el INSERT del rol falla, la membresía tampoco queda
// escrita -- provocarlo a propósito». La atomicidad de executeApprovalTx
// (access_requests.go:268-335) estaba sin verificar: nada garantizaba que un
// fallo a mitad de la tx no dejara tenant_members escrito con
// iam_user_roles vacío.
//
// Se llama a executeApprovalTx DIRECTAMENTE con un role_id sintácticamente
// válido pero que NO existe en iam_roles: la FK
// iam_user_roles.role_id → iam_roles(id) (0016_iam_user_roles.sql:19) hace
// fallar ese segundo INSERT. Si la tx es de verdad atómica, el PRIMER INSERT
// (tenant_members, que en solitario habría tenido éxito -- tenantID y userID
// son válidos) tiene que desaparecer con el rollback.
func TestExecuteApprovalTx_RoleInsertFailureRollsBackEverything(t *testing.T) {
	t.Parallel()
	db := openInternalTestDB(t)
	r := NewRepository(db)
	ctx := context.Background()

	userID := uuid.NewString()
	email := fmt.Sprintf("atomic-%d@example.com", time.Now().UnixNano())
	if err := r.CreateAccessRequest(ctx, userID, email, "bff"); err != nil {
		t.Fatalf("CreateAccessRequest: %v", err)
	}
	items, err := r.ListAccessRequests(ctx, "pending")
	if err != nil {
		t.Fatalf("ListAccessRequests: %v", err)
	}
	var requestID string
	for _, it := range items {
		if it.UserID == userID {
			requestID = it.ID
			break
		}
	}
	if requestID == "" {
		t.Fatalf("no se encontró solicitud pending para %s", userID)
	}

	slug := fmt.Sprintf("pa-atomic-%d", time.Now().UnixNano())
	ten, err := r.CreateTenant(ctx, slug, "Atomic Tx Tenant", nil)
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	nonexistentRoleID := uuid.NewString() // UUID bien formado, SIN fila en iam_roles.
	operatorID := uuid.NewString()

	if err := r.executeApprovalTx(ctx, requestID, ten.ID, userID, nonexistentRoleID, operatorID); err == nil {
		t.Fatal("esperado error por violación de FK en iam_user_roles.role_id, obtenido nil")
	}

	// LA COMPROBACIÓN DE VERDAD: el INSERT en tenant_members -- que ocurre
	// ANTES del insert de rol, dentro de la MISMA tx -- no debe sobrevivir.
	var tenantMembers int
	if qerr := db.QueryRowContext(ctx, `
		SELECT count(*) FROM public.tenant_members WHERE user_id = $1 AND tenant_id = $2
	`, userID, ten.ID).Scan(&tenantMembers); qerr != nil {
		t.Fatalf("contar tenant_members: %v", qerr)
	}
	if tenantMembers != 0 {
		t.Fatalf("tenant_members NO debía sobrevivir al rollback: %d fila(s) encontradas -- executeApprovalTx no es atómica", tenantMembers)
	}

	// Y la solicitud tiene que seguir 'pending': el UPDATE final tampoco corrió.
	var status string
	if qerr := db.QueryRowContext(ctx, `SELECT status FROM public.access_requests WHERE id = $1`, requestID).Scan(&status); qerr != nil {
		t.Fatalf("leer status: %v", qerr)
	}
	if status != "pending" {
		t.Fatalf("status esperado 'pending' tras el rollback, obtenido: %q", status)
	}
}
