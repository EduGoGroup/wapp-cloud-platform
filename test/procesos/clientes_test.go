//go:build integracion

package procesos

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	// plazoClienteDefecto es el plazo de cada petición de un clienteHTTP: lo que tarda, como
	// máximo, en volver una respuesta entera. ConPlazo lo cambia (P5 lo sube por encima de los
	// 10 s del WriteTimeout del servidor).
	plazoClienteDefecto = 15 * time.Second
	// maxCuerpoRespuesta acota cuántos bytes de una respuesta se leen: ninguna respuesta de la
	// nube se acerca a esto, y un servidor desbocado no puede llenar la memoria del test.
	maxCuerpoRespuesta = 16 << 20
	// maxCuerpoEnMensaje es cuántos bytes de un cuerpo se copian a un mensaje de fallo.
	maxCuerpoEnMensaje = 600

	// prefijoTenantLLM es el prefijo de las rutas que guardan la configuración LLM del tenant:
	// la única puerta por la que un test podría pedir la vía `api`, que llamaría a un proveedor
	// real (internal/llmvia/llmvia.go no admite BaseURL, así que no hay forma de redirigirla a
	// un doble).
	prefijoTenantLLM = "/api/v1/tenant-llm"
	// viaAPI es el valor del campo `via` que el cliente se niega a enviar.
	viaAPI = "api"

	// rutaCanje es el canje de un Identity Token por un Context Token (listener público).
	rutaCanje = "/api/v1/auth/exchange"
	// rutaTenants es el alta y el listado de empresas del plano de plataforma (listener admin).
	rutaTenants = "/admin/tenants"
	// planTenantPorDefecto es el plan con el que crearTenant da de alta una empresa: sembrado por
	// las migraciones 0039/0074, trae menu, cart_basic, intakes_export, catalog_import, crm_bridge,
	// llm_intent, survey, media y llm_intake, y NO trae api_llm. Es decir, abre todas las
	// capacidades que los procesos ejercitan y deja cerrada la vía API del LLM (gasto real).
	planTenantPorDefecto = "advisor_ai_local"
)

// errViaAPI marca el veto del cliente a una petición que pediría la vía LLM `api`.
var errViaAPI = errors.New("la vía LLM api llamaría al proveedor real y el arnés no gasta")

// respuesta es lo que devolvió el servidor: el código HTTP y el cuerpo entero (leído hasta el
// final). Un 4xx o un 5xx NO es un error del cliente: el test decide si lo esperaba.
type respuesta struct {
	Codigo int
	Cuerpo []byte
}

// JSON decodifica el cuerpo de la respuesta en destino (un puntero, como en json.Unmarshal). Falla
// el test (t.Fatalf) si el cuerpo no es JSON o no encaja en destino, y vuelca el código y el
// principio del cuerpo para que se vea qué contestó el servidor.
func (r respuesta) JSON(t *testing.T, destino any) {
	t.Helper()
	if err := json.Unmarshal(r.Cuerpo, destino); err != nil {
		t.Fatalf("la respuesta HTTP %d no es el JSON esperado (%T): %v\ncuerpo: %s", r.Codigo, destino, err, recortar(r.Cuerpo))
	}
}

// recortar devuelve el principio de un cuerpo (hasta 600 bytes) para un mensaje de fallo.
func recortar(cuerpo []byte) string {
	if len(cuerpo) <= maxCuerpoEnMensaje {
		return string(cuerpo)
	}
	return string(cuerpo[:maxCuerpoEnMensaje]) + "…"
}

// clienteHTTP es un cliente HTTP de un listener de un servidor, con el token Bearer ya puesto.
// No sigue redirecciones (el test ve el 3xx tal cual), no reutiliza conexiones y no usa proxy.
// Lo crea servidor.Publica o servidor.Admin; es seguro entre goroutines.
type clienteHTTP struct {
	base    string // "http://127.0.0.1:<puerto>", sin barra final
	token   string // "" = sin cabecera Authorization
	cliente *http.Client
}

