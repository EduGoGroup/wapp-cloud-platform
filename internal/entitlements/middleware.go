package entitlements

import (
	"encoding/json"
	"net/http"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// featureDeniedBody es el cuerpo del 403 del gate: un código estable que la UI
// puede reconocer sin parsear prosa (feature_not_enabled) + la clave que faltó.
// El orden de los campos es el del contrato (design §D-040.5).
//
// `features` (plural) es de RequireAnyFeature y lleva las claves de las que
// BASTA UNA. Va aparte del singular y no reutilizándolo porque son respuestas
// distintas: «te falta cart_basic» es accionable, y decir eso cuando en realidad
// valía cualquiera de cuatro mandaría a la UI a ofrecer el upgrade equivocado.
//
// Los dos llevan omitempty, así que el cuerpo de RequireFeature sale EXACTAMENTE
// igual que antes de existir el plural (`features` nil se omite): el contrato
// vigente del Plan 040 no se toca.
type featureDeniedBody struct {
	Error    string   `json:"error"`
	Feature  string   `json:"feature,omitempty"`
	Features []string `json:"features,omitempty"`
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

// RequireAnyFeature es el gate de una capacidad que NO pertenece a una feature
// sino a varias: pasa el tenant que tenga AL MENOS UNA de las claves dadas.
//
// Nace del listado de eventos conversacionales (Plan 043 · T3.9b, decisión de
// Jhoan del 2026-08-09): esa bandeja abarca los cuatro tipos de fábrica —menu,
// cart, survey, media— y cada uno es una feature por derecho propio, así que
// gatearla con una sola habría cegado a un tenant de solo encuestas sobre sus
// PROPIAS encuestas. Quien la use debe además filtrar el CONTENIDO por las
// features que el tenant sí tiene: pasar el gate por una no da derecho a ver las
// otras.
//
// Mismas reglas que RequireFeature, y por las mismas razones: se compone después
// de Authenticate y RequirePermission, y es FAIL-CLOSED en los tres modos de
// no-resolución (sin identidad, resolver caído, resolver nil) — con una vuelta de
// tuerca propia del plural: un error al preguntar por UNA clave no se traga para
// seguir probando las demás, porque entonces un resolver caído se leería como «no
// tiene ninguna» solo a veces, según el orden. Un error corta con 403 en el acto.
//
// Sin claves NO abre: una lista vacía es «ninguna basta», no «todas valen». Lo
// consigue el propio bucle —no itera y cae en la denegación final—, así que NO hay
// una guarda `len(features) == 0` al principio: se escribió, se comprobó que no
// cambiaba nada y se quitó. Una guarda que no altera el comportamiento sugiere que
// sin ella pasaría lo contrario, y lo que sostiene la regla es
// TestRequireAnyFeature_SinClavesNoAbre.
func RequireAnyFeature(resolver Resolver, features ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, ok := httpapi.IdentityFromContext(r.Context())
			if !ok || id.TenantID == "" || resolver == nil {
				writeAnyFeatureDenied(w, features)
				return
			}
			for _, f := range features {
				has, err := resolver.Has(r.Context(), id.TenantID, f)
				if err != nil {
					writeAnyFeatureDenied(w, features)
					return
				}
				if has {
					next.ServeHTTP(w, r)
					return
				}
			}
			writeAnyFeatureDenied(w, features)
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

// writeAnyFeatureDenied es el 403 del gate plural: el MISMO código de error, con
// la lista de las que habrían valido en vez de una clave suelta.
//
// El cuerpo no dice cuál falló ni en qué orden se preguntó, y eso es deliberado:
// las tres no-resoluciones (sin identidad, resolver caído, ninguna concedida)
// responden EXACTAMENTE lo mismo, igual que en el singular. Un cuerpo que
// distinguiera «no pude averiguarlo» de «no lo tienes» invitaría a reintentar
// hasta colarse.
func writeAnyFeatureDenied(w http.ResponseWriter, features []string) {
	body, err := json.Marshal(featureDeniedBody{Error: "feature_not_enabled", Features: features})
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
