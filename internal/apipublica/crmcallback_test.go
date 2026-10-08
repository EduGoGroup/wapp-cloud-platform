//go:build pendiente

package apipublica_test

// crmcallback_test.go — cubre el contrato de crmcallback.go (CRMSecretReader, CRMBridgeGate,
// CRMReflector, CRMStatusNotifier, CRMCallbackDeps, MountCRMCallback): G17. Aquí van los dobles,
// el montaje, la cadena sin JWT, el camino feliz, el reflejo y el aviso; la credencial (headers,
// ventana, firma, techo del cuerpo) va en crmcallback_auth_test.go, el gate en
// crmcallback_gate_test.go y el cuerpo contra el schema publicado en crmcallback_schema_test.go
// (05 E-13).

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica/apipublicahelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/integrations"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/integrations/integrationshelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/integrations/sigv1"
)

const (
	crmTarget  = "/api/v1/integrations/callback"
	crmPattern = "POST /api/v1/integrations/callback"

	crmHeaderTenant    = "X-Wapp-Tenant"
	crmHeaderTimestamp = "X-Wapp-Timestamp"
	crmHeaderSignature = "X-Wapp-Signature"

	crmUnauthorized = `{"error":"no autenticado"}`
	crmGateClosed   = `{"error":"el puente CRM no está activo para este tenant"}`
	crmNotFound     = `{"error":"solicitud no encontrada"}`

	// Valores evidentemente ficticios: no son credenciales de nada.
	crmFakeSigner  = "valor-ficticio-del-puente-de-prueba-A"
	crmOtherSigner = "valor-ficticio-del-puente-de-prueba-B"
	crmIntakeID    = "11111111-2222-3333-4444-555555555555"
)

// Los cuatro puertos de G17 los cumplen las piezas REALES del módulo solicitudes nuevo.
var (
	_ apipublica.CRMSecretReader   = (*integrations.Postgres)(nil)
	_ apipublica.CRMSecretReader   = (*integrationshelpertest.Memoria)(nil)
	_ apipublica.CRMBridgeGate     = (*integrations.EntitlementsGate)(nil)
	_ apipublica.CRMReflector      = (*intakes.Postgres)(nil)
	_ apipublica.CRMStatusNotifier = (*intakes.Notifier)(nil)
)

// crmNow es el reloj inyectado de los tests: un instante fijo, en un huso que NO es UTC para que
// se vea quién normaliza.
var crmNow = time.Date(2026, 9, 1, 9, 0, 0, 0, time.FixedZone("-03", -3*3600))

// crmSecretsFake es CRMSecretReader: un secreto por tenant, o err.
type crmSecretsFake struct {
	byTenant map[string]string
	err      error
	calls    int
}

func (f *crmSecretsFake) GetTenantSecret(_ context.Context, tenantID string) (string, bool, error) {
	f.calls++
	if f.err != nil {
		return "", false, f.err
	}
	secret, found := f.byTenant[tenantID]
	return secret, found, nil
}

// crmGateFake es CRMBridgeGate: abierto o cerrado para todos, o err.
type crmGateFake struct {
	enabled bool
	err     error
	tenants []string
}

func (f *crmGateFake) Enabled(_ context.Context, tenantID string) (bool, error) {
	f.tenants = append(f.tenants, tenantID)
	return f.enabled, f.err
}

// crmReflectCall es lo que recibió una llamada a ReflectCRMStatus.
type crmReflectCall struct {
	tenant, intake, status, externalRef string
	syncedAt                            time.Time
}

// crmReflectorFake es CRMReflector acotado por tenant: solo «encuentra» la solicitud cuyo par
// (tenant, intake) esté en owner; con anyIntake encuentra cualquiera. Es lo que hace verificable
// el 404 indistinguible.
type crmReflectorFake struct {
	owner     map[string]string
	anyIntake bool
	changed   bool
	err       error
	calls     []crmReflectCall
}

func (f *crmReflectorFake) ReflectCRMStatus(_ context.Context, tenantID, intakeID, status, externalRef string,
	syncedAt time.Time) (intakes.CRMReflection, error) {
	f.calls = append(f.calls, crmReflectCall{tenantID, intakeID, status, externalRef, syncedAt})
	if f.err != nil {
		return intakes.CRMReflection{}, f.err
	}
	if !f.anyIntake && f.owner[tenantID] != intakeID {
		return intakes.CRMReflection{}, nil
	}
	return intakes.CRMReflection{Found: true, Changed: f.changed, Intake: intakes.Intake{ID: intakeID}}, nil
}

