package intakeshelpertest

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

// snapshot es la MARCA DE ESTADO de una solicitud: todo lo que una operación del puerto puede
// tocar, de una vez (hallazgo 35 de F1). Una marca que vigilara una sola columna dejaría pasar la
// escritura que acierta en esa y estropea la de al lado.
type snapshot struct {
	// detail es lo que da Get: la cabecera entera, las líneas, las revisiones y la presencia de
	// datos del comprador.
	detail intakes.Detail
	// stored es la clave de estado tal como está guardada (Get la da normalizada).
	stored string
	// event es el estado del evento que la solicitud declara.
	event string
}

// take saca la marca de estado de la solicitud.
func take(t *testing.T, m Montaje, r ref) snapshot {
	t.Helper()
	return snapshot{
		detail: get(t, m, r),
		stored: m.StoredStatus(t, r.tenant, r.id),
		event:  m.EventStatus(t, r.eventID),
	}
}

// clone copia la marca para que un caso derive de ella la que espera sin tocar la original.
func (s snapshot) clone() snapshot {
	out := s
	out.detail.Items = append([]intakes.Item(nil), s.detail.Items...)
	out.detail.Revisions = append([]intakes.Revision(nil), s.detail.Revisions...)
	return out
}

// requireSame afirma que got es la misma marca de estado que want: la fila ENTERA.
func requireSame(t *testing.T, what string, got, want snapshot) {
	t.Helper()
	requireSameHeader(t, what, got.detail.Intake, want.detail.Intake)
	if got.stored != want.stored {
		t.Errorf("%s: estado almacenado = %q, quería %q", what, got.stored, want.stored)
	}
	if got.event != want.event {
		t.Errorf("%s: estado de su evento = %q, quería %q", what, got.event, want.event)
	}
	if got.detail.BuyerDataPresent != want.detail.BuyerDataPresent {
		t.Errorf("%s: BuyerDataPresent = %v, quería %v", what, got.detail.BuyerDataPresent, want.detail.BuyerDataPresent)
	}
	requireSameItems(t, what, got.detail.Items, want.detail.Items)
	requireSameRevisions(t, what, got.detail.Revisions, want.detail.Revisions)
}

// requireSameHeader compara las once columnas de la cabecera que el puerto enseña.
func requireSameHeader(t *testing.T, what string, got, want intakes.Intake) {
	t.Helper()
	if got.ID != want.ID || got.ContactID != want.ContactID || got.SessionID != want.SessionID {
		t.Errorf("%s: (id, contacto, sesión) = (%q, %q, %q), quería (%q, %q, %q)", what,
			got.ID, got.ContactID, got.SessionID, want.ID, want.ContactID, want.SessionID)
	}
	if got.Status != want.Status {
		t.Errorf("%s: Status = %q, quería %q", what, got.Status, want.Status)
	}
	if got.Total != want.Total {
		t.Errorf("%s: Total = %v, quería %v", what, got.Total, want.Total)
	}
	if got.CustomerNote != want.CustomerNote {
		t.Errorf("%s: CustomerNote = %q, quería %q", what, got.CustomerNote, want.CustomerNote)
	}
	for _, f := range []struct {
		name      string
		got, want time.Time
	}{
		{"CreatedAt", got.CreatedAt, want.CreatedAt},
		{"UpdatedAt", got.UpdatedAt, want.UpdatedAt},
		{"DepositDueAt", got.DepositDueAt, want.DepositDueAt},
		{"DepositRemindedAt", got.DepositRemindedAt, want.DepositRemindedAt},
		{"ExpiryRemindedAt", got.ExpiryRemindedAt, want.ExpiryRemindedAt},
	} {
		if !f.got.Equal(f.want) {
			t.Errorf("%s: %s = %v, quería %v", what, f.name, f.got, f.want)
		}
	}
}

// requireSameItems compara las líneas una a una, en orden, con sus seis campos.
func requireSameItems(t *testing.T, what string, got, want []intakes.Item) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: %d líneas, quería %d: %+v", what, len(got), len(want), got)
		return
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.SKU != w.SKU || g.Label != w.Label || g.Customization != w.Customization ||
			g.Qty != w.Qty || g.UnitPrice != w.UnitPrice {
			t.Errorf("%s: línea %d = %+v, quería %+v", what, i, g, w)
		}
		if !g.AddedAt.Equal(w.AddedAt) {
			t.Errorf("%s: el AddedAt de la línea %d (%s) = %v, quería %v", what, i, w.SKU, g.AddedAt, w.AddedAt)
		}
	}
}

