// Porta internal/flujos/modules/cart/consulta.go @ 9d5a4b6

// consulta.go — CUÁNDO EL CARRITO PIDE AYUDA, Y QUÉ ACEPTA DE VUELTA
// (Plan 044 · Ola 3.5 · T3.5-2).
//
// No exporta nada: lo que promete se ve por Module.Step (Result.Query) y por lo que
// el módulo hace con el modules.Verdict que el engine le siembra. El punto por el
// que Step traduce la entrada en las DOS pasadas del turno se llama
// preresolveOrQuery (`preresolveOConsulta` en el viejo, E-11), y su sitio dentro de
// Step lo vigila orden_consulta_ast_test.go.
//
// ════════════════════════════════════════════════════════════════════════════
// EL TERCER ESCALÓN
// ════════════════════════════════════════════════════════════════════════════
//
// La cascada determinista (preresolutor.go) traduce «hamburgesa» al código 1. Lo
// que no puede hacer es aritmética del lenguaje («mejor dos» → 2) ni entender un
// rótulo abreviado por donde no toca («finalizar» contra «Confirmar y finalizar»).
// Eso es una CONSULTA: el módulo la eleva y el engine la resuelve
// (modules/consulta.go, engine/consulta.go).
//
// El orden es el de siempre y no cambia: código exacto → cascada → consulta.
// Preguntar es lo ÚLTIMO, porque es lo único que cuesta tiempo de un turno de
// WhatsApp y lo único que puede equivocarse de forma interesante.
//
//	1.ª pasada (sin veredicto en Vars): corre la cascada. Si resuelve, el turno
//	   sigue normal con el código. Si no resuelve y el turno es consultable, Step
//	   devuelve SOLO la petición y su turno termina ahí.
//	2.ª pasada (con el veredicto sembrado por el engine): aplica el veredicto si es
//	   admisible y sigue. NO vuelve a correr la cascada —su telemetría cuenta UNA
//	   vez por mensaje, no dos— y sobre todo NO vuelve a pedir.
//
// ════════════════════════════════════════════════════════════════════════════
// QUÉ LLEVA LA PETICIÓN (modules.Query)
// ════════════════════════════════════════════════════════════════════════════
//
//   - Class: modules.QueryClassQuantity en el nivel de la cantidad;
//     modules.QueryClassOption en los demás.
//   - Level: el nivel de la sub-máquina (una constante Level*).
//   - Text: la entrada del cliente, recortada.
//   - Options: en una consulta de opción, las MISMAS opciones que el nivel ofrece a
//     la cascada, en su orden —código y etiqueta—; en una de cantidad, ninguna: la
//     respuesta admisible son dígitos y no hay catálogo que ofrecer.
//   - Chunks: vacío. Los trozos son del troceado (troceo.go).
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 QUÉ NIVELES PREGUNTAN, Y CUÁLES NO PUEDEN PREGUNTAR NUNCA
// ════════════════════════════════════════════════════════════════════════════
//
// Preguntan los MISMOS que admiten cascada, más quantity. Y no se pregunta por la
// entrada vacía, ni por un NÚMERO (un índice de pantalla o una cantidad: los dos
// los resuelve el camino de siempre, y preguntar sería pagar un modelo para que
// confirme lo obvio), ni por un código del nivel, ni por PROSA de más de 16 tokens
// (un turno de una persona no puede quedarse esperando a que un modelo digiera una
// parrafada).
//
//   - item_note, order_note y buyer_data SIGUEN EXCLUIDOS, y aquí el motivo pesa
//     MÁS que en la cascada: allí el texto del cliente se comparaba en memoria y no
//     salía del proceso; una consulta lo MANDA FUERA, a un modelo. Mandar el nombre,
//     el RUT o la dirección de alguien —que se escriben CIFRADOS justo para que no
//     queden en claro en ningún sitio— a un servicio de interpretación sería
//     deshacer el ADR-0017 por la puerta de atrás. La exclusión se HEREDA de la
//     lista fail-closed de la cascada en vez de repetirse aquí, donde alguien podría
//     olvidarse de mantenerla al día.
//
//   - quantity SÍ pregunta, y es el caso que justifica la tarea entera: está fuera
//     de la cascada porque la similitud ortográfica no sabe convertir «dos» en 2, y
//     eso es exactamente lo que un modelo hace bien.
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 LO QUE VUELVE SE VALIDA CONTRA LO QUE SE OFRECIÓ
// ════════════════════════════════════════════════════════════════════════════
//
// El resolutor es un modelo: puede inventarse un código, devolver la frase del
// cliente tal cual, o contestar en prosa. Por eso el módulo NO se cree lo que le
// llega: solo acepta un Code que él mismo puso en Query.Options o, para la
// cantidad, una tira de 1 a 4 dígitos ASCII (la regla de negocio —que sea >= 1— la
// sigue poniendo el nivel de la cantidad; los cuatro dígitos son una guarda contra
// un resolutor que devuelva una cifra absurda).
//
// Todo lo demás deja la entrada INTACTA hacia la sub-máquina, y eso cubre los
// cuatro casos degradados con el MISMO gesto —sin resolutor, fallo, no concluyente
// y código inadmisible—: la pantalla que sale es la que el carrito produce ante
// algo que no entiende (el reprompt de su nivel y, a los tres, el menú de salida).
// El módulo NO inventa un mensaje de «no te entendí porque el modelo no estaba»:
// eso sería contarle a la clienta una avería nuestra.
//
// Esa validación es también la última barrera de privacidad: si el veredicto solo
// puede ser un código del catálogo, no hay forma de que el texto del cliente entre
// en el estado del carrito por esta puerta, ni siquiera si el modelo lo devuelve.

package cart

