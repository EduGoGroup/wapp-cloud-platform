package ingest

import (
	"context"
	"slices"
	"testing"
)

// Test del auxiliar no exportado maybeSweep (nace con el verde, 05 E-4): la guarda de la cadencia
// cero, que los exportados no alcanzan porque WithSweep ignora el cero.

// TestMaybeSweep_ZeroCadenceNeverSweeps: un PostgresDeduper que no pasó por el constructor tiene
// la cadencia a cero. No poda nunca, y no divide por cero.
func TestMaybeSweep_ZeroCadenceNeverSweeps(t *testing.T) {
	fake := newFakeDB()
	d := &PostgresDeduper{db: fake.open(t)}
	for _, id := range []string{"wamid.A", "wamid.B", "wamid.C"} {
		seen, err := d.Seen(context.Background(), "session-1", id)
		if err != nil || seen {
			t.Fatalf("Seen(%q) = (%v, %v), quería (false, nil)", id, seen, err)
		}
	}
	if kinds := fake.kinds(); !slices.Equal(kinds, []string{kindInsert, kindInsert, kindInsert}) {
		t.Errorf("sentencias = %v, quería tres INSERT y ninguna poda", kinds)
	}
}
