// troceo.go — «Go DESCOMPONE → el LLM decide chiquito → Go RECOMPONE», dentro de
// un turno de WhatsApp (Plan 044 · Ola 3.5 · T3.5-3).
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
// El pipeline de intakes (Nivel C, Olas 2 y 3, desplegado) ya hace fan-out de una
// llamada por ítem —P3—, así que la FORMA no es nueva y este fichero no construye
// un segundo pipeline (C2 del ADR-0044: eso está prohibido). Lo que sí es distinto,
// y es lo que justifica que esto exista:
//
//   - **La descomposición aquí la hace Go, no el modelo.** En el Nivel C quien parte
//     el texto en ideas es P2, que es OTRA llamada al LLM. En un turno interactivo
//     no hay presupuesto para pagar una llamada solo para partir una frase: se parte
//     con separadores (abajo), que cuesta microsegundos y no puede fallar por red.
//   - **El Nivel C no llega hasta aquí.** Los mensajes de una conversación que YA
//     está dentro de un flujo los atiende el motor de flujos; no nacen jobs de
//     intake por ellos. Hoy, en el carrito, esa frase pierde productos y no hay
//     ningún pipeline que la recoja después.
//   - **El destino es otro**: líneas de carrito con el precio del catálogo del
//     tenant, en el mismo turno. El Nivel C produce un borrador que el DUEÑO aprueba
//     minutos después.
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 EL ORDEN: SEPARADORES → CANTIDAD EN GO → CASCADA → (y solo entonces) LLM
// ════════════════════════════════════════════════════════════════════════════
//
// Es el I2 del ADR-0046 («el LLM es el último peldaño, no el primero») aplicado por
// trozo, y tiene una consecuencia que conviene decir alta porque contradice la
// lectura ingenua del criterio: **con un catálogo cuyas etiquetas se parecen a lo
// que el cliente escribe, los dos fixtures del criterio se resuelven con CERO
// llamadas al modelo**. «pizzas» casa «Pizzas» por exacto y «hamburguesa» casa
// «Hamburguesas» por fuzzy (0,92 > 0,85). El troceado gasta una llamada SOLO en el
// trozo que la cascada no supo casar, y entonces gasta UNA por ese trozo — nunca
// una por el lote. Eso es lo que el contador de llamadas vigila.
//
// La CANTIDAD tampoco es del modelo: «2» son dígitos y «una» está en una tabla de
// doce entradas. El pre-resolutor determinista se negó a hacer esto (preresolutor.go
// lo dice: «aritmética del lenguaje, no similitud ortográfica») y tenía razón EN SU
// SITIO —una cascada de similitud no traduce «dos» a 2—, pero una tabla literal sí,
// y aquí la cantidad viene pegada al producto en el mismo trozo. Sin ella habría que
// pagar una llamada por cada cantidad, que es justo lo que este fichero evita.
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 QUÉ TROZO CUENTA COMO PETICIÓN, Y POR QUÉ EL FILTRO ES ESE Y NO OTRO
// ════════════════════════════════════════════════════════════════════════════
//
// El fixture del criterio trae ruido de verdad: «hola» y «para llevar» viajan en el
// mismo lote agrupado que «quiero 2 pizzas» y «y una hamburguesa». Mandar el ruido
// al modelo serían DOS llamadas tiradas de las tres que caben en un turno, y encima
// las dos primeras — o sea, el ruido se comería el presupuesto de los productos.
//
// Un trozo es una PETICIÓN si, y solo si, se cumple una de las dos:
//
//  1. trae una CANTIDAD EXPLÍCITA (dígitos o palabra de la tabla), o
//  2. la CASCADA lo casa con un artículo del catálogo.
//
// «hola» y «para llevar» fallan las dos y se descartan SIN gastar nada. «quiero 2
// pizzas» cumple las dos. «quiero pizza» cumple la 2 (cantidad 1, que es lo que
// quiere decir). Y al modelo solo suben los que cumplen la 1 y fallan la 2: «quiero
// 3 hamburgesa con queso» contra una etiqueta «Hamburguesa clásica» — el caso que de
// verdad justifica pagar una inferencia.
//
// ⚠️ Lo que este filtro deja fuera a propósito: «quiero pizza y algo de tomar». El
// segundo trozo no trae cantidad y no casa nada, así que NO se pregunta. Preferimos
// no gastar la plaza única del Edge en adivinar si una frase era un producto.
//
// ════════════════════════════════════════════════════════════════════════════
// DÓNDE ATERRIZA, Y LOS DOS CASOS QUE NO SE AGREGAN
// ════════════════════════════════════════════════════════════════════════════
//
// Aterriza en LevelCategories —el arranque del carrito, que es donde de verdad llega
// una frase así— con la MISMA forma que prime.go lleva usando desde el Plan 029:
// líneas pre-agregadas y salto a LevelContinue, sin recorrer la sub-máquina paso a
// paso. Los demás niveles NO trocean, y es una decisión de alcance: dentro de una
// categoría el cliente está eligiendo, no pidiendo, y el valor medido está en el
// arranque.
//
// Dos productos identificados NO se agregan, y los dos por el mismo motivo (no
// inventar un precio):
//
//   - **Artículo CON VARIANTES**: pre-agregarlo obligaría a elegir el precio de
//     referencia, que es exactamente lo que el contrato v2 prohíbe (prime.go). Con
//     una sola línea prime.go pregunta la variante; con N no hay a quién preguntar
//     primero, así que se deja fuera y el cliente lo agrega por el menú.
//   - **Empate en la cascada**: dos artículos igual de parecidos no son una
//     elección, son una pregunta (la regla de oro del pre-resolutor).
//
// Los dos se CUENTAN y salen en la pantalla: el cliente lee cuántos entraron y
// cuántos no. Eso es el «no se pierde en silencio» del ADR-0044 §5 dicho hacia el
// lado que importa aquí, que es el de la persona que está pidiendo.
package cart

