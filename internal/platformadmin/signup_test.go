package platformadmin_test

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/ratelimit"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platformadmin"
	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"
	"github.com/google/uuid"
	"golang.org/x/time/rate"
)

type mockSignupM2M struct {
	signupCalled  bool
	ensureCalled  bool
	systemsCalled bool
	signupUserID  string
	systemsSet    []string
	systemsLeidos int
	signupErr     error
	ensureUserErr error
}

func (m *mockSignupM2M) EnsureUser(_ context.Context, email, firstName, lastName string) (domain.IdentityUser, error) {
	m.ensureCalled = true
	if m.ensureUserErr != nil {
		return domain.IdentityUser{}, m.ensureUserErr
	}
	return domain.IdentityUser{ID: m.signupUserID, Email: email, Created: false}, nil
}

func (m *mockSignupM2M) ReplaceUserSystems(_ context.Context, _ string, systems []string) (domain.IdentitySystemsDiff, error) {
	m.systemsCalled = true
	m.systemsSet = systems
	return domain.IdentitySystemsDiff{Systems: systems}, nil
}

// GetUserSystems completa out.IdentityM2MClient (Plan 047 · Ola B). El signup
// NO lo usa y no debe usarlo: ver TestSignup_ElAltaPublicaSigueMandandoUnaSola
// Aplicacion. `systemsLeidos` deja constancia de si alguien lo llamó.
func (m *mockSignupM2M) GetUserSystems(_ context.Context, _ string) ([]string, error) {
	m.systemsLeidos++
	return []string{}, nil
}

func (m *mockSignupM2M) Signup(_ context.Context, email, password, firstName, lastName string) (string, error) {
	m.signupCalled = true
	if m.signupErr != nil {
		return "", m.signupErr
	}
	return m.signupUserID, nil
}

func TestSignupHandler_Success_NewUser(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	repo := platformadmin.NewRepository(db)
	uid := uuid.NewString()
	m2m := &mockSignupM2M{signupUserID: uid}

	handler := platformadmin.SignupHandler(repo, m2m, nil, false, nil)
	email := fmt.Sprintf("new-user-%d@example.com", time.Now().UnixNano())
	body := fmt.Sprintf(`{"email":"%s","password":"Password123456!","first_name":"Juan","last_name":"Perez","origin":"bff"}`, email)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/signup", strings.NewReader(body))
	req.RemoteAddr = "192.0.2.1:1234"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202. Body: %s", rec.Code, rec.Body.String())
	}

	var res platformadmin.SignupResponse
	if err := json.NewDecoder(rec.Body).Decode(&res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Message != "Listo. Entra con tu correo y tu clave." {
		t.Fatalf("mensaje inesperado: %q", res.Message)
	}

	// Verificar llamada a identity
	if !m2m.signupCalled {
		t.Fatal("Signup no fue llamado")
	}
	if len(m2m.systemsSet) != 1 || m2m.systemsSet[0] != "wapp.bff" {
		t.Fatalf("sistemas otorgados inesperados: %v", m2m.systemsSet)
	}

	// Verificar fila en access_requests
	requests, err := repo.ListAccessRequests(context.Background(), "pending")
	if err != nil {
		t.Fatalf("ListAccessRequests: %v", err)
	}
	var found bool
	for _, it := range requests {
		if it.UserID == uid && it.Origin == "bff" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("solicitud no encontrada en access_requests")
	}

	// Verificar que NO se crearon filas en tenant_members ni iam_user_roles (REQ-056.8)
	verifyNoPrematureMembership(t, db, uid)
}

func verifyNoPrematureMembership(t *testing.T, db *sql.DB, uid string) {
	t.Helper()
	var memberCount, roleCount int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM public.tenant_members WHERE user_id = $1`, uid).Scan(&memberCount); err != nil {
		t.Fatalf("scan memberCount: %v", err)
	}
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM public.iam_user_roles WHERE user_id = $1`, uid).Scan(&roleCount); err != nil {
		t.Fatalf("scan roleCount: %v", err)
	}
	if memberCount != 0 || roleCount != 0 {
		t.Fatalf("el signup prematuramente asignó membresías o roles: members=%d, roles=%d", memberCount, roleCount)
	}
}

