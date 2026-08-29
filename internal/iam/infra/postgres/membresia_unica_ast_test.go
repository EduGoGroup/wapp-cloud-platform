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
// tenant_members que nadie decidió darle. El defecto está en el código que NO
// llama a la guarda, y a eso no se llega ejercitando el que sí; hay que
// preguntarle al AST.
//
// 🔧 El daño concreto cambió el 2026-08-29 y el candado NO: hasta el Plan 047 ·
// Ola 5 · T5.1 esas dos filas dejaban a la persona sin poder canjear
// (ErrMultipleTenants, hoy retirado); ahora el canje las resuelve por la empresa
// activa (D-047.14). Sigue habiendo UN solo escritor porque el alta en una
// segunda empresa tiene que ser una decisión, no un efecto colateral — MD-055.2
// decide cuándo y cómo se permite.
//
// 🚨 GUARDA ANTI-HUECO: un barrido que no encuentra nada pasa siempre. Por eso
// exige encontrar EXACTAMENTE el escritor conocido antes de que su veredicto
// signifique algo — si la tabla se renombra o el SQL se compone por trozos, este
// candado falla y se entera alguien, en vez de vigilar una pared.
//
// ════════════════════════════════════════════════════════════════════════════
// 🔒 ENDURECIDO el 2026-08-29 (Plan 047 · Ola 5 · T5.2): «llama» no bastaba
// ════════════════════════════════════════════════════════════════════════════
// Hasta hoy el candado preguntaba una sola cosa: ¿aparece EN ALGÚN PUNTO del
// fichero una llamada a la guarda? Eso lo cumple un fichero que la llame y NO la
// evalúe. El ejemplo no es hipotético, es justo la forma que T5.2 pone sobre la
// mesa al hacer condicional el rechazo:
//
//	if multiEmpresa {
//	    // escribe y sale, saltándose el conteo
//	    return escribirMembresia(ctx, exec, userID, tenantID)
//	}
//	others, err := countOtherMemberships(...)   // sigue estando: verde
//
// El barrido sintáctico de antes daba VERDE ahí, con la guarda muerta. Y la
// variante de extraer el INSERT a un ayudante del mismo fichero también: el
// ayudante no llama a nada y el que llama ya no inserta.
//
// Por eso ahora se exigen CUATRO cosas a cada función que contenga el INSERT, y
// las tres últimas son de ORDEN y de ANIDAMIENTO, que es lo que un test de
// conducta no puede ver (una transacción que hace rollback borra la diferencia
// entre «contó y escribió» y «escribió sin contar» cuando el desenlace coincide):
//
//  1. LLAMA a la guarda contable (countOtherMemberships).
//  2. La llama en el CUERPO de la función, no dentro de un if/for/switch ni de
//     un func literal: una guarda condicionada es una guarda que alguien puede
//     rodear con una condición nueva.
//  3. La llamada va ANTES —por posición en el fichero— de cualquier INSERT.
//  4. Y el CERROJO (pg_advisory_xact_lock) va antes que la guarda, que es lo que
//     cierra la ventana TOCTOU de T5.2: contar sin haber tomado el cerrojo es
//     contar un estado que otra transacción puede estar cambiando.

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
	// cerrojoDeLaPersona es el literal del advisory lock que serializa las altas
	// de un mismo usuario (Plan 047 · Ola 5 · T5.2). Sin él, la guarda cuenta un
	// estado que otra transacción puede estar cambiando a la vez.
	cerrojoDeLaPersona = "pg_advisory_xact_lock"
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

		for _, fn := range funcionesConLiteral(archivo, insertDeMembresia) {
			verificarEscritor(t, fset, rel, fn)
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

// verificarEscritor aplica las CUATRO exigencias de la cabecera a una función
// que escribe en tenant_members. Se le pasa el fset para poder hablar de
// posiciones en el mensaje de error: un candado estructural que dice «está mal»
// sin decir dónde manda a leer el fichero entero.
func verificarEscritor(t *testing.T, fset *token.FileSet, rel string, fn *ast.FuncDecl) {
	t.Helper()

	llamadas := llamadasDirectasEnElCuerpo(fn, guardaCompartida)
	if len(llamadas) == 0 {
		anidada := posicionDeLaLlamada(fn, guardaCompartida)
		if anidada != token.NoPos {
			t.Errorf("%s · %s llama a %s DENTRO de un if/for/switch (%s): la guarda tiene que evaluarse "+
				"SIEMPRE. Condicionarla es exactamente cómo se rodea sin ponerse en rojo.",
				rel, fn.Name.Name, guardaCompartida, fset.Position(anidada))
			return
		}
		t.Errorf("%s · %s inserta en tenant_members y NO llama a %s: puede escribir una segunda empresa "+
			"a espaldas de la guarda (MD-055.2, Plan 047 · Ola 5 · T5.2)", rel, fn.Name.Name, guardaCompartida)
		return
	}
	posGuarda := llamadas[0]

	for _, posInsert := range posicionesDeLiteral(fn, insertDeMembresia) {
		if posInsert < posGuarda {
			t.Errorf("%s · %s tiene un INSERT en tenant_members (%s) ANTES de la guarda %s (%s): "+
				"hay un camino que escribe sin contar. Que la llamada exista más abajo no la evalúa.",
				rel, fn.Name.Name, fset.Position(posInsert), guardaCompartida, fset.Position(posGuarda))
		}
	}

	cerrojos := posicionesDeLiteral(fn, cerrojoDeLaPersona)
	if len(cerrojos) == 0 {
		t.Errorf("%s · %s cuenta y escribe SIN tomar %s: vuelve a abrirse la ventana TOCTOU que T5.2 "+
			"cerró (dos altas simultáneas de la misma persona cuentan cero las dos y escriben las dos).",
			rel, fn.Name.Name, cerrojoDeLaPersona)
		return
	}
	if cerrojos[0] > posGuarda {
		t.Errorf("%s · %s toma %s (%s) DESPUÉS de la guarda %s (%s): el cerrojo tiene que preceder al "+
			"conteo o no protege el conteo.",
			rel, fn.Name.Name, cerrojoDeLaPersona, fset.Position(cerrojos[0]), guardaCompartida,
			fset.Position(posGuarda))
	}
}

// funcionesConLiteral devuelve las funciones del archivo que contienen el texto
// dado en alguno de sus literales de cadena.
func funcionesConLiteral(archivo *ast.File, texto string) []*ast.FuncDecl {
	var res []*ast.FuncDecl
	for _, decl := range archivo.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		if len(posicionesDeLiteral(fn, texto)) > 0 {
			res = append(res, fn)
		}
	}
	return res
}

