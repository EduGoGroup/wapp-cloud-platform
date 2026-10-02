package candados

import "testing"

// Los tests de la lista blanca de quién abre conexiones (D-F9-6, Jhoan, 2026-10-02), la
// segunda regla de SinBDViva. Están aparte de sinbdviva_test.go solo por tamaño: comparten sus
// ayudas (contarPatron, exigeViolacion…) y los árboles de testdata/sinbdviva, donde cada
// evasión de la contradicción 22 del README de F9 que la regla cierra tiene su caso
// (TestSinBDVivaMuerde).

// dbOpenerCases son TODAS las aperturas de conexión que persigue la lista blanca de D-F9-6,
// escritas a mano aquí (no derivadas de la tabla del candado: si alguien quita una de allí,
// su caso cae). Son las de las librerías con las que el repo habla con Postgres, leídas de
// go.mod y de la caché de módulos (pgx v5.10.0): database/sql, pgx, pgxpool, pgconn y
// pgx/stdlib. path es la ruta de import; name, el nombre del paquete, con el que el motivo
// nombra la apertura (name.fn) sea cual sea el alias; fn, la función o el tipo que abre.
// pgxpool.Connect y pgxpool.ConnectConfig son las de pgx v4 (en v5 se llaman New y
// NewWithConfig): se persiguen igual, porque la versión mayor de la ruta no cuenta.
var dbOpenerCases = []struct {
	path string
	name string
	fn   string
}{
	{"database/sql", "sql", "Open"},
	{"database/sql", "sql", "OpenDB"},
	{"github.com/jackc/pgx/v5", "pgx", "Connect"},
	{"github.com/jackc/pgx/v5", "pgx", "ConnectConfig"},
	{"github.com/jackc/pgx/v5", "pgx", "ConnectWithOptions"},
	{"github.com/jackc/pgx/v5/pgconn", "pgconn", "Connect"},
	{"github.com/jackc/pgx/v5/pgconn", "pgconn", "ConnectConfig"},
	{"github.com/jackc/pgx/v5/pgconn", "pgconn", "ConnectWithOptions"},
	{"github.com/jackc/pgx/v5/pgxpool", "pgxpool", "New"},
	{"github.com/jackc/pgx/v5/pgxpool", "pgxpool", "NewWithConfig"},
	{"github.com/jackc/pgx/v5/pgxpool", "pgxpool", "Connect"},
	{"github.com/jackc/pgx/v5/pgxpool", "pgxpool", "ConnectConfig"},
	{"github.com/jackc/pgx/v5/stdlib", "stdlib", "OpenDB"},
	{"github.com/jackc/pgx/v5/stdlib", "stdlib", "OpenDBFromPool"},
	{"github.com/jackc/pgx/v5/stdlib", "stdlib", "GetConnector"},
	{"github.com/jackc/pgx/v5/stdlib", "stdlib", "GetPoolConnector"},
	{"github.com/jackc/pgx/v5/stdlib", "stdlib", "GetDefaultDriver"},
	{"github.com/jackc/pgx/v5/stdlib", "stdlib", "Driver"},
}

// dbOpenerPatternNames devuelve los patrones de dbOpenerCases, "name.fn".
func dbOpenerPatternNames() []string {
	out := make([]string, 0, len(dbOpenerCases))
	for _, c := range dbOpenerCases {
		out = append(out, c.name+"."+c.fn)
	}
	return out
}

