//go:build integracion

package procesos

import (
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Los tests propios del doble del CRM (TestArnes_CRM): no arrancan servidor ni tocan Postgres. Lo que
// fijan es que el doble juzga como el contrato le pide a un puente, para que un proceso que se apoye
// en él no pueda quedar verde por un doble que acepta cualquier cosa.

const (
	// Vector de respuesta conocida de la firma, calculado FUERA de Go:
	//   printf 'v1:1786139112:{"a":1}' | openssl dgst -sha256 -hmac '<crmFakeVectorKey>'
	crmFakeVectorKey  = "secreto-de-prueba-del-arnes-crm"
	crmFakeVectorTime = int64(1786139112)
	crmFakeVectorBody = `{"a":1}`
	crmFakeVectorSig  = "e7cb2746586f0edd39a883ee3c7cc21cf8802fa680b4cbff15640a525ade1f95"

	// Vector de la huella, el de internal/integrations/crud_integration_test.go (calculado allí con
	// `printf '%s' … | shasum -a 256`).
	crmFakeVectorPrintIn  = "secreto-de-firma-del-puente-jjx-2026"
	crmFakeVectorPrintOut = "e5c47775"
)

// crmFakeSignatureShape es la forma de la cabecera de firma: `v1=` y 64 hexadecimales en minúscula.
var crmFakeSignatureShape = regexp.MustCompile(`^v1=[0-9a-f]{64}$`)

// TestArnes_CRM prueba el doble del CRM contra el contrato publicado.
func TestArnes_CRM(t *testing.T) {
	t.Parallel()
	t.Run("signature_known_answer", crmFakeTestKnownAnswer)
	t.Run("verify_is_strict", crmFakeTestVerifyStrict)
	t.Run("window_edges", crmFakeTestWindow)
	t.Run("push_schema", crmFakeTestPushSchema)
	t.Run("receives_and_answers", crmFakeTestReceives)
	t.Run("signs_callback", crmFakeTestSignsCallback)
	t.Run("status_schema", crmFakeTestStatusSchema)
}

// crmFakeExample lee un ejemplo publicado del contrato. Falla (t.Fatalf) si no está.
func crmFakeExample(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := fs.ReadFile(os.DirFS(crmFakeContractDir), "examples/"+name)
	if err != nil {
		t.Fatalf("leer el ejemplo publicado %s: %v", name, err)
	}
	return raw
}

// crmFakeMutate devuelve el documento raw con la mutación aplicada, vuelto a serializar.
func crmFakeMutate(t *testing.T, raw []byte, mutate func(map[string]any)) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("el ejemplo no es un objeto JSON: %v", err)
	}
	mutate(doc)
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("serializar la mutación: %v", err)
	}
	return out
}

func crmFakeTestKnownAnswer(t *testing.T) {
	if got := crmFakeSign(crmFakeVectorKey, crmFakeVectorTime, []byte(crmFakeVectorBody)); got != crmFakeVectorSig {
		t.Errorf("crmFakeSign = %s, quería %s (HMAC-SHA256 de «v1:<ts>:<cuerpo>» calculado con openssl)", got, crmFakeVectorSig)
	}
	if got := crmFakeFingerprint(crmFakeVectorPrintIn); got != crmFakeVectorPrintOut {
		t.Errorf("crmFakeFingerprint = %s, quería %s", got, crmFakeVectorPrintOut)
	}
	if a, b := crmFakeRandomSecret(t), crmFakeRandomSecret(t); a == b || len(a) != 48 {
		t.Errorf("crmFakeRandomSecret dio %q y %q: quería dos secretos distintos de 48 caracteres", a, b)
	}
}

