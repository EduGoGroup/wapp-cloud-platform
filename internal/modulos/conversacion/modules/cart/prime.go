// Porta internal/flujos/modules/cart/prime.go @ 9d5a4b6

// prime.go implementa la capacidad modules.Primer del carrito (Plan 029 · T8,
// design.md §4.c): al ARRANCAR el flujo por una decisión kind='llm', el runtime
// siembra en Vars los intent_params extraídos por el clasificador (p. ej.
// {producto:"pizza", cantidad:"2"}); el carrito los CONSUME aquí, hace matching
// difuso contra el catálogo del tenant y —si hay un match claro— pre-agrega la línea
// y salta directo al estado de confirmación de ítem (LevelContinue), en vez de
// mostrar el listado de categorías vacío. "el LLM extrae, el código resuelve".
//
// PUREZA (invariante del módulo): sin I/O. El catálogo llega ya resuelto (Prime recibe
// model.Content, igual que Render); el matching es en memoria. Si no hay match o es
// ambiguo, arranca el flujo normal desde categorías SIN inventar nada.
//
// El matching difuso es un PORTE de miniWapp/handlers_negocio.go
// (normalizar/prefijoComun/buscarArticulo) renombrado al estilo del módulo
// (ADR-0004, copia-adaptación).

package cart

import (
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// Prime pre-carga el carrito a partir de los intent_params sembrados en Vars bajo
// modules.VarIntentParams. Del mapa interpreta dos claves y solo dos —`producto` y
// `cantidad`, el vocabulario que fija la config de intents del tenant—; el resto se
// ignora. Lo lee tolerando el tipo nativo (map[string]string, recién sembrado por el
// runtime) y el round-trip JSONB (map[string]any, del que solo cuentan los valores
// string).
//
// handled=false ⇒ el engine hace el Render normal, y el Result es el cero:
//   - sin intent_params: clave ausente, nil, mapa vacío, de otra forma, o un
//     map[string]any sin un solo valor string (no-regresión: arranque de siempre);
//   - catálogo no disponible (catalogo.ParseCatalog falla): se deja el camino de
//     siempre, que muestra «El catálogo no está disponible…». En este caso las Vars
//     NO se tocan y los params NO se consumen aquí.
//
// handled=true ⇒ el módulo CONSUMIÓ los params, UNA sola vez: Result.Vars es una
// COPIA de vars (el mapa recibido no se muta) sin modules.VarIntentParams ni
// modules.VarIntentName, para que no persistan ni se reapliquen en el próximo Step.
// A partir de ahí hay tres desenlaces:
//
//  1. SIN PRODUCTO, SIN MATCH O AMBIGUO ⇒ arranque normal: la pantalla de
//     categorías (L1, página 0, con el tamaño de página del Module), sin estado
//     guardado y SIN efectos. El cart_started lo declarará el primer Step, como en
//     el arranque normal (no-regresión de telemetría).
//
//  2. ARTÍCULO CLARO, VARIANTE NO ("quiero una torta de chocolate", con dos tamaños
//     a precios distintos) ⇒ se PIDE la variante en vez de suponerla: estado en
//     LevelVariant con la categoría y el artículo en foco, la pantalla de variantes,
//     sin línea y SIN efectos. Pre-agregar al precio de referencia sería inventar el
//     precio, que es lo que el contrato v2 prohíbe; y tampoco se tira lo que el
//     cliente ya dijo.
//
//  3. MATCH CLARO ⇒ pre-agrega la línea y salta a la confirmación de ítem: estado en
//     LevelContinue con la categoría y el artículo en foco, `started` y la línea;
//     DOS efectos, en este orden: cart_started {} e item_added {sku, label, qty,
//     unit_price} con la foto privada de líneas (mismo contrato que el add manual:
//     con este efecto el runtime ASEGURA la solicitud "open"). La pantalla antepone
//     QUÉ se agregó a la confirmación de ítem:
//
//     Agregué <qty> × <etiqueta> ($<precio> c/u) a tu pedido.
//
//     Añadido al pedido ✅ …
//
//     Con variante, la línea, su etiqueta, su sku compuesto y su precio son los de
//     la VARIANTE. Un combo es UNA línea a su precio, sin desplegar componentes.
//
// CÓMO CASA. Texto y etiquetas se normalizan (minúsculas y sin los diacríticos
// comunes del español: á é í ó ú ñ) y se comparan palabra a palabra por PREFIJO
// COMÚN: dos palabras de 4 o más BYTES casan si comparten los primeros min(len, 5);
// con una de menos de 4, solo si son iguales. Eso tolera plurales y erratas leves
// ("pizzas de peperoni" → "Pizza pepperoni"). Gana el artículo de TODO el catálogo
// con más palabras en común; con puntuación 0 o EMPATE en la mejor no hay match.
// La variante se elige en una SEGUNDA pasada, contra la etiqueta de la variante y
// no contra la compuesta: "torta de chocolate" puntúa igual en todas las variantes,
// y una pasada única elegiría el tamaño por el cliente.
//
// LA CANTIDAD es un entero >= 1 (con espacios alrededor tolerados); ausente,
// ilegible o < 1 ⇒ 1. El clasificador puede omitirla o extraer basura: el código
// nunca falla por eso.
//
// Si el Module tiene logger, avisa UNA vez por cada campo del catálogo v2 que el
// parseo tolerante descartó, igual que Render (ver WithLogger).
func (m Module) Prime(_ model.Node, content model.Content, vars map[string]any) (modules.Result, bool) {
	panic(pendiente.Implementar("cart.Module.Prime"))
}
