package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

// fakeRLObserver cuenta los hits de rate-limit por ámbito (para verificar la métrica).
type fakeRLObserver struct {
	hits map[string]int
}

func (f *fakeRLObserver) RateLimitHit(scope string) {
	if f.hits == nil {
		f.hits = map[string]int{}
	}
	f.hits[scope]++
}

// okNext responde 200: el handler protegido tras el rate-limit.
func okNext() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
}

// TestLimiter_AllowsBurstThenBlocks verifica que un cubo con burst N permite N y
// bloquea el N+1 (sin refill: rate 1/hora).
func TestLimiter_AllowsBurstThenBlocks(t *testing.T) {
	l := NewLimiter(rate.Every(time.Hour), 2)
	if !l.Allow("k") {
		t.Fatal("la 1.ª petición debería permitirse (burst=2)")
	}
	if !l.Allow("k") {
		t.Fatal("la 2.ª petición debería permitirse (burst=2)")
	}
	if l.Allow("k") {
		t.Fatal("la 3.ª petición debería bloquearse (burst agotado)")
	}
	// Otra clave tiene su propio cubo.
	if !l.Allow("otra") {
		t.Fatal("una clave distinta debería tener su propio cubo")
	}
}

// TestPublicRateLimit_ByCredential_429 verifica que la API pública limita por
// credencial (api-key) y responde 429 + Retry-After al exceder, contando el hit.
func TestPublicRateLimit_ByCredential_429(t *testing.T) {
	obs := &fakeRLObserver{}
	publicLim := NewLimiter(rate.Every(time.Hour), 1) // burst 1
	loginLim := NewLimiter(rate.Every(time.Hour), 100)
	h := PublicRateLimit(okNext(), publicLim, loginLim, obs, nil)

	newReq := func(key string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/messages", nil)
		r.Header.Set("X-API-Key", key)
		return r
	}

	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, newReq("cred-A"))
	if rec1.Code != http.StatusOK {
		t.Fatalf("1.ª petición: got %d, want 200", rec1.Code)
	}

	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, newReq("cred-A"))
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("2.ª petición (misma api-key): got %d, want 429", rec2.Code)
	}
	if rec2.Header().Get("Retry-After") == "" {
		t.Error("un 429 debería incluir Retry-After")
	}
	if obs.hits["public"] != 1 {
		t.Errorf("hits public: got %d, want 1", obs.hits["public"])
	}

	// Una api-key distinta NO comparte cubo: se permite.
	rec3 := httptest.NewRecorder()
	h.ServeHTTP(rec3, newReq("cred-B"))
	if rec3.Code != http.StatusOK {
		t.Fatalf("api-key distinta: got %d, want 200", rec3.Code)
	}
}

// TestPublicRateLimit_LoginByIP_429 verifica el límite anti fuerza bruta del
// login: por IP, independiente del cubo público, con 429 al exceder.
func TestPublicRateLimit_LoginByIP_429(t *testing.T) {
	obs := &fakeRLObserver{}
	publicLim := NewLimiter(rate.Every(time.Hour), 100)
	loginLim := NewLimiter(rate.Every(time.Hour), 1) // burst 1 por IP
	h := PublicRateLimit(okNext(), publicLim, loginLim, obs, nil)

	newReq := func(ip string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
		r.RemoteAddr = ip + ":40000"
		return r
	}

	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, newReq("10.0.0.1"))
	if rec1.Code != http.StatusOK {
		t.Fatalf("1.er login: got %d, want 200", rec1.Code)
	}
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, newReq("10.0.0.1"))
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("2.º login (misma IP): got %d, want 429", rec2.Code)
	}
	if obs.hits["login"] != 1 {
		t.Errorf("hits login: got %d, want 1", obs.hits["login"])
	}
	// Otra IP tiene su propio cubo.
	rec3 := httptest.NewRecorder()
	h.ServeHTTP(rec3, newReq("10.0.0.2"))
	if rec3.Code != http.StatusOK {
		t.Fatalf("login desde otra IP: got %d, want 200", rec3.Code)
	}
}