// TestDBPackagesMatchCases: la tabla del candado y dbOpenerCases dicen lo mismo, en los dos
// sentidos. Toda apertura de dbPackages tiene su caso (quien añada una sin caso, cae aquí;
// quien quite una, cae en TestSinBDVivaOpenersBite), la ruta de la tabla es la del caso sin la
// versión mayor, y las exenciones son las dos rutas exactas de D-F9-6.
func TestDBPackagesMatchCases(t *testing.T) {
	if dbOpenerAllowedPath != "test/procesos/base_test.go" {
		t.Errorf("dbOpenerAllowedPath = %q; D-F9-6 fija test/procesos/base_test.go", dbOpenerAllowedPath)
	}
	if selfExemptPath != "test/procesos/sin_bd_viva_test.go" {
		t.Errorf("selfExemptPath = %q; D-F9-6 fija test/procesos/sin_bd_viva_test.go", selfExemptPath)
	}
	want := make(map[string]bool, len(dbOpenerCases))
	for _, c := range dbOpenerCases {
		want[importPathWithoutMajor(c.path)+" "+c.name+"."+c.fn] = true
	}
	if len(want) != len(dbOpenerCases) {
		t.Errorf("dbOpenerCases repite un caso: %d distintos de %d", len(want), len(dbOpenerCases))
	}
	got := 0
	for _, p := range dbPackages {
		for _, fn := range p.openers {
			got++
			if !want[p.path+" "+p.name+"."+fn] {
				t.Errorf("dbPackages tiene %s (%s.%s) y dbOpenerCases no: falta su caso", p.path, p.name, fn)
			}
		}
	}
	if got != len(dbOpenerCases) {
		t.Errorf("dbPackages tiene %d aperturas y dbOpenerCases %d", got, len(dbOpenerCases))
	}
}

// TestIsDBOpenerPattern: es apertura exactamente «<nombre del paquete>.<apertura suya>». No lo
// son los patrones de la lista negra, la apertura de otro paquete, un nombre que solo empieza
// o termina igual, la apertura sin paquete, ni el alias con que un fichero la escriba.
func TestIsDBOpenerPattern(t *testing.T) {
	for _, patron := range dbOpenerPatternNames() {
		if !isDBOpenerPattern(patron) {
			t.Errorf("isDBOpenerPattern(%q) = false; quiero true", patron)
		}
	}
	no := []string{"", "WAPP_TEST_DB_DSN", ":5432", "postgres://", "postgresql://", "WithReuseByName",
		"os.Environ", "t.Skip", "testing.Short", "sql.Connect", "pgx.Open", "pgxpool.OpenDB",
		"stdlib.Connect", "pgconn.New", "sql.Opener", "sql.Ope", "xsql.Open", "sql.", "sql", "Open",
		"Connect", "zz.Connect", "pgx.Connect.x", "sql.open", "SQL.Open", "os.Open"}
	for _, patron := range no {
		if isDBOpenerPattern(patron) {
			t.Errorf("isDBOpenerPattern(%q) = true; quiero false", patron)
		}
	}
}

// TestImportPathWithoutMajor: quita los elementos «v» + dígitos, estén donde estén, y nada más.
func TestImportPathWithoutMajor(t *testing.T) {
	cases := map[string]string{
		"os":                              "os",
		"database/sql":                    "database/sql",
		"github.com/jackc/pgx":            "github.com/jackc/pgx",
		"github.com/jackc/pgx/v5":         "github.com/jackc/pgx",
		"github.com/jackc/pgx/v5/pgxpool": "github.com/jackc/pgx/pgxpool",
		"github.com/jackc/pgx/v4/stdlib":  "github.com/jackc/pgx/stdlib",
		"github.com/jackc/pgx/v10/pgconn": "github.com/jackc/pgx/pgconn",
		"github.com/jackc/pgx/v":          "github.com/jackc/pgx/v",
		"github.com/jackc/pgx/vx":         "github.com/jackc/pgx/vx",
		"github.com/jackc/pgx/v5x":        "github.com/jackc/pgx/v5x",
		"github.com/jackc/pgx/x5":         "github.com/jackc/pgx/x5",
		"github.com/jackc/pgx/V5":         "github.com/jackc/pgx/V5",
		"github.com/jackc/pgx/5":          "github.com/jackc/pgx/5",
		"github.com/jackc/pgx/v5.1":       "github.com/jackc/pgx/v5.1",
		"github.com/jackc/pgxv5":          "github.com/jackc/pgxv5",
		"":                                "",
	}
	for in, want := range cases {
		if got := importPathWithoutMajor(in); got != want {
			t.Errorf("importPathWithoutMajor(%q) = %q; quiero %q", in, got, want)
		}
	}
}

