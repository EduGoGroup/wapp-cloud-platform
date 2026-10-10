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

import (
	"strconv"
	"strings"

	"github.com/EduGoGroup/wapp-shared/textmatch"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
)

// minChunksToSplit es cuántas PETICIONES tiene que haber para que este camino se
// active. Dos, porque con una sola el camino de siempre (cascada → consulta) hace
// exactamente lo mismo y mejor: este fichero existe para lo que el turno de un solo
// código no sabe hacer, que es meter VARIOS productos de una vez. Con una petición
// el troceado se aparta y no se nota que existe.
const minChunksToSplit = 2

// chunkSeparators son los cortes por los que se parte el turno, del más fuerte al
// más débil. El salto de línea va primero porque el lote agrupado del Edge concatena
// los mensajes sueltos del cliente, y cada mensaje suyo es ya una unidad de sentido
// —justo la que la agrupación fundía—.
//
// La «y» y la «e» van CON ESPACIOS a los dos lados a propósito: sin ellos partirían
// «yogur» y «empanadas» por la mitad. Es la misma disciplina de la puerta 2 del
// pre-resolutor: un separador que casa dentro de una palabra no es un separador.
var chunkSeparators = []string{"\n", ",", ";", "+", " y ", " e "}

// numberWords es la tabla de cantidades escritas. Es LITERAL y corta a
// propósito: doce entradas cubren lo que una persona pide por WhatsApp, y todo lo
// que no esté aquí cae al camino de siempre (cantidad 1 si el producto casó, o el
// trozo descartado si no). Una tabla más larga no compra nada medible y cada entrada
// de más es una forma nueva de casar algo que no era una cantidad.
//
// «docena» y «par» están porque el few-shot del turno acotado ya las trata como
// cantidades (turnoacotado/prompt.go): si el modelo las entiende y Go no, la misma
// frase daría resultados distintos según qué peldaño la atendiera.
var numberWords = map[string]int{
	"un": 1, "uno": 1, "una": 1, "par": 2, "dos": 2, "tres": 3, "cuatro": 4,
	"cinco": 5, "seis": 6, "siete": 7, "ocho": 8, "nueve": 9, "diez": 10,
	"once": 11, "doce": 12, "docena": 12,
}

// request es UN trozo ya interpretado por el lado determinista: qué pidió (tokens
// sin la cantidad), cuántas, y —si la cascada lo supo— a qué artículo del catálogo
// apunta. `idx` < 0 significa «la cascada no lo casó»: ese es, y solo ese, el trozo
// que puede llegar a costar una llamada al modelo.
type request struct {
	tokens   []string
	qty      int
	idx      int
	explicit bool // la cantidad venía escrita (dígitos o palabra), no supuesta
}

// candidate es un artículo del catálogo ENTERO con su categoría, listo para casar.
// El troceado busca en todo el catálogo y no en el nivel actual porque el cliente
// nombró un PRODUCTO, no una opción de la pantalla que tiene delante.
type candidate struct {
	category catalogo.Category
	article  catalogo.Article
}

// chunked es el camino completo, en sus DOS pasadas, y devuelve ok=false siempre que
// esto no sea asunto suyo — y entonces Step sigue byte a byte como el día antes.
//
// 🔴 VA POR ENCIMA DE TODA MUTACIÓN de Step, por el mismo motivo que la consulta
// (ver la nota grande de Step): la primera pasada puede terminar devolviendo una
// PETICIÓN que el engine descarta entera, y si por el camino hubiera declarado
// cart_started, la segunda pasada lo declararía otra vez.
func (m Module) chunked(cat catalogo.Catalog, st cartState, vars map[string]any, input string) (modules.Result, bool) {
	if st.Level != LevelCategories {
		return modules.Result{}, false
	}
	requests, cands := m.requestsOf(cat, input)
	if len(requests) < minChunksToSplit {
		return modules.Result{}, false
	}
	if v, found := modules.VerdictFrom(vars); found {
		// 2.ª pasada: el engine ya preguntó. Se vuelve a trocear porque trocear es una
		// función PURA de la entrada —mismo texto, mismos trozos, mismo orden—, que es
		// la misma disciplina con la que applyVerdict re-ejecuta consultable.
		applyCodes(requests, v.Codes)
		return m.recompose(st, vars, requests, cands)
	}
	if pending := unmatched(requests); len(pending) > 0 {
		return modules.Result{
			Vars: vars,
			Query: &modules.Query{
				Class:   modules.QueryClassOption,
				Level:   st.Level,
				Text:    strings.TrimSpace(input),
				Chunks:  pending,
				Options: candidateOptions(cands),
			},
		}, true
	}
	// La cascada resolvió TODO: ni una llamada. Es el caso de los dos fixtures del
	// criterio y es el caso bueno, no una excepción.
	return m.recompose(st, vars, requests, cands)
}