import (
	"strconv"
	"strings"

	"github.com/EduGoGroup/wapp-shared/textmatch"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
)

// minTrozosParaTrocear es cuántas PETICIONES tiene que haber para que este camino se
// active. Dos, porque con una sola el camino de siempre (cascada → consulta) hace
// exactamente lo mismo y mejor: este fichero existe para lo que el turno de un solo
// código no sabe hacer, que es meter VARIOS productos de una vez. Con una petición
// el troceado se aparta y no se nota que existe.
const minTrozosParaTrocear = 2

// separadoresTrozo son los cortes por los que se parte el turno, del más fuerte al
// más débil. El salto de línea va primero porque el lote agrupado del Edge concatena
// los mensajes sueltos del cliente, y cada mensaje suyo es ya una unidad de sentido
// —justo la que la agrupación fundía—.
//
// La «y» y la «e» van CON ESPACIOS a los dos lados a propósito: sin ellos partirían
// «yogur» y «empanadas» por la mitad. Es la misma disciplina de la puerta 2 del
// pre-resolutor: un separador que casa dentro de una palabra no es un separador.
var separadoresTrozo = []string{"\n", ",", ";", "+", " y ", " e "}

// numerosEnPalabra es la tabla de cantidades escritas. Es LITERAL y corta a
// propósito: doce entradas cubren lo que una persona pide por WhatsApp, y todo lo
// que no esté aquí cae al camino de siempre (cantidad 1 si el producto casó, o el
// trozo descartado si no). Una tabla más larga no compra nada medible y cada entrada
// de más es una forma nueva de casar algo que no era una cantidad.
//
// «docena» y «par» están porque el few-shot del turno acotado ya las trata como
// cantidades (turnoacotado/prompt.go): si el modelo las entiende y Go no, la misma
// frase daría resultados distintos según qué peldaño la atendiera.
var numerosEnPalabra = map[string]int{
	"un": 1, "uno": 1, "una": 1, "par": 2, "dos": 2, "tres": 3, "cuatro": 4,
	"cinco": 5, "seis": 6, "siete": 7, "ocho": 8, "nueve": 9, "diez": 10,
	"once": 11, "doce": 12, "docena": 12,
}

