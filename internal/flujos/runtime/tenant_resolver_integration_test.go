package runtime_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
)

// dsnEnv habilita los tests de integración con BD real (igual que en store/lease).
const dsnEnv = "WAPP_TEST_DB_DSN"

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		if os.Getenv("WAPP_TEST_REQUIRE_DB") != "" {
			t.Fatalf("%s no definido pero WAPP_TEST_REQUIRE_DB exige BD (Plan 027 · Ola 1 · T7): la integración DEBE correr", dsnEnv)
		}
		t.Skipf("%s no definido: se omiten los tests de integración con BD", dsnEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, err := postgres.Open(ctx, postgres.Config{DSN: dsn})
	if err != nil {
		if os.Getenv("WAPP_TEST_REQUIRE_DB") != "" {
			t.Fatalf("BD no disponible en %s (%v) pero WAPP_TEST_REQUIRE_DB exige BD (Plan 027 · Ola 1 · T7)", dsnEnv, err)
		}
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
	slug := fmt.Sprintf("tenant-resolver-%d", time.Now().UnixNano())
	ten, err := repo.Create(context.Background(), slug, "Tenant Resolver Test")
	if err != nil {
		t.Fatalf("crear tenant: %v", err)
	}
	return ten.ID
}

// seedFleetSession siembra una fila online en fleet_sessions con el PERFIL dado
// (Plan 046 · T1.1). El perfil se escribe explícitamente y no se deja al DEFAULT
// porque el DEFAULT de la 0063 es pasivo: una fila sembrada "a pelo" ya no
// resuelve activa, y dejarla al default convertiría cada seed en un caso de prueba
// distinto del que el test dice estar probando.
func seedFleetSession(t *testing.T, db *sql.DB, tenantID, edgeID, sessionID, profile string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO public.fleet_sessions
			(tenant_id, edge_id, session_id, state, profile, last_connected_at, last_seen_at, updated_at)
		VALUES ($1, $2, $3, 'online', $4, now(), now(), now())
		ON CONFLICT (tenant_id, edge_id, session_id)
			DO UPDATE SET state = 'online', profile = EXCLUDED.profile
	`, tenantID, edgeID, sessionID, profile)
	if err != nil {
		t.Fatalf("sembrar fleet_sessions (profile=%s): %v", profile, err)
	}
}

// seedFleetSessionSinPerfil siembra una fila online SIN tocar la columna profile:
// la deja caer al DEFAULT de la 0063. Es el sembrado que ejerce D-07 (una sesión
// nueva nace pasiva).
func seedFleetSessionSinPerfil(t *testing.T, db *sql.DB, tenantID, edgeID, sessionID string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO public.fleet_sessions
			(tenant_id, edge_id, session_id, state, last_connected_at, last_seen_at, updated_at)
		VALUES ($1, $2, $3, 'online', now(), now(), now())
		ON CONFLICT (tenant_id, edge_id, session_id) DO UPDATE SET state = 'online'
	`, tenantID, edgeID, sessionID)
	if err != nil {
		t.Fatalf("sembrar fleet_sessions sin perfil: %v", err)
	}
}

func TestIntegration_PostgresTenantResolver_Resuelve(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)
	sessionID := fmt.Sprintf("sess-%d", time.Now().UnixNano())
	seedFleetSession(t, db, tenantID, "edge-A", sessionID, "active")

	res := runtime.NewPostgresTenantResolver(db)
	got, role, err := res.ResolveTenant(ctx, sessionID)
	if err != nil {
		t.Fatalf("ResolveTenant: %v", err)
	}
	if got != tenantID {
		t.Fatalf("tenant resuelto: got %q, want %q", got, tenantID)
	}
	if role != "bot" {
		t.Fatalf("una sesión de perfil activo debe resolver bot: got %q", role)
	}
}

// TestIntegration_PostgresTenantResolver_PerfilPorDefectoEsPassive fija D-07 (Plan
// 046 · T1.1): una sesión sembrada SIN tocar la columna profile cae al DEFAULT de
// la 0063 y resuelve PASIVA — es decir, una sesión recién emparejada NO
// auto-responde hasta que su dueño la active.
//
// 🔴 Es un cambio de comportamiento deliberado respecto de la 0025 (DEFAULT 'bot',
// que resolvía bot). Si este test se pone rojo con got=="bot", alguien devolvió el
// default a activo y con él la captura de tráfico por defecto que este plan existe
// para cerrar.
func TestIntegration_PostgresTenantResolver_PerfilPorDefectoEsPassive(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)
	sessionID := fmt.Sprintf("sess-default-%d", time.Now().UnixNano())
	seedFleetSessionSinPerfil(t, db, tenantID, "edge-A", sessionID)

	res := runtime.NewPostgresTenantResolver(db)
	got, role, err := res.ResolveTenant(ctx, sessionID)
	if err != nil {
		t.Fatalf("ResolveTenant: %v", err)
	}
	if got != tenantID {
		t.Fatalf("tenant resuelto: got %q, want %q", got, tenantID)
	}
	if role != "passive" {
		t.Fatalf("perfil por defecto (0063, D-07): got %q, want passive", role)
	}
}

