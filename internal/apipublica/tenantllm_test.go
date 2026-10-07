//go:build pendiente

package apipublica_test

// tenantllm_test.go — cubre el contrato de tenantllm.go (TenantLLMStore, TenantLLMDeps,
// MountTenantLLM): el montaje «las tres o ninguna», la cadena y el gate `api_llm` de F1–F3, la
// lectura F1, el borrado F3 y la promesa de que la clave no sale por ninguna puerta. La
// semántica del PUT va en tenantllm_put_test.go y la lectura de su cuerpo en
// tenantllm_body_test.go (E-13). Aquí viven los dobles que comparten los tres ficheros.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica/apipublicahelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements/entitlementshelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/tenantllm"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/tenantllm/tenantllmhelpertest"
)

const (
	llmTarget    = "/api/v1/tenant-llm"
	llmReadPerm  = "llm.read"
	llmWritePerm = "llm.write"
	llmResource  = "tenant_llm"

	patternLLMGet    = "GET /api/v1/tenant-llm"
	patternLLMPut    = "PUT /api/v1/tenant-llm"
	patternLLMDelete = "DELETE /api/v1/tenant-llm"

	// llmKey tiene la forma de una clave de proveedor, pero no es una credencial: es un valor
	// de test.
	//nolint:gosec // no es una credencial: es el valor de un test
	llmKey   = "sk-ant-api03-clave-de-prueba-0123456789"
	llmModel = "claude-modelo-de-prueba"

	llmNotConfigured = `{"configured":false,"via":"local","key_set":false}`
	llmAPIDenied     = `{"error":"feature_not_enabled","feature":"api_llm"}`
	msgLLMReadFailed = "no se pudo leer la configuración LLM"
)

// Los puertos de F1–F3 los cumplen las piezas REALES del módulo inferencia nuevo.
var (
	_ apipublica.TenantLLMStore = (*tenantllm.Postgres)(nil)
	_ apipublica.TenantLLMStore = tenantllm.Store(nil)
	_ apipublica.TenantLLMStore = (*tenantllmhelpertest.Memoria)(nil)
)

// llmUpsert es lo que recibió Upsert.
type llmUpsert struct {
	cfg         tenantllm.Config
	apiKey      string
	consentedAt time.Time
}

// llmStoreSpy es TenantLLMStore sobre el doble en memoria del módulo: delega en él salvo donde
// el test le pide fallar o devolver una fila fija, y apunta lo que recibió.
type llmStoreSpy struct {
	*tenantllmhelpertest.Memoria
	getErr, upsertErr, deleteErr error
	// rereadErr es el fallo de Get SOLO después de un Upsert (la relectura del PUT).
	rereadErr error
	// dropUpserts hace que Upsert responda nil sin guardar: la relectura no encuentra fila.
	dropUpserts bool
	// fixed, si no es nil, es lo que devuelve Get.
	fixed   *tenantllm.Config
	gets    []string
	deletes []string
	upserts []llmUpsert
	// bounded dice si alguna llamada llegó con un contexto con plazo.
	bounded bool
}

var _ apipublica.TenantLLMStore = (*llmStoreSpy)(nil)

func newLLMStore() *llmStoreSpy {
	return &llmStoreSpy{Memoria: tenantllmhelpertest.NewMemoria()}
}

func (s *llmStoreSpy) note(ctx context.Context) {
	if _, ok := ctx.Deadline(); ok {
		s.bounded = true
	}
}

func (s *llmStoreSpy) Get(ctx context.Context, tenantID string) (tenantllm.Config, bool, error) {
	s.note(ctx)
	s.gets = append(s.gets, tenantID)
	switch {
	case s.getErr != nil:
		return tenantllm.Config{}, false, s.getErr
	case s.rereadErr != nil && len(s.upserts) > 0:
		return tenantllm.Config{}, false, s.rereadErr
	case s.fixed != nil:
		return *s.fixed, true, nil
	}
	return s.Memoria.Get(ctx, tenantID)
}

func (s *llmStoreSpy) Upsert(ctx context.Context, cfg tenantllm.Config, apiKey string, consentedAt time.Time) error {
	s.note(ctx)
	s.upserts = append(s.upserts, llmUpsert{cfg, apiKey, consentedAt})
	if s.upsertErr != nil || s.dropUpserts {
		return s.upsertErr
	}
	return s.Memoria.Upsert(ctx, cfg, apiKey, consentedAt)
}