// crmNotifierFake es CRMStatusNotifier: apunta cada aviso y, si se le pide, revienta DESPUÉS de
// apuntarlo (lo peor que puede hacer un notificador).
type crmNotifierFake struct {
	notices []string
	explode bool
}

func (f *crmNotifierFake) NotifyCRMStatus(_ context.Context, tenantID string, in intakes.Intake, crmStatus string) {
	f.notices = append(f.notices, tenantID+"/"+in.ID+"/"+crmStatus)
	if f.explode {
		panic("el Edge no responde")
	}
}

// crmRig es el montaje de G17 con sus dobles a la vista.
type crmRig struct {
	h         *apipublicahelpertest.Harness
	secrets   *crmSecretsFake
	gate      *crmGateFake
	reflector *crmReflectorFake
	notifier  *crmNotifierFake
	cara      *apipublica.Cara
}

// newCRMRig monta G17 con los dobles por defecto: tenantA con secreto, puente activo, la
// solicitud crmIntakeID de tenantA, y el reloj en crmNow.
func newCRMRig(t *testing.T) *crmRig {
	t.Helper()
	rig := &crmRig{
		h:         apipublicahelpertest.New(t),
		secrets:   &crmSecretsFake{byTenant: map[string]string{tenantA: crmFakeSigner, tenantB: crmOtherSigner}},
		gate:      &crmGateFake{enabled: true},
		reflector: &crmReflectorFake{owner: map[string]string{tenantA: crmIntakeID}, changed: true},
		notifier:  &crmNotifierFake{},
	}
	rig.cara = apipublica.Nueva()
	apipublica.MountCRMCallback(rig.cara, rig.h.Common(), rig.deps())
	return rig
}

func (rig *crmRig) deps() apipublica.CRMCallbackDeps {
	return apipublica.CRMCallbackDeps{CRMSecrets: rig.secrets, CRMGate: rig.gate, CRMReflect: rig.reflector,
		CRMNotify: rig.notifier, Now: func() time.Time { return crmNow }}
}

// crmBody es un intake.status válido con ese estado.
func crmBody(status string) string {
	return `{"contract_version":"1","verb":"intake.status","intake_id":"` + crmIntakeID +
		`","status":"` + status + `","occurred_at":"2026-08-08T12:00:00Z"}`
}

// crmRequest es UNA petición al callback con sus tres headers CRUDOS (vacío = header ausente).
type crmRequest struct {
	tenant, timestamp, signature, body string
}

// crmSigned arma la petición como la mandaría un puente real: el timestamp del header es el
// mismo que entra en la firma, y la firma se calcula sobre el cuerpo crudo.
func crmSigned(tenant, secret, body string, ts int64) crmRequest {
	return crmRequest{
		tenant:    tenant,
		timestamp: strconv.FormatInt(ts, 10),
		signature: sigv1.SignatureHeader(sigv1.Sign(secret, ts, []byte(body))),
		body:      body,
	}
}

// crmGood es la petición buena de tenantA con ese cuerpo, firmada en el instante del reloj.
func crmGood(body string) crmRequest {
	return crmSigned(tenantA, crmFakeSigner, body, crmNow.Unix())
}

// crmSend sirve la petición SIN Authorization: el callback no lleva JWT.
func crmSend(handler http.Handler, req crmRequest) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, crmTarget, strings.NewReader(req.body))
	for name, value := range map[string]string{crmHeaderTenant: req.tenant, crmHeaderTimestamp: req.timestamp, crmHeaderSignature: req.signature} {
		if value != "" {
			r.Header[name] = []string{value}
		}
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, r)
	return rec
}

// crmLogged dice si el log del arnés tiene una línea de ese nivel y mensaje, y la devuelve.
func crmLogged(h *apipublicahelpertest.Harness, level, msg string) (apipublicahelpertest.LogEntry, bool) {
	for _, e := range h.Log().Entries() {
		if e.Level == level && e.Msg == msg {
			return e, true
		}
	}
	return apipublicahelpertest.LogEntry{}, false
}

