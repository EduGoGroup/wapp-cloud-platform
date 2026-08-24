package bootstrap

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestCalentamientoCableado fija, contra el ÁRBOL DE SINTAXIS de bootstrap.go, que el
// hook de precalentado del Gateway se enchufa de verdad al pool que sabe emitirlo
// (Plan 044 · Ola 1.7 · T1.7-4).
//
// 🔴 POR QUÉ UN TEST DE AST, mismo molde y mismo motivo que TestSendBudgetCableado:
// `Server.OnWarmup` es un hook OPCIONAL con cero-valor útil —nil significa «no se
// precalienta», que es el comportamiento anterior a esta ola—, así que **olvidar la
// asignación compila, pasa el vet, pasa el lint y deja TODOS los demás tests en
// verde**. Los tests del gateway se ponen ellos mismos un OnWarmup para poder medirlo,
// y los del pool llaman a Warm directamente, de modo que ninguno de los dos puede
// notar que producción no los une.
//
// Y el síntoma en campo sería el más caro de todos: la prueba A/B de T1.7-4 diría «no
// hay diferencia entre precalentar y no precalentar», y la conclusión natural —falsa—
// sería que el mecanismo no sirve, cuando lo que faltaría es el cable.
//
// Es exactamente el modo en que falló WithOpeningBuilder: construido, probado y sin
// enchufar durante meses.
func TestCalentamientoCableado(t *testing.T) {
	fset := token.NewFileSet()
	archivo, err := parser.ParseFile(fset, "bootstrap.go", nil, 0)
	if err != nil {
		t.Fatalf("parseando bootstrap.go: %v", err)
	}

	var asignado bool
	ast.Inspect(archivo, func(n ast.Node) bool {
		asig, ok := n.(*ast.AssignStmt)
		if !ok || len(asig.Lhs) != 1 || len(asig.Rhs) != 1 {
			return true
		}
		if campo(asig.Lhs[0]) != "gw.OnWarmup" {
			return true
		}
		// No basta con que se asigne ALGO. Tiene que ser el Warm del pool de adelanto:
		// es el único que arma la ClassifyRequestInput con la MISMA `entrada` que la P1
		// real, y por tanto el único que deja cacheado el prefijo que el primer mensaje
		// del cliente va a pedir. Cualquier otra función aquí calentaría otro prompt y
		// el fallo sería mudo — ni error, ni log, ni mejora de latencia.
		if quien := campo(asig.Rhs[0]); quien != "intakeAhead.Warm" {
			t.Fatalf("gw.OnWarmup se asigna con %q y no con intakeAhead.Warm: solo ese arma el "+
				"prompt con la misma `entrada` que la clasificación real, que es lo único que "+
				"hace que el prefijo cacheado coincida (%s)", quien, fset.Position(asig.Pos()))
		}
		asignado = true
		return false
	})

	if !asignado {
		t.Fatal("internal/bootstrap/bootstrap.go NO cablea gw.OnWarmup.\n" +
			"Sin esa línea el hook queda nil, el Gateway no avisa a nadie de que la caché de " +
			"prefijo se enfrió, y NADA precalienta: cada Edge recién conectado y cada catálogo " +
			"publicado devuelven al primer mensaje real el prefill FRÍO de ~50 s. Todos los " +
			"demás tests siguen verdes, por eso existe este.")
	}
}

