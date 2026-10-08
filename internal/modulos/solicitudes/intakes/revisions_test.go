package intakes

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"
)

// Las salidas esperadas de este fichero son LITERALES calculados con el fichero
// viejo (internal/intakes/revisions.go @ 64c181a): el candado de fronteras impide
// importarlo desde aquí, así que la equivalencia viejo ↔ nuevo se fija con lo que
// el viejo devolvió para este mismo corpus. Las claves de wire van escritas a mano,
// no con las constantes, para que el test no se mueva con lo que vigila.

// oneFrozenLine es la línea del ejemplo canónico: 2 × 2500 = 5000.
func oneFrozenLine() []RevisionLine {
	return []RevisionLine{{SKU: "emp-pino", Label: "Empanada de pino", Qty: 2, UnitPrice: 2500}}
}

// canonicalLinesPayload es lo que las tres puertas escriben para oneFrozenLine y un
// total de 5000 cuando no hay señal de corrección.
const canonicalLinesPayload = `{"version":1,"total":5000,"items":[{"sku":"emp-pino","label":"Empanada de pino","qty":2,"unit_price":2500}]}`

// TestRevisionKinds_AreTheSevenWireKeys: las clases son claves de wire en inglés y
// las cierra un CHECK de la tabla; ni una se renombra.
func TestRevisionKinds_AreTheSevenWireKeys(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, got, want string }{
		{"RevisionKindCart", RevisionKindCart, "cart"},
		{"RevisionKindInterpreted", RevisionKindInterpreted, "interpreted"},
		{"RevisionKindCorrected", RevisionKindCorrected, "corrected"},
		{"RevisionKindApproved", RevisionKindApproved, "approved"},
		{"RevisionKindCRM", RevisionKindCRM, "crm"},
		{"RevisionKindDiscarded", RevisionKindDiscarded, "discarded"},
		{"RevisionKindRevalidated", RevisionKindRevalidated, "revalidated"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, quería %q", c.name, c.got, c.want)
		}
	}
}

// TestRevisionAuthors_AreRolesNotPeople: los tres autores son roles fijos.
func TestRevisionAuthors_AreRolesNotPeople(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, got, want string }{
		{"RevisionBySystem", RevisionBySystem, "system"},
		{"RevisionByOwner", RevisionByOwner, "owner"},
		{"RevisionByCRM", RevisionByCRM, "crm"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, quería %q", c.name, c.got, c.want)
		}
	}
}

// TestRevisionPayloadVersion_IsOne: la versión del esquema que escribe este código.
func TestRevisionPayloadVersion_IsOne(t *testing.T) {
	t.Parallel()
	if RevisionPayloadVersion != 1 {
		t.Errorf("RevisionPayloadVersion = %d, quería 1", RevisionPayloadVersion)
	}
}

// TestErrEmptyRevisionPayload_Text: el texto del centinela es observable.
func TestErrEmptyRevisionPayload_Text(t *testing.T) {
	t.Parallel()
	const want = "la revisión no lleva payload"
	if got := ErrEmptyRevisionPayload.Error(); got != want {
		t.Errorf("ErrEmptyRevisionPayload = %q, quería %q", got, want)
	}
}

// TestCorrectionSignalKeys_MatchTheJSONTags: las tres claves son contrato del cable
// y coinciden con las etiquetas reales de CorrectionSignal, en su orden.
func TestCorrectionSignalKeys_MatchTheJSONTags(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, got, want string }{
		{"KeyAsCorrection", KeyAsCorrection, "as_correction"},
		{"KeyCorrectsRevisionNo", KeyCorrectsRevisionNo, "corrects_revision_no"},
		{"KeyCorrectsKind", KeyCorrectsKind, "corrects_kind"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, quería %q", c.name, c.got, c.want)
		}
	}

	raw, err := json.Marshal(CorrectionSignal{AsCorrection: true, CorrectsRevisionNo: 2, CorrectsKind: "cart"})
	if err != nil {
		t.Fatalf("serializar la señal: %v", err)
	}
	const want = `{"as_correction":true,"corrects_revision_no":2,"corrects_kind":"cart"}`
	if string(raw) != want {
		t.Errorf("señal completa = %s, quería %s", raw, want)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("la señal serializada no es un objeto: %v", err)
	}
	for _, key := range []string{KeyAsCorrection, KeyCorrectsRevisionNo, KeyCorrectsKind} {
		if _, ok := fields[key]; !ok {
			t.Errorf("la constante %q no es una etiqueta JSON de CorrectionSignal: %s", key, raw)
		}
	}
}

