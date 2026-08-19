package publicapi_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	sharedjwt "github.com/EduGoGroup/wapp-shared/auth/jwt"
	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/diagnostics"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet/fleettest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
)

// ==================== Plan 050 · Ola 3 · T3.2/T3.3/T3.5 ====================
//
// El plazo de las LECTURAS a BD de la API pública. Lo que estos tests defienden no
// es "que exista una constante", sino la CONDUCTA: con una base que no contesta, el
// handler se rinde dentro del presupuesto en vez de colgar al llamante hasta que el
// servidor cierre la conexión sin cuerpo (el incidente del 2026-08-06).
//
// El doble de latencia es fleettest.SlowRepository, el MISMO que usa la prueba de
// carga del gateway. No se fabrica aquí ningún segundo doble ni ningún time.Sleep
// que haga su papel: un decorador gemelo divergiría del original y dejaría de
// probar lo mismo.
//
// ⚠️ Lo que estos tests NO afirman (fleettest/slowrepo.go:66-78): que tras el
// deadline "no se escribió nada". Contra este doble esa aserción queda verde
// gratis y contra Postgres real es flaky, porque la base puede haber aplicado el
// cambio y devolver el error del contexto igualmente.

const (
	// presupuestoDB es el plazo con el que se cablean estos tests: el MISMO default
	// que config.PublicAPIDBTimeout y que el suelo de publicapi.
	presupuestoDB = 1500 * time.Millisecond
	// latenciaMuerta es lo que tarda la "base": mucho más que el presupuesto, para
	// que el único desenlace posible dentro del plazo sea rendirse.
	latenciaMuerta = 5 * time.Second
	// cotaDeRendicion es la cota SUPERIOR con holgura: si el handler tarda más que
	// esto, no se rindió con su presupuesto (el margen absorbe la lentitud de un CI
	// cargado con -race, y sigue muy por debajo de los 5s de la latencia).
	cotaDeRendicion = 3 * time.Second
	// cotaInferior descarta el fallo OPUESTO: un plazo de cero (WithTimeout(0))
	// respondería en microsegundos. Que se haya esperado casi el presupuesto entero
	// es lo que prueba que el suelo del default se aplicó de verdad.
	cotaInferior = 1200 * time.Millisecond
)

// --- Espía de logs -----------------------------------------------------------

// dbLogSpy retiene TODO lo emitido para poder afirmar dos cosas a la vez: que el
// camino del plazo vencido DEJA traza (antes era mudo) y que en esa traza no
// aparece ni el destino ni el texto del mensaje (CERO PII).
type dbLogSpy struct {
	mu    sync.Mutex
	lines []string
}

func (l *dbLogSpy) record(level, msg string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, level+" "+msg+" "+fmt.Sprint(args...))
}

func (l *dbLogSpy) Debug(msg string, args ...any) { l.record("DEBUG", msg, args...) }
func (l *dbLogSpy) Info(msg string, args ...any)  { l.record("INFO", msg, args...) }
func (l *dbLogSpy) Warn(msg string, args ...any)  { l.record("WARN", msg, args...) }
func (l *dbLogSpy) Error(msg string, args ...any) { l.record("ERROR", msg, args...) }

func (l *dbLogSpy) With(args ...any) sharedlogger.Logger {
	return &dbLogSpyChild{parent: l, args: args}
}

func (l *dbLogSpy) all() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

type dbLogSpyChild struct {
	parent *dbLogSpy
	args   []any
}

func (c *dbLogSpyChild) Debug(msg string, args ...any) {
	c.parent.record("DEBUG", msg, c.join(args)...)
}
func (c *dbLogSpyChild) Info(msg string, args ...any) {
	c.parent.record("INFO", msg, c.join(args)...)
}
func (c *dbLogSpyChild) Warn(msg string, args ...any) {
	c.parent.record("WARN", msg, c.join(args)...)
}
func (c *dbLogSpyChild) Error(msg string, args ...any) {
	c.parent.record("ERROR", msg, c.join(args)...)
}
func (c *dbLogSpyChild) With(args ...any) sharedlogger.Logger {
	return &dbLogSpyChild{parent: c.parent, args: c.join(args)}
}
func (c *dbLogSpyChild) join(args []any) []any {
	return append(append([]any(nil), c.args...), args...)
}

