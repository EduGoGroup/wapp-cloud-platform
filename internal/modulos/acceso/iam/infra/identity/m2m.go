// Porta internal/iam/infra/identity/m2m.go @ 9a77307
//
// m2m.go implementa out.IdentityM2MClient: el adaptador HTTP con el que wApp habla con
// identity-api como MÁQUINA (Plan 056 · T2.4).
//
// Es HERMANO de Client (client.go) y no una ampliación suya. Client presenta las credenciales de
// una PERSONA y devuelve su sesión; este presenta la credencial de wApp —una API key que se
// CANJEA por un Service Token, nunca se presenta (identity ADR-0025)— y opera sobre el padrón
// global del grupo: asegura personas, les abre aplicaciones y, en la única ruta pública que toca,
// registra a quien trae su propia contraseña.
//
// HIGIENE (la misma que client.go, y aquí pesa más): este fichero NO loguea NADA. Por él viajan
// la API key de wApp, Service Tokens y contraseñas ajenas; los errores que devuelve nombran la
// operación y el código HTTP, nunca el material.

package iamidentity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
)

// Rutas M2M de identity-api (contrato verificado contra su router: router.go:563, :758, :738 y
// :538 respectivamente).
const (
	pathServiceToken = "/api/v1/auth/token" //nolint:gosec // ruta HTTP, no es una credencial
	pathEnsureUser   = "/api/v1/users/ensure"
	pathSignup       = "/api/v1/auth/signup"
	// pathUserSystemsFmt lleva el UUID de la persona en la RUTA, nunca en el cuerpo: identity lo
	// lee de ahí y repetirlo abajo solo crearía la posibilidad de que un día los dos digan cosas
	// distintas.
	pathUserSystemsFmt = "/api/v1/users/%s/systems"
)

// codeSystemAccessDenied es el `code` con el que identity distingue el 403 de FRONTERA DE
// ECOSISTEMA —una aplicación que no es de wApp o que no existe— del 403 de scope insuficiente,
// que llega como "FORBIDDEN". Los dos son 403 y significan cosas opuestas: uno lo arregla quien
// pidió el conjunto, el otro quien configuró la credencial.
const codeSystemAccessDenied = "SYSTEM_ACCESS_DENIED"

// tokenSafetyMargin es lo que se le resta a `expires_in` para dar por vencido el Service Token
// ANTES de que identity lo haga. Cubre el vuelo de la petición y el desfase de reloj entre las
// dos máquinas: sin margen, un token que caduca mientras la petición viaja se traduce en un 401
// que hay que recanjear, y ese recanje es justo lo que el margen ahorra en el caso normal.
const tokenSafetyMargin = 30 * time.Second

// exchangeFailureTTL es lo que dura la memoria de un canje FALLIDO. Mientras siga fresca, quien
// llegue detrás se lleva ese mismo fallo SIN volver a llamar: un identity caído cuesta UN intento
// por ventana y no uno por goroutine, que es justo lo que la ruta de canje frena por IP. Es corta
// a propósito —absorber la ráfaga, no dejar a wApp sin token cuando identity vuelve—.
const exchangeFailureTTL = time.Second

