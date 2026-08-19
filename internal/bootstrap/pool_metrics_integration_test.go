package bootstrap

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/config"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/metrics"
)

// Cierre de extremo a extremo de T4.2 y T4.3 (Plan 050 · Ola 4, REQ-050.14 y
// REQ-050.15).
//
// POR QUÉ EXISTE ESTE ARCHIVO, teniendo cada eslabón su test unitario: los tres
// que ya había prueban TRAMOS, no el cable.
//
//   - internal/platform/config prueba  env → cfg
//   - internal/platform/storage/postgres prueba  Config → *sql.DB (applyPool)
//   - internal/platform/metrics prueba  *sql.DB → scrape
//
// Nadie probaba la CADENA. El eslabón que ninguno cubre es la línea de
// setupDatabase (internal/bootstrap/database.go) que copia cfg.DB.MaxOpenConns
// dentro de postgres.Config: si alguien la borra, los tres tests siguen verdes
// —cada tramo sigue siendo correcto por separado— y el operador que arranca con
// WAPP_DB_MAX_OPEN_CONNS=7 ve un 25 en /metrics sin ningún aviso. Esa mutación
// exacta se ejecutó al escribir este test: puso rojo SOLO al subtest del 7.
//
// Por eso el test llama a setupDatabase de verdad (es del propio paquete) en vez
// de reconstruir la conexión: reimplementar el cable sería probar la copia, no
// el original. Y como setupDatabase corre las migraciones, necesita Postgres:
// se acoge al gating de integración de la casa (WAPP_TEST_DB_DSN /
// WAPP_TEST_REQUIRE_DB, que fija el target `make test-integration`).

// poolDSNEnv es la variable que habilita los tests de integración con BD real.
// Mismo contrato que el resto de *_integration_test.go del repo.
const poolDSNEnv = "WAPP_TEST_DB_DSN"

// poolRequireDBEnv exige la BD en vez de saltarse el test (Plan 027 · Ola 1 · T7).
const poolRequireDBEnv = "WAPP_TEST_REQUIRE_DB"

// seriesDelPool son las SEIS series que T4.3 exige en /metrics. El criterio del
// plan se comprueba en campo con `curl :8100/metrics | grep wapp_db_`; aquí se
// afirma una por una para que el fallo diga CUÁL falta en vez de "no hay
// wapp_db_".
var seriesDelPool = []string{
	"wapp_db_wait_count",
	"wapp_db_wait_duration_seconds",
	"wapp_db_in_use",
	"wapp_db_idle",
	"wapp_db_max_open",
	"wapp_db_max_idle_closed",
}

// TestIntegration_PoolDelEntornoLlegaHastaMetrics recorre el cable entero
// env → config.Load → setupDatabase → postgres.Open/applyPool → db.Stats() →
// PromHandler, una sola vez por subtest y sin atajos por el medio.
func TestIntegration_PoolDelEntornoLlegaHastaMetrics(t *testing.T) {
	dsn := os.Getenv(poolDSNEnv)
	if dsn == "" {
		if os.Getenv(poolRequireDBEnv) != "" {
			t.Fatalf("%s no definido pero %s exige BD (Plan 027 · Ola 1 · T7): la integración DEBE correr", poolDSNEnv, poolRequireDBEnv)
		}
		t.Skipf("%s no definido: se omite el test de integración del pool", poolDSNEnv)
	}

	// El entorno se aísla en el PADRE, antes de los subtests: así el subtest del
	// default arranca con las cuatro WAPP_DB_*_CONNS fuera (si el desarrollador
	// tuviera una puesta en su shell, el "sin variable" no estaría midiendo
	// nada) y el del 7 solo tiene que poner la suya encima.
	prepararEntornoDeBD(t, dsn)

	// Subtests como funciones NOMBRADAS y no closures inline: gocyclo
	// (min-complexity 15) imputa los FuncLit anidados a la función madre.
	t.Run("sin variable el pool queda en el default 25", subtestPoolPorDefecto)
	t.Run("WAPP_DB_MAX_OPEN_CONNS=7 llega hasta /metrics", subtestPoolDesdeElEntorno)
}

