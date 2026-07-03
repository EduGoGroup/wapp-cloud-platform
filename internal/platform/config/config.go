// Package config define la configuración de arranque de la Plataforma Cloud y
// su carga desde archivo YAML con overlay de variables de entorno (prefijo
// WAPP_).
//
// Se apoya en github.com/EduGoGroup/wapp-shared/config para la lectura del YAML
// y el acceso tipado a variables de entorno. En el corte T0 solo cubre los
// parámetros del servidor HTTP de health y el logging; PostgreSQL y gRPC entran
// en T1/T2.
package config

import (
	"fmt"
	"os"
	"time"

	sharedconfig "github.com/EduGoGroup/wapp-shared/config"
)

// EnvPrefix es el prefijo aplicado a las variables de entorno de la Plataforma
// Cloud. Por ejemplo, la clave HTTP_ADDR se lee de la variable WAPP_HTTP_ADDR.
const EnvPrefix = "WAPP_"

// fileEnvKey es la clave (sin prefijo) que indica la ruta del archivo YAML de
// configuración. Por ejemplo, WAPP_CONFIG_FILE=/etc/wapp/platform.yaml.
const fileEnvKey = "CONFIG_FILE"

// AppConfig agrupa los parámetros mínimos de arranque de la Plataforma Cloud.
type AppConfig struct {
	// Env es la señal de entorno de ejecución: "prod" o "dev" (default "dev",
	// seguro para desarrollo). En "prod" el KeyProvider EXIGE la indexKey
	// explícita (WAPP_KEK_INDEX_B64) y falla-rápido si falta (Plan 012 §10.C).
	// Se lee de WAPP_APP_ENV.
	Env string `yaml:"env"`
	// HTTPAddr es la dirección de escucha del servidor HTTP de health/admin
	// (interno). Se lee de WAPP_HTTP_ADDR (default :8100).
	HTTPAddr string `yaml:"http_addr"`
	// PublicHTTPAddr es la dirección del SEGUNDO servidor HTTP: la API pública
	// /api/v1 para terceros (Plan 018, Decisión D/INV-7). Mismo binario, un solo
	// proceso; separado del admin para no exponer la red de administración. Se lee
	// de WAPP_PUBLIC_HTTP_ADDR (default :8103, banda 81xx).
	PublicHTTPAddr string `yaml:"public_http_addr"`
	// GRPCEnrollAddr es la dirección del servidor gRPC de Enrolamiento (TLS de
	// servidor SOLAMENTE: el Edge enrola aquí SIN cert de cliente).
	GRPCEnrollAddr string `yaml:"grpc_enroll_addr"`
	// GRPCConnectAddr es la dirección del servidor gRPC CloudLink (mTLS estricto:
	// el Edge conecta aquí con el cert emitido en el enrolamiento).
	GRPCConnectAddr string `yaml:"grpc_connect_addr"`
	// LogLevel es el nivel mínimo de logging: debug, info, warn o error.
	LogLevel string `yaml:"log_level"`
	// LogJSON selecciona el formato JSON del logger cuando es true.
	LogJSON bool `yaml:"log_json"`
	// DB es la configuración de conexión a PostgreSQL.
	DB DatabaseConfig `yaml:"db"`
	// PKI son las rutas a la CA y el cert de servidor (los genera
	// scripts/gen-dev-certs.sh).
	PKI PKIConfig `yaml:"pki"`
	// Lease es la configuración del kill-switch (clave de firma y TTL). La
	// consume el Gateway al construir el lease.Manager (cableado en T5).
	Lease LeaseConfig `yaml:"lease"`
	// Crypto es la configuración del fundamento criptográfico de PII (Plan 011,
	// ADR-0017): la KEK maestra y la indexKey del índice ciego. Se expone aquí;
	// el cableado a los repos/main entra en tramos posteriores (T1/T3).
	Crypto CryptoConfig `yaml:"crypto"`
	// Storage es la configuración del almacén de objetos Cloudflare R2 (Plan 017):
	// credenciales, endpoint y vigencia de las URLs prefirmadas. La consume el
	// main al construir el objectstore.PresignClient (cableado en tramos
	// posteriores). Se lee con prefijo WAPP_STORAGE_S3_.
	Storage StorageConfig `yaml:"storage"`
	// JWT es la configuración de firma/validación de los tokens del IAM (Plan
	// 018): secreto HS256, issuer esperado y audiencia del service token M2M. El
	// secreto NUNCA se hardcodea ni se loguea (zero-knowledge); en dev, si falta,
	// se genera uno efímero con warning (como la clave del lease). Se lee con
	// prefijo WAPP_ (WAPP_JWT_SECRET, WAPP_JWT_ISSUER, WAPP_SERVICE_JWT_AUDIENCE).
	JWT JWTConfig `yaml:"jwt"`
	// RateLimit es la configuración del rate-limit de la API pública (Plan 018 ·
	// T10, R11): límite por api-key/tenant y límite por IP en el login. Se lee con
	// prefijo WAPP_RATELIMIT_.
	RateLimit RateLimitConfig `yaml:"rate_limit"`
}