func crmFakeTestVerifyStrict(t *testing.T) {
	body := []byte(crmFakeVectorBody)
	cases := []struct {
		name   string
		key    string
		ts     int64
		body   []byte
		header string
		want   bool
	}{
		{"buena", crmFakeVectorKey, crmFakeVectorTime, body, "v1=" + crmFakeVectorSig, true},
		{"otro secreto", crmFakeVectorKey + "x", crmFakeVectorTime, body, "v1=" + crmFakeVectorSig, false},
		{"otro timestamp", crmFakeVectorKey, crmFakeVectorTime + 1, body, "v1=" + crmFakeVectorSig, false},
		{"otro cuerpo", crmFakeVectorKey, crmFakeVectorTime, []byte(`{"a": 1}`), "v1=" + crmFakeVectorSig, false},
		{"sin prefijo", crmFakeVectorKey, crmFakeVectorTime, body, crmFakeVectorSig, false},
		{"prefijo doble", crmFakeVectorKey, crmFakeVectorTime, body, "v1==" + crmFakeVectorSig, false},
		{"prefijo en mayúscula", crmFakeVectorKey, crmFakeVectorTime, body, "V1=" + crmFakeVectorSig, false},
		{"hexadecimal en mayúsculas", crmFakeVectorKey, crmFakeVectorTime, body, "v1=" + strings.ToUpper(crmFakeVectorSig), false},
		{"solo el prefijo", crmFakeVectorKey, crmFakeVectorTime, body, "v1=", false},
		{"vacía", crmFakeVectorKey, crmFakeVectorTime, body, "", false},
		{"truncada", crmFakeVectorKey, crmFakeVectorTime, body, "v1=" + crmFakeVectorSig[:63], false},
	}
	for _, c := range cases {
		if got := crmFakeVerify(c.key, c.ts, c.body, c.header); got != c.want {
			t.Errorf("%s: crmFakeVerify = %v, quería %v", c.name, got, c.want)
		}
	}
}

func crmFakeTestWindow(t *testing.T) {
	c := crmFakeNew(t, crmFakeVectorKey)
	now := time.Unix(crmFakeVectorTime, 0)
	body := crmFakeExample(t, "intake.push.json")
	for _, tc := range []struct {
		offset time.Duration
		header string
		want   bool
	}{
		{0, "", true},
		{300 * time.Second, "", true},
		{-300 * time.Second, "", true},
		{301 * time.Second, "", false},
		{-301 * time.Second, "", false},
		{0, "no-es-un-numero", false},
		{0, "١٧٨٦١٣٩١١٢", false},
		{0, " " + strconv.FormatInt(crmFakeVectorTime, 10), false},
	} {
		ts := now.Add(-tc.offset).Unix()
		header := http.Header{}
		value := strconv.FormatInt(ts, 10)
		if tc.header != "" {
			value = tc.header
		}
		header.Set(crmFakeHeaderTimestamp, value)
		header.Set(crmFakeHeaderSignature, crmFakeSignaturePrefix+crmFakeSign(crmFakeVectorKey, ts, body))
		d := c.judge(crmFakeVectorKey, header, body, now)
		// Con un timestamp numérico la firma es buena esté o no en ventana (son dos preguntas); con uno
		// ilegible no hay ni ventana ni firma que comprobar.
		if d.InWindow != tc.want || d.SignatureOK != (tc.header == "") {
			t.Errorf("timestamp %q (desfase %s): InWindow=%v SignatureOK=%v, quería ventana %v y firma %v",
				value, tc.offset, d.InWindow, d.SignatureOK, tc.want, tc.header == "")
		}
	}
}

