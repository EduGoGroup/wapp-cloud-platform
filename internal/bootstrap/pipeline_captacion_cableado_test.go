package bootstrap

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestPipelineCaptacionCableado fija, contra el ÁRBOL DE SINTAXIS de bootstrap.go, que
// el worker del pipeline de captación (Plan 044) está de verdad ENCENDIDO: enchufado al
// flanco a READY, arrancado UNA sola vez, construido CON su aforo, CON las CINCO etapas
// y su caché de catálogo, y CON el lector de zonas de envío.
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
// 🔄 AMPLIADO POR T3.8 CON DOS CRITERIOS MÁS, Y NO POR SIMETRÍA: la Ola 3 REPITIÓ el
// defecto que este test existía para no repetir. `stages.NewMatch` y `stages.NewDraft`
// se escribieron con sus tests y se quedaron SIN UN SOLO CALL-SITE DE PRODUCCIÓN —
// `pipeline.go` no nombraba `StageMatch` ni `StageDraft`, y cada job terminaba en
// `done` sin `intake_id`—, exactamente como la Ola 2 antes de T2.9. Este test contaba
// tres cosas y ninguna de las tres podía verlo, porque las tres miraban al worker y no
// a lo que el worker lleva dentro. Ahora cuenta:
//
//   - (d) los CINCO constructores de etapa MÁS la caché del catálogo. Sin `NewMatch` o
//     `NewDraft` el worker ya no compila (T3.8 los hizo obligatorios), pero eso solo
//     protege de omitirlos: NO protege de que alguien construya la etapa y no la pase,
//     ni de que la caché del catálogo se sustituya por otra cosa;
//   - (e) `pipeline.ConZonasDeEnvio`, que sí es omisible sin error: sin ella TODO
//     borrador sale con la línea de envío sin precio, también el del tenant que tiene
//     su tarifa plana configurada, y el único síntoma es un Warn del arranque.
//
// Es exactamente el modo en que falló WithOpeningBuilder: construido, probado y sin
// enchufar durante meses. Este pipeline estuvo ocho tareas completo y apagado, y sus
// dos últimas etapas otra ola entera.
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

// cableadoDelPipeline lleva la cuenta de las cosas que hay que ver en bootstrap.go.
type cableadoDelPipeline struct {
	asignado  bool // (a) gw.OnEdgeReady = intakePipeline.Despertar
	arranques int  // (b) cuántas veces se hace `go intakePipeline.Run(ctx)`
	conAforo  bool // (c) pipeline.NewWorker(..., pipeline.ConAforo(...))
	// piezas son los constructores del criterio (d) vistos en el fichero. Se cuenta el
	// CONJUNTO y no una lista ordenada: lo que importa es que no falte ninguno, no en
	// qué orden se escribieron.
	piezas   map[string]bool
	conZonas bool // (e) pipeline.NewWorker(..., pipeline.ConZonasDeEnvio(...))
}

// piezasDelPipeline son los constructores que bootstrap.go TIENE que llamar para que un
// job llegue de la ventana al borrador. Cada uno con el síntoma de su ausencia, porque
// un test que solo dijera «falta stages.NewMatch» obligaría a abrir el plan.
var piezasDelPipeline = map[string]string{
	"stages.NewP2": "sin P2 no se extrae ni una idea del mensaje del cliente: el pipeline no tiene por dónde empezar",
	"stages.NewP3": "sin P3 ningún ítem queda especificado y el borrador saldría sin líneas",
	"stages.NewP4": "sin P4 no hay cantidades ni fecha de entrega normalizadas",
	"stages.NewMatch": "sin el match no se cruza el catálogo: no habría líneas, ni precios, ni presupuesto " +
		"— y ésta es EXACTAMENTE la que la Ola 3 dejó escrita y sin llamante",
	"stages.NewDraft": "sin el draft no nace la solicitud ni su revisión: el job termina en `done` sin `intake_id` " +
		"y el dueño no ve NADA en la bandeja, sin un solo error en el log",
	"catalogo.NewCache": "sin la caché del catálogo el match no tiene índice que consultar y devuelve ErrSinCatalogo " +
		"en cada job: la cadena entera muere por infraestructura",
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
		c.anotaPieza(v)
	}
}

// anotaPieza cubre el criterio (d): las cinco etapas y la caché se CONSTRUYEN aquí.
func (c *cableadoDelPipeline) anotaPieza(llamada *ast.CallExpr) {
	nombre := campo(llamada.Fun)
	if _, esPieza := piezasDelPipeline[nombre]; !esPieza {
		return
	}
	if c.piezas == nil {
		c.piezas = map[string]bool{}
	}
	c.piezas[nombre] = true
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
	for _, arg := range llamada.Args {
		opt, ok := arg.(*ast.CallExpr)
		if !ok || campo(opt.Fun) != "pipeline.ConZonasDeEnvio" {
			continue
		}
		// Un `nil` aquí tampoco da error: ConZonasDeEnvio lo trata como «sin lector» y
		// vuelve sin hacer nada, igual que ConAforo. Por eso no basta con verla escrita.
		if len(opt.Args) != 1 || campo(opt.Args[0]) == "nil" {
			t.Fatalf("pipeline.ConZonasDeEnvio no recibe un lector utilizable: la opción se traga el nil "+
				"y el worker se queda sin zonas en silencio (%s)", fset.Position(opt.Pos()))
		}
		c.conZonas = true
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
	for pieza, sintoma := range piezasDelPipeline {
		if c.piezas[pieza] {
			continue
		}
		t.Errorf("internal/bootstrap/bootstrap.go NO llama a %s.\n%s.\n"+
			"Que el constructor del worker exija la pieza protege de OMITIRLA, no de "+
			"construir otra cosa en su lugar ni de dejar de construirla aquí; este criterio sí.",
			pieza, sintoma)
	}
	if !c.conZonas {
		t.Error("pipeline.NewWorker se construye SIN pipeline.ConZonasDeEnvio.\n" +
			"Sin ese lector la etapa `match` recibe CERO zonas y todo borrador sale con la línea " +
			"de envío «Envío por confirmar» a precio vacío — también el del tenant que tiene UNA " +
			"zona configurada con su tarifa plana, que la perdería sin enterarse. No falla: el dueño " +
			"precifica a mano un envío que ya estaba precificado. El único otro síntoma es un Warn " +
			"del arranque.")
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