// TestSignupHandler_EmailTaken_NoAdoption invierte la versión anterior de este
// test (que se llamaba TestSignupHandler_Success_ExistingUser_SameResponse y
// EXIGÍA ensureCalled==true). C-01: el 409 de identity (caso D, ADR-0027) es
// terminal — wApp no cae a EnsureUser con su propio M2M ni concede sistemas
// sobre la cuenta ajena. Antes este test consagraba el bug; ahora fija el
// arreglo.
func TestSignupHandler_EmailTaken_NoAdoption(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	repo := platformadmin.NewRepository(db)
	uid := uuid.NewString()
	m2m := &mockSignupM2M{
		signupUserID: uid,
		signupErr:    domain.ErrEmailTaken, // el correo ya existe en identity (caso D)
	}

	handler := platformadmin.SignupHandler(repo, m2m, nil, false, nil)
	email := fmt.Sprintf("exist-user-%d@example.com", time.Now().UnixNano())
	body := fmt.Sprintf(`{"email":"%s","password":"Password123456!","first_name":"Ana","last_name":"Gomez","origin":"edge"}`, email)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/signup", strings.NewReader(body))
	req.RemoteAddr = "192.0.2.2:1234"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409. Body: %s", rec.Code, rec.Body.String())
	}

	if !m2m.signupCalled {
		t.Fatal("Signup debía haberse llamado")
	}
	// El corazón de C-01: el 409 termina la petición. Nada de EnsureUser (que
	// adoptaría la cuenta ajena) ni de ReplaceUserSystems (que se la
	// revocaría, al ser declarativo) sobre una cuenta que no es la que
	// escribió esta petición.
	if m2m.ensureCalled {
		t.Fatal("EnsureUser NO debía llamarse tras un 409 de identity (C-01: adopción de cuenta ajena)")
	}
	if m2m.systemsCalled {
		t.Fatal("ReplaceUserSystems NO debía llamarse tras un 409 de identity (C-01: revocación de sistemas ajenos)")
	}

	// Y tampoco se creó la solicitud local: caso D "no crea ni toca nada".
	requests, err := repo.ListAccessRequests(context.Background(), "pending")
	if err != nil {
		t.Fatalf("ListAccessRequests: %v", err)
	}
	for _, it := range requests {
		if it.UserID == uid {
			t.Fatalf("no debía crearse access_request para la cuenta ajena: %+v", it)
		}
	}
}

func TestSignupHandler_RateLimiter(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	repo := platformadmin.NewRepository(db)
	m2m := &mockSignupM2M{signupUserID: uuid.NewString()}

	limiter := ratelimit.NewLimiter(rate.Limit(1), 1) // 1 rps, burst 1
	handler := platformadmin.SignupHandler(repo, m2m, limiter, false, nil)

	body := `{"email":"test@example.com","password":"Password123456!","first_name":"T","last_name":"U","origin":"bff"}`

	// Primera request -> 202
	req1 := httptest.NewRequest(http.MethodPost, "/api/v1/signup", strings.NewReader(body))
	req1.RemoteAddr = "198.51.100.1:4567"
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusAccepted {
		t.Fatalf("primera request status = %d, want 202", rec1.Code)
	}

	// Segunda request inmediata desde la misma IP -> 429
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/signup", strings.NewReader(body))
	req2.RemoteAddr = "198.51.100.1:4567"
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("segunda request status = %d, want 429", rec2.Code)
	}
}

// TestSignupHandler_RateLimiter_TrustProxyFalse_IgnoresForwardedFor fija A-06a:
// sin trustProxy, X-Forwarded-For no sirve para estrenar un cubo nuevo por
// petición — la clave real es siempre la IP de socket (RemoteAddr).
func TestSignupHandler_RateLimiter_TrustProxyFalse_IgnoresForwardedFor(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	repo := platformadmin.NewRepository(db)
	m2m := &mockSignupM2M{signupUserID: uuid.NewString()}

	limiter := ratelimit.NewLimiter(rate.Limit(1), 1) // 1 rps, burst 1
	handler := platformadmin.SignupHandler(repo, m2m, limiter, false, nil)

	body := `{"email":"test2@example.com","password":"Password123456!","first_name":"T","last_name":"U","origin":"bff"}`

	req1 := httptest.NewRequest(http.MethodPost, "/api/v1/signup", strings.NewReader(body))
	req1.RemoteAddr = "203.0.113.9:5555"
	req1.Header.Set("X-Forwarded-For", "10.0.0.1")
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusAccepted {
		t.Fatalf("primera request status = %d, want 202", rec1.Code)
	}

	// Misma IP de socket, X-Forwarded-For DISTINTO: sin trustProxy debe seguir
	// contando contra el mismo cubo (203.0.113.9) y agotarlo.
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/signup", strings.NewReader(body))
	req2.RemoteAddr = "203.0.113.9:5555"
	req2.Header.Set("X-Forwarded-For", "10.0.0.2")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("segunda request (X-Forwarded-For distinto) status = %d, want 429 — la cabecera no debía eludir el freno", rec2.Code)
	}
}

