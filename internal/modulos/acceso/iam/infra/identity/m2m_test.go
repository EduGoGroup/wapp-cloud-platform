package iamidentity_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	iamidentity "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/infra/identity"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
)

// Los tests del M2M corren contra un identity de mentira (httptest.Server) y con un reloj FALSO
// (WithClock): la caducidad del Service Token y de la caché negativa se prueban moviendo el
// reloj, nunca durmiendo.

var (
	_ out.IdentityM2MClient = (*iamidentity.M2MClient)(nil)
	_ out.UserSystemsClient = (*iamidentity.M2MClient)(nil)
	_ fmt.Stringer          = (*iamidentity.M2MClient)(nil)
	_ fmt.GoStringer        = (*iamidentity.M2MClient)(nil)
)

const (
	m2mAPIKey = "ak_una-credencial-de-mentira" //nolint:gosec // credencial de mentira de un test
	m2mEmail  = "nueva@tenant.example"
	m2mUserID = "11111111-2222-3333-4444-555555555555"
	// ensureOK es el 201 de /users/ensure con SUS nombres de campo.
	ensureOK = `{"id":"` + m2mUserID + `","email":"` + m2mEmail + `","created":true}`
)

// fakeClock es un reloj parado que solo avanza cuando el test lo dice. Seguro para concurrencia.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// m2mRequest es lo que el fake vio de una petición de NEGOCIO (no del canje).
type m2mRequest struct {
	method string
	// path es la ruta decodificada; escapedPath, la que viajó (para ver el escape del id).
	path        string
	escapedPath string
	bearer      string
	rawBody     string
	body        map[string]any
}

// m2mReply es la respuesta del fake a una petición de negocio. status >= 400 contesta el cuerpo
// de error de identity con code y, si los hay, details (JSON crudo).
type m2mReply struct {
	status  int
	code    string
	details string
	body    string
}

// fakeM2M imita las cuatro rutas M2M de identity más el canje. Cuenta los canjes aparte de las
// llamadas de negocio: las aserciones sobre la CACHÉ miran exchanges; las de «qué hizo el
// cliente», calls.
type fakeM2M struct {
	srv *httptest.Server

	mu        sync.Mutex
	exchanges int
	calls     []m2mRequest
	// tokenBody y tokenAuth son el cuerpo y la cabecera Authorization del ÚLTIMO canje.
	tokenBody map[string]any
	tokenAuth string
	// expiresIn, tokenStatus y tokenCode gobiernan la respuesta del canje; tokenRaw, si no es
	// "", la sustituye entera (para canjes mal formados).
	expiresIn   int
	tokenStatus int
	tokenCode   string
	tokenRaw    string
	// onExchange, si no es nil, corre en cada canje ANTES de contestar y SIN el candado del fake:
	// es donde un test retiene el canje para ver qué hacen los demás mientras tanto.
	onExchange func(r *http.Request)
	// reply decide la respuesta de negocio; por defecto, el 201 de un alta (ensureOK).
	reply func(req m2mRequest) m2mReply
}

func newFakeM2M(t *testing.T) *fakeM2M {
	t.Helper()
	f := &fakeM2M{expiresIn: 900, tokenStatus: http.StatusOK}
	f.reply = func(m2mRequest) m2mReply { return m2mReply{status: http.StatusCreated, body: ensureOK} }
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("leyendo el cuerpo de prueba: %v", err)
		}
		var body map[string]any
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Errorf("el cliente mandó un cuerpo que no es JSON: %q", raw)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/auth/token" {
			f.serveExchange(t, w, r, body)
			return
		}
		req := m2mRequest{
			method: r.Method, path: r.URL.Path, escapedPath: r.URL.EscapedPath(),
			bearer: strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), rawBody: string(raw), body: body,
		}
		f.mu.Lock()
		f.calls = append(f.calls, req)
		reply := f.reply
		f.mu.Unlock()

		out := reply(req)
		w.WriteHeader(out.status)
		if out.status < http.StatusBadRequest {
			writeBody(t, w, out.body)
			return
		}
		details := ""
		if out.details != "" {
			details = `,"details":` + out.details
		}
		writeBody(t, w, `{"error":"x","code":"`+out.code+`"`+details+`}`)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// serveExchange contesta el canje: svc-token-<n> con n el número de canje.
func (f *fakeM2M) serveExchange(t *testing.T, w http.ResponseWriter, r *http.Request, body map[string]any) {
	f.mu.Lock()
	f.exchanges++
	n := f.exchanges
	f.tokenBody, f.tokenAuth = body, r.Header.Get("Authorization")
	hook, status, code, raw, expiresIn := f.onExchange, f.tokenStatus, f.tokenCode, f.tokenRaw, f.expiresIn
	f.mu.Unlock()

	if hook != nil {
		hook(r)
	}
	switch {
	case status >= http.StatusBadRequest:
		w.WriteHeader(status)
		writeBody(t, w, `{"error":"x","code":"`+code+`"}`)
	case raw != "":
		writeBody(t, w, raw)
	default:
		writeBody(t, w, fmt.Sprintf(`{"status":"ok","service_token":"svc-token-%d","expires_in":%d}`, n, expiresIn))
	}
}

// set cambia la configuración del fake bajo su candado.
func (f *fakeM2M) set(change func(f *fakeM2M)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(f)
}

// respondWith fija una respuesta de negocio constante.
func (f *fakeM2M) respondWith(r m2mReply) {
	f.set(func(f *fakeM2M) { f.reply = func(m2mRequest) m2mReply { return r } })
}

func (f *fakeM2M) snapshot() (exchanges int, calls []m2mRequest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.exchanges, append([]m2mRequest(nil), f.calls...)
}

func (f *fakeM2M) bearers() []string {
	_, calls := f.snapshot()
	out := make([]string, 0, len(calls))
	for _, c := range calls {
		out = append(out, c.bearer)
	}
	return out
}

// client construye el cliente con el reloj falso dado (nil: sin WithClock).
func (f *fakeM2M) client(t *testing.T, clock *fakeClock) *iamidentity.M2MClient {
	t.Helper()
	var opts []iamidentity.M2MOption
	if clock != nil {
		opts = append(opts, iamidentity.WithClock(clock.Now))
	}
	c, err := iamidentity.NewM2M(f.srv.URL, m2mAPIKey, 2*time.Second, opts...)
	if err != nil {
		t.Fatalf("NewM2M: %v", err)
	}
	return c
}

