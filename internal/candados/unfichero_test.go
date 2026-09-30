//go:build pendiente

package candados

import "testing"

// TestUnFicheroUnTestMuerde: sin test y sin excepción verificada, cada x.go es una
// violación que nombra el x_test.go que falta.
func TestUnFicheroUnTestMuerde(t *testing.T) {
	fuentes := recorrerCaso(t, "testdata/unfichero/muerde", []string{"internal"}, true)
	vs := UnFicheroUnTest(fuentes)

	const m = "internal/modulos/m/"
	casos := []struct {
		nombre  string
		fichero string
		falta   string
	}{
		{"fichero sin test al lado", m + "huerfano/huerfano.go", "falta huerfano_test.go"},
		{"un «puerto» con una función ya no es solo de interfaces, aunque tenga suite", m + "puerto/puerto.go", "falta puerto_test.go"},
		{"un puerto sin suite Contrato en <paquete>test necesita test", m + "sinsuite/sinsuite.go", "falta sinsuite_test.go"},
		{"un doc.go con una declaración necesita test", m + "documento/doc.go", "falta doc_test.go"},
		{"un fichero de //go:embed con otra var necesita test", m + "embebido/embed.go", "falta embed_test.go"},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			exigeViolacion(t, vs, c.fichero, c.falta)
		})
	}
	if len(vs) != len(casos) {
		t.Errorf("se esperaban %d violaciones; hay %d: %v", len(casos), len(vs), vs)
	}
	// La suite de un paquete …test no necesita test (D-F1-3 = sí).
	exigeNingunaEn(t, vs, m+"puerto/puertotest/contrato.go")
	exigeOrdenadas(t, vs)
}

// TestUnFicheroUnTestPasa: con test al lado, doc.go solo con su comentario, ficheros solo de
// //go:embed (también en grupo), un puerto con su suite Contrato, y un paquete …test entero
// (incluido un doble con lógica, D-F1-3 = sí): cero violaciones.
func TestUnFicheroUnTestPasa(t *testing.T) {
	fuentes := recorrerCaso(t, "testdata/unfichero/pasa", []string{"internal"}, true)
	exigeCero(t, UnFicheroUnTest(fuentes))
}

// TestUnFicheroUnTestSoloEntreFuentes: el test se busca solo entre fuentes; sin los tests
// (incluirTests = false), el fichero que sí lo tiene en disco muerde.
func TestUnFicheroUnTestSoloEntreFuentes(t *testing.T) {
	fuentes := recorrerCaso(t, "testdata/unfichero/pasa", []string{"internal"}, false)
	vs := UnFicheroUnTest(fuentes)
	exigeViolacion(t, vs, "internal/modulos/m/con/con.go", "falta con_test.go")
}
