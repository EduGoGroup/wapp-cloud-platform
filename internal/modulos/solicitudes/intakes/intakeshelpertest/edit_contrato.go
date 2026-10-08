package intakeshelpertest

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

// ownerShipping es la línea de envío que el dueño ya precificó a mano.
func ownerShipping() intakes.Item { return shippingLine(intakes.ShippingPendingLabel, 4500) }

// editedLines son las líneas que manda el dueño en una edición (sin fecha: la pone el store).
func editedLines() []intakes.Item {
	return []intakes.Item{
		{SKU: "hamb", Label: "Hamburguesa", Customization: "con queso extra", Qty: 2, UnitPrice: 8},
		{SKU: "beb", Label: "Bebida", Qty: 1, UnitPrice: 1.5},
	}
}

// revisionLines congela líneas en la forma del payload de una revisión.
func revisionLines(items []intakes.Item) []intakes.RevisionLine {
	out := make([]intakes.RevisionLine, 0, len(items))
	for _, it := range items {
		out = append(out, intakes.RevisionLine{SKU: it.SKU, Label: it.Label, Qty: it.Qty, UnitPrice: it.UnitPrice})
	}
	return out
}

// replaceItems edita o falla el test; devuelve el detalle que el puerto devolvió.
func replaceItems(t *testing.T, m Montaje, r ref, items []intakes.Item, expected []string, mode intakes.EditMode) intakes.Detail {
	t.Helper()
	got, err := m.Store.ReplaceItems(bg(), r.tenant, r.id, items, expected, mode)
	if err != nil {
		t.Fatalf("ReplaceItems(%s): error inesperado %v", r.id, err)
	}
	return got
}

// requireReturned afirma que el detalle que devolvió la escritura es el que después se lee.
func requireReturned(t *testing.T, what string, returned intakes.Detail, read snapshot) {
	t.Helper()
	requireSame(t, "lo devuelto por "+what, snapshot{detail: returned, stored: read.stored, event: read.event}, read)
}

// requireCorrectedPayload afirma que el payload de la revisión `corrected` es la foto de lo
// persistido, con esa señal.
func requireCorrectedPayload(t *testing.T, what string, rev intakes.Revision, total float64, lines []intakes.Item, signal intakes.CorrectionSignal) {
	t.Helper()
	want, err := intakes.CorrectedRevisionPayload(total, revisionLines(lines), signal)
	if err != nil {
		t.Fatalf("armando el payload esperado: %v", err)
	}
	if !sameJSON(rev.Payload, want) {
		t.Errorf("%s: payload de la revisión = %s, quería %s", what, rev.Payload, want)
	}
}

// caseReplaceItems: la edición sustituye las líneas del cliente, deja la de envío INTACTA y
// delante, recalcula el total entero, refresca UpdatedAt y escribe UNA revisión `corrected` de
// `owner` con la foto de lo persistido. Con EditPlain la revisión no lleva ninguna señal.
func caseReplaceItems(t *testing.T, m Montaje) {
	w := seedWitnesses(t, m)
	lines := append(customerLines(), ownerShipping())
	r := seed(t, m, m.TenantA, intakes.StatusPendingApproval, 1, eventCancelled, lines...)
	insertRevision(t, m, r.id, intakes.RevisionKindCart, intakes.RevisionBySystem, "")
	want := take(t, m, r).clone()
	m.Advance(t)

	returned := replaceItems(t, m, r, editedLines(), intakes.StoredVariants(intakes.StatusPendingApproval), intakes.EditPlain)
	got := take(t, m, r)
	want.detail.Items = append([]intakes.Item{ownerShipping()}, editedLines()...)
	want.detail.Total = 4500 + 16 + 1.5
	adoptAddedAt(t, "ReplaceItems", got, &want, 1)
	adoptRefreshedUpdatedAt(t, "ReplaceItems", got, &want)
	rev := adoptNewRevision(t, "ReplaceItems", got, &want, intakes.RevisionKindCorrected, intakes.RevisionByOwner, "")
	requireSame(t, "tras ReplaceItems", got, want)
	requireReturned(t, "ReplaceItems", returned, got)
	requireCorrectedPayload(t, "EditPlain", rev, want.detail.Total, want.detail.Items, intakes.CorrectionSignal{})
	payload := decode(t, rev.Payload)
	for _, key := range []string{intakes.KeyAsCorrection, intakes.KeyCorrectsRevisionNo, intakes.KeyCorrectsKind} {
		if _, present := payload[key]; present {
			t.Errorf("EditPlain: la revisión lleva la clave %q; sin corrección declarada no hay señal", key)
		}
	}
	w.requireUntouched(t, m)
}

