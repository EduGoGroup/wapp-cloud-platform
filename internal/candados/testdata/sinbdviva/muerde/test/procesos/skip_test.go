//go:build integracion

package procesos

import "testing"

func TestSaltado(t *testing.T) {
	t.Skip("sin Docker en esta máquina")
	t.SkipNow()
	t.Skipf("falta %s", "el binario")
	testing.Short()
}
