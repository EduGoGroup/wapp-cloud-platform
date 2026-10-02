package candados

import (
	"fmt"
	"go/ast"
	"go/token"
	"path"
	"sort"
	"strconv"
	"strings"
)

// SinBDViva prohíbe apuntar un test de proceso a un Postgres vivo (05 §7.2): la única cadena
// de conexión válida es la que devuelve el contenedor de testcontainers
// (ctr.ConnectionString(ctx, "sslmode=disable")). Y prohíbe dos atajos que dejarían pasar la
// pantalla en verde sin haber probado nada (plan/F9-procesos/diseno.md §6). fuentes son TODOS
// los .go de test/procesos (con o sin etiqueta integracion; las etiquetas se ignoran).
//
// Se salta el fichero cuyo nombre base es "sin_bd_viva_test.go" (el propio candado, que
// nombra los patrones para perseguirlos). En los demás, cada aparición de uno de estos
// patrones es una violación:
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
// os.Environ y testing.Short se persiguen por el PAQUETE, no por el nombre con que el fichero
// lo importa. La regla se resuelve fichero a fichero, leyendo sus imports ("os" y "testing",
// por ruta exacta):
//   - el selector <nombre>.Environ dispara si <nombre> es el literal os o cualquier alias con
//     que ESE fichero importa "os" (import e "os" → e.Environ); ídem <nombre>.Short con
//     testing y sus alias. El literal os / testing dispara siempre, importe el fichero lo que
//     importe (mejor morder de más);
//   - con import de punto (import . "os" / import . "testing") dispara el identificador
//     suelto Environ / Short, es decir, todo identificador con ese nombre que NO sea el Sel de
//     un selector. Un selector ajeno (x.Environ, x.Short, con x que no es el paquete) no
//     dispara ni siquiera con el import de punto presente, y os.Environ sigue contando una
//     sola vez. En cambio DECLARAR en ese fichero algo llamado Environ o Short (un método
//     propio, un campo, una variable local) sí dispara: el candado no resuelve tipos ni
//     ámbitos y prefiere morder de más;
//   - el import en blanco (_ "os") no añade nada, y el alias o el punto de un fichero no
//     valen en otro.
//
// Los comentarios no cuentan: se mira el AST, no el texto. Fichero es la Ruta y Motivo
// contiene el patrón que la dispara, literal: "WAPP_TEST_DB_DSN", ":5432", "postgres://",
// "postgresql://", "WithReuseByName", "os.Environ", "t.Skip" o "testing.Short". El patrón se
// nombra siempre así, también cuando lo que dispara es un alias o un identificador suelto.
// Un literal que dispara dos patrones da dos violaciones. La salida va ordenada por Fichero
// y, a igual Fichero, por Motivo.
func SinBDViva(fuentes []Fuente) []Violacion {
	vs := make([]Violacion, 0)
	for _, f := range fuentes {
		// El propio candado nombra los patrones como literales para perseguirlos: se salta a
		// sí mismo por nombre base, esté donde esté.
		if path.Base(f.Ruta) == "sin_bd_viva_test.go" {
			continue
		}
		// Los nombres locales de os y testing se resuelven por fichero: un alias o un import
		// de punto solo valen donde se declaran.
		names := newFileNames(f.Archivo)
		// ast.Inspect no visita los comentarios (cuelgan de File.Comments, no del árbol de
		// nodos que recorre), así que un patrón citado en un comentario no dispara.
		ast.Inspect(f.Archivo, func(n ast.Node) bool {
			for _, patron := range patronesBDViva(n, names) {
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

// razonBDViva es la explicación que cierra el motivo de cada patrón: por qué un test de
// proceso no puede hacerlo. Todo patrón que no sea de entorno o de SKIP es de conexión.
func razonBDViva(patron string) string {
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

// resolveLocalNames resuelve los nombres locales del paquete pkg entre los imports de un
// fichero. pkg es a la vez la ruta de import y el nombre del paquete (vale para "os" y
// "testing", los dos únicos que persigue el candado). La ruta se compara exacta y ya sin
// comillas, sea el literal interpretado o crudo. Promesas:
//   - pkg está SIEMPRE entre los qualifiers, lo importe el fichero o no: el candado prefiere
//     morder de más que fiarse de que «os» sea otra cosa;
//   - import <alias> "pkg" añade <alias>; varios alias del mismo paquete, todos;
//   - import . "pkg" pone dot;
//   - import _ "pkg" no añade nada (no da nombre con el que llamar a nada);
//   - un import de otra ruta no cuenta, aunque su alias sea el mismo o su ruta termine en pkg.
func resolveLocalNames(imports []*ast.ImportSpec, pkg string) localNames {
	ln := localNames{qualifiers: map[string]bool{pkg: true}}
	for _, imp := range imports {
		// Sin Name el paquete entra con su propio nombre, que ya está entre los qualifiers.
		if imp.Name == nil || valorLiteral(imp.Path.Value) != pkg {
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
// nombra a os y a testing, y qué identificadores son el Sel de un selector. Se crea uno por
// fichero y no se comparte: el alias de un fichero no vale en otro.
type fileNames struct {
	os      localNames
	testing localNames
	// sels son los *ast.Ident que ya se vieron como Sel de un SelectorExpr. ast.Inspect
	// visita ese Sel OTRA vez, como *ast.Ident, y ahí ya no se sabe si era suelto o colgaba de
	// un receptor. Sin este registro, con import de punto, os.Environ contaría dos veces y
	// x.Environ (un método ajeno) dispararía por la rama del identificador suelto. Inspect
	// recorre en preorden: el selector siempre se visita antes que su Sel.
	sels map[*ast.Ident]bool
}

// newFileNames resuelve los nombres locales de os y testing en los imports de file, con el
// registro de Sel vacío.
func newFileNames(file *ast.File) *fileNames {
	return &fileNames{
		os:      resolveLocalNames(file.Imports, "os"),
		testing: resolveLocalNames(file.Imports, "testing"),
		sels:    make(map[*ast.Ident]bool),
	}
}

// patronesBDViva devuelve los patrones prohibidos que dispara el nodo n, uno por patrón (un
// literal puede disparar varios). names es el estado del fichero al que pertenece n, y esta
// función lo va completando: al pasar por un SelectorExpr anota su Sel, para reconocerlo
// cuando Inspect lo visite después como *ast.Ident.
//
// Un selector testcontainers.WithReuseByName se cuenta una sola vez: se detecta en el Ident
// (suelto o Sel, da igual), no en el SelectorExpr, para no duplicarlo. Los patrones de entorno
// y de SKIP, en cambio, SÍ se detectan en el SelectorExpr (necesitan su receptor); en el
// Ident solo se detectan cuando el identificador es suelto, que es el caso del import de punto.
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
// es os.Environ si el fichero importa "os" con punto, y Short es testing.Short si importa
// "testing" con punto. Sin ese import de punto un Environ o un Short sueltos son del propio
// paquete y no disparan. No distingue un uso de una declaración: con el import de punto
// presente, un método propio llamado Environ también dispara (mejor morder de más; resolverlo
// exigiría tipos y ámbitos, que el candado no tiene).
func looseIdentPatterns(id *ast.Ident, names *fileNames) []string {
	switch {
	case id.Name == "Environ" && names.os.dot:
		return []string{"os.Environ"}
	case id.Name == "Short" && names.testing.dot:
		return []string{"testing.Short"}
	}
	return nil
}

// patronesSelector detecta Skip* sobre cualquier receptor, y os.Environ y testing.Short sobre
// el suyo. Skip, SkipNow y Skipf se juzgan solo por el nombre del método (el candado prefiere
// morder de más: un método propio llamado Skip también dispara); un nombre que apenas
// contiene «Skip» (Skipper, NoSkip) no. Environ y Short, en cambio, se juzgan por su receptor:
// tiene que ser uno de los nombres con que el fichero se refiere a os o a testing (el literal
// o un alias); un x.Environ sobre cualquier otro receptor no dispara.
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
	return nil
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
