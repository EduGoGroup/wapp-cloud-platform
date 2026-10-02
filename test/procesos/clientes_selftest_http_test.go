//go:build integracion

package procesos

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// Los tests propios del cliente HTTP del arnés: el rechazo de la vía LLM «api» antes de salir a la
// red (TestArnes_SinViaAPI) y los métodos, el token y el plazo (TestArnes_Cliente), contra un eco.
// Sale de clientes_test.go (D-F9-11: solo se movieron declaraciones).

// ---------------------------------------------------------------------------------------------
// Tests propios del arnés
// ---------------------------------------------------------------------------------------------

// TestArnes_SinViaAPI prueba el candado de cero gasto: la función pura veta un PUT con via=api a
// /api/v1/tenant-llm (y sus variantes de ruta, mayúsculas y método) y deja pasar todo lo demás, y
// el cliente real corta ANTES de enviar: el servidor de prueba no ve ni una petición vetada y sí la
// permitida. No necesita Docker.
func TestArnes_SinViaAPI(t *testing.T) {
	t.Parallel()
	const llm = prefijoTenantLLM
	casos := []struct {
		nombre string
		metodo string
		ruta   string
		cuerpo string
		veta   bool
	}{
		{"PUT via api", "PUT", llm, `{"via":"api","provider":"x","api_key":"k","consented":true}`, true},
		{"PUT via local", "PUT", llm, `{"via":"local"}`, false},
		{"GET sin cuerpo", "GET", llm, ``, false},
		{"GET aunque el cuerpo diga api", "GET", llm, `{"via":"api"}`, false},
		{"otra ruta con via api", "PUT", "/api/v1/integrations", `{"via":"api"}`, false},
		{"POST a una subruta", "POST", llm + "/probar", `{"via":"api"}`, true},
		{"PATCH", "PATCH", llm, `{"via":"api"}`, true},
		{"método en minúsculas", "put", llm, `{"via":"api"}`, true},
		{"con query", "PUT", llm + "?x=1", `{"via":"api"}`, true},
		{"con barra final", "PUT", llm + "/", `{"via":"api"}`, true},
		{"barras dobles", "PUT", "/api/v1//tenant-llm", `{"via":"api"}`, true},
		{"con punto punto", "PUT", "/api/v1/x/../tenant-llm", `{"via":"api"}`, true},
		{"con %2D", "PUT", "/api/v1/tenant%2Dllm", `{"via":"api"}`, true},
		{"campo Via en mayúscula", "PUT", llm, `{"Via":"api"}`, true},
		{"valor API en mayúsculas", "PUT", llm, `{"via":"API"}`, true},
		{"valor con espacios", "PUT", llm, `{"via":" api "}`, true},
		{"campo escapado", "PUT", llm, `{"via":"api"}`, true},
		{"via api entre otros campos", "PUT", llm, `{"provider":"anthropic","model":"m","via":"api","consented":true}`, true},
		{"DELETE sin cuerpo", "DELETE", llm, ``, false},
		{"PUT sin cuerpo", "PUT", llm, ``, false},
		{"cuerpo que no es JSON", "PUT", llm, `via=api`, false},
		{"cuerpo que es un arreglo", "PUT", llm, `[{"via":"api"}]`, false},
		{"via que no es cadena", "PUT", llm, `{"via":["api"]}`, false},
		{"via local con otro campo api", "PUT", llm, `{"via":"local","provider":"api"}`, false},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			t.Parallel()
			err := viaAPIProhibida(c.metodo, c.ruta, []byte(c.cuerpo))
			switch {
			case c.veta && !errors.Is(err, errViaAPI):
				t.Errorf("viaAPIProhibida(%s %s %s) = %v, quería el veto errViaAPI", c.metodo, c.ruta, c.cuerpo, err)
			case !c.veta && err != nil:
				t.Errorf("viaAPIProhibida(%s %s %s) = %v, quería nil", c.metodo, c.ruta, c.cuerpo, err)
			}
		})
	}

	t.Run("el cliente corta antes de enviar", func(t *testing.T) { probarClienteCortaAntes(t) })
}

// peticionVista es lo que servidorEco vio de una petición.
type peticionVista struct {
	Metodo, Ruta, Autorizacion, ContentType, Cuerpo string
}

// servidorEco es un servidor HTTP de prueba que apunta cada petición que recibe y contesta según la
// ruta: «/no-existe» 404, «/redirige» 307 a «/destino», «/lenta» no contesta hasta que el cliente se
// va o el test acaba, y cualquier otra 200 con {"ok":true,"n":7}. Lo cierra el Cleanup del test.
type servidorEco struct {
	srv     *httptest.Server
	liberar chan struct{}

	mu     sync.Mutex
	vistas []peticionVista
}