// subtestPoolPorDefecto es el lado "sin tocar nada" del criterio de T4.2.
func subtestPoolPorDefecto(t *testing.T) {
	cuerpo := scrapearElPoolReal(t)

	// 25 LITERAL a propósito, no postgres.DefaultMaxOpenConns: lo que T4.2 pide
	// verificar es el número que el operador LEE en /metrics, y compararlo
	// contra la constante lo volvería tautológico (mover la constante dejaría
	// este test verde con el criterio del plan incumplido). Que la constante en
	// sí no se haya movido lo clava aparte
	// TestDefaultsDelPool_NoSeMovieronEnLaOla4, en
	// internal/platform/storage/postgres/connect_test.go. Son dos afirmaciones
	// distintas y por eso viven separadas.
	const quiero = "wapp_db_max_open 25"
	if !strings.Contains(cuerpo, quiero) {
		t.Errorf("sin WAPP_DB_MAX_OPEN_CONNS el scrape debería decir %q\n%s", quiero, extraerSeriesDelPool(cuerpo))
	}

	afirmarLasSeisSeries(t, cuerpo)
}

// subtestPoolDesdeElEntorno es el lado "el operador lo cambia" del criterio de
// T4.2: la variable entra por el entorno y tiene que salir por /metrics sin que
// nadie recompile. Es el subtest que caza el cable roto en setupDatabase.
func subtestPoolDesdeElEntorno(t *testing.T) {
	t.Setenv(config.EnvPrefix+"DB_MAX_OPEN_CONNS", "7")

	cuerpo := scrapearElPoolReal(t)

	// El 7 no es un número redondo por gusto: no coincide con ningún default del
	// pool (25/5), así que verlo en el scrape solo puede venir del entorno.
	const quiero = "wapp_db_max_open 7"
	if !strings.Contains(cuerpo, quiero) {
		t.Errorf("WAPP_DB_MAX_OPEN_CONNS=7 no llegó a /metrics: falta %q — revisa que setupDatabase siga pasando cfg.DB.MaxOpenConns a postgres.Config\n%s", quiero, extraerSeriesDelPool(cuerpo))
	}

	afirmarLasSeisSeries(t, cuerpo)
}

// scrapearElPoolReal ejecuta el cable de producción de punta a punta y devuelve
// el cuerpo del scrape de /metrics.
func scrapearElPoolReal(t *testing.T) string {
	t.Helper()

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	// setupDatabase es el eslabón bajo prueba: se llama al de verdad (misma
	// función que usa bootstrap.Run), no a una copia.
	db, err := setupDatabase(t.Context(), cfg, quietLogger())
	if err != nil {
		t.Fatalf("setupDatabase contra %s: %v", poolDSNEnv, err)
	}
	// El repo no acepta `_ = db.Close()`: errcheck corre con check-blank.
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Logf("cerrando el pool de test: %v", cerr)
		}
	})

	// El mismo orden que bootstrap: las métricas nacen antes que la base y el
	// pool se registra DESPUÉS de abrirla.
	m := metrics.New()
	if err := m.RegisterDBStats(db); err != nil {
		t.Fatalf("RegisterDBStats: %v", err)
	}

	rec := httptest.NewRecorder()
	m.PromHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics: got %d, want 200", rec.Code)
	}
	return rec.Body.String()
}

// afirmarLasSeisSeries cierra el criterio de T4.3 (`grep wapp_db_` devuelve las
// seis). Una aserción por serie, con t.Errorf y no t.Fatalf, para que un scrape
// al que le falten tres las liste las tres de una vez.
func afirmarLasSeisSeries(t *testing.T, cuerpo string) {
	t.Helper()
	for _, serie := range seriesDelPool {
		if !strings.Contains(cuerpo, serie) {
			t.Errorf("falta la serie %q en /metrics (T4.3 exige las seis)", serie)
		}
	}
}

// extraerSeriesDelPool recorta el scrape a las líneas wapp_db_ para que el
// mensaje de fallo quepa en la pantalla: el cuerpo completo son cientos de
// líneas de métricas del runtime de Go que no dicen nada del pool.
func extraerSeriesDelPool(cuerpo string) string {
	var b strings.Builder
	for _, linea := range strings.Split(cuerpo, "\n") {
		if strings.HasPrefix(linea, "wapp_db_") {
			b.WriteString(linea)
			b.WriteByte('\n')
		}
	}
	if b.Len() == 0 {
		return "(el scrape no trae NINGUNA serie wapp_db_)"
	}
	return b.String()
}

