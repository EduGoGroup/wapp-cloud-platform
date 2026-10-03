package arranque

import "testing"

// TestBridge es el test de bridge.go, no el de un adaptador: no sale ni pidiendo tests.
func TestBridge(t *testing.T) {
	if bridge() != 2 {
		t.Fatal("bridge")
	}
}
