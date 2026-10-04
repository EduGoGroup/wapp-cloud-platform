// Porta internal/platformadmin/access_requests.go @ 9a77307: las REGLAS y los handlers de la
// bandeja de solicitudes de acceso. Su SQL (líneas 135-336 y 481-526 del viejo) vive en
// access_requests_postgres.go (D-F2-3), detrás del puerto AccessRequestStore de ports.go.

package platformadmin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// systemWappPlatform es el namespace de la consola de plataforma (== usecase.SystemWappPlatform
// de iam/usecase/exchange.go). Se re-declara aquí en vez de importar el paquete usecase --que
// arrastra el canje de tokens completo-- porque lo único que hace falta es el valor de catálogo,
// no el comportamiento (trampa T-12 de F2).
const systemWappPlatform = "wapp.platform"

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
//
// El reintento de una aprobación que falló al sincronizar systems CONVERGE (C-04): si la
// solicitud ya está 'approved' hacia la MISMA empresa Y con el MISMO rol, se salta por completo la
// escritura local --ya está hecha-- y va directo a reintentar solo la mitad que pudo haber
// fallado. Un reintento que pide un rol distinto NO converge: se rechaza en vez de fingir que se
// aplicó.
func ApproveAccessRequest(ctx context.Context, tenants TenantStore, requests AccessRequestStore, requestID, tenantID, role, operatorID string, systems []string, m2m out.IdentityM2MClient) error {
	if requestID == "" || tenantID == "" || role == "" {
		return ErrInvalidInput
	}
	// (C) wapp.platform NO se concede desde la bandeja -- segundo cerrojo, el servidor no se fía
	// de que la consola haya quitado la casilla.
	if slices.Contains(systems, systemWappPlatform) {
		return ErrPlatformSystemForbidden
	}

	userID, status, err := requests.LookupAccessRequestStatus(ctx, requestID)
	if err != nil {
		return err
	}

	// El rol se resuelve UNA vez, ANTES de bifurcar por status: tanto el camino 'pending'
	// (ExecuteApprovalTx lo necesita para el INSERT) como el camino 'approved' (CheckRetryApproved
	// lo necesita para comparar contra lo ya escrito) lo usan -- resolverlo aquí evita que el
	// reintento se salte la validación por saltarse ExecuteApprovalTx entero (1.2).
	roleID, err := requests.ResolveRoleID(ctx, role)
	if err != nil {
		return err
	}

	if err := resolveApprovalWrite(ctx, tenants, requests, status, requestID, tenantID, userID, roleID, operatorID); err != nil {
		return err
	}

	return syncApprovedSystems(ctx, userID, systems, m2m)
}

// resolveApprovalWrite ejecuta -- o converge sobre -- la escritura LOCAL (tenant + rol) de la
// aprobación, según el status con el que llegó la solicitud. Mismo cuerpo y mismo orden que el
// viejo Repository.resolveApprovalWrite, sobre los puertos.
func resolveApprovalWrite(ctx context.Context, tenants TenantStore, requests AccessRequestStore, status, requestID, tenantID, userID, roleID, operatorID string) error {
	switch status {
	case "pending":
		// (P3) Un tenant_id sintácticamente válido pero inexistente violaba la FK
		// tenant_members.tenant_id -> tenants(id) DENTRO de la tx y salía como 500 genérico.
		// Comprobarlo aquí, antes de la escritura, lo convierte en un ErrTenantNotFound legible --
		// no hace falta en el camino 'approved': un 'approved' de verdad ya exige que exista una
		// fila en tenant_members para ese tenant_id (esa misma FK, satisfecha la primera vez).
		exists, err := tenants.ExistsTenant(ctx, tenantID)
		if err != nil {
			return fmt.Errorf("platformadmin: comprobar existencia de tenant: %w", err)
		}
		if !exists {
			return ErrTenantNotFound
		}
		return requests.ExecuteApprovalTx(ctx, requestID, tenantID, userID, roleID, operatorID)
	case "approved":
		// Lo local (tenant + rol) ya está escrito de una pasada anterior: solo se acepta si
		// coincide con lo que se pide ahora, y entonces NO se vuelve a escribir; solo se
		// reintenta systems.
		return requests.CheckRetryApproved(ctx, userID, tenantID, roleID)
	default:
		return ErrConflict
	}
}

