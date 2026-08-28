package iampostgres_test

// membresia_unica_ast_test.go — EL CANDADO DE «UNA SOLA EMPRESA POR USUARIO»
// (Plan 047 · Ola 1.0 · T1.0-2).
//
// El criterio de T1.0-2 pedía UN solo sitio que insertara en
// public.tenant_members, «o dos, con justificación escrita». Es UNO:
// iampostgres.GrantTenantAccess, el caso de uso compartido por las dos vías de
// alta (la bandeja del operador y el plano de administración del tenant).
//
// 🔴 LO QUE VIGILA, y por qué no basta un test de conducta. Un segundo INSERT
// escrito en otro sitio no da error: da un usuario con dos filas en
// tenant_members al que el canje deja de dejar entrar (domain.ErrMultipleTenants,
// MD-055.2). El defecto está en el código que NO llama a la guarda, y a eso no
// se llega ejercitando el que sí; hay que preguntarle al AST.
//
// 🚨 GUARDA ANTI-HUECO: un barrido que no encuentra nada pasa siempre. Por eso
// exige encontrar EXACTAMENTE el escritor conocido antes de que su veredicto
// signifique algo — si la tabla se renombra o el SQL se compone por trozos, este
// candado falla y se entera alguien, en vez de vigilar una pared.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	// raizDelBarrido es internal/ visto desde este paquete.
	raizDelBarrido = "../../.."
	// insertDeMembresia es el literal que marca a un escritor de la tabla.
	insertDeMembresia = "INSERT INTO public.tenant_members"
	// guardaCompartida es la única función que decide si esa alta puede pasar.
	guardaCompartida = "countOtherMemberships"
)

// escritoresEsperados es el ÚNICO sitio que puede dar de alta una membresía, con
// la ruta relativa a raizDelBarrido. Añadir uno aquí sin la guarda no engaña al
// test: la guarda se comprueba aparte, fichero por fichero.
var escritoresEsperados = []string{
	"iam/infra/postgres/memberships.go",
}

// TestMembresiaUnica_TodoElQueInsertaLlamaALaGuarda.
func TestMembresiaUnica_TodoElQueInsertaLlamaALaGuarda(t *testing.T) {
	fset := token.NewFileSet()
	var encontrados []string

	err := filepath.WalkDir(raizDelBarrido, func(ruta string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(ruta, ".go") || strings.HasSuffix(ruta, "_test.go") {
			return nil
		}
		archivo, perr := parser.ParseFile(fset, ruta, nil, 0)
		if perr != nil {
			return perr
		}
		if !contieneLiteral(archivo, insertDeMembresia) {
			return nil
		}

		rel, rerr := filepath.Rel(raizDelBarrido, ruta)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		encontrados = append(encontrados, rel)

		if !llamaA(archivo, guardaCompartida) {
			t.Errorf("%s inserta en tenant_members y NO llama a %s: puede escribir una segunda empresa "+
				"y dejar a esa persona sin poder canjear (domain.ErrMultipleTenants)", rel, guardaCompartida)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("barriendo %s: %v", raizDelBarrido, err)
	}

	slices.Sort(encontrados)
	esperados := slices.Clone(escritoresEsperados)
	slices.Sort(esperados)
	if !slices.Equal(encontrados, esperados) {
		t.Fatalf("escritores de tenant_members = %v, esperaba %v.\n"+
			"Si el barrido no encontró NADA, el candado está vigilando una pared (¿se renombró la tabla, "+
			"o el SQL se compone por trozos?). Si encontró uno de más, alguien duplicó el alta en vez de "+
			"llamar a GrantTenantAccess: o lo reconduce ahí, o documenta por qué existe y se asegura de "+
			"que llama a %s.", encontrados, esperados, guardaCompartida)
	}
}

// contieneLiteral dice si algún literal de cadena del archivo contiene el texto
// dado. Mira el AST y no el fichero en bruto a propósito: así un comentario que
// mencione el INSERT —los hay, y explican justo esto— no cuenta como escritor.
func contieneLiteral(archivo *ast.File, texto string) bool {
	encontrado := false
	ast.Inspect(archivo, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		if strings.Contains(lit.Value, texto) {
			encontrado = true
			return false
		}
		return true
	})
	return encontrado
}

// llamaA dice si el archivo llama en algún punto a la función dada. El receptor
// o el paquete no importan: `CountOtherMemberships(...)` e
// `iampostgres.CountOtherMemberships(...)` cuentan igual.
func llamaA(archivo *ast.File, nombre string) bool {
	encontrado := false
	ast.Inspect(archivo, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			if fn.Name == nombre {
				encontrado = true
			}
		case *ast.SelectorExpr:
			if fn.Sel.Name == nombre {
				encontrado = true
			}
		}
		return !encontrado
	})
	return encontrado
}
