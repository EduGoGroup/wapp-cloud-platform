//go:build pendiente

package intakes

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

// Los textos y los defectos esperados de este fichero son LITERALES calculados con
// el fichero viejo (internal/intakes/edit.go @ 64c181a): el candado de fronteras
// impide importarlo desde aquí, así que la equivalencia viejo ↔ nuevo se fija con lo
// que el viejo devolvió para este mismo corpus, casos adversarios incluidos. Las
// runas invisibles van con su escape numérico, nunca pegadas en el fuente.

const (
	editTenant = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	editIntake = "33333333-3333-3333-3333-333333333333"
)

// El cero de EditMode es EditPlain: el valor por descuido es la edición que ya
// existía, nunca la que estrena conducta.
func TestEditMode_ZeroValueIsPlain(t *testing.T) {
	var mode EditMode
	if mode != EditPlain {
		t.Fatalf("el cero de EditMode es %d; se esperaba EditPlain (%d)", mode, EditPlain)
	}
	if EditAsCorrection == EditPlain {
		t.Fatal("EditAsCorrection y EditPlain son el mismo valor; deben distinguirse")
	}
}

// TestEditConstants_Literals: el prefijo reservado, la cota y el estado editable. El prefijo
// tiene que cubrir la línea de envío: si ShippingSKU dejara de empezar por él, la edición
// manual podría borrar el envío del pedido sin que nadie lo notara.
func TestEditConstants_Literals(t *testing.T) {
	t.Parallel()
	if ReservedSKUPrefix != "_" {
		t.Errorf("ReservedSKUPrefix = %q, quería %q", ReservedSKUPrefix, "_")
	}
	if !strings.HasPrefix(ShippingSKU, ReservedSKUPrefix) {
		t.Errorf("ShippingSKU = %q no empieza por el prefijo reservado %q", ShippingSKU, ReservedSKUPrefix)
	}
	if MaxEditableItems != 200 {
		t.Errorf("MaxEditableItems = %d, quería 200", MaxEditableItems)
	}
	if EditableStatus != "pending_approval" {
		t.Errorf("EditableStatus = %q, quería pending_approval", EditableStatus)
	}
	if EditableStatus != StatusPendingApproval {
		t.Errorf("EditableStatus = %q ya no es StatusPendingApproval (%q)", EditableStatus, StatusPendingApproval)
	}
}

// TestInvalidItemsError_Text: cuenta DEFECTOS y no pluraliza. Se recoge como puntero.
func TestInvalidItemsError_Text(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		defects int
		want    string
	}{
		{name: "three defects", defects: 3, want: "la edición tiene 3 líneas inválidas"},
		{name: "one defect is not singularized", defects: 1, want: "la edición tiene 1 líneas inválidas"},
		{name: "no defects", defects: 0, want: "la edición tiene 0 líneas inválidas"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			var err error = &InvalidItemsError{Defects: make([]LineDefect, c.defects)}
			if got := err.Error(); got != c.want {
				t.Errorf("Error() = %q, quería %q", got, c.want)
			}
			var invalid *InvalidItemsError
			if !errors.As(err, &invalid) {
				t.Fatalf("errors.As no recoge un *InvalidItemsError: %v", err)
			}
		})
	}
}

// TestTooManyItemsError_Text: el texto lleva lo que llegó y el máximo, tal cual.
func TestTooManyItemsError_Text(t *testing.T) {
	t.Parallel()
	cases := []struct {
		count, max int
		want       string
	}{
		{count: 201, max: 200, want: "la edición trae 201 líneas y el máximo es 200"},
		{count: -1, max: 0, want: "la edición trae -1 líneas y el máximo es 0"},
	}
	for _, c := range cases {
		if got := (&TooManyItemsError{Count: c.count, Max: c.max}).Error(); got != c.want {
			t.Errorf("Error() = %q, quería %q", got, c.want)
		}
	}
}

