package bootstrap

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// turno_acotado_cableado_test.go — QUE LA OLA ESTÉ ENCENDIDA, NO SOLO CERRADA
// (Plan 044 · Ola 3.5 · T3.5-2).
//
// 🔴 POR QUÉ ESTE TEST EXISTE, Y NO ES CELO: EN ESTE PLAN YA HA PASADO DOS VECES.
// Un mecanismo completo, con sus tests verdes y su plan al 100 %, que en producción
// NO LO EJECUTA NADIE porque faltaba la línea que lo enchufa. El re-entry de
// consultas es el candidato perfecto a repetirlo: sin resolutor cableado el engine
// devuelve «sin_resolutor», el carrito repromptea como el día antes, no hay error,
// no hay test rojo y la única señal es una línea de log que nadie mira.
//
// Y las Options son VARIÁDICAS, que es lo que remata el modo de fallo: omitir una
// compila, pasa el vet, pasa el lint y deja el paquete entero en verde. Es
// literalmente cómo falló WithOpeningBuilder (ver flow_options_cableadas_test.go).
//
// Este test es la señal. Si mañana alguien reordena el arranque y una de estas
// líneas se cae, el rojo sale aquí y no en la conversación de una clienta a la que
// el carrito dejó de entender.
func TestTurnoAcotadoCableado(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "bootstrap.go", nil, 0)
	if err != nil {
		t.Fatalf("parsear bootstrap.go: %v", err)
	}

	// llamadas[paquete][función] = los argumentos, en texto.
	llamadas := map[string]map[string][]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if llamadas[pkg.Name] == nil {
			llamadas[pkg.Name] = map[string][]string{}
		}
		args := make([]string, 0, len(call.Args))
		for _, a := range call.Args {
			args = append(args, textoDe(a))
		}
		llamadas[pkg.Name][sel.Sel.Name] = args
		return true
	})

	// (a) El RESOLUTOR se construye. Sin esta línea no hay a quién preguntar.
	if _, ok := llamadas["turnoacotado"]["New"]; !ok {
		t.Error("turnoacotado.New NO se llama en bootstrap.go: el turno acotado del Nivel B " +
			"está construido y NO LO EJECUTA NADIE — el engine devolvería «sin_resolutor» " +
			"en todas las consultas, sin un solo error")
	}

	// (b) Y se ENCHUFA al engine. Es la línea que separa una ola cerrada de una ola
	// encendida, y el argumento importa tanto como la llamada: cablear un nil deja el
	// mecanismo apagado exactamente igual (WithConsultaResolver ignora los nil a
	// propósito, para que un cableado a medias no deje el engine peor que sin cablear).
	args, ok := llamadas["engine"]["WithConsultaResolver"]
	if !ok {
		t.Error("engine.WithConsultaResolver NO está cableada en bootstrap.go — sin ella el " +
			"carrito pide ayuda que nadie le da y repromptea como antes de esta ola, en silencio")
	} else if len(args) != 1 || args[0] != "consultaResolver" {
		t.Errorf("engine.WithConsultaResolver recibe %v, quiero el resolutor construido con "+
			"turnoacotado.New: un nil aquí apaga el escalón sin que nada lo diga", args)
	}

	// (c) El OBSERVADOR de desenlaces. Sin él una degradación es indistinguible de un
	// turno normal, que es el modo de fallo del best-effort mudo del content.
	if _, ok := llamadas["engine"]["WithConsultaObserver"]; !ok {
		t.Error("engine.WithConsultaObserver NO está cableada: los desenlaces de las consultas " +
			"(resuelto, fallo, no_concluyente, bucle) no saldrían por ninguna parte")
	}

	// (d) El CONTADOR de caídas a Nivel A. Es el dato de campo que desbloquea el
	// desalojo del Mecanismo 1 (D-044.41), y internal/intake/pipeline/plaza.go dice
	// por escrito que sin él eso no se construye. Sin este cable la serie no existe y
	// la decisión se queda esperando otro mes.
	if _, ok := llamadas["llmvia"]["WithDegradacionObservada"]; !ok {
		t.Error("llmvia.WithDegradacionObservada NO está cableada: wapp_llm_degradacion_total " +
			"no se publicaría y D-044.41 seguiría sin poder decidirse")
	}
}

// textoDe rinde un argumento como el texto que se lee en el fuente. Cubre lo que hace
// falta aquí —un identificador o un selector como `mtx.LLMDegradacion`— y devuelve ""
// para todo lo demás, que es suficiente para que la comparación falle en vez de
// dar un falso verde.
func textoDe(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return textoDe(v.X) + "." + v.Sel.Name
	default:
		return ""
	}
}
