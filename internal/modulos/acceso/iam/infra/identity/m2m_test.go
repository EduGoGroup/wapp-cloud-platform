package iamidentity_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
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
