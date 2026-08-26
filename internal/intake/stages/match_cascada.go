package stages

import (
	"context"
	"sort"
	"unicode/utf8"

	"github.com/EduGoGroup/wapp-shared/textmatch"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake/catalogo"
)

// match_cascada.go — LA CASCADA `Exact → Fuzzy → zona gris` CONTRA EL CATÁLOGO.
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 EL ESCALÓN CARO NUNCA ENTRA EN EL BUCLE
// ════════════════════════════════════════════════════════════════════════════
//
// El comparador que recorre los candidatos es DETERMINISTA por construcción
// (CascadaPorDefecto lo devuelve ya `.Deterministic()`, o sea SIN zona gris: no es
// una promesa, es que el campo está a nil y `Cascade.Compare` no tiene a quién
// llamar). La zona gris se consulta APARTE, en consultarZonaGris, una sola vez por
// ítem que los escalones deterministas no cubrieron y con los candidatos ya
// preseleccionados.
//
// Si estuviera dentro del bucle, un catálogo de 2.000 artículos serían 2.000
// llamadas al modelo por ítem: a 2-4 s cada una, un pedido de 10 ítems tardaría
// días. Por eso `textmatch` separa Comparator de GrayZone, y por eso aquí se
// separan las dos funciones.
//
// ════════════════════════════════════════════════════════════════════════════
// LOS ESCALONES, EN ORDEN, Y EL COSTE DE CADA UNO
// ════════════════════════════════════════════════════════════════════════════
//
//  1. **Por clave, O(1)** (porClave): sku, etiqueta normalizada, variante y tag. Son
//     los cuatro accesos hash del índice de T3.7. Es el `Exact` de la cascada hecho
//     por tabla en vez de por barrido: el MISMO predicado —igualdad tras normalizar,
//     con el MISMO normalizador, que `catalogo.VerificarNormalizador` obliga a que
//     sea `textmatch.Normalize`— pero sin recorrer nada.
//  2. **Fuzzy, O(artículos)** (porFuzzy): la cascada completa contra cada etiqueta,
//     con un prefiltro de dos cotas que NO puede descartar un match (ver
//     descartaPorCota). Es el escalón que rescata la errata: «tequenos» sin ñ contra
//     «Tequeños» son 19 runas y 1 edición, 0,947 — pasa de sobra.
//  3. **Zona gris** (consultarZonaGris): lo que queda. Es opcional; sin ella el
//     borrador sale igual, con más renglones para el dueño.
//
// El paso 2 vuelve a probar `Exact` sobre cada candidato, y esa redundancia con el
// paso 1 es DELIBERADA: es lo que hace que el día que el índice y `Exact` dejaran de
// opinar lo mismo, el resultado siga siendo correcto (más lento, pero correcto).

// CascadaPorDefecto es el comparador determinista del bucle: `Exact → Fuzzy(0,85)`,
// SIN tercer escalón.
//
// 🔴 EL 0,85 NO SE TOCA (D-044.45). `NewFuzzy(0)` cae al DefaultFuzzyThreshold del
// módulo, que es 0,85. La consecuencia está medida y aceptada: un umbral RELATIVO
// (`1 − dist/len`) no es una tolerancia constante a erratas, sino una que crece con
// la palabra, así que con 0,85 UNA errata solo se perdona a partir de 7 runas
// —`torta`, `pizza`, `ñoquis` no llegan—. Lo decidido es que esas caigan a la ZONA
// GRIS, no bajar el umbral: con 0,80 el determinista podría casar `torta` con
// `tarta` y meter en el presupuesto el artículo equivocado, y un presupuesto mal
// armado es peor que uno que tarda un escalón más.
func CascadaPorDefecto() *textmatch.Cascade {
	return textmatch.NewCascade(textmatch.Exact{}, textmatch.NewFuzzy(0)).Deterministic()
}

