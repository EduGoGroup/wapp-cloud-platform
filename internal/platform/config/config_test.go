package config

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
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

	// La comparación es de la struct ENTERA, así que el pool entra aquí aunque
	// este test no lo sobreescriba: se afirma que los cuatro campos siguen en su
	// default. Es deliberado — si un día alguien mueve un default del pool, este
	// test lo cuenta además del suyo propio (TestLoad_DatabasePool).
	want := DatabaseConfig{
		Host: "pg", Port: 6000, User: "admin",
		Password: "secret", Name: "mydb", SSLMode: "require",
		MaxOpenConns:    postgres.DefaultMaxOpenConns,
		MaxIdleConns:    postgres.DefaultMaxIdleConns,
		ConnMaxLifetime: postgres.DefaultConnMaxLifetime,
		ConnMaxIdleTime: postgres.DefaultConnMaxIdleTime,
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

// TestLoad_GRPCAckTimeout cubre el reloj de la espera del Ack (env
// WAPP_GRPC_ACK_TIMEOUT). Sin él, el select del Ack esperaba contra un contexto
// sin deadline y un Edge saturado colgaba al llamante HTTP indefinidamente
// (incidente del 2026-08-06: 88s sin respuesta ni log).
func TestLoad_GRPCAckTimeout(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		isolateEnv(t)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load devolvió error inesperado: %v", err)
		}
		if cfg.GRPCAckTimeout != 8*time.Second {
			t.Errorf("GRPCAckTimeout: got %v, want 8s", cfg.GRPCAckTimeout)
		}
		// INVARIANTE: por debajo del WriteTimeout del servidor HTTP (10s,
		// internal/bootstrap/http.go). Si no, el 504 se genera con el deadline de
		// escritura vencido y el cliente sigue viendo la conexión cerrada sin cuerpo.
		if cfg.GRPCAckTimeout >= 10*time.Second {
			t.Errorf("GRPCAckTimeout=%v >= WriteTimeout HTTP (10s): el 504 no llegaría a escribirse", cfg.GRPCAckTimeout)
		}
	})

	t.Run("override por env", func(t *testing.T) {
		isolateEnv(t)
		t.Setenv(EnvPrefix+"GRPC_ACK_TIMEOUT", "3s")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load devolvió error inesperado: %v", err)
		}
		if cfg.GRPCAckTimeout != 3*time.Second {
			t.Errorf("GRPCAckTimeout: got %v, want 3s", cfg.GRPCAckTimeout)
		}
	})

	// Un valor no parseable cae al default en vez de dejar el campo en cero: cero
	// sería "sin reloj", exactamente el bug que este ajuste elimina. La red de
	// seguridad final está en gatewaygrpc.New, que también materializa <=0.
	t.Run("valor inválido cae al default", func(t *testing.T) {
		isolateEnv(t)
		t.Setenv(EnvPrefix+"GRPC_ACK_TIMEOUT", "no-es-duracion")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load devolvió error inesperado: %v", err)
		}
		if cfg.GRPCAckTimeout != 8*time.Second {
			t.Errorf("GRPCAckTimeout: got %v, want 8s (default)", cfg.GRPCAckTimeout)
		}
	})
}