// nuevoClienteHTTP recibe la URL base, el token ("" = sin Authorization) y el plazo por petición, y
// devuelve el cliente. Una petición que pase del plazo falla; la conexión se abre en cada petición
// (sin keep-alive) para que parar o reiniciar el servidor no deje conexiones a medias.
func nuevoClienteHTTP(base, token string, plazo time.Duration) *clienteHTTP {
	return &clienteHTTP{
		base:  strings.TrimRight(base, "/"),
		token: token,
		cliente: &http.Client{
			Timeout:   plazo,
			Transport: &http.Transport{DisableKeepAlives: true},
			// Una redirección escondida podría llevar la petición a otra ruta sin pasar por el veto
			// de la vía api, y además el test quiere ver el 3xx, no lo que hay detrás.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// Publica devuelve el cliente de la API pública (el :8103 de producción, s.PublicaAddr), con
// Authorization: Bearer <token> en cada petición, o sin cabecera si token es "". Las rutas son las
// de /api/v1/…. No falla.
func (s *servidor) Publica(token string) *clienteHTTP {
	return nuevoClienteHTTP("http://"+s.PublicaAddr, token, plazoClienteDefecto)
}

// Admin devuelve el cliente del HTTP admin (el :8100 de producción, s.AdminAddr), con
// Authorization: Bearer <token> en cada petición, o sin cabecera si token es "". Las rutas son las
// de /admin/…, /healthz y /metrics. No falla.
func (s *servidor) Admin(token string) *clienteHTTP {
	return nuevoClienteHTTP("http://"+s.AdminAddr, token, plazoClienteDefecto)
}

// ConPlazo devuelve una copia del cliente (misma base, mismo token) con otro plazo por petición.
// El cliente original no cambia. Sirve para las rutas que tardan más que el WriteTimeout global
// del servidor (10 s), como quote-suggestion de P5. No falla.
func (c *clienteHTTP) ConPlazo(plazo time.Duration) *clienteHTTP {
	return nuevoClienteHTTP(c.base, c.token, plazo)
}

// Get hace GET ruta y devuelve la respuesta. ruta empieza por «/»; cuerpo es nil o cualquier valor
// que json.Marshal acepte (un json.RawMessage va tal cual). Falla el test (t.Fatalf) si la
// petición no se puede construir, no llega al servidor, pasa del plazo o el cliente la veta (ver
// viaAPIProhibida); un 4xx o 5xx NO falla.
func (c *clienteHTTP) Get(t *testing.T, ruta string, cuerpo any) respuesta {
	t.Helper()
	return c.hacer(t, http.MethodGet, ruta, cuerpo)
}

// Post hace POST ruta con cuerpo en JSON (nil = sin cuerpo). Mismas reglas y mismos fallos que Get.
func (c *clienteHTTP) Post(t *testing.T, ruta string, cuerpo any) respuesta {
	t.Helper()
	return c.hacer(t, http.MethodPost, ruta, cuerpo)
}

// Put hace PUT ruta con cuerpo en JSON (nil = sin cuerpo). Mismas reglas y mismos fallos que Get.
func (c *clienteHTTP) Put(t *testing.T, ruta string, cuerpo any) respuesta {
	t.Helper()
	return c.hacer(t, http.MethodPut, ruta, cuerpo)
}

// Delete hace DELETE ruta con cuerpo en JSON (nil = sin cuerpo). Mismas reglas y mismos fallos que
// Get.
func (c *clienteHTTP) Delete(t *testing.T, ruta string, cuerpo any) respuesta {
	t.Helper()
	return c.hacer(t, http.MethodDelete, ruta, cuerpo)
}

// hacer recibe el método, la ruta y el cuerpo (nil, o lo que acepte json.Marshal), lo pasa a JSON y
// lo manda con despachar. Devuelve la respuesta. Falla el test (t.Fatalf) en cualquier error del
// cliente: cuerpo que no es JSON, petición vetada, error de transporte, plazo vencido.
func (c *clienteHTTP) hacer(t *testing.T, metodo, ruta string, cuerpo any) respuesta {
	t.Helper()
	var datos []byte
	if cuerpo != nil {
		var err error
		if datos, err = json.Marshal(cuerpo); err != nil {
			t.Fatalf("%s %s: el cuerpo no se puede pasar a JSON: %v", metodo, ruta, err)
		}
	}
	r, err := c.despachar(t.Context(), metodo, ruta, datos)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// despachar es el camino único de toda petición: PRIMERO la comprobación (comprobar), sin tocar la
// red, y solo si pasa, la petición. Recibe el contexto, el método, la ruta ("/…") y el cuerpo ya en
// JSON (nil = sin cuerpo, y sin Content-Type). Pone Authorization: Bearer solo si el cliente tiene
// token. Devuelve la respuesta, o un error si la petición está vetada, no se puede construir, el
// transporte falla, se pasa el plazo o no se puede leer el cuerpo. Un 4xx o 5xx no es error.
func (c *clienteHTTP) despachar(ctx context.Context, metodo, ruta string, datos []byte) (respuesta, error) {
	if err := c.comprobar(metodo, ruta, datos); err != nil {
		return respuesta{}, fmt.Errorf("%s %s no se envía: %w", metodo, ruta, err)
	}
	var cuerpo io.Reader
	if datos != nil {
		cuerpo = bytes.NewReader(datos)
	}
	req, err := http.NewRequestWithContext(ctx, metodo, c.base+ruta, cuerpo)
	if err != nil {
		return respuesta{}, fmt.Errorf("construir %s %s: %w", metodo, ruta, err)
	}
	if datos != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.cliente.Do(req)
	if err != nil {
		return respuesta{}, fmt.Errorf("%s %s: %w", metodo, ruta, err)
	}
	leido, errLectura := io.ReadAll(io.LimitReader(resp.Body, maxCuerpoRespuesta))
	if errCierre := resp.Body.Close(); errCierre != nil && errLectura == nil {
		errLectura = errCierre
	}
	if errLectura != nil {
		return respuesta{}, fmt.Errorf("leer la respuesta de %s %s: %w", metodo, ruta, errLectura)
	}
	return respuesta{Codigo: resp.StatusCode, Cuerpo: leido}, nil
}

// comprobar decide, antes de enviar nada, si la petición puede salir: la ruta debe empezar por «/»
// y no puede pedir la vía LLM api (viaAPIProhibida). Recibe el método, la ruta y el cuerpo ya en
// JSON. Devuelve nil si puede salir, o el motivo (que envuelve errViaAPI en el segundo caso).
func (c *clienteHTTP) comprobar(metodo, ruta string, cuerpo []byte) error {
	if !strings.HasPrefix(ruta, "/") {
		return fmt.Errorf("la ruta %q debe empezar por «/»", ruta)
	}
	return viaAPIProhibida(metodo, ruta, cuerpo)
}

// viaAPIProhibida es el candado de «cero gasto». Recibe el método HTTP, la ruta (con o sin query)
// y el cuerpo en JSON, y devuelve un error que envuelve errViaAPI si la petición no es un GET, la
// ruta empieza por /api/v1/tenant-llm y el cuerpo es un objeto JSON cuyo campo `via` vale «api».
// Devuelve nil en cualquier otro caso. Es deliberadamente más estricta que el servidor: la ruta se
// normaliza (query, %xx y «..» incluidos), el nombre del campo y su valor se comparan sin
// distinguir mayúsculas (encoding/json lo hace al decodificar) y el valor sin espacios. Un cuerpo
// que no es un objeto JSON no se veta: el servidor lo rechaza con 400 antes de tocar a ningún
// proveedor. Es pura: no toca la red ni el estado.
func viaAPIProhibida(metodo, ruta string, cuerpo []byte) error {
	if m := strings.ToUpper(metodo); m == "" || m == http.MethodGet {
		return nil
	}
	if !strings.HasPrefix(rutaNormalizada(ruta), prefijoTenantLLM) {
		return nil
	}
	if declaraViaAPI(cuerpo) {
		return fmt.Errorf("%s %s lleva via=%q: %w", metodo, ruta, viaAPI, errViaAPI)
	}
	return nil
}

// rutaNormalizada recibe una ruta HTTP y devuelve la que vería el enrutador del servidor: sin
// query ni fragmento, con los %xx decodificados y sin «//», «.» ni «..». Siempre empieza por «/».
func rutaNormalizada(ruta string) string {
	limpia := ruta
	if i := strings.IndexAny(limpia, "?#"); i >= 0 {
		limpia = limpia[:i]
	}
	if decodificada, err := url.PathUnescape(limpia); err == nil {
		limpia = decodificada
	}
	return path.Clean("/" + limpia)
}

// declaraViaAPI dice si el cuerpo es un objeto JSON con algún campo `via` (sin distinguir
// mayúsculas) cuyo valor, una cadena, es «api» (sin distinguir mayúsculas ni espacios en los
// extremos). Un cuerpo que no es un objeto JSON, o un `via` que no es una cadena, dan false.
func declaraViaAPI(cuerpo []byte) bool {
	var campos map[string]json.RawMessage
	if err := json.Unmarshal(cuerpo, &campos); err != nil {
		return false // no es un objeto JSON: el servidor lo rechaza con 400
	}
	for nombre, crudo := range campos {
		if !strings.EqualFold(nombre, "via") {
			continue
		}
		var valor string
		if err := json.Unmarshal(crudo, &valor); err == nil && strings.EqualFold(strings.TrimSpace(valor), viaAPI) {
			return true
		}
	}
	return false
}

// resultadoCanje es la respuesta 200 de POST /api/v1/auth/exchange.
type resultadoCanje struct {
	ContextToken string `json:"context_token"`
	TokenType    string `json:"token_type"`
	ExpiresAt    string `json:"expires_at"`
	Context      struct {
		TenantID string   `json:"tenant_id"`
		UserID   string   `json:"user_id"`
		Roles    []string `json:"roles"`
	} `json:"context"`
}

// canje recibe el test, el servidor y un Identity Token (el que sea, bueno o malo) y hace
// POST /api/v1/auth/exchange {"identity_token"} en el listener público, SIN Authorization.
// Devuelve la respuesta cruda, sea cual sea su código: 200 con el Context Token, 400 si falta el
// token, 401 si el token no es aceptable (caducado, de otro emisor, de un system desconocido,
// basura) o 503 si el servidor no tiene identity configurado. Es la entrada de los casos negativos;
// canjear la usa para el positivo. Solo falla (t.Fatalf) si la petición no llega al servidor.
func canje(t *testing.T, s *servidor, identityToken string) respuesta {
	t.Helper()
	return s.Publica("").Post(t, rutaCanje, map[string]string{"identity_token": identityToken})
}

// canjear recibe el test, el servidor y un Identity Token, lo canjea con canje y devuelve el
// context_token (el Bearer que aceptan el :8103 y el :8100). Falla (t.Fatalf) si la respuesta no
// es un 200 con un context_token no vacío; el mensaje trae el código y el cuerpo.
func canjear(t *testing.T, s *servidor, identityToken string) string {
	t.Helper()
	r := canje(t, s, identityToken)
	if r.Codigo != http.StatusOK {
		t.Fatalf("canje: HTTP %d, quería 200\ncuerpo: %s", r.Codigo, recortar(r.Cuerpo))
	}
	var res resultadoCanje
	r.JSON(t, &res)
	if res.ContextToken == "" {
		t.Fatalf("canje: HTTP 200 sin context_token\ncuerpo: %s", recortar(r.Cuerpo))
	}
	return res.ContextToken
}

// crearTenant recibe el test, el servidor, el Context Token de un staff de plataforma (ver
// altaStaffPlataforma) y el slug de la empresa, y la da de alta con POST /admin/tenants con el plan
// planTenantPorDefecto (advisor_ai_local). Devuelve el id (UUID) de la empresa. Falla (t.Fatalf)
// si la respuesta no es un 201 con id, p. ej. 401/403 sin staff o 409 si el slug ya existe.
func crearTenant(t *testing.T, s *servidor, tokenStaff, slug string) string {
	t.Helper()
	return crearTenantConPlan(t, s, tokenStaff, slug, planTenantPorDefecto)
}

// crearTenantConPlan es crearTenant con el plan a elección (cualquiera de los sembrados: basic,
// pro, commerce, advisor_ai, advisor_ai_pro, advisor_ai_local). Mismo resultado y mismos fallos;
// un plan que no existe lo rechaza la clave foránea y el servidor contesta 500.
func crearTenantConPlan(t *testing.T, s *servidor, tokenStaff, slug, plan string) string {
	t.Helper()
	r := s.Admin(tokenStaff).Post(t, rutaTenants, map[string]string{
		"slug":         slug,
		"display_name": "Procesos " + slug,
		"plan_id":      plan,
	})
	if r.Codigo != http.StatusCreated {
		t.Fatalf("crearTenant(%q, plan %q): HTTP %d, quería 201\ncuerpo: %s", slug, plan, r.Codigo, recortar(r.Cuerpo))
	}
	var creado struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
	}
	r.JSON(t, &creado)
	if !identidadUUID.MatchString(creado.ID) || creado.Slug != slug {
		t.Fatalf("crearTenant(%q): respuesta inesperada (id %q, slug %q)\ncuerpo: %s", slug, creado.ID, creado.Slug, recortar(r.Cuerpo))
	}
	return creado.ID
}

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

// TestArnes_Canje prueba el canje de identidad contra un servidor real: un usuario sin membresías
// canjea un Identity Token válido y recibe un Context Token SIN empresa (el estado «en espera»), y
// los tokens malos —caducado, de otro emisor, de un system que wApp no acepta, basura, cuerpo
// vacío— se rechazan con el código real del servidor (401 / 400). Necesita Docker.
func TestArnes_Canje(t *testing.T) {
	t.Parallel()
	s := arrancar(t, opcionesServidor{Proceso: "clientes_canje"})
	usuario := uuidAleatorio(t)

	t.Run("un usuario sin membresías canjea y queda sin empresa", func(t *testing.T) { probarCanjeValido(t, s, usuario) })
	t.Run("el context token se verifica", func(t *testing.T) {
		token := canjear(t, s, s.Identidad.TokenDe(usuario, "wapp.bff"))
		r := s.Publica("").Post(t, "/api/v1/auth/verify", map[string]string{"token": token})
		var v struct {
			Valid   bool   `json:"valid"`
			Subject string `json:"subject"`
		}
		r.JSON(t, &v)
		if r.Codigo != http.StatusOK || !v.Valid || v.Subject != usuario {
			t.Errorf("verify del context token: HTTP %d %+v, quería 200 válido con subject %s", r.Codigo, v, usuario)
		}
	})
	t.Run("rechaza los tokens malos", func(t *testing.T) { probarCanjeRechazos(t, s, usuario) })
	t.Run("solo POST", func(t *testing.T) {
		if r := s.Publica("").Get(t, rutaCanje, nil); r.Codigo != http.StatusMethodNotAllowed {
			t.Errorf("GET %s: HTTP %d, quería 405", rutaCanje, r.Codigo)
		}
	})
}

// probarCanjeValido canjea un Identity Token de cada system que wApp acepta y comprueba el 200, la
// forma de la respuesta y el contexto: el usuario, sin empresa y sin roles.
func probarCanjeValido(t *testing.T, s *servidor, usuario string) {
	t.Helper()
	for _, system := range []string{"wapp.bff", "wapp.edge", "wapp.platform"} {
		r := canje(t, s, s.Identidad.TokenDe(usuario, system))
		if r.Codigo != http.StatusOK {
			t.Fatalf("canje de un token de %s: HTTP %d, quería 200\ncuerpo: %s", system, r.Codigo, recortar(r.Cuerpo))
		}
		var res resultadoCanje
		r.JSON(t, &res)
		caduca, err := time.Parse(time.RFC3339, res.ExpiresAt)
		if res.ContextToken == "" || res.TokenType != "Bearer" || err != nil || !caduca.After(time.Now()) {
			t.Errorf("canje de %s: respuesta incompleta (token vacío=%v, token_type=%q, expires_at=%q, err=%v)",
				system, res.ContextToken == "", res.TokenType, res.ExpiresAt, err)
		}
		if res.Context.UserID != usuario || res.Context.TenantID != "" || len(res.Context.Roles) != 0 {
			t.Errorf("canje de %s: contexto %+v, quería user_id=%s sin empresa ni roles", system, res.Context, usuario)
		}
	}
}

// probarCanjeRechazos canjea un token caducado, uno de otro emisor, uno de un system que wApp no
// acepta, una basura y un token vacío, y comprueba el código de cada rechazo y que ninguno trae un
// context_token.
func probarCanjeRechazos(t *testing.T, s *servidor, usuario string) {
	t.Helper()
	rechazos := []struct {
		nombre string
		token  string
		codigo int
	}{
		{"caducado", s.Identidad.TokenCaducado(usuario, "wapp.bff"), http.StatusUnauthorized},
		{"de otro emisor", s.Identidad.TokenDeOtroEmisor(usuario, "wapp.bff"), http.StatusUnauthorized},
		{"de un system equivocado", s.Identidad.TokenDe(usuario, "edugo.kmp"), http.StatusUnauthorized},
		{"que es basura", "esto-no-es-un-jwt", http.StatusUnauthorized},
		{"vacío", "", http.StatusBadRequest},
	}
	for _, c := range rechazos {
		r := canje(t, s, c.token)
		if r.Codigo != c.codigo {
			t.Errorf("canje de un token %s: HTTP %d, quería %d\ncuerpo: %s", c.nombre, r.Codigo, c.codigo, recortar(r.Cuerpo))
		}
		if strings.Contains(string(r.Cuerpo), "context_token") {
			t.Errorf("el rechazo de un token %s no debe traer un context_token: %s", c.nombre, recortar(r.Cuerpo))
		}
	}
}

// TestArnes_StaffYTenant prueba el alta de un staff de plataforma y de una empresa por la puerta
// HTTP: sin alta, el canje funciona pero /admin/tenants responde 403; con altaStaffPlataforma, el
// Context Token lleva la empresa de plataforma y el rol platform_admin y crearTenant da de alta
// la empresa (comprobada por SQL, con su plan), un slug repetido es 409 y el staff la lee. Necesita
// Docker.
func TestArnes_StaffYTenant(t *testing.T) {
	t.Parallel()
	s := arrancar(t, opcionesServidor{Proceso: "clientes_staff"})
	db := s.Base.Abrir(t)
	staff, sinAlta := uuidAleatorio(t), uuidAleatorio(t)

	t.Run("sin alta de staff: 401 sin token y 403 con un token sin empresa", func(t *testing.T) { probarSinAltaStaff(t, s, db, sinAlta) })

	altaStaffPlataforma(t, db, staff)
	r := canje(t, s, s.Identidad.TokenDe(staff, "wapp.bff"))
	var res resultadoCanje
	r.JSON(t, &res)
	if r.Codigo != http.StatusOK || res.Context.TenantID != tenantPlataformaID || !slices.Contains(res.Context.Roles, "platform_admin") {
		t.Fatalf("canje del staff: HTTP %d contexto %+v, quería 200 en %s con el rol platform_admin", r.Codigo, res.Context, tenantPlataformaID)
	}

	t.Run("con alta de staff: crearTenant da de alta la empresa", func(t *testing.T) {
		probarCrearTenant(t, s, db, res.ContextToken, canjear(t, s, s.Identidad.TokenDe(sinAlta, "wapp.bff")))
	})
}

// probarSinAltaStaff comprueba que sin token /admin/tenants da 401, que con el Context Token de un
// usuario sin alta de staff (canje correcto, sin empresa) da 403, y que el 403 no crea nada.
func probarSinAltaStaff(t *testing.T, s *servidor, db *sql.DB, sinAlta string) {
	t.Helper()
	cuerpo := map[string]string{"slug": "no-debe-existir", "display_name": "No debe existir"}
	if r := s.Admin("").Post(t, rutaTenants, cuerpo); r.Codigo != http.StatusUnauthorized {
		t.Errorf("POST %s sin token: HTTP %d, quería 401", rutaTenants, r.Codigo)
	}
	token := canjear(t, s, s.Identidad.TokenDe(sinAlta, "wapp.bff"))
	if r := s.Admin(token).Post(t, rutaTenants, cuerpo); r.Codigo != http.StatusForbidden {
		t.Errorf("POST %s sin alta de staff: HTTP %d, quería 403\ncuerpo: %s", rutaTenants, r.Codigo, recortar(r.Cuerpo))
	}
	if hay := consultaEntero(t, db, `SELECT count(*) FROM public.tenants WHERE slug = 'no-debe-existir'`); hay != 0 {
		t.Errorf("el 403 dejó %d empresa(s) creadas", hay)
	}
}

// probarCrearTenant da de alta dos empresas con el token del staff (una con el plan por defecto, otra
// con basic), comprueba por SQL su slug y su plan, que un slug repetido es 409 y que el staff lee la
// empresa (200) mientras quien no es staff (tokenAjeno) recibe 403.
func probarCrearTenant(t *testing.T, s *servidor, db *sql.DB, tokenStaff, tokenAjeno string) {
	t.Helper()
	plan := func(id string) (slug, plan string) {
		if err := db.QueryRowContext(t.Context(), `SELECT slug, plan_id FROM public.tenants WHERE id = $1::uuid`, id).Scan(&slug, &plan); err != nil {
			t.Fatalf("la empresa %s no está en tenants: %v", id, err)
		}
		return slug, plan
	}
	id := crearTenant(t, s, tokenStaff, "cliente-uno")
	if slug, p := plan(id); slug != "cliente-uno" || p != planTenantPorDefecto {
		t.Errorf("empresa %s = slug %q plan %q, quería cliente-uno y %s", id, slug, p, planTenantPorDefecto)
	}
	otra := crearTenantConPlan(t, s, tokenStaff, "cliente-dos", "basic")
	if slug, p := plan(otra); otra == id || slug != "cliente-dos" || p != "basic" {
		t.Errorf("segunda empresa %s = slug %q plan %q, quería otro id, cliente-dos y basic", otra, slug, p)
	}

	if dup := s.Admin(tokenStaff).Post(t, rutaTenants, map[string]string{"slug": "cliente-uno", "display_name": "Otra"}); dup.Codigo != http.StatusConflict {
		t.Errorf("slug repetido: HTTP %d, quería 409", dup.Codigo)
	}
	if lectura := s.Admin(tokenStaff).Get(t, rutaTenants+"/"+id, nil); lectura.Codigo != http.StatusOK || !strings.Contains(string(lectura.Cuerpo), "cliente-uno") {
		t.Errorf("GET %s/%s: HTTP %d %s, quería 200 con la empresa", rutaTenants, id, lectura.Codigo, recortar(lectura.Cuerpo))
	}
	if lectura := s.Admin(tokenAjeno).Get(t, rutaTenants+"/"+id, nil); lectura.Codigo != http.StatusForbidden {
		t.Errorf("GET %s/%s sin staff: HTTP %d, quería 403", rutaTenants, id, lectura.Codigo)
	}
}
