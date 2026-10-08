//go:build pendiente

package apipublica_test

// integrations_test.go — cubre el contrato de integrations.go (IntegrationsStore,
// IntegrationsDeps, MountIntegrations): G13–G16. Aquí van los dobles, el montaje, la cadena, el
// gate, las lecturas (G13, G14), el borrado (G16) y el barrido del secreto; la semántica de G15
// va en integrations_put_test.go (05 E-13).

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica/apipublicahelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements/entitlementshelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/integrations"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/integrations/integrationshelpertest"
)

const (
	integrationTarget        = "/api/v1/integrations"
	integrationOutboxTarget  = "/api/v1/integrations/outbox"
	integrationPatternGet    = "GET /api/v1/integrations"
	integrationPatternOutbox = "GET /api/v1/integrations/outbox"
	integrationPatternPut    = "PUT /api/v1/integrations"
	integrationPatternDelete = "DELETE /api/v1/integrations"
	integrationReadPerm      = "integrations.read"
	integrationWritePerm     = "integrations.write"
	integrationResource      = "integration"

	integrationDenied      = `{"error":"feature_not_enabled","feature":"crm_bridge"}`
	integrationDefaultBody = `{"configured":false,"catalog_adapter":"local","events_adapter":"local","enabled":false,"secret_set":false}`
	integrationOutboxZero  = `{"pending":0,"delivering":0,"delivered":0,"dead":0}`

	// Valores evidentemente ficticios: no son credenciales de nada.
	integrationFakeSigner    = "valor-ficticio-de-prueba-uno-0001"
	integrationRotatedSigner = "valor-ficticio-de-prueba-dos-0002"
	integrationEndpoint      = "https://puente.example.test/wapp"
)

// El puerto de G13–G16 lo cumple la pieza REAL del módulo solicitudes nuevo.
var _ apipublica.IntegrationsStore = (*integrations.Postgres)(nil)

// integrationStoreFake es IntegrationsStore sobre el doble del módulo (Memoria, el que pasa la
// suite del puerto), más lo que el puerto de la cara pide y Memoria no trae: SecretFingerprint
// (la huella de verdad, integrations.Fingerprint) y CountOutbox (counts, fijo). Apunta las
// llamadas y falla a la carta.
type integrationStoreFake struct {
	*integrationshelpertest.Memoria
	counts integrations.OutboxCounts

	getErr, upsertErr, deleteErr, fingerprintErr, countErr error
	// fingerprintLost simula un puerto que dice «no encuentro el secreto» con HasSecret=true.
	fingerprintLost bool
	// upsertLost simula un upsert que dice «hecho» y no deja fila.
	upsertLost bool

	gets, upserts, deletes, fingerprints, countCalls int
	tenant                                           string
	remaining                                        time.Duration
}

var _ apipublica.IntegrationsStore = (*integrationStoreFake)(nil)

func newIntegrationStore() *integrationStoreFake {
	m := integrationshelpertest.NewMemoria()
	m.SetClock(func() time.Time { return time.Date(2026, 9, 1, 9, 0, 0, 0, time.FixedZone("-03", -3*3600)) })
	return &integrationStoreFake{Memoria: m, remaining: -1}
}

// note apunta el tenant y el plazo del contexto (-1 = sin plazo).
func (s *integrationStoreFake) note(ctx context.Context, tenantID string) {
	s.tenant = tenantID
	if dl, ok := ctx.Deadline(); ok {
		s.remaining = time.Until(dl)
	}
}

func (s *integrationStoreFake) GetTenantIntegration(ctx context.Context, tenantID string) (integrations.TenantIntegration, bool, error) {
	s.gets++
	s.note(ctx, tenantID)
	if s.getErr != nil {
		return integrations.TenantIntegration{}, false, s.getErr
	}
	return s.Memoria.GetTenantIntegration(ctx, tenantID)
}

func (s *integrationStoreFake) UpsertTenantIntegration(ctx context.Context, ti integrations.TenantIntegration, secret string) error {
	s.upserts++
	s.note(ctx, ti.TenantID)
	if s.upsertErr != nil || s.upsertLost {
		return s.upsertErr
	}
	return s.Memoria.UpsertTenantIntegration(ctx, ti, secret)
}

