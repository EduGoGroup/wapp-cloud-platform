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
	// LogLevel es el nivel mínimo de logging: debug, info, warn o error.
	LogLevel string `yaml:"log_level"`
	// LogJSON selecciona el formato JSON del logger cuando es true.
	LogJSON bool `yaml:"log_json"`
	// DB es la configuración de conexión a PostgreSQL.
	DB DatabaseConfig `yaml:"db"`
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
		HTTPAddr: ":8080",
		LogLevel: "info",
		LogJSON:  false,
		DB: DatabaseConfig{
			Host:     "localhost",
			Port:     5432,
			User:     "wapp",
			Password: "wapp",
			Name:     "wapp_cloud",
			SSLMode:  "disable",
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
	cfg.LogLevel = loader.GetString("LOG_LEVEL", cfg.LogLevel)
	cfg.LogJSON = loader.GetBool("LOG_JSON", cfg.LogJSON)

	cfg.DB.Host = loader.GetString("DB_HOST", cfg.DB.Host)
	cfg.DB.Port = loader.GetInt("DB_PORT", cfg.DB.Port)
	cfg.DB.User = loader.GetString("DB_USER", cfg.DB.User)
	cfg.DB.Password = loader.GetString("DB_PASSWORD", cfg.DB.Password)
	cfg.DB.Name = loader.GetString("DB_NAME", cfg.DB.Name)
	cfg.DB.SSLMode = loader.GetString("DB_SSLMODE", cfg.DB.SSLMode)

	return cfg, nil
}
