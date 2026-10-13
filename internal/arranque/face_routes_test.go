package arranque

// face_routes_test.go — QUE LA CARA DEL ARRANQUE REAL REGISTRE EXACTAMENTE LAS RUTAS DEL :8103
// DE LA DORADA (F10 · T10.9 · FX TX.25), y la ayuda para preguntarle a una cara por un patrón.
//
// La huella (huella_test.go) sondea el :8103 con candidatos sacados de los literales de
// `X.Handle("…")` del código y de la propia dorada: un patrón que la cara registrara sin
// literal (una variable, una concatenación) no sería candidato y no saldría como «sobra».
// Aquí se le pregunta a la cara por su lista, que no depende de cómo se escribió el registro.

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/arranque/huellatest"
)

var comodin = regexp.MustCompile(`\{[^}]*\}`)

// peticionDe sintetiza la petición que casa con un patrón: su método (GET si no lleva)
// y cada comodín sustituido por «x».
func peticionDe(patron string) *http.Request {
	metodo, camino := http.MethodGet, patron
	if i := strings.IndexAny(patron, " \t"); i >= 0 {
		metodo, camino = patron[:i], strings.TrimSpace(patron[i+1:])
	}
	return httptest.NewRequest(metodo, comodin.ReplaceAllString(camino, "x"), nil)
}

// TestNewFace_RegistersExactlyTheGoldenRoutes: en los dos perfiles de la huella, los
// Patrones() de la cara REAL que la fase 8 guardó en el contenedor son, uno a uno, las rutas
// del :8103 de la dorada (73): ni falta una ni sobra otra. Y la cara resuelve cada una con su
// MISMO patrón: ningún comodín de otra ruta se la lleva.
func TestNewFace_RegistersExactlyTheGoldenRoutes(t *testing.T) {
	golden, err := huellatest.ReadGolden(doradaDeHuella)
	if err != nil {
		t.Fatalf("leyendo la dorada %s: %v", doradaDeHuella, err)
	}
	wantByProfile := map[string][]string{}
	for _, h := range golden {
		wantByProfile[h.Perfil] = slices.Sorted(slices.Values(h.Rutas[":8103"]))
	}
	for _, profile := range perfilesDeHuella {
		t.Run(profile, func(t *testing.T) {
			c := contenedorDeHuella(t, profile)
			if c.publicCara == nil {
				t.Fatal("la fase 8 no guardó la cara del :8103 en el contenedor")
			}
			want := wantByProfile[profile]
			if len(want) != 73 {
				t.Fatalf("la dorada trae %d rutas del :8103 para el perfil %q; se esperan 73", len(want), profile)
			}
			got := c.publicCara.Patrones()
			slices.Sort(got)
			if !slices.Equal(got, want) {
				t.Errorf("la cara registra:\n%s\ny la dorada dice:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
			for _, route := range want {
				if _, pattern := c.publicCara.Handler(peticionDe(route)); pattern != route {
					t.Errorf("%s resuelve con el patrón %q en la cara; se espera %q", route, pattern, route)
				}
			}
		})
	}
}