func (s *integrationStoreFake) DeleteTenantIntegration(ctx context.Context, tenantID string) error {
	s.deletes++
	s.note(ctx, tenantID)
	if s.deleteErr != nil {
		return s.deleteErr
	}
	return s.Memoria.DeleteTenantIntegration(ctx, tenantID)
}

func (s *integrationStoreFake) SecretFingerprint(ctx context.Context, tenantID string) (string, bool, error) {
	s.fingerprints++
	s.note(ctx, tenantID)
	if s.fingerprintErr != nil {
		return "", false, s.fingerprintErr
	}
	secret, found, err := s.GetTenantSecret(ctx, tenantID)
	if err != nil || !found || s.fingerprintLost {
		return "", false, err
	}
	return integrations.Fingerprint(secret), true, nil
}

func (s *integrationStoreFake) CountOutbox(ctx context.Context, tenantID string) (integrations.OutboxCounts, error) {
	s.countCalls++
	s.note(ctx, tenantID)
	if s.countErr != nil {
		return integrations.OutboxCounts{}, s.countErr
	}
	if tenantID != tenantA {
		return integrations.OutboxCounts{}, nil
	}
	return s.counts, nil
}

// touched dice cuántas llamadas recibió el almacén.
func (s *integrationStoreFake) touched() int {
	return s.gets + s.upserts + s.deletes + s.fingerprints + s.countCalls
}

// seed guarda una fila por el doble, sin pasar por la cara.
func (s *integrationStoreFake) seed(t *testing.T, ti integrations.TenantIntegration, secret string) {
	t.Helper()
	if err := s.Memoria.UpsertTenantIntegration(context.Background(), ti, secret); err != nil {
		t.Fatalf("sembrando la integración de %s: %v", ti.TenantID, err)
	}
}

// integrationLive es un puente webhook encendido y completo del tenant dado.
func integrationLive(tenant string) integrations.TenantIntegration {
	return integrations.TenantIntegration{TenantID: tenant, CatalogAdapter: "local", EventsAdapter: "webhook",
		EndpointURL: integrationEndpoint, Enabled: true}
}

// integrationCara monta G13–G16 con k, el almacén y el resolver dados.
func integrationCara(k apipublica.Common, store apipublica.IntegrationsStore, resolver entitlements.Resolver) *apipublica.Cara {
	c := apipublica.Nueva()
	apipublica.MountIntegrations(c, k, apipublica.IntegrationsDeps{Integrations: store, Entitlements: resolver})
	return c
}

// integrationSetup es el montaje habitual: arnés, almacén y la cara con `crm_bridge` encendida.
func integrationSetup(t *testing.T) (*apipublicahelpertest.Harness, *integrationStoreFake, *apipublica.Cara) {
	t.Helper()
	h := apipublicahelpertest.New(t)
	store := newIntegrationStore()
	return h, store, integrationCara(h.Common(), store, withFeatures(entitlements.FeatureCRMBridge))
}

// integrationRoutes son las cuatro rutas con un cuerpo admisible para el PUT.
var integrationRoutes = []routeCase{
	{id: "G13", method: http.MethodGet, target: integrationTarget, perm: integrationReadPerm, want: http.StatusOK},
	{id: "G14", method: http.MethodGet, target: integrationOutboxTarget, perm: integrationReadPerm, want: http.StatusOK},
	{id: "G15", method: http.MethodPut, target: integrationTarget, body: `{"events_adapter":"webhook"}`,
		perm: integrationWritePerm, resource: integrationResource, want: http.StatusOK},
	{id: "G16", method: http.MethodDelete, target: integrationTarget,
		perm: integrationWritePerm, resource: integrationResource, want: http.StatusNoContent},
}

func TestMountIntegrations_Chain(t *testing.T) {
	h, _, cara := integrationSetup(t)
	wantPatterns(t, "G13–G16", cara,
		[]string{integrationPatternGet, integrationPatternOutbox, integrationPatternPut, integrationPatternDelete})
	for _, rc := range integrationRoutes {
		checkChain(t, h, cara, rc)
	}
}