// TestCorrectionSignal_EmptySerializesNothing: los tres campos llevan omitempty; la
// ausencia ES el «no» y nunca se escribe `false`.
func TestCorrectionSignal_EmptySerializesNothing(t *testing.T) {
	t.Parallel()
	raw, err := json.Marshal(CorrectionSignal{})
	if err != nil {
		t.Fatalf("serializar la señal vacía: %v", err)
	}
	if string(raw) != `{}` {
		t.Errorf("señal vacía = %s, quería {}", raw)
	}
}

// TestRevisionLine_JSONTagsAreThePayloadContract: cuatro claves, en orden y sin
// omitempty.
func TestRevisionLine_JSONTagsAreThePayloadContract(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		line RevisionLine
		want string
	}{
		{"filled line", RevisionLine{SKU: "a", Label: "b", Qty: 1, UnitPrice: 2.5}, `{"sku":"a","label":"b","qty":1,"unit_price":2.5}`},
		{"zero line keeps its four keys", RevisionLine{}, `{"sku":"","label":"","qty":0,"unit_price":0}`},
	}
	for _, c := range cases {
		raw, err := json.Marshal(c.line)
		if err != nil {
			t.Fatalf("%s: serializar: %v", c.name, err)
		}
		if string(raw) != c.want {
			t.Errorf("%s: got %s, quería %s", c.name, raw, c.want)
		}
	}
}

// insertOnlyWriter solo sabe escribir revisiones: no tiene ni una lectura.
type insertOnlyWriter struct{ last Revision }

func (w *insertOnlyWriter) InsertRevision(_ context.Context, rev Revision) (Revision, error) {
	w.last = rev
	return rev, nil
}

// TestRevisionWriter_IsAWriteOnlyPort: el puerto está separado del Store de lectura;
// lo satisface quien solo tenga InsertRevision, y la revisión viaja entera por él.
// Lo que cada store promete al escribir (numeración, payload vacío, fecha) lo prueba
// la suite de contrato del puerto, no este fichero.
func TestRevisionWriter_IsAWriteOnlyPort(t *testing.T) {
	t.Parallel()
	stub := &insertOnlyWriter{}
	var writer RevisionWriter = stub

	in := Revision{
		IntakeID:     "intake-a",
		RevisionNo:   7,
		Kind:         RevisionKindCart,
		Payload:      json.RawMessage(canonicalLinesPayload),
		RenderedText: "Tu pedido: 2 × Empanada de pino",
		CreatedBy:    RevisionBySystem,
		CreatedAt:    time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC),
	}
	out, err := writer.InsertRevision(context.Background(), in)
	if err != nil {
		t.Fatalf("InsertRevision: %v", err)
	}
	if out.IntakeID != in.IntakeID || out.RevisionNo != in.RevisionNo || out.Kind != in.Kind ||
		string(out.Payload) != string(in.Payload) || out.RenderedText != in.RenderedText ||
		out.CreatedBy != in.CreatedBy || !out.CreatedAt.Equal(in.CreatedAt) {
		t.Errorf("la revisión no cruzó el puerto entera: %+v", out)
	}
	if !out.LiteralPrunedAt.IsZero() {
		t.Errorf("una revisión que nadie podó trae sello de poda: %v", out.LiteralPrunedAt)
	}
	if stub.last.IntakeID != "intake-a" {
		t.Errorf("el puerto no llegó a la implementación: %+v", stub.last)
	}
}

