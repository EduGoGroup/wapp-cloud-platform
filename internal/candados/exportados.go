package candados

import (
	"go/ast"
	"go/token"
	"path"
	"sort"
	"strings"
)

// ExportadosCubiertos exige que cada exportado de x.go aparezca como identificador en su
// x_test.go (05 E-9, «se cumple ya en rojo»).
//
// Solo mira los x.go de producción cuyo x_test.go (mismo directorio) está entre fuentes; un
// x.go sin test no es asunto de este candado (lo es de UnFicheroUnTest). Quedan fuera los
// paquetes cuyo nombre termina en "test" (D-F1-3 = sí, la misma condición que
// UnFicheroUnTest).
//
// Exportados de x.go: las funciones, tipos, variables y constantes de primer nivel con nombre
// exportado, y los métodos exportados de sus tipos exportados. Los campos de struct no se
// exigen; los identificadores no exportados, tampoco.
//
// Mención en x_test.go: cualquier *ast.Ident del fichero con ese nombre —un identificador
// suelto en el mismo paquete, o el Sel de un selector (cosa.Hacer) en el paquete externo
// x_test—. Un comentario NO es una mención, ni un literal de cadena. Las etiquetas de
// compilación se ignoran: un test en rojo (`//go:build pendiente`) cuenta.
//
// Una violación por exportado sin mención: Fichero es la Ruta del x.go y Motivo contiene el
// nombre del símbolo (para un método, "Tipo.Metodo") y el nombre base del test ("x_test.go").
func ExportadosCubiertos(fuentes []Fuente) []Violacion {
	// Como UnFicheroUnTest, el test se busca solo entre fuentes: el candado no lee el disco.
	tests := make(map[string]*ast.File)
	for _, f := range fuentes {
		if f.EsTest {
			tests[f.Ruta] = f.Archivo
		}
	}
	vs := make([]Violacion, 0)
	for _, f := range fuentes {
		// D-F1-3 = sí: un paquete …test entero (suite Contrato y dobles) queda fuera.
		if f.EsTest || strings.HasSuffix(f.Paquete, "test") {
			continue
		}
		ruta := strings.TrimSuffix(f.Ruta, ".go") + "_test.go"
		test, ok := tests[ruta]
		if !ok {
			continue // sin test: asunto de UnFicheroUnTest, no de este candado
		}
		menciones := identificadores(test)
		for _, s := range exportados(f.Archivo) {
			if !menciones[s.nombre] {
				vs = append(vs, Violacion{
					Fichero: f.Ruta,
					Motivo:  s.etiqueta + " no aparece como identificador en " + path.Base(ruta),
				})
			}
		}
	}
	sort.Slice(vs, func(i, j int) bool {
		if vs[i].Fichero != vs[j].Fichero {
			return vs[i].Fichero < vs[j].Fichero
		}
		return vs[i].Motivo < vs[j].Motivo
	})
	return vs
}

// simbolo es un exportado de x.go: nombre es lo que debe aparecer como *ast.Ident en el test
// (para un método, el nombre del método: es lo que queda en el Sel de c.Medir()); etiqueta es
// como se nombra en la violación ("Medir" o "Cosa.Medir").
type simbolo struct {
	nombre   string
	etiqueta string
}

// exportados lista los exportados de primer nivel de a y los métodos exportados de sus tipos
// exportados. Los campos de struct no se miran: solo los TypeSpec, no su cuerpo.
func exportados(a *ast.File) []simbolo {
	var ss []simbolo
	for _, d := range a.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			if s, ok := simboloDeFunc(d); ok {
				ss = append(ss, s)
			}
		case *ast.GenDecl:
			ss = append(ss, simbolosDeGen(d)...)
		}
	}
	return ss
}

// simboloDeFunc: una función exportada, o un método exportado cuyo receptor es un tipo
// exportado (un método exportado de un tipo no exportado no se ve desde fuera por su tipo).
func simboloDeFunc(fn *ast.FuncDecl) (simbolo, bool) {
	if !fn.Name.IsExported() {
		return simbolo{}, false
	}
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return simbolo{nombre: fn.Name.Name, etiqueta: fn.Name.Name}, true
	}
	tipo := nombreReceptor(fn.Recv.List[0].Type)
	if !token.IsExported(tipo) {
		return simbolo{}, false
	}
	return simbolo{nombre: fn.Name.Name, etiqueta: tipo + "." + fn.Name.Name}, true
}

// nombreReceptor quita el puntero y los parámetros de tipo del receptor: T, *T, T[K], *T[K, V].
func nombreReceptor(e ast.Expr) string {
	for {
		switch x := e.(type) {
		case *ast.StarExpr:
			e = x.X
		case *ast.ParenExpr:
			e = x.X
		case *ast.IndexExpr:
			e = x.X
		case *ast.IndexListExpr:
			e = x.X
		case *ast.Ident:
			return x.Name
		default:
			return ""
		}
	}
}

// simbolosDeGen: los tipos, variables y constantes exportados de una declaración (import no
// aporta nada: no tiene ValueSpec ni TypeSpec).
func simbolosDeGen(g *ast.GenDecl) []simbolo {
	var ss []simbolo
	for _, spec := range g.Specs {
		var nombres []*ast.Ident
		switch sp := spec.(type) {
		case *ast.TypeSpec:
			nombres = []*ast.Ident{sp.Name}
		case *ast.ValueSpec:
			nombres = sp.Names
		}
		for _, n := range nombres {
			if n.IsExported() {
				ss = append(ss, simbolo{nombre: n.Name, etiqueta: n.Name})
			}
		}
	}
	return ss
}

// identificadores es el conjunto de nombres de *ast.Ident del fichero. Recorrer el AST (y no
// el texto) es lo que deja fuera comentarios y literales de cadena: ninguno es un Ident.
func identificadores(a *ast.File) map[string]bool {
	m := make(map[string]bool)
	ast.Inspect(a, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			m[id.Name] = true
		}
		return true
	})
	return m
}
