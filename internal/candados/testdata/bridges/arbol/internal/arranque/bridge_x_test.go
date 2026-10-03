package arranque

import "testing"

// TestBridgeX es el test del adaptador: sale solo si se piden tests.
func TestBridgeX(t *testing.T) {
	if bridgeX(1, 2) != 2 {
		t.Fatal("bridgeX")
	}
}