func (s *llmStoreSpy) Delete(ctx context.Context, tenantID string) error {
	s.note(ctx)
	s.deletes = append(s.deletes, tenantID)
	if s.deleteErr != nil {
		return s.deleteErr
	}
	return s.Memoria.Delete(ctx, tenantID)
}

// touched dice cuántas llamadas recibió el almacén.
func (s *llmStoreSpy) touched() int { return len(s.gets) + len(s.upserts) + len(s.deletes) }

// withFeatures devuelve un resolver con esas features encendidas para tenantA y tenantB.
func withFeatures(features ...string) *entitlementshelpertest.Fake {
	f := entitlementshelpertest.NewFake()
	for _, feature := range features {
		f.Enable(tenantA, feature)
		f.Enable(tenantB, feature)
	}
	return f
}

// llmCara monta F1–F3 con k, el almacén y el resolver dados.
func llmCara(k apipublica.Common, store apipublica.TenantLLMStore, resolver entitlements.Resolver) *apipublica.Cara {
	c := apipublica.Nueva()
	apipublica.MountTenantLLM(c, k, apipublica.TenantLLMDeps{TenantLLM: store, Entitlements: resolver})
	return c
}

// llmSetup es el montaje habitual: arnés, almacén espía y la cara con `api_llm` encendida.
func llmSetup(t *testing.T) (*apipublicahelpertest.Harness, *llmStoreSpy, *apipublica.Cara) {
	t.Helper()
	h := apipublicahelpertest.New(t)
	store := newLLMStore()
	return h, store, llmCara(h.Common(), store, withFeatures(entitlements.FeatureAPILLM))
}

// validLLMBody es un PUT admisible de la vía api, como mapa para que cada test lo retoque.
func validLLMBody() map[string]any {
	return map[string]any{
		"via": tenantllm.ViaAPI, "provider": tenantllm.ProviderAnthropic,
		"model": llmModel, "api_key": llmKey, "consented": true,
	}
}

// llmJSON serializa el cuerpo de un PUT.
func llmJSON(t *testing.T, body map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("serializando el cuerpo de prueba: %v", err)
	}
	return string(raw)
}

// putLLM hace el PUT como tenantA con el permiso de escritura.
func putLLM(h *apipublicahelpertest.Harness, cara http.Handler, body string) *httptest.ResponseRecorder {
	return h.Call(cara, h.With(tenantA, llmWritePerm), http.MethodPut, llmTarget, body)
}

// wantExactBody exige el cuerpo byte a byte.
func wantExactBody(t *testing.T, what string, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	if got := rec.Body.String(); got != want {
		t.Errorf("%s: cuerpo\n  %s\nquiero (byte a byte)\n  %s", what, got, want)
	}
}

func TestMountTenantLLM_Chain(t *testing.T) {
	h, _, cara := llmSetup(t)
	wantPatterns(t, "F1–F3", cara, []string{patternLLMGet, patternLLMPut, patternLLMDelete})
	checkChain(t, h, cara, routeCase{id: "F1", method: http.MethodGet, target: llmTarget, perm: llmReadPerm, want: http.StatusOK})
	checkChain(t, h, cara, routeCase{id: "F2", method: http.MethodPut, target: llmTarget, body: llmJSON(t, validLLMBody()),
		perm: llmWritePerm, resource: llmResource, want: http.StatusOK})
	checkChain(t, h, cara, routeCase{id: "F3", method: http.MethodDelete, target: llmTarget,
		perm: llmWritePerm, resource: llmResource, want: http.StatusNoContent})
}

func TestMountTenantLLM_AllThreeOrNone(t *testing.T) {
	for name, d := range map[string]apipublica.TenantLLMDeps{
		"without_store":    {Entitlements: withFeatures(entitlements.FeatureAPILLM)},
		"without_resolver": {TenantLLM: newLLMStore()},
		"without_both":     {},
	} {
		t.Run(name, func(t *testing.T) {
			h := apipublicahelpertest.New(t)
			c := apipublica.Nueva()
			apipublica.MountTenantLLM(c, h.Common(), d)
			wantPatterns(t, name, c, nil)
			for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
				rec := h.Call(c, h.With(tenantA, "llm.*"), method, llmTarget, "")
				wantCode(t, name+": "+method, rec, http.StatusNotFound)
			}
		})
	}
}