// RateLimitConfig gobierna el token-bucket EN MEMORIA de la API pública (Plan
// 018 · T10). PublicRPS/PublicBurst acotan cada credencial (api-key/tenant) en
// las rutas de operación; LoginPerMin/LoginBurst acotan por IP el
// /api/v1/auth/login (anti fuerza bruta). Defaults sanos; cero o negativo cae al
// default (nunca desactiva el límite por accidente).
type RateLimitConfig struct {
	// PublicRPS es el ritmo sostenido (peticiones/seg) por credencial en la API
	// pública. Default 20. Se lee de WAPP_RATELIMIT_PUBLIC_RPS.
	PublicRPS int `yaml:"public_rps"`
	// PublicBurst es la ráfaga admitida por credencial. Default 40. Se lee de
	// WAPP_RATELIMIT_PUBLIC_BURST.
	PublicBurst int `yaml:"public_burst"`
	// LoginPerMin es el ritmo sostenido (peticiones/min) por IP en el login.
	// Default 10. Se lee de WAPP_RATELIMIT_LOGIN_PER_MIN.
	LoginPerMin int `yaml:"login_per_min"`
	// LoginBurst es la ráfaga admitida por IP en el login. Default 5. Se lee de
	// WAPP_RATELIMIT_LOGIN_BURST.
	LoginBurst int `yaml:"login_burst"`
}

// JWTConfig agrupa el material de firma del IAM (Plan 018 §6). El Secret firma
// tanto el JWT de usuario como el service token M2M (HS256); Issuer es el emisor
// esperado en la validación; ServiceAudience es la `aud` exigida al service
// token (evita que un token de usuario se cuele por rutas M2M y viceversa).
type JWTConfig struct {
	// Secret es el secreto HS256. SIN default: en prod es obligatorio (fail-fast);
	// en dev, vacío ⇒ secreto efímero generado con warning. NUNCA se loguea.
	Secret string `yaml:"secret"`
	// Issuer es el emisor (`iss`) que se firma y se valida. Default "wapp-cloud".
	Issuer string `yaml:"issuer"`
	// ServiceAudience es la audiencia (`aud`) del service token M2M. Default
	// "wapp-public-api".
	ServiceAudience string `yaml:"service_audience"`
}

// StorageConfig agrupa los parámetros del almacén de objetos Cloudflare R2
// (S3-compatible; NO AWS) del Plan 017. R2 se apunta por Endpoint (BaseEndpoint
// del SDK) y comparte cuenta/bucket con EduGo en alpha (bucket edugo-materials,
// prefijo wapp/ en las keys). Las credenciales (AccessKeyID/SecretAccessKey) y
// el Endpoint NO tienen default: van en el .env NO versionado. Se leen con
// prefijo WAPP_STORAGE_S3_ (p. ej. WAPP_STORAGE_S3_BUCKET).
type StorageConfig struct {
	// Region es la región del SDK. R2 la ignora, pero aws-sdk-go-v2 la exige.
	Region string `yaml:"region"`
	// Bucket es el bucket de R2 (alpha: edugo-materials, compartido con EduGo).
	Bucket string `yaml:"bucket"`
	// AccessKeyID es la Access Key ID del token R2 (sin default; va en .env).
	AccessKeyID string `yaml:"access_key_id"`
	// SecretAccessKey es la Secret Access Key del token R2 (sin default; .env).
	SecretAccessKey string `yaml:"secret_access_key"`
	// Endpoint es el endpoint S3 de R2 (https://<accountid>.r2.cloudflarestorage.com;
	// sin default, va en .env).
	Endpoint string `yaml:"endpoint"`
	// PresignExpiry es la vigencia de las URLs prefirmadas (default 15m). Se lee
	// como cadena time.Duration de WAPP_STORAGE_S3_PRESIGN_EXPIRY.
	PresignExpiry time.Duration `yaml:"presign_expiry"`
}