var (
	_ sharedlogger.Logger = (*dbLogSpy)(nil)
	_ sharedlogger.Logger = (*dbLogSpyChild)(nil)
)

// --- Harness -----------------------------------------------------------------

// newAPIConLog es newAPI con logger: el harness base pasa nil, y aquí el log es
// justo una de las cosas que se prueban.
func newAPIConLog(d publicapi.Deps, keys map[string]testIdentity, log sharedlogger.Logger) *testAPI {
	jwt := sharedjwt.NewJWTManager(tokenSecret, tokenIssuer)
	mw := httpapi.NewMiddleware(jwt, nil)
	mux := http.NewServeMux()
	publicapi.Register(mux, d, mw, noopAuditor{}, log)
	return &testAPI{mux: mux, jwt: jwt, identities: keys}
}

// flotaLenta devuelve un fleet.Repository que tarda d antes de cada llamada. Es un
// SessionLister válido: publicapi.SessionLister es un subconjunto de
// fleet.Repository (solo List), así que el decorador encaja sin adaptador.
func flotaLenta(d time.Duration) *fleettest.SlowRepository {
	return fleettest.NewSlow(fleet.NewMemoryRepository(), d)
}

// leerError decodifica el {error} de una respuesta de esta API.
func leerError(t *testing.T, body []byte) string {
	t.Helper()
	var resp struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decodificando el error: %v; body=%s", err, body)
	}
	return resp.Error
}

// exigirRendicion comprueba la cota de tiempo por los DOS lados: que no se colgó
// hasta la latencia de la base (cota superior) y que tampoco respondió al instante
// (cota inferior) — un plazo de cero sería tan defectuoso como no tener plazo.
func exigirRendicion(t *testing.T, transcurrido time.Duration) {
	t.Helper()
	if transcurrido > cotaDeRendicion {
		t.Fatalf("tardó %v: no se rindió con su presupuesto de %v (la base tardaba %v)",
			transcurrido, presupuestoDB, latenciaMuerta)
	}
	if transcurrido < cotaInferior {
		t.Fatalf("respondió en %v, demasiado rápido: el plazo no puede ser ~0, tiene que ser ~%v",
			transcurrido, presupuestoDB)
	}
}

// --- T3.2 · la guarda de tenant del envío ------------------------------------

// TestO3_Messages_GuardaDeTenant_SeRindeConSuPlazo es el caso que da nombre a la
// tarea: POST /api/v1/messages consulta a la flota ANTES de empujar nada, y hasta
// esta ola lo hacía con el contexto pelado de la petición. Con una base que no
// contesta, el handler se colgaba hasta que el servidor cerraba la conexión.
//
// Se afirman CUATRO cosas, y ninguna sobra: el código (504, no 500 — el llamante
// tiene que poder distinguir «no pude» de «no me dio tiempo»), el tiempo (dentro
// del presupuesto), que NO se empujó nada al Edge, y que quedó traza en el log.
func TestO3_Messages_GuardaDeTenant_SeRindeConSuPlazo(t *testing.T) {
	sender := &fakeSender{ack: okAck()}
	log := &dbLogSpy{}
	api := newAPIConLog(publicapi.Deps{
		Sender:      sender,
		SessionDeps: publicapi.SessionDeps{Sessions: flotaLenta(latenciaMuerta)},
		DBTimeout:   presupuestoDB,
	}, apiKeys(), log)

	inicio := time.Now()
	rec := call(api, keyAFull, http.MethodPost, "/api/v1/messages",
		`{"session_id":"sess-a","to":"+15551234567","text":"hola"}`)
	transcurrido := time.Since(inicio)

	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("code=%d, quiero 504; body=%s", rec.Code, rec.Body.String())
	}
	exigirRendicion(t, transcurrido)

	// El texto tiene que ser accionable Y verdadero: el comando no llegó a salir,
	// así que reintentar es seguro (lo contrario del 504 del Ack).
	if msg := leerError(t, rec.Body.Bytes()); !strings.Contains(msg, "NO se envió") {
		t.Fatalf("el 504 de la guarda debe decir que el mensaje no salió; msg=%q", msg)
	}
	if sender.called {
		t.Fatal("se empujó al Edge pese a que la guarda de tenant nunca resolvió")
	}

	// El camino era MUDO antes de esta tarea: sin log, un 504 así es indistinguible
	// de un servidor que no hizo nada.
	lineas := log.all()
	if !strings.Contains(lineas, "WARN") || !strings.Contains(lineas, "vencida") {
		t.Fatalf("el plazo vencido no dejó traza en el log; log=%q", lineas)
	}
	if !strings.Contains(lineas, tenantA) || !strings.Contains(lineas, "sess-a") {
		t.Fatalf("la traza no permite ubicar el fallo (falta tenant_id o session_id); log=%q", lineas)
	}
	// CERO PII: ni el destino ni el texto del mensaje pueden aparecer.
	if strings.Contains(lineas, "+15551234567") || strings.Contains(lineas, "hola") {
		t.Fatalf("PII en el log del plazo vencido; log=%q", lineas)
	}
}

