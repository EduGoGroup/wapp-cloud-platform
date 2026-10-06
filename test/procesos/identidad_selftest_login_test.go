//go:build integracion

package procesos

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/EduGoGroup/identity-shared/auth/jwt"
)

// TestArnes_IdentityLogin fija el contrato de la mitad «identity-api» del doble de identity
// (identidad_login_test.go), por su puerta HTTP y con el mismo verificador que usa el servidor: un
// login bueno entrega un Identity Token del operador para el system pedido y un refresh token vivo;
// correo desconocido y contraseña mala dan el MISMO 401; un campo vacío, 400; un operador con guion,
// su código aunque acierte la contraseña; el refresh rota (el viejo muere); el logout da 204 siempre;
// las llamadas quedan anotadas sin la contraseña; y fuera de esas tres rutas POST el doble sigue
// siendo el de antes (404). No necesita Docker.
func TestArnes_IdentityLogin(t *testing.T) {
	t.Parallel()
	c := identidadLoginCase{id: nuevaIdentidad(t), password: "clave-" + edgeAleatorioHex(t, 8)}
	c.id.registerOperator(t, identidadLoginEmail, c.password, identidadLoginUserID)
	c.id.scriptLoginStatus(t, identidadLoginGatedEmail, c.password, "0b6f1c1e-9a53-4f0e-8f2b-6d2c4a1e7b90", http.StatusForbidden)
	mv, err := jwt.NewMultiVerifierFromJWKS(identidadEmisor, jwt.JWKSOptions{URL: c.id.JWKSURL()})
	if err != nil {
		t.Fatalf("el cliente JWKS rechazó el documento del doble: %v", err)
	}
	c.mv = mv

	// En orden: cada paso parte de lo que dejó el anterior (la sesión del login, las llamadas anotadas).
	sess := c.checkGoodLogin(t)
	c.checkRejections(t)
	c.checkRefreshAndLogout(t, sess)

	// Lo anotado: la primera llamada es el login bueno, con su correo y su system; son 15 en total.
	calls := c.id.loginCalls()
	want := identidadLoginCall{Path: identidadLoginRuta, Email: identidadLoginEmail, System: identidadLoginSystem, Status: http.StatusOK}
	if len(calls) != 15 || calls[0] != want {
		t.Errorf("loginCalls: %d llamadas, la primera %+v", len(calls), calls[0])
	}
	// Fuera de las tres rutas POST, el doble de siempre: GET a la ruta de login es un 404.
	if codigo, _, _ := descargarJWKS(t, c.id.servidor.URL+identidadLoginRuta); codigo != http.StatusNotFound {
		t.Errorf("GET %s: HTTP %d, quería 404", identidadLoginRuta, codigo)
	}
	if env := c.id.loginEnv(); len(env) != 1 || env[0] != "WAPP_IDENTITY_URL="+c.id.servidor.URL {
		t.Errorf("loginEnv = %v", env)
	}
}

const (
	// identidadLoginUserID, …Email y …System son el operador bueno del selftest y la aplicación
	// para la que entra; identidadLoginGatedEmail, el del operador con guion 403.
	identidadLoginUserID     = "7f3c2a9e-4b1d-4e6a-9c58-2d1f0b8a6e34"
	identidadLoginEmail      = "ana@procesos.test"
	identidadLoginGatedEmail = "sin-app@procesos.test"
	identidadLoginSystem     = "wapp.edge"
)

// identidadLoginCase es lo que comparten los pasos del selftest: el doble, el verificador del
// servidor contra su JWKS y la contraseña (generada) de los dos operadores.
type identidadLoginCase struct {
	id       *identidad
	mv       *jwt.MultiVerifier
	password string
}

// checkGoodLogin hace un login bueno y comprueba la sesión completa: el Identity Token verifica
// con el JWKS y es del operador para el system pedido, y el refresh token queda vivo a su nombre.
// Devuelve la sesión.
func (c identidadLoginCase) checkGoodLogin(t *testing.T) identidadLoginSessionBody {
	t.Helper()
	status, sess := identidadLoginPost(t, c.id, identidadLoginRuta,
		map[string]string{"email": identidadLoginEmail, "password": c.password, "system": identidadLoginSystem})
	if status != http.StatusOK || sess.SessionID == "" || sess.RefreshToken == "" || sess.ExpiresIn != int64(identidadLoginTTL/time.Second) {
		t.Fatalf("login bueno: HTTP %d, sesión %+v", status, sess)
	}
	claims, err := c.mv.ValidateIdentityToken(sess.IdentityToken, identidadLoginSystem)
	if err != nil {
		t.Fatalf("el identity_token del login no verifica: %v", err)
	}
	if claims.Subject != identidadLoginUserID || claims.Email != identidadLoginEmail || claims.System != identidadLoginSystem {
		t.Errorf("el identity_token del login no es el del operador: %+v", claims)
	}
	if owner, ok := c.id.refreshOwner(sess.RefreshToken); !ok || owner != identidadLoginUserID {
		t.Errorf("refreshOwner = %q, %v; quería %q y vivo", owner, ok, identidadLoginUserID)
	}
	return sess
}