// TestNotEditableError_Text: el estado va entrecomillado con %q, así que comillas y saltos
// salen escapados.
func TestNotEditableError_Text(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		status string
		want   string
	}{
		{name: "known status", status: "confirmed", want: `una solicitud en "confirmed" no se puede editar a mano`},
		{name: "empty status", status: "", want: `una solicitud en "" no se puede editar a mano`},
		{name: "quotes and newline are escaped", status: "a\"b\nñ", want: `una solicitud en "a\"b\nñ" no se puede editar a mano`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := (&NotEditableError{Status: c.status}).Error(); got != c.want {
				t.Errorf("Error() = %q, quería %q", got, c.want)
			}
		})
	}
}

// TestLineDefect_WireShape: las tres etiquetas JSON son contrato (el defecto viaja al 400).
func TestLineDefect_WireShape(t *testing.T) {
	t.Parallel()
	raw, err := json.Marshal(LineDefect{Index: 2, Field: "qty", Message: "m"})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if want := `{"index":2,"field":"qty","message":"m"}`; string(raw) != want {
		t.Errorf("JSON = %s, quería %s", raw, want)
	}
}

// TestValidateEditableItems_Accepts: lo que SÍ pasa. La lista vacía (quitar la última línea
// es una edición legítima), dos líneas con el mismo sku (D-041.20) y justo la cota.
func TestValidateEditableItems_Accepts(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		items []Item
	}{
		{name: "nil list", items: nil},
		{name: "empty list", items: []Item{}},
		{name: "two lines sharing a sku", items: []Item{{SKU: "a", Label: "x", Qty: 1}, {SKU: "a", Label: "x", Qty: 1}}},
		{name: "exactly the bound", items: editLines(MaxEditableItems, true)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if err := ValidateEditableItems(c.items); err != nil {
				t.Errorf("ValidateEditableItems = %v, quería nil", err)
			}
		})
	}
}

// TestValidateEditableItems_BoundGoesFirst: una más que la cota se rechaza entera SIN mirar
// las líneas (aunque todas estén mal, el error es el de la cota); justo en la cota, las
// líneas malas sí se miran y cada una aporta sus tres defectos.
func TestValidateEditableItems_BoundGoesFirst(t *testing.T) {
	t.Parallel()
	for _, valid := range []bool{true, false} {
		err := ValidateEditableItems(editLines(MaxEditableItems+1, valid))
		var tooMany *TooManyItemsError
		if !errors.As(err, &tooMany) {
			t.Fatalf("con 201 líneas (válidas=%v) el error es %v, quería un *TooManyItemsError", valid, err)
		}
		if tooMany.Count != 201 || tooMany.Max != 200 {
			t.Errorf("el error lleva (%d, %d), quería (201, 200)", tooMany.Count, tooMany.Max)
		}
	}

	err := ValidateEditableItems(editLines(MaxEditableItems, false))
	var invalid *InvalidItemsError
	if !errors.As(err, &invalid) {
		t.Fatalf("con 200 líneas vacías el error es %v, quería un *InvalidItemsError", err)
	}
	if len(invalid.Defects) != 600 {
		t.Errorf("defectos = %d, quería 600 (sku, label y qty de cada una de las 200)", len(invalid.Defects))
	}
	if got, want := err.Error(), "la edición tiene 600 líneas inválidas"; got != want {
		t.Errorf("Error() = %q, quería %q", got, want)
	}
}

// editLines devuelve n líneas: válidas, o en su valor cero (sin sku, sin etiqueta, qty 0).
func editLines(n int, valid bool) []Item {
	out := make([]Item, n)
	if valid {
		for i := range out {
			out[i] = Item{SKU: "a", Label: "x", Qty: 1}
		}
	}
	return out
}

// --- Service.ReplaceItems ---------------------------------------------------

// editStore es un Store que solo sabe leer y reemplazar líneas, y apunta el orden y los
// argumentos de lo que se le pide. Embebe el puerto para no tener que escribir los métodos
// que ReplaceItems no puede tocar: si los tocara, el puntero nil lo delataría.
type editStore struct {
	Store

	current Detail // lo que devuelve Get
	getErr  error
	result  Detail // lo que devuelve ReplaceItems
	putErr  error

	calls       []string // "get" / "replace", en orden
	gotTenant   string
	gotIntake   string
	gotItems    []Item
	gotExpected []string
	gotMode     EditMode
}

