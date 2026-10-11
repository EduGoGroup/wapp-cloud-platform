package apipublica_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
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

// TestNueva_SinPatrones: una cara recién hecha no tiene patrones.
func TestNueva_SinPatrones(t *testing.T) {
	var _ http.Handler = (*apipublica.Cara)(nil) // la Cara es un http.Handler
	c := apipublica.Nueva()
	if got := c.Patrones(); len(got) != 0 {
		t.Errorf("Nueva().Patrones() = %q; quiero longitud 0", got)
	}
}

// TestNueva_TodoA404: una cara recién hecha responde a todo con el 404 del ServeMux.
func TestNueva_TodoA404(t *testing.T) {
	c := apipublica.Nueva()
	for _, p := range []struct{ metodo, destino string }{
		{http.MethodGet, "/"},
		{http.MethodGet, "/api/v1/entitlements"},
		{http.MethodPost, "/api/v1/intakes/1/quote-suggestion"},
	} {
		t.Run(p.metodo+" "+p.destino, func(t *testing.T) {
			rec := servir(c, p.metodo, p.destino)
			if rec.Code != http.StatusNotFound || rec.Body.String() != cuerpo404 {
				t.Errorf("%s %s = %d %q; quiero 404 %q", p.metodo, p.destino, rec.Code, rec.Body.String(), cuerpo404)
			}
		})
	}
}

// TestHandle_Registra: lo registrado por Handle lo sirve el mux de la cara.
func TestHandle_Registra(t *testing.T) {
	h := &contador{estado: http.StatusCreated, cuerpo: "hecho"}
	c := apipublica.Nueva()
	c.Handle("POST /api/v1/x/{id}", h)

	rec := servir(c, http.MethodPost, "/api/v1/x/5")

	if h.llamadas != 1 || rec.Code != http.StatusCreated || rec.Body.String() != "hecho" {
		t.Errorf("llamadas=%d respuesta=%d %q; quiero 1, 201 %q", h.llamadas, rec.Code, rec.Body.String(), "hecho")
	}
}

// TestPatrones_EnOrden: Patrones() da los patrones registrados, con su texto exacto y en orden
// de registro.
func TestPatrones_EnOrden(t *testing.T) {
	want := []string{"POST /api/v1/x/{id}", "/f/", "GET /api/v1/a"}
	c := apipublica.Nueva()
	for _, p := range want {
		c.Handle(p, &contador{})
	}
	if got := c.Patrones(); !reflect.DeepEqual(got, want) {
		t.Errorf("Patrones() = %q; quiero %q", got, want)
	}
}

// registradoEn casa con la ubicación que el ServeMux añade a su mensaje de conflicto.
var registradoEn = regexp.MustCompile(` \(registered at [^)]*\)`)

// TestHandle_ConflictoPanicaComoServeMux: un patrón en conflicto hace panic con el MISMO mensaje
// que http.ServeMux.Handle (no lo esconde ni lo traduce), salvo las ubicaciones «(registered at
// fichero:línea)» que el ServeMux mete en él y que dependen de quién llamó.
func TestHandle_ConflictoPanicaComoServeMux(t *testing.T) {
	const primero, conflicto = "GET /api/v1/x/{id}", "GET /api/v1/x/{otro}"
	ref := http.NewServeMux()
	ref.Handle(primero, &contador{})
	want := recuperar(func() { ref.Handle(conflicto, &contador{}) })

	got := recuperar(func() {
		c := apipublica.Nueva()
		c.Handle(primero, &contador{})
		c.Handle(conflicto, &contador{})
	})

	sinUbicacion := func(v any) string { return registradoEn.ReplaceAllString(fmt.Sprint(v), "") }
	if want == nil || sinUbicacion(got) != sinUbicacion(want) {
		t.Errorf("panic de Handle = %v; quiero el de ServeMux.Handle: %v", got, want)
	}
}

// TestPatrones_Copia: mutar el slice devuelto no altera la cara.
func TestPatrones_Copia(t *testing.T) {
	c := apipublica.Nueva()
	c.Handle("GET /api/v1/a", &contador{})
	c.Handle("GET /api/v1/b", &contador{})

	p := c.Patrones()
	p[0] = "mutado"
	_ = append(p[:1], "añadido")

	want := []string{"GET /api/v1/a", "GET /api/v1/b"}
	if got := c.Patrones(); !reflect.DeepEqual(got, want) {
		t.Errorf("Patrones() tras mutar la copia = %q; quiero %q", got, want)
	}
}