func TestMountTenantLLM_NilMWPanicsAtMount(t *testing.T) {
	d := apipublica.TenantLLMDeps{TenantLLM: newLLMStore(), Entitlements: withFeatures(entitlements.FeatureAPILLM)}
	v := recuperar(func() { apipublica.MountTenantLLM(apipublica.Nueva(), apipublica.Common{}, d) })
	if v == nil || esPendiente(v) || !strings.Contains(fmt.Sprint(v), "MountTenantLLM") {
		t.Errorf("MountTenantLLM con MW nil: panic = %v; quiero un panic de cableado que nombre MountTenantLLM", v)
	}
}

// TestMountTenantLLM_FeatureGate: con el permiso y sin `api_llm`, las tres cortan con el 403 del
// gate sin tocar el almacén. Tener `llm_intake` no abre la vía, un override apagado tampoco, y
// un resolver caído corta igual (fail-closed). El corte queda auditado en las dos escrituras.
func TestMountTenantLLM_FeatureGate(t *testing.T) {
	down := entitlementshelpertest.NewFake()
	down.Err = errors.New("bd caída")
	off := withFeatures()
	off.Disable(tenantA, entitlements.FeatureAPILLM)
	resolvers := map[string]entitlements.Resolver{
		"capability_does_not_open_the_route": withFeatures(entitlements.FeatureLLMIntake),
		"override_disabled":                  off,
		"resolver_down_fails_closed":         down,
	}
	routes := []struct {
		method, perm string
		audited      bool
	}{
		{http.MethodGet, llmReadPerm, false},
		{http.MethodPut, llmWritePerm, true},
		{http.MethodDelete, llmWritePerm, true},
	}
	for name, resolver := range resolvers {
		for _, route := range routes {
			t.Run(name+"/"+route.method, func(t *testing.T) {
				h := apipublicahelpertest.New(t)
				store := newLLMStore()
				cara := llmCara(h.Common(), store, resolver)

				rec := h.Call(cara, h.With(tenantA, route.perm), route.method, llmTarget, llmJSON(t, validLLMBody()))

				wantCode(t, "sin api_llm", rec, http.StatusForbidden)
				wantExactBody(t, "sin api_llm", rec, llmAPIDenied)
				if store.touched() != 0 {
					t.Errorf("el almacén recibió %d llamadas con el gate cerrado, quiero 0", store.touched())
				}
				records := h.Auditor().Records()
				if !route.audited {
					if len(records) != 0 {
						t.Errorf("F1 es R: el corte del gate dejó %d registros, quiero 0", len(records))
					}
					return
				}
				if len(records) != 1 {
					t.Fatalf("el corte del gate dejó %d registros de auditoría, quiero exactamente 1", len(records))
				}
				r := records[0]
				if r.TenantID != tenantA || r.Action != llmWritePerm || r.Resource != llmResource || r.Result != "failure" || r.Meta["status"] != http.StatusForbidden {
					t.Errorf("registro %+v, quiero tenant %s, action %s, resource %s, result failure, status 403", r, tenantA, llmWritePerm, llmResource)
				}
			})
		}
	}
}

// TestMountTenantLLM_PermissionGoesBeforeTheFeature: sin el permiso Y sin la feature, el 403 es
// el del permiso; y leer no da derecho a escribir.
func TestMountTenantLLM_PermissionGoesBeforeTheFeature(t *testing.T) {
	cases := []struct{ name, method, grant string }{
		{"get_without_permission", http.MethodGet, "otra.cosa"},
		{"put_with_read_only", http.MethodPut, llmReadPerm},
		{"delete_with_read_only", http.MethodDelete, llmReadPerm},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := apipublicahelpertest.New(t)
			store := newLLMStore()
			cara := llmCara(h.Common(), store, withFeatures())
			rec := h.Call(cara, h.With(tenantA, tc.grant), tc.method, llmTarget, llmJSON(t, validLLMBody()))
			wantCode(t, tc.name, rec, http.StatusForbidden)
			wantErrorBody(t, tc.name, rec, "permiso denegado")
			if store.touched() != 0 || len(h.Auditor().Records()) != 0 {
				t.Errorf("%s: %d llamadas al almacén y %d registros, quiero 0 y 0", tc.name, store.touched(), len(h.Auditor().Records()))
			}
		})
	}
}

