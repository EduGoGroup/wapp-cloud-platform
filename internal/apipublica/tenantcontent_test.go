//go:build pendiente

package apipublica_test

// tenantcontent_test.go — cubre el contrato de tenantcontent.go (TenantContentStore,
// TenantContentDeps, MountTenantContent): el montaje sin condición de las cinco rutas, sus
// cadenas, el upsert con su techo, el listado, la lectura y el borrado de I6–I10.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica/apipublicahelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo/catalogimport"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
)

const (
	tenantContentPatternPut    = "PUT /api/v1/tenant-content/{ref}"
	tenantContentPatternPost   = "POST /api/v1/tenant-content/{ref}"
	tenantContentPatternDelete = "DELETE /api/v1/tenant-content/{ref}"
	tenantContentPatternList   = "GET /api/v1/tenant-content"
	tenantContentPatternGet    = "GET /api/v1/tenant-content/{ref}"

	tenantContentBase     = "/api/v1/tenant-content"
	tenantContentRef      = "menu-principal"
	tenantContentTarget   = tenantContentBase + "/" + tenantContentRef
	tenantContentRead     = "content.read"
	tenantContentWrite    = "content.write"
	tenantContentResource = "tenant_content"
	tenantContentBlob     = `{"prompt":"Elige","options":{"1":"a"}}`

	tenantContentMsgNoStore  = "store de contenido no configurado"
	tenantContentMsgBadBody  = "el cuerpo debe ser un JSON válido (el blob de contenido)"
	tenantContentMsgUnread   = "no se pudo leer el cuerpo"
	tenantContentMsgUpsert   = "no se pudo registrar el contenido"
	tenantContentMsgList     = "no se pudo listar el contenido"
	tenantContentMsgGet      = "no se pudo leer el contenido"
	tenantContentMsgDelete   = "no se pudo borrar el contenido"
	tenantContentMsgNotFound = "contenido no encontrado"
)

// El puerto lo cumplen los dos repositorios del módulo conversación nuevo.
var (
	_ apipublica.TenantContentStore = (*store.PostgresRepository)(nil)
	_ apipublica.TenantContentStore = (*store.MemoryRepository)(nil)
)

// tenantContentStoreSpy es TenantContentStore en memoria, acotado por tenant: guarda los blobs
// que recibe, contesta el listado que se le siembra y apunta cada llamada. Con err, toda
// operación falla con él.
type tenantContentStoreSpy struct {
	blobs     map[string][]byte
	summaries []store.TenantContentSummary
	err       error

	calls []string
}

var _ apipublica.TenantContentStore = (*tenantContentStoreSpy)(nil)

// tenantContentPatterns son los cinco patrones de I6–I10.
func tenantContentPatterns() []string {
	return []string{tenantContentPatternPut, tenantContentPatternPost, tenantContentPatternDelete,
		tenantContentPatternList, tenantContentPatternGet}
}

// tenantContentNewSpy devuelve un doble vacío.
func tenantContentNewSpy() *tenantContentStoreSpy {
	return &tenantContentStoreSpy{blobs: map[string][]byte{}}
}

// seed deja un blob guardado sin apuntar la llamada.
func (s *tenantContentStoreSpy) seed(tenantID, ref, blob string) {
	s.blobs[tenantID+"|"+ref] = []byte(blob)
}

func (s *tenantContentStoreSpy) UpsertTenantContent(_ context.Context, tenantID, ref string, blob []byte) error {
	s.calls = append(s.calls, "upsert "+tenantID+" "+ref)
	if s.err != nil {
		return s.err
	}
	s.blobs[tenantID+"|"+ref] = append([]byte(nil), blob...)
	return nil
}

func (s *tenantContentStoreSpy) GetTenantContent(_ context.Context, tenantID, ref string) ([]byte, error) {
	s.calls = append(s.calls, "get "+tenantID+" "+ref)
	if s.err != nil {
		return nil, s.err
	}
	blob, ok := s.blobs[tenantID+"|"+ref]
	if !ok {
		return nil, fmt.Errorf("doble: %w", store.ErrTenantContentNotFound)
	}
	return blob, nil
}