// TestSinBDVivaOpenersBite: cada apertura de dbOpenerCases muerde en un fichero que no es el de
// la lista blanca, en las cuatro formas en que un fichero puede nombrarla —con el nombre del
// paquete, con un alias, suelta tras un import de punto, y con el nombre del paquete SIN
// importarlo (el literal dispara siempre, como os y testing)—, y el motivo la nombra siempre
// «name.fn». El MISMO fichero en la ruta de la lista blanca no da ninguna violación.
func TestSinBDVivaOpenersBite(t *testing.T) {
	const file = "test/procesos/caso_test.go"
	const allowed = "test/procesos/base_test.go"
	for _, c := range dbOpenerCases {
		forms := []struct {
			name string
			src  string
		}{
			{"package name", "package procesos\nimport \"" + c.path + "\"\nvar a = " + c.name + "." + c.fn + "\n"},
			{"alias", "package procesos\nimport zz \"" + c.path + "\"\nvar a = zz." + c.fn + "\n"},
			{"dot import", "package procesos\nimport . \"" + c.path + "\"\nvar a = " + c.fn + "\n"},
			{"package name without the import", "package procesos\n\nvar a = " + c.name + "." + c.fn + "\n"},
		}
		for _, f := range forms {
			t.Run(c.name+"."+c.fn+" by "+f.name, func(t *testing.T) {
				vs := SinBDViva([]Fuente{fuenteEnMemoria(t, file, f.src)})
				if got := contarPatron(vs, file, c.name+"."+c.fn); got != 1 || len(vs) != 1 {
					t.Errorf("se esperaba 1 violación de %s.%s en la línea 3; hay %v", c.name, c.fn, vs)
				}
				exigeViolacion(t, vs, file, c.name+"."+c.fn+" en la línea 3: ", "lista blanca", allowed)
				exigeCero(t, SinBDViva([]Fuente{fuenteEnMemoria(t, allowed, f.src)}))
			})
		}
	}
}

