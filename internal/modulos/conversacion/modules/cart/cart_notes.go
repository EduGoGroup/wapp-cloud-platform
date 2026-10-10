// Porta internal/flujos/modules/cart/cart.go @ 9d5a4b6 (trozo «indicaciones»:
// toItemNote, stepItemNoteScope, stepItemNote, stepOrderNote y backToContinue; el
// viejo es un solo fichero y aquí nace partido, 05 E-13)

// cart_notes.go son los tres niveles de INDICACIÓN del cliente (Plan 041 · T4.1c,
// D-041.19 + D-041.20): la de una línea («sin cebolla») y la de todo el pedido
// («dejarlo en portería»). NO son pasos del recorrido: son ramas OPCIONALES colgadas
// de la tecla «3» de dos menús que ya se imprimían (L5 y L6), y se vuelve al nivel
// de origen en cuanto se resuelven. Quien no pulsa «3» teclea exactamente lo mismo
// que antes de que existieran (INV-15). En cualquier otro nivel, «3» sigue siendo lo
// que era: un código de catálogo, una cantidad o un inválido.
//
// No exporta nada: lo que promete se ve por Module.Step. Una indicación JAMÁS toca
// el dinero: ni el total ni el precio de una línea (INV-13).
//
// «3» EN L5 · indicación de la línea. La línea comentada es SIEMPRE la última: a L5
// solo se llega tras un item_added, así que «este artículo» no es ambiguo nunca, ni
// con dos líneas del mismo producto.
//   - Con UNA unidad va directo al texto (LevelItemNote): el caso corriente no gana
//     una pantalla.
//   - Con MÁS de una pregunta antes el ALCANCE (LevelItemNoteScope), porque la
//     indicación es siempre de línea entera y sin preguntar anotaría las N unidades.
//   - Sin líneas —un estado que el recorrido no produce— es un inválido de opción
//     de L5.
//
// ALCANCE (LevelItemNoteScope): «1» ⇒ para las N; «2» ⇒ solo para 1; «0» ⇒ vuelve a
// L5 sin indicación; lo demás, inválido. Elegir el alcance NO parte nada todavía:
// solo decide qué se hará al guardar un texto válido.
//
// TEXTO DE LÍNEA (LevelItemNote)
//   - «0» ⇒ vuelve a L5 sin tocar nada; si la línea ya tenía indicación, se queda
//     como estaba.
//   - El texto pasa por el saneo de la puerta (intakes.SanitizeNote). Vacío tras
//     sanear ≡ «0»: sin indicación y sin error (REQ-33e). Pasado del límite
//     (intakes.MaxNoteRunes, medido DESPUÉS de sanear) se RECHAZA en el mismo paso
//     —«Esa indicación es muy larga (N de 280 caracteres). Escríbela más corta.», una
//     línea en blanco y la misma pantalla— y jamás se trunca; el carrito no cambia.
//   - Un texto válido se guarda como `customization` de la última línea
//     (reemplazando la que hubiera), declara note_added y vuelve a L5 con el acuse
//     —`Anotado: "<texto>" ✅`— delante de la pantalla.
//   - «Solo para 1» sobre una línea ×N la PARTE al guardar: ×(N-1), que CONSERVA la
//     indicación que la línea ya tuviera, y ×1 con la nueva, EN ESE ORDEN (la
//     comentada queda la última, que es la que el siguiente «3» volvería a tomar).
//     Mismo sku, misma etiqueta, mismo precio y misma suma de cantidades: es una
//     re-agrupación, no una edición, y el total no se mueve (INV-16). Que el resto
//     herede la indicación es la decisión D-044.18: quien pidió «con cebolla» para
//     las dos y luego «sin sal» para una NO se queda sin cebolla en la otra. El
//     acuse es `Anotado para 1 de las N: "<texto>" ✅` y el efecto lleva
//     split_from_qty. El split ocurre AQUÍ y no al elegir el alcance: si el cliente
//     desiste o se pasa del límite, la línea no se ha partido.
//   - El alcance en curso muere SIEMPRE al salir del nivel, se haya guardado o no.
//
// TEXTO DE PEDIDO (LevelOrderNote): «0» o vacío tras sanear ⇒ vuelve al resumen sin
// tocar la nota que hubiera; demasiado largo ⇒ rechazo en el mismo paso; un texto
// válido se guarda como la nota del pedido (reemplazando la anterior), declara
// note_added con ámbito "order" y vuelve al resumen —que ya la enseña bajo el total—
// con el acuse `Anotado para todo el pedido: "<texto>" ✅` delante.
//
// Los tres niveles de texto son TEXTO LIBRE: ni cascada ni consulta los miran
// (preresolutor.go, consulta.go), y un rechazo por largo no es un inválido de opción.

package cart
