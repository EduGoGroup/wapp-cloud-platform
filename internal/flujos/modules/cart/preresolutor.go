// preresolutor.go — LA CASCADA DETERMINISTA, DELANTE DE LA SUB-MÁQUINA
// (Plan 044 · Ola 3.5 · T3.5-1).
//
// ════════════════════════════════════════════════════════════════════════════
// QUÉ PROBLEMA RESUELVE
// ════════════════════════════════════════════════════════════════════════════
//
// Hasta esta tarea el carrito solo entendía CÓDIGOS EXACTOS: findCategory y
// findArticle comparan `c.Code == code` sin normalizar nada, y todo lo demás
// —«hamburgesa», «agrega 2 hamburguesas», «cancelar»— caía en reprompt(). El
// cliente que escribe en vez de teclear un número no se equivocaba: es que el
// carrito solo sabía leer números.
//
// El pre-resolutor traduce lo que el cliente escribió al CÓDIGO CANÓNICO que los
// step* ya entienden, y a partir de ahí no cambia NADA: la sub-máquina recibe "2"
// venga de un teclado numérico o de la palabra «hamburguesa». Por eso no hay una
// sola función step* tocada, ni advance() —el CORAZÓN PURO— tiene una rama nueva.
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 LA REGLA DE ORO: SI NO RESUELVE CON CERTEZA, NO TOCA NADA
// ════════════════════════════════════════════════════════════════════════════
//
// Todo lo que este fichero no resuelve sale por donde entró —el input INTACTO— y
// el flujo se comporta EXACTAMENTE como el día antes de esta tarea. Las tres
// puertas que garantizan la regresión cero, en este orden:
//
//  1. Un nivel EXCLUIDO no llega ni a mirar la entrada (opcionesDelNivel ⇒ nil).
//  2. Un CÓDIGO —cualquier cosa puramente numérica, y cualquier código del nivel—
//     se devuelve intacto SIN pasar por la cascada. Esto no es una optimización:
//     es lo que impide que el "3" que el cliente teclea para paginar acabe casando
//     por fuzzy con una etiqueta. Un número que el usuario escribe es un índice de
//     pantalla, jamás un nombre.
//  3. Un empate entre DOS opciones distintas no resuelve. «torta» con «Torta de
//     chocolate» y «Torta de vainilla» en pantalla no es una elección: es una
//     pregunta, y adivinar metería el artículo equivocado en el pedido.
//
// ════════════════════════════════════════════════════════════════════════════
// PUREZA (invariante del módulo, design.md §2/§4.1)
// ════════════════════════════════════════════════════════════════════════════
//
// La cascada es `Exact → Fuzzy(0,85)` y se construye YA `.Deterministic()`, o sea
// SIN zona gris — igual que stages.CascadaPorDefecto (internal/intake/stages/
// match_cascada.go:62). No es una promesa: es que el campo grayZone queda a nil y
// Cascade.Compare no tiene a quién llamar. CERO I/O, cero red, cero LLM: el módulo
// sigue siendo puro y el turno del cliente no puede colgarse esperando a nadie.
//
// 🔴 EL UMBRAL 0,85 NO SE TOCA (D-044.45). `NewFuzzy(0)` cae al
// DefaultFuzzyThreshold del módulo. Es un umbral RELATIVO (`1 − dist/len`), así
// que una errata solo se perdona a partir de 7 runas: «torta», «pizza» y «ñoquis»
// NO la perdonan, y eso está decidido y aceptado. Bajarlo a 0,80 casaría
// «torta» con «tarta», que es meter otro artículo en el pedido de alguien.
//
// ════════════════════════════════════════════════════════════════════════════
// CÓMO CASA UNA FRASE CONTRA UNA ETIQUETA (el porqué de los PREFIJOS)
// ════════════════════════════════════════════════════════════════════════════
//
// La cascada compara PARES de textos, no una frase contra una lista. Comparar
// «agrega 2 hamburguesas» entero contra «Hamburguesa» no casa ni de lejos, así que
// aquí se compara por VENTANAS de tokens: para una etiqueta de k tokens se prueban
// sus PREFIJOS de 1..k tokens contra cada ventana contigua de la entrada del mismo
// tamaño.
//
// Prefijos y no cualquier sub-conjunto, y la razón es lingüística, no técnica: en
// castellano el sintagma nominal es de núcleo inicial y la gente abrevia POR LA
// DERECHA —«torta» por «Torta de chocolate», «cancelar» por «Cancelar pedido»—,
// nunca por la izquierda. Aceptar cualquier token de la etiqueta haría casar
// «pedido» con tres opciones a la vez (finalizar/cancelar/indicación) y sería un
// empate en el mejor caso y un acierto por azar en el peor.
//
// Consecuencia CONOCIDA y aceptada: «finalizar» en el resumen no casa nada, porque
// ahí la opción se llama «Confirmar y finalizar» y su prefijo de 1 token es
// «confirmar». Eso no es un fallo del pre-resolutor: es exactamente el material
// del turno LLM (T3.5-2), y el contador `ninguno` de la telemetría es el que dirá
// cuánto pesa de verdad.
//
// Desempate: gana la coincidencia que CONSUME MÁS TOKENS de la entrada y, a
// igualdad, la de más confianza. Una coincidencia larga es más evidencia que una
// corta: «solo para 1» casa la opción 2 entera (3 tokens) y no la opción 1 por su
// «para» suelto (1 token).
//
// COSTE: O(candidatos × tokens_de_la_etiqueta × tokens_de_la_entrada) distancias
// de edición sobre textos cortos. Con una categoría de 200 artículos y una frase
// de 8 tokens son ~5k distancias de ~15 runas: milisegundos, una vez por mensaje
// del cliente. Lo que NO está acotado por naturaleza es la entrada —WhatsApp
// admite 4.096 caracteres—, y por eso maxTokensEntrada existe: una parrafada no es
// una elección de menú, es prosa, y la prosa es del LLM.
package cart

