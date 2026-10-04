// Porta internal/platformadmin/handlers.go @ 9a77307: los handlers de la bandeja de empresas. Reciben
// el puerto TenantStore, no el *Repository (D-F2-3).

package platformadmin

import (
	"context"
	"net/http"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// ListTenantsResponse es el cuerpo JSON de GET /admin/tenants: la página (nunca null) y el limit y
// el offset EFECTIVOS, ya acotados como los acota TenantStore.ListTenants.
type ListTenantsResponse struct {
	Items  []TenantListItem `json:"items"`
	Limit  int              `json:"limit"`
	Offset int              `json:"offset"`
}

// ListInstallationsResponse es el cuerpo JSON de GET /admin/tenants/{id}/installations.
type ListInstallationsResponse struct {
	Items []InstallationItem `json:"items"`
}

// CodeIssuer persiste un código de enrolamiento de una empresa con su vencimiento.
type CodeIssuer interface {
	Create(ctx context.Context, code, tenantID string, expiresAt time.Time) error
}

// IssueEnrollmentCodeRequest es el cuerpo OPCIONAL de POST /admin/tenants/{id}/enrollment-codes:
// el TTL del código en segundos.
type IssueEnrollmentCodeRequest struct {
	TTLSeconds int `json:"ttl,omitempty"`
}

// IssueEnrollmentCodeResponse es la respuesta de la emisión de un código de enrolamiento.
type IssueEnrollmentCodeResponse struct {
	Code      string    `json:"code"`
	ExpiresAt time.Time `json:"expires_at"`
}

// CreateTenantRequest es el cuerpo JSON de POST /admin/tenants.
type CreateTenantRequest struct {
	Slug        string  `json:"slug"`
	DisplayName string  `json:"display_name"`
	PlanID      *string `json:"plan_id,omitempty"`
}

// Todos los handlers de este fichero cortan con httpapi.EnforcePlatformCaller ANTES de tocar el
// almacén (R-A1): 401 sin identidad o sin tenant, 403 si el tenant no es platformTenantID. Los que
// llevan {id} lo validan antes de consultar (R-A2): vacío ⇒ 400 «id de empresa requerido»; que no
// es UUID ⇒ 404 «empresa no encontrada». Sus respuestas JSON van con Content-Type
// application/json; sus errores, con http.Error y el texto literal.

// ListTenantsHandler devuelve el handler de GET /admin/tenants. Lee ?limit= y ?offset= (un valor
// que no es un entero se ignora: 50 y 0), pide la página a ListTenants tal cual y responde 200
// ListTenantsResponse con el limit y el offset efectivos (≤ 0 ⇒ 50, > 500 ⇒ 500; < 0 ⇒ 0). Un
// fallo del almacén ⇒ 500 «error al listar empresas».
func ListTenantsHandler(tenants TenantStore, platformTenantID string) http.Handler {
	panic(pendiente.Implementar("platformadmin.ListTenantsHandler"))
}

// GetTenantHandler devuelve el handler de GET /admin/tenants/{id}: publica {id} como tenant
// objetivo de la auditoría y responde 200 con el TenantDetail. ErrNotFound ⇒ 404 «empresa no
// encontrada»; otro fallo ⇒ 500 «error al leer empresa».
func GetTenantHandler(tenants TenantStore, platformTenantID string) http.Handler {
	panic(pendiente.Implementar("platformadmin.GetTenantHandler"))
}

// ListInstallationsHandler devuelve el handler de GET /admin/tenants/{id}/installations: publica
// {id} como tenant objetivo, comprueba que la empresa existe con la consulta LIGERA (ExistsTenant,
// no GetTenant: un fallo ⇒ 500 «error al verificar empresa»; no existe ⇒ 404 «empresa no
// encontrada») y responde 200 ListInstallationsResponse. Un fallo al listar ⇒ 500 «error al
// listar instalaciones».
func ListInstallationsHandler(tenants TenantStore, platformTenantID string) http.Handler {
	panic(pendiente.Implementar("platformadmin.ListInstallationsHandler"))
}

// CreateTenantHandler devuelve el handler de POST /admin/tenants. Cuerpo que no es JSON ⇒ 400
// «cuerpo JSON inválido»; slug o display_name vacíos ⇒ 400 «slug y display_name son requeridos»,
// sin tocar el almacén. Crea con CreateTenant: ErrConflict ⇒ 409 «el slug ya existe»;
// ErrInvalidInput ⇒ 400 «entrada inválida»; otro ⇒ 500 «error al crear empresa». Con éxito publica
// el id NUEVO como tenant objetivo y responde 201 CreatedTenant.
func CreateTenantHandler(tenants TenantStore, platformTenantID string) http.Handler {
	panic(pendiente.Implementar("platformadmin.CreateTenantHandler"))
}

// IssueEnrollmentCodeHandler devuelve el handler de POST /admin/tenants/{id}/enrollment-codes:
// publica {id} como tenant objetivo y comprueba la empresa como ListInstallationsHandler (500
// «error al verificar empresa» / 404 «empresa no encontrada»).
//
// El TTL (R-A4): 86400 s por defecto; si el cuerpo trae contenido y es un
// IssueEnrollmentCodeRequest con ttl > 0, ese (un cuerpo que no es JSON o un ttl ≤ 0 se ignoran);
// y siempre acotado a [60 s, 30 días]. El código es "WAPP-" seguido de 10 bytes aleatorios en
// hexadecimal (20 caracteres en minúsculas); expires_at = ahora (UTC) + TTL. Se persiste con
// issuer.Create(código, {id}, expires_at) —un fallo ⇒ 500 «error al persistir código de
// enrolamiento»; uno al generar el código ⇒ 500 «error al generar código»— y se responde 201
// IssueEnrollmentCodeResponse con el MISMO código y el mismo vencimiento.
func IssueEnrollmentCodeHandler(tenants TenantStore, issuer CodeIssuer, platformTenantID string) http.Handler {
	panic(pendiente.Implementar("platformadmin.IssueEnrollmentCodeHandler"))
}