// requestsOf descompone el turno y deja cada trozo interpretado hasta donde llega
// el determinismo. Devuelve además la lista de candidatos, que es la MISMA con la que
// se casó y por tanto la única contra la que los códigos del veredicto pueden
// traducirse sin ambigüedad.
func (m Module) requestsOf(cat catalogo.Catalog, input string) ([]request, []candidate) {
	in := strings.TrimSpace(input)
	if in == "" || isNumber(in) {
		return nil, nil
	}
	// El mismo techo de prosa del pre-resolutor, y por el mismo motivo: WhatsApp
	// admite 4.096 caracteres y una parrafada no es un pedido. De paso acota cuántos
	// trozos pueden salir —16 tokens no dan para más de ocho—, así que el número de
	// trozos no necesita un tope propio: el que cuesta dinero es el de LLAMADAS, y ese
	// lo pone el resolutor (turnoacotado.MaxLlamadasPorTurno).
	if n := len(textmatch.SplitTokens(in)); n == 0 || n > maxInputTokens {
		return nil, nil
	}
	// 🔴 EL CORTE BARATO VA ANTES DE APLANAR EL CATÁLOGO, y no es micro-optimización:
	// esto corre en TODOS los turnos del nivel de categorías, y casar contra el
	// catálogo ENTERO es mucho más caro que casar contra la lista de categorías que el
	// pre-resolutor ya hacía. Un turno de un solo trozo —el 99 %— sale por aquí sin
	// haber tocado un solo artículo.
	chunks := splitChunks(in)
	if len(chunks) < minChunksToSplit {
		return nil, nil
	}
	cands := candidatesOf(cat)
	if len(cands) == 0 {
		return nil, nil
	}
	options := internalOptions(cands)
	var out []request
	for _, chunk := range chunks {
		p, ok := interpret(chunk, options)
		if ok {
			out = append(out, p)
		}
	}
	return out, cands
}

// interpret convierte UN trozo en una petición, o dice que no lo es. Aquí vive el
// filtro de las dos condiciones de la cabecera.
func interpret(chunk string, options []option) (request, bool) {
	tokens := textmatch.SplitTokens(chunk)
	if len(tokens) == 0 {
		return request{}, false
	}
	qty, rest, explicit := quantityOf(tokens)
	if len(rest) == 0 {
		// Un trozo que era SOLO una cantidad («2», «un par») no nombra nada. No es una
		// petición ni una pregunta: es un número suelto que el camino de siempre ya
		// sabe tratar (la puerta 2 del pre-resolutor).
		return request{}, false
	}
	idx := -1
	if v, ok := bestOption(options, rest); ok {
		// El código de una opción interna es su POSICIÓN escrita con Itoa
		// (internalOptions), así que este Atoi no puede fallar. Se comprueba igual:
		// el día que alguien cambie ese código por otra cosa, el troceado tiene que
		// dejar la petición sin casar —y bajarla al modelo— en vez de meter el índice
		// cero en el pedido de alguien.
		if n, err := strconv.Atoi(v.code); err == nil {
			idx = n
		}
	}
	if idx < 0 && !explicit {
		// Ni la cascada lo casa ni trae cantidad: es ruido («hola», «para llevar»). Se
		// descarta SIN gastar una llamada, que es la mitad del valor de este filtro.
		return request{}, false
	}
	return request{tokens: rest, qty: qty, idx: idx, explicit: explicit}, true
}

