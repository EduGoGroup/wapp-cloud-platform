package cosatest

import "testing"

// Contrato es el MISMO código que cosahelpertest/contrato.go en un paquete con el sufijo
// VIEJO «test» a secas: no es un paquete de suite (D-F1-10), así que llamarse contrato.go no
// lo exime: se mide y su 0 % muerde.
func Contrato(t *testing.T, nuevo func() *Doble) {
	t.Helper()
	if nuevo().Leer() != 1 {
		t.Error("la primera lectura devuelve 1")
	}
}