// CryptoConfig agrupa el material de clave del cifrado de PII en reposo (Plan
// 011). La KEK maestra (KEKMasterB64) envuelve las DEKs por-valor; la indexKey
// (KEKIndexB64) alimenta el índice ciego HMAC. Ambas van en base64 estándar y
// SEPARADAS del dato de negocio (no viven en la BD; §10.A). Se leen con prefijo
// WAPP_ (p. ej. WAPP_KEK_MASTER_B64), en paralelo a la clave del lease.
type CryptoConfig struct {
	// KEKKeyring es el keyring versionado del Plan 012: entradas "id:base64"
	// (cada KEK 32B AES-256) separadas por coma. Con él, WrapDEK usa la KEK
	// KEKCurrent y UnwrapDEK selecciona por key_id, habilitando la rotación sin
	// re-cifrar. Vacío = camino compat con KEKMasterB64 (key_id "1"). Se lee de
	// WAPP_KEK_KEYRING.
	KEKKeyring string `yaml:"kek_keyring"`
	// KEKCurrent es el key_id de la KEK current dentro de KEKKeyring (la que
	// envuelve las DEK nuevas). Obligatorio cuando KEKKeyring viene y debe existir
	// en él. Se lee de WAPP_KEK_CURRENT.
	KEKCurrent string `yaml:"kek_current"`
	// KEKMasterB64 es la KEK maestra única del Plan 011 (32B, AES-256) en base64.
	// Camino de compatibilidad: si no hay keyring, se carga como el key_id inicial
	// "1" y es la current. Su ausencia (junto con KEKKeyring) hace fallar-rápido al
	// construir el KeyProvider. Se lee de WAPP_KEK_MASTER_B64.
	KEKMasterB64 string `yaml:"kek_master_b64"`
	// KEKIndexB64 es la indexKey del índice ciego (32B) en base64, INDEPENDIENTE de
	// la KEK (Plan 012 §10.C). OBLIGATORIA en prod (fail-fast si falta) y estable de
	// por vida (cambiarla = reindexar value_bidx, que es PK). En dev, si queda vacía
	// se deriva de la KEK por HKDF-SHA256 con warning. Se lee de WAPP_KEK_INDEX_B64.
	KEKIndexB64 string `yaml:"kek_index_b64"`
	// CloudEncPrivKeyB64 es la clave privada X25519 (32B) del par de cifrado de
	// tránsito de la nube (Plan 011 §10.F), en base64 estándar. Con ella la nube
	// abre (OpenWith) el enc_payload sellado por el Edge; su pública se publica al
	// Edge en el enrolamiento. Es DISTINTA de la Ed25519 del lease y de la DEK.
	// Vacía = se genera un par efímero de dev (no apta para producción), como la
	// clave del lease. Se lee de WAPP_CLOUD_ENC_PRIVKEY_B64.
	CloudEncPrivKeyB64 string `yaml:"cloud_enc_privkey_b64"`
}

// PKIConfig agrupa las rutas de la PKI del Gateway. El cert de servidor es
// compartido por ambos listeners (enroll y connect); la CA firma los certs de
// Edge en el enrolamiento y valida a los Edges en el mTLS de CloudLink. La clave
// de la CA (CAKeyFile) la necesita el servicio de enrolamiento para firmar CSRs.
// Los defaults coinciden con scripts/gen-dev-certs.sh (directorio certs/). Se
// leen con prefijo WAPP_PKI_ (p. ej. WAPP_PKI_CA_KEY_FILE).
type PKIConfig struct {
	// ServerCertFile es el PEM del cert de servidor (SAN localhost en dev).
	ServerCertFile string `yaml:"server_cert_file"`
	// ServerKeyFile es el PEM de la clave privada del cert de servidor.
	ServerKeyFile string `yaml:"server_key_file"`
	// CACertFile es el PEM del cert de la CA (firma certs de Edge y los valida).
	CACertFile string `yaml:"ca_cert_file"`
	// CAKeyFile es el PEM de la clave privada de la CA (firma CSRs en el enroll).
	CAKeyFile string `yaml:"ca_key_file"`
}

// LeaseConfig agrupa la configuración del lease del Gateway (ADR-0007). La clave
// privada Ed25519 firma los leases; precedencia: archivo PEM > base64 >
// generación efímera de dev (si ambos quedan vacíos). Se lee con prefijo
// WAPP_LEASE_ (p. ej. WAPP_LEASE_PRIVATE_KEY_B64).
type LeaseConfig struct {
	// PrivateKeyFile es la ruta a un PEM PKCS#8 con la clave Ed25519. Tiene
	// prioridad sobre PrivateKeyB64. Vacío = no usar archivo.
	PrivateKeyFile string `yaml:"private_key_file"`
	// PrivateKeyB64 es la clave Ed25519 (semilla de 32B o clave de 64B) en base64.
	// Vacío = no usar base64. Si también PrivateKeyFile está vacío, se genera una
	// clave de dev efímera (NO apta para producción).
	PrivateKeyB64 string `yaml:"private_key_b64"`
	// TTLMinutes es la vigencia del lease en minutos. <=0 usa el default del
	// gestor (5 min). Se renueva en cada Heartbeat del Edge.
	TTLMinutes int `yaml:"ttl_minutes"`
}