import (
	"strings"

	"github.com/EduGoGroup/wapp-shared/textmatch"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
)

// maxQuantityDigits acota lo que se acepta como cantidad ANTES de dársela a la
// sub-máquina. No es la regla de negocio —esa es de stepQuantity, que exige >= 1 y
// hace su propio Atoi—: es una guarda contra un resolutor que devuelva una tira de
// dígitos absurda. Cuatro dígitos es más de lo que nadie pide por WhatsApp.
const maxQuantityDigits = 4

// preresolveOrQuery es el punto ÚNICO por el que el carrito traduce lo que el
// cliente escribió, en las DOS pasadas del turno:
//
//	1.ª pasada (sin veredicto en Vars): corre la cascada determinista. Si resuelve,
//	   devuelve el código y el turno sigue normal. Si no resuelve y el nivel admite
//	   consulta, devuelve la PETICIÓN y el turno del módulo termina ahí.
//	2.ª pasada (con veredicto sembrado por el engine): aplica el veredicto si es
//	   admisible y sigue. NO vuelve a correr la cascada —ya corrió en la primera, y
//	   su telemetría cuenta UNA vez por mensaje, no dos— y sobre todo NO vuelve a
//	   pedir: la presencia de la clave es la señal de «ya preguntaste».
//
// Devolver (input, nil) es siempre el camino de siempre, byte a byte.
func (m Module) preresolveOrQuery(cat catalogo.Catalog, st cartState, vars map[string]any, input string) (string, *modules.Query) {
	if v, found := modules.VerdictFrom(vars); found {
		return applyVerdict(cat, st, input, v), nil
	}
	out, step := m.preresolve(cat, st, input)
	if step != "" && step != stepNone {
		return out, nil // La cascada resolvió: no hay nada que preguntar.
	}
	c, ok := queryable(cat, st, input)
	if !ok {
		return out, nil
	}
	return input, &c
}

// queryable decide si este turno merece una consulta y, si la merece, construye
// la petición. Las cuatro puertas de la regla de oro del pre-resolutor siguen
// valiendo aquí, y por los mismos motivos:
//
//  1. Entrada vacía: no hay nada que interpretar.
//  2. Entrada NUMÉRICA: un número que el cliente teclea es un índice de pantalla o
//     una cantidad, y los dos los resuelve el camino de siempre. Preguntar por un
//     número sería pagar un modelo para que confirme lo obvio.
//  3. Un CÓDIGO del nivel: idem, su dueño ya lo resuelve por igualdad exacta.
//  4. PROSA por encima de maxInputTokens: aquí el techo NO es por coste de
//     comparar (eso era la cascada) sino porque WhatsApp admite 4.096 caracteres y
//     un turno de una persona no puede quedarse esperando a que un modelo digiera
//     una parrafada. Si algún día se decide que la prosa larga también es del LLM,
//     se sube esta constante y se mide; hoy el valor de la tarea está en «mejor
//     dos» y en «finalizar», que caben de sobra.
func queryable(cat catalogo.Catalog, st cartState, input string) (modules.Query, bool) {
	in := strings.TrimSpace(input)
	if in == "" || isNumber(in) {
		return modules.Query{}, false
	}
	tokens := textmatch.SplitTokens(in)
	if len(tokens) == 0 || len(tokens) > maxInputTokens {
		return modules.Query{}, false
	}
	if st.Level == LevelQuantity {
		// El nivel de la CANTIDAD no tiene opciones que ofrecer: la respuesta es un
		// número. Es el único nivel que pregunta sin estar en levelOptions, y la
		// razón está en la cabecera.
		return modules.Query{Class: modules.QueryClassQuantity, Level: st.Level, Text: in}, true
	}
	options := levelOptions(cat, st)
	if len(options) == 0 || isLevelCode(options, in) {
		// 🔴 Sin opciones NO se pregunta, y ese `nil` es el que trae la exclusión de
		// item_note / order_note / buyer_data desde levelOptions: la privacidad
		// se hereda de una sola lista fail-closed en vez de repetirse aquí, donde
		// alguien podría olvidarse de mantenerla al día.
		return modules.Query{}, false
	}
	offered := make([]modules.QueryOption, 0, len(options))
	for _, o := range options {
		offered = append(offered, modules.QueryOption{Code: o.code, Label: o.label})
	}
	return modules.Query{Class: modules.QueryClassOption, Level: st.Level, Text: in, Options: offered}, true
}

// applyVerdict traduce el veredicto a la entrada que verá la sub-máquina, o
// deja la entrada INTACTA si no hay nada aplicable.
//
// Dejarla intacta cubre los cuatro casos degradados con el MISMO gesto —sin
// resolutor, fallo, no concluyente y código inadmisible— y no es una omisión: la
// pantalla que sale entonces es la que el carrito produce hoy ante algo que no
// entiende (el reprompt de su nivel, y a los tres el menú de salida). El módulo NO
// inventa un mensaje nuevo de «no te entendí porque el modelo no estaba»: eso sería
// contarle a la clienta una avería nuestra, y el motivo del veredicto está en el
// enum para quien quiera decidir otra cosa más adelante, no para imprimirlo.
func applyVerdict(cat catalogo.Catalog, st cartState, input string, v modules.Verdict) string {
	if !v.Resolved() {
		return input
	}
	c, ok := queryable(cat, st, input)
	if !ok || !admissibleCode(c, v.Code) {
		return input
	}
	return v.Code
}

// admissibleCode es la aduana: solo pasa lo que el propio módulo ofreció.
func admissibleCode(c modules.Query, code string) bool {
	if c.Class == modules.QueryClassQuantity {
		return isNumber(code) && len(code) <= maxQuantityDigits
	}
	for _, o := range c.Options {
		if o.Code == code {
			return true
		}
	}
	return false
}
