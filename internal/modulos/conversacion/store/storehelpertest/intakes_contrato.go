package storehelpertest

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
)

// Los casos de la cabecera de una solicitud: UpsertIntake, GetOpenIntake, GetIntakeByEvent y
// MarkIntakeStatus sobre intakes. Las líneas están en intake_items_contrato.go y el cierre en
// close_contrato.go.

// Los estados que la suite guarda. Para el puerto son texto: no hay CHECK ni máquina de estados.
const (
	statusOpen     = "open"
	statusClosed   = "closed"
	statusPending  = "pending_approval"
	statusCanceled = "cancelled"
)

// intakeOf saca de una marca de estado la solicitud, que TIENE que estar.
func (w world) intakeOf(t *testing.T, tenant, id string) intakeMark {
	t.Helper()
	mark, ok := w.tenants[tenant].intakes[id]
	if !ok {
		t.Fatalf("la solicitud %s no está en la marca de estado del tenant %s", id, tenant)
	}
	return mark
}

// seedIntakeWitnesses siembra los dos testigos de un caso de solicitudes, las dos "open" y con una
// línea: la de OTRO contacto del mismo tenant y la del MISMO contacto en el otro tenant.
func seedIntakeWitnesses(t *testing.T, m Montaje, contact string) {
	t.Helper()
	for _, in := range []Intake{
		seedIntake(t, m, m.TenantA, uuid.NewString(), statusOpen),
		seedIntake(t, m, m.TenantB, contact, statusOpen),
	} {
		mustReplaceItems(t, m, in.ID, line("TESTIGO", "Testigo", 1, 1))
	}
}

// requireHeaderRead afirma que una lectura de cabecera del puerto devolvió la fila row: los diez
// campos que la lectura trae, y CustomerNote vacía (ninguna de las dos lecturas la lee).
func requireHeaderRead(t *testing.T, what string, got Intake, found bool, err error, row Intake) {
	t.Helper()
	if err != nil || !found {
		t.Fatalf("%s = (found %v, %v), quería la solicitud %s", what, found, err, row.ID)
	}
	row.CustomerNote = ""
	requireSameIntake(t, what, got, row)
}

// requireNoIntake afirma que una lectura de cabecera no encontró nada: found=false, sin error y
// con la solicitud cero.
func requireNoIntake(t *testing.T, what string, got Intake, found bool, err error) {
	t.Helper()
	if err != nil || found {
		t.Errorf("%s = (found %v, %v), quería (false, nil)", what, found, err)
	}
	requireSameIntake(t, what, got, Intake{})
}

// caseUpsertIntakeCreates: el alta guarda la cabecera con sus campos, fechada por la
// implementación (el CreatedAt, el UpdatedAt y la nota del argumento se ignoran), y las dos
// lecturas de cabecera devuelven esa misma fila.
func caseUpsertIntakeCreates(t *testing.T, m Montaje) {
	contact := uuid.NewString()
	seedIntakeWitnesses(t, m, contact)
	before := take(t, m)

	in := newIntake(t, m, m.TenantA, contact, statusOpen)
	in.Total = 12.5
	in.CreatedAt = time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	in.UpdatedAt = in.CreatedAt
	in.CustomerNote = "UpsertIntake no escribe la nota"
	t0 := m.Now(t)
	mustUpsertIntake(t, m, in)
	t1 := m.Now(t)

	row := observedIntake(t, m, m.TenantA, in.ID)
	requireStamped(t, "alta: CreatedAt", row.CreatedAt, t0, t1)
	want := in
	want.CreatedAt, want.UpdatedAt, want.CustomerNote = row.CreatedAt, row.CreatedAt, ""
	requireSameIntake(t, "alta", row, want)
	if items := m.IntakeItems(t, in.ID); len(items) != 0 {
		t.Errorf("la solicitud nace con %d líneas, quería ninguna: %+v", len(items), items)
	}

	got, found, err := m.Store.GetOpenIntake(ctx, m.TenantA, contact)
	requireHeaderRead(t, "GetOpenIntake", got, found, err, row)
	got, found, err = m.Store.GetIntakeByEvent(ctx, m.TenantA, in.EventID)
	requireHeaderRead(t, "GetIntakeByEvent", got, found, err, row)
	requireRestUntouched(t, "alta", before, take(t, m), forgetIntake(m.TenantA, in.ID))
}