// TestLoad_GatewayWorkLane cubre las dos perillas del carril de trabajo del stream
// CloudLink (Plan 050 · Ola 1, ADR-0040): el tope de cola POR SESIÓN y el
// presupuesto de pared por trabajo. Sin ellas, cada frame del Edge arrancaba una
// goroutine sin techo y sin reloj.
func TestLoad_GatewayWorkLane(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		isolateEnv(t)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load devolvió error inesperado: %v", err)
		}
		if cfg.GatewayWorkQueue != 64 {
			t.Errorf("GatewayWorkQueue: got %d, want 64", cfg.GatewayWorkQueue)
		}
		if cfg.GatewayWorkTimeout != 5*time.Second {
			t.Errorf("GatewayWorkTimeout: got %v, want 5s", cfg.GatewayWorkTimeout)
		}
		// El 64 no es un número redondo cualquiera: está igualado al techo de
		// entrantes concurrentes del runtime de flujos para que ninguna de las dos
		// colas sea el cuello por accidente. Si alguien mueve una, que mueva la otra.
		if cfg.GatewayWorkQueue != cfg.Flow.MaxConcurrentIncoming {
			t.Errorf("GatewayWorkQueue=%d != Flow.MaxConcurrentIncoming=%d: los defaults van igualados",
				cfg.GatewayWorkQueue, cfg.Flow.MaxConcurrentIncoming)
		}
	})

	t.Run("override por env", func(t *testing.T) {
		isolateEnv(t)
		t.Setenv(EnvPrefix+"GATEWAY_WORK_QUEUE", "8")
		t.Setenv(EnvPrefix+"GATEWAY_WORK_TIMEOUT", "2s")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load devolvió error inesperado: %v", err)
		}
		if cfg.GatewayWorkQueue != 8 {
			t.Errorf("GatewayWorkQueue: got %d, want 8", cfg.GatewayWorkQueue)
		}
		if cfg.GatewayWorkTimeout != 2*time.Second {
			t.Errorf("GatewayWorkTimeout: got %v, want 2s", cfg.GatewayWorkTimeout)
		}
	})

	// Un valor no parseable cae al default en vez de dejar el campo en cero: cero
	// sería "cola sin tope" y "trabajo sin reloj", los dos defectos que el carril
	// elimina. La red de seguridad final está en gatewaygrpc.New, que también
	// materializa <=0.
	t.Run("valor inválido cae al default", func(t *testing.T) {
		isolateEnv(t)
		t.Setenv(EnvPrefix+"GATEWAY_WORK_QUEUE", "no-es-entero")
		t.Setenv(EnvPrefix+"GATEWAY_WORK_TIMEOUT", "no-es-duracion")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load devolvió error inesperado: %v", err)
		}
		if cfg.GatewayWorkQueue != 64 {
			t.Errorf("GatewayWorkQueue: got %d, want 64 (default)", cfg.GatewayWorkQueue)
		}
		if cfg.GatewayWorkTimeout != 5*time.Second {
			t.Errorf("GatewayWorkTimeout: got %v, want 5s (default)", cfg.GatewayWorkTimeout)
		}
	})
}

// TestLoad_PublicAPIDBTimeout cubre el plazo de las consultas a BD de la API
// pública (env WAPP_PUBLICAPI_DB_TIMEOUT, Plan 050 · Ola 3). El valor no se
// justifica solo: se calibra contra la SUMA con el reloj del Ack, porque los dos
// relojes del envío corren SECUENCIALMENTE (guarda de tenant, luego empuje).
func TestLoad_PublicAPIDBTimeout(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		isolateEnv(t)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load devolvió error inesperado: %v", err)
		}
		if cfg.PublicAPIDBTimeout != 1500*time.Millisecond {
			t.Errorf("PublicAPIDBTimeout: got %v, want 1.5s", cfg.PublicAPIDBTimeout)
		}
		// INVARIANTE (REQ-050.10, INV-050.6): lo que cuenta contra el WriteTimeout del
		// servidor HTTP (10s, internal/bootstrap/http.go) es la SUMA de los dos plazos,
		// no cada uno. Con 3s aquí la suma sería 11s y el 504 se generaría con el
		// deadline de escritura vencido — el síntoma que GRPCAckTimeout eliminó.
		if cfg.PublicAPIDBTimeout+cfg.GRPCAckTimeout >= 10*time.Second {
			t.Errorf("PublicAPIDBTimeout(%v)+GRPCAckTimeout(%v) >= WriteTimeout HTTP (10s): el 504 no llegaría a escribirse",
				cfg.PublicAPIDBTimeout, cfg.GRPCAckTimeout)
		}
	})

	t.Run("override por env", func(t *testing.T) {
		isolateEnv(t)
		t.Setenv(EnvPrefix+"PUBLICAPI_DB_TIMEOUT", "700ms")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load devolvió error inesperado: %v", err)
		}
		if cfg.PublicAPIDBTimeout != 700*time.Millisecond {
			t.Errorf("PublicAPIDBTimeout: got %v, want 700ms", cfg.PublicAPIDBTimeout)
		}
	})

	// Un valor no parseable cae al default en vez de dejar el campo en cero: cero
	// sería "consulta sin plazo", exactamente el hueco que este reloj cierra.
	t.Run("valor inválido cae al default", func(t *testing.T) {
		isolateEnv(t)
		t.Setenv(EnvPrefix+"PUBLICAPI_DB_TIMEOUT", "no-es-duracion")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load devolvió error inesperado: %v", err)
		}
		if cfg.PublicAPIDBTimeout != 1500*time.Millisecond {
			t.Errorf("PublicAPIDBTimeout: got %v, want 1.5s (default)", cfg.PublicAPIDBTimeout)
		}
	})
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

