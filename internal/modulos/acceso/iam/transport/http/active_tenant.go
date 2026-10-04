// Porta internal/iam/transport/http/active_tenant.go @ 9a77307

package iamhttp

// active_tenant.go — LA PUERTA HTTP DE LA ELECCIÓN DE EMPRESA
// (Plan 047 · Ola 5 · T5.1, POST /api/v1/auth/active-tenant y GET /api/v1/auth/tenants).
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
// recurso ajeno. El cuerpo lo escribe la función compartida, así que no hay dos
// cadenas que alguien pueda editar por separado.
//
// EL 405 LO DA EL MUX: las dos rutas se registran con el patrón método+ruta de Go 1.22, así que
// un método ajeno ni siquiera llega aquí (por eso no hay un `if r.Method != …` que sería código
// muerto).

import (
	"net/http"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
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

// ActiveTenantHandler sirve las DOS mitades de la empresa del sujeto: LEER entre
// cuáles puede elegir (List) y ESCRIBIR cuál elige (Select). Es transporte y nada
// más: no consulta membresías, no decide desenlaces — traduce JSON ⇄ puertos y
// errores tipados ⇄ códigos HTTP.
//
// Recibe DOS puertos y no uno aunque hoy los satisfaga el mismo servicio: leer y
// escribir son capacidades distintas, y un handler que solo pintara el selector
// no tendría por qué recibir de paso la de cambiar la empresa activa.
type ActiveTenantHandler struct {
	selector in.ActiveTenantSelector
	lister   in.TenantLister
}

// NewActiveTenantHandler construye el handler sobre los puertos de entrada. No valida nada: el
// cableado lo hace el arranque.
func NewActiveTenantHandler(selector in.ActiveTenantSelector, lister in.TenantLister) *ActiveTenantHandler {
	return &ActiveTenantHandler{selector: selector, lister: lister}
}

// tenantOptionDTO es UNA empresa en la respuesta de GET /api/v1/auth/tenants.
//
// Tiene TRES campos y ninguno sobra: sin `id` el selector no puede mandar la
// elección de vuelta, sin `display_name` pinta UUIDs y sin `active` no sabe cuál marcar al
// cargar.
//
// 🔴 Y NO TIENE UN CUARTO. Nada de `slug`, `plan_id`, `revoked_at`, `created_at`
// ni conteos: eso es el detalle de empresa del plano de PLATAFORMA
// (platformadmin.TenantListItem), cuya audiencia es el operador. Aquí la
// audiencia es alguien que solo quiere saber por cuál puerta entra.
type tenantOptionDTO struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	// Active marca la que llevará el PRÓXIMO Context Token. Como mucho una es
	// true, y puede que ninguna — «ninguna» es un estado legítimo y se expresa
	// sin marcar nada, sin necesidad de un elemento centinela.
	Active bool `json:"active"`
}

// tenantListDTO es la respuesta de GET /api/v1/auth/tenants.
//
// 🔴 NO LLEVA UN `active_tenant_id` AL LADO, y es deliberado aunque fuera cómodo:
// sería una SEGUNDA fuente del mismo hecho que ya expresa el `active` de cada
// elemento, y dos fuentes para el mismo dato es como se desincronizan.
//
// ⚠️ Es un OBJETO y no un array pelado, aunque hoy solo tenga una clave: un array
// en la raíz no admite añadir nada después sin romper a todos los clientes.
type tenantListDTO struct {
	// Tenants NUNCA es null: cero empresas se serializa como `[]`. Que el cliente
	// tenga que distinguir `null` de `[]` para el mismo hecho es un defecto, no
	// una economía.
	Tenants []tenantOptionDTO `json:"tenants"`
}

// Select sirve POST /api/v1/auth/active-tenant. Cuerpo `{"tenant_id":"…"}` (un campo, y es un
// tenant: la puerta que deja INV-8 entero en el canje; la empresa no se cree, la comprueba el
// usecase contra las membresías).
//
// R-H1, LOS DESENLACES:
//   - 204 SIN cuerpo — guardada; el `tenant_id` del cuerpo llega TAL CUAL al puerto
//     (in.ActiveTenantSelector.SelectActiveTenant), una vez. El SIGUIENTE canje usará esa
//     empresa; aquí NO se acuña un token (emitir exige un Identity Token delante).
//   - 400 «cuerpo JSON inválido» — JSON roto, y el puerto no se llama.
//   - 400 — el puerto devuelve domain.ErrInvalidInput (falta `tenant_id`, o el contexto no
//     acredita a nadie: fallo de cableado, no credencial que falte).
//   - 404 — el puerto devuelve domain.ErrNotFound: quien llama NO es miembro. El cuerpo es el
//     genérico de writeDomainError («recurso no encontrado») y no nombra miembro, empresa ni
//     existencia: no es oráculo.
//   - 500 — cualquier otro error (infraestructura).
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

// List sirve GET /api/v1/auth/tenants: las empresas del sujeto, con su nombre y
// con la activa marcada. Existe porque el Context Token de quien tiene CERO empresas y el de quien
// tiene DOS sin elegir son idénticos; la lista vacía y la de dos separan la pantalla de espera
// del selector sin tocar el token.
//
// R-H2, LA FORMA EXACTA: `{"tenants":[{"id":…,"display_name":…,"active":…}, …]}`.
//   - Un objeto con UNA clave (`tenants`), no un array pelado (para poder crecer) y SIN
//     `active_tenant_id` al lado (sería una segunda fuente del mismo hecho).
//   - Cada elemento con TRES claves y ni una más: nada de `slug`, `plan_id`, totales ni
//     conteos (eso es del plano de plataforma). Nunca una pista de cuántas empresas hay fuera.
//   - El ORDEN del puerto se conserva: el transporte no reordena.
//   - `active` es true SOLO en la empresa cuyo ID es el activeID que devuelve el puerto; con
//     activeID vacío ninguna se marca (ningún ID real es "").
//   - Cero empresas (también un nil del puerto) ⇒ 200 con la cadena literal `"tenants":[]`,
//     nunca `null`: no pertenecer a ninguna todavía es un estado del producto (D-056.12).
//
// Fallos: el listado NO tiene 404. domain.ErrInvalidInput (el contexto no acredita a nadie:
// cableado) ⇒ 400; cualquier otro ⇒ 500.
func (h *ActiveTenantHandler) List() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenants, activeID, err := h.lister.TenantsOfCaller(r.Context())
		if err != nil {
			writeDomainError(w, err)
			return
		}
		// La proyección, y el `active` calculado en UN solo sitio: comparando
		// contra el activeID que el puerto ya resolvió con la misma regla que el
		// canje. Aquí no se decide cuál es la activa, se pinta.
		// (options: `opciones` en el viejo, E-11.) make con capacidad, nunca nil: un nil del
		// puerto también sale como `[]`.
		options := make([]tenantOptionDTO, 0, len(tenants))
		for _, t := range tenants {
			options = append(options, tenantOptionDTO{
				ID:          t.ID,
				DisplayName: t.DisplayName,
				// activeID vacío ("ninguna") no marca nada: ningún ID real es "".
				Active: activeID != "" && t.ID == activeID,
			})
		}
		writeJSON(w, http.StatusOK, tenantListDTO{Tenants: options})
	})
}
