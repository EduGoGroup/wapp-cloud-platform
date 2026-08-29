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

// NewActiveTenantHandler construye el handler sobre los puertos de entrada.
func NewActiveTenantHandler(selector in.ActiveTenantSelector, lister in.TenantLister) *ActiveTenantHandler {
	return &ActiveTenantHandler{selector: selector, lister: lister}
}

// tenantOptionDTO es UNA empresa en la respuesta de GET /api/v1/auth/tenants.
//
// Tiene TRES campos y ninguno sobra: sin `id` el selector no puede mandar la
// elección de vuelta, sin `display_name` pinta UUIDs (que es lo que la consola
// hacía hasta hoy) y sin `active` no sabe cuál marcar al cargar.
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
// elemento, y dos fuentes para el mismo dato es como se desincronizan (la casa ya
// lo tiene escrito en la cabecera de la 0085 sobre el TTL). Con los flags de los
// elementos basta: «ninguna activa» es «ningún elemento con active:true».
//
// ⚠️ Es un OBJETO y no un array pelado, aunque hoy solo tenga una clave: un array
// en la raíz no admite añadir nada después sin romper a todos los clientes, y una
// respuesta que ya se sabe que va a crecer (paginación no, pero sí quizá un aviso
// de empresa revocada) no se sirve así.
type tenantListDTO struct {
	// Tenants NUNCA es null: cero empresas se serializa como `[]`. Que el cliente
	// tenga que distinguir `null` de `[]` para el mismo hecho es un defecto, no
	// una economía.
	Tenants []tenantOptionDTO `json:"tenants"`
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

// List sirve GET /api/v1/auth/tenants: las empresas del sujeto, con su nombre y
// con la activa marcada.
//
// 🔴 POR QUÉ ESTE ENDPOINT EXISTE, que es más que «faltaba un listado». El
// Context Token de quien tiene CERO empresas y el de quien tiene DOS y no ha
// elegido son IDÉNTICOS: los dos sin tenant y sin un solo grant. Sin este
// listado, la consola no puede distinguir si tocaba pintar la pantalla de espera
// («tu acceso está en revisión») o el selector («¿con cuál entras?»). La lista
// vacía y la lista de dos separan los dos casos sin tocar el token.
//
// LOS DESENLACES:
//   - 200 con `{"tenants":[...]}` — incluida la lista VACÍA, que NO es un 404:
//     no pertenecer a ninguna empresa todavía es un estado del producto
//     (D-056.12).
//   - 400 — el contexto no acredita a nadie (fallo de cableado del servidor: a
//     este handler solo se llega detrás de Authenticate).
//   - 500 — infraestructura.
//
// 🔴 NUNCA UNA EMPRESA AJENA, NI UNA PISTA DE QUE EXISTA. La respuesta no lleva
// total, ni conteo, ni mensaje que insinúe cuántas hay fuera: es exactamente lo
// que el usuario ya sabe (las suyas) y nada más. La garantía no está en este
// fichero —el transporte pinta lo que le den—, está en el INNER JOIN gobernado
// por `user_id` del adaptador (iampostgres.MembershipRepo.UserTenants).
//
// SE MONTA DETRÁS DE `Authenticate` Y SIN `RequirePermission`, igual que Select y
// por lo mismo: quien necesita este listado es precisamente quien todavía no
// tiene empresa en su token, así que cualquier grant requerido le daría 403.
//
// EL 405 LO DA EL MUX (patrón método+ruta de Go 1.22), como en el resto del
// paquete.
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
		opciones := make([]tenantOptionDTO, 0, len(tenants))
		for _, t := range tenants {
			opciones = append(opciones, tenantOptionDTO{
				ID:          t.ID,
				DisplayName: t.DisplayName,
				// activeID vacío ("ninguna") no marca nada: ningún ID real es "".
				Active: activeID != "" && t.ID == activeID,
			})
		}
		writeJSON(w, http.StatusOK, tenantListDTO{Tenants: opciones})
	})
}
