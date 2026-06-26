package httpapi_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// fakeRevoker registra la última llamada a RevokeLease y permite forzar un error.
type fakeRevoker struct {
	gotTenant string
	gotEdge   string
	calls     int
	err       error
}

func (f *fakeRevoker) RevokeLease(_ context.Context, tenantID, edgeID string) error {
	f.calls++
	f.gotTenant = tenantID
	f.gotEdge = edgeID
	return f.err
}

func TestRevokeLeaseHandler_OK(t *testing.T) {
	rev := &fakeRevoker{}
	h := httpapi.RevokeLeaseHandler(rev)

	body := `{"tenant_id":"t-1","edge_id":"edge-1"}`
	req := httptest.NewRequest(http.MethodPost, "/admin/leases/revoke", strings.NewReader(body))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusNoContent)
	}
	if rev.calls != 1 {
		t.Fatalf("RevokeLease llamado %d veces, want 1", rev.calls)
	}
	if rev.gotTenant != "t-1" || rev.gotEdge != "edge-1" {
		t.Fatalf("argumentos: got (%q,%q), want (t-1,edge-1)", rev.gotTenant, rev.gotEdge)
	}
}

func TestRevokeLeaseHandler_MissingFields(t *testing.T) {
	rev := &fakeRevoker{}
	h := httpapi.RevokeLeaseHandler(rev)

	req := httptest.NewRequest(http.MethodPost, "/admin/leases/revoke", strings.NewReader(`{"tenant_id":"t-1"}`))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if rev.calls != 0 {
		t.Fatalf("RevokeLease NO debería invocarse con campos faltantes (calls=%d)", rev.calls)
	}
}

func TestRevokeLeaseHandler_WrongMethod(t *testing.T) {
	rev := &fakeRevoker{}
	h := httpapi.RevokeLeaseHandler(rev)

	req := httptest.NewRequest(http.MethodGet, "/admin/leases/revoke", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
	if rev.calls != 0 {
		t.Fatalf("RevokeLease NO debería invocarse con método incorrecto (calls=%d)", rev.calls)
	}
}

func TestRevokeLeaseHandler_RevokerError(t *testing.T) {
	rev := &fakeRevoker{err: errors.New("boom")}
	h := httpapi.RevokeLeaseHandler(rev)

	req := httptest.NewRequest(http.MethodPost, "/admin/leases/revoke", strings.NewReader(`{"tenant_id":"t-1","edge_id":"edge-1"}`))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}
