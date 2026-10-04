package apipublica_test

// audit_test.go — cubre el contrato de audit.go (AuditReader, AuditDeps, MountAudit): C1.

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica/apipublicahelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
)

// auditReaderFake es AuditReader: apunta los argumentos de cada llamada y devuelve events o err.
type auditReaderFake struct {
	mu                   sync.Mutex
	events               []domain.AuditEvent
	err                  error
	tenant               string
	limit, offset, calls int
}

var _ apipublica.AuditReader = (*auditReaderFake)(nil)

func (f *auditReaderFake) ListAudit(_ context.Context, tenantID string, limit, offset int) ([]domain.AuditEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.tenant, f.limit, f.offset = tenantID, limit, offset
	return f.events, f.err
}

// auditCara monta C1 con k y el lector dado.
func auditCara(k apipublica.Common, reader apipublica.AuditReader) *apipublica.Cara {
	c := apipublica.Nueva()
	apipublica.MountAudit(c, k, apipublica.AuditDeps{Audit: reader})
	return c
}

func TestMountAudit_Chain(t *testing.T) {
	h := apipublicahelpertest.New(t)
	cara := auditCara(h.Common(), &auditReaderFake{})
	wantPatterns(t, "C1", cara, []string{"GET /api/v1/audit"})
	checkChain(t, h, cara, routeCase{id: "C1", method: http.MethodGet, target: "/api/v1/audit", perm: "audit.read", want: http.StatusOK})
}

func TestMountAudit_NoReaderIs404(t *testing.T) {
	h := apipublicahelpertest.New(t)
	c := apipublica.Nueva()
	apipublica.MountAudit(c, h.Common(), apipublica.AuditDeps{})
	wantPatterns(t, "C1 sin lector", c, nil)
	wantCode(t, "C1 sin lector", h.Call(c, h.With(tenantA, "audit.read"), http.MethodGet, "/api/v1/audit", ""), http.StatusNotFound)
}

func TestMountAudit_Body(t *testing.T) {
	h := apipublicahelpertest.New(t)
	tenant := tenantA
	at := time.Date(2026, 10, 4, 14, 30, 15, 999, time.FixedZone("UTC-5", -5*3600))
	reader := &auditReaderFake{events: []domain.AuditEvent{
		{ID: 7, TenantID: &tenant, Actor: subject, Action: "roles.write", Resource: "role", Result: "success",
			Meta: map[string]any{"status": 201}, At: at},
		{ID: 6, TenantID: &tenant, Actor: subject, Action: "members.write", Resource: "member", Result: "failure", At: at},
	}}
	rec := h.Call(auditCara(h.Common(), reader), h.With(tenantA, "audit.read"), http.MethodGet, "/api/v1/audit", "")
	wantCode(t, "C1", rec, http.StatusOK)
	var body struct {
		Events []map[string]any `json:"events"`
	}
	wantJSON(t, "C1", rec, &body)
	if len(body.Events) != 2 {
		t.Fatalf("C1: %d eventos, quiero 2 en el orden del puerto", len(body.Events))
	}
	first := body.Events[0]
	want := map[string]any{"id": float64(7), "actor": subject, "action": "roles.write", "resource": "role",
		"result": "success", "at": "2026-10-04T19:30:15Z"}
	for k, v := range want {
		if first[k] != v {
			t.Errorf("C1 evento 0: %s = %v, quiero %v", k, first[k], v)
		}
	}
	if meta, ok := first["meta"].(map[string]any); !ok || meta["status"] != float64(201) {
		t.Errorf("C1 evento 0: meta = %v, quiero {status: 201}", first["meta"])
	}
	if _, ok := first["tenant_id"]; ok {
		t.Error("C1: el evento trae tenant_id, que es el del token y no viaja")
	}
	if _, ok := body.Events[1]["meta"]; ok {
		t.Errorf("C1 evento 1: meta vacía viaja (%v), quiero que se omita", body.Events[1]["meta"])
	}
}

func TestMountAudit_EmptyIsArray(t *testing.T) {
	h := apipublicahelpertest.New(t)
	rec := h.Call(auditCara(h.Common(), &auditReaderFake{}), h.With(tenantA, "audit.read"), http.MethodGet, "/api/v1/audit", "")
	if got := rec.Body.String(); got != `{"events":[]}` && got != "{\"events\":[]}\n" {
		t.Errorf("C1 sin eventos: cuerpo %q, quiero {\"events\":[]}", got)
	}
}

func TestMountAudit_TenantFromTokenAndPaging(t *testing.T) {
	cases := []struct {
		name          string
		query         string
		limit, offset int
	}{
		{"defaults", "", 100, 0},
		{"explicit", "?limit=20&offset=40", 20, 40},
		{"clamped_to_500", "?limit=501", 500, 0},
		{"exactly_500", "?limit=500", 500, 0},
		{"zero_is_zero", "?limit=0&offset=0", 0, 0},
		{"negative_is_default", "?limit=-1&offset=-5", 100, 0},
		{"not_integer_is_default", "?limit=diez&offset=1.5", 100, 0},
		{"non_ascii_digits_are_default", "?limit=%D9%A3&offset=%EF%BC%91", 100, 0},
		{"tenant_in_query_ignored", "?tenant_id=" + apipublicahelpertest.TenantB, 100, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := apipublicahelpertest.New(t)
			reader := &auditReaderFake{}
			h.Call(auditCara(h.Common(), reader), h.With(tenantA, "audit.read"), http.MethodGet, "/api/v1/audit"+tc.query, "")
			if reader.calls != 1 || reader.tenant != tenantA || reader.limit != tc.limit || reader.offset != tc.offset {
				t.Errorf("ListAudit(%q, %d, %d) en %d llamadas; quiero (%q, %d, %d) en 1",
					reader.tenant, reader.limit, reader.offset, reader.calls, tenantA, tc.limit, tc.offset)
			}
		})
	}
}

func TestMountAudit_ReaderErrorIs500(t *testing.T) {
	h := apipublicahelpertest.New(t)
	rec := h.Call(auditCara(h.Common(), &auditReaderFake{err: errors.New("bd caída")}), h.With(tenantA, "audit.read"), http.MethodGet, "/api/v1/audit", "")
	wantCode(t, "C1 con el lector caído", rec, http.StatusInternalServerError)
	wantErrorBody(t, "C1 con el lector caído", rec, "no se pudo listar la auditoría")
}