// prepararEntornoDeBD deja el entorno en el estado que este test necesita:
//
//  1. fuera WAPP_CONFIG_FILE y las cuatro WAPP_DB_*_CONNS/TIME heredadas del
//     shell, para que el subtest del default mida el default y no la costumbre
//     del desarrollador;
//  2. dentro las WAPP_DB_HOST/PORT/USER/PASSWORD/NAME/SSLMODE que apuntan al
//     Postgres del gate.
//
// El paso 2 existe por un DESAJUSTE de formatos que hay que salvar a mano: el
// gating de la casa publica un DSN tipo URL (postgres://user:pass@host:port/db)
// mientras config.DatabaseConfig se arma por campos sueltos y produce un DSN
// keyword/value. Volcar el DSN entero en una variable no serviría: no existe
// WAPP_DB_DSN, y si existiera este test se saltaría precisamente el tramo
// campos → DSN() que forma parte del cable.
func prepararEntornoDeBD(t *testing.T, dsn string) {
	t.Helper()

	desfijarEnv(t,
		config.EnvPrefix+"CONFIG_FILE",
		config.EnvPrefix+"DB_MAX_OPEN_CONNS",
		config.EnvPrefix+"DB_MAX_IDLE_CONNS",
		config.EnvPrefix+"DB_CONN_MAX_LIFETIME",
		config.EnvPrefix+"DB_CONN_MAX_IDLE_TIME",
	)

	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("%s=%q no parsea como URL: %v — se esperaba postgres://usuario:clave@host:puerto/base?sslmode=…", poolDSNEnv, dsn, err)
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		t.Fatalf("%s=%q: esquema %q inesperado, se esperaba postgres:// o postgresql://", poolDSNEnv, dsn, u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		t.Fatalf("%s=%q no trae host", poolDSNEnv, dsn)
	}
	base := strings.TrimPrefix(u.Path, "/")
	if base == "" {
		t.Fatalf("%s=%q no trae nombre de base de datos", poolDSNEnv, dsn)
	}
	// Puerto y sslmode son opcionales en la URL; se completan con lo que asume
	// libpq (5432) y con lo que usa el Postgres efímero del Makefile (disable).
	puerto := u.Port()
	if puerto == "" {
		puerto = "5432"
	}
	if _, err := strconv.Atoi(puerto); err != nil {
		t.Fatalf("%s=%q: puerto %q no es un número: %v", poolDSNEnv, dsn, puerto, err)
	}
	sslmode := u.Query().Get("sslmode")
	if sslmode == "" {
		sslmode = "disable"
	}
	clave, _ := u.User.Password() // *url.Userinfo nil-safe; sin clave se manda vacía.

	t.Setenv(config.EnvPrefix+"DB_HOST", host)
	t.Setenv(config.EnvPrefix+"DB_PORT", puerto)
	t.Setenv(config.EnvPrefix+"DB_USER", u.User.Username())
	t.Setenv(config.EnvPrefix+"DB_PASSWORD", clave)
	t.Setenv(config.EnvPrefix+"DB_NAME", base)
	t.Setenv(config.EnvPrefix+"DB_SSLMODE", sslmode)
}

// desfijarEnv BORRA las claves indicadas durante el test y las restaura al
// terminar.
//
// No vale t.Setenv(k, "") para esto: el loader compartido usa LookupEnv, así
// que una clave puesta a cadena vacía EXISTE, y en un GetInt eso es un valor
// presente-pero-inválido (warning ruidoso) en vez de "no está". Aquí hace falta
// que no esté. El t.Setenv previo es lo que registra la restauración del valor
// original; el Unsetenv que le sigue es lo que la deja fuera mientras corre el
// test.
func desfijarEnv(t *testing.T, claves ...string) {
	t.Helper()
	for _, clave := range claves {
		anterior, presente := os.LookupEnv(clave)
		if !presente {
			continue
		}
		t.Setenv(clave, anterior)
		if err := os.Unsetenv(clave); err != nil {
			t.Fatalf("desfijando %s: %v", clave, err)
		}
	}
}
