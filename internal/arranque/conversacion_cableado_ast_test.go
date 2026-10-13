package arranque

// conversacion_cableado_ast_test.go — la mitad por AST del CANDADO DE CABLEADO de
// conmutar(conversacion) (F8 · T8.33, T8.34; ver conversacion_cableado_test.go). Aquí va lo que
// la reflexión sobre el arranque real no puede ver: a QUÉ se enchufa un hook (un valor de método
// no se compara por receptor), qué recibe un handler que es una clausura, el `go …Run(ctx)` de la
// fase 9 (que la huella no ejecuta) y las rutas de import del paquete entero.
//
// Son candados de cableado, como los vecinos (calentamiento_cableado_test.go): leen el árbol de
// sintaxis de la producción de internal/arranque, sin BD ni red, y no se saltan nunca.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestCableado_TheGatewayHooksAreWired (T-5, T-7): cada hook del gateway se asigna UNA vez y con
// quien debe: OnIncoming con el OnIncoming de c.flowRuntime (EL runtime, T-1), OnWarmup con el
// Warm del pool de adelanto, OnEdgeReady con el Wake del worker del pipeline y OnHeartbeat con
// una función. Y la fuente del gauge de rachas es el MaxAutoreplyStreak del mismo runtime,
// llamada una sola vez. Olvidar cualquiera compila y deja el resto de tests en verde.
func TestCableado_TheGatewayHooksAreWired(t *testing.T) {
	fset, files := astDelArranque(t)
	// "" = cualquier literal de función.
	hooks := map[string]string{
		"gw.OnIncoming":  "flowRuntime.OnIncoming",
		"gw.OnWarmup":    "intakeAhead.Warm",
		"gw.OnEdgeReady": "intakePipeline.Wake",
		"gw.OnHeartbeat": "",
	}
	assigned := make(map[string]int, len(hooks))
	gaugeSources := 0
	inspecciona(files, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			if len(x.Lhs) != 1 || len(x.Rhs) != 1 {
				return true
			}
			hook := textoDe(x.Lhs[0])
			want, ok := hooks[hook]
			if !ok {
				return true
			}
			assigned[hook]++
			if want == "" {
				if _, isFunc := x.Rhs[0].(*ast.FuncLit); !isFunc {
					t.Errorf("%s: %s no se asigna con un literal de función", fset.Position(x.Pos()), hook)
				}
				return true
			}
			if got := textoDe(x.Rhs[0]); got != want {
				t.Errorf("%s: %s se asigna con %q; se espera %s", fset.Position(x.Pos()), hook, got, want)
			}
		case *ast.CallExpr:
			if textoDe(x.Fun) != "mtx.SetFlowAutoreplyStreakMaxSource" {
				return true
			}
			gaugeSources++
			if len(x.Args) != 1 || textoDe(x.Args[0]) != "flowRuntime.MaxAutoreplyStreak" {
				t.Errorf("%s: SetFlowAutoreplyStreakMaxSource no recibe c.flowRuntime.MaxAutoreplyStreak: el gauge "+
					"leería (y barrería) las rachas de otro runtime, o de ninguno", fset.Position(x.Pos()))
			}
		}
		return true
	})
	for hook := range hooks {
		if assigned[hook] != 1 {
			t.Errorf("c.%s se asigna %d veces en la producción de internal/arranque; se espera 1 "+
				"(sin OnIncoming el proceso arranca entero y ninguna conversación avanza, T-5)", hook, assigned[hook])
		}
	}
	if gaugeSources != 1 {
		t.Errorf("mtx.SetFlowAutoreplyStreakMaxSource se llama %d veces; se espera 1: es el único sitio del que "+
			"cuelga MaxAutoreplyStreak, que barre las rachas vencidas en cada scrape (T-7)", gaugeSources)
	}
}

// TestCableado_AdminStartRunsOnTheOneRuntime (T-1, R8.5.b): J19 (`/admin/flows/start`, :8100)
// arranca flujos sobre c.flowRuntime: el campo flowsStart de las rutas admin es
// flowadmin.StartHandler(c.flowRuntime), el de conversacion/admin, y ese constructor se llama una
// sola vez. El handler es una clausura: su Starter no se puede leer por reflexión.
func TestCableado_AdminStartRunsOnTheOneRuntime(t *testing.T) {
	fset, files := astDelArranque(t)
	wired := 0
	for _, f := range files {
		local, ok := importsOf(t, f)[conversationAdminPath]
		ast.Inspect(f, func(n ast.Node) bool {
			kv, isKV := n.(*ast.KeyValueExpr)
			if !isKV || textoDe(kv.Key) != "flowsStart" {
				return true
			}
			wired++
			call, isCall := kv.Value.(*ast.CallExpr)
			if !ok || !isCall || !esLlamada(call, local, "StartHandler") {
				t.Errorf("%s: flowsStart no es el StartHandler de internal/modulos/conversacion/admin", fset.Position(kv.Pos()))
				return true
			}
			if len(call.Args) != 1 || textoDe(call.Args[0]) != "flowRuntime" {
				t.Errorf("%s: el StartHandler de J19 no recibe c.flowRuntime: /admin/flows/start arrancaría flujos "+
					"en otro runtime que el del gateway (T-1)", fset.Position(call.Pos()))
			}
			return true
		})
	}
	if wired != 1 {
		t.Errorf("flowsStart se cablea %d veces en la producción de internal/arranque; se espera 1", wired)
	}
	if n := callsTo(t, conversationAdminPath, "StartHandler"); n != 1 {
		t.Errorf("flowadmin.StartHandler aparece %d veces en la producción de internal/arranque; se espera 1 (J19)", n)
	}
}