func (s *tenantContentStoreSpy) ListTenantContent(_ context.Context, tenantID string) ([]store.TenantContentSummary, error) {
	s.calls = append(s.calls, "list "+tenantID)
	return s.summaries, s.err
}

func (s *tenantContentStoreSpy) DeleteTenantContent(_ context.Context, tenantID, ref string) error {
	s.calls = append(s.calls, "delete "+tenantID+" "+ref)
	if s.err != nil {
		return s.err
	}
	if _, ok := s.blobs[tenantID+"|"+ref]; !ok {
		return fmt.Errorf("doble: %w", store.ErrTenantContentNotFound)
	}
	delete(s.blobs, tenantID+"|"+ref)
	return nil
}

// tenantContentBench es un banco con las cinco rutas montadas sobre un store y un techo.
type tenantContentBench struct {
	h    *apipublicahelpertest.Harness
	cara *apipublica.Cara
}

// tenantContentSetup monta I6–I10 con el store y el techo dados.
func tenantContentSetup(t *testing.T, cs apipublica.TenantContentStore, maxBytes int64) tenantContentBench {
	t.Helper()
	h := apipublicahelpertest.New(t)
	cara := apipublica.Nueva()
	apipublica.MountTenantContent(cara, h.Common(), apipublica.TenantContentDeps{Content: cs, MaxBytes: maxBytes})
	return tenantContentBench{h: h, cara: cara}
}

// do sirve una petición como tenantID con los dos permisos del área.
func (b tenantContentBench) do(tenantID, method, target, body string) *httptest.ResponseRecorder {
	return b.h.Call(b.cara, b.h.With(tenantID, tenantContentRead, tenantContentWrite), method, target, body)
}

// tenantContentWantCalls exige las llamadas EXACTAS que recibió el store, en orden.
func tenantContentWantCalls(t *testing.T, what string, cs *tenantContentStoreSpy, want ...string) {
	t.Helper()
	if strings.Join(cs.calls, "; ") != strings.Join(want, "; ") {
		t.Errorf("%s: el store recibió %q, quiero %q", what, cs.calls, want)
	}
}

// tenantContentWantOneAudit exige EXACTAMENTE un registro de auditoría con ese resultado y código.
func tenantContentWantOneAudit(t *testing.T, what string, b tenantContentBench, result string, status int) {
	t.Helper()
	records := b.h.Auditor().Records()
	if len(records) != 1 {
		t.Fatalf("%s: quedaron %d registros de auditoría, quiero exactamente 1", what, len(records))
	}
	r := records[0]
	if r.TenantID != tenantA || r.Action != tenantContentWrite || r.Resource != tenantContentResource || r.Result != result || r.Meta["status"] != status {
		t.Errorf("%s: registro %+v, quiero tenant %s, action %s, resource %s, result %s, status %d",
			what, r, tenantA, tenantContentWrite, tenantContentResource, result, status)
	}
}

// tenantContentJSONOfSize devuelve una cadena JSON válida de exactamente n bytes.
func tenantContentJSONOfSize(n int) string {
	return `"` + strings.Repeat("a", n-2) + `"`
}

// tenantContentTooLargeBody es el cuerpo EXACTO del 413 para ese techo.
func tenantContentTooLargeBody(ceiling int) string {
	return fmt.Sprintf(`{"error":"el contenido excede el tamaño máximo de %d bytes","max_bytes":%d}`, ceiling, ceiling)
}

// tenantContentBrokenBody es un cuerpo cuyo Read falla.
type tenantContentBrokenBody struct{}

func (tenantContentBrokenBody) Read([]byte) (int, error) { return 0, errors.New("conexión cortada") }

