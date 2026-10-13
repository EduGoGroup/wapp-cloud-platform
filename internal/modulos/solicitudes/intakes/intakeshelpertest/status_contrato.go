package intakeshelpertest

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

// updateStatus aplica el CAS o falla el test; devuelve la cabecera que el puerto devolvió.
func updateStatus(t *testing.T, m Montaje, r ref, to string, expected []string) intakes.Intake {
	t.Helper()
	got, err := m.Store.UpdateStatus(bg(), r.tenant, r.id, to, expected)
	if err != nil {
		t.Fatalf("UpdateStatus(%s → %s): error inesperado %v", r.id, to, err)
	}
	return got
}

// caseUpdateStatusOverLegacy: el CAS casa con la fila legada `closed` por las variantes de
// `confirmed`, escribe la clave TAL CUAL y devuelve la cabecera normalizada. Solo se mueven el
// estado y UpdatedAt.
func caseUpdateStatusOverLegacy(t *testing.T, m Montaje) {
	w := seedWitnesses(t, m)
	r := seed(t, m, m.TenantA, intakes.StatusClosedLegacy, 1, eventCancelled, customerLines()...)
	insertRevision(t, m, r.id, intakes.RevisionKindCart, intakes.RevisionBySystem, "")
	want := take(t, m, r).clone()
	m.Advance(t)

	returned := updateStatus(t, m, r, intakes.StatusSettled, intakes.StoredVariants(intakes.StatusConfirmed))
	got := take(t, m, r)
	want.stored, want.detail.Status = intakes.StatusSettled, intakes.StatusSettled
	adoptRefreshedUpdatedAt(t, "UpdateStatus", got, &want)
	requireSame(t, "tras UpdateStatus", got, want)
	requireSameHeader(t, "la cabecera devuelta", returned, got.detail.Intake)

	// La clave se escribe como llega: pedir la legada la guarda legada, y se lee normalizada.
	legacy := seed(t, m, m.TenantA, intakes.StatusOpen, 2, eventCancelled)
	returned = updateStatus(t, m, legacy, intakes.StatusClosedLegacy, []string{intakes.StatusOpen})
	if stored := m.StoredStatus(t, legacy.tenant, legacy.id); stored != intakes.StatusClosedLegacy {
		t.Errorf("estado almacenado = %q, quería %q (el store no normaliza al escribir)", stored, intakes.StatusClosedLegacy)
	}
	if returned.Status != intakes.StatusConfirmed {
		t.Errorf("Status devuelto = %q, quería confirmed (normalizado)", returned.Status)
	}
	w.requireUntouched(t, m)
}

// caseUpdateStatusConflict: si el estado almacenado no es uno de los esperados, ErrConflict y
// NADA escrito — tampoco la línea de envío que el destino pediría.
func caseUpdateStatusConflict(t *testing.T, m Montaje) {
	w := seedWitnesses(t, m)
	r := seed(t, m, m.TenantA, intakes.StatusOpen, 1, eventCancelled, customerLines()...)
	before := take(t, m, r)
	m.Advance(t)
	for name, expected := range map[string][]string{
		"otro estado": intakes.StoredVariants(intakes.StatusConfirmed),
		"lista vacía": {},
		"lista nil":   nil,
		"otra grafía": {"OPEN"},
	} {
		got, err := m.Store.UpdateStatus(bg(), r.tenant, r.id, intakes.StatusPendingApproval, expected)
		if !errors.Is(err, intakes.ErrConflict) {
			t.Errorf("UpdateStatus con %s: err = %v, quería ErrConflict", name, err)
		}
		if got.ID != "" {
			t.Errorf("UpdateStatus con %s devolvió una cabecera: %+v", name, got)
		}
	}
	requireSame(t, "tras los conflictos", take(t, m, r), before)

	// La secuencia de dos operadores: el primero gana y el segundo, con lo que leyó antes, no.
	updateStatus(t, m, r, intakes.StatusCancelled, []string{intakes.StatusOpen})
	after := take(t, m, r)
	m.Advance(t)
	if _, err := m.Store.UpdateStatus(bg(), r.tenant, r.id, intakes.StatusAbandoned, []string{intakes.StatusOpen}); !errors.Is(err, intakes.ErrConflict) {
		t.Errorf("segundo CAS desde el estado ya movido: err = %v, quería ErrConflict", err)
	}
	requireSame(t, "tras el segundo CAS", take(t, m, r), after)
	w.requireUntouched(t, m)
}

// caseUpdateStatusNotFound: una solicitud de otro tenant no existe (INV-8), aunque su estado sea
// justo el esperado; tampoco una inexistente ni un id que no es UUID.
func caseUpdateStatusNotFound(t *testing.T, m Montaje) {
	w := seedWitnesses(t, m)
	expected := intakes.StoredVariants(intakes.StatusPendingApproval) // el estado de los testigos
	for name, target := range map[string]ref{
		"de otro tenant":    {tenant: m.TenantA, id: w.foreign().id},
		"inexistente":       {tenant: m.TenantA, id: uuid.NewString()},
		"id que no es UUID": {tenant: m.TenantA, id: "no-soy-un-uuid"},
		"tenant sin nada":   {tenant: uuid.NewString(), id: w.foreign().id},
	} {
		got, err := m.Store.UpdateStatus(bg(), target.tenant, target.id, intakes.StatusConfirmed, expected)
		if !errors.Is(err, intakes.ErrNotFound) {
			t.Errorf("UpdateStatus de una solicitud %s: err = %v, quería ErrNotFound", name, err)
		}
		if got.ID != "" {
			t.Errorf("UpdateStatus de una solicitud %s devolvió una cabecera: %+v", name, got)
		}
	}
	w.requireUntouched(t, m)
}

