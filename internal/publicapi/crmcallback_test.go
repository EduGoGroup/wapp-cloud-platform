package publicapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/integrations/sigv1"
)

// ─────────────────────────── Dobles ───────────────────────────

type fakeCRMSecrets struct {
	secret string
	found  bool
	err    error
}

func (f fakeCRMSecrets) GetTenantSecret(context.Context, string) (string, bool, error) {
	return f.secret, f.found, f.err
}

type fakeCRMGate struct {
	enabled bool
	err     error
}

func (f fakeCRMGate) Enabled(context.Context, string) (bool, error) { return f.enabled, f.err }

type fakeCRMReflector struct {
	// porTenant simula el acotado por tenant: solo se encuentra la solicitud cuyo par
	// (tenant, intake) esté aquí. Es lo que hace verificable el 404 indistinguible.
	porTenant map[string]string // tenantID -> intakeID que le pertenece
	changed   bool
	err       error

	vistos []string // "tenant/intake/status" de cada llamada
}

func (f *fakeCRMReflector) ReflectCRMStatus(_ context.Context, tenantID, intakeID, status, _ string,
	_ time.Time) (intakes.CRMReflection, error) {
	f.vistos = append(f.vistos, fmt.Sprintf("%s/%s/%s", tenantID, intakeID, status))
	if f.err != nil {
		return intakes.CRMReflection{}, f.err
	}
	if f.porTenant[tenantID] != intakeID {
		return intakes.CRMReflection{}, nil // no encontrada (o de otro tenant)
	}
	return intakes.CRMReflection{
		Found:   true,
		Changed: f.changed,
		Intake:  intakes.Intake{ID: intakeID, ContactID: "ct-1", SessionID: "se-1"},
	}, nil
}

type fakeCRMNotifier struct {
	avisos []string
	// pánico simula lo peor que puede hacer el notificador: reventar. El del 041
	// contiene sus propios pánicos, así que aquí sirve para comprobar que el handler
	// no depende de esa cortesía.
	pánico bool
}

func (f *fakeCRMNotifier) NotifyCRMStatus(_ context.Context, tenantID string, in intakes.Intake,
	crmStatus string) {
	f.avisos = append(f.avisos, fmt.Sprintf("%s/%s/%s", tenantID, in.ID, crmStatus))
	if f.pánico {
		panic("el Edge no responde")
	}
}

// ─────────────────────────── Helpers ───────────────────────────

const (
	crmTenant   = "acme-panaderia"
	crmSecret   = "secreto-del-puente-de-acme" // #nosec G101 -- secreto de prueba, no una credencial real
	crmIntakeID = "11111111-2222-3333-4444-555555555555"
)

func crmBody(status string) string {
	return `{"contract_version":"1","verb":"intake.status","intake_id":"` + crmIntakeID +
		`","status":"` + status + `","occurred_at":"2026-08-08T12:00:00Z"}`
}

// crmPost arma la petición FIRMADA como la mandaría un puente real: el timestamp del
// header es el mismo que entra en la cadena canónica, y la firma se calcula sobre el
// cuerpo crudo. Si el test quiere romper algo, lo rompe con los parámetros.
func crmPost(h http.Handler, tenant, secret, body string, ts int64) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/callback", strings.NewReader(body))
	req.Header.Set(headerCRMTenant, tenant)
	req.Header.Set(headerCRMTimestamp, strconv.FormatInt(ts, 10))
	req.Header.Set(headerCRMSignature, sigv1.SignatureHeader(sigv1.Sign(secret, ts, []byte(body))))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// crmHandler monta el handler con los dobles por defecto: tenant con secreto, puente
