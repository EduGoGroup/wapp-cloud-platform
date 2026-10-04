// Porta internal/platformadmin/handlers.go @ 9a77307: los handlers de la bandeja de empresas. Reciben
// el puerto TenantStore, no el *Repository (D-F2-3).

package platformadmin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
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
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !httpapi.EnforcePlatformCaller(w, r, platformTenantID) {
			return
		}

		limit := defaultLimit
		offset := 0
		q := r.URL.Query()
		if lStr := q.Get("limit"); lStr != "" {
			if l, err := strconv.Atoi(lStr); err == nil {
				limit = l
			}
		}
		if oStr := q.Get("offset"); oStr != "" {
			if o, err := strconv.Atoi(oStr); err == nil {
				offset = o
			}
		}

		items, err := tenants.ListTenants(r.Context(), limit, offset)
		if err != nil {
			http.Error(w, "error al listar empresas", http.StatusInternalServerError)
			return
		}

		// La respuesta dice la página EFECTIVA, con la misma regla que aplica el almacén
		// (clampPage: en el viejo, una segunda copia aquí).
		actualLimit, actualOffset := clampPage(limit, offset)
		writeJSON(w, http.StatusOK, ListTenantsResponse{
			Items:  items,
			Limit:  actualLimit,
			Offset: actualOffset,
		})
	})
}

// tenantIDFromPath extrae y valida el {id} de empresa del path. Un id vacío es 400 (falta el
// parámetro); un id que no es UUID es 404 (design.md:585-587): así la consulta nunca llega a la BD
// con un valor que la columna UUID no puede codificar, que es justo lo que hacía salir un 500 en
// vez del 404 que pide el contrato.
func tenantIDFromPath(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "id de empresa requerido", http.StatusBadRequest)
		return "", false
	}
	if _, err := uuid.Parse(id); err != nil {
		http.Error(w, "empresa no encontrada", http.StatusNotFound)
		return "", false
	}
	return id, true
}

// GetTenantHandler devuelve el handler de GET /admin/tenants/{id}: publica {id} como tenant
// objetivo de la auditoría y responde 200 con el TenantDetail. ErrNotFound ⇒ 404 «empresa no
// encontrada»; otro fallo ⇒ 500 «error al leer empresa».
func GetTenantHandler(tenants TenantStore, platformTenantID string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !httpapi.EnforcePlatformCaller(w, r, platformTenantID) {
			return
		}

		id, ok := tenantIDFromPath(w, r)
		if !ok {
			return
		}
		httpapi.SetAuditTargetTenant(r.Context(), id)

		detail, err := tenants.GetTenant(r.Context(), id)
		switch {
		case errors.Is(err, ErrNotFound):
			http.Error(w, "empresa no encontrada", http.StatusNotFound)
			return
		case err != nil:
			http.Error(w, "error al leer empresa", http.StatusInternalServerError)
			return
		}

		writeJSON(w, http.StatusOK, detail)
	})
}

// ListInstallationsHandler devuelve el handler de GET /admin/tenants/{id}/installations: publica
// {id} como tenant objetivo, comprueba que la empresa existe con la consulta LIGERA (ExistsTenant,
// no GetTenant: un fallo ⇒ 500 «error al verificar empresa»; no existe ⇒ 404 «empresa no
// encontrada») y responde 200 ListInstallationsResponse. Un fallo al listar ⇒ 500 «error al
// listar instalaciones».
func ListInstallationsHandler(tenants TenantStore, platformTenantID string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !httpapi.EnforcePlatformCaller(w, r, platformTenantID) {
			return
		}

		tenantID, ok := tenantIDFromPath(w, r)
		if !ok {
			return
		}
		httpapi.SetAuditTargetTenant(r.Context(), tenantID)

		// Validar que el tenant existe antes de listar instalaciones (una sola consulta ligera:
		// no hace falta el detalle completo -fila + conteo + features- que trae GetTenant solo
		// para decidir un 404).
		if !requireTenant(w, r, tenants, tenantID) {
			return
		}

		items, err := tenants.ListInstallations(r.Context(), tenantID)
		if err != nil {
			http.Error(w, "error al listar instalaciones", http.StatusInternalServerError)
			return
		}

		writeJSON(w, http.StatusOK, ListInstallationsResponse{
			Items: items,
		})
	})
}

// requireTenant comprueba con ExistsTenant que la empresa existe; si no puede comprobarlo (500) o
// no existe (404) ya escribió la respuesta y devuelve false. Era el mismo bloque copiado en
// ListInstallationsHandler e IssueEnrollmentCodeHandler.
func requireTenant(w http.ResponseWriter, r *http.Request, tenants TenantStore, tenantID string) bool {
	exists, err := tenants.ExistsTenant(r.Context(), tenantID)
	if err != nil {
		http.Error(w, "error al verificar empresa", http.StatusInternalServerError)
		return false
	}
	if !exists {
		http.Error(w, "empresa no encontrada", http.StatusNotFound)
		return false
	}
	return true
}

