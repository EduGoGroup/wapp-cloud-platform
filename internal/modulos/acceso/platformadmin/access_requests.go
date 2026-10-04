// Porta internal/platformadmin/access_requests.go @ 9a77307: las REGLAS y los handlers de la
// bandeja de solicitudes de acceso. Su SQL (líneas 135-336 y 481-526 del viejo) vive en
// access_requests_postgres.go (D-F2-3), detrás del puerto AccessRequestStore de ports.go.

package platformadmin

import (
	"errors"
	"time"
)

var (
	// ErrPlatformSystemForbidden se devuelve cuando la aprobación intenta conceder wapp.platform
	// desde la bandeja de solicitudes de acceso. Decisión de Jhoan (2026-08-15, Plan 056 Tanda
	// 2): la consola de plataforma NO se concede por esta vía; el servidor es el SEGUNDO cerrojo
	// -- la consola quita la casilla, pero esto no se fía del cliente.
	ErrPlatformSystemForbidden = errors.New("platformadmin: wapp.platform no se concede desde la bandeja de solicitudes de acceso")

	// ErrSystemsUnionUnavailable se devuelve cuando la aprobación tendría que UNIR los systems
	// nuevos con los que el usuario YA tiene y NO pudo LEER su conjunto vigente en identity
	// (GetUserSystems falló). Sin esa lectura, un ReplaceUserSystems declarativo REEMPLAZARÍA
	// -- no sumaría -- el conjunto real y borraría accesos que nadie pidió tocar. Se prefiere
	// fallar alto: lo local (tenant + rol) queda escrito igual, los systems de identity NO se
	// tocan. Envuelve también el error de la lectura.
	ErrSystemsUnionUnavailable = errors.New("platformadmin: no se puede unir con los systems actuales del usuario en identity (sin lectura)")

	// ErrIdentityM2MUnavailable se devuelve cuando la aprobación traía systems que conceder pero
	// NO hay cliente M2M configurado hacia identity. Es DISTINTO de no traer systems (un caso
	// legítimo, sin error): aquí había algo que conceder y no hay con qué (Tanda 6 · 1.1). Lo
	// local (tenant + rol) queda escrito igual.
	ErrIdentityM2MUnavailable = errors.New("platformadmin: no hay cliente M2M configurado hacia identity; no se pudieron conceder los systems solicitados")

	// ErrRetryRoleMismatch se devuelve cuando un reintento sobre una solicitud YA 'approved' pide
	// un ROL distinto del que quedó escrito en la primera pasada (Tanda 6 · 1.2). Converger
	// significa reproducir el MISMO estado, no aplicar en silencio lo que pida el segundo clic: sin
	// esta comprobación, un rol distinto -- incluso uno que no existe -- convergía con 204 sin
	// cambiar el rol y disparaba ReplaceUserSystems con los systems de ESA llamada.
	ErrRetryRoleMismatch = errors.New("platformadmin: el reintento pide un rol distinto del ya aprobado la primera vez; no converge")

	// ErrTenantNotFound se devuelve cuando el tenant_id de la aprobación es sintácticamente
	// válido pero no existe (Tanda 6 · P3): se comprueba ANTES de la escritura local para dar un
	// 404 legible en vez del 500 de una FK violada.
	ErrTenantNotFound = errors.New("platformadmin: el tenant_id de la aprobación no existe")

	// ErrSystemsSyncFailed envuelve CUALQUIER fallo de ReplaceUserSystems tras una escritura local
	// exitosa: identity caído, rate-limit, credencial de máquina inválida… Todos comparten el
	// mismo problema de fondo (C-04): lo local ya quedó escrito y hace falta poder reintentar sin
	// duplicar filas. Envuelve también el error de identity.
	ErrSystemsSyncFailed = errors.New("platformadmin: fallo al sincronizar systems en identity tras aprobar localmente")
)

// AccessRequestItem es una solicitud en la bandeja de acceso. Los tags JSON son contrato con la
// consola de plataforma.
type AccessRequestItem struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	Email     string    `json:"email"`
	Origin    string    `json:"origin"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	// Systems son los systems que el usuario YA tiene hoy (C-05). Nunca null: arreglo vacío tanto
	// si de verdad no tiene ninguno como si no se pudo averiguar -- el segundo caso lo distingue
	// SystemsKnown.
	Systems []string `json:"systems"`
	// SystemsKnown es false mientras la bandeja no lea los systems actuales de cada usuario: la
	// consola NO debe leer Systems==[] como "no tiene nada" cuando esto es false (D-056.7).
	SystemsKnown bool `json:"systems_known"`
}

// ApprovePartialResult es el cuerpo JSON de una aprobación cuya mitad LOCAL (tenant + rol) quedó
// escrita pero la sincronización de systems en identity no se hizo -- porque falló
// (identity="failed") o porque se saltó a propósito (identity="skipped"). Existe para que la
// consola pueda decírselo al operador en vez de un texto plano indistinguible (C-04).
type ApprovePartialResult struct {
	Local    string `json:"local"`
	Identity string `json:"identity"`
	Reason   string `json:"reason"`
}

// ListAccessRequestsResponse es el cuerpo JSON de GET /admin/access-requests. Items nunca es
// null.
type ListAccessRequestsResponse struct {
	Items []AccessRequestItem `json:"items"`
}

// ApproveAccessRequestRequest es el cuerpo JSON de POST /admin/access-requests/{id}/approve.
// Role es el NOMBRE o el id del rol.
type ApproveAccessRequestRequest struct {
	TenantID string   `json:"tenant_id"`
	Role     string   `json:"role"`
	Systems  []string `json:"systems"`
}

// RejectAccessRequestRequest es el cuerpo JSON de POST /admin/access-requests/{id}/reject.
type RejectAccessRequestRequest struct {
	Reason string `json:"reason"`
}
