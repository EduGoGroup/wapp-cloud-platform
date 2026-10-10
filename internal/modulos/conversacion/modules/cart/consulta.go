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