// syncApprovedSystems sincroniza en identity los systems pedidos, DESPUÉS de que la escritura
// local ya quedó resuelta. Mismo cuerpo y mismo orden que el viejo
// Repository.syncApprovedSystems, incluidos los desenlaces de (1.1) y (D).
func syncApprovedSystems(ctx context.Context, userID string, systems []string, m2m out.IdentityM2MClient) error {
	// (1.1) len(systems)==0 es un caso LEGÍTIMO -- no había nada que conceder -- y devuelve nil.
	// m2m==nil es DISTINTO: SÍ había algo que conceder y no hay con qué. Antes ambos compartían
	// la misma salida silenciosa.
	if len(systems) == 0 {
		return nil
	}
	if m2m == nil {
		return ErrIdentityM2MUnavailable
	}

	// (D) Unión, no reemplazo: ReplaceUserSystems es declarativo, así que mandarle solo los
	// systems de ESTA solicitud reemplazaría -- no sumaría -- el conjunto real de la persona. Se
	// une de verdad, igual que la vía de la dueña (iam/usecase/memberships.go): leer, unir,
	// declarar. (Hasta el 2026-08-28 se APROXIMABA la unión con una señal local, un proxy
	// estructuralmente equivocado; su excusa caducó con GetUserSystems.)
	//
	// current era `vigentes` en el viejo.
	current, err := m2m.GetUserSystems(ctx, userID)
	if err != nil {
		// Sin poder LEER no se puede unir, y un ReplaceUserSystems a ciegas borraría accesos que
		// no son de esta bandeja. Se rehúsa por un fallo MEDIDO.
		return fmt.Errorf("%w: %w", ErrSystemsUnionUnavailable, err)
	}

	// slices.Clone: no se escribe en el arreglo que devolvió el puerto, que no es nuestro. El
	// orden es el que dio identity con los nuevos al final: estable y reproducible.
	//
	// desired era `deseados` en el viejo.
	desired := slices.Clone(current)
	for _, s := range systems {
		if !slices.Contains(desired, s) {
			desired = append(desired, s)
		}
	}
	// Si no hay nada que añadir, no se escribe: identity no tiene por qué recibir un PUT que no
	// cambia nada (mismo criterio que T-B4).
	if len(desired) == len(current) {
		return nil
	}

	// ⚠️ La unión PRESERVA lo que la persona ya tuviera, incluido wapp.platform. Eso NO contradice
	// ErrPlatformSystemForbidden: esa guarda prohíbe CONCEDERLO desde esta bandeja, y aquí no se
	// concede nada nuevo -- se evita borrar lo que otra vía otorgó.
	if _, err := m2m.ReplaceUserSystems(ctx, userID, desired); err != nil {
		return fmt.Errorf("%w: %w", ErrSystemsSyncFailed, err)
	}

	return nil
}

// accessRequestIDFromPath extrae y valida el {id} de solicitud del path, mismo criterio que
// tenantIDFromPath (handlers.go) para M-03 (Tanda 6 · P3): un id vacío es 400 (falta el
// parámetro); un id que no es UUID es 404, no 500 -- sin esto, un `WHERE id = $1` sobre una
// columna UUID con un valor que no codifica revienta con un error que no es ErrNotFound y acababa
// en el 500 genérico.
func accessRequestIDFromPath(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "id de solicitud requerido", http.StatusBadRequest)
		return "", false
	}
	if _, err := uuid.Parse(id); err != nil {
		http.Error(w, "solicitud no encontrada", http.StatusNotFound)
		return "", false
	}
	return id, true
}

// operatorFrom devuelve el Subject de la identidad de la petición ("" si no hay): el operador
// que resuelve una solicitud.
func operatorFrom(r *http.Request) string {
	if id, ok := httpapi.IdentityFromContext(r.Context()); ok {
		return id.Subject
	}
	return ""
}

// ListAccessRequestsHandler devuelve el handler de GET /admin/access-requests: corta con
// httpapi.EnforcePlatformCaller ANTES de tocar el almacén (401 sin identidad o sin tenant; 403 si
// el tenant no es el de plataforma). Lista las solicitudes con el status de ?status= ("" ⇒
// pending) como 200 {"items":[…]} (nunca null). Un fallo del almacén ⇒ 500 «error al listar
// solicitudes de acceso».
func ListAccessRequestsHandler(requests AccessRequestStore, platformTenantID string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !httpapi.EnforcePlatformCaller(w, r, platformTenantID) {
			return
		}

		status := r.URL.Query().Get("status")
		if status == "" {
			status = "pending"
		}

		items, err := requests.ListAccessRequests(r.Context(), status)
		if err != nil {
			http.Error(w, "error al listar solicitudes de acceso", http.StatusInternalServerError)
			return
		}

		writeJSON(w, http.StatusOK, ListAccessRequestsResponse{
			Items: items,
		})
	})
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
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !httpapi.EnforcePlatformCaller(w, r, platformTenantID) {
			return
		}

		requestID, ok := accessRequestIDFromPath(w, r)
		if !ok {
			return
		}

		req, ok := decodeApproveAccessRequestBody(w, r)
		if !ok {
			return
		}

		httpapi.SetAuditTargetTenant(r.Context(), req.TenantID)

		err := ApproveAccessRequest(r.Context(), tenants, requests, requestID, req.TenantID, req.Role, operatorFrom(r), req.Systems, m2m)
		writeApproveAccessRequestResult(w, err)
	})
}

