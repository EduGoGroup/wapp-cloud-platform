package runtime

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// Invariante estructural del Plan 049 · Opción A: TODO `rt.store.Delete` del runtime
// va acompañado de un `rt.autoreplyStreaks.Close`. Morir el estado de la conversación
// ES el fin del episodio; si un camino borra el estado y no cierra la racha, esa racha
// se queda huérfana en el mapa hasta que vence por inactividad —media hora más tarde—
// y para entonces ya ha inflado el gauge y ha llegado tarde al histograma.
//
// 🔴 POR QUÉ ESTE TEST EXISTE, Y POR QUÉ ES ESTRUCTURAL Y NO DE CONDUCTA.
// Lo escribió el informe de mutación de T2.4, que encontró exactamente lo que el plan
// temía: de los SEIS cierres, **cinco se podían borrar con la suite entera en verde**.
// Solo incoming.go:404 estaba cubierto. Es el modo de fallo más difícil de ver que
// tiene este plan —quitar un cierre no rompe nada visible, solo publica un número
// inflado en silencio— y es justo el que ningún test de conducta caza barato: haría
// falta un test por camino, cada uno montando un escenario distinto (TTL vencido,
// escape, entrada a evento, menú sin flujo…), y aun así el séptimo camino que alguien
// añada mañana seguiría sin cubrir.
//
// Un test estructural, en cambio, cubre los seis de golpe Y el que venga después. La
// pregunta que hace no es «¿se comporta bien este camino?» sino «¿alguien ha añadido
// un Delete sin su Close?», que es la pregunta que de verdad falla en la práctica.
//
// Lo que este test NO prueba, y hay que tenerlo claro para no confiarse: que el Close
// se ejecute (podría estar tras un `return` inalcanzable) ni que reciba la clave
// correcta. Eso lo cubren los tests de conducta —TestRacha_CierreDeConversacionObservaLaRacha
// y TestRacha_MaxBarreYObservaLasVencidas—, y por eso los dos siguen haciendo falta:
// este vigila la COBERTURA de los caminos, aquellos vigilan la SEMÁNTICA de uno.
func TestRacha_TodoDeleteCierraElEpisodio(t *testing.T) {
	fset := token.NewFileSet()
	ficheros := parsearProduccion(t, fset)

	deletes, huerfanos := buscarDeletesSinCierre(fset, ficheros)

	// Si el paquete se reorganiza y este test deja de VER los Delete, se volvería
	// verde sin comprobar nada — el modo de fallo clásico de un test estructural.
	// Seis es el número de caminos que borran estado hoy; si cambia, que alguien lo
	// mire a conciencia en vez de que el test calle.
	const esperados = 6
	if deletes != esperados {
		t.Errorf("se encontraron %d llamadas a store.Delete y se esperaban %d. "+
			"Si has añadido o quitado un camino que borra el estado de la conversación, "+
			"actualiza esta constante Y comprueba que el camino nuevo cierra su episodio; "+
			"si has movido el paquete de sitio, arregla el parseo antes de fiarte del verde.",
			deletes, esperados)
	}

	for _, h := range huerfanos {
		t.Errorf("%s: este store.Delete NO cierra la racha de auto-respuestas. "+
			"Borrar el estado de la conversación es el fin del episodio: añade "+
			"rt.autoreplyStreaks.Close(key, rt.now()) justo después (Plan 049 · Opción A). "+
			"Sin él, la racha se queda huérfana media hora inflando "+
			"wapp_flow_autoreply_streak_max y llega tarde al histograma.", h)
	}
}

// parsearProduccion lee los .go del paquete SIN los _test.go: el invariante es sobre
// el código que corre en campo, no sobre los andamios de los tests.
func parsearProduccion(t *testing.T, fset *token.FileSet) []*ast.File {
	t.Helper()

	entradas, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("no se pudo listar el paquete: %v", err)
	}
	var ficheros []*ast.File
	for _, e := range entradas {
		nombre := e.Name()
		if e.IsDir() || !strings.HasSuffix(nombre, ".go") || strings.HasSuffix(nombre, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, nombre, nil, 0)
		if err != nil {
			t.Fatalf("no se pudo parsear %s: %v", nombre, err)
		}
		ficheros = append(ficheros, f)
	}
	if len(ficheros) == 0 {
		t.Fatal("no se parseó ni un fichero de producción: el test no está mirando nada")
	}
	return ficheros
}

// buscarDeletesSinCierre devuelve cuántos store.Delete hay y las posiciones de los que
// no cierran su episodio.
//
// Tolerancia: el Close casi siempre es la sentencia siguiente al `if err := Delete`,
// pero se admite algo de holgura para no romper por un log intercalado. Lo que NO se
// admite es que esté en otro bloque: un Close en otra rama del árbol no se ejecuta en
// este camino, que es precisamente el fallo que se persigue.
func buscarDeletesSinCierre(fset *token.FileSet, ficheros []*ast.File) (int, []string) {
	const holgura = 3

	var deletes int
	var huerfanos []string

	for _, fichero := range ficheros {
		ast.Inspect(fichero, func(n ast.Node) bool {
			bloque, ok := n.(*ast.BlockStmt)
			if !ok {
				return true
			}
			for i, sent := range bloque.List {
				if !contieneLlamada(sent, "store", "Delete") || envuelveOtroDelete(sent) {
					continue
				}
				deletes++
				if !cierraDespues(bloque, i, holgura) {
					huerfanos = append(huerfanos, fset.Position(sent.Pos()).String())
				}
			}
			return true
		})
	}
	return deletes, huerfanos
}

// cierraDespues mira si alguna de las siguientes sentencias del MISMO bloque cierra la
// racha.
func cierraDespues(bloque *ast.BlockStmt, desde, holgura int) bool {
	for j := desde + 1; j <= desde+holgura && j < len(bloque.List); j++ {
		if contieneLlamada(bloque.List[j], "autoreplyStreaks", "Close") {
			return true
		}
	}
	return false
}

// contieneLlamada dice si la sentencia contiene, a cualquier profundidad, una llamada
// del tipo `<lo que sea>.receptor.metodo(...)`. Se mira el selector y no el tipo real
// porque este test corre sobre el AST del paquete, sin información de tipos: es una
// comprobación de forma, deliberadamente barata.
func contieneLlamada(sent ast.Stmt, receptor, metodo string) bool {
	encontrada := false
	ast.Inspect(sent, func(n ast.Node) bool {
		if encontrada {
			return false
		}
		llamada, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := llamada.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != metodo {
			return true
		}
		// El receptor es el selector de la izquierda: rt.store.Delete → "store".
		if interno, ok := sel.X.(*ast.SelectorExpr); ok && interno.Sel.Name == receptor {
			encontrada = true
		}
		return true
	})
	return encontrada
}

// envuelveOtroDelete dice si la sentencia contiene un bloque anidado que ya tiene, él
// mismo, una sentencia con el Delete: en ese caso esta es la envolvente y no el camino.
func envuelveOtroDelete(sent ast.Stmt) bool {
	anidado := false
	ast.Inspect(sent, func(n ast.Node) bool {
		if anidado {
			return false
		}
		bloque, ok := n.(*ast.BlockStmt)
		if !ok {
			return true
		}
		for _, s := range bloque.List {
			if contieneLlamada(s, "store", "Delete") {
				anidado = true
				return false
			}
		}
		return true
	})
	return anidado
}
