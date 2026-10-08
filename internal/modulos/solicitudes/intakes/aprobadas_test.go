//go:build pendiente

package intakes

import "testing"

// TestMaxApprovedTexts_Is50: la cota del historial aprobado que se le enseña al modelo.
func TestMaxApprovedTexts_Is50(t *testing.T) {
	t.Parallel()
	if MaxApprovedTexts != 50 {
		t.Errorf("MaxApprovedTexts = %d, quería 50", MaxApprovedTexts)
	}
}