// TestO3_Messages_GuardaRapida_SigueEnviando es el control del caso feliz: el plazo
// no puede cobrarle nada a una base que sí contesta. Sin este test, "rendirse
// siempre" pasaría los demás.
func TestO3_Messages_GuardaRapida_SigueEnviando(t *testing.T) {
	flota := flotaLenta(0) // sin latencia: paso a través
	if err := flota.MarkOnline(t.Context(), tenantA, "edge-1", "sess-a"); err != nil {
		t.Fatalf("sembrando la sesión: %v", err)
	}
	sender := &fakeSender{ack: okAck()}
	api := newAPIConLog(publicapi.Deps{
		Sender:      sender,
		SessionDeps: publicapi.SessionDeps{Sessions: flota},
		DBTimeout:   presupuestoDB,
	}, apiKeys(), &dbLogSpy{})

	rec := call(api, keyAFull, http.MethodPost, "/api/v1/messages",
		`{"session_id":"sess-a","to":"+15551234567","text":"hola"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	if !sender.called {
		t.Fatal("con la guarda resuelta el envío tiene que ocurrir")
	}
}

// --- T3.3 · las otras dos rutas del mismo presupuesto ------------------------

// TestO3_Sessions_ListadoSeRindeConSuPlazo: GET /api/v1/sessions es la ruta que la
// consola repregunta cada pocos segundos. Sin plazo, una base lenta se traduce en
// pantallas colgadas en vez de en un error legible.
func TestO3_Sessions_ListadoSeRindeConSuPlazo(t *testing.T) {
	log := &dbLogSpy{}
	api := newAPIConLog(publicapi.Deps{
		SessionDeps: publicapi.SessionDeps{Sessions: flotaLenta(latenciaMuerta)},
		DBTimeout:   presupuestoDB,
	}, apiKeys(), log)

	inicio := time.Now()
	rec := call(api, keyASessions, http.MethodGet, "/api/v1/sessions", "")
	transcurrido := time.Since(inicio)

	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("code=%d, quiero 504; body=%s", rec.Code, rec.Body.String())
	}
	exigirRendicion(t, transcurrido)
	if msg := leerError(t, rec.Body.Bytes()); !strings.Contains(msg, "reintenta") {
		t.Fatalf("el 504 del listado debe decirle al llamante qué hacer; msg=%q", msg)
	}
	if lineas := log.all(); !strings.Contains(lineas, "WARN") || !strings.Contains(lineas, tenantA) {
		t.Fatalf("el plazo vencido del listado no dejó traza; log=%q", lineas)
	}
}

// TestO3_Diagnostics_PreflightSeRindeConSuPlazo: el preflight del diagnóstico
// remoto hace DOS consultas (consentimiento y guarda de tenant) antes de emitir
// nada al Edge. Aquí solo la segunda es lenta, que es el reparto realista: el
// consentimiento sale de una tabla diminuta y la flota de una que crece.
func TestO3_Diagnostics_PreflightSeRindeConSuPlazo(t *testing.T) {
	gw := &fakeDiagRequester{}
	log := &dbLogSpy{}
	api := newAPIConLog(publicapi.Deps{
		SessionDeps: publicapi.SessionDeps{Sessions: flotaLenta(latenciaMuerta)},
		DiagDeps: publicapi.DiagDeps{
			Diagnostics:          diagnostics.NewMemoryStore(),
			DiagnosticsRequester: gw,
		},
		DBTimeout: presupuestoDB,
	}, diagKeys(), log)

	inicio := time.Now()
	rec := call(api, keyADiag, http.MethodPost, "/api/v1/sessions/sess-a/diagnostics", "")
	transcurrido := time.Since(inicio)

	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("code=%d, quiero 504; body=%s", rec.Code, rec.Body.String())
	}
	exigirRendicion(t, transcurrido)
	if gw.called {
		t.Fatal("se emitió el DiagnosticsRequest pese a que el preflight nunca resolvió")
	}
	if lineas := log.all(); !strings.Contains(lineas, "WARN") || !strings.Contains(lineas, "sess-a") {
		t.Fatalf("el plazo vencido del preflight no dejó traza; log=%q", lineas)
	}
}

// --- El suelo del default -----------------------------------------------------

// TestO3_DBTimeoutCeroCaeAlDefault hace verdad la promesa que los docstrings de
// Deps.DBTimeout y de config.PublicAPIDBTimeout llevaban escrita sin que nadie la
// cumpliera: el cargador de config NO normaliza el <=0, así que si el consumidor
// no pone el suelo, unas Deps sin el campo cableado darían un WithTimeout(0) y
// TODA lectura moriría en el acto.
//
// Por eso la aserción mira los DOS lados: no puede tardar 5s (sin plazo) ni
// responder al instante (plazo cero). Tiene que rendirse cerca del default.
func TestO3_DBTimeoutCeroCaeAlDefault(t *testing.T) {
	sender := &fakeSender{ack: okAck()}
	api := newAPIConLog(publicapi.Deps{
		Sender:      sender,
		SessionDeps: publicapi.SessionDeps{Sessions: flotaLenta(latenciaMuerta)},
		// DBTimeout deliberadamente SIN cablear (cero-valor).
	}, apiKeys(), &dbLogSpy{})

	inicio := time.Now()
	rec := call(api, keyAFull, http.MethodPost, "/api/v1/messages",
		`{"session_id":"sess-a","to":"+15551234567","text":"hola"}`)
	transcurrido := time.Since(inicio)

	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("code=%d, quiero 504; body=%s", rec.Code, rec.Body.String())
	}
	exigirRendicion(t, transcurrido)
	if sender.called {
		t.Fatal("se empujó al Edge con la guarda sin resolver")
	}
}

// TestO3_DBTimeoutNegativoCaeAlDefault: el mismo suelo para un valor negativo, que
// es lo que produce un WAPP_PUBLICAPI_DB_TIMEOUT mal puesto ("-1s"). Un negativo
// sin suelo es idéntico a un cero: contexto ya vencido antes de la primera consulta.
func TestO3_DBTimeoutNegativoCaeAlDefault(t *testing.T) {
	log := &dbLogSpy{}
	api := newAPIConLog(publicapi.Deps{
		SessionDeps: publicapi.SessionDeps{Sessions: flotaLenta(latenciaMuerta)},
		DBTimeout:   -1 * time.Second,
	}, apiKeys(), log)

	inicio := time.Now()
	rec := call(api, keyASessions, http.MethodGet, "/api/v1/sessions", "")
	transcurrido := time.Since(inicio)

	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("code=%d, quiero 504; body=%s", rec.Code, rec.Body.String())
	}
	exigirRendicion(t, transcurrido)
}