// MargenLongitud es la fracción del texto que el umbral de la cascada permite
// EDITAR: con 0,85, una de cada ~6,7 runas. De ahí sale la distancia máxima que
// todavía puede dar Match, y de ahí las dos cotas que autorizan a saltarse un
// candidato SIN compararlo.
//
// 🔴 ESTÁ ATADO AL UMBRAL DE LA CASCADA POR DEFECTO. Un comparador inyectado con un
// umbral MÁS BAJO (ConComparador) casaría pares que estas cotas ya descartaron. Es
// la razón por la que ConComparador existe solo para los tests y para una segunda
// cascada que traiga su propio margen.
const MargenLongitud = 1 - textmatch.DefaultFuzzyThreshold

// distanciaMaxima es cuántas ediciones caben todavía dentro del umbral para dos
// textos cuyo más largo mide `mayor` runas: `sim = 1 − dist/mayor >= t` equivale a
// `dist <= (1−t)·mayor`, y como la distancia es entera, al suelo.
//
// Es la tabla de D-044.45 dicha en código: con 7 runas cabe UNA errata (1,05 ⇒ 1) y
// con 6 no cabe ninguna (0,9 ⇒ 0), que es por lo que «ñoquis» no casa «noquis».
func distanciaMaxima(mayor int) int {
	return int(MargenLongitud * float64(mayor))
}

// ---------------------------------------------------------------------------
// LAS DOS COTAS INFERIORES DE LA DISTANCIA — EL PREFILTRO
// ---------------------------------------------------------------------------
//
// 🔴 POR QUÉ HAY UN PREFILTRO Y NO SE COMPARA CONTRA TODO. Con el catálogo en su
// techo (2.000 artículos, D-044.44) y un pedido en el suyo (10 ítems, D-044.39),
// comparar todo son 20.000 distancias de edición por pedido. MEDIDO en este repo:
// **20,3 ms por ítem**, contra un criterio de 5 ms. El barrido ingenuo NO cabe, y no
// por poco.
//
// Lo que sí cabe es descartar por dos COTAS INFERIORES de la distancia, las dos
// exactas y las dos O(1) sobre datos ya calculados:
//
//  1. **Longitud**: `dist >= |la − lb|`. Una edición cambia la longitud como mucho
//     en uno.
//  2. **Composición**: `dist >= ⌈Σ|cA(r) − cB(r)| / 2⌉`, donde `c(r)` es cuántas
//     veces aparece la runa `r`. Cada edición mueve el histograma como mucho en 2
//     —una sustitución quita una runa y pone otra, un alta o una baja mueven una—, y
//     una TRANSPOSICIÓN no lo mueve nada, así que la cota vale también para la OSA
//     que usa `textmatch.EditDistance`.
//
// Si CUALQUIERA de las dos cotas ya pasa de distanciaMaxima, el par no puede dar
// Match y no se compara. Nunca al revés: una cota inferior jamás descarta un match
// —eso es lo que la palabra INFERIOR significa— y lo custodian dos tests, el
// diferencial contra el oráculo lineal y el de la propiedad `cota <= distancia real`.

// huecosPerfil son los cubos del histograma: 26 letras, uno para los dígitos, uno
// para el espacio y uno para TODO LO DEMÁS (ñ, símbolos, letras no latinas).
//
// Fundir runas distintas en un cubo solo puede BAJAR la suma de diferencias, o sea
// AFLOJAR la cota: sigue siendo una cota inferior, solo que menos apretada. Lo que
// no puede es apretarla de más, que es lo único que rompería el prefiltro.
const huecosPerfil = 29

// perfil es el histograma de runas de un texto YA NORMALIZADO.
//
// Los contadores son `uint8` y SATURAN en 255. Saturar tampoco rompe nada por el
// mismo motivo: una diferencia recortada es una diferencia menor, y una cota menor
// sigue siendo inferior. Una etiqueta de catálogo con 255 veces la misma letra no
// existe, pero el razonamiento no depende de eso.
type perfil [huecosPerfil]uint8