// TestCartRevisionPayload_Vectors: la forma canónica, el `[]` de la lista vacía y el
// formato de números y textos, byte a byte como los escribe el viejo.
func TestCartRevisionPayload_Vectors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		total float64
		lines []RevisionLine
		want  string
	}{
		{name: "canonical form", total: 5000, lines: oneFrozenLine(), want: canonicalLinesPayload},
		{name: "nil lines are an empty list not null", total: 0, lines: nil, want: `{"version":1,"total":0,"items":[]}`},
		{name: "empty lines are an empty list", total: 0, lines: []RevisionLine{}, want: `{"version":1,"total":0,"items":[]}`},
		{
			name:  "html runes and line separator are escaped, non ascii digits and nbsp are not",
			total: 0.1 + 0.2,
			lines: []RevisionLine{
				{SKU: "a<b>&c", Label: "Ñandú \u2028 «x» \u0661\u0662\u0663\u00a0", Qty: -1, UnitPrice: 1e21},
				{},
			},
			want: `{"version":1,"total":0.3,"items":[{"sku":"a\u003cb\u003e\u0026c","label":"Ñandú \u2028 «x» ` +
				"\u0661\u0662\u0663\u00a0" + `","qty":-1,"unit_price":1e+21},{"sku":"","label":"","qty":0,"unit_price":0}]}`,
		},
		{
			name:  "float formatting at the exponent limits",
			total: 1e20,
			lines: []RevisionLine{
				{SKU: "s", Label: "l", Qty: 1, UnitPrice: 0.000001},
				{SKU: "s", Label: "l", Qty: 1, UnitPrice: 0.0000001},
			},
			want: `{"version":1,"total":100000000000000000000,"items":[{"sku":"s","label":"l","qty":1,"unit_price":0.000001},{"sku":"s","label":"l","qty":1,"unit_price":1e-7}]}`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			raw, err := CartRevisionPayload(c.total, c.lines)
			if err != nil {
				t.Fatalf("CartRevisionPayload: %v", err)
			}
			if string(raw) != c.want {
				t.Errorf("payload:\n got %s\nquería %s", raw, c.want)
			}
		})
	}
}

