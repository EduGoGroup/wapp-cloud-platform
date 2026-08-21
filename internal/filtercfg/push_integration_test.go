package filtercfg_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/filtercfg"
	flowadmin "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/admin"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
)

// dsnEnv habilita los tests de integración con BD real (mismo patrón que fleet/lease).
const dsnEnv = "WAPP_TEST_DB_DSN"

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		if os.Getenv("WAPP_TEST_REQUIRE_DB") != "" {
			t.Fatalf("%s no definido pero WAPP_TEST_REQUIRE_DB exige BD", dsnEnv)
		}
		t.Skipf("%s no definido: se omiten los tests de integración con BD", dsnEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, err := postgres.Open(ctx, postgres.Config{DSN: dsn})
	if err != nil {
		if os.Getenv("WAPP_TEST_REQUIRE_DB") != "" {
			t.Fatalf("BD no disponible en %s (%v) pero WAPP_TEST_REQUIRE_DB exige BD", dsnEnv, err)
		}
		t.Skipf("BD no disponible en %s (%v): se omiten", dsnEnv, err)
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
	slug := fmt.Sprintf("filters-%d", time.Now().UnixNano())
	ten, err := repo.Create(context.Background(), slug, "Filters Push Test")
	if err != nil {
		t.Fatalf("crear tenant: %v", err)
	}
	return ten.ID
}

// postProfile ejerce la ruta REAL de producción —el handler de T1.2 con el hook de
// T2.1 enchufado— sobre el repositorio Postgres real. No simula el handler: lo monta.
func postProfile(t *testing.T, repo *fleet.PostgresRepository, pusher flowadmin.ProfilePusher,
	tenantID, sessionID, perfil string,
) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/sessions/{id}/profile", flowadmin.SetSessionProfileHandler(repo, pusher, nil))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID+"/profile",
		strings.NewReader(`{"profile":"`+perfil+`"}`))
	req = req.WithContext(httpapi.WithIdentity(req.Context(),
		httpapi.Identity{TenantID: tenantID, Subject: "user-1"}))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// postearPerfilOK ejerce el POST real de producción y exige un 200. El código de estado se
// comprueba AQUÍ y no en el test porque un 4xx silencioso convertiría «no hubo push» en un
// falso negativo sobre el hook, que es justo lo que estos tests existen para vigilar.
func postearPerfilOK(t *testing.T, repo *fleet.PostgresRepository, pusher flowadmin.ProfilePusher,
	tenantID, sessionID, perfil, etapa string,
) {
	t.Helper()
	if rec := postProfile(t, repo, pusher, tenantID, sessionID, perfil); rec.Code != http.StatusOK {
		t.Fatalf("POST /profile (%s): code=%d body=%s", etapa, rec.Code, rec.Body.String())
	}
}

// exigirPushDeFilters comprueba que llegó EXACTAMENTE el push que T2.1 promete: el número
// de llamadas esperado, del kind acordado y del tenant correcto.
func exigirPushDeFilters(t *testing.T, espia *pushEspia, tenantID string, llamadas int) {
	t.Helper()
	if espia.llamadas != llamadas {
		t.Fatalf("pushes = %d, quiero %d", espia.llamadas, llamadas)
	}
	if espia.kind != "filters" {
		t.Fatalf("kind = %q, quiero \"filters\"", espia.kind)
	}
	if espia.tenant != tenantID {
		t.Fatalf("tenant del push = %q, quiero %q", espia.tenant, tenantID)
	}
}

// versionDelFrame parsea la version del frame como el ENTERO DECIMAL que el Edge compara.
// Si dejara de serlo, el Edge no la compararía mal: la descartaría entera.
func versionDelFrame(t *testing.T, espia *pushEspia, etapa string) int64 {
	t.Helper()
	v, err := strconv.ParseInt(espia.version, 10, 64)
	if err != nil {
		t.Fatalf("version del frame %q (%s) no es un entero decimal: %v", espia.version, etapa, err)
	}
	return v
}

