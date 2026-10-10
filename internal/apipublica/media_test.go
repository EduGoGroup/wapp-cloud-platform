package apipublica_test

// media_test.go — cubre el contrato de media.go (MediaPresignUploader, MediaDeps, MountMedia): el
// montaje sin condición, la cadena W, el orden de los desenlaces, la respuesta de I5 y la forma
// de la key (namespace por tenant, uuid y nombre saneado).

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica/apipublicahelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/objectstore"
)

const (
	mediaPattern  = "POST /api/v1/media/upload-url"
	mediaTarget   = "/api/v1/media/upload-url"
	mediaPerm     = "media.upload"
	mediaResource = "media"

	mediaSignedURL = "https://r2.example/put?firma=abc"
	mediaValidBody = `{"filename":"lista.pdf","mime":"application/pdf"}`

	mediaMsgNoStore  = "almacén de objetos no configurado"
	mediaMsgBadJSON  = "cuerpo JSON inválido"
	mediaMsgRequired = "filename y mime son requeridos"
	mediaMsgPresign  = "no se pudo presignar la subida"
)

// mediaKeyShape es la forma de la key: prefijo compilado, tenant, uuid canónico y nombre saneado.
var mediaKeyShape = regexp.MustCompile(`^wapp/media/([^/]+)/([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})-([^/]+)$`)

// El puerto de I5 lo cumple el cliente de presign de la plataforma (es un subconjunto suyo).
var _ apipublica.MediaPresignUploader = objectstore.PresignClient(nil)

// mediaUploaderSpy es MediaPresignUploader: contesta lo que se le siembra (o err) y apunta cada
// llamada con su key y si su contexto traía plazo.
type mediaUploaderSpy struct {
	url       string
	expiresAt time.Time
	err       error

	calls       int
	keys        []string
	hasDeadline bool
}

var _ apipublica.MediaPresignUploader = (*mediaUploaderSpy)(nil)

func (s *mediaUploaderSpy) GenerateUploadURL(ctx context.Context, key string) (string, time.Time, error) {
	s.calls++
	s.keys = append(s.keys, key)
	_, s.hasDeadline = ctx.Deadline()
	return s.url, s.expiresAt, s.err
}

// mediaNewSpy devuelve un doble que firma siempre la misma URL, con vencimiento fijo.
func mediaNewSpy() *mediaUploaderSpy {
	return &mediaUploaderSpy{url: mediaSignedURL, expiresAt: time.Date(2026, 10, 10, 12, 30, 45, 0, time.UTC)}
}

// mediaCara monta I5 con k y el puerto dado.
func mediaCara(k apipublica.Common, up apipublica.MediaPresignUploader) *apipublica.Cara {
	c := apipublica.Nueva()
	apipublica.MountMedia(c, k, apipublica.MediaDeps{Uploader: up})
	return c
}

// mediaDo sirve UNA petición a I5 con un token del tenant dado que trae SOLO el permiso de subida,
// y devuelve también el banco para mirar la auditoría.
func mediaDo(t *testing.T, up apipublica.MediaPresignUploader, tenantID, target, body string) (*apipublicahelpertest.Harness, *httptest.ResponseRecorder) {
	t.Helper()
	h := apipublicahelpertest.New(t)
	cara := mediaCara(h.Common(), up)
	return h, h.Call(cara, h.With(tenantID, mediaPerm), http.MethodPost, target, body)
}

// mediaWantCalls exige cuántas veces se pidió una firma.
func mediaWantCalls(t *testing.T, what string, up *mediaUploaderSpy, want int) {
	t.Helper()
	if up.calls != want {
		t.Errorf("%s: el puerto de presign recibió %d llamadas, quiero %d", what, up.calls, want)
	}
}

// mediaWantOneAudit exige EXACTAMENTE un registro de auditoría de I5 con ese resultado y código.
func mediaWantOneAudit(t *testing.T, what string, h *apipublicahelpertest.Harness, result string, status int) {
	t.Helper()
	records := h.Auditor().Records()
	if len(records) != 1 {
		t.Fatalf("%s: I5 dejó %d registros de auditoría, quiero exactamente 1", what, len(records))
	}
	r := records[0]
	if r.TenantID != tenantA || r.Action != mediaPerm || r.Resource != mediaResource || r.Result != result || r.Meta["status"] != status {
		t.Errorf("%s: registro %+v, quiero tenant %s, action %s, resource %s, result %s, status %d",
			what, r, tenantA, mediaPerm, mediaResource, result, status)
	}
}

