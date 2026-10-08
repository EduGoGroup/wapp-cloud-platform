package intakes

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"
)

// La parte PURA de approve.go: constantes, centinelas, los dos errores tipados y qué es una
// línea sin precio. La acción Approve está en approve_service_test.go.
//
// Las salidas esperadas son LITERALES calculados con el fichero viejo
// (internal/intakes/approve.go @ 64c181a): el candado de fronteras impide importarlo desde aquí.

// TestApprove_WireConstants: el estado desde el que se aprueba y las dos claves del contrato
// §7.4 son texto de wire; se conservan byte a byte aunque las constantes cambiaran de nombre.
func TestApprove_WireConstants(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, got, want string }{
		{name: "ApprovableStatus", got: ApprovableStatus, want: "pending_approval"},
		{name: "LineKeyUnitPrice", got: LineKeyUnitPrice, want: "unit_price"},
		{name: "LineKeyLabel", got: LineKeyLabel, want: "label"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, quería %q", c.name, c.got, c.want)
		}
	}
	if ApprovableStatus != StatusPendingApproval {
		t.Errorf("ApprovableStatus = %q, quería el estado del presupuesto por aprobar", ApprovableStatus)
	}
}

// TestApprove_SentinelTexts: los cuatro centinelas son texto observable (llegan a la respuesta
// del dueño) y son distinguibles entre sí con errors.Is.
func TestApprove_SentinelTexts(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want string
	}{
		{name: "ErrEmptyQuoteText", err: ErrEmptyQuoteText, want: "la aprobación no trae el texto de la cotización que se le manda al cliente"},
		{name: "ErrEmptyQuote", err: ErrEmptyQuote, want: "la solicitud no tiene ni una línea de cliente que cotizar"},
		{name: "ErrNoQuoteSender", err: ErrNoQuoteSender, want: "intakes: el servicio no tiene canal para responderle al cliente (falta WithQuoteSender)"},
		{name: "ErrNoRevisionWriter", err: ErrNoRevisionWriter, want: "intakes: el store cableado no sabe escribir revisiones (RevisionWriter)"},
	}
	for i, c := range cases {
		if got := c.err.Error(); got != c.want {
			t.Errorf("%s = %q, quería %q", c.name, got, c.want)
		}
		for j, other := range cases {
			if i != j && errors.Is(c.err, other.err) {
				t.Errorf("%s se confunde con %s", c.name, other.name)
			}
		}
	}
}

// TestApprove_NormalizesWhatItReads: el estado se normaliza AQUÍ. Con una lectura que trae
// `closed` crudo, el rechazo dice `confirmed`.
func TestApprove_NormalizesWhatItReads(t *testing.T) {
	t.Parallel()
	st := svcRawStatusStore{MemoryStore: svcSeedStore(t, StatusClosedLegacy), raw: StatusClosedLegacy}
	_, err := NewService(st, WithQuoteSender(&svcQuoteSpy{})).Approve(context.Background(), svcTenantA, svcIntakeID, "cotiza")
	svcWantNotApprovable(StatusConfirmed)(t, err)
}

// TestPendingPriceError_Text: recuento y cada línea como `índice:etiqueta`, sin concordancia de
// número y sin escapar la etiqueta. Es un error por PUNTERO.
func TestPendingPriceError_Text(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		lines []PendingPriceLine
		want  string
	}{
		{name: "no lines", want: "el borrador tiene 0 líneas sin precio ()"},
		{name: "one line keeps the plural", lines: []PendingPriceLine{{Index: 0, Label: "Torta"}}, want: "el borrador tiene 1 líneas sin precio (0:Torta)"},
		{
			name:  "empty label, a label with the separator and non ascii",
			lines: []PendingPriceLine{{Index: 1, Label: ""}, {Index: 3, Label: "pan, dulce"}, {Index: 12, Label: "ñandú \U0001F382"}},
			want:  "el borrador tiene 3 líneas sin precio (1:, 3:pan, dulce, 12:ñandú \U0001F382)",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			var err error = &PendingPriceError{Lines: c.lines}
			if got := err.Error(); got != c.want {
				t.Errorf("Error() = %q, quería %q", got, c.want)
			}
			var pending *PendingPriceError
			if !errors.As(err, &pending) || !reflect.DeepEqual(pending.Lines, c.lines) {
				t.Errorf("errors.As no recupera las líneas: %+v", pending)
			}
		})
	}
}

