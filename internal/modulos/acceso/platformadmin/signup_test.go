package platformadmin_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/time/rate"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/platformadmin"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/ratelimit"
	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"
)

// Las ayudas comunes (fakeM2M, inbox, wantText, wantJSON…) viven en access_requests_test.go.

const signupOK = `{"message":"Listo. Entra con tu correo y tu clave."}`

// signupM2M es fakeM2M registrando además lo que llega a Signup: los campos y el plazo del
// contexto.
type signupM2M struct {
	*fakeM2M
	mu       sync.Mutex
	args     []signupArgs
	deadline time.Time
}

type signupArgs struct{ email, password, firstName, lastName string }

func newSignupM2M() *signupM2M {
	return &signupM2M{fakeM2M: &fakeM2M{signupUserID: uuid.NewString()}}
}

func (m *signupM2M) Signup(ctx context.Context, email, password, firstName, lastName string) (string, error) {
	m.mu.Lock()
	m.args = append(m.args, signupArgs{email, password, firstName, lastName})
	m.deadline, _ = ctx.Deadline()
	m.mu.Unlock()
	return m.fakeM2M.Signup(ctx, email, password, firstName, lastName)
}

// signupBody es el cuerpo de un alta válida con ese correo y ese origen.
func signupBody(email, origin string) map[string]string {
	return map[string]string{"email": email, "password": "Password123456!", "first_name": "Ana", "last_name": "Perez", "origin": origin}
}

