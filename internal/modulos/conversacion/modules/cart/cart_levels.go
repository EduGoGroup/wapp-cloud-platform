// Porta internal/flujos/modules/cart/cart.go @ 9d5a4b6 (trozo «niveles»: advance y los
// step* de L1 a L6; el viejo es un solo fichero y aquí nace partido, 05 E-13)

// cart_levels.go es el CORAZÓN PURO de la sub-máquina: dada la topología fija, el
// estado actual y la entrada ya recortada y ya traducida a código, produce el nuevo
// estado, la pantalla a emitir y los efectos declarados por la transición (design.md
// §4.2). No toca Vars ni BD; Module.Step la envuelve.
//
// No exporta nada: lo que promete se ve por Module.Step. Los códigos de control son
// fijos: «0» es volver en cada nivel (L1 es la raíz y no lo ofrece), «9» es cancelar
// en los dos niveles de decisión (L5 y L6) y «3» es la tecla de las DOS ranuras de
// indicación, la de línea en L5 y la de pedido en L6 (D-041.19; ver cart_notes.go).
// El código de «Más ▾» es DINÁMICO: el mayor código numérico del nivel más uno, para
// no colisionar con ningún ítem; «3» es la tecla de indicación porque L5 y L6 no
// paginan nunca y ahí «Más ▾» no puede aparecer.
//
// Toda entrada que el nivel no reconoce es un INVÁLIDO DE OPCIÓN: se re-muestra la
// pantalla del nivel precedida de «Opción no válida. Responde con el número de una
// de las opciones.» y una línea en blanco, sin avanzar (y, dentro de un evento,
// cuenta: ver Module.Step).
//
// L1 · CATEGORÍAS (LevelCategories)
//   - el código de «Más ▾», si quedan más páginas ⇒ página siguiente;
//   - el código de una categoría —de CUALQUIER página, no solo de la que está en
//     pantalla— ⇒ L2 de esa categoría, página 0, y el efecto category_selected;
//   - lo demás, inválido.
//
// L2 · ARTÍCULOS (LevelArticles)
//   - «0» ⇒ L1, con las líneas intactas (design.md §9.C) y sin categoría, artículo,
//     página ni variante en foco;
//   - «Más ▾», si quedan más ⇒ página siguiente;
//   - el código de un artículo de la categoría ⇒ L3, la ficha SIN descripción. El
//     artículo en foco se guarda por SKU, no por código: el SKU es el identificador
//     estable y no depende de la posición en el catálogo. Elegir artículo NO declara
//     efecto;
//   - lo demás, inválido.
//
// L3 · FICHA DEL ARTÍCULO (LevelArticle)
//   - «1» ⇒ re-muestra la ficha CON la descripción y declara item_viewed {sku};
//   - «2» ⇒ agregar: el nivel de variante si el artículo tiene, o la cantidad si no
//     (variants.go);
//   - «0» ⇒ L2 de la misma categoría, página 0;
//   - lo demás, inválido.
//
// L4 · CANTIDAD (LevelQuantity)
//   - «0» ⇒ un paso atrás (variants.go);
//   - un entero >= 1 ⇒ agrega la línea AL FINAL (dos veces el mismo artículo son dos
//     líneas), pasa a L5 y declara item_added con la foto del carrito;
//   - lo demás —no numérico, negativo, con decimales— repregunta el MISMO paso con
//     «Escribe una cantidad válida (un número mayor o igual a 1).»,
//     una línea en blanco y la pantalla de la cantidad (design.md §9.D). No es un
//     inválido de opción.
//
// L5 · CONTINUAR (LevelContinue)
//   - «1» ⇒ agregar más de la MISMA categoría: L2 con la categoría intacta (§9.C);
//   - «2» ⇒ L6, el resumen;
//   - «3» ⇒ indicación para la línea recién agregada (cart_notes.go);
//   - «9» ⇒ cancela el pedido completo: LevelCancelled, la pantalla de cancelado y
//     el efecto cart_cancelled;
//   - «0» ⇒ vuelve a la ficha del artículo en foco;
//   - lo demás, inválido.
//
// L6 · RESUMEN (LevelSummary) — no ofrece «volver»
//   - «1» ⇒ confirmar: el checklist del comprador si queda algún campo por
//     preguntar (buyer.go) y, si no, el cierre: LevelClosed, la pantalla de
//     confirmado y el efecto cart_closed;
//   - «2» ⇒ seguir agregando: L2 de la categoría en foco;
//   - «3» ⇒ indicación para TODO el pedido (cart_notes.go);
//   - «9» ⇒ cancela, igual que en L5;
//   - lo demás —«0» incluido—, inválido.
//
// Los niveles de variante, de indicación y del checklist están en variants.go,
// cart_notes.go y buyer.go; qué pasa cuando el catálogo ya no tiene lo que el estado
// dice tener en foco, en cart_navigation.go.

package cart