import (
	"context"
	"strconv"
	"strings"

	"github.com/EduGoGroup/wapp-shared/textmatch"
)

// escalonNinguno es el valor de telemetría de «la cascada corrió y no resolvió».
// Los otros dos valores posibles NO se declaran aquí a propósito: salen de
// textmatch.Result.Strategy ("exact"/"fuzzy"), que es la PROCEDENCIA que la
// propia estrategia declara. Copiarlos aquí sería una segunda verdad que el día
// que la cascada gane un escalón se quedaría desincronizada en silencio.
const escalonNinguno = "ninguno"

// maxTokensEntrada es el techo de tokens que se consideran una posible elección
// de opción. Por encima, el pre-resolutor no mira: es prosa, y el coste de
// barrerla contra el catálogo crece con el producto de las dos longitudes.
const maxTokensEntrada = 16

// cascadaCarrito es el comparador determinista del pre-resolutor. Es inmutable y
// sin estado (dos structs de valor detrás de un puntero), así que vive a nivel de
// paquete en vez de reconstruirse en cada mensaje.
var cascadaCarrito = textmatch.NewCascade(textmatch.Exact{}, textmatch.NewFuzzy(0)).Deterministic()

// opcion es UNA elección del nivel actual: el CÓDIGO que la sub-máquina ya
// entiende y la ETIQUETA que el cliente tiene delante en la pantalla. La cascada
// compara contra la etiqueta y lo que se devuelve es el código; ese es todo el
// truco de que ningún step* se entere de nada.
type opcion struct {
	codigo   string
	etiqueta string
}

// veredicto es la mejor coincidencia encontrada, con lo necesario para desempatar
// (tokens consumidos, confianza) y para la telemetría (escalón). codigo vacío ⇒
// no hay coincidencia.
type veredicto struct {
	codigo    string
	escalon   string
	tokens    int
	confianza float64
}

// preresolve es el pre-resolutor DEL MÓDULO: la función pura de abajo más el
// aviso al observador. Se separan para que la lógica se pueda probar sin montar
// un Module y para que el único sitio que conoce el hook sea este.
//
// Se cuenta lo que la CASCADA hizo, no lo que el turno hizo: un mensaje resuelto
// por código exacto (el cliente tecleó "2") o un nivel excluido no dejan rastro
// aquí, porque en ninguno de los dos corrió cascada alguna. Quien lea la métrica
// tiene que saberlo o leerá el volumen al revés.
// Devuelve el mismo par que la función pura —(salida, escalón)— porque el
// llamante necesita las DOS cosas desde T3.5-2: la salida para seguir, y el
// escalón para saber si la cascada resolvió o si hay que elevar una consulta
// (consulta.go). Distinguirlo por «¿cambió el texto?» funcionaría hoy y sería una
// deducción frágil: el escalón es la respuesta explícita.
func (m Module) preresolve(cat Catalog, st cartState, input string) (string, string) {
	salida, escalon := preresolve(cat, st, input)
	if escalon != "" && m.onMatch != nil {
		m.onMatch(escalon, st.Level)
	}
	return salida, escalon
}