// TestMountCRMCallback_HappyPath: firma buena, puente activo, solicitud del tenant ⇒ 200 con el
// cuerpo exacto, el reflejo con el tenant del header y el instante del reloj en UTC, y un aviso.
func TestMountCRMCallback_HappyPath(t *testing.T) {
	rig := newCRMRig(t)
	wantPatterns(t, "G17", rig.cara, []string{crmPattern})

	body := `{"contract_version":"1","verb":"intake.status","intake_id":"` + crmIntakeID +
		`","status":"paid","external_ref":"  F-2026-0001 ","occurred_at":"2020-01-01T00:00:00-05:00"}`
	rec := crmSend(rig.cara, crmGood(body))
	wantCode(t, "G17", rec, http.StatusOK)
	wantExactBody(t, "G17", rec, `{"changed":true,"crm_status":"paid","intake_id":"`+crmIntakeID+`"}`)

	if len(rig.reflector.calls) != 1 {
		t.Fatalf("ReflectCRMStatus recibió %d llamadas, quiero 1", len(rig.reflector.calls))
	}
	call := rig.reflector.calls[0]
	if call.tenant != tenantA || call.intake != crmIntakeID || call.status != "paid" || call.externalRef != "F-2026-0001" {
		t.Errorf("el reflejo recibió %+v; quiero el tenant del header, el intake y el status del cuerpo y external_ref recortado", call)
	}
	if !call.syncedAt.Equal(crmNow) || call.syncedAt.Location() != time.UTC {
		t.Errorf("syncedAt = %s; quiero el instante del reloj inyectado (%s) en UTC, no occurred_at", call.syncedAt, crmNow.UTC())
	}
	if len(rig.notifier.notices) != 1 || rig.notifier.notices[0] != tenantA+"/"+crmIntakeID+"/paid" {
		t.Errorf("avisos = %v, quiero uno solo de %s/%s/paid", rig.notifier.notices, tenantA, crmIntakeID)
	}
	if len(rig.gate.tenants) != 1 || rig.gate.tenants[0] != tenantA {
		t.Errorf("el gate se consultó con %v, quiero una vez con el tenant del header", rig.gate.tenants)
	}
}

// TestMountCRMCallback_ThreeDependenciesOrNothing: faltando secretos, gate o reflector la ruta
// no existe (T-11); el notificador no condiciona el montaje.
func TestMountCRMCallback_ThreeDependenciesOrNothing(t *testing.T) {
	for name, strip := range map[string]func(*apipublica.CRMCallbackDeps){
		"without_secrets":   func(d *apipublica.CRMCallbackDeps) { d.CRMSecrets = nil },
		"without_gate":      func(d *apipublica.CRMCallbackDeps) { d.CRMGate = nil },
		"without_reflector": func(d *apipublica.CRMCallbackDeps) { d.CRMReflect = nil },
		"without_anything":  func(d *apipublica.CRMCallbackDeps) { *d = apipublica.CRMCallbackDeps{} },
	} {
		t.Run(name, func(t *testing.T) {
			rig := newCRMRig(t)
			d := rig.deps()
			strip(&d)
			cara := apipublica.Nueva()
			apipublica.MountCRMCallback(cara, rig.h.Common(), d)
			wantPatterns(t, name, cara, nil)
			wantCode(t, name, crmSend(cara, crmGood(crmBody("paid"))), http.StatusNotFound)
		})
	}
	t.Run("without_notifier_still_mounts", func(t *testing.T) {
		rig := newCRMRig(t)
		d := rig.deps()
		d.CRMNotify = nil
		cara := apipublica.Nueva()
		apipublica.MountCRMCallback(cara, rig.h.Common(), d)
		wantPatterns(t, "sin notificador", cara, []string{crmPattern})
		rec := crmSend(cara, crmGood(crmBody("paid")))
		wantCode(t, "sin notificador", rec, http.StatusOK)
		wantExactBody(t, "sin notificador", rec, `{"changed":true,"crm_status":"paid","intake_id":"`+crmIntakeID+`"}`)
		if len(rig.reflector.calls) != 1 {
			t.Errorf("sin notificador el reflejo se aplicó %d veces, quiero 1", len(rig.reflector.calls))
		}
	})
}