// M2MClient habla con identity-api presentando la credencial de máquina de wApp
// (out.IdentityM2MClient, y por tanto también out.UserSystemsClient). Es SEGURO para uso
// concurrente.
//
// El Service Token (canje y caché), que ningún llamante ve:
//
//   - R-I4: el canje es POST /api/v1/auth/token con la API key en el CUERPO como {"api_key"} y
//     SIN cabecera Authorization (la key se canjea, no se presenta), sin `grant_type`, `client_id`
//     ni `scope`. La respuesta trae `service_token` y `expires_in` (segundos). Las rutas de negocio
//     que lo necesitan lo presentan como `Authorization: Bearer <service_token>`; la API key no
//     viaja NUNCA como portador.
//   - R-I3: se canjea UNA vez y el token se reutiliza hasta que expira. La vida que la caché le
//     da es expires_in MENOS un margen de seguridad de 30 s (vuelo de la petición y desfase de
//     reloj), salvo que eso lo dejara nacido muerto: si expires_in <= 60 s, se usa la MITAD.
//     Vencida esa vida (según el reloj del cliente, WithClock), la siguiente llamada vuelve a
//     canjear.
//   - R-I3: si una ruta de negocio rechaza el token con 401, se hace UN recanje y UN reintento
//     con el token nuevo; si el reintento también es 401, se devuelve el error (nunca un bucle).
//     Si dos llamadas se comen el 401 del MISMO token a la vez, recanjea solo una: la otra
//     reutiliza el token nuevo. Si el recanje falla, el token rechazado sale de la caché: la
//     siguiente llamada canjea en vez de volver a presentarlo.
//   - R-I5: el canje está serializado (un turno de UNO): con la caché fría, una ráfaga de N
//     llamadas concurrentes produce UN canje y todas usan su token.
//   - R-I5: un contexto cancelado (o vencido) NO espera el canje: ya muerto antes de empezar, la
//     llamada vuelve con ctx.Err() sin tocar identity; cancelado mientras otro canjea, vuelve en
//     el acto con ctx.Err() sin esperar a que ese canje acabe.
//   - Caché NEGATIVA: un canje FALLIDO se recuerda 1 s (por el reloj del cliente). Mientras
//     dure, toda llamada que necesite token recibe ese mismo error SIN volver a canjear (un
//     identity caído cuesta un intento por ventana, no uno por goroutine). Pasado ese segundo,
//     se vuelve a canjear. Un canje que falla porque el contexto DE QUIEN canjeaba se canceló
//     NO se recuerda: no dice nada de identity, y quien llegue detrás canjea.
//   - R-I9: los fallos del canje son de la credencial de MÁQUINA o de identity, nunca de la
//     persona: 401 y 400 → domain.ErrMachineCredentialInvalid; 429 → domain.ErrRateLimited;
//     500/503 → domain.ErrIdentityUnavailable. Un canje sin `service_token` → error
//     "iam: identity devolvió un canje sin service token"; con `expires_in` <= 0 → error
//     "iam: identity devolvió un service token sin vigencia". Sin token no se llama a la ruta
//     de negocio.
//
// Comunes a todas las operaciones: un identity INALCANZABLE devuelve un error que envuelve
// domain.ErrIdentityUnavailable (R-I2, como Client); un código no mapeado sube como error opaco
// (nunca nil).
//
// Cómo lo cumple (no es contrato, es el porqué de los campos):
//
//   - slot es un turno de UNO. Quien lo consigue canjea; el resto lo espera en un select contra
//     ctx.Done(), así que una petición cancelada —o vencida— se va con su ctx.Err() en vez de
//     quedarse colgada del canje ajeno. Esto importa porque detrás de esto hay una ruta PÚBLICA:
//     sin el select, un identity lento convierte una ráfaga externa en goroutines bloqueadas.
//   - mu protege token/expiresAt y la caché negativa, y SOLO eso: se toma y se suelta alrededor
//     de la lectura o de la escritura, NUNCA durante el HTTP.
//
// Con la caché fría, una ráfaga de N peticiones produce UN canje: el primero lo hace y guarda el
// token ANTES de soltar el turno, y los demás lo encuentran ya puesto al releer. Si ese canje
// FALLA tampoco hay N reintentos: el fallo queda cacheado exchangeFailureTTL y la ráfaga se lo
// lleva sin tocar identity.
type M2MClient struct {
	baseURL string
	apiKey  string
	http    *http.Client
	// now es el reloj de las dos cachés (D-F2-6). El viejo llamaba a time.Now en cuatro sitios;
	// aquí pasan todos por él.
	now func() time.Time

	// slot es el turno para canjear. Es un canal de capacidad 1 y no un candado porque un candado
	// no se puede esperar con select: sync.Mutex.Lock() no mira el contexto de nadie.
	slot chan struct{}

	mu        sync.RWMutex
	token     string
	expiresAt time.Time
	// lastErr es el fallo del último canje y lastErrAt cuándo ocurrió: juntos son la caché
	// negativa. Se limpian en cuanto un canje sale bien.
	lastErr   error
	lastErrAt time.Time
}

var _ out.IdentityM2MClient = (*M2MClient)(nil)

// M2MOption ajusta un M2MClient al construirlo con NewM2M. Es nuevo, sin equivalente en el
// paquete viejo (D-F2-6).
type M2MOption func(*M2MClient)

// WithClock inyecta el reloj con el que el cliente decide si el Service Token cacheado sigue
// vivo y si la memoria de un canje fallido sigue fresca (D-F2-6, que la llama WithReloj; se
// escribe en inglés por E-11, como los WithClock de infra/memory). Sin esta opción, el reloj es
// time.Now. Un now nil se ignora (queda time.Now). No cambia nada observable en el cable: solo
// permite probar la caducidad sin dormir.
func WithClock(now func() time.Time) M2MOption {
	return func(c *M2MClient) {
		if now != nil {
			c.now = now
		}
	}
}