// CreateTenantHandler devuelve el handler de POST /admin/tenants. Cuerpo que no es JSON ⇒ 400
// «cuerpo JSON inválido»; slug o display_name vacíos ⇒ 400 «slug y display_name son requeridos»,
// sin tocar el almacén. Crea con CreateTenant: ErrConflict ⇒ 409 «el slug ya existe»;
// ErrInvalidInput ⇒ 400 «entrada inválida»; otro ⇒ 500 «error al crear empresa». Con éxito publica
// el id NUEVO como tenant objetivo y responde 201 CreatedTenant.
func CreateTenantHandler(tenants TenantStore, platformTenantID string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !httpapi.EnforcePlatformCaller(w, r, platformTenantID) {
			return
		}

		var req CreateTenantRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "cuerpo JSON inválido", http.StatusBadRequest)
			return
		}

		if req.Slug == "" || req.DisplayName == "" {
			http.Error(w, "slug y display_name son requeridos", http.StatusBadRequest)
			return
		}

		created, err := tenants.CreateTenant(r.Context(), req.Slug, req.DisplayName, req.PlanID)
		switch {
		case errors.Is(err, ErrConflict):
			http.Error(w, "el slug ya existe", http.StatusConflict)
			return
		case errors.Is(err, ErrInvalidInput):
			http.Error(w, "entrada inválida", http.StatusBadRequest)
			return
		case err != nil:
			http.Error(w, "error al crear empresa", http.StatusInternalServerError)
			return
		}

		httpapi.SetAuditTargetTenant(r.Context(), created.ID)
		writeJSON(w, http.StatusCreated, created)
	})
}

// enrollmentCodeBytes es la entropía de un código de enrolamiento: 10 bytes, 20 caracteres hex.
const enrollmentCodeBytes = 10

// Los límites del TTL de un código de enrolamiento, en segundos.
const (
	enrollmentTTLDefault = 86400      // 24 horas
	enrollmentTTLMin     = 60         // un minuto
	enrollmentTTLMax     = 30 * 86400 // treinta días
)

// generateEnrollmentCode devuelve "WAPP-" seguido de enrollmentCodeBytes aleatorios (crypto/rand)
// en hexadecimal en minúsculas.
func generateEnrollmentCode() (string, error) {
	var b [enrollmentCodeBytes]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generando código: %w", err)
	}
	return "WAPP-" + hex.EncodeToString(b[:]), nil
}

// enrollmentTTL decide el TTL de un código a partir de la petición: el por defecto salvo que el
// cuerpo traiga contenido y sea JSON con ttl > 0 (lo demás se ignora), y siempre acotado a
// [enrollmentTTLMin, enrollmentTTLMax].
func enrollmentTTL(r *http.Request) int {
	ttl := enrollmentTTLDefault
	if r.Body != nil && r.ContentLength > 0 {
		var req IssueEnrollmentCodeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil && req.TTLSeconds > 0 {
			ttl = req.TTLSeconds
		}
	}
	return min(max(ttl, enrollmentTTLMin), enrollmentTTLMax)
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
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !httpapi.EnforcePlatformCaller(w, r, platformTenantID) {
			return
		}

		tenantID, ok := tenantIDFromPath(w, r)
		if !ok {
			return
		}
		httpapi.SetAuditTargetTenant(r.Context(), tenantID)

		if !requireTenant(w, r, tenants, tenantID) {
			return
		}

		ttl := enrollmentTTL(r)

		code, err := generateEnrollmentCode()
		if err != nil {
			http.Error(w, "error al generar código", http.StatusInternalServerError)
			return
		}

		expiresAt := time.Now().UTC().Add(time.Duration(ttl) * time.Second)
		if err := issuer.Create(r.Context(), code, tenantID, expiresAt); err != nil {
			http.Error(w, "error al persistir código de enrolamiento", http.StatusInternalServerError)
			return
		}

		writeJSON(w, http.StatusCreated, IssueEnrollmentCodeResponse{
			Code:      code,
			ExpiresAt: expiresAt,
		})
	})
}

// writeJSON escribe data como JSON con ese status y Content-Type application/json. Si no se puede
// codificar, 500 «codificando respuesta» (no pasa con los DTO de este paquete).
func writeJSON(w http.ResponseWriter, status int, data any) {
	body, err := json.Marshal(data)
	if err != nil {
		http.Error(w, "codificando respuesta", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, werr := w.Write(body); werr != nil {
		return
	}
}