// DatabaseConfig agrupa los parámetros de conexión a PostgreSQL. Se lee de las
// variables de entorno con prefijo WAPP_DB_ (p. ej. WAPP_DB_HOST) y/o del YAML.
// Los defaults coinciden con deploy/docker-compose.yml para arranque local.
type DatabaseConfig struct {
	// Host es el hostname del servidor PostgreSQL.
	Host string `yaml:"host"`
	// Port es el puerto TCP del servidor PostgreSQL.
	Port int `yaml:"port"`
	// User es el usuario de conexión.
	User string `yaml:"user"`
	// Password es la contraseña de conexión.
	Password string `yaml:"password"`
	// Name es el nombre de la base de datos.
	Name string `yaml:"name"`
	// SSLMode es el modo SSL de libpq (disable, require, verify-full, …).
	SSLMode string `yaml:"sslmode"`
}

// DSN construye la cadena de conexión en formato keyword/value de libpq, apta
// para pgx (sql.Open("pgx", dsn)).
func (d DatabaseConfig) DSN() string {
	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		d.Host, d.Port, d.User, d.Password, d.Name, d.SSLMode,
	)
}

// defaults devuelve la configuración con valores por defecto sensatos.
func defaults() AppConfig {
	return AppConfig{
		Env:             "dev",
		HTTPAddr:        ":8100",
		PublicHTTPAddr:  ":8103",
		GRPCEnrollAddr:  ":8102",
		GRPCConnectAddr: ":8101",
		LogLevel:        "info",
		LogJSON:         false,
		JWT: JWTConfig{
			Issuer:          "wapp-cloud",
			ServiceAudience: "wapp-public-api",
		},
		RateLimit: RateLimitConfig{
			PublicRPS:   20,
			PublicBurst: 40,
			LoginPerMin: 10,
			LoginBurst:  5,
		},
		DB: DatabaseConfig{
			Host:     "localhost",
			Port:     5432,
			User:     "wapp",
			Password: "wapp",
			Name:     "wapp_cloud",
			SSLMode:  "disable",
		},
		PKI: PKIConfig{
			ServerCertFile: "certs/server.crt",
			ServerKeyFile:  "certs/server.key",
			CACertFile:     "certs/ca.crt",
			CAKeyFile:      "certs/ca.key",
		},
		Storage: StorageConfig{
			Region:        "us-east-1",
			Bucket:        "edugo-materials",
			PresignExpiry: 15 * time.Minute,
		},
	}
}

