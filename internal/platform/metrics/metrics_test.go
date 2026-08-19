package metrics

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib" // registra el driver "pgx": abre un *sql.DB real SIN conectar.
)

// scrape devuelve el cuerpo de /metrics del registry propio.
func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.PromHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics: got %d, want 200", rec.Code)
	}
	return rec.Body.String()
}

// TestInstrumentHTTP_CountsRequests verifica que /metrics expone los contadores
// con el PATRÓN de ruta como etiqueta, y que el contador de logins ya no existe
// (murió con el login en la Ola 5 del Plan 003 de identity).
func TestInstrumentHTTP_CountsRequests(t *testing.T) {
	m := New()

	mux := http.NewServeMux()
	mux.Handle("/api/v1/auth/exchange", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	mux.Handle("/api/v1/flows/{id}", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	h := m.InstrumentHTTP("public", mux)

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/v1/auth/exchange", nil))
	// Ruta con {id}: el patrón (no el valor) debe ser la etiqueta.
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/flows/abc-123", nil))

	body := scrape(t, m)

	if !strings.Contains(body, "wapp_http_requests_total") {
		t.Fatal("falta wapp_http_requests_total en /metrics")
	}
	if strings.Contains(body, "wapp_auth_logins_total") {
		t.Errorf("wapp_auth_logins_total sigue publicándose: wApp ya no valida credenciales\n%s", body)
	}
	// CERO PII: la etiqueta route usa el PATRÓN {id}, nunca el valor real "abc-123".
	if strings.Contains(body, "abc-123") {
		t.Error("la métrica NO debe exponer el valor real del path (PII/cardinalidad)")
	}
	if !strings.Contains(body, `/api/v1/flows/{id}`) {
		t.Errorf("la etiqueta route debería ser el patrón /api/v1/flows/{id}:\n%s", body)
	}
}

// TestRateLimitAndReceiptCounters verifica los contadores auxiliares.
func TestRateLimitAndReceiptCounters(t *testing.T) {
	m := New()
	m.RateLimitHit("public")
	m.RateLimitHit("login")
	m.Receipt("delivered")
	m.Receipt("read")
	m.Receipt("delivered")

	body := scrape(t, m)
	for _, want := range []string{
		`wapp_ratelimit_hits_total{scope="public"} 1`,
		`wapp_ratelimit_hits_total{scope="login"} 1`,
		`wapp_receipts_total{status="delivered"} 2`,
		`wapp_receipts_total{status="read"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("falta %q en /metrics:\n%s", want, body)
		}
	}
}

// TestNilSafe garantiza que los métodos sobre un *Metrics nil no rompen.
func TestNilSafe(t *testing.T) {
	var m *Metrics
	m.RateLimitHit("public")
	m.Receipt("read")
	h := m.InstrumentHTTP("public", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("nil metrics no debería alterar el handler: got %d", rec.Code)
	}
}

// openIdleDB abre un *sql.DB REAL contra un DSN que nunca se usa. sql.Open no
// abre socket: el pool solo conecta en la primera consulta, y estos tests jamás
// consultan. Así db.Stats() devuelve el estado auténtico del pool (que es lo que
// T4.3 publica) sin depender de que haya un PostgreSQL vivo.
func openIdleDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", "postgres://u:p@127.0.0.1:1/nodb")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Logf("cerrando el pool de test: %v", cerr)
		}
	})
	return db
}

// dbStatsSeries son las seis series que T4.3 exige en /metrics. El criterio del
// plan se verifica con `grep wapp_db_`; aquí se comprueba una por una para que
// el fallo diga CUÁL falta.
var dbStatsSeries = []string{
	"wapp_db_wait_count",
	"wapp_db_wait_duration_seconds",
	"wapp_db_max_idle_closed",
	"wapp_db_in_use",
	"wapp_db_idle",
	"wapp_db_max_open",
}

// TestRegisterDBStats_PublicaLasSeisSeries verifica que tras cablear el pool las
// seis series aparecen en el scrape (y que antes de cablearlo NO aparecen: si
// salieran igual, el test no estaría probando el registro).
func TestRegisterDBStats_PublicaLasSeisSeries(t *testing.T) {
	m := New()
	if body := scrape(t, m); strings.Contains(body, "wapp_db_") {
		t.Fatal("sin RegisterDBStats no debería haber ninguna serie wapp_db_ en /metrics")
	}

	if err := m.RegisterDBStats(openIdleDB(t)); err != nil {
		t.Fatalf("RegisterDBStats: %v", err)
	}

	body := scrape(t, m)
	for _, want := range dbStatsSeries {
		if !strings.Contains(body, want) {
			t.Errorf("falta la serie %q en /metrics", want)
		}
	}
}

// TestRegisterDBStats_LeeElDatoDeVerdad es el assert que distingue "publiqué la
// serie" de "publiqué el DATO": fija un techo raro de conexiones y exige verlo
// en el scrape. Si el valor viniera de una constante en vez de db.Stats(), este
// test se pone rojo (mutación comprobada en T4.3).
func TestRegisterDBStats_LeeElDatoDeVerdad(t *testing.T) {
	db := openIdleDB(t)
	db.SetMaxOpenConns(7)

	m := New()
	if err := m.RegisterDBStats(db); err != nil {
		t.Fatalf("RegisterDBStats: %v", err)
	}

	body := scrape(t, m)
	if !strings.Contains(body, "wapp_db_max_open 7") {
		t.Errorf("el scrape no refleja SetMaxOpenConns(7): la serie no está leyendo db.Stats()\n%s", body)
	}
	// El pool está ocioso y nadie ha consultado: ninguna espera todavía. Es el
	// "antes" contra el que T5.5 comparará la carga real.
	for _, want := range []string{"wapp_db_wait_count 0", "wapp_db_in_use 0"} {
		if !strings.Contains(body, want) {
			t.Errorf("falta %q en /metrics (pool recién abierto)\n%s", want, body)
		}
	}
}

// TestRegisterDBStats_NilSafe cubre los dos nils del contrato: un *Metrics nil no
// entra en pánico, y un *sql.DB nil deja el scrape SANO (las demás series siguen
// saliendo) en vez de tumbar el arranque de un despliegue sin base.
func TestRegisterDBStats_NilSafe(t *testing.T) {
	var nilM *Metrics
	if err := nilM.RegisterDBStats(openIdleDB(t)); err != nil {
		t.Errorf("*Metrics nil no debería dar error: %v", err)
	}

	m := New()
	if err := m.RegisterDBStats(nil); err != nil {
		t.Errorf("db nil no debería dar error: %v", err)
	}
	// El contador se toca ANTES del scrape a propósito: un CounterVec sin
	// ninguna serie observada no emite NADA en /metrics, así que buscar una
	// métrica virgen no probaría que el registry sigue sirviendo.
	m.Receipt("read")
	body := scrape(t, m)
	if strings.Contains(body, "wapp_db_") {
		t.Error("con db nil no debe publicarse ninguna serie del pool")
	}
	if !strings.Contains(body, `wapp_receipts_total{status="read"} 1`) {
		t.Errorf("el scrape debe seguir sano tras RegisterDBStats(nil):\n%s", body)
	}
}

// TestRegisterDBStats_DobleLlamadaNoRevienta fija la decisión documentada: el
// llamante previsto es uno solo (bootstrap), pero un cable duplicado por
// descuido no debe tumbar el arranque por una métrica.
func TestRegisterDBStats_DobleLlamadaNoRevienta(t *testing.T) {
	m := New()
	db := openIdleDB(t)
	if err := m.RegisterDBStats(db); err != nil {
		t.Fatalf("primera llamada: %v", err)
	}
	if err := m.RegisterDBStats(db); err != nil {
		t.Fatalf("segunda llamada debería ser idempotente, dio: %v", err)
	}
	body := scrape(t, m)
	for _, want := range dbStatsSeries {
		if !strings.Contains(body, want) {
			t.Errorf("falta la serie %q tras la doble llamada", want)
		}
	}
	if strings.Count(body, "# TYPE wapp_db_max_open") != 1 {
		t.Errorf("la serie no debe duplicarse en el scrape:\n%s", body)
	}
}