// caseUpsertIntakeUpdates: repetir UpsertIntake con el mismo id ACTUALIZA la fila —contacto,
// sesión, estado, total y ExpiresAt— y refresca updated_at, pero deja tres cosas como estaban:
// created_at, el evento padre ya declarado (otro valor no lo pisa) y la nota que puso el cierre.
func caseUpsertIntakeUpdates(t *testing.T, m Montaje) {
	contact := uuid.NewString()
	seedIntakeWitnesses(t, m, contact)
	in := seedIntake(t, m, m.TenantA, contact, statusOpen)
	created := observedIntake(t, m, m.TenantA, in.ID)

	m.Advance(t)
	before := take(t, m)
	update := Intake{
		ID: in.ID, TenantID: m.TenantA, ContactID: uuid.NewString(), SessionID: sessionTwo,
		Status: statusPending, Total: 9.5, EventID: m.NewEvent(t, m.TenantA), ExpiresAt: at(20, 10),
	}
	t0 := m.Now(t)
	mustUpsertIntake(t, m, update)
	t1 := m.Now(t)
	row := observedIntake(t, m, m.TenantA, in.ID)
	if !row.UpdatedAt.After(created.UpdatedAt) {
		t.Errorf("la actualización no refrescó UpdatedAt: era %v y quedó %v", created.UpdatedAt, row.UpdatedAt)
	}
	requireStamped(t, "actualización: UpdatedAt", row.UpdatedAt, t0, t1)
	want := update
	want.EventID, want.CreatedAt, want.UpdatedAt = in.EventID, created.CreatedAt, row.UpdatedAt
	requireSameIntake(t, "actualización", row, want)
	requireRestUntouched(t, "actualización", before, take(t, m), forgetIntake(m.TenantA, in.ID))

	// Otro evento más, y sin ExpiresAt en el argumento: el padre declarado sigue; el vencimiento se
	// borra. (Con el EventID VACÍO no se prueba aquí: desde la 0055 la columna es NOT NULL y Postgres
	// rechaza la fila propuesta antes de resolver el conflicto. Lo cubre el test del gemelo.)
	update.EventID, update.ExpiresAt = m.NewEvent(t, m.TenantA), time.Time{}
	mustUpsertIntake(t, m, update)
	row = observedIntake(t, m, m.TenantA, in.ID)
	if row.EventID != in.EventID {
		t.Errorf("un tercer UpsertIntake dejó EventID = %q, quería el declarado (%q)", row.EventID, in.EventID)
	}
	if !row.ExpiresAt.IsZero() {
		t.Errorf("un UpsertIntake sin ExpiresAt dejó %v, quería cero", row.ExpiresAt)
	}

	// La nota la pone el cierre, y un upsert posterior sobre la solicitud cerrada no la borra.
	noted := seedIntake(t, m, m.TenantA, contact, statusOpen)
	const note = "Dejarlo en portería"
	if _, err := m.Store.CloseIntake(ctx, store.IntakeClose{
		TenantID: m.TenantA, ContactID: contact, Total: 3, CustomerNote: note, EventID: noted.EventID,
	}); err != nil {
		t.Fatalf("CloseIntake: %v", err)
	}
	noted.Status = statusClosed
	mustUpsertIntake(t, m, noted)
	if got := observedIntake(t, m, m.TenantA, noted.ID).CustomerNote; got != note {
		t.Errorf("un UpsertIntake sobre la solicitud cerrada dejó CustomerNote = %q, quería %q", got, note)
	}
}

