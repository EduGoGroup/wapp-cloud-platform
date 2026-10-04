//go:build integracion

package procesos

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// El doble del CRM (diseno.md §3.5, T9.19): el PUENTE del cliente visto desde la nube. Hace las dos
// mitades del contrato wapp-crm-v1:
//
//   - RECIBE el `intake.push` (la ida): un httptest.Server en loopback que apunta cada entrega, verifica
//     su firma como el contrato le pide a un puente (cuerpo crudo, HMAC-SHA256, ventana de ±300 s) y
//     valida el JSON contra el schema PUBLICADO, leído del repo en ejecución
//     (docs/contracts/wapp-crm-v1/, que está congelado: se lee, no se copia ni se edita). Contesta lo que
//     el proceso le pida.
//   - FIRMA el `intake.status` (la vuelta): arma las tres cabeceras del callback como las mandaría un
//     puente, y deja que el proceso estropee cualquiera de ellas para la tabla adversaria.
//
// La firma está REIMPLEMENTADA aquí con la biblioteca estándar, sin importar el paquete del servidor
// (R9.4.d): si las dos implementaciones se separan, el proceso lo ve como una firma que no verifica.

const (
	// crmFakeContractDir es el contrato publicado, relativo al directorio del paquete (donde corre
	// `go test`).
	crmFakeContractDir = "../../docs/contracts/wapp-crm-v1"
	// Los dos schemas que el doble usa: el de la ida y el de la vuelta.
	crmFakePushSchemaFile   = "intake.push.schema.json"
	crmFakeStatusSchemaFile = "intake.status.schema.json"

	// Las cabeceras del contrato (README del contrato §4 e intake.status.md).
	crmFakeHeaderSignature = "X-Wapp-Signature"
	crmFakeHeaderTimestamp = "X-Wapp-Timestamp"
	crmFakeHeaderDelivery  = "X-Wapp-Delivery"
	crmFakeHeaderTenant    = "X-Wapp-Tenant"

	// crmFakeSignaturePrefix identifica el ESQUEMA de firma en la cabecera, no la versión del contrato.
	crmFakeSignaturePrefix = "v1="
	// crmFakeWindow es la ventana anti-replay que el contrato pide a los dos lados: ±300 s.
	crmFakeWindow = 300 * time.Second
	// crmFakeMaxBody acota lo que el doble lee de una entrega: un `intake.push` no se acerca.
	crmFakeMaxBody = 1 << 20

	// crmFakeCallbackPath es la puerta de la vuelta en la API pública del servidor.
	crmFakeCallbackPath = "/api/v1/integrations/callback"
	// crmFakeCallbackTimeout es el plazo de un callback, de la petición a la respuesta entera.
	crmFakeCallbackTimeout = 15 * time.Second
)

// crmFakeDelivery es una entrega tal como la recibió el doble, ya juzgada.
type crmFakeDelivery struct {
	// Method, Path, Header y Body son la petición cruda: el método, la ruta, las cabeceras y los
	// bytes del cuerpo tal como llegaron.
	Method string
	Path   string
	Header http.Header
	Body   []byte
	// ID es X-Wapp-Delivery (el id de la fila de webhook_outbox) y Timestamp el X-Wapp-Timestamp.
	ID        string
	Timestamp string
	// SignatureOK dice si X-Wapp-Signature es `v1=` + el HMAC-SHA256 en hexadecimal minúscula de
	// «v1:<timestamp>:<cuerpo crudo>» con el secreto vigente del doble.
	SignatureOK bool
	// InWindow dice si el timestamp es un entero Unix a ±300 s del reloj del doble.
	InWindow bool
	// SchemaErr es nil si el cuerpo valida contra intake.push.schema.json (con los formatos uuid y
	// date-time exigidos), o el motivo.
	SchemaErr error
	// Doc es el cuerpo decodificado (nil si no es un objeto JSON). Los números son float64.
	Doc map[string]any
	// Status es el código HTTP que el doble contestó a esta entrega.
	Status int
	// At es cuándo llegó, según el reloj del doble.
	At time.Time
}

// Text devuelve el campo de texto key del documento, o «» si falta o no es una cadena.
func (d crmFakeDelivery) Text(key string) string {
	v, ok := d.Doc[key].(string)
	if !ok {
		return ""
	}
	return v
}