// decodeApproveAccessRequestBody decodifica y valida el cuerpo JSON de
// POST /admin/access-requests/{id}/approve. Si el cuerpo es inválido o le faltan tenant_id/role,
// ya escribió la respuesta de error y devuelve ok=false.
func decodeApproveAccessRequestBody(w http.ResponseWriter, r *http.Request) (ApproveAccessRequestRequest, bool) {
	var req ApproveAccessRequestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "cuerpo JSON inválido", http.StatusBadRequest)
		return req, false
	}
	if req.TenantID == "" || req.Role == "" {
		http.Error(w, "tenant_id y role son requeridos", http.StatusBadRequest)
		return req, false
	}
	return req, true
}

// writeApproveAccessRequestResult mapea el resultado de ApproveAccessRequest al status y cuerpo
// HTTP de la respuesta. Mismo switch, mismos casos y mismo orden que el viejo: ninguno de los
// errors.Is coincide cuando err es nil, así que ese caso cae al 204 de abajo.
func writeApproveAccessRequestResult(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		http.Error(w, "solicitud no encontrada", http.StatusNotFound)
		return
	case errors.Is(err, ErrTenantNotFound):
		http.Error(w, "empresa no encontrada", http.StatusNotFound)
		return
	case errors.Is(err, ErrConflict):
		http.Error(w, "la solicitud ya fue resuelta o la persona ya pertenece a otra empresa", http.StatusConflict)
		return
	case errors.Is(err, ErrInvalidInput):
		http.Error(w, "datos de solicitud o rol inválidos", http.StatusBadRequest)
		return
	case errors.Is(err, ErrPlatformSystemForbidden):
		http.Error(w, "wapp.platform no se concede desde la bandeja de solicitudes de acceso", http.StatusBadRequest)
		return
	case errors.Is(err, ErrSystemsUnionUnavailable):
		// (D) Lo local quedó escrito; los systems de identity NO se tocaron porque FALLÓ LA
		// LECTURA de su conjunto actual, y sin leerlo un PUT declarativo borraría lo que otra vía
		// concedió. 409 y no 502 porque hace falta mirar: reintentar a ciegas no lo arregla.
		writeJSON(w, http.StatusConflict, ApprovePartialResult{
			Local: "ok", Identity: "skipped",
			Reason: "no se pudo leer el conjunto actual de systems del usuario en identity; para no reemplazarlo por accidente no se tocó nada en identity",
		})
		return
	case errors.Is(err, ErrSystemsSyncFailed):
		// (C-04) Lo local (tenant + rol) quedó escrito; solo falló la sincronización con
		// identity. 502 distinguible para que la consola pueda decírselo al operador y
		// reintentar más tarde.
		writeJSON(w, http.StatusBadGateway, ApprovePartialResult{
			Local: "ok", Identity: "failed", Reason: err.Error(),
		})
		return
	case errors.Is(err, ErrIdentityM2MUnavailable):
		// (1.1) Mismo cuerpo que los dos anteriores (lo local quedó escrito) pero 503: no es un
		// conflicto de datos ni un fallo transitorio de red, es que este despliegue no tiene
		// cliente M2M configurado (mismo código que SignupHandler para el mismo m2m == nil).
		writeJSON(w, http.StatusServiceUnavailable, ApprovePartialResult{
			Local: "ok", Identity: "skipped",
			Reason: "no hay cliente M2M configurado hacia identity en este despliegue; lo local (empresa y rol) quedó escrito pero los systems solicitados NO se concedieron",
		})
		return
	case errors.Is(err, ErrRetryRoleMismatch):
		// (1.2) El reintento pide un rol distinto del que ya quedó aprobado la primera vez: NO
		// converge, así que no se toca nada. 409: hace falta que el operador reconcilie a mano.
		http.Error(w, "la solicitud ya fue aprobada con un rol distinto; el reintento no converge", http.StatusConflict)
		return
	case err != nil:
		http.Error(w, "error al aprobar solicitud", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
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
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !httpapi.EnforcePlatformCaller(w, r, platformTenantID) {
			return
		}

		requestID, ok := accessRequestIDFromPath(w, r)
		if !ok {
			return
		}

		var req RejectAccessRequestRequest
		if r.Body != nil && r.ContentLength > 0 {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "cuerpo JSON inválido", http.StatusBadRequest)
				return
			}
		}

		err := requests.RejectAccessRequest(r.Context(), requestID, req.Reason, operatorFrom(r))
		switch {
		case errors.Is(err, ErrNotFound):
			http.Error(w, "solicitud no encontrada", http.StatusNotFound)
			return
		case errors.Is(err, ErrConflict):
			http.Error(w, "la solicitud ya fue resuelta", http.StatusConflict)
			return
		case errors.Is(err, ErrInvalidInput):
			http.Error(w, "entrada inválida", http.StatusBadRequest)
			return
		case err != nil:
			http.Error(w, "error al rechazar solicitud", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	})
}
