// Porta internal/platformadmin/access_requests.go @ 9a77307: las REGLAS y los handlers de la
// bandeja de solicitudes de acceso. Su SQL (líneas 135-336 y 481-526 del viejo) vive en
// access_requests_postgres.go (D-F2-3), detrás del puerto AccessRequestStore de ports.go.

package platformadmin

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
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

// ApproveAccessRequest aprueba la solicitud requestID: da a su persona acceso a la empresa
// tenantID con el rol role (nombre o id) y le concede en identity los systems pedidos, UNIDOS a
// los que ya tenga. Es la orquestación que el viejo hacía en Repository.ApproveAccessRequest, ahora
// sobre los puertos: la escritura local la hace requests.ExecuteApprovalTx, atómica.
//
// En este orden, y cortando en el primer error:
//  1. requestID, tenantID o role vacíos ⇒ ErrInvalidInput, sin tocar los almacenes.
//  2. systems contiene "wapp.platform" ⇒ ErrPlatformSystemForbidden, sin tocar nada: la consola
//     de plataforma NO se concede desde la bandeja (segundo cerrojo; el servidor no se fía de que
//     la consola haya quitado la casilla).
//  3. La solicitud: ErrNotFound si no existe (LookupAccessRequestStatus).
//  4. El rol se resuelve UNA vez, ANTES de bifurcar por status (ResolveRoleID; ErrInvalidInput si
//     no existe): así el reintento no se salta la validación del rol.
//  5. La escritura local, según el status:
//     - 'pending': la empresa tiene que existir (ExistsTenant; ErrTenantNotFound si no, y un fallo
//     al comprobarlo se devuelve envuelto) y se escribe con ExecuteApprovalTx (ErrConflict si
//     la persona ya es de otra empresa o la solicitud dejó de estar pendiente);
//     - 'approved': un REINTENTO, que converge sin volver a escribir si CheckRetryApproved
//     acepta (misma empresa y mismo rol); si no, ErrConflict (otra empresa) o
//     ErrRetryRoleMismatch (otro rol), y no se toca nada más (ni identity);
//     - cualquier otro ('rejected') ⇒ ErrConflict.
//  6. Los systems, DESPUÉS de lo local (lo local queda escrito pase lo que pase aquí):
//     - sin systems ⇒ nil, aunque m2m sea nil (no había nada que conceder);
//     - m2m nil ⇒ ErrIdentityM2MUnavailable;
//     - se LEEN los vigentes (GetUserSystems); si la lectura falla ⇒ ErrSystemsUnionUnavailable
//     envolviendo ese error, y NO se declara nada (un PUT declarativo a ciegas borraría lo que
//     concedió otra vía);
//     - se declara la UNIÓN (ReplaceUserSystems): los vigentes en el orden de identity y, al
//     final, los pedidos que falten, sin repetir. La unión conserva lo que ya tuviera,
//     wapp.platform incluido (no se concede: se evita borrarlo). El arreglo que devolvió
//     identity no se modifica;
//     - si la unión no añade nada, NO se escribe (nil);
//     - si la escritura falla ⇒ ErrSystemsSyncFailed envolviendo ese error.
//
// operatorID es el sujeto de quien aprueba; se guarda como decided_by solo si es un UUID.
func ApproveAccessRequest(ctx context.Context, tenants TenantStore, requests AccessRequestStore, requestID, tenantID, role, operatorID string, systems []string, m2m out.IdentityM2MClient) error {
	panic(pendiente.Implementar("platformadmin.ApproveAccessRequest"))
}

// ListAccessRequestsHandler devuelve el handler de GET /admin/access-requests: corta con
// httpapi.EnforcePlatformCaller ANTES de tocar el almacén (401 sin identidad o sin tenant; 403 si
// el tenant no es el de plataforma). Lista las solicitudes con el status de ?status= ("" ⇒
// pending) como 200 {"items":[…]} (nunca null). Un fallo del almacén ⇒ 500 «error al listar
// solicitudes de acceso».
func ListAccessRequestsHandler(requests AccessRequestStore, platformTenantID string) http.Handler {
	panic(pendiente.Implementar("platformadmin.ListAccessRequestsHandler"))
}

// ApproveAccessRequestHandler devuelve el handler de POST /admin/access-requests/{id}/approve.
// Corta con httpapi.EnforcePlatformCaller ANTES de tocar nada. Después:
//   - {id} vacío ⇒ 400 «id de solicitud requerido»; {id} que no es UUID ⇒ 404 «solicitud no
//     encontrada», sin consultar (R-A2);
//   - cuerpo que no es JSON ⇒ 400 «cuerpo JSON inválido»; sin tenant_id o sin role ⇒ 400
//     «tenant_id y role son requeridos»;
//   - publica tenant_id como tenant objetivo de la auditoría y aprueba con ApproveAccessRequest,
//     con el Subject de la identidad como operador.
//
// Desenlaces: nil ⇒ 204; ErrNotFound ⇒ 404 «solicitud no encontrada»; ErrTenantNotFound ⇒ 404
// «empresa no encontrada»; ErrConflict ⇒ 409 «la solicitud ya fue resuelta o la persona ya
// pertenece a otra empresa»; ErrInvalidInput ⇒ 400 «datos de solicitud o rol inválidos»;
// ErrPlatformSystemForbidden ⇒ 400 «wapp.platform no se concede desde la bandeja de solicitudes
// de acceso»; ErrRetryRoleMismatch ⇒ 409 «la solicitud ya fue aprobada con un rol distinto; el
// reintento no converge»; y, con lo local escrito, un JSON ApprovePartialResult con local "ok":
// ErrSystemsUnionUnavailable ⇒ 409 identity "skipped" (hace falta mirar; reintentar a ciegas no
// lo arregla), ErrSystemsSyncFailed ⇒ 502 identity "failed" con el texto del error como motivo,
// ErrIdentityM2MUnavailable ⇒ 503 identity "skipped". Cualquier otro error ⇒ 500 «error al
// aprobar solicitud».
func ApproveAccessRequestHandler(tenants TenantStore, requests AccessRequestStore, m2m out.IdentityM2MClient, platformTenantID string) http.Handler {
	panic(pendiente.Implementar("platformadmin.ApproveAccessRequestHandler"))
}

// RejectAccessRequestHandler devuelve el handler de POST /admin/access-requests/{id}/reject.
// Corta con httpapi.EnforcePlatformCaller ANTES de tocar nada; {id} como en el de aprobar
// (400 vacío, 404 no UUID). El cuerpo {"reason":…} solo se lee si trae contenido: uno que no es
// JSON ⇒ 400 «cuerpo JSON inválido»; sin cuerpo, el motivo es "". Rechaza con
// RejectAccessRequest y el Subject de la identidad como operador. Desenlaces: nil ⇒ 204;
// ErrNotFound ⇒ 404 «solicitud no encontrada»; ErrConflict ⇒ 409 «la solicitud ya fue resuelta»;
// ErrInvalidInput (motivo en blanco) ⇒ 400 «entrada inválida»; otro ⇒ 500 «error al rechazar
// solicitud».
func RejectAccessRequestHandler(requests AccessRequestStore, platformTenantID string) http.Handler {
	panic(pendiente.Implementar("platformadmin.RejectAccessRequestHandler"))
}