// TestNotApprovableError_Text: el estado va entre comillas al modo de %q.
func TestNotApprovableError_Text(t *testing.T) {
	t.Parallel()
	cases := []struct{ status, want string }{
		{status: "open", want: `una solicitud en "open" no se puede aprobar`},
		{status: "confirmed", want: `una solicitud en "confirmed" no se puede aprobar`},
		{status: "", want: `una solicitud en "" no se puede aprobar`},
		{status: `con "comillas"`, want: `una solicitud en "con \"comillas\"" no se puede aprobar`},
		{status: "línea\nrota", want: `una solicitud en "línea\nrota" no se puede aprobar`},
	}
	for _, c := range cases {
		var err error = &NotApprovableError{Status: c.status}
		if got := err.Error(); got != c.want {
			t.Errorf("Error() con %q = %q, quería %q", c.status, got, c.want)
		}
		var notApprovable *NotApprovableError
		if !errors.As(err, &notApprovable) || notApprovable.Status != c.status {
			t.Errorf("errors.As no recupera el estado %q", c.status)
		}
	}
}

// TestPendingPriceLine_JSONShape: la línea pendiente viaja en el cuerpo del rechazo con estas dos
// claves.
func TestPendingPriceLine_JSONShape(t *testing.T) {
	t.Parallel()
	got, err := json.Marshal(PendingPriceLine{Index: 3, Label: "pan dulce"})
	if err != nil {
		t.Fatalf("json.Marshal devolvió el error %v", err)
	}
	if want := `{"index":3,"label":"pan dulce"}`; string(got) != want {
		t.Errorf("JSON = %s, quería %s", got, want)
	}
}

// TestLastRevision: la de número MÁS ALTO, esté donde esté en la lista; el bool distingue «no
// hay» de «la primera»; en un empate gana la que aparece antes.
func TestLastRevision(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		in       []Revision
		wantNo   int
		wantKind string
		wantOK   bool
	}{
		{name: "nil has none", in: nil},
		{name: "empty has none", in: []Revision{}},
		{name: "single", in: []Revision{{RevisionNo: 1, Kind: "a"}}, wantNo: 1, wantKind: "a", wantOK: true},
		{
			name:   "highest is in the middle and the tie keeps the first",
			in:     []Revision{{RevisionNo: 2, Kind: "b"}, {RevisionNo: 5, Kind: "e"}, {RevisionNo: 1, Kind: "a"}, {RevisionNo: 5, Kind: "dup"}},
			wantNo: 5, wantKind: "e", wantOK: true,
		},
		{
			name:   "a zero number is still a revision",
			in:     []Revision{{RevisionNo: 0, Kind: "zero"}, {RevisionNo: -1, Kind: "neg"}},
			wantNo: 0, wantKind: "zero", wantOK: true,
		},
		{
			name:   "all negative",
			in:     []Revision{{RevisionNo: -2, Kind: "neg2"}, {RevisionNo: -1, Kind: "neg1"}},
			wantNo: -1, wantKind: "neg1", wantOK: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			before := slices.Clone(c.in)
			got, ok := LastRevision(c.in)
			if ok != c.wantOK || got.RevisionNo != c.wantNo || got.Kind != c.wantKind {
				t.Errorf("LastRevision = (%d %q, %v), quería (%d %q, %v)", got.RevisionNo, got.Kind, ok, c.wantNo, c.wantKind, c.wantOK)
			}
			if !reflect.DeepEqual(c.in, before) {
				t.Error("LastRevision mutó la lista de entrada")
			}
		})
	}
}

// TestPendingPriceLines_LooksOnlyAtTheLastRevision: el histórico es el rastro de cómo se llegó
// aquí, no lo que se vende; y el orden en que el store devuelva la lista no cambia la respuesta.
func TestPendingPriceLines_LooksOnlyAtTheLastRevision(t *testing.T) {
	t.Parallel()
	priced := json.RawMessage(`{"lines":[{"label":"A","unit_price":3}]}`)
	pendingA := json.RawMessage(`{"lines":[{"label":"A","unit_price":null}]}`)
	pendingB := json.RawMessage(`{"lines":[{"label":"B","unit_price":null}]}`)
	cases := []struct {
		name string
		in   []Revision
		want []PendingPriceLine
	}{
		{name: "no revisions", in: nil, want: nil},
		{
			name: "priced later, list in reverse order",
			in:   []Revision{{RevisionNo: 2, Payload: priced}, {RevisionNo: 1, Payload: pendingA}},
			want: nil,
		},
		{
			name: "priced first, pending later",
			in:   []Revision{{RevisionNo: 1, Payload: priced}, {RevisionNo: 2, Payload: pendingB}},
			want: []PendingPriceLine{{Index: 0, Label: "B"}},
		},
		{
			name: "pending later, list in reverse order",
			in:   []Revision{{RevisionNo: 2, Payload: pendingB}, {RevisionNo: 1, Payload: priced}},
			want: []PendingPriceLine{{Index: 0, Label: "B"}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := PendingPriceLines(c.in); !reflect.DeepEqual(got, c.want) {
				t.Errorf("PendingPriceLines = %+v, quería %+v", got, c.want)
			}
		})
	}
}