// activo y la solicitud perteneciendo al tenant.
func crmHandler(t *testing.T, opts ...func(*fakeCRMSecrets, *fakeCRMGate, *fakeCRMReflector, *fakeCRMNotifier)) (
	http.Handler, *fakeCRMReflector, *fakeCRMNotifier) {
	t.Helper()
	secrets := &fakeCRMSecrets{secret: crmSecret, found: true}
	gate := &fakeCRMGate{enabled: true}
	reflector := &fakeCRMReflector{porTenant: map[string]string{crmTenant: crmIntakeID}, changed: true}
	notifier := &fakeCRMNotifier{}
	for _, o := range opts {
		o(secrets, gate, reflector, notifier)
	}
	return crmCallbackHandler(secrets, gate, reflector, notifier, nil), reflector, notifier
}

// ─────────────────────── El criterio de T4.2 ───────────────────────

// TestCRMCallback_FirmaValida_Refleja es el camino feliz: firma buena, puente activo,
// solicitud del tenant ⇒ 2xx y el reflejo aplicado.
func TestCRMCallback_FirmaValida_Refleja(t *testing.T) {
	h, reflector, notifier := crmHandler(t)

	rec := crmPost(h, crmTenant, crmSecret, crmBody("paid"), time.Now().Unix())
	if rec.Code != http.StatusOK {
		t.Fatalf("firma válida debía responder 200, got %d — %s", rec.Code, rec.Body.String())
	}
	if len(reflector.vistos) != 1 || reflector.vistos[0] != crmTenant+"/"+crmIntakeID+"/paid" {
		t.Fatalf("el reflejo no recibió lo que llegó: %v", reflector.vistos)
	}
	// El tenant del HEADER es el que manda: el cuerpo no lo lleva (INV-05).
	if len(notifier.avisos) != 1 {
		t.Fatalf("un cambio real debía avisar al cliente una vez: %v", notifier.avisos)
	}
}

// TestCRMCallback_FirmaDeOtroSecreto_401: la firma es sintácticamente correcta pero
// se calculó con otro secreto. Es el caso de un puente mal configurado — o de alguien
// que no tiene el secreto.
func TestCRMCallback_FirmaDeOtroSecreto_401(t *testing.T) {
	h, reflector, _ := crmHandler(t)

	rec := crmPost(h, crmTenant, "otro-secreto-cualquiera", crmBody("paid"), time.Now().Unix())
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("una firma de otro secreto debía dar 401, got %d", rec.Code)
	}
	if len(reflector.vistos) != 0 {
		t.Fatal("NADA puede escribirse antes de autenticar: el reflejo se llamó igual")
	}
}

// TestCRMCallback_TimestampFueraDeVentana_401 cubre las dos orillas: diez minutos
// atrás (el replay de un cuerpo capturado) y diez minutos adelante (un puente con el
// reloj torcido). La firma es CORRECTA en ambos: lo que corta es la ventana.
func TestCRMCallback_TimestampFueraDeVentana_401(t *testing.T) {
	for _, c := range []struct {
		nombre string
		ts     int64
	}{
		{"diez minutos atrás", time.Now().Add(-10 * time.Minute).Unix()},
		{"diez minutos adelante", time.Now().Add(10 * time.Minute).Unix()},
	} {
		t.Run(c.nombre, func(t *testing.T) {
			h, reflector, _ := crmHandler(t)
			rec := crmPost(h, crmTenant, crmSecret, crmBody("paid"), c.ts)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("fuera de la ventana ±300s debía dar 401, got %d", rec.Code)
			}
			if len(reflector.vistos) != 0 {
				t.Fatal("un cuerpo fuera de ventana no puede llegar a escribir")
			}
		})
	}

	// …y el borde de dentro sigue pasando: la ventana no puede ser tan estrecha que
	// rechace a un puente con unos segundos de deriva, que es el caso normal.
	t.Run("cuatro minutos atrás SÍ pasa", func(t *testing.T) {
		h, _, _ := crmHandler(t)
		rec := crmPost(h, crmTenant, crmSecret, crmBody("paid"), time.Now().Add(-4*time.Minute).Unix())
		if rec.Code != http.StatusOK {
			t.Fatalf("dentro de la ventana debía pasar, got %d", rec.Code)
		}
	})
}

