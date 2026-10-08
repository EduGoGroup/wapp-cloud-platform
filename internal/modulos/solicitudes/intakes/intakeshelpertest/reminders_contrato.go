package intakeshelpertest

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

// Los recordatorios reciben el instante `at` del llamante: la suite lo fija y no necesita mover
// ningún reloj para cruzar un plazo.

// seedDeposit siembra una solicitud con ese estado, esa fecha límite de seña y esa marca de
// recordatorio (cero = sin ella), del contacto dado.
func seedDeposit(t *testing.T, m Montaje, tenant, status, contact string, due, reminded time.Time) ref {
	t.Helper()
	in := header(status, 1, customerLines())
	in.ContactID, in.DepositDueAt, in.DepositRemindedAt = contact, due, reminded
	return seedHeader(t, m, tenant, in, eventCancelled, customerLines()...)
}

// seedQuote siembra una solicitud con ese estado, tocada por última vez en updated y con esa
// marca de aviso del plazo (cero = sin ella).
func seedQuote(t *testing.T, m Montaje, tenant, status string, updated, reminded time.Time) ref {
	t.Helper()
	in := header(status, 1, customerLines())
	in.UpdatedAt, in.ExpiryRemindedAt = updated, reminded
	return seedHeader(t, m, tenant, in, eventCancelled, customerLines()...)
}

// requireNotItsTurn afirma el «no procede» de un compare-and-swap: false, sin error, una cabecera
// a cero y NADA escrito.
func requireNotItsTurn(t *testing.T, what string, got intakes.Intake, won bool, err error) {
	t.Helper()
	if err != nil {
		t.Errorf("%s: err = %v; que no le toque no es un error", what, err)
	}
	if won {
		t.Errorf("%s: ganó el recordatorio y no le tocaba", what)
	}
	if got.ID != "" {
		t.Errorf("%s: devolvió una cabecera sin haber ganado: %+v", what, got)
	}
}

// caseDepositRemindedOnce: la primera llamada gana y escribe la marca; la segunda no gana y no
// escribe. La fecha límite EXACTA ya cuenta como vencida. UpdatedAt no se mueve.
func caseDepositRemindedOnce(t *testing.T, m Montaje) {
	w := seedWitnesses(t, m)
	at := day(10)
	r := seedDeposit(t, m, m.TenantA, intakes.StatusDepositRequested, contactA, at, time.Time{})
	want := take(t, m, r).clone()
	m.Advance(t)

	returned, won, err := m.Store.MarkDepositReminded(bg(), r.tenant, r.id, at)
	if err != nil || !won {
		t.Fatalf("MarkDepositReminded en el instante del vencimiento: (ganó=%v, err=%v), quería ganar", won, err)
	}
	got := take(t, m, r)
	want.detail.DepositRemindedAt = at
	requireSame(t, "tras el primer recordatorio", got, want)
	requireSameHeader(t, "la cabecera devuelta", returned, got.detail.Intake)

	again, won, err := m.Store.MarkDepositReminded(bg(), r.tenant, r.id, at.Add(48*time.Hour))
	requireNotItsTurn(t, "el segundo recordatorio", again, won, err)
	requireSame(t, "tras el segundo recordatorio", take(t, m, r), want)
	w.requireUntouched(t, m)
}

