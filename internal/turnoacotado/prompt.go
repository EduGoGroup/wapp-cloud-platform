// prompt.go — LOS DOS PROMPTS DEL TURNO ACOTADO, Y POR QUÉ SON DOS
// (Plan 044 · Ola 3.5 · T3.5-2).
//
// ════════════════════════════════════════════════════════════════════════════
// 🔑 UN ESQUEMA ÚNICO DA 9/12; SEPARADO POR TIPO DE PREGUNTA DA 11/12
// ════════════════════════════════════════════════════════════════════════════
//
// Es el hallazgo de la medición (2026-08-26, qwen3:1.7b, 12 fixtures, dos pasadas,
// contra Ollama real en GPU local y en el VPS de CPU), y no es una intuición de
// estilo: mezclar las dos preguntas confunde al modelo porque el campo `value`
// significa COSAS DISTINTAS en cada una. En una pregunta de CANTIDAD, `value` ES la
// cantidad («mejor dos» → 2). En una de ELECCIÓN, `value` es el NÚMERO DE OPCIÓN de
// la lista («quiero el Helado» → 2 porque Helado es la segunda), que no tiene nada
// que ver con lo que el cliente escribió. Un solo prompt tiene que explicar las dos
// cosas y el modelo acaba aplicando la regla equivocada.
//
// Por eso cada clase lleva SU prompt de sistema, SU few-shot y SU esquema. Enrutar
// por clase no es trampa: quien pregunta es el propio carrito, que es exactamente
// quien decidió qué pregunta estaba haciendo (modules.Consulta.Clase).
//
// ════════════════════════════════════════════════════════════════════════════
// 🔑 EL FEW-SHOT NO ES DECORACIÓN
// ════════════════════════════════════════════════════════════════════════════
//
// El ADR-0020 §6 lo dice para el clasificador y vale igual aquí: en un modelo de
// 1–2B los EJEMPLOS pesan más que las instrucciones. Sin ellos el modelo falla al
// mapear «mejor dos» → 2, que es el caso que justifica la tarea entera. Los cinco
// ejemplos de cada clase son LITERALES PORTADOS del guion que midió 11/12; no se
// «mejoran» sin volver a medir, porque lo único que dice si un few-shot sirve es
// una corrida contra el modelo real.
//
// ════════════════════════════════════════════════════════════════════════════
// ⚠️ LO QUE SÍ CAMBIA RESPECTO DE LA MEDICIÓN: UN SOLO TURNO DE USUARIO
// ════════════════════════════════════════════════════════════════════════════
//
// El guion hablaba con /api/chat mandando un `messages` de once entradas (system +
// cinco pares user/assistant + la pregunta). Por el cable de wApp NO cabe eso: el
// frame de CloudLink lleva UN campo `prompt` y el Edge lo entrega en un único
// mensaje de usuario, a propósito («partirlo aquí en system+user sería
// interpretarlo», cajero/servidor.go). Así que el mismo contenido se APLANA a texto
// con los rótulos de abajo. El texto de las instrucciones y el de los ejemplos es
// idéntico al medido; lo que cambia es el envoltorio.
//
// Consecuencia honesta: el 11/12 se midió con la forma multi-turno. Esta forma
// aplanada NO se ha medido contra el modelo real —los tests de este paquete corren
// sin red, por regla del repo— así que el número que hay que volver a levantar en
// campo es ese, y el sitio donde mirarlo es la telemetría del desenlace de la
// consulta (engine.ObservadorConsulta), no un test.
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 POR QUÉ ESTOS PROMPTS VIVEN AQUÍ Y NO EN wapp-shared/llm
// ════════════════════════════════════════════════════════════════════════════
//
// Los cinco prompts del PIPELINE viven en shared porque C2 (ADR-0044) exige que las
// dos vías —local y api— ejecuten el MISMO esquema de orquestación: un prompt en el
// adaptador local sería el primer paso hacia dos pipelines. El turno acotado no está
// en esa situación: no es una etapa del pipeline, no lo sirve la vía API
// (llmvia.ErrViaSinTurnoAcotado) y por tanto no hay dos implementaciones que puedan
// divergir. Meterlo en shared costaría una release del módulo compartido para un
// texto que hoy tiene UN solo consumidor.
package turnoacotado

