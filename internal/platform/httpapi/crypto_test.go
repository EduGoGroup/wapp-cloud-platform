package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// rekeyResponse refleja el JSON del endpoint para decodificar en los tests.
type rekeyResponse struct {
	Processed      int            `json:"processed"`
	CurrentKeyID   string         `json:"current_key_id"`
	PendingByKeyID map[string]int `json:"pending_by_key_id"`
}

func TestCryptoRekeyHandler_OK(t *testing.T) {
	var gotBatch int
	var calls int
	fn := func(_ context.Context, batch int) (crypto.Report, error) {
		calls++
		gotBatch = batch
		return crypto.Report{
			Processed:      7,
			CurrentKeyID:   "B",
			PendingByKeyID: map[string]int{"A": 3},
		}, nil
	}
	h := httpapi.CryptoRekeyHandler(fn)

	req := httptest.NewRequest(http.MethodPost, "/admin/crypto/rekey?batch=200", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusOK)
	}
	if calls != 1 || gotBatch != 200 {
		t.Fatalf("rekey llamado calls=%d batch=%d, want calls=1 batch=200", calls, gotBatch)
	}
	var resp rekeyResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decodificando respuesta: %v", err)
	}
	if resp.Processed != 7 || resp.CurrentKeyID != "B" || resp.PendingByKeyID["A"] != 3 {
		t.Fatalf("respuesta: got %+v, want processed=7 current=B pending[A]=3", resp)
	}
}

// TestCryptoRekeyHandler_DefaultBatch: sin ?batch, se pasa 0 (crypto.Rekey aplica
// su default).
func TestCryptoRekeyHandler_DefaultBatch(t *testing.T) {
	gotBatch := -1
	fn := func(_ context.Context, batch int) (crypto.Report, error) {
		gotBatch = batch
		return crypto.Report{CurrentKeyID: "1", PendingByKeyID: map[string]int{}}, nil
	}
	h := httpapi.CryptoRekeyHandler(fn)

	req := httptest.NewRequest(http.MethodPost, "/admin/crypto/rekey", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusOK)
	}
	if gotBatch != 0 {
		t.Fatalf("batch sin query: got %d, want 0 (default en crypto.Rekey)", gotBatch)
	}
	// pending vacío debe serializar como {} (mapa no nil).
	body := rec.Body.String()
	if !strings.Contains(body, `"pending_by_key_id":{}`) {
		t.Fatalf("pending vacío debe ser {}, got body=%s", body)
	}
}

func TestCryptoRekeyHandler_NilPendingSerializesEmpty(t *testing.T) {
	fn := func(_ context.Context, _ int) (crypto.Report, error) {
		// Report con PendingByKeyID nil: el handler debe emitir {} no null.
		return crypto.Report{Processed: 0, CurrentKeyID: "1"}, nil
	}
	h := httpapi.CryptoRekeyHandler(fn)

	req := httptest.NewRequest(http.MethodPost, "/admin/crypto/rekey", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusOK)
	}
	if body := rec.Body.String(); !strings.Contains(body, `"pending_by_key_id":{}`) {
		t.Fatalf("PendingByKeyID nil debe serializar como {}, got body=%s", body)
	}
}

func TestCryptoRekeyHandler_BadBatch(t *testing.T) {
	calls := 0
	fn := func(_ context.Context, _ int) (crypto.Report, error) {
		calls++
		return crypto.Report{}, nil
	}
	h := httpapi.CryptoRekeyHandler(fn)

	for _, q := range []string{"?batch=abc", "?batch=-5"} {
		req := httptest.NewRequest(http.MethodPost, "/admin/crypto/rekey"+q, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status got %d, want %d", q, rec.Code, http.StatusBadRequest)
		}
	}
	if calls != 0 {
		t.Fatalf("rekey NO debe invocarse con batch inválido (calls=%d)", calls)
	}
}

func TestCryptoRekeyHandler_WrongMethod(t *testing.T) {
	calls := 0
	fn := func(_ context.Context, _ int) (crypto.Report, error) {
		calls++
		return crypto.Report{}, nil
	}
	h := httpapi.CryptoRekeyHandler(fn)

	req := httptest.NewRequest(http.MethodGet, "/admin/crypto/rekey", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
	if calls != 0 {
		t.Fatalf("rekey NO debe invocarse con método incorrecto (calls=%d)", calls)
	}
}

// TestCryptoRekeyHandler_Error: un fallo de rotación → 500 con mensaje genérico
// (sin filtrar el detalle del error, que podría referenciar key_id/PK).
func TestCryptoRekeyHandler_Error(t *testing.T) {
	fn := func(_ context.Context, _ int) (crypto.Report, error) {
		return crypto.Report{}, errors.New("rekey: ReWrap de (tenant=t kind=phone value_bidx=deadbeef) con key_id \"A\": boom")
	}
	h := httpapi.CryptoRekeyHandler(fn)

	req := httptest.NewRequest(http.MethodPost, "/admin/crypto/rekey", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if body := rec.Body.String(); strings.Contains(body, "value_bidx") || strings.Contains(body, "boom") {
		t.Fatalf("la respuesta NO debe filtrar el detalle del error: %q", body)
	}
}