// postSignup manda el alta desde remoteAddr.
func postSignup(t *testing.T, h http.Handler, body any, remoteAddr string, headers ...string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if s, ok := body.(string); ok {
		r = httptest.NewRequest(http.MethodPost, "/api/v1/signup", strings.NewReader(s))
	} else {
		r = httptest.NewRequest(http.MethodPost, "/api/v1/signup", jsonBody(t, body))
	}
	r.RemoteAddr = remoteAddr
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

// pendingOf devuelve las solicitudes pendientes de la bandeja.
func pendingOf(t *testing.T, b inbox) []platformadmin.AccessRequestItem {
	t.Helper()
	items, err := b.f.ListAccessRequests(context.Background(), "pending")
	if err != nil {
		t.Fatalf("ListAccessRequests: %v", err)
	}
	return items
}

// El alta feliz: identity registra, se concede la aplicación del origen, nace la solicitud
// pendiente con el correo normalizado y se responde el 202 constante.
func TestSignupHandler_Success(t *testing.T) {
	for _, origin := range []string{"bff", "edge"} {
		t.Run(origin, func(t *testing.T) {
			b := newInbox(t)
			m2m := newSignupM2M()
			h := platformadmin.SignupHandler(b.f, m2m, nil, false, nil)
			body := map[string]string{"email": "  Ana@X.com ", "password": " Password123456! ", "first_name": " Ana ", "last_name": " Perez ", "origin": " " + origin + " "}
			rec := postSignup(t, h, body, "192.0.2.1:1234")
			wantJSON(t, rec, http.StatusAccepted, signupOK)
			want := signupArgs{"ana@x.com", " Password123456! ", "Ana", "Perez"}
			if len(m2m.args) != 1 || m2m.args[0] != want {
				t.Fatalf("Signup recibió %+v, quiero una vez %+v (correo normalizado, nombres recortados, clave tal cual)", m2m.args, want)
			}
			items := pendingOf(t, b)
			if len(items) != 1 || items[0].UserID != m2m.signupUserID || items[0].Email != "ana@x.com" || items[0].Origin != origin {
				t.Fatalf("solicitudes = %+v, quiero una de %s con ana@x.com y %s", items, m2m.signupUserID, origin)
			}
		})
	}
}

// R-A8 (restricción 1 de la Ola B): el alta pública manda UNA sola aplicación, la de su origen, y
// no lee las que la cuenta ya tuviera (este camino reemplaza; la unión es del alta de miembros).
func TestSignupHandler_GrantsExactlyOneApplication(t *testing.T) {
	for _, origin := range []string{"bff", "edge"} {
		t.Run(origin, func(t *testing.T) {
			b := newInbox(t)
			m2m := newSignupM2M()
			m2m.current = []string{"wapp.platform", "wapp.bff", "wapp.edge"}
			rec := postSignup(t, platformadmin.SignupHandler(b.f, m2m, nil, false, nil), signupBody("ana@x.com", origin), "192.0.2.1:1")
			if rec.Code != http.StatusAccepted {
				t.Fatalf("status = %d %s", rec.Code, rec.Body.String())
			}
			if len(m2m.replaced) != 1 || !slices.Equal(m2m.replaced[0], []string{"wapp." + origin}) {
				t.Fatalf("declarado %v, quiero exactamente [[wapp.%s]]", m2m.replaced, origin)
			}
			if m2m.getCalls != 0 || m2m.ensureCalls != 0 {
				t.Fatalf("el alta pública leyó accesos (%d) o aseguró cuentas (%d)", m2m.getCalls, m2m.ensureCalls)
			}
		})
	}
}

// R-A8 (C-01): el 409 de identity NO adopta la cuenta: ni EnsureUser, ni aplicaciones, ni
// solicitud.
func TestSignupHandler_EmailTaken_NoAdoption(t *testing.T) {
	b := newInbox(t)
	m2m := newSignupM2M()
	m2m.signupErr = domain.ErrEmailTaken
	rec := postSignup(t, platformadmin.SignupHandler(b.f, m2m, nil, false, nil), signupBody("ana@x.com", "bff"), "192.0.2.1:1")
	wantText(t, rec, http.StatusConflict, "ese correo ya tiene cuenta: entra con tu clave")
	if m2m.ensureCalls != 0 || len(m2m.replaced) != 0 || m2m.getCalls != 0 {
		t.Fatalf("un correo ya registrado no se toca: ensure %d, replace %v, get %d", m2m.ensureCalls, m2m.replaced, m2m.getCalls)
	}
	if items := pendingOf(t, b); len(items) != 0 {
		t.Fatalf("un correo ya registrado no deja solicitud: %+v", items)
	}
}

// Los demás desenlaces de identity, del registro y del almacén, con su código y su texto; ninguno
// sigue adelante.
func TestSignupHandler_IdentityAndStoreFailures(t *testing.T) {
	for _, c := range []struct {
		name        string
		signupErr   error
		replaceErr  error
		storeErr    error
		code        int
		text        string
		wantReplace int
	}{
		{"PasswordPolicy_400", domain.ErrPasswordPolicy, nil, nil, http.StatusBadRequest,
			"la contraseña no cumple la política de seguridad (mínimo 12 caracteres)", 0},
		{"IdentityUnavailable_502", domain.ErrIdentityUnavailable, nil, nil, http.StatusBadGateway, "servicio de identidad no disponible", 0},
		{"OtherSignupError_500", domain.ErrRateLimited, nil, nil, http.StatusInternalServerError, "error al procesar registro", 0},
		{"ReplaceFails_502", nil, errors.New("identity 503"), nil, http.StatusBadGateway, "error al configurar aplicaciones", 1},
		{"StoreFails_500", nil, nil, errors.New("bd caída"), http.StatusInternalServerError, "error al registrar solicitud", 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := newInbox(t)
			m2m := newSignupM2M()
			m2m.signupErr, m2m.replaceErr = c.signupErr, c.replaceErr
			if c.storeErr != nil {
				b.f.Fail("CreateAccessRequest", c.storeErr)
			}
			rec := postSignup(t, platformadmin.SignupHandler(b.f, m2m, nil, false, nil), signupBody("ana@x.com", "edge"), "192.0.2.1:1")
			wantText(t, rec, c.code, c.text)
			if len(m2m.replaced) != c.wantReplace {
				t.Fatalf("ReplaceUserSystems se llamó %d veces, quiero %d", len(m2m.replaced), c.wantReplace)
			}
			if c.storeErr == nil {
				if items := pendingOf(t, b); len(items) != 0 {
					t.Fatalf("un alta fallida dejó solicitud: %+v", items)
				}
			}
		})
	}
}