import (
	"strconv"
	"strings"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
)

// ---------------------------------------------------------------------------
// PREGUNTAS DE CANTIDAD
// ---------------------------------------------------------------------------

// sistemaCantidad son las instrucciones de una pregunta de CANTIDAD. Literal
// portado de la medición.
//
// ⚠️ La última regla —«fuera_de_rango NO aplica a preguntas de cantidad»— no es
// relleno: sin ella el modelo la usaba para cualquier número que le pareciera raro,
// y ese motivo existe para la otra clase. De todos modos el `reason` es TELEMETRÍA
// y no decide nada (ver Salida.Reason).
const sistemaCantidad = `Eres el intérprete de una respuesta de un cliente a una pregunta de CANTIDAD dentro de un carrito de compras conversacional por WhatsApp (p. ej. "¿Cuántas pizzas querés?"). Se te da la pregunta y la respuesta en texto libre del cliente. Tu tarea es decidir si la respuesta es utilizable y, si lo es, extraer la cantidad pedida (aceptá dígitos, números en palabras, y expresiones como "una docena" = 12, "un par" = 2).

Reglas:
- Si el cliente da una cantidad válida, usable=true, value=<la cantidad>, motivo="ok".
- Si el cliente cambia de tema, rechaza el pedido, o pide algo distinto a lo que se le preguntó, usable=false, value=null, motivo="cambio_de_intencion".
- Si el cliente hace una pregunta en vez de responder, usable=false, value=null, motivo="otra_pregunta".
- Si la respuesta no se puede interpretar como una cantidad, usable=false, value=null, motivo="no_entendido".
- El motivo "fuera_de_rango" NO aplica a preguntas de cantidad (no hay un máximo conocido de antemano); no lo uses nunca acá.

Respondé ÚNICAMENTE con el JSON pedido por el esquema, sin explicaciones ni texto extra.`

// ejemplosCantidad son los cinco pares (caso, respuesta) del few-shot medido, en su
// orden original. El orden se conserva porque el prefijo del prompt se cachea
// entero en el Ollama del Edge: reordenarlos invalida esa caché para todos los
// turnos siguientes, que es el recurso que la Ola 1.7 existe para conservar.
var ejemplosCantidad = []ejemplo{
	{caso: casoCantidad("¿Cuántos refrescos querés?", "quiero cinco"), salida: `{"usable": true, "value": 5, "reason": "ok"}`},
	{caso: casoCantidad("¿Cuántas empanadas querés?", "dame un par"), salida: `{"usable": true, "value": 2, "reason": "ok"}`},
	{caso: casoCantidad("¿Cuántas pizzas querés?", "en realidad prefiero un flan"), salida: `{"usable": false, "value": null, "reason": "cambio_de_intencion"}`},
	{caso: casoCantidad("¿Cuántas pizzas querés?", "asdkj no sé qué poner"), salida: `{"usable": false, "value": null, "reason": "no_entendido"}`},
	{caso: casoCantidad("¿Cuántas pizzas querés?", "¿hacen envíos a domicilio?"), salida: `{"usable": false, "value": null, "reason": "otra_pregunta"}`},
}

// esquemaCantidad es el JSON Schema serializado que viaja en el campo `format` del
// frame. Se escribe a mano como constante y NO se genera con json.Marshal de un
// mapa: un mapa de Go no tiene orden, así que el string saldría distinto entre
// arranques y el prefijo del prompt dejaría de ser estable —o sea, adiós caché de
// prefijo del Edge por un detalle invisible—.
//
// 🔴 EL EJEMPLO DEL ESQUEMA NO PUEDE CONTENER UN VALOR QUE SU PROPIO VALIDADOR
// RECHACE. Aquí no hay ejemplos incrustados justamente por eso: el modelo copia lo
// que ve, y P4 estuvo 0 de 14 en campo por una plantilla que imprimía un
// `"package_size": 0` que su validador prohibía.
const esquemaCantidad = `{"type":"object","properties":{"usable":{"type":"boolean","description":"true si el cliente dio una cantidad utilizable"},"value":{"type":["integer","null"],"description":"la cantidad pedida por el cliente; null si no aplica"},"reason":{"type":"string","enum":["ok","cambio_de_intencion","fuera_de_rango","no_entendido","otra_pregunta"]}},"required":["usable","value","reason"]}`