// splitChunks parte el turno por los separadores, del más fuerte al más débil, y
// devuelve los trozos no vacíos en el ORDEN en que el cliente los escribió. Es pura,
// sin catálogo y sin estado: es la mitad «Go descompone» del título del fichero, y la
// que se puede probar sin un modelo delante.
func splitChunks(in string) []string {
	chunks := []string{in}
	for _, sep := range chunkSeparators {
		next := make([]string, 0, len(chunks))
		for _, t := range chunks {
			next = append(next, strings.Split(t, sep)...)
		}
		chunks = next
	}
	out := make([]string, 0, len(chunks))
	for _, t := range chunks {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// quantityOf extrae la cantidad del trozo y devuelve el RESTO de los tokens, la
// cantidad y si venía escrita. Quitar el token de la cantidad no es cosmético: deja
// que la cascada compare solo contra lo que nombra el producto, y evita que un «una»
// suelto compita por fuzzy contra una etiqueta corta.
//
// Gana el PRIMER número del trozo: en castellano la cantidad precede al producto
// («2 pizzas»), y si hubiera otro número más adelante suele ser parte del nombre
// («2 empanadas de 3 quesos»), no una segunda cantidad.
//
// ⚠️ LA EXCEPCIÓN DEL COMPUESTO: «un par» y «una docena» son DOS tokens que nombran
// UNA cantidad, y quedarse con el primero daría 1 donde la persona dijo 2 o 12. No es
// un detalle de estilo: el few-shot del turno acotado ya trata «dame un par» como 2
// (turnoacotado/prompt.go), así que sin esta regla la MISMA frase daría resultados
// distintos según qué peldaño la atendiera — el escalón determinista y el del modelo
// tienen que estar de acuerdo o el sistema contesta según lo cargado que esté el Edge.
func quantityOf(tokens []string) (qty int, rest []string, explicit bool) {
	qty, explicit = 1, false
	rest = make([]string, 0, len(tokens))
	for i := 0; i < len(tokens); i++ {
		if explicit {
			rest = append(rest, tokens[i])
			continue
		}
		n, ok := numericValue(tokens[i])
		if !ok || n < 1 {
			rest = append(rest, tokens[i])
			continue
		}
		if n == 1 && i+1 < len(tokens) {
			// «un par», «una docena»: el artículo indefinido no es la cantidad, es el
			// determinante de la palabra que sí lo es.
			if m, ok := numericValue(tokens[i+1]); ok && m > 1 {
				n, i = m, i+1
			}
		}
		qty, explicit = n, true
	}
	return qty, rest, explicit
}

// numericValue lee un token como cantidad: dígitos o palabra de la tabla.
func numericValue(t string) (int, bool) {
	if isNumber(t) {
		n, err := strconv.Atoi(t)
		// Un desbordamiento («99999999999999999999») no es una cantidad: se ignora y el
		// token se queda como parte del nombre, que es lo conservador.
		return n, err == nil
	}
	n, ok := numberWords[strings.ToLower(t)]
	return n, ok
}

// candidatesOf aplana el catálogo entero a la lista contra la que se casa.
func candidatesOf(cat catalogo.Catalog) []candidate {
	var out []candidate
	for _, c := range cat.Categories {
		for _, a := range c.Items {
			out = append(out, candidate{category: c, article: a})
		}
	}
	return out
}

// internalOptions es la lista para la CASCADA: el código es la POSICIÓN en cands,
// no el `Code` del artículo. La posición es única por construcción; el `Code` es
// único dentro de su categoría y aquí se mezclan todas.
func internalOptions(cands []candidate) []option {
	out := make([]option, 0, len(cands))
	for i, c := range cands {
		out = append(out, option{code: strconv.Itoa(i), label: c.article.Label})
	}
	return out
}

// candidateOptions es la MISMA lista pero para el modelo, y por eso lleva
// etiquetas: al modelo nunca se le enseñan códigos (turnoacotado/prompt.go). Lo que
// vuelve es una posición de esta lista traducida a su código en Go, y el código es
// el índice — o sea que las dos listas son la misma y no pueden desalinearse.
func candidateOptions(cands []candidate) []modules.QueryOption {
	out := make([]modules.QueryOption, 0, len(cands))
	for i, c := range cands {
		out = append(out, modules.QueryOption{Code: strconv.Itoa(i), Label: c.article.Label})
	}
	return out
}

// unmatched devuelve el TEXTO de los trozos que la cascada no resolvió, en orden. Es
// justo lo que viaja en Query.Chunks, y su longitud es el número máximo de
// llamadas que este turno puede llegar a hacer.
func unmatched(ps []request) []string {
	var out []string
	for _, p := range ps {
		if p.idx < 0 {
			out = append(out, strings.Join(p.tokens, " "))
		}
	}
	return out
}

// applyCodes vuelca el veredicto sobre las peticiones que quedaron sin casar,
// EN EL MISMO ORDEN en que se pidieron. Un código vacío, ilegible o fuera de la lista
// de candidatos deja la petición sin resolver: es la misma aduana que admissibleCode
// (consulta.go) y por el mismo motivo — el resolutor es un modelo y puede inventar.
func applyCodes(ps []request, codes []string) {
	i := 0
	for k := range ps {
		if ps[k].idx >= 0 {
			continue
		}
		if i < len(codes) {
			if n, err := strconv.Atoi(codes[i]); err == nil && n >= 0 {
				ps[k].idx = n
			}
		}
		i++
	}
}

// recompose es la mitad «Go recompone»: convierte las peticiones resueltas en
// líneas del pedido y deja la conversación en la confirmación de ítem, con la MISMA
// forma que primeAdd (Plan 029) y los mismos efectos que el add manual.
//
// Devuelve ok=false si al final no entró NADA: entonces no hay pedido que recomponer
// y el turno sigue por el camino de siempre, que repromptea como cualquier otro día.
func (m Module) recompose(st cartState, vars map[string]any, ps []request, cands []candidate) (modules.Result, bool) {
	out := modules.CloneVars(vars)
	lines := cloneLines(st.Lines)
	effects := make([]modules.Effect, 0, len(ps)+1)
	if !st.Started {
		st.Started = true
		effects = append(effects, event(EffectCartStarted, map[string]any{}))
	}
	var added []cartLine
	var last candidate
	left := 0
	for _, p := range ps {
		c, ok := lineOf(cands, p)
		if !ok {
			left++
			continue
		}
		line := newLine(c.article, catalogo.Variant{}, false, p.qty)
		lines = append(lines, line)
		added = append(added, line)
		last = c
		effects = append(effects, withLineSnapshot(event(EffectItemAdded, map[string]any{
			"sku": line.SKU, "label": line.Label, "qty": line.Qty, "unit_price": line.UnitPrice,
		}), lines))
	}
	if len(added) == 0 {
		return modules.Result{}, false
	}
	m.observeChunks(len(added), left, st.Level)

	st.Level = LevelContinue
	st.CatCode = last.category.Code
	st.SKU = last.article.SKU
	st.Page = 0
	st.VariantCode = ""
	st.Lines = lines
	// El contador de inválidos se reinicia igual que cuando advance() acepta una
	// entrada: este turno FUE válido, y de los más válidos que hay.
	st.Reprompts, st.RepromptsEvent = 0, ""
	storeState(out, st)
	return modules.Result{
		Vars:    out,
		Outputs: []string{chunkScreen(added, last.category, left)},
		Effects: effects,
	}, true
}

// lineOf traduce una petición resuelta a su candidato, o dice que no se agrega.
//
// El artículo CON VARIANTES se queda fuera aquí, y es la puerta que impide inventar
// un precio: ver la cabecera. Es la misma decisión que primeAskVariant toma para una
// sola línea, con la única respuesta posible cuando hay N.
func lineOf(cands []candidate, p request) (candidate, bool) {
	if p.idx < 0 || p.idx >= len(cands) {
		return candidate{}, false
	}
	c := cands[p.idx]
	if c.article.HasVariants() {
		return candidate{}, false
	}
	return c, true
}

// chunkScreen antecede a la confirmación de ítem con el detalle de lo que entró
// —y, si algo se quedó fuera, con CUÁNTO se quedó fuera—.
//
// 🔴 NO SE LE DEVUELVE AL CLIENTE EL TEXTO QUE NO SE ENTENDIÓ, y no es por
// privacidad (la pantalla va a la persona que lo escribió) sino porque repetirle sus
// propias palabras al lado de un «no pude» suena a reproche y no le dice qué hacer.
// El número y la salida —elegir del menú— sí.
func chunkScreen(lines []cartLine, category catalogo.Category, left int) string {
	var b strings.Builder
	b.WriteString("Agregué a tu pedido:")
	for _, l := range lines {
		b.WriteString("\n• " + strconv.Itoa(l.Qty) + " × " + l.Label + " (" + money(l.UnitPrice) + " c/u)")
	}
	if left > 0 {
		b.WriteString("\n\nNo pude identificar ")
		if left == 1 {
			b.WriteString("1 producto más")
		} else {
			b.WriteString(strconv.Itoa(left) + " productos más")
		}
		b.WriteString(" de tu mensaje; podés agregarlo eligiendo del menú.")
	}
	b.WriteString("\n\n" + screenContinue(category))
	return b.String()
}

// observeChunks cuenta el desenlace por el MISMO hook que la cascada (WithMatchHook),
// con los escalones de abajo. No se abre un segundo canal de telemetría para esto:
// quien lee `wapp_cart_match_total` está leyendo «cómo se interpretó lo que el cliente
// escribió», y el troceado es una respuesta más a esa pregunta.
//
// 🔴 CERO TEXTO DEL CLIENTE: las dos etiquetas son el escalón (constantes de abajo) y
// el nivel (vocabulario cerrado de state.go). Es la misma regla del hook original.
func (m Module) observeChunks(added, left int, level string) {
	if m.onMatch == nil {
		return
	}
	for i := 0; i < added; i++ {
		m.onMatch(stepChunk, level)
	}
	for i := 0; i < left; i++ {
		m.onMatch(stepChunkLost, level)
	}
}

// stepChunk y stepChunkLost son los dos desenlaces de un trozo que SÍ era
// una petición: entró en el pedido, o no se pudo identificar. Se cuentan por TROZO y
// no por turno porque la pregunta que hay que poder responder desde fuera es «¿cuánto
// del pedido de la gente se está perdiendo?», y esa se responde con la proporción
// entre los dos, no con cuántos turnos hubo.
const (
	stepChunk     = "troceo"
	stepChunkLost = "troceo_perdido"
)
