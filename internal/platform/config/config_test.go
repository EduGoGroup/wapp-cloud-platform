package config

import (
	"testing"
	"time"
)

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
	t.Setenv(EnvPrefix+"STORAGE_S3_PRESIGN_EXPIRY", "no-es-duracion")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load devolvió error inesperado: %v", err)
	}
	if cfg.Storage.PresignExpiry != 15*time.Minute {
		t.Fatalf("PresignExpiry inválido debería caer al default 15m, got %v", cfg.Storage.PresignExpiry)
	}
}
