package cosahelpertest

import "testing"

// mergeCases es un trozo por tema de la suite. Vive en un fichero <tema>_contrato.go de un
// paquete …helpertest: es un fichero de suite, sale al 0 % en el perfil de su paquete y NO se
// mide (D-F1-13).
func mergeCases(t *testing.T, nuevo func() *Doble) {
	t.Helper()
	d := nuevo()
	if d.Leer() == d.Leer() {
		t.Error("dos lecturas seguidas no devuelven lo mismo")
	}
}
