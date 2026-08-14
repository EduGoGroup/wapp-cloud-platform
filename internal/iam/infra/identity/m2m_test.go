// m2m_test.go cubre el cliente M2M contra un servidor que imita el contrato REAL
// de identity-api: sus rutas, sus nombres de campo (`api_key`, `service_token`,
// `expires_in`, `created`) y sus códigos.
//
// ⚠️ NUNCA SE HAN EJECUTADO: se escribieron en un entorno sin toolchain de Go
// (sin compilador, sin `go test`, sin `gofmt`). Son la primera cosa que hay que
// correr al recoger este trabajo.

package iamidentity_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	iamidentity "github.com/EduGoGroup/wapp-cloud-platform/internal/iam/infra/identity"
)

const (
	m2mAPIKey = "ak_una-credencial-de-mentira" //nolint:gosec // credencial de mentira de un test
	m2mEmail  = "nueva@tenant.example"
	m2mUserID = "11111111-2222-3333-4444-555555555555"
)

// fakeM2M levanta un identity de mentira con las cuatro rutas que este cliente
// toca. Cuenta los canjes para poder afirmar sobre la CACHÉ, que es la mitad de
// lo que T2.4 pide.
type fakeM2M struct {
	srv *httptest.Server

	mu        sync.Mutex
	exchanges int
	// calls y bearers registran SOLO las rutas de negocio: el canje se cuenta en
	// exchanges y se inspecciona por lastTokenBody, así que las aserciones de
	// "cuántas llamadas hizo el cliente" no se contaminan con los canjes.
	calls   []string
	bearers []string
	// lastTokenBody es el cuerpo del CANJE, aparte del de negocio: es donde viaja
	// `api_key` y es lo más fácil de equivocar del contrato (identity ADR-0025).
	lastTokenBody map[string]any
	// lastTokenAuth es la cabecera Authorization del canje. Debe estar VACÍA
	// siempre: la key se canjea, no se presenta.
	lastTokenAuth string
	lastBody      map[string]any
	lastRawIn     string
	expiresIn     int
	// tokenStatus y tokenCode gobiernan la respuesta del CANJE, independientes de
	// status/code, que gobiernan la ruta de negocio.
	tokenStatus int
	tokenCode   string
	status      int
	code        string
	detailsRaw  string
	body        string
}