// caseReplaceItemsAsCorrection: la corrección declarada deja escrito CUÁL era el borrador que
// corrigió: el número y la clase de la revisión más alta que había antes de escribir ésta.
func caseReplaceItemsAsCorrection(t *testing.T, m Montaje) {
	w := seedWitnesses(t, m)
	expected := intakes.StoredVariants(intakes.StatusPendingApproval)
	r := seed(t, m, m.TenantA, intakes.StatusPendingApproval, 1, eventCancelled, customerLines()...)
	insertRevision(t, m, r.id, intakes.RevisionKindCart, intakes.RevisionBySystem, "")
	insertRevision(t, m, r.id, intakes.RevisionKindInterpreted, intakes.RevisionBySystem, "")

	for _, c := range []struct {
		name   string
		signal intakes.CorrectionSignal
	}{
		{"primera corrección", intakes.CorrectionSignal{AsCorrection: true, CorrectsRevisionNo: 2, CorrectsKind: intakes.RevisionKindInterpreted}},
		{"segunda pasada", intakes.CorrectionSignal{AsCorrection: true, CorrectsRevisionNo: 3, CorrectsKind: intakes.RevisionKindCorrected}},
	} {
		want := take(t, m, r).clone()
		m.Advance(t)
		returned := replaceItems(t, m, r, editedLines(), expected, intakes.EditAsCorrection)
		got := take(t, m, r)
		want.detail.Items, want.detail.Total = editedLines(), 17.5
		adoptAddedAt(t, c.name, got, &want, 0)
		adoptRefreshedUpdatedAt(t, c.name, got, &want)
		rev := adoptNewRevision(t, c.name, got, &want, intakes.RevisionKindCorrected, intakes.RevisionByOwner, "")
		requireSame(t, c.name, got, want)
		requireReturned(t, c.name, returned, got)
		requireCorrectedPayload(t, c.name, rev, 17.5, want.detail.Items, c.signal)
	}

	// Sin ninguna revisión previa la marca va sola: no hay borrador que señalar.
	bare := seed(t, m, m.TenantA, intakes.StatusPendingApproval, 2, eventCancelled)
	replaceItems(t, m, bare, editedLines(), expected, intakes.EditAsCorrection)
	revs := get(t, m, bare).Revisions
	if len(revs) != 1 {
		t.Fatalf("la solicitud sin revisiones quedó con %d, quería 1", len(revs))
	}
	requireCorrectedPayload(t, "sin revisión previa", revs[0], 17.5, editedLines(), intakes.CorrectionSignal{AsCorrection: true})
	w.requireUntouched(t, m)
}

// caseReplaceItemsRepeated: repetir la edición deja SIEMPRE una sola línea de envío, la misma —con
// el precio que el dueño le puso y su fecha—, y vaciar las líneas del cliente la deja a ella sola.
func caseReplaceItemsRepeated(t *testing.T, m Montaje) {
	w := seedWitnesses(t, m)
	expected := intakes.StoredVariants(intakes.StatusPendingApproval)
	r := seed(t, m, m.TenantA, intakes.StatusPendingApproval, 1, eventCancelled, append(customerLines(), ownerShipping())...)
	want := take(t, m, r).clone()

	for i, items := range [][]intakes.Item{editedLines(), editedLines()[:1], {}, editedLines()} {
		m.Advance(t)
		replaceItems(t, m, r, items, expected, intakes.EditPlain)
		got := take(t, m, r)
		want.detail.Items = append([]intakes.Item{ownerShipping()}, items...)
		want.detail.Total = totalOf(want.detail.Items)
		adoptAddedAt(t, "edición repetida", got, &want, 1)
		adoptRefreshedUpdatedAt(t, "edición repetida", got, &want)
		adoptNewRevision(t, "edición repetida", got, &want, intakes.RevisionKindCorrected, intakes.RevisionByOwner, "")
		requireSame(t, "edición repetida", got, want)
		if n := len(got.detail.Revisions); n != i+1 {
			t.Errorf("tras %d ediciones hay %d revisiones", i+1, n)
		}
	}
	w.requireUntouched(t, m)
}

