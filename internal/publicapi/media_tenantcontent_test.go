package publicapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	flowstore "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
)

// fakePresign es un doble de PresignUploader: NO golpea R2, captura la key firmada.
type fakePresign struct {
	gotKey string
	url    string
	err    error
}

func (f *fakePresign) GenerateUploadURL(_ context.Context, key string) (string, time.Time, error) {
	f.gotKey = key
	if f.err != nil {
		return "", time.Time{}, f.err
	}
	u := f.url
	if u == "" {
		u = "https://r2.example/put?sig=abc"
	}
	return u, time.Now().Add(15 * time.Minute), nil
}

// ============================ /api/v1/media/upload-url ============================

func TestUploadURL_OK_WithScope_TenantInKey(t *testing.T) {
	pres := &fakePresign{}
	mux := newAPI(publicapi.Deps{Media: pres}, apiKeys())

	rec := call(mux, keyAContent, http.MethodPost, "/api/v1/media/upload-url",
		`{"filename":"lista precios.pdf","mime":"application/pdf"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		URL       string `json:"url"`
		Key       string `json:"key"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// La key namespacea por tenant (INV-8): wapp/media/<tenantA>/...
	wantPrefix := "wapp/media/" + tenantA + "/"
	if !strings.HasPrefix(resp.Key, wantPrefix) {
		t.Fatalf("key=%q, quiero prefijo %q (aislamiento por tenant)", resp.Key, wantPrefix)
	}
	// El nombre se sanea (espacio → '_') y conserva la extensión.
	if !strings.HasSuffix(resp.Key, "-lista_precios.pdf") {
		t.Fatalf("key=%q no conserva el filename saneado", resp.Key)
	}
	// La key firmada por el presign es EXACTAMENTE la devuelta (coincide con lo que
	// el runtime presigna para descargar: model.MediaRef.Key verbatim).
	if pres.gotKey != resp.Key {
		t.Fatalf("presign firmó %q pero se devolvió %q", pres.gotKey, resp.Key)
	}
	if resp.URL == "" || resp.ExpiresAt == "" {
		t.Fatalf("respuesta incompleta: %+v", resp)
	}
}

func TestUploadURL_403_NoScope(t *testing.T) {
	pres := &fakePresign{}
	mux := newAPI(publicapi.Deps{Media: pres}, apiKeys())

	// keyARead: flows.read NO cubre media.upload.
	rec := call(mux, keyARead, http.MethodPost, "/api/v1/media/upload-url",
		`{"filename":"x.pdf","mime":"application/pdf"}`)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("code=%d, quiero 403; body=%s", rec.Code, rec.Body.String())
	}
	if pres.gotKey != "" {
		t.Fatal("no debió presignar sin scope")
	}
}

func TestUploadURL_401_NoToken(t *testing.T) {
	mux := newAPI(publicapi.Deps{Media: &fakePresign{}}, apiKeys())

	rec := call(mux, "", http.MethodPost, "/api/v1/media/upload-url",
		`{"filename":"x.pdf","mime":"application/pdf"}`)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d, quiero 401", rec.Code)
	}
}

