package storehelpertest

import (
	"testing"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
)

// Los casos de CloseIntake: el cierre atómico de la solicitud abierta de un contacto, sobre
// intakes e intake_items. Su carrera está en concurrency_contrato.go.

// mustClose cierra y devuelve el id de la solicitud cerrada, o falla el test.
func mustClose(t *testing.T, m Montaje, in store.IntakeClose) string {
	t.Helper()
	id, err := m.Store.CloseIntake(ctx, in)
	if err != nil {
		t.Fatalf("CloseIntake(%s, %s): %v", in.TenantID, in.ContactID, err)
	}
	return id
}

// caseCloseOpen: con una solicitud "open" del contacto, CloseIntake cierra ESA y devuelve su id:
// estado, total y nota del cierre, updated_at refrescado. Conserva su evento padre (el del cierre
// no lo pisa), su sesión y su created_at. Las líneas quedan EXACTAMENTE en las del cierre: las que
// la proyección ya había materializado se sustituyen, no se duplican. No toca la solicitud abierta
// de otro contacto ni la del mismo contacto en otro tenant.
func caseCloseOpen(t *testing.T, m Montaje) {
	contact := uuid.NewString()
	in := seedIntake(t, m, m.TenantA, contact, statusOpen)
	mustReplaceItems(t, m, in.ID, line("CAFE", "Café", 2, 2.5), line("TE", "Té", 1, 2))
	// Los testigos nacen DESPUÉS: son las abiertas más recientes, y un cierre que perdiera el
	// filtro del contacto o el del tenant se iría a por una de ellas.
	m.Advance(t)
	seedIntakeWitnesses(t, m, contact)

	m.Advance(t)
	before := take(t, m)
	was := before.intakeOf(t, m.TenantA, in.ID)
	customized := line("CAFE", "Café", 2, 2.5)
	customized.Customization = "sin azúcar"
	final := []IntakeItem{customized, line("TE", "Té", 1, 2)}
	t0 := m.Now(t)
	closedID := mustClose(t, m, store.IntakeClose{
		TenantID: m.TenantA, ContactID: contact, SessionID: sessionTwo, Total: 7,
		CustomerNote: "Dejarlo en portería", EventID: m.NewEvent(t, m.TenantA), Items: final,
	})
	t1 := m.Now(t)
	if closedID != in.ID {
		t.Fatalf("CloseIntake devolvió %q, quería el id de la solicitud abierta (%q)", closedID, in.ID)
	}

	after := take(t, m)
	now := after.intakeOf(t, m.TenantA, in.ID)
	if !now.header.UpdatedAt.After(was.header.UpdatedAt) {
		t.Errorf("el cierre no refrescó UpdatedAt: era %v y quedó %v", was.header.UpdatedAt, now.header.UpdatedAt)
	}
	requireStamped(t, "cierre: UpdatedAt", now.header.UpdatedAt, t0, t1)
	want := was.header
	want.Status, want.Total, want.CustomerNote, want.UpdatedAt = statusClosed, 7, "Dejarlo en portería", now.header.UpdatedAt
	requireSameIntake(t, "cierre", now.header, want)
	requireLines(t, m, "cierre", in.ID, final...)

	got, found, err := m.Store.GetOpenIntake(ctx, m.TenantA, contact)
	requireNoIntake(t, "GetOpenIntake tras el cierre", got, found, err)
	requireRestUntouched(t, "cierre", before, after, forgetIntake(m.TenantA, in.ID))
}