// TestHandler_ComoServeMux: Handler da el mismo patrón que http.ServeMux.Handler con los mismos
// registros ("" si no casa, por 404 o por 405), y el handler que da para un patrón es el
// registrado.
func TestHandler_ComoServeMux(t *testing.T) {
	registrados := []string{"GET /api/v1/x/{id}", "POST /api/v1/y", "/f/"}
	h := &contador{cuerpo: "registrado"}
	c := apipublica.Nueva()
	ref := http.NewServeMux()
	for _, p := range registrados {
		c.Handle(p, h)
		ref.Handle(p, &contador{})
	}
	casos := []struct {
		nombre, metodo, destino string
	}{
		{"casa con comodín", http.MethodGet, "/api/v1/x/1"},
		{"casa por prefijo", http.MethodPut, "/f/a/b"},
		{"método equivocado", http.MethodGet, "/api/v1/y"},
		{"ruta inexistente", http.MethodGet, "/no-existe"},
	}
	for _, k := range casos {
		t.Run(k.nombre, func(t *testing.T) {
			_, got := c.Handler(httptest.NewRequest(k.metodo, k.destino, nil))
			_, want := ref.Handler(httptest.NewRequest(k.metodo, k.destino, nil))
			if got != want {
				t.Errorf("Handler(%s %s) patrón = %q; quiero %q", k.metodo, k.destino, got, want)
			}
		})
	}
	t.Run("el handler devuelto es el registrado", func(t *testing.T) {
		hh, _ := c.Handler(httptest.NewRequest(http.MethodGet, "/api/v1/x/1", nil))
		if rec := servir(hh, http.MethodGet, "/api/v1/x/1"); rec.Body.String() != "registrado" {
			t.Errorf("el handler de Handler respondió %q; quiero %q", rec.Body.String(), "registrado")
		}
	})
}

// TestHandler_NoEscribeNiMuta: Handler solo consulta: no invoca el handler y no muta r.
func TestHandler_NoEscribeNiMuta(t *testing.T) {
	h := &contador{}
	c := apipublica.Nueva()
	c.Handle("GET /api/v1/x/{id}", h)
	r := httptest.NewRequest(http.MethodGet, "/api/v1/x/1", nil)

	c.Handler(r)

	if h.llamadas != 0 || r.Pattern != "" || r.PathValue("id") != "" {
		t.Errorf("tras Handler: llamadas=%d r.Pattern=%q id=%q; quiero 0, \"\", \"\"", h.llamadas, r.Pattern, r.PathValue("id"))
	}
}

// TestServeHTTP_DelegaEnElMux: ServeHTTP sirve con el mux de la cara (r.Pattern y PathValue
// rellenos) y lo que no casa recibe el 405 con Allow del propio ServeMux.
func TestServeHTTP_DelegaEnElMux(t *testing.T) {
	h := &contador{}
	c := apipublica.Nueva()
	c.Handle("GET /api/v1/x/{id}", h)
	ref := http.NewServeMux()
	ref.Handle("GET /api/v1/x/{id}", &contador{})

	r := httptest.NewRequest(http.MethodGet, "/api/v1/x/42", nil)
	c.ServeHTTP(httptest.NewRecorder(), r)
	if h.vista != r || r.Pattern != "GET /api/v1/x/{id}" || r.PathValue("id") != "42" {
		t.Errorf("servida: mismo r=%v Pattern=%q id=%q; quiero true, %q, %q",
			h.vista == r, r.Pattern, r.PathValue("id"), "GET /api/v1/x/{id}", "42")
	}

	got := servir(c, http.MethodDelete, "/api/v1/x/42")
	want := servir(ref, http.MethodDelete, "/api/v1/x/42")
	if got.Code != want.Code || got.Header().Get("Allow") != want.Header().Get("Allow") {
		t.Errorf("DELETE = %d Allow=%q; quiero %d Allow=%q", got.Code, got.Header().Get("Allow"), want.Code, want.Header().Get("Allow"))
	}
}
