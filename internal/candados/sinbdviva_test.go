//go:build pendiente

package candados

import "testing"

// TestSinBDVivaMuerde: cada uno de los patrones prohibidos dispara, nombrado en el motivo;
// las etiquetas no esconden un fichero, y el propio candado se salta.
func TestSinBDVivaMuerde(t *testing.T) {
	fuentes := recorrerCaso(t, "testdata/sinbdviva/muerde", []string{"test/procesos"}, true)
	vs := SinBDViva(fuentes)

	const d = "test/procesos/"
	casos := []struct {
		nombre  string
		fichero string
		patron  string
	}{
		{"literal con la variable de la BD viva", d + "dsn_test.go", "WAPP_TEST_DB_DSN"},
		{"literal con el puerto de Postgres", d + "puerto_test.go", ":5432"},
		{"identificador de reuso de contenedor, con etiqueta integracion", d + "reuso_test.go", "WithReuseByName"},
		{"literal que empieza por postgres://", d + "url_test.go", "postgres://"},
		{"literal crudo que empieza por postgresql://", d + "url_test.go", "postgresql://"},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			exigeViolacion(t, vs, c.fichero, c.patron)
		})
	}
	if len(vs) != len(casos) {
		t.Errorf("se esperaban %d violaciones; hay %d: %v", len(casos), len(vs), vs)
	}
	exigeNingunaEn(t, vs, d+"sin_bd_viva_test.go")
	exigeOrdenadas(t, vs)
}

// TestSinBDVivaPasa: los patrones en comentarios no cuentan, la cadena de
// ctr.ConnectionString es válida y sin_bd_viva_test.go se salta.
func TestSinBDVivaPasa(t *testing.T) {
	fuentes := recorrerCaso(t, "testdata/sinbdviva/pasa", []string{"test/procesos"}, true)
	exigeCero(t, SinBDViva(fuentes))
}