func crmFakeTestPushSchema(t *testing.T) {
	c := crmFakeNew(t, crmFakeVectorKey)
	example := crmFakeExample(t, "intake.push.json")
	if _, err := crmFakeValidate(c.pushSchema, example); err != nil {
		t.Fatalf("el ejemplo publicado de intake.push no valida: %v", err)
	}
	mutations := map[string]func(map[string]any){
		"lifecycle_status closed":     func(d map[string]any) { d["lifecycle_status"] = "closed" },
		"currency":                    func(d map[string]any) { d["currency"] = "CLP" },
		"contact_phone reservado":     func(d map[string]any) { d["contact_phone"] = "+56912345678" },
		"revision_no 0":               func(d map[string]any) { d["revision_no"] = 0 },
		"revision_no con decimales":   func(d map[string]any) { d["revision_no"] = 1.5 },
		"total como cadena":           func(d map[string]any) { d["total"] = "24" },
		"sin customer_note":           func(d map[string]any) { delete(d, "customer_note") },
		"sin variables":               func(d map[string]any) { delete(d, "variables") },
		"sin buyer_data":              func(d map[string]any) { delete(d, "buyer_data") },
		"items null":                  func(d map[string]any) { d["items"] = nil },
		"intake_id que no es un uuid": func(d map[string]any) { d["intake_id"] = "a@@b" },
		"timestamp que no es fecha":   func(d map[string]any) { d["timestamp"] = "ayer" },
		"contract_version numérica":   func(d map[string]any) { d["contract_version"] = 1 },
		"línea sin customization": func(d map[string]any) {
			d["items"] = []any{map[string]any{"sku": "A", "label": "a", "qty": 1, "unit_price": 1}}
		},
		"línea con qty 0": func(d map[string]any) {
			d["items"] = []any{map[string]any{"sku": "A", "label": "a", "customization": "", "qty": 0, "unit_price": 1}}
		},
	}
	for name, mutate := range mutations {
		if _, err := crmFakeValidate(c.pushSchema, crmFakeMutate(t, example, mutate)); err == nil {
			t.Errorf("%s: la mutación debía romper la validación contra el schema publicado y validó", name)
		}
	}
	for name, body := range map[string]string{"no es JSON": "{", "no es un objeto": `[1]`, "vacío": ""} {
		if doc, err := crmFakeValidate(c.pushSchema, []byte(body)); err == nil || doc != nil && name != "no es un objeto" {
			t.Errorf("%s: crmFakeValidate dio (%v, %v), quería un error", name, doc, err)
		}
	}
}

// crmFakeDeliver manda body al doble como lo mandaría el worker del servidor, firmado con key.
func crmFakeDeliver(t *testing.T, c *crmFake, key, id string, body []byte) int {
	t.Helper()
	ts := time.Now().Unix()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, c.URL(), bytes.NewReader(body))
	if err != nil {
		t.Fatalf("construir la entrega: %v", err)
	}
	req.Header.Set(crmFakeHeaderSignature, crmFakeSignaturePrefix+crmFakeSign(key, ts, body))
	req.Header.Set(crmFakeHeaderTimestamp, strconv.FormatInt(ts, 10))
	req.Header.Set(crmFakeHeaderDelivery, id)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("entregar al doble: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("cerrar la respuesta del doble: %v", err)
	}
	return resp.StatusCode
}

