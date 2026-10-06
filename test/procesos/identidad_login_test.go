//go:build integracion

package procesos

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/identity-shared/auth/jwt"
)

// La mitad «identity-api» del doble de identity: las tres rutas de sesión de un operador (login,
// refresh, logout) con el formato del cable que consume el cliente del servidor
// (internal/iam/infra/identity/client.go y su gemelo de internal/modulos/acceso). identidad_test.go
// es la otra mitad (el JWKS y los emisores); aquí vive quién puede entrar y con qué contraseña.
//
// Sin esta mitad —y sin WAPP_IDENTITY_URL, que la señala— el servidor no tiene a quién preguntarle
// por unas credenciales y contesta «auth no disponible» a todo login de operador: el IAM local se
// eliminó. Por eso solo se enciende con opcionesServidor.IdentityLogin.
//
// Lo que el doble decide es lo que decidiría identity: 401 si el correo no existe o la contraseña
// no casa (la misma respuesta, sin oráculo), 400 si falta un campo y, para un operador con guion
// (scriptLoginStatus), el código HTTP del guion aunque la contraseña sea la buena —el 403 del System
// Gate, un 503—. Lo que NO decide es la empresa: identity no conoce tenants; esa parte es del canje.

const (
	// identidadLoginRuta, …Refresh y …Logout son las rutas de identity-api que llama el servidor.
	identidadLoginRuta        = "/api/v1/auth/login"
	identidadLoginRutaRefresh = "/api/v1/auth/refresh"
	identidadLoginRutaLogout  = "/api/v1/auth/logout"
	// identidadLoginTTL es la vigencia del Identity Token que entrega un login o un refresh.
	identidadLoginTTL = time.Hour
	// identidadLoginMaxBody acota el cuerpo que se lee de una petición.
	identidadLoginMaxBody = 16 << 10
)

// identidadLoginUser es un operador que el doble conoce: su id (el sub del Identity Token), su
// contraseña y, si status no es cero, el código HTTP con el que el doble contesta a su login aunque
// la contraseña sea la buena.
type identidadLoginUser struct {
	userID, password string
	status           int
}

// identidadLoginSession es a quién pertenece un refresh token vivo.
type identidadLoginSession struct{ userID, email, system string }

// identidadLoginCall es una llamada que el servidor le hizo al doble: la ruta, el correo y el
// system del cuerpo (vacíos en refresh y logout) y el código con el que se contestó. La contraseña
// y los tokens no se guardan.
type identidadLoginCall struct {
	Path, Email, System string
	Status              int
}

// identidadLogin es el estado de la mitad «identity-api». Seguro para uso concurrente.
type identidadLogin struct {
	t      *testing.T // test que creó el doble: solo para anotar (t.Logf) un fallo al escribir
	emisor *jwt.Manager

	mu       sync.Mutex
	users    map[string]identidadLoginUser    // por correo
	sessions map[string]identidadLoginSession // por refresh token vivo
	calls    []identidadLoginCall
}

// newIdentidadLogin devuelve el estado vacío: sin operadores, todo login da 401.
func newIdentidadLogin(t *testing.T, emisor *jwt.Manager) *identidadLogin {
	return &identidadLogin{
		t:        t,
		emisor:   emisor,
		users:    map[string]identidadLoginUser{},
		sessions: map[string]identidadLoginSession{},
	}
}

// loginEnv devuelve la variable "K=V" que le dice al servidor a quién preguntarle por las
// credenciales de un operador: WAPP_IDENTITY_URL, la raíz del doble. Slice nuevo en cada llamada.
func (i *identidad) loginEnv() []string {
	return []string{"WAPP_IDENTITY_URL=" + i.servidor.URL}
}

// registerOperator da de alta en el doble a un operador: con ese correo y esa contraseña, el login
// entrega un Identity Token cuyo sub es userID. Falla (t.Fatalf) si userID no es un UUID o falta el
// correo o la contraseña. Registrar dos veces el mismo correo lo sustituye.
func (i *identidad) registerOperator(t *testing.T, email, password, userID string) {
	t.Helper()
	i.login.register(t, email, identidadLoginUser{userID: userID, password: password})
}

// scriptLoginStatus da de alta a un operador cuyo login contesta SIEMPRE con status (un 4xx o un
// 5xx) cuando la contraseña es la buena: es el guion de «identity dice que no» por algo que no son
// las credenciales (403 = no tiene concedida la aplicación, 503 = identity caído, 400). Con una
// contraseña mala sigue dando 401. Mismos fallos que registerOperator, y si status no es un error.
func (i *identidad) scriptLoginStatus(t *testing.T, email, password, userID string, status int) {
	t.Helper()
	if status < http.StatusBadRequest {
		t.Fatalf("scriptLoginStatus: %d no es un código de error", status)
	}
	i.login.register(t, email, identidadLoginUser{userID: userID, password: password, status: status})
}

// loginCalls devuelve una copia de las llamadas que el servidor le hizo a la mitad «identity-api»,
// en orden de llegada.
func (i *identidad) loginCalls() []identidadLoginCall {
	i.login.mu.Lock()
	defer i.login.mu.Unlock()
	return slices.Clone(i.login.calls)
}

// refreshOwner dice de quién (id de usuario) es un refresh token VIVO que entregó el doble; false
// si no lo entregó él, ya se rotó o se revocó.
func (i *identidad) refreshOwner(refreshToken string) (string, bool) {
	i.login.mu.Lock()
	defer i.login.mu.Unlock()
	s, ok := i.login.sessions[refreshToken]
	return s.userID, ok
}

