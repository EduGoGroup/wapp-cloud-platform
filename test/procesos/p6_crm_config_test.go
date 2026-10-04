//go:build integracion

package procesos

import (
	"bytes"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

// La configuración del puente CRM por sus puertas: GET / PUT / DELETE /api/v1/integrations. Re-expresa
// internal/integrations/crud_integration_test.go y lo que postgres_integration_test.go afirma del
// secreto (cifrado en reposo, «secreto vacío conserva el existente»), visto desde fuera.

// p6Bridge es el cuerpo del PUT de un puente webhook hacia endpoint. secret «» no manda el campo:
// el secreto guardado se conserva.
func p6Bridge(endpoint, secret string, enabled bool) map[string]any {
	body := map[string]any{
		"catalog_adapter": "local", "events_adapter": "webhook", "endpoint_url": endpoint, "enabled": enabled,
	}
	if secret != "" {
		body["secret"] = secret
	}
	return body
}

// endpoint es la URL del CRM falso que la empresa del proceso configura como destino.
func (w *p6World) endpoint() string { return w.crm.URL() + "/hook" }

// p6DefaultIntegration es lo que contesta el GET de una empresa sin fila: local/local, apagado.
func p6DefaultIntegration() map[string]any {
	return map[string]any{
		"configured": false, "catalog_adapter": "local", "events_adapter": "local", "enabled": false, "secret_set": false,
	}
}

// configDefault: sin fila, el GET contesta el default (200, no 404) y la cola, todo a cero y sin
// `oldest_pending_at`. Nada de eso escribe en Postgres.
func (w *p6World) configDefault(t *testing.T) {
	r := w.call(t, w.admin, "", http.MethodGet, p6PathIntegration, nil)
	if got := p6Fields(t, r); r.Codigo != http.StatusOK || !reflect.DeepEqual(got, p6DefaultIntegration()) {
		t.Errorf("GET integrations sin fila: HTTP %d %v, quería 200 con el default local/local", r.Codigo, got)
	}
	r = w.call(t, w.admin, "", http.MethodGet, p6PathOutbox, nil)
	want := map[string]any{"pending": 0.0, "delivering": 0.0, "delivered": 0.0, "dead": 0.0}
	if got := p6Fields(t, r); r.Codigo != http.StatusOK || !reflect.DeepEqual(got, want) {
		t.Errorf("GET integrations/outbox sin entregas: HTTP %d %v, quería 200 con los cuatro contadores a cero", r.Codigo, got)
	}
	if got := p9Scalar(t, w.sc.DB, `SELECT (SELECT count(*) FROM public.tenant_integrations)::text || '|' ||
		(SELECT count(*) FROM public.webhook_outbox)::text`); got != "0|0" {
		t.Errorf("tenant_integrations|webhook_outbox = %s filas antes de configurar nada, quería 0|0", got)
	}
}

// p6ConfigCase es un PUT que la API tiene que rechazar: el cuerpo (un valor para JSON, o los bytes
// crudos), el código y un trozo del mensaje.
type p6ConfigCase struct {
	name string
	body any
	raw  []byte
	code int
	text string
}

// p6ConfigRejections es la tabla de rechazos del PUT. Lleva los del contrato de la ruta y los
// adversarios de reglas.md §2 en cada campo que entra por la puerta.
func (w *p6World) p6ConfigRejections() []p6ConfigCase {
	const (
		catalogMsg   = "catalog_adapter debe ser «local» o «webhook»"
		eventsMsg    = "events_adapter debe ser «local» o «webhook»"
		endpointMsg  = "endpoint_url debe ser una URL absoluta http(s)"
		keyLengthMsg = "el secreto de firma debe tener entre 24 y 256 caracteres"
		deferredMsg  = "catalog.pull diferido"
	)
	url := w.endpoint()
	// sized es un PUT de catalog `http` relleno de espacios hasta medir exactamente n bytes.
	sized := func(n int) []byte {
		const head = `{"catalog_adapter":"http"`
		return append(append([]byte(head), bytes.Repeat([]byte(" "), n-len(head)-1)...), '}')
	}
	return []p6ConfigCase{
		// catalog.pull está documentado y DIFERIDO: 422, no 400 (la petición se entiende y no se puede).
		{name: "catalog http", body: map[string]any{"catalog_adapter": "http"}, code: 422, text: deferredMsg},
		{name: "catalog http con U+00A0", body: map[string]any{"catalog_adapter": "\u00a0http\u00a0"}, code: 422, text: deferredMsg},
		{name: "catalog http con puente entero", body: map[string]any{"catalog_adapter": "http", "events_adapter": "webhook",
			"endpoint_url": url, "secret": w.secret, "enabled": true}, code: 422, text: deferredMsg},
		{name: "catalog http en 8192 bytes justos", raw: sized(8192), code: 422, text: deferredMsg},
		{name: "cuerpo de 8193 bytes", raw: sized(8193), code: 413, text: `"max_bytes":8192`},
		{name: "catalog en mayúsculas", body: map[string]any{"catalog_adapter": "HTTP"}, code: 400, text: catalogMsg},
		{name: "catalog con separador repetido", body: map[string]any{"catalog_adapter": "lo@@cal"}, code: 400, text: catalogMsg},
		{name: "catalog con dígitos no ASCII", body: map[string]any{"catalog_adapter": "١٢٣"}, code: 400, text: catalogMsg},
		{name: "catalog con U+00A0 dentro", body: map[string]any{"catalog_adapter": "lo\u00a0cal"}, code: 400, text: catalogMsg},
		{name: "events http", body: map[string]any{"events_adapter": "http"}, code: 400, text: eventsMsg},
		{name: "events con separador repetido", body: map[string]any{"events_adapter": "web@@hook"}, code: 400, text: eventsMsg},
		{name: "events con dígitos no ASCII", body: map[string]any{"events_adapter": "webhook١"}, code: 400, text: eventsMsg},
		{name: "endpoint relativo", body: map[string]any{"endpoint_url": "/hook"}, code: 400, text: endpointMsg},
		{name: "endpoint sin esquema", body: map[string]any{"endpoint_url": "127.0.0.1:8080/hook"}, code: 400, text: endpointMsg},
		{name: "endpoint ftp", body: map[string]any{"endpoint_url": "ftp://127.0.0.1/hook"}, code: 400, text: endpointMsg},
		{name: "endpoint sin host", body: map[string]any{"endpoint_url": "http:///hook"}, code: 400, text: endpointMsg},
		{name: "endpoint con separador repetido", body: map[string]any{"endpoint_url": "a@@b"}, code: 400, text: endpointMsg},
		{name: "endpoint con puerto en dígitos no ASCII", body: map[string]any{"endpoint_url": "http://127.0.0.1:١٢٣/hook"}, code: 400, text: endpointMsg},
		{name: "endpoint de 2001 bytes", body: map[string]any{"endpoint_url": "http://127.0.0.1/" + strings.Repeat("a", 2001-17)},
			code: 400, text: "endpoint_url es demasiado larga"},
		{name: "secreto de 23", body: map[string]any{"secret": strings.Repeat("s", 23)}, code: 400, text: keyLengthMsg},
		{name: "secreto de 257", body: map[string]any{"secret": strings.Repeat("s", 257)}, code: 400, text: keyLengthMsg},
		{name: "secreto de 7 dígitos no ASCII", body: map[string]any{"secret": strings.Repeat("١", 7)}, code: 400, text: keyLengthMsg},
		{name: "webhook encendido sin endpoint", body: map[string]any{"events_adapter": "webhook", "secret": w.secret, "enabled": true},
			code: 400, text: "un puente webhook encendido necesita endpoint_url"},
		{name: "no es JSON", raw: []byte(`{`), code: 400, text: "el cuerpo debe ser un JSON"},
		{name: "es una lista", raw: []byte(`[]`), code: 400, text: "el cuerpo debe ser un JSON"},
		{name: "enabled de otro tipo", raw: []byte(`{"enabled":"sí"}`), code: 400, text: "el cuerpo debe ser un JSON"},
	}
}

// configAdversarial recorre la tabla de rechazos del PUT: cada uno contesta su código y su motivo, y
// ninguno deja fila. Entre ellos, catalog.pull → 422 «catalog.pull diferido».
func (w *p6World) configAdversarial(t *testing.T) {
	cases := append(w.p6ConfigRejections(), p6ConfigCase{
		// Sin fila no hay secreto guardado que valga: un puente encendido sin secreto en el cuerpo no entra.
		name: "webhook encendido sin secreto", body: p6Bridge(w.endpoint(), "", true),
		code: 400, text: "un puente webhook encendido necesita un secreto de firma",
	})
	for _, c := range cases {
		var r respuesta
		if c.raw != nil {
			r = w.raw(t, w.admin, p6ActWrite, http.MethodPut, p6PathIntegration, c.raw)
		} else {
			r = w.call(t, w.admin, p6ActWrite, http.MethodPut, p6PathIntegration, c.body)
		}
		p6WantError(t, "PUT integrations ("+c.name+")", r, c.code, c.text)
		if strings.Contains(string(r.Cuerpo), w.secret) {
			t.Errorf("PUT integrations (%s): la respuesta devuelve el secreto", c.name)
		}
	}
	p6WantMark(t, w.sc, "tras la tabla de rechazos del PUT", "sin fila", p6IntegrationMark, w.sc.Tenant)
}

// configGates: las dos guardias de las cuatro rutas. Sin token, 401; el viewer lee y no escribe (403 de
// permiso, que no se audita); y una empresa de un plan sin crm_bridge no tiene ninguna de las cuatro
// (403 `feature_not_enabled`, que en las escrituras SÍ se audita: la feature se mira tras el permiso).
func (w *p6World) configGates(t *testing.T) {
	routes := []struct{ method, path string }{
		{http.MethodGet, p6PathIntegration}, {http.MethodPut, p6PathIntegration},
		{http.MethodDelete, p6PathIntegration}, {http.MethodGet, p6PathOutbox},
	}
	anon := p9Caller{client: w.sc.S.Publica(""), tenant: w.sc.Tenant}
	for _, route := range routes {
		write := route.method != http.MethodGet
		if r := w.call(t, anon, "", route.method, route.path, p6Bridge(w.endpoint(), w.secret, true)); r.Codigo != http.StatusUnauthorized {
			t.Errorf("%s %s sin token: HTTP %d, quería 401", route.method, route.path, r.Codigo)
		}
		r := w.call(t, w.viewer, "", route.method, route.path, p6Bridge(w.endpoint(), w.secret, true))
		if want := map[bool]int{true: http.StatusForbidden, false: http.StatusOK}[write]; r.Codigo != want {
			t.Errorf("%s %s como viewer: HTTP %d, quería %d", route.method, route.path, r.Codigo, want)
		}
		action := ""
		if write {
			action = p6ActWrite
		}
		r = w.call(t, w.noBridge, action, route.method, route.path, p6Bridge(w.endpoint(), w.secret, true))
		if got := p6Fields(t, r); r.Codigo != http.StatusForbidden || got["error"] != "feature_not_enabled" || got["feature"] != "crm_bridge" {
			t.Errorf("%s %s sin la feature crm_bridge: HTTP %d %v, quería 403 feature_not_enabled", route.method, route.path, r.Codigo, got)
		}
	}
	if got := p9Scalar(t, w.sc.DB, `SELECT count(*)::text FROM public.tenant_integrations`); got != "0" {
		t.Errorf("tenant_integrations = %s filas tras los rechazos de las guardias, quería 0", got)
	}
}

// p6WantBridge exige que la respuesta sea el DTO de un puente configurado con esos valores y esa
// huella, y que no lleve el secreto. Devuelve created_at y updated_at.
func p6WantBridge(t *testing.T, what string, r respuesta, endpoint, secret string, enabled bool) (created, updated time.Time) {
	t.Helper()
	got := p6Fields(t, r)
	want := map[string]any{
		"configured": true, "catalog_adapter": "local", "events_adapter": "webhook", "endpoint_url": endpoint,
		"enabled": enabled, "secret_set": true, "secret_fingerprint": crmFakeFingerprint(secret),
	}
	var stamps [2]time.Time
	for i, key := range []string{"created_at", "updated_at"} {
		text, ok := got[key].(string)
		stamp, err := time.Parse(time.RFC3339, text)
		if !ok || err != nil {
			t.Errorf("%s: %s = %v, quería un instante RFC3339", what, key, got[key])
		}
		stamps[i] = stamp
		delete(got, key)
	}
	if r.Codigo != http.StatusOK || !reflect.DeepEqual(got, want) {
		t.Errorf("%s: HTTP %d %v, quería 200 %v", what, r.Codigo, got, want)
	}
	if strings.Contains(string(r.Cuerpo), secret) {
		t.Errorf("%s: la respuesta devuelve el secreto en claro", what)
	}
	return stamps[0], stamps[1]
}

// p6EnvelopeMark es el sobre del secreto de una empresa: las tres piezas, o «» si falta alguna.
const p6EnvelopeMark = `SELECT md5(secret_enc) || md5(secret_dek) || secret_kek_id FROM public.tenant_integrations
	WHERE tenant_id = $1 AND secret_enc IS NOT NULL AND secret_dek IS NOT NULL AND secret_kek_id IS NOT NULL`

// p6KeyInClear cuenta las filas de tenant_integrations que llevan el secreto dado en claro, en
// cualquiera de sus columnas: las de texto y las dos binarias del sobre.
const p6KeyInClear = `SELECT count(*)::text FROM public.tenant_integrations t WHERE
	position($1 in to_jsonb(t)::text) > 0 OR position(convert_to($1, 'UTF8') in secret_enc) > 0 OR
	position(convert_to($1, 'UTF8') in secret_dek) > 0`

// configSecretAtRest configura el puente de la empresa del proceso y el de la otra, y afirma lo que
// queda en tenant_integrations: el secreto en un sobre de tres piezas y NUNCA en claro; un PUT sin
// secreto conserva el sobre byte a byte; rotarlo lo cambia; y un PUT rechazado no mueve la fila.
func (w *p6World) configSecretAtRest(t *testing.T) {
	first := crmFakeRandomSecret(t)
	r := w.call(t, w.admin, p6ActWrite, http.MethodPut, p6PathIntegration, p6Bridge(w.crm.URL()+"/old", first, false))
	created, _ := p6WantBridge(t, "PUT con el primer secreto", r, w.crm.URL()+"/old", first, false)
	envelope := p9Scalar(t, w.sc.DB, p6EnvelopeMark, w.sc.Tenant)
	if envelope == "" {
		t.Fatalf("tras el PUT con secreto la fila no tiene las tres piezas del sobre (secret_enc, secret_dek, secret_kek_id)")
	}

	// Sin secreto en el cuerpo: cambia el endpoint y el interruptor, el sobre no se toca.
	r = w.call(t, w.admin, p6ActWrite, http.MethodPut, p6PathIntegration, p6Bridge(w.endpoint(), "", true))
	sameCreated, updated := p6WantBridge(t, "PUT sin secreto", r, w.endpoint(), first, true)
	if !sameCreated.Equal(created) || updated.Before(created) {
		t.Errorf("PUT sin secreto: created_at %s → %s, updated_at %s; created_at no se mueve", created, sameCreated, updated)
	}
	p6WantMark(t, w.sc, "tras el PUT sin secreto", envelope, p6EnvelopeMark, w.sc.Tenant)

	// Rotar: el sobre cambia y la huella es la del secreto nuevo, que es el que usa el proceso.
	r = w.call(t, w.admin, p6ActWrite, http.MethodPut, p6PathIntegration, p6Bridge(w.endpoint(), w.secret, true))
	p6WantBridge(t, "PUT rotando el secreto", r, w.endpoint(), w.secret, true)
	if got := p9Scalar(t, w.sc.DB, p6EnvelopeMark, w.sc.Tenant); got == envelope || got == "" {
		t.Errorf("tras rotar el secreto el sobre vale %q (antes %q): tenía que cambiar", got, envelope)
	}
	p6WantBridge(t, "GET tras configurar", w.call(t, w.admin, "", http.MethodGet, p6PathIntegration, nil), w.endpoint(), w.secret, true)
	p6WantBridge(t, "GET como viewer", w.call(t, w.viewer, "", http.MethodGet, p6PathIntegration, nil), w.endpoint(), w.secret, true)

	// Un PUT rechazado, con la fila ya puesta, no mueve ni una columna.
	mark := p9Scalar(t, w.sc.DB, p6IntegrationMark, w.sc.Tenant)
	for _, c := range w.p6ConfigRejections() {
		if c.raw != nil {
			continue
		}
		p6WantError(t, "PUT integrations con fila ("+c.name+")", w.call(t, w.admin, p6ActWrite, http.MethodPut, p6PathIntegration, c.body), c.code, c.text)
	}
	p6WantMark(t, w.sc, "tras los PUT rechazados con la fila puesta", mark, p6IntegrationMark, w.sc.Tenant)

	w.configOtherTenant(t)
	p6WantMark(t, w.sc, "tras configurar la otra empresa", mark, p6IntegrationMark, w.sc.Tenant)

	for _, secret := range []string{first, w.secret, w.otherSecret} {
		if got := p9Scalar(t, w.sc.DB, p6KeyInClear, secret); got != "0" {
			t.Errorf("FUGA: %s filas de tenant_integrations llevan un secreto de firma en claro", got)
		}
		if strings.Contains(w.sc.S.Log(), secret) {
			t.Errorf("FUGA: el log del servidor lleva un secreto de firma en claro")
		}
	}
	if got := p9Scalar(t, w.sc.DB, `SELECT count(*)::text FROM public.tenant_integrations`); got != "2" {
		t.Errorf("tenant_integrations = %s filas, quería 2 (una por empresa con puente)", got)
	}
}

// configOtherTenant configura el puente de la OTRA empresa con un cuerpo adversario que la API
// ACEPTA: los tres campos de texto con espacios Unicode en los extremos (se recortan), un endpoint con
// un U+00A0 DENTRO del host (pasa por URL absoluta http y se guarda tal cual), un `tenant_id` ajeno en
// el cuerpo (no existe para la ruta: manda el del token, INV-8) y un secreto de 12 dígitos no ASCII,
// que pasa el mínimo de «24 caracteres» porque se cuenta en BYTES. Después deja el secreto de la otra
// empresa que usa el proceso.
func (w *p6World) configOtherTenant(t *testing.T) {
	t.Helper()
	const endpoint = "http://127.0.0.1" + p6NBSP + ":9/otra"
	short := strings.Repeat("١", 12)
	r := w.call(t, w.other, p6ActWrite, http.MethodPut, p6PathIntegration, map[string]any{
		"tenant_id":       w.sc.Tenant,
		"catalog_adapter": "\u00a0local\u00a0", "events_adapter": " webhook\u00a0",
		"endpoint_url": "\u00a0" + endpoint + "\u00a0", "secret": short, "enabled": true,
	})
	p6WantBridge(t, "PUT adversario de la otra empresa", r, endpoint, short, true)
	if got := p9Scalar(t, w.sc.DB, `SELECT catalog_adapter || '|' || events_adapter || '|' || endpoint_url || '|' || enabled::text
		FROM public.tenant_integrations WHERE tenant_id = $1`, w.other.tenant); got != "local|webhook|"+endpoint+"|true" {
		t.Errorf("la fila de la otra empresa guarda %q, quería los campos recortados", got)
	}
	r = w.call(t, w.other, p6ActWrite, http.MethodPut, p6PathIntegration, p6Bridge(endpoint, w.otherSecret, true))
	p6WantBridge(t, "PUT de la otra empresa", r, endpoint, w.otherSecret, true)
}

// configDelete: el DELETE devuelve la empresa a local/local y se lleva el secreto; es idempotente
// (204 también sin fila), no toca la fila de otra empresa ni las entregas ya hechas, y con la fila
// borrada el callback del puente vuelve a ser un 401.
func (w *p6World) configDelete(t *testing.T) {
	other := p9Scalar(t, w.sc.DB, p6IntegrationMark, w.other.tenant)
	outbox := p9Scalar(t, w.sc.DB, p6OutboxMark)
	intake := p9Scalar(t, w.sc.DB, p6IntakeMark, w.approved)

	for i := range 2 {
		if r := w.call(t, w.admin, p6ActWrite, http.MethodDelete, p6PathIntegration, nil); r.Codigo != http.StatusNoContent || len(r.Cuerpo) != 0 {
			t.Errorf("DELETE integrations nº %d: HTTP %d %s, quería 204 sin cuerpo", i+1, r.Codigo, recortar(r.Cuerpo))
		}
		p6WantMark(t, w.sc, "tras el DELETE", "sin fila", p6IntegrationMark, w.sc.Tenant)
	}
	r := w.call(t, w.admin, "", http.MethodGet, p6PathIntegration, nil)
	if got := p6Fields(t, r); r.Codigo != http.StatusOK || !reflect.DeepEqual(got, p6DefaultIntegration()) {
		t.Errorf("GET integrations tras el DELETE: HTTP %d %v, quería el default local/local", r.Codigo, got)
	}
	p6WantMark(t, w.sc, "la fila de la otra empresa tras el DELETE", other, p6IntegrationMark, w.other.tenant)
	p6WantMark(t, w.sc, "la cola tras el DELETE", outbox, p6OutboxMark)

	// Sin fila no hay secreto con el que verificar: el callback bien firmado de antes ya no entra.
	body := crmFakeStatusBody(t, w.approved, "paid", "", time.Now())
	cb := crmFakeSignedCallback(w.secret, w.sc.Tenant, time.Now(), body)
	p6WantError(t, "callback tras borrar el puente", w.post(t, cb), http.StatusUnauthorized, "no autenticado")
	p6WantMark(t, w.sc, "la solicitud tras el callback sin puente", intake, p6IntakeMark, w.approved)
}