// perfilDe cuenta las runas de un texto normalizado.
func perfilDe(s string) perfil {
	var p perfil
	for _, r := range s {
		i := huecosPerfil - 1
		switch {
		case r >= 'a' && r <= 'z':
			i = int(r - 'a')
		case r >= '0' && r <= '9':
			i = 26
		case r == ' ':
			i = 27
		}
		if p[i] < 255 {
			p[i]++
		}
	}
	return p
}

// cota devuelve ⌈Σ|diferencias| / 2⌉, la cota inferior por composición.
func (p perfil) cota(q perfil) int {
	suma := 0
	for i := range p {
		if p[i] > q[i] {
			suma += int(p[i] - q[i])
			continue
		}
		suma += int(q[i] - p[i])
	}
	return (suma + 1) / 2
}

// MaxCandidatosZonaGris es cuántos candidatos se le ofrecen al escalón caro. Es una
// cota de PROMPT, no de calidad: la decisión la sigue tomando el modelo, pero
// ofrecerle 2.000 etiquetas no cabría en el contexto y lo que sí cabe se decide
// mejor con cinco buenas que con cincuenta mediocres.
const MaxCandidatosZonaGris = 5

// minRunasToken es el tamaño a partir del cual un token cuenta para preseleccionar
// candidatos. «de», «la», «un» aparecen en media carta y no identifican nada;
// contarlos haría que el ranking lo ganara la etiqueta más larga en vez de la más
// parecida.
const minRunasToken = 3

// Nombres de los escalones deterministas, para ProcedenciaMatch.Strategy. Los del
// Fuzzy y el Exact los pone `textmatch` en su Result y NO se duplican aquí; estos
// son los que solo existen en este cruce.
const (
	// EstrategiaSKU es «el cliente escribió el identificador de negocio».
	EstrategiaSKU = "sku"
	// EstrategiaExacta es la igualdad de etiqueta resuelta por el índice.
	EstrategiaExacta = "exact"
	// EstrategiaVariante es la igualdad contra el label de una VARIANTE.
	EstrategiaVariante = "variante"
	// EstrategiaTag es la igualdad contra un tag informativo del artículo.
	EstrategiaTag = "tag"
	// EstrategiaNGrama es el trozo del texto de un AÑADIDO que casó una etiqueta
	// del catálogo («extra de queso» ⇒ «queso»). Ver buscarNGrama.
	EstrategiaNGrama = "ngrama"
)

// hallazgo es un match resuelto: qué se encontró y quién lo encontró.
type hallazgo struct {
	coincidencia catalogo.Coincidencia
	procedencia  ProcedenciaMatch
}

// escaner es el catálogo PREPARADO PARA BARRER, construido UNA VEZ POR JOB junto al
// índice y reutilizado por todos los ítems.
//
// El índice de T3.7 resuelve las cuatro búsquedas por clave en O(1), pero un hash no
// sabe contestar «¿qué etiqueta se PARECE a ésta?». Esto es lo que falta para el
// escalón del medio: las etiquetas ya normalizadas (para no volver a normalizar
// 2.000 cadenas por cada ítem) y su longitud en runas (para el prefiltro).
//
// Los TOKENS se construyen PEREZOSAMENTE: solo hacen falta cuando hay que preguntarle
// a la zona gris, y un pedido en el que todo casa —el caso normal— no debe pagar
// 2.000 `SplitTokens` para nada.
type escaner struct {
	idx      *catalogo.Indice
	norm     []string
	runas    []int
	perfiles []perfil

	tokens [][]string // nil hasta el primer uso; ver tokensDe
}

// nuevoEscaner normaliza las etiquetas del catálogo y calcula sus histogramas UNA
// SOLA VEZ por job. Con el catálogo en su techo son 2.000 normalizaciones y 2.000
// histogramas, que repartidos entre los hasta 10 ítems del pedido salen a una
// fracción del presupuesto por ítem — MEDIDO en match_rendimiento_test.go.
//
// No se cachea entre jobs a propósito: la etapa se queda SIN ESTADO, como P2, P3 y
// P4, y por tanto es segura para varias goroutines sin candado. El día que el precio
// del catálogo pese, el sitio de la caché es al lado del índice (que ya la tiene por
// contenido), no aquí.
func nuevoEscaner(idx *catalogo.Indice) *escaner {
	n := idx.Articulos()
	e := &escaner{idx: idx, norm: make([]string, n), runas: make([]int, n), perfiles: make([]perfil, n)}
	for i := 0; i < n; i++ {
		e.norm[i] = textmatch.Normalize(idx.Etiqueta(i))
		e.runas[i] = utf8.RuneCountInString(e.norm[i])
		e.perfiles[i] = perfilDe(e.norm[i])
	}
	return e
}