// Number devuelve el campo numérico key del documento, o -1 si falta o no es un número.
func (d crmFakeDelivery) Number(key string) float64 {
	v, ok := d.Doc[key].(float64)
	if !ok {
		return -1
	}
	return v
}

// crmFake es el doble. Lo crea crmFakeNew; es seguro entre goroutines.
type crmFake struct {
	server       *httptest.Server
	pushSchema   *jsonschema.Schema
	statusSchema *jsonschema.Schema

	mu         sync.Mutex
	secret     string
	respond    func(crmFakeDelivery) int
	deliveries []crmFakeDelivery
}

// crmFakeNew levanta el doble en 127.0.0.1:<puerto efímero>, compila los dos schemas del contrato
// publicado y registra su cierre en t.Cleanup. Recibe el secreto de firma del tenant (el mismo que el
// proceso le da al servidor por PUT /api/v1/integrations). Nace contestando 200 a todo. Falla
// (t.Fatalf) si un schema no se puede leer o compilar, o si el listener no quedó en loopback IPv4.
func crmFakeNew(t *testing.T, secret string) *crmFake {
	t.Helper()
	c := &crmFake{
		secret:       secret,
		pushSchema:   crmFakeCompile(t, crmFakePushSchemaFile),
		statusSchema: crmFakeCompile(t, crmFakeStatusSchemaFile),
		respond:      func(crmFakeDelivery) int { return http.StatusOK },
	}
	c.server = httptest.NewServer(http.HandlerFunc(c.serve))
	t.Cleanup(c.server.Close)
	if !strings.HasPrefix(c.server.URL, "http://127.0.0.1:") {
		t.Fatalf("crmFakeNew: el doble debe escuchar en 127.0.0.1 y escucha en %s", c.server.URL)
	}
	return c
}

// crmFakeCompile compila un schema del contrato publicado con los FORMATOS exigidos (el dialecto
// 2020-12 los trata como anotación si no se pide): así `intake_id` tiene que ser un uuid y `timestamp`
// un date-time de verdad. Falla (t.Fatalf) si el fichero no está o no compila.
func crmFakeCompile(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	schema, err := compiler.Compile(filepath.Join(crmFakeContractDir, name))
	if err != nil {
		t.Fatalf("crmFakeCompile: compilar %s del contrato publicado: %v", name, err)
	}
	return schema
}

// crmFakeRandomSecret devuelve un secreto de firma aleatorio de 48 caracteres hexadecimales: entra en
// los 24–256 que exige la API y no hay ninguno escrito en el repo. Falla (t.Fatalf) sin entropía.
func crmFakeRandomSecret(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		t.Fatalf("crmFakeRandomSecret: %v", err)
	}
	return hex.EncodeToString(raw)
}

// URL devuelve el endpoint del doble, «http://127.0.0.1:<puerto>»: lo que va en `endpoint_url`.
func (c *crmFake) URL() string { return c.server.URL }

// SetSecret cambia el secreto con el que el doble verifica lo que reciba A PARTIR de ahora.
func (c *crmFake) SetSecret(secret string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.secret = secret
}

// Respond hace que el doble conteste status a toda entrega a partir de ahora.
func (c *crmFake) Respond(status int) {
	c.RespondWith(func(crmFakeDelivery) int { return status })
}

// RespondWith hace que el doble decida el código de cada entrega con rule, que la recibe ya juzgada
// (firma, ventana, schema y documento). rule corre en la goroutine de la conexión y sin el candado del
// doble: puede mirar lo que quiera de la entrega, pero no debe llamar a métodos del doble que esperen.
func (c *crmFake) RespondWith(rule func(crmFakeDelivery) int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.respond = rule
}

// Deliveries devuelve una COPIA, en orden de llegada, de todo lo recibido. Nunca nil.
func (c *crmFake) Deliveries() []crmFakeDelivery {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]crmFakeDelivery{}, c.deliveries...)
}

// Select devuelve, en orden de llegada, las entregas para las que keep da true.
func (c *crmFake) Select(keep func(crmFakeDelivery) bool) []crmFakeDelivery {
	out := []crmFakeDelivery{}
	for _, d := range c.Deliveries() {
		if keep(d) {
			out = append(out, d)
		}
	}
	return out
}

