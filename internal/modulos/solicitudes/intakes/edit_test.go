package intakes

import "testing"

// El cero de EditMode es EditPlain: el valor por descuido es la edición que ya
// existía, nunca la que estrena conducta.
func TestEditMode_ZeroValueIsPlain(t *testing.T) {
	var mode EditMode
	if mode != EditPlain {
		t.Fatalf("el cero de EditMode es %d; se esperaba EditPlain (%d)", mode, EditPlain)
	}
	if EditAsCorrection == EditPlain {
		t.Fatal("EditAsCorrection y EditPlain son el mismo valor; deben distinguirse")
	}
}