// preresolve traduce la entrada del cliente al código canónico del nivel.
//
// Devuelve (salida, escalón). El escalón VACÍO significa que la cascada ni
// siquiera corrió —nivel excluido, entrada vacía, un código, prosa larga— y por
// tanto no hay nada que contar: la telemetría mide la cascada, no los turnos.
func preresolve(cat Catalog, st cartState, input string) (string, string) {
	in := strings.TrimSpace(input)
	if in == "" {
		return input, ""
	}
	opciones := opcionesDelNivel(cat, st)
	if len(opciones) == 0 {
		return input, "" // Nivel excluido (o sin opciones que ofrecer).
	}
	// PUERTA 2 de la regla de oro. Lo numérico es del camino de siempre —códigos
	// del catálogo, posiciones de variante, "0"/"3"/"9" de control y el "Más ▾"
	// dinámico, que por ser dinámico NO está en opciones—; y un código no numérico
	// (un catálogo puede traerlos) tampoco se toca, porque su dueño ya lo resuelve
	// por igualdad exacta.
	if esNumero(in) || esCodigoDelNivel(opciones, in) {
		return input, ""
	}
	tokens := textmatch.SplitTokens(in)
	if len(tokens) == 0 || len(tokens) > maxTokensEntrada {
		return input, ""
	}
	mejor, ok := mejorOpcion(opciones, tokens)
	if !ok {
		return input, escalonNinguno
	}
	return mejor.codigo, mejor.escalon
}

// opcionesDelNivel enumera las opciones sobre las que la cascada puede opinar en
// el nivel actual, o nil si el nivel NO admite pre-resolución.
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 LOS TRES NIVELES QUE NO APARECEN AQUÍ, Y POR QUÉ ES UN REQUISITO
// ════════════════════════════════════════════════════════════════════════════
//
// item_note, order_note y buyer_data NO son elecciones de opción: son TEXTO LIBRE
// del cliente. Y buyer_data en particular recoge DATOS PERSONALES —nombre, RUT,
// dirección— que salen del módulo dentro de un efecto privado y acaban CIFRADOS en
// intake_buyer_data, precisamente para que no queden en claro en ningún sitio.
// Pasarlos por una cascada de similitud sería:
//
//   - manosear un dato personal para adivinar a qué opción «se parece», cuando no
//     hay opción ninguna a la que deba parecerse, y
//   - abrir la puerta a que ese texto acabe en una etiqueta de telemetría el día
//     que alguien añada una etiqueta de más.
//
// No es una omisión ni una optimización: es privacidad, y lo fija un test.
//
// quantity queda fuera por otra razón, esta de diseño: hoy es strconv.Atoi
// estricto, y una cascada DETERMINISTA no sabe traducir «dos» a 2 —eso es
// aritmética del lenguaje, no similitud ortográfica—. Inventar aquí un parseo de
// números en palabras sería construir media función del turno LLM (T3.5-2) en el
// sitio equivocado.
//
// closed/cancelled son terminales: la entrada se ignora.
//
// Los DOS `default` de abajo son FAIL-CLOSED a propósito: un nivel nuevo que
// alguien añada a state.go NO entra en la cascada hasta que lo declare de forma
// explícita en una de las dos mitades. La ausencia nunca puede ser un sí.
//
// El reparto en dos mitades no es estético: separa los niveles cuyas opciones las
// pone el CATÁLOGO DEL TENANT —y que por tanto pueden quedarse sin opciones si el
// catálogo cambió bajo los pies— de los que llevan un menú FIJO impreso en
// pantalla. Y de paso deja cada mitad por debajo del techo de complejidad.
func opcionesDelNivel(cat Catalog, st cartState) []opcion {
	if ops := opcionesDeCatalogo(cat, st); ops != nil {
		return ops
	}
	return opcionesDeMenu(cat, st)
}

