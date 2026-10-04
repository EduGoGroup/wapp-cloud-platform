//go:build integracion

package procesos

import (
	"net/http"
	"strings"
	"testing"
)

// Los adversarios de P8: ids, cuerpos y credenciales que `POST /api/v1/intakes/{id}/reanalyze`
// rechaza. Se recorren SIN ningún job en vuelo, así que la marca es total: tras la tabla entera, ni
// una fila ni una columna de las tablas vigiladas cambió, y el Edge no atendió ni una inferencia. Se
// afirma lo que hace el viejo, con el cuerpo exacto de cada rechazo.

// p8Who dice con qué credencial se pide.
type p8Who int

const (
	p8Owner    p8Who = iota // la administradora de la empresa del proceso
	p8Outsider              // la administradora de otra empresa con llm_intake
	p8NoLLM                 // la administradora de una empresa sin llm_intake
)

// p8Adversary es una petición que la puerta rechaza.
type p8Adversary struct {
	name string
	who  p8Who
	// id es la forma del id: nil = la solicitud principal tal cual; si no, recibe el id principal.
	id   func(main string) string
	body any
	code int
	want string // el cuerpo exacto
}

// p8Unknown es un UUID bien formado que no es de ninguna solicitud.
const p8Unknown = "0f0f0f0f-0f0f-4f0f-8f0f-0f0f0f0f0f0f"

// p8Long es una transcripción de 281 runas: una más del tope del saneo. p8LongArabic, 281 dígitos
// árabe-índicos: 562 bytes y 281 runas (el tope cuenta runas).
var (
	p8Long       = strings.Repeat("a", 281)
	p8LongArabic = strings.Repeat("١", 281)
)

// p8AdversaryTable son los casos, medidos contra el viejo. El orden de los chequeos de la puerta es
// contrato: forma de `via` y de `text` (400) → capacidad llm_intake (403) → la vía afirmada coincide
// con la efectiva (400) → la solicitud es de la empresa (404). «No existe» y «es de otra empresa» son
// la misma respuesta, y quien no tiene la capacidad recibe 403 exista o no la solicitud.
var p8AdversaryTable = []p8Adversary{
	// El id.
	{name: "id desconocido", id: func(string) string { return p8Unknown }, body: map[string]any{}, code: 404, want: p8NotFound},
	{name: "id con dígitos no ASCII", id: p9ArabicDigits, body: map[string]any{}, code: 404, want: p8NotFound},
	{name: "id con separador repetido", id: func(string) string { return "a@@b" }, body: map[string]any{}, code: 404, want: p8NotFound},
	{name: "id con U+00A0 detrás", id: func(m string) string { return m + "\u00a0" }, body: map[string]any{}, code: 404, want: p8NotFound},
	{name: "id con U+00A0 delante", id: func(m string) string { return "\u00a0" + m }, body: map[string]any{}, code: 404, want: p8NotFound},
	{name: "id con un espacio delante", id: func(m string) string { return " " + m }, body: map[string]any{}, code: 404, want: p8NotFound},
	{name: "id con separador repetido dentro", id: func(m string) string { return strings.Replace(m, "-", "@@", 1) }, body: map[string]any{},
		code: 404, want: p8NotFound},
	{name: "solicitud de otra empresa", who: p8Outsider, body: map[string]any{}, code: 404, want: p8NotFound},
	{name: "solicitud de otra empresa, afirmando la vía", who: p8Outsider, body: map[string]any{"via": "local"}, code: 404, want: p8NotFound},

	// La capacidad.
	{name: "sin llm_intake, solicitud ajena", who: p8NoLLM, body: map[string]any{}, code: 403, want: p8NoIntake},
	{name: "sin llm_intake, solicitud desconocida", who: p8NoLLM, id: func(string) string { return p8Unknown }, body: map[string]any{},
		code: 403, want: p8NoIntake},
	{name: "sin llm_intake, id que no es un UUID", who: p8NoLLM, id: func(string) string { return "a@@b" }, body: map[string]any{},
		code: 403, want: p8NoIntake},
	{name: "sin llm_intake, vía api: la capacidad gana a la coincidencia", who: p8NoLLM, body: map[string]any{"via": "api"},
		code: 403, want: p8NoIntake},
	{name: "sin llm_intake, vía inválida: la forma gana a la capacidad", who: p8NoLLM, body: map[string]any{"via": "chatgpt"},
		code: 400, want: `{"error":"invalid_via","via":"chatgpt"}`},

	// La vía.
	{name: "vía fuera del vocabulario", body: map[string]any{"via": "chatgpt"}, code: 400, want: `{"error":"invalid_via","via":"chatgpt"}`},
	{name: "vía en mayúsculas", body: map[string]any{"via": "LOCAL"}, code: 400, want: `{"error":"invalid_via","via":"LOCAL"}`},
	{name: "vía con U+00A0", body: map[string]any{"via": "local\u00a0"}, code: 400, want: "{\"error\":\"invalid_via\",\"via\":\"local\u00a0\"}"},
	{name: "vía con un espacio delante", body: map[string]any{"via": " local"}, code: 400, want: `{"error":"invalid_via","via":" local"}`},
	{name: "vía con separador repetido", body: map[string]any{"via": "a@@b"}, code: 400, want: `{"error":"invalid_via","via":"a@@b"}`},
	{name: "vía con dígitos no ASCII", body: map[string]any{"via": "١٢٣"}, code: 400, want: `{"error":"invalid_via","via":"١٢٣"}`},
	{name: "vía api sin fila en tenant_llm: contradice la efectiva", body: map[string]any{"via": "api"},
		code: 400, want: `{"error":"invalid_via","via":"api","configured_via":"local"}`},
	{name: "vía inválida sobre una solicitud desconocida: la forma gana", id: func(string) string { return p8Unknown },
		body: map[string]any{"via": "chatgpt"}, code: 400, want: `{"error":"invalid_via","via":"chatgpt"}`},

	// El texto.
	{name: "transcripción de 281 runas", body: map[string]any{"text": p8Long}, code: 400, want: `{"error":"text_too_long","runes":281,"max":280}`},
	{name: "transcripción de 281 dígitos no ASCII", body: map[string]any{"text": p8LongArabic},
		code: 400, want: `{"error":"text_too_long","runes":281,"max":280}`},
	{name: "transcripción larga y vía inválida: gana la vía", body: map[string]any{"via": "chatgpt", "text": p8Long},
		code: 400, want: `{"error":"invalid_via","via":"chatgpt"}`},
	{name: "transcripción larga sobre una solicitud desconocida: la forma gana", id: func(string) string { return p8Unknown },
		body: map[string]any{"text": p8Long}, code: 400, want: `{"error":"text_too_long","runes":281,"max":280}`},

	// El cuerpo.
	{name: "sin cuerpo", body: nil, code: 400, want: p8BadBody},
	{name: "cuerpo lista", body: []any{}, code: 400, want: p8BadBody},
	{name: "cuerpo cadena", body: "x", code: 400, want: p8BadBody},
	{name: "vía que no es una cadena", body: map[string]any{"via": 3}, code: 400, want: p8BadBody},
	{name: "transcripción que no es una cadena", body: map[string]any{"text": []string{"a"}}, code: 400, want: p8BadBody},
}