func ensure(c *iamidentity.M2MClient) error {
	_, err := c.EnsureUser(context.Background(), m2mEmail, "Ana", "Pérez")
	return err
}

func requireExchanges(t *testing.T, f *fakeM2M, want int, why string) {
	t.Helper()
	if got, _ := f.snapshot(); got != want {
		t.Errorf("canjes = %d, quería %d (%s)", got, want, why)
	}
}

// --- Constructor (R-I2) ---

// TestNewM2M_RequiresURLAndCredential (R-I2): sin URL usable o sin API key no hay cliente, con
// sus textos literales.
func TestNewM2M_RequiresURLAndCredential(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, url, key, wantErr string
	}{
		{name: "empty_url", url: "", key: m2mAPIKey, wantErr: "iam: la URL de identity-api no puede estar vacía"},
		{name: "url_without_scheme", url: "localhost:8200", key: m2mAPIKey, wantErr: `iam: la URL de identity-api debe ser http(s): "localhost:8200"`},
		{name: "empty_key", url: "http://localhost:8200", key: "", wantErr: "iam: la credencial M2M de identity (WAPP_IDENTITY_API_KEY) no puede estar vacía"},
		{name: "blank_key", url: "http://localhost:8200", key: "  ", wantErr: "iam: la credencial M2M de identity (WAPP_IDENTITY_API_KEY) no puede estar vacía"},
	}
	for _, tt := range tests {
		c, err := iamidentity.NewM2M(tt.url, tt.key, time.Second)
		if err == nil || err.Error() != tt.wantErr || c != nil {
			t.Errorf("%s: NewM2M = %v, %v; quería nil y «%s»", tt.name, c, err, tt.wantErr)
		}
	}
}

// TestNewM2M_TrimsAndDoesNotCallIdentity: construir no canjea; la URL y la key se recortan; un
// WithClock(nil) se ignora y el cliente funciona con el reloj real.
func TestNewM2M_TrimsAndDoesNotCallIdentity(t *testing.T) {
	t.Parallel()
	f := newFakeM2M(t)
	f.respondWith(m2mReply{status: http.StatusCreated, body: ensureOK})

	c, err := iamidentity.NewM2M(" "+f.srv.URL+"/ ", "  "+m2mAPIKey+"  ", 0, iamidentity.WithClock(nil))
	if err != nil {
		t.Fatalf("NewM2M: %v", err)
	}
	requireExchanges(t, f, 0, "construir no llama a identity")
	for range 2 {
		if err := ensure(c); err != nil {
			t.Fatalf("EnsureUser: %v", err)
		}
	}
	requireExchanges(t, f, 1, "con el reloj real el token sigue vivo")
	body, _ := f.tokenBodyAndAuth()
	if body["api_key"] != m2mAPIKey {
		t.Errorf("api_key del canje = %v, quería la key recortada", body["api_key"])
	}
	if _, calls := f.snapshot(); calls[0].path != "/api/v1/users/ensure" {
		t.Errorf("path = %q, quería /api/v1/users/ensure (sin doble barra)", calls[0].path)
	}
}

func (f *fakeM2M) tokenBodyAndAuth() (map[string]any, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tokenBody, f.tokenAuth
}

// --- El Service Token: canje, caché y caducidad (R-I3, R-I4) ---

// TestM2M_ExchangesOnceAndReusesServiceToken (R-I3): dos operaciones, un canje, el mismo
// portador; la API key nunca es portador.
func TestM2M_ExchangesOnceAndReusesServiceToken(t *testing.T) {
	t.Parallel()
	f := newFakeM2M(t)
	f.respondWith(m2mReply{status: http.StatusCreated, body: ensureOK})
	c := f.client(t, newFakeClock())

	for i := range 3 {
		if err := ensure(c); err != nil {
			t.Fatalf("EnsureUser #%d: %v", i+1, err)
		}
	}
	requireExchanges(t, f, 1, "el token se cachea hasta que expira")
	for _, b := range f.bearers() {
		if b != "svc-token-1" {
			t.Errorf("portador = %q, quería svc-token-1", b)
		}
	}
}

// TestM2M_ExpiredTokenIsExchangedAgain (R-I3): pasada su vida, la siguiente llamada canjea de
// nuevo y usa el token nuevo.
func TestM2M_ExpiredTokenIsExchangedAgain(t *testing.T) {
	t.Parallel()
	f := newFakeM2M(t)
	clock := newFakeClock()
	c := f.client(t, clock)

	if err := ensure(c); err != nil {
		t.Fatalf("primera EnsureUser: %v", err)
	}
	clock.Advance(15 * time.Minute)
	if err := ensure(c); err != nil {
		t.Fatalf("segunda EnsureUser: %v", err)
	}
	requireExchanges(t, f, 2, "el token venció entre las dos llamadas")
	if b := f.bearers(); len(b) != 2 || b[0] != "svc-token-1" || b[1] != "svc-token-2" {
		t.Errorf("portadores = %v, quería [svc-token-1 svc-token-2]", b)
	}
}

// TestM2M_TokenLifetimeKeepsSafetyMargin (R-I3): la caché da por vencido el token 30 s ANTES de
// expires_in, salvo que eso lo dejara nacido muerto (expires_in <= 60 s): entonces, la mitad.
func TestM2M_TokenLifetimeKeepsSafetyMargin(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		expiresIn int
		usable    time.Duration
	}{
		{name: "long_token_minus_margin", expiresIn: 900, usable: 870 * time.Second},
		{name: "just_above_twice_the_margin", expiresIn: 61, usable: 31 * time.Second},
		{name: "twice_the_margin_uses_half", expiresIn: 60, usable: 30 * time.Second},
		{name: "short_token_uses_half", expiresIn: 40, usable: 20 * time.Second},
		{name: "one_second_token_uses_half", expiresIn: 1, usable: 500 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFakeM2M(t)
			f.set(func(f *fakeM2M) { f.expiresIn = tt.expiresIn })
			clock := newFakeClock()
			c := f.client(t, clock)

			if err := ensure(c); err != nil {
				t.Fatalf("EnsureUser: %v", err)
			}
			clock.Advance(tt.usable - time.Millisecond)
			if err := ensure(c); err != nil {
				t.Fatalf("EnsureUser: %v", err)
			}
			requireExchanges(t, f, 1, "un milisegundo antes de su vida usable el token sigue valiendo")
			clock.Advance(time.Millisecond)
			if err := ensure(c); err != nil {
				t.Fatalf("EnsureUser: %v", err)
			}
			requireExchanges(t, f, 2, "al cumplir su vida usable el token se da por vencido")
		})
	}
}