func TestMountTenantContent_Chain(t *testing.T) {
	cs := tenantContentNewSpy()
	b := tenantContentSetup(t, cs, 0)
	wantPatterns(t, "I6–I10", b.cara, tenantContentPatterns())

	checkChain(t, b.h, b.cara, routeCase{id: "I6", method: http.MethodPut, target: tenantContentTarget, body: tenantContentBlob,
		perm: tenantContentWrite, resource: tenantContentResource, want: http.StatusOK})
	checkChain(t, b.h, b.cara, routeCase{id: "I7", method: http.MethodPost, target: tenantContentTarget, body: tenantContentBlob,
		perm: tenantContentWrite, resource: tenantContentResource, want: http.StatusOK})
	checkChain(t, b.h, b.cara, routeCase{id: "I9", method: http.MethodGet, target: tenantContentBase,
		perm: tenantContentRead, want: http.StatusOK})
	checkChain(t, b.h, b.cara, routeCase{id: "I10", method: http.MethodGet, target: tenantContentTarget,
		perm: tenantContentRead, want: http.StatusOK})
	checkChain(t, b.h, b.cara, routeCase{id: "I8", method: http.MethodDelete, target: tenantContentTarget,
		perm: tenantContentWrite, resource: tenantContentResource, want: http.StatusNoContent})

	// De las cuatro peticiones de cada checkChain solo la que pasa la cadena toca el store.
	tenantContentWantCalls(t, "I6–I10 cadena", cs,
		"upsert "+tenantA+" "+tenantContentRef, "upsert "+tenantA+" "+tenantContentRef,
		"list "+tenantA, "get "+tenantA+" "+tenantContentRef, "delete "+tenantA+" "+tenantContentRef)
}

// TestMountTenantContent_ReadPermissionDoesNotWrite: los dos permisos no se sustituyen: con solo
// la lectura no se escribe ni se borra, y con solo la escritura no se lee.
func TestMountTenantContent_ReadPermissionDoesNotWrite(t *testing.T) {
	cs := tenantContentNewSpy()
	b := tenantContentSetup(t, cs, 0)
	reader, writer := b.h.With(tenantA, tenantContentRead), b.h.With(tenantA, tenantContentWrite)
	for _, tc := range []struct{ name, token, method, target, body string }{
		{"put with read", reader, http.MethodPut, tenantContentTarget, tenantContentBlob},
		{"post with read", reader, http.MethodPost, tenantContentTarget, tenantContentBlob},
		{"delete with read", reader, http.MethodDelete, tenantContentTarget, ""},
		{"list with write", writer, http.MethodGet, tenantContentBase, ""},
		{"get with write", writer, http.MethodGet, tenantContentTarget, ""},
	} {
		rec := b.h.Call(b.cara, tc.token, tc.method, tc.target, tc.body)
		wantCode(t, tc.name, rec, http.StatusForbidden)
		wantErrorBody(t, tc.name, rec, "permiso denegado")
	}
	tenantContentWantCalls(t, "permiso cruzado", cs)
}

// TestMountTenantContent_MountsWithoutStore: las rutas no tienen condición de montaje. Sin store
// existen igual y responden 500 antes de mirar la ref o el cuerpo; la cadena sigue delante.
func TestMountTenantContent_MountsWithoutStore(t *testing.T) {
	h := apipublicahelpertest.New(t)
	cara := apipublica.Nueva()
	apipublica.MountTenantContent(cara, h.Common(), apipublica.TenantContentDeps{})
	wantPatterns(t, "sin store", cara, tenantContentPatterns())
	wantCode(t, "sin store y sin token", h.Call(cara, "", http.MethodGet, tenantContentBase, ""), http.StatusUnauthorized)

	token := h.With(tenantA, tenantContentRead, tenantContentWrite)
	for _, tc := range []struct{ name, method, target, body string }{
		{"put", http.MethodPut, tenantContentTarget, tenantContentBlob},
		{"put with unreadable blob", http.MethodPut, tenantContentTarget, "no-es-json"},
		{"post", http.MethodPost, tenantContentTarget, tenantContentBlob},
		{"delete", http.MethodDelete, tenantContentTarget, ""},
		{"list", http.MethodGet, tenantContentBase, ""},
		{"get", http.MethodGet, tenantContentTarget, ""},
	} {
		rec := h.Call(cara, token, tc.method, tc.target, tc.body)
		wantCode(t, "sin store: "+tc.name, rec, http.StatusInternalServerError)
		wantErrorBody(t, "sin store: "+tc.name, rec, tenantContentMsgNoStore)
	}
}

