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

// TestSinBDVivaBordes: un literal que dispara dos patrones da dos violaciones; cada aparición
// cuenta (dos literales iguales, dos violaciones); WithReuseByName suelto y como selector; el
// candado se salta por nombre base en cualquier directorio; salida ordenada.
func TestSinBDVivaBordes(t *testing.T) {
	const d = "test/procesos/"
	fuentes := []Fuente{
		fuenteEnMemoria(t, d+"doble_test.go", `package procesos
// "postgres://localhost:5432" en un comentario no cuenta.
var (
	a = "postgres://localhost:5432/wapp"
	b = "127.0.0.1:5432"
	c = "127.0.0.1:5432"
	e = "no-postgres://x"
)
`),
		fuenteEnMemoria(t, d+"ident_test.go", `package procesos
import tc "github.com/testcontainers/testcontainers-go"
func WithReuseByName() {}
var _ = tc.WithReuseByName
`),
		fuenteEnMemoria(t, d+"sub/sin_bd_viva_test.go", `package sub
var _ = "WAPP_TEST_DB_DSN"
`),
	}
	vs := SinBDViva(fuentes)
	exigeViolacion(t, vs, d+"doble_test.go", "postgres://", "línea 4")
	exigeViolacion(t, vs, d+"doble_test.go", ":5432", "línea 4")
	exigeViolacion(t, vs, d+"doble_test.go", ":5432", "línea 5")
	exigeViolacion(t, vs, d+"doble_test.go", ":5432", "línea 6")
	exigeViolacion(t, vs, d+"ident_test.go", "WithReuseByName", "línea 3")
	exigeViolacion(t, vs, d+"ident_test.go", "WithReuseByName", "línea 4")
	exigeNingunaEn(t, vs, d+"sub/sin_bd_viva_test.go")
	if len(vs) != 6 {
		t.Errorf("se esperaban 6 violaciones; hay %d: %v", len(vs), vs)
	}
	exigeOrdenadas(t, vs)
}

// TestValorLiteral: quita comillas interpretadas y crudas; un texto que no es literal válido
// se juzga tal cual.
func TestValorLiteral(t *testing.T) {
	casos := map[string]string{
		`"a\tb"`:        "a\tb",
		"`postgres://`": "postgres://",
		`"sin cerrar`:   `"sin cerrar`,
	}
	for lit, want := range casos {
		if got := valorLiteral(lit); got != want {
			t.Errorf("valorLiteral(%q) = %q; se esperaba %q", lit, got, want)
		}
	}
}
