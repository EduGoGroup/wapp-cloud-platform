package memory_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/infra/memory"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out/outhelpertest"
)

// Las firmas del doble.
var (
	_ out.AuditRepo = (*memory.AuditStore)(nil)

	_ func() *memory.AuditStore                                                                = memory.NewAuditStore
	_ func(*memory.AuditStore, func() time.Time) *memory.AuditStore                            = (*memory.AuditStore).WithClock
	_ func(*memory.AuditStore, context.Context, domain.AuditEvent) error                       = (*memory.AuditStore).Record
	_ func(*memory.AuditStore, context.Context, string, int, int) ([]domain.AuditEvent, error) = (*memory.AuditStore).List
	_ func(*memory.AuditStore) []domain.AuditEvent                                             = (*memory.AuditStore).Events
)

// clockStart es el primer instante de tickingClock: fijo, para que ningún test dependa del reloj
// real.
var clockStart = time.Date(2026, time.October, 4, 12, 0, 0, 0, time.UTC)

// tickingClock devuelve un reloj de prueba que avanza un milisegundo en cada lectura: dos
// escrituras seguidas nunca comparten instante, como dos sentencias seguidas en Postgres. Es
// seguro para uso concurrente.
func tickingClock() func() time.Time {
	var mu sync.Mutex
	ticks := 0
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		ticks++
		return clockStart.Add(time.Duration(ticks) * time.Millisecond)
	}
}

// auditMontaje monta la suite sobre un AuditStore.
func auditMontaje(store *memory.AuditStore) outhelpertest.MontajeAuditRepo {
	return outhelpertest.MontajeAuditRepo{Repo: store, TenantA: uuid.NewString(), TenantB: uuid.NewString()}
}

// TestAuditStore_Contrato corre la suite del puerto contra el doble, con el reloj de prueba.
func TestAuditStore_Contrato(t *testing.T) {
	outhelpertest.ContratoAuditRepo(t, func(*testing.T) outhelpertest.MontajeAuditRepo {
		return auditMontaje(memory.NewAuditStore().WithClock(tickingClock()))
	})
}

// TestAuditStore_ClockAndEvents: el instante de un evento sin fecha sale del reloj inyectado, uno
// con fecha la conserva, y Events devuelve todo en orden de registro, pre-auth incluidos.
func TestAuditStore_ClockAndEvents(t *testing.T) {
	stamp := time.Date(2020, time.January, 1, 0, 0, 0, 0, time.UTC)
	store := memory.NewAuditStore().WithClock(func() time.Time { return clockStart }).WithClock(nil)
	tenant := uuid.NewString()
	for _, e := range []domain.AuditEvent{
		{TenantID: &tenant, Action: "sin-fecha"},
		{Action: "pre-auth", At: stamp},
	} {
		if err := store.Record(context.Background(), e); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
	events := store.Events()
	if len(events) != 2 || events[0].Action != "sin-fecha" || events[1].Action != "pre-auth" {
		t.Fatalf("Events = %+v, quiero los dos en orden de registro", events)
	}
	if !events[0].At.Equal(clockStart) {
		t.Fatalf("At del evento sin fecha = %v, quiero el del reloj inyectado %v (WithClock(nil) no lo cambia)", events[0].At, clockStart)
	}
	if !events[1].At.Equal(stamp) || events[0].ID != 1 || events[1].ID != 2 {
		t.Fatalf("Events = %+v, quiero el instante propio conservado e IDs 1 y 2", events)
	}
}
