//go:build pendiente

package turnoacotado_test

// turnoacotado_test.go — EL RESOLUTOR, PROBADO SIN RED.
//
// Ningún test de este paquete llama a un modelo: lo que se ejerce es todo lo que el
// paquete decide —qué pregunta, qué acepta de vuelta y qué rechaza— con un doble del
// selector de vía en medio. Los dobles de este fichero los usan también
// troceado_test.go y prompt_test.go.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/turnoacotado"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/llmvia"
)

// El selector de vía NUEVO satisface el puerto tal cual: si hiciera falta un
// adaptador entre los dos, esto no compilaría.
var (
	_ turnoacotado.Turner = (*llmvia.Selector)(nil)
	_ turnoacotado.Turner = (*fakeTurner)(nil)
)

// markerKey es la clave con la que los tests marcan el ctx para comprobar que es el
// del llamante (o uno derivado de él) el que llega al selector.
type markerKey struct{}

// turnCall es una llamada a Turno tal como la vio el doble.
type turnCall struct {
	tenantID    string
	sessionID   string
	req         llmvia.TurnoRequest
	marker      any
	deadline    time.Time
	hasDeadline bool
}

// fakeTurner es el doble del selector de vía: guarda cada petición en orden y
// contesta lo que se le diga por llamada.
type fakeTurner struct {
	replies  []string // una por llamada; agotadas, se repite la última
	err      error    // con failFrom > 0, falla desde esa llamada (1 = la primera)
	failFrom int
	delay    time.Duration // lo que tarda cada llamada; solo en una burbuja de synctest
	calls    []turnCall
}

func (f *fakeTurner) Turno(ctx context.Context, tenantID, sessionID string, t llmvia.TurnoRequest) (string, error) {
	c := turnCall{tenantID: tenantID, sessionID: sessionID, req: t, marker: ctx.Value(markerKey{})}
	c.deadline, c.hasDeadline = ctx.Deadline()
	f.calls = append(f.calls, c)
	if f.delay > 0 {
		time.Sleep(f.delay) // reloj de la burbuja
	}
	if f.err != nil && f.failFrom > 0 && len(f.calls) >= f.failFrom {
		return "", f.err
	}
	if len(f.replies) == 0 {
		return "", nil
	}
	return f.replies[min(len(f.calls), len(f.replies))-1], nil
}

func newResolver(t *testing.T, f *fakeTurner) *turnoacotado.Resolver {
	t.Helper()
	r, err := turnoacotado.New(f)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if r == nil {
		t.Fatal("New devolvió un resolutor nil sin error")
	}
	return r
}

// fourOptionMenu es una consulta de ELECCIÓN con cuatro opciones cuyos códigos NO son
// números: el caso real del carrito, y lo que obliga a que el modelo conteste una
// posición y la traducción al código se haga en Go.
func fourOptionMenu(text string) modules.Query {
	return modules.Query{
		Class: modules.QueryClassOption, Level: "articles", Text: text,
		Options: []modules.QueryOption{
			{Code: "burger-clasica", Label: "Hamburguesa clásica"},
			{Code: "burger-doble", Label: "Hamburguesa doble"},
			{Code: "papas", Label: "Papas fritas"},
			{Code: "volver", Label: "Volver"},
		},
	}
}

func quantity(text string) modules.Query {
	return modules.Query{Class: modules.QueryClassQuantity, Level: "quantity", Text: text}
}

// picks es la respuesta cruda de un modelo que eligió el valor n.
func picks(n int) string {
	return `{"usable": true, "value": ` + strconv.Itoa(n) + `, "reason": "ok"}`
}

const inconclusiveReply = `{"usable": false, "value": null, "reason": "no_entendido"}`

// ---------------------------------------------------------------------------
// New y los dos centinelas
// ---------------------------------------------------------------------------

func TestNew_NilTurnerFailsAtStartup(t *testing.T) {
	t.Parallel()
	r, err := turnoacotado.New(nil)
	if !errors.Is(err, turnoacotado.ErrNoTurner) {
		t.Fatalf("err = %v, quiero ErrNoTurner: un resolutor sin con quién preguntar se ve al arrancar", err)
	}
	if r != nil {
		t.Errorf("resolutor = %v, quiero nil junto al error", r)
	}
}

// El texto de los dos errores es observable (acaba en un log): se copia literal.
func TestSentinels_LiteralTexts(t *testing.T) {
	t.Parallel()
	if got := turnoacotado.ErrNoTurner.Error(); got != "turnoacotado: el resolutor necesita un turnero (el selector de vía)" {
		t.Errorf("ErrNoTurner = %q", got)
	}
	if got := turnoacotado.ErrUnknownClass.Error(); got != "turnoacotado: clase de consulta fuera del vocabulario cerrado" {
		t.Errorf("ErrUnknownClass = %q", got)
	}
}

// ---------------------------------------------------------------------------
// Lo que el modelo devuelve
// ---------------------------------------------------------------------------

