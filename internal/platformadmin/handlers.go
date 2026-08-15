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

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
	"github.com/google/uuid"
)

// ListTenantsResponse es el cuerpo JSON de la lista de empresas.
type ListTenantsResponse struct {
	Items  []TenantListItem `json:"items"`
	Limit  int              `json:"limit"`
	Offset int              `json:"offset"`
}

// ListInstallationsResponse es el cuerpo JSON de las instalaciones de una empresa.
type ListInstallationsResponse struct {
	Items []InstallationItem `json:"items"`
}

// CodeIssuer emite códigos de enrolamiento para un tenant.
type CodeIssuer interface {
	Create(ctx context.Context, code, tenantID string, expiresAt time.Time) error
}

// IssueEnrollmentCodeRequest es el cuerpo opcional para POST /admin/tenants/{id}/enrollment-codes.
type IssueEnrollmentCodeRequest struct {
	TTLSeconds int `json:"ttl,omitempty"`
}

// IssueEnrollmentCodeResponse es la respuesta JSON de emisión de código de enrolamiento.
type IssueEnrollmentCodeResponse struct {
	Code      string    `json:"code"`
	ExpiresAt time.Time `json:"expires_at"`
}

// ListTenantsHandler devuelve el handler para GET /admin/tenants.
func ListTenantsHandler(repo *Repository, platformTenantID string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !httpapi.EnforcePlatformCaller(w, r, platformTenantID) {
			return
		}

		limit := 50
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

		items, err := repo.ListTenants(r.Context(), limit, offset)
		if err != nil {
			http.Error(w, "error al listar empresas", http.StatusInternalServerError)
			return
		}

		actualLimit := limit
		if actualLimit <= 0 {
			actualLimit = 50
		} else if actualLimit > maxLimit {
			actualLimit = maxLimit
		}
		actualOffset := offset
		if actualOffset < 0 {
			actualOffset = 0
		}

		writeJSON(w, http.StatusOK, ListTenantsResponse{
			Items:  items,
			Limit:  actualLimit,
			Offset: actualOffset,
		})
	})
}

// tenantIDFromPath extrae y valida el {id} de empresa del path. Un id vacío
// es 400 (falta el parámetro); un id que no es UUID es 404 (design.md:585-587):
// así la consulta nunca llega a la BD con un valor que la columna UUID no
// puede codificar, que es justo lo que hacía salir un 500 en vez del 404 que
// pide el contrato.
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

// GetTenantHandler devuelve el handler para GET /admin/tenants/{id}.
func GetTenantHandler(repo *Repository, platformTenantID string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !httpapi.EnforcePlatformCaller(w, r, platformTenantID) {
			return
		}

		id, ok := tenantIDFromPath(w, r)
		if !ok {
			return
		}
		httpapi.SetAuditTargetTenant(r.Context(), id)

		detail, err := repo.GetTenant(r.Context(), id)
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

// ListInstallationsHandler devuelve el handler para GET /admin/tenants/{id}/installations.
func ListInstallationsHandler(repo *Repository, platformTenantID string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !httpapi.EnforcePlatformCaller(w, r, platformTenantID) {
			return
		}

		tenantID, ok := tenantIDFromPath(w, r)
		if !ok {
			return
		}
		httpapi.SetAuditTargetTenant(r.Context(), tenantID)

		// Validar que el tenant existe antes de listar instalaciones (una sola
		// consulta ligera: no hace falta el detalle completo -fila + conteo +
		// features- que trae GetTenant solo para decidir un 404).
		exists, err := repo.ExistsTenant(r.Context(), tenantID)
		if err != nil {
			http.Error(w, "error al verificar empresa", http.StatusInternalServerError)
			return
		}
		if !exists {
			http.Error(w, "empresa no encontrada", http.StatusNotFound)
			return
		}

		items, err := repo.ListInstallations(r.Context(), tenantID)
		if err != nil {
			http.Error(w, "error al listar instalaciones", http.StatusInternalServerError)
			return
		}

		writeJSON(w, http.StatusOK, ListInstallationsResponse{
			Items: items,
		})
	})
}

// CreateTenantRequest es el cuerpo JSON para POST /admin/tenants.
type CreateTenantRequest struct {
	Slug        string  `json:"slug"`
	DisplayName string  `json:"display_name"`
	PlanID      *string `json:"plan_id,omitempty"`
}

// CreateTenantHandler devuelve el handler para POST /admin/tenants.
func CreateTenantHandler(repo *Repository, platformTenantID string) http.Handler {
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

		created, err := repo.CreateTenant(r.Context(), req.Slug, req.DisplayName, req.PlanID)
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

func generateEnrollmentCode() (string, error) {
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generando código: %w", err)
	}
	return "WAPP-" + hex.EncodeToString(b[:]), nil
}

// IssueEnrollmentCodeHandler devuelve el handler para POST /admin/tenants/{id}/enrollment-codes.
func IssueEnrollmentCodeHandler(repo *Repository, issuer CodeIssuer, platformTenantID string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !httpapi.EnforcePlatformCaller(w, r, platformTenantID) {
			return
		}

		tenantID, ok := tenantIDFromPath(w, r)
		if !ok {
			return
		}
		httpapi.SetAuditTargetTenant(r.Context(), tenantID)

		exists, err := repo.ExistsTenant(r.Context(), tenantID)
		if err != nil {
			http.Error(w, "error al verificar empresa", http.StatusInternalServerError)
			return
		}
		if !exists {
			http.Error(w, "empresa no encontrada", http.StatusNotFound)
			return
		}

		ttl := 86400 // 24 horas por defecto
		if r.Body != nil && r.ContentLength > 0 {
			var req IssueEnrollmentCodeRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err == nil && req.TTLSeconds > 0 {
				ttl = req.TTLSeconds
			}
		}

		if ttl < 60 {
			ttl = 60
		} else if ttl > 30*86400 {
			ttl = 30 * 86400
		}

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