// TestCRMCallback_IntakeDeOtroTenant_404IndistinguibleDelInexistente es la garantía
// que impide usar el callback como oráculo de identificadores: las dos respuestas
// tienen que ser IGUALES, byte por byte, no solo del mismo código.
func TestCRMCallback_IntakeDeOtroTenant_404IndistinguibleDelInexistente(t *testing.T) {
	// El mismo reflector conoce la solicitud, pero perteneciendo a OTRO tenant.
	ajeno, _, _ := crmHandler(t, func(_ *fakeCRMSecrets, _ *fakeCRMGate, r *fakeCRMReflector, _ *fakeCRMNotifier) {
		r.porTenant = map[string]string{"otro-tenant": crmIntakeID}
	})
	recAjeno := crmPost(ajeno, crmTenant, crmSecret, crmBody("paid"), time.Now().Unix())

	// Y una que no existe para nadie.
	inexistente, _, _ := crmHandler(t, func(_ *fakeCRMSecrets, _ *fakeCRMGate, r *fakeCRMReflector, _ *fakeCRMNotifier) {
		r.porTenant = map[string]string{}
	})
	recInexistente := crmPost(inexistente, crmTenant, crmSecret, crmBody("paid"), time.Now().Unix())

	if recAjeno.Code != http.StatusNotFound || recInexistente.Code != http.StatusNotFound {
		t.Fatalf("ambas debían ser 404: ajena=%d inexistente=%d", recAjeno.Code, recInexistente.Code)
	}
	if recAjeno.Body.String() != recInexistente.Body.String() {
		t.Fatalf("las respuestas DIFIEREN y eso permite sondear ids de otros tenants:\n ajena=%s\n inexistente=%s",
			recAjeno.Body.String(), recInexistente.Body.String())
	}
}

// TestCRMCallback_EstadoNoCanonico_422: `shipped` es un estado plausible de cualquier
// CRM y NO es de los cuatro. El puente tiene que enterarse, no que se lo traguen.
func TestCRMCallback_EstadoNoCanonico_422(t *testing.T) {
	h, reflector, _ := crmHandler(t)

	rec := crmPost(h, crmTenant, crmSecret, crmBody("shipped"), time.Now().Unix())
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("un estado fuera de los cuatro canónicos debía dar 422, got %d", rec.Code)
	}
	if len(reflector.vistos) != 0 {
		t.Fatal("un estado no canónico no puede llegar al reflejo")
	}
	// El mensaje nombra los cuatro: el contrato dice que reintentar dará SIEMPRE 422,
	// así que el autor del puente necesita saber contra qué mapear.
	for _, canonico := range []string{"paid", "preparing", "delivered", "rejected"} {
		if !strings.Contains(rec.Body.String(), canonico) {
			t.Errorf("el 422 debía nombrar %q para que el puente sepa corregir: %s", canonico, rec.Body.String())
		}
	}
}

// ─────────────────── Lo que el criterio no pide pero el contrato sí ───────────────────

// TestCRMCallback_SinFeature_403 comprueba el gate comercial, y que va DESPUÉS de la
// firma: un desconocido no puede distinguir qué tenants tienen puente contratado.
func TestCRMCallback_SinFeature_403(t *testing.T) {
	h, reflector, _ := crmHandler(t, func(_ *fakeCRMSecrets, g *fakeCRMGate, _ *fakeCRMReflector, _ *fakeCRMNotifier) {
		g.enabled = false
	})

	rec := crmPost(h, crmTenant, crmSecret, crmBody("paid"), time.Now().Unix())
	if rec.Code != http.StatusForbidden {
		t.Fatalf("sin el puente activo debía dar 403, got %d", rec.Code)
	}
	if len(reflector.vistos) != 0 {
		t.Fatal("sin la capacidad no se escribe nada")
	}

	// Y con la firma MAL, el mismo tenant sin feature sale por 401 — el 403 nunca se
	// alcanza sin autenticar, que es lo que impide sondear quién tiene puente.
	recMal := crmPost(h, crmTenant, "otro-secreto", crmBody("paid"), time.Now().Unix())
	if recMal.Code != http.StatusUnauthorized {
		t.Fatalf("sin firma válida debía cortar en 401 antes del gate, got %d", recMal.Code)
	}
}