// TestSinBDVivaOpenerResolution: la apertura se persigue por el PAQUETE, resuelto por la ruta
// de sus imports fichero a fichero, igual que os.Environ y testing.Short. Llamada o como
// valor; con cualquier versión mayor del módulo en la ruta (v4, v5, v6 o ninguna); una sola
// vez aunque haya import de punto. No muerde una ruta que solo se parece, la apertura de OTRO
// paquete (sql.Connect, pgx.Open), lo que no abre (tipos, errores, ParseConfig), un receptor
// que no es el paquete (un método propio, un campo, os.Open), el import en blanco, ni lo que
// viene en un comentario o en un literal. Con import de punto, DECLARAR algo llamado como una
// apertura sí muerde, y un alias que coincide con el nombre de otro de los paquetes muerde por
// los dos: el candado no resuelve tipos ni ámbitos y prefiere morder de más.
func TestSinBDVivaOpenerResolution(t *testing.T) {
	const file = "test/procesos/caso_test.go"
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{
			"called and as a value",
			`package procesos
import "github.com/jackc/pgx/v5"
var a, _ = pgx.Connect(nil, "")
var b = pgx.Connect
`,
			[]string{"pgx.Connect en la línea 3", "pgx.Connect en la línea 4"},
		},
		{
			"the major version in the import path does not matter",
			`package procesos
import (
	four "github.com/jackc/pgx/v4"
	six "github.com/jackc/pgx/v6"
	bare "github.com/jackc/pgx"
	pool "github.com/jackc/pgx/v4/pgxpool"
	std "github.com/jackc/pgx/v10/stdlib"
)
var a = four.Connect
var b = six.ConnectConfig
var c = bare.ConnectWithOptions
var d = pool.Connect
var e = std.OpenDB
`,
			[]string{"pgx.Connect en la línea 9", "pgx.ConnectConfig en la línea 10",
				"pgx.ConnectWithOptions en la línea 11", "pgxpool.Connect en la línea 12", "stdlib.OpenDB en la línea 13"},
		},
		{
			"alias with a raw string import path",
			"package procesos\nimport p `github.com/jackc/pgx/v5`\nvar a = p.Connect\n",
			[]string{"pgx.Connect en la línea 3"},
		},
		{
			"two aliases of the same package both bite",
			`package procesos
import (
	a "database/sql"
	b "database/sql"
)
var x = a.Open
var y = b.OpenDB
`,
			[]string{"sql.Open en la línea 6", "sql.OpenDB en la línea 7"},
		},
		{
			"a path that only looks like one of the packages is not a receiver",
			`package procesos
import (
	a "github.com/jackc/pgxx/v5"
	b "github.com/jackc/pgx/v5x"
	c "github.com/jackc/pgx/vx"
	d "github.com/jackc/pgx/v"
	e "example.com/github.com/jackc/pgx/v5"
	f "github.com/jackc/pgx/v5/pgtype"
	g "example.com/database/sql"
	h "database/sql/driver"
	i "github.com/jackc/pgx/V5"
)
var _ = a.Connect
var _ = b.Connect
var _ = c.Connect
var _ = d.Connect
var _ = e.Connect
var _ = f.Connect
var _ = g.Open
var _ = h.Open
var _ = i.Connect
`,
			nil,
		},
		{
			"the opener of one package is not the opener of another",
			`package procesos
import (
	s "database/sql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)
var _ = sql.Connect
var _ = s.New
var _ = pgx.Open
var _ = pgx.New
var _ = pgconn.New
var _ = pgconn.OpenDB
var _ = pgxpool.Open
var _ = pgxpool.GetConnector
var _ = stdlib.Connect
var _ = stdlib.New
`,
			nil,
		},
		{
			"what does not open does not bite",
			`package procesos
import (
	"database/sql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)
var _ *sql.DB
var _ = sql.ErrNoRows
var _ = sql.NullTime{}
var _ = sql.Drivers
var _ = pgx.Identifier{"t"}
var _ = pgx.ParseConfig
var _ *pgx.Conn
var _ *pgconn.PgError
var _ = pgxpool.ParseConfig
var _ *pgxpool.Pool
var _ = stdlib.RegisterConnConfig
`,
			nil,
		},
		{
			"a receiver that is not the package does not bite",
			`package procesos
import "os"
type own struct{ pgx struct{ Connect func() } }
func (own) Connect() {}
func (own) Open() {}
func (own) New() {}
func f(o own, db interface{ Driver() interface{ Open(string) } }) {
	o.Connect()
	o.Open()
	o.New()
	o.pgx.Connect()
	_, _ = os.Open("x")
	db.Driver().Open("")
}
`,
			nil,
		},
		{
			"dot import: bare opener called and as a value",
			`package procesos
import . "github.com/jackc/pgx/v5/pgxpool"
var a, _ = New(nil, "")
var b = NewWithConfig
`,
			[]string{"pgxpool.New en la línea 3", "pgxpool.NewWithConfig en la línea 4"},
		},
		{
			"dot import present: the qualified opener counts once, not twice",
			`package procesos
import (
	"github.com/jackc/pgx/v5"
	. "github.com/jackc/pgx/v5"
)
var a = pgx.Connect
`,
			[]string{"pgx.Connect en la línea 6"},
		},
		{
			"dot import present: a foreign selector does not bite",
			`package procesos
import (
	. "database/sql"
	. "github.com/jackc/pgx/v5"
	"example.com/fake"
)
func f(s fake.Server) {
	_ = s.Connect()
	_ = s.Open
	_ = fake.New().OpenDB()
}
`,
			nil,
		},
		{
			"dot import present: declaring something named like an opener bites (bite too much)",
			`package procesos
import . "github.com/jackc/pgx/v5"
type own struct{}
func (own) Connect() {}
`,
			[]string{"pgx.Connect en la línea 4"},
		},
		{
			"the dot import of one package does not bite the opener of another",
			`package procesos
import . "github.com/jackc/pgx/v5"
func Open() {}
func New() {}
func OpenDB() {}
var _ = Identifier{"t"}
`,
			nil,
		},
		{
			"the dot import of another path does not bite",
			`package procesos
import . "example.com/pgx"
func Connect() {}
var _ = Connect
`,
			nil,
		},
		{
			"blank imports add nothing",
			`package procesos
import (
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "database/sql"
)
func OpenDB() {}
func Open() {}
var a = _.OpenDB
var b = _.Open
`,
			nil,
		},
		{
			"an alias equal to the name of another of the packages bites as both (bite too much)",
			`package procesos
import pgx "github.com/jackc/pgx/v5/pgconn"
var a = pgx.Connect
`,
			[]string{"pgconn.Connect en la línea 3", "pgx.Connect en la línea 3"},
		},
		{
			"comments and string literals do not count",
			`package procesos
// pgx.Connect(ctx, "") y sql.Open("pgx", "") en un comentario no cuentan.
var a = "pgx.Connect, sql.Open, pgxpool.New"
`,
			nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			vs := SinBDViva([]Fuente{fuenteEnMemoria(t, file, c.src)})
			for _, w := range c.want {
				exigeViolacion(t, vs, file, w+": ")
			}
			if len(vs) != len(c.want) {
				t.Errorf("se esperaban %d violaciones; hay %d: %v", len(c.want), len(vs), vs)
			}
			exigeOrdenadas(t, vs)
		})
	}
}

