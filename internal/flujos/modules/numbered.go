package modules

import (
	"strings"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
)

// CloneVars copia el mapa de variables para mantener la PUREZA de los módulos (no
// mutar el Vars de entrada). nil → mapa nuevo. Extraído de menú/encuesta, que lo
// duplicaban byte-a-byte (Plan 027 · Ola 2 · T9, cierra H12).
func CloneVars(in map[string]any) map[string]any {
	out := make(map[string]any, len(in)+1)
	for k, v := range in {
		out[k] = v
	}
	return out
}

// GetInt lee un entero de Vars tolerando el tipo que deja un round-trip por JSON
// (float64) además de int/int64. Valor ausente/de otro tipo → 0. Extraído de
// menú/encuesta (Plan 027 · Ola 2 · T9, cierra H12).
func GetInt(vars map[string]any, key string) int {
	switch v := vars[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}

// NumberedStep resuelve el patrón COMÚN de los nodos de opción numerada
// (menú/encuesta, design.md §3/§10.E), extraído para eliminar la duplicación
// byte-a-byte SIN cambiar la conducta observable (Plan 027 · Ola 2 · T9, cierra
// H12). Clona Vars (pureza), recorta la entrada, la casa contra node.Options y
// aplica la escalera de reprompt acotada:
//
//   - Opción VÁLIDA: borra el contador de reprompt y delega en onValid. El módulo
//     usa onValid para registrar sus efectos PROPIOS (la encuesta anota la respuesta
//     y declara el efecto survey_answer; el menú no hace nada extra) y devolver el
//     Result de transición al target. onValid recibe los Vars ya clonados y sin el
//     contador, la opción elegida (choice = la clave de Options) y el nodo destino.
//   - Opción INVÁLIDA con < maxReprompts intentos: incrementa el contador y re-emite
//     el prompt precedido del aviso (permanece en el nodo).
//   - Al alcanzar maxReprompts: emite el mensaje de ayuda, reinicia el contador y
//     permanece.
//
// repromptKey es la clave del contador (propia de cada módulo; no colisionan).
func NumberedStep(
	node model.Node,
	conv model.Conversation,
	input string,
	repromptKey string,
	maxReprompts int,
	onValid func(vars map[string]any, choice, target string) Result,
) Result {
	vars := CloneVars(conv.Vars)
	trimmed := strings.TrimSpace(input)

	if target, ok := node.Options[trimmed]; ok {
		// Opción válida → el contador se reinicia al transicionar; onValid arma el Result.
		delete(vars, repromptKey)
		return onValid(vars, trimmed, target)
	}

	// Opción inválida.
	attempts := GetInt(vars, repromptKey) + 1
	if attempts >= maxReprompts {
		// Último intento inválido: mensaje de ayuda + permanecer + reinicio.
		delete(vars, repromptKey)
		return Result{Vars: vars, Outputs: []string{numberedHelpText(node.Prompt)}}
	}
	vars[repromptKey] = attempts
	return Result{Vars: vars, Outputs: []string{numberedInvalidText(node.Prompt)}}
}

// numberedInvalidText y numberedHelpText son los avisos del reprompt acotado,
// idénticos byte-a-byte a los que menú/encuesta emitían por separado.
func numberedInvalidText(prompt string) string {
	return "Opción no válida. Responde con el número de una de las opciones.\n\n" + prompt
}

func numberedHelpText(prompt string) string {
	return "No logré entender tu respuesta. Por favor elige una de las opciones escribiendo solo su número.\n\n" + prompt
}