func TestMountIntegrations_BothDependenciesOrNothing(t *testing.T) {
	for name, d := range map[string]apipublica.IntegrationsDeps{
		"without_store":    {Entitlements: withFeatures(entitlements.FeatureCRMBridge)},
		"without_resolver": {Integrations: newIntegrationStore()},
		"without_both":     {},
	} {
		t.Run(name, func(t *testing.T) {
			h := apipublicahelpertest.New(t)
			cara := apipublica.Nueva()
			apipublica.MountIntegrations(cara, h.Common(), d)
			wantPatterns(t, name, cara, nil)
			for _, rc := range integrationRoutes {
				rec := h.Call(cara, h.With(tenantA, "integrations.*"), rc.method, rc.target, rc.body)
				wantCode(t, name+" "+rc.id, rec, http.StatusNotFound)
			}
		})
	}
}

func TestMountIntegrations_NilMWPanicsAtMount(t *testing.T) {
	v := recuperar(func() {
		integrationCara(apipublica.Common{}, newIntegrationStore(), withFeatures(entitlements.FeatureCRMBridge))
	})
	if v == nil || esPendiente(v) || !strings.Contains(fmt.Sprint(v), "MountIntegrations") {
		t.Errorf("MountIntegrations con MW nil: panic = %v; quiero un panic de cableado que nombre MountIntegrations", v)
	}
}

// TestMountIntegrations_FeatureGate: sin `crm_bridge` las CUATRO responden el 403 del gate
// (también las lecturas), igual que con el resolver caído; el puerto ni se toca; el 403 del
// permiso gana al del gate; y en las W el 403 del gate queda auditado como failure.
func TestMountIntegrations_FeatureGate(t *testing.T) {
	down := entitlementshelpertest.NewFake()
	down.Err = errors.New("bd caída")
	for name, resolver := range map[string]entitlements.Resolver{
		"no_feature":     withFeatures(),
		"other_feature":  withFeatures(entitlements.FeatureLLMIntake),
		"resolver_fails": down,
	} {
		for _, rc := range integrationRoutes {
			t.Run(name+"_"+rc.id, func(t *testing.T) {
				h := apipublicahelpertest.New(t)
				store := newIntegrationStore()
				cara := integrationCara(h.Common(), store, resolver)

				rec := h.Call(cara, h.With(tenantA, rc.perm), rc.method, rc.target, rc.body)
				wantCode(t, rc.id, rec, http.StatusForbidden)
				wantExactBody(t, rc.id, rec, integrationDenied)
				if store.touched() != 0 {
					t.Errorf("%s sin la feature tocó el puerto %d veces, quiero 0", rc.id, store.touched())
				}
				records := h.Auditor().Records()
				if rc.resource == "" && len(records) != 0 {
					t.Errorf("%s es R y dejó %d registros de auditoría", rc.id, len(records))
				}
				if rc.resource != "" && (len(records) != 1 || records[0].Result != "failure" || records[0].Meta["status"] != http.StatusForbidden) {
					t.Errorf("%s es W: el 403 del gate debe quedar auditado como failure: %+v", rc.id, records)
				}

				rec = h.Call(cara, h.With(tenantA, "otra.cosa"), rc.method, rc.target, rc.body)
				wantErrorBody(t, rc.id+" sin permiso ni feature", rec, "permiso denegado")
			})
		}
	}
}

// TestMountIntegrations_OwnScopes: los permisos son propios. `content.*` no alcanza ninguna de
// las cuatro y `*.read` alcanza las dos lecturas y ninguna escritura.
func TestMountIntegrations_OwnScopes(t *testing.T) {
	h, store, cara := integrationSetup(t)
	for _, rc := range integrationRoutes {
		rec := h.Call(cara, h.With(tenantA, "content.*"), rc.method, rc.target, rc.body)
		wantCode(t, rc.id+" con content.*", rec, http.StatusForbidden)

		want := map[bool]int{true: http.StatusOK, false: http.StatusForbidden}[rc.resource == ""]
		rec = h.Call(cara, h.With(tenantA, "*.read"), rc.method, rc.target, rc.body)
		wantCode(t, rc.id+" con *.read", rec, want)
	}
	if store.upserts != 0 || store.deletes != 0 {
		t.Errorf("un token de solo lectura escribió (upserts=%d, deletes=%d)", store.upserts, store.deletes)
	}
}

