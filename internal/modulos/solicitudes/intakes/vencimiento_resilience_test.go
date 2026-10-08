//go:build pendiente

package intakes

import (
	"context"
	"sync"
	"testing"
	"time"
)

// Lo que el recordatorio del plazo no puede romper: el aviso repetido, la carrera entre
// toques, el recordatorio a medias y el pánico de un colaborador. Los dobles y el escenario
// viven en vencimiento_test.go.

// TestRemindOverdue_RepeatedTouchesNotifyOnce: el dueño refresca su bandeja tres veces con
// la misma página ya leída y el aviso sale UNO. Lo sostiene la marca del store.
func TestRemindOverdue_RepeatedTouchesNotifyOnce(t *testing.T) {
	t.Parallel()
	sc := newExpiryScene(expiryWaiting(expiryIntake, 72*time.Hour))
	touched := []Intake{expiryWaiting(expiryIntake, 72*time.Hour)}

	for range 3 {
		sc.reminder.RemindOverdue(context.Background(), expiryTenant, touched)
	}

	if sc.owner.count() != 1 {
		t.Errorf("avisos al dueño = %d, quería 1", sc.owner.count())
	}
}

// TestRemindOverdue_ConcurrentTouchesNotifyOne: veinte pestañas del dueño que LEYERON LO
// MISMO y refrescan a la vez. Las veinte pasan el pre-filtro y solo una gana la marca. Corre
// con -race en el gate.
func TestRemindOverdue_ConcurrentTouchesNotifyOne(t *testing.T) {
	t.Parallel()
	sc := newExpiryScene(expiryWaiting(expiryIntake, 72*time.Hour))
	touched := []Intake{expiryWaiting(expiryIntake, 72*time.Hour)}

	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() { sc.reminder.RemindOverdue(context.Background(), expiryTenant, touched) })
	}
	wg.Wait()

	if sc.owner.count() != 1 {
		t.Errorf("avisos al dueño = %d, quería exactamente 1", sc.owner.count())
	}
	if sc.store.markCount() != 20 {
		t.Errorf("marcas pedidas = %d, quería 20: la garantía es del store, no del pre-filtro", sc.store.markCount())
	}
}

// TestRemindOverdue_HalfWiredStaysSilent: un recordatorio a medias no avisa, pero tampoco se
// lleva por delante la lectura que lo invocó.
func TestRemindOverdue_HalfWiredStaysSilent(t *testing.T) {
	t.Parallel()
	touched := []Intake{expiryWaiting(expiryIntake, 72*time.Hour)}
	store := &expiryStore{rows: map[string]Intake{expiryIntake: touched[0]}}
	owner := &expiryOwnerSpy{}

	cases := map[string]*ExpiryReminder{
		"nil receiver": nil,
		"no notice":    NewExpiryReminder(nil, store, newExpiryLog()),
		"no store":     NewExpiryReminder(owner, nil, newExpiryLog()),
		"no log":       NewExpiryReminder(owner, store, nil),
	}
	for _, reminder := range cases {
		reminder.RemindOverdue(context.Background(), expiryTenant, touched)
	}
	if store.markCount() != 0 || owner.count() != 0 {
		t.Errorf("un recordatorio a medias hizo %d marcas y %d avisos, quería 0 y 0", store.markCount(), owner.count())
	}
}

// TestRemindOverdue_PanicIsContained: un pánico en el store o en el emisor no convierte la
// bandeja del dueño en un 500, y queda registrado como error.
func TestRemindOverdue_PanicIsContained(t *testing.T) {
	t.Parallel()
	inStore := newExpiryScene(expiryWaiting(expiryIntake, 72*time.Hour))
	inStore.store.panicky = true
	inNotice := newExpiryScene(expiryWaiting(expiryIntake, 72*time.Hour))
	inNotice.owner.panicky = true

	for name, sc := range map[string]*expiryScene{"store": inStore, "notice": inNotice} {
		sc.reminder.RemindOverdue(context.Background(), expiryTenant, []Intake{expiryWaiting(expiryIntake, 72*time.Hour)})
		if !sc.log.has("ERROR") {
			t.Errorf("el pánico del %s no quedó registrado como error: %v", name, sc.log.all())
		}
	}
}