// R-A8 (A-12): ningún log del alta lleva el correo ni la contraseña, en ninguna de las tres ramas
// que registran; y las tres registran algo (si no, el test no vigilaría nada).
func TestSignupHandler_NoPIIInLogs(t *testing.T) {
	const email, password = "secreto-ana@example.com", "ClaveSecreta123456!"
	for _, c := range []struct {
		name       string
		signupErr  error
		replaceErr error
		storeErr   error
		code       int
		wantInLog  string
	}{
		{"GenericSignupError", domain.ErrRateLimited, nil, nil, http.StatusInternalServerError, "signup: registro en identity falló"},
		{"ReplaceFails", nil, errors.New("identity 503"), nil, http.StatusBadGateway, "signup: concesión de sistema falló"},
		{"StoreFails", nil, nil, errors.New("bd caída"), http.StatusInternalServerError, "signup: creación de access_request falló"},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := newInbox(t)
			m2m := newSignupM2M()
			m2m.signupErr, m2m.replaceErr = c.signupErr, c.replaceErr
			if c.storeErr != nil {
				b.f.Fail("CreateAccessRequest", c.storeErr)
			}
			var buf bytes.Buffer
			log := sharedlogger.New(sharedlogger.WithWriter(&buf))
			body := map[string]string{"email": email, "password": password, "first_name": "Ana", "last_name": "Perez", "origin": "bff"}
			rec := postSignup(t, platformadmin.SignupHandler(b.f, m2m, nil, false, log), body, "192.0.2.1:1")
			if rec.Code != c.code {
				t.Fatalf("status = %d, quiero %d", rec.Code, c.code)
			}
			out := buf.String()
			if !strings.Contains(out, c.wantInLog) {
				t.Fatalf("el log no registra la rama (%q): %s", c.wantInLog, out)
			}
			if strings.Contains(out, email) || strings.Contains(strings.ToLower(out), "secreto-ana") || strings.Contains(out, password) {
				t.Fatalf("el log lleva PII (A-12): %s", out)
			}
		})
	}
}

// R-A8 (C-02): sin cliente M2M, 503 y sin pánico.
func TestSignupHandler_NoM2M_503WithoutPanic(t *testing.T) {
	b := newInbox(t)
	failAll(b.f)
	var rec *httptest.ResponseRecorder
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("SignupHandler entró en pánico con m2m nil: %v", r)
			}
		}()
		rec = postSignup(t, platformadmin.SignupHandler(b.f, nil, nil, false, nil), signupBody("ana@x.com", "bff"), "192.0.2.1:1")
	}()
	wantText(t, rec, http.StatusServiceUnavailable, "registro no disponible")
}

// R-A8 (A-09): el cuerpo se acota a 8 KiB antes de decodificar: justo en el límite pasa, un byte
// más es 400 sin llamar a identity.
func TestSignupHandler_BodyLimit(t *testing.T) {
	const limit = 8 << 10
	pad := func(total int) string {
		head := `{"email":"ana@x.com","password":"Password123456!","first_name":"Ana","last_name":"Perez","origin":"bff","x":"`
		return head + strings.Repeat("a", total-len(head)-2) + `"}`
	}
	for _, c := range []struct {
		name   string
		size   int
		code   int
		called bool
	}{
		{"AtTheLimit_Accepted", limit, http.StatusAccepted, true},
		{"OneByteOver_400", limit + 1, http.StatusBadRequest, false},
		{"OneMiB_400", 1 << 20, http.StatusBadRequest, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			body := pad(c.size)
			if len(body) != c.size {
				t.Fatalf("cuerpo de %d bytes, quería %d", len(body), c.size)
			}
			m2m := newSignupM2M()
			rec := postSignup(t, platformadmin.SignupHandler(newInbox(t).f, m2m, nil, false, nil), body, "192.0.2.1:1")
			if rec.Code != c.code || (len(m2m.args) > 0) != c.called {
				t.Fatalf("status %d, llamadas a Signup %d; quiero %d y llamada=%v", rec.Code, len(m2m.args), c.code, c.called)
			}
			if c.code == http.StatusBadRequest {
				wantText(t, rec, c.code, "cuerpo o campos de registro inválidos")
			}
		})
	}
}

