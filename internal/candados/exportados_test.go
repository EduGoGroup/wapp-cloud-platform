//go:build pendiente

package candados

import (
	"strings"
	"testing"
)

// TestExportadosCubiertosMuerde: un exportado citado solo en un comentario, o solo en un
// literal de cadena, no está cubierto; un método exportado se nombra "Tipo.Metodo".
func TestExportadosCubiertosMuerde(t *testing.T) {
	fuentes := recorrerCaso(t, "testdata/exportados/muerde", []string{"internal"}, true)
	vs := ExportadosCubiertos(fuentes)

	const fichero = "internal/modulos/m/cosa/cosa.go"
	casos := []struct {
		nombre  string
		simbolo string
	}{
		{"constante citada solo en un comentario del test", "Limite"},
		{"método exportado citado solo en un literal", "Cosa.Medir"},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			exigeViolacion(t, vs, fichero, c.simbolo, "cosa_test.go")
		})
	}
	if len(vs) != len(casos) {
		t.Errorf("se esperaban %d violaciones; hay %d: %v", len(casos), len(vs), vs)
	}
	for _, v := range vs {
		if strings.Contains(v.Motivo, "Hacer") {
			t.Errorf("Hacer aparece como identificador en el test y no debe violar: %v", v)
		}
	}
	exigeOrdenadas(t, vs)
}

// TestExportadosCubiertosPasa: un test en rojo (etiqueta pendiente) del paquete externo que
// nombra todo por selector cuenta; campos y no exportados no se exigen; un x.go sin test no
// es asunto de este candado; un paquete …test queda fuera (D-F1-3 = sí).
func TestExportadosCubiertosPasa(t *testing.T) {
	fuentes := recorrerCaso(t, "testdata/exportados/pasa", []string{"internal"}, true)
	exigeCero(t, ExportadosCubiertos(fuentes))
}
