//go:build pendiente

package runtime_test

// Porta la REGLA de internal/flujos/runtime/streak_invariante_test.go @ e0159171
// (TestRacha_TodoDeleteCierraElEpisodio en el viejo).
//
// Es uno de los DOS candados AST que la reconstrucción permite (05 E-7, diseno.md §4.2 de
// F8): lee el código de producción como texto porque la regla que vigila no se puede cazar
// barato con conducta.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// streakProductionDir es el directorio cuyos .go de producción recorre el candado: el del
// propio paquete. Es una constante —y no una lista de nombres de fichero— a propósito:
// incoming.go y events.go nacen PARTIDOS POR TEMA en el verde (E-13), y un candado atado a
// dos nombres dejaría de ver los Delete que se muden a incoming_<tema>.go sin enterarse.
const streakProductionDir = "."

// expectedDeletes es el número de caminos que borran el estado de la conversación.
//
// 🔴 HAY QUE RE-MEDIRLA sobre el código nuevo al pasar a verde incoming.go y events.go
// (F8-04b). Hoy vale lo que mide el viejo: 6 —events.go:716 (la entrada a un evento con
// flujo) y :1221 (la suelta por inactividad del evento); incoming.go:239 (el TTL del limbo),
// :450 (la suelta del estado terminal), :489 (la suelta del menú huérfano) y :1077 (el
// escape)—. Con los contratos sin lógica el candado ve 0 y por eso está en rojo.
const expectedDeletes = 6

// streakSlack es cuántas sentencias del MISMO bloque, a partir de la del Delete, pueden
// separar el borrado de su cierre.
const streakSlack = 3

// TestStreak_EveryDeleteClosesTheEpisode es el candado de rachas (RT-11, EV-10; Plan 049 ·
// Opción A; trampa T-12 de la fase): TODO `rt.store.Delete` del runtime va seguido, en el
// mismo bloque y a no más de 3 sentencias, de un `rt.autoreplyStreaks.Close`. Morir el
// estado de la conversación ES el fin del episodio; si un camino borra el estado y no
// cierra la racha, esa racha se queda huérfana en el mapa hasta que vence por inactividad
// —media hora más tarde— y para entonces ya infló wapp_flow_autoreply_streak_max y llegó
// tarde al histograma.
//
// 🔴 Por qué es estructural y no de conducta. Lo escribió un informe de mutación: de los
// seis cierres, CINCO se podían borrar con la suite entera en verde. Quitar un cierre no
// rompe nada visible, solo publica un número inflado en silencio, y cazarlo con conducta
// pediría un test por camino (TTL vencido, escape, entrada a evento, menú sin flujo…) y aun
// así el séptimo camino que alguien añada mañana seguiría sin cubrir. Este cubre los seis de
// golpe y el que venga después.
//
// Lo que NO prueba, para no confiarse: que el Close se ejecute (podría estar tras un return
// inalcanzable) ni que reciba la clave correcta. Eso lo cubren los tests de conducta de
// streak.go y los del plano de eventos; este vigila la COBERTURA de los caminos, aquellos la
// SEMÁNTICA de uno.
func TestStreak_EveryDeleteClosesTheEpisode(t *testing.T) {
	fset := token.NewFileSet()
	files := streakParseProduction(t, fset)

	deletes, orphans := streakFindDeletesWithoutClose(fset, files)

	// Si el paquete se reorganiza y el candado deja de VER los Delete, se volvería verde sin
	// comprobar nada: el modo de fallo clásico de un test estructural. Por eso la cuenta es
	// EXACTA: si cambia, que alguien lo mire a conciencia en vez de que el test calle.
	if deletes != expectedDeletes {
		t.Errorf("se encontraron %d llamadas a store.Delete y se esperaban %d. "+
			"Si has añadido o quitado un camino que borra el estado de la conversación, "+
			"actualiza la constante expectedDeletes Y comprueba que el camino nuevo cierra su episodio; "+
			"si has movido o partido el paquete, arregla el recorrido antes de fiarte del verde.",
			deletes, expectedDeletes)
	}

	for _, orphan := range orphans {
		t.Errorf("%s: este store.Delete NO cierra la racha de auto-respuestas. "+
			"Borrar el estado de la conversación es el fin del episodio: añade "+
			"rt.autoreplyStreaks.Close(key, rt.now()) justo después (Plan 049 · Opción A). "+
			"Sin él, la racha se queda huérfana media hora inflando "+
			"wapp_flow_autoreply_streak_max y llega tarde al histograma.", orphan)
	}
}