// opcionesDeCatalogo cubre los niveles cuyas opciones salen del catálogo del
// tenant. nil ⇒ el nivel no es de catálogo, O el catálogo ya no puede resolverlo
// (categoría/artículo desaparecidos): en los dos casos decide el step*, no esto.
func opcionesDeCatalogo(cat Catalog, st cartState) []opcion {
	switch st.Level {
	case LevelCategories:
		// Todas las categorías, no solo las de la página en pantalla: findCategory
		// busca en el catálogo entero, así que nombrar una categoría de la página 3
		// funciona y ahorra dos «Más ▾».
		//
		// 🔴 EL PROPIO «Más ▾» NO ES CANDIDATO, y no por olvido. Su rótulo normaliza
		// a «mas», que es una palabra corrientísima dentro de una frase: «mas
		// hamburguesas» casaría «mas» por EXACT (confianza 1,0) y le ganaría a
		// «hamburguesas» por fuzzy (~0,92), mandando al cliente a la página
		// siguiente en vez de a su artículo. Paginar se seguirá haciendo con su
		// número, que es lo que la pantalla ofrece.
		out := make([]opcion, 0, len(cat.Categories))
		for _, c := range cat.Categories {
			out = append(out, opcion{codigo: c.Code, etiqueta: c.Label})
		}
		return out
	case LevelArticles:
		category, ok := findCategory(cat, st.CatCode)
		if !ok {
			return nil // El catálogo cambió bajo los pies: que decida stepArticles.
		}
		out := make([]opcion, 0, len(category.Items)+1)
		for _, a := range category.Items {
			out = append(out, opcion{codigo: a.Code, etiqueta: a.Label})
		}
		return append(out, opcion{codigo: codeVolver, etiqueta: "Volver"})
	case LevelVariant:
		_, a, ok := locate(cat, st.CatCode, st.SKU)
		if !ok || !a.HasVariants() {
			return nil
		}
		// La lista de variantes va numerada POR POSICIÓN (screenVariants), no por
		// Variant.Code: el código que hay que devolver es el ordinal de pantalla, que
		// es lo que variantByPosition sabe resolver.
		out := make([]opcion, 0, len(a.Variants)+1)
		for i, v := range a.Variants {
			out = append(out, opcion{codigo: strconv.Itoa(i + 1), etiqueta: v.Label})
		}
		return append(out, opcion{codigo: codeVolver, etiqueta: "Volver"})
	default:
		return nil
	}
}

// opcionesDeMenu cubre los niveles con menú FIJO —los rótulos que la pantalla
// imprime siempre igual—, más el `default` fail-closed de toda la función. Ver la
// nota grande de opcionesDelNivel: aquí es donde NO están item_note, order_note,
// buyer_data ni quantity, y esa ausencia es el requisito.
func opcionesDeMenu(cat Catalog, st cartState) []opcion {
	switch st.Level {
	case LevelArticle:
		if _, _, ok := locate(cat, st.CatCode, st.SKU); !ok {
			return nil
		}
		return []opcion{
			{codigo: "1", etiqueta: "Ver descripción"},
			{codigo: "2", etiqueta: "Agregar al pedido"},
			{codigo: codeVolver, etiqueta: "Volver"},
		}
	case LevelContinue:
		// La opción 1 se imprime como «Agregar más de <categoría>»; aquí va sin la
		// parte variable. No es inventarse una etiqueta: es quedarse con el trozo
		// ESTABLE del rótulo, el que no depende del catálogo del tenant.
		return []opcion{
			{codigo: "1", etiqueta: "Agregar más"},
			{codigo: "2", etiqueta: "Finalizar pedido"},
			{codigo: codeIndicacion, etiqueta: "Indicación para este artículo"},
			{codigo: codeCancelar, etiqueta: "Cancelar pedido"},
			{codigo: codeVolver, etiqueta: "Volver"},
		}
	case LevelSummary:
		return []opcion{
			{codigo: "1", etiqueta: "Confirmar y finalizar"},
			{codigo: "2", etiqueta: "Seguir agregando"},
			{codigo: codeIndicacion, etiqueta: "Indicación para todo el pedido"},
			{codigo: codeCancelar, etiqueta: "Cancelar pedido"},
		}
	case LevelItemNoteScope:
		if len(st.Lines) == 0 {
			return nil
		}
		n := strconv.Itoa(st.Lines[len(st.Lines)-1].Qty)
		return []opcion{
			{codigo: "1", etiqueta: "Para las " + n},
			{codigo: "2", etiqueta: "Solo para 1"},
			{codigo: codeVolver, etiqueta: "Volver sin indicación"},
		}
	default:
		// 🔴 item_note, order_note, buyer_data, quantity, closed, cancelled y
		// cualquier nivel FUTURO. Ver la nota grande de esta función.
		return nil
	}
}

