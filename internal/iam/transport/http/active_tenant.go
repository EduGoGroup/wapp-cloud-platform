package iamhttp

// active_tenant.go — LA PUERTA HTTP DE LA ELECCIÓN DE EMPRESA
// (Plan 047 · Ola 5 · T5.1, POST /api/v1/auth/active-tenant).
//
// ------------------------------------------------------------
// POR QUÉ ESTA RUTA EXISTE, EN VEZ DE UN CAMPO EN EL CANJE
// ------------------------------------------------------------
// Es la mitad visible de D-047.14. `exchangeRequest` sigue teniendo UN SOLO
// campo (`identity_token`) y lo va a seguir teniendo: INV-8 dice que el tenant se
// deriva del token y jamás se acepta del llamante. Aquí no es una regla
// abstracta — los tres consumidores web RE-CANJEAN SOLOS cada ~13 min, sin nadie
// delante (el Context Token dura 15 min por defecto), así que un `tenant_id` en
// el canje viajaría en cada refresco desatendido. En esta ruta viaja UNA vez, en
// una acción deliberada de una persona, y lo que el canje lee después está en el
// servidor.
//
// ------------------------------------------------------------
// SE MONTA DETRÁS DE `Authenticate` Y SIN `RequirePermission`
// ------------------------------------------------------------
// Es lo mismo que hace POST /api/v1/invitations/accept, y por la misma razón
// exacta: quien llega aquí tiene DOS membresías y ninguna elegida, así que su
// Context Token se emitió SIN empresa y SIN un solo grant. Cualquier
// `RequirePermission` le contestaría 403 — a todas las personas para las que este
// endpoint existe, siempre. El precedente canónico es /api/v1/auth/whoami.
//
// No es una puerta abierta: `Authenticate` sigue exigiendo un Context Token
// válido de wApp (un anónimo se lleva 401), y lo que autoriza la elección no es
// un grant sino SER MIEMBRO de la empresa que se pide — que es lo que el usecase
// comprueba contra tenant_members.
//
// ------------------------------------------------------------
// 🔴 EL RECHAZO ES 404, Y NO 403, PARA NO SERVIR DE ORÁCULO
// ------------------------------------------------------------
// Si «no eres miembro de esa empresa» y «esa empresa no existe» tuvieran
// respuestas distintas, cualquiera con un token válido podría sondear UUIDs y
// levantar el censo de empresas de la plataforma. El 404 sale por
// `writeDomainError` sobre `domain.ErrNotFound`, o sea con el MISMO cuerpo
// genérico («recurso no encontrado») con el que el resto del módulo contesta al
// recurso ajeno. Y ese mapeo no se inventa aquí: es el que ya existía, con su
// porqué escrito en http.go — «incluye el recurso de OTRA empresa: el usecase lo
// devuelve así a propósito y aquí no se puede convertir en 403 sin confirmar que
// ese rol o esa persona existen fuera».
//
// Es la misma familia del anti-oráculo de T-A3 (canje.go), resuelta aquí sin
// código nuevo: el cuerpo lo escribe la función compartida, así que no hay dos
// cadenas que alguien pueda editar por separado.

import (
	"net/http"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
)

// selectActiveTenantRequest es el cuerpo de POST /api/v1/auth/active-tenant.
//
// ⚠️ TIENE UN CAMPO Y ES UN TENANT, que es lo contrario de lo que hace el resto
// de este paquete. No es una grieta en INV-8: es la puerta que permite que INV-8
// siga entero en el canje, que es donde importa. La empresa que entra aquí no se
// cree — se comprueba contra las membresías de quien llama antes de guardarse, y
// se vuelve a comprobar en CADA canje posterior.
type selectActiveTenantRequest struct {
	TenantID string `json:"tenant_id"`
}

// ActiveTenantHandler sirve la elección de empresa. Es transporte y nada más: no
// consulta membresías, no decide desenlaces — traduce JSON ⇄ puerto y errores
// tipados ⇄ códigos HTTP.
type ActiveTenantHandler struct {
	selector in.ActiveTenantSelector
}

// NewActiveTenantHandler construye el handler sobre el puerto de entrada.
func NewActiveTenantHandler(selector in.ActiveTenantSelector) *ActiveTenantHandler {
	return &ActiveTenantHandler{selector: selector}
}

// Select sirve POST /api/v1/auth/active-tenant.
//
// LOS TRES DESENLACES:
//   - 204 — guardada; el SIGUIENTE canje usará esa empresa.
//   - 400 — falta `tenant_id` (o el contexto no acredita a nadie, que es un
//     fallo de cableado del servidor y no una credencial que falte).
//   - 404 — quien llama NO es miembro de esa empresa. Mismo cuerpo que el de una
//     empresa inexistente, a propósito.
//
// 🔴 NO SE ACUÑA UN TOKEN AQUÍ, y es una decisión, no una tarea pendiente.
// Emitir un Context Token exige un Identity Token válido delante (es lo único
// que acredita a la persona ante wApp) y aquí solo hay un Context Token ya
// emitido; fabricar un segundo camino de emisión sería duplicar el punto más
// delicado del sistema para ahorrar una llamada. La consola refresca su sesión
// por el camino que ya existe: POST /api/v1/auth/exchange.
//
// EL 405 LO DA EL MUX: la ruta se registra con el patrón método+ruta de Go 1.22,
// así que un GET ni siquiera llega aquí. Mismo criterio que canje.go y roles.go,
// y por eso no hay un `if r.Method != http.MethodPost` que sería código muerto.
func (h *ActiveTenantHandler) Select() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req selectActiveTenantRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		if err := h.selector.SelectActiveTenant(r.Context(), req.TenantID); err != nil {
			writeDomainError(w, err)
			return
		}
		// 204 y no 200 con cuerpo: no hay nada que devolver. Lo que la persona
		// necesita después —su empresa y sus grants— no está en esta respuesta,
		// está en su SIGUIENTE Context Token; el que tiene en la mano se emitió
		// antes y sigue diciendo lo que decía.
		w.WriteHeader(http.StatusNoContent)
	})
}
