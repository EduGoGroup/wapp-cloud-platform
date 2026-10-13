package sub

import "testing"

// TestBridgeY tampoco sale: está en el subdirectorio.
func TestBridgeY(t *testing.T) {
	if bridgeY() != 3 {
		t.Fatal("bridgeY")
	}
}