// TestInterruptoresLLMCableados: los dos interruptores de campo tienen que llegar a
// quien decide, o el `.env` del VPS no gobernaría nada.
//
// 🔴 ES LA MITAD QUE LA OLA PASADA SE DEJÓ. Un valor de configuración leído en
// config.go y no cableado es indistinguible —desde cualquier test y desde el log— de
// uno cableado: el default del binario sigue mandando y el operador cree que lo apagó.
// Aquí se exige que cada uno viaje a su consumidor por su nombre.
func TestInterruptoresLLMCableados(t *testing.T) {
	fset := token.NewFileSet()
	archivo, err := parser.ParseFile(fset, "bootstrap.go", nil, 0)
	if err != nil {
		t.Fatalf("parseando bootstrap.go: %v", err)
	}

	// Slice y no mapa a propósito: un map[string]string con estos literales dispara el
	// G101 de gosec («potential hardcoded credentials») por la palabra Tokens.
	cables := []struct{ opcion, valor string }{
		{"local.WithMaxOutputTokens", "cfg.LLM.MaxOutputTokensEnabled"},
		{"intakeahead.WithCalentamiento", "cfg.LLM.WarmupEnabled"},
	}
	visto := map[string]bool{}
	ast.Inspect(archivo, func(n ast.Node) bool {
		llamada, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		fn := campo(llamada.Fun)
		for _, c := range cables {
			if c.opcion != fn {
				continue
			}
			if len(llamada.Args) != 1 || ruta(llamada.Args[0]) != c.valor {
				t.Fatalf("%s no recibe %s: el interruptor se construiría con un literal y el "+
					"WAPP_LLM_* del .env dejaría de gobernarlo, sin que nada lo diga (%s)",
					fn, c.valor, fset.Position(llamada.Pos()))
			}
			visto[fn] = true
		}
		return true
	})
	for _, c := range cables {
		if !visto[c.opcion] {
			t.Errorf("bootstrap.go no llama a %s(%s): ese interruptor no llega a producción y "+
				"el lado B del control A/B de campo exigiría recompilar", c.opcion, c.valor)
		}
	}
}

// ruta rinde un selector ANIDADO (`cfg.LLM.WarmupEnabled`), que el `campo` compartido
// de send_budget_cableado_test.go no cubre: aquel se detiene en un nivel y devuelve la
// cadena vacía a partir del segundo. No se amplía `campo` porque otros tests comparan
// contra su resultado y ensancharlo cambiaría lo que ellos afirman.
func ruta(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		if base := ruta(v.X); base != "" {
			return base + "." + v.Sel.Name
		}
	}
	return ""
}

// TestInferenceStatsCableado: el almacén del parte de inferencia tiene DOS extremos y
// los dos son asignaciones sueltas —el Gateway lo escribe, /metrics lo lee—. Faltando
// cualquiera de las dos, todo compila y todos los tests siguen verdes: el colector
// publicaría series vacías (falta el escritor) o no publicaría ninguna (falta el
// lector), y en los dos casos el síntoma en campo es idéntico —un /metrics sin datos
// de inferencia— pero la causa es la contraria.
//
// Es la misma red que TestCalentamientoCableado y por el mismo motivo: aquí lo que se
// quedaría inerte es la tarea entera (T1.7-9), cuyo único criterio es que el dato se
// pueda leer sin abrir un log.
func TestInferenceStatsCableado(t *testing.T) {
	fset := token.NewFileSet()
	archivo, err := parser.ParseFile(fset, "bootstrap.go", nil, 0)
	if err != nil {
		t.Fatalf("parseando bootstrap.go: %v", err)
	}

	var lector, escritor bool
	ast.Inspect(archivo, func(n ast.Node) bool {
		llamada, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch campo(llamada.Fun) {
		case "mtx.RegisterInferenceStats":
			// La fuente tiene que ser el Agrega del almacén y no una función suelta: es
			// lo único que lee lo que el Gateway escribe.
			if len(llamada.Args) != 1 || ruta(llamada.Args[0]) != "inferStats.Agrega" {
				t.Fatalf("RegisterInferenceStats no recibe inferStats.Agrega (%s)",
					fset.Position(llamada.Pos()))
			}
			lector = true
		case "gatewaygrpc.WithInferenceStats":
			if len(llamada.Args) != 1 || ruta(llamada.Args[0]) != "inferStats" {
				t.Fatalf("WithInferenceStats no recibe el MISMO almacén que lee /metrics: dos "+
					"almacenes distintos publicarían series vacías sin ningún error (%s)",
					fset.Position(llamada.Pos()))
			}
			escritor = true
		}
		return true
	})

	if !escritor {
		t.Error("bootstrap.go no cablea gatewaygrpc.WithInferenceStats: el parte de inferencia " +
			"del Edge sigue subiendo y muriendo en la base, que es lo que T1.7-9 arregla.")
	}
	if !lector {
		t.Error("bootstrap.go no llama a mtx.RegisterInferenceStats: los números se recogerían " +
			"en memoria y no saldría ni una serie por /metrics.")
	}
}
