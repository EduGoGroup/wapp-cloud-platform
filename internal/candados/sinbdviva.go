package candados

import (
	"fmt"
	"go/ast"
	"go/token"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// selfExemptPath es la ruta EXACTA del propio candado en el árbol real. Es el único fichero al
// que no se le aplican los patrones de la lista negra, porque puede nombrarlos como literales
// para perseguirlos. Hasta D-F9-6 la exención era por nombre base, y un sin_bd_viva_test.go
// en cualquier subdirectorio quedaba sin mirar (contradicción 22 del README de F9).
const selfExemptPath = "test/procesos/sin_bd_viva_test.go"

// dbOpenerAllowedPath es la lista blanca de D-F9-6 (Jhoan, 2026-10-02): la ruta EXACTA del
// único fichero de test/procesos que puede abrir una conexión a la base de datos. Es donde
// se abren hoy: pgx.Connect contra la base de mantenimiento del contenedor y sql.Open sobre
// la base clonada de cada proceso. Ampliar la lista es cambiar esta constante, a la vista de
// quien revise.
const dbOpenerAllowedPath = "test/procesos/base_test.go"

// SinBDViva prohíbe apuntar un test de proceso a un Postgres vivo (05 §7.2): la única cadena
// de conexión válida es la que devuelve el contenedor de testcontainers
// (ctr.ConnectionString(ctx, "sslmode=disable")). Y prohíbe dos atajos que dejarían pasar la
// pantalla en verde sin haber probado nada (plan/F9-procesos/diseno.md §6). fuentes son TODOS
// los .go de test/procesos (con o sin etiqueta integracion; las etiquetas se ignoran), y su
// Ruta es relativa a la raíz del repo: las dos exenciones de abajo son rutas EXACTAS, no
// nombres base, ni sufijos, ni prefijos.
//
// Son DOS reglas, y cada exención lo es de la suya y de ninguna más.
//
// 1 · LISTA BLANCA de quién abre conexiones (D-F9-6, Jhoan, 2026-10-02). Toda referencia —
// llamada o tomada como valor— a una apertura de conexión de dbPackages es una violación,
// salvo en dbOpenerAllowedPath (test/procesos/base_test.go). No se mira la cadena que
// recibe: se persigue la apertura, y por eso muerde lo que una lista negra de literales no
// puede ver (medido en la contradicción 22 del README de F9): un DSN vacío o sin host ni
// puerto, que pgx completa con PGHOST/PGPORT o con sus valores por defecto; un DSN clave=valor
// («port=5432», sin los dos puntos); un literal construido por trozos o con
// net.JoinHostPort; un os.Getenv de la cadena de un Postgres de fuera. El propio candado
// (selfExemptPath) NO está exento de esta regla: nombra patrones, no abre conexiones.
// Las aperturas son las de las librerías con las que el repo habla con Postgres (go.mod):
//   - database/sql: Open, OpenDB;
//   - github.com/jackc/pgx (v5): Connect, ConnectConfig, ConnectWithOptions;
//   - …/pgx/pgconn: Connect, ConnectConfig, ConnectWithOptions;
//   - …/pgx/pgxpool: New, NewWithConfig (y Connect, ConnectConfig, sus nombres en pgx v4);
//   - …/pgx/stdlib: OpenDB, OpenDBFromPool, GetConnector, GetPoolConnector, GetDefaultDriver
//     y el tipo Driver (los cuatro últimos abren por un método, sin nombrar una apertura).
//
// 2 · LISTA NEGRA de patrones. Se salta SOLO selfExemptPath (el propio candado); el fichero
// de la lista blanca no: base_test.go abre conexiones, pero con la cadena del contenedor. En
// los demás, cada aparición de uno de estos patrones es una violación:
//   - un literal de cadena (interpretado o crudo) que contiene "WAPP_TEST_DB_DSN";
//   - un literal de cadena que contiene ":5432" (cubre "localhost:5432" y "127.0.0.1:5432");
//   - un literal de cadena que empieza por "postgres://" o por "postgresql://";
//   - el identificador WithReuseByName (suelto o como Sel de un selector);
//   - os.Environ (llamado o como valor): el servidor del proceso recibe un entorno armado
//     desde cero, nunca el del shell del desarrollador, cuyo .env exportado apuntaría a otra
//     base (diseno.md §2). Solo se persigue Environ; os.Getenv de UNA variable concreta es
//     legítimo;
//   - Skip, SkipNow o Skipf sobre CUALQUIER receptor (t, b, tb, un campo…), llamados o como
//     valor de método; el patrón se nombra «t.Skip» y cada aparición da una violación
//     (mejor morder de más que dejar pasar uno). E-5: nunca un SKIP en código nuevo, porque
//     un rc=0 cuenta un `--- SKIP` igual que un `--- PASS`; un proceso que no puede correr
//     falla;
//   - testing.Short (llamado o como valor): el atajo para saltar por la puerta de atrás, con
//     el mismo motivo (E-5).
//
// La lista negra se CONSERVA junto a la blanca (defensa en profundidad): la blanca no mira
// las cadenas, y hay cadenas que no pasan por una apertura del proceso de test —la que se le
// entrega al servidor por su entorno, o la que se le da a un ayudante de base_test.go—; y
// os.Environ, Skip* y testing.Short no tienen que ver con abrir conexiones.
//
// os.Environ, testing.Short y las aperturas se persiguen por el PAQUETE, no por el nombre con
// que el fichero lo importa. La regla se resuelve fichero a fichero, leyendo sus imports por
// ruta exacta ("os", "testing" y las de dbPackages; en estas últimas el elemento de versión
// mayor del módulo —/v4, /v5…— no cuenta, para que un salto de versión de pgx no deje ciego
// al candado):
//   - el selector <nombre>.Environ dispara si <nombre> es el literal os o cualquier alias con
//     que ESE fichero importa "os" (import e "os" → e.Environ); ídem <nombre>.Short con
//     testing y sus alias, y <nombre>.<apertura> con el nombre del paquete (sql, pgx, pgconn,
//     pgxpool, stdlib) y sus alias. El literal dispara siempre, importe el fichero lo que
//     importe (mejor morder de más);
//   - con import de punto (import . "os" / import . "testing" / import . "…/pgx/v5") dispara
//     el identificador suelto Environ / Short / <apertura>, es decir, todo identificador con
//     ese nombre que NO sea el Sel de un selector. Un selector ajeno (x.Environ, x.Short,
//     x.Connect, con x que no es el paquete) no dispara ni siquiera con el import de punto
//     presente, y os.Environ sigue contando una sola vez. En cambio DECLARAR en ese fichero
//     algo llamado Environ, Short o Connect (un método propio, un campo, una variable local)
//     sí dispara: el candado no resuelve tipos ni ámbitos y prefiere morder de más;
//   - el import en blanco (_ "os") no añade nada, y el alias o el punto de un fichero no
//     valen en otro;
//   - una apertura solo lo es de SU paquete: sql.Connect o pgx.Open no disparan. Si el alias
//     de un fichero coincide con el nombre de otro de los paquetes (import pgx "…/pgconn"),
//     pgx.Connect dispara por los dos (mejor morder de más).
//
// Los comentarios no cuentan: se mira el AST, no el texto. Fichero es la Ruta y Motivo
// contiene el patrón que la dispara, literal: "WAPP_TEST_DB_DSN", ":5432", "postgres://",
// "postgresql://", "WithReuseByName", "os.Environ", "t.Skip", "testing.Short" o, para una
// apertura, "<paquete>.<apertura>" con el nombre del paquete ("sql.Open", "pgx.Connect",
// "pgxpool.New", "stdlib.OpenDB"…). El patrón se nombra siempre así, también cuando lo que
// dispara es un alias o un identificador suelto. Un literal que dispara dos patrones da dos
// violaciones. La salida va ordenada por Fichero y, a igual Fichero, por Motivo.
//
// Lo que NO ve (límites de mirar el AST sin resolver tipos, y de vigilar solo quién abre una
// conexión dentro del proceso de test; fijados en TestSinBDVivaKnownGaps): un cliente lanzado
// como subproceso (psql); la base que se le entrega al servidor por su entorno; un exec.Cmd
// con Env nil, syscall.Environ o cmd.Environ, que heredan el entorno; os.Exit(0) en TestMain;
// una apertura por método sobre un valor (db.Driver().Open); un ayudante de base_test.go
// llamado con una cadena propia; una librería que no está en dbPackages; un socket crudo; y
// lo que haga el propio base_test.go, que se revisa a mano.
func SinBDViva(fuentes []Fuente) []Violacion {
	vs := make([]Violacion, 0)
	for _, f := range fuentes {
		// Los nombres locales de os, de testing y de las librerías de base de datos se
		// resuelven por fichero: un alias o un import de punto solo valen donde se declaran.
		names := newFileNames(f.Archivo)
		// ast.Inspect no visita los comentarios (cuelgan de File.Comments, no del árbol de
		// nodos que recorre), así que un patrón citado en un comentario no dispara.
		ast.Inspect(f.Archivo, func(n ast.Node) bool {
			for _, patron := range patronesBDViva(n, names) {
				if exemptFromRule(f.Ruta, patron) {
					continue
				}
				linea := f.Fset.Position(n.Pos()).Line
				vs = append(vs, Violacion{
					Fichero: f.Ruta,
					Motivo:  fmt.Sprintf("%s en la línea %d: %s", patron, linea, razonBDViva(patron)),
				})
			}
			return true
		})
	}
	sort.Slice(vs, func(i, j int) bool {
		if vs[i].Fichero != vs[j].Fichero {
			return vs[i].Fichero < vs[j].Fichero
		}
		return vs[i].Motivo < vs[j].Motivo
	})
	return vs
}

// exemptFromRule dice si la regla que dispara pattern NO rige en el fichero file (su Ruta).
// Cada regla tiene UN fichero exento, por ruta exacta, y la exención de una no vale para la
// otra:
//   - una apertura de conexión (lista blanca) solo se permite en dbOpenerAllowedPath;
//   - cualquier otro patrón (lista negra) solo se perdona en selfExemptPath, el propio candado.
func exemptFromRule(file, pattern string) bool {
	if isDBOpenerPattern(pattern) {
		return file == dbOpenerAllowedPath
	}
	return file == selfExemptPath
}

// dbPackage es una librería que abre conexiones a la base de datos: path es su ruta de import
// SIN el elemento de versión mayor del módulo (importPathWithoutMajor); name, el nombre del
// paquete, que es el receptor por defecto de sus selectores y el prefijo con que el motivo
// nombra cada apertura; openers, las funciones (o el tipo) de primer nivel que abren una
// conexión o entregan algo que la abre por un método.
type dbPackage struct {
	path    string
	name    string
	openers []string
}

// dbPackages son las librerías con las que el repo habla con Postgres (go.mod: pgx v5 y el
// database/sql de la biblioteca estándar) y sus aperturas, leídas del código de pgx v5.10.0:
//   - database/sql: Open y OpenDB devuelven el *sql.DB, sea cual sea el driver;
//   - pgx: Connect, ConnectConfig, ConnectWithOptions (conn.go);
//   - pgconn: las tres de bajo nivel, con los mismos nombres (pgconn.go). Construct no está:
//     arma un PgConn a partir de una conexión ya abierta y secuestrada, no abre nada;
//   - pgxpool: New y NewWithConfig (pool.go). Connect y ConnectConfig son sus nombres en pgx
//     v4: en v5 no existen, así que perseguirlos no puede morder nada legítimo;
//   - stdlib: OpenDB y OpenDBFromPool devuelven un *sql.DB sin pasar por sql.Open;
//     GetConnector y GetPoolConnector, el conector que abre con sql.OpenDB o con su método
//     Connect; GetDefaultDriver y el tipo Driver, el driver, cuyo método Open abre con la
//     cadena que se le dé. Los cuatro últimos se persiguen porque lo que abre después es un
//     método sobre un valor, y eso el candado no lo ve.
//
// Una librería que no está aquí (lib/pq directo, sqlx, gorm…) no se persigue: hoy ninguna
// está en go.mod, y si entra una, entra en esta tabla.
var dbPackages = []dbPackage{
	{"database/sql", "sql", []string{"Open", "OpenDB"}},
	{"github.com/jackc/pgx", "pgx", []string{"Connect", "ConnectConfig", "ConnectWithOptions"}},
	{"github.com/jackc/pgx/pgconn", "pgconn", []string{"Connect", "ConnectConfig", "ConnectWithOptions"}},
	{"github.com/jackc/pgx/pgxpool", "pgxpool", []string{"New", "NewWithConfig", "Connect", "ConnectConfig"}},
	{"github.com/jackc/pgx/stdlib", "stdlib", []string{"OpenDB", "OpenDBFromPool", "GetConnector", "GetPoolConnector", "GetDefaultDriver", "Driver"}},
}

// opens dice si ident es una de las aperturas de ESTE paquete (la comparación es exacta: la
// apertura de un paquete no lo es de otro).
func (p dbPackage) opens(ident string) bool {
	return slices.Contains(p.openers, ident)
}

// isDBOpenerPattern dice si pattern es el nombre de una apertura de dbPackages tal como la
// escribe el motivo: "<name>.<apertura>". Distingue la regla de la lista blanca de los
// patrones de la lista negra, que nunca tienen esa forma con uno de esos nombres.
func isDBOpenerPattern(pattern string) bool {
	for _, p := range dbPackages {
		if ident, ok := strings.CutPrefix(pattern, p.name+"."); ok && p.opens(ident) {
			return true
		}
	}
	return false
}

// importPathWithoutMajor quita de una ruta de import los elementos de versión mayor de módulo
// (v2, v5, v10…): "github.com/jackc/pgx/v5/pgxpool" → "github.com/jackc/pgx/pgxpool". Así la
// tabla dbPackages no depende de la versión de pgx que fije go.mod. Una ruta sin ese elemento
// queda igual ("os", "database/sql"), y uno que solo se le parece no se quita: «v», «vx»,
// «v5x» y «V5» no son una versión mayor.
func importPathWithoutMajor(p string) string {
	elems := strings.Split(p, "/")
	kept := make([]string, 0, len(elems))
	for _, e := range elems {
		if !isMajorVersionElement(e) {
			kept = append(kept, e)
		}
	}
	return strings.Join(kept, "/")
}

// isMajorVersionElement dice si e es «v» seguida de uno o más dígitos y nada más.
func isMajorVersionElement(e string) bool {
	if len(e) < 2 || e[0] != 'v' {
		return false
	}
	for _, c := range e[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// razonBDViva es la explicación que cierra el motivo de cada patrón: por qué un test de
// proceso no puede hacerlo. Una apertura de conexión dice cuál es el fichero de la lista
// blanca; todo otro patrón que no sea de entorno o de SKIP es de cadena de conexión.
func razonBDViva(patron string) string {
	if isDBOpenerPattern(patron) {
		return "solo " + dbOpenerAllowedPath + " abre conexiones a la base de datos (lista blanca, " +
			"D-F9-6): los demás ficheros usan la que ese fichero les da, con la cadena del contenedor"
	}
	switch patron {
	case "os.Environ":
		return "el servidor de un proceso recibe un entorno armado desde cero, nunca el del " +
			"shell (un .env exportado apuntaría a otra base): se lee con os.Getenv UNA variable concreta"
	case "t.Skip":
		return "E-5: nunca un SKIP en código nuevo (un rc=0 lo cuenta como PASS); " +
			"un proceso que no puede correr falla"
	case "testing.Short":
		return "E-5: testing.Short() es el atajo para saltar un proceso; " +
			"un proceso que no puede correr falla, no se salta"
	}
	return "un test de proceso solo usa la cadena de ctr.ConnectionString del contenedor, " +
		"nunca un Postgres vivo"
}

// localNames son los nombres con que UN fichero puede referirse a un paquete: qualifiers,
// los receptores válidos de un selector (el nombre literal del paquete, siempre, más cada
// alias con que el fichero lo importa); dot, si además lo importa con punto, en cuyo caso sus
// símbolos aparecen sueltos, sin receptor.
type localNames struct {
	qualifiers map[string]bool
	dot        bool
}

// resolveLocalNames resuelve, entre los imports de un fichero, los nombres locales del paquete
// de ruta path y nombre name ("os" y "os", "testing" y "testing", o un dbPackage, cuya ruta
// no es su nombre). La ruta de cada import se compara exacta, ya sin comillas —sea el literal
// interpretado o crudo— y sin el elemento de versión mayor del módulo (importPathWithoutMajor,
// que a "os" y a "testing" no les cambia nada). Promesas:
//   - name está SIEMPRE entre los qualifiers, lo importe el fichero o no: el candado prefiere
//     morder de más que fiarse de que «os» o «pgx» sean otra cosa;
//   - import <alias> "path" añade <alias>; varios alias del mismo paquete, todos;
//   - import . "path" pone dot;
//   - import _ "path" no añade nada (no da nombre con el que llamar a nada);
//   - un import de otra ruta no cuenta, aunque su alias sea el mismo o su ruta termine en path.
func resolveLocalNames(imports []*ast.ImportSpec, path, name string) localNames {
	ln := localNames{qualifiers: map[string]bool{name: true}}
	for _, imp := range imports {
		// Sin Name el paquete entra con su propio nombre, que ya está entre los qualifiers.
		if imp.Name == nil || importPathWithoutMajor(valorLiteral(imp.Path.Value)) != path {
			continue
		}
		switch imp.Name.Name {
		case ".":
			ln.dot = true
		case "_":
			// En blanco: el paquete se enlaza, pero no queda nombre con el que llamarlo.
		default:
			ln.qualifiers[imp.Name.Name] = true
		}
	}
	return ln
}

// qualifies dice si e es un identificador simple que nombra al paquete en este fichero: su
// nombre literal o uno de sus alias. Un selector que lo contenga (a.os, s.T) no lo es.
func (ln localNames) qualifies(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && ln.qualifiers[id.Name]
}

// fileNames es lo que el candado necesita saber de UN fichero para juzgar sus nodos: cómo
// nombra a os, a testing y a cada librería de base de datos, y qué identificadores son el Sel
// de un selector. Se crea uno por fichero y no se comparte: el alias de un fichero no vale en
// otro.
type fileNames struct {
	os      localNames
	testing localNames
	// db son los nombres locales de cada paquete de dbPackages, en el mismo orden.
	db []localNames
	// sels son los *ast.Ident que ya se vieron como Sel de un SelectorExpr. ast.Inspect
	// visita ese Sel OTRA vez, como *ast.Ident, y ahí ya no se sabe si era suelto o colgaba de
	// un receptor. Sin este registro, con import de punto, os.Environ contaría dos veces y
	// x.Environ (un método ajeno) dispararía por la rama del identificador suelto. Inspect
	// recorre en preorden: el selector siempre se visita antes que su Sel.
	sels map[*ast.Ident]bool
}

// newFileNames resuelve los nombres locales de os, de testing y de dbPackages en los imports
// de file, con el registro de Sel vacío.
func newFileNames(file *ast.File) *fileNames {
	names := &fileNames{
		os:      resolveLocalNames(file.Imports, "os", "os"),
		testing: resolveLocalNames(file.Imports, "testing", "testing"),
		db:      make([]localNames, 0, len(dbPackages)),
		sels:    make(map[*ast.Ident]bool),
	}
	for _, p := range dbPackages {
		names.db = append(names.db, resolveLocalNames(file.Imports, p.path, p.name))
	}
	return names
}

// dbOpenerPatterns devuelve las aperturas de dbPackages que nombra ident en este fichero,
// como "<name>.<ident>". inPackage dice, para cada paquete, si ident se está juzgando como
// símbolo suyo: el receptor del selector lo nombra, o el fichero lo importa con punto y el
// identificador va suelto. Casi siempre devuelve una o ninguna; devuelve dos si el fichero
// nombra así a dos paquetes que tienen una apertura con ese nombre (mejor morder de más).
func (names *fileNames) dbOpenerPatterns(ident string, inPackage func(localNames) bool) []string {
	var ps []string
	for i, p := range dbPackages {
		if p.opens(ident) && inPackage(names.db[i]) {
			ps = append(ps, p.name+"."+ident)
		}
	}
	return ps
}

// patronesBDViva devuelve los patrones que dispara el nodo n —los de la lista negra y las
// aperturas de conexión de la lista blanca—, uno por patrón (un literal puede disparar varios).
// Si el fichero está exento de la regla de cada uno lo decide después exemptFromRule. names es
// el estado del fichero al que pertenece n, y esta función lo va completando: al pasar por un
// SelectorExpr anota su Sel, para reconocerlo cuando Inspect lo visite después como *ast.Ident.
//
// Un selector testcontainers.WithReuseByName se cuenta una sola vez: se detecta en el Ident
// (suelto o Sel, da igual), no en el SelectorExpr, para no duplicarlo. Los patrones de entorno
// y de SKIP y las aperturas, en cambio, SÍ se detectan en el SelectorExpr (necesitan su
// receptor); en el Ident solo se detectan cuando el identificador es suelto, que es el caso
// del import de punto.
func patronesBDViva(n ast.Node, names *fileNames) []string {
	switch x := n.(type) {
	case *ast.Ident:
		if x.Name == "WithReuseByName" {
			return []string{"WithReuseByName"}
		}
		if !names.sels[x] {
			return looseIdentPatterns(x, names)
		}
	case *ast.BasicLit:
		if x.Kind == token.STRING {
			return patronesLiteral(valorLiteral(x.Value))
		}
	case *ast.SelectorExpr:
		names.sels[x.Sel] = true
		return patronesSelector(x, names)
	}
	return nil
}

// looseIdentPatterns juzga un identificador SUELTO (el que no es Sel de un selector): Environ
// es os.Environ si el fichero importa "os" con punto, Short es testing.Short si importa
// "testing" con punto, y una apertura de dbPackages lo es si importa SU paquete con punto. Sin
// ese import de punto un Environ, un Short o un Connect sueltos son del propio paquete y no
// disparan. No distingue un uso de una declaración: con el import de punto presente, un método
// propio llamado Environ también dispara (mejor morder de más; resolverlo exigiría tipos y
// ámbitos, que el candado no tiene).
func looseIdentPatterns(id *ast.Ident, names *fileNames) []string {
	switch {
	case id.Name == "Environ" && names.os.dot:
		return []string{"os.Environ"}
	case id.Name == "Short" && names.testing.dot:
		return []string{"testing.Short"}
	}
	return names.dbOpenerPatterns(id.Name, func(ln localNames) bool { return ln.dot })
}

// patronesSelector detecta Skip* sobre cualquier receptor, y os.Environ, testing.Short y las
// aperturas de dbPackages sobre el suyo. Skip, SkipNow y Skipf se juzgan solo por el nombre
// del método (el candado prefiere morder de más: un método propio llamado Skip también
// dispara); un nombre que apenas contiene «Skip» (Skipper, NoSkip) no. Environ, Short y las
// aperturas, en cambio, se juzgan por su receptor: tiene que ser uno de los nombres con que
// el fichero se refiere al paquete (el literal o un alias); un x.Environ o un x.Connect sobre
// cualquier otro receptor no dispara.
func patronesSelector(x *ast.SelectorExpr, names *fileNames) []string {
	switch x.Sel.Name {
	case "Skip", "SkipNow", "Skipf":
		return []string{"t.Skip"}
	case "Environ":
		if names.os.qualifies(x.X) {
			return []string{"os.Environ"}
		}
	case "Short":
		if names.testing.qualifies(x.X) {
			return []string{"testing.Short"}
		}
	}
	return names.dbOpenerPatterns(x.Sel.Name, func(ln localNames) bool { return ln.qualifies(x.X) })
}

// patronesLiteral aplica al valor ya sin comillas las tres reglas de literal de cadena.
// "postgresql://" no empieza por "postgres://" (la décima letra es «q», no «:»), así que los
// dos prefijos no se pisan.
func patronesLiteral(s string) []string {
	var ps []string
	for _, p := range []string{"WAPP_TEST_DB_DSN", ":5432"} {
		if strings.Contains(s, p) {
			ps = append(ps, p)
		}
	}
	for _, p := range []string{"postgres://", "postgresql://"} {
		if strings.HasPrefix(s, p) {
			ps = append(ps, p)
		}
	}
	return ps
}

// valorLiteral quita las comillas de un literal de cadena, sea interpretado ("…") o crudo
// (`…`): el prefijo se juzga sobre el valor, no sobre la comilla de apertura. Un literal que
// el parser aceptó siempre se deja; si aun así fallara, se juzga el texto tal cual (mejor
// morder de más que dejar pasar).
func valorLiteral(lit string) string {
	s, err := strconv.Unquote(lit)
	if err != nil {
		return lit
	}
	return s
}
