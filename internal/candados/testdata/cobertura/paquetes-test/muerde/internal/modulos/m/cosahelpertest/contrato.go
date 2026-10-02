package cosahelpertest

import "testing"

// Contrato es la suite del puerto. Es un FICHERO DE SUITE: se llama contrato.go y su paquete
// termina en «helpertest». Solo se ejecuta desde otros paquetes, sale al 0 % en el perfil del
// suyo y NO se mide (D-F1-13).
func Contrato(t *testing.T, nuevo func() *Doble) {
	t.Helper()
	if nuevo().Leer() != 1 {
		t.Error("la primera lectura devuelve 1")
	}
}
