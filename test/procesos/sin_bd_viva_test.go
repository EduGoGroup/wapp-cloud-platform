package procesos

import (
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/candados"
)

// TestSinBDViva es el candado de F9 (F0 T0.8): ningún test de proceso apunta a un Postgres
// vivo; la única cadena de conexión válida es la del contenedor de la corrida (05 §7.2). Va sin
// etiqueta integracion para correr en ci-local (plan/F0-andamiaje/reglas.md §3), y recorre
// todos los .go del paquete, con o sin etiqueta, tests incluidos (los procesos de F9 son
// _test.go). Los patrones los define candados.SinBDViva, que se salta este fichero por nombre;
// su caso que muerde vive en internal/candados/testdata/sinbdviva/muerde (T0.5).
func TestSinBDViva(t *testing.T) {
	fuentes, err := candados.Recorrer("../..", []string{"test/procesos"}, true)
	if err != nil {
		t.Fatalf("recorrer test/procesos: %v", err)
	}
	// Un recorrido vacío pasaría sin mirar nada: hoy hay al menos doc.go y este fichero.
	if len(fuentes) == 0 {
		t.Fatal("recorridos = 0: el candado no vio ningún fichero de test/procesos")
	}
	t.Logf("recorridos = %d", len(fuentes))
	for _, v := range candados.SinBDViva(fuentes) {
		t.Errorf("%s: %s", v.Fichero, v.Motivo)
	}
}
