package bootstrap

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestPipelineCaptacionCableado fija, contra el ÁRBOL DE SINTAXIS de bootstrap.go, que
// el worker del pipeline de captación (Plan 044 · Ola 2) está de verdad ENCENDIDO:
// enchufado al flanco a READY, arrancado UNA sola vez, y construido CON su aforo.
//
// 🔴 POR QUÉ UN TEST DE AST, mismo molde y mismo motivo que TestCalentamientoCableado:
// las tres cosas que custodia son OMISIBLES SIN ERROR. `Server.OnEdgeReady` es un hook
// opcional con cero-valor útil (nil = «nadie se entera del flanco», que es como nació);
// `go w.Run(ctx)` es una línea suelta que nadie espera ni comprueba; y `ConAforo` es una
// opción variádica cuya ausencia el worker solo GRITA en un Warn del arranque que ningún
// test lee. Olvidar cualquiera de las tres compila, pasa el vet, pasa el lint y deja
// TODOS los demás tests en verde — los del paquete `pipeline` se construyen su propio
// worker y le llaman a `Despertar`/`Drenar` a mano, así que ninguno puede notar que
// producción no los une.
//
// Y el síntoma en campo de cada una es distinto y ninguno da error:
//
//   - sin la asignación: los jobs de un Edge que acaba de recuperar su Ollama esperan a
//     que venza su backoff (hasta 5 min) en vez de arrancar en el acto;
//   - sin el Run: las ventanas cierran, los jobs se quedan `pending` PARA SIEMPRE y no
//     se genera un solo presupuesto — el 7 h 28 min que el plan existe para borrar;
//   - sin el aforo: dos cadenas de lote del mismo Edge se solapan y la espera de un
//     turno interactivo deja de estar acotada a UNA llamada de lote (ADR-0046).
//
// Es exactamente el modo en que falló WithOpeningBuilder: construido, probado y sin
// enchufar durante meses. Este pipeline estuvo ocho tareas completo y apagado.
func TestPipelineCaptacionCableado(t *testing.T) {
	fset := token.NewFileSet()
	archivo, err := parser.ParseFile(fset, "bootstrap.go", nil, 0)
	if err != nil {
		t.Fatalf("parseando bootstrap.go: %v", err)
	}

	// Se recorre el FICHERO y no la función Run: parte del cableado vive en el helper
	// nuevoStackLLMDeCaptacion, del mismo fichero, y lo que importa es que producción
	// lo haga — no en qué función esté escrito.
	var visto cableadoDelPipeline
	ast.Inspect(archivo, func(n ast.Node) bool {
		visto.anota(t, fset, n)
		return true
	})
	visto.exige(t)
}

// cableadoDelPipeline lleva la cuenta de las tres cosas que hay que ver en bootstrap.go.
type cableadoDelPipeline struct {
	asignado  bool // (a) gw.OnEdgeReady = intakePipeline.Despertar
	arranques int  // (b) cuántas veces se hace `go intakePipeline.Run(ctx)`
	conAforo  bool // (c) pipeline.NewWorker(..., pipeline.ConAforo(...))
}

func (c *cableadoDelPipeline) anota(t *testing.T, fset *token.FileSet, n ast.Node) {
	t.Helper()
	switch v := n.(type) {
	case *ast.AssignStmt:
		c.anotaHook(t, fset, v)
	case *ast.GoStmt:
		if campo(v.Call.Fun) == "intakePipeline.Run" {
			c.arranques++
		}
	case *ast.CallExpr:
		c.anotaAforo(t, fset, v)
	}
}

