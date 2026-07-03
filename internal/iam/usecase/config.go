// Package usecase implementa los casos de uso del módulo IAM (puertos in/*)
// sobre los repositorios (puertos out/*) y las primitivas de wapp-shared/auth
// (JWT, glob-RBAC, bcrypt, refresh, service-token). Es la capa que NO conoce SQL
// ni HTTP: recibe repos por interface y el tenant_id siempre del contexto de
// identidad (INV-8). Los grants EFECTIVOS se resuelven AL EMITIR el token
// (cadena de roles ⊕ overrides), no por request (design.md §5).
package usecase

import "time"

// TTLs por defecto de los tokens, aplicados cuando el campo de Config es cero.
// Access corto (los grants viajan embebidos y pueden quedar obsoletos tras un
// cambio de rol; el TTL corto acota la ventana, design.md §12). Refresh largo
// (el logout lo revoca). Service token corto (M2M re-emitible).
const (
	// DefaultAccessTTL es la vida por defecto del access token de usuario.
	DefaultAccessTTL = 15 * time.Minute
	// DefaultRefreshTTL es la vida por defecto del refresh token opaco.
	DefaultRefreshTTL = 30 * 24 * time.Hour
	// DefaultServiceTTL es la vida por defecto del service token M2M.
	DefaultServiceTTL = 15 * time.Minute
)

// Config agrupa los TTLs de los tokens que emiten los usecases. Los campos en
// cero toman los defaults de arriba (los aplica withDefaults).
type Config struct {
	AccessTTL  time.Duration
	RefreshTTL time.Duration
	ServiceTTL time.Duration
}

// withDefaults devuelve una copia de cfg con los TTLs en cero sustituidos por
// sus defaults.
func (cfg Config) withDefaults() Config {
	if cfg.AccessTTL == 0 {
		cfg.AccessTTL = DefaultAccessTTL
	}
	if cfg.RefreshTTL == 0 {
		cfg.RefreshTTL = DefaultRefreshTTL
	}
	if cfg.ServiceTTL == 0 {
		cfg.ServiceTTL = DefaultServiceTTL
	}
	return cfg
}