// TestM2M_ExchangeCarriesKeyInBody (R-I4): la key viaja en el cuerpo como `api_key`, el canje no
// lleva Authorization ni campos inventados, y la key no es nunca portador de negocio.
func TestM2M_ExchangeCarriesKeyInBody(t *testing.T) {
	t.Parallel()
	f := newFakeM2M(t)
	if err := ensure(f.client(t, newFakeClock())); err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}
	body, auth := f.tokenBodyAndAuth()
	if len(body) != 1 || body["api_key"] != m2mAPIKey {
		t.Errorf("cuerpo del canje = %v, quería solo api_key=%q", body, m2mAPIKey)
	}
	for _, extra := range []string{"grant_type", "client_id", "scope"} {
		if _, present := body[extra]; present {
			t.Errorf("el canje lleva %q: identity no lo pide", extra)
		}
	}
	if auth != "" {
		t.Errorf("Authorization del canje = %q, quería ninguna: la key se canjea, no se presenta", auth)
	}
	for _, b := range f.bearers() {
		if strings.Contains(b, m2mAPIKey) {
			t.Error("la API key viajó como portador de negocio")
		}
	}
}

// --- Token rechazado (R-I3) ---

// rejectToken hace que la ruta de negocio conteste 401 al token dado y 201 al resto.
func rejectToken(token string) func(m2mRequest) m2mReply {
	return func(req m2mRequest) m2mReply {
		if req.bearer == token {
			return m2mReply{status: http.StatusUnauthorized, code: "UNAUTHORIZED"}
		}
		return m2mReply{status: http.StatusCreated, body: ensureOK}
	}
}

// TestM2M_RejectedTokenIsReexchangedOnceAndRetriedOnce (R-I3): con un 401 perpetuo, un recanje y
// un reintento, no un bucle; el reintento usa el token recién canjeado.
func TestM2M_RejectedTokenIsReexchangedOnceAndRetriedOnce(t *testing.T) {
	t.Parallel()
	f := newFakeM2M(t)
	f.respondWith(m2mReply{status: http.StatusUnauthorized, code: "UNAUTHORIZED"})

	err := ensure(f.client(t, newFakeClock()))
	if !errors.Is(err, domain.ErrMachineCredentialInvalid) {
		t.Fatalf("err = %v, quería ErrMachineCredentialInvalid", err)
	}
	requireExchanges(t, f, 2, "uno inicial y uno tras el 401")
	if b := f.bearers(); len(b) != 2 || b[0] != "svc-token-1" || b[1] != "svc-token-2" {
		t.Errorf("portadores = %v, quería [svc-token-1 svc-token-2]: un intento y un reintento", b)
	}
}

// TestM2M_RejectedTokenRecoversWithFreshToken (R-I3): si el token nuevo vale, la operación sale
// bien y el nuevo queda en la caché.
func TestM2M_RejectedTokenRecoversWithFreshToken(t *testing.T) {
	t.Parallel()
	f := newFakeM2M(t)
	f.set(func(f *fakeM2M) { f.reply = rejectToken("svc-token-1") })
	c := f.client(t, newFakeClock())

	for i := range 2 {
		if err := ensure(c); err != nil {
			t.Fatalf("EnsureUser #%d: %v", i+1, err)
		}
	}
	requireExchanges(t, f, 2, "un recanje y después se reutiliza el nuevo")
	if b := f.bearers(); len(b) != 3 || b[2] != "svc-token-2" {
		t.Errorf("portadores = %v, quería [svc-token-1 svc-token-2 svc-token-2]", b)
	}
}

// TestM2M_ConcurrentRejectionsReexchangeOnce (R-I3): dos llamadas que se comen el 401 del MISMO
// token a la vez producen UN recanje, no dos.
func TestM2M_ConcurrentRejectionsReexchangeOnce(t *testing.T) {
	t.Parallel()
	f := newFakeM2M(t)
	var (
		mu       sync.Mutex
		rejected int
		bothIn   = make(chan struct{})
	)
	reject := rejectToken("svc-token-1")
	f.set(func(f *fakeM2M) {
		f.reply = func(req m2mRequest) m2mReply {
			if req.bearer == "svc-token-1" {
				// Las dos se retienen hasta que AMBAS han presentado svc-token-1.
				mu.Lock()
				rejected++
				if rejected == 2 {
					close(bothIn)
				}
				mu.Unlock()
				select {
				case <-bothIn:
				case <-time.After(2 * time.Second):
					t.Error("las dos llamadas no llegaron a presentar el mismo token a la vez")
				}
			}
			return reject(req)
		}
	})
	c := f.client(t, newFakeClock())

	errs := make(chan error, 2)
	for range 2 {
		go func() { errs <- ensure(c) }()
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("EnsureUser: %v", err)
		}
	}
	requireExchanges(t, f, 2, "el canje inicial y UN recanje compartido")
}

// TestM2M_FailedReexchangeEvictsRejectedToken (R-I3): si el recanje falla, el token rechazado
// sale de la caché y la siguiente llamada canjea en vez de volver a presentarlo.
func TestM2M_FailedReexchangeEvictsRejectedToken(t *testing.T) {
	t.Parallel()
	f := newFakeM2M(t)
	f.set(func(f *fakeM2M) { f.reply = rejectToken("svc-token-1") })
	clock := newFakeClock()
	c := f.client(t, clock)

	if err := ensure(c); err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}
	// Ahora identity rechaza svc-token-2 y además el canje se cae.
	f.set(func(f *fakeM2M) {
		f.reply = rejectToken("svc-token-2")
		f.tokenStatus = http.StatusServiceUnavailable
	})
	clock.Advance(time.Minute)
	if err := ensure(c); !errors.Is(err, domain.ErrIdentityUnavailable) {
		t.Fatalf("err = %v, quería ErrIdentityUnavailable (el recanje se cayó)", err)
	}
	f.set(func(f *fakeM2M) { f.tokenStatus = http.StatusOK })
	clock.Advance(2 * time.Second) // fuera de la caché negativa
	if err := ensure(c); err != nil {
		t.Fatalf("EnsureUser tras recuperarse identity: %v", err)
	}
	// Sin desalojo, la tercera llamada presentaría otra vez svc-token-2 y se comería un 401 más.
	want := []string{"svc-token-1", "svc-token-2", "svc-token-2", "svc-token-4"}
	if b := f.bearers(); strings.Join(b, ",") != strings.Join(want, ",") {
		t.Errorf("portadores = %v, quería %v: no se vuelve a presentar el token que identity rechazó", b, want)
	}
	requireExchanges(t, f, 4, "inicial, recanje de svc-token-1, recanje caído y canje nuevo")
}

