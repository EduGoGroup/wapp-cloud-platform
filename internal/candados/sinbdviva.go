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
// (ctr.ConnectionString(ctx, "sslmode=disable")). fuentes son TODOS los .go de test/procesos
// (con o sin etiqueta integracion; las etiquetas se ignoran).
//
// Se salta el fichero cuyo nombre base es "sin_bd_viva_test.go" (el propio candado, que
// nombra los patrones para perseguirlos). En los demás, cada aparición de uno de estos
// patrones es una violación:
//   - un literal de cadena (interpretado o crudo) que contiene "WAPP_TEST_DB_DSN";
//   - un literal de cadena que contiene ":5432" (cubre "localhost:5432" y "127.0.0.1:5432");
//   - un literal de cadena que empieza por "postgres://" o por "postgresql://";
//   - el identificador WithReuseByName (suelto o como Sel de un selector).
//
// Los comentarios no cuentan: se mira el AST, no el texto. Fichero es la Ruta y Motivo
// contiene el patrón que la dispara, literal: "WAPP_TEST_DB_DSN", ":5432", "postgres://",
// "postgresql://" o "WithReuseByName". Un literal que dispara dos patrones da dos violaciones.
func SinBDViva(fuentes []Fuente) []Violacion {
	vs := make([]Violacion, 0)
	for _, f := range fuentes {
		// El propio candado nombra los patrones como literales para perseguirlos: se salta a
		// sí mismo por nombre base, esté donde esté.
		if path.Base(f.Ruta) == "sin_bd_viva_test.go" {
			continue
		}
		// ast.Inspect no visita los comentarios (cuelgan de File.Comments, no del árbol de
		// nodos que recorre), así que un patrón citado en un comentario no dispara.
		ast.Inspect(f.Archivo, func(n ast.Node) bool {
			for _, patron := range patronesBDViva(n) {
				linea := f.Fset.Position(n.Pos()).Line
				vs = append(vs, Violacion{
					Fichero: f.Ruta,
					Motivo: fmt.Sprintf("%s en la línea %d: un test de proceso solo usa la cadena "+
						"de ctr.ConnectionString del contenedor, nunca un Postgres vivo", patron, linea),
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

// patronesBDViva devuelve los patrones prohibidos que dispara el nodo n, uno por patrón (un
// literal puede disparar varios). Un selector testcontainers.WithReuseByName se cuenta una
// sola vez: Inspect visita su Sel como *ast.Ident, y es ahí donde se detecta, no en el
// SelectorExpr, para no duplicarlo.
func patronesBDViva(n ast.Node) []string {
	switch x := n.(type) {
	case *ast.Ident:
		if x.Name == "WithReuseByName" {
			return []string{"WithReuseByName"}
		}
	case *ast.BasicLit:
		if x.Kind == token.STRING {
			return patronesLiteral(valorLiteral(x.Value))
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
