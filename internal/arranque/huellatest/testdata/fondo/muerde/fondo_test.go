package fondo

import "testing"

// TestNoCuenta: ni sus go ni sus ganchos entran en la huella.
func TestNoCuenta(t *testing.T) {
	m := &Metricas{}
	go m.RegisterDBStats()
	_ = m.InstrumentHTTP(nil)
}