// --- Caché negativa ---

// TestM2M_FailedExchangeIsRememberedBriefly: un canje caído se recuerda 1 s; dentro de esa
// ventana nadie vuelve a canjear y todos reciben el mismo error; pasada, se canjea de nuevo.
func TestM2M_FailedExchangeIsRememberedBriefly(t *testing.T) {
	t.Parallel()
	f := newFakeM2M(t)
	f.set(func(f *fakeM2M) { f.tokenStatus = http.StatusServiceUnavailable })
	clock := newFakeClock()
	c := f.client(t, clock)

	first := ensure(c)
	if !errors.Is(first, domain.ErrIdentityUnavailable) {
		t.Fatalf("err = %v, quería ErrIdentityUnavailable", first)
	}
	clock.Advance(time.Second - time.Millisecond)
	if err := ensure(c); !errors.Is(err, domain.ErrIdentityUnavailable) {
		t.Errorf("dentro de la ventana: err = %v, quería el mismo ErrIdentityUnavailable", err)
	}
	requireExchanges(t, f, 1, "dentro de la ventana el fallo se sirve de memoria")

	f.set(func(f *fakeM2M) { f.tokenStatus = http.StatusOK })
	clock.Advance(time.Millisecond)
	if err := ensure(c); err != nil {
		t.Fatalf("pasada la ventana: %v", err)
	}
	requireExchanges(t, f, 2, "pasada la ventana se vuelve a canjear")
}

// TestM2M_CanceledExchangeIsNotRemembered: un canje que falla porque el contexto de quien
// canjeaba se canceló no se recuerda; el siguiente canjea sin mover el reloj.
func TestM2M_CanceledExchangeIsNotRemembered(t *testing.T) {
	t.Parallel()
	f := newFakeM2M(t)
	arrived := make(chan struct{}, 1)
	release := make(chan struct{})
	f.set(func(f *fakeM2M) {
		f.onExchange = func(r *http.Request) {
			arrived <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
			}
		}
	})
	c := f.client(t, newFakeClock())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.EnsureUser(ctx, m2mEmail, "Ana", "Pérez")
		done <- err
	}()
	<-arrived
	cancel()
	if err := <-done; err == nil {
		t.Fatal("la llamada cancelada a mitad del canje devolvió nil")
	}
	f.set(func(f *fakeM2M) { f.onExchange = nil })
	close(release)

	if err := ensure(c); err != nil {
		t.Fatalf("EnsureUser tras la cancelación ajena: %v (se cacheó una prisa ajena)", err)
	}
	requireExchanges(t, f, 2, "la cancelación no se recuerda")
}

// --- El candado (R-I5) ---

// TestM2M_ColdBurstProducesSingleExchange (R-I5): con la caché fría, N llamadas concurrentes
// producen UN canje y todas usan su token.
func TestM2M_ColdBurstProducesSingleExchange(t *testing.T) {
	t.Parallel()
	f := newFakeM2M(t)
	arrived := make(chan struct{}, 64)
	release := make(chan struct{})
	f.set(func(f *fakeM2M) {
		f.onExchange = func(*http.Request) {
			arrived <- struct{}{}
			<-release
		}
	})
	c := f.client(t, newFakeClock())

	const burst = 20
	errs := make(chan error, burst)
	for range burst {
		go func() { errs <- ensure(c) }()
	}
	// El primer canje queda retenido; se da margen a que una estampida se viera antes de soltarlo.
	<-arrived
	time.Sleep(50 * time.Millisecond)
	close(release)
	for range burst {
		if err := <-errs; err != nil {
			t.Fatalf("EnsureUser: %v", err)
		}
	}
	requireExchanges(t, f, 1, "una ráfaga fría no es una estampida")
	b := f.bearers()
	if len(b) != burst {
		t.Errorf("llamadas de negocio = %d, quería %d", len(b), burst)
	}
	for _, bearer := range b {
		if bearer != "svc-token-1" { //nolint:gosec // token de mentira de un test
			t.Errorf("portador = %q, quería el único token canjeado", bearer)
		}
	}
}

