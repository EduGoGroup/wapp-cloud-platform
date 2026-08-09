// crmcallback_e2e_integration_test.go es el criterio de T4.4 del Plan 042: el camino
// COMPLETO de la vuelta del puente, sin trozos simulados en medio.
//
// Lo que hace e2e a este test y no a los otros del callback: la petición entra por el
// mux montado con publicapi.Register —así que también prueba que la RUTA está
// cableada—, el reflejo lo aplica el store de verdad contra Postgres real, y el aviso
// sale por el Notifier de verdad del Plan 041. Lo único simulado es CloudLink (el
// Sender espía) y la vía custodiada de PII, que es exactamente lo que el criterio
// pide simular.
//
// La cadena que se verifica de punta a punta:
//
//	POST firmado → HMAC verificada → gate → schema → UPDATE en public.intakes →
//	Notifier → SendText por la sesión de la solicitud, al destino custodiado
package publicapi_test

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	sharedjwt "github.com/EduGoGroup/wapp-shared/auth/jwt"
	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/integrations/sigv1"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
)

const (
	// e2eSecret es el secreto HMAC del puente.
	e2eSecret = "secreto-del-puente-e2e" // #nosec G101 -- secreto de prueba, no una credencial real
	// e2eDestino es el número al que el aviso tiene que salir. Es un literal RARO a
	// propósito: el test lo BUSCA en todo lo que quedó escrito, y con un número
	// plausible no se distinguiría de cualquier otro dato.
	e2eDestino = "+56900011122-qqz"
)

// ─────────────────────── CloudLink simulado y vía custodiada ───────────────────────

type e2eEnvío struct{ sessionID, to, text string }

// e2eSender es CloudLink simulado: apunta lo que se le pidió despachar en vez de
// abrir un stream al Edge.
type e2eSender struct {
	mu       sync.Mutex
	enviados []e2eEnvío
}

func (s *e2eSender) SendText(_ context.Context, sessionID, to, text string) (*cloudlinkv1.Ack, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enviados = append(s.enviados, e2eEnvío{sessionID: sessionID, to: to, text: text})
	return &cloudlinkv1.Ack{}, nil
}

func (s *e2eSender) todos() []e2eEnvío {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]e2eEnvío(nil), s.enviados...)
}

// e2eDestinations hace de vía custodiada de PII: traduce el contact_id OPACO al
// número. En producción esto lo hace *contact.PostgresResolver descifrando con la
// KEK; aquí basta con que devuelva algo, porque lo que el test vigila es que el
// número NO aparezca en ningún sitio salvo en el SendText.
type e2eDestinations struct{}

func (e2eDestinations) Destino(context.Context, string, string) (contact.Ref, error) {
	return contact.Ref{Kind: contact.KindPhoneE164, Value: e2eDestino}, nil
}

// e2eSecrets/e2eGate son las dos piezas que en producción salen de
// tenant_integrations. Se simulan porque el secreto real va cifrado con la KEK y
// sembrarlo exigiría montar el cifrador entero: lo que este test prueba es la cadena
// del callback, no el almacén del secreto (que ya tiene su propio test contra
// Postgres, TestTenantSecret_CifradoEnReposo).
type e2eSecrets struct{}

func (e2eSecrets) GetTenantSecret(context.Context, string) (string, bool, error) {
	return e2eSecret, true, nil
}

type e2eGate struct{}

func (e2eGate) Enabled(context.Context, string) (bool, error) { return true, nil }

// e2eLogger descarta la salida: lo que este test afirma son los EFECTOS (la fila
// escrita y el SendText), no lo que se logueó por el camino.
func e2eLogger() sharedlogger.Logger {
	return sharedlogger.New(sharedlogger.WithWriter(e2eDiscard{}))
}

type e2eDiscard struct{}

func (e2eDiscard) Write(p []byte) (int, error) { return len(p), nil }

// ─────────────────────────── Andamiaje de BD ───────────────────────────

func e2eOpenDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("WAPP_TEST_DB_DSN")
	if dsn == "" {
		if os.Getenv("WAPP_TEST_REQUIRE_DB") != "" {
			t.Fatal("WAPP_TEST_DB_DSN no definido pero WAPP_TEST_REQUIRE_DB exige BD")
		}
		t.Skip("WAPP_TEST_DB_DSN no definido: se omite el e2e del callback")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := postgres.Open(ctx, postgres.Config{DSN: dsn})
	if err != nil {
		if os.Getenv("WAPP_TEST_REQUIRE_DB") != "" {
			t.Fatalf("BD no disponible (%v) pero WAPP_TEST_REQUIRE_DB exige BD", err)
		}
		t.Skipf("BD no disponible (%v): se omite", err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Logf("cerrando BD: %v", cerr)
		}
	})
	if _, err := migrations.Migrate(ctx, db); err != nil {
		t.Fatalf("migrando BD de test: %v", err)
	}
	return db
}