// peticion es UN trozo ya interpretado por el lado determinista: qué pidió (tokens
// sin la cantidad), cuántas, y —si la cascada lo supo— a qué artículo del catálogo
// apunta. `idx` < 0 significa «la cascada no lo casó»: ese es, y solo ese, el trozo
// que puede llegar a costar una llamada al modelo.
type peticion struct {
	tokens   []string
	qty      int
	idx      int
	explicit bool // la cantidad venía escrita (dígitos o palabra), no supuesta
}

// candidato es un artículo del catálogo ENTERO con su categoría, listo para casar.
// El troceado busca en todo el catálogo y no en el nivel actual porque el cliente
// nombró un PRODUCTO, no una opción de la pantalla que tiene delante.
type candidato struct {
	category Category
	article  Article
}

// troceado es el camino completo, en sus DOS pasadas, y devuelve ok=false siempre que
// esto no sea asunto suyo — y entonces Step sigue byte a byte como el día antes.
//
// 🔴 VA POR ENCIMA DE TODA MUTACIÓN de Step, por el mismo motivo que la consulta
// (ver la nota grande de Step): la primera pasada puede terminar devolviendo una
// PETICIÓN que el engine descarta entera, y si por el camino hubiera declarado
// cart_started, la segunda pasada lo declararía otra vez.
func (m Module) troceado(cat Catalog, st cartState, vars map[string]any, input string) (modules.Result, bool) {
	if st.Level != LevelCategories {
		return modules.Result{}, false
	}
	peticiones, cands := m.peticionesDe(cat, input)
	if len(peticiones) < minTrozosParaTrocear {
		return modules.Result{}, false
	}
	if v, hay := modules.VeredictoDe(vars); hay {
		// 2.ª pasada: el engine ya preguntó. Se vuelve a trocear porque trocear es una
		// función PURA de la entrada —mismo texto, mismos trozos, mismo orden—, que es
		// la misma disciplina con la que aplicaVeredicto re-ejecuta consultable.
		aplicaCodigos(peticiones, v.Codigos)
		return m.recomponer(st, vars, peticiones, cands)
	}
	if pendientes := sinCasar(peticiones); len(pendientes) > 0 {
		return modules.Result{
			Vars: vars,
			Consulta: &modules.Consulta{
				Clase:    modules.ClaseOpcion,
				Nivel:    st.Level,
				Texto:    strings.TrimSpace(input),
				Trozos:   pendientes,
				Opciones: opcionesDeCandidatos(cands),
			},
		}, true
	}
	// La cascada resolvió TODO: ni una llamada. Es el caso de los dos fixtures del
	// criterio y es el caso bueno, no una excepción.
	return m.recomponer(st, vars, peticiones, cands)
}

// peticionesDe descompone el turno y deja cada trozo interpretado hasta donde llega
// el determinismo. Devuelve además la lista de candidatos, que es la MISMA con la que
// se casó y por tanto la única contra la que los códigos del veredicto pueden
// traducirse sin ambigüedad.
func (m Module) peticionesDe(cat Catalog, input string) ([]peticion, []candidato) {
	in := strings.TrimSpace(input)
	if in == "" || esNumero(in) {
		return nil, nil
	}
	// El mismo techo de prosa del pre-resolutor, y por el mismo motivo: WhatsApp
	// admite 4.096 caracteres y una parrafada no es un pedido. De paso acota cuántos
	// trozos pueden salir —16 tokens no dan para más de ocho—, así que el número de
	// trozos no necesita un tope propio: el que cuesta dinero es el de LLAMADAS, y ese
	// lo pone el resolutor (turnoacotado.MaxLlamadasPorTurno).
	if n := len(textmatch.SplitTokens(in)); n == 0 || n > maxTokensEntrada {
		return nil, nil
	}
	// 🔴 EL CORTE BARATO VA ANTES DE APLANAR EL CATÁLOGO, y no es micro-optimización:
	// esto corre en TODOS los turnos del nivel de categorías, y casar contra el
	// catálogo ENTERO es mucho más caro que casar contra la lista de categorías que el
	// pre-resolutor ya hacía. Un turno de un solo trozo —el 99 %— sale por aquí sin
	// haber tocado un solo artículo.
	trozos := trocear(in)
	if len(trozos) < minTrozosParaTrocear {
		return nil, nil
	}
	cands := candidatosDe(cat)
	if len(cands) == 0 {
		return nil, nil
	}
	opciones := opcionesInternas(cands)
	var out []peticion
	for _, trozo := range trozos {
		p, ok := interpretar(trozo, opciones)
		if ok {
			out = append(out, p)
		}
	}
	return out, cands
}

