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

import (
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
)

// --- transiciones auxiliares ----------------------------------------------

// toCategories reencauza a L1 conservando las líneas y la solicitud (design.md §9.C:
// "volver" desde artículos sube a categorías con el carrito intacto).
func toCategories(cat catalogo.Catalog, st cartState, size int) (cartState, []string) {
	st.Level = LevelCategories
	st.CatCode = ""
	st.SKU = ""
	st.Page = 0
	st.VariantCode = "" // sin artículo en foco no hay variante en curso
	return st, []string{screenCategories(cat, st, size)}
}

// toArticles reencauza a L2 de la categoría en foco; si ya no existe, cae a L1.
func toArticles(cat catalogo.Catalog, st cartState, size int) (cartState, []string) {
	if category, ok := findCategory(cat, st.CatCode); ok {
		return toArticlesOf(category, st, size)
	}
	return toCategories(cat, st, size)
}

// toArticlesOf reencauza a L2 de una categoría concreta (misma categoría).
func toArticlesOf(category catalogo.Category, st cartState, size int) (cartState, []string) {
	st.Level = LevelArticles
	st.CatCode = category.Code
	st.SKU = ""
	st.Page = 0
	st.VariantCode = "" // se suelta el artículo en foco: la variante muere con él
	return st, []string{screenArticles(category, st, size)}
}

// mustCategory devuelve la categoría si es válida, o una vacía si no (para el
// reprompt de L5 cuando el catálogo cambió: se re-muestra un continuar neutro).
func mustCategory(category catalogo.Category, ok bool) catalogo.Category {
	if ok {
		return category
	}
	return catalogo.Category{}
}

// reprompt re-muestra el nivel actual precedido de un aviso, sin avanzar. DENTRO de
// un evento cuenta los inválidos y, al tercero, ARMA el menú de salida (D-043.10) en
// vez de repreguntar por enésima vez; fuera de un evento repromptea como siempre.
func reprompt(st cartState, screen string) (cartState, []string) {
	if st.inEvent {
		st.Reprompts++
		if st.Reprompts >= modules.MaxReprompts {
			st.Reprompts = 0
			st.exitScreen = screen
			return st, nil // Module.Step traduce exitScreen a la salida.
		}
	}
	return st, []string{"Opción no válida. Responde con el número de una de las opciones.\n\n" + screen}
}

// --- localización en el catálogo ------------------------------------------

func findCategory(cat catalogo.Catalog, code string) (catalogo.Category, bool) {
	if code == "" {
		return catalogo.Category{}, false
	}
	for _, c := range cat.Categories {
		if c.Code == code {
			return c, true
		}
	}
	return catalogo.Category{}, false
}

func findArticle(category catalogo.Category, code string) (catalogo.Article, bool) {
	if code == "" {
		return catalogo.Article{}, false
	}
	for _, a := range category.Items {
		if a.Code == code {
			return a, true
		}
	}
	return catalogo.Article{}, false
}

// locate resuelve la categoría (por código) y el artículo en foco (por SKU) del
// estado. El artículo se guarda por SKU (identificador de negocio estable),
// no por Code, para no depender de la posición en el catálogo.
func locate(cat catalogo.Catalog, catCode, sku string) (catalogo.Category, catalogo.Article, bool) {
	category, ok := findCategory(cat, catCode)
	if !ok {
		return catalogo.Category{}, catalogo.Article{}, false
	}
	for _, a := range category.Items {
		if a.SKU == sku {
			return category, a, true
		}
	}
	return category, catalogo.Article{}, false
}
