package iamidentity_test

// Parte de m2m_test.go, partido por tamaño (F2-03, a petición de Jhoan; solo se movieron
// declaraciones): el identity de mentira (httptest) y el reloj falso.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	iamidentity "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/infra/identity"
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
	// authorization es la cabecera Authorization tal cual viajó, con su esquema.
	authorization string
	rawBody       string
	body          map[string]any
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
			bearer:        strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "),
			authorization: r.Header.Get("Authorization"), rawBody: string(raw), body: body,
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

func (f *fakeM2M) tokenBodyAndAuth() (map[string]any, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tokenBody, f.tokenAuth
}