// caseDepositRemindedNotItsTurn barre las formas de «no procede»: la seña que aún no vence, la que
// no tiene fecha, la ya resuelta (el cliente pagó, se canceló), la ya recordada, la de otro tenant
// y la que no existe. Ninguna es un error y ninguna escribe.
func caseDepositRemindedNotItsTurn(t *testing.T, m Montaje) {
	w := seedWitnesses(t, m)
	at := day(10)
	for name, r := range map[string]ref{
		"aún no vence (por un segundo)": seedDeposit(t, m, m.TenantA, intakes.StatusDepositRequested, contactA, at.Add(time.Second), time.Time{}),
		"sin fecha límite":              seedDeposit(t, m, m.TenantA, intakes.StatusDepositRequested, contactA, time.Time{}, time.Time{}),
		"ya pagada":                     seedDeposit(t, m, m.TenantA, intakes.StatusDepositPaid, contactA, day(5), time.Time{}),
		"cancelada":                     seedDeposit(t, m, m.TenantA, intakes.StatusCancelled, contactA, day(5), time.Time{}),
		"ya recordada":                  seedDeposit(t, m, m.TenantA, intakes.StatusDepositRequested, contactA, day(5), day(6)),
	} {
		before := take(t, m, r)
		got, won, err := m.Store.MarkDepositReminded(bg(), r.tenant, r.id, at)
		requireNotItsTurn(t, "seña "+name, got, won, err)
		requireSame(t, "seña "+name, take(t, m, r), before)
	}

	theirs := seedDeposit(t, m, m.TenantB, intakes.StatusDepositRequested, contactA, day(5), time.Time{})
	before := take(t, m, theirs)
	for name, target := range map[string]ref{
		"de otro tenant":    {tenant: m.TenantA, id: theirs.id},
		"inexistente":       {tenant: m.TenantA, id: uuid.NewString()},
		"id que no es UUID": {tenant: m.TenantA, id: "no-soy-un-uuid"},
	} {
		got, won, err := m.Store.MarkDepositReminded(bg(), target.tenant, target.id, at)
		requireNotItsTurn(t, "seña "+name, got, won, err)
	}
	requireSame(t, "la seña vencida del otro tenant", take(t, m, theirs), before)
	w.requireUntouched(t, m)
}

// casePendingDepositReminders: la consulta del toque del mensaje entrante trae SOLO lo del contacto
// que habló, solo lo vencido y sin recordar, lo más vencido primero y acotado. Es de solo lectura.
func casePendingDepositReminders(t *testing.T, m Montaje) {
	w := seedWitnesses(t, m)
	at := day(10)
	requested := intakes.StatusDepositRequested
	middle := seedDeposit(t, m, m.TenantA, requested, contactA, day(8), time.Time{})
	oldest := seedDeposit(t, m, m.TenantA, requested, contactA, day(3), time.Time{})
	edge := seedDeposit(t, m, m.TenantA, requested, contactA, at, time.Time{}) // vence justo ahora: cuenta
	seedDeposit(t, m, m.TenantA, requested, contactA, day(11), time.Time{})    // aún no vence
	seedDeposit(t, m, m.TenantA, requested, contactA, day(2), day(4))          // ya recordada
	seedDeposit(t, m, m.TenantA, intakes.StatusDepositPaid, contactA, day(1), time.Time{})
	otherContact := seedDeposit(t, m, m.TenantA, requested, contactB, day(1), time.Time{})
	seedDeposit(t, m, m.TenantB, requested, contactA, day(1), time.Time{})
	before := take(t, m, oldest)

	pending := func(what, contact string, limit int, want ...ref) {
		t.Helper()
		got, err := m.Store.PendingDepositReminders(bg(), m.TenantA, contact, at, limit)
		if err != nil {
			t.Fatalf("PendingDepositReminders (%s): error inesperado %v", what, err)
		}
		if len(got) != len(want) {
			t.Fatalf("PendingDepositReminders (%s): %d solicitudes, quería %d: %+v", what, len(got), len(want), got)
		}
		for i, r := range want {
			if got[i].ID != r.id {
				t.Errorf("PendingDepositReminders (%s): posición %d = %s, quería %s", what, i, got[i].ID, r.id)
			}
			requireSameHeader(t, "PendingDepositReminders ("+what+")", got[i], get(t, m, r).Intake)
		}
	}
	pending("lo más vencido primero", contactA, 10, oldest, middle, edge)
	pending("acotada a 2", contactA, 2, oldest, middle)
	pending("límite 0", contactA, 0)
	pending("límite negativo", contactA, -1)
	pending("el otro contacto", contactB, 10, otherContact)
	pending("un contacto sin señas", "contact-opaque-nobody", 10)
	requireSame(t, "consultar no marca nada", take(t, m, oldest), before)

	if _, won, err := m.Store.MarkDepositReminded(bg(), oldest.tenant, oldest.id, at); err != nil || !won {
		t.Fatalf("MarkDepositReminded de la más vencida: (ganó=%v, err=%v)", won, err)
	}
	pending("tras recordar la más vencida", contactA, 10, middle, edge)
	w.requireUntouched(t, m)
}

