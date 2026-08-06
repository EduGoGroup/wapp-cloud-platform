package publicapi

import (
	"context"
	"net/http"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// EntitlementsResolver es el puerto de derechos comerciales que consume la API
// pública (ADR-0022). Agrupa las DOS preguntas que se le hacen desde aquí: el
// gate binario de una capacidad (FeatureChecker.Has, el del PUT de intents) y la
// lectura de los derechos efectivos del tenant que alimenta
// GET /api/v1/entitlements (Plan 040 · T2.2). Lo satisface *entitlements.Postgres
// (y el Fake en tests).
type EntitlementsResolver interface {
	FeatureChecker

	// ListEffective devuelve el plan efectivo y las features ENCENDIDAS del
	// tenant, en orden alfabético.
	ListEffective(ctx context.Context, tenantID string) (plan string, features []string, err error)

	// CacheTTL es el TTL con el que el resolver cachea: la cota superior de lo
	// que tarda en verse un cambio de plan/override.
	CacheTTL() time.Duration
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
func listEntitlementsHandler(ents EntitlementsResolver) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}
		if ents == nil {
			writeError(w, http.StatusInternalServerError, "resolver de entitlements no configurado")
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