// TestMountIntegrations_GetWithoutRowIsTheDefault: «no tengo puente» es un 200 con el default
// local/local, byte a byte, y la huella ni se pide.
func TestMountIntegrations_GetWithoutRowIsTheDefault(t *testing.T) {
	h, store, cara := integrationSetup(t)
	rec := h.Call(cara, h.With(tenantA, integrationReadPerm), http.MethodGet, integrationTarget, "")
	wantCode(t, "G13 sin fila", rec, http.StatusOK)
	wantExactBody(t, "G13 sin fila", rec, integrationDefaultBody)
	if store.fingerprints != 0 {
		t.Errorf("sin fila se pidió la huella %d veces, quiero 0", store.fingerprints)
	}
}

// TestMountIntegrations_GetBody: la forma entera, byte a byte, con los instantes en UTC, la
// huella corta y sin el secreto.
func TestMountIntegrations_GetBody(t *testing.T) {
	h, store, cara := integrationSetup(t)
	store.seed(t, integrationLive(tenantA), integrationFakeSigner)

	rec := h.Call(cara, h.With(tenantA, integrationReadPerm), http.MethodGet, integrationTarget, "")
	wantCode(t, "G13", rec, http.StatusOK)
	wantExactBody(t, "G13", rec, `{"configured":true,"catalog_adapter":"local","events_adapter":"webhook",`+
		`"endpoint_url":"`+integrationEndpoint+`","enabled":true,"secret_set":true,`+
		`"secret_fingerprint":"`+integrations.Fingerprint(integrationFakeSigner)+`",`+
		`"created_at":"2026-09-01T12:00:00Z","updated_at":"2026-09-01T12:00:00Z"}`)
	if len(integrations.Fingerprint(integrationFakeSigner)) != integrations.FingerprintHexLen {
		t.Fatal("la huella de prueba no tiene la longitud publicada: el test no probaría nada")
	}
}

// TestMountIntegrations_GetWithoutSecretInventsNoFingerprint: una fila a mano en local/local,
// sin secreto, sale con configured:true y sin huella (ni se pide); y si el puerto dice que hay
// secreto pero luego no lo encuentra, la huella se omite sin error.
func TestMountIntegrations_GetWithoutSecretInventsNoFingerprint(t *testing.T) {
	h, store, cara := integrationSetup(t)
	store.seed(t, integrations.TenantIntegration{TenantID: tenantA, CatalogAdapter: "local", EventsAdapter: "local"}, "")
	rec := h.Call(cara, h.With(tenantA, integrationReadPerm), http.MethodGet, integrationTarget, "")
	wantCode(t, "G13 sin secreto", rec, http.StatusOK)
	wantExactBody(t, "G13 sin secreto", rec, `{"configured":true,"catalog_adapter":"local","events_adapter":"local",`+
		`"enabled":false,"secret_set":false,"created_at":"2026-09-01T12:00:00Z","updated_at":"2026-09-01T12:00:00Z"}`)
	if store.fingerprints != 0 {
		t.Errorf("sin secreto se pidió la huella %d veces, quiero 0", store.fingerprints)
	}

	store.seed(t, integrationLive(tenantA), integrationFakeSigner)
	store.fingerprintLost = true
	rec = h.Call(cara, h.With(tenantA, integrationReadPerm), http.MethodGet, integrationTarget, "")
	wantCode(t, "G13 con la huella perdida", rec, http.StatusOK)
	if body := rec.Body.String(); !strings.Contains(body, `"secret_set":true`) || strings.Contains(body, "secret_fingerprint") {
		t.Errorf("con la huella perdida quiero secret_set:true y ninguna huella: %s", body)
	}
}