// nuevoServidorEco levanta el servidor de prueba en 127.0.0.1 y registra su cierre en t.Cleanup.
func nuevoServidorEco(t *testing.T) *servidorEco {
	t.Helper()
	e := &servidorEco{liberar: make(chan struct{})}
	e.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		datos, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("servidor de prueba: leyendo el cuerpo: %v", err)
		}
		e.mu.Lock()
		e.vistas = append(e.vistas, peticionVista{r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type"), string(datos)})
		e.mu.Unlock()
		switch r.URL.Path {
		case "/no-existe":
			http.Error(w, `{"error":"no existe"}`, http.StatusNotFound)
		case "/redirige":
			http.Redirect(w, r, "/destino", http.StatusTemporaryRedirect)
		case "/lenta":
			select {
			case <-r.Context().Done():
			case <-e.liberar:
			}
		default:
			w.Header().Set("Content-Type", "application/json")
			if _, err := io.WriteString(w, `{"ok":true,"n":7}`); err != nil {
				t.Errorf("servidor de prueba: escribiendo: %v", err)
			}
		}
	}))
	t.Cleanup(func() {
		close(e.liberar)
		e.srv.Close()
	})
	return e
}

// todas devuelve una copia de las peticiones recibidas hasta ahora, en orden.
func (e *servidorEco) todas() []peticionVista {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.vistas)
}

// ultima devuelve la última petición recibida; falla el test si no ha recibido ninguna.
func (e *servidorEco) ultima(t *testing.T) peticionVista {
	t.Helper()
	todas := e.todas()
	if len(todas) == 0 {
		t.Fatalf("el servidor de prueba no ha recibido ninguna petición")
	}
	return todas[len(todas)-1]
}

// probarClienteCortaAntes prueba, con el camino real del cliente (despachar), que un PUT con via=api
// a tenant-llm no llega al servidor, que el mismo PUT con via=local sí, y que una ruta sin «/»
// inicial tampoco sale.
func probarClienteCortaAntes(t *testing.T) {
	t.Helper()
	eco := nuevoServidorEco(t)
	c := nuevoClienteHTTP(eco.srv.URL, "tok", plazoClienteDefecto)

	_, err := c.despachar(t.Context(), http.MethodPut, prefijoTenantLLM, []byte(`{"via":"api","provider":"x","api_key":"k","consented":true}`))
	if !errors.Is(err, errViaAPI) {
		t.Fatalf("PUT con via=api: error %v, quería errViaAPI", err)
	}
	if strings.Contains(err.Error(), `"k"`) || strings.Contains(err.Error(), "api_key") {
		t.Errorf("el mensaje de veto no debe repetir el cuerpo (puede llevar una clave): %v", err)
	}
	if _, err := c.despachar(t.Context(), http.MethodPut, "api/v1/tenant-llm", []byte(`{"via":"local"}`)); err == nil {
		t.Errorf("una ruta sin «/» inicial debía rechazarse")
	}
	if enviadas := eco.todas(); len(enviadas) != 0 {
		t.Fatalf("el servidor recibió %d petición(es) vetadas, quería 0: %+v", len(enviadas), enviadas)
	}

	r, err := c.despachar(t.Context(), http.MethodPut, prefijoTenantLLM, []byte(`{"via":"local"}`))
	if err != nil || r.Codigo != http.StatusOK {
		t.Fatalf("PUT con via=local: código %d, error %v; quería 200 sin error", r.Codigo, err)
	}
	if enviadas := eco.todas(); len(enviadas) != 1 || enviadas[0].Metodo != "PUT" || enviadas[0].Cuerpo != `{"via":"local"}` {
		t.Errorf("peticiones recibidas = %+v, quería solo el PUT con via=local", enviadas)
	}
}