// TestCableado_TheNewFaceReceivesTheConversationDeps: lo que TestIdentidad_… afirma sobre
// conversationDepsOfTheNewFace(c) vale para la cara REAL solo si es eso lo que la fase 8 le pasa:
// una única llamada, como valor del campo conversation de newFaceDeps.
func TestCableado_TheNewFaceReceivesTheConversationDeps(t *testing.T) {
	_, files := astDelArranque(t)
	calls, asField := 0, 0
	inspecciona(files, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			if textoDe(x.Fun) == "conversationDepsOfTheNewFace" {
				calls++
			}
		case *ast.KeyValueExpr:
			call, ok := x.Value.(*ast.CallExpr)
			if ok && textoDe(x.Key) == "conversation" && textoDe(call.Fun) == "conversationDepsOfTheNewFace" &&
				len(call.Args) == 1 && textoDe(call.Args[0]) == "c" {
				asField++
			}
		}
		return true
	})
	if calls != 1 || asField != 1 {
		t.Errorf("conversationDepsOfTheNewFace se llama %d veces y %d como `conversation: conversationDepsOfTheNewFace(c)`; "+
			"se espera 1 y 1: es lo que monta I1–I19 en la cara nueva", calls, asField)
	}
}

// TestCableado_SinksAndResumePolicyGetRealArguments (D-F8-16): por AST, el runtime recibe DOS
// WithEventSink y UN WithResumePolicy, ninguno con un nil literal (que el runtime nuevo ignora en
// silencio): los sinks nacen de NewPersistSink y de NewWebhookSink, y la política es la del nodo
// del carrito, de cart.NewResumePolicy. Lo construido de verdad lo afirman
// TestCableado_TheRuntimeHasItsRealSinks y TestCableado_TheRuntimeHasTheCartResumePolicy.
func TestCableado_SinksAndResumePolicyGetRealArguments(t *testing.T) {
	fset, files := astDelArranque(t)
	sinkBuilders := map[string]int{}
	sinks, policies := 0, 0
	for _, f := range files {
		local, ok := importsOf(t, f)[conversationRuntimePath]
		if !ok {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, isCall := n.(*ast.CallExpr)
			if !isCall {
				return true
			}
			switch {
			case esLlamada(call, local, "WithEventSink"):
				sinks++
				if len(call.Args) != 1 {
					t.Errorf("%s: WithEventSink con %d argumentos", fset.Position(call.Pos()), len(call.Args))
					return true
				}
				builder := conversationRootConstructor(call.Args[0])
				if builder == "" {
					t.Errorf("%s: WithEventSink no recibe un sink recién construido (un nil se ignora en silencio "+
						"y el runtime se queda con el LogSink de relleno)", fset.Position(call.Pos()))
				}
				sinkBuilders[builder]++
			case esLlamada(call, local, "WithResumePolicy"):
				policies++
				if len(call.Args) != 2 || textoDe(call.Args[0]) != "cart.NodeTypeCart" ||
					conversationRootConstructor(call.Args[1]) != "cart.NewResumePolicy" {
					t.Errorf("%s: WithResumePolicy no recibe (cart.NodeTypeCart, cart.NewResumePolicy(…)): un nil "+
						"se ignora en silencio", fset.Position(call.Pos()))
				}
			}
			return true
		})
	}
	if sinks != 2 || sinkBuilders["flowruntime.NewPersistSink"] != 1 || sinkBuilders["flowruntime.NewWebhookSink"] != 1 {
		t.Errorf("el arranque pasa %d WithEventSink, construidos con %v; se esperan 2: uno de "+
			"flowruntime.NewPersistSink y uno de flowruntime.NewWebhookSink", sinks, sinkBuilders)
	}
	if policies != 1 {
		t.Errorf("el arranque pasa %d WithResumePolicy; se espera 1, la del carrito", policies)
	}
}

// conversationRootConstructor devuelve el «paquete.Función» de la llamada de la que nace e,
// bajando por los métodos encadenados: de `flowruntime.NewPersistSink(…).WithDecisionThread(x)`
// rinde `flowruntime.NewPersistSink`. Si e no nace de una llamada paquete.Función, "".
func conversationRootConstructor(e ast.Expr) string {
	for {
		call, ok := e.(*ast.CallExpr)
		if !ok {
			return ""
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return ""
		}
		if id, ok := sel.X.(*ast.Ident); ok {
			return id.Name + "." + sel.Sel.Name
		}
		e = sel.X
	}
}

