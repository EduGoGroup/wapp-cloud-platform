package intakeshelpertest

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

// Los estados de un evento conversacional (public.conversation_events.status). Son del dominio de
// conversación; aquí solo se siembran y se observan.
const (
	eventOpen      = "open"
	eventClosed    = "closed"
	eventCancelled = "cancelled"
)

// Sesiones y contactos de las siembras. ASCII y opacos: ninguno decide un orden.
const (
	sessionA = "sess-a"
	sessionB = "sess-b"
	contactA = "contact-opaque-a"
	contactB = "contact-opaque-b"
)

// bg es el contexto de todas las llamadas de la suite.
func bg() context.Context { return context.Background() }

// day es el mediodía UTC del día d de agosto de 2026: la fecha de una siembra. Todas son
// anteriores a seedsNotBefore, y por tanto a Montaje.Now.
func day(d int) time.Time { return time.Date(2026, 8, d, 12, 0, 0, 0, time.UTC) }

// lineAt es el AddedAt de la línea i-ésima de una siembra: crecientes, a cinco minutos.
func lineAt(i int) time.Time {
	return time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC).Add(time.Duration(i) * 5 * time.Minute)
}

// ref localiza una solicitud sembrada: su tenant, su id y el evento que declara.
type ref struct {
	tenant, id, eventID string
}

// customerLines son tres líneas de cliente fechadas en orden: 2×2 + 1×3 + 1×1 = 8.
func customerLines() []intakes.Item {
	return []intakes.Item{
		{SKU: "pan", Label: "Pan", Customization: "sin sal", Qty: 2, UnitPrice: 2, AddedAt: lineAt(0)},
		{SKU: "queso", Label: "Queso", Customization: "", Qty: 1, UnitPrice: 3, AddedAt: lineAt(1)},
		{SKU: "leche", Label: "Leche", Customization: "tibia", Qty: 1, UnitPrice: 1, AddedAt: lineAt(2)},
	}
}

// shippingLine es una línea de envío ya guardada, con esa etiqueta y ese precio, fechada después
// de las de cliente.
func shippingLine(label string, price float64) intakes.Item {
	return intakes.Item{SKU: intakes.ShippingSKU, Label: label, Qty: 1, UnitPrice: price, AddedAt: lineAt(9)}
}

// totalOf suma Qty × UnitPrice de las líneas: el total que la cabecera tiene que llevar.
func totalOf(items []intakes.Item) float64 {
	var total float64
	for _, it := range items {
		total += float64(it.Qty) * it.UnitPrice
	}
	return total
}

// header arma la cabecera de una siembra: id nuevo, el estado ALMACENADO pedido, creada y
// actualizada el día d, de sessionA y contactA, con el total de sus líneas.
func header(status string, d int, items []intakes.Item) intakes.Intake {
	return intakes.Intake{
		ID: uuid.NewString(), ContactID: contactA, SessionID: sessionA, Status: status,
		Total: totalOf(items), CreatedAt: day(d), UpdatedAt: day(d),
	}
}

// seed siembra en el tenant una solicitud con ese estado almacenado, ese día, ese estado de su
// evento y esas líneas, y devuelve dónde quedó.
func seed(t *testing.T, m Montaje, tenant, status string, d int, event string, items ...intakes.Item) ref {
	t.Helper()
	return seedHeader(t, m, tenant, header(status, d, items), event, items...)
}

// seedHeader siembra la cabecera dada tal cual (para los casos que fijan fechas de recordatorio,
// contacto o sesión) y devuelve dónde quedó.
func seedHeader(t *testing.T, m Montaje, tenant string, in intakes.Intake, event string, items ...intakes.Item) ref {
	t.Helper()
	eventID := m.Seed(t, tenant, Seed{Intake: in, Items: items, EventStatus: event})
	if _, err := uuid.Parse(eventID); err != nil {
		t.Fatalf("Montaje.Seed devolvió un evento que no es un UUID: %q", eventID)
	}
	return ref{tenant: tenant, id: in.ID, eventID: eventID}
}

// cartPayload es el payload de una revisión del carrito, sin literal del cliente.
func cartPayload(t *testing.T) json.RawMessage {
	t.Helper()
	raw, err := intakes.CartRevisionPayload(8, []intakes.RevisionLine{{SKU: "pan", Label: "Pan", Qty: 2, UnitPrice: 2}})
	if err != nil {
		t.Fatalf("armando el payload del carrito: %v", err)
	}
	return raw
}

