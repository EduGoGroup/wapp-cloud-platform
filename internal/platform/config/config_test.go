package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

// isolateEnv deja el proceso SIN ninguna variable WAPP_* mientras dura el test y
// restaura el entorno original al terminar.
//
// Sin esto, Load() lee el entorno REAL de quien corre los tests y los casos de
// default comparan contra valores que el test nunca puso. No es hipotético: el
// arranque documentado del repo es `set -a; . ./.env; set +a` (deploy/README.md),
// así que correr `go test ./...` en esa misma shell hacía fallar los defaults de
// storage e identity con las credenciales del .env.
//
// Se UNSETea, no se pone "": el loader resuelve con os.LookupEnv
// (wapp-shared/config/provider.go:20), donde una variable definida y vacía SÍ
// existe y gana al default — poner "" cambiaría el fallo, no lo quitaría.
func isolateEnv(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		key, val, _ := strings.Cut(kv, "=")
		if !strings.HasPrefix(key, EnvPrefix) {
			continue
		}
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("aislar entorno: unset %s: %v", key, err)
		}
		t.Cleanup(func() {
			if err := os.Setenv(key, val); err != nil {
				t.Errorf("restaurar entorno: set %s: %v", key, err)
			}
		})
	}
}

func TestDatabaseConfig_DSN(t *testing.T) {
	db := DatabaseConfig{
		Host:     "db.example",
		Port:     5433,
		User:     "u",
		Password: "p",
		Name:     "n",
		SSLMode:  "require",
	}
	want := "host=db.example port=5433 user=u password=p dbname=n sslmode=require"
	if got := db.DSN(); got != want {
		t.Fatalf("DSN: got %q, want %q", got, want)
	}
}

func TestLoad_DBEnvOverrides(t *testing.T) {
	isolateEnv(t)
	t.Setenv(EnvPrefix+"DB_HOST", "pg")
	t.Setenv(EnvPrefix+"DB_PORT", "6000")
	t.Setenv(EnvPrefix+"DB_USER", "admin")
	t.Setenv(EnvPrefix+"DB_PASSWORD", "secret")
	t.Setenv(EnvPrefix+"DB_NAME", "mydb")
	t.Setenv(EnvPrefix+"DB_SSLMODE", "require")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load devolvió error inesperado: %v", err)
	}

	want := DatabaseConfig{
		Host: "pg", Port: 6000, User: "admin",
		Password: "secret", Name: "mydb", SSLMode: "require",
	}
	if cfg.DB != want {
		t.Fatalf("DB: got %+v, want %+v", cfg.DB, want)
	}
}

func TestLoad_Defaults(t *testing.T) {
	isolateEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load devolvió error inesperado: %v", err)
	}

	want := defaults()
	if cfg != want {
		t.Fatalf("defaults: got %+v, want %+v", cfg, want)
	}
}

func TestLoad_EnvOverrides(t *testing.T) {
	isolateEnv(t)
	t.Setenv(EnvPrefix+"HTTP_ADDR", ":9090")
	t.Setenv(EnvPrefix+"LOG_LEVEL", "debug")
	t.Setenv(EnvPrefix+"LOG_JSON", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load devolvió error inesperado: %v", err)
	}

	if cfg.HTTPAddr != ":9090" {
		t.Errorf("HTTPAddr: got %q, want :9090", cfg.HTTPAddr)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel: got %q, want debug", cfg.LogLevel)
	}
	if !cfg.LogJSON {
		t.Errorf("LogJSON: got false, want true")
	}
}

func TestLoad_StorageDefaults(t *testing.T) {
	isolateEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load devolvió error inesperado: %v", err)
	}

	want := StorageConfig{
		Region:        "us-east-1",
		Bucket:        "edugo-materials",
		PresignExpiry: 15 * time.Minute,
	}
	if cfg.Storage != want {
		t.Fatalf("Storage defaults: got %+v, want %+v", cfg.Storage, want)
	}
}

func TestLoad_StorageEnvOverrides(t *testing.T) {
	isolateEnv(t)
	t.Setenv(EnvPrefix+"STORAGE_S3_REGION", "auto")
	t.Setenv(EnvPrefix+"STORAGE_S3_BUCKET", "wapp-media")
	t.Setenv(EnvPrefix+"STORAGE_S3_ACCESS_KEY_ID", "AKIA")
	t.Setenv(EnvPrefix+"STORAGE_S3_SECRET_ACCESS_KEY", "s3cr3t")
	t.Setenv(EnvPrefix+"STORAGE_S3_ENDPOINT", "https://acc.r2.cloudflarestorage.com")
	t.Setenv(EnvPrefix+"STORAGE_S3_PRESIGN_EXPIRY", "30m")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load devolvió error inesperado: %v", err)
	}

	want := StorageConfig{
		Region:          "auto",
		Bucket:          "wapp-media",
		AccessKeyID:     "AKIA",
		SecretAccessKey: "s3cr3t",
		Endpoint:        "https://acc.r2.cloudflarestorage.com",
		PresignExpiry:   30 * time.Minute,
	}
	if cfg.Storage != want {
		t.Fatalf("Storage overrides: got %+v, want %+v", cfg.Storage, want)
	}
}

func TestLoad_StoragePresignExpiryInvalidFallsBack(t *testing.T) {
	isolateEnv(t)
	t.Setenv(EnvPrefix+"STORAGE_S3_PRESIGN_EXPIRY", "no-es-duracion")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load devolvió error inesperado: %v", err)
	}
	if cfg.Storage.PresignExpiry != 15*time.Minute {
		t.Fatalf("PresignExpiry inválido debería caer al default 15m, got %v", cfg.Storage.PresignExpiry)
	}
}

func TestLoad_IdentityJWKSURLIsOffByDefault(t *testing.T) {
	isolateEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load devolvió error inesperado: %v", err)
	}
	// Sin la variable, el modo dual con identity queda apagado: cloud-platform
	// arranca sin depender de identity-core (identity Plan 003 · T1.2).
	if cfg.Identity.JWKSURL != "" {
		t.Fatalf("Identity.JWKSURL debería nacer vacía (modo dual apagado), got %q", cfg.Identity.JWKSURL)
	}
}

func TestLoad_IdentityJWKSURLEnvOverride(t *testing.T) {
	isolateEnv(t)
	t.Setenv(EnvPrefix+"IDENTITY_JWKS_URL", "http://localhost:8200/.well-known/jwks.json")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load devolvió error inesperado: %v", err)
	}
	if cfg.Identity.JWKSURL != "http://localhost:8200/.well-known/jwks.json" {
		t.Fatalf("Identity.JWKSURL: got %q", cfg.Identity.JWKSURL)
	}
}
