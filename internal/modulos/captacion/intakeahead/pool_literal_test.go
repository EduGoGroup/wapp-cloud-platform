package intakeahead

// pool_literal_test.go — lo que el Pool promete sobre sí mismo cuando NO pasa por New ni por
// serve: los defaults «en el uso» del calentamiento (warmup.go) y el reintento sin plazo
// (intakeahead_classify.go). Desde fuera no se ven: New rellena siempre el presupuesto y serve
// acota siempre el ctx. Por eso es un test INTERNO, con el Pool armado con literal de struct.

import (
	"context"
	"testing"
	"time"
)

// TestPoolLiteral_WarmupDefaultsApplyOnUse: un Pool armado con literal se comporta igual que
// uno construido con New. El presupuesto del calentamiento cae a DefaultWarmTimeout —y uno
// puesto a mano se respeta—, y el cerrojo «uno en vuelo por Edge» funciona sin mapa previo: el
// primero lo toma, el segundo del mismo Edge no, y otro Edge sí.
func TestPoolLiteral_WarmupDefaultsApplyOnUse(t *testing.T) {
	p := &Pool{}
	if got := p.warmBudget(); got != DefaultWarmTimeout {
		t.Errorf("warmBudget() de un Pool literal = %v, se esperaba DefaultWarmTimeout (%v)", got, DefaultWarmTimeout)
	}
	if got := (&Pool{warmTimeout: -time.Second}).warmBudget(); got != DefaultWarmTimeout {
		t.Errorf("warmBudget() con presupuesto negativo = %v, se esperaba DefaultWarmTimeout (%v)", got, DefaultWarmTimeout)
	}
	if got := (&Pool{warmTimeout: 7 * time.Second}).warmBudget(); got != 7*time.Second {
		t.Errorf("warmBudget() con 7 s puestos a mano = %v, se esperaban 7 s", got)
	}

	edge := edgeKey{tenantID: "t-1", edgeID: "e-1"}
	if !p.markWarmup(edge) {
		t.Fatal("el primer calentamiento de un Edge no tomó el cerrojo en un Pool literal")
	}
	if p.markWarmup(edge) {
		t.Error("un segundo calentamiento del MISMO Edge tomó el cerrojo con el primero en vuelo")
	}
	if !p.markWarmup(edgeKey{tenantID: "t-1", edgeID: "e-2"}) {
		t.Error("el calentamiento de OTRO Edge no tomó el cerrojo")
	}
}

// TestRetryFits_WithoutDeadlineThereIsNothingToSplit: sin deadline no hay presupuesto que
// repartir y el reintento cabe siempre, por grande que sea el plazo configurado. Con deadline
// manda la media vuelta: cabe si queda al menos la mitad, y no si queda menos.
func TestRetryFits_WithoutDeadlineThereIsNothingToSplit(t *testing.T) {
	p := &Pool{timeout: 24 * time.Hour}
	if !p.retryFits(context.Background()) {
		t.Error("retryFits sin deadline = false; sin plazo no hay nada que agotar y el reintento cabe")
	}

	short, cancelShort := context.WithTimeout(context.Background(), time.Hour)
	defer cancelShort()
	if p.retryFits(short) {
		t.Error("retryFits con 1 h de 24 h = true; queda menos de la mitad del presupuesto")
	}
	whole, cancelWhole := context.WithTimeout(context.Background(), 23*time.Hour)
	defer cancelWhole()
	if !p.retryFits(whole) {
		t.Error("retryFits con 23 h de 24 h = false; queda más de la mitad del presupuesto")
	}
}