// TestSignupHandler_NoM2MClient_Returns503 fija C-02: un m2m nil DE VERDAD
// (interfaz, no puntero concreto envuelto) hace que la guarda `== nil`
// devuelva 503, nunca panic.
//
// Sin BD (Tanda 6 · 2.3): el guard `m2m == nil` es el PRIMER paso de
// SignupHandler -- corta antes de tocar repo (repo.CreateAccessRequest ni
// siquiera se alcanza) -- así que corre con Repository{db: nil}, mismo
// patrón que TestHandlers_EnforcePlatformCaller_Gates (setupAdminMux(nil)).
// Antes usaba openTestDB(t): sin WAPP_TEST_DB_DSN el test se SALTABA
// (t.Skipf) y el criterio (1) de la Tanda 1 -- C-02 -- quedaba invisible en
// cualquier corrida de `go test ./...` sin Postgres.
func TestSignupHandler_NoM2MClient_Returns503(t *testing.T) {
	t.Parallel()
	repo := platformadmin.NewRepository(nil)

	handler := platformadmin.SignupHandler(repo, nil, nil, false, nil)
	body := `{"email":"whoever@example.com","password":"Password123456!","first_name":"T","last_name":"U","origin":"bff"}`

	req := httptest.NewRequest(http.MethodPost, "/api/v1/signup", strings.NewReader(body))
	req.RemoteAddr = "192.0.2.50:1234"
	rec := httptest.NewRecorder()

	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("SignupHandler entró en pánico con m2m nil: %v", r)
			}
		}()
		handler.ServeHTTP(rec, req)
	}()

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503. Body: %s", rec.Code, rec.Body.String())
	}
}

// TestSignupHandler_BodyTooLarge_Returns400 fija A-09: MaxBytesReader corta un
// cuerpo por encima del límite ANTES de que json.Decode intente reservar
// memoria para él.
//
// Sin BD (Tanda 6 · 2.3): el rechazo ocurre en json.Decode, antes de llegar a
// registerIdentityUser o a repo.CreateAccessRequest -- ver el m2m.signupCalled
// == false más abajo, que ya lo demostraba. Repository{db: nil} basta.
func TestSignupHandler_BodyTooLarge_Returns400(t *testing.T) {
	t.Parallel()
	repo := platformadmin.NewRepository(nil)
	m2m := &mockSignupM2M{signupUserID: uuid.NewString()}

	handler := platformadmin.SignupHandler(repo, m2m, nil, false, nil)

	// 1 MiB de relleno en un campo de sobra, muy por encima del límite de 8 KiB.
	huge := strings.Repeat("a", 1<<20)
	body := fmt.Sprintf(`{"email":"big@example.com","password":"Password123456!","first_name":"%s","last_name":"U","origin":"bff"}`, huge)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/signup", strings.NewReader(body))
	req.RemoteAddr = "192.0.2.60:1234"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400. Body: %s", rec.Code, rec.Body.String())
	}
	if m2m.signupCalled {
		t.Fatal("Signup no debía llamarse: el cuerpo se rechaza antes de decodificar")
	}
}

