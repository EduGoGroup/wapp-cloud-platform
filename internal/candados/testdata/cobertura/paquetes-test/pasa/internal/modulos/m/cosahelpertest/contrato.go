package cosahelpertest

import "testing"

// Contrato es la suite del puerto: solo la ejecutan los tests de las implementaciones, desde
// otros paquetes; en el perfil de ESTE paquete sale al 0 %. Es un FICHERO DE SUITE (se llama
// contrato.go y su paquete termina en «helpertest»): lo único que D-F1-13 deja sin medir.
func Contrato(t *testing.T, nuevo func() *Doble) {
	t.Helper()
	if nuevo().Leer() != 1 {
		t.Error("la primera lectura devuelve 1")
	}
}