// TestMountCRMCallback_NoJWTChain (T-7): la ruta no pasa por Authenticate. Monta y sirve con MW
// nil, no mira el Authorization, no audita y deja su línea de access-log sin tenant_id.
func TestMountCRMCallback_NoJWTChain(t *testing.T) {
	t.Run("nil_mw_and_nil_log_mount_and_serve", func(t *testing.T) {
		rig := newCRMRig(t)
		cara := apipublica.Nueva()
		if v := recuperar(func() { apipublica.MountCRMCallback(cara, apipublica.Common{}, rig.deps()) }); v != nil {
			t.Fatalf("MountCRMCallback con MW nil hizo panic (%v): esta ruta no lleva cadena de token", v)
		}
		wantCode(t, "MW y Log nil", crmSend(cara, crmGood(crmBody("paid"))), http.StatusOK)
		rig.gate.enabled = false
		wantCode(t, "Log nil con el gate cerrado", crmSend(cara, crmGood(crmBody("paid"))), http.StatusForbidden)
	})
	t.Run("authorization_header_is_not_looked_at", func(t *testing.T) {
		rig := newCRMRig(t)
		for name, credential := range map[string]string{
			"garbage":           "esto-no-es-un-token",
			"valid_no_grants":   rig.h.With(tenantB),
			"valid_all_grants":  rig.h.With(tenantB, "*"),
			"tenantless_person": rig.h.Tenantless("sin-empresa"),
		} {
			rec := rig.h.Call(rig.cara, credential, http.MethodPost, crmTarget, crmBody("paid"))
			wantCode(t, name+": un token no autentica el callback", rec, http.StatusUnauthorized)
			wantExactBody(t, name, rec, crmUnauthorized)
		}
		// Y al revés: con la firma buena, un Authorization inválido no estorba.
		good := crmGood(crmBody("paid"))
		r := httptest.NewRequest(http.MethodPost, crmTarget, strings.NewReader(good.body))
		r.Header.Set("Authorization", "Bearer esto-no-es-un-token")
		r.Header.Set(crmHeaderTenant, good.tenant)
		r.Header.Set(crmHeaderTimestamp, good.timestamp)
		r.Header.Set(crmHeaderSignature, good.signature)
		rec := httptest.NewRecorder()
		rig.cara.ServeHTTP(rec, r)
		wantCode(t, "firma buena con Authorization inválido", rec, http.StatusOK)
	})
	t.Run("never_audits_and_always_logs_access", func(t *testing.T) {
		rig := newCRMRig(t)
		crmSend(rig.cara, crmGood(crmBody("paid")))
		crmSend(rig.cara, crmRequest{body: crmBody("paid")})
		if n := len(rig.h.Auditor().Records()); n != 0 {
			t.Errorf("G17 dejó %d registros de auditoría, quiero 0", n)
		}
		var statuses []any
		for _, e := range rig.h.Log().Entries() {
			if e.Msg != accessLogMsg {
				continue
			}
			statuses = append(statuses, e.Fields["status"])
			if _, has := e.Fields["tenant_id"]; has || e.Fields["path"] != crmTarget || e.Fields["method"] != http.MethodPost {
				t.Errorf("línea de access-log inesperada (sin tenant_id, con el camino y el método): %+v", e.Fields)
			}
		}
		if fmt.Sprint(statuses) != "[200 401]" {
			t.Errorf("access-log con estados %v, quiero [200 401]: toda petición deja rastro, el 401 también", statuses)
		}
	})
	t.Run("only_post_is_registered", func(t *testing.T) {
		rig := newCRMRig(t)
		r := httptest.NewRequest(http.MethodGet, crmTarget, nil)
		rec := httptest.NewRecorder()
		rig.cara.ServeHTTP(rec, r)
		wantCode(t, "GET al callback", rec, http.StatusMethodNotAllowed)
	})
}

// TestMountCRMCallback_NilNowUsesTheProcessClock: sin reloj inyectado la ventana se mide contra
// el reloj del proceso (se firma con él; el margen de ±300 s absorbe la duración del test).
func TestMountCRMCallback_NilNowUsesTheProcessClock(t *testing.T) {
	rig := newCRMRig(t)
	d := rig.deps()
	d.Now = nil
	cara := apipublica.Nueva()
	apipublica.MountCRMCallback(cara, rig.h.Common(), d)

	before := time.Now()
	rec := crmSend(cara, crmSigned(tenantA, crmFakeSigner, crmBody("paid"), before.Unix()))
	wantCode(t, "con el reloj del proceso", rec, http.StatusOK)
	if got := rig.reflector.calls[0].syncedAt; got.Before(before.Add(-time.Second)) || got.After(time.Now().Add(time.Second)) {
		t.Errorf("syncedAt = %s, quiero el instante del proceso (≈ %s)", got, before)
	}
	// El instante fijo de los otros tests queda fuera de la ventana del reloj real.
	wantCode(t, "firmado en crmNow", crmSend(cara, crmGood(crmBody("paid"))), http.StatusUnauthorized)
}

