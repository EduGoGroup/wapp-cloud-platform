// Porta internal/flujos/modules/cart/cart.go @ 9d5a4b6 (trozo «navegación»: los
// reencauces toCategories/toArticles/toArticlesOf, mustCategory, reprompt y la
// localización en el catálogo; el viejo es un solo fichero y aquí nace partido, 05 E-13)

// cart_navigation.go son los reencauces de la sub-máquina, el reprompt y la
// localización de lo que el estado dice tener en foco dentro del catálogo.
//
// No exporta nada: lo que promete se ve por Module.Step, y es sobre todo qué pasa
// cuando EL CATÁLOGO CAMBIA BAJO LOS PIES —el dueño quita una categoría o un
// artículo con la conversación viva—. La regla es una: nunca se queda nadie
// atrapado y nunca se pierde lo que el cliente ya dijo. Se sube al nivel más
// cercano que todavía existe, SIN aviso de inválido y con las líneas, `started` y
// la nota del pedido intactas.
//
//   - L2 con la categoría en foco desaparecida ⇒ L1, sea cual sea la entrada.
//   - L3, L3b y L4 con el artículo desaparecido ⇒ L2 de su categoría, o L1 si
//     tampoco está la categoría.
//   - L5: «1» sin categoría ⇒ L1; «0» sin artículo ⇒ L2 de la categoría, o L1; y el
//     inválido re-muestra un continuar NEUTRO («1) Agregar más», sin nombre de
//     categoría).
//   - L6: «2» sin categoría ⇒ L1.
//
// La categoría se localiza por su código y el artículo por su SKU, por igualdad
// EXACTA, sin recortar ni plegar mayúsculas (quien traduce lo que el cliente
// escribe es el pre-resolutor, antes). Un código vacío no localiza nada.
//
// Subir de nivel suelta lo que ya no aplica: volver a L2 suelta el artículo, la
// página y la variante en curso; volver a L1 suelta además la categoría.
//
// El reprompt es UNO para todos los niveles —«Opción no válida. Responde con el
// número de una de las opciones.», una línea en blanco y la pantalla del nivel— y
// es lo ÚNICO que mueve el contador de inválidos (ver Module.Step).

package cart
