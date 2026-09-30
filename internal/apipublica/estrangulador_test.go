package apipublica_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
)

// cuerpo404 es lo que escribe el 404 del ServeMux (http.NotFound): la huella lo usa para dar una
// ruta por no montada.
const cuerpo404 = "404 page not found\n"

// contador es un handler que cuenta sus llamadas y, si escribir, responde estado + cuerpo.
type contador struct {
	llamadas int
	estado   int
	cuerpo   string
	vista    *http.Request // la última petición recibida (el puntero, no una copia)
}

func (c *contador) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.llamadas++
	c.vista = r
	if c.estado != 0 {
		w.WriteHeader(c.estado)
	}
	if c.cuerpo != "" {
		escribir(w, c.cuerpo)
	}
}

// escribir escribe s en w. Un ResponseRecorder no falla al escribir: si fallara, el test no
// sabría qué está midiendo, así que es un panic y no un error ignorado.
func escribir(w http.ResponseWriter, s string) {
	//nolint:gosec // G705: el destino es un httptest.ResponseRecorder, no un navegador
	if _, err := io.WriteString(w, s); err != nil {
		panic("escribir en la respuesta de prueba: " + err.Error())
	}
}

// servir hace una petición httptest sobre h y devuelve la respuesta grabada.
func servir(h http.Handler, metodo, destino string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(metodo, destino, nil))
	return rec
}

// recuperar ejecuta f y devuelve el valor de su panic (nil si no hubo).
func recuperar(f func()) (valor any) {
	defer func() { valor = recover() }()
	f()
	return nil
}

// esPendiente dice si un valor de panic es el de un contrato sin lógica: un panic así NO cuenta
// como la promesa cumplida (si no, el rojo pasaría por accidente).
func esPendiente(v any) bool {
	return strings.HasPrefix(fmt.Sprint(v), "pendiente: ")
}

// TestComponer_GanaLaNueva: si las dos caras casan, sirve la nueva y la vieja no se invoca
// (regla 1, RX.1.a).
func TestComponer_GanaLaNueva(t *testing.T) {
	nuevaH := &contador{estado: http.StatusOK, cuerpo: "nueva"}
	viejaH := &contador{estado: http.StatusOK, cuerpo: "vieja"}
	nueva := apipublica.Nueva()
	nueva.Handle("GET /api/v1/x/{id}", nuevaH)
	vieja := http.NewServeMux()
	vieja.Handle("GET /api/v1/x/{id}", viejaH)

	rec := servir(apipublica.Componer(nueva, vieja), http.MethodGet, "/api/v1/x/7")

	if nuevaH.llamadas != 1 || viejaH.llamadas != 0 || rec.Body.String() != "nueva" {
		t.Errorf("nueva=%d vieja=%d cuerpo=%q; quiero nueva=1 vieja=0 cuerpo=%q",
			nuevaH.llamadas, viejaH.llamadas, rec.Body.String(), "nueva")
	}
}

// TestComponer_DelegaEnLaVieja: lo que la nueva no casa y la vieja sí, lo sirve la vieja
// (regla 2, RX.1.b).
func TestComponer_DelegaEnLaVieja(t *testing.T) {
	nuevaH := &contador{estado: http.StatusOK, cuerpo: "nueva"}
	viejaH := &contador{estado: http.StatusAccepted, cuerpo: "vieja"}
	nueva := apipublica.Nueva()
	nueva.Handle("GET /api/v1/x", nuevaH)
	vieja := http.NewServeMux()
	vieja.Handle("POST /api/v1/y/{id}", viejaH)

	rec := servir(apipublica.Componer(nueva, vieja), http.MethodPost, "/api/v1/y/9")

	if viejaH.llamadas != 1 || nuevaH.llamadas != 0 || rec.Code != http.StatusAccepted || rec.Body.String() != "vieja" {
		t.Errorf("vieja=%d nueva=%d respuesta=%d %q; quiero vieja=1 nueva=0 respuesta=202 %q",
			viejaH.llamadas, nuevaH.llamadas, rec.Code, rec.Body.String(), "vieja")
	}
}

// TestComponer_MismaPeticion: el handler viejo recibe EL MISMO *http.Request (sin copia) y, tras
// servir, r.Pattern de ese puntero es el patrón viejo (lo lee la métrica, RX.1.b).
func TestComponer_MismaPeticion(t *testing.T) {
	const patronViejo = "GET /api/v1/y/{id}"
	viejaH := &contador{}
	vieja := http.NewServeMux()
	vieja.Handle(patronViejo, viejaH)
	x := apipublica.Componer(apipublica.Nueva(), vieja)

	r := httptest.NewRequest(http.MethodGet, "/api/v1/y/3", nil)
	x.ServeHTTP(httptest.NewRecorder(), r)

	if viejaH.vista != r || r.Pattern != patronViejo {
		t.Errorf("mismo puntero=%v r.Pattern=%q; quiero true y %q", viejaH.vista == r, r.Pattern, patronViejo)
	}
}

