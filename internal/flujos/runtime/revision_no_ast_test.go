package runtime

// revision_no_ast_test.go — EL `revision_no` DEL CONTRATO NO PUEDE SER UNA
// CONSTANTE, y se vigila sobre el AST de todo el paquete (Plan 044 · T4.10,
// criterio (b)).

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestRevisionNo_NingunaConstanteEnElPaquete recorre TODOS los ficheros de
// producción de internal/flujos/runtime/ y exige que cada sitio donde se fija el
// campo RevisionNo del contrato wapp-crm-v1 tome su valor de algo CALCULADO —hoy
// modules.AsInt(eff.Payload["revision_no"]), el número que la base le asignó a la
// revisión— y nunca de un literal ni de una constante del paquete.
//
// 🔴 QUÉ VUELVE A ROMPER, Y POR QUÉ NINGÚN TEST DE CONDUCTA LO CAZA SOLO. Hasta
// esta tarea el sink emitía `RevisionNo: 1` fijo. El puente CRM hace UPSERT por
// (intake_id, revision_no) y descarta todo par repetido como duplicado del mismo
// estado —es lo que el manual del integrador le pide hacer, §4—, así que dos
// pushes de la misma solicitud con el mismo número dejan al CRM con el PRIMER
// estado para siempre: el pedido corregido no llega, y no hay error en ningún log
// ni en ninguna métrica. Un test de conducta sobre un solo push pasa igual con la
// constante (el número coincide por casualidad cuando la revisión es la 1), y ese
// «por casualidad» dejó de valer con T4.0: el pipeline del 044 cuelga su revisión
// `interpreted` de la MISMA fila que el carrito dejó en `open`, así que el cierre
// escribe la 2 y el literal ya mentía en producción.
//
// Lo que este test NO prueba: que el número sea el CORRECTO. Eso lo prueban los
// tests de conducta (webhook_sink_revision_test.go y el e2e de
// revision_no_e2e_test.go). Éste solo vigila que nadie vuelva a clavarlo.
func TestRevisionNo_NingunaConstanteEnElPaquete(t *testing.T) {
	consts, sitios := barreElPaquete(t)

	// Un test estructural que no ve nada pasa siempre: si el campo se renombra o el
	// sink se muda de paquete, esto tiene que cortar, no dar verde.
	if len(sitios) == 0 {
		t.Fatal("no se encontró ni un solo sitio que fije RevisionNo en internal/flujos/runtime/. " +
			"Si el campo se renombró o el sink se mudó, arregla este barrido ANTES de fiarte del verde")
	}

	for _, s := range sitios {
		if motivo := esConstante(s.valor, consts); motivo != "" {
			t.Errorf("🔴 %s: RevisionNo se fija con %s. El contrato wapp-crm-v1 clavea por "+
				"(intake_id, revision_no) y el puente descarta los pares repetidos: un número fijo "+
				"hace que el CRM se quede con el primer estado de la solicitud PARA SIEMPRE, sin un "+
				"solo error. Léelo del efecto (modules.AsInt(eff.Payload[\"revision_no\"])), que es el "+
				"número que la base asignó a la revisión.", s.donde, motivo)
		}
	}
}

// sitio es un punto del código donde se fija RevisionNo, con el fichero:línea para
// que el rojo diga DÓNDE sin que nadie tenga que grepear.
type sitio struct {
	donde string
	valor ast.Expr
}

// barreElPaquete parsea los ficheros de producción del directorio del paquete y
// devuelve las constantes declaradas en él (para reconocer `RevisionNo: revUno`,
// que es la misma trampa con otro disfraz) y todos los sitios que fijan el campo.
//
// Se leen los ficheros a mano en vez de con parser.ParseDir —deprecado desde Go
// 1.22— y se excluyen los _test.go a propósito: un fixture de test SÍ puede fijar
// un número, y de hecho lo hace.
func barreElPaquete(t *testing.T) (map[string]bool, []sitio) {
	t.Helper()
	entradas, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("no se pudo listar el paquete: %v", err)
	}

	consts := map[string]bool{}
	var sitios []sitio
	vistos := 0
	for _, e := range entradas {
		nombre := e.Name()
		if e.IsDir() || !strings.HasSuffix(nombre, ".go") || strings.HasSuffix(nombre, "_test.go") {
			continue
		}
		vistos++
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, nombre, nil, 0)
		if err != nil {
			t.Fatalf("no se pudo parsear %s: %v", nombre, err)
		}
		recogeConstantes(f, consts)
		sitios = append(sitios, sitiosDeRevisionNo(fset, nombre, f)...)
	}
	if vistos == 0 {
		t.Fatal("el barrido no leyó ni un fichero de producción: no está mirando nada")
	}
	return consts, sitios
}

// recogeConstantes apunta los nombres declarados con `const` en el fichero.
func recogeConstantes(f *ast.File, consts map[string]bool) {
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, n := range vs.Names {
				consts[n.Name] = true
			}
		}
	}
}

// sitiosDeRevisionNo localiza las DOS formas de fijar el campo: dentro de un
// literal compuesto (`intakePushTemplate{… RevisionNo: x …}`) y por asignación
// posterior (`p.RevisionNo = x`), que es como se colaría el arreglo «rápido».
func sitiosDeRevisionNo(fset *token.FileSet, fichero string, f *ast.File) []sitio {
	var out []sitio
	pos := func(n ast.Node) string {
		p := fset.Position(n.Pos())
		return fichero + ":" + strconv.Itoa(p.Line)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.KeyValueExpr:
			if id, ok := v.Key.(*ast.Ident); ok && id.Name == "RevisionNo" {
				out = append(out, sitio{donde: pos(v), valor: v.Value})
			}
		case *ast.AssignStmt:
			for i, lhs := range v.Lhs {
				sel, ok := lhs.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "RevisionNo" || i >= len(v.Rhs) {
					continue
				}
				out = append(out, sitio{donde: pos(v), valor: v.Rhs[i]})
			}
		}
		return true
	})
	return out
}

// esConstante devuelve el motivo del rojo, o "" si el valor es algo calculado.
// Reconoce el literal pelado (`1`), el literal con signo (`-1`, que además el
// schema rechaza) y el ident que apunta a una constante del paquete.
func esConstante(v ast.Expr, consts map[string]bool) string {
	switch e := v.(type) {
	case *ast.BasicLit:
		return "el literal " + e.Value
	case *ast.UnaryExpr:
		if lit, ok := e.X.(*ast.BasicLit); ok {
			return "el literal " + e.Op.String() + lit.Value
		}
	case *ast.Ident:
		if consts[e.Name] {
			return "la constante del paquete " + e.Name
		}
	}
	return ""
}
