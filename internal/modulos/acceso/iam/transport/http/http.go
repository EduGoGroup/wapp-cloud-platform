// Porta internal/iam/transport/http/http.go @ 9a77307

package iamhttp

// http.go — LA FONTANERÍA COMPARTIDA DEL TRANSPORTE HTTP DEL IAM: decodificar el cuerpo, leer el
// `Bearer`, escribir JSON y, sobre todo, MAPEAR LOS ERRORES TIPADOS DEL DOMINIO A CÓDIGOS HTTP en
// UN solo sitio (writeDomainError), para que no haya dos cuerpos para el mismo error que alguien
// pueda editar por separado.
//
// Sin exportados: no tuvo contrato en rojo (T-14, E-4; hallazgo 19 de F2) y nace entero en su
// verde con el test de sus reglas (P6). Los textos son los de diseño §5, byte a byte: el BFF y
// `wapp-ctl` los enseñan tal cual.
//
// ⚠️ Dos piezas del viejo http.go viven ahora en auth.go: `rfc3339` y `toVerifyResultDTO`. La
// segunda proyecta sobre `verifyResultDTO`, que es de auth.go, y este fichero nace ANTES que
// aquel (sus handlers usan estos auxiliares); con ellas aquí no compilaría. La constante va con
// su primer consumidor (Exchange) por la misma razón: sin él sería un `unused`.

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
)

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
//   - ErrConflict          → 409
//   - ErrGlobalRoleImmutable → 422 (el cuerpo se entiende; lo que no se puede
//     procesar es editar una plantilla que vale para todos los tenants)
//   - identity inalcanzable → 503 (indisponibilidad, NO rechazo)
//   - identity sin configurar → 503 con cuerpo PROPIO (falta la credencial M2M
//     de este despliegue: no es indisponibilidad, es configuración)
//   - aplicación no acreditable → 502 (identity rechazó `wapp.bff`; el fallo
//     está aguas arriba, no en wApp)
//   - resto (infra)        → 500. Aquí cae a propósito ErrMachineCredentialInvalid:
//     una API key de wApp sin el scope `identity.users.systems.read` es un fallo
//     del SERVIDOR y el llamante no puede hacer nada con esa distinción
//
// ⚠️ ErrInvitationExpired NO tiene rama aquí, y es deliberado: cae en el 500. El único que lo
// produce es el canje, y canje.go lo saca ANTES de llegar aquí con su cuerpo compartido con el
// 404 (anti-oráculo, R-H4). Darle aquí un texto propio sería abrir la puerta a dos cuerpos.
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
	case errors.Is(err, domain.ErrIdentityUnavailable):
		writeError(w, http.StatusServiceUnavailable, "identity no está disponible")
	case errors.Is(err, domain.ErrIdentityNotConfigured):
		// 503 como el de arriba, pero con un cuerpo que se distingue: aquel dice
		// «identity no contesta» (espera y reintenta) y este «a este despliegue le
		// falta WAPP_IDENTITY_API_KEY» (no hay nada que esperar, hay que
		// configurarlo). Un solo cuerpo para los dos mandaría a mirar el sitio
		// equivocado justo cuando alguien está de guardia.
		writeError(w, http.StatusServiceUnavailable, "identity_no_configurado")
	case errors.Is(err, domain.ErrSystemNotAllowed):
		// 502 y no 500: la petición era buena y wApp también; quien rechazó fue
		// identity, aguas arriba, al no reconocer la aplicación del conjunto. Un
		// 500 diría «wApp se rompió» y mandaría a leer estos logs, donde no hay
		// nada que ver.
		writeError(w, http.StatusBadGateway, "system_no_acreditable")
	default:
		writeError(w, http.StatusInternalServerError, "error interno")
	}
}