func TestMountTenantContent_NilMWPanicsAtMount(t *testing.T) {
	v := recuperar(func() {
		apipublica.MountTenantContent(apipublica.Nueva(), apipublica.Common{}, apipublica.TenantContentDeps{Content: tenantContentNewSpy()})
	})
	if v == nil || esPendiente(v) || !strings.Contains(fmt.Sprint(v), "MountTenantContent") {
		t.Errorf("MountTenantContent con MW nil: panic = %v; quiero un panic de cableado que nombre MountTenantContent", v)
	}
}

// TestMountTenantContent_UpsertStoresTheRawBody: PUT y POST son el mismo handler: guardan los
// bytes del cuerpo tal cual, bajo el tenant del token y la ref de la ruta, y responden {"ref"}.
func TestMountTenantContent_UpsertStoresTheRawBody(t *testing.T) {
	const raw = "{ \"prompt\" : \"Elige <b>\",\n  \"z\": 1, \"a\": 2 }"
	other := apipublicahelpertest.TenantB
	for _, method := range []string{http.MethodPut, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			cs := tenantContentNewSpy()
			b := tenantContentSetup(t, cs, 0)
			rec := b.do(tenantA, method, tenantContentTarget+"?tenant_id="+other, raw)
			wantCode(t, method, rec, http.StatusOK)
			wantExactBody(t, method, rec, `{"ref":"`+tenantContentRef+`"}`)
			tenantContentWantCalls(t, method, cs, "upsert "+tenantA+" "+tenantContentRef)
			if got := string(cs.blobs[tenantA+"|"+tenantContentRef]); got != raw {
				t.Errorf("%s: el store guardó\n  %q\nquiero los bytes del cuerpo tal cual\n  %q", method, got, raw)
			}
			tenantContentWantOneAudit(t, method, b, "success", http.StatusOK)
		})
	}
}

// TestMountTenantContent_UpsertAcceptsAnyJSONValue: la forma del blob es del Motor; aquí basta
// con que sea JSON.
func TestMountTenantContent_UpsertAcceptsAnyJSONValue(t *testing.T) {
	for _, body := range []string{`{}`, `[]`, `[1,2]`, `7`, `"texto"`, `null`, `true`} {
		cs := tenantContentNewSpy()
		b := tenantContentSetup(t, cs, 0)
		rec := b.do(tenantA, http.MethodPut, tenantContentTarget, body)
		wantCode(t, body, rec, http.StatusOK)
		if got := string(cs.blobs[tenantA+"|"+tenantContentRef]); got != body {
			t.Errorf("%s: el store guardó %q, quiero el cuerpo tal cual", body, got)
		}
	}
}

// TestMountTenantContent_UpsertRejectsNonJSON: un cuerpo vacío o que no es JSON es 400, no toca
// el store y queda auditado como fallo.
func TestMountTenantContent_UpsertRejectsNonJSON(t *testing.T) {
	for _, body := range []string{"", "no-es-json", `{"a":1`, `{"a":1} {"b":2}`, `{'a':1}`} {
		for _, method := range []string{http.MethodPut, http.MethodPost} {
			cs := tenantContentNewSpy()
			b := tenantContentSetup(t, cs, 0)
			rec := b.do(tenantA, method, tenantContentTarget, body)
			what := method + " " + body
			wantCode(t, what, rec, http.StatusBadRequest)
			wantErrorBody(t, what, rec, tenantContentMsgBadBody)
			tenantContentWantCalls(t, what, cs)
			tenantContentWantOneAudit(t, what, b, "failure", http.StatusBadRequest)
		}
	}
}