// interpretar convierte UN trozo en una petición, o dice que no lo es. Aquí vive el
// filtro de las dos condiciones de la cabecera.
func interpretar(trozo string, opciones []opcion) (peticion, bool) {
	tokens := textmatch.SplitTokens(trozo)
	if len(tokens) == 0 {
		return peticion{}, false
	}
	qty, resto, explicit := cantidadDe(tokens)
	if len(resto) == 0 {
		// Un trozo que era SOLO una cantidad («2», «un par») no nombra nada. No es una
		// petición ni una pregunta: es un número suelto que el camino de siempre ya
		// sabe tratar (la puerta 2 del pre-resolutor).
		return peticion{}, false
	}
	idx := -1
	if v, ok := mejorOpcion(opciones, resto); ok {
		// El código de una opción interna es su POSICIÓN escrita con Itoa
		// (opcionesInternas), así que este Atoi no puede fallar. Se comprueba igual:
		// el día que alguien cambie ese código por otra cosa, el troceado tiene que
		// dejar la petición sin casar —y bajarla al modelo— en vez de meter el índice
		// cero en el pedido de alguien.
		if n, err := strconv.Atoi(v.codigo); err == nil {
			idx = n
		}
	}
	if idx < 0 && !explicit {
		// Ni la cascada lo casa ni trae cantidad: es ruido («hola», «para llevar»). Se
		// descarta SIN gastar una llamada, que es la mitad del valor de este filtro.
		return peticion{}, false
	}
	return peticion{tokens: resto, qty: qty, idx: idx, explicit: explicit}, true
}