// mediaResponse es el cuerpo del 200 de I5.
type mediaResponse struct {
	URL       string `json:"url"`
	Key       string `json:"key"`
	ExpiresAt string `json:"expires_at"`
}

// mediaSanitizedName firma una subida con ese filename y devuelve el nombre saneado de su key.
func mediaSanitizedName(t *testing.T, filename string) string {
	t.Helper()
	up := mediaNewSpy()
	body := fmt.Sprintf(`{"filename":%q,"mime":"application/pdf"}`, filename)
	_, rec := mediaDo(t, up, tenantA, mediaTarget, body)
	wantCode(t, filename, rec, http.StatusOK)
	var got mediaResponse
	wantJSON(t, filename, rec, &got)
	m := mediaKeyShape.FindStringSubmatch(got.Key)
	if m == nil {
		t.Fatalf("%q: la key %q no tiene la forma wapp/media/<tenant>/<uuid>-<nombre>", filename, got.Key)
	}
	return m[3]
}

func TestMountMedia_Chain(t *testing.T) {
	h := apipublicahelpertest.New(t)
	up := mediaNewSpy()
	cara := mediaCara(h.Common(), up)
	wantPatterns(t, "I5", cara, []string{mediaPattern})
	checkChain(t, h, cara, routeCase{id: "I5", method: http.MethodPost, target: mediaTarget, body: mediaValidBody,
		perm: mediaPerm, resource: mediaResource, want: http.StatusOK})
	// De las cuatro peticiones de checkChain solo la que pasa la cadena firma algo.
	mediaWantCalls(t, "I5 cadena", up, 1)
}

// TestMountMedia_MountsWithoutUploader: la ruta no tiene condición de montaje. Sin almacén existe
// igual y responde 500 —también a un cuerpo ilegible: el almacén se mira ANTES de leer—, y la
// cadena sigue delante (401 sin token).
func TestMountMedia_MountsWithoutUploader(t *testing.T) {
	h := apipublicahelpertest.New(t)
	cara := apipublica.Nueva()
	apipublica.MountMedia(cara, h.Common(), apipublica.MediaDeps{})
	wantPatterns(t, "sin almacén", cara, []string{mediaPattern})

	wantCode(t, "sin almacén y sin token", h.Call(cara, "", http.MethodPost, mediaTarget, mediaValidBody), http.StatusUnauthorized)

	for _, body := range []string{mediaValidBody, "{no es json", `{"filename":"","mime":""}`} {
		h := apipublicahelpertest.New(t)
		cara := apipublica.Nueva()
		apipublica.MountMedia(cara, h.Common(), apipublica.MediaDeps{})
		rec := h.Call(cara, h.With(tenantA, mediaPerm), http.MethodPost, mediaTarget, body)
		wantCode(t, "sin almacén: "+body, rec, http.StatusInternalServerError)
		wantErrorBody(t, "sin almacén: "+body, rec, mediaMsgNoStore)
		mediaWantOneAudit(t, "sin almacén: "+body, h, "failure", http.StatusInternalServerError)
	}
}

func TestMountMedia_NilMWPanicsAtMount(t *testing.T) {
	v := recuperar(func() {
		apipublica.MountMedia(apipublica.Nueva(), apipublica.Common{}, apipublica.MediaDeps{Uploader: mediaNewSpy()})
	})
	if v == nil || esPendiente(v) || !strings.Contains(fmt.Sprint(v), "MountMedia") {
		t.Errorf("MountMedia con MW nil: panic = %v; quiero un panic de cableado que nombre MountMedia", v)
	}
}