// TestMountTenantContent_UpsertUnreadableBodyIs400: un fallo al leer el cuerpo que no es el techo
// es 400 con su propio texto.
func TestMountTenantContent_UpsertUnreadableBodyIs400(t *testing.T) {
	cs := tenantContentNewSpy()
	b := tenantContentSetup(t, cs, 0)
	req := httptest.NewRequest(http.MethodPut, tenantContentTarget, io.NopCloser(tenantContentBrokenBody{}))
	req.Header.Set("Authorization", "Bearer "+b.h.With(tenantA, tenantContentWrite))
	rec := httptest.NewRecorder()
	b.cara.ServeHTTP(rec, req)
	wantCode(t, "cuerpo roto", rec, http.StatusBadRequest)
	wantErrorBody(t, "cuerpo roto", rec, tenantContentMsgUnread)
	tenantContentWantCalls(t, "cuerpo roto", cs)
}

// TestMountTenantContent_UpsertCeiling: el techo es el que se cablea —y 1 MiB si no se cablea—;
// justo en el techo entra, con un byte más es 413 con la cifra, y se aplica antes de mirar si el
// cuerpo es JSON.
func TestMountTenantContent_UpsertCeiling(t *testing.T) {
	if catalogimport.DefaultMaxJSONBytes != 1<<20 {
		t.Fatalf("catalogimport.DefaultMaxJSONBytes = %d; el techo por defecto del contrato es 1 MiB", catalogimport.DefaultMaxJSONBytes)
	}
	cases := []struct {
		name              string
		maxBytes, ceiling int
	}{
		{"configured ceiling", 64, 64},
		{"zero falls to the default", 0, 1 << 20},
		{"negative falls to the default", -5, 1 << 20},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs := tenantContentNewSpy()
			b := tenantContentSetup(t, cs, int64(tc.maxBytes))
			rec := b.do(tenantA, http.MethodPut, tenantContentTarget, tenantContentJSONOfSize(tc.ceiling))
			wantCode(t, "justo en el techo", rec, http.StatusOK)
			if got := len(cs.blobs[tenantA+"|"+tenantContentRef]); got != tc.ceiling {
				t.Errorf("justo en el techo: el store guardó %d bytes, quiero %d", got, tc.ceiling)
			}

			cs = tenantContentNewSpy()
			b = tenantContentSetup(t, cs, int64(tc.maxBytes))
			rec = b.do(tenantA, http.MethodPost, tenantContentTarget, tenantContentJSONOfSize(tc.ceiling+1))
			wantCode(t, "un byte más", rec, http.StatusRequestEntityTooLarge)
			wantExactBody(t, "un byte más", rec, tenantContentTooLargeBody(tc.ceiling))
			tenantContentWantCalls(t, "un byte más", cs)
			tenantContentWantOneAudit(t, "un byte más", b, "failure", http.StatusRequestEntityTooLarge)

			cs = tenantContentNewSpy()
			b = tenantContentSetup(t, cs, int64(tc.maxBytes))
			rec = b.do(tenantA, http.MethodPut, tenantContentTarget, strings.Repeat("x", tc.ceiling+1))
			wantCode(t, "se pasa y no es JSON", rec, http.StatusRequestEntityTooLarge)
			wantExactBody(t, "se pasa y no es JSON", rec, tenantContentTooLargeBody(tc.ceiling))
		})
	}
}

// TestMountTenantContent_RefArrivesUnescaped: la ref llega al store como la entrega el mux, sin
// el escapado de URL y sin recortar.
func TestMountTenantContent_RefArrivesUnescaped(t *testing.T) {
	cs := tenantContentNewSpy()
	b := tenantContentSetup(t, cs, 0)
	rec := b.do(tenantA, http.MethodPut, tenantContentBase+"/men%C3%BA%20Del%20D%C3%ADa", `{}`)
	wantCode(t, "ref escapada", rec, http.StatusOK)
	wantExactBody(t, "ref escapada", rec, `{"ref":"menú Del Día"}`)
	tenantContentWantCalls(t, "ref escapada", cs, "upsert "+tenantA+" menú Del Día")
}