// e2eSeed siembra el tenant y la solicitud que el callback va a reflejar. La sesión
// se guarda tal cual: el aviso tiene que salir por ESA y no por otra.
//
// El tenant se CREA en cada corrida y su id lo genera la base (public.tenants.id es
// uuid): no se puede usar un literal, y eso además deja los dos tests aislados entre
// sí, que importa porque el barrido de PII mira todo lo del tenant.
func e2eSeed(t *testing.T, db *sql.DB, sessionID, contactID string) (tenantID, intakeID string) {
	t.Helper()
	ctx := context.Background()
	slug := "tenant-e2e-crm-" + sessionID
	if err := db.QueryRowContext(ctx,
		`INSERT INTO public.tenants (slug, display_name) VALUES ($1, $2) RETURNING id::text`,
		slug, "E2E callback CRM").Scan(&tenantID); err != nil {
		t.Fatalf("sembrando tenant: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db.ExecContext(context.Background(),
			`DELETE FROM public.tenants WHERE id = $1`, tenantID); err != nil {
			t.Logf("limpiando tenant: %v", err)
		}
	})
	if err := db.QueryRowContext(ctx, `
		INSERT INTO public.intakes
			(id, tenant_id, contact_id, session_id, status, total, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, $2, $3, 'confirmed', 18000, now(), now())
		RETURNING id::text
	`, tenantID, contactID, sessionID).Scan(&intakeID); err != nil {
		t.Fatalf("sembrando solicitud: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db.ExecContext(context.Background(),
			`DELETE FROM public.intakes WHERE id = $1`, intakeID); err != nil {
			t.Logf("limpiando solicitud: %v", err)
		}
	})
	return tenantID, intakeID
}

// ─────────────────────────── El e2e ───────────────────────────

// TestE2E_CallbackCRM_ReflejaYAvisaAlCliente es el criterio de T4.4 completo.
func TestE2E_CallbackCRM_ReflejaYAvisaAlCliente(t *testing.T) {
	db := e2eOpenDB(t)
	const sesión, contacto = "sess-del-negocio-e2e", "contacto-opaco-e2e"
	tenantID, intakeID := e2eSeed(t, db, sesión, contacto)

	sender := &e2eSender{}
	store := intakes.NewPostgres(db)
	notifier := intakes.NewNotifier(sender, e2eDestinations{}, store, e2eLogger())

	// El mux REAL, con Register: si la ruta no está cableada, esto da 404 y el test
	// falla — que es justo lo que tiene que pasar.
	mux := http.NewServeMux()
	mw := httpapi.NewMiddleware(sharedjwt.NewJWTManager("secreto-de-test-e2e", "wapp-test"), nil)
	publicapi.Register(mux, publicapi.Deps{
		CRMSecrets: e2eSecrets{},
		CRMGate:    e2eGate{},
		CRMReflect: store,
		CRMNotify:  notifier,
	}, mw, noopAuditor{}, nil)

	// ── El puente manda el callback, firmado como en producción.
	body := `{"contract_version":"1","verb":"intake.status","intake_id":"` + intakeID +
		`","status":"paid","external_ref":"F-E2E-0001","occurred_at":"2026-08-08T12:00:00Z"}`
	rec := e2ePost(mux, tenantID, body, time.Now().Unix())
	if rec.Code != http.StatusOK {
		t.Fatalf("el callback debía responder 200, got %d — %s", rec.Code, rec.Body.String())
	}

	// ── 1. El reflejo quedó ESCRITO en Postgres (no en un doble).
	var crmStatus, extRef sql.NullString
	var syncedAt sql.NullTime
	var statusDueño string
	if err := db.QueryRowContext(context.Background(), `
		SELECT crm_status, crm_external_ref, crm_synced_at, status
		FROM public.intakes WHERE id = $1
	`, intakeID).Scan(&crmStatus, &extRef, &syncedAt, &statusDueño); err != nil {
		t.Fatalf("releyendo la solicitud: %v", err)
	}
	if crmStatus.String != "paid" || extRef.String != "F-E2E-0001" || !syncedAt.Valid {
		t.Fatalf("el reflejo no quedó escrito: status=%v ref=%v synced=%v", crmStatus, extRef, syncedAt)
	}
	if statusDueño != "confirmed" {
		t.Fatalf("el CRM pisó el estado del dueño: %q", statusDueño)
	}

	// ── 2. Salió UN SendText, por la sesión de la solicitud y al destino custodiado.
	envíos := sender.todos()
	if len(envíos) != 1 {
		t.Fatalf("envíos = %d, quiero exactamente 1: %+v", len(envíos), envíos)
	}
	if envíos[0].sessionID != sesión {
		t.Fatalf("el aviso salió por la sesión %q y no por la de la solicitud (%q)",
			envíos[0].sessionID, sesión)
	}
	if envíos[0].to != e2eDestino {
		t.Fatalf("destino = %q, quiero el que resolvió la vía custodiada", envíos[0].to)
	}
	// El texto es el de la plantilla del estado del CRM, no el del ciclo de vida.
	if !strings.Contains(envíos[0].text, "Recibimos tu pago") {
		t.Fatalf("el texto no es el de `paid`: %q", envíos[0].text)
	}

	// ── 3. El número NO aparece en claro fuera del camino custodiado. Se barre lo que
	// el callback dejó escrito: la fila de la solicitud y los eventos del flujo.
	e2eNoHayNúmeroEnClaro(t, db, tenantID, intakeID)
}