// ---------------------------------------------------------------------------
// PREGUNTAS DE ELECCIÓN DE OPCIÓN
// ---------------------------------------------------------------------------

// sistemaOpcion son las instrucciones de una pregunta de ELECCIÓN. Literal portado
// de la medición.
//
// 🔴 «EL NÚMERO DE LA OPCIÓN SEGÚN LA LISTA (NO EL NOMBRE DEL PRODUCTO)» ES LA FRASE
// QUE HACE QUE ESTO FUNCIONE. Lo que el carrito necesita de vuelta es un CÓDIGO de
// su catálogo (`burgers`, `volver`, un ordinal de variante), y esos códigos no se le
// enseñan al modelo: se le enseña la lista numerada que el cliente tiene delante y
// se traduce la posición al código en Go (ver Resolver.veredicto). Pedirle
// directamente el código le daría un vocabulario que no entiende y una forma más de
// inventarse una cadena.
const sistemaOpcion = `Eres el intérprete de una respuesta de un cliente a una pregunta de ELECCIÓN dentro de un carrito de compras conversacional por WhatsApp: se le mostró una lista numerada de opciones y debe elegir una. Se te da la pregunta, la lista de opciones ofrecidas y la respuesta en texto libre del cliente. Tu tarea es decidir si la respuesta es utilizable y, si lo es, extraer el NÚMERO de la opción elegida según la lista (no el nombre del producto), sin importar si el cliente lo dice en dígito, en palabra, por posición ("la segunda") o nombrando el producto de la lista.

Reglas:
- Si el cliente elige un número dentro del rango de la lista, usable=true, value=<número de esa opción>, motivo="ok".
- Si el número que pide el cliente NO está en la lista (mayor al total de opciones, o es 0 o negativo), usable=false, value=null, motivo="fuera_de_rango".
- Si el cliente rechaza todas las opciones o pide algo fuera de la lista, usable=false, value=null, motivo="cambio_de_intencion".
- Si el cliente hace una pregunta en vez de elegir, usable=false, value=null, motivo="otra_pregunta".
- Si la respuesta no se puede interpretar como una elección, usable=false, value=null, motivo="no_entendido".

Respondé ÚNICAMENTE con el JSON pedido por el esquema, sin explicaciones ni texto extra.`

// ejemplosOpcion son los cinco pares del few-shot medido, en su orden original.
var ejemplosOpcion = []ejemplo{
	{caso: casoOpcion(preguntaOpcion, []string{"Torta", "Helado"}, "dale, quiero la 1"), salida: `{"usable": true, "value": 1, "reason": "ok"}`},
	{caso: casoOpcion(preguntaOpcion, []string{"Torta", "Helado"}, "quiero el Helado"), salida: `{"usable": true, "value": 2, "reason": "ok"}`},
	{caso: casoOpcion(preguntaOpcion, []string{"Torta", "Helado"}, "la 9"), salida: `{"usable": false, "value": null, "reason": "fuera_de_rango"}`},
	{caso: casoOpcion(preguntaOpcion, []string{"Torta", "Helado"}, "no quiero nada de eso, cancelá el pedido"), salida: `{"usable": false, "value": null, "reason": "cambio_de_intencion"}`},
	{caso: casoOpcion(preguntaOpcion, []string{"Torta", "Helado"}, "¿cuánto sale el envío?"), salida: `{"usable": false, "value": null, "reason": "otra_pregunta"}`},
}

// esquemaOpcion es el JSON Schema serializado de la clase opción. Ver esquemaCantidad
// para por qué es una constante escrita a mano.
const esquemaOpcion = `{"type":"object","properties":{"usable":{"type":"boolean","description":"true si el cliente eligió una opción válida de la lista"},"value":{"type":["integer","null"],"description":"el número de la opción elegida según la lista; null si no aplica"},"reason":{"type":"string","enum":["ok","cambio_de_intencion","fuera_de_rango","no_entendido","otra_pregunta"]}},"required":["usable","value","reason"]}`

