// Package usecase implementa los casos de uso del módulo IAM (puertos in/*)
// sobre los repositorios (puertos out/*) y las primitivas de wapp-shared/auth y
// identity-shared/auth (JWT, glob-RBAC). Es la capa que NO conoce SQL ni HTTP:
// recibe repos por interface y el tenant_id siempre del contexto de identidad
// (INV-8). Los grants EFECTIVOS se resuelven AL EMITIR el token (cadena de roles
// ⊕ overrides), no por request (design.md §5).
//
// Desde la Ola 5 del Plan 003 de identity, aquí NO se validan credenciales ni se
// custodian sesiones: eso es de identity-core. Lo que queda es el canje
// (ExchangeService), su delegación para el relé del Edge (DelegatedAuthService),
// la verificación del Context Token propio (ContextTokenService) y la auditoría.
package usecase

import "time"

// DefaultAccessTTL es la vida por defecto del Context Token, aplicada cuando el
// campo de Config va en cero. Corto a propósito: los grants viajan embebidos y
// pueden quedar obsoletos tras un cambio de rol, y el TTL acota esa ventana
// (design.md §12). Además queda SIEMPRE acotado por el `exp` del Identity Token
// que lo originó (REQ-A2).
const DefaultAccessTTL = 15 * time.Minute

// Config agrupa los TTLs de los tokens que emiten los usecases. Los campos en
// cero toman su default (lo aplica withDefaults).
type Config struct {
	AccessTTL time.Duration
}

// withDefaults devuelve una copia de cfg con los TTLs en cero sustituidos por
// sus defaults.
func (cfg Config) withDefaults() Config {
	if cfg.AccessTTL == 0 {
		cfg.AccessTTL = DefaultAccessTTL
	}
	return cfg
}