// TestSinBDVivaOpenerImportsPerFile: el alias y el import de punto de una librería de base de
// datos valen SOLO en el fichero que los declara, como los de os y testing.
func TestSinBDVivaOpenerImportsPerFile(t *testing.T) {
	const d = "test/procesos/"
	fuentes := []Fuente{
		fuenteEnMemoria(t, d+"a_alias_test.go", `package procesos
import p "github.com/jackc/pgx/v5"
var a = p.Connect
`),
		fuenteEnMemoria(t, d+"b_other_alias_test.go", `package procesos
import p "example.com/dialer"
var b = p.Connect
`),
		fuenteEnMemoria(t, d+"c_dot_test.go", `package procesos
import . "database/sql"
var c = Open
`),
		fuenteEnMemoria(t, d+"d_no_dot_test.go", `package procesos
func Open() {}
var d = Open
`),
	}
	vs := SinBDViva(fuentes)
	exigeViolacion(t, vs, d+"a_alias_test.go", "pgx.Connect en la línea 3")
	exigeNingunaEn(t, vs, d+"b_other_alias_test.go")
	exigeViolacion(t, vs, d+"c_dot_test.go", "sql.Open en la línea 3")
	exigeNingunaEn(t, vs, d+"d_no_dot_test.go")
	if len(vs) != 2 {
		t.Errorf("se esperaban 2 violaciones; hay %d: %v", len(vs), vs)
	}
	exigeOrdenadas(t, vs)
}

// TestSinBDVivaExactPaths: la lista blanca y la auto-exención son rutas EXACTAS (D-F9-6), no
// nombres base, ni sufijos, ni prefijos, y cada una exime de SU regla y de ninguna más. El
// MISMO fichero —una apertura y un literal de la lista negra— se juzga en cada ruta:
//   - test/procesos/base_test.go (lista blanca): su apertura no muerde; su literal, sí;
//   - test/procesos/sin_bd_viva_test.go (el candado): su literal no muerde; su apertura, sí;
//   - cualquier otra ruta, por mucho que se parezca: muerden los dos.
func TestSinBDVivaExactPaths(t *testing.T) {
	const src = `package procesos
import "github.com/jackc/pgx/v5"
var a = pgx.Connect
var b = "WAPP_TEST_DB_DSN"
`
	cases := []struct {
		file    string
		opener  bool // la apertura muerde
		literal bool // el literal muerde
	}{
		{"test/procesos/base_test.go", false, true},
		{"test/procesos/sin_bd_viva_test.go", true, false},
		{"test/procesos/other_test.go", true, true},
		{"test/procesos/sub/base_test.go", true, true},
		{"test/procesos/sub/sin_bd_viva_test.go", true, true},
		{"base_test.go", true, true},
		{"sin_bd_viva_test.go", true, true},
		{"procesos/base_test.go", true, true},
		{"procesos/sin_bd_viva_test.go", true, true},
		{"x/test/procesos/base_test.go", true, true},
		{"x/test/procesos/sin_bd_viva_test.go", true, true},
		{"test/procesos/xbase_test.go", true, true},
		{"test/procesos/no_sin_bd_viva_test.go", true, true},
		{"test/procesos/base_test.go.old.go", true, true},
		{"test/procesos/sin_bd_viva_test.go.old.go", true, true},
		{"test/procesos/base.go", true, true},
		{"test/procesos/Base_test.go", true, true},
		{"test/procesos/SIN_BD_VIVA_test.go", true, true},
		{"./test/procesos/base_test.go", true, true},
		{"test/otros/base_test.go", true, true},
		{"internal/candados/base_test.go", true, true},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			vs := SinBDViva([]Fuente{fuenteEnMemoria(t, c.file, src)})
			if got := contarPatron(vs, c.file, "pgx.Connect") == 1; got != c.opener {
				t.Errorf("la apertura muerde = %v; quiero %v: %v", got, c.opener, vs)
			}
			if got := contarPatron(vs, c.file, "WAPP_TEST_DB_DSN") == 1; got != c.literal {
				t.Errorf("el literal muerde = %v; quiero %v: %v", got, c.literal, vs)
			}
			want := 0
			for _, bites := range []bool{c.opener, c.literal} {
				if bites {
					want++
				}
			}
			if len(vs) != want {
				t.Errorf("se esperaban %d violaciones; hay %d: %v", want, len(vs), vs)
			}
		})
	}
}

