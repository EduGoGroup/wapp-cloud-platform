package cosatest

import "testing"

// Contrato es la suite del puerto: solo la ejecutan los tests de las implementaciones, desde
// otros paquetes; en el perfil de ESTE paquete sale al 0 %. D-F1-6 la deja fuera.
func Contrato(t *testing.T, nuevo func() *Doble) {
	t.Helper()
	if nuevo().Leer() != 1 {
		t.Error("la primera lectura devuelve 1")
	}
}
