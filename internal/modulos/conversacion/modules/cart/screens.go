// Porta internal/flujos/modules/cart/screens.go @ 9d5a4b6

// screens.go son las PANTALLAS del carrito: los textos que el cliente recibe por
// WhatsApp, la paginación de las listas y el formato de los importes.
//
// No exporta nada: lo que promete se ve por Module.Render, Module.Step, Module.Prime
// y RevalidationMessage. Los textos son OBSERVABLES y se conservan byte a byte: los
// sujetan los goldens de testdata/ (cart_v1_transcript, cart_v2_transcript) y los
// tests de este fichero.
//
// IMPORTES. Siempre `$` y dos decimales, sin separador de miles: «$2.50»,
// «$18000.00». El total de una línea es qty × unit_price y el del pedido la suma de
// sus líneas, y de nada más (INV-13).
//
// PAGINACIÓN (L1 y L2). Una página enseña `tamaño` elementos; un tamaño <= 0 vale
// DefaultPageSize y una página negativa vale 0. Si tras la página quedan más, la
// lista acaba con «<código>) Más ▾», donde el código es el mayor código NUMÉRICO de
// TODA la lista del nivel más uno (los códigos no numéricos no cuentan; sin ninguno
// numérico es «1»). La última página no lo ofrece.
//
//	L1   🛒 Elige una categoría:
//	     <código>) <categoría>            …una por línea; SIN «volver»: es la raíz
//
//	L2   <categoría>:
//	     <código>) <artículo> · <precio>
//	     0) ← Volver
//
//	L3   <artículo> · <precio>
//	     <descripción>                    solo tras «1»; «(sin descripción)» si no tiene
//	     1) Ver descripción
//	     2) Agregar al pedido
//	     0) ← Volver
//
//	L3b  <artículo> · elige una opción:
//	     <n>) <variante> · <precio>       numeradas POR POSICIÓN, sin paginar
//	     0) ← Volver
//
//	L4   ¿Cuántos "<lo que se pide>"? Escribe la cantidad (0 ← volver)
//
//	L5   Añadido al pedido ✅
//	     1) Agregar más de <categoría>    «1) Agregar más» si no hay categoría en foco
//	     2) Finalizar pedido
//	     3) ✏️ Indicación para este artículo
//	     9) Cancelar pedido
//	     0) ← Volver
//
//	L6   🧾 Resumen del pedido:
//	     <línea> x<qty>  <total de la línea>
//	        ✏️ <indicación de la línea>   solo si la tiene, pegada bajo su línea
//	     TOTAL  <total>
//	     ✏️ Para todo el pedido: <nota>   solo si la hay, bajo el total
//	     1) Confirmar y finalizar
//	     2) Seguir agregando
//	     3) ✏️ Indicación para todo el pedido
//	     9) Cancelar pedido
//
// El <precio> de un artículo CON variantes es «desde <el de la variante más
// barata>», en las listas y en la ficha: su Price es solo una referencia (D-041.2), y
// prometer un precio fijo para cobrar otro al elegir la presentación sería mentir en
// pantalla. En L4, <lo que se pide> es el artículo a secas o «<artículo> —
// <variante>».
//
// INDICACIONES (D-041.19 / D-041.20). Las dos pantallas de texto repiten tres líneas
// que no son decoración —el ADR-0034 exige decir para qué sirve el campo, y la
// advertencia es la CONTENCIÓN de la ranura, porque aquí wApp no puede detectar PII—:
//
//	Es una instrucción para quien lo prepara y NO cambia el precio.
//	No escribas aquí datos personales, direcciones ni datos de pago.
//	Máx. 280 caracteres.
//
//	alcance   ✏️ Indicación para "<línea>" (pediste <N>):
//	          1) Para las <N>
//	          2) Solo para 1 (la separo en dos líneas)
//	          0) ← Volver sin indicación
//
//	línea     ✏️ Escribe la indicación para "<línea>".
//	          ✏️ Escribe la indicación para 1 de las <N> "<línea>".    con «solo para 1»
//	          …las tres líneas… y «0) ← Volver sin indicación»
//
//	pedido    ✏️ Escribe una indicación para todo el pedido.
//	          …las tres líneas… y «0) ← Volver sin indicación»
//
// Si ya hay una indicación anotada, las pantallas de texto la anteponen —`Indicación
// actual: "<texto>" — escribe otra para reemplazarla.`— y entonces «0» significa
// «déjala como está».
//
// FINALES. Confirmado: «✅ ¡Pedido confirmado! Total <total>.». Cancelado: «Pedido
// cancelado. Si quieres hacer otro, escribe "carrito" y lo empezamos de cero.». La
// frase del cancelado cambió con el hallazgo #29 (decisión de Jhoan, 2026-08-11):
// decía «Puedes iniciar uno nuevo cuando quieras», que era cierto mientras cualquier
// texto reabría el catálogo; desde que el flujo TERMINA al cancelar hace falta el
// disparador real. ⚠️ Costura conocida: la palabra «carrito» va fija y el módulo es
// PURO —no conoce las reglas `event_start` del tenant—; en un tenant con otra palabra
// ese texto no casa ninguna regla y cae en la oferta/fallback, que sí enumera lo que
// se puede hacer. Nombrarla de verdad exige que el disparador viaje hasta el módulo
// (Plan 053).

package cart