// TestSignupHandler_InvalidEmail_Returns400 fija A-09: un correo sin '@' se
// rechaza en la validación, sin gastar una llamada M2M.
//
// Sin BD (Tanda 6 · 2.3): validateSignupRequest corta antes de
// registerIdentityUser o repo.CreateAccessRequest -- ver el m2m.signupCalled
// == false más abajo. Repository{db: nil} basta.
func TestSignupHandler_InvalidEmail_Returns400(t *testing.T) {
	t.Parallel()
	repo := platformadmin.NewRepository(nil)
	m2m := &mockSignupM2M{signupUserID: uuid.NewString()}

	handler := platformadmin.SignupHandler(repo, m2m, nil, false, nil)
	body := `{"email":"no-es-un-correo","password":"Password123456!","first_name":"T","last_name":"U","origin":"bff"}`

	req := httptest.NewRequest(http.MethodPost, "/api/v1/signup", strings.NewReader(body))
	req.RemoteAddr = "192.0.2.70:1234"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400. Body: %s", rec.Code, rec.Body.String())
	}
	if m2m.signupCalled {
		t.Fatal("Signup no debía llamarse con un correo inválido")
	}
}

// TestSignupHandler_EmailNormalization_SameRow fija A-09: "Ana@X.com" y
// "ana@x.com" deben producir la MISMA fila en access_requests (mismo
// user_id, correo normalizado a minúsculas), no dos filas con grafías
// distintas.
func TestSignupHandler_EmailNormalization_SameRow(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	repo := platformadmin.NewRepository(db)
	uid := uuid.NewString()
	m2m := &mockSignupM2M{signupUserID: uid}
	handler := platformadmin.SignupHandler(repo, m2m, nil, false, nil)

	suffix := time.Now().UnixNano()
	mixedCase := fmt.Sprintf("Ana%d@X.com", suffix)
	lowerCase := fmt.Sprintf("ana%d@x.com", suffix)

	send := func(email, remoteAddr string) int {
		body := fmt.Sprintf(`{"email":"%s","password":"Password123456!","first_name":"Ana","last_name":"Gomez","origin":"bff"}`, email)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/signup", strings.NewReader(body))
		req.RemoteAddr = remoteAddr
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}

	if code := send(mixedCase, "192.0.2.80:1"); code != http.StatusAccepted {
		t.Fatalf("primer alta status = %d, want 202", code)
	}
	if code := send(lowerCase, "192.0.2.81:1"); code != http.StatusAccepted {
		t.Fatalf("segundo alta status = %d, want 202", code)
	}

	requests, err := repo.ListAccessRequests(context.Background(), "pending")
	if err != nil {
		t.Fatalf("ListAccessRequests: %v", err)
	}
	var matches int
	var gotEmail string
	for _, it := range requests {
		if it.UserID == uid {
			matches++
			gotEmail = it.Email
		}
	}
	if matches != 1 {
		t.Fatalf("esperaba UNA sola fila para %s, encontré %d", uid, matches)
	}
	wantEmail := strings.ToLower(mixedCase)
	if gotEmail != wantEmail {
		t.Fatalf("email guardado = %q, want %q (normalizado a minúsculas)", gotEmail, wantEmail)
	}
}

// TestSignupHandler_NoPIIInLogs fija A-12: ningún log.Warn de signup.go lleva
// el correo de quien se registra. Fuerza la rama genérica de error (ni
// ErrPasswordPolicy, ni ErrEmailTaken, ni ErrIdentityUnavailable) para que
// dispare el único log.Warn que queda con "error" en signup.go y comprueba
// que el correo no aparece en el texto emitido.
//
// Sin BD (Tanda 6 · 2.3): domain.ErrRateLimited hace que registerIdentityUser
// devuelva 500 ANTES de llegar a repo.CreateAccessRequest -- el mismo error
// genérico que dispara el log.Warn es lo que corta el flujo. Repository{db:
// nil} basta.
func TestSignupHandler_NoPIIInLogs(t *testing.T) {
	t.Parallel()
	repo := platformadmin.NewRepository(nil)
	m2m := &mockSignupM2M{signupErr: domain.ErrRateLimited}

	var buf bytes.Buffer
	log := sharedlogger.New(sharedlogger.WithWriter(&buf))

	handler := platformadmin.SignupHandler(repo, m2m, nil, false, log)
	email := fmt.Sprintf("secreto-%d@example.com", time.Now().UnixNano())
	body := fmt.Sprintf(`{"email":"%s","password":"Password123456!","first_name":"T","last_name":"U","origin":"bff"}`, email)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/signup", strings.NewReader(body))
	req.RemoteAddr = "192.0.2.90:1234"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500. Body: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(buf.String(), email) {
		t.Fatalf("el correo apareció en el log (A-12, CERO PII): %s", buf.String())
	}
}