// TestSinBDVivaKnownGaps: lo que el candado NO ve, medido. No son promesas, son los límites
// de un candado que mira el AST sin resolver tipos, y de una lista blanca que vigila quién
// ABRE una conexión dentro del proceso de test: no sigue una cadena hasta un subproceso, ni
// una llamada de método sobre un valor, ni una librería que no está en su tabla (y que hoy no
// está en go.mod). Están aquí para que nadie los dé por cerrados; si uno empieza a morder, es
// una mejora: su caso pasa a la tabla de los que muerden y se actualiza la contradicción 22
// del README de F9.
func TestSinBDVivaKnownGaps(t *testing.T) {
	cases := []struct {
		name string
		file string
		src  string
	}{
		{
			"a client launched as a subprocess: psql against a live server",
			"test/procesos/caso_test.go",
			`package procesos
import "os/exec"
var cmd = exec.Command("psql", "-h", "db.example", "-c", "select 1")
`,
		},
		{
			"a subprocess with a nil Env inherits the whole environment of the shell",
			"test/procesos/caso_test.go",
			`package procesos
import "os/exec"
func run(bin string) error { return exec.Command(bin).Run() }
`,
		},
		{
			"the environment by another door: syscall.Environ and cmd.Environ",
			"test/procesos/caso_test.go",
			`package procesos
import ("os/exec"; "syscall")
func env(cmd *exec.Cmd) []string { return append(syscall.Environ(), cmd.Environ()...) }
`,
		},
		{
			"an outside database handed to the server binary through its environment",
			"test/procesos/caso_test.go",
			`package procesos
import ("os"; "os/exec")
func run(bin string) *exec.Cmd {
	cmd := exec.Command(bin)
	cmd.Env = []string{"WAPP_DB_HOST=" + os.Getenv("PGHOST"), "WAPP_DB_PORT=" + "54" + "32"}
	return cmd
}
`,
		},
		{
			"os.Exit(0) in TestMain: the run ends green without running anything",
			"test/procesos/caso_test.go",
			`package procesos
import ("os"; "testing")
func TestMain(m *testing.M) { os.Exit(0) }
`,
		},
		{
			"a method on a value: the driver of a handle somebody else opened",
			"test/procesos/caso_test.go",
			`package procesos
import "database/sql"
func other(db *sql.DB) { _, _ = db.Driver().Open("host=db.example") }
`,
		},
		{
			"a helper of the allowlisted file fed with a connection string of its own",
			"test/procesos/caso_test.go",
			`package procesos
import "testing"
func other(t *testing.T) { _ = baseClonada{DSN: "host=db.example dbname=wapp"}.Abrir(t) }
`,
		},
		{
			"a library that is not in the table (none of these is in go.mod today)",
			"test/procesos/caso_test.go",
			`package procesos
import (
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"gorm.io/gorm"
)
var a, _ = sqlx.Connect("postgres", "")
var b, _ = pq.NewConnector("")
var c, _ = gorm.Open(nil)
`,
		},
		{
			"a raw socket to the server",
			"test/procesos/caso_test.go",
			`package procesos
import "net"
var conn, _ = net.Dial("tcp", net.JoinHostPort("db.example", "54"+"32"))
`,
		},
		{
			"inside the allowlisted file, a connection string that is not the container's",
			"test/procesos/base_test.go",
			`package procesos
import "github.com/jackc/pgx/v5"
var conn, _ = pgx.Connect(nil, "")
`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			exigeCero(t, SinBDViva([]Fuente{fuenteEnMemoria(t, c.file, c.src)}))
		})
	}
}