// TestM2M_CanceledContextDoesNotWaitForExchange (R-I5): con el contexto ya muerto no se toca
// identity; cancelado mientras otro canjea, se vuelve en el acto sin esperar ese canje.
func TestM2M_CanceledContextDoesNotWaitForExchange(t *testing.T) {
	t.Parallel()
	t.Run("already_canceled_never_reaches_identity", func(t *testing.T) {
		t.Parallel()
		f := newFakeM2M(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		c := f.client(t, newFakeClock())
		// Se repite: con el turno libre y el ctx muerto, un select sin la comprobación previa
		// elegiría al azar entre los dos casos, y una sola llamada acertaría por suerte la mitad
		// de las veces.
		for i := range 64 {
			_, err := c.EnsureUser(ctx, m2mEmail, "Ana", "Pérez")
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("llamada #%d: err = %v, quería context.Canceled", i+1, err)
			}
		}
		if exchanges, calls := f.snapshot(); exchanges != 0 || len(calls) != 0 {
			t.Errorf("canjes = %d, llamadas = %d; quería ninguno con el ctx ya muerto", exchanges, len(calls))
		}
	})
	t.Run("canceled_while_another_exchanges_returns_at_once", func(t *testing.T) {
		t.Parallel()
		f := newFakeM2M(t)
		arrived := make(chan struct{}, 1)
		release := make(chan struct{})
		f.set(func(f *fakeM2M) {
			f.onExchange = func(*http.Request) {
				arrived <- struct{}{}
				<-release
			}
		})
		c := f.client(t, newFakeClock())

		holder := make(chan error, 1)
		go func() { holder <- ensure(c) }()
		<-arrived // el primero tiene el turno y está canjeando

		ctx, cancel := context.WithCancel(context.Background())
		waiter := make(chan error, 1)
		go func() {
			_, err := c.EnsureUser(ctx, m2mEmail, "Ana", "Pérez")
			waiter <- err
		}()
		time.Sleep(20 * time.Millisecond) // que el segundo llegue a esperar el turno
		cancel()
		select {
		case err := <-waiter:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("err = %v, quería context.Canceled", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("la llamada cancelada siguió esperando el canje ajeno")
		}
		close(release)
		if err := <-holder; err != nil {
			t.Fatalf("EnsureUser del que canjeaba: %v", err)
		}
		requireExchanges(t, f, 1, "el cancelado no canjeó")
	})
}

// --- Fallos del canje (R-I9) ---

// TestM2M_ExchangeFailuresAreMachineCredential (R-I9): los fallos del canje son de la credencial
// de máquina o de identity, nunca de la persona; sin token no se llama a la ruta de negocio.
func TestM2M_ExchangeFailuresAreMachineCredential(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status int
		want   error
	}{
		{name: "key_rejected", status: http.StatusUnauthorized, want: domain.ErrMachineCredentialInvalid},
		{name: "empty_key_is_machine_credential", status: http.StatusBadRequest, want: domain.ErrMachineCredentialInvalid},
		{name: "rate_limited", status: http.StatusTooManyRequests, want: domain.ErrRateLimited},
		{name: "internal_error", status: http.StatusInternalServerError, want: domain.ErrIdentityUnavailable},
		{name: "service_unavailable", status: http.StatusServiceUnavailable, want: domain.ErrIdentityUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFakeM2M(t)
			f.set(func(f *fakeM2M) { f.tokenStatus, f.tokenCode = tt.status, "X" })
			err := ensure(f.client(t, newFakeClock()))
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, quería %v", err, tt.want)
			}
			if errors.Is(err, domain.ErrInvalidInput) || errors.Is(err, domain.ErrInvalidCredentials) {
				t.Errorf("err = %v: un fallo del canje no es de la persona", err)
			}
			if exchanges, calls := f.snapshot(); exchanges != 1 || len(calls) != 0 {
				t.Errorf("canjes = %d, llamadas = %d; quería 1 y ninguna sin Service Token", exchanges, len(calls))
			}
		})
	}
}

// TestM2M_MalformedExchangeIsAnError (R-I9): un canje sin token o sin vigencia es un error con su
// texto, y no se llama a la ruta de negocio.
func TestM2M_MalformedExchangeIsAnError(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, raw, wantErr string }{
		{name: "without_service_token", raw: `{"status":"ok","expires_in":900}`, wantErr: "iam: identity devolvió un canje sin service token"},
		{name: "access_token_is_not_service_token", raw: `{"access_token":"x","expires_in":900}`, wantErr: "iam: identity devolvió un canje sin service token"},
		{name: "zero_lifetime", raw: `{"service_token":"svc","expires_in":0}`, wantErr: "iam: identity devolvió un service token sin vigencia"},
		{name: "negative_lifetime", raw: `{"service_token":"svc","expires_in":-5}`, wantErr: "iam: identity devolvió un service token sin vigencia"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFakeM2M(t)
			f.set(func(f *fakeM2M) { f.tokenRaw = tt.raw })
			err := ensure(f.client(t, newFakeClock()))
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("err = %v, quería «%s»", err, tt.wantErr)
			}
			if _, calls := f.snapshot(); len(calls) != 0 {
				t.Errorf("llamadas = %d, quería ninguna sin Service Token", len(calls))
			}
		})
	}
}

// TestM2M_UnreachableIdentityIsUnavailable (R-I2): un fallo de transporte es
// ErrIdentityUnavailable, también en la ruta pública.
func TestM2M_UnreachableIdentityIsUnavailable(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	c, err := iamidentity.NewM2M(url, m2mAPIKey, time.Second, iamidentity.WithClock(newFakeClock().Now))
	if err != nil {
		t.Fatalf("NewM2M: %v", err)
	}
	if err := ensure(c); !errors.Is(err, domain.ErrIdentityUnavailable) || errors.Is(err, domain.ErrMachineCredentialInvalid) {
		t.Errorf("EnsureUser: err = %v, quería ErrIdentityUnavailable", err)
	}
	if _, err := c.Signup(context.Background(), m2mEmail, "una-frase-de-acceso-larga", "Ana", "Pérez"); !errors.Is(err, domain.ErrIdentityUnavailable) {
		t.Errorf("Signup: err = %v, quería ErrIdentityUnavailable", err)
	}
}

// --- Higiene ---

// TestM2MClient_StringHidesSecrets: ningún verbo de fmt vuelca la API key ni el Service Token;
// String y GoString dicen lo mismo y nombran la URL.
func TestM2MClient_StringHidesSecrets(t *testing.T) {
	t.Parallel()
	f := newFakeM2M(t)
	c := f.client(t, newFakeClock())
	if err := ensure(c); err != nil { // con un token ya en la caché
		t.Fatalf("EnsureUser: %v", err)
	}
	if c.String() != c.GoString() {
		t.Errorf("String = %q y GoString = %q, quería lo mismo", c.String(), c.GoString())
	}
	if !strings.Contains(c.String(), f.srv.URL) {
		t.Errorf("String = %q, quería que nombrara la URL base", c.String())
	}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		out := fmt.Sprintf(verb, c)
		if strings.Contains(out, m2mAPIKey) || strings.Contains(out, "svc-token-1") {
			t.Errorf("%s volcó material secreto: %q", verb, out)
		}
	}
}

// --- EnsureUser ---