// TestCableado_TheAggregatorRunsInTheBackgroundPhase (T-4): fase9_fondo.go lanza
// `go c.intakeAggregator.Run(ctx)` exactamente una vez, y nadie más lo lanza. Sin el Run las
// ventanas se abren y no se cierran nunca, en silencio; con dos, dos barridos sobre la misma
// tabla. La huella del arranque real no ejecuta la fase 9.
func TestCableado_TheAggregatorRunsInTheBackgroundPhase(t *testing.T) {
	fset, files := astDelArranque(t)
	runs := 0
	inspecciona(files, func(n ast.Node) bool {
		g, ok := n.(*ast.GoStmt)
		if !ok || textoDe(g.Call.Fun) != "intakeAggregator.Run" {
			return true
		}
		runs++
		if file := filepath.Base(fset.Position(g.Pos()).Filename); file != "fase9_fondo.go" {
			t.Errorf("%s lanza el Run del agregador; las goroutines de fondo se lanzan en fase9_fondo.go", file)
		}
		if len(g.Call.Args) != 1 || textoDe(g.Call.Args[0]) != "ctx" {
			t.Errorf("%s: el Run del agregador no recibe el ctx de la fase de fondo: no pararía con el proceso",
				fset.Position(g.Pos()))
		}
		return true
	})
	if runs != 1 {
		t.Errorf("`go c.intakeAggregator.Run(ctx)` aparece %d veces en la producción de internal/arranque; se espera 1 "+
			"(sin él las ventanas se abren y nunca se cierran, T-4)", runs)
	}
}

// conversationOldPackage casa la ruta de import de un paquete VIEJO de la plataforma: los de la
// definición de hecho 5 de F8 (reglas.md §4.5), DIRECTAMENTE bajo internal/. El ancla importa:
// internal/modulos/captacion/intake o internal/modulos/acceso/iam son los nuevos y no casan.
var conversationOldPackage = regexp.MustCompile(`^github\.com/EduGoGroup/wapp-cloud-platform/internal/(` +
	`flujos|turnoacotado|publicapi|intake|intakes|intakeahead|reanalisis|catalogimport|gateway|iam|` +
	`platformadmin|entitlements|llmvia|prompts|tenantllm|degradation|integrations|tenantvars|diagnostics|` +
	`inferstats|receipts|ingest|filtercfg|evidence|casebank|intentcfg|bootstrap)(/|$)`)

// TestCableado_NoBootFileImportsAnOldPackage (definición de hecho 5 de F8; hallazgo 39: por ruta
// de import, no por campo del contenedor): ningún .go de internal/arranque, producción Y tests,
// importa un paquete viejo. Sustituye a los tres «solo el adaptador importa lo viejo» de F4, F6 y
// F7: con conmutar(conversacion) murió el último adaptador y ya no hay excepción. Un test que
// importara lo viejo para comparar salidas sería un puente a escondidas.
func TestCableado_NoBootFileImportsAnOldPackage(t *testing.T) {
	// El ancla, comprobada: lo viejo casa y su homónimo nuevo no.
	const root = "github.com/EduGoGroup/wapp-cloud-platform/internal/"
	for path, old := range map[string]bool{
		root + "flujos/runtime":                    true,
		root + "intake":                            true,
		root + "bootstrap":                         true,
		root + "modulos/captacion/intake":          false,
		root + "modulos/acceso/iam/usecase":        false,
		root + "modulos/conversacion/turnoacotado": false,
		root + "intakeshelper":                     false,
		root + "apipublica":                        false,
	} {
		if conversationOldPackage.MatchString(path) != old {
			t.Fatalf("la regla de paquete viejo dice %t para %s; se espera %t", !old, path, old)
		}
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("leyendo el directorio del arranque: %v", err)
	}
	fset := token.NewFileSet()
	scanned := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parseando %s: %v", name, err)
		}
		scanned++
		for path := range importsOf(t, f) {
			if conversationOldPackage.MatchString(path) {
				t.Errorf("%s importa %s: desde conmutar(conversacion) ningún fichero de internal/arranque, ni de "+
					"producción ni de test, puede importar un paquete viejo", name, path)
			}
		}
	}
	if scanned == 0 {
		t.Fatal("recorridos = 0: el barrido no miró ningún fichero")
	}
}

// TestCableado_NoBridgeFileIsLeft (definición de hecho 6 de F8): en internal/arranque no queda
// ningún bridge_*.go, ni de producción ni de test. F8 es la última fase de módulo: mueren todos
// los adaptadores y no nace ninguno.
func TestCableado_NoBridgeFileIsLeft(t *testing.T) {
	bridges, err := filepath.Glob("bridge_*.go")
	if err != nil {
		t.Fatalf("buscando bridge_*.go: %v", err)
	}
	if len(bridges) > 0 {
		t.Errorf("quedan adaptadores en internal/arranque: %v; conmutar(conversacion) los mata todos", bridges)
	}
}