// anotaHook cubre el criterio (a): el flanco a READY llega al worker.
func (c *cableadoDelPipeline) anotaHook(t *testing.T, fset *token.FileSet, asig *ast.AssignStmt) {
	t.Helper()
	if len(asig.Lhs) != 1 || len(asig.Rhs) != 1 || campo(asig.Lhs[0]) != "gw.OnEdgeReady" {
		return
	}
	// No basta con que se asigne ALGO. Tiene que ser `Despertar` del worker: es el único
	// que empuja la plaza al buzón que `Run` atiende, y el único que cumple la exigencia
	// del hook —volver en el acto, porque corre inline en la goroutine del Recv del
	// stream—. Cualquier otra función aquí dejaría el flanco sin efecto o, peor, pararía
	// la recepción de ese Edge.
	if quien := campo(asig.Rhs[0]); quien != "intakePipeline.Despertar" {
		t.Fatalf("gw.OnEdgeReady se asigna con %q y no con intakePipeline.Despertar: solo ese "+
			"avisa al worker del pipeline sin bloquear el bucle Recv del stream, que es lo "+
			"único que el hook exige (%s)", quien, fset.Position(asig.Pos()))
	}
	c.asignado = true
}

// anotaAforo cubre el criterio (c): el worker nace CON el entero del ADR-0046.
func (c *cableadoDelPipeline) anotaAforo(t *testing.T, fset *token.FileSet, llamada *ast.CallExpr) {
	t.Helper()
	if campo(llamada.Fun) != "pipeline.NewWorker" {
		return
	}
	for _, arg := range llamada.Args {
		opt, ok := arg.(*ast.CallExpr)
		if !ok || campo(opt.Fun) != "pipeline.ConAforo" {
			continue
		}
		// El segundo argumento tiene que ser el MISMO selector de vía que usan las
		// etapas: él es quien resuelve a qué Edge apunta una inferencia (y quien sabe
		// que por vía API no hay plaza que tomar). 🔴 Y un `nil` ahí NO da error: la
		// propia ConAforo lo trata como «sin aforo» y vuelve sin hacer nada, así que el
		// entero desaparecería sin que nada lo dijera salvo un Warn del arranque.
		if len(opt.Args) != 2 || campo(opt.Args[1]) != "llmSelector" {
			t.Fatalf("pipeline.ConAforo no recibe llmSelector como resolutor de plazas: el "+
				"aforo indexaría por una dirección distinta de la que enruta la inferencia, "+
				"o quedaría desactivado en silencio (%s)", fset.Position(opt.Pos()))
		}
		c.conAforo = true
	}
}

func (c *cableadoDelPipeline) exige(t *testing.T) {
	t.Helper()
	if !c.asignado {
		t.Error("internal/bootstrap/bootstrap.go NO cablea gw.OnEdgeReady.\n" +
			"Sin esa línea el hook queda nil, el Gateway detecta el flanco a READY y no se lo " +
			"dice a nadie, y los jobs de un Edge que acaba de poder servir inferencia esperan a " +
			"que venza su backoff —hasta 5 minutos— en vez de reanudarse en el acto. Todos los " +
			"demás tests siguen verdes, por eso existe este.")
	}
	if !c.conAforo {
		t.Error("pipeline.NewWorker se construye SIN pipeline.ConAforo.\n" +
			"Sin el aforo no existe el K por Edge: dos cadenas de lote del mismo Edge pueden " +
			"solaparse y la espera de un turno interactivo deja de estar acotada a UNA llamada " +
			"de lote (ADR-0046 · Mecanismo 1). El worker lo dice en un Warn del arranque y en " +
			"ningún otro sitio: no falla, sirve peor.")
	}
	// 🔴 EXACTAMENTE UNO, y el cero y el dos fallan por motivos OPUESTOS. Cero: nadie
	// reclama los jobs y no se genera un solo presupuesto. Dos o más: los workers extra
	// se bloquean en la única plaza del Edge CON UN JOB YA RECLAMADO en la mano —bloqueo
	// en cabeza— sin procesar nada más rápido. W = 1 es una decisión, y esta cuenta es
	// dónde está escrita de forma ejecutable.
	if c.arranques != 1 {
		t.Errorf("bootstrap.go arranca el worker del pipeline %d veces y tiene que arrancarlo "+
			"EXACTAMENTE 1 (W = 1).\n"+
			"Con 0 los jobs se quedan `pending` para siempre sin un solo error en el log. Con "+
			"2 o más, los workers de sobra se encolan en el mismo asiento del aforo (K = 1 por "+
			"Edge) reteniendo cada uno un job reclamado, y aparece bloqueo en cabeza sin ganar "+
			"throughput. La palanca para subir W es el número de Edges activos.", c.arranques)
	}
}