func TestMountTenantLLM_Get_NoRowIsLocalNot404(t *testing.T) {
	h, store, cara := llmSetup(t)
	rec := h.Call(cara, h.With(tenantA, llmReadPerm), http.MethodGet, llmTarget, "")
	wantCode(t, "F1 sin fila", rec, http.StatusOK)
	wantExactBody(t, "F1 sin fila", rec, llmNotConfigured)
	if !slices.Equal(store.gets, []string{tenantA}) {
		t.Errorf("Get recibió %v, quiero una llamada con el tenant del token", store.gets)
	}
}

func TestMountTenantLLM_Get_Body(t *testing.T) {
	// Un instante con zona: la respuesta lo da en UTC.
	zone := time.FixedZone("-03", -3*3600)
	consented := time.Date(2026, 9, 1, 9, 30, 15, 987, zone)
	created := time.Date(2026, 8, 31, 22, 0, 0, 0, zone)
	updated := time.Date(2026, 9, 2, 0, 0, 1, 0, time.UTC)
	cases := []struct {
		name string
		row  tenantllm.Config
		want string
	}{
		{"api_row", tenantllm.Config{TenantID: tenantA, Via: tenantllm.ViaAPI, Provider: tenantllm.ProviderAnthropic, Model: llmModel,
			HasAPIKey: true, ConsentedAt: consented, CreatedAt: created, UpdatedAt: updated},
			`{"configured":true,"via":"api","provider":"anthropic","model":"` + llmModel + `","key_set":true,` +
				`"consented_at":"2026-09-01T12:30:15Z","created_at":"2026-09-01T01:00:00Z","updated_at":"2026-09-02T00:00:01Z"}`},
		{"local_row_omits_the_api_axis", tenantllm.Config{TenantID: tenantA, Via: tenantllm.ViaLocal, CreatedAt: created, UpdatedAt: updated},
			`{"configured":true,"via":"local","key_set":false,"created_at":"2026-09-01T01:00:00Z","updated_at":"2026-09-02T00:00:01Z"}`},
		{"zero_instants_are_omitted", tenantllm.Config{TenantID: tenantA, Via: tenantllm.ViaLocal},
			`{"configured":true,"via":"local","key_set":false}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, store, cara := llmSetup(t)
			store.fixed = &tc.row
			rec := h.Call(cara, h.With(tenantA, llmReadPerm), http.MethodGet, llmTarget, "")
			wantCode(t, tc.name, rec, http.StatusOK)
			wantExactBody(t, tc.name, rec, tc.want)
		})
	}
}

func TestMountTenantLLM_Get_StoreErrorIs500(t *testing.T) {
	h, store, cara := llmSetup(t)
	store.getErr = errors.New("postgres://usuario:secreto@host/bd: conexión rechazada")
	rec := h.Call(cara, h.With(tenantA, llmReadPerm), http.MethodGet, llmTarget, "")
	wantCode(t, "F1 con el almacén caído", rec, http.StatusInternalServerError)
	wantErrorBody(t, "F1 con el almacén caído", rec, msgLLMReadFailed)
}

// TestMountTenantLLM_Delete: revoca la fila entera (credencial y consentimiento) y es idempotente.
func TestMountTenantLLM_Delete(t *testing.T) {
	h, store, cara := llmSetup(t)
	wantCode(t, "F2 previo", putLLM(h, cara, llmJSON(t, validLLMBody())), http.StatusOK)

	for _, attempt := range []string{"with_row", "without_row"} {
		rec := h.Call(cara, h.With(tenantA, llmWritePerm), http.MethodDelete, llmTarget, "")
		wantCode(t, "F3 "+attempt, rec, http.StatusNoContent)
		if rec.Body.Len() != 0 {
			t.Errorf("F3 %s: cuerpo %q, quiero ninguno", attempt, rec.Body.String())
		}
	}
	if !slices.Equal(store.deletes, []string{tenantA, tenantA}) {
		t.Errorf("Delete recibió %v, quiero dos llamadas con el tenant del token", store.deletes)
	}
	if _, found := store.Row(tenantA); found {
		t.Error("tras F3 la fila sigue en el almacén: no se revocó")
	}
	rec := h.Call(cara, h.With(tenantA, llmReadPerm), http.MethodGet, llmTarget, "")
	wantExactBody(t, "F1 tras F3", rec, llmNotConfigured)
}

func TestMountTenantLLM_Delete_StoreErrorIs500(t *testing.T) {
	h, store, cara := llmSetup(t)
	store.deleteErr = errors.New("bd caída")
	rec := h.Call(cara, h.With(tenantA, llmWritePerm), http.MethodDelete, llmTarget, "")
	wantCode(t, "F3 con el almacén caído", rec, http.StatusInternalServerError)
	wantErrorBody(t, "F3 con el almacén caído", rec, "no se pudo borrar la configuración LLM")
}

// TestMountTenantLLM_TheKeyNeverLeaves: ni las respuestas, ni la auditoría, ni el access-log
// llevan la clave —ni entera ni su prefijo—, nadie se la pide al almacén, y el puerto no tiene
// con qué: sus métodos son exactamente Delete, Get y Upsert.
func TestMountTenantLLM_TheKeyNeverLeaves(t *testing.T) {
	h, store, cara := llmSetup(t)
	seen := map[string]string{}
	seen["PUT"] = putLLM(h, cara, llmJSON(t, validLLMBody())).Body.String()
	seen["GET"] = h.Call(cara, h.With(tenantA, llmReadPerm), http.MethodGet, llmTarget, "").Body.String()
	seen["DELETE"] = h.Call(cara, h.With(tenantA, llmWritePerm), http.MethodDelete, llmTarget, "").Body.String()
	seen["auditoría"] = fmt.Sprintf("%+v", h.Auditor().Records())
	seen["access-log"] = fmt.Sprintf("%+v", h.Log().Entries())

	if !strings.Contains(seen["PUT"], `"key_set":true`) {
		t.Fatalf("el PUT no guardó la clave (%s): el test no probaría nada", seen["PUT"])
	}
	for door, text := range seen {
		// El prefijo además de la clave entera: un truncado «de cortesía» seguiría siendo fuga.
		if strings.Contains(text, llmKey) || strings.Contains(text, "sk-ant-") {
			t.Errorf("FUGA por %s: lleva la API key o su prefijo: %s", door, text)
		}
	}
	if n := store.APIKeyCalls(); n != 0 {
		t.Errorf("la cara pidió la credencial al almacén %d veces, quiero 0", n)
	}

	port := reflect.TypeOf((*apipublica.TenantLLMStore)(nil)).Elem()
	var methods []string
	for i := range port.NumMethod() {
		methods = append(methods, port.Method(i).Name)
	}
	if !slices.Equal(methods, []string{"Delete", "Get", "Upsert"}) {
		t.Errorf("TenantLLMStore tiene los métodos %v, quiero exactamente [Delete Get Upsert]: ninguno devuelve la credencial", methods)
	}
}

// TestMountTenantLLM_TenantIsTheTokens: cada tenant ve y toca solo lo suyo (INV-7).
func TestMountTenantLLM_TenantIsTheTokens(t *testing.T) {
	h, store, cara := llmSetup(t)
	wantCode(t, "F2 de tenantA", putLLM(h, cara, llmJSON(t, validLLMBody())), http.StatusOK)

	rec := h.Call(cara, h.With(tenantB, llmReadPerm), http.MethodGet, llmTarget+"?tenant_id="+tenantA, "")
	wantExactBody(t, "F1 de tenantB", rec, llmNotConfigured)
	wantCode(t, "F3 de tenantB", h.Call(cara, h.With(tenantB, llmWritePerm), http.MethodDelete, llmTarget, ""), http.StatusNoContent)

	if _, found := store.Row(tenantA); !found {
		t.Error("el DELETE de tenantB borró la fila de tenantA")
	}
	if got := store.gets[len(store.gets)-1]; got != tenantB {
		t.Errorf("el Get de tenantB fue con el tenant %q, quiero %q (un tenant en la query no cuenta)", got, tenantB)
	}
	if store.bounded {
		t.Error("el almacén recibió un contexto con plazo; F1–F3 lo llaman con el de la petición, sin plazo propio")
	}
}