// trocear parte el turno por los separadores, del más fuerte al más débil, y
// devuelve los trozos no vacíos en el ORDEN en que el cliente los escribió. Es pura,
// sin catálogo y sin estado: es la mitad «Go descompone» del título del fichero, y la
// que se puede probar sin un modelo delante.
func trocear(in string) []string {
	trozos := []string{in}
	for _, sep := range separadoresTrozo {
		siguiente := make([]string, 0, len(trozos))
		for _, t := range trozos {
			siguiente = append(siguiente, strings.Split(t, sep)...)
		}
		trozos = siguiente
	}
	out := make([]string, 0, len(trozos))
	for _, t := range trozos {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// cantidadDe extrae la cantidad del trozo y devuelve el RESTO de los tokens, la
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
func cantidadDe(tokens []string) (qty int, resto []string, explicit bool) {
	qty, explicit = 1, false
	resto = make([]string, 0, len(tokens))
	for i := 0; i < len(tokens); i++ {
		if explicit {
			resto = append(resto, tokens[i])
			continue
		}
		n, ok := valorNumerico(tokens[i])
		if !ok || n < 1 {
			resto = append(resto, tokens[i])
			continue
		}
		if n == 1 && i+1 < len(tokens) {
			// «un par», «una docena»: el artículo indefinido no es la cantidad, es el
			// determinante de la palabra que sí lo es.
			if m, ok := valorNumerico(tokens[i+1]); ok && m > 1 {
				n, i = m, i+1
			}
		}
		qty, explicit = n, true
	}
	return qty, resto, explicit
}

// valorNumerico lee un token como cantidad: dígitos o palabra de la tabla.
func valorNumerico(t string) (int, bool) {
	if esNumero(t) {
		n, err := strconv.Atoi(t)
		// Un desbordamiento («99999999999999999999») no es una cantidad: se ignora y el
		// token se queda como parte del nombre, que es lo conservador.
		return n, err == nil
	}
	n, ok := numerosEnPalabra[strings.ToLower(t)]
	return n, ok
}

// candidatosDe aplana el catálogo entero a la lista contra la que se casa.
func candidatosDe(cat Catalog) []candidato {
	var out []candidato
	for _, c := range cat.Categories {
		for _, a := range c.Items {
			out = append(out, candidato{category: c, article: a})
		}
	}
	return out
}

// opcionesInternas es la lista para la CASCADA: el código es la POSICIÓN en cands,
// no el `Code` del artículo. La posición es única por construcción; el `Code` es
// único dentro de su categoría y aquí se mezclan todas.
func opcionesInternas(cands []candidato) []opcion {
	out := make([]opcion, 0, len(cands))
	for i, c := range cands {
		out = append(out, opcion{codigo: strconv.Itoa(i), etiqueta: c.article.Label})
	}
	return out
}

// opcionesDeCandidatos es la MISMA lista pero para el modelo, y por eso lleva
// etiquetas: al modelo nunca se le enseñan códigos (turnoacotado/prompt.go). Lo que
// vuelve es una posición de esta lista traducida a su código en Go, y el código es
// el índice — o sea que las dos listas son la misma y no pueden desalinearse.
func opcionesDeCandidatos(cands []candidato) []modules.OpcionConsulta {
	out := make([]modules.OpcionConsulta, 0, len(cands))
	for i, c := range cands {
		out = append(out, modules.OpcionConsulta{Codigo: strconv.Itoa(i), Etiqueta: c.article.Label})
	}
	return out
}

// sinCasar devuelve el TEXTO de los trozos que la cascada no resolvió, en orden. Es
// justo lo que viaja en Consulta.Trozos, y su longitud es el número máximo de
// llamadas que este turno puede llegar a hacer.
func sinCasar(ps []peticion) []string {
	var out []string
	for _, p := range ps {
		if p.idx < 0 {
			out = append(out, strings.Join(p.tokens, " "))
		}
	}
	return out
}

// aplicaCodigos vuelca el veredicto sobre las peticiones que quedaron sin casar,
// EN EL MISMO ORDEN en que se pidieron. Un código vacío, ilegible o fuera de la lista
// de candidatos deja la petición sin resolver: es la misma aduana que codigoAdmisible
// (consulta.go) y por el mismo motivo — el resolutor es un modelo y puede inventar.
func aplicaCodigos(ps []peticion, codigos []string) {
	i := 0
	for k := range ps {
		if ps[k].idx >= 0 {
			continue
		}
		if i < len(codigos) {
			if n, err := strconv.Atoi(codigos[i]); err == nil && n >= 0 {
				ps[k].idx = n
			}
		}
		i++
	}
}

// recomponer es la mitad «Go recompone»: convierte las peticiones resueltas en
// líneas del pedido y deja la conversación en la confirmación de ítem, con la MISMA
// forma que primeAdd (Plan 029) y los mismos efectos que el add manual.
//
// Devuelve ok=false si al final no entró NADA: entonces no hay pedido que recomponer
// y el turno sigue por el camino de siempre, que repromptea como cualquier otro día.
func (m Module) recomponer(st cartState, vars map[string]any, ps []peticion, cands []candidato) (modules.Result, bool) {
	out := cloneVars(vars)
	lines := cloneLines(st.Lines)
	efectos := make([]modules.Effect, 0, len(ps)+1)
	if !st.Started {
		st.Started = true
		efectos = append(efectos, event(EffectCartStarted, map[string]any{}))
	}
	var añadidas []cartLine
	var ultima candidato
	fuera := 0
	for _, p := range ps {
		c, ok := lineaDe(cands, p)
		if !ok {
			fuera++
			continue
		}
		line := newLine(c.article, Variant{}, false, p.qty)
		lines = append(lines, line)
		añadidas = append(añadidas, line)
		ultima = c
		efectos = append(efectos, withLineSnapshot(event(EffectItemAdded, map[string]any{
			"sku": line.SKU, "label": line.Label, "qty": line.Qty, "unit_price": line.UnitPrice,
		}), lines))
	}
	if len(añadidas) == 0 {
		return modules.Result{}, false
	}
	m.observaTroceo(len(añadidas), fuera, st.Level)

	st.Level = LevelContinue
	st.CatCode = ultima.category.Code
	st.SKU = ultima.article.SKU
	st.Page = 0
	st.VariantCode = ""
	st.Lines = lines
	// El contador de inválidos se reinicia igual que cuando advance() acepta una
	// entrada: este turno FUE válido, y de los más válidos que hay.
	st.Reprompts, st.RepromptsEvent = 0, ""
	storeState(out, st)
	return modules.Result{
		Vars:    out,
		Outputs: []string{pantallaTroceo(añadidas, ultima.category, fuera)},
		Effects: efectos,
	}, true
}

// lineaDe traduce una petición resuelta a su candidato, o dice que no se agrega.
//
// El artículo CON VARIANTES se queda fuera aquí, y es la puerta que impide inventar
// un precio: ver la cabecera. Es la misma decisión que primeAskVariant toma para una
// sola línea, con la única respuesta posible cuando hay N.
func lineaDe(cands []candidato, p peticion) (candidato, bool) {
	if p.idx < 0 || p.idx >= len(cands) {
		return candidato{}, false
	}
	c := cands[p.idx]
	if c.article.HasVariants() {
		return candidato{}, false
	}
	return c, true
}

// pantallaTroceo antecede a la confirmación de ítem con el detalle de lo que entró
// —y, si algo se quedó fuera, con CUÁNTO se quedó fuera—.
//
// 🔴 NO SE LE DEVUELVE AL CLIENTE EL TEXTO QUE NO SE ENTENDIÓ, y no es por
// privacidad (la pantalla va a la persona que lo escribió) sino porque repetirle sus
// propias palabras al lado de un «no pude» suena a reproche y no le dice qué hacer.
// El número y la salida —elegir del menú— sí.
func pantallaTroceo(lines []cartLine, category Category, fuera int) string {
	var b strings.Builder
	b.WriteString("Agregué a tu pedido:")
	for _, l := range lines {
		b.WriteString("\n• " + strconv.Itoa(l.Qty) + " × " + l.Label + " (" + money(l.UnitPrice) + " c/u)")
	}
	if fuera > 0 {
		b.WriteString("\n\nNo pude identificar ")
		if fuera == 1 {
			b.WriteString("1 producto más")
		} else {
			b.WriteString(strconv.Itoa(fuera) + " productos más")
		}
		b.WriteString(" de tu mensaje; podés agregarlo eligiendo del menú.")
	}
	b.WriteString("\n\n" + screenContinue(category))
	return b.String()
}

// observaTroceo cuenta el desenlace por el MISMO hook que la cascada (WithMatchHook),
// con los escalones de abajo. No se abre un segundo canal de telemetría para esto:
// quien lee `wapp_cart_match_total` está leyendo «cómo se interpretó lo que el cliente
// escribió», y el troceado es una respuesta más a esa pregunta.
//
// 🔴 CERO TEXTO DEL CLIENTE: las dos etiquetas son el escalón (constantes de abajo) y
// el nivel (vocabulario cerrado de state.go). Es la misma regla del hook original.
func (m Module) observaTroceo(añadidas, fuera int, nivel string) {
	if m.onMatch == nil {
		return
	}
	for i := 0; i < añadidas; i++ {
		m.onMatch(escalonTroceo, nivel)
	}
	for i := 0; i < fuera; i++ {
		m.onMatch(escalonTroceoPerdido, nivel)
	}
}

// escalonTroceo y escalonTroceoPerdido son los dos desenlaces de un trozo que SÍ era
// una petición: entró en el pedido, o no se pudo identificar. Se cuentan por TROZO y
// no por turno porque la pregunta que hay que poder responder desde fuera es «¿cuánto
// del pedido de la gente se está perdiendo?», y esa se responde con la proporción
// entre los dos, no con cuántos turnos hubo.
const (
	escalonTroceo        = "troceo"
	escalonTroceoPerdido = "troceo_perdido"
)
