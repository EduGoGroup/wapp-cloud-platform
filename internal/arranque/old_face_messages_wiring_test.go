package arranque

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestCableado_TheOldFaceNeverServesMessages fija el hallazgo 70. La cara VIEJA registra
// «POST /api/v1/messages» siempre (no tiene condición de montaje), y desde conmutar(edge) lo
// hace con Sender a nil: si una petición llegara a ese handler sería un nil-pointer en
// producción. No puede llegar, y este test dice por qué con el compuesto REAL del arranque:
//   - el POST lo resuelve la cara nueva, que va delante;
//   - cualquier otro método sobre ese camino NO lo resuelve la vieja (solo registró el POST):
//     cae al 405 de la nueva, que conoce la ruta.
//
// El candado de mudanzas ya afirma lo primero para las 29 rutas mudadas; aquí se afirma además
// lo segundo, que es lo que hace inalcanzable el handler viejo y no solo «tapado».
func TestCableado_TheOldFaceNeverServesMessages(t *testing.T) {
	const path = "/api/v1/messages"
	methods := []string{
		http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodOptions,
	}
	for _, profile := range perfilesDeHuella {
		t.Run(profile, func(t *testing.T) {
			c := contenedorDeHuella(t, profile)
			if c.publicCompuesto == nil {
				t.Fatal("la fase 8 no guardó el compuesto del :8103 en el contenedor")
			}
			for _, method := range methods {
				face, pattern := c.publicCompuesto.Resolver(httptest.NewRequest(method, path, nil))
				if face == "vieja" {
					t.Errorf("%s %s lo resuelve la cara vieja (%q), que tiene el Sender a nil", method, path, pattern)
				}
				if method == http.MethodPost && (face != "nueva" || pattern != "POST "+path) {
					t.Errorf("POST %s resuelve (%q, %q); se espera (\"nueva\", \"POST %s\")", path, face, pattern, path)
				}
			}
		})
	}
}