// mejorOpcion elige la ÚNICA opción que la entrada resuelve, o declara que no hay
// certeza. El empate entre códigos distintos NO resuelve: ver la regla de oro.
func mejorOpcion(opciones []opcion, tokens []string) (veredicto, bool) {
	var mejor veredicto
	empatada := false
	for _, o := range opciones {
		v, ok := comparaOpcion(o, tokens)
		if !ok {
			continue
		}
		switch comparaVeredictos(v, mejor) {
		case 1:
			mejor, empatada = v, false
		case 0:
			// Empate real. Con una etiqueta por opción y códigos únicos por nivel
			// esto es siempre ambigüedad, pero se comprueba el código igualmente:
			// dos rótulos que apuntaran al mismo sitio no serían una duda.
			if v.codigo != mejor.codigo {
				empatada = true
			}
		}
	}
	if mejor.codigo == "" || empatada {
		return veredicto{}, false
	}
	return mejor, true
}

// comparaOpcion busca la mejor coincidencia de UNA opción contra la entrada:
// cada prefijo de 1..k tokens de la etiqueta contra cada ventana contigua de la
// entrada del mismo tamaño. Ver la nota de cabecera para el porqué de los
// prefijos y del desempate.
func comparaOpcion(o opcion, tokens []string) (veredicto, bool) {
	etiqueta := textmatch.SplitTokens(o.etiqueta)
	if len(etiqueta) == 0 {
		return veredicto{}, false // Etiqueta vacía o solo puntuación: nada que casar.
	}
	var mejor veredicto
	for j := 1; j <= len(etiqueta) && j <= len(tokens); j++ {
		prefijo := strings.Join(etiqueta[:j], " ")
		for i := 0; i+j <= len(tokens); i++ {
			ventana := strings.Join(tokens[i:i+j], " ")
			if esNumero(ventana) {
				// El «2» de «agrega 2 hamburguesas» es una cantidad, no un nombre.
				// Dejarlo competir contra las etiquetas es justo el defecto que la
				// puerta 2 evita en la entrada entera.
				continue
			}
			// context.Background() y no un ctx heredado porque no hay ninguno que
			// heredar: modules.Module.Step no recibe contexto (el módulo es PURO y
			// no hace I/O), y la cascada determinista jamás lo consulta —ni cancela,
			// ni vence, ni bloquea—. El parámetro existe en la interfaz de textmatch
			// para las estrategias que SÍ salen al mundo (la zona gris), y aquí no
			// hay ninguna.
			r, err := cascadaCarrito.Compare(context.Background(), prefijo, ventana)
			if err != nil || r.Outcome != textmatch.OutcomeMatch {
				// Una cascada determinista no devuelve error nunca (Exact y Fuzzy no
				// fallan); si algún día lo hiciera, el fallo se trata como «no casa»
				// y el turno sigue por el camino de siempre. Nada se rompe.
				continue
			}
			v := veredicto{codigo: o.codigo, escalon: r.Strategy, tokens: j, confianza: r.Confidence}
			if comparaVeredictos(v, mejor) > 0 {
				mejor = v
			}
		}
	}
	return mejor, mejor.codigo != ""
}

// comparaVeredictos ordena dos coincidencias: primero por tokens consumidos,
// luego por confianza. Devuelve 1 si a es mejor, -1 si b lo es, 0 si empatan.
// El veredicto CERO (sin código) pierde contra cualquiera, porque consume 0
// tokens y toda coincidencia real consume al menos 1.
func comparaVeredictos(a, b veredicto) int {
	switch {
	case a.tokens != b.tokens:
		return signo(a.tokens - b.tokens)
	case a.confianza != b.confianza:
		if a.confianza > b.confianza {
			return 1
		}
		return -1
	default:
		return 0
	}
}

func signo(n int) int {
	if n > 0 {
		return 1
	}
	return -1
}

// esCodigoDelNivel comprueba si la entrada YA es uno de los códigos que el nivel
// acepta, para devolverla intacta sin pasar por la cascada.
func esCodigoDelNivel(opciones []opcion, in string) bool {
	for _, o := range opciones {
		if o.codigo == in {
			return true
		}
	}
	return false
}

// esNumero informa si el texto es una tira de dígitos ASCII. No usa strconv.Atoi
// a propósito: Atoi acepta signo ("-3") y desborda con tiras largas, y aquí la
// pregunta no es «cuánto vale» sino «esto lo tecleó como número».
func esNumero(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