// register guarda al operador bajo su correo, tras validar la entrada.
func (l *identidadLogin) register(t *testing.T, email string, u identidadLoginUser) {
	t.Helper()
	if email == "" || u.password == "" || !identidadUUID.MatchString(u.userID) {
		t.Fatalf("identidad: operador inválido (correo %q, usuario %q, contraseña vacía %v)", email, u.userID, u.password == "")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.users[email] = u
}

// serve atiende la petición si es de una de las tres rutas de sesión (POST) y devuelve true; si es
// de otra ruta no escribe nada y devuelve false, para que el doble siga con el JWKS o su 404.
func (l *identidadLogin) serve(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	var body struct {
		Email        string `json:"email"`
		Password     string `json:"password"`
		System       string `json:"system"`
		RefreshToken string `json:"refresh_token"`
	}
	switch r.URL.Path {
	case identidadLoginRuta, identidadLoginRutaRefresh, identidadLoginRutaLogout:
	default:
		return false
	}
	crudo, err := io.ReadAll(io.LimitReader(r.Body, identidadLoginMaxBody))
	if err == nil {
		err = json.Unmarshal(crudo, &body)
	}
	call := identidadLoginCall{Path: r.URL.Path, Email: body.Email, System: body.System}
	var res any
	switch {
	case err != nil:
		call.Status = http.StatusBadRequest
	case r.URL.Path == identidadLoginRuta:
		call.Status, res = l.doLogin(body.Email, body.Password, body.System)
	case r.URL.Path == identidadLoginRutaRefresh:
		call.Status, res = l.doRefresh(body.RefreshToken)
	default:
		call.Status = l.doLogout(body.RefreshToken)
	}
	l.mu.Lock()
	l.calls = append(l.calls, call)
	l.mu.Unlock()
	if err := identidadLoginWrite(w, call.Status, res); err != nil {
		l.t.Logf("identidad: escribiendo la respuesta de %s: %v", r.URL.Path, err)
	}
	return true
}

// identidadLoginWrite escribe la respuesta: el JSON de la sesión en un 200, nada en un 204 y
// {"code": …} en un error (el cliente del servidor decide por el código HTTP; el code es adorno).
// Devuelve el error de serializar o de escribir (un cliente que se fue).
func identidadLoginWrite(w http.ResponseWriter, status int, res any) error {
	if status == http.StatusNoContent {
		w.WriteHeader(status)
		return nil
	}
	if status != http.StatusOK {
		res = map[string]string{"code": fmt.Sprintf("identity_double_%d", status)}
	}
	cuerpo, err := json.Marshal(res)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return fmt.Errorf("serializar la respuesta: %w", err)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, err = w.Write(cuerpo)
	return err
}

// doLogin decide un login como identity: 400 si falta un campo, 401 si el correo no existe o la
// contraseña no casa, el código del guion si el operador lo tiene, y si no un 200 con la sesión.
func (l *identidadLogin) doLogin(email, password, system string) (int, any) {
	if email == "" || password == "" || system == "" {
		return http.StatusBadRequest, nil
	}
	l.mu.Lock()
	u, ok := l.users[email]
	l.mu.Unlock()
	switch {
	case !ok || u.password != password:
		return http.StatusUnauthorized, nil
	case u.status != 0:
		return u.status, nil
	}
	return l.openSession(identidadLoginSession{userID: u.userID, email: email, system: system})
}

// doRefresh rota un refresh token vivo: lo invalida y abre una sesión nueva del mismo operador y
// el mismo system. 400 si viene vacío y 401 si no está vivo (nunca existió, ya se rotó o se revocó).
func (l *identidadLogin) doRefresh(refreshToken string) (int, any) {
	if refreshToken == "" {
		return http.StatusBadRequest, nil
	}
	l.mu.Lock()
	s, ok := l.sessions[refreshToken]
	delete(l.sessions, refreshToken)
	l.mu.Unlock()
	if !ok {
		return http.StatusUnauthorized, nil
	}
	return l.openSession(s)
}

// doLogout revoca un refresh token y contesta 204 tanto si estaba vivo como si no (anti-oráculo,
// como identity); 400 si viene vacío.
func (l *identidadLogin) doLogout(refreshToken string) int {
	if refreshToken == "" {
		return http.StatusBadRequest
	}
	l.mu.Lock()
	delete(l.sessions, refreshToken)
	l.mu.Unlock()
	return http.StatusNoContent
}

// openSession firma el Identity Token del operador para el system pedido, genera un refresh token
// aleatorio, lo deja vivo y devuelve el 200 con la sesión en el formato de identity-api
// (identity_token, NO access_token; expires_in en segundos). Si la firma o el azar fallan, 500.
func (l *identidadLogin) openSession(s identidadLoginSession) (int, any) {
	token, _, err := l.emisor.GenerateIdentityToken(jwt.IdentityTokenInput{
		UserID: s.userID, System: s.system, Email: s.email, TokenVersion: 0, TTL: identidadLoginTTL,
	})
	azar := make([]byte, 24)
	if _, rerr := rand.Read(azar); err != nil || rerr != nil {
		return http.StatusInternalServerError, nil
	}
	refresh := "rt-" + hex.EncodeToString(azar)
	l.mu.Lock()
	l.sessions[refresh] = s
	l.mu.Unlock()
	return http.StatusOK, map[string]any{
		"session_id":     "sess-" + hex.EncodeToString(azar[:8]),
		"identity_token": token,
		"refresh_token":  refresh,
		"expires_in":     int64(identidadLoginTTL / time.Second),
	}
}