// TestMountTenantContent_List: el listado es un arreglo en el orden del store, con los instantes
// en RFC3339 UTC y sin la clave del que es cero; vacío es `[]`, no `null`.
func TestMountTenantContent_List(t *testing.T) {
	cs := tenantContentNewSpy()
	b := tenantContentSetup(t, cs, 0)
	rec := b.do(tenantA, http.MethodGet, tenantContentBase+"?tenant_id="+apipublicahelpertest.TenantB, "")
	wantCode(t, "vacío", rec, http.StatusOK)
	wantExactBody(t, "vacío", rec, `[]`)
	tenantContentWantCalls(t, "vacío", cs, "list "+tenantA)

	zone := time.FixedZone("-03", -3*3600)
	cs = tenantContentNewSpy()
	cs.summaries = []store.TenantContentSummary{
		{Ref: "zeta", CreatedAt: time.Date(2026, 10, 1, 9, 0, 0, 500, zone), UpdatedAt: time.Date(2026, 10, 2, 9, 30, 15, 0, zone)},
		{Ref: "alfa", CreatedAt: time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)},
		{Ref: "sin-fechas"},
	}
	b = tenantContentSetup(t, cs, 0)
	rec = b.do(tenantA, http.MethodGet, tenantContentBase, "")
	wantCode(t, "tres refs", rec, http.StatusOK)
	wantExactBody(t, "tres refs", rec, `[`+
		`{"ref":"zeta","created_at":"2026-10-01T12:00:00Z","updated_at":"2026-10-02T12:30:15Z"},`+
		`{"ref":"alfa","created_at":"2026-09-30T00:00:00Z"},`+
		`{"ref":"sin-fechas"}]`)
	if n := len(b.h.Auditor().Records()); n != 0 {
		t.Errorf("el listado dejó %d registros de auditoría; es una lectura", n)
	}
}

// TestMountTenantContent_Get: la lectura devuelve el MISMO valor JSON, compactado y con `<`, `>`
// y `&` escapados (no los bytes que entraron); una ref que el tenant no tiene es 404, y un blob
// almacenado que no es JSON, 500.
func TestMountTenantContent_Get(t *testing.T) {
	cs := tenantContentNewSpy()
	cs.seed(tenantA, tenantContentRef, "{ \"prompt\" : \"<b>Tú & yo</b>\" ,\n \"n\": 1.0 }")
	b := tenantContentSetup(t, cs, 0)
	rec := b.do(tenantA, http.MethodGet, tenantContentTarget, "")
	wantCode(t, "I10", rec, http.StatusOK)
	wantExactBody(t, "I10", rec, `{"prompt":"<b>Tú & yo</b>","n":1.0}`)
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("I10: Content-Type %q, quiero application/json", got)
	}
	tenantContentWantCalls(t, "I10", cs, "get "+tenantA+" "+tenantContentRef)

	rec = b.do(tenantA, http.MethodGet, tenantContentBase+"/no-existe", "")
	wantCode(t, "ref ausente", rec, http.StatusNotFound)
	wantErrorBody(t, "ref ausente", rec, tenantContentMsgNotFound)

	// Un blob almacenado que no es JSON no se puede emitir: 500 en texto plano.
	cs.seed(tenantA, tenantContentRef, "no-es-json")
	rec = b.do(tenantA, http.MethodGet, tenantContentTarget, "")
	wantCode(t, "blob roto", rec, http.StatusInternalServerError)
	wantExactBody(t, "blob roto", rec, "codificando respuesta\n")
}