// Cada fila es un modo de fallo distinto de la salida del modelo. Ninguna es un
// error de la vía: o sale un código aplicable, o sale «no concluyente».
func TestResolveQuery_VerdictTable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		query modules.Query
		raw   string
		code  string // "" ⇒ no resuelto
	}{
		{"option by position", fourOptionMenu("quiero la doble"), `{"usable":true,"value":2,"reason":"ok"}`, "burger-doble"},
		{"first option", fourOptionMenu("la clásica"), picks(1), "burger-clasica"},
		{"last option is one more option", fourOptionMenu("mejor volvé atrás"), picks(4), "volver"},
		{"quantity in digits", quantity("mejor dos"), picks(2), "2"},
		{"quantity lower bound", quantity("una"), picks(1), "1"},
		{"quantity upper bound", quantity("muchas"), picks(9999), "9999"},
		{"quantity ignores the options range", modules.Query{
			Class: modules.QueryClassQuantity, Text: "siete",
			Options: []modules.QueryOption{{Code: "a", Label: "A"}},
		}, picks(7), "7"},

		{"option out of range", fourOptionMenu("quiero 5"), picks(5), ""},
		{"option zero", fourOptionMenu("ninguna"), picks(0), ""},
		{"negative option", fourOptionMenu("?"), picks(-1), ""},
		{"quantity zero", quantity("ninguna"), picks(0), ""},
		{"negative quantity", quantity("menos una"), picks(-3), ""},
		{"quantity above four digits", quantity("un millón"), picks(10000), ""},

		{"model says no", fourOptionMenu("¿hacen envíos?"), `{"usable":false,"value":null,"reason":"otra_pregunta"}`, ""},
		{"not usable even with a value in range", fourOptionMenu("la doble"), `{"usable":false,"value":2,"reason":"ok"}`, ""},
		{"usable without value", fourOptionMenu("la doble"), `{"usable":true}`, ""},
		{"null value with usable true", fourOptionMenu("la doble"), `{"usable":true,"value":null,"reason":"ok"}`, ""},
		{"value is not an integer", fourOptionMenu("la doble"), `{"usable":true,"value":2.5,"reason":"ok"}`, ""},
		{"value is a string of digits", fourOptionMenu("la doble"), `{"usable":true,"value":"2","reason":"ok"}`, ""},
		{"prose instead of JSON", fourOptionMenu("la doble"), `Creo que quiere la hamburguesa doble.`, ""},
		{"truncated JSON", fourOptionMenu("la doble"), `{"usable":true,"value":2`, ""},
		{"empty output", fourOptionMenu("la doble"), ``, ""},

		{"markdown fence", fourOptionMenu("la doble"), "```json\n{\"usable\":true,\"value\":3,\"reason\":\"ok\"}\n```", "papas"},
		{"reason lies and does not matter", fourOptionMenu("quiero papas"), `{"usable":true,"value":3,"reason":"no_entendido"}`, "papas"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v, err := newResolver(t, &fakeTurner{replies: []string{tc.raw}}).
				ResolveQuery(context.Background(), "tenant-1", "s-1", tc.query)
			if err != nil {
				t.Fatalf("una respuesta mala del modelo no es un error de la vía: %v", err)
			}
			if v.Code != tc.code {
				t.Fatalf("Code = %q, quiero %q", v.Code, tc.code)
			}
			wantReason := modules.QueryReason("")
			if tc.code == "" {
				wantReason = modules.QueryReasonInconclusive
			}
			if v.Reason != wantReason {
				t.Errorf("Reason = %q, quiero %q", v.Reason, wantReason)
			}
			if v.Codes != nil {
				t.Errorf("Codes = %v: una consulta sin trozos no devuelve códigos por trozo", v.Codes)
			}
		})
	}
}

// Aunque el modelo devuelva la frase del cliente en `value`, lo único que sale es un
// código del catálogo o unos dígitos: el Verdict se siembra en Vars y acaba en claro
// en flow_state.
func TestResolveQuery_NeverReturnsClientText(t *testing.T) {
	t.Parallel()
	// gosec ve un RUT y grita G101; es texto de una clienta, no una credencial.
	const clientText = "quiero la doble para Juan Pérez, RUT 12.345.678-9" //nolint:gosec
	for _, raw := range []string{
		`{"usable":true,"value":"` + clientText + `","reason":"ok"}`,
		`{"usable":true,"value":2,"reason":"` + clientText + `"}`,
	} {
		v, err := newResolver(t, &fakeTurner{replies: []string{raw}}).
			ResolveQuery(context.Background(), "tenant-1", "s-1", fourOptionMenu(clientText))
		if err != nil {
			t.Fatalf("ResolveQuery: %v", err)
		}
		if got := fmt.Sprintf("%+v", v); strings.Contains(got, "Juan") {
			t.Errorf("el texto del cliente salió en el veredicto: %s", got)
		}
	}
}

// ---------------------------------------------------------------------------
// La llamada
// ---------------------------------------------------------------------------