// TestArnes_Cliente prueba el cliente HTTP contra un servidor de prueba: los cuatro métodos, el
// Bearer solo con token, el cuerpo en JSON con su Content-Type (y nada si es nil), que un 4xx no
// falla, que una redirección se ve y no se sigue, que respuesta.JSON decodifica, y que ConPlazo
// devuelve una copia con otro plazo sin tocar el original. No necesita Docker.
func TestArnes_Cliente(t *testing.T) {
	t.Parallel()
	eco := nuevoServidorEco(t)
	c := nuevoClienteHTTP(eco.srv.URL+"/", "secreto", plazoClienteDefecto) // la barra final sobrante se ignora

	t.Run("métodos, token y cuerpo", func(t *testing.T) { probarClienteMetodos(t, c, eco) })
	t.Run("sin token y sin cuerpo", func(t *testing.T) {
		nuevoClienteHTTP(eco.srv.URL, "", plazoClienteDefecto).Get(t, "/x", nil)
		if v := eco.ultima(t); v.Autorizacion != "" || v.ContentType != "" || v.Cuerpo != "" {
			t.Errorf("GET sin token ni cuerpo trae cabeceras o cuerpo de más: %+v", v)
		}
	})
	t.Run("un 4xx no falla y se ve el cuerpo", func(t *testing.T) {
		r := c.Get(t, "/no-existe", nil)
		if r.Codigo != http.StatusNotFound || !strings.Contains(string(r.Cuerpo), "no existe") {
			t.Errorf("respuesta = %d %q, quería 404 con el cuerpo del servidor", r.Codigo, r.Cuerpo)
		}
	})
	t.Run("una redirección se ve y no se sigue", func(t *testing.T) {
		antes := len(eco.todas())
		r := c.Post(t, "/redirige", map[string]int{"a": 1})
		if r.Codigo != http.StatusTemporaryRedirect {
			t.Errorf("código = %d, quería 307 sin seguir la redirección", r.Codigo)
		}
		if peticiones := len(eco.todas()) - antes; peticiones != 1 {
			t.Errorf("el servidor vio %d peticiones, quería 1 (la redirección no se sigue)", peticiones)
		}
	})
	t.Run("ConPlazo devuelve una copia", func(t *testing.T) { probarClienteConPlazo(t, c) })
}

// probarClienteMetodos hace un GET, un POST, un PUT y un DELETE con cuerpo y comprueba, con lo que
// vio el servidor de prueba, el método, la ruta, el Bearer, el Content-Type y el cuerpo, y que
// respuesta.JSON decodifica la respuesta; y que un json.RawMessage va tal cual.
func probarClienteMetodos(t *testing.T, c *clienteHTTP, eco *servidorEco) {
	t.Helper()
	cuerpo := map[string]any{"a": 1, "b": "dos"}
	const quiereCuerpo = `{"a":1,"b":"dos"}`
	casos := []struct {
		metodo string
		hacer  func(*testing.T, string, any) respuesta
	}{
		{"GET", c.Get}, {"POST", c.Post}, {"PUT", c.Put}, {"DELETE", c.Delete},
	}
	for _, caso := range casos {
		ruta := "/ruta/" + strings.ToLower(caso.metodo)
		r := caso.hacer(t, ruta, cuerpo)
		quiere := peticionVista{caso.metodo, ruta, "Bearer secreto", "application/json", quiereCuerpo}
		if v := eco.ultima(t); v != quiere {
			t.Errorf("%s: el servidor vio %+v, quería %+v", caso.metodo, v, quiere)
		}
		var res struct {
			OK bool `json:"ok"`
			N  int  `json:"n"`
		}
		r.JSON(t, &res)
		if r.Codigo != http.StatusOK || !res.OK || res.N != 7 {
			t.Errorf("%s: respuesta %d %+v, quería 200 {ok:true,n:7}", caso.metodo, r.Codigo, res)
		}
	}
	c.Post(t, "/cruda", json.RawMessage(`{"x": [1, 2]}`))
	if v := eco.ultima(t); v.Cuerpo != `{"x":[1,2]}` {
		t.Errorf("el json.RawMessage llegó como %q", v.Cuerpo)
	}
}

// probarClienteConPlazo comprueba que ConPlazo devuelve otro cliente con la misma base y el mismo
// token y el plazo pedido, sin cambiar el original, y que el plazo corto se hace valer contra una
// respuesta que no llega y no estorba a una que sí.
func probarClienteConPlazo(t *testing.T, c *clienteHTTP) {
	t.Helper()
	corto := c.ConPlazo(150 * time.Millisecond)
	if corto == c || corto.base != c.base || corto.token != c.token {
		t.Fatalf("ConPlazo debe devolver otro cliente con la misma base y el mismo token: %+v", corto)
	}
	if c.cliente.Timeout != plazoClienteDefecto || corto.cliente.Timeout != 150*time.Millisecond {
		t.Errorf("plazos = %s y %s, quería %s y 150ms", c.cliente.Timeout, corto.cliente.Timeout, plazoClienteDefecto)
	}
	if _, err := corto.despachar(t.Context(), http.MethodGet, "/lenta", nil); err == nil {
		t.Errorf("una respuesta que tarda más que el plazo debía fallar")
	}
	if r := corto.Get(t, "/rapida", nil); r.Codigo != http.StatusOK {
		t.Errorf("el cliente con plazo corto no sirve ni para una respuesta inmediata: %d", r.Codigo)
	}
}
