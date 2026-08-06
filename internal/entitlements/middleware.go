package entitlements

import (
	"encoding/json"
	"net/http"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// featureDeniedBody es el cuerpo del 403 del gate: un código estable que la UI
// puede reconocer sin parsear prosa (feature_not_enabled) + la clave que faltó.
// El orden de los campos es el del contrato (design §D-040.5).
type featureDeniedBody struct {
	Error   string `json:"error"`
	Feature string `json:"feature"`
}

// RequireFeature devuelve un middleware net/http que exige la feature al tenant
// de la Identity autenticada (INV-8: el tenant sale del token). Es la forma
// canónica nº 1 del gate de features (design §D-040.5); la nº 2, el check
// in-code para puntos que no son HTTP, está documentada en el comentario de
// paquete.
//
// Se compone SIEMPRE después de Authenticate y de RequirePermission: el scope
// dice "puedes operar esto", la feature dice "tu plan lo incluye" — son dos
// preguntas distintas y ninguna sustituye a la otra. Sin la feature corta con
// 403 y el cuerpo {"error":"feature_not_enabled","feature":"<clave>"}.
//
// FAIL-CLOSED en los tres modos de no-resolución: sin Identity (o sin tenant en
// ella), con el Resolver caído, o con el Resolver nil. Los tres cortan con 403,
// no con 500: el llamante no debe distinguir "no lo tienes" de "no pude
// averiguarlo", y un 5xx invitaría a reintentar hasta colarse.
func RequireFeature(resolver Resolver, feature string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, ok := httpapi.IdentityFromContext(r.Context())
			if !ok || id.TenantID == "" || resolver == nil {
				writeFeatureDenied(w, feature)
				return
			}
			has, err := resolver.Has(r.Context(), id.TenantID, feature)
			if err != nil || !has {
				writeFeatureDenied(w, feature)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// writeFeatureDenied responde el 403 del gate. Ante un fallo de codificación
// (imposible con este struct) responde igualmente 403, en texto plano: nunca deja
// pasar por un error de serialización.
func writeFeatureDenied(w http.ResponseWriter, feature string) {
	body, err := json.Marshal(featureDeniedBody{Error: "feature_not_enabled", Feature: feature})
	if err != nil {
		http.Error(w, "feature_not_enabled", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	if _, werr := w.Write(body); werr != nil {
		return
	}
}