// TestMountMedia_SignedUploadResponse: el 200 lleva la URL del puerto tal cual, la MISMA key que
// se firmó y el vencimiento en RFC3339 UTC; el puerto se llama una vez y sin plazo propio.
func TestMountMedia_SignedUploadResponse(t *testing.T) {
	up := mediaNewSpy()
	// Un vencimiento en otra zona y con fracción: viaja en UTC y al segundo.
	up.expiresAt = time.Date(2026, 10, 10, 9, 30, 45, 987_000_000, time.FixedZone("-03", -3*3600))
	h, rec := mediaDo(t, up, tenantA, mediaTarget, mediaValidBody)
	wantCode(t, "I5", rec, http.StatusOK)
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("I5: Content-Type %q, quiero application/json", got)
	}
	mediaWantCalls(t, "I5", up, 1)
	if up.hasDeadline {
		t.Error("I5: el contexto del puerto trae plazo; la puerta no pone uno propio")
	}

	var got mediaResponse
	wantJSON(t, "I5", rec, &got)
	if got.URL != mediaSignedURL {
		t.Errorf("I5: url %q, quiero la del puerto tal cual (%q)", got.URL, mediaSignedURL)
	}
	if got.ExpiresAt != "2026-10-10T12:30:45Z" {
		t.Errorf("I5: expires_at %q, quiero 2026-10-10T12:30:45Z (RFC3339 UTC, al segundo)", got.ExpiresAt)
	}
	if got.Key != up.keys[0] {
		t.Errorf("I5: la respuesta lleva la key %q y se firmó %q; tienen que ser la misma", got.Key, up.keys[0])
	}
	wantExactBody(t, "I5 (orden de las claves)", rec,
		`{"url":"`+mediaSignedURL+`","key":"`+got.Key+`","expires_at":"2026-10-10T12:30:45Z"}`)
	mediaWantOneAudit(t, "I5", h, "success", http.StatusOK)
}

// TestMountMedia_KeyIsNamespacedByTokenTenant: la key es wapp/media/<tenant>/<uuid>-<nombre>, con
// el tenant del TOKEN (uno en el cuerpo o en la query no cuenta), y dos peticiones idénticas dan
// dos keys distintas.
func TestMountMedia_KeyIsNamespacedByTokenTenant(t *testing.T) {
	other := apipublicahelpertest.TenantB
	body := `{"filename":"lista.pdf","mime":"application/pdf","tenant_id":"` + other + `"}`

	up := mediaNewSpy()
	_, rec := mediaDo(t, up, tenantA, mediaTarget+"?tenant_id="+other, body)
	wantCode(t, "tenant A", rec, http.StatusOK)
	mediaWantCalls(t, "tenant A", up, 1)
	m := mediaKeyShape.FindStringSubmatch(up.keys[0])
	if m == nil {
		t.Fatalf("tenant A: la key %q no tiene la forma wapp/media/<tenant>/<uuid>-<nombre>", up.keys[0])
	}
	if m[1] != tenantA || m[3] != "lista.pdf" {
		t.Errorf("tenant A: key %q, quiero el tenant del token (%s) y el nombre lista.pdf", up.keys[0], tenantA)
	}

	_, rec = mediaDo(t, up, tenantA, mediaTarget, mediaValidBody)
	wantCode(t, "tenant A, otra vez", rec, http.StatusOK)
	if up.keys[0] == up.keys[1] {
		t.Errorf("dos subidas del mismo archivo dieron la misma key %q; el uuid la hace única", up.keys[0])
	}

	upB := mediaNewSpy()
	_, rec = mediaDo(t, upB, other, mediaTarget, mediaValidBody)
	wantCode(t, "tenant B", rec, http.StatusOK)
	if !strings.HasPrefix(upB.keys[0], "wapp/media/"+other+"/") {
		t.Errorf("tenant B: key %q, quiero el prefijo wapp/media/%s/", upB.keys[0], other)
	}
}

// TestMountMedia_FilenameIsSanitized: el nombre de la key no puede traer separadores ni
// traversal.
func TestMountMedia_FilenameIsSanitized(t *testing.T) {
	cases := []struct {
		name     string
		filename string
		want     string
	}{
		{"safe name is kept", "Lista-2026_v1.pdf", "Lista-2026_v1.pdf"},
		{"range edges are safe", "azAZ09.pdf", "azAZ09.pdf"},
		{"space becomes underscore", "lista precios.pdf", "lista_precios.pdf"},
		{"edges are trimmed", "  lista.pdf  ", "lista.pdf"},
		{"only the last path segment", "../../etc/passwd", "passwd"},
		{"backslash is a separator", `C:\docs\lista.pdf`, "lista.pdf"},
		{"trailing separator is dropped", "dir/", "dir"},
		{"one underscore per character, not per byte", "menú.pdf", "men_.pdf"},
		{"symbols become underscores", "a+b@c#d.pdf", "a_b_c_d.pdf"},
		{"dot is degenerate", ".", "file"},
		{"dot dot is degenerate", "..", "file"},
		{"segment of spaces is degenerate", "dir/ /", "file"},
		{"lone separator becomes underscore", "/", "_"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mediaSanitizedName(t, tc.filename); got != tc.want {
				t.Errorf("filename %q: nombre saneado %q, quiero %q", tc.filename, got, tc.want)
			}
		})
	}
}