// TestMountIntegrations_ReadErrorsAre500: los fallos del puerto no se disfrazan ni se repiten.
func TestMountIntegrations_ReadErrorsAre500(t *testing.T) {
	boom := errors.New("postgres://usuario:ficticio@host/bd: conexión rechazada")
	cases := []struct {
		name     string
		sabotage func(*integrationStoreFake)
		target   string
		msg      string
	}{
		{"get_fails", func(s *integrationStoreFake) { s.getErr = boom }, integrationTarget, "no se pudo leer la integración"},
		{"fingerprint_fails", func(s *integrationStoreFake) { s.fingerprintErr = boom }, integrationTarget, "no se pudo leer la integración"},
		{"count_fails", func(s *integrationStoreFake) { s.countErr = boom }, integrationOutboxTarget, "no se pudo leer el estado de la cola de entregas"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, store, cara := integrationSetup(t)
			store.seed(t, integrationLive(tenantA), integrationFakeSigner)
			tc.sabotage(store)
			rec := h.Call(cara, h.With(tenantA, integrationReadPerm), http.MethodGet, tc.target, "")
			wantCode(t, tc.name, rec, http.StatusInternalServerError)
			wantErrorBody(t, tc.name, rec, tc.msg)
		})
	}
}

// TestMountIntegrations_Outbox: los cuatro contadores con el nombre del CHECK, la antigüedad en
// UTC solo si hay cola, todo a cero como respuesta sana, y el tenant del token (INV-8).
func TestMountIntegrations_Outbox(t *testing.T) {
	h, store, cara := integrationSetup(t)
	store.counts = integrations.OutboxCounts{Pending: 3, Delivering: 1, Delivered: 9_000_000_000, Dead: 2,
		OldestPendingAt: time.Date(2026, 9, 1, 9, 30, 0, 0, time.FixedZone("-03", -3*3600))}

	rec := h.Call(cara, h.With(tenantA, integrationReadPerm), http.MethodGet, integrationOutboxTarget+"?tenant_id="+tenantB, "")
	wantCode(t, "G14", rec, http.StatusOK)
	wantExactBody(t, "G14", rec, `{"pending":3,"delivering":1,"delivered":9000000000,"dead":2,"oldest_pending_at":"2026-09-01T12:30:00Z"}`)
	if store.tenant != tenantA {
		t.Errorf("CountOutbox recibió el tenant %q, quiero el del token %q", store.tenant, tenantA)
	}

	rec = h.Call(cara, h.With(tenantB, integrationReadPerm), http.MethodGet, integrationOutboxTarget+"?tenant_id="+tenantA, "")
	wantCode(t, "G14 de tenantB", rec, http.StatusOK)
	wantExactBody(t, "G14 de tenantB", rec, integrationOutboxZero)
	if store.gets != 0 {
		t.Errorf("G14 leyó la configuración %d veces: solo cuenta", store.gets)
	}
}

// TestMountIntegrations_DeleteIsIdempotentAndScoped: 204 sin cuerpo con o sin fila, borra la
// fila entera (secreto incluido) y solo la del tenant del token.
func TestMountIntegrations_DeleteIsIdempotentAndScoped(t *testing.T) {
	h, store, cara := integrationSetup(t)
	store.seed(t, integrationLive(tenantA), integrationFakeSigner)
	store.seed(t, integrationLive(tenantB), integrationRotatedSigner)
	token := h.With(tenantA, "integrations.*")

	for _, attempt := range []string{"with_row", "again_without_row"} {
		rec := h.Call(cara, token, http.MethodDelete, integrationTarget+"?tenant_id="+tenantB, `{"tenant_id":"`+tenantB+`"}`)
		wantCode(t, attempt, rec, http.StatusNoContent)
		if rec.Body.Len() != 0 {
			t.Errorf("%s: el 204 trae cuerpo %q", attempt, rec.Body.String())
		}
	}
	if _, found := store.IntegrationRow(tenantA); found {
		t.Error("tras el DELETE la fila de tenantA sigue ahí")
	}
	if _, found, err := store.GetTenantSecret(context.Background(), tenantA); err != nil || found {
		t.Error("tras el DELETE el secreto de tenantA sigue ahí")
	}
	if row, found := store.IntegrationRow(tenantB); !found || !row.HasSecret {
		t.Errorf("el DELETE de tenantA tocó la fila de tenantB: %+v (found=%v)", row, found)
	}
	rec := h.Call(cara, token, http.MethodGet, integrationTarget, "")
	wantExactBody(t, "G13 tras borrar", rec, integrationDefaultBody)

	store.deleteErr = errors.New("postgres://usuario:ficticio@host/bd: conexión rechazada")
	rec = h.Call(cara, token, http.MethodDelete, integrationTarget, "")
	wantCode(t, "G16 con el puerto caído", rec, http.StatusInternalServerError)
	wantErrorBody(t, "G16 con el puerto caído", rec, "no se pudo borrar la integración")
}