// TestLinesWithoutPrice recorre las formas de payload que existen de verdad en la tabla y las
// adversarias. `want` nil es «nada pendiente». Las runas no ASCII van con su escape.
func TestLinesWithoutPrice(t *testing.T) {
	t.Parallel()
	a := []PendingPriceLine{{Index: 0, Label: "A"}}
	unlabeled := []PendingPriceLine{{Index: 0, Label: ""}}
	cases := []struct {
		name    string
		payload string
		want    []PendingPriceLine
	}{
		// Lo que no es un borrador con `lines`: nada que buscar.
		{name: "empty payload", payload: ``},
		{name: "json null", payload: `null`},
		{name: "array root", payload: `[]`},
		{name: "string root", payload: `"x"`},
		{name: "number root", payload: `12`},
		{name: "broken json", payload: `{`},
		{name: "empty object", payload: `{}`},
		{name: "cart-shaped payload ignores its items", payload: `{"version":1,"total":10,"items":[{"sku":"a","unit_price":null}]}`},
		{name: "lines is null", payload: `{"lines":null}`},
		{name: "lines is an object", payload: `{"lines":{}}`},
		{name: "lines is a string", payload: `{"lines":"x"}`},
		{name: "lines is empty", payload: `{"lines":[]}`},
		{name: "lines key in upper case is not the key", payload: `{"LINES":[{"label":"A","unit_price":null}]}`},
		{name: "repeated lines key: the last one wins", payload: `{"lines":[{"label":"A","unit_price":null}],"lines":[]}`},

		// Sin precio.
		{name: "explicit null", payload: `{"lines":[{"label":"A","unit_price":null}]}`, want: a},
		{name: "missing key", payload: `{"lines":[{"label":"A"}]}`, want: a},
		{name: "price as a string", payload: `{"lines":[{"label":"A","unit_price":"12"}]}`, want: a},
		{name: "price as a boolean", payload: `{"lines":[{"label":"A","unit_price":true}]}`, want: a},
		{name: "price as an object", payload: `{"lines":[{"label":"A","unit_price":{}}]}`, want: a},
		{name: "price as a list", payload: `{"lines":[{"label":"A","unit_price":[1]}]}`, want: a},
		{name: "price overflows a float64", payload: `{"lines":[{"label":"A","unit_price":1e999}]}`, want: a},
		{name: "key with another case", payload: `{"lines":[{"label":"A","Unit_Price":5}]}`, want: a},
		{name: "key in upper case", payload: `{"lines":[{"label":"A","UNIT_PRICE":5}]}`, want: a},
		{name: "repeated price key ending in null", payload: `{"lines":[{"label":"A","unit_price":5,"unit_price":null}]}`, want: a},
		{name: "whitespace around the null", payload: ` {"lines":[{"label":"A","unit_price": null }]} `, want: a},

		// Con precio: un 0 es un regalo, no un pendiente.
		{name: "zero is a price", payload: `{"lines":[{"label":"A","unit_price":0}]}`},
		{name: "zero with decimals is a price", payload: `{"lines":[{"label":"A","unit_price":0.0}]}`},
		{name: "decimal price", payload: `{"lines":[{"label":"A","unit_price":12.5}]}`},
		{name: "negative is a price", payload: `{"lines":[{"label":"A","unit_price":-3}]}`},
		{name: "repeated price key ending in a number", payload: `{"lines":[{"label":"A","unit_price":null,"unit_price":5}]}`},
		// Un dígito no ASCII hace inválido el JSON ENTERO: el payload deja de ser un objeto.
		{name: "non ascii digit breaks the whole payload", payload: "{\"lines\":[{\"label\":\"A\",\"unit_price\":\u0663},{\"label\":\"B\"}]}"},

		// La etiqueta.
		{name: "no label", payload: `{"lines":[{"unit_price":null}]}`, want: unlabeled},
		{name: "empty line object", payload: `{"lines":[{}]}`, want: unlabeled},
		{name: "label is a number", payload: `{"lines":[{"label":7,"unit_price":null}]}`, want: unlabeled},
		{name: "label is null", payload: `{"lines":[{"label":null,"unit_price":null}]}`, want: unlabeled},
		{name: "label key with another case", payload: `{"lines":[{"Label":"B","unit_price":null}]}`, want: unlabeled},
		{
			name:    "label is not trimmed",
			payload: `{"lines":[{"label":"  con espacios  ","unit_price":null}]}`,
			want:    []PendingPriceLine{{Index: 0, Label: "  con espacios  "}},
		},
		{
			name:    "label keeps non ascii and the no-break space",
			payload: `{"lines":[{"label":"ñandú 🎂\u00a0x","unit_price":null}]}`,
			want:    []PendingPriceLine{{Index: 0, Label: "ñandú \U0001F382\u00a0x"}},
		},

		// Las líneas que no son objetos se saltan y siguen ocupando su posición.
		{
			name:    "non object lines keep their position",
			payload: `{"lines":[7,null,"x",[1],{"label":"D","unit_price":null},{"label":"E","unit_price":1},{"label":"F"}]}`,
			want:    []PendingPriceLine{{Index: 4, Label: "D"}, {Index: 6, Label: "F"}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			payload := json.RawMessage(c.payload)
			got := LinesWithoutPrice(payload)
			if c.want == nil {
				if got != nil {
					t.Errorf("LinesWithoutPrice(%s) = %+v, quería nil", c.payload, got)
				}
				return
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("LinesWithoutPrice(%s) = %+v, quería %+v", c.payload, got, c.want)
			}
			if string(payload) != c.payload {
				t.Error("LinesWithoutPrice mutó el payload de entrada")
			}
		})
	}
}