// Load construye la configuración de la Plataforma Cloud.
//
// Orden de precedencia (de menor a mayor): valores por defecto, archivo YAML
// indicado por WAPP_CONFIG_FILE (opcional; si no existe se ignora) y variables
// de entorno con prefijo WAPP_. Devuelve error solo si el YAML existe pero no
// puede leerse o parsearse.
func Load() (AppConfig, error) {
	cfg := defaults()

	loader := sharedconfig.New(
		sharedconfig.WithEnvPrefix(EnvPrefix),
		sharedconfig.WithFile(os.Getenv(EnvPrefix+fileEnvKey)),
	)

	if err := loader.Unmarshal(&cfg); err != nil {
		return AppConfig{}, err
	}

	// Overlay de entorno: usa el valor actual (default o YAML) como fallback.
	cfg.Env = loader.GetString("APP_ENV", cfg.Env)
	cfg.HTTPAddr = loader.GetString("HTTP_ADDR", cfg.HTTPAddr)
	cfg.PublicHTTPAddr = loader.GetString("PUBLIC_HTTP_ADDR", cfg.PublicHTTPAddr)
	cfg.GRPCEnrollAddr = loader.GetString("GRPC_ENROLL_ADDR", cfg.GRPCEnrollAddr)
	cfg.GRPCConnectAddr = loader.GetString("GRPC_CONNECT_ADDR", cfg.GRPCConnectAddr)
	cfg.LogLevel = loader.GetString("LOG_LEVEL", cfg.LogLevel)
	cfg.LogJSON = loader.GetBool("LOG_JSON", cfg.LogJSON)

	cfg.DB.Host = loader.GetString("DB_HOST", cfg.DB.Host)
	cfg.DB.Port = loader.GetInt("DB_PORT", cfg.DB.Port)
	cfg.DB.User = loader.GetString("DB_USER", cfg.DB.User)
	cfg.DB.Password = loader.GetString("DB_PASSWORD", cfg.DB.Password)
	cfg.DB.Name = loader.GetString("DB_NAME", cfg.DB.Name)
	cfg.DB.SSLMode = loader.GetString("DB_SSLMODE", cfg.DB.SSLMode)

	cfg.PKI.ServerCertFile = loader.GetString("PKI_SERVER_CERT_FILE", cfg.PKI.ServerCertFile)
	cfg.PKI.ServerKeyFile = loader.GetString("PKI_SERVER_KEY_FILE", cfg.PKI.ServerKeyFile)
	cfg.PKI.CACertFile = loader.GetString("PKI_CA_CERT_FILE", cfg.PKI.CACertFile)
	cfg.PKI.CAKeyFile = loader.GetString("PKI_CA_KEY_FILE", cfg.PKI.CAKeyFile)

	cfg.Lease.PrivateKeyFile = loader.GetString("LEASE_PRIVATE_KEY_FILE", cfg.Lease.PrivateKeyFile)
	cfg.Lease.PrivateKeyB64 = loader.GetString("LEASE_PRIVATE_KEY_B64", cfg.Lease.PrivateKeyB64)
	cfg.Lease.TTLMinutes = loader.GetInt("LEASE_TTL_MINUTES", cfg.Lease.TTLMinutes)

	cfg.Crypto.KEKKeyring = loader.GetString("KEK_KEYRING", cfg.Crypto.KEKKeyring)
	cfg.Crypto.KEKCurrent = loader.GetString("KEK_CURRENT", cfg.Crypto.KEKCurrent)
	cfg.Crypto.KEKMasterB64 = loader.GetString("KEK_MASTER_B64", cfg.Crypto.KEKMasterB64)
	cfg.Crypto.KEKIndexB64 = loader.GetString("KEK_INDEX_B64", cfg.Crypto.KEKIndexB64)
	cfg.Crypto.CloudEncPrivKeyB64 = loader.GetString("CLOUD_ENC_PRIVKEY_B64", cfg.Crypto.CloudEncPrivKeyB64)

	cfg.Storage.Region = loader.GetString("STORAGE_S3_REGION", cfg.Storage.Region)
	cfg.Storage.Bucket = loader.GetString("STORAGE_S3_BUCKET", cfg.Storage.Bucket)
	cfg.Storage.AccessKeyID = loader.GetString("STORAGE_S3_ACCESS_KEY_ID", cfg.Storage.AccessKeyID)
	cfg.Storage.SecretAccessKey = loader.GetString("STORAGE_S3_SECRET_ACCESS_KEY", cfg.Storage.SecretAccessKey)
	cfg.Storage.Endpoint = loader.GetString("STORAGE_S3_ENDPOINT", cfg.Storage.Endpoint)
	cfg.Storage.PresignExpiry = getDuration(loader, "STORAGE_S3_PRESIGN_EXPIRY", cfg.Storage.PresignExpiry)

	cfg.JWT.Secret = loader.GetString("JWT_SECRET", cfg.JWT.Secret)
	cfg.JWT.Issuer = loader.GetString("JWT_ISSUER", cfg.JWT.Issuer)
	cfg.JWT.ServiceAudience = loader.GetString("SERVICE_JWT_AUDIENCE", cfg.JWT.ServiceAudience)

	cfg.RateLimit.PublicRPS = loader.GetInt("RATELIMIT_PUBLIC_RPS", cfg.RateLimit.PublicRPS)
	cfg.RateLimit.PublicBurst = loader.GetInt("RATELIMIT_PUBLIC_BURST", cfg.RateLimit.PublicBurst)
	cfg.RateLimit.LoginPerMin = loader.GetInt("RATELIMIT_LOGIN_PER_MIN", cfg.RateLimit.LoginPerMin)
	cfg.RateLimit.LoginBurst = loader.GetInt("RATELIMIT_LOGIN_BURST", cfg.RateLimit.LoginBurst)

	return cfg, nil
}

// getDuration lee una clave como cadena time.Duration (p. ej. "15m", "30s") y la
// parsea. El loader de wapp-shared solo expone GetString/GetInt/GetBool (no un
// GetDuration), así que se parsea aquí: vacío o inválido cae al default def.
func getDuration(loader *sharedconfig.Loader, key string, def time.Duration) time.Duration {
	raw := loader.GetString(key, "")
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return def
	}
	return d
}
