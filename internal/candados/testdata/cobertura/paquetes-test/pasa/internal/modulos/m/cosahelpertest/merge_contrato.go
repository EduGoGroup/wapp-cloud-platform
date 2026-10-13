package cosahelpertest

import "testing"

// mergeCases es un trozo por tema de la suite: vive en un fichero <tema>_contrato.go, que
// también es un fichero de suite (D-F1-13). Solo se ejecuta desde otros paquetes, a través de
// Contrato, y en el perfil de ESTE paquete sale al 0 %: no se mide.
func mergeCases(t *testing.T, nuevo func() *Doble) {
	t.Helper()
	d := nuevo()
	if d.Leer() == d.Leer() {
		t.Error("dos lecturas seguidas no devuelven lo mismo")
	}
}
