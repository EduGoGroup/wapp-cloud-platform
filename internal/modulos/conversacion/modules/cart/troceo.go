// Porta internal/flujos/modules/cart/troceo.go @ 9d5a4b6

// troceo.go — «Go DESCOMPONE → el LLM decide chiquito → Go RECOMPONE», dentro de
// un turno de WhatsApp (Plan 044 · Ola 3.5 · T3.5-3).
//
// No exporta nada: lo que promete se ve por Module.Step y por el observador de
// WithMatchHook.
//
// ════════════════════════════════════════════════════════════════════════════
// QUÉ PROBLEMA RESUELVE, Y POR QUÉ NO LO RESUELVE YA EL NIVEL C
// ════════════════════════════════════════════════════════════════════════════
//
// Medido en campo el 2026-08-17 (journal §13.3): un turno con cuatro peticiones
// —«7 pizzas, 2 hamburguesas, 9 empanadas y 4 jugos»— mandado de UNA sentada a un
// modelo chico devuelve UNA sola y pierde tres. La causa es estructural, no del
// modelo: el hueco donde cabe la respuesta es singular. Ningún prompt arregla un
// contrato sin sitio para la respuesta, y por eso el arreglo es TROCEAR.
//
// El pipeline de solicitudes (Nivel C) ya hace fan-out de una llamada por ítem, así
// que la FORMA no es nueva y este fichero no construye un segundo pipeline (C2 del
// ADR-0044: eso está prohibido). Lo que sí es distinto: aquí la descomposición la
// hace Go, no el modelo (en un turno interactivo no hay presupuesto para pagar una
// llamada solo para partir una frase); el Nivel C no llega a los mensajes de una
// conversación que YA está dentro de un flujo; y el destino es otro: líneas de
// carrito con el precio del catálogo del tenant, en el mismo turno.
//
// ════════════════════════════════════════════════════════════════════════════
// DÓNDE Y CUÁNDO SE ACTIVA
// ════════════════════════════════════════════════════════════════════════════
//
// SOLO en el nivel de categorías —el arranque del carrito, que es donde de verdad
// llega una frase así—: dentro de una categoría el cliente está eligiendo, no
// pidiendo. Y solo con DOS o más PETICIONES dentro del turno: con una sola, el
// camino de siempre (cascada → consulta) hace lo mismo y mejor, y el troceado se
// aparta sin que se note que existe. Tampoco mira la entrada vacía, la puramente
// numérica ni la prosa de más de 16 tokens.
//
// Va POR ENCIMA de toda mutación de Step y ANTES que el pre-resolutor (ver
// Module.Step): si la cascada mirara primero, casaría UN producto y se tragaría el
// resto del mensaje.
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 EL ORDEN: SEPARADORES → CANTIDAD EN GO → CASCADA → (y solo entonces) LLM
// ════════════════════════════════════════════════════════════════════════════
//
// 1 · SEPARADORES. El turno se parte, del corte más fuerte al más débil, por el
// salto de línea (el lote agrupado del Edge concatena los mensajes sueltos del
// cliente), la coma, el punto y coma, el «+» y las conjunciones « y » y « e » CON
// ESPACIOS a los dos lados: sin ellos partirían «yogur» y «empanadas» por la mitad.
// Un separador que casa dentro de una palabra no es un separador. Los trozos vacíos
// —dos separadores seguidos— se descartan, y los demás conservan el ORDEN en que el
// cliente los escribió.
//
// 2 · CANTIDAD. No es del modelo: «2» son dígitos (ASCII) y «una» está en una tabla
// LITERAL y corta a propósito — un, uno, una (1); par, dos (2); tres … diez; once
// (11); doce, docena (12) —, sin distinguir mayúsculas. Gana el PRIMER número del
// trozo: en castellano la cantidad precede al producto («2 pizzas»), y otro número
// más adelante suele ser parte del nombre («2 empanadas de 3 quesos»). ⚠️ La
// excepción del compuesto: «un par» y «una docena» son DOS tokens que nombran UNA
// cantidad (2 y 12) —el turno acotado ya trata «dame un par» como 2, y el escalón
// determinista y el del modelo tienen que estar de acuerdo—. Un número que desborda
// no es una cantidad: se queda como parte del nombre. Sin cantidad escrita, es 1. El
// token de la cantidad se QUITA antes de casar, para que la cascada compare solo
// contra lo que nombra el producto.
//
// 3 · CASCADA. Lo que queda del trozo se casa con la MISMA cascada del
// pre-resolutor, pero contra los artículos del catálogo ENTERO: el cliente nombró un
// PRODUCTO, no una opción de la pantalla que tiene delante.
//
// 🔴 QUÉ TROZO CUENTA COMO PETICIÓN. El fixture trae ruido de verdad: «hola» y «para
// llevar» viajan en el mismo lote que «quiero 2 pizzas». Un trozo es una PETICIÓN si,
// y solo si, trae una CANTIDAD EXPLÍCITA o la cascada lo casa con un artículo. «hola»
// falla las dos y se descarta SIN gastar nada; un trozo que era SOLO una cantidad
// («2», «un par») no nombra nada y tampoco cuenta. ⚠️ Lo que este filtro deja fuera
// a propósito: «quiero pizza y algo de tomar» — el segundo trozo no trae cantidad y
// no casa nada, así que NO se pregunta.
//
// 4 · LLM, EL ÚLTIMO PELDAÑO (I2 del ADR-0046). Al modelo solo suben las peticiones
// con cantidad explícita que la cascada NO casó. Entonces Step devuelve SOLO una
// petición (modules.Query) de clase opción, con el nivel, el texto del turno
// recortado, en Chunks el texto de esos trozos —sin su cantidad, en orden— y en
// Options el catálogo entero: el código de cada opción es su POSICIÓN en esa lista
// ("0", "1", …; única por construcción, a diferencia del código del artículo, que
// solo es único dentro de su categoría) y la etiqueta la del artículo. Con un
// catálogo cuyas etiquetas se parecen a lo que el cliente escribe, los dos fixtures
// del criterio se resuelven con CERO llamadas: es el caso bueno, no una excepción.
//
// ════════════════════════════════════════════════════════════════════════════
// LA SEGUNDA PASADA Y LA RECOMPOSICIÓN
// ════════════════════════════════════════════════════════════════════════════
//
// Con el veredicto sembrado se vuelve a trocear —trocear es una función PURA de la
// entrada: mismo texto, mismos trozos, mismo orden— y Verdict.Codes se vuelca sobre
// las peticiones sin casar, POR POSICIÓN. Un código vacío, ilegible, negativo o
// fuera de la lista deja la petición sin resolver: es la misma aduana de la consulta
// (el resolutor es un modelo y puede inventar).
//
// Las peticiones resueltas entran como líneas, en orden, con la MISMA forma que
// Prime: el estado salta a LevelContinue con el foco en la ÚLTIMA línea agregada
// (su categoría y su artículo), cart_started si aún no se había declarado y un
// item_added POR LÍNEA, cada uno con la foto del carrito HASTA esa línea. El
// contador de inválidos se reinicia: este turno fue válido.
//
// Dos productos identificados NO se agregan, y los dos por no inventar un precio: el
// artículo CON VARIANTES (con N líneas no hay a quién preguntar primero la variante:
// se deja fuera y el cliente lo agrega por el menú) y lo que no llegó a resolverse.
// Los dos se CUENTAN y salen en la pantalla — el «no se pierde en silencio» del
// ADR-0044 §5 dicho hacia la persona que está pidiendo:
//
//	Agregué a tu pedido:
//	• <qty> × <etiqueta> (<precio> c/u)
//
//	No pude identificar 1 producto más de tu mensaje; podés agregarlo eligiendo del menú.
//
//	Añadido al pedido ✅ …
//
// («N productos más» con más de uno; el párrafo no sale si nada quedó fuera.) 🔴 NO
// se le devuelve al cliente el texto que no se entendió: repetirle sus propias
// palabras al lado de un «no pude» suena a reproche y no le dice qué hacer.
//
// Si al final no entró NADA, no hay pedido que recomponer y el turno sigue por el
// camino de siempre, que repromptea como cualquier otro día.
//
// TELEMETRÍA: por el mismo observador de la cascada, una vez POR TROZO que sí era
// una petición — "troceo" si entró en el pedido, "troceo_perdido" si no —, con el
// nivel. La pregunta que hay que poder responder desde fuera es «¿cuánto del pedido
// de la gente se está perdiendo?», y se responde con la proporción entre los dos.
// 🔴 CERO TEXTO DEL CLIENTE.

package cart
