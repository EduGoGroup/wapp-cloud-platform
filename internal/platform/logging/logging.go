// Package logging adapta el logger estructurado de wapp-shared a la
// configuración de la Plataforma Cloud (nivel y formato JSON tomados de
// config.AppConfig). Copia-adaptación del adaptador homólogo del Edge Agent.
package logging

import (
	"log/slog"
	"strings"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/config"
	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"
)

// New construye el Logger de la Plataforma Cloud a partir de la configuración
// dada, aplicando nivel y formato (texto/JSON) según cfg.
func New(cfg config.AppConfig) sharedlogger.Logger {
	return sharedlogger.New(
		sharedlogger.WithLevel(ParseLevel(cfg.LogLevel)),
		sharedlogger.WithJSON(cfg.LogJSON),
	)
}

// ParseLevel traduce un nivel textual (debug, info, warn/warning, error) al
// slog.Level correspondiente. Cualquier valor desconocido o vacío devuelve
// slog.LevelInfo.
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
