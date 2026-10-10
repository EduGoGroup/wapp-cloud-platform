// Copia de internal/bootstrap/arranque/turno_acotado_cableado_test.go @ 80807ba (F0 · 05 §6). Desde F4
// (T4.27, conmutar(inferencia)) el selector de vía es el de internal/modulos/inferencia, y desde F8 (T8.32,
// conmutar(conversacion)) el resolutor y el engine son los de internal/modulos/conversacion: turnoacotado.New
// recibe el selector del contenedor SIN adaptador (murió turneroBridge) y las dos opciones del engine cambiaron
// de nombre con E-11 (WithConsultaResolver → WithQueryResolver, WithConsultaObserver → WithQueryObserver; T-6).
package arranque

import (
	"go/ast"
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
	_, ficheros := astDelArranque(t)

	llamadas := packageCallsOf(ficheros)

	// (a) El RESOLUTOR se construye. Sin esta línea no hay a quién preguntar.
	if args, ok := llamadas["turnoacotado"]["New"]; !ok {
		t.Error("turnoacotado.New NO se llama en internal/arranque: el turno acotado del Nivel B " +
			"está construido y NO LO EJECUTA NADIE — el engine devolvería «sin_resolutor» " +
			"en todas las consultas, sin un solo error")
	} else if len(args) != 1 || args[0] != "llmSelector" {
		// Desde F8 el resolutor es el nuevo y recibe el selector a pelo (hasta entonces, detrás
		// de turneroBridge). Tiene que ser EL selector del contenedor, el mismo de las etapas, el
		// aforo, quotetext e intakeahead: otro selector, o un adaptador, serían dos verdades sobre
		// la vía de un tenant (R4.7.b).
		t.Errorf("turnoacotado.New recibe %v, quiero c.llmSelector a secas: el turno acotado tiene que "+
			"preguntar al MISMO selector de vía que el resto del arranque, sin adaptador", args)
	}

	// (b) Y se ENCHUFA al engine. Es la línea que separa una ola cerrada de una ola
	// encendida, y el argumento importa tanto como la llamada: cablear un nil deja el
	// mecanismo apagado exactamente igual (WithQueryResolver ignora los nil a
	// propósito, para que un cableado a medias no deje el engine peor que sin cablear).
	//
	// 🔴 T-6: en el engine nuevo la opción se llama WithQueryResolver. Buscar el nombre viejo
	// (WithConsultaResolver) daría un rojo falso; no buscar ninguno, un verde mudo.
	args, ok := llamadas["engine"]["WithQueryResolver"]
	if !ok {
		t.Error("engine.WithQueryResolver NO está cableada en internal/arranque — sin ella el " +
			"carrito pide ayuda que nadie le da y repromptea como antes de esta ola, en silencio")
	} else if len(args) != 1 || args[0] != "consultaResolver" {
		t.Errorf("engine.WithQueryResolver recibe %v, quiero el resolutor construido con "+
			"turnoacotado.New: un nil aquí apaga el escalón sin que nada lo diga", args)
	}

	// (c) El OBSERVADOR de desenlaces. Sin él una degradación es indistinguible de un
	// turno normal, que es el modo de fallo del best-effort mudo del content.
	if _, ok := llamadas["engine"]["WithQueryObserver"]; !ok {
		t.Error("engine.WithQueryObserver NO está cableada: los desenlaces de las consultas " +
			"(resuelto, fallo, no_concluyente, bucle) no saldrían por ninguna parte")
	}
	for _, old := range []string{"WithConsultaResolver", "WithConsultaObserver"} {
		if _, ok := llamadas["engine"][old]; ok {
			t.Errorf("engine.%s sigue cableada: es el nombre del engine VIEJO (E-11, T-6)", old)
		}
	}

	// (d) El CONTADOR de caídas a Nivel A. Es el dato de campo que desbloquea el
	// desalojo del Mecanismo 1 (D-044.41), y el aforo del pipeline de captación dice
	// por escrito que sin él eso no se construye. Sin este cable la serie no existe y
	// la decisión se queda esperando otro mes.
	if _, ok := llamadas["llmvia"]["WithDegradacionObservada"]; !ok {
		t.Error("llmvia.WithDegradacionObservada NO está cableada: wapp_llm_degradacion_total " +
			"no se publicaría y D-044.41 seguiría sin poder decidirse")
	}
}

// packageCallsOf recorre la producción del arranque y devuelve llamadas[paquete][función] = los
// argumentos, en texto (textoDe), de la última llamada `paquete.Función(…)` vista.
func packageCallsOf(ficheros []*ast.File) map[string]map[string][]string {
	llamadas := map[string]map[string][]string{}
	inspecciona(ficheros, func(n ast.Node) bool {
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
	return llamadas
}

// textoDe rinde un argumento como el texto que se lee en el fuente. Cubre lo que hace
// falta aquí —un identificador o un selector como `mtx.LLMDegradacion`— y devuelve ""
// para todo lo demás, que es suficiente para que la comparación falle en vez de
// dar un falso verde.
// El receptor del contenedor del arranque se pela: `c.consultaResolver` rinde
// `consultaResolver`, que es el cable que este test exige desde antes de que el
// arranque se partiera en fases. Ver sinContenedor en astpaquete_test.go.
func textoDe(e ast.Expr) string {
	return sinContenedor(textoCompletoDe(e))
}

func textoCompletoDe(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return textoCompletoDe(v.X) + "." + v.Sel.Name
	default:
		return ""
	}
}