// La petición entera tiene un plazo de 10 s, que hereda la llamada a identity.
func TestSignupHandler_RequestTimeout(t *testing.T) {
	m2m := newSignupM2M()
	start := time.Now()
	rec := postSignup(t, platformadmin.SignupHandler(newInbox(t).f, m2m, nil, false, nil), signupBody("ana@x.com", "bff"), "192.0.2.1:1")
	end := time.Now()
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d", rec.Code)
	}
	if m2m.deadline.IsZero() || m2m.deadline.Before(start.Add(10*time.Second)) || m2m.deadline.After(end.Add(10*time.Second)) {
		t.Fatalf("plazo del contexto = %v, quiero ahora + 10 s (entre %v y %v)", m2m.deadline, start.Add(10*time.Second), end.Add(10*time.Second))
	}
}

// Validación de campos: cada regla del contrato, con 400 y sin llamar a identity.
func TestSignupHandler_FieldValidation(t *testing.T) {
	email254 := strings.Repeat("a", 254-len("@x.com")) + "@x.com"
	for _, c := range []struct {
		name  string
		field string
		value string
		ok    bool
	}{
		{"EmptyEmail", "email", "", false},
		{"BlankEmail", "email", "   ", false},
		{"EmptyPassword", "password", "", false},
		{"EmptyFirstName", "first_name", "  ", false},
		{"EmptyLastName", "last_name", "", false},
		{"UnknownOrigin", "origin", "web", false},
		{"EmptyOrigin", "origin", "", false},
		{"Email254Bytes", "email", email254, true},
		{"Email255Bytes", "email", "a" + email254, false},
		{"FirstName100Bytes", "first_name", strings.Repeat("n", 100), true},
		{"FirstName101Bytes", "first_name", strings.Repeat("n", 101), false},
		{"LastName101Bytes", "last_name", strings.Repeat("n", 101), false},
		{"Password72Bytes", "password", strings.Repeat("p", 72), true},
		{"Password73Bytes", "password", strings.Repeat("p", 73), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			body := signupBody("ana@x.com", "bff")
			body[c.field] = c.value
			m2m := newSignupM2M()
			rec := postSignup(t, platformadmin.SignupHandler(newInbox(t).f, m2m, nil, false, nil), body, "192.0.2.1:1")
			if c.ok {
				if rec.Code != http.StatusAccepted {
					t.Fatalf("status = %d %s, quiero 202", rec.Code, rec.Body.String())
				}
				return
			}
			wantText(t, rec, http.StatusBadRequest, "cuerpo o campos de registro inválidos")
			if len(m2m.args) != 0 {
				t.Fatal("un alta inválida no llama a identity")
			}
		})
	}
	t.Run("NotJSON", func(t *testing.T) {
		m2m := newSignupM2M()
		wantText(t, postSignup(t, platformadmin.SignupHandler(newInbox(t).f, m2m, nil, false, nil), "{", "192.0.2.1:1"),
			http.StatusBadRequest, "cuerpo o campos de registro inválidos")
	})
}

