package httpapi_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
	"github.com/EduGoGroup/wapp-shared/auth"
)

// mwActiveKid es el key id de la clave ES256 activa en el MultiVerifier bajo prueba.
const mwActiveKid = "es256-test"

// newES256MW arma el middleware exactamente como lo cablea buildPublicAPIServer
// tras el retiro de HS256 del plano de usuario (Plan 028 · T4, ADR-0019): un
// MultiVerifier con la ÚNICA entrada ES256 por su `kid` y SIN default, de modo
// que cualquier token HS256 de usuario (con o sin `kid`) se rechaza. Devuelve
// además la privada ES256 para poder emitir tokens de prueba.
func newES256MW(t *testing.T) (*httpapi.Middleware, *ecdsa.PrivateKey) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generando clave ES256: %v", err)
	}
	mv, err := auth.NewMultiVerifier(
		mwIssuer,
		map[string]auth.VerifierKey{mwActiveKid: auth.ES256VerifierKey(&priv.PublicKey)},
		auth.VerifierKey{},
	)
	if err != nil {
		t.Fatalf("NewMultiVerifier: %v", err)
	}
	svc := fakeM2M{svcJWT: auth.NewServiceJWTManager(mwSecret, mwIssuer, mwAudience)}
	return httpapi.NewMiddleware(mv, svc, nil), priv
}

func es256UserToken(t *testing.T, priv *ecdsa.PrivateKey, kid string) string {
	t.Helper()
	mgr, err := auth.NewJWTManagerES256(priv, mwIssuer)
	if err != nil {
		t.Fatalf("NewJWTManagerES256: %v", err)
	}
	tok, _, err := mgr.WithKid(kid).GenerateToken(mwUser, mwTenant, []string{"operator"}, auth.Grants{Allow: []string{"flows.*"}}, time.Hour)
	if err != nil {
		t.Fatalf("GenerateToken ES256: %v", err)
	}
	return tok
}

// hs256UserToken firma un token HS256 legacy con el secreto viejo (sin `kid`),
// tal como los emitía el plano de usuario antes del corte a ES256.
func hs256UserToken(t *testing.T) string {
	t.Helper()
	tok, _, err := auth.NewJWTManager(mwSecret, mwIssuer).GenerateToken(mwUser, mwTenant, []string{"operator"}, auth.Grants{Allow: []string{"flows.*"}}, time.Hour)
	if err != nil {
		t.Fatalf("GenerateToken HS256: %v", err)
	}
	return tok
}

// hs256UserTokenWithKid firma un token HS256 pero le estampa el `kid` dado (para
// forjar el ataque de confusión de algoritmos: HS256 disfrazado del kid de ES256).
func hs256UserTokenWithKid(t *testing.T, kid string) string {
	t.Helper()
	tok, _, err := auth.NewJWTManager(mwSecret, mwIssuer).WithKid(kid).GenerateToken(mwUser, mwTenant, []string{"operator"}, auth.Grants{Allow: []string{"flows.*"}}, time.Hour)
	if err != nil {
		t.Fatalf("GenerateToken HS256+kid: %v", err)
	}
	return tok
}

func doAuth(t *testing.T, mw *httpapi.Middleware, bearer string) int {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/x", nil)
	req.Header.Set("Authorization", "Bearer "+bearer)
	mw.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)
	return rec.Code
}

// TestES256MW_ActiveKid_Passes: los tokens ES256 con el `kid` activo los valida
// la entrada correspondiente.
func TestES256MW_ActiveKid_Passes(t *testing.T) {
	t.Parallel()
	mw, priv := newES256MW(t)
	tok := es256UserToken(t, priv, mwActiveKid)
	if code := doAuth(t, mw, tok); code != http.StatusOK {
		t.Fatalf("token ES256 con kid activo: code = %d, want 200", code)
	}
}

// TestES256MW_HS256Legacy_Rejected: un token HS256 legacy (firmado con el secreto
// viejo, sin `kid`) se RECHAZA — el MultiVerifier ya no tiene default HS256 (T4).
func TestES256MW_HS256Legacy_Rejected(t *testing.T) {
	t.Parallel()
	mw, _ := newES256MW(t)
	tok := hs256UserToken(t)
	if code := doAuth(t, mw, tok); code != http.StatusUnauthorized {
		t.Fatalf("HS256 legacy sin kid: code = %d, want 401 (HS256 retirado del plano de usuario)", code)
	}
}

// TestES256MW_UnknownKid_Rejected: un `kid` que no está en el mapa se rechaza.
func TestES256MW_UnknownKid_Rejected(t *testing.T) {
	t.Parallel()
	mw, priv := newES256MW(t)
	tok := es256UserToken(t, priv, "es256-desconocido")
	if code := doAuth(t, mw, tok); code != http.StatusUnauthorized {
		t.Fatalf("token con kid desconocido: code = %d, want 401", code)
	}
}

// TestES256MW_ForgedHS256WithES256Kid_Rejected: un HS256 forjado con el `kid` de
// la entrada ES256 se rechaza (guard anti alg-confusion de extremo a extremo).
func TestES256MW_ForgedHS256WithES256Kid_Rejected(t *testing.T) {
	t.Parallel()
	mw, _ := newES256MW(t)
	forged := hs256UserTokenWithKid(t, mwActiveKid)
	if code := doAuth(t, mw, forged); code != http.StatusUnauthorized {
		t.Fatalf("HS256 forjado con kid ES256: code = %d, want 401 (alg-confusion)", code)
	}
}