// TestIntegration_PostProfile_EmpujaFiltersConLasDosSesiones cubre los criterios (a) y
// (b) de T2.1 contra Postgres real:
//
//	(a) con dos sesiones del mismo tenant (una active, una passive), UN
//	    POST /sessions/{id}/profile produce un push de kind "filters" cuyo payload
//	    deserializa a un mapa con LAS DOS sesiones y sus perfiles correctos;
//	(b) un segundo cambio produce una version cuyo valor NUMÉRICO es estrictamente
//	    mayor que el primero.
//
// 📌 Se captura en el puerto ConfigPusher, que es donde termina la responsabilidad de
// T2.1. El armado del frame ConfigUpdate (command_id, session_id del sobre) es del
// Gateway y lo cubren sus propios tests en internal/gateway/grpc.
func TestIntegration_PostProfile_EmpujaFiltersConLasDosSesiones(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)
	repo := fleet.NewPostgresRepository(db)

	for _, s := range []string{"sess-1", "sess-2"} {
		if err := repo.MarkOnline(ctx, tenantID, "edge-1", s); err != nil {
			t.Fatalf("MarkOnline %s: %v", s, err)
		}
	}
	// sess-1 queda ACTIVE; sess-2 se deja como nació (passive, DEFAULT de la 0063).
	if found, err := repo.SetProfile(ctx, tenantID, "sess-1", fleet.ProfileActive); err != nil || !found {
		t.Fatalf("preparar sess-1 active: found=%v err=%v", found, err)
	}

	espia := &pushEspia{}
	pusher := filtercfg.NewPusher(repo, espia)

	// (a) El POST que dispara: sess-2 pasa a passive (idempotente, pero el push va).
	postearPerfilOK(t, repo, pusher, tenantID, "sess-2", "passive", "el que dispara")
	exigirPushDeFilters(t, espia, tenantID, 1)
	got := decodePayload(t, espia.payload)
	if len(got.Sessions) != 2 {
		t.Fatalf("el payload trae %d sesiones (%v), quiero LAS DOS del tenant", len(got.Sessions), got.Sessions)
	}
	if got.Sessions["sess-1"].Profile != "active" || got.Sessions["sess-2"].Profile != "passive" {
		t.Fatalf("perfiles del payload mal: %s", espia.payload)
	}
	v1 := versionDelFrame(t, espia, "primer push")
	if v1 != got.Version {
		t.Fatalf("version del frame (%d) != version del payload (%d): tienen que ser el MISMO entero", v1, got.Version)
	}

	// (b) Segundo cambio ⇒ version estrictamente mayor. La aserción es sobre `>`.
	postearPerfilOK(t, repo, pusher, tenantID, "sess-1", "passive", "segundo cambio")
	v2 := versionDelFrame(t, espia, "segundo push")
	if v2 <= v1 {
		t.Fatalf("version no creció: %d -> %d (estrictamente mayor, no «distinta»)", v1, v2)
	}
	if got2 := decodePayload(t, espia.payload); got2.Sessions["sess-1"].Profile != "passive" {
		t.Fatalf("el segundo cambio no viajó: %s", espia.payload)
	}
}

// TestIntegration_TenantSinNingunaPasiva_RecibeIgualSuFrame es el criterio (c): el test
// que FALLA si el provider decide devolver nil «porque no hay nada que filtrar». Un
// mapa todo-active ES información: es lo que hace converger al Edge cuando una sesión
// deja de ser pasiva.
func TestIntegration_TenantSinNingunaPasiva_RecibeIgualSuFrame(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)
	repo := fleet.NewPostgresRepository(db)

	for _, s := range []string{"sess-1", "sess-2"} {
		if err := repo.MarkOnline(ctx, tenantID, "edge-1", s); err != nil {
			t.Fatalf("MarkOnline %s: %v", s, err)
		}
		if found, err := repo.SetProfile(ctx, tenantID, s, fleet.ProfileActive); err != nil || !found {
			t.Fatalf("activar %s: found=%v err=%v", s, found, err)
		}
	}

	version, payload, err := filtercfg.ForTenant(ctx, repo, tenantID)
	if err != nil {
		t.Fatalf("ForTenant: %v", err)
	}
	if payload == nil {
		t.Fatal("payload nil para un tenant sin ni una pasiva: el frame se manda SIEMPRE (regla 2 de T2.1)")
	}
	if version == "" || version == "0" {
		t.Fatalf("version = %q: un tenant con sesiones tiene max(profile_updated_at) > 0", version)
	}
	got := decodePayload(t, payload)
	if len(got.Sessions) != 2 {
		t.Fatalf("mapa con %d sesiones, quiero las 2 (todas active)", len(got.Sessions))
	}
	for id, s := range got.Sessions {
		if s.Profile != "active" {
			t.Fatalf("%s = %q, quiero active", id, s.Profile)
		}
	}
}

// TestIntegration_PushFallido_NoCambiaElCodigoDeRespuesta es el criterio (e): el
// contrato best-effort (molde de intents.go:167). El perfil ya quedó PERSISTIDO y el
// push al conectar reconcilia; un fallo de entrega no puede convertirse en un 500 que
// haga reintentar al dueño una escritura que ya ocurrió.
func TestIntegration_PushFallido_NoCambiaElCodigoDeRespuesta(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)
	repo := fleet.NewPostgresRepository(db)
	if err := repo.MarkOnline(ctx, tenantID, "edge-1", "sess-1"); err != nil {
		t.Fatalf("MarkOnline: %v", err)
	}

	espia := &pushEspia{err: errors.New("gateway caído")}
	pusher := filtercfg.NewPusher(repo, espia)

	rec := postProfile(t, repo, pusher, tenantID, "sess-1", "passive")
	if rec.Code != http.StatusOK {
		t.Fatalf("un push fallido cambió el código: %d (quiero 200)", rec.Code)
	}
	if espia.llamadas != 1 {
		t.Fatalf("pushes = %d, quiero 1 (se intentó)", espia.llamadas)
	}
	s, found, err := repo.Get(ctx, tenantID, "edge-1", "sess-1")
	if err != nil || !found {
		t.Fatalf("Get: found=%v err=%v", found, err)
	}
	if s.Profile != fleet.ProfilePassive {
		t.Fatalf("el push fallido se llevó por delante la escritura: profile=%q", s.Profile)
	}
}
