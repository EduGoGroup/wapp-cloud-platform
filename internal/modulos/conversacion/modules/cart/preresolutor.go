// Porta internal/flujos/modules/cart/preresolutor.go @ 9d5a4b6

// preresolutor.go — LA CASCADA DETERMINISTA, DELANTE DE LA SUB-MÁQUINA
// (Plan 044 · Ola 3.5 · T3.5-1).
//
// No exporta nada: lo que promete se ve por Module.Step y por el observador de
// WithMatchHook.
//
// ════════════════════════════════════════════════════════════════════════════
// QUÉ PROBLEMA RESUELVE
// ════════════════════════════════════════════════════════════════════════════
//
// Hasta esta tarea el carrito solo entendía CÓDIGOS EXACTOS, y todo lo demás
// —«hamburgesa», «agrega 2 hamburguesas», «cancelar»— caía en el reprompt. El
// cliente que escribe en vez de teclear un número no se equivocaba: es que el
// carrito solo sabía leer números.
//
// El pre-resolutor traduce lo que el cliente escribió al CÓDIGO CANÓNICO que la
// sub-máquina ya entiende, y a partir de ahí no cambia NADA: la sub-máquina recibe
// "2" venga de un teclado numérico o de la palabra «hamburguesa». Por eso ningún
// nivel tiene una rama nueva para esto.
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 LA REGLA DE ORO: SI NO RESUELVE CON CERTEZA, NO TOCA NADA
// ════════════════════════════════════════════════════════════════════════════
//
// Todo lo que este fichero no resuelve sale por donde entró —la entrada INTACTA— y
// el flujo se comporta EXACTAMENTE como el día antes de esta tarea. Las tres
// puertas que garantizan la regresión cero, en este orden:
//
//  1. Un nivel EXCLUIDO no llega ni a mirar la entrada.
//  2. Un CÓDIGO —cualquier cosa puramente numérica (dígitos ASCII), y cualquier
//     código del nivel— se devuelve intacto SIN pasar por la cascada. Esto no es una
//     optimización: es lo que impide que el "3" que el cliente teclea para paginar
//     acabe casando por fuzzy con una etiqueta. Un número que el usuario escribe es
//     un índice de pantalla, jamás un nombre. «Numérico» no es strconv.Atoi a
//     propósito: Atoi acepta signo ("-3") y desborda con tiras largas, y aquí la
//     pregunta no es «cuánto vale» sino «esto lo tecleó como número».
//  3. Un empate entre DOS opciones distintas no resuelve. «torta» con «Torta de
//     chocolate» y «Torta de vainilla» en pantalla no es una elección: es una
//     pregunta, y adivinar metería el artículo equivocado en el pedido.
//
// Tampoco se mira la entrada vacía ni la PROSA: por encima de 16 tokens no es una
// elección de menú —WhatsApp admite 4.096 caracteres— y barrerla contra el catálogo
// cuesta el producto de las dos longitudes.
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 QUÉ NIVELES ADMITEN CASCADA, Y POR QUÉ LOS DEMÁS NO ES UN OLVIDO
// ════════════════════════════════════════════════════════════════════════════
//
// Admiten cascada, con estas opciones (código que se devuelve ← etiqueta contra la
// que se compara):
//
//   - categories: todas las categorías del catálogo, no solo las de la página en
//     pantalla (nombrar una de la página 3 funciona y ahorra dos «Más ▾»). El propio
//     «Más ▾» NO es candidato: su rótulo normaliza a «mas», palabra corrientísima
//     («mas hamburguesas» mandaría a la página siguiente en vez de al artículo).
//   - articles: los artículos de la categoría en foco, más «0» ← «Volver».
//   - variant: el ORDINAL de pantalla (1..N) ← la etiqueta de cada variante, más
//     «0» ← «Volver». No el código de negocio de la variante, que nadie teclea.
//   - article: «1» ← «Ver descripción», «2» ← «Agregar al pedido», «0» ← «Volver».
//   - continue: «1» ← «Agregar más» (el trozo ESTABLE del rótulo, sin la categoría),
//     «2» ← «Finalizar pedido», «3» ← «Indicación para este artículo», «9» ←
//     «Cancelar pedido», «0» ← «Volver».
//   - summary: «1» ← «Confirmar y finalizar», «2» ← «Seguir agregando», «3» ←
//     «Indicación para todo el pedido», «9» ← «Cancelar pedido».
//   - item_note_scope: «1» ← «Para las N», «2» ← «Solo para 1», «0» ← «Volver sin
//     indicación».
//
// Si el catálogo ya no puede resolver lo que el nivel tiene en foco (categoría o
// artículo desaparecidos, artículo sin variantes en el nivel de variante, alcance
// sin líneas), el nivel no ofrece opciones y decide la sub-máquina.
//
// item_note, order_note y buyer_data NO están, y esa ausencia es un REQUISITO DE
// PRIVACIDAD: no son elecciones de opción, son TEXTO LIBRE del cliente. buyer_data
// en particular recoge DATOS PERSONALES —nombre, RUT, dirección— que salen del
// módulo dentro de un efecto privado y acaban CIFRADOS, precisamente para que no
// queden en claro en ningún sitio. Pasarlos por una cascada de similitud sería
// manosear un dato personal para adivinar a qué opción «se parece», y abrir la
// puerta a que ese texto acabe en una etiqueta de telemetría. quantity queda fuera
// por diseño: una cascada DETERMINISTA no sabe traducir «dos» a 2 —es aritmética del
// lenguaje, no similitud ortográfica—. closed y cancelled son terminales. Y la
// lista es FAIL-CLOSED: un nivel nuevo NO entra en la cascada hasta que se declare
// de forma explícita. La ausencia nunca puede ser un sí.
//
// ════════════════════════════════════════════════════════════════════════════
// PUREZA Y UMBRAL
// ════════════════════════════════════════════════════════════════════════════
//
// La cascada es `Exact → Fuzzy(0,85)` de wapp-shared/textmatch, construida SIN zona
// gris (determinista): CERO I/O, cero red, cero LLM. El módulo sigue siendo puro y
// el turno del cliente no puede colgarse esperando a nadie.
//
// 🔴 EL UMBRAL 0,85 NO SE TOCA (D-044.45). Es RELATIVO (`1 − dist/len`), así que
// una errata solo se perdona a partir de 7 runas: «torta», «pizza» y «ñoquis» NO la
// perdonan, y eso está decidido y aceptado. Bajarlo a 0,80 casaría «torta» con
// «tarta», que es meter otro artículo en el pedido de alguien.
//
// ════════════════════════════════════════════════════════════════════════════
// CÓMO CASA UNA FRASE CONTRA UNA ETIQUETA (el porqué de los PREFIJOS)
// ════════════════════════════════════════════════════════════════════════════
//
// Se compara por VENTANAS de tokens: para una etiqueta de k tokens se prueban sus
// PREFIJOS de 1..k tokens contra cada ventana contigua de la entrada del mismo
// tamaño. Una ventana puramente numérica no compite: el «2» de «agrega 2
// hamburguesas» es una cantidad, no un nombre.
//
// Prefijos y no cualquier sub-conjunto, y la razón es lingüística: en castellano el
// sintagma nominal es de núcleo inicial y la gente abrevia POR LA DERECHA —«torta»
// por «Torta de chocolate», «cancelar» por «Cancelar pedido»—, nunca por la
// izquierda. Aceptar cualquier token haría casar «pedido» con tres opciones a la
// vez. Consecuencia CONOCIDA y aceptada: «finalizar» en el resumen no casa nada,
// porque ahí la opción se llama «Confirmar y finalizar»; es material de la consulta
// (consulta.go).
//
// Desempate: gana la coincidencia que CONSUME MÁS TOKENS de la entrada y, a
// igualdad, la de más confianza: «solo para 1» casa la opción 2 entera (3 tokens) y
// no la opción 1 por su «para» suelto.
//
// ════════════════════════════════════════════════════════════════════════════
// TELEMETRÍA
// ════════════════════════════════════════════════════════════════════════════
//
// El observador recibe el escalón que resolvió —la procedencia que declara la
// propia estrategia, "exact" o "fuzzy"— o "ninguno" si la cascada corrió y no
// resolvió, y el nivel. Se cuenta lo que la CASCADA hizo, no lo que el turno hizo:
// si no corrió (nivel excluido, entrada vacía, un código, prosa larga) no hay aviso.

package cart
