package cosa

import "testing"

// TestHacer cita Limite solo en este comentario, y Medir solo en un literal.
func TestHacer(t *testing.T) {
	_ = Cosa{}
	if Hacer() == 0 {
		t.Fatal("Medir")
	}
}