// Wait sondea hasta que haya al menos n entregas que cumplan keep y las devuelve. Falla (t.Fatalf)
// con lo que llegó si pasa timeout.
func (c *crmFake) Wait(t *testing.T, timeout time.Duration, what string, n int, keep func(crmFakeDelivery) bool) []crmFakeDelivery {
	t.Helper()
	var got []crmFakeDelivery
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()
	if !edgeSondear(ctx, func() bool {
		got = c.Select(keep)
		return len(got) >= n
	}) {
		t.Fatalf("el CRM falso esperaba %d entregas (%s) y en %s llegaron %d de %d recibidas en total",
			n, what, timeout, len(got), len(c.Deliveries()))
	}
	return got
}

// serve apunta la entrega, la juzga y contesta lo que diga la regla vigente. Un cuerpo que no se
// puede leer contesta 400 y queda apuntado con SchemaErr.
func (c *crmFake) serve(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, crmFakeMaxBody))
	c.mu.Lock()
	secret, rule := c.secret, c.respond
	c.mu.Unlock()

	d := c.judge(secret, r.Header.Clone(), body, time.Now())
	d.Method, d.Path = r.Method, r.URL.Path
	d.Status = rule(d)
	if err != nil {
		d.SchemaErr = fmt.Errorf("leer el cuerpo de la entrega: %w", err)
		d.Status = http.StatusBadRequest
	}
	if r.Method != http.MethodPost {
		d.SchemaErr = fmt.Errorf("la entrega llegó por %s y el contrato pide POST", r.Method)
		d.Status = http.StatusMethodNotAllowed
	}

	c.mu.Lock()
	c.deliveries = append(c.deliveries, d)
	c.mu.Unlock()
	w.WriteHeader(d.Status)
}

// judge hace lo que el contrato le pide a un puente al recibir (README del contrato §4): leer el
// cuerpo CRUDO, comprobar la ventana de ±300 s sobre el timestamp, recalcular el HMAC y compararlo en
// tiempo constante, y validar el documento contra el schema. Es pura salvo por now.
func (c *crmFake) judge(secret string, header http.Header, body []byte, now time.Time) crmFakeDelivery {
	d := crmFakeDelivery{
		Header:    header,
		Body:      body,
		ID:        header.Get(crmFakeHeaderDelivery),
		Timestamp: header.Get(crmFakeHeaderTimestamp),
		At:        now,
	}
	if ts, err := strconv.ParseInt(d.Timestamp, 10, 64); err == nil {
		d.InWindow = now.Sub(time.Unix(ts, 0)).Abs() <= crmFakeWindow
		d.SignatureOK = crmFakeVerify(secret, ts, body, header.Get(crmFakeHeaderSignature))
	}
	d.Doc, d.SchemaErr = crmFakeValidate(c.pushSchema, body)
	return d
}

// crmFakeValidate decodifica body y lo valida contra schema. Devuelve el documento (nil si no es un
// objeto JSON) y nil, o el motivo por el que no es un documento válido del contrato.
func crmFakeValidate(schema *jsonschema.Schema, body []byte) (map[string]any, error) {
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("el cuerpo no es JSON: %w", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("el cuerpo no es un objeto JSON: %w", err)
	}
	if err := schema.Validate(instance); err != nil {
		return doc, fmt.Errorf("el cuerpo no cumple el schema publicado: %w", err)
	}
	return doc, nil
}

// ValidateStatus dice si body es un `intake.status` válido según el schema publicado de la vuelta:
// nil si lo es, o el motivo. Sirve para que el proceso sepa qué cuerpos de su tabla SON del contrato.
func (c *crmFake) ValidateStatus(body []byte) error {
	_, err := crmFakeValidate(c.statusSchema, body)
	return err
}

// crmFakeSign calcula la firma del contrato (README §4): el HMAC-SHA256, en hexadecimal minúscula, de
// la cadena canónica «v1:<timestamp>:<cuerpo crudo>» con el secreto del tenant. Es pura.
func crmFakeSign(secret string, timestamp int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v1:" + strconv.FormatInt(timestamp, 10) + ":"))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// crmFakeVerify dice si header es `v1=` seguido de la firma de (secret, timestamp, body), comparada
// en tiempo constante. Es ESTRICTA, como el contrato: sin el prefijo, con otro prefijo o con el
// hexadecimal en mayúsculas no verifica.
func crmFakeVerify(secret string, timestamp int64, body []byte, header string) bool {
	got, ok := strings.CutPrefix(header, crmFakeSignaturePrefix)
	if !ok {
		return false
	}
	return hmac.Equal([]byte(got), []byte(crmFakeSign(secret, timestamp, body)))
}

// crmFakeFingerprint devuelve la huella que la API publica de un secreto: los ocho primeros
// hexadecimales de su SHA-256 (GET /api/v1/integrations, `secret_fingerprint`).
func crmFakeFingerprint(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])[:8]
}