// caseGetOpenIntakeScope: GetOpenIntake solo ve la solicitud "open" de ESE contacto en ESE tenant.
// Las de cualquier otro estado, la de otro contacto y la del mismo contacto en otro tenant no
// cuentan, y no encontrar nada no es un error.
func caseGetOpenIntakeScope(t *testing.T, m Montaje) {
	contact := uuid.NewString()
	got, found, err := m.Store.GetOpenIntake(ctx, m.TenantA, contact)
	requireNoIntake(t, "GetOpenIntake sin solicitudes", got, found, err)

	for _, status := range []string{statusPending, statusClosed, statusCanceled, "expired"} {
		seedIntake(t, m, m.TenantA, contact, status)
	}
	seedIntake(t, m, m.TenantA, uuid.NewString(), statusOpen)
	foreign := seedIntake(t, m, m.TenantB, contact, statusOpen)
	got, found, err = m.Store.GetOpenIntake(ctx, m.TenantA, contact)
	requireNoIntake(t, "GetOpenIntake con solicitudes que no son suyas o no están abiertas", got, found, err)

	own := seedIntake(t, m, m.TenantA, contact, statusOpen)
	got, found, err = m.Store.GetOpenIntake(ctx, m.TenantA, contact)
	requireHeaderRead(t, "GetOpenIntake(A)", got, found, err, observedIntake(t, m, m.TenantA, own.ID))
	got, found, err = m.Store.GetOpenIntake(ctx, m.TenantB, contact)
	requireHeaderRead(t, "GetOpenIntake(B)", got, found, err, observedIntake(t, m, m.TenantB, foreign.ID))
}

// caseGetOpenIntakeNewest: el negocio promete UNA solicitud "open" por contacto, pero ningún
// índice lo impone. Si hay varias, gana la MÁS RECIENTE; y cuando esa deja de estar abierta,
// vuelve a verse la anterior.
func caseGetOpenIntakeNewest(t *testing.T, m Montaje) {
	contact := uuid.NewString()
	older := seedIntake(t, m, m.TenantA, contact, statusOpen)
	m.Advance(t)
	newer := seedIntake(t, m, m.TenantA, contact, statusOpen)
	m.Advance(t)
	// Reescribir la antigua refresca su updated_at, no su created_at: sigue siendo la antigua.
	mustUpsertIntake(t, m, older)

	got, found, err := m.Store.GetOpenIntake(ctx, m.TenantA, contact)
	requireHeaderRead(t, "GetOpenIntake con dos abiertas", got, found, err, observedIntake(t, m, m.TenantA, newer.ID))

	if err := m.Store.MarkIntakeStatus(ctx, newer.ID, statusCanceled, 0); err != nil {
		t.Fatalf("MarkIntakeStatus: %v", err)
	}
	got, found, err = m.Store.GetOpenIntake(ctx, m.TenantA, contact)
	requireHeaderRead(t, "GetOpenIntake tras cancelar la reciente", got, found, err, observedIntake(t, m, m.TenantA, older.ID))
}

// caseGetIntakeByEvent: GetIntakeByEvent encuentra la solicitud que declara ese evento ESTÉ EN EL
// ESTADO QUE ESTÉ —es su contrato entero: GetOpenIntake no ve una `pending_approval`— y solo
// dentro de su tenant. Devuelve la misma cabecera que la fila, sin la nota.
func caseGetIntakeByEvent(t *testing.T, m Montaje) {
	contact := uuid.NewString()
	seedIntakeWitnesses(t, m, contact)
	for _, status := range []string{statusPending, statusClosed, statusCanceled, statusOpen} {
		in := seedIntake(t, m, m.TenantA, uuid.NewString(), status)
		got, found, err := m.Store.GetIntakeByEvent(ctx, m.TenantA, in.EventID)
		requireHeaderRead(t, "GetIntakeByEvent de una "+status, got, found, err, observedIntake(t, m, m.TenantA, in.ID))
		got, found, err = m.Store.GetIntakeByEvent(ctx, m.TenantB, in.EventID)
		requireNoIntake(t, "GetIntakeByEvent desde otro tenant", got, found, err)
	}

	pending := seedIntake(t, m, m.TenantA, contact, statusPending)
	got, found, err := m.Store.GetOpenIntake(ctx, m.TenantA, contact)
	requireNoIntake(t, "GetOpenIntake de un contacto con una pending_approval", got, found, err)
	got, found, err = m.Store.GetIntakeByEvent(ctx, m.TenantA, pending.EventID)
	requireHeaderRead(t, "GetIntakeByEvent de esa pending_approval", got, found, err, observedIntake(t, m, m.TenantA, pending.ID))

	// Una solicitud cerrada con nota: la lectura por evento tampoco la trae.
	closing := seedIntake(t, m, m.TenantA, contact, statusOpen)
	if _, err := m.Store.CloseIntake(ctx, store.IntakeClose{
		TenantID: m.TenantA, ContactID: contact, Total: 2, CustomerNote: "Sin timbre", EventID: closing.EventID,
	}); err != nil {
		t.Fatalf("CloseIntake: %v", err)
	}
	row := observedIntake(t, m, m.TenantA, closing.ID)
	if row.CustomerNote != "Sin timbre" {
		t.Fatalf("la fila cerrada tiene CustomerNote = %q, quería %q", row.CustomerNote, "Sin timbre")
	}
	got, found, err = m.Store.GetIntakeByEvent(ctx, m.TenantA, closing.EventID)
	requireHeaderRead(t, "GetIntakeByEvent de una cerrada con nota", got, found, err, row)
}