// TestLoad_DatabasePool cubre el pool de conexiones a PostgreSQL, configurable
// por WAPP_DB_* desde el Plan 050 · Ola 4 (T4.1/T4.2).
//
// El subtest del negativo es el que importa: es la red de ARRIBA de las dos que
// protegen el mismo síntoma. La de ABAJO —postgres.applyPool descartando el
// negativo— tiene su propio test en el paquete postgres, y este NO pasa por ella:
// mira cfg.DB.*, no un *sql.DB. Así, quitar cualquiera de las dos guardas tumba
// exactamente un test, que es lo que hace que ninguna de las dos parezca
// redundante y se borre.
func TestLoad_DatabasePool(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		isolateEnv(t)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load devolvió error inesperado: %v", err)
		}
		assertPoolDefaults(t, cfg)
	})

	t.Run("override por env", func(t *testing.T) {
		isolateEnv(t)
		t.Setenv(EnvPrefix+"DB_MAX_OPEN_CONNS", "90")
		t.Setenv(EnvPrefix+"DB_MAX_IDLE_CONNS", "30")
		t.Setenv(EnvPrefix+"DB_CONN_MAX_LIFETIME", "30m")
		t.Setenv(EnvPrefix+"DB_CONN_MAX_IDLE_TIME", "2m")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load devolvió error inesperado: %v", err)
		}
		assertPool(t, cfg, 90, 30, 30*time.Minute, 2*time.Minute)
	})

	t.Run("valor inválido cae al default", func(t *testing.T) {
		isolateEnv(t)
		t.Setenv(EnvPrefix+"DB_MAX_OPEN_CONNS", "no-es-entero")
		t.Setenv(EnvPrefix+"DB_MAX_IDLE_CONNS", "no-es-entero")
		t.Setenv(EnvPrefix+"DB_CONN_MAX_LIFETIME", "no-es-duracion")
		t.Setenv(EnvPrefix+"DB_CONN_MAX_IDLE_TIME", "no-es-duracion")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load devolvió error inesperado: %v", err)
		}
		assertPoolDefaults(t, cfg)
	})

	// Un negativo NO desactiva el tope: en database/sql un maxOpen negativo
	// significa pool ILIMITADO y un lifetime negativo, conexiones eternas. Contra
	// Neon eso no da un error, da conexiones abiertas hasta que la base las
	// rechaza. Aquí se descarta y manda el default.
	t.Run("valor negativo cae al default y no desactiva el tope", func(t *testing.T) {
		isolateEnv(t)
		t.Setenv(EnvPrefix+"DB_MAX_OPEN_CONNS", "-1")
		t.Setenv(EnvPrefix+"DB_MAX_IDLE_CONNS", "-1")
		t.Setenv(EnvPrefix+"DB_CONN_MAX_LIFETIME", "-1s")
		t.Setenv(EnvPrefix+"DB_CONN_MAX_IDLE_TIME", "-1s")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load devolvió error inesperado: %v", err)
		}
		assertPoolDefaults(t, cfg)
	})
}

// assertPool afirma los cuatro parámetros del pool de una vez. Está extraído a
// función NOMBRADA, no repetido dentro de cada t.Run: los subtests inline imputan
// su complejidad a la función madre y TestLoad_DatabasePool tiene cuatro.
func assertPool(t *testing.T, cfg AppConfig, wantOpen, wantIdle int, wantLifetime, wantIdleTime time.Duration) {
	t.Helper()
	if cfg.DB.MaxOpenConns != wantOpen {
		t.Errorf("DB.MaxOpenConns: got %d, want %d", cfg.DB.MaxOpenConns, wantOpen)
	}
	if cfg.DB.MaxIdleConns != wantIdle {
		t.Errorf("DB.MaxIdleConns: got %d, want %d", cfg.DB.MaxIdleConns, wantIdle)
	}
	if cfg.DB.ConnMaxLifetime != wantLifetime {
		t.Errorf("DB.ConnMaxLifetime: got %v, want %v", cfg.DB.ConnMaxLifetime, wantLifetime)
	}
	if cfg.DB.ConnMaxIdleTime != wantIdleTime {
		t.Errorf("DB.ConnMaxIdleTime: got %v, want %v", cfg.DB.ConnMaxIdleTime, wantIdleTime)
	}
}

// assertPoolDefaults afirma que los cuatro parámetros quedaron en el default del
// paquete postgres. Se compara contra las CONSTANTES, no contra literales: son la
// única fuente del valor, y copiarlas aquí haría que el test dejara de detectar
// precisamente la divergencia que justifica exportarlas.
func assertPoolDefaults(t *testing.T, cfg AppConfig) {
	t.Helper()
	assertPool(t, cfg,
		postgres.DefaultMaxOpenConns,
		postgres.DefaultMaxIdleConns,
		postgres.DefaultConnMaxLifetime,
		postgres.DefaultConnMaxIdleTime,
	)
}