// insertRevision escribe una revisión de esa clase y ese texto en la solicitud, o falla el test.
func insertRevision(t *testing.T, m Montaje, intakeID, kind, by, text string) intakes.Revision {
	t.Helper()
	rev, err := m.Store.InsertRevision(bg(), intakes.Revision{
		IntakeID: intakeID, Kind: kind, Payload: cartPayload(t), RenderedText: text, CreatedBy: by,
	})
	if err != nil {
		t.Fatalf("InsertRevision(%s, %s): error inesperado %v", intakeID, kind, err)
	}
	return rev
}

// get lee el detalle o falla el test.
func get(t *testing.T, m Montaje, r ref) intakes.Detail {
	t.Helper()
	d, err := m.Store.Get(bg(), r.tenant, r.id)
	if err != nil {
		t.Fatalf("Get(%s): error inesperado %v", r.id, err)
	}
	return d
}

// list lista con el filtro o falla el test; devuelve los ids en el orden servido y el total.
func list(t *testing.T, m Montaje, tenant string, f intakes.Filter) (ids []string, total int) {
	t.Helper()
	got, total, err := m.Store.List(bg(), tenant, f)
	if err != nil {
		t.Fatalf("List(%+v): error inesperado %v", f, err)
	}
	if got == nil {
		t.Errorf("List(%+v) = nil, quería un slice no nil", f)
	}
	ids = make([]string, 0, len(got))
	for _, in := range got {
		ids = append(ids, in.ID)
	}
	return ids, total
}

// requireIDs afirma el orden exacto de ids y el total.
func requireIDs(t *testing.T, what string, gotIDs []string, gotTotal int, wantTotal int, want ...string) {
	t.Helper()
	if gotTotal != wantTotal {
		t.Errorf("%s: total = %d, quería %d", what, gotTotal, wantTotal)
	}
	if !slices.Equal(gotIDs, want) {
		t.Errorf("%s: ids = %v, quería %v", what, gotIDs, want)
	}
}

// witness son las dos solicitudes que un caso NO toca —una hermana del mismo tenant y una del otro
// tenant— con su marca de estado al sembrarlas.
type witness struct {
	refs  []ref
	marks []snapshot
}

// seedWitnesses siembra los dos testigos, cada uno con las MISMAS claves que usan los casos
// (sesión, contacto, skus, línea de envío, una revisión, evento `open`), guarda su marca de estado
// y deja pasar el reloj: si una escritura rozara a cualquiera de los dos, saldría distinta.
func seedWitnesses(t *testing.T, m Montaje) witness {
	t.Helper()
	var w witness
	for _, tenant := range []string{m.TenantA, m.TenantB} {
		lines := append(customerLines(), shippingLine(intakes.ShippingPendingLabel, 4))
		in := header(intakes.StatusPendingApproval, 20, lines)
		in.CustomerNote = "testigo"
		in.DepositDueAt, in.DepositRemindedAt, in.ExpiryRemindedAt = day(21), day(22), day(23)
		r := seedHeader(t, m, tenant, in, eventOpen, lines...)
		insertRevision(t, m, r.id, intakes.RevisionKindCart, intakes.RevisionBySystem, "")
		w.refs = append(w.refs, r)
	}
	for _, r := range w.refs {
		w.marks = append(w.marks, take(t, m, r))
	}
	m.Advance(t)
	return w
}

// requireUntouched afirma que los dos testigos siguen exactamente como se sembraron.
func (w witness) requireUntouched(t *testing.T, m Montaje) {
	t.Helper()
	for i, r := range w.refs {
		what := "la hermana del mismo tenant"
		if r.tenant == m.TenantB {
			what = "la solicitud del otro tenant"
		}
		requireSame(t, what, take(t, m, r), w.marks[i])
	}
}

// foreign devuelve el testigo del otro tenant: la víctima de los casos de aislamiento.
func (w witness) foreign() ref { return w.refs[1] }

// sortedIDs devuelve n UUID nuevos en orden ascendente. Son minúsculas ASCII (dígitos y a–f): el
// orden byte a byte de Go es el mismo con el que Postgres compara el tipo uuid.
func sortedIDs(n int) []string {
	out := make([]string, 0, n)
	for range n {
		out = append(out, uuid.NewString())
	}
	slices.Sort(out)
	return out
}

// decode lee un payload como objeto JSON o falla el test.
func decode(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("el payload no es un objeto JSON: %v (%s)", err, raw)
	}
	return out
}
