package candados

import (
	"strings"
	"testing"
)

// TestSinBDVivaMuerde: cada uno de los patrones prohibidos dispara, nombrado en el motivo;
// las etiquetas no esconden un fichero, y el propio candado se salta. veces es el número de
// apariciones del patrón en el fichero: cada aparición es una violación (skip_test.go tiene
// tres llamadas Skip*, así que t.Skip da tres). Un alias o un import de punto de os o de
// testing no esconde el patrón, y el motivo lo sigue nombrando «os.Environ» / «testing.Short».
//
// Desde D-F9-6 (Jhoan, 2026-10-02) muerde además toda APERTURA de conexión fuera de la lista
// blanca (test/procesos/base_test.go), diga lo que diga su cadena: son las evasiones de la
// contradicción 22 del README de F9 que la lista negra de literales no veía (DSN vacío o
// clave=valor, literales construidos, os.Getenv de la cadena de fuera), más el alias, el
// import de punto y las otras puertas de las mismas librerías. La lista blanca y la
// auto-exención son rutas EXACTAS: sub/base_test.go y sub/sin_bd_viva_test.go muerden; y cada
// una exime de SU regla: base_test.go sigue bajo la lista negra, y el candado, bajo la blanca.
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
		{"aliased os import: e.Environ()", d + "alias_test.go", "os.Environ", 1},
		{"aliased testing import: tt.Short()", d + "alias_test.go", "testing.Short", 1},
		{"dot import of os: bare Environ()", d + "dot_import_test.go", "os.Environ", 1},
		{"dot import of testing: bare Short()", d + "dot_import_test.go", "testing.Short", 1},
		{"empty DSN: pgx fills host and port from PGHOST/PGPORT", d + "open_empty_dsn_test.go", "pgx.Connect", 1},
		{"DSN without host or port through database/sql", d + "open_empty_dsn_test.go", "sql.Open", 1},
		{"key=value DSN: the port without the colon", d + "open_keyvalue_dsn_test.go", "pgxpool.New", 1},
		{"URL built from pieces and net.JoinHostPort", d + "open_built_literal_test.go", "pgx.Connect", 1},
		{"config parsed from a built literal", d + "open_built_literal_test.go", "pgx.ConnectConfig", 1},
		{"os.Getenv of an outside connection string", d + "open_getenv_test.go", "sql.Open", 1},
		{"os.Getenv of PGHOST", d + "open_getenv_test.go", "pgx.Connect", 1},
		{"aliased database/sql: db.Open", d + "open_alias_test.go", "sql.Open", 1},
		{"aliased pgx: p.Connect", d + "open_alias_test.go", "pgx.Connect", 1},
		{"aliased pgxpool: pool.NewWithConfig", d + "open_alias_test.go", "pgxpool.NewWithConfig", 1},
		{"dot import of pgx: bare Connect()", d + "open_dot_import_test.go", "pgx.Connect", 1},
		{"low-level pgconn connection", d + "open_other_openers_test.go", "pgconn.Connect", 1},
		{"stdlib.OpenDB builds a *sql.DB without sql.Open", d + "open_other_openers_test.go", "stdlib.OpenDB", 1},
		{"sql.OpenDB over a connector", d + "open_other_openers_test.go", "sql.OpenDB", 1},
		{"the stdlib connector", d + "open_other_openers_test.go", "stdlib.GetConnector", 1},
		{"the stdlib driver opens by method", d + "open_other_openers_test.go", "stdlib.GetDefaultDriver", 1},
		{"the opener taken as a value and called later", d + "open_other_openers_test.go", "pgx.Connect", 1},
		{"self-exemption is an exact path: sub/sin_bd_viva_test.go bites", d + "sub/sin_bd_viva_test.go", "WAPP_TEST_DB_DSN", 1},
		{"the allowlist is an exact path: sub/base_test.go bites", d + "sub/base_test.go", "pgx.Connect", 1},
		{"the allowlisted file stays under the denylist", d + "base_test.go", ":5432", 1},
		{"the lock itself may not open a connection", d + "sin_bd_viva_test.go", "pgx.Connect", 1},
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
	// Lo que cada exención SÍ cubre: los cinco literales que el candado nombra no muerden en
	// su fichero (solo su apertura), y la apertura de base_test.go no muerde (solo su literal).
	if got := countInFile(vs, d+"sin_bd_viva_test.go"); got != 1 {
		t.Errorf("sin_bd_viva_test.go: %d violaciones; se esperaba 1 (su apertura): %v", got, vs)
	}
	if got := countInFile(vs, d+"base_test.go"); got != 1 {
		t.Errorf("base_test.go: %d violaciones; se esperaba 1 (su literal): %v", got, vs)
	}
	exigeOrdenadas(t, vs)
}