// R-A8, corpus adversario del correo (reglas §5, hallazgo 40 de F1): la equivalencia con el viejo,
// medida corriendo internal/platformadmin.SignupHandler @ 9a77307 con este mismo corpus (el correo
// que llegó a Signup, o el 400 sin llamada). Fija lo que el viejo HACE, quirks incluidos: los
// espacios Unicode que strings.TrimSpace conoce se recortan, U+200B y U+FEFF no (hallazgo 14 de
// F2 para el token de invitación, aquí igual); net/mail acepta una dirección con nombre visible
// («Ana <ana@x.com>») y se guarda tal cual en minúsculas.
func TestSignupHandler_EmailCorpus_EquivalentToOld(t *testing.T) {
	for _, c := range []struct {
		in   string
		want string // "" ⇒ 400 sin llamar a identity
	}{
		{"ana@x.com", "ana@x.com"},
		{"Ana@X.COM", "ana@x.com"},
		{"  ana@x.com  ", "ana@x.com"},
		{"\u00a0Ana@X.com\u3000", "ana@x.com"},
		{"ana@x.com\u2003", "ana@x.com"},
		{"\u202fana@x.com", "ana@x.com"},
		{"ana@x.com\n", "ana@x.com"},
		{"ana@x.com\u200b", "ana@x.com\u200b"},
		{"\ufeffana@x.com", "\ufeffana@x.com"},
		{"ana\u0661\u0662@x.com", "ana\u0661\u0662@x.com"},
		{"ana@x\u0661.com", "ana@x\u0661.com"},
		{"\uff21\uff2e\uff21@x.com", "\uff41\uff4e\uff41@x.com"},
		{"\u0130nes@x.com", "ines@x.com"},
		{"ana@x", "ana@x"},
		{"Ana <ana@x.com>", "ana <ana@x.com>"},
		{`"a b"@x.com`, `"a b"@x.com`},
		{"ana@[127.0.0.1]", "ana@[127.0.0.1]"},
		{"a@@b.com", ""},
		{"ana..b@x.com", ""},
		{"ana@x..com", ""},
		{"no-es-un-correo", ""},
		{"ana @x.com", ""},
		{"ana\t@x.com", ""},
		{"@x.com", ""},
		{"ana@", ""},
		{"ana@x.com,bob@y.com", ""},
	} {
		t.Run(c.in, func(t *testing.T) {
			b := newInbox(t)
			m2m := newSignupM2M()
			rec := postSignup(t, platformadmin.SignupHandler(b.f, m2m, nil, false, nil), signupBody(c.in, "bff"), "192.0.2.1:1")
			if c.want == "" {
				wantText(t, rec, http.StatusBadRequest, "cuerpo o campos de registro inválidos")
				if len(m2m.args) != 0 {
					t.Fatalf("un correo inválido llamó a identity con %q", m2m.args[0].email)
				}
				return
			}
			if rec.Code != http.StatusAccepted || len(m2m.args) != 1 || m2m.args[0].email != c.want {
				t.Fatalf("status %d, a identity llegó %+v; quiero 202 y %q", rec.Code, m2m.args, c.want)
			}
			if items := pendingOf(t, b); len(items) != 1 || items[0].Email != c.want {
				t.Fatalf("solicitud = %+v, quiero el correo %q", items, c.want)
			}
		})
	}
}

// R-A8 (A-09): «Ana@X.com» y «ana@x.com» de la misma persona son la MISMA solicitud, con el correo
// en minúsculas.
func TestSignupHandler_EmailNormalization_SameRow(t *testing.T) {
	b := newInbox(t)
	m2m := newSignupM2M()
	h := platformadmin.SignupHandler(b.f, m2m, nil, false, nil)
	for i, email := range []string{"Ana@X.com", "ana@x.com"} {
		if rec := postSignup(t, h, signupBody(email, "bff"), "192.0.2.1:1"); rec.Code != http.StatusAccepted {
			t.Fatalf("alta %d: status %d", i, rec.Code)
		}
	}
	items := pendingOf(t, b)
	if len(items) != 1 || items[0].Email != "ana@x.com" {
		t.Fatalf("solicitudes = %+v, quiero UNA con ana@x.com", items)
	}
}

// strictLimiter deja pasar UNA petición por clave y ninguna más durante el test (ratio ~0).
func strictLimiter() *ratelimit.Limiter { return ratelimit.NewLimiter(rate.Limit(1e-9), 1) }