// crmFakeCallback es un callback `intake.status` listo para mandar: el cuerpo crudo y las tres
// cabeceras del contrato. Los campos son cadenas para que la tabla adversaria pueda poner en ellos lo
// que un puente roto pondría; una cabecera vacía NO se manda.
type crmFakeCallback struct {
	Tenant    string
	Timestamp string
	Signature string
	Body      []byte
}

// crmFakeSignedCallback arma el callback como lo haría el puente: X-Wapp-Tenant con el tenant,
// X-Wapp-Timestamp con el instante Unix en segundos y X-Wapp-Signature con `v1=` + la firma del cuerpo
// crudo con el secreto del tenant. Es pura: el proceso elige el instante.
func crmFakeSignedCallback(secret, tenant string, at time.Time, body []byte) crmFakeCallback {
	ts := at.Unix()
	return crmFakeCallback{
		Tenant:    tenant,
		Timestamp: strconv.FormatInt(ts, 10),
		Signature: crmFakeSignaturePrefix + crmFakeSign(secret, ts, body),
		Body:      body,
	}
}

// crmFakeStatusBody arma el cuerpo de un `intake.status` con los campos obligatorios del contrato y,
// si externalRef no es vacío, la referencia del CRM. Falla (t.Fatalf) si no se puede serializar.
func crmFakeStatusBody(t *testing.T, intakeID, status, externalRef string, occurredAt time.Time) []byte {
	t.Helper()
	doc := map[string]string{
		"contract_version": "1",
		"verb":             "intake.status",
		"intake_id":        intakeID,
		"status":           status,
		"occurred_at":      occurredAt.UTC().Format(time.RFC3339),
	}
	if externalRef != "" {
		doc["external_ref"] = externalRef
	}
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("crmFakeStatusBody: %v", err)
	}
	return body
}

// Post manda el callback a la API pública del servidor, SIN Authorization (la credencial del puente es
// la firma) y con Content-Type JSON, y devuelve la respuesta cruda sea cual sea su código. Falla
// (t.Fatalf) solo si la petición no se puede construir o no llega.
func (cb crmFakeCallback) Post(t *testing.T, s *servidor) respuesta {
	t.Helper()
	r, err := cb.send(t.Context(), "http://"+s.PublicaAddr+crmFakeCallbackPath)
	if err != nil {
		t.Fatalf("callback del CRM: %v", err)
	}
	return r
}

// send hace el POST a url con las cabeceras no vacías del callback. Devuelve la respuesta, o el error
// de construir, mandar o leer. Un servidor que corta la conexión tras contestar (cuerpo demasiado
// grande) no es un error si la respuesta llegó.
func (cb crmFakeCallback) send(ctx context.Context, url string) (respuesta, error) {
	ctx, cancel := context.WithTimeout(ctx, crmFakeCallbackTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(cb.Body))
	if err != nil {
		return respuesta{}, fmt.Errorf("construir el POST: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for name, value := range map[string]string{
		crmFakeHeaderTenant:    cb.Tenant,
		crmFakeHeaderTimestamp: cb.Timestamp,
		crmFakeHeaderSignature: cb.Signature,
	} {
		if value != "" {
			req.Header.Set(name, value)
		}
	}
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := client.Do(req)
	if err != nil {
		return respuesta{}, fmt.Errorf("POST %s: %w", url, err)
	}
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, maxCuerpoRespuesta))
	if errClose := resp.Body.Close(); errClose != nil && errRead == nil {
		errRead = errClose
	}
	if errRead != nil {
		return respuesta{}, errors.Join(fmt.Errorf("leer la respuesta de %s", url), errRead)
	}
	return respuesta{Codigo: resp.StatusCode, Cuerpo: body}, nil
}
