package contact_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
)

// dsnEnv habilita los tests de integración con BD real (igual que en store/lease).
const dsnEnv = "WAPP_TEST_DB_DSN"

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		t.Skipf("%s no definido: se omiten los tests de integración con BD", dsnEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, err := postgres.Open(ctx, postgres.Config{DSN: dsn})
	if err != nil {
		t.Skipf("BD no disponible en %s (%v): se omiten los tests de integración", dsnEnv, err)
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

func seedTenant(t *testing.T, db *sql.DB) string {
	t.Helper()
	repo := postgres.NewTenantRepository(db)
	slug := fmt.Sprintf("tenant-contacts-%d", time.Now().UnixNano())
	ten, err := repo.Create(context.Background(), slug, "Contacts Resolver Test")
	if err != nil {
		t.Fatalf("crear tenant: %v", err)
	}
	return ten.ID
}

// seedFlowState inserta una fila mínima de flow_state para (tenant, session,
// contactID), simulando una conversación viva que la fusión debe migrar.
func seedFlowState(t *testing.T, db *sql.DB, tenantID, sessionID, contactID string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO public.flow_state
			(tenant_id, session_id, contact_id, flow_id, flow_version, current_node, vars)
		VALUES ($1, $2, $3, 'f1', 1, 'root', '{}')
	`, tenantID, sessionID, contactID)
	if err != nil {
		t.Fatalf("insertar flow_state: %v", err)
	}
}

func flowStateContactID(t *testing.T, db *sql.DB, tenantID, sessionID string) (string, bool) {
	t.Helper()
	var cid string
	err := db.QueryRowContext(context.Background(), `
		SELECT contact_id::text FROM public.flow_state
		WHERE tenant_id = $1 AND session_id = $2
	`, tenantID, sessionID).Scan(&cid)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", false
	case err != nil:
		t.Fatalf("leer flow_state: %v", err)
	}
	return cid, true
}

func mustRef(t *testing.T, kind, value string) contact.Ref {
	t.Helper()
	ref, err := contact.NewRef(kind, value)
	if err != nil {
		t.Fatalf("NewRef(%s, %q): %v", kind, value, err)
	}
	return ref
}

func TestPG_Resolve_ReusaYAta(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenant := seedTenant(t, db)
	r := contact.NewPostgresResolver(db)
	phone := mustRef(t, contact.KindPhoneE164, "573001112233")
	lid := mustRef(t, contact.KindWALID, "88887777")

	id1, err := r.Resolve(ctx, tenant, []contact.Ref{phone}, "Juan")
	if err != nil {
		t.Fatalf("Resolve phone: %v", err)
	}
	// misma ref → mismo id
	id2, err := r.Resolve(ctx, tenant, []contact.Ref{phone}, "")
	if err != nil || id2 != id1 {
		t.Fatalf("ref existente debe reusar: id=%q err=%v (quiero %q)", id2, err, id1)
	}
	// phone + lid nueva → mismo id, lid atado
	id3, err := r.Resolve(ctx, tenant, []contact.Ref{phone, lid}, "")
	if err != nil || id3 != id1 {
		t.Fatalf("phone+lid debe reusar id1: id=%q err=%v", id3, err)
	}
	id4, err := r.Resolve(ctx, tenant, []contact.Ref{lid}, "")
	if err != nil || id4 != id1 {
		t.Fatalf("lid quedó atado a otro id: %q vs %q (err=%v)", id4, id1, err)
	}
}

func TestPG_Resolve_FusionMigraFlowState(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenant := seedTenant(t, db)
	r := contact.NewPostgresResolver(db)
	phone := mustRef(t, contact.KindPhoneE164, "573009998877")
	lid := mustRef(t, contact.KindWALID, "77776666")

	c1, err := r.Resolve(ctx, tenant, []contact.Ref{phone}, "")
	if err != nil {
		t.Fatalf("Resolve phone: %v", err)
	}
	// C2 más nuevo (creado después) por LID; sembramos su flow_state.
	c2, err := r.Resolve(ctx, tenant, []contact.Ref{lid}, "")
	if err != nil {
		t.Fatalf("Resolve lid: %v", err)
	}
	if c1 == c2 {
		t.Fatal("precondición: C1 y C2 distintos")
	}
	seedFlowState(t, db, tenant, "s-huerfano", c2)

	// Fusión: canónico = C1 (más antiguo); el flow_state de C2 migra a C1.
	canonical, err := r.Resolve(ctx, tenant, []contact.Ref{phone, lid}, "")
	if err != nil {
		t.Fatalf("Resolve fusión: %v", err)
	}
	if canonical != c1 {
		t.Fatalf("canónico debe ser el más antiguo C1=%q, fue %q", c1, canonical)
	}
	got, ok := flowStateContactID(t, db, tenant, "s-huerfano")
	if !ok {
		t.Fatal("el flow_state desapareció tras la fusión")
	}
	if got != c1 {
		t.Fatalf("flow_state quedó en %q, debía migrar a canónico %q", got, c1)
	}
}

func TestPG_Resolve_FusionConflictoConservaCanonico(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenant := seedTenant(t, db)
	r := contact.NewPostgresResolver(db)
	phone := mustRef(t, contact.KindPhoneE164, "573001110000")
	lid := mustRef(t, contact.KindWALID, "66665555")

	c1, err := r.Resolve(ctx, tenant, []contact.Ref{phone}, "")
	if err != nil {
		t.Fatalf("Resolve phone: %v", err)
	}
	c2, err := r.Resolve(ctx, tenant, []contact.Ref{lid}, "")
	if err != nil {
		t.Fatalf("Resolve lid: %v", err)
	}
	// AMBOS tienen flow_state en la MISMA sesión → conflicto de PK al fundir.
	seedFlowState(t, db, tenant, "s-conflicto", c1)
	seedFlowState(t, db, tenant, "s-conflicto", c2)

	canonical, err := r.Resolve(ctx, tenant, []contact.Ref{phone, lid}, "")
	if err != nil {
		t.Fatalf("Resolve fusión: %v", err)
	}
	// Política: se CONSERVA el estado del canónico y se descarta el del huérfano.
	got, ok := flowStateContactID(t, db, tenant, "s-conflicto")
	if !ok || got != canonical {
		t.Fatalf("tras conflicto debe quedar SOLO el estado del canónico %q, quedó %q (ok=%v)", canonical, got, ok)
	}
}

func TestPG_Destino_Preferencia(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenant := seedTenant(t, db)
	r := contact.NewPostgresResolver(db)
	phone := mustRef(t, contact.KindPhoneE164, "573002223344")
	lid := mustRef(t, contact.KindWALID, "55554444")

	id, err := r.Resolve(ctx, tenant, []contact.Ref{lid, phone}, "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	dst, err := r.Destino(ctx, tenant, id)
	if err != nil {
		t.Fatalf("Destino: %v", err)
	}
	if dst.Kind != contact.KindPhoneE164 {
		t.Fatalf("destino = %+v, quiero phone_e164", dst)
	}
}
