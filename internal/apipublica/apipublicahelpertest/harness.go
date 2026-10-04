// Package apipublicahelpertest es el arnés de test de la cara nueva (internal/apipublica): firma
// Context Tokens DE VERDAD con sharedjwt, los pasa por el middleware real de httpapi y llama a
// una Cara; trae además un auditor y un logger que graban lo que reciben.
//
// Porta el arnés de internal/publicapi/publicapi_test.go @ 9a77307 (líneas 120-168: testAPI,
// call y token). En la spec FX era `arnes.go` con `Arnes`, `Nuevo`, `Con`, `Llamar` y
// `AuditorDoble` (05 E-11: Harness, New, With, Call y AuditRecorderFake).
//
// Como los dobles de los demás paquetes …helpertest (entitlementshelpertest,
// platformadminhelpertest), nace COMPLETO, sin rojo: no es un contrato de producción sino la
// herramienta con la que se escriben los rojos de la cara, y si panicara taparía el panic de
// pendiente que esos rojos tienen que enseñar. Tiene lógica (firma, grabación), así que lleva su
// harness_test.go (05 E-3, fila «Dobles de test»).
package apipublicahelpertest

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	sharedjwt "github.com/EduGoGroup/wapp-shared/auth/jwt"
	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// Empresas y persona de referencia de los tests de la cara. Son UUID porque así son los ids de
// verdad; no significan nada más.
const (
	TenantA = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	TenantB = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	// Subject es el user_id de los tokens que firma With.
	Subject = "11111111-1111-1111-1111-111111111111"
)

// Material de firma: no es una credencial, es el secreto HS256 de un test.
const (
	tokenIssuer = "wapp-test"
	//nolint:gosec // no es una credencial: es material de firma de un test
	tokenSecret = "secret-hs256-de-test-apipublica"
	tokenTTL    = time.Hour
)

// Harness reúne lo que un test de la cara necesita: el firmador de Context Tokens, el
// middleware que los valida (el mismo tipo que en producción), un AuditRecorderFake y un
// LogRecorder. Se construye con New; cada test usa el suyo (no comparten estado).
type Harness struct {
	t       testing.TB
	jwt     *sharedjwt.JWTManager
	mw      *httpapi.Middleware
	auditor *AuditRecorderFake
	log     *LogRecorder
}

// New devuelve un arnés nuevo, con su propio firmador, middleware, auditor vacío y logger vacío.
// Un fallo al firmar un token (fixture roto) hace fallar t en el acto con t.Fatalf.
func New(t testing.TB) *Harness {
	t.Helper()
	jwt := sharedjwt.NewJWTManager(tokenSecret, tokenIssuer)
	return &Harness{
		t:       t,
		jwt:     jwt,
		mw:      httpapi.NewMiddleware(jwt, nil),
		auditor: &AuditRecorderFake{},
		log:     &LogRecorder{sink: &logSink{}},
	}
}

// MW es el middleware de autenticación del arnés: acepta exactamente los tokens que firman
// With, WithSubject y Tenantless.
func (h *Harness) MW() *httpapi.Middleware { return h.mw }

// Auditor es el auditor del arnés: el que Common pone en Common.Auditor.
func (h *Harness) Auditor() *AuditRecorderFake { return h.auditor }

// Log es el logger del arnés: el que Common pone en Common.Log.
func (h *Harness) Log() *LogRecorder { return h.log }

// Common devuelve el apipublica.Common de prueba: MW, Auditor y Log del arnés. Para probar un
// campo nil, el test arma su propio Common con los que quiera.
func (h *Harness) Common() apipublica.Common {
	return apipublica.Common{MW: h.mw, Auditor: h.auditor, Log: h.log}
}

// With firma un Context Token de Subject en tenantID con los grants dados (patrones RBAC de
// permiso «allow», p. ej. "roles.read" o "roles.*"); sin grants, el token autentica pero no
// autoriza nada. tenantID no puede ser vacío: para un token sin empresa está Tenantless.
func (h *Harness) With(tenantID string, grants ...string) string {
	h.t.Helper()
	return h.WithSubject(Subject, tenantID, grants...)
}

// WithSubject es With para otra persona.
func (h *Harness) WithSubject(subject, tenantID string, grants ...string) string {
	h.t.Helper()
	tok, _, err := h.jwt.GenerateToken(subject, tenantID, []string{"operator"},
		sharedjwt.Grants{Allow: slices.Clone(grants)}, tokenTTL)
	if err != nil {
		h.t.Fatalf("firmando el Context Token de prueba (%s en %s): %v", subject, tenantID, err)
	}
	return tok
}