// TestMountIntegrations_NoDBTimeout: ninguna de las cuatro pone plazo propio a la BD.
func TestMountIntegrations_NoDBTimeout(t *testing.T) {
	h, store, cara := integrationSetup(t)
	for _, rc := range integrationRoutes {
		h.Call(cara, h.With(tenantA, rc.perm), rc.method, rc.target, rc.body)
	}
	if store.touched() == 0 || store.remaining != -1 {
		t.Errorf("alguna ruta puso plazo a la BD (quedaban %s tras %d llamadas); quiero -1: el viejo no lo tiene",
			store.remaining, store.touched())
	}
}

// TestMountIntegrations_TheSecretNeverLeaves: el barrido de fuga. Tras meter el secreto dos
// veces por el PUT (alta y rotación), ni las respuestas, ni el log, ni la auditoría lo llevan.
func TestMountIntegrations_TheSecretNeverLeaves(t *testing.T) {
	h, _, cara := integrationSetup(t)
	token := h.With(tenantA, "integrations.*")
	put := func(secret string) string {
		return `{"events_adapter":"webhook","endpoint_url":"` + integrationEndpoint + `","secret":"` + secret + `","enabled":true}`
	}
	responses := map[string]*httptest.ResponseRecorder{
		"PUT (alta)":       h.Call(cara, token, http.MethodPut, integrationTarget, put(integrationFakeSigner)),
		"GET":              h.Call(cara, token, http.MethodGet, integrationTarget, ""),
		"PUT (rotación)":   h.Call(cara, token, http.MethodPut, integrationTarget, put(integrationRotatedSigner)),
		"GET (tras rotar)": h.Call(cara, token, http.MethodGet, integrationTarget, ""),
		"outbox":           h.Call(cara, token, http.MethodGet, integrationOutboxTarget, ""),
		"PUT rechazado":    h.Call(cara, token, http.MethodPut, integrationTarget, strings.Replace(put(integrationFakeSigner), "webhook", "kafka", 1)),
		"DELETE":           h.Call(cara, token, http.MethodDelete, integrationTarget, ""),
	}
	sweep := func(where, data string) {
		t.Helper()
		for _, secret := range []string{integrationFakeSigner, integrationRotatedSigner} {
			if strings.Contains(data, secret) {
				t.Errorf("FUGA en %s: contiene el secreto de firma.\n%s", where, data)
			}
		}
	}
	for where, rec := range responses {
		// El recorrido tiene que haber ocurrido de verdad: si no, el barrido no probaría nada.
		if rec.Code >= http.StatusBadRequest && where != "PUT rechazado" {
			t.Fatalf("%s falló (código %d): el barrido no probaría nada; cuerpo %s", where, rec.Code, rec.Body.String())
		}
		sweep(where, rec.Body.String())
	}
	if !strings.Contains(responses["GET (tras rotar)"].Body.String(), integrations.Fingerprint(integrationRotatedSigner)) {
		t.Error("tras rotar, el GET no trae la huella del secreto nuevo: la rotación no ocurrió")
	}
	records := h.Auditor().Records()
	if len(records) != 4 {
		t.Fatalf("se auditaron %d escrituras, quiero 4 (tres PUT y un DELETE)", len(records))
	}
	sweep("la bitácora de auditoría", fmt.Sprintf("%+v", records))
	sweep("el log del servidor", fmt.Sprintf("%+v", h.Log().Entries()))
}
