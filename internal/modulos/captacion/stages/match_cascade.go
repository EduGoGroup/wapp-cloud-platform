// Porta internal/intake/stages/match_cascada.go @ 4cd9cfb

package stages

import (
	"github.com/EduGoGroup/wapp-shared/textmatch"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// match_cascade.go — LA CASCADA `clave → barrido → zona gris` CONTRA EL CATÁLOGO, que es
// como Match.Run busca el PRODUCTO de un ítem (los añadidos usan solo la clave y el
// n-grama; las personalizaciones, nada).
//
// # LOS ESCALONES, EN ORDEN, Y EL PRIMERO QUE RESUELVE GANA
//
//  1. **Por clave, O(1)**: los cuatro accesos del índice del catálogo, del más
//     específico al menos —sku, etiqueta, variante, tag (ver los `Strategy…`)—. Es la
//     igualdad tras normalizar hecha por tabla en vez de por barrido.
//  2. **Barrido, O(artículos)**: el comparador determinista (DefaultCascade, o el de
//     WithComparator) contra cada etiqueta, con un prefiltro que NO puede descartar un
//     match (ver LengthMargin). Gana la MEJOR confianza, no la primera que pase el
//     umbral; a igualdad, la primera del documento: mismo catálogo y mismo texto, misma
//     línea, siempre. La procedencia copia la estrategia y la confianza que declara el
//     `Result` del comparador («fuzzy», 0,947…), no las pone el barrido.
//  3. **Zona gris**: lo que queda, si está cableada (ver MaxGrayZoneCandidates).
//
// # 🔴 EL ESCALÓN CARO NUNCA ENTRA EN EL BUCLE
//
// El comparador que recorre los candidatos es determinista POR CONSTRUCCIÓN. Si la zona
// gris estuviera dentro del bucle, un catálogo de 2.000 artículos serían 2.000 llamadas
// al modelo por ítem.
//
// # 🔴 UN SOLO NORMALIZADOR (R-05)
//
// La igualdad del escalón 1 la decide el normalizador con el que se construyó el índice;
// la del barrido, `textmatch.Normalize`. Tienen que ser EL MISMO: el arranque lo
// verifica con `indice.VerificarNormalizador(textmatch.Normalize)` y aborta si no. Si
// divergieran, «Tequeños» casaría por clave y no por barrido, o al revés.
//
// Lo que ese normalizador hace es lo que el match ve, y nada más (corpus adversario en
// match_corpus_test.go, fijado contra el paquete viejo):
//
//   - pliega mayúsculas y acentos —también el acento combinante— y colapsa los blancos
//     Unicode (no-break, em, ideográfico, separador de línea…);
//   - la «ñ» NO es una «n», y la «n» con virgulilla combinante SÍ es la «ñ»;
//   - NO quita puntuación ni caracteres invisibles (zero width space, BOM, soft
//     hyphen…), y un dígito de otra escritura no es el dígito ASCII: cada uno de esos
//     caracteres es UNA EDICIÓN para el barrido. No casan por clave, pero en una
//     etiqueta larga caben en el margen y casan como «fuzzy»; en una corta, no.
//
// # UN COMPARADOR QUE REVIENTA NO SE DISFRAZA
//
// El error solo puede venir de un comparador inyectado. No tumba el job y no se propaga:
// la línea de ESE ítem sale `unmatched` —sin consultar la zona gris—, y queda un `Error`
// `match: el comparador determinista falló; el ítem sale sin match` con `item_pos`. Es
// un defecto nuestro, no un dato raro del cliente.

// DefaultCascade (antes `CascadaPorDefecto`) es el comparador determinista del barrido:
// `Exact → Fuzzy(0,85)`, SIN tercer escalón (`HasGrayZone()` es false).
//
// 🔴 EL 0,85 NO SE TOCA (D-044.45): es `textmatch.DefaultFuzzyThreshold`. Un umbral
// RELATIVO (`1 − dist/len`) no es una tolerancia constante a erratas: con 0,85, UNA
// errata solo se perdona a partir de 7 runas. Quedan FIJADOS los dos hechos:
//
//   - «ñoquis»/«noquis» (6 runas, 1 edición ⇒ 0,8333) NO casa, y con el umbral que se
//     descartó (0,80) sí casaría;
//   - «torta»/«tarta» (0,80) NO casa: con 0,80 el determinista metería en el presupuesto
//     el artículo vecino, y un presupuesto mal armado es peor que uno que tarda un
//     escalón más. Esas palabras cortas caen a la zona gris, o a `unmatched`;
//   - «tequenos congelados»/«Tequeños congelados» (19 runas, 1 edición ⇒ 0,9473) SÍ.
//
// Cada llamada devuelve una cascada utilizable; nunca nil.
func DefaultCascade() *textmatch.Cascade {
	panic(pendiente.Implementar("stages.DefaultCascade"))
}

// LengthMargin (antes `MargenLongitud`) es la fracción del texto que el umbral de la
// cascada permite EDITAR: `1 − textmatch.DefaultFuzzyThreshold` = 0,15, una de cada ~6,7
// runas. Para dos textos cuyo más largo mide `n` runas, las ediciones que caben son
// `⌊LengthMargin · n⌋` —la tabla de D-044.45: 0 hasta 6 runas, 1 con 7, 2 con 19, 3 con
// 20, 6 con 40—.
//
// De ahí sale el PREFILTRO del barrido: un par que no puede dar match se salta SIN
// llamar al comparador, por dos cotas INFERIORES de la distancia (la diferencia de
// longitudes y la de composición de runas). Lo que promete:
//
//   - 🔴 **nunca descarta un match**: el resultado de la etapa es el del barrido ingenuo
//     que compara contra todas las etiquetas, también en el borde (distancia igual al
//     tope: «alfajor de maicena prem» contra «Alfajor de maicena premium», 3 de 3);
//   - **descarta de verdad**: con el catálogo en su techo (2.000 artículos, D-044.44) y
//     un pedido en el suyo (10 ítems), un texto que no casa nada NO cuesta 2.000
//     comparaciones por ítem. Es lo que sostiene el criterio de latencia de D-044.44
//     (≤ 5 ms p99 por ítem; el barrido ingenuo medía 20,3 ms).
//
// 🔴 ESTÁ ATADO AL UMBRAL DE LA CASCADA POR DEFECTO. Un comparador inyectado con un
// umbral MÁS BAJO casaría pares que estas cotas ya descartaron: por eso WithComparator
// es para los tests y para una segunda cascada que traiga su propio margen.
const LengthMargin = 1 - textmatch.DefaultFuzzyThreshold

// MaxGrayZoneCandidates (antes `MaxCandidatosZonaGris`) es cuántos candidatos se le
// ofrecen como mucho al escalón caro: **5**. Es una cota de PROMPT, no de calidad.
//
// La consulta a la zona gris, cuando está cableada (WithGrayZone):
//
//   - se hace COMO MUCHO UNA VEZ por ítem cuyo producto no resolvieron los escalones
//     deterministas, y nunca por una personalización ni por un añadido; cada consulta
//     suma uno a `MatchArtifact.GrayZoneCalls`, falle o no;
//   - recibe el producto tal como vino (sin blancos en los bordes) y las ETIQUETAS DEL
//     CATÁLOGO de los candidatos, tal como las escribió el dueño;
//   - los candidatos se preseleccionan por SOLAPE DE TOKENS, no por distancia —si se
//     llegó aquí es porque la distancia ya dijo que no—: cuentan los tokens de 3 runas o
//     más («de», «la», «un» no identifican nada), se ordenan por cuántos comparten con
//     el texto y, a igualdad, por orden de documento. Un artículo sin ningún token en
//     común no se ofrece;
//   - sin ningún candidato NO se pregunta: sería gastar una llamada para que el modelo
//     conteste «ninguno» sobre una lista vacía;
//   - un índice fuera de la lista ofrecida (p. ej. -1) es «ninguno corresponde», una
//     respuesta válida: la línea sale `unmatched` SIN aviso;
//   - un índice válido ⇒ la línea casa ese artículo —y resuelve su variante como
//     cualquier otra—, con `strategy` = el `Name()` de la zona gris y la confianza que
//     ella declare;
//   - un ERROR degrada el ítem, no el job: la línea sale `unmatched` con Warning
//     `zona_gris_caida`, y los demás ítems conservan su precio.
const MaxGrayZoneCandidates = 5

// Nombres de los escalones para MatchProvenance.Strategy (antes `Estrategia…`). Los del
// barrido los pone `textmatch` en su Result y NO se duplican aquí. Los valores viajan en
// el artefacto y en la revisión: son literales. Todos llevan confianza 1.
const (
	// StrategySKU (antes `EstrategiaSKU`) es «el cliente escribió el identificador de
	// negocio». 🔴 El sku NO se normaliza: es una clave opaca, y «teq 30» NO es
	// «TEQ-30».
	StrategySKU = "sku"
	// StrategyExact (antes `EstrategiaExacta`) es la igualdad de etiqueta tras
	// normalizar. Con varios artículos que normalizan a la MISMA etiqueta gana el
	// primero del documento: se llaman igual, y eso es un defecto del catálogo.
	StrategyExact = "exact"
	// StrategyVariant (antes `EstrategiaVariante`) es la igualdad contra el label de una
	// VARIANTE; la línea sale con esa variante resuelta. 🔴 Con más de un artículo NO se
	// decide y se sigue bajando: «Grande» puede ser la variante de cinco.
	StrategyVariant = "variante"
	// StrategyTag (antes `EstrategiaTag`) es la igualdad contra un tag informativo del
	// artículo. 🔴 Con más de un artículo tampoco decide: elegir uno de los veinte
	// «vegano» sería inventar cuál pidió el cliente.
	StrategyTag = "tag"
	// StrategyNGram (antes `EstrategiaNGrama`) solo existe para los AÑADIDOS: un trozo
	// contiguo de tokens del texto que es la etiqueta de un artículo («extra de queso» ⇒
	// «Queso»). Se prueba del trozo más largo al más corto —con «Queso» y «Queso azul»
	// en la carta, «con queso azul» casa el segundo— y, a igual largo, de izquierda a
	// derecha. Un PRODUCTO nunca se parte: el coste de equivocarse es distinto —un
	// producto mal casado cobra el artículo equivocado; un añadido que no casa cae a
	// `customization`—.
	StrategyNGram = "ngrama"
)
