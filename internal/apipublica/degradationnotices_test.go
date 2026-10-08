package apipublica_test

// degradationnotices_test.go — cubre el contrato de degradationnotices.go
// (DegradationNoticeLister, DegradationNoticesDeps, MountDegradationNotices): F4.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica/apipublicahelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements/entitlementshelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/degradation"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/degradation/degradationhelpertest"
)

const (
	noticesTarget  = "/api/v1/degradation-notices"
	patternNotices = "GET /api/v1/degradation-notices"

	noticesEmpty     = `{"notices":[],"limit":50,"offset":0}`
	noticesDenied    = `{"error":"feature_not_enabled","feature":"llm_intake"}`
	msgNoticesFailed = "no se pudieron leer los avisos de degradación"
)

// El puerto de F4 lo cumplen las piezas REALES del módulo inferencia nuevo.
var (
	_ apipublica.DegradationNoticeLister = (*degradation.Postgres)(nil)
	_ apipublica.DegradationNoticeLister = degradation.Store(nil)
	_ apipublica.DegradationNoticeLister = (*degradationhelpertest.Memoria)(nil)
)

// noticeListerSpy es DegradationNoticeLister: devuelve notices o err, o no contesta (block:
// espera a que el contexto muera, sin time.Sleep), y apunta lo que recibió y el plazo de su
// contexto (-1 = sin plazo).
type noticeListerSpy struct {
	notices   []degradation.Notice
	err       error
	block     bool
	calls     int
	tenant    string
	filter    degradation.ListFilter
	remaining time.Duration
}

var _ apipublica.DegradationNoticeLister = (*noticeListerSpy)(nil)

func (s *noticeListerSpy) List(ctx context.Context, tenantID string, f degradation.ListFilter) ([]degradation.Notice, error) {
	s.calls++
	s.tenant, s.filter, s.remaining = tenantID, f, -1
	if dl, ok := ctx.Deadline(); ok {
		s.remaining = time.Until(dl)
	}
	if s.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return s.notices, s.err
}

// noticesCara monta F4 con k y d.
func noticesCara(k apipublica.Common, d apipublica.DegradationNoticesDeps) *apipublica.Cara {
	c := apipublica.Nueva()
	apipublica.MountDegradationNotices(c, k, d)
	return c
}

// noticesDeps son las dependencias de F4 con `llm_intake` encendida y SIN `api_llm`.
func noticesDeps(lister apipublica.DegradationNoticeLister) apipublica.DegradationNoticesDeps {
	return apipublica.DegradationNoticesDeps{DegradationNotices: lister, Entitlements: withFeatures(entitlements.FeatureLLMIntake)}
}

func TestMountDegradationNotices_Chain(t *testing.T) {
	h := apipublicahelpertest.New(t)
	cara := noticesCara(h.Common(), noticesDeps(&noticeListerSpy{}))
	wantPatterns(t, "F4", cara, []string{patternNotices})
	checkChain(t, h, cara, routeCase{id: "F4", method: http.MethodGet, target: noticesTarget, perm: "llm.read", want: http.StatusOK})
}

func TestMountDegradationNotices_BothDependenciesOrNothing(t *testing.T) {
	for name, d := range map[string]apipublica.DegradationNoticesDeps{
		"without_lister":   {Entitlements: withFeatures(entitlements.FeatureLLMIntake)},
		"without_resolver": {DegradationNotices: &noticeListerSpy{}},
		"without_both":     {DBTimeout: time.Second},
	} {
		t.Run(name, func(t *testing.T) {
			h := apipublicahelpertest.New(t)
			cara := noticesCara(h.Common(), d)
			wantPatterns(t, name, cara, nil)
			wantCode(t, name, h.Call(cara, h.With(tenantA, "llm.read"), http.MethodGet, noticesTarget, ""), http.StatusNotFound)
		})
	}
}

func TestMountDegradationNotices_NilMWPanicsAtMount(t *testing.T) {
	v := recuperar(func() {
		apipublica.MountDegradationNotices(apipublica.Nueva(), apipublica.Common{}, noticesDeps(&noticeListerSpy{}))
	})
	if v == nil || esPendiente(v) || !strings.Contains(fmt.Sprint(v), "MountDegradationNotices") {
		t.Errorf("MountDegradationNotices con MW nil: panic = %v; quiero un panic de cableado que nombre MountDegradationNotices", v)
	}
}