// TestM2M_EnsureUser_SendsThreeFieldsAndReadsAccount: POST /users/ensure con el Service Token y
// solo email, first_name y last_name; devuelve id, email y created de identity.
func TestM2M_EnsureUser_SendsThreeFieldsAndReadsAccount(t *testing.T) {
	t.Parallel()
	f := newFakeM2M(t)
	f.respondWith(m2mReply{status: http.StatusCreated, body: `{"id":"` + m2mUserID + `","email":"normalizado@tenant.example","created":true}`})

	user, err := f.client(t, newFakeClock()).EnsureUser(context.Background(), m2mEmail, "Ana", "Pérez")
	if err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}
	if user.ID != m2mUserID || user.Email != "normalizado@tenant.example" || !user.Created {
		t.Errorf("usuario = %+v, quería el que devolvió identity", user)
	}
	_, calls := f.snapshot()
	req := calls[0]
	if req.method != http.MethodPost || req.path != "/api/v1/users/ensure" || req.bearer != "svc-token-1" {
		t.Errorf("petición = %s %s con portador %q", req.method, req.path, req.bearer)
	}
	want := map[string]any{"email": m2mEmail, "first_name": "Ana", "last_name": "Pérez"}
	if len(req.body) != len(want) {
		t.Errorf("cuerpo = %v, quería exactamente %v (sin password, systems ni system)", req.body, want)
	}
	for k, v := range want {
		if req.body[k] != v {
			t.Errorf("cuerpo[%s] = %v, quería %v", k, req.body[k], v)
		}
	}
}

// TestM2M_EnsureUser_RejectsBadInputAndBadAnswers: correo vacío no sale al cable; un alta sin id
// es un error con su texto.
func TestM2M_EnsureUser_RejectsBadInputAndBadAnswers(t *testing.T) {
	t.Parallel()
	f := newFakeM2M(t)
	c := f.client(t, newFakeClock())
	if _, err := c.EnsureUser(context.Background(), "  ", "Ana", "Pérez"); !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("correo vacío: err = %v, quería ErrInvalidInput", err)
	}
	requireExchanges(t, f, 0, "un correo vacío no sale al cable")

	f.respondWith(m2mReply{status: http.StatusCreated, body: `{"email":"` + m2mEmail + `","created":true}`})
	_, err := c.EnsureUser(context.Background(), m2mEmail, "Ana", "Pérez")
	if err == nil || err.Error() != "iam: identity devolvió un alta sin identificador" {
		t.Errorf("err = %v, quería «iam: identity devolvió un alta sin identificador»", err)
	}
}

// m2mCodeCase es un código de identity y el centinela en que debe traducirse (nil: error opaco).
type m2mCodeCase struct {
	name    string
	status  int
	code    string
	details string
	want    error
}

// runCodeCases lanza cada caso contra un fake nuevo y comprueba la traducción.
func runCodeCases(t *testing.T, cases []m2mCodeCase, call func(c *iamidentity.M2MClient) error) {
	t.Helper()
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFakeM2M(t)
			f.respondWith(m2mReply{status: tt.status, code: tt.code, details: tt.details})
			err := call(f.client(t, newFakeClock()))
			if tt.want == nil {
				if err == nil {
					t.Fatal("un código no traducido devolvió nil")
				}
				return
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, quería %v", err, tt.want)
			}
		})
	}
}

// TestM2M_EnsureUser_MapsCodes: 401 (tras el reintento) y 403 son de la credencial de wApp.
func TestM2M_EnsureUser_MapsCodes(t *testing.T) {
	t.Parallel()
	runCodeCases(t, []m2mCodeCase{
		{name: "bad_request", status: http.StatusBadRequest, code: "INVALID_REQUEST", want: domain.ErrInvalidInput},
		{name: "unauthorized_after_retry", status: http.StatusUnauthorized, code: "UNAUTHORIZED", want: domain.ErrMachineCredentialInvalid},
		{name: "forbidden_scope", status: http.StatusForbidden, code: "FORBIDDEN", want: domain.ErrMachineCredentialInvalid},
		{name: "rate_limited", status: http.StatusTooManyRequests, code: "TOO_MANY_REQUESTS", want: domain.ErrRateLimited},
		{name: "internal_error", status: http.StatusInternalServerError, code: "INTERNAL", want: domain.ErrIdentityUnavailable},
		{name: "service_unavailable", status: http.StatusServiceUnavailable, code: "SERVICE_UNAVAILABLE", want: domain.ErrIdentityUnavailable},
		{name: "unmapped_is_opaque", status: http.StatusTeapot, code: "TEAPOT"},
	}, ensure)
}

// --- GetUserSystems (R-I7) ---

// TestM2M_GetUserSystems_ReturnsWholeSetWithoutBody (R-I7): GET sobre la ruta del recurso, sin
// cuerpo, con el id escapado; devuelve TODAS las claves en su orden.
func TestM2M_GetUserSystems_ReturnsWholeSetWithoutBody(t *testing.T) {
	t.Parallel()
	f := newFakeM2M(t)
	f.respondWith(m2mReply{status: http.StatusOK, body: `{"systems":["wapp.bff","edugo.web"]}`})
	c := f.client(t, newFakeClock())

	got, err := c.GetUserSystems(context.Background(), m2mUserID)
	if err != nil {
		t.Fatalf("GetUserSystems: %v", err)
	}
	if len(got) != 2 || got[0] != "wapp.bff" || got[1] != "edugo.web" {
		t.Fatalf("systems = %v, quería [wapp.bff edugo.web]", got)
	}
	if _, err := c.GetUserSystems(context.Background(), "a/b"); err != nil {
		t.Fatalf("GetUserSystems con barra: %v", err)
	}
	_, calls := f.snapshot()
	if calls[0].method != http.MethodGet || calls[0].path != "/api/v1/users/"+m2mUserID+"/systems" || calls[0].bearer != "svc-token-1" {
		t.Errorf("petición = %s %s con portador %q", calls[0].method, calls[0].path, calls[0].bearer)
	}
	if calls[0].rawBody != "" {
		t.Errorf("la lectura viajó con cuerpo %q: no declara nada", calls[0].rawBody)
	}
	if calls[1].escapedPath != "/api/v1/users/a%2Fb/systems" {
		t.Errorf("ruta = %q, quería el id escapado en un solo segmento", calls[1].escapedPath)
	}
}

// TestM2M_GetUserSystems_NullIsEmptyNotNil (R-I7): `null` y la clave ausente son el arreglo
// vacío, nunca nil.
func TestM2M_GetUserSystems_NullIsEmptyNotNil(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{"null": `{"systems":null}`, "missing": `{}`} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFakeM2M(t)
			f.respondWith(m2mReply{status: http.StatusOK, body: body})
			got, err := f.client(t, newFakeClock()).GetUserSystems(context.Background(), m2mUserID)
			if err != nil {
				t.Fatalf("GetUserSystems: %v", err)
			}
			if got == nil || len(got) != 0 {
				t.Fatalf("systems = %#v, quería el arreglo vacío y no nil", got)
			}
		})
	}
}

