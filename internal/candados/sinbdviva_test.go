package candados

import (
	"strings"
	"testing"
)

// TestSinBDVivaMuerde: cada uno de los patrones prohibidos dispara, nombrado en el motivo;
// las etiquetas no esconden un fichero, y el propio candado se salta. veces es el número de
// apariciones del patrón en el fichero: cada aparición es una violación (skip_test.go tiene
// tres llamadas Skip*, así que t.Skip da tres).
func TestSinBDVivaMuerde(t *testing.T) {
	fuentes := recorrerCaso(t, "testdata/sinbdviva/muerde", []string{"test/procesos"}, true)
	vs := SinBDViva(fuentes)

	const d = "test/procesos/"
	casos := []struct {
		nombre  string
		fichero string
		patron  string
		veces   int
	}{
		{"literal con la variable de la BD viva", d + "dsn_test.go", "WAPP_TEST_DB_DSN", 1},
		{"literal con el puerto de Postgres", d + "puerto_test.go", ":5432", 1},
		{"identificador de reuso de contenedor, con etiqueta integracion", d + "reuso_test.go", "WithReuseByName", 1},
		{"literal que empieza por postgres://", d + "url_test.go", "postgres://", 1},
		{"literal crudo que empieza por postgresql://", d + "url_test.go", "postgresql://", 1},
		{"os.Environ() hereda el entorno del shell", d + "environ_test.go", "os.Environ", 1},
		{"t.Skip, t.SkipNow y t.Skipf, con etiqueta integracion", d + "skip_test.go", "t.Skip", 3},
		{"testing.Short() como pretexto para saltar, con etiqueta integracion", d + "skip_test.go", "testing.Short", 1},
	}
	esperadas := 0
	for _, c := range casos {
		esperadas += c.veces
		t.Run(c.nombre, func(t *testing.T) {
			exigeViolacion(t, vs, c.fichero, c.patron)
			if got := contarPatron(vs, c.fichero, c.patron); got != c.veces {
				t.Errorf("%s: %d violaciones de %q; se esperaban %d", c.fichero, got, c.patron, c.veces)
			}
		})
	}
	if len(vs) != esperadas {
		t.Errorf("se esperaban %d violaciones; hay %d: %v", esperadas, len(vs), vs)
	}
	exigeNingunaEn(t, vs, d+"sin_bd_viva_test.go")
	exigeOrdenadas(t, vs)
}

// contarPatron cuenta las violaciones de fichero cuyo Motivo empieza por el patrón (el motivo
// es "<patrón> en la línea N: ..."): así «t.Skip» no cuenta las de otro patrón que lo cite.
func contarPatron(vs []Violacion, fichero, patron string) int {
	n := 0
	for _, v := range vs {
		if v.Fichero == fichero && strings.HasPrefix(v.Motivo, patron+" en la línea ") {
			n++
		}
	}
	return n
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

// TestSinBDVivaEntornoYSkip: os.Environ, Skip* y testing.Short disparan con su línea; Skip*
// dispara con CUALQUIER receptor (t, b, tb, un campo) y también como valor de método sin
// llamar; en cambio no disparan lo que se parece sin serlo (os.Getenv, otro Environ, otro
// Short, t.Helper, un Skip suelto sin receptor ni un nombre que solo contiene «Skip») ni lo que
// viene en comentarios o en literales de cadena.
func TestSinBDVivaEntornoYSkip(t *testing.T) {
	const d = "test/procesos/"
	fuentes := []Fuente{
		fuenteEnMemoria(t, d+"dispara_test.go", `package procesos
import ("os"; "testing")
func f(t *testing.T, b *testing.B, tb testing.TB, s struct{ T *testing.T }) {
	_ = os.Environ
	t.Skip("a")
	b.SkipNow()
	tb.Skipf("c")
	s.T.Skip()
	saltar := t.Skip
	_ = saltar
	if testing.Short() {
		return
	}
	_ = append(os.Environ(), "X=1")
}
`),
		fuenteEnMemoria(t, d+"no_dispara_test.go", `package procesos
import ("os"; "testing")
// os.Environ(), t.Skip, t.SkipNow, t.Skipf y testing.Short() en un comentario no cuentan.
type otro struct{}
func (otro) Environ() []string { return nil }
func (otro) Short() bool       { return false }
func (otro) Skipper()          {}
func (otro) NoSkip()           {}
func Skip()                    {}
func g(t *testing.T, o otro) {
	t.Helper()
	t.Log("t.Skip y testing.Short() y os.Environ() solo en una cadena")
	_ = os.Getenv("WAPP_PROCESOS_BINARIO")
	_ = o.Environ()
	_ = o.Short()
	o.Skipper()
	o.NoSkip()
	Skip()
	_ = testing.Verbose()
	_ = os.Args
}
`),
	}
	vs := SinBDViva(fuentes)
	exigeViolacion(t, vs, d+"dispara_test.go", "os.Environ en la línea 4")
	exigeViolacion(t, vs, d+"dispara_test.go", "t.Skip en la línea 5")
	exigeViolacion(t, vs, d+"dispara_test.go", "t.Skip en la línea 6")
	exigeViolacion(t, vs, d+"dispara_test.go", "t.Skip en la línea 7")
	exigeViolacion(t, vs, d+"dispara_test.go", "t.Skip en la línea 8")
	exigeViolacion(t, vs, d+"dispara_test.go", "t.Skip en la línea 9")
	exigeViolacion(t, vs, d+"dispara_test.go", "testing.Short en la línea 11")
	exigeViolacion(t, vs, d+"dispara_test.go", "os.Environ en la línea 14")
	exigeNingunaEn(t, vs, d+"no_dispara_test.go")
	if len(vs) != 8 {
		t.Errorf("se esperaban 8 violaciones; hay %d: %v", len(vs), vs)
	}
	exigeOrdenadas(t, vs)
}

// TestSinBDVivaRazon: el motivo conserva su estilo «<patrón> en la línea N: <razón>» y la
// razón es la del patrón: los de conexión siguen explicando ctr.ConnectionString, los nuevos
// citan E-5 (SKIP) o el entorno armado desde cero, y ninguna razón nombra OTRO patrón (así
// contarPatron y exigeViolacion no se confunden).
func TestSinBDVivaRazon(t *testing.T) {
	casos := []struct {
		patron string
		razon  string
	}{
		{"WAPP_TEST_DB_DSN", "ctr.ConnectionString"},
		{":5432", "ctr.ConnectionString"},
		{"postgres://", "ctr.ConnectionString"},
		{"postgresql://", "ctr.ConnectionString"},
		{"WithReuseByName", "ctr.ConnectionString"},
		{"os.Environ", "desde cero"},
		{"t.Skip", "E-5"},
		{"testing.Short", "E-5"},
	}
	patrones := []string{"WAPP_TEST_DB_DSN", ":5432", "postgres://", "postgresql://",
		"WithReuseByName", "os.Environ", "t.Skip", "testing.Short"}
	for _, c := range casos {
		t.Run(c.patron, func(t *testing.T) {
			razon := razonBDViva(c.patron)
			if !strings.Contains(razon, c.razon) {
				t.Errorf("razonBDViva(%q) = %q; debe contener %q", c.patron, razon, c.razon)
			}
			for _, otro := range patrones {
				if otro != c.patron && strings.Contains(razon, otro) {
					t.Errorf("la razón de %q nombra otro patrón %q: %q", c.patron, otro, razon)
				}
			}
		})
	}
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