// TestCartRevisionPayload_CarriesTheVersionInsideTheBlob: la versión viaja DENTRO
// del payload y es la de la constante.
func TestCartRevisionPayload_CarriesTheVersionInsideTheBlob(t *testing.T) {
	t.Parallel()
	raw, err := CartRevisionPayload(5000, oneFrozenLine())
	if err != nil {
		t.Fatalf("CartRevisionPayload: %v", err)
	}
	var got struct {
		Version int            `json:"version"`
		Total   float64        `json:"total"`
		Items   []RevisionLine `json:"items"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("el payload no es JSON válido (%s): %v", raw, err)
	}
	if got.Version != RevisionPayloadVersion {
		t.Errorf("version = %d, quería %d", got.Version, RevisionPayloadVersion)
	}
	if got.Total != 5000 || len(got.Items) != 1 || got.Items[0] != oneFrozenLine()[0] {
		t.Errorf("la foto no devuelve lo que se congeló: %+v", got)
	}
}

// TestCorrectedRevisionPayload_Vectors: sin señal sale byte a byte como el del
// carrito; con señal, sus campos no vacíos van en la raíz, detrás de `items`, y
// cada uno se omite por separado.
func TestCorrectedRevisionPayload_Vectors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		lines  []RevisionLine
		signal CorrectionSignal
		want   string
	}{
		{name: "empty signal is the cart payload", lines: oneFrozenLine(), signal: CorrectionSignal{}, want: canonicalLinesPayload},
		{
			name:   "full signal goes after items in tag order",
			lines:  oneFrozenLine(),
			signal: CorrectionSignal{AsCorrection: true, CorrectsRevisionNo: 3, CorrectsKind: "interpreted"},
			want:   `{"version":1,"total":5000,"items":[{"sku":"emp-pino","label":"Empanada de pino","qty":2,"unit_price":2500}],"as_correction":true,"corrects_revision_no":3,"corrects_kind":"interpreted"}`,
		},
		{
			name:   "mark without the pair",
			lines:  oneFrozenLine(),
			signal: CorrectionSignal{AsCorrection: true},
			want:   `{"version":1,"total":5000,"items":[{"sku":"emp-pino","label":"Empanada de pino","qty":2,"unit_price":2500}],"as_correction":true}`,
		},
		{
			name:   "pair without the mark never writes false, nil lines are a list",
			lines:  nil,
			signal: CorrectionSignal{CorrectsRevisionNo: 1, CorrectsKind: "cart"},
			want:   `{"version":1,"total":5000,"items":[],"corrects_revision_no":1,"corrects_kind":"cart"}`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			raw, err := CorrectedRevisionPayload(5000, c.lines, c.signal)
			if err != nil {
				t.Fatalf("CorrectedRevisionPayload: %v", err)
			}
			if string(raw) != c.want {
				t.Errorf("payload:\n got %s\nquería %s", raw, c.want)
			}
		})
	}
}

// TestApprovedRevisionPayload_Vectors: la misma forma que la del carrito; lo que
// distingue a la aprobación es su `kind`, no su payload.
func TestApprovedRevisionPayload_Vectors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		total float64
		lines []RevisionLine
		want  string
	}{
		{name: "canonical form", total: 5000, lines: oneFrozenLine(), want: canonicalLinesPayload},
		{name: "nil lines are an empty list", total: 0, lines: nil, want: `{"version":1,"total":0,"items":[]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			raw, err := ApprovedRevisionPayload(c.total, c.lines)
			if err != nil {
				t.Fatalf("ApprovedRevisionPayload: %v", err)
			}
			if string(raw) != c.want {
				t.Errorf("payload:\n got %s\nquería %s", raw, c.want)
			}
		})
	}
}

// TestRevisionPayloads_SerializationErrorNamesTheRevision: un número que JSON no
// puede escribir devuelve payload nil y un error que dice QUÉ revisión se perdió;
// el de encoding/json va envuelto.
func TestRevisionPayloads_SerializationErrorNamesTheRevision(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		build func() (json.RawMessage, error)
		want  string
	}{
		{
			name:  "cart with a NaN total",
			build: func() (json.RawMessage, error) { return CartRevisionPayload(math.NaN(), oneFrozenLine()) },
			want:  "intakes: serializar payload de la revisión del carrito: json: unsupported value: NaN",
		},
		{
			name: "manual correction with an infinite total",
			build: func() (json.RawMessage, error) {
				return CorrectedRevisionPayload(math.Inf(1), oneFrozenLine(), CorrectionSignal{})
			},
			want: "intakes: serializar payload de la revisión de la corrección manual: json: unsupported value: +Inf",
		},
		{
			name: "approval with an infinite line price",
			build: func() (json.RawMessage, error) {
				return ApprovedRevisionPayload(5000, []RevisionLine{{UnitPrice: math.Inf(-1)}})
			},
			want: "intakes: serializar payload de la revisión de la aprobación: json: unsupported value: -Inf",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			raw, err := c.build()
			if err == nil {
				t.Fatalf("se serializó un número imposible: %s", raw)
			}
			if raw != nil {
				t.Errorf("con error el payload debe ser nil, got %s", raw)
			}
			if got := err.Error(); got != c.want {
				t.Errorf("error = %q, quería %q", got, c.want)
			}
			var unsupported *json.UnsupportedValueError
			if !errors.As(err, &unsupported) {
				t.Errorf("el error de encoding/json no va envuelto con %%w: %v", err)
			}
		})
	}
}