// streakParseProduction lee TODOS los .go del directorio del paquete SIN los _test.go: el
// invariante es sobre el código que corre en campo, no sobre los andamios de los tests. No
// baja a subdirectorios (runtimehelpertest son dobles).
func streakParseProduction(t *testing.T, fset *token.FileSet) []*ast.File {
	t.Helper()

	entries, err := os.ReadDir(streakProductionDir)
	if err != nil {
		t.Fatalf("no se pudo listar el paquete: %v", err)
	}
	var files []*ast.File
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(streakProductionDir, name), nil, 0)
		if err != nil {
			t.Fatalf("no se pudo parsear %s: %v", name, err)
		}
		files = append(files, file)
	}
	if len(files) == 0 {
		t.Fatal("no se parseó ni un fichero de producción: el candado no está mirando nada")
	}
	return files
}

// streakFindDeletesWithoutClose devuelve cuántos store.Delete hay y las posiciones de los
// que no cierran su episodio.
//
// Tolerancia: el Close casi siempre es la sentencia siguiente al `if err := Delete`, pero se
// admite algo de holgura para no romper por un log intercalado. Lo que NO se admite es que
// esté en otro bloque: un Close en otra rama del árbol no se ejecuta en este camino, que es
// precisamente el fallo que se persigue.
func streakFindDeletesWithoutClose(fset *token.FileSet, files []*ast.File) (int, []string) {
	var deletes int
	var orphans []string

	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			block, ok := n.(*ast.BlockStmt)
			if !ok {
				return true
			}
			for i, stmt := range block.List {
				if !streakContainsCall(stmt, "store", "Delete") || streakWrapsAnotherDelete(stmt) {
					continue
				}
				deletes++
				if !streakClosesAfter(block, i) {
					orphans = append(orphans, fset.Position(stmt.Pos()).String())
				}
			}
			return true
		})
	}
	return deletes, orphans
}

// streakClosesAfter mira si alguna de las siguientes sentencias del MISMO bloque, hasta la
// holgura, cierra la racha.
func streakClosesAfter(block *ast.BlockStmt, from int) bool {
	for j := from + 1; j <= from+streakSlack && j < len(block.List); j++ {
		if streakContainsCall(block.List[j], "autoreplyStreaks", "Close") {
			return true
		}
	}
	return false
}

// streakContainsCall dice si la sentencia contiene, a cualquier profundidad, una llamada del
// tipo `<lo que sea>.receiver.method(...)`. Se mira el selector y no el tipo real porque el
// candado corre sobre el AST, sin información de tipos: es una comprobación de forma,
// deliberadamente barata.
func streakContainsCall(stmt ast.Stmt, receiver, method string) bool {
	found := false
	ast.Inspect(stmt, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != method {
			return true
		}
		// El receptor es el selector de la izquierda: rt.store.Delete → "store".
		if inner, ok := sel.X.(*ast.SelectorExpr); ok && inner.Sel.Name == receiver {
			found = true
		}
		return true
	})
	return found
}

// streakWrapsAnotherDelete dice si la sentencia contiene un bloque anidado que ya tiene, él
// mismo, una sentencia con el Delete: en ese caso esta es la envolvente y no el camino.
func streakWrapsAnotherDelete(stmt ast.Stmt) bool {
	nested := false
	ast.Inspect(stmt, func(n ast.Node) bool {
		if nested {
			return false
		}
		block, ok := n.(*ast.BlockStmt)
		if !ok {
			return true
		}
		for _, s := range block.List {
			if streakContainsCall(s, "store", "Delete") {
				nested = true
				return false
			}
		}
		return true
	})
	return nested
}
