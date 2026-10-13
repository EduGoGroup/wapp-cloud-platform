//go:build otraetiqueta

package a

import "testing"

func TestUno(t *testing.T) {
	if Uno() != 1 {
		t.Fatal("uno")
	}
}