// caseGetIntakeByEventNotFound: un evento sin solicitud, la cadena vacía y un id que no es un UUID
// son la misma respuesta: found=false, sin error y con la solicitud cero.
func caseGetIntakeByEventNotFound(t *testing.T, m Montaje) {
	seedIntake(t, m, m.TenantA, uuid.NewString(), statusOpen)
	for _, eventID := range []string{m.NewEvent(t, m.TenantA), uuid.NewString(), "", "no-soy-un-uuid"} {
		got, found, err := m.Store.GetIntakeByEvent(ctx, m.TenantA, eventID)
		requireNoIntake(t, "GetIntakeByEvent("+eventID+")", got, found, err)
	}
}

// caseMarkIntakeStatus: MarkIntakeStatus cambia el estado y el total de ESA solicitud y refresca
// su updated_at. Nada más: ni el resto de la cabecera, ni sus líneas, ni otra solicitud del mismo
// contacto, ni las de los testigos.
func caseMarkIntakeStatus(t *testing.T, m Montaje) {
	contact := uuid.NewString()
	seedIntakeWitnesses(t, m, contact)
	sibling := seedIntake(t, m, m.TenantA, contact, statusPending)
	mustReplaceItems(t, m, sibling.ID, line("HERMANA", "Hermana", 1, 1))
	in := seedIntake(t, m, m.TenantA, contact, statusOpen)
	mustReplaceItems(t, m, in.ID, line("CAFE", "Café", 2, 2.5), line("TE", "Té", 1, 2))

	m.Advance(t)
	before := take(t, m)
	was := before.intakeOf(t, m.TenantA, in.ID)
	t0 := m.Now(t)
	if err := m.Store.MarkIntakeStatus(ctx, in.ID, statusCanceled, 3.5); err != nil {
		t.Fatalf("MarkIntakeStatus: %v", err)
	}
	t1 := m.Now(t)

	after := take(t, m)
	now := after.intakeOf(t, m.TenantA, in.ID)
	if !now.header.UpdatedAt.After(was.header.UpdatedAt) {
		t.Errorf("MarkIntakeStatus no refrescó UpdatedAt: era %v y quedó %v", was.header.UpdatedAt, now.header.UpdatedAt)
	}
	requireStamped(t, "MarkIntakeStatus: UpdatedAt", now.header.UpdatedAt, t0, t1)
	want := was.header
	want.Status, want.Total, want.UpdatedAt = statusCanceled, 3.5, now.header.UpdatedAt
	requireSameIntake(t, "MarkIntakeStatus", now.header, want)
	requireSameItems(t, "MarkIntakeStatus: líneas", now.items, was.items)
	requireRestUntouched(t, "MarkIntakeStatus", before, after, forgetIntake(m.TenantA, in.ID))
}

// caseMarkIntakeStatusUnknown: marcar una solicitud que no existe es un no-op sin error.
func caseMarkIntakeStatusUnknown(t *testing.T, m Montaje) {
	seedIntakeWitnesses(t, m, uuid.NewString())
	before := take(t, m)
	if err := m.Store.MarkIntakeStatus(ctx, uuid.NewString(), statusClosed, 99); err != nil {
		t.Errorf("MarkIntakeStatus de una solicitud que no existe = %v, quería nil", err)
	}
	requireUntouched(t, "MarkIntakeStatus de una solicitud que no existe", before, take(t, m))
}