// ---------------------------------------------------------------------------
// Regresión de la Ola B del Plan 047 (restricción 1)
// ---------------------------------------------------------------------------

// bdMuerta es un *sql.DB que no conecta con nada. Existe para que esta regresión
// NO dependa de una base: los tests de este fichero que usan openTestDB se SALTAN
// cuando no hay WAPP_TEST_DB_DSN, y un candado que solo cierra cuando alguien
// exporta una variable de entorno no es un candado.
//
// Le vale con fallar: los pasos 1 y 2 del handler (identity) ocurren ANTES del
// paso 3 (la fila en access_requests), así que cuando la base revienta el
// conjunto que viajó a identity ya está registrado en el doble.
func bdMuerta(t *testing.T) *sql.DB {
	t.Helper()
	db := sql.OpenDB(conectorMuerto{})
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Logf("cerrando la bd de mentira: %v", err)
		}
	})
	return db
}

type conectorMuerto struct{}

func (conectorMuerto) Connect(context.Context) (driver.Conn, error) {
	return nil, errors.New("bd de mentira: aquí no se conecta nadie")
}
func (conectorMuerto) Driver() driver.Driver { return driverMuerto{} }

type driverMuerto struct{}

func (driverMuerto) Open(string) (driver.Conn, error) {
	return nil, errors.New("bd de mentira: aquí no se abre nadie")
}

// TestSignup_ElAltaPublicaSigueMandandoUnaSolaAplicacion es el candado de la
// restricción 1 de la Ola B.
//
// 🔴 QUÉ PROTEGE. El alta de un MIEMBRO pasó a hacer UNIÓN sobre los accesos que
// la persona ya tiene, y esa unión vive en el usecase de membresías —no en el
// cliente M2M, que sigue siendo un reemplazo declarativo—. Si algún día alguien
// «unificara» los dos caminos moviendo la unión al cliente, este alta pública
// empezaría a leer lo que la cuenta ya tenía y a sumarle la suya: un registro con
// `origin=edge` heredaría `wapp.bff` —la consola web— sin que nadie lo decida, y
// al revés. El conjunto que sale de aquí tiene que seguir siendo de UN elemento,
// el de su propio origen, y nada más.
//
// Por eso el assert es sobre la longitud EXACTA además del contenido: un
// `contiene "wapp.edge"` pasaría igual con los dos dentro.
func TestSignup_ElAltaPublicaSigueMandandoUnaSolaAplicacion(t *testing.T) {
	t.Parallel()

	for _, origen := range []string{"bff", "edge"} {
		t.Run(origen, func(t *testing.T) {
			t.Parallel()
			m2m := &mockSignupM2M{signupUserID: uuid.NewString()}
			handler := platformadmin.SignupHandler(platformadmin.NewRepository(bdMuerta(t)), m2m, nil, false, nil)

			body := fmt.Sprintf(`{"email":"regresion-%s@example.com","password":"Password123456!",`+
				`"first_name":"Ana","last_name":"Perez","origin":"%s"}`, origen, origen)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/signup", strings.NewReader(body))
			req.RemoteAddr = "192.0.2.9:1234"
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			// El paso 3 falla contra la bd de mentira; lo que se afirma aquí es lo
			// que ya pasó en el paso 2. Se comprueba el código para que un cambio que
			// mueva la escritura de systems DESPUÉS de la fila local salga rojo aquí
			// en vez de dejar el assert de abajo mirando un doble sin llamar.
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("code = %d, quiero 500 (la fila local no puede escribirse): body %s", rec.Code, rec.Body.String())
			}
			if !m2m.systemsCalled {
				t.Fatal("el alta pública no llamó a ReplaceUserSystems: sin esa llamada este candado no vigila nada")
			}
			if len(m2m.systemsSet) != 1 || m2m.systemsSet[0] != "wapp."+origen {
				t.Fatalf("el alta pública mandó %v; quiero exactamente [wapp.%s] y NADA más.\n"+
					"La unión de la Ola B es del usecase de membresías; si llega hasta aquí, "+
					"un registro por el Edge hereda la consola web (y al revés).", m2m.systemsSet, origen)
			}
			if m2m.systemsLeidos != 0 {
				t.Errorf("el alta pública leyó los accesos previos %d veces: este camino REEMPLAZA, no une",
					m2m.systemsLeidos)
			}
		})
	}
}