// TestM2M_GetUserSystems_EmptyIDNeverReachesTheWire (R-I7).
func TestM2M_GetUserSystems_EmptyIDNeverReachesTheWire(t *testing.T) {
	t.Parallel()
	f := newFakeM2M(t)
	if _, err := f.client(t, newFakeClock()).GetUserSystems(context.Background(), "  "); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("err = %v, quería ErrInvalidInput", err)
	}
	if exchanges, calls := f.snapshot(); exchanges != 0 || len(calls) != 0 {
		t.Errorf("canjes = %d, llamadas = %d; quería ninguno", exchanges, len(calls))
	}
}

// TestM2M_GetUserSystems_HasItsOwnCodeMapping (R-I7): el 403 SYSTEM_ACCESS_DENIED aquí es de la
// credencial, no «esa aplicación no es tuya» como en el PUT.
func TestM2M_GetUserSystems_HasItsOwnCodeMapping(t *testing.T) {
	t.Parallel()
	runCodeCases(t, []m2mCodeCase{
		{name: "not_in_registry", status: http.StatusNotFound, code: "NOT_FOUND", want: domain.ErrNotFound},
		{name: "forbidden_scope", status: http.StatusForbidden, code: "FORBIDDEN", want: domain.ErrMachineCredentialInvalid},
		{name: "ecosystem_403_is_credential_here", status: http.StatusForbidden, code: "SYSTEM_ACCESS_DENIED", want: domain.ErrMachineCredentialInvalid},
		{name: "unauthorized_after_retry", status: http.StatusUnauthorized, code: "UNAUTHORIZED", want: domain.ErrMachineCredentialInvalid},
		{name: "bad_request", status: http.StatusBadRequest, code: "INVALID_REQUEST", want: domain.ErrInvalidInput},
		{name: "rate_limited", status: http.StatusTooManyRequests, code: "RATE_LIMITED", want: domain.ErrRateLimited},
		{name: "internal_error", status: http.StatusInternalServerError, code: "INTERNAL", want: domain.ErrIdentityUnavailable},
		{name: "service_unavailable", status: http.StatusServiceUnavailable, code: "SERVICE_UNAVAILABLE", want: domain.ErrIdentityUnavailable},
		{name: "unmapped_is_opaque", status: http.StatusTeapot, code: "TEAPOT"},
	}, func(c *iamidentity.M2MClient) error {
		_, err := c.GetUserSystems(context.Background(), m2mUserID)
		return err
	})
}

// --- ReplaceUserSystems (R-I6) ---

// TestM2M_ReplaceUserSystems_DeclaresSetAndReadsDiff (R-I6): PUT con el conjunto en el cuerpo,
// el id solo en la ruta, y el diff de identity de vuelta.
func TestM2M_ReplaceUserSystems_DeclaresSetAndReadsDiff(t *testing.T) {
	t.Parallel()
	f := newFakeM2M(t)
	f.respondWith(m2mReply{status: http.StatusOK, body: `{"systems":["wapp.bff"],"granted":["wapp.bff"],"revoked":["wapp.edge"]}`})

	diff, err := f.client(t, newFakeClock()).ReplaceUserSystems(context.Background(), m2mUserID, []string{"wapp.bff"})
	if err != nil {
		t.Fatalf("ReplaceUserSystems: %v", err)
	}
	if len(diff.Systems) != 1 || diff.Systems[0] != "wapp.bff" || len(diff.Granted) != 1 || diff.Granted[0] != "wapp.bff" ||
		len(diff.Revoked) != 1 || diff.Revoked[0] != "wapp.edge" {
		t.Errorf("diff = %+v, quería el que devolvió identity", diff)
	}
	_, calls := f.snapshot()
	req := calls[0]
	if req.method != http.MethodPut || req.path != "/api/v1/users/"+m2mUserID+"/systems" || req.bearer != "svc-token-1" {
		t.Errorf("petición = %s %s con portador %q", req.method, req.path, req.bearer)
	}
	if req.rawBody != `{"systems":["wapp.bff"]}` {
		t.Errorf("cuerpo = %s, quería solo el conjunto (el user_id va en la ruta)", req.rawBody)
	}
}

// TestM2M_ReplaceUserSystems_NilSetTravelsAsEmpty (R-I6): nil viaja como `[]` explícito, y un diff
// con `null` o claves ausentes vuelve con los tres campos no nil.
func TestM2M_ReplaceUserSystems_NilSetTravelsAsEmpty(t *testing.T) {
	t.Parallel()
	f := newFakeM2M(t)
	f.respondWith(m2mReply{status: http.StatusOK, body: `{"systems":null,"revoked":["wapp.bff"]}`})

	diff, err := f.client(t, newFakeClock()).ReplaceUserSystems(context.Background(), m2mUserID, nil)
	if err != nil {
		t.Fatalf("ReplaceUserSystems: %v", err)
	}
	if _, calls := f.snapshot(); calls[0].rawBody != `{"systems":[]}` {
		t.Errorf("cuerpo = %s, quería systems como arreglo vacío explícito", calls[0].rawBody)
	}
	if diff.Systems == nil || diff.Granted == nil || diff.Revoked == nil {
		t.Errorf("diff = %#v, quería los tres campos no nil", diff)
	}
}

// TestM2M_ReplaceUserSystems_EmptyIDNeverReachesTheWire (R-I6).
func TestM2M_ReplaceUserSystems_EmptyIDNeverReachesTheWire(t *testing.T) {
	t.Parallel()
	f := newFakeM2M(t)
	if _, err := f.client(t, newFakeClock()).ReplaceUserSystems(context.Background(), " ", []string{"wapp.bff"}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("err = %v, quería ErrInvalidInput", err)
	}
	if exchanges, calls := f.snapshot(); exchanges != 0 || len(calls) != 0 {
		t.Errorf("canjes = %d, llamadas = %d; quería ninguno", exchanges, len(calls))
	}
}