// R-A9 (A-06a): la clave del rate-limit. Sin trustProxy es la IP de socket y las cabeceras no
// estrenan cubo; con trustProxy, la primera de X-Forwarded-For o X-Real-IP. El freno va ANTES que
// todo lo demás, también que la guarda de m2m nil.
func TestSignupHandler_RateLimitKey(t *testing.T) {
	type req struct {
		remote  string
		headers []string
	}
	for _, c := range []struct {
		name       string
		trustProxy bool
		first      req
		second     req
		second429  bool
	}{
		{"NoTrust_SameSocket_OtherForwardedFor_Limited", false,
			req{"203.0.113.9:5555", []string{"X-Forwarded-For", "10.0.0.1"}},
			req{"203.0.113.9:6666", []string{"X-Forwarded-For", "10.0.0.2"}}, true},
		{"NoTrust_SameSocket_OtherRealIP_Limited", false,
			req{"203.0.113.9:5555", []string{"X-Real-IP", "10.0.0.1"}},
			req{"203.0.113.9:5555", []string{"X-Real-IP", "10.0.0.2"}}, true},
		{"NoTrust_OtherSocket_SameForwardedFor_Allowed", false,
			req{"203.0.113.9:5555", []string{"X-Forwarded-For", "10.0.0.1"}},
			req{"203.0.113.10:5555", []string{"X-Forwarded-For", "10.0.0.1"}}, false},
		{"NoTrust_SocketWithoutPort", false, req{"198.51.100.7", nil}, req{"198.51.100.7", nil}, true},
		{"Trust_OtherForwardedFor_Allowed", true,
			req{"203.0.113.9:5555", []string{"X-Forwarded-For", "10.0.0.1"}},
			req{"203.0.113.9:5555", []string{"X-Forwarded-For", "10.0.0.2"}}, false},
		{"Trust_FirstForwardedForTrimmed_Limited", true,
			req{"203.0.113.9:5555", []string{"X-Forwarded-For", "10.0.0.1, 10.0.0.9"}},
			req{"203.0.113.10:5555", []string{"X-Forwarded-For", " 10.0.0.1 "}}, true},
		{"Trust_RealIPWhenNoForwardedFor_Limited", true,
			req{"203.0.113.9:5555", []string{"X-Real-IP", " 10.0.0.5 "}},
			req{"203.0.113.10:5555", []string{"X-Real-IP", "10.0.0.5"}}, true},
		{"Trust_NoHeaders_Socket_Limited", true, req{"203.0.113.9:5555", nil}, req{"203.0.113.9:1", nil}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := platformadmin.SignupHandler(newInbox(t).f, newSignupM2M(), strictLimiter(), c.trustProxy, nil)
			if rec := postSignup(t, h, signupBody("ana@x.com", "bff"), c.first.remote, c.first.headers...); rec.Code != http.StatusAccepted {
				t.Fatalf("primera: status %d %s", rec.Code, rec.Body.String())
			}
			rec := postSignup(t, h, signupBody("bea@x.com", "bff"), c.second.remote, c.second.headers...)
			if c.second429 {
				wantText(t, rec, http.StatusTooManyRequests, "demasiadas solicitudes desde esta IP")
			} else if rec.Code != http.StatusAccepted {
				t.Fatalf("segunda: status %d, quiero 202 (cubo distinto)", rec.Code)
			}
		})
	}
	t.Run("LimiterBeforeNilM2M", func(t *testing.T) {
		h := platformadmin.SignupHandler(newInbox(t).f, nil, strictLimiter(), false, nil)
		wantText(t, postSignup(t, h, signupBody("ana@x.com", "bff"), "192.0.2.1:1"), http.StatusServiceUnavailable, "registro no disponible")
		wantText(t, postSignup(t, h, signupBody("ana@x.com", "bff"), "192.0.2.1:1"), http.StatusTooManyRequests, "demasiadas solicitudes desde esta IP")
	})
	t.Run("NilLimiter_NoLimit", func(t *testing.T) {
		h := platformadmin.SignupHandler(newInbox(t).f, newSignupM2M(), nil, false, nil)
		for i := range 3 {
			if rec := postSignup(t, h, signupBody("ana@x.com", "bff"), "192.0.2.1:1"); rec.Code != http.StatusAccepted {
				t.Fatalf("petición %d: status %d", i, rec.Code)
			}
		}
	})
}

// Los cuerpos del alta, con sus nombres de campo; y el centinela, con su texto.
func TestSignupDTOsAndSentinel(t *testing.T) {
	var req platformadmin.SignupRequest
	if err := json.Unmarshal([]byte(`{"email":"e","password":"p","first_name":"f","last_name":"l","origin":"o"}`), &req); err != nil {
		t.Fatalf("json: %v", err)
	}
	if req != (platformadmin.SignupRequest{Email: "e", Password: "p", FirstName: "f", LastName: "l", Origin: "o"}) {
		t.Fatalf("SignupRequest = %+v", req)
	}
	if got, err := json.Marshal(platformadmin.SignupResponse{Message: "m"}); err != nil || string(got) != `{"message":"m"}` {
		t.Fatalf("SignupResponse = %s (%v)", got, err)
	}
	if platformadmin.ErrSignupNotAvailable.Error() != "platformadmin: servicio de registro no disponible" {
		t.Fatalf("ErrSignupNotAvailable = %q", platformadmin.ErrSignupNotAvailable.Error())
	}
}
