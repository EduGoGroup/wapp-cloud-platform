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
	// HTTPAddr es la dirección de escucha del servidor HTTP de health/admin.
	HTTPAddr string `yaml:"http_addr"`
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
}

// CryptoConfig agrupa el material de clave del cifrado de PII en reposo (Plan
// 011). La KEK maestra (KEKMasterB64) envuelve las DEKs por-valor; la indexKey
// (KEKIndexB64) alimenta el índice ciego HMAC. Ambas van en base64 estándar y
// SEPARADAS del dato de negocio (no viven en la BD; §10.A). Se leen con prefijo
// WAPP_ (p. ej. WAPP_KEK_MASTER_B64), en paralelo a la clave del lease.
type CryptoConfig struct {
	// KEKMasterB64 es la KEK maestra (32B, AES-256) en base64. Obligatoria para
	// operar el cifrado; su ausencia hace fallar-rápido al construir el
	// KeyProvider (cableado en T3), no aquí.
	KEKMasterB64 string `yaml:"kek_master_b64"`
	// KEKIndexB64 es la indexKey del índice ciego (32B) en base64. Opcional: si
	// queda vacía, se deriva de la KEK maestra vía HKDF-SHA256.
	KEKIndexB64 string `yaml:"kek_index_b64"`
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
		HTTPAddr:        ":8080",
		GRPCEnrollAddr:  ":8444",
		GRPCConnectAddr: ":8443",
		LogLevel:        "info",
		LogJSON:         false,
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
	cfg.HTTPAddr = loader.GetString("HTTP_ADDR", cfg.HTTPAddr)
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

	cfg.Crypto.KEKMasterB64 = loader.GetString("KEK_MASTER_B64", cfg.Crypto.KEKMasterB64)
	cfg.Crypto.KEKIndexB64 = loader.GetString("KEK_INDEX_B64", cfg.Crypto.KEKIndexB64)

	return cfg, nil
}
