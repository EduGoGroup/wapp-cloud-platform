package ingesthelpertest

import "testing"

// TestMemoria_Contrato corre la suite del dedupe contra la Memoria.
func TestMemoria_Contrato(t *testing.T) {
	ContratoDeduper(t, func(t *testing.T) Montaje {
		t.Helper()
		return Montaje{Deduper: NewMemoria(), SessionA: "session-a", SessionB: "session-b"}
	})
}