// caseExpiryRemindedOnce: la primera llamada gana y escribe la marca; la segunda no. El instante
// EXACTO en que se cumple el plazo ya cuenta. 🔴 UpdatedAt NO se mueve —es la base del plazo: si se
// moviera, la marca se apagaría justo al encender el aviso— y la solicitud sigue en su estado: la
// marca no mata nada.
func caseExpiryRemindedOnce(t *testing.T, m Montaje) {
	w := seedWitnesses(t, m)
	at := day(3).Add(intakes.QuoteDeadline)
	r := seedQuote(t, m, m.TenantA, intakes.StatusPendingApproval, day(3), time.Time{})
	want := take(t, m, r).clone()
	m.Advance(t)

	returned, won, err := m.Store.MarkExpiryReminded(bg(), r.tenant, r.id, at)
	if err != nil || !won {
		t.Fatalf("MarkExpiryReminded en el instante en que se cumple el plazo: (ganó=%v, err=%v), quería ganar", won, err)
	}
	got := take(t, m, r)
	want.detail.ExpiryRemindedAt = at
	requireSame(t, "tras el primer aviso", got, want)
	requireSameHeader(t, "la cabecera devuelta", returned, got.detail.Intake)

	again, won, err := m.Store.MarkExpiryReminded(bg(), r.tenant, r.id, at.Add(48*time.Hour))
	requireNotItsTurn(t, "el segundo aviso", again, won, err)
	requireSame(t, "tras el segundo aviso", take(t, m, r), want)
	w.requireUntouched(t, m)
}

// caseExpiryRemindedNotItsTurn barre el «no procede» del plazo: aún en plazo por un segundo, fuera
// de `pending_approval` (el dueño ya decidió), ya avisada, de otro tenant e inexistente.
func caseExpiryRemindedNotItsTurn(t *testing.T, m Montaje) {
	w := seedWitnesses(t, m)
	at := day(10)
	stale := day(1) // tocada hace nueve días: vencidísima si estuviera esperando al dueño
	for name, r := range map[string]ref{
		"aún en plazo (por un segundo)": seedQuote(t, m, m.TenantA, intakes.StatusPendingApproval, at.Add(-intakes.QuoteDeadline).Add(time.Second), time.Time{}),
		"ya aprobada (legada)":          seedQuote(t, m, m.TenantA, intakes.StatusClosedLegacy, stale, time.Time{}),
		"pendiente de información":      seedQuote(t, m, m.TenantA, intakes.StatusNeedsInfo, stale, time.Time{}),
		"todavía abierta":               seedQuote(t, m, m.TenantA, intakes.StatusOpen, stale, time.Time{}),
		"ya avisada":                    seedQuote(t, m, m.TenantA, intakes.StatusPendingApproval, stale, day(2)),
	} {
		before := take(t, m, r)
		got, won, err := m.Store.MarkExpiryReminded(bg(), r.tenant, r.id, at)
		requireNotItsTurn(t, "presupuesto "+name, got, won, err)
		requireSame(t, "presupuesto "+name, take(t, m, r), before)
	}

	theirs := seedQuote(t, m, m.TenantB, intakes.StatusPendingApproval, stale, time.Time{})
	before := take(t, m, theirs)
	for name, target := range map[string]ref{
		"de otro tenant":    {tenant: m.TenantA, id: theirs.id},
		"inexistente":       {tenant: m.TenantA, id: uuid.NewString()},
		"id que no es UUID": {tenant: m.TenantA, id: "no-soy-un-uuid"},
	} {
		got, won, err := m.Store.MarkExpiryReminded(bg(), target.tenant, target.id, at)
		requireNotItsTurn(t, "presupuesto "+name, got, won, err)
	}
	requireSame(t, "el presupuesto vencido del otro tenant", take(t, m, theirs), before)
	w.requireUntouched(t, m)
}
