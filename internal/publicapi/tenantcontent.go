package publicapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	flowstore "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// maxTenantContentBytes acota el tamaño del blob JSONB de tenant_content (defensa
// DoS). 1 MiB cubre catálogos/menús grandes sin permitir cargas abusivas.
const maxTenantContentBytes = 1 << 20

// TenantContentStore es el puerto MÍNIMO del CRUD de contenido dinámico por-tenant
// (public.tenant_content) que la API pública consume. Lo satisfacen
// *store.PostgresRepository y *store.MemoryRepository. TODAS las operaciones van
// acotadas al tenant (INV-8): el aislamiento lo garantiza el store (PK/WHERE
// tenant_id), NUNCA el cuerpo del request.
type TenantContentStore interface {
	UpsertTenantContent(ctx context.Context, tenantID, ref string, blob []byte) error
	GetTenantContent(ctx context.Context, tenantID, ref string) ([]byte, error)
	ListTenantContent(ctx context.Context, tenantID string) ([]flowstore.TenantContentSummary, error)
	DeleteTenantContent(ctx context.Context, tenantID, ref string) error
}

// tenantContentSummaryDTO es una fila del listado GET /api/v1/tenant-content: la ref
// lógica y las marcas de tiempo. NO incluye el blob (se obtiene con GET /{ref}).
type tenantContentSummaryDTO struct {
	Ref       string `json:"ref"`
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

const rfc3339 = "2006-01-02T15:04:05Z07:00"

// upsertTenantContentHandler devuelve el handler de PUT/POST
// /api/v1/tenant-content/{ref}: registra (upsert) el blob JSON crudo del cuerpo bajo
// (tenant del token, ref de la ruta). El cuerpo ES el blob que luego lee el adapter
// content.JSON del Motor (source:json,ref) — se valida que sea JSON. AUDITORÍA CERO
// PII: se audita ref/action (content.write), NUNCA el contenido del blob. Respuestas:
//
//   - 200 con {ref} al persistir.
//   - 400 si falta ref, el cuerpo está vacío o NO es JSON válido.
//   - 401 sin identidad; 413 si el blob excede el límite; 500 en fallo del store.
func upsertTenantContentHandler(cs TenantContentStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}
		if cs == nil {
			writeError(w, http.StatusInternalServerError, "store de contenido no configurado")
			return
		}
		ref := r.PathValue("ref")
		if ref == "" {
			writeError(w, http.StatusBadRequest, "ref requerida en la ruta")
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, maxTenantContentBytes+1))
		if err != nil {
			writeError(w, http.StatusBadRequest, "no se pudo leer el cuerpo")
			return
		}
		if len(body) > maxTenantContentBytes {
			writeError(w, http.StatusRequestEntityTooLarge, "el contenido excede el tamaño máximo")
			return
		}
		if len(body) == 0 || !json.Valid(body) {
			writeError(w, http.StatusBadRequest, "el cuerpo debe ser un JSON válido (el blob de contenido)")
			return
		}

		if err := cs.UpsertTenantContent(r.Context(), id.TenantID, ref, body); err != nil {
			writeError(w, http.StatusInternalServerError, "no se pudo registrar el contenido")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"ref": ref})
	})
}

// listTenantContentHandler devuelve el handler de GET /api/v1/tenant-content: lista
// las refs de contenido del tenant del token (INV-8), cada una con sus timestamps.
// 200 con el arreglo (vacío si no hay); 401 sin identidad; 500 en fallo del store.
func listTenantContentHandler(cs TenantContentStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}
		if cs == nil {
			writeError(w, http.StatusInternalServerError, "store de contenido no configurado")
			return
		}
		summaries, err := cs.ListTenantContent(r.Context(), id.TenantID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "no se pudo listar el contenido")
			return
		}
		out := make([]tenantContentSummaryDTO, 0, len(summaries))
		for _, s := range summaries {
			dto := tenantContentSummaryDTO{Ref: s.Ref}
			if !s.CreatedAt.IsZero() {
				dto.CreatedAt = s.CreatedAt.UTC().Format(rfc3339)
			}
			if !s.UpdatedAt.IsZero() {
				dto.UpdatedAt = s.UpdatedAt.UTC().Format(rfc3339)
			}
			out = append(out, dto)
		}
		writeJSON(w, http.StatusOK, out)
	})
}

// getTenantContentHandler devuelve el handler de GET /api/v1/tenant-content/{ref}: el
// blob JSON crudo de contenido para (tenant del token, ref). Responde el blob TAL
// CUAL (application/json). 200 con el blob; 404 si el tenant no tiene esa ref (o es
// de otro tenant: el store filtra por tenant → 404); 401 sin identidad; 500 en otro
// fallo.
func getTenantContentHandler(cs TenantContentStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}
		if cs == nil {
			writeError(w, http.StatusInternalServerError, "store de contenido no configurado")
			return
		}
		ref := r.PathValue("ref")
		if ref == "" {
			writeError(w, http.StatusBadRequest, "ref requerida en la ruta")
			return
		}
		blob, err := cs.GetTenantContent(r.Context(), id.TenantID, ref)
		if err != nil {
			if errors.Is(err, flowstore.ErrTenantContentNotFound) {
				writeError(w, http.StatusNotFound, "contenido no encontrado")
				return
			}
			writeError(w, http.StatusInternalServerError, "no se pudo leer el contenido")
			return
		}
		// El blob se almacenó ya validado como JSON (upsert exige json.Valid); se
		// devuelve VERBATIM vía writeJSON(json.RawMessage) como application/json.
		writeJSON(w, http.StatusOK, json.RawMessage(blob))
	})
}

// deleteTenantContentHandler devuelve el handler de DELETE
// /api/v1/tenant-content/{ref}: borra el blob (tenant del token, ref). Escritura
// auditada (content.write) sin PII. Respuestas: 204 al borrar; 404 si no existía (o
// es de otro tenant); 401 sin identidad; 400 sin ref; 500 en otro fallo.
func deleteTenantContentHandler(cs TenantContentStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}
		if cs == nil {
			writeError(w, http.StatusInternalServerError, "store de contenido no configurado")
			return
		}
		ref := r.PathValue("ref")
		if ref == "" {
			writeError(w, http.StatusBadRequest, "ref requerida en la ruta")
			return
		}
		if err := cs.DeleteTenantContent(r.Context(), id.TenantID, ref); err != nil {
			if errors.Is(err, flowstore.ErrTenantContentNotFound) {
				writeError(w, http.StatusNotFound, "contenido no encontrado")
				return
			}
			writeError(w, http.StatusInternalServerError, "no se pudo borrar el contenido")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
