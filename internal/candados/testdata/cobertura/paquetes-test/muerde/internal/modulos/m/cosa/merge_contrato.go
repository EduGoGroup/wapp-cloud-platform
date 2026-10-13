package cosa

import "testing"

// mergeCases es el MISMO código que cosahelpertest/merge_contrato.go en un paquete que no
// termina en «helpertest»: el sufijo _contrato.go no basta para ser un fichero de suite, así
// que se mide y su 0 % muerde (D-F1-13).
func mergeCases(t *testing.T, nuevo func() *Doble) {
	t.Helper()
	d := nuevo()
	if d.Leer() == d.Leer() {
		t.Error("dos lecturas seguidas no devuelven lo mismo")
	}
}
