package candados

import (
	"go/ast"
	"go/token"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// UnFicheroUnTest exige que cada x.go de producción de fuentes tenga su x_test.go en el
// mismo directorio (05 E-3). fuentes debe traer los tests (Recorrer con incluirTests =
// true): el test se busca SOLO entre fuentes, por Ruta ("dir/x.go" → "dir/x_test.go").
//
// Quedan fuera, sin mirar nada más:
//   - los ficheros de test (EsTest);
//   - todo fichero de un paquete de suite y dobles (D-F1-3 = sí: suites Contrato y dobles;
//     los dobles con lógica llevan igualmente su test, pero eso lo exige la fase, no este
//     candado). Ese paquete es el que dice isHelperTestPackage: su nombre —la cláusula
//     `package`, no el directorio— termina en el sufijo compuesto "helpertest" (D-F1-10,
//     Jhoan, 2026-10-02, que estrecha D-F1-3). Un paquete que termina en "test" a secas NO
//     queda fuera: con esa regla, la de antes, un paquete de producción llamado latest
//     pasaba el candado sin test.
//
// Excepciones de E-3, VERIFICADAS (se comprueba la condición, no se lista el fichero):
//   - doc.go: se exime si no tiene ninguna declaración (ni import): solo `package` y su
//     comentario. Un doc.go con una declaración necesita test como cualquiera;
//   - fichero de //go:embed: se exime si tiene al menos una declaración y todas las que no
//     son import son `var` cuyas especificaciones llevan una directiva //go:embed en su
//     comentario; una sola declaración de otra clase le quita la exención;
//   - puerto (solo interfaces): se exime si tiene al menos una declaración, todas las que no
//     son import son `type X interface{…}` (ni funciones, ni métodos, ni var, ni const, ni
//     tipos de otra clase) Y entre fuentes hay un fichero del paquete <paquete>helpertest
//     en el subdirectorio <dir>/<paquete>helpertest (p. ej.
//     internal/nucleo/contact/contacthelpertest; es el patrón de
//     internal/gateway/fleet/fleettest con el sufijo de D-F1-10) que declara una función de
//     primer nivel `Contrato` cuyo primer parámetro es *testing.T. Sin esa suite entre
//     fuentes, el puerto necesita test; una suite en <paquete>test, el nombre viejo, no vale;
//   - puerto de ENTRADA sin suite por decisión (D-F2-5, Jhoan, 2026-09-30; excepción en el
//     candado, 2026-10-04): el mismo fichero de solo interfaces se exime SIN suite si su
//     directorio, path.Dir(Ruta), es IGUAL a uno de InboundPortDirsWithoutSuite (hoy solo
//     internal/modulos/acceso/iam/ports/in: lo cubre el test del usecase que lo implementa).
//     Ni un subdirectorio del listado ni un hermano con el mismo prefijo heredan la excepción,
//     y en el directorio listado un fichero con una func, var, const o tipo que no sea
//     interfaz necesita test como cualquiera. Ampliar la lista exige decisión escrita.
//
// Una violación por fichero sin test y sin excepción: Fichero es la Ruta del x.go y Motivo
// contiene "falta x_test.go" (el nombre base del test que falta).
func UnFicheroUnTest(fuentes []Fuente) []Violacion {
	// El test se busca solo entre fuentes (no en disco): así el candado no lee el disco y un
	// recorrido sin tests muerde en vez de mirar lo que no se le dio.
	tests := make(map[string]bool)
	for _, f := range fuentes {
		if f.EsTest {
			tests[f.Ruta] = true
		}
	}
	vs := make([]Violacion, 0)
	for _, f := range fuentes {
		// D-F1-3 = sí: un paquete …helpertest entero (suite Contrato y dobles) queda fuera;
		// el sufijo es el de D-F1-10 (uno que acaba en "test" a secas, como latest, no).
		if f.EsTest || isHelperTestPackage(f.Paquete) {
			continue
		}
		test := strings.TrimSuffix(f.Ruta, ".go") + "_test.go"
		if tests[test] || eximido(f, fuentes) {
			continue
		}
		vs = append(vs, Violacion{Fichero: f.Ruta, Motivo: "falta " + path.Base(test)})
	}
	sort.Slice(vs, func(i, j int) bool {
		if vs[i].Fichero != vs[j].Fichero {
			return vs[i].Fichero < vs[j].Fichero
		}
		return vs[i].Motivo < vs[j].Motivo
	})
	return vs
}

// eximido aplica las excepciones verificadas de E-3 (doc.go, //go:embed y puerto, este con
// suite Contrato o en un directorio de InboundPortDirsWithoutSuite, D-F2-5). Se comprueba la
// condición, nunca el nombre suelto: un doc.go con lógica o un «puerto» con una función
// necesitan test, también en un directorio de la lista.
func eximido(f Fuente, fuentes []Fuente) bool {
	decls := declsSinImport(f.Archivo)
	if path.Base(f.Ruta) == "doc.go" && len(f.Archivo.Decls) == 0 {
		// Ni siquiera un import: un doc.go solo lleva `package` y su comentario.
		return true
	}
	if len(decls) == 0 {
		return false
	}
	if todas(decls, esVarEmbed) {
		return true
	}
	// El puerto sin suite solo se exime en un directorio de la lista cerrada de D-F2-5, por
	// IGUALDAD de directorio: ni un subdirectorio ni un hermano con el mismo prefijo.
	return todas(decls, esTipoInterfaz) &&
		(haySuiteContrato(f, fuentes) || slices.Contains(InboundPortDirsWithoutSuite(), path.Dir(f.Ruta)))
}

// declsSinImport devuelve las declaraciones de primer nivel que no son import.
func declsSinImport(a *ast.File) []ast.Decl {
	var ds []ast.Decl
	for _, d := range a.Decls {
		if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.IMPORT {
			continue
		}
		ds = append(ds, d)
	}
	return ds
}

func todas(ds []ast.Decl, cumple func(ast.Decl) bool) bool {
	for _, d := range ds {
		if !cumple(d) {
			return false
		}
	}
	return true
}

// esVarEmbed: una `var` cuyas especificaciones llevan todas //go:embed. En una `var X`
// suelta el parser cuelga el comentario de la declaración, no de la especificación; en un
// grupo `var ( … )`, de cada especificación.
func esVarEmbed(d ast.Decl) bool {
	g, ok := d.(*ast.GenDecl)
	if !ok || g.Tok != token.VAR || len(g.Specs) == 0 {
		return false
	}
	for _, s := range g.Specs {
		vs, ok := s.(*ast.ValueSpec)
		if !ok {
			return false
		}
		doc := vs.Doc
		if !g.Lparen.IsValid() {
			doc = g.Doc
		}
		if !tieneEmbed(doc) {
			return false
		}
	}
	return true
}

// tieneEmbed mira las líneas crudas: CommentGroup.Text() descarta las directivas.
func tieneEmbed(cg *ast.CommentGroup) bool {
	if cg == nil {
		return false
	}
	for _, c := range cg.List {
		if c.Text == "//go:embed" || strings.HasPrefix(c.Text, "//go:embed ") {
			return true
		}
	}
	return false
}

// esTipoInterfaz: un `type` cuyas especificaciones son todas `X interface{…}`.
func esTipoInterfaz(d ast.Decl) bool {
	g, ok := d.(*ast.GenDecl)
	if !ok || g.Tok != token.TYPE || len(g.Specs) == 0 {
		return false
	}
	for _, s := range g.Specs {
		ts, ok := s.(*ast.TypeSpec)
		if !ok {
			return false
		}
		if _, ok := ts.Type.(*ast.InterfaceType); !ok {
			return false
		}
	}
	return true
}

// haySuiteContrato busca, entre fuentes, la suite del puerto f: un fichero del paquete
// <paquete>helpertest en <dir>/<paquete>helpertest con `func Contrato(t *testing.T, …)` de
// primer nivel. Tienen que casar LOS DOS, el nombre del paquete y el directorio.
//
// El sufijo es helperTestSuffix, el mismo con el que isHelperTestPackage exime a ese paquete
// (D-F1-10, Jhoan, 2026-10-02): así la suite que exime al puerto es siempre un paquete que
// los candados reconocen como suite. Hasta D-F1-10 se buscaba en <paquete>test; una suite que
// siga ahí ya no exime al puerto (ni está exenta ella: necesita su test), y tampoco un
// paquete llamado "helpertest" a secas, que no lleva el nombre de f delante.
func haySuiteContrato(f Fuente, fuentes []Fuente) bool {
	paquete := f.Paquete + helperTestSuffix
	dir := path.Join(path.Dir(f.Ruta), paquete)
	for _, s := range fuentes {
		if s.Paquete != paquete || path.Dir(s.Ruta) != dir {
			continue
		}
		for _, d := range s.Archivo.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok && esContrato(fn, nombreImport(s.Archivo, "testing")) {
				return true
			}
		}
	}
	return false
}

// esContrato: función (no método) llamada Contrato cuyo primer parámetro es *<testing>.T,
// con <testing> el nombre local con el que el fichero importa "testing".
func esContrato(fn *ast.FuncDecl, testing string) bool {
	if fn.Recv != nil || fn.Name.Name != "Contrato" || testing == "" {
		return false
	}
	ps := fn.Type.Params.List
	if len(ps) == 0 {
		return false
	}
	star, ok := ps[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "T" {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	return ok && x.Name == testing
}

// nombreImport devuelve el nombre local con el que a importa ruta ("" si no la importa):
// el alias si lo hay, si no el último elemento de la ruta.
func nombreImport(a *ast.File, ruta string) string {
	for _, imp := range a.Imports {
		if p, err := strconv.Unquote(imp.Path.Value); err != nil || p != ruta {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		return path.Base(ruta)
	}
	return ""
}