// posicionesDeLiteral devuelve, en orden, las posiciones de los literales de
// cadena de fn que contienen el texto dado.
func posicionesDeLiteral(fn *ast.FuncDecl, texto string) []token.Pos {
	var res []token.Pos
	ast.Inspect(fn, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if ok && lit.Kind == token.STRING && strings.Contains(lit.Value, texto) {
			res = append(res, lit.Pos())
		}
		return true
	})
	slices.Sort(res)
	return res
}

// llamadasDirectasEnElCuerpo devuelve las posiciones de las llamadas a `nombre`
// que son SENTENCIAS del cuerpo de la función —incluida la forma
// `x, err := nombre(...)`, que es la que usa el código real—, y NO las que
// cuelgan de un if, un for, un switch o un func literal.
//
// La distinción es el corazón del endurecimiento: una guarda dentro de un `if`
// se salta cambiando la condición, y el barrido de antes no notaba la
// diferencia.
func llamadasDirectasEnElCuerpo(fn *ast.FuncDecl, nombre string) []token.Pos {
	var res []token.Pos
	for _, stmt := range fn.Body.List {
		var exprs []ast.Expr
		switch st := stmt.(type) {
		case *ast.AssignStmt:
			exprs = st.Rhs
		case *ast.ExprStmt:
			exprs = []ast.Expr{st.X}
		default:
			continue
		}
		for _, e := range exprs {
			if call, ok := e.(*ast.CallExpr); ok && nombreDeLaLlamada(call) == nombre {
				res = append(res, call.Pos())
			}
		}
	}
	slices.Sort(res)
	return res
}

// posicionDeLaLlamada devuelve la posición de la PRIMERA llamada a `nombre` en
// cualquier punto de fn (anidada o no), o token.NoPos. Sirve para distinguir
// «no la llama» de «la llama condicionada», que merecen mensajes distintos.
func posicionDeLaLlamada(fn *ast.FuncDecl, nombre string) token.Pos {
	pos := token.NoPos
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || nombreDeLaLlamada(call) != nombre {
			return true
		}
		pos = call.Pos()
		return false
	})
	return pos
}

// nombreDeLaLlamada devuelve el identificador final de la función llamada:
// `f(...)` → "f" y `pkg.F(...)` → "F". El receptor o el paquete no importan.
func nombreDeLaLlamada(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		return fn.Sel.Name
	}
	return ""
}
