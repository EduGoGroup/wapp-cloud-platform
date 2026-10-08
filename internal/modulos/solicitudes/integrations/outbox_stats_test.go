package integrations_test

import (
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/integrations"
)

// outbox_stats.go solo lleva el tipo OutboxCounts (la consulta que lo rellena, CountOutbox, está en
// postgres.go por D-F6-6 y la afirma postgres_tenant_test.go): no tiene lógica y nació en verde.

// TestOutboxCounts_ZeroIsAnEmptyQueue: el valor cero es «no hay nada en la cola»: cuatro
// contadores a cero y NINGUNA fecha (no hay «la más vieja de ninguna»). El tipo es comparable, que
// es como sus consumidores distinguen la cola vacía.
func TestOutboxCounts_ZeroIsAnEmptyQueue(t *testing.T) {
	var empty integrations.OutboxCounts
	if empty.Pending != 0 || empty.Delivering != 0 || empty.Delivered != 0 || empty.Dead != 0 {
		t.Errorf("el valor cero trae contadores: %+v", empty)
	}
	if !empty.OldestPendingAt.IsZero() {
		t.Errorf("el valor cero trae una antigüedad: %v", empty.OldestPendingAt)
	}
	if empty != (integrations.OutboxCounts{}) {
		t.Error("dos colas vacías no son iguales")
	}
}

// TestOutboxCounts_CarriesFourCountersAndTheOldestWait: una foto de la cola son los cuatro
// contadores, uno por estado, y la fecha de lo más viejo que espera; dos fotos con los mismos
// cinco valores son iguales y una que difiere en cualquiera, no.
func TestOutboxCounts_CarriesFourCountersAndTheOldestWait(t *testing.T) {
	oldest := time.Date(2026, 10, 8, 6, 0, 0, 0, time.UTC)
	base := integrations.OutboxCounts{Pending: 3, Delivering: 1, Delivered: 40, Dead: 2, OldestPendingAt: oldest}
	if base != (integrations.OutboxCounts{Pending: 3, Delivering: 1, Delivered: 40, Dead: 2, OldestPendingAt: oldest}) {
		t.Errorf("dos fotos con los mismos valores no son iguales: %+v", base)
	}
	different := []integrations.OutboxCounts{
		{Pending: 4, Delivering: 1, Delivered: 40, Dead: 2, OldestPendingAt: oldest},
		{Pending: 3, Delivering: 2, Delivered: 40, Dead: 2, OldestPendingAt: oldest},
		{Pending: 3, Delivering: 1, Delivered: 41, Dead: 2, OldestPendingAt: oldest},
		{Pending: 3, Delivering: 1, Delivered: 40, Dead: 3, OldestPendingAt: oldest},
		{Pending: 3, Delivering: 1, Delivered: 40, Dead: 2, OldestPendingAt: oldest.Add(time.Hour)},
	}
	for i, other := range different {
		if other == base {
			t.Errorf("la foto %d difiere en un campo y sale igual a la base", i)
		}
	}
}