// TestM2M_ReplaceUserSystems_MapsCodes (R-I6): los DOS 403 se separan por el code.
func TestM2M_ReplaceUserSystems_MapsCodes(t *testing.T) {
	t.Parallel()
	runCodeCases(t, []m2mCodeCase{
		{name: "ecosystem_boundary", status: http.StatusForbidden, code: "SYSTEM_ACCESS_DENIED", want: domain.ErrSystemNotAllowed},
		{name: "forbidden_scope", status: http.StatusForbidden, code: "FORBIDDEN", want: domain.ErrMachineCredentialInvalid},
		{name: "unauthorized_after_retry", status: http.StatusUnauthorized, code: "UNAUTHORIZED", want: domain.ErrMachineCredentialInvalid},
		{name: "user_not_found", status: http.StatusNotFound, code: "NOT_FOUND", want: domain.ErrNotFound},
		{name: "bad_request", status: http.StatusBadRequest, code: "INVALID_REQUEST", want: domain.ErrInvalidInput},
		{name: "rate_limited", status: http.StatusTooManyRequests, code: "TOO_MANY_REQUESTS", want: domain.ErrRateLimited},
		{name: "internal_error", status: http.StatusInternalServerError, code: "INTERNAL", want: domain.ErrIdentityUnavailable},
		{name: "service_unavailable", status: http.StatusServiceUnavailable, code: "SERVICE_UNAVAILABLE", want: domain.ErrIdentityUnavailable},
		{name: "unmapped_is_opaque", status: http.StatusTeapot, code: "TEAPOT"},
	}, func(c *iamidentity.M2MClient) error {
		_, err := c.ReplaceUserSystems(context.Background(), m2mUserID, []string{"edugo.kmp"})
		return err
	})
}

// --- Signup (R-I8) ---

// TestM2M_Signup_DoesNotPresentServiceToken (R-I8): la ruta es pública: ni canje ni portador; el
// cuerpo lleva los cuatro campos y se devuelve el id.
func TestM2M_Signup_DoesNotPresentServiceToken(t *testing.T) {
	t.Parallel()
	f := newFakeM2M(t)
	f.respondWith(m2mReply{status: http.StatusCreated, body: `{"id":"` + m2mUserID + `"}`})

	id, err := f.client(t, newFakeClock()).Signup(context.Background(), m2mEmail, "una-frase-de-acceso-larga", "Ana", "Pérez")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}
	if id != m2mUserID {
		t.Errorf("id = %q, quería %q", id, m2mUserID)
	}
	exchanges, calls := f.snapshot()
	if exchanges != 0 {
		t.Errorf("canjes = %d, quería 0: el signup es público", exchanges)
	}
	if len(calls) != 1 || calls[0].method != http.MethodPost || calls[0].path != "/api/v1/auth/signup" || calls[0].bearer != "" {
		t.Fatalf("llamadas = %+v, quería un POST /api/v1/auth/signup sin portador", calls)
	}
	want := map[string]any{"email": m2mEmail, "password": "una-frase-de-acceso-larga", "first_name": "Ana", "last_name": "Pérez"} //nolint:gosec // credencial de mentira de un test
	for k, v := range want {
		if calls[0].body[k] != v {
			t.Errorf("cuerpo[%s] = %v, quería %v", k, calls[0].body[k], v)
		}
	}
}

// TestM2M_Signup_RejectsBadInputAndBadAnswers (R-I8): un campo vacío no sale al cable; un 201 sin
// id es un error con su texto.
func TestM2M_Signup_RejectsBadInputAndBadAnswers(t *testing.T) {
	t.Parallel()
	f := newFakeM2M(t)
	c := f.client(t, newFakeClock())
	ctx := context.Background()
	for name, args := range map[string][4]string{
		"blank_email":      {" ", "pw-larga-de-sobra", "Ana", "Pérez"},
		"empty_password":   {m2mEmail, "", "Ana", "Pérez"},
		"blank_first_name": {m2mEmail, "pw-larga-de-sobra", " ", "Pérez"},
		"blank_last_name":  {m2mEmail, "pw-larga-de-sobra", "Ana", " "},
	} {
		if _, err := c.Signup(ctx, args[0], args[1], args[2], args[3]); !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("%s: err = %v, quería ErrInvalidInput", name, err)
		}
	}
	if _, calls := f.snapshot(); len(calls) != 0 {
		t.Errorf("llamadas = %d con campos vacíos, quería 0", len(calls))
	}

	f.respondWith(m2mReply{status: http.StatusCreated, body: `{}`})
	_, err := c.Signup(ctx, m2mEmail, "pw-larga-de-sobra", "Ana", "Pérez")
	if err == nil || err.Error() != "iam: identity devolvió un registro sin identificador" {
		t.Errorf("err = %v, quería «iam: identity devolvió un registro sin identificador»", err)
	}
}

// TestM2M_Signup_MapsCodes (R-I8): el 400 con details.password es la política de contraseña, y
// el motivo de identity viaja en el texto.
func TestM2M_Signup_MapsCodes(t *testing.T) {
	t.Parallel()
	signup := func(c *iamidentity.M2MClient) error {
		_, err := c.Signup(context.Background(), m2mEmail, "una-frase-de-acceso-larga", "Ana", "Pérez")
		return err
	}
	runCodeCases(t, []m2mCodeCase{
		{name: "email_taken", status: http.StatusConflict, code: "CONFLICT", want: domain.ErrEmailTaken},
		{name: "other_bad_request", status: http.StatusBadRequest, code: "INVALID_REQUEST", details: `{"email":"invalid shape"}`, want: domain.ErrInvalidInput},
		{name: "rate_limited", status: http.StatusTooManyRequests, code: "TOO_MANY_REQUESTS", want: domain.ErrRateLimited},
		{name: "internal_error", status: http.StatusInternalServerError, code: "INTERNAL", want: domain.ErrIdentityUnavailable},
		{name: "service_unavailable", status: http.StatusServiceUnavailable, code: "SERVICE_UNAVAILABLE", want: domain.ErrIdentityUnavailable},
		{name: "unmapped_is_opaque", status: http.StatusTeapot, code: "TEAPOT"},
	}, signup)

	f := newFakeM2M(t)
	f.respondWith(m2mReply{status: http.StatusBadRequest, code: "INVALID_REQUEST", details: `{"password":"must be at least 12 characters long"}`})
	err := signup(f.client(t, newFakeClock()))
	if !errors.Is(err, domain.ErrPasswordPolicy) || errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("err = %v, quería ErrPasswordPolicy (y no ErrInvalidInput)", err)
	}
	if !strings.Contains(err.Error(), "must be at least 12 characters long") {
		t.Errorf("err = %q, quería el motivo de identity en el texto", err.Error())
	}
}