// Sin trozos se hace UNA llamada, con el tenant, la sesión y el ctx del llamante.
func TestResolveQuery_SingleCallCarriesTenantSessionAndContext(t *testing.T) {
	t.Parallel()
	f := &fakeTurner{replies: []string{picks(2)}}
	ctx := context.WithValue(context.Background(), markerKey{}, "ctx del llamante")

	if _, err := newResolver(t, f).ResolveQuery(ctx, "tenant-7", "session-9", fourOptionMenu("la doble")); err != nil {
		t.Fatalf("ResolveQuery: %v", err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("llamadas = %d, quiero 1", len(f.calls))
	}
	c := f.calls[0]
	if c.tenantID != "tenant-7" || c.sessionID != "session-9" {
		t.Errorf("tenant = %q, sesión = %q: no son los recibidos", c.tenantID, c.sessionID)
	}
	if c.marker != "ctx del llamante" {
		t.Errorf("el ctx que llegó al selector no es el del llamante (marca = %v)", c.marker)
	}
	if c.hasDeadline {
		t.Errorf("el turno de un solo texto no pone plazo propio: lo pone el selector (deadline = %v)", c.deadline)
	}
}

// ---------------------------------------------------------------------------
// Errores y ausencias
// ---------------------------------------------------------------------------

func TestResolveQuery_TransportFailurePropagatesIntact(t *testing.T) {
	t.Parallel()
	failure := errors.New("ollama caído")
	v, err := newResolver(t, &fakeTurner{err: failure, failFrom: 1}).
		ResolveQuery(context.Background(), "t", "s", fourOptionMenu("la doble"))
	if err != failure { //nolint:errorlint // se afirma que NO se envuelve: identidad, no errors.Is
		t.Fatalf("err = %v, quiero el fallo de la vía sin envolver", err)
	}
	if v.Code != "" || v.Reason != "" || v.Codes != nil {
		t.Errorf("veredicto = %+v, quiero el cero junto al error", v)
	}
}

func TestResolveQuery_UnknownClassHurtsWithoutSpendingACall(t *testing.T) {
	t.Parallel()
	for _, chunks := range [][]string{nil, {"uno", "dos"}} {
		f := &fakeTurner{replies: []string{picks(1)}}
		q := fourOptionMenu("hola")
		q.Class, q.Chunks = "inventada", chunks

		v, err := newResolver(t, f).ResolveQuery(context.Background(), "t", "s", q)
		if !errors.Is(err, turnoacotado.ErrUnknownClass) {
			t.Fatalf("trozos=%v: err = %v, quiero ErrUnknownClass", chunks, err)
		}
		if v.ResolvedAny() || v.Reason != "" {
			t.Errorf("trozos=%v: veredicto = %+v, quiero el cero junto al error", chunks, v)
		}
		if len(f.calls) != 0 {
			t.Errorf("trozos=%v: se gastó una inferencia en una consulta que no se sabe formular", chunks)
		}
	}
}

// Para un tenant en vía API este escalón no existe: se dice con el motivo, no con un
// error, para que no dispare el aviso al dueño. El centinela se reconoce envuelto.
func TestResolveQuery_APIRouteIsNotAFailure(t *testing.T) {
	t.Parallel()
	for _, failure := range []error{
		llmvia.ErrViaSinTurnoAcotado,
		fmt.Errorf("envuelto: %w", llmvia.ErrViaSinTurnoAcotado),
	} {
		v, err := newResolver(t, &fakeTurner{err: failure, failFrom: 1}).
			ResolveQuery(context.Background(), "t", "s", fourOptionMenu("la doble"))
		if err != nil {
			t.Fatalf("err = %v: una vía que no sirve este escalón no es una avería", err)
		}
		if v.Resolved() || v.Reason != modules.QueryReasonNoResolver {
			t.Errorf("veredicto = %+v, quiero no resuelto con motivo %q", v, modules.QueryReasonNoResolver)
		}
	}
}

// Elegir entre cero opciones es pagar una plaza del Edge por una respuesta que no se
// podría usar: no se pregunta, traiga o no trozos la consulta.
func TestResolveQuery_OptionWithoutOptionsSpendsNoCall(t *testing.T) {
	t.Parallel()
	for _, chunks := range [][]string{nil, {"uno", "dos"}} {
		f := &fakeTurner{replies: []string{picks(1)}}
		v, err := newResolver(t, f).ResolveQuery(context.Background(), "t", "s",
			modules.Query{Class: modules.QueryClassOption, Level: "articles", Text: "la doble", Chunks: chunks})
		if err != nil {
			t.Fatalf("trozos=%v: err = %v", chunks, err)
		}
		if v.ResolvedAny() || v.Reason != modules.QueryReasonInconclusive || v.Codes != nil {
			t.Errorf("trozos=%v: veredicto = %+v, quiero solo el motivo %q", chunks, v, modules.QueryReasonInconclusive)
		}
		if len(f.calls) != 0 {
			t.Errorf("trozos=%v: se llamó al modelo para elegir entre cero opciones", chunks)
		}
	}
}
