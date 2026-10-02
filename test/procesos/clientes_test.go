//go:build integracion

package procesos

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
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