func TestUploadURL_400_MissingFields(t *testing.T) {
	mux := newAPI(publicapi.Deps{Media: &fakePresign{}}, apiKeys())

	rec := call(mux, keyAContent, http.MethodPost, "/api/v1/media/upload-url",
		`{"filename":"","mime":""}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d, quiero 400; body=%s", rec.Code, rec.Body.String())
	}
}

// ============================ /api/v1/tenant-content ============================

func TestTenantContent_Upsert_Get_List_OK(t *testing.T) {
	repo := flowstore.NewMemoryRepository()
	mux := newAPI(publicapi.Deps{Content: repo}, apiKeys())

	blob := `{"prompt":"Elige","options":{"1":"a"}}`
	if rec := call(mux, keyAContent, http.MethodPut, "/api/v1/tenant-content/menu-x", blob); rec.Code != http.StatusOK {
		t.Fatalf("upsert code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}

	// GET del blob devuelve el JSON tal cual, bajo tenantA.
	rec := call(mux, keyAContent, http.MethodGet, "/api/v1/tenant-content/menu-x", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	if strings.TrimSpace(rec.Body.String()) != blob {
		t.Fatalf("blob=%q, quiero %q", rec.Body.String(), blob)
	}
	// Persistido bajo tenantA verificable por el store (misma forma que lee content.JSON).
	got, err := repo.GetTenantContent(context.Background(), tenantA, "menu-x")
	if err != nil || string(got) != blob {
		t.Fatalf("store: got=%q err=%v", string(got), err)
	}

	// List muestra la ref.
	recL := call(mux, keyAContent, http.MethodGet, "/api/v1/tenant-content", "")
	var list []struct {
		Ref string `json:"ref"`
	}
	if err := json.Unmarshal(recL.Body.Bytes(), &list); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if len(list) != 1 || list[0].Ref != "menu-x" {
		t.Fatalf("list=%+v, quiero [menu-x]", list)
	}
}

func TestTenantContent_Post_AliasUpsert(t *testing.T) {
	repo := flowstore.NewMemoryRepository()
	mux := newAPI(publicapi.Deps{Content: repo}, apiKeys())

	if rec := call(mux, keyAContent, http.MethodPost, "/api/v1/tenant-content/cat", `{"a":1}`); rec.Code != http.StatusOK {
		t.Fatalf("post upsert code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	if _, err := repo.GetTenantContent(context.Background(), tenantA, "cat"); err != nil {
		t.Fatalf("no se persistió por POST: %v", err)
	}
}

func TestTenantContent_CrossTenant_Isolation(t *testing.T) {
	repo := flowstore.NewMemoryRepository()
	mux := newAPI(publicapi.Deps{Content: repo}, apiKeys())

	// tenantA registra un blob.
	if rec := call(mux, keyAContent, http.MethodPut, "/api/v1/tenant-content/secret", `{"x":1}`); rec.Code != http.StatusOK {
		t.Fatalf("upsert A code=%d", rec.Code)
	}

	// tenantB NO lo ve en list.
	recL := call(mux, keyBContent, http.MethodGet, "/api/v1/tenant-content", "")
	var listB []any
	if err := json.Unmarshal(recL.Body.Bytes(), &listB); err != nil {
		t.Fatalf("unmarshal list B: %v", err)
	}
	if len(listB) != 0 {
		t.Fatalf("tenantB no debe ver contenido ajeno: %+v", listB)
	}
	// tenantB NO lo puede leer → 404 (el store filtra por tenant).
	if rec := call(mux, keyBContent, http.MethodGet, "/api/v1/tenant-content/secret", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant get code=%d, quiero 404", rec.Code)
	}
	// tenantB NO lo puede borrar → 404 (no revela existencia en otro tenant).
	if rec := call(mux, keyBContent, http.MethodDelete, "/api/v1/tenant-content/secret", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant delete code=%d, quiero 404", rec.Code)
	}
	// El de A sigue intacto.
	if _, err := repo.GetTenantContent(context.Background(), tenantA, "secret"); err != nil {
		t.Fatalf("el blob de A no debió tocarse: %v", err)
	}
}

func TestTenantContent_Delete_OK_Then_404(t *testing.T) {
	repo := flowstore.NewMemoryRepository()
	mux := newAPI(publicapi.Deps{Content: repo}, apiKeys())

	call(mux, keyAContent, http.MethodPut, "/api/v1/tenant-content/tmp", `{"a":1}`)

	if rec := call(mux, keyAContent, http.MethodDelete, "/api/v1/tenant-content/tmp", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete code=%d, quiero 204; body=%s", rec.Code, rec.Body.String())
	}
	// Ya no existe.
	if rec := call(mux, keyAContent, http.MethodGet, "/api/v1/tenant-content/tmp", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("get tras delete code=%d, quiero 404", rec.Code)
	}
	// Borrar de nuevo → 404.
	if rec := call(mux, keyAContent, http.MethodDelete, "/api/v1/tenant-content/tmp", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("re-delete code=%d, quiero 404", rec.Code)
	}
	if _, err := repo.GetTenantContent(context.Background(), tenantA, "tmp"); !errors.Is(err, flowstore.ErrTenantContentNotFound) {
		t.Fatalf("el blob no se borró del store: %v", err)
	}
}

func TestTenantContent_Upsert_403_NoScope(t *testing.T) {
	repo := flowstore.NewMemoryRepository()
	mux := newAPI(publicapi.Deps{Content: repo}, apiKeys())

	// keyARead: flows.read NO cubre content.write.
	rec := call(mux, keyARead, http.MethodPut, "/api/v1/tenant-content/x", `{"a":1}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code=%d, quiero 403; body=%s", rec.Code, rec.Body.String())
	}
}

func TestTenantContent_Upsert_401_NoToken(t *testing.T) {
	repo := flowstore.NewMemoryRepository()
	mux := newAPI(publicapi.Deps{Content: repo}, apiKeys())

	rec := call(mux, "", http.MethodPut, "/api/v1/tenant-content/x", `{"a":1}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d, quiero 401", rec.Code)
	}
}

func TestTenantContent_Upsert_400_InvalidJSON(t *testing.T) {
	repo := flowstore.NewMemoryRepository()
	mux := newAPI(publicapi.Deps{Content: repo}, apiKeys())

	rec := call(mux, keyAContent, http.MethodPut, "/api/v1/tenant-content/x", `no-es-json`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d, quiero 400; body=%s", rec.Code, rec.Body.String())
	}
}