// checkRejections recorre los rechazos de un login, cada uno con su código y sin sesión.
func (c identidadLoginCase) checkRejections(t *testing.T) {
	t.Helper()
	body := func(email, password, system string) map[string]string {
		return map[string]string{"email": email, "password": password, "system": system}
	}
	for _, r := range []struct {
		name string
		body map[string]string
		want int
	}{
		{"contraseña mala", body(identidadLoginEmail, c.password+"x", identidadLoginSystem), http.StatusUnauthorized},
		{"correo desconocido", body("nadie@procesos.test", c.password, identidadLoginSystem), http.StatusUnauthorized},
		{"sin correo", body("", c.password, identidadLoginSystem), http.StatusBadRequest},
		{"sin contraseña", body(identidadLoginEmail, "", identidadLoginSystem), http.StatusBadRequest},
		{"sin system", body(identidadLoginEmail, c.password, ""), http.StatusBadRequest},
		{"con guion 403 y la contraseña buena", body(identidadLoginGatedEmail, c.password, identidadLoginSystem), http.StatusForbidden},
		{"con guion 403 y la contraseña mala", body(identidadLoginGatedEmail, "otra", identidadLoginSystem), http.StatusUnauthorized},
	} {
		if got, s := identidadLoginPost(t, c.id, identidadLoginRuta, r.body); got != r.want || s.IdentityToken != "" || s.RefreshToken != "" {
			t.Errorf("login %s: HTTP %d con sesión %+v; quería %d y sin sesión", r.name, got, s, r.want)
		}
	}
}

// checkRefreshAndLogout comprueba que el refresh rota (par nuevo del mismo operador y el viejo
// muere), que un refresh ya rotado, inventado o vacío se rechaza, y que el logout revoca y contesta
// 204 también la segunda vez (anti-oráculo).
func (c identidadLoginCase) checkRefreshAndLogout(t *testing.T, sess identidadLoginSessionBody) {
	t.Helper()
	refresh := func(token string) (int, identidadLoginSessionBody) {
		return identidadLoginPost(t, c.id, identidadLoginRutaRefresh, map[string]string{"refresh_token": token})
	}
	status, rotated := refresh(sess.RefreshToken)
	if status != http.StatusOK || rotated.RefreshToken == "" || rotated.RefreshToken == sess.RefreshToken {
		t.Fatalf("refresh: HTTP %d, sesión %+v", status, rotated)
	}
	if claims, err := c.mv.ValidateIdentityToken(rotated.IdentityToken, identidadLoginSystem); err != nil || claims.Subject != identidadLoginUserID {
		t.Errorf("el identity_token del refresh no es el del operador: %v", err)
	}
	if _, vivo := c.id.refreshOwner(sess.RefreshToken); vivo {
		t.Errorf("el refresh token rotado sigue vivo")
	}
	for token, want := range map[string]int{sess.RefreshToken: http.StatusUnauthorized, "rt-inventado": http.StatusUnauthorized, "": http.StatusBadRequest} {
		if got, _ := refresh(token); got != want {
			t.Errorf("refresh de %.12q: HTTP %d, quería %d", token, got, want)
		}
	}
	for range 2 {
		if got, _ := identidadLoginPost(t, c.id, identidadLoginRutaLogout, map[string]string{"refresh_token": rotated.RefreshToken}); got != http.StatusNoContent {
			t.Errorf("logout: HTTP %d, quería 204", got)
		}
	}
	if got, _ := refresh(rotated.RefreshToken); got != http.StatusUnauthorized {
		t.Errorf("refresh tras logout: HTTP %d, quería 401", got)
	}
}

// identidadLoginSessionBody es la sesión que devuelve identity-api en un login o un refresh.
type identidadLoginSessionBody struct {
	SessionID     string `json:"session_id"`
	IdentityToken string `json:"identity_token"`
	RefreshToken  string `json:"refresh_token"`
	ExpiresIn     int64  `json:"expires_in"`
}

// identidadLoginPost hace POST de body (JSON) a una ruta del doble y devuelve el código y, si el
// cuerpo de la respuesta es una sesión, la sesión (vacía en un error o un 204). Falla (t.Fatalf) si
// la petición no se pudo hacer o leer.
func identidadLoginPost(t *testing.T, id *identidad, ruta string, body map[string]string) (int, identidadLoginSessionBody) {
	t.Helper()
	crudo, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("serializar el cuerpo: %v", err)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, id.servidor.URL+ruta, bytes.NewReader(crudo))
	if err != nil {
		t.Fatalf("construir POST %s: %v", ruta, err)
	}
	req.Header.Set("Content-Type", "application/json")
	cliente := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := cliente.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", ruta, err)
	}
	leido, err := io.ReadAll(resp.Body)
	if cerr := resp.Body.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatalf("leer POST %s: %v", ruta, err)
	}
	var sess identidadLoginSessionBody
	if len(leido) > 0 {
		if err := json.Unmarshal(leido, &sess); err != nil {
			t.Fatalf("POST %s: el cuerpo no es JSON: %v\n%s", ruta, err, leido)
		}
	}
	return resp.StatusCode, sess
}
