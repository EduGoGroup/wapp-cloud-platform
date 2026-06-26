package config

import "testing"

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