// TestE2E_CallbackCRM_RepetidoNoVuelveAAvisar es la otra mitad del criterio, y la que
// de verdad protege al cliente: un puente con reintentos manda el mismo estado muchas
// veces y el cliente no puede recibir el mismo mensaje una vez por reintento.
func TestE2E_CallbackCRM_RepetidoNoVuelveAAvisar(t *testing.T) {
	db := e2eOpenDB(t)
	tenantID, intakeID := e2eSeed(t, db, "sess-repetido", "contacto-repetido")

	sender := &e2eSender{}
	store := intakes.NewPostgres(db)
	mux := http.NewServeMux()
	mw := httpapi.NewMiddleware(sharedjwt.NewJWTManager("secreto-de-test-e2e", "wapp-test"), nil)
	publicapi.Register(mux, publicapi.Deps{
		CRMSecrets: e2eSecrets{},
		CRMGate:    e2eGate{},
		CRMReflect: store,
		CRMNotify:  intakes.NewNotifier(sender, e2eDestinations{}, store, e2eLogger()),
	}, mw, noopAuditor{}, nil)

	body := `{"contract_version":"1","verb":"intake.status","intake_id":"` + intakeID +
		`","status":"preparing","occurred_at":"2026-08-08T12:00:00Z"}`

	for i := range 3 {
		rec := e2ePost(mux, tenantID, body, time.Now().Unix())
		if rec.Code != http.StatusOK {
			t.Fatalf("callback %d: esperaba 200, got %d — %s", i+1, rec.Code, rec.Body.String())
		}
	}

	if n := len(sender.todos()); n != 1 {
		t.Fatalf("tres callbacks idénticos produjeron %d avisos: el cliente recibiría el mismo "+
			"mensaje una vez por reintento del puente", n)
	}
}

// e2ePost manda el callback firmado como lo mandaría un puente real.
func e2ePost(mux http.Handler, tenantID, body string, ts int64) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/callback", strings.NewReader(body))
	req.Header.Set("X-Wapp-Tenant", tenantID)
	req.Header.Set("X-Wapp-Timestamp", strconv.FormatInt(ts, 10))
	req.Header.Set("X-Wapp-Signature", sigv1.SignatureHeader(sigv1.Sign(e2eSecret, ts, []byte(body))))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// e2eNoHayNúmeroEnClaro barre lo que el callback pudo dejar escrito y busca el
// número. No hay ningún grep sobre el código aquí: se lee lo PERSISTIDO.
func e2eNoHayNúmeroEnClaro(t *testing.T, db *sql.DB, tenantID, intakeID string) {
	t.Helper()
	ctx := context.Background()

	var fila string
	if err := db.QueryRowContext(ctx,
		`SELECT to_jsonb(i)::text FROM public.intakes i WHERE id = $1`, intakeID).Scan(&fila); err != nil {
		t.Fatalf("serializando la solicitud: %v", err)
	}
	if strings.Contains(fila, e2eDestino) {
		t.Fatalf("FUGA: el número del cliente acabó en public.intakes:\n%s", fila)
	}

	var eventos sql.NullString
	if err := db.QueryRowContext(ctx, `
		SELECT string_agg(payload::text, ' | ')
		FROM public.flow_events WHERE tenant_id = $1
	`, tenantID).Scan(&eventos); err != nil {
		t.Fatalf("leyendo flow_events: %v", err)
	}
	if eventos.Valid && strings.Contains(eventos.String, e2eDestino) {
		t.Fatalf("FUGA: el número del cliente acabó en public.flow_events:\n%s", eventos.String)
	}
}
