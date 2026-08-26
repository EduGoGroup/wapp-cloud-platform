package cart

// orden_consulta_ast_test.go — EL ORDEN DENTRO DE Step ES UN INVARIANTE, y se
// vigila sobre el AST (Plan 044 · Ola 3.5 · T3.5-2).

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestOrden_LaConsultaVaANTESDeToda MUTACION comprueba que, dentro de Module.Step,
// la traducción de la entrada (m.preresolveOConsulta) y el return de la PETICIÓN
// preceden a las dos únicas mutaciones del método: `st.Started = true` —que declara
// cart_started EXACTAMENTE una vez— y la llamada a advance(), que es quien mueve el
// contador de inválidos.
//
// 🔴 QUÉ MUTACIÓN LO PONE ROJO, Y POR QUÉ NINGÚN TEST DE CONDUCTA LA CAZA.
// Mover el bloque `input, consulta := m.preresolveOConsulta(...)` + `if consulta !=
// nil { return … }` por DEBAJO de `if !st.Started { st.Started = true; … }` deja la
// suite ENTERA en verde —comprobado por mutación antes de escribir este test— y
// rompe el mecanismo de la peor manera posible: la primera pasada declararía
// cart_started y el engine descartaría ese Result, la segunda pasada volvería a
// declararlo... y esa sí se despacha. Un efecto duplicado por cada mensaje que
// necesite consulta. Con el contador de inválidos pasa lo mismo: dos inválidos
// contados por UN mensaje del cliente, y el menú de salida armado a destiempo.
//
// No lo caza un test de conducta barato porque el observable de las dos versiones
// es idéntico en todos los caminos que NO consultan (el 99 %), y en los que
// consultan hace falta un doble de resolutor Y mirar los efectos de la pasada
// descartada, que por definición no salen. Es la misma clase de defecto que llevó
// al invariante estructural de las rachas (runtime/streak_invariante_test.go): la
// pregunta que de verdad falla en la práctica no es «¿se comporta bien este
// camino?» sino «¿alguien ha movido esta línea?».
//
// Lo que este test NO prueba: que la traducción sea correcta, ni que el módulo
// pregunte cuándo debe. Eso es consulta_test.go. Este solo vigila el ORDEN.
func TestOrden_LaConsultaVaANTESDeTodaMutacion(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "cart.go", nil, 0)
	if err != nil {
		t.Fatalf("no se pudo parsear cart.go: %v", err)
	}
	p := piezasDelOrden(fset, cuerpoDeStep(t, f))

	// Si Step se reorganiza y este test deja de VER alguna de las cuatro piezas, se
	// volvería verde sin comprobar nada: el modo de fallo clásico de un test
	// estructural. Por eso se exige exactamente una de cada.
	unaSola(t, "la llamada a m.preresolveOConsulta", p.consulta)
	unaSola(t, "el return con la PETICIÓN de consulta (modules.Result{… Consulta: …})", p.peticion)
	unaSola(t, "la asignación st.Started = true", p.started)
	unaSola(t, "la llamada a advance(", p.avance)

	if p.consulta[0] > p.peticion[0] {
		t.Error("el return de la petición está ANTES de la llamada que la produce")
	}
	if p.peticion[0] > p.started[0] {
		t.Errorf("🔴 el carrito muta st.Started ANTES de poder devolver la petición de consulta. "+
			"La primera pasada declararía cart_started, el engine la descartaría y la segunda "+
			"volvería a declararlo: efecto DUPLICADO por cada mensaje que necesite consulta. "+
			"Devuelve la petición antes de tocar nada (cart.go, offsets %d vs %d).", p.peticion[0], p.started[0])
	}
	if p.peticion[0] > p.avance[0] {
		t.Errorf("🔴 el carrito llama a advance() ANTES de poder devolver la petición de consulta. "+
			"advance mueve el contador de inválidos: con las dos pasadas se contarían DOS "+
			"inválidos por UN mensaje del cliente y el menú de salida se armaría a destiempo "+
			"(cart.go, offsets %d vs %d).", p.peticion[0], p.avance[0])
	}
}

// piezas guarda el OFFSET en el fichero de cada una de las cuatro cosas que este
// invariante ordena. Slices y no un int por pieza: encontrar DOS de algo es tan
// significativo como no encontrar ninguna.
type piezas struct{ consulta, peticion, started, avance []int }

// piezasDelOrden recorre el cuerpo de Step y localiza las cuatro piezas. Vive
// aparte del test porque gocyclo imputa el FuncLit de ast.Inspect a la función
// madre, y el test es a la vez el que explica el invariante.
func piezasDelOrden(fset *token.FileSet, cuerpo *ast.BlockStmt) piezas {
	var p piezas
	offset := func(n ast.Node) int { return fset.Position(n.Pos()).Offset }
	ast.Inspect(cuerpo, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.CallExpr:
			switch fn := v.Fun.(type) {
			case *ast.SelectorExpr:
				if fn.Sel.Name == "preresolveOConsulta" {
					p.consulta = append(p.consulta, offset(n))
				}
			case *ast.Ident:
				if fn.Name == "advance" {
					p.avance = append(p.avance, offset(n))
				}
			}
		case *ast.ReturnStmt:
			if devuelveConsulta(v) {
				p.peticion = append(p.peticion, offset(n))
			}
		case *ast.AssignStmt:
			if asignaStartedATrue(v) {
				p.started = append(p.started, offset(n))
			}
		}
		return true
	})
	return p
}

// unaSola corta el test si una pieza no aparece EXACTAMENTE una vez.
func unaSola(t *testing.T, nombre string, pos []int) {
	t.Helper()
	if len(pos) != 1 {
		t.Fatalf("se encontraron %d apariciones de %s dentro de Module.Step y se esperaba 1. "+
			"Si has reorganizado el método, arregla este parseo ANTES de fiarte del verde: "+
			"un test estructural que no ve nada pasa siempre.", len(pos), nombre)
	}
}

// cuerpoDeStep localiza el método Module.Step en el fichero parseado.
func cuerpoDeStep(t *testing.T, f *ast.File) *ast.BlockStmt {
	t.Helper()
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "Step" || fn.Recv == nil || fn.Body == nil {
			continue
		}
		return fn.Body
	}
	t.Fatal("no se encontró el método Step en cart.go: el test no está mirando nada")
	return nil
}

// devuelveConsulta reconoce el `return modules.Result{… Consulta: …}` de la
// petición. Se identifica por la CLAVE del literal y no por su posición: da igual
// cómo se llame la variable o en qué orden estén los campos.
func devuelveConsulta(r *ast.ReturnStmt) bool {
	for _, res := range r.Results {
		lit, ok := res.(*ast.CompositeLit)
		if !ok {
			continue
		}
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			if id, ok := kv.Key.(*ast.Ident); ok && id.Name == "Consulta" {
				return true
			}
		}
	}
	return false
}

// asignaStartedATrue reconoce `st.Started = true` (cualquier receptor: lo que
// importa es el campo).
func asignaStartedATrue(a *ast.AssignStmt) bool {
	if len(a.Lhs) != 1 || len(a.Rhs) != 1 {
		return false
	}
	sel, ok := a.Lhs[0].(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Started" {
		return false
	}
	id, ok := a.Rhs[0].(*ast.Ident)
	return ok && id.Name == "true"
}