// TestMountCRMCallback_ForeignAndMissingIntakeAreTheSame404: la solicitud de otro tenant y la
// que no existe responden lo mismo, byte a byte, y el reflector siempre recibe el tenant
// AUTENTICADO.
func TestMountCRMCallback_ForeignAndMissingIntakeAreTheSame404(t *testing.T) {
	rig := newCRMRig(t)
	foreign := crmSend(rig.cara, crmSigned(tenantB, crmOtherSigner, crmBody("paid"), crmNow.Unix()))
	missing := crmSend(rig.cara, crmGood(strings.Replace(crmBody("paid"), crmIntakeID, "99999999-9999-4999-8999-999999999999", 1)))
	for name, rec := range map[string]*httptest.ResponseRecorder{"ajena": foreign, "inexistente": missing} {
		wantCode(t, name, rec, http.StatusNotFound)
		wantExactBody(t, name, rec, crmNotFound)
	}
	if len(rig.reflector.calls) != 2 || rig.reflector.calls[0].tenant != tenantB || rig.reflector.calls[1].tenant != tenantA {
		t.Errorf("el reflector recibió %+v; quiero tenantB y luego tenantA, los de cada firma", rig.reflector.calls)
	}
	if len(rig.notifier.notices) != 0 {
		t.Errorf("un 404 avisó al cliente: %v", rig.notifier.notices)
	}
}

// TestMountCRMCallback_ReflectErrorIs500: el error del puerto se registra y no viaja al cliente.
func TestMountCRMCallback_ReflectErrorIs500(t *testing.T) {
	rig := newCRMRig(t)
	rig.reflector.err = errors.New("postgres://usuario:ficticio@host/bd: conexión rechazada")
	rec := crmSend(rig.cara, crmGood(crmBody("paid")))
	wantCode(t, "el reflejo falla", rec, http.StatusInternalServerError)
	wantErrorBody(t, "el reflejo falla", rec, "no se pudo aplicar el estado")
	entry, ok := crmLogged(rig.h, "error", "callback CRM: no se pudo reflejar el estado")
	if !ok || entry.Fields["tenant"] != tenantA || entry.Fields["error"] == nil {
		t.Errorf("el fallo del reflejo no dejó su Error con tenant y error: %+v", rig.h.Log().Entries())
	}
	if len(rig.notifier.notices) != 0 {
		t.Errorf("un reflejo fallido avisó al cliente: %v", rig.notifier.notices)
	}
}

// TestMountCRMCallback_Notice: se avisa solo si algo CAMBIÓ, y un notificador que revienta no
// tumba un reflejo que ya está aplicado.
func TestMountCRMCallback_Notice(t *testing.T) {
	t.Run("unchanged_does_not_notify", func(t *testing.T) {
		rig := newCRMRig(t)
		rig.reflector.changed = false
		for range 3 { // un puente con reintentos
			rec := crmSend(rig.cara, crmGood(crmBody("delivered")))
			wantCode(t, "sin cambio", rec, http.StatusOK)
			wantExactBody(t, "sin cambio", rec, `{"changed":false,"crm_status":"delivered","intake_id":"`+crmIntakeID+`"}`)
		}
		if len(rig.notifier.notices) != 0 {
			t.Errorf("sin cambio se avisó %d veces, quiero 0", len(rig.notifier.notices))
		}
	})
	t.Run("panicking_notifier_is_contained", func(t *testing.T) {
		rig := newCRMRig(t)
		rig.notifier.explode = true
		rec := crmSend(rig.cara, crmGood(crmBody("preparing")))
		wantCode(t, "el aviso revienta", rec, http.StatusOK)
		wantExactBody(t, "el aviso revienta", rec, `{"changed":true,"crm_status":"preparing","intake_id":"`+crmIntakeID+`"}`)
		if len(rig.notifier.notices) != 1 {
			t.Fatalf("el notificador recibió %d avisos, quiero 1 (el que revienta)", len(rig.notifier.notices))
		}
		entry, ok := crmLogged(rig.h, "error", "callback CRM: pánico avisando al cliente; el reflejo YA está aplicado")
		if !ok || entry.Fields["tenant"] != tenantA || entry.Fields["intake"] != crmIntakeID || entry.Fields["panic"] != "el Edge no responde" {
			t.Errorf("el pánico no quedó registrado con tenant, intake y panic: %+v", rig.h.Log().Entries())
		}
	})
	t.Run("panicking_notifier_with_nil_log", func(t *testing.T) {
		rig := newCRMRig(t)
		rig.notifier.explode = true
		cara := apipublica.Nueva()
		apipublica.MountCRMCallback(cara, apipublica.Common{}, rig.deps())
		wantCode(t, "el aviso revienta sin logger", crmSend(cara, crmGood(crmBody("paid"))), http.StatusOK)
	})
}
