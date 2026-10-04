// Porta internal/iam/usecase/config.go @ 9a77307

// Package usecase implementa los casos de uso del IAM del módulo acceso (puertos
// iam/ports/in) sobre los repositorios (puertos iam/ports/out) y las primitivas de
// wapp-shared/auth e identity-shared/auth (JWT, glob-RBAC). Es la capa que NO conoce SQL ni
// HTTP: recibe repos por interface y el tenant_id siempre del contexto de identidad (INV-8).
// Los grants EFECTIVOS se resuelven AL EMITIR el token (cadena de roles ⊕ overrides), no por
// request (design.md §5).
//
// Aquí NO se validan credenciales ni se custodian sesiones: eso es de identity-core (identity
// Plan 003 · Ola 5). Lo que queda es el canje (ExchangeService), su delegación para el relé del
// Edge (DelegatedAuthService), la verificación del Context Token propio
// (ContextTokenService), la auditoría (AuditService), la elección de empresa
// (ActiveTenantService) y la administración de roles, miembros e invitaciones de una empresa
// (RoleService, MembershipService, InvitationService, RedeemService).
//
// Regla común de los constructores: toda dependencia estructural nil se rechaza al arrancar
// (fail-fast) con un error cuyo texto es "iam: <Servicio> requiere …"; un servicio a medias no
// se construye.
package usecase

import "time"

// DefaultAccessTTL es la vida por defecto del Context Token: 15 minutos, aplicada cuando el
// campo AccessTTL de Config va en cero. Corto a propósito: los grants viajan embebidos y pueden
// quedar obsoletos tras un cambio de rol, y el TTL acota esa ventana (design.md §12). Además
// queda SIEMPRE acotado por el `exp` del Identity Token que lo originó (REQ-A2, ver
// ExchangeService).
const DefaultAccessTTL = 15 * time.Minute

// Config agrupa los TTLs de los tokens que emiten los usecases (R-U33). Un campo en cero toma
// su default (AccessTTL → DefaultAccessTTL); un valor distinto de cero se respeta tal cual. El
// default lo aplica el constructor que recibe la Config, sobre una COPIA: la Config del
// llamante no se modifica.
type Config struct {
	// AccessTTL es la vida del Context Token. Cero = DefaultAccessTTL.
	AccessTTL time.Duration
}

// withDefaults devuelve una copia de cfg con los TTLs en cero sustituidos por sus defaults.
func (cfg Config) withDefaults() Config {
	if cfg.AccessTTL == 0 {
		cfg.AccessTTL = DefaultAccessTTL
	}
	return cfg
}