// caller devuelve la credencial de un caso.
func (w *p8World) caller(who p8Who) p9Caller {
	switch who {
	case p8Outsider:
		return w.outsider
	case p8NoLLM:
		return w.noIntake
	default:
		return w.owner
	}
}

// adversarial recorre p8AdversaryTable, los cuerpos crudos y las credenciales que corta el
// middleware, con todos los jobs terminados. Después exige que nada cambió: las nueve tablas
// vigiladas con la misma huella, ni una inferencia, ningún texto al cliente y ningún job vivo.
func (w *p8World) adversarial(t *testing.T) {
	sc := w.sc
	before := p8Snapshot(t, sc, p8Tables)
	calls := len(sc.Script.Calls(""))

	for _, tc := range p8AdversaryTable {
		id := w.main.id
		if tc.id != nil {
			id = tc.id(w.main.id)
		}
		if r := w.post(t, w.caller(tc.who), id, tc.body); !p8Is(r, tc.code, tc.want) {
			t.Errorf("%s: HTTP %d %s; quería %d %s", tc.name, r.Codigo, recortar(r.Cuerpo), tc.code, tc.want)
		}
	}
	w.rawBodies(t)
	w.middleware(t)

	p8Unchanged(t, sc, before, "tras la tabla de adversarios")
	if n := len(sc.Script.Calls("")) - calls; n != 0 {
		t.Errorf("la tabla de adversarios provocó %d inferencias", n)
	}
	if n := consultaEntero(t, sc.DB, `SELECT count(*) FROM public.intake_jobs WHERE status NOT IN ('done', 'failed')`); n != 0 {
		t.Errorf("la tabla de adversarios dejó %d jobs vivos", n)
	}
	sc.expectNoPendingText(t, "tras la tabla de adversarios")
}

// rawBodies manda cuerpos que no son JSON, o que pasan del tope de 8 KiB de la puerta: 400 «cuerpo
// JSON inválido» en todos, sin llegar a mirar la solicitud.
func (w *p8World) rawBodies(t *testing.T) {
	t.Helper()
	for name, raw := range map[string]string{
		"JSON truncado":            `{"via":"local"`,
		"no es JSON":               `via=local`,
		"cuerpo vacío con espacio": " ",
		"más de 8 KiB":             `{"text":"` + strings.Repeat("a", 9000) + `"}`,
	} {
		if r := w.postRaw(t, w.main.id, []byte(raw)); !p8Is(r, http.StatusBadRequest, p8BadBody) {
			t.Errorf("cuerpo crudo «%s»: HTTP %d %s; quería 400 %s", name, r.Codigo, recortar(r.Cuerpo), p8BadBody)
		}
	}
}

// middleware afirma las dos credenciales que no llegan al handler, y que por eso no auditan: sin
// token (401) y un miembro de la empresa sin el permiso intakes.write (403), también con una vía
// inválida —el permiso va antes que la forma— y con una solicitud desconocida.
func (w *p8World) middleware(t *testing.T) {
	t.Helper()
	anon := p9Caller{client: w.sc.S.Publica(""), tenant: w.sc.Tenant}
	path := p8Path(w.main.id)
	if r := w.calls.call(t, anon, "", http.MethodPost, path, map[string]any{}); r.Codigo != http.StatusUnauthorized {
		t.Errorf("sin token: HTTP %d %s; quería 401", r.Codigo, recortar(r.Cuerpo))
	}
	viewer := p9Caller{client: w.viewer, tenant: w.sc.Tenant}
	for name, tc := range map[string]struct {
		path string
		body any
	}{
		"la solicitud principal":    {path, map[string]any{}},
		"una vía inválida":          {path, map[string]any{"via": "chatgpt"}},
		"una solicitud desconocida": {p8Path(p8Unknown), map[string]any{}},
	} {
		if r := w.calls.call(t, viewer, "", http.MethodPost, tc.path, tc.body); r.Codigo != http.StatusForbidden {
			t.Errorf("un viewer con %s: HTTP %d %s; quería 403", name, r.Codigo, recortar(r.Cuerpo))
		}
	}
}
