package intakes

import "testing"

// El cero de DiscardOutcome no afirma nada: ni descartó, ni hay evento vivo.
func TestDiscardOutcome_ZeroValueClaimsNothing(t *testing.T) {
	var outcome DiscardOutcome
	if outcome.Discarded || outcome.LiveEvent || outcome.Status != "" {
		t.Fatalf("el cero de DiscardOutcome afirma algo: %+v", outcome)
	}
}