// NewM2M construye el cliente M2M contra la URL base de identity-api (la MISMA que usa Client:
// WAPP_IDENTITY_URL) y la API key de wApp, aplicando las opciones en orden.
//
// Las dos son obligatorias y SIN default (criterio de T2.4: sin WAPP_IDENTITY_API_KEY el
// constructor falla al arrancar, no devuelve un nil silencioso):
//
//   - R-I2: URL vacía → error "iam: la URL de identity-api no puede estar vacía"; sin esquema
//     http(s) → error "iam: la URL de identity-api debe ser http(s): <url entrecomillada>". La
//     barra final y los espacios se recortan.
//   - API key vacía o de solo espacios → error "iam: la credencial M2M de identity
//     (WAPP_IDENTITY_API_KEY) no puede estar vacía". La key se recorta antes de usarla.
//
// timeout <= 0 usa el de por defecto (10 s). Construir NO llama a identity: el primer canje
// ocurre con la primera operación que lo necesita.
func NewM2M(baseURL, apiKey string, timeout time.Duration, opts ...M2MOption) (*M2MClient, error) {
	base, err := normalizeBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	key := strings.TrimSpace(apiKey)
	if key == "" {
		return nil, errors.New("iam: la credencial M2M de identity (WAPP_IDENTITY_API_KEY) no puede estar vacía")
	}
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	c := &M2MClient{
		baseURL: base,
		apiKey:  key,
		http:    &http.Client{Timeout: timeout},
		now:     time.Now,
		slot:    make(chan struct{}, 1),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// String evita que un %v o %+v del cliente vuelque la API key o el Service Token: la cadena
// nombra la URL base y marca la key y el token como ocultos, sin su valor.
func (c *M2MClient) String() string {
	// fmt imprime también los campos NO exportados, así que sin esto cualquier log descuidado de
	// un struct que contenga este cliente publicaría el material.
	return fmt.Sprintf("iamidentity.M2MClient{baseURL:%s, apiKey:[oculta], serviceToken:[oculto]}", c.baseURL)
}

// GoString cierra la misma puerta para %#v, que ignora String: devuelve lo mismo que String.
func (c *M2MClient) GoString() string {
	return c.String()
}

// EnsureUser asegura la persona en el padrón global con POST /api/v1/users/ensure (con Service
// Token) y el cuerpo {"email","first_name","last_name"} —sin `password`, `systems` ni
// `system`—. Devuelve el `id`, `email` y `created` que contesta identity.
//
// Email vacío (o de solo espacios) → domain.ErrInvalidInput sin salir al cable. Una respuesta
// sin `id` → error "iam: identity devolvió un alta sin identificador". Errores: 400 →
// domain.ErrInvalidInput; 401 (tras el recanje y el reintento) y 403 →
// domain.ErrMachineCredentialInvalid (son de la credencial de wApp, no del correo); 429 →
// domain.ErrRateLimited; 500/503 → domain.ErrIdentityUnavailable.
func (c *M2MClient) EnsureUser(ctx context.Context, email, firstName, lastName string) (domain.IdentityUser, error) {
	if strings.TrimSpace(email) == "" {
		return domain.IdentityUser{}, domain.ErrInvalidInput
	}
	body := ensureUserRequest{Email: email, FirstName: firstName, LastName: lastName}
	var res ensureUserResponse
	if err := c.authorized(ctx, http.MethodPost, pathEnsureUser, body, &res); err != nil {
		return domain.IdentityUser{}, mapEnsureError(err)
	}
	if res.ID == "" {
		return domain.IdentityUser{}, errors.New("iam: identity devolvió un alta sin identificador")
	}
	return domain.IdentityUser{ID: res.ID, Email: res.Email, Created: res.Created}, nil
}

// GetUserSystems lee el conjunto de aplicaciones de la persona con GET
// /api/v1/users/{userID}/systems (con Service Token; el id va escapado en la RUTA).
//
// R-I7: la petición viaja SIN cuerpo (no declara nada). Devuelve TODAS las claves de `systems`
// en su orden; un `null` es el arreglo VACÍO, nunca nil. userID vacío (o de solo espacios) →
// domain.ErrInvalidInput sin salir al cable. Su mapeo de errores es PROPIO y no el del PUT: 400
// → domain.ErrInvalidInput; 401 y CUALQUIER 403 —también SYSTEM_ACCESS_DENIED, que en esta ruta
// no puede significar «esa aplicación no es tuya» porque no se nombra ninguna— →
// domain.ErrMachineCredentialInvalid; 404 (la persona no está en el padrón) → domain.ErrNotFound;
// 429 → domain.ErrRateLimited; 500/503 → domain.ErrIdentityUnavailable.
func (c *M2MClient) GetUserSystems(ctx context.Context, userID string) ([]string, error) {
	// Comparte URL con ReplaceUserSystems y se distingue por el MÉTODO, que es como identity lo
	// montó (su router.go:110): es el mismo recurso —el conjunto de accesos de esa persona en el
	// ecosistema de la credencial— leído en vez de escrito. Por eso reutiliza pathUserSystemsFmt
	// y su respuesta, y no estrena ninguna de las dos.
	if strings.TrimSpace(userID) == "" {
		return nil, domain.ErrInvalidInput
	}
	path := fmt.Sprintf(pathUserSystemsFmt, url.PathEscape(userID))
	var res userSystemsResponse
	if err := c.authorized(ctx, http.MethodGet, path, nil, &res); err != nil {
		return nil, mapGetUserSystemsError(err)
	}
	// Quien recorra el conjunto no tiene que distinguir el vacío del nil justo sobre la persona
	// que aún no tiene ningún acceso, que es el caso que esta lectura existe para reportar.
	return nonNilStrings(res.Systems), nil
}

// ReplaceUserSystems declara el conjunto COMPLETO de aplicaciones con PUT
// /api/v1/users/{userID}/systems (con Service Token) y el cuerpo {"systems":[...]}.
//
// R-I6: el user id va en la RUTA y NUNCA en el cuerpo; un conjunto nil viaja como `[]`
// explícito (la revocación total queda escrita en el cable como intencionada). Devuelve el diff
// de identity (`systems`, `granted`, `revoked`) con los tres campos NUNCA nil, aunque llegue
// `null` o falte la clave. userID vacío → domain.ErrInvalidInput sin salir al cable. Errores:
// 400 → domain.ErrInvalidInput; 403 con code SYSTEM_ACCESS_DENIED (frontera de ecosistema) →
// domain.ErrSystemNotAllowed; cualquier otro 403 (scope) y 401 →
// domain.ErrMachineCredentialInvalid; 404 → domain.ErrNotFound; 429 → domain.ErrRateLimited;
// 500/503 → domain.ErrIdentityUnavailable.
func (c *M2MClient) ReplaceUserSystems(ctx context.Context, userID string, systems []string) (domain.IdentitySystemsDiff, error) {
	if strings.TrimSpace(userID) == "" {
		return domain.IdentitySystemsDiff{}, domain.ErrInvalidInput
	}
	// nil se manda como `[]` y NO se omite: para identity un cuerpo sin la clave y un arreglo
	// vacío son lo mismo —«ninguna aplicación»—, pero mandarlo explícito deja escrito en el cable
	// que la revocación total fue intencionada.
	if systems == nil {
		systems = []string{}
	}
	path := fmt.Sprintf(pathUserSystemsFmt, url.PathEscape(userID))
	var res userSystemsResponse
	if err := c.authorized(ctx, http.MethodPut, path, userSystemsRequest{Systems: systems}, &res); err != nil {
		return domain.IdentitySystemsDiff{}, mapUserSystemsError(err)
	}
	// domain.IdentitySystemsDiff promete los tres campos NUNCA nil y el JSON no lo garantiza: un
	// `null` —o una clave ausente— deja el slice en nil y quien recorra el diff sin comprobarlo
	// se encuentra con una promesa rota. Se normaliza aquí, en el borde.
	return domain.IdentitySystemsDiff{
		Systems: nonNilStrings(res.Systems),
		Granted: nonNilStrings(res.Granted),
		Revoked: nonNilStrings(res.Revoked),
	}, nil
}

// Signup registra a la persona con su propia contraseña en POST /api/v1/auth/signup con
// {"email","password","first_name","last_name"} y devuelve el `id` de su cuenta.
//
// R-I8: es la ÚNICA operación que NO presenta el Service Token (la ruta es pública): ni canjea
// ni manda Authorization. Cualquiera de los cuatro campos vacío (email, nombre y apellido tras
// recortar; la contraseña tal cual) → domain.ErrInvalidInput sin salir al cable. Respuesta sin
// `id` → error "iam: identity devolvió un registro sin identificador". Errores: 400 con
// `details.password` → error que envuelve domain.ErrPasswordPolicy y lleva el motivo de
// identity en su texto; cualquier otro 400 → domain.ErrInvalidInput; 409 →
// domain.ErrEmailTaken; 429 → domain.ErrRateLimited; 500/503 → domain.ErrIdentityUnavailable.
func (c *M2MClient) Signup(ctx context.Context, email, password, firstName, lastName string) (string, error) {
	// Vive aquí aunque no presente el Service Token porque el alta es un solo acto —signup y
	// después PUT systems— y partirlo entre dos clientes solo daría dos sitios donde olvidarse
	// del segundo paso. 🔴 El 201 NO habilita el login: la cuenta nace sin ninguna fila en
	// `iam.user_systems` y el System Gate contesta 403 hasta que ReplaceUserSystems le abra
	// alguna aplicación (identity ADR-0020).
	if strings.TrimSpace(email) == "" || password == "" ||
		strings.TrimSpace(firstName) == "" || strings.TrimSpace(lastName) == "" {
		return "", domain.ErrInvalidInput
	}
	body := signupRequest{Email: email, Password: password, FirstName: firstName, LastName: lastName}
	var res signupResponse
	if err := c.do(ctx, http.MethodPost, pathSignup, "", body, &res); err != nil {
		return "", mapSignupError(err)
	}
	if res.ID == "" {
		return "", errors.New("iam: identity devolvió un registro sin identificador")
	}
	return res.ID, nil
}

// ---------------------------------------------------------------------------
// Wire format M2M de identity-api (sus nombres, no los nuestros)
// ---------------------------------------------------------------------------

// serviceTokenRequest es el cuerpo del canje. La key va en el CUERPO y NO en `Authorization`:
// ahí viaja el token, no la credencial (identity ADR-0025; dto/service_token_dto.go:30-41). No
// lleva `grant_type` ni `client_id`.
type serviceTokenRequest struct {
	APIKey string `json:"api_key"`
}

// serviceTokenResponse es el canje exitoso. El campo se llama `service_token` (NO
// `access_token`) y la vigencia viaja como `expires_in` en SEGUNDOS, truncados hacia abajo. No
// hay refresh: una máquina no tiene sesión que rotar; lo que la sostiene entre tokens es su
// propia API key.
//
// El `status` de identity —siempre "ok"— no se declara: un campo constante en todas las
// respuestas no es contrato, y declararlo solo daría algo que alguien acabaría comparando.
type serviceTokenResponse struct {
	ServiceToken string `json:"service_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

// ensureUserRequest es el cuerpo del alta create-or-attach. Tres campos, y lo que NO lleva es la
// mitad del contrato: no hay `password` (vetado por escrito), ni `systems`, ni `ecosystem`, ni
// `system` — el ecosistema sale de la credencial y lo demás se ignora en silencio
// (dto/user_dto.go:25-42).
type ensureUserRequest struct {
	Email     string `json:"email"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

type ensureUserResponse struct {
	ID      string `json:"id"`
	Email   string `json:"email"`
	Created bool   `json:"created"`
}

// userSystemsRequest declara el conjunto COMPLETO de aplicaciones. Es un reemplazo, no una suma
// (dto/user_systems_dto.go:13-22).
type userSystemsRequest struct {
	Systems []string `json:"systems"`
}

type userSystemsResponse struct {
	Systems []string `json:"systems"`
	Granted []string `json:"granted"`
	Revoked []string `json:"revoked"`
}

// signupRequest es el registro público. Los CUATRO campos son obligatorios: identity rechaza con
// 400 el que llegue vacío tras recortar espacios (handler/auth/post_signup_handler.go:53-67).
type signupRequest struct {
	Email     string `json:"email"`
	Password  string `json:"password"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

// signupResponse es deliberadamente de UN campo: los tres caminos del 201 —cuenta creada,
// adoptada o reconocida— son INDISTINGUIBLES (identity ADR-0027, dto/auth_dto.go:141-146).
type signupResponse struct {
	ID string `json:"id"`
}

// ---------------------------------------------------------------------------
// Service Token: canje y caché
// ---------------------------------------------------------------------------

// authorized ejecuta una llamada M2M con el Service Token vigente y, si identity lo rechaza con
// 401, hace UN recanje y UN reintento. Uno, no un bucle: si el token recién canjeado también se
// rechaza, el problema es la credencial y reintentar solo multiplica el tráfico contra una ruta
// que identity frena.
//
// El cuerpo se serializa dentro de do en cada intento, así que el reintento no arrastra un
// io.Reader ya consumido.
func (c *M2MClient) authorized(ctx context.Context, method, path string, body, out any) error {
	token, err := c.serviceToken(ctx, "")
	if err != nil {
		return err
	}
	err = c.do(ctx, method, path, token, body, out)
	if !isUnauthorized(err) {
		return err
	}
	// El token que se acaba de usar es el que identity rechazó: se pasa como `stale` para que un
	// recanje que otra goroutine ya hizo no se repita.
	fresh, cerr := c.serviceToken(ctx, token)
	if cerr != nil {
		return cerr
	}
	return c.do(ctx, method, path, fresh, body, out)
}

// serviceToken devuelve un Service Token vigente, canjeando la API key si hace falta.
//
// stale, cuando no está vacío, es el token que el llamante vio rechazado: solo se recanjea si la
// caché sigue teniendo ESE. Así, dos peticiones que se comen el mismo 401 a la vez producen UN
// canje y no dos.
//
// El HTTP se hace FUERA de cualquier candado y el turno se espera con select: mientras uno
// canjea, los demás o encuentran el resultado al despertar o se van con su propio ctx.Err().
// Ninguno se queda esperando un canje que ya no le sirve.
func (c *M2MClient) serviceToken(ctx context.Context, stale string) (string, error) {
	// 1) Camino feliz: la caché sirve y basta una lectura compartida.
	if token, ok := c.cachedToken(stale); ok {
		return token, nil
	}
	// Un contexto ya muerto no compite por el turno: si entrara en el select podría llevárselo
	// por sorteo y gastar un canje que nadie va a leer.
	if err := ctx.Err(); err != nil {
		return "", err
	}

	// 2) El turno. Aquí manda el ctx del llamante: si se cancela o vence esperando, se vuelve con
	//    su error en vez de sumarse a la cola.
	select {
	case c.slot <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-c.slot }()

	// 3) Con el turno en la mano, releer: mientras se esperaba, otro pudo canjear —y entonces no
	//    hay nada que hacer— o fallar —y entonces tampoco, que repetir el intento contra un
	//    identity que se acaba de ver caído es la estampida que se quiere evitar—.
	if token, ok := c.cachedToken(stale); ok {
		return token, nil
	}
	if err := c.recentFailure(); err != nil {
		return "", err
	}

	token, lifetime, err := c.exchangeAPIKey(ctx)
	if err != nil {
		c.storeFailure(ctx, err, stale)
		return "", err
	}
	c.storeToken(token, lifetime)
	return token, nil
}

// cachedToken devuelve el token cacheado cuando sirve: existe, no es el que el llamante acaba de
// ver rechazado y aún no ha vencido.
func (c *M2MClient) cachedToken(stale string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.token == "" || c.token == stale || !c.now().Before(c.expiresAt) {
		return "", false
	}
	return c.token, true
}

// recentFailure devuelve el fallo del último canje mientras siga dentro de exchangeFailureTTL, y
// nil cuando ya caducó. Es lo que impide que una ráfaga entera se convierta en una ráfaga de
// canjes contra un identity caído.
func (c *M2MClient) recentFailure() error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.lastErr == nil || !c.now().Before(c.lastErrAt.Add(exchangeFailureTTL)) {
		return nil
	}
	return c.lastErr
}

// storeToken guarda el canje bueno y borra la caché negativa: identity contestó, así que lo que
// se supiera de su último fallo ya no describe la realidad.
func (c *M2MClient) storeToken(token string, lifetime time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.token, c.expiresAt = token, c.now().Add(lifetime)
	c.lastErr, c.lastErrAt = nil, time.Time{}
}

// storeFailure apunta el fallo del canje para que la ráfaga que viene detrás se lo lleve sin
// llamar, y vacía la caché si lo que quedaba dentro es justamente el token que identity acaba de
// rechazar: mejor "sin token" que un token malo.
//
// Un ctx cancelado NO se apunta: eso no dice nada de identity —dice que ESE llamante se fue— y
// cachearlo castigaría a los demás por una prisa ajena.
func (c *M2MClient) storeFailure(ctx context.Context, err error, stale string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if stale != "" && c.token == stale {
		c.token, c.expiresAt = "", time.Time{}
	}
	if ctx.Err() != nil {
		return
	}
	c.lastErr, c.lastErrAt = err, c.now()
}

// exchangeAPIKey hace el canje y NADA más: ni candados ni caché. Devuelve el token y la vida que
// la caché se permite darle.
func (c *M2MClient) exchangeAPIKey(ctx context.Context) (string, time.Duration, error) {
	var res serviceTokenResponse
	if err := c.do(ctx, http.MethodPost, pathServiceToken, "", serviceTokenRequest{APIKey: c.apiKey}, &res); err != nil {
		return "", 0, mapExchangeError(err)
	}
	if res.ServiceToken == "" {
		return "", 0, errors.New("iam: identity devolvió un canje sin service token")
	}
	if res.ExpiresIn <= 0 {
		return "", 0, errors.New("iam: identity devolvió un service token sin vigencia")
	}
	return res.ServiceToken, usableLifetime(res.ExpiresIn), nil
}

// nonNilStrings convierte el nil que produce un `null` del JSON en el arreglo vacío que el
// dominio promete. No copia: solo sustituye el nil.
func nonNilStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// usableLifetime traduce `expires_in` en la vida que la caché se permite usar.
//
// Resta el margen de seguridad salvo cuando el token es tan corto que restarlo lo dejaría nacido
// muerto: ahí se usa la mitad. Un identity mal configurado con TTLs minúsculos degrada en más
// canjes, no en un token que nunca se reutiliza.
func usableLifetime(expiresIn int64) time.Duration {
	lifetime := time.Duration(expiresIn) * time.Second
	if lifetime > 2*tokenSafetyMargin {
		return lifetime - tokenSafetyMargin
	}
	return lifetime / 2
}

// ---------------------------------------------------------------------------
// Transporte y traducción de errores
// ---------------------------------------------------------------------------

// m2mError es el error de una respuesta no exitosa de identity en las rutas M2M: su código HTTP,
// el `code` estable de su cuerpo y los `details` por campo cuando los trae (identity
// dto/health_dto.go:11-17).
//
// Es un tipo APARTE de httpError —el de client.go— porque aquí los `details` SÍ deciden: son los
// que distinguen "la contraseña es corta" de cualquier otro 400 del signup. NO lleva el cuerpo
// completo ni nada que haya viajado en la petición.
type m2mError struct {
	status  int
	code    string
	details map[string]string
}

func (e *m2mError) Error() string {
	return fmt.Sprintf("identity respondió %d (%s)", e.status, e.code)
}

// isUnauthorized dice si el error es un 401 de identity, que es el único que justifica recanjear
// el Service Token.
func isUnauthorized(err error) bool {
	var me *m2mError
	return errors.As(err, &me) && me.status == http.StatusUnauthorized
}

// do ejecuta una petición contra identity y decodifica la respuesta en out (o la descarta si out
// es nil). bearer, si no está vacío, viaja como portador.
//
// Es hermana del post de client.go y no la misma función porque aquí hace falta el verbo (PUT,
// GET) y los `details` del error; fundirlas obligaría a tocar el cliente de personas.
func (c *M2MClient) do(ctx context.Context, method, path, bearer string, body, out any) error {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("iam: serializando la petición a identity: %w", err)
		}
		payload = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, payload)
	if err != nil {
		return fmt.Errorf("iam: construyendo la petición a identity: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		// Un identity inalcanzable NO es una credencial rechazada.
		return fmt.Errorf("%w: %s", domain.ErrIdentityUnavailable, path)
	}
	defer drainAndClose(resp.Body)

	if resp.StatusCode >= http.StatusBadRequest {
		code, details := errorPayload(resp.Body)
		return &m2mError{status: resp.StatusCode, code: code, details: details}
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("iam: respuesta de identity ilegible: %w", err)
	}
	return nil
}

// errorPayload extrae el `code` y los `details` del cuerpo de error de identity. Si no se puede
// leer, devuelve vacíos: el código HTTP ya basta para decidir.
func errorPayload(body io.Reader) (string, map[string]string) {
	var payload struct {
		Code    string            `json:"code"`
		Details map[string]string `json:"details"`
	}
	if err := json.NewDecoder(io.LimitReader(body, maxErrorBody)).Decode(&payload); err != nil {
		return "", nil
	}
	return payload.Code, payload.Details
}

// mapExchangeError traduce el fallo del canje de la API key.
//
// El 401 es UNO para tres causas —key desconocida, revocada o vencida—: identity no las distingue
// a propósito y wApp no puede inventarse la diferencia.
func mapExchangeError(err error) error {
	var me *m2mError
	if !errors.As(err, &me) {
		return err
	}
	switch me.status {
	case http.StatusUnauthorized:
		return domain.ErrMachineCredentialInvalid
	case http.StatusBadRequest:
		// api_key vacío. Con el constructor validando no debería ocurrir, así que si ocurre es de
		// la credencial, no de quien pidió el alta.
		return domain.ErrMachineCredentialInvalid
	case http.StatusTooManyRequests:
		return domain.ErrRateLimited
	case http.StatusInternalServerError, http.StatusServiceUnavailable:
		return domain.ErrIdentityUnavailable
	default:
		return err
	}
}

// mapEnsureError traduce el fallo del alta create-or-attach.
//
// No hay 404 ni 409 en esta ruta por diseño de identity: al volver sin error la cuenta existe
// siempre, y dos altas simultáneas del mismo correo acaban las dos en la MISMA cuenta
// (post_ensure_user_handler.go:66-70).
func mapEnsureError(err error) error {
	var me *m2mError
	if !errors.As(err, &me) {
		return err
	}
	switch me.status {
	case http.StatusBadRequest:
		return domain.ErrInvalidInput
	case http.StatusUnauthorized, http.StatusForbidden:
		// 401 tras el recanje y el reintento, o 403 por scope insuficiente: las dos son de la
		// credencial de wApp, no del correo que se aseguraba.
		return domain.ErrMachineCredentialInvalid
	case http.StatusTooManyRequests:
		return domain.ErrRateLimited
	case http.StatusInternalServerError, http.StatusServiceUnavailable:
		return domain.ErrIdentityUnavailable
	default:
		return err
	}
}

// mapGetUserSystemsError traduce el fallo de la LECTURA de accesos.
//
// Es una función APARTE de mapUserSystemsError y no la misma, aunque hoy sus tablas coincidan
// salvo en una línea: esa línea es el 403. En el PUT hay DOS 403 —la frontera de ecosistema y el
// scope insuficiente— y hay que separarlos por el `code`; en el GET solo cabe el segundo, y la
// razón está escrita en el propio identity (get_user_systems_handler.go): sin declaración no hay
// ninguna clave que el llamante haya nombrado, luego ninguna puede ser ajena. Compartir mapper
// haría que un SYSTEM_ACCESS_DENIED imposible en esta ruta saliera como ErrSystemNotAllowed —«esa
// aplicación no es tuya»— cuando lo que de verdad pasa es que a la credencial de wApp le falta
// `identity.users.systems.read`.
//
// El 404 es un desenlace de NEGOCIO aquí, no un fallo: dice que esa persona no está en el
// padrón, que es distinto de estar y no tener ningún acceso (200 con la lista vacía).
func mapGetUserSystemsError(err error) error {
	var me *m2mError
	if !errors.As(err, &me) {
		return err
	}
	switch me.status {
	case http.StatusBadRequest:
		// El identificador no tiene forma de UUID.
		return domain.ErrInvalidInput
	case http.StatusUnauthorized, http.StatusForbidden:
		// 401 tras el recanje y el reintento, o 403 por scope: las dos son de la credencial de
		// wApp, no de la persona que se consultaba.
		return domain.ErrMachineCredentialInvalid
	case http.StatusNotFound:
		return domain.ErrNotFound
	case http.StatusTooManyRequests:
		return domain.ErrRateLimited
	case http.StatusInternalServerError, http.StatusServiceUnavailable:
		return domain.ErrIdentityUnavailable
	default:
		return err
	}
}

// mapUserSystemsError traduce el fallo de la escritura de accesos.
//
// Los DOS 403 posibles significan cosas opuestas y se separan por el `code`:
// SYSTEM_ACCESS_DENIED es la frontera de ecosistema —lo arregla quien pidió el conjunto— y
// FORBIDDEN es scope insuficiente —lo arregla quien configuró la credencial—. Confundirlos haría
// que un error de configuración de wApp se le contara al administrador como "esa aplicación no es
// tuya".
func mapUserSystemsError(err error) error {
	var me *m2mError
	if !errors.As(err, &me) {
		return err
	}
	switch me.status {
	case http.StatusBadRequest:
		return domain.ErrInvalidInput
	case http.StatusForbidden:
		if me.code == codeSystemAccessDenied {
			return domain.ErrSystemNotAllowed
		}
		return domain.ErrMachineCredentialInvalid
	case http.StatusUnauthorized:
		return domain.ErrMachineCredentialInvalid
	case http.StatusNotFound:
		return domain.ErrNotFound
	case http.StatusTooManyRequests:
		return domain.ErrRateLimited
	case http.StatusInternalServerError, http.StatusServiceUnavailable:
		return domain.ErrIdentityUnavailable
	default:
		return err
	}
}

// mapSignupError traduce el fallo del registro público.
//
// El 400 se abre en dos por los `details`: si identity nombra el campo `password`, lo que falló
// es la POLÍTICA de contraseña —12 caracteres mínimo, 72 bytes máximo— y quien llame necesita
// poder decírselo a la persona sin adivinar. Cualquier otro 400 es entrada inválida a secas.
//
// El 409 tapa tres estados que identity NO distingue en el cable —correo con otra clave, cuenta
// bloqueada, cuenta inactiva—, así que wApp tampoco los distingue.
func mapSignupError(err error) error {
	var me *m2mError
	if !errors.As(err, &me) {
		return err
	}
	switch me.status {
	case http.StatusBadRequest:
		if reason, ok := me.details["password"]; ok {
			return fmt.Errorf("%w: %s", domain.ErrPasswordPolicy, reason)
		}
		return domain.ErrInvalidInput
	case http.StatusConflict:
		return domain.ErrEmailTaken
	case http.StatusTooManyRequests:
		// Sin Retry-After: identity no lo emite, así que no hay cuándo.
		return domain.ErrRateLimited
	case http.StatusInternalServerError, http.StatusServiceUnavailable:
		return domain.ErrIdentityUnavailable
	default:
		return err
	}
}
