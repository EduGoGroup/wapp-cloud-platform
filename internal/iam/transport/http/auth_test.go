package iamhttp_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	identityrbac "github.com/EduGoGroup/identity-shared/auth/rbac"
	sharedjwt "github.com/EduGoGroup/wapp-shared/auth/jwt"
	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/infra/memory"
	iamhttp "github.com/EduGoGroup/wapp-cloud-platform/internal/iam/transport/http"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/usecase"
)

const (
	tSecret = "material-de-firma-hs256-para-los-handlers"
	tIssuer = "wapp-iam-test"
	tTenant = "11111111-1111-1111-1111-111111111111"
	tEmail  = "op@tenant.example"
)

// harness arma el mux con los handlers reales sobre usecases con store en
// memoria (tokens reales) y el canje APAGADO, para ejercitar de paso el 503 del
// modo dual.
type harness struct {
	mux   *http.ServeMux
	store *memory.Store
	jwt   *sharedjwt.JWTManager
}

func newHarness(t *testing.T) harness {
	t.Helper()
	store := memory.NewStore()
	jwt := sharedjwt.NewJWTManager(tSecret, tIssuer)

	verifier, err := usecase.NewContextTokenService(jwt)
	if err != nil {
		t.Fatalf("NewContextTokenService: %v", err)
	}

	mux := http.NewServeMux()
	// Sin canje (nil): este harness ejercita /verify y el 503 del exchange con el
	// modo dual apagado.
	iamhttp.Register(mux, verifier, nil, nil)
	return harness{mux: mux, store: store, jwt: jwt}
}

func ptr(s string) *string { return &s }

// contextToken emite un Context Token de wApp para el sujeto dado. Ya no se
// obtiene haciendo login: wApp no valida contraseñas desde la Ola 3 y desde la
// Ola 5 no tiene siquiera con qué.
func (h harness) contextToken(t *testing.T, userID string) string {
	t.Helper()
	tok, _, err := h.jwt.GenerateToken(userID, tTenant, []string{"operator"},
		identityrbac.Grants{Allow: []string{"flows.*"}}, time.Hour)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	return tok
}

// mustJSON deserializa data en v y falla el test si el JSON no parsea.
func mustJSON(t *testing.T, data []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("unmarshal: %v (body %s)", err, string(data))
	}
}

// do ejecuta una request JSON contra el mux y devuelve el recorder.
func (h harness) do(t *testing.T, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, r)
	return rec
}

// TestElIAMViejoYaNoEstaEnElCable es la prueba de REQ-A1 en su forma correcta:
// por AUSENCIA. Que el login del BFF funcione no demuestra que el IAM propio
// murió —demuestra que el nuevo vive—; lo que lo demuestra es que las rutas que
// validaban contraseñas, rotaban sesiones y canjeaban api-keys ya no existen.
//
// 404 y no 401: un 401 significaría que la ruta sigue ahí y ha rechazado la
// credencial, que es exactamente el estado del que venimos (design.md Ola 5 §6:
// el 2026-08-02 /login todavía respondía 401 a credenciales falsas).
func TestElIAMViejoYaNoEstaEnElCable(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	muertas := []struct {
		path string
		body string
	}{
		{"/api/v1/auth/login", `{"email":"` + tEmail + `","password":"la-que-sea"}`},
		{"/api/v1/auth/refresh", `{"refresh_token":"rft_loquesea"}`},
		{"/api/v1/auth/logout", `{"refresh_token":"rft_loquesea"}`},
		{"/api/v1/auth/token", ``},
	}
	for _, m := range muertas {
		t.Run(m.path, func(t *testing.T) {
			t.Parallel()
			rec := h.do(t, http.MethodPost, m.path, m.body, map[string]string{"X-API-Key": "la-que-sea"})
			if rec.Code != http.StatusNotFound {
				t.Fatalf("%s: code = %d, want 404 (la ruta debía DESAPARECER, no rechazar; body %s)",
					m.path, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestVerify_ValidAndInvalid(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	userID := uuid.NewString()
	token := h.contextToken(t, userID)

	// Válido (token en el cuerpo).
	rec := h.do(t, http.MethodPost, "/api/v1/auth/verify", `{"token":"`+token+`"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("verify code = %d, want 200", rec.Code)
	}
	var v struct {
		Valid    bool   `json:"valid"`
		TenantID string `json:"tenant_id"`
		Subject  string `json:"subject"`
	}
	mustJSON(t, rec.Body.Bytes(), &v)
	if !v.Valid || v.TenantID != tTenant || v.Subject != userID {
		t.Fatalf("verify válido inesperado: %+v", v)
	}

	// Inválido → 200 con valid=false (no 401).
	bad := h.do(t, http.MethodPost, "/api/v1/auth/verify", `{"token":"not-a-token"}`, nil)
	if bad.Code != http.StatusOK {
		t.Fatalf("verify(inválido) code = %d, want 200", bad.Code)
	}
	var bv struct {
		Valid bool `json:"valid"`
	}
	mustJSON(t, bad.Body.Bytes(), &bv)
	if bv.Valid {
		t.Fatal("verify(inválido) debe ser valid=false")
	}
}

func TestVerify_HeaderBearer(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	token := h.contextToken(t, uuid.NewString())

	rec := h.do(t, http.MethodGet, "/api/v1/auth/verify", "",
		map[string]string{"Authorization": "Bearer " + token})
	if rec.Code != http.StatusOK {
		t.Fatalf("verify(header) code = %d, want 200", rec.Code)
	}
}

func TestVerify_SinTokenEs400(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	rec := h.do(t, http.MethodPost, "/api/v1/auth/verify", `{}`, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("verify sin token: code = %d, want 400", rec.Code)
	}
}
