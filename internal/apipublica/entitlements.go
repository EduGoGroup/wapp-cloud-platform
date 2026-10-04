// Porta internal/publicapi/entitlements.go @ 9a77307 (listEntitlementsHandler) y el registro de
// C2 (internal/publicapi/publicapi.go @ 9a77307, línea 559).
//
// entitlements.go — LOS DERECHOS COMERCIALES DE LA EMPRESA DEL TOKEN (mapa §2.3, C2; ADR-0022,
// Plan 040 · T2.2).

package apipublica

import (
	"net/http"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// EntitlementsDeps es lo que C2 necesita.
type EntitlementsDeps struct {
	// Entitlements es el resolver de derechos del módulo acceso NUEVO: el MISMO, con su caché de
	// 60 s, que gatea el resto de la plataforma (arquitectura.md §4: una sola caché). nil = C2
	// no se monta.
	Entitlements entitlements.Resolver
}

// MountEntitlements registra en c "GET /api/v1/entitlements" (C2), solo si d.Entitlements no
// es nil (sin él, 404 de ruta inexistente), con cadena R y permiso "entitlements.read" (ver
// Common: 401 sin token, 403 sin el permiso, ningún registro de auditoría). El 403 lo da la
// cadena, antes del handler, sin filtrar qué features existen.
//
// La respuesta:
//
//   - ListEffective recibe SIEMPRE el tenant del token (INV-8);
//   - 200 {"plan": …, "features": […], "cache_ttl_seconds": …}: el plan y las features
//     ENCENDIDAS tal como las da el resolver (en orden alfabético), y el TTL de su caché en
//     segundos enteros, truncado (60 s ⇒ 60; un TTL de menos de un segundo ⇒ 0). Solo lista
//     las efectivas, no un mapa feature→bool (D-040.3); el tenant NO viaja;
//   - un tenant sin features responde "features": [] y nunca null;
//   - un fallo de ListEffective ⇒ 500 {"error":"no se pudieron resolver los derechos del tenant"}.
//
// Defensa que los tokens de sharedjwt no alcanzan (RequirePermission corta antes): una identidad
// sin empresa que llegara al handler recibiría 401 {"error":"autenticación requerida"}.
//
// Fallo de cableado: k.MW nil con d.Entitlements no nil hace panic AL MONTAR (ver Common).
func MountEntitlements(c *Cara, k Common, d EntitlementsDeps) {
	// Derechos comerciales del tenant (Plan 040 · T2.2, ADR-0022/ADR-0033): plan
	// efectivo + features encendidas + el TTL con el que se cachean. Lectura sin
	// auditoría, acotada al tenant del token (INV-8): NO hay consulta cross-tenant,
	// el tenant no viaja en la URL. Scope entitlements.read — que tenant_admin ('*')
	// y viewer ('*.read') ya cubren por glob, y operator recibe explícito en la
	// migración 0040 (design §D-040.4). Toda UI que pinte capacidades depende de
	// esta ruta.
	if d.Entitlements == nil {
		return
	}
	mustHaveMW(k, "MountEntitlements")
	c.Handle("GET /api/v1/entitlements", protectRead(k, "entitlements.read", listEntitlementsHandler(d.Entitlements)))
}

// entitlementsResponse es la respuesta de GET /api/v1/entitlements (design §3).
// Solo lista las features EFECTIVAS (habilitadas), no un mapa completo
// feature→bool: no hay catálogo de claves en BD, así que un mapa obligaría al
// servidor a hardcodear la lista y cada plan nuevo la desincronizaría (D-040.3).
// La UI decide por `contains`.
//
// El tenant NO viaja en la respuesta: es el del token (INV-8). CERO PII y CERO
// credenciales — son derechos comerciales.
type entitlementsResponse struct {
	Plan            string   `json:"plan"`
	Features        []string `json:"features"`
	CacheTTLSeconds int      `json:"cache_ttl_seconds"`
}

// listEntitlementsHandler devuelve GET /api/v1/entitlements: el plan efectivo del
// tenant del token (INV-8) y sus features encendidas en orden alfabético, más el
// TTL real de la caché del resolver para que el cliente sepa cuánto puede tardar
// en verse un cambio. Solo lectura, sin auditoría. 200 con el contrato; 401 sin
// identidad; 500 ante fallo de infraestructura del resolver.
//
// El 403 no lo emite este handler: lo emite RequirePermission("entitlements.read")
// de la cadena, antes de llegar aquí (sin filtrar qué claves existen en el cuerpo).
//
// La rama vieja «ents == nil ⇒ 500 resolver de entitlements no configurado» no se porta: la ruta
// solo se monta con un resolver no nil (MountEntitlements), así que era inalcanzable.
func listEntitlementsHandler(ents entitlements.Resolver) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}

		plan, features, err := ents.ListEffective(r.Context(), id.TenantID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "no se pudieron resolver los derechos del tenant")
			return
		}
		if features == nil {
			// Un tenant sin features responde [], nunca null: la UI itera sin
			// ramificar por el nulo.
			features = []string{}
		}

		writeJSON(w, http.StatusOK, entitlementsResponse{
			Plan:     plan,
			Features: features,
			// Truncado a segundos enteros: el contrato habla de segundos y el TTL
			// real es de 60 s (un TTL sub-segundo, solo de tests, reporta 0).
			CacheTTLSeconds: int(ents.CacheTTL().Seconds()),
		})
	})
}