// TestMountTenantContent_Delete: borrar es 204 sin cuerpo; borrar lo que no existe, 404.
func TestMountTenantContent_Delete(t *testing.T) {
	cs := tenantContentNewSpy()
	cs.seed(tenantA, tenantContentRef, tenantContentBlob)
	b := tenantContentSetup(t, cs, 0)
	rec := b.do(tenantA, http.MethodDelete, tenantContentTarget, "")
	wantCode(t, "I8", rec, http.StatusNoContent)
	if rec.Body.Len() != 0 {
		t.Errorf("I8: el 204 lleva cuerpo (%q)", rec.Body.String())
	}
	tenantContentWantCalls(t, "I8", cs, "delete "+tenantA+" "+tenantContentRef)
	tenantContentWantOneAudit(t, "I8", b, "success", http.StatusNoContent)

	b = tenantContentSetup(t, cs, 0)
	rec = b.do(tenantA, http.MethodDelete, tenantContentTarget, "")
	wantCode(t, "ya borrado", rec, http.StatusNotFound)
	wantErrorBody(t, "ya borrado", rec, tenantContentMsgNotFound)
	tenantContentWantOneAudit(t, "ya borrado", b, "failure", http.StatusNotFound)
}

// TestMountTenantContent_IsolatedByTokenTenant: la ref de un tenant no existe para otro —ni para
// leerla ni para borrarla— y cada uno escribe bajo su propio tenant.
func TestMountTenantContent_IsolatedByTokenTenant(t *testing.T) {
	other := apipublicahelpertest.TenantB
	cs := tenantContentNewSpy()
	b := tenantContentSetup(t, cs, 0)
	wantCode(t, "A escribe", b.do(tenantA, http.MethodPut, tenantContentTarget, `{"de":"A"}`), http.StatusOK)

	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		rec := b.do(other, method, tenantContentTarget+"?tenant_id="+tenantA, "")
		wantCode(t, "B "+method+" la ref de A", rec, http.StatusNotFound)
		wantErrorBody(t, "B "+method+" la ref de A", rec, tenantContentMsgNotFound)
	}

	wantCode(t, "B escribe la misma ref", b.do(other, http.MethodPut, tenantContentTarget, `{"de":"B"}`), http.StatusOK)
	rec := b.do(tenantA, http.MethodGet, tenantContentTarget, "")
	wantCode(t, "A lee", rec, http.StatusOK)
	wantExactBody(t, "A lee", rec, `{"de":"A"}`)
	tenantContentWantCalls(t, "aislamiento", cs,
		"upsert "+tenantA+" "+tenantContentRef, "get "+other+" "+tenantContentRef, "delete "+other+" "+tenantContentRef,
		"upsert "+other+" "+tenantContentRef, "get "+tenantA+" "+tenantContentRef)
}

// TestMountTenantContent_StoreFailureIs500: un fallo del store es 500 con el texto de su
// operación, sin repetir el error.
func TestMountTenantContent_StoreFailureIs500(t *testing.T) {
	cases := []struct{ name, method, target, body, msg string }{
		{"put", http.MethodPut, tenantContentTarget, tenantContentBlob, tenantContentMsgUpsert},
		{"post", http.MethodPost, tenantContentTarget, tenantContentBlob, tenantContentMsgUpsert},
		{"list", http.MethodGet, tenantContentBase, "", tenantContentMsgList},
		{"get", http.MethodGet, tenantContentTarget, "", tenantContentMsgGet},
		{"delete", http.MethodDelete, tenantContentTarget, "", tenantContentMsgDelete},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs := tenantContentNewSpy()
			cs.err = errors.New("pq: DSN SECRETO rechazado")
			b := tenantContentSetup(t, cs, 0)
			rec := b.do(tenantA, tc.method, tc.target, tc.body)
			wantCode(t, tc.name, rec, http.StatusInternalServerError)
			wantErrorBody(t, tc.name, rec, tc.msg)
			if strings.Contains(rec.Body.String(), "SECRETO") {
				t.Errorf("%s: el cuerpo repite el error del store (%s)", tc.name, rec.Body.String())
			}
			if len(cs.calls) != 1 {
				t.Errorf("%s: el store recibió %d llamadas, quiero 1", tc.name, len(cs.calls))
			}
		})
	}
}