// TestMountMedia_BadRequestNeverSigns: un cuerpo ilegible o sin sus dos campos es 400 con su
// texto, no firma nada y queda auditado como fallo.
func TestMountMedia_BadRequestNeverSigns(t *testing.T) {
	cases := []struct {
		name string
		body string
		msg  string
	}{
		{"empty body", "", mediaMsgBadJSON},
		{"broken json", "{no es json", mediaMsgBadJSON},
		{"array", `[]`, mediaMsgBadJSON},
		{"string", `"lista.pdf"`, mediaMsgBadJSON},
		{"filename of another type", `{"filename":7,"mime":"application/pdf"}`, mediaMsgBadJSON},
		{"mime of another type", `{"filename":"a.pdf","mime":["x"]}`, mediaMsgBadJSON},
		{"null", `null`, mediaMsgRequired},
		{"empty object", `{}`, mediaMsgRequired},
		{"both empty", `{"filename":"","mime":""}`, mediaMsgRequired},
		{"missing mime", `{"filename":"a.pdf"}`, mediaMsgRequired},
		{"missing filename", `{"mime":"application/pdf"}`, mediaMsgRequired},
		{"blank filename", `{"filename":"   ","mime":"application/pdf"}`, mediaMsgRequired},
		{"blank mime", `{"filename":"a.pdf","mime":" \t"}`, mediaMsgRequired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			up := mediaNewSpy()
			h, rec := mediaDo(t, up, tenantA, mediaTarget, tc.body)
			wantCode(t, tc.name, rec, http.StatusBadRequest)
			wantErrorBody(t, tc.name, rec, tc.msg)
			mediaWantCalls(t, tc.name, up, 0)
			mediaWantOneAudit(t, tc.name, h, "failure", http.StatusBadRequest)
		})
	}
}

// TestMountMedia_OnlyTheFirstJSONValueIsRead: lo que viene detrás del primer valor no se mira, y
// un campo que el contrato no publica se ignora.
func TestMountMedia_OnlyTheFirstJSONValueIsRead(t *testing.T) {
	for _, body := range []string{
		mediaValidBody + ` basura`,
		mediaValidBody + `{"filename":"otro.pdf"}`,
		`{"filename":"lista.pdf","mime":"application/pdf","size":10,"key":"wapp/media/x/y"}`,
	} {
		up := mediaNewSpy()
		_, rec := mediaDo(t, up, tenantA, mediaTarget, body)
		wantCode(t, body, rec, http.StatusOK)
		mediaWantCalls(t, body, up, 1)
		if !strings.HasSuffix(up.keys[0], "-lista.pdf") {
			t.Errorf("%s: se firmó la key %q; quiero la del primer valor (…-lista.pdf)", body, up.keys[0])
		}
	}
}

// TestMountMedia_PresignFailureIs502: si la firma falla es 502 con el texto fijo, sin repetir el
// error del puerto.
func TestMountMedia_PresignFailureIs502(t *testing.T) {
	up := mediaNewSpy()
	up.err = errors.New("r2: credencial SECRETA rechazada")
	h, rec := mediaDo(t, up, tenantA, mediaTarget, mediaValidBody)
	wantCode(t, "firma caída", rec, http.StatusBadGateway)
	wantErrorBody(t, "firma caída", rec, mediaMsgPresign)
	if strings.Contains(rec.Body.String(), "SECRETA") {
		t.Errorf("firma caída: el cuerpo repite el error del puerto (%s)", rec.Body.String())
	}
	mediaWantCalls(t, "firma caída", up, 1)
	mediaWantOneAudit(t, "firma caída", h, "failure", http.StatusBadGateway)
}