// caseCloseCreates: sin solicitud "open" del contacto, CloseIntake crea una "closed" coherente y
// devuelve su id nuevo: con la sesión, el evento, el total, la nota y las líneas del cierre,
// fechada por la implementación. La abierta del mismo contacto en OTRO tenant no cuenta.
func caseCloseCreates(t *testing.T, m Montaje) {
	contact := uuid.NewString()
	seedIntakeWitnesses(t, m, contact)
	before := take(t, m)

	event := m.NewEvent(t, m.TenantA)
	items := []IntakeItem{line("CAFE", "Café", 3, 2.5), line("TE", "Té", 4, 2)}
	t0 := m.Now(t)
	closedID := mustClose(t, m, store.IntakeClose{
		TenantID: m.TenantA, ContactID: contact, SessionID: sessionTwo, Total: 15.5,
		CustomerNote: "Sin cebolla", EventID: event, Items: items,
	})
	t1 := m.Now(t)
	if _, err := uuid.Parse(closedID); err != nil {
		t.Fatalf("CloseIntake devolvió %q, quería un UUID nuevo: %v", closedID, err)
	}
	if _, existed := before.tenants[m.TenantA].intakes[closedID]; existed {
		t.Fatalf("CloseIntake devolvió el id de una solicitud que ya existía (%s) y no era de ese contacto", closedID)
	}

	row := observedIntake(t, m, m.TenantA, closedID)
	requireStamped(t, "cierre que crea: CreatedAt", row.CreatedAt, t0, t1)
	requireSameIntake(t, "cierre que crea", row, Intake{
		ID: closedID, TenantID: m.TenantA, ContactID: contact, SessionID: sessionTwo,
		Status: statusClosed, Total: 15.5, EventID: event, CustomerNote: "Sin cebolla",
		CreatedAt: row.CreatedAt, UpdatedAt: row.CreatedAt,
	})
	requireLines(t, m, "cierre que crea", closedID, items...)

	got, found, err := m.Store.GetIntakeByEvent(ctx, m.TenantA, event)
	requireHeaderRead(t, "GetIntakeByEvent del cierre que crea", got, found, err, row)
	requireRestUntouched(t, "cierre que crea", before, take(t, m), forgetIntake(m.TenantA, closedID))
}

// caseCloseIgnoresNonOpen: solo una solicitud "open" se cierra. Las del mismo contacto en otro
// estado —aunque sean más recientes que la abierta— ni se cierran ni se tocan.
func caseCloseIgnoresNonOpen(t *testing.T, m Montaje) {
	contact := uuid.NewString()
	open := seedIntake(t, m, m.TenantA, contact, statusOpen)
	mustReplaceItems(t, m, open.ID, line("VIEJA", "Vieja", 1, 1))
	for _, status := range []string{statusPending, statusClosed, statusCanceled} {
		m.Advance(t)
		newer := seedIntake(t, m, m.TenantA, contact, status)
		mustReplaceItems(t, m, newer.ID, line("OTRA", "De la "+status, 1, 1))
	}
	m.Advance(t)
	before := take(t, m)

	closedID := mustClose(t, m, store.IntakeClose{
		TenantID: m.TenantA, ContactID: contact, Total: 2.5, EventID: m.NewEvent(t, m.TenantA),
		Items: []IntakeItem{line("CAFE", "Café", 1, 2.5)},
	})
	if closedID != open.ID {
		t.Errorf("CloseIntake devolvió %q, quería la única abierta (%q)", closedID, open.ID)
	}
	requireRestUntouched(t, "cierre con solicitudes en otro estado", before, take(t, m), forgetIntake(m.TenantA, open.ID))
}

// caseCloseNewest: si el contacto tiene varias "open" —el negocio no lo produce, pero ningún
// índice lo impide—, CloseIntake cierra la MÁS RECIENTE por created_at y deja la otra abierta e
// intacta.
func caseCloseNewest(t *testing.T, m Montaje) {
	contact := uuid.NewString()
	older := seedIntake(t, m, m.TenantA, contact, statusOpen)
	m.Advance(t)
	newer := seedIntake(t, m, m.TenantA, contact, statusOpen)
	m.Advance(t)
	// Reescribir la antigua refresca su updated_at, no su created_at: sigue siendo la antigua.
	mustUpsertIntake(t, m, older)
	m.Advance(t)
	before := take(t, m)

	closedID := mustClose(t, m, store.IntakeClose{
		TenantID: m.TenantA, ContactID: contact, Total: 1, EventID: m.NewEvent(t, m.TenantA),
		Items: []IntakeItem{line("CAFE", "Café", 1, 1)},
	})
	if closedID != newer.ID {
		t.Errorf("CloseIntake devolvió %q, quería la abierta más reciente (%q)", closedID, newer.ID)
	}
	after := take(t, m)
	if got := after.intakeOf(t, m.TenantA, older.ID).header.Status; got != statusOpen {
		t.Errorf("la abierta antigua quedó %q, quería %q", got, statusOpen)
	}
	requireRestUntouched(t, "cierre con dos abiertas", before, after, forgetIntake(m.TenantA, newer.ID))
}