// requireSameRevisions compara las revisiones una a una, en orden; el payload, por su contenido.
func requireSameRevisions(t *testing.T, what string, got, want []intakes.Revision) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: %d revisiones, quería %d: %+v", what, len(got), len(want), got)
		return
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.IntakeID != w.IntakeID || g.RevisionNo != w.RevisionNo || g.Kind != w.Kind ||
			g.RenderedText != w.RenderedText || g.CreatedBy != w.CreatedBy {
			t.Errorf("%s: revisión %d = (%s, nº %d, %s, %q, %s), quería (%s, nº %d, %s, %q, %s)", what, i,
				g.IntakeID, g.RevisionNo, g.Kind, g.RenderedText, g.CreatedBy,
				w.IntakeID, w.RevisionNo, w.Kind, w.RenderedText, w.CreatedBy)
		}
		if !g.CreatedAt.Equal(w.CreatedAt) || !g.LiteralPrunedAt.Equal(w.LiteralPrunedAt) {
			t.Errorf("%s: revisión %d: (CreatedAt, LiteralPrunedAt) = (%v, %v), quería (%v, %v)", what, i,
				g.CreatedAt, g.LiteralPrunedAt, w.CreatedAt, w.LiteralPrunedAt)
		}
		if !sameJSON(g.Payload, w.Payload) {
			t.Errorf("%s: el payload de la revisión %d = %s, quería %s", what, i, g.Payload, w.Payload)
		}
	}
}

// sameJSON dice si dos payloads tienen el mismo contenido, sin mirar el orden de sus claves.
func sameJSON(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

// Las tres funciones adopt* son la forma de afirmar una escritura sin perder de vista el resto de
// la fila: comprueban lo que el puerto promete de un valor que pone la implementación (una fecha,
// una revisión nueva), lo copian a la marca esperada, y después requireSame compara TODO.

// adoptRefreshedUpdatedAt afirma que la escritura refrescó UpdatedAt (es posterior al que había) y
// lo adopta en want.
func adoptRefreshedUpdatedAt(t *testing.T, what string, got snapshot, want *snapshot) {
	t.Helper()
	if !got.detail.UpdatedAt.After(want.detail.UpdatedAt) {
		t.Errorf("%s: UpdatedAt no se refrescó: era %v y quedó %v", what, want.detail.UpdatedAt, got.detail.UpdatedAt)
	}
	want.detail.UpdatedAt = got.detail.UpdatedAt
}

// adoptNewRevision afirma que la escritura añadió UNA revisión al final —numerada como la
// siguiente, de esa clase, ese autor y ese texto, fechada y sin sello de poda—, la adopta en want
// y la devuelve para que el caso mire su payload.
func adoptNewRevision(t *testing.T, what string, got snapshot, want *snapshot, kind, by, text string) intakes.Revision {
	t.Helper()
	revs := got.detail.Revisions
	if len(revs) != len(want.detail.Revisions)+1 {
		t.Fatalf("%s: %d revisiones, quería %d (una nueva)", what, len(revs), len(want.detail.Revisions)+1)
	}
	rev := revs[len(revs)-1]
	if rev.RevisionNo != len(revs) || rev.Kind != kind || rev.CreatedBy != by || rev.RenderedText != text {
		t.Errorf("%s: revisión nueva = (nº %d, %s, %s, %q), quería (nº %d, %s, %s, %q)", what,
			rev.RevisionNo, rev.Kind, rev.CreatedBy, rev.RenderedText, len(revs), kind, by, text)
	}
	if rev.IntakeID != got.detail.ID {
		t.Errorf("%s: la revisión nueva es de %q, quería %q", what, rev.IntakeID, got.detail.ID)
	}
	if rev.CreatedAt.IsZero() || !rev.LiteralPrunedAt.IsZero() {
		t.Errorf("%s: revisión nueva con CreatedAt %v y LiteralPrunedAt %v; quería fechada y sin sello",
			what, rev.CreatedAt, rev.LiteralPrunedAt)
	}
	want.detail.Revisions = append(want.detail.Revisions, rev)
	return rev
}

// adoptAddedAt afirma que las líneas de want desde la posición from —las que la escritura acaba
// de crear— vienen fechadas, y adopta su AddedAt.
func adoptAddedAt(t *testing.T, what string, got snapshot, want *snapshot, from int) {
	t.Helper()
	for i := from; i < len(want.detail.Items) && i < len(got.detail.Items); i++ {
		if got.detail.Items[i].AddedAt.IsZero() {
			t.Errorf("%s: la línea nueva %d (%s) trae AddedAt cero", what, i, got.detail.Items[i].SKU)
		}
		want.detail.Items[i].AddedAt = got.detail.Items[i].AddedAt
	}
}