// TestMountDegradationNotices_GateIsTheCapabilityNotTheRoute: el gate es `llm_intake`. Un
// tenant con el nivel y SIN `api_llm` (el de la vía local) lee sus avisos; uno con `api_llm` y
// sin el nivel recibe el 403 del gate, igual que con el resolver caído, y el puerto ni se
// consulta. Sin el permiso, el 403 es el del permiso aunque tampoco haya feature.
func TestMountDegradationNotices_GateIsTheCapabilityNotTheRoute(t *testing.T) {
	down := entitlementshelpertest.NewFake()
	down.Err = errors.New("bd caída")
	cases := []struct {
		name     string
		resolver entitlements.Resolver
		grant    string
		code     int
		body     string
	}{
		{"capability_without_api_llm_reads", withFeatures(entitlements.FeatureLLMIntake), "llm.read", http.StatusOK, noticesEmpty},
		{"api_llm_without_capability_is_403", withFeatures(entitlements.FeatureAPILLM), "llm.read", http.StatusForbidden, noticesDenied},
		{"no_features_is_403", withFeatures(), "llm.read", http.StatusForbidden, noticesDenied},
		{"resolver_down_fails_closed", down, "llm.read", http.StatusForbidden, noticesDenied},
		{"permission_goes_before_the_feature", withFeatures(), "otra.cosa", http.StatusForbidden, `{"error":"permiso denegado"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := apipublicahelpertest.New(t)
			lister := &noticeListerSpy{}
			cara := noticesCara(h.Common(), apipublica.DegradationNoticesDeps{DegradationNotices: lister, Entitlements: tc.resolver})
			rec := h.Call(cara, h.With(tenantA, tc.grant), http.MethodGet, noticesTarget, "")
			wantCode(t, tc.name, rec, tc.code)
			wantExactBody(t, tc.name, rec, tc.body)
			if wantCalls := map[bool]int{true: 1, false: 0}[tc.code == http.StatusOK]; lister.calls != wantCalls {
				t.Errorf("%s: el puerto recibió %d llamadas, quiero %d", tc.name, lister.calls, wantCalls)
			}
			if n := len(h.Auditor().Records()); n != 0 {
				t.Errorf("%s: F4 es R y dejó %d registros de auditoría, quiero 0", tc.name, n)
			}
		})
	}
}

// TestMountDegradationNotices_EmptyIsAnArray: sin avisos —el caso sano— responde [] y no null,
// también si el puerto devuelve un slice nil.
func TestMountDegradationNotices_EmptyIsAnArray(t *testing.T) {
	for name, notices := range map[string][]degradation.Notice{"nil": nil, "empty": {}} {
		h := apipublicahelpertest.New(t)
		cara := noticesCara(h.Common(), noticesDeps(&noticeListerSpy{notices: notices}))
		rec := h.Call(cara, h.With(tenantA, "llm.read"), http.MethodGet, noticesTarget, "")
		wantCode(t, name, rec, http.StatusOK)
		wantExactBody(t, name, rec, noticesEmpty)
	}
}

// TestMountDegradationNotices_Body: la forma de cada aviso, byte a byte: en el orden del puerto,
// instantes en UTC, `read` siempre y `read_at` solo si está leído, sin el tenant.
func TestMountDegradationNotices_Body(t *testing.T) {
	zone := time.FixedZone("-03", -3*3600)
	start := time.Date(2026, 9, 1, 9, 0, 0, 0, zone)
	unread := degradation.Notice{
		ID: "00000000-0000-4000-8000-000000000002", TenantID: tenantA, Reason: degradation.ReasonOllamaDown, Via: degradation.ViaLocal,
		WindowStart: start, WindowEnd: start.Add(15 * time.Minute), Occurrences: 7,
		CreatedAt: start.Add(time.Minute), LastSeenAt: start.Add(14 * time.Minute),
	}
	read := degradation.Notice{
		ID: "00000000-0000-4000-8000-000000000001", TenantID: tenantA, Reason: degradation.ReasonAPIError, Via: degradation.ViaAPI,
		WindowStart: start.Add(-time.Hour), WindowEnd: start.Add(-45 * time.Minute), Occurrences: 1,
		ReadAt:    start.Add(2 * time.Hour),
		CreatedAt: start.Add(-50 * time.Minute), LastSeenAt: start.Add(-50 * time.Minute),
	}
	want := `{"notices":[` +
		`{"id":"00000000-0000-4000-8000-000000000002","reason":"ollama_down","via":"local",` +
		`"window_start":"2026-09-01T12:00:00Z","window_end":"2026-09-01T12:15:00Z","occurrences":7,"read":false,` +
		`"created_at":"2026-09-01T12:01:00Z","last_seen_at":"2026-09-01T12:14:00Z"},` +
		`{"id":"00000000-0000-4000-8000-000000000001","reason":"api_error","via":"api",` +
		`"window_start":"2026-09-01T11:00:00Z","window_end":"2026-09-01T11:15:00Z","occurrences":1,"read":true,` +
		`"read_at":"2026-09-01T14:00:00Z","created_at":"2026-09-01T11:10:00Z","last_seen_at":"2026-09-01T11:10:00Z"}` +
		`],"limit":50,"offset":0}`

	h := apipublicahelpertest.New(t)
	cara := noticesCara(h.Common(), noticesDeps(&noticeListerSpy{notices: []degradation.Notice{unread, read}}))
	rec := h.Call(cara, h.With(tenantA, "llm.read"), http.MethodGet, noticesTarget, "")
	wantCode(t, "F4", rec, http.StatusOK)
	wantExactBody(t, "F4", rec, want)
	if strings.Contains(rec.Body.String(), tenantA) {
		t.Error("F4: la respuesta lleva el tenant; es el del token y no viaja")
	}
}

// TestMountDegradationNotices_Query: la query se acota sin rechazar nada, el filtro que llega al
// puerto es el efectivo y la respuesta lo declara. Con entradas adversarias.
func TestMountDegradationNotices_Query(t *testing.T) {
	cases := []struct {
		query         string
		limit, offset int
		unread        bool
	}{
		{"", 50, 0, false},
		{"?limit=10&offset=20", 10, 20, false},
		{"?limit=200", 200, 0, false},
		{"?limit=201", 200, 0, false},
		{"?limit=500", 200, 0, false},
		{"?limit=99999999999999999999", 50, 0, false},
		{"?limit=0", 50, 0, false},
		{"?limit=-5", 50, 0, false},
		{"?limit=abc", 50, 0, false},
		{"?limit=1.5", 50, 0, false},
		{"?limit=%D9%A3", 50, 0, false},
		{"?limit=%2010", 50, 0, false},
		{"?limit=", 50, 0, false},
		{"?limit=10&offset=-5", 10, 0, false},
		{"?offset=abc", 50, 0, false},
		{"?offset=%D9%A3", 50, 0, false},
		{"?unread=true", 50, 0, true},
		{"?unread=false", 50, 0, false},
		{"?unread=TRUE", 50, 0, false},
		{"?unread=1", 50, 0, false},
		{"?unread=true%20", 50, 0, false},
		{"?unread=", 50, 0, false},
		{"?unread=true&limit=3&offset=6", 3, 6, true},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			h := apipublicahelpertest.New(t)
			lister := &noticeListerSpy{}
			cara := noticesCara(h.Common(), noticesDeps(lister))
			rec := h.Call(cara, h.With(tenantA, "llm.read"), http.MethodGet, noticesTarget+tc.query, "")
			wantCode(t, tc.query, rec, http.StatusOK)
			want := degradation.ListFilter{SoloSinLeer: tc.unread, Limit: tc.limit, Offset: tc.offset}
			if !reflect.DeepEqual(lister.filter, want) {
				t.Errorf("%q: el puerto recibió el filtro %+v, quiero %+v", tc.query, lister.filter, want)
			}
			wantExactBody(t, tc.query, rec, fmt.Sprintf(`{"notices":[],"limit":%d,"offset":%d}`, tc.limit, tc.offset))
		})
	}
}

// TestMountDegradationNotices_WithTheModuleFake: con el doble del módulo (el que pasa la suite
// del puerto), cada tenant ve SOLO lo suyo —un tenant en la query no cuenta— y `unread=true`
// deja fuera lo leído.
func TestMountDegradationNotices_WithTheModuleFake(t *testing.T) {
	store := degradationhelpertest.NewMemoria()
	start := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	seed := func(tenant string, reason degradation.Reason, window int) {
		t.Helper()
		ws := start.Add(time.Duration(window) * degradation.VentanaPorDefecto)
		n := degradation.Notice{TenantID: tenant, Reason: reason, Via: degradation.ViaLocal, WindowStart: ws, WindowEnd: ws.Add(degradation.VentanaPorDefecto)}
		if _, err := store.Save(context.Background(), n); err != nil {
			t.Fatalf("sembrando el aviso de %s: %v", tenant, err)
		}
	}
	seed(tenantA, degradation.ReasonOllamaDown, 0)
	seed(tenantA, degradation.ReasonTimeout, 1)
	seed(tenantB, degradation.ReasonBreakerOpen, 2)
	for _, n := range store.Rows(tenantA) {
		if n.Reason == degradation.ReasonOllamaDown && !store.MarkRead(tenantA, n.ID, start.Add(time.Hour)) {
			t.Fatal("no se pudo marcar como leído el aviso sembrado")
		}
	}

	h := apipublicahelpertest.New(t)
	cara := noticesCara(h.Common(), noticesDeps(store))
	reasons := func(token, query string) []string {
		t.Helper()
		rec := h.Call(cara, token, http.MethodGet, noticesTarget+query, "")
		wantCode(t, query, rec, http.StatusOK)
		var body struct {
			Notices []struct {
				Reason string `json:"reason"`
			} `json:"notices"`
		}
		wantJSON(t, query, rec, &body)
		out := make([]string, 0, len(body.Notices))
		for _, n := range body.Notices {
			out = append(out, n.Reason)
		}
		return out
	}

	if got := reasons(h.With(tenantA, "llm.read"), "?tenant_id="+tenantB); !reflect.DeepEqual(got, []string{"timeout", "ollama_down"}) {
		t.Errorf("tenantA ve %v, quiero [timeout ollama_down]: lo suyo, el más reciente primero", got)
	}
	if got := reasons(h.With(tenantA, "llm.read"), "?unread=true"); !reflect.DeepEqual(got, []string{"timeout"}) {
		t.Errorf("tenantA con unread=true ve %v, quiero [timeout]", got)
	}
	if got := reasons(h.With(tenantB, "llm.read"), ""); !reflect.DeepEqual(got, []string{"breaker_open"}) {
		t.Errorf("tenantB ve %v, quiero [breaker_open]", got)
	}
}

// TestMountDegradationNotices_ListErrorIs500: el 500 no repite el error del puerto.
func TestMountDegradationNotices_ListErrorIs500(t *testing.T) {
	h := apipublicahelpertest.New(t)
	lister := &noticeListerSpy{err: errors.New("postgres://usuario:secreto@host/bd: conexión rechazada")}
	rec := h.Call(noticesCara(h.Common(), noticesDeps(lister)), h.With(tenantA, "llm.read"), http.MethodGet, noticesTarget, "")
	wantCode(t, "F4 con el puerto caído", rec, http.StatusInternalServerError)
	wantErrorBody(t, "F4 con el puerto caído", rec, msgNoticesFailed)
	if lister.tenant != tenantA {
		t.Errorf("List recibió el tenant %q, quiero el del token %q", lister.tenant, tenantA)
	}
}

// TestMountDegradationNotices_DBTimeout: la lectura va acotada por DBTimeout (<= 0 ⇒ 1,5 s), y
// un puerto que no contesta acaba en el 500 de siempre cuando vence.
func TestMountDegradationNotices_DBTimeout(t *testing.T) {
	for name, tc := range map[string]struct{ wired, floor, ceil time.Duration }{
		"zero_falls_to_1500ms":     {0, time.Second, 1500 * time.Millisecond},
		"negative_falls_to_1500ms": {-time.Second, time.Second, 1500 * time.Millisecond},
		"wired_timeout":            {5 * time.Second, 4 * time.Second, 5 * time.Second},
	} {
		t.Run(name, func(t *testing.T) {
			h := apipublicahelpertest.New(t)
			lister := &noticeListerSpy{}
			d := noticesDeps(lister)
			d.DBTimeout = tc.wired
			wantCode(t, name, h.Call(noticesCara(h.Common(), d), h.With(tenantA, "llm.read"), http.MethodGet, noticesTarget, ""), http.StatusOK)
			if lister.remaining <= tc.floor || lister.remaining > tc.ceil {
				t.Errorf("al contexto de List le quedaban %s, quiero entre %s y %s (-1 = sin plazo)", lister.remaining, tc.floor, tc.ceil)
			}
		})
	}
	t.Run("lister_that_does_not_answer", func(t *testing.T) {
		h := apipublicahelpertest.New(t)
		d := noticesDeps(&noticeListerSpy{block: true})
		d.DBTimeout = 20 * time.Millisecond
		rec := h.Call(noticesCara(h.Common(), d), h.With(tenantA, "llm.read"), http.MethodGet, noticesTarget, "")
		wantCode(t, "plazo vencido", rec, http.StatusInternalServerError)
		wantErrorBody(t, "plazo vencido", rec, msgNoticesFailed)
	})
}
