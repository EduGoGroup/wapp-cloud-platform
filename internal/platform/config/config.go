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
}

// defaults devuelve la configuración con valores por defecto sensatos.
func defaults() AppConfig {
	return AppConfig{
		HTTPAddr: ":8080",
		LogLevel: "info",
		LogJSON:  false,
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

	return cfg, nil
}