// caseUpdateStatusPendingApproval: entrar a `pending_approval` deja puesta la línea de envío en la
// MISMA escritura, y la cabecera DEVUELTA ya trae el total con ella (la consola pinta ese total
// sin releer). Sin zonas sale «por confirmar» a 0; con una zona, con su tarifa.
func caseUpdateStatusPendingApproval(t *testing.T, m Montaje) {
	w := seedWitnesses(t, m)
	for _, c := range []struct {
		name  string
		zones []intakes.ShippingZone
		line  intakes.Item
		total float64
	}{
		{"sin zonas", nil, intakes.Item{SKU: intakes.ShippingSKU, Label: intakes.ShippingPendingLabel, Qty: 1}, 8},
		{"con una zona", []intakes.ShippingZone{{Code: "prov", Label: "Providencia", Price: 3000}},
			intakes.Item{SKU: intakes.ShippingSKU, Label: "Envío — Providencia", Qty: 1, UnitPrice: 3000}, 3008},
	} {
		m.SetShippingZones(t, m.TenantA, c.zones...)
		r := seed(t, m, m.TenantA, intakes.StatusOpen, 1, eventCancelled, customerLines()...)
		want := take(t, m, r).clone()
		m.Advance(t)

		returned := updateStatus(t, m, r, intakes.StatusPendingApproval, []string{intakes.StatusOpen})
		got := take(t, m, r)
		want.stored, want.detail.Status = intakes.StatusPendingApproval, intakes.StatusPendingApproval
		want.detail.Items = append(want.detail.Items, c.line)
		want.detail.Total = c.total
		adoptAddedAt(t, c.name, got, &want, 3)
		adoptRefreshedUpdatedAt(t, c.name, got, &want)
		requireSame(t, "pending_approval "+c.name, got, want)
		requireSameHeader(t, "la cabecera devuelta "+c.name, returned, got.detail.Intake)
	}
	w.requireUntouched(t, m)
}

// dstSlack es la holgura con la que se acota el plazo de la seña: Postgres suma días de calendario
// en la zona de su sesión y la suite los suma en la del instante que le dio Now; un cambio de hora
// entre las dos fechas las separa, como mucho, esto.
const dstSlack = 2 * time.Hour

// caseUpdateStatusDepositRequested: pedir la seña fija su fecha límite (reloj + los días del
// tenant) en la MISMA escritura del estado y limpia el recordatorio de una seña anterior. Un
// tenant sin configurar no es un error: se le aplica el plazo por defecto.
func caseUpdateStatusDepositRequested(t *testing.T, m Montaje) {
	w := seedWitnesses(t, m)
	request := func(what string, days int) {
		t.Helper()
		in := header(intakes.StatusClosedLegacy, 1, customerLines())
		in.DepositDueAt, in.DepositRemindedAt = day(2), day(3) // el rastro de una seña anterior
		r := seedHeader(t, m, m.TenantA, in, eventCancelled, customerLines()...)
		want := take(t, m, r).clone()
		m.Advance(t)

		lo := m.Now(t)
		returned := updateStatus(t, m, r, intakes.StatusDepositRequested, intakes.StoredVariants(intakes.StatusConfirmed))
		hi := m.Now(t)
		got := take(t, m, r)

		due := got.detail.DepositDueAt
		if due.Before(lo.AddDate(0, 0, days).Add(-dstSlack)) || due.After(hi.AddDate(0, 0, days).Add(dstSlack)) {
			t.Errorf("%s: DepositDueAt = %v, quería el reloj (%v … %v) más %d días", what, due, lo, hi, days)
		}
		want.stored, want.detail.Status = intakes.StatusDepositRequested, intakes.StatusDepositRequested
		want.detail.DepositDueAt = due
		want.detail.DepositRemindedAt = time.Time{}
		adoptRefreshedUpdatedAt(t, what, got, &want)
		requireSame(t, what, got, want)
		requireSameHeader(t, "la cabecera devuelta, "+what, returned, got.detail.Intake)
	}
	request("tenant sin configurar", intakes.DefaultDepositDueDays)
	m.SetDepositTemplate(t, m.TenantA, "Transfiere la seña a la cuenta 123", 5)
	request("tenant con plazo de 5 días", 5)
	w.requireUntouched(t, m)
}

// caseUpdateStatusAbandoned: abandonar cambia el ESTADO y nada más —las líneas y la negociación
// auditada sobreviven—, y el filtro del listado alcanza la clave sin contaminar a la que sigue
// abierta.
func caseUpdateStatusAbandoned(t *testing.T, m Montaje) {
	w := seedWitnesses(t, m)
	alive := seed(t, m, m.TenantA, intakes.StatusOpen, 2, eventCancelled)
	r := seed(t, m, m.TenantA, intakes.StatusOpen, 1, eventCancelled, customerLines()...)
	insertRevision(t, m, r.id, intakes.RevisionKindCart, intakes.RevisionBySystem, "")
	want := take(t, m, r).clone()
	m.Advance(t)

	updateStatus(t, m, r, intakes.StatusAbandoned, intakes.StoredVariants(intakes.StatusOpen))
	got := take(t, m, r)
	want.stored, want.detail.Status = intakes.StatusAbandoned, intakes.StatusAbandoned
	adoptRefreshedUpdatedAt(t, "abandonar", got, &want)
	requireSame(t, "tras abandonar", got, want)

	ids, total := list(t, m, m.TenantA, intakes.Filter{Statuses: []string{intakes.StatusAbandoned}})
	requireIDs(t, "List(abandoned)", ids, total, 1, r.id)
	ids, total = list(t, m, m.TenantA, intakes.Filter{Statuses: []string{intakes.StatusOpen}})
	requireIDs(t, "List(open)", ids, total, 1, alive.id)
	w.requireUntouched(t, m)
}
