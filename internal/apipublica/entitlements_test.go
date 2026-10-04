package apipublica_test

// entitlements_test.go — cubre el contrato de entitlements.go (EntitlementsDeps,
// MountEntitlements): C2. La cadena (401, 403, cero auditoría) la afirma también
// TestCommon_ReadChain sobre esta misma ruta.

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica/apipublicahelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements/entitlementshelpertest"
)

// resolverSpy es entitlements.Resolver: devuelve lo programado y apunta el tenant que le piden.
type resolverSpy struct {
	plan     string
	features []string
	ttl      time.Duration
	err      error
	tenant   string
}

var _ entitlements.Resolver = (*resolverSpy)(nil)

func (r *resolverSpy) Has(context.Context, string, string) (bool, error) { return false, nil }

func (r *resolverSpy) ListEffective(_ context.Context, tenantID string) (string, []string, error) {
	r.tenant = tenantID
	return r.plan, r.features, r.err
}

func (r *resolverSpy) CacheTTL() time.Duration { return r.ttl }

// callEntitlements monta C2 con el resolver dado y la pide con el permiso.
func callEntitlements(t *testing.T, resolver entitlements.Resolver) (int, string) {
	t.Helper()
	h := apipublicahelpertest.New(t)
	c := apipublica.Nueva()
	apipublica.MountEntitlements(c, h.Common(), apipublica.EntitlementsDeps{Entitlements: resolver})
	wantPatterns(t, "C2", c, []string{"GET /api/v1/entitlements"})
	rec := h.Call(c, h.With(tenantA, "entitlements.read"), http.MethodGet, "/api/v1/entitlements", "")
	return rec.Code, rec.Body.String()
}

func TestMountEntitlements_Body(t *testing.T) {
	cases := []struct {
		name     string
		resolver *resolverSpy
		want     string
	}{
		{"plan_and_features", &resolverSpy{plan: "pro", features: []string{"cart_basic", "llm_intent"}, ttl: time.Minute},
			`{"plan":"pro","features":["cart_basic","llm_intent"],"cache_ttl_seconds":60}`},
		{"no_features_is_empty_array", &resolverSpy{plan: "basic", ttl: time.Minute},
			`{"plan":"basic","features":[],"cache_ttl_seconds":60}`},
		{"ttl_truncated_to_seconds", &resolverSpy{plan: "basic", features: []string{}, ttl: 1500 * time.Millisecond},
			`{"plan":"basic","features":[],"cache_ttl_seconds":1}`},
		{"sub_second_ttl_is_zero", &resolverSpy{plan: "basic", features: []string{}, ttl: 10 * time.Millisecond},
			`{"plan":"basic","features":[],"cache_ttl_seconds":0}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, body := callEntitlements(t, tc.resolver)
			if code != http.StatusOK || (body != tc.want && body != tc.want+"\n") {
				t.Errorf("C2: %d %s, quiero 200 %s", code, body, tc.want)
			}
			if tc.resolver.tenant != tenantA {
				t.Errorf("C2: ListEffective recibió el tenant %q, quiero el del token %q", tc.resolver.tenant, tenantA)
			}
		})
	}
}

// TestMountEntitlements_WithTheModuleFake: con el doble del módulo (el que pasa la suite del
// puerto), las features encendidas en orden alfabético y su TTL por defecto de 60 s.
func TestMountEntitlements_WithTheModuleFake(t *testing.T) {
	f := entitlementshelpertest.NewFake()
	f.SetPlan(tenantA, "pro")
	f.Enable(tenantA, "llm_intent")
	f.Enable(tenantA, "cart_basic")
	f.Disable(tenantA, "media")
	code, body := callEntitlements(t, f)
	want := `{"plan":"pro","features":["cart_basic","llm_intent"],"cache_ttl_seconds":60}`
	if code != http.StatusOK || (body != want && body != want+"\n") {
		t.Errorf("C2: %d %s, quiero 200 %s", code, body, want)
	}
}

func TestMountEntitlements_ResolverErrorIs500(t *testing.T) {
	h := apipublicahelpertest.New(t)
	c := apipublica.Nueva()
	apipublica.MountEntitlements(c, h.Common(), apipublica.EntitlementsDeps{Entitlements: &resolverSpy{err: errors.New("bd caída")}})
	rec := h.Call(c, h.With(tenantA, "entitlements.read"), http.MethodGet, "/api/v1/entitlements", "")
	wantCode(t, "C2 con el resolver caído", rec, http.StatusInternalServerError)
	wantErrorBody(t, "C2 con el resolver caído", rec, "no se pudieron resolver los derechos del tenant")
}

func TestMountEntitlements_NoResolverIs404(t *testing.T) {
	h := apipublicahelpertest.New(t)
	c := apipublica.Nueva()
	apipublica.MountEntitlements(c, h.Common(), apipublica.EntitlementsDeps{})
	wantPatterns(t, "C2 sin resolver", c, nil)
	wantCode(t, "C2 sin resolver", h.Call(c, h.With(tenantA, "entitlements.read"), http.MethodGet, "/api/v1/entitlements", ""), http.StatusNotFound)
}
