package arranque

import (
	"net/http"
	"testing"
)

// TestCableado_TheOldFaceServesNoRoute fija, sobre el compuesto REAL del arranque (los dos
// perfiles de la huella), la regla de F8 · conmutar(conversacion) (FX TX.24): la cara vieja no
// sirve NINGUNA ruta. Las 73 filas del :8103 del mapa las resuelve la cara nueva con su mismo
// patrón, y ninguna petición —ni la de una fila, ni otro método sobre el camino de una fila—
// resuelve en la vieja, porque detrás del estrangulador hay un mux vacío.
//
// Generaliza el hallazgo 70 (TestCableado_TheOldFaceNeverServesMessages), que de F3 a F8
// afirmaba esto mismo solo para «POST /api/v1/messages»: la cara vieja la registraba con el
// Sender a nil y lo único que la hacía inalcanzable era que la nueva iba delante. Ya no hay
// handler viejo que tapar.
//
// Lo segundo (otros métodos) no es redundante con lo primero: un mux de detrás con una ruta
// SIN método («/api/v1/flows») no le quitaría ninguna fila a la nueva y aun así serviría los
// métodos que ella no registra.
func TestCableado_TheOldFaceServesNoRoute(t *testing.T) {
	methods := []string{
		http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodOptions,
	}
	rows := leerMapa(t)
	for _, profile := range perfilesDeHuella {
		t.Run(profile, func(t *testing.T) {
			c := contenedorDeHuella(t, profile)
			if c.publicCompuesto == nil {
				t.Fatal("la fase 8 no guardó el compuesto del :8103 en el contenedor")
			}
			onTheNew := 0
			for _, row := range rows {
				if row.listener != ":8103" {
					continue
				}
				req := peticionDe(row.patron)
				face, pattern := c.publicCompuesto.Resolver(req)
				if face != "nueva" || pattern != row.patron {
					t.Errorf("%s %q resuelve (%q, %q); se espera (\"nueva\", %q): desde F8 las 73 rutas son de la cara nueva",
						row.id, row.patron, face, pattern, row.patron)
					continue
				}
				onTheNew++
				for _, method := range methods {
					other := req.Clone(req.Context())
					other.Method = method
					if face, pattern := c.publicCompuesto.Resolver(other); face == "vieja" {
						t.Errorf("%s %s lo resuelve la cara vieja (%q): detrás del estrangulador tiene que haber un mux vacío",
							method, req.URL.Path, pattern)
					}
				}
			}
			if onTheNew != 73 {
				t.Errorf("la cara nueva resuelve %d filas del :8103; se esperan las 73", onTheNew)
			}
		})
	}
}