// caseReplaceItemsRejected: si la solicitud ya no está en el estado esperado o no es del tenant,
// no se escribe NADA: ni líneas, ni total, ni revisión.
func caseReplaceItemsRejected(t *testing.T, m Montaje) {
	w := seedWitnesses(t, m)
	r := seed(t, m, m.TenantA, intakes.StatusOpen, 1, eventCancelled, append(customerLines(), ownerShipping())...)
	before := take(t, m, r)
	m.Advance(t)
	expected := intakes.StoredVariants(intakes.StatusPendingApproval) // el estado de los testigos

	got, err := m.Store.ReplaceItems(bg(), r.tenant, r.id, editedLines(), expected, intakes.EditAsCorrection)
	if !errors.Is(err, intakes.ErrConflict) || got.ID != "" {
		t.Errorf("ReplaceItems fuera de estado: (%+v, %v), quería un detalle a cero y ErrConflict", got, err)
	}
	requireSame(t, "tras el conflicto", take(t, m, r), before)

	for name, id := range map[string]string{
		"de otro tenant": w.foreign().id, "inexistente": uuid.NewString(), "id que no es UUID": "no-soy-un-uuid",
	} {
		got, err := m.Store.ReplaceItems(bg(), m.TenantA, id, editedLines(), expected, intakes.EditPlain)
		if !errors.Is(err, intakes.ErrNotFound) || got.ID != "" {
			t.Errorf("ReplaceItems de una solicitud %s: (%+v, %v), quería un detalle a cero y ErrNotFound", name, got, err)
		}
	}
	w.requireUntouched(t, m)
}

// martaRevalidation es el diff de la escena de los tests viejos: se re-precia el pan (que además
// cambia de etiqueta), se retira el queso y la leche no cambia.
func martaRevalidation() intakes.Revalidation {
	return intakes.Revalidation{
		Items: []intakes.Item{
			{SKU: "pan", Label: "Pan integral", Customization: "sin sal", Qty: 2, UnitPrice: 2.5, AddedAt: lineAt(0)},
			{SKU: "leche", Label: "Leche", Customization: "tibia", Qty: 1, UnitPrice: 1, AddedAt: lineAt(2)},
		},
		Changes: []intakes.LineChange{
			{SKU: "pan", Label: "Pan integral", Qty: 2, From: 2, To: 2.5},
			{SKU: "queso", Label: "Queso", Qty: 1, From: 3, Removed: true},
		},
		TotalBefore: 8,
		TotalAfter:  6,
	}
}

// requireRevalidatedPayload afirma que el payload de la revisión `revalidated` es el del diff.
func requireRevalidatedPayload(t *testing.T, rev intakes.Revision, rv intakes.Revalidation) {
	t.Helper()
	want, err := intakes.RevalidatedRevisionPayload(rv)
	if err != nil {
		t.Fatalf("armando el payload esperado: %v", err)
	}
	if !sameJSON(rev.Payload, want) {
		t.Errorf("payload de la revisión revalidated = %s, quería %s", rev.Payload, want)
	}
}

// caseApplyRevalidation: la escritura es QUIRÚRGICA. La línea re-preciada cambia de etiqueta y de
// precio y de nada más; la retirada desaparece; las que quedan conservan su orden y su AddedAt (al
// cliente no se le reordena el pedido); el total se cuadra; el estado NO se toca; y queda UNA
// revisión `revalidated` de `system` con el texto exacto que se le mandó.
func caseApplyRevalidation(t *testing.T, m Montaje) {
	w := seedWitnesses(t, m)
	r := seed(t, m, m.TenantA, intakes.StatusOpen, 1, eventCancelled, customerLines()...)
	insertRevision(t, m, r.id, intakes.RevisionKindCart, intakes.RevisionBySystem, "")
	want := take(t, m, r).clone()
	m.Advance(t)

	const text = "Hola, tu pedido cambió: el pan ahora cuesta 2,5 y el queso ya no está."
	rv := martaRevalidation()
	returned, err := m.Store.ApplyRevalidation(bg(), r.tenant, r.id, rv, text, intakes.StoredVariants(intakes.StatusOpen))
	if err != nil {
		t.Fatalf("ApplyRevalidation: error inesperado %v", err)
	}
	got := take(t, m, r)
	want.detail.Items = rv.Items
	want.detail.Total = 6
	adoptRefreshedUpdatedAt(t, "ApplyRevalidation", got, &want)
	rev := adoptNewRevision(t, "ApplyRevalidation", got, &want, intakes.RevisionKindRevalidated, intakes.RevisionBySystem, text)
	requireSame(t, "tras ApplyRevalidation", got, want)
	requireReturned(t, "ApplyRevalidation", returned, got)
	requireRevalidatedPayload(t, rev, rv)
	w.requireUntouched(t, m)
}

