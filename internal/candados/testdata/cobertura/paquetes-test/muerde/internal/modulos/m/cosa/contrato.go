package cosa

import "testing"

// Contrato es el MISMO código que cosahelpertest/contrato.go en un paquete que no termina en
// «helpertest»: llamarse contrato.go no basta para ser un fichero de suite, así que se mide
// y su 0 % muerde (D-F1-13).
func Contrato(t *testing.T, nuevo func() *Doble) {
	t.Helper()
	if nuevo().Leer() != 1 {
		t.Error("la primera lectura devuelve 1")
	}
}