// ---------------------------------------------------------------------------
// COMPOSICIÓN
// ---------------------------------------------------------------------------

// ejemplo es un par del few-shot: el caso tal como se le presenta al modelo y la
// respuesta JSON que debería dar.
type ejemplo struct {
	caso   string
	salida string
}

// preguntaOpcion y preguntaCantidad son las preguntas GENÉRICAS con las que se le
// presenta el caso al modelo.
//
// ⚠️ POR QUÉ GENÉRICAS, dicho porque es una pérdida real y conviene que se vea:
// modules.Consulta NO lleva el texto de la pantalla que el carrito imprimió, así
// que aquí no se sabe si la pregunta fue «¿Cuántas pizzas querés?» o «¿Cuántas
// empanadas?». Para la clase opción no importa —el contexto lo da la LISTA, que sí
// viaja— y para cantidad tampoco cambia la respuesta: lo que hay que interpretar es
// «mejor dos», no de qué es. Si algún día se quiere el literal exacto, el sitio es
// un campo nuevo en modules.Consulta (es texto NUESTRO, de la pantalla, no del
// cliente: no choca con la regla de privacidad) y no una adivinanza desde el nivel.
const (
	preguntaOpcion   = "Elegí una opción:"
	preguntaCantidad = "¿Cuántas unidades querés?"
)

// sinOpciones es el rótulo literal que el guion medido usaba para decirle al modelo
// que esta pregunta no trae lista.
const sinOpciones = "Opciones ofrecidas: (ninguna, es una pregunta abierta de cantidad)"

// casoCantidad y casoOpcion arman el bloque «caso» con el MISMO formato de tres
// líneas del guion medido. Los usan tanto el few-shot como la pregunta real: si los
// dos no tuvieran exactamente la misma forma, el modelo no reconocería el patrón que
// los ejemplos le acaban de enseñar, que es todo lo que un few-shot hace.
func casoCantidad(pregunta, respuesta string) string {
	return "Pregunta hecha al cliente: \"" + pregunta + "\"\n" +
		sinOpciones + "\n" +
		"Respuesta del cliente: \"" + respuesta + "\""
}

func casoOpcion(pregunta string, etiquetas []string, respuesta string) string {
	var b strings.Builder
	b.WriteString("Pregunta hecha al cliente: \"" + pregunta + "\"\n")
	b.WriteString("Opciones ofrecidas:\n")
	for i, e := range etiquetas {
		b.WriteString(strconv.Itoa(i+1) + ". " + e + "\n")
	}
	b.WriteString("Respuesta del cliente: \"" + respuesta + "\"")
	return b.String()
}

// prompt compone el texto COMPLETO que viaja en el frame y devuelve además el
// esquema de esa clase. Es la única función que sabe cómo se aplana el multi-turno.
//
// El orden —instrucciones, ejemplos, caso— es el del guion medido y además es el que
// hace útil la caché de prefijo del Edge: todo lo que NO cambia entre dos turnos va
// delante y lo único variable (el caso) va al final. Invertirlo dejaría el prefijo
// distinto en cada mensaje y cada turno pagaría prefill en frío, que son los 18 s
// que el plazo de 12 corta.
func prompt(c modules.Consulta) (texto, esquema string) {
	sistema, ejemplos, esq, caso := sistemaCantidad, ejemplosCantidad, esquemaCantidad,
		casoCantidad(preguntaCantidad, c.Texto)
	if c.Clase == modules.ClaseOpcion {
		sistema, ejemplos, esq = sistemaOpcion, ejemplosOpcion, esquemaOpcion
		etiquetas := make([]string, 0, len(c.Opciones))
		for _, o := range c.Opciones {
			etiquetas = append(etiquetas, o.Etiqueta)
		}
		caso = casoOpcion(preguntaOpcion, etiquetas, c.Texto)
	}

	var b strings.Builder
	b.WriteString(sistema)
	b.WriteString("\n\nEjemplos resueltos:\n")
	for _, ej := range ejemplos {
		b.WriteString("\n" + ej.caso + "\n" + ej.salida + "\n")
	}
	b.WriteString("\nAhora resolvé este caso:\n\n")
	b.WriteString(caso)
	return b.String(), esq
}