// Tenantless firma el token de una persona SIN empresa (D-056.12): se autentica, no trae tenant
// ni un solo grant.
func (h *Harness) Tenantless(subject string) string {
	h.t.Helper()
	tok, _, err := h.jwt.GenerateTenantlessToken(subject, tokenTTL)
	if err != nil {
		h.t.Fatalf("firmando el token sin empresa de prueba (%s): %v", subject, err)
	}
	return tok
}

// Call sirve UNA petición con target (camino y query) y method contra handler (normalmente una
// *apipublica.Cara) y devuelve lo que respondió. credential es el token tal cual: vacía = sin
// cabecera Authorization; si no, "Authorization: Bearer <credential>" (un texto que no es un
// token produce el 401 del middleware). Un body no vacío viaja con Content-Type
// application/json. La IP de origen es la fija de httptest (192.0.2.1).
func (h *Harness) Call(handler http.Handler, credential, method, target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if credential != "" {
		req.Header.Set("Authorization", "Bearer "+credential)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// AuditRecorderFake es un httpapi.AuditRecorder que GUARDA cada registro, en orden, y nunca
// falla. Seguro para uso concurrente. Su valor cero está listo.
type AuditRecorderFake struct {
	mu      sync.Mutex
	records []httpapi.AuditInput
}

var _ httpapi.AuditRecorder = (*AuditRecorderFake)(nil)

// Record guarda in y devuelve nil.
func (f *AuditRecorderFake) Record(_ context.Context, in httpapi.AuditInput) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records = append(f.records, in)
	return nil
}

// Records devuelve una COPIA de los registros guardados, en orden de llegada.
func (f *AuditRecorderFake) Records() []httpapi.AuditInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.records)
}

// LogEntry es una línea que recibió un LogRecorder: su nivel ("debug", "info", "warn" o
// "error"), su mensaje y sus campos (los pares clave/valor de la llamada, precedidos de los de
// With). Una clave que no es string se guarda con fmt.Sprint; un valor sin pareja, con clave
// "!BADKEY" (el criterio de slog).
type LogEntry struct {
	Level  string
	Msg    string
	Fields map[string]any
}

// LogRecorder es un sharedlogger.Logger que guarda cada línea. Los hijos de With escriben en el
// mismo registro que su padre. Seguro para uso concurrente. Se obtiene de Harness.Log: su valor
// cero no está listo.
type LogRecorder struct {
	sink *logSink
	args []any
}

// logSink es el registro compartido por un LogRecorder y sus hijos.
type logSink struct {
	mu      sync.Mutex
	entries []LogEntry
}

var _ sharedlogger.Logger = (*LogRecorder)(nil)

// Debug guarda una línea de nivel "debug".
func (l *LogRecorder) Debug(msg string, args ...any) { l.add("debug", msg, args) }

// Info guarda una línea de nivel "info".
func (l *LogRecorder) Info(msg string, args ...any) { l.add("info", msg, args) }

// Warn guarda una línea de nivel "warn".
func (l *LogRecorder) Warn(msg string, args ...any) { l.add("warn", msg, args) }

// Error guarda una línea de nivel "error".
func (l *LogRecorder) Error(msg string, args ...any) { l.add("error", msg, args) }

// With devuelve un hijo que antepone args a los campos de cada línea y escribe en el mismo
// registro.
func (l *LogRecorder) With(args ...any) sharedlogger.Logger {
	return &LogRecorder{sink: l.sink, args: append(slices.Clone(l.args), args...)}
}

// Entries devuelve una COPIA de las líneas guardadas (por este logger y sus hijos), en orden.
func (l *LogRecorder) Entries() []LogEntry {
	l.sink.mu.Lock()
	defer l.sink.mu.Unlock()
	return slices.Clone(l.sink.entries)
}

// add arma la línea con los campos de With y los de la llamada, y la guarda.
func (l *LogRecorder) add(level, msg string, args []any) {
	all := append(slices.Clone(l.args), args...)
	fields := make(map[string]any, (len(all)+1)/2)
	for i := 0; i < len(all); i += 2 {
		if i+1 == len(all) {
			fields["!BADKEY"] = all[i]
			break
		}
		key, ok := all[i].(string)
		if !ok {
			key = fmt.Sprint(all[i])
		}
		fields[key] = all[i+1]
	}
	l.sink.mu.Lock()
	defer l.sink.mu.Unlock()
	l.sink.entries = append(l.sink.entries, LogEntry{Level: level, Msg: msg, Fields: fields})
}