func crmFakeTestReceives(t *testing.T) {
	c := crmFakeNew(t, crmFakeVectorKey)
	example := crmFakeExample(t, "intake.push.json")
	if got := len(c.Deliveries()); got != 0 {
		t.Fatalf("el doble nace con %d entregas", got)
	}

	if code := crmFakeDeliver(t, c, crmFakeVectorKey, "41", example); code != http.StatusOK {
		t.Errorf("el doble recién creado contestó %d, quería 200", code)
	}
	c.Respond(http.StatusInternalServerError)
	if code := crmFakeDeliver(t, c, "otro-secreto-distinto-del-doble", "42", example); code != http.StatusInternalServerError {
		t.Errorf("tras Respond(500) el doble contestó %d", code)
	}
	c.RespondWith(func(d crmFakeDelivery) int {
		if d.SignatureOK && d.SchemaErr == nil && d.Number("revision_no") == 1 {
			return http.StatusAccepted
		}
		return http.StatusTeapot
	})
	if code := crmFakeDeliver(t, c, crmFakeVectorKey, "43", example); code != http.StatusAccepted {
		t.Errorf("la regla por entrega contestó %d, quería 202", code)
	}
	if code := crmFakeDeliver(t, c, crmFakeVectorKey, "44", []byte(`{"verb":"intake.push"}`)); code != http.StatusTeapot {
		t.Errorf("la regla por entrega contestó %d a un documento inválido, quería 418", code)
	}
	c.SetSecret("otro-secreto-distinto-del-doble")
	crmFakeDeliver(t, c, crmFakeVectorKey, "45", example)

	got := c.Wait(t, edgeTopeFila, "las cinco", 5, func(crmFakeDelivery) bool { return true })
	crmFakeCheckReceived(t, got)
	if d := got[0]; !bytes.Equal(d.Body, example) || d.Text("verb") != "intake.push" || d.Number("revision_no") != 1 ||
		d.Text("no-existe") != "" || d.Number("verb") != -1 {
		t.Errorf("la primera entrega no conserva el cuerpo crudo o no se lee por campo: %s", d.Body)
	}
	if n := len(c.Select(func(d crmFakeDelivery) bool { return d.Status >= 500 })); n != 1 {
		t.Errorf("Select(5xx) dio %d entregas, quería 1", n)
	}
}

// crmFakeCheckReceived exige que las cinco entregas de crmFakeTestReceives quedaran apuntadas con su
// id, su juicio de firma y de schema y el código que el doble contestó.
func crmFakeCheckReceived(t *testing.T, got []crmFakeDelivery) {
	t.Helper()
	want := []struct {
		id        string
		signature bool
		schema    bool
		status    int
	}{
		{"41", true, true, 200}, {"42", false, true, 500}, {"43", true, true, 202}, {"44", true, false, 418}, {"45", false, true, 418},
	}
	if len(got) != len(want) {
		t.Fatalf("el doble apuntó %d entregas, quería %d", len(got), len(want))
	}
	for i, w := range want {
		d := got[i]
		judged := d.SignatureOK == w.signature && (d.SchemaErr == nil) == w.schema && d.InWindow
		if d.ID != w.id || !judged || d.Status != w.status || d.Method != http.MethodPost || d.Path != "/" {
			t.Errorf("entrega %d: %s %s id=%q firma=%v schema=%v status=%d ventana=%v; quería POST / id=%q firma=%v schema=%v status=%d",
				i, d.Method, d.Path, d.ID, d.SignatureOK, d.SchemaErr, d.Status, d.InWindow, w.id, w.signature, w.schema, w.status)
		}
	}
}

// crmFakeEcho es un servidor que apunta la última petición que recibió: sus cabeceras y su cuerpo.
type crmFakeEcho struct {
	server *httptest.Server
	mu     sync.Mutex
	header http.Header
	body   []byte
	err    error
}

// crmFakeNewEcho levanta el eco, que contesta 418 a todo, y registra su cierre.
func crmFakeNewEcho(t *testing.T) *crmFakeEcho {
	t.Helper()
	e := &crmFakeEcho{}
	e.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.header = r.Header.Clone()
		e.body, e.err = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(e.server.Close)
	return e
}

// last devuelve las cabeceras y el cuerpo de la última petición. Falla (t.Fatalf) si no se pudo leer.
func (e *crmFakeEcho) last(t *testing.T) (http.Header, []byte) {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err != nil {
		t.Fatalf("el eco no pudo leer el cuerpo: %v", e.err)
	}
	return e.header, e.body
}