func newFakeM2M(t *testing.T) *fakeM2M {
	t.Helper()
	f := &fakeM2M{expiresIn: 900, status: http.StatusOK, tokenStatus: http.StatusOK, body: "{}"}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()

		var raw []byte
		var payload map[string]any
		if r.Body != nil {
			dec := json.NewDecoder(r.Body)
			if err := dec.Decode(&payload); err == nil {
				if encoded, merr := json.Marshal(payload); merr == nil {
					raw = encoded
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")

		if r.URL.Path == "/api/v1/auth/token" {
			f.exchanges++
			f.lastTokenBody = payload
			f.lastTokenAuth = r.Header.Get("Authorization")
			// tokenStatus se mira ANTES de contestar: si no, el canje no puede
			// fallar nunca y mapExchangeError se queda sin una sola línea de test.
			if f.tokenStatus >= http.StatusBadRequest {
				w.WriteHeader(f.tokenStatus)
				writeCuerpo(t, w, fmt.Sprintf(`{"error":"x","code":"%s"}`, f.tokenCode))
				return
			}
			writeCuerpo(t, w, fmt.Sprintf(`{"status":"ok","service_token":"svc-token-%d","expires_in":%d}`, f.exchanges, f.expiresIn))
			return
		}

		f.lastBody, f.lastRawIn = payload, string(raw)

		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		f.bearers = append(f.bearers, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))

		if f.status >= http.StatusBadRequest {
			w.WriteHeader(f.status)
			details := ""
			if f.detailsRaw != "" {
				details = `,"details":` + f.detailsRaw
			}
			writeCuerpo(t, w, fmt.Sprintf(`{"error":"x","code":"%s"%s}`, f.code, details))
			return
		}
		w.WriteHeader(f.status)
		writeCuerpo(t, w, f.body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// writeCuerpo escribe el cuerpo de prueba y falla el test si no se pudo escribir
// (errcheck está activo también sobre los tests, .golangci.yml).
func writeCuerpo(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()
	if _, err := w.Write([]byte(body)); err != nil {
		t.Errorf("escribiendo la respuesta de prueba: %v", err)
	}
}

func (f *fakeM2M) client(t *testing.T) *iamidentity.M2MClient {
	t.Helper()
	c, err := iamidentity.NewM2M(f.srv.URL, m2mAPIKey, 2*time.Second)
	if err != nil {
		t.Fatalf("NewM2M: %v", err)
	}
	return c
}

func (f *fakeM2M) snapshot() (exchanges int, calls, bearers []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.exchanges, append([]string(nil), f.calls...), append([]string(nil), f.bearers...)
}

// tokenSnapshot devuelve lo que llegó en el ÚLTIMO canje: su cuerpo y su
// cabecera Authorization.
func (f *fakeM2M) tokenSnapshot() (body map[string]any, auth string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastTokenBody, f.lastTokenAuth
}

func TestNewM2M_ExigeURLYCredencial(t *testing.T) {
	t.Parallel()
	if _, err := iamidentity.NewM2M("", m2mAPIKey, time.Second); err == nil {
		t.Error("una URL vacía debería impedir construir el cliente")
	}
	// El criterio de T2.4 es explícito: sin WAPP_IDENTITY_API_KEY el constructor
	// falla al arrancar, no devuelve un nil silencioso.
	if _, err := iamidentity.NewM2M("http://localhost:8200", "  ", time.Second); err == nil {
		t.Error("una API key vacía debería impedir construir el cliente")
	}
}

func TestM2M_CanjeaUnaVezYReutilizaElServiceToken(t *testing.T) {
	t.Parallel()
	f := newFakeM2M(t)
	f.status, f.body = http.StatusCreated, `{"id":"`+m2mUserID+`","email":"`+m2mEmail+`","created":true}`

	c := f.client(t)
	user, err := c.EnsureUser(context.Background(), m2mEmail, "Ana", "Pérez")
	if err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}
	if user.ID != m2mUserID || user.Email != m2mEmail || !user.Created {
		t.Errorf("usuario = %+v", user)
	}
	if _, err := c.EnsureUser(context.Background(), m2mEmail, "Ana", "Pérez"); err != nil {
		t.Fatalf("segunda EnsureUser: %v", err)
	}

	exchanges, calls, bearers := f.snapshot()
	if exchanges != 1 {
		t.Errorf("canjes = %d, want 1 (el token se cachea hasta expires_in)", exchanges)
	}
	if len(calls) != 2 || calls[0] != "POST /api/v1/users/ensure" {
		t.Errorf("llamadas = %v", calls)
	}
	for _, b := range bearers {
		if b != "svc-token-1" {
			t.Errorf("portador = %q, want svc-token-1", b)
		}
	}
	// La key se canjea; NO se presenta como portador (identity ADR-0025).
	if strings.Contains(strings.Join(bearers, "|"), m2mAPIKey) {
		t.Error("la API key no debe viajar nunca en Authorization")
	}
}

func TestM2M_UnTokenRECHAZADOSeRecanjeaUnaVezYSeReintentaUnaVez(t *testing.T) {
	t.Parallel()
	f := newFakeM2M(t)
	// identity rechaza SIEMPRE con 401: el cliente debe recanjear UNA vez y
	// reintentar UNA vez, no entrar en bucle.
	f.status, f.code = http.StatusUnauthorized, "UNAUTHORIZED"

	_, err := f.client(t).EnsureUser(context.Background(), m2mEmail, "Ana", "Pérez")
	if !errors.Is(err, domain.ErrMachineCredentialInvalid) {
		t.Fatalf("err = %v, want ErrMachineCredentialInvalid", err)
	}
	exchanges, calls, bearers := f.snapshot()
	if exchanges != 2 {
		t.Errorf("canjes = %d, want 2 (uno inicial y uno tras el 401)", exchanges)
	}
	if len(calls) != 2 {
		t.Errorf("intentos = %d, want 2 (uno y un reintento, no un bucle)", len(calls))
	}
	if len(bearers) != 2 {
		t.Fatalf("portadores = %v, want 2 (el intento y el reintento)", bearers)
	}
	if bearers[1] != "svc-token-2" {
		t.Errorf("el reintento usó %q, want el token recién canjeado", bearers[1])
	}
}

func TestM2M_ReplaceUserSystems_DeclaraElConjuntoYLeeElDiff(t *testing.T) {
	t.Parallel()
	f := newFakeM2M(t)
	f.body = `{"systems":["wapp.bff"],"granted":["wapp.bff"],"revoked":["wapp.edge"]}`

	diff, err := f.client(t).ReplaceUserSystems(context.Background(), m2mUserID, []string{"wapp.bff"})
	if err != nil {
		t.Fatalf("ReplaceUserSystems: %v", err)
	}
	_, calls, _ := f.snapshot()
	if len(calls) != 1 || calls[0] != "PUT /api/v1/users/"+m2mUserID+"/systems" {
		t.Errorf("llamadas = %v", calls)
	}
	if len(diff.Granted) != 1 || len(diff.Revoked) != 1 {
		t.Errorf("diff = %+v", diff)
	}
	// El identificador va en la RUTA y NUNCA en el cuerpo.
	f.mu.Lock()
	_, present := f.lastBody["user_id"]
	f.mu.Unlock()
	if present {
		t.Error("el user_id no debe viajar en el cuerpo")
	}
}

func TestM2M_ReplaceUserSystems_UnConjuntoNilViajaComoVacio(t *testing.T) {
	t.Parallel()
	f := newFakeM2M(t)
	f.body = `{"systems":[],"granted":[],"revoked":["wapp.bff"]}`

	if _, err := f.client(t).ReplaceUserSystems(context.Background(), m2mUserID, nil); err != nil {
		t.Fatalf("ReplaceUserSystems: %v", err)
	}
	f.mu.Lock()
	raw := f.lastRawIn
	f.mu.Unlock()
	if !strings.Contains(raw, `"systems":[]`) {
		t.Errorf("cuerpo = %s, want systems como arreglo vacío explícito", raw)
	}
}

func TestM2M_ReplaceUserSystems_TraduceLosCodigos(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status int
		code   string
		want   error
	}{
		// La frontera de ecosistema: es ATÓMICA, no se escribió nada.
		{name: "aplicación de otro ecosistema", status: http.StatusForbidden, code: "SYSTEM_ACCESS_DENIED", want: domain.ErrSystemNotAllowed},
		// El OTRO 403: a la credencial de wApp le falta el scope. Lo arregla
		// quien la configuró, no quien pidió el conjunto.
		{name: "scope insuficiente", status: http.StatusForbidden, code: "FORBIDDEN", want: domain.ErrMachineCredentialInvalid},
		{name: "usuario inexistente", status: http.StatusNotFound, code: "NOT_FOUND", want: domain.ErrNotFound},
		{name: "conjunto inadmisible", status: http.StatusBadRequest, code: "INVALID_REQUEST", want: domain.ErrInvalidInput},
		{name: "identity indispuesto", status: http.StatusServiceUnavailable, code: "SERVICE_UNAVAILABLE", want: domain.ErrIdentityUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFakeM2M(t)
			f.status, f.code = tt.status, tt.code
			_, err := f.client(t).ReplaceUserSystems(context.Background(), m2mUserID, []string{"edugo.kmp"})
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestM2M_Signup_NoPresentaElServiceToken(t *testing.T) {
	t.Parallel()
	f := newFakeM2M(t)
	f.status, f.body = http.StatusCreated, `{"id":"`+m2mUserID+`"}`

	id, err := f.client(t).Signup(context.Background(), m2mEmail, "una-frase-de-acceso-larga", "Ana", "Pérez")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}
	if id != m2mUserID {
		t.Errorf("id = %q, want %q", id, m2mUserID)
	}
	exchanges, calls, bearers := f.snapshot()
	// La ruta es PÚBLICA: ni canje ni portador.
	if exchanges != 0 {
		t.Errorf("canjes = %d, want 0 (el signup es público)", exchanges)
	}
	if len(calls) != 1 || calls[0] != "POST /api/v1/auth/signup" {
		t.Errorf("llamadas = %v", calls)
	}
	if len(bearers) != 1 {
		t.Fatalf("portadores = %v, want 1", bearers)
	}
	if bearers[0] != "" {
		t.Errorf("portador = %q, want ninguno", bearers[0])
	}
}

func TestM2M_Signup_TraduceLosCodigos(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		status  int
		code    string
		details string
		want    error
	}{
		// El 409 tapa tres estados que identity no distingue: correo con otra
		// clave, cuenta bloqueada y cuenta inactiva.
		{name: "correo ya registrado", status: http.StatusConflict, code: "CONFLICT", want: domain.ErrEmailTaken},
		{
			name: "contraseña corta", status: http.StatusBadRequest, code: "INVALID_REQUEST",
			details: `{"password":"must be at least 12 characters long"}`, want: domain.ErrPasswordPolicy,
		},
		{name: "correo mal formado", status: http.StatusBadRequest, code: "INVALID_REQUEST", details: `{"email":"invalid shape"}`, want: domain.ErrInvalidInput},
		{name: "freno por IP", status: http.StatusTooManyRequests, code: "TOO_MANY_REQUESTS", want: domain.ErrRateLimited},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFakeM2M(t)
			f.status, f.code, f.detailsRaw = tt.status, tt.code, tt.details
			_, err := f.client(t).Signup(context.Background(), m2mEmail, "una-frase-de-acceso-larga", "Ana", "Pérez")
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

// TestM2M_ElCanjeLlevaLaKeyEnElCuerpo fija el punto más fácil de equivocar del
// contrato: la API key viaja en el CUERPO como `api_key` y NUNCA en
// Authorization (identity ADR-0025, dto/service_token_dto.go:30-41).
func TestM2M_ElCanjeLlevaLaKeyEnElCuerpo(t *testing.T) {
	t.Parallel()
	f := newFakeM2M(t)
	f.status, f.body = http.StatusCreated, `{"id":"`+m2mUserID+`","email":"`+m2mEmail+`","created":true}`

	if _, err := f.client(t).EnsureUser(context.Background(), m2mEmail, "Ana", "Pérez"); err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}
	body, auth := f.tokenSnapshot()
	if body == nil {
		t.Fatal("el canje llegó sin cuerpo")
	}
	if got, ok := body["api_key"]; !ok || got != m2mAPIKey {
		t.Errorf("cuerpo del canje = %v, want api_key=%q", body, m2mAPIKey)
	}
	if auth != "" {
		t.Errorf("Authorization del canje = %q, want ninguna: la key se canjea, no se presenta", auth)
	}
	// Ni `grant_type` ni `client_id`: identity no los pide y mandarlos inventaría
	// un contrato que no existe.
	for _, sobrante := range []string{"grant_type", "client_id", "scope"} {
		if _, present := body[sobrante]; present {
			t.Errorf("el canje no debe llevar %q", sobrante)
		}
	}
}

// TestM2M_ElCanjeTraduceLosCodigos cubre mapExchangeError: los fallos del canje
// son de la CREDENCIAL DE MÁQUINA de wApp o de identity, nunca de la persona que
// se estaba dando de alta.
func TestM2M_ElCanjeTraduceLosCodigos(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status int
		code   string
		want   error
	}{
		// El 401 es UNO para tres causas —key desconocida, revocada o vencida—:
		// identity no las distingue y wApp no se las inventa.
		{name: "key rechazada", status: http.StatusUnauthorized, code: "UNAUTHORIZED", want: domain.ErrMachineCredentialInvalid},
		{name: "freno por IP", status: http.StatusTooManyRequests, code: "TOO_MANY_REQUESTS", want: domain.ErrRateLimited},
		{name: "identity indispuesto", status: http.StatusServiceUnavailable, code: "SERVICE_UNAVAILABLE", want: domain.ErrIdentityUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFakeM2M(t)
			f.tokenStatus, f.tokenCode = tt.status, tt.code

			_, err := f.client(t).EnsureUser(context.Background(), m2mEmail, "Ana", "Pérez")
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			exchanges, calls, _ := f.snapshot()
			if exchanges != 1 {
				t.Errorf("canjes = %d, want 1", exchanges)
			}
			// Sin token no se llama a la ruta de negocio: se falla antes.
			if len(calls) != 0 {
				t.Errorf("llamadas = %v, want ninguna sin Service Token", calls)
			}
		})
	}
}

// TestM2M_UnTokenVENCIDOSeVuelveACanjear prueba la EXPIRACIÓN de verdad —no un
// 401—: es el criterio de T2.4 «reutiliza el token hasta que expira» y lo único
// que ejercita usableLifetime.
//
// ⚠️ Depende del reloj: con expires_in=1 la vida usable es la MITAD (500 ms,
// porque restar el margen de 30 s lo dejaría nacido muerto), y el test duerme
// 700 ms para cruzarla. No hay reloj inyectable en el cliente; si esto resulta
// inestable en CI, lo que hay que introducir es esa costura, no subir el sleep.
func TestM2M_UnTokenVENCIDOSeVuelveACanjear(t *testing.T) {
	t.Parallel()
	f := newFakeM2M(t)
	f.expiresIn = 1
	f.status, f.body = http.StatusCreated, `{"id":"`+m2mUserID+`","email":"`+m2mEmail+`","created":true}`

	c := f.client(t)
	if _, err := c.EnsureUser(context.Background(), m2mEmail, "Ana", "Pérez"); err != nil {
		t.Fatalf("primera EnsureUser: %v", err)
	}
	time.Sleep(700 * time.Millisecond)
	if _, err := c.EnsureUser(context.Background(), m2mEmail, "Ana", "Pérez"); err != nil {
		t.Fatalf("segunda EnsureUser: %v", err)
	}

	exchanges, calls, bearers := f.snapshot()
	if exchanges != 2 {
		t.Errorf("canjes = %d, want 2 (el token venció entre las dos llamadas)", exchanges)
	}
	if len(calls) != 2 {
		t.Fatalf("llamadas = %v, want 2", calls)
	}
	// Y el 401 no tuvo nada que ver: cada llamada usó el token de SU canje.
	if bearers[0] != "svc-token-1" || bearers[1] != "svc-token-2" {
		t.Errorf("portadores = %v, want [svc-token-1 svc-token-2]", bearers)
	}
}

// TestM2M_UnaRafagaFriaProduceUnSoloCanje es el test del candado: N peticiones
// que arrancan a la vez con la caché fría producen UN canje, no N. Es lo que
// justifica que el canje esté serializado.
func TestM2M_UnaRafagaFriaProduceUnSoloCanje(t *testing.T) {
	t.Parallel()
	f := newFakeM2M(t)
	f.status, f.body = http.StatusCreated, `{"id":"`+m2mUserID+`","email":"`+m2mEmail+`","created":true}`

	c := f.client(t)
	const rafaga = 20
	arranque := make(chan struct{})
	errs := make([]error, rafaga)
	var wg sync.WaitGroup
	for i := range rafaga {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-arranque
			_, errs[i] = c.EnsureUser(context.Background(), m2mEmail, "Ana", "Pérez")
		}(i)
	}
	close(arranque)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("EnsureUser #%d: %v", i, err)
		}
	}
	exchanges, calls, bearers := f.snapshot()
	if exchanges != 1 {
		t.Errorf("canjes = %d, want 1 (una ráfaga fría no es una estampida)", exchanges)
	}
	if len(calls) != rafaga {
		t.Errorf("llamadas = %d, want %d", len(calls), rafaga)
	}
	for _, b := range bearers {
		if b != "svc-token-1" {
			t.Errorf("portador = %q, want el único token canjeado", b)
		}
	}
}

// TestM2M_UnContextoCanceladoNoEsperaElCanje: el contexto del llamante MANDA. Si
// ya está cancelado, la petición se va con su error y no toca identity —el canje
// vive detrás de una ruta pública y encolar goroutines ahí es agotarlas—.
func TestM2M_UnContextoCanceladoNoEsperaElCanje(t *testing.T) {
	t.Parallel()
	f := newFakeM2M(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := f.client(t).EnsureUser(ctx, m2mEmail, "Ana", "Pérez")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	exchanges, calls, _ := f.snapshot()
	if exchanges != 0 || len(calls) != 0 {
		t.Errorf("canjes = %d, llamadas = %v, want ninguno: el ctx ya estaba muerto", exchanges, calls)
	}
}