// tokensDe devuelve los tokens de la etiqueta n, construyéndolos la primera vez que
// alguien los pide.
func (e *escaner) tokensDe(n int) []string {
	if e.tokens == nil {
		e.tokens = make([][]string, len(e.norm))
		for i, s := range e.norm {
			e.tokens[i] = textmatch.SplitTokens(s)
		}
	}
	return e.tokens[n]
}

// buscarProducto corre la cascada entera sobre el texto de un PRODUCTO.
//
// Devuelve el hallazgo y si hubo match. El error solo puede venir del comparador
// inyectado —la cascada por defecto no falla nunca— y NO se degrada: un comparador
// que revienta es un defecto nuestro, no un dato raro del cliente, y taparlo
// convertiría todas las líneas en `unmatched` sin que nadie se enterara.
//
// 🔴 UN PRODUCTO SE BUSCA ENTERO, NUNCA POR TROZOS. Lo contrario —n-gramas, como en
// los añadidos— casaría «torta de chocolate» con un artículo llamado «Chocolate» y
// cobraría una tableta en vez de una torta. La regla 1 de match.go, aplicada.
func (s *Match) buscarProducto(ctx context.Context, esc *escaner, texto string) (hallazgo, bool, error) {
	if texto == "" {
		return hallazgo{}, false, nil
	}
	if h, ok := porClave(esc.idx, texto); ok {
		return h, true, nil
	}
	return s.porFuzzy(ctx, esc, texto)
}

// porClave son los cuatro accesos O(1) del índice, del más específico al menos.
//
// Los dos criterios de desempate, que es donde vive el «nunca inventa»:
//
//   - **etiqueta**: con varios artículos que normalizan a la MISMA etiqueta gana el
//     primero del documento. No es elegir entre dos cosas distintas: los dos se
//     llaman igual, y un catálogo con etiquetas duplicadas es un defecto del
//     catálogo que el dueño ve en la bandeja.
//   - **variante y tag**: con más de uno NO se decide y se sigue bajando. «Grande»
//     puede ser la variante de cinco artículos y «vegano» el tag de veinte; elegir
//     uno sería inventar cuál pidió el cliente.
func porClave(idx *catalogo.Indice, texto string) (hallazgo, bool) {
	// El sku NO se normaliza: es la clave opaca con la que el dueño nombra su
	// producto, y normalizarla colapsaría "TORTA-CHOC" con "torta choc".
	if c, ok := idx.PorSKU(texto); ok {
		return hallazgo{c, ProcedenciaMatch{Strategy: EstrategiaSKU, Confidence: 1}}, true
	}
	if cs := idx.PorEtiqueta(texto); len(cs) > 0 {
		return hallazgo{cs[0], ProcedenciaMatch{Strategy: EstrategiaExacta, Confidence: 1}}, true
	}
	if cs := idx.PorVariante(texto); len(cs) == 1 {
		return hallazgo{cs[0], ProcedenciaMatch{Strategy: EstrategiaVariante, Confidence: 1}}, true
	}
	if cs := idx.PorTag(texto); len(cs) == 1 {
		return hallazgo{cs[0], ProcedenciaMatch{Strategy: EstrategiaTag, Confidence: 1}}, true
	}
	return hallazgo{}, false
}

