//go:build pendiente

package apipublica_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
)

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

// TestCaraVacia_TodoALaVieja (T0.16, R0.6.a): una cara vacía compuesta delante del mux viejo deja
// pasar TODA petición al viejo sin tocarla: mismo estado, cuerpo y cabeceras que el mux viejo
// solo, también en su 404 y en su 405 con Allow.
func TestCaraVacia_TodoALaVieja(t *testing.T) {
	vieja := http.NewServeMux()
	vieja.HandleFunc("GET /api/v1/entitlements", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Vieja", "1")
		escribir(w, `{"plan":"basic"}`)
	})
	vieja.HandleFunc("POST /api/v1/intakes/{id}/quote-suggestion", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		escribir(w, r.PathValue("id"))
	})
	vieja.HandleFunc("/admin/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	x := apipublica.Componer(apipublica.Nueva(), vieja)

	for _, p := range []struct{ metodo, destino string }{
		{http.MethodGet, "/api/v1/entitlements"},
		{http.MethodPost, "/api/v1/intakes/7/quote-suggestion"},
		{http.MethodPatch, "/admin/cualquier/cosa"},
		{http.MethodGet, "/no-existe"},              // 404 del mux viejo
		{http.MethodDelete, "/api/v1/entitlements"}, // 405 del mux viejo, con Allow
	} {
		t.Run(p.metodo+" "+p.destino, func(t *testing.T) {
			got := servir(x, p.metodo, p.destino)
			want := servir(vieja, p.metodo, p.destino)
			if got.Code != want.Code || got.Body.String() != want.Body.String() || !reflect.DeepEqual(got.Header(), want.Header()) {
				t.Errorf("%s %s = %d %v %q; quiero lo del viejo: %d %v %q", p.metodo, p.destino,
					got.Code, got.Header(), got.Body.String(), want.Code, want.Header(), want.Body.String())
			}
		})
	}
}
