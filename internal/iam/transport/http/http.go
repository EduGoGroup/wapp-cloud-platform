package iamhttp

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
)

// rfc3339 es el formato de los instantes de expiración en el wire.
const rfc3339 = time.RFC3339

// decodeJSON decodifica el cuerpo en dst. Responde 400 y devuelve false si el
// JSON es inválido (el caller debe abortar).
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "cuerpo JSON inválido")
		return false
	}
	return true
}

// bearer extrae el token de Authorization: Bearer <token>. ok=false si falta o
// el esquema no es Bearer.
func bearer(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	tok := strings.TrimSpace(h[len(prefix):])
	return tok, tok != ""
}

// methodNotAllowed responde 405 con cuerpo JSON tipado.
func methodNotAllowed(w http.ResponseWriter) {
	writeError(w, http.StatusMethodNotAllowed, "método no permitido")
}

// writeError responde un error como JSON tipado {error}.
func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// writeJSON serializa v como JSON con el código dado.
func writeJSON(w http.ResponseWriter, code int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "error codificando respuesta", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if _, werr := w.Write(body); werr != nil {
		return
	}
}

// writeDomainError mapea los errores tipados del dominio IAM a códigos HTTP:
//   - ErrInvalidInput      → 400
//   - credenciales/refresh/usuario inactivo → 401 (opacos, no filtran)
//   - identity token no aceptable / sujeto sin migrar → 401 (con motivo: el
//     cliente es el BFF o el gateway, no un anónimo probando contraseñas, y
//     necesita distinguir "refresca" de "este usuario no está en wApp")
//   - ErrNoTenant          → 403 (el token acredita a la persona pero no trae
//     empresa, D-056.12; es el MISMO código con el que el middleware ya rechaza
//     un token sin empresa en las rutas de negocio — ver tenantless_test.go)
//   - ErrNotFound          → 404 (incluye el recurso de OTRA empresa: el usecase
//     lo devuelve así a propósito y aquí no se puede convertir en 403 sin
//     confirmar que ese rol o esa persona existen fuera)
//   - ErrConflict / más de un tenant → 409
//   - ErrGlobalRoleImmutable → 422 (el cuerpo se entiende; lo que no se puede
//     procesar es editar una plantilla que vale para todos los tenants)
//   - identity inalcanzable → 503 (indisponibilidad, NO rechazo)
//   - resto (infra)        → 500
func writeDomainError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, "entrada inválida")
	case errors.Is(err, domain.ErrNoTenant):
		writeError(w, http.StatusForbidden, "el token no trae empresa: no puede administrar roles ni miembros")
	case errors.Is(err, domain.ErrNotFound):
		writeError(w, http.StatusNotFound, "recurso no encontrado")
	case errors.Is(err, domain.ErrConflict):
		writeError(w, http.StatusConflict, "conflicto: el recurso ya existe o la persona ya pertenece a otra empresa")
	case errors.Is(err, domain.ErrGlobalRoleImmutable):
		writeError(w, http.StatusUnprocessableEntity, "las plantillas de rol globales no se modifican desde una empresa")
	case errors.Is(err, domain.ErrInvalidCredentials),
		errors.Is(err, domain.ErrUserInactive),
		errors.Is(err, domain.ErrRefreshInvalid):
		writeError(w, http.StatusUnauthorized, "no autorizado")
	case errors.Is(err, domain.ErrIdentityTokenInvalid):
		writeError(w, http.StatusUnauthorized, "identity token inválido")
	case errors.Is(err, domain.ErrIdentityTokenExpiring):
		writeError(w, http.StatusUnauthorized, "al identity token le queda muy poca vida: refresca antes de canjearlo")
	case errors.Is(err, domain.ErrUserNotMigrated):
		writeError(w, http.StatusUnauthorized, "usuario no migrado")
	case errors.Is(err, domain.ErrMultipleTenants):
		writeError(w, http.StatusConflict, "el usuario pertenece a más de un tenant: sin resolución hasta el Plan 005")
	case errors.Is(err, domain.ErrIdentityUnavailable):
		writeError(w, http.StatusServiceUnavailable, "identity no está disponible")
	default:
		writeError(w, http.StatusInternalServerError, "error interno")
	}
}

// toVerifyResultDTO proyecta el VerifyResult del puerto al wire format. Solo
// serializa los campos de identidad cuando Valid=true.
func toVerifyResultDTO(v in.VerifyResult) verifyResultDTO {
	dto := verifyResultDTO{Valid: v.Valid}
	if v.Valid {
		dto.TenantID = v.TenantID
		dto.Subject = v.Subject
		dto.Roles = v.Roles
		if !v.ExpiresAt.IsZero() {
			dto.ExpiresAt = v.ExpiresAt.UTC().Format(rfc3339)
		}
	}
	return dto
}