// porFuzzy barre las etiquetas y se queda con la MEJOR, no con la primera.
//
// # POR QUÉ LA MEJOR Y NO LA PRIMERA
//
// Con 2.000 artículos, «la primera que pase de 0,85» puede ser un 0,86 mientras diez
// posiciones más abajo hay un 0,99. El empate se rompe por orden de documento, así
// que el resultado sigue siendo determinista: mismo catálogo y mismo texto, misma
// línea, siempre.
func (s *Match) porFuzzy(ctx context.Context, esc *escaner, texto string) (hallazgo, bool, error) {
	objetivo := textmatch.Normalize(texto)
	runas := utf8.RuneCountInString(objetivo)
	huella := perfilDe(objetivo)

	mejor, mejorConf, mejorEstrategia := -1, 0.0, ""
	for i, cand := range esc.norm {
		if descartaPorCota(runas, esc.runas[i], huella, esc.perfiles[i]) {
			continue
		}
		r, err := s.cmp.Compare(ctx, objetivo, cand)
		if err != nil {
			return hallazgo{}, false, err
		}
		if r.Outcome == textmatch.OutcomeMatch && r.Confidence > mejorConf {
			mejor, mejorConf, mejorEstrategia = i, r.Confidence, r.Strategy
		}
	}
	if mejor < 0 {
		return hallazgo{}, false, nil
	}
	// La estrategia la declara el propio Result, no este bucle: el escalón que
	// resolvió el par lo sabe él, y copiarlo evita que la procedencia diga «fuzzy»
	// el día que la cascada gane un escalón determinista más.
	return hallazgo{
		coincidencia: esc.idx.En(mejor),
		procedencia:  ProcedenciaMatch{Strategy: mejorEstrategia, Confidence: mejorConf},
	}, true, nil
}

// descartaPorCota responde si el par NO PUEDE dar Match, usando las dos cotas
// inferiores de la distancia. Ver el bloque «LAS DOS COTAS INFERIORES».
//
// Es la única función de este fichero que decide sin comparar, así que es la única
// que podría perder un match. Por eso las dos desigualdades van en el mismo sitio y
// con el mismo tope, y por eso hay un test que las contrasta contra la distancia de
// verdad y otro que contrasta la etapa entera contra un barrido ingenuo.
func descartaPorCota(la, lb int, pa, pb perfil) bool {
	mayor := la
	if lb > mayor {
		mayor = lb
	}
	tope := distanciaMaxima(mayor)

	dif := la - lb
	if dif < 0 {
		dif = -dif
	}
	if dif > tope {
		return true
	}
	return pa.cota(pb) > tope
}

// consultarZonaGris es el TERCER escalón, y se llama COMO MUCHO UNA VEZ por ítem que
// los deterministas no cubrieron. Sin zona gris cableada no hace nada.
//
// # LOS CANDIDATOS SE PRESELECCIONAN POR SOLAPE DE TOKENS, NO POR DISTANCIA
//
// Y esto no es un detalle de implementación: es la razón de que el escalón exista.
// Si llegamos aquí es porque la distancia de edición ya dijo que no, y suele decirlo
// porque los dos textos miden cosas distintas —el cliente escribe «torta de
// chocolate» (18 runas) y el catálogo dice «Torta chocolate húmedo + crema choc.»
// (33)—. Ordenar por distancia volvería a traer justo lo que ya se descartó. El
// solape de tokens sí ve que comparten «torta» y «chocolate».
//
// # UN FALLO DEL MODELO DEGRADA EL ÍTEM, NO EL JOB
//
// Devuelve el motivo del aviso cuando la llamada falla, para que el llamante lo
// registre y siga: el ítem cae a `unmatched` —que es lo que habría pasado sin zona
// gris— y los demás conservan su precio. Ver la cabecera de match.go.
func (s *Match) consultarZonaGris(ctx context.Context, art *ArtefactoMatch, esc *escaner, texto string) (hallazgo, bool, string) {
	if s.zonaGris == nil || texto == "" {
		return hallazgo{}, false, ""
	}
	posiciones := esc.preseleccion(texto)
	if len(posiciones) == 0 {
		// Sin nada que ofrecer, preguntar sería gastar una llamada para que el
		// modelo conteste «ninguno» sobre una lista vacía.
		return hallazgo{}, false, ""
	}
	etiquetas := make([]string, len(posiciones))
	for i, p := range posiciones {
		etiquetas[i] = esc.idx.Etiqueta(p)
	}

	art.GrayZoneCalls++
	d, err := s.zonaGris.Resolve(ctx, texto, etiquetas)
	if err != nil {
		return hallazgo{}, false, MotivoZonaGrisCaida
	}
	if d.Index < 0 || d.Index >= len(posiciones) {
		return hallazgo{}, false, "" // «ninguno corresponde», que es una respuesta válida
	}
	return hallazgo{
		coincidencia: esc.idx.En(posiciones[d.Index]),
		procedencia:  ProcedenciaMatch{Strategy: s.zonaGris.Name(), Confidence: d.Confidence},
	}, true, ""
}