func crmFakeTestSignsCallback(t *testing.T) {
	const tenant = "0b0b0b0b-0000-4000-8000-000000000001"
	at := time.Unix(crmFakeVectorTime, 0)
	body := crmFakeStatusBody(t, "3f2a1c9e-5b7d-4a10-9c3e-8d16b4f2a077", "paid", "OC-1", at)
	cb := crmFakeSignedCallback(crmFakeVectorKey, tenant, at, body)
	if cb.Tenant != tenant || cb.Timestamp != strconv.FormatInt(crmFakeVectorTime, 10) || !bytes.Equal(cb.Body, body) {
		t.Errorf("el callback firmado no lleva el tenant, el timestamp o el cuerpo: %+v", cb)
	}
	if !crmFakeSignatureShape.MatchString(cb.Signature) || !crmFakeVerify(crmFakeVectorKey, crmFakeVectorTime, body, cb.Signature) {
		t.Errorf("la firma del callback %q no es `v1=` + 64 hex o no verifica", cb.Signature)
	}

	echo := crmFakeNewEcho(t)
	if r, err := cb.send(t.Context(), echo.server.URL); err != nil || r.Codigo != http.StatusTeapot {
		t.Fatalf("send: código %d, error %v", r.Codigo, err)
	}
	seen, seenBody := echo.last(t)
	want := map[string]string{
		crmFakeHeaderTenant: tenant, crmFakeHeaderTimestamp: cb.Timestamp, crmFakeHeaderSignature: cb.Signature,
		"Authorization": "", "Content-Type": "application/json",
	}
	for name, value := range want {
		if seen.Get(name) != value {
			t.Errorf("el callback llegó con %s = %q, quería %q", name, seen.Get(name), value)
		}
	}
	if !bytes.Equal(seenBody, body) {
		t.Errorf("el callback llegó con el cuerpo %s, quería %s", seenBody, body)
	}

	// Una cabecera vacía NO se manda: es como la tabla adversaria prueba «cabecera ausente».
	cb.Signature, cb.Tenant = "", ""
	if _, err := cb.send(t.Context(), echo.server.URL); err != nil {
		t.Fatalf("send sin firma ni tenant: %v", err)
	}
	seen, _ = echo.last(t)
	for _, name := range []string{crmFakeHeaderSignature, crmFakeHeaderTenant} {
		if _, ok := seen[name]; ok {
			t.Errorf("la cabecera %s vacía se mandó: %v", name, seen)
		}
	}
}

func crmFakeTestStatusSchema(t *testing.T) {
	c := crmFakeNew(t, crmFakeVectorKey)
	example := crmFakeExample(t, "intake.status.json")
	if err := c.ValidateStatus(example); err != nil {
		t.Fatalf("el ejemplo publicado de intake.status no valida: %v", err)
	}
	at := time.Unix(crmFakeVectorTime, 0)
	for _, ref := range []string{"", "OC-2026-004512"} {
		body := crmFakeStatusBody(t, "3f2a1c9e-5b7d-4a10-9c3e-8d16b4f2a077", "delivered", ref, at)
		if err := c.ValidateStatus(body); err != nil {
			t.Errorf("crmFakeStatusBody(ref %q) no es un intake.status válido: %v\n%s", ref, err, body)
		}
		if strings.Contains(string(body), "external_ref") != (ref != "") {
			t.Errorf("external_ref solo viaja cuando hay referencia: %s", body)
		}
	}
	mutations := map[string]func(map[string]any){
		"status del otro vocabulario": func(d map[string]any) { d["status"] = "confirmed" },
		"status en mayúsculas":        func(d map[string]any) { d["status"] = "PAID" },
		"tenant en el cuerpo":         func(d map[string]any) { d["tenant"] = "acme" },
		"sin occurred_at":             func(d map[string]any) { delete(d, "occurred_at") },
		"intake_id que no es un uuid": func(d map[string]any) { d["intake_id"] = "١٢٣" },
		"verbo de la ida":             func(d map[string]any) { d["verb"] = "intake.push" },
	}
	for name, mutate := range mutations {
		if err := c.ValidateStatus(crmFakeMutate(t, example, mutate)); err == nil {
			t.Errorf("%s: la mutación debía romper la validación del intake.status y validó", name)
		}
	}
}