// svcRetotaledStore es un store cuya transición devuelve la solicitud con OTRO total: el caso en
// que el total cambia en la propia escritura. Es lo único que separa «el total de la solicitud ya
// transicionada» de «el total que se leyó antes». Su lectura dice además que hay datos del
// comprador, para poder exigir que el detalle devuelto lo conserve.
type svcRetotaledStore struct {
	*MemoryStore
	total float64
}

func (s *svcRetotaledStore) Get(ctx context.Context, tenantID, intakeID string) (Detail, error) {
	d, err := s.MemoryStore.Get(ctx, tenantID, intakeID)
	d.BuyerDataPresent = true
	return d, err
}

func (s *svcRetotaledStore) UpdateStatus(ctx context.Context, tenantID, intakeID, to string, expected []string) (Intake, error) {
	in, err := s.MemoryStore.UpdateStatus(ctx, tenantID, intakeID, to, expected)
	in.Total = s.total
	return in, err
}

// TestApprove_RevisionCarriesTheTransitionedTotal: la foto `approved` lleva el total de la
// solicitud YA transicionada (el que devuelve la escritura ganadora), no el de la lectura previa.
// Y el detalle devuelto conserva BuyerDataPresent tal como se leyó.
func TestApprove_RevisionCarriesTheTransitionedTotal(t *testing.T) {
	t.Parallel()
	st := &svcRetotaledStore{MemoryStore: svcSeedStore(t, StatusPendingApproval), total: 99}
	svc := NewService(st, WithQuoteSender(&svcQuoteSpy{trace: &svcTrace{}}))

	detail, err := svc.Approve(context.Background(), svcTenantA, svcIntakeID, "cotiza")
	if err != nil {
		t.Fatalf("Approve devolvió el error %v", err)
	}
	if !detail.BuyerDataPresent {
		t.Error("el detalle perdió BuyerDataPresent: se compone con lo leído, no se recalcula")
	}
	if detail.Total != 99 {
		t.Errorf("total devuelto = %v, quería 99 (el de la solicitud transicionada)", detail.Total)
	}
	last, ok := LastRevision(st.Revisions(svcIntakeID))
	if !ok || last.Kind != RevisionKindApproved {
		t.Fatalf("última revisión = %+v, quería la approved", last)
	}
	var payload struct {
		Total float64 `json:"total"`
	}
	if err := json.Unmarshal(last.Payload, &payload); err != nil || payload.Total != 99 {
		t.Errorf("total de la revisión = %v (err %v), quería 99", payload.Total, err)
	}
}