// caseApplyRevalidationPlatformLines: la línea de la plataforma sobrevive con el precio que le
// puso el dueño aunque el catálogo se lleve TODAS las del cliente y aunque un cambio la nombrara.
// Y la solicitud se queda en su estado aunque no le quede ninguna línea de cliente.
func caseApplyRevalidationPlatformLines(t *testing.T, m Montaje) {
	w := seedWitnesses(t, m)
	r := seed(t, m, m.TenantA, intakes.StatusOpen, 1, eventCancelled, append(customerLines(), ownerShipping())...)
	want := take(t, m, r).clone()
	m.Advance(t)

	rv := intakes.Revalidation{
		Items: []intakes.Item{ownerShipping()},
		Changes: []intakes.LineChange{
			{SKU: "pan", Label: "Pan", Qty: 2, From: 2, Removed: true},
			{SKU: "queso", Label: "Queso", Qty: 1, From: 3, Removed: true},
			{SKU: "leche", Label: "Leche", Qty: 1, From: 1, Removed: true},
			{SKU: intakes.ShippingSKU, Label: intakes.ShippingPendingLabel, Qty: 1, From: 4500, Removed: true},
		},
		TotalBefore: 4508,
		TotalAfter:  4500,
	}
	const text = "Lo sentimos: ya no tenemos nada de lo que pediste."
	returned, err := m.Store.ApplyRevalidation(bg(), r.tenant, r.id, rv, text, intakes.StoredVariants(intakes.StatusOpen))
	if err != nil {
		t.Fatalf("ApplyRevalidation: error inesperado %v", err)
	}
	got := take(t, m, r)
	want.detail.Items = []intakes.Item{ownerShipping()}
	want.detail.Total = 4500
	adoptRefreshedUpdatedAt(t, "ApplyRevalidation", got, &want)
	adoptNewRevision(t, "ApplyRevalidation", got, &want, intakes.RevisionKindRevalidated, intakes.RevisionBySystem, text)
	requireSame(t, "tras retirar todas las líneas del cliente", got, want)
	requireReturned(t, "ApplyRevalidation", returned, got)

	// El re-precio tampoco la alcanza.
	rv = intakes.Revalidation{
		Items:       []intakes.Item{ownerShipping()},
		Changes:     []intakes.LineChange{{SKU: intakes.ShippingSKU, Label: "Envío gratis", Qty: 1, From: 4500, To: 0}},
		TotalBefore: 4500, TotalAfter: 4500,
	}
	m.Advance(t)
	if _, err := m.Store.ApplyRevalidation(bg(), r.tenant, r.id, rv, text, intakes.StoredVariants(intakes.StatusOpen)); err != nil {
		t.Fatalf("ApplyRevalidation con un re-precio del envío: error inesperado %v", err)
	}
	got = take(t, m, r)
	adoptRefreshedUpdatedAt(t, "el re-precio del envío", got, &want)
	adoptNewRevision(t, "el re-precio del envío", got, &want, intakes.RevisionKindRevalidated, intakes.RevisionBySystem, text)
	requireSame(t, "tras intentar re-preciar el envío", got, want)
	w.requireUntouched(t, m)
}

// caseApplyRevalidationRejected: si alguien movió la solicitud entre el cálculo del diff y la
// escritura, o no es del tenant, no se escribe NADA.
func caseApplyRevalidationRejected(t *testing.T, m Montaje) {
	w := seedWitnesses(t, m)
	r := seed(t, m, m.TenantA, intakes.StatusClosedLegacy, 1, eventCancelled, customerLines()...)
	before := take(t, m, r)
	m.Advance(t)
	rv := martaRevalidation()

	got, err := m.Store.ApplyRevalidation(bg(), r.tenant, r.id, rv, "texto", intakes.StoredVariants(intakes.StatusOpen))
	if !errors.Is(err, intakes.ErrConflict) || got.ID != "" {
		t.Errorf("ApplyRevalidation fuera de estado: (%+v, %v), quería un detalle a cero y ErrConflict", got, err)
	}
	requireSame(t, "tras el conflicto", take(t, m, r), before)

	expected := intakes.StoredVariants(intakes.StatusPendingApproval) // el estado de los testigos
	for name, id := range map[string]string{
		"de otro tenant": w.foreign().id, "inexistente": uuid.NewString(), "id que no es UUID": "no-soy-un-uuid",
	} {
		got, err := m.Store.ApplyRevalidation(bg(), m.TenantA, id, rv, "texto", expected)
		if !errors.Is(err, intakes.ErrNotFound) || got.ID != "" {
			t.Errorf("ApplyRevalidation de una solicitud %s: (%+v, %v), quería un detalle a cero y ErrNotFound", name, got, err)
		}
	}
	w.requireUntouched(t, m)
}