// TestCRMCallback_TenantSinIntegracion_401NoDelataElPuente: sin integración no hay
// secreto, así que es indistinguible de una firma mala. NO puede salir 403, que
// delataría que ese tenant existe y no tiene puente.
func TestCRMCallback_TenantSinIntegracion_401NoDelataElPuente(t *testing.T) {
	h, _, _ := crmHandler(t, func(s *fakeCRMSecrets, _ *fakeCRMGate, _ *fakeCRMReflector, _ *fakeCRMNotifier) {
		s.found = false
		s.secret = ""
	})

	rec := crmPost(h, crmTenant, crmSecret, crmBody("paid"), time.Now().Unix())
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("un tenant sin integración debía dar 401 (no 403), got %d", rec.Code)
	}
}

// TestCRMCallback_CampoDeMas_422 es el `additionalProperties:false` del schema visto
// desde Go. `tenant` es el campo que un autor de puentes manda por instinto, y el
// contrato es explícito en que se RECHAZA en vez de ignorarse.
func TestCRMCallback_CampoDeMas_422(t *testing.T) {
	h, _, _ := crmHandler(t)
	body := `{"contract_version":"1","verb":"intake.status","intake_id":"` + crmIntakeID +
		`","status":"paid","occurred_at":"2026-08-08T12:00:00Z","tenant":"acme-panaderia"}`

	rec := crmPost(h, crmTenant, crmSecret, body, time.Now().Unix())
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("un campo que el contrato no admite debía dar 422, got %d — %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "tenant") {
		t.Errorf("el 422 debía nombrar el campo que sobra: %s", rec.Body.String())
	}
}

// TestCRMCallback_SinCambio_NoAvisaAlCliente: un puente con reintentos manda el mismo
// estado muchas veces. El reflejo es idempotente y el cliente NO puede recibir el
// mismo mensaje una vez por reintento.
func TestCRMCallback_SinCambio_NoAvisaAlCliente(t *testing.T) {
	h, _, notifier := crmHandler(t, func(_ *fakeCRMSecrets, _ *fakeCRMGate, r *fakeCRMReflector, _ *fakeCRMNotifier) {
		r.changed = false
	})

	rec := crmPost(h, crmTenant, crmSecret, crmBody("paid"), time.Now().Unix())
	if rec.Code != http.StatusOK {
		t.Fatalf("un callback repetido sigue siendo válido: esperaba 200, got %d", rec.Code)
	}
	if len(notifier.avisos) != 0 {
		t.Fatalf("sin cambio real no se avisa al cliente: %v", notifier.avisos)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("respuesta ilegible: %v", err)
	}
	if body["changed"] != false {
		t.Errorf("la respuesta debía decir que no cambió nada: %v", body)
	}
}

// TestCRMCallback_AvisoQueRevienta_NoTumbaElReflejo: el reflejo YA está escrito
// cuando se avisa, así que el aviso no puede cambiar la respuesta. El notificador del
// 041 contiene sus propios pánicos —regla 1—, pero el handler no debe DEPENDER de esa
// cortesía: un notificador nuevo que no la tuviera convertiría un reflejo aplicado en
// un 500, y el puente reintentaría escribiendo lo mismo para nada.
func TestCRMCallback_AvisoQueRevienta_NoTumbaElReflejo(t *testing.T) {
	h, _, _ := crmHandler(t, func(_ *fakeCRMSecrets, _ *fakeCRMGate, _ *fakeCRMReflector, n *fakeCRMNotifier) {
		n.pánico = true
	})

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("el pánico del aviso escapó del handler: %v", r)
		}
	}()

	rec := crmPost(h, crmTenant, crmSecret, crmBody("paid"), time.Now().Unix())
	if rec.Code != http.StatusOK {
		t.Fatalf("un aviso reventado no puede tumbar un reflejo ya aplicado: got %d", rec.Code)
	}
}