// countInFile cuenta las violaciones que nombran file, sean del patrón que sean.
func countInFile(vs []Violacion, file string) int {
	n := 0
	for _, v := range vs {
		if v.Fichero == file {
			n++
		}
	}
	return n
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
// ctr.ConnectionString es válida y sin_bd_viva_test.go se salta. Tampoco muerde un alias de
// os o de testing que no llega a Environ ni a Short (alias_getenv_test.go), ni un método
// propio llamado Environ o Short en un fichero sin import de punto (own_methods_test.go).
// Y desde D-F9-6: base_test.go, el fichero de la lista blanca, abre conexiones con pgx.Connect
// y sql.Open sin morder; y usar lo que otro abrió (tipos y errores de database/sql y de pgx),
// un método propio llamado Connect, Open o New y os.Open tampoco muerden (db_user_test.go).
func TestSinBDVivaPasa(t *testing.T) {
	fuentes := recorrerCaso(t, "testdata/sinbdviva/pasa", []string{"test/procesos"}, true)
	exigeCero(t, SinBDViva(fuentes))
}

// TestSinBDVivaBordes: un literal que dispara dos patrones da dos violaciones; cada aparición
// cuenta (dos literales iguales, dos violaciones); WithReuseByName suelto y como selector; el
// candado se salta SOLO por su ruta exacta (D-F9-6: hasta entonces era por nombre base, y un
// sin_bd_viva_test.go en cualquier subdirectorio quedaba sin mirar); salida ordenada.
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
		fuenteEnMemoria(t, d+"sin_bd_viva_test.go", `package procesos
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
	exigeViolacion(t, vs, d+"sub/sin_bd_viva_test.go", "WAPP_TEST_DB_DSN", "línea 2")
	exigeNingunaEn(t, vs, d+"sin_bd_viva_test.go")
	if len(vs) != 7 {
		t.Errorf("se esperaban 7 violaciones; hay %d: %v", len(vs), vs)
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

// TestSinBDVivaAliasAndDotImports: os.Environ y testing.Short se persiguen por el paquete, no
// por el nombre con que el fichero lo importa. Con alias dispara <alias>.Environ y
// <alias>.Short; con import de punto, el identificador suelto Environ / Short; en los dos
// casos llamado o como valor, y el motivo sigue nombrando «os.Environ» / «testing.Short». El
// literal os / testing dispara siempre, lo importe el fichero como lo importe. No dispara el
// import en blanco (ni siquiera hace de «_» un receptor: _.Environ no compila, pero parsea,
// y es la única forma de observar que no añadió nada), el alias de OTRO paquete, ni el punto
// de un paquete para el nombre del otro. Con import de punto presente, un selector ajeno (x.Environ, x.Short) NO dispara y
// os.Environ cuenta una sola vez, porque el Sel de un selector no es un identificador suelto;
// en cambio DECLARAR ahí algo llamado Environ o Short (un método propio) sí dispara: el
// candado no resuelve tipos y prefiere morder de más.
func TestSinBDVivaAliasAndDotImports(t *testing.T) {
	const ruta = "test/procesos/caso_test.go"
	casos := []struct {
		name string
		src  string
		want []string
	}{
		{
			"aliased os bites called and as a value",
			`package procesos
import e "os"
var a = e.Environ()
var b = e.Environ
`,
			[]string{"os.Environ en la línea 3", "os.Environ en la línea 4"},
		},
		{
			"aliased testing bites called and as a value",
			`package procesos
import tt "testing"
var a = tt.Short()
var b = tt.Short
`,
			[]string{"testing.Short en la línea 3", "testing.Short en la línea 4"},
		},
		{
			"alias with a raw string import path",
			"package procesos\nimport e `os`\nvar a = e.Environ()\n",
			[]string{"os.Environ en la línea 3"},
		},
		{
			"two aliases of the same package both bite",
			`package procesos
import (
	a "os"
	b "os"
)
var x = a.Environ()
var y = b.Environ()
`,
			[]string{"os.Environ en la línea 6", "os.Environ en la línea 7"},
		},
		{
			"the literal names keep biting next to an alias",
			`package procesos
import (
	e "os"
	tt "testing"
)
var x = os.Environ()
var y = testing.Short()
var z = e.Getenv("UNA")
var w = tt.Verbose()
`,
			[]string{"os.Environ en la línea 6", "testing.Short en la línea 7"},
		},
		{
			"dot import of os: bare Environ called",
			`package procesos
import . "os"
var a = Environ()
`,
			[]string{"os.Environ en la línea 3"},
		},
		{
			"dot import of os: bare Environ as a value",
			`package procesos
import . "os"
var a = Environ
`,
			[]string{"os.Environ en la línea 3"},
		},
		{
			"dot import of testing: bare Short called and as a value",
			`package procesos
import . "testing"
var a = Short()
var b = Short
`,
			[]string{"testing.Short en la línea 3", "testing.Short en la línea 4"},
		},
		{
			"dot import present: os.Environ and testing.Short count once, not twice",
			`package procesos
import (
	"os"
	. "os"
	"testing"
	. "testing"
)
var a = os.Environ()
var b = testing.Short()
`,
			[]string{"os.Environ en la línea 8", "testing.Short en la línea 9"},
		},
		{
			"dot import present: a foreign selector does not bite",
			`package procesos
import (
	. "os"
	. "testing"
	"example.com/fake"
)
func f(s fake.Server, w struct{ S fake.Server }) {
	_ = s.Environ()
	_ = s.Short()
	_ = s.Environ
	_ = w.S.Environ()
	_ = fake.New().Short()
}
`,
			nil,
		},
		{
			"dot import present: declaring a method named Environ or Short bites (bite too much)",
			`package procesos
import (
	. "os"
	. "testing"
)
type own struct{}
func (own) Environ() []string { return nil }
func (own) Short() bool { return false }
`,
			[]string{"os.Environ en la línea 7", "testing.Short en la línea 8"},
		},
		{
			"the dot import of one package does not bite the name of the other",
			`package procesos
import . "os"
func Short() bool { return false }
var a = Short()
var b = Getenv("UNA")
`,
			nil,
		},
		{
			"the dot import of testing does not bite a bare Environ",
			`package procesos
import . "testing"
func Environ() []string { return nil }
var a = Environ()
var b = Verbose()
`,
			nil,
		},
		{
			"blank imports add nothing",
			`package procesos
import (
	_ "os"
	_ "testing"
)
func Environ() []string { return nil }
func Short() bool { return false }
var a = Environ()
var b = Short()
var c = _.Environ()
var d = _.Short()
`,
			nil,
		},
		{
			"an alias of another package is not a receiver",
			`package procesos
import (
	e "example.com/env"
	tt "example.com/testing"
	. "example.com/os"
)
var a = e.Environ()
var b = tt.Short()
var c = Environ()
var d = Short()
`,
			nil,
		},
		{
			"the alias of os is not a receiver for Short, nor the alias of testing for Environ",
			`package procesos
import (
	e "os"
	tt "testing"
)
var a = e.Short()
var b = tt.Environ()
var c = e.Getenv("UNA")
`,
			nil,
		},
	}
	for _, c := range casos {
		t.Run(c.name, func(t *testing.T) {
			vs := SinBDViva([]Fuente{fuenteEnMemoria(t, ruta, c.src)})
			for _, w := range c.want {
				exigeViolacion(t, vs, ruta, w)
			}
			if len(vs) != len(c.want) {
				t.Errorf("se esperaban %d violaciones; hay %d: %v", len(c.want), len(vs), vs)
			}
			exigeOrdenadas(t, vs)
		})
	}
}

// TestSinBDVivaImportsPerFile: el alias y el import de punto valen SOLO en el fichero que los
// declara. El mismo nombre «e» es os en un fichero y otro paquete en el siguiente, y el
// Environ suelto del fichero que no tiene import de punto es una función propia: ninguno de
// los dos hereda lo que resolvió el fichero anterior.
func TestSinBDVivaImportsPerFile(t *testing.T) {
	const d = "test/procesos/"
	fuentes := []Fuente{
		fuenteEnMemoria(t, d+"a_alias_test.go", `package procesos
import e "os"
var a = e.Environ()
`),
		fuenteEnMemoria(t, d+"b_other_alias_test.go", `package procesos
import e "example.com/env"
var b = e.Environ()
`),
		fuenteEnMemoria(t, d+"c_dot_test.go", `package procesos
import (
	. "os"
	. "testing"
)
var c = Environ()
var d = Short()
`),
		fuenteEnMemoria(t, d+"d_no_dot_test.go", `package procesos
func Environ() []string { return nil }
func Short() bool { return false }
var e = Environ()
var f = Short()
`),
	}
	vs := SinBDViva(fuentes)
	exigeViolacion(t, vs, d+"a_alias_test.go", "os.Environ en la línea 3")
	exigeNingunaEn(t, vs, d+"b_other_alias_test.go")
	exigeViolacion(t, vs, d+"c_dot_test.go", "os.Environ en la línea 6")
	exigeViolacion(t, vs, d+"c_dot_test.go", "testing.Short en la línea 7")
	exigeNingunaEn(t, vs, d+"d_no_dot_test.go")
	if len(vs) != 3 {
		t.Errorf("se esperaban 3 violaciones; hay %d: %v", len(vs), vs)
	}
	exigeOrdenadas(t, vs)
}

// TestSinBDVivaRazon: el motivo conserva su estilo «<patrón> en la línea N: <razón>» y la
// razón es la del patrón: los de conexión siguen explicando ctr.ConnectionString, los nuevos
// citan E-5 (SKIP) o el entorno armado desde cero, las aperturas (D-F9-6) dicen cuál es el
// fichero de la lista blanca, y ninguna razón nombra OTRO patrón (así contarPatron y
// exigeViolacion no se confunden).
func TestSinBDVivaRazon(t *testing.T) {
	type reasonCase struct {
		patron string
		razon  string
	}
	denylist := []reasonCase{
		{"WAPP_TEST_DB_DSN", "ctr.ConnectionString"},
		{":5432", "ctr.ConnectionString"},
		{"postgres://", "ctr.ConnectionString"},
		{"postgresql://", "ctr.ConnectionString"},
		{"WithReuseByName", "ctr.ConnectionString"},
		{"os.Environ", "desde cero"},
		{"t.Skip", "E-5"},
		{"testing.Short", "E-5"},
	}
	openers := dbOpenerPatternNames()
	casos := make([]reasonCase, 0, len(denylist)+3*len(openers))
	patrones := make([]string, 0, len(denylist)+len(openers))
	for _, c := range denylist {
		casos = append(casos, c)
		patrones = append(patrones, c.patron)
	}
	for _, opener := range openers {
		casos = append(casos,
			reasonCase{opener, "lista blanca"},
			reasonCase{opener, "test/procesos/base_test.go"},
			reasonCase{opener, "D-F9-6"})
		patrones = append(patrones, opener)
	}
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