func TestIntegration_PostgresTenantResolver_CeroFilas(t *testing.T) {
	db := openTestDB(t)
	res := runtime.NewPostgresTenantResolver(db)
	_, _, err := res.ResolveTenant(context.Background(), fmt.Sprintf("inexistente-%d", time.Now().UnixNano()))
	if !errors.Is(err, runtime.ErrTenantNotResolved) {
		t.Fatalf("0 filas debería dar ErrTenantNotResolved, dio: %v", err)
	}
}

// Un mismo session_id bajo dos edge_id del MISMO tenant resuelve sin ambigüedad
// (DISTINCT tenant_id colapsa).
func TestIntegration_PostgresTenantResolver_MismoTenantVariosEdges(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)
	sessionID := fmt.Sprintf("sess-%d", time.Now().UnixNano())
	seedFleetSession(t, db, tenantID, "edge-A", sessionID, "active")
	seedFleetSession(t, db, tenantID, "edge-B", sessionID, "active")

	res := runtime.NewPostgresTenantResolver(db)
	got, _, err := res.ResolveTenant(ctx, sessionID)
	if err != nil {
		t.Fatalf("ResolveTenant: %v", err)
	}
	if got != tenantID {
		t.Fatalf("tenant resuelto: got %q, want %q", got, tenantID)
	}
}

// TestIntegration_PostgresTenantResolver_PerfilDesconocidoCaeAPasiva fija la regla
// que dejó escrita la Ola 1 del Plan 046 —ANTE LA DUDA, PASIVA— en el único punto
// donde el runtime decide si una sesión auto-responde.
//
// Por qué existe: el predicado de ResolveTenant y el de su gemelo
// PostgresSelfNumbers se escribieron en la misma ola sobre la misma columna y
// divergían en la dirección del fallo. Sobre el dominio de dos valores de la 0063
// `profile = 'passive'` y `profile <> 'active'` son EQUIVALENTES —por eso ningún
// test de los otros lo distinguía—, pero ante un valor fuera del dominio el primero
// da any_passive=false y la sesión AUTO-RESPONDE. Este test es lo único que separa
// las dos formulaciones: con `= 'passive'` se pone rojo con got=="bot".
//
// Para poder sembrar el valor desconocido hay que retirar el CHECK de la 0063, que
// es justamente lo que hoy hace ese caso inalcanzable. Se restaura al terminar; es
// seguro porque la integración corre con `-p 1` (Makefile).
func TestIntegration_PostgresTenantResolver_PerfilDesconocidoCaeAPasiva(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)
	sessionID := fmt.Sprintf("sess-desconocido-%d", time.Now().UnixNano())

	sinCheckDePerfil(t, db)
	seedFleetSession(t, db, tenantID, "edge-A", sessionID, "supervisor")

	res := runtime.NewPostgresTenantResolver(db)
	got, role, err := res.ResolveTenant(ctx, sessionID)
	if err != nil {
		t.Fatalf("ResolveTenant: %v", err)
	}
	if got != tenantID {
		t.Fatalf("tenant resuelto: got %q, want %q", got, tenantID)
	}
	if role != "passive" {
		t.Fatalf("un perfil fuera del dominio debe caer a PASIVA: got %q. Alguien devolvió el "+
			"predicado a `profile = 'passive'` y una sesión de perfil desconocido auto-responde", role)
	}
}

// sinCheckDePerfil retira el CHECK de dominio de fleet_sessions.profile y registra
// su restauración. El Cleanup borra ANTES las filas fuera de dominio: sin eso el
// ADD CONSTRAINT las encontraría y fallaría. Red adicional: la propia 0063 recrea
// el CHECK (DROP IF EXISTS + ADD) en el siguiente replay.
func sinCheckDePerfil(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(),
		`ALTER TABLE public.fleet_sessions DROP CONSTRAINT IF EXISTS fleet_sessions_profile_chk`); err != nil {
		t.Fatalf("retirar fleet_sessions_profile_chk: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		if _, err := db.ExecContext(ctx,
			`DELETE FROM public.fleet_sessions WHERE profile NOT IN ('active', 'passive')`); err != nil {
			t.Logf("retirando las filas de perfil fuera de dominio: %v", err)
		}
		if _, err := db.ExecContext(ctx,
			`ALTER TABLE public.fleet_sessions
			   ADD CONSTRAINT fleet_sessions_profile_chk CHECK (profile IN ('active', 'passive'))`); err != nil {
			t.Logf("restaurando fleet_sessions_profile_chk: %v", err)
		}
	})
}