// TestComponer_405DeLaCaraQueConoce: con método equivocado responde el 405 de la cara que conoce
// la ruta, con SU Allow (regla 3, RX.1.c).
func TestComponer_405DeLaCaraQueConoce(t *testing.T) {
	nueva := apipublica.Nueva()
	nueva.Handle("GET /api/v1/x", &contador{})
	vieja := http.NewServeMux()
	vieja.Handle("POST /admin/y", &contador{})
	x := apipublica.Componer(nueva, vieja)

	// La referencia es cada mux sirviendo solo: el Compuesto no inventa su 405.
	refNueva := http.NewServeMux()
	refNueva.Handle("GET /api/v1/x", &contador{})

	casos := []struct {
		nombre     string
		metodo     string
		destino    string
		referencia http.Handler
	}{
		{"familia en la nueva, método ajeno: 405 con el Allow de la nueva", http.MethodDelete, "/api/v1/x", refNueva},
		{"familia en la vieja, método ajeno: 405 de la vieja", http.MethodGet, "/admin/y", vieja},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			got := servir(x, c.metodo, c.destino)
			want := servir(c.referencia, c.metodo, c.destino)
			if got.Code != http.StatusMethodNotAllowed || got.Header().Get("Allow") != want.Header().Get("Allow") {
				t.Errorf("%s %s = %d Allow=%q; quiero 405 Allow=%q",
					c.metodo, c.destino, got.Code, got.Header().Get("Allow"), want.Header().Get("Allow"))
			}
		})
	}
}

// TestComponer_404: una ruta que ninguna cara conoce termina en el 404 del mux, con el cuerpo
// exacto que la huella reconoce como «no montada» (RX.1.c).
func TestComponer_404(t *testing.T) {
	nueva := apipublica.Nueva()
	nueva.Handle("GET /api/v1/x", &contador{})
	vieja := http.NewServeMux()
	vieja.Handle("GET /admin/y", &contador{})

	rec := servir(apipublica.Componer(nueva, vieja), http.MethodGet, "/no-existe")

	if rec.Code != http.StatusNotFound || rec.Body.String() != cuerpo404 {
		t.Errorf("GET /no-existe = %d %q; quiero 404 %q", rec.Code, rec.Body.String(), cuerpo404)
	}
}

// TestComponer_NoEscribeNada: con un handler de cada cara que no escribe, la respuesta del
// Compuesto es idéntica a la del handler solo: ni estado, ni cabeceras, ni cuerpo propios
// (RX.1.d).
func TestComponer_NoEscribeNada(t *testing.T) {
	nueva := apipublica.Nueva()
	nueva.Handle("GET /api/v1/x", &contador{})
	vieja := http.NewServeMux()
	vieja.Handle("GET /admin/y", &contador{})
	x := apipublica.Componer(nueva, vieja)

	for _, destino := range []string{"/api/v1/x", "/admin/y"} {
		t.Run(destino, func(t *testing.T) {
			got := servir(x, http.MethodGet, destino)
			want := servir(&contador{}, http.MethodGet, destino)
			if got.Code != want.Code || !reflect.DeepEqual(got.Header(), want.Header()) || got.Body.String() != want.Body.String() {
				t.Errorf("GET %s = %d %v %q; quiero %d %v %q", destino,
					got.Code, got.Header(), got.Body.String(), want.Code, want.Header(), want.Body.String())
			}
		})
	}
}

// TestComponer_NilPanic: un nil en cualquiera de las dos caras hace panic al construir, con un
// mensaje propio (no el de un contrato pendiente).
func TestComponer_NilPanic(t *testing.T) {
	casos := []struct {
		nombre    string
		construir func() *apipublica.Compuesto
	}{
		{"cara nueva nil", func() *apipublica.Compuesto { return apipublica.Componer(nil, http.NewServeMux()) }},
		{"mux viejo nil", func() *apipublica.Compuesto { return apipublica.Componer(apipublica.Nueva(), nil) }},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			v := recuperar(func() { c.construir() })
			if v == nil || esPendiente(v) {
				t.Errorf("Componer con nil: panic = %v; quiero un panic de cableado (no nil, no pendiente)", v)
			}
		})
	}
}

// TestResolver: dice qué cara serviría y con qué patrón, con la lógica de las reglas (1)–(2), y
// no sirve.
func TestResolver(t *testing.T) {
	nuevaH := &contador{}
	viejaH := &contador{}
	nueva := apipublica.Nueva()
	nueva.Handle("GET /api/v1/x/{id}", nuevaH)
	vieja := http.NewServeMux()
	vieja.Handle("GET /api/v1/x/{id}", viejaH)
	vieja.Handle("POST /admin/y", viejaH)
	x := apipublica.Componer(nueva, vieja)

	casos := []struct {
		nombre     string
		metodo     string
		destino    string
		cara, patr string
	}{
		{"casan las dos: gana la nueva", http.MethodGet, "/api/v1/x/1", "nueva", "GET /api/v1/x/{id}"},
		{"solo la vieja", http.MethodPost, "/admin/y", "vieja", "POST /admin/y"},
		{"ninguna conoce la ruta", http.MethodGet, "/no-existe", "", ""},
		{"método equivocado no es resolución", http.MethodDelete, "/admin/y", "", ""},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			cara, patron := x.Resolver(httptest.NewRequest(c.metodo, c.destino, nil))
			if cara != c.cara || patron != c.patr {
				t.Errorf("Resolver(%s %s) = (%q, %q); quiero (%q, %q)", c.metodo, c.destino, cara, patron, c.cara, c.patr)
			}
		})
	}
	t.Run("no sirve", func(t *testing.T) {
		if nuevaH.llamadas+viejaH.llamadas != 0 {
			t.Errorf("Resolver invocó %d handlers; quiero 0", nuevaH.llamadas+viejaH.llamadas)
		}
	})
}