func (s *editStore) Get(_ context.Context, tenantID, intakeID string) (Detail, error) {
	s.calls = append(s.calls, "get")
	s.gotTenant, s.gotIntake = tenantID, intakeID
	return s.current, s.getErr
}

func (s *editStore) ReplaceItems(_ context.Context, tenantID, intakeID string, items []Item, expected []string, mode EditMode) (Detail, error) {
	s.calls = append(s.calls, "replace")
	s.gotTenant, s.gotIntake = tenantID, intakeID
	s.gotItems, s.gotExpected, s.gotMode = items, expected, mode
	return s.result, s.putErr
}

// editCRMSpy apunta los empujes al CRM.
type editCRMSpy struct {
	tenants   []string
	details   []Detail
	revisions []int
}

func (s *editCRMSpy) PushRevision(_ context.Context, tenantID string, d Detail, revisionNo int) {
	s.tenants = append(s.tenants, tenantID)
	s.details = append(s.details, d)
	s.revisions = append(s.revisions, revisionNo)
}

// editMetricSpy apunta las métricas publicadas.
type editMetricSpy struct {
	names    []string
	contacts []string
}

func (s *editMetricSpy) PublishMetric(_ context.Context, _, contactID, name string, _ map[string]any) error {
	s.names = append(s.names, name)
	s.contacts = append(s.contacts, contactID)
	return nil
}

// editPendingStore devuelve un store con la solicitud por aprobar y un resultado de
// escritura con las revisiones dadas.
func editPendingStore(revisionNos ...int) *editStore {
	head := Intake{ID: editIntake, ContactID: "contacto-opaco-1", Status: StatusPendingApproval, Total: 8}
	result := Detail{Intake: head, Items: []Item{{SKU: "burger", Label: "Hamburguesa", Qty: 1, UnitPrice: 9}}}
	result.Total = 9
	for _, no := range revisionNos {
		result.Revisions = append(result.Revisions, Revision{IntakeID: editIntake, RevisionNo: no, Kind: RevisionKindCorrected})
	}
	return &editStore{
		current: Detail{Intake: head, Items: []Item{{SKU: "burger", Label: "Hamburguesa", Qty: 1, UnitPrice: 8}}},
		result:  result,
	}
}

func editOwnerLines() []Item {
	return []Item{
		{SKU: "burger", Label: "Hamburguesa", Qty: 1, UnitPrice: 8},
		{SKU: "queso", Label: "Queso extra", Qty: 1, UnitPrice: 1},
	}
}

// TestReplaceItems_WritesWhatTheOwnerSent: el camino entero. Lee, escribe con las líneas tal
// cual, el modo tal cual y los estados esperados de EditableStatus, y devuelve el detalle del
// store.
func TestReplaceItems_WritesWhatTheOwnerSent(t *testing.T) {
	t.Parallel()
	for _, mode := range []EditMode{EditPlain, EditAsCorrection} {
		st := editPendingStore(1)
		lines := editOwnerLines()

		got, err := NewService(st).ReplaceItems(context.Background(), editTenant, editIntake, lines, mode)
		if err != nil {
			t.Fatalf("ReplaceItems(modo %d) = %v, quería nil", mode, err)
		}
		if !slices.Equal(st.calls, []string{"get", "replace"}) {
			t.Errorf("llamadas al store = %v, quería [get replace]", st.calls)
		}
		if st.gotTenant != editTenant || st.gotIntake != editIntake {
			t.Errorf("el store recibió (%q, %q), quería el tenant y la solicitud pedidos", st.gotTenant, st.gotIntake)
		}
		if !slices.Equal(st.gotItems, lines) {
			t.Errorf("líneas escritas = %+v, quería las del dueño %+v", st.gotItems, lines)
		}
		if !slices.Equal(st.gotExpected, []string{"pending_approval"}) || !slices.Equal(st.gotExpected, StoredVariants(EditableStatus)) {
			t.Errorf("estados esperados = %v, quería [pending_approval]", st.gotExpected)
		}
		if st.gotMode != mode {
			t.Errorf("modo escrito = %d, quería %d", st.gotMode, mode)
		}
		if got.ID != editIntake || got.Total != 9 || len(got.Revisions) != 1 {
			t.Errorf("detalle devuelto = %+v, quería el que devolvió el store", got)
		}
	}
}