// caseCloseEmptyItems: las líneas del cierre son la verdad final. Un cierre sin líneas deja la
// solicitud sin líneas de cliente, y en cualquier cierre las de la plataforma sobreviven.
func caseCloseEmptyItems(t *testing.T, m Montaje) {
	shipping := line(platformSKU, "Envío", 1, 3000)
	for _, tc := range []struct {
		name  string
		items []IntakeItem
	}{
		{"cierre sin líneas", nil},
		{"cierre con otra foto", []IntakeItem{line("TE", "Té", 2, 2)}},
	} {
		contact := uuid.NewString()
		in := seedIntake(t, m, m.TenantA, contact, statusOpen)
		mustReplaceItems(t, m, in.ID, shipping, line("CAFE", "Café", 1, 2.5))
		closedID := mustClose(t, m, store.IntakeClose{
			TenantID: m.TenantA, ContactID: contact, Total: 3000, EventID: in.EventID, Items: tc.items,
		})
		if closedID != in.ID {
			t.Errorf("%s: CloseIntake devolvió %q, quería %q", tc.name, closedID, in.ID)
		}
		requireLines(t, m, tc.name, in.ID, append([]IntakeItem{shipping}, tc.items...)...)
		if got := observedIntake(t, m, m.TenantA, in.ID).Status; got != statusClosed {
			t.Errorf("%s: la solicitud quedó %q, quería %q", tc.name, got, statusClosed)
		}
	}
}

// caseCloseAfterClose: una solicitud ya cerrada no se reabre ni se reescribe. Un cierre posterior
// del mismo contacto, sin otra abierta, crea OTRA solicitud "closed" con su propio evento, y la
// primera —cabecera, nota y líneas— queda como estaba.
func caseCloseAfterClose(t *testing.T, m Montaje) {
	contact := uuid.NewString()
	in := seedIntake(t, m, m.TenantA, contact, statusOpen)
	first := mustClose(t, m, store.IntakeClose{
		TenantID: m.TenantA, ContactID: contact, Total: 5, CustomerNote: "primera", EventID: in.EventID,
		Items: []IntakeItem{line("CAFE", "Café", 2, 2.5)},
	})
	m.Advance(t)
	before := take(t, m)

	event := m.NewEvent(t, m.TenantA)
	second := mustClose(t, m, store.IntakeClose{
		TenantID: m.TenantA, ContactID: contact, SessionID: sessionTwo, Total: 4, CustomerNote: "segunda",
		EventID: event, Items: []IntakeItem{line("TE", "Té", 2, 2)},
	})
	if second == first {
		t.Fatalf("el segundo cierre devolvió la solicitud ya cerrada (%s), quería una nueva", first)
	}
	row := observedIntake(t, m, m.TenantA, second)
	requireSameIntake(t, "segundo cierre", row, Intake{
		ID: second, TenantID: m.TenantA, ContactID: contact, SessionID: sessionTwo,
		Status: statusClosed, Total: 4, EventID: event, CustomerNote: "segunda",
		CreatedAt: row.CreatedAt, UpdatedAt: row.CreatedAt,
	})
	requireLines(t, m, "segundo cierre", second, line("TE", "Té", 2, 2))
	requireRestUntouched(t, "segundo cierre", before, take(t, m), forgetIntake(m.TenantA, second))
}