// preseleccion devuelve hasta MaxCandidatosZonaGris posiciones del catálogo,
// ordenadas por cuántos tokens comparten con el texto y, a igualdad, por orden de
// documento. Un candidato sin ningún token en común no entra: ofrecerlo sería pedirle
// al modelo que elija entre cosas que no tienen nada que ver.
func (e *escaner) preseleccion(texto string) []int {
	pedidos := map[string]struct{}{}
	for _, t := range textmatch.SplitTokens(texto) {
		if utf8.RuneCountInString(t) >= minRunasToken {
			pedidos[t] = struct{}{}
		}
	}
	if len(pedidos) == 0 {
		return nil
	}

	type puntuado struct{ pos, solape int }
	var ranking []puntuado
	for i := range e.norm {
		solape := 0
		for _, t := range e.tokensDe(i) {
			if _, ok := pedidos[t]; ok {
				solape++
			}
		}
		if solape > 0 {
			ranking = append(ranking, puntuado{pos: i, solape: solape})
		}
	}
	// SliceStable + criterio único: el desempate lo pone el orden de documento, que
	// es el que ya traía el slice.
	sort.SliceStable(ranking, func(i, j int) bool { return ranking[i].solape > ranking[j].solape })

	if len(ranking) > MaxCandidatosZonaGris {
		ranking = ranking[:MaxCandidatosZonaGris]
	}
	out := make([]int, len(ranking))
	for i, r := range ranking {
		out[i] = r.pos
	}
	return out
}

// buscarNGrama es el escalón que solo corre para los AÑADIDOS: parte el texto en
// n-gramas contiguos y busca cada uno en el índice por etiqueta exacta, del más
// largo al más corto.
//
// # POR QUÉ LOS AÑADIDOS SÍ SE PARTEN Y LOS PRODUCTOS NO
//
// Un añadido llega envuelto en relleno —«extra de queso», «con queso», «más
// queso»— y lo que identifica al artículo es el sustantivo. Un producto, en cambio,
// ES el texto entero: partirlo casaría «torta de chocolate» con «Chocolate».
//
// La asimetría se sostiene porque el COSTE DE EQUIVOCARSE es distinto en cada lado:
// un producto mal casado cobra el artículo equivocado, y un añadido que no casa cae
// a `customization` —sin precio, pero llega a producción—.
//
// Del más largo al más corto porque el n-grama largo es el más específico: con un
// catálogo que tenga «queso» y «queso azul», «con queso azul» debe casar el segundo.
func (e *escaner) buscarNGrama(texto string) (hallazgo, bool) {
	tokens := textmatch.SplitTokens(texto)
	for l := len(tokens); l >= 1; l-- {
		for start := 0; start+l <= len(tokens); start++ {
			trozo := ""
			for i := start; i < start+l; i++ {
				if i > start {
					trozo += " "
				}
				trozo += tokens[i]
			}
			if cs := e.idx.PorEtiqueta(trozo); len(cs) > 0 {
				return hallazgo{cs[0], ProcedenciaMatch{Strategy: EstrategiaNGrama, Confidence: 1}}, true
			}
		}
	}
	return hallazgo{}, false
}