// TestReplaceItems_InvalidLinesTouchNothing: la validación va primero. Con líneas malas no
// se lee ni se escribe, y el error es el de la validación.
func TestReplaceItems_InvalidLinesTouchNothing(t *testing.T) {
	t.Parallel()
	st := editPendingStore(1)
	svc := NewService(st)

	_, err := svc.ReplaceItems(context.Background(), editTenant, editIntake, []Item{{SKU: ShippingSKU, Label: "Envío", Qty: 1}}, EditPlain)
	var invalid *InvalidItemsError
	if !errors.As(err, &invalid) {
		t.Fatalf("con un sku reservado el error es %v, quería un *InvalidItemsError", err)
	}
	_, err = svc.ReplaceItems(context.Background(), editTenant, editIntake, editLines(MaxEditableItems+1, true), EditPlain)
	var tooMany *TooManyItemsError
	if !errors.As(err, &tooMany) {
		t.Fatalf("con 201 líneas el error es %v, quería un *TooManyItemsError", err)
	}
	if len(st.calls) != 0 {
		t.Errorf("llamadas al store = %v, quería ninguna", st.calls)
	}
}

// TestReplaceItems_ResourceBeforeState: una solicitud ajena o inexistente responde
// ErrNotFound y no se escribe.
func TestReplaceItems_ResourceBeforeState(t *testing.T) {
	t.Parallel()
	st := &editStore{getErr: ErrNotFound}
	_, err := NewService(st).ReplaceItems(context.Background(), editTenant, editIntake, editOwnerLines(), EditPlain)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("ReplaceItems = %v, quería ErrNotFound", err)
	}
	if !slices.Equal(st.calls, []string{"get"}) {
		t.Errorf("llamadas al store = %v, quería solo [get]", st.calls)
	}
}

// TestReplaceItems_OnlyFromPendingApproval: fuera de `pending_approval` no se escribe, y el
// error lleva el estado actual YA normalizado (la clave legada `closed` sale `confirmed`).
// El modo no amplía los estados editables: `needs_info` se rechaza igual con la corrección.
func TestReplaceItems_OnlyFromPendingApproval(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		status string
		mode   EditMode
		want   string
	}{
		{name: "confirmed", status: StatusConfirmed, mode: EditPlain, want: "confirmed"},
		{name: "legacy closed is reported normalized", status: "closed", mode: EditPlain, want: "confirmed"},
		{name: "needs info with a plain edit", status: StatusNeedsInfo, mode: EditPlain, want: "needs_info"},
		{name: "needs info is not opened by the correction", status: StatusNeedsInfo, mode: EditAsCorrection, want: "needs_info"},
		{name: "cancelled", status: StatusCancelled, mode: EditAsCorrection, want: "cancelled"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			st := editPendingStore(1)
			st.current.Status = c.status
			crm := &editCRMSpy{}

			_, err := NewService(st, WithCRMPusher(crm)).ReplaceItems(context.Background(), editTenant, editIntake, editOwnerLines(), c.mode)
			var notEditable *NotEditableError
			if !errors.As(err, &notEditable) {
				t.Fatalf("ReplaceItems = %v, quería un *NotEditableError", err)
			}
			if notEditable.Status != c.want {
				t.Errorf("el error lleva el estado %q, quería %q", notEditable.Status, c.want)
			}
			if !slices.Equal(st.calls, []string{"get"}) {
				t.Errorf("llamadas al store = %v, quería solo [get]: no se escribe", st.calls)
			}
			if len(crm.revisions) != 0 {
				t.Errorf("empujes al CRM = %d, quería 0", len(crm.revisions))
			}
		})
	}
}

