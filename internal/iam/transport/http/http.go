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
//   - credenciales/refresh/api-key/usuario inactivo → 401 (opacos, no filtran)
//   - resto (infra)        → 500
func writeDomainError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, "entrada inválida")
	case errors.Is(err, domain.ErrInvalidCredentials),
		errors.Is(err, domain.ErrUserInactive),
		errors.Is(err, domain.ErrRefreshInvalid),
		errors.Is(err, domain.ErrAPIKeyInvalid):
		writeError(w, http.StatusUnauthorized, "no autorizado")
	default:
		writeError(w, http.StatusInternalServerError, "error interno")
	}
}

// toAuthResultDTO proyecta el AuthResult del dominio al wire format.
func toAuthResultDTO(res domain.AuthResult) authResultDTO {
	return authResultDTO{
		AccessToken:  res.AccessToken,
		RefreshToken: res.RefreshToken,
		TokenType:    res.TokenType,
		ExpiresAt:    res.ExpiresAt.UTC().Format(rfc3339),
		Context: identityContextDTO{
			TenantID: res.Context.TenantID,
			UserID:   res.Context.UserID,
			Roles:    res.Context.Roles,
		},
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