// TestReplaceItems_StoreErrorIsReturnedUntouched: ErrConflict (alguien la movió entre la
// lectura y la escritura) y cualquier fallo de infraestructura vuelven intactos, y sin
// escritura no hay empuje ni métrica.
func TestReplaceItems_StoreErrorIsReturnedUntouched(t *testing.T) {
	t.Parallel()
	infra := errors.New("pool agotado")
	for _, storeErr := range []error{ErrConflict, infra} {
		st := editPendingStore(1)
		st.putErr = storeErr
		crm, metrics := &editCRMSpy{}, &editMetricSpy{}

		_, err := NewService(st, WithCRMPusher(crm), WithMetrics(metrics, nil)).
			ReplaceItems(context.Background(), editTenant, editIntake, editOwnerLines(), EditAsCorrection)
		if !errors.Is(err, storeErr) {
			t.Fatalf("ReplaceItems = %v, quería %v", err, storeErr)
		}
		if len(crm.revisions) != 0 || len(metrics.names) != 0 {
			t.Errorf("sin escritura hubo %d empujes y %d métricas, quería 0 y 0", len(crm.revisions), len(metrics.names))
		}
	}
}

// TestReplaceItems_PushHangsFromTheRevisionNotTheMode: el empuje al CRM sale con el número
// de la revisión MÁS ALTA del detalle escrito, con el detalle escrito, y sale igual con los
// dos modos: un CRM que no se entere de una edición rutinaria se queda con un documento que
// ya no es verdad.
func TestReplaceItems_PushHangsFromTheRevisionNotTheMode(t *testing.T) {
	t.Parallel()
	for _, mode := range []EditMode{EditPlain, EditAsCorrection} {
		st := editPendingStore(3, 7, 5)
		crm := &editCRMSpy{}

		if _, err := NewService(st, WithCRMPusher(crm)).ReplaceItems(context.Background(), editTenant, editIntake, editOwnerLines(), mode); err != nil {
			t.Fatalf("ReplaceItems(modo %d) = %v, quería nil", mode, err)
		}
		if !slices.Equal(crm.revisions, []int{7}) {
			t.Fatalf("modo %d: revisiones empujadas = %v, quería [7]", mode, crm.revisions)
		}
		if crm.tenants[0] != editTenant || crm.details[0].Total != 9 || len(crm.details[0].Revisions) != 3 {
			t.Errorf("modo %d: se empujó (%q, %+v), quería el tenant y el detalle escrito", mode, crm.tenants[0], crm.details[0])
		}
	}
}

// TestReplaceItems_NoRevisionNoPush: si el detalle vuelve sin revisiones no se empuja (un
// push con revision_no 0 es lo único que el contrato del CRM rechaza); y sin puente cableado
// la edición funciona igual.
func TestReplaceItems_NoRevisionNoPush(t *testing.T) {
	t.Parallel()
	st := editPendingStore()
	crm := &editCRMSpy{}
	if _, err := NewService(st, WithCRMPusher(crm)).ReplaceItems(context.Background(), editTenant, editIntake, editOwnerLines(), EditPlain); err != nil {
		t.Fatalf("ReplaceItems = %v, quería nil", err)
	}
	if len(crm.revisions) != 0 {
		t.Errorf("empujes al CRM = %v, quería ninguno sin revisión", crm.revisions)
	}

	unwired := editPendingStore(1)
	if _, err := NewService(unwired).ReplaceItems(context.Background(), editTenant, editIntake, editOwnerLines(), EditPlain); err != nil {
		t.Fatalf("sin puente cableado ReplaceItems = %v, quería nil", err)
	}
}

// TestReplaceItems_PublishesTheCorrectionMetric: tras escribir sale UNA métrica de
// corrección, a nombre del contacto de la solicitud.
func TestReplaceItems_PublishesTheCorrectionMetric(t *testing.T) {
	t.Parallel()
	st := editPendingStore(1)
	metrics := &editMetricSpy{}

	if _, err := NewService(st, WithMetrics(metrics, nil)).ReplaceItems(context.Background(), editTenant, editIntake, editOwnerLines(), EditPlain); err != nil {
		t.Fatalf("ReplaceItems = %v, quería nil", err)
	}
	if !slices.Equal(metrics.names, []string{EventLineCorrected}) {
		t.Fatalf("métricas publicadas = %v, quería [%s]", metrics.names, EventLineCorrected)
	}
	if metrics.contacts[0] != "contacto-opaco-1" {
		t.Errorf("la métrica va a nombre de %q, quería el contacto de la solicitud", metrics.contacts[0])
	}
}
