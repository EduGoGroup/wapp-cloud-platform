// Porta internal/intake/stages/match.go @ 4cd9cfb

package stages

import (
	"context"
	"errors"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/logger"
	"github.com/EduGoGroup/wapp-shared/textmatch"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo/indice"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// match.go — LA ETAPA `match` (Plan 044 · Ola 3 · T3.2): lo que P4 dejó normalizado se
// cruza con el catálogo del tenant y se convierte en las LÍNEAS del presupuesto.
//
// Es la primera etapa que NO habla con un modelo en su camino normal: es Go, con una
// llamada al LLM SOLO en el escalón de zona gris —opcional, y que producción NO cablea
// (R-04)— y solo por el ítem que los dos escalones deterministas no resolvieron.
//
// # 🔴 LAS DOS REGLAS QUE GOBIERNAN TODO LO DEMÁS
//
//  1. **EL MATCH NUNCA INVENTA** (REQ-17). Si el catálogo no tiene el producto, la línea
//     sale `unmatched` CON PRECIO VACÍO y el dueño decide. Jamás se elige «el artículo
//     más parecido» para que el presupuesto salga completo: un presupuesto con el
//     artículo equivocado es peor que uno con un renglón sin precio, porque el segundo
//     se ve y el primero se cobra. `unmatched` es el renglón que precifica la dueña, no
//     una pérdida.
//  2. **UNA PERSONALIZACIÓN NO ES UN ARTÍCULO** (D-044.14). «Sin sal» no se busca en el
//     catálogo —ni siquiera se ofrece a la cascada—: se aparta ANTES de comparar nada y
//     se pega a su línea después.
//
// # 🔴 DEUDA-044.16: UN ÍTEM MALO **NO** TIRA EL BORRADOR
//
// Se DEGRADA el ítem y el resto vive, nunca al revés: la unidad de daño es el ítem, no
// el pedido (un parseo todo-o-nada mató en campo 13 de 14 jobs por una clave `qty`
// ausente); es la doctrina del pipeline (P3 aísla y sigue); y degradar no pierde la
// información, la ESCALA a un humano: el ítem sale como línea `unmatched` con su texto y
// su evidencia, más un Warning con el motivo.
//
// Lo que SÍ hace fallar la etapa entera es de infraestructura y se reintenta: que no
// haya artefacto de P4, que no haya índice de catálogo (un borrador con todo `unmatched`
// mentiría sobre el catálogo, no sobre el ítem) y que la persistencia falle.
//
// ⚠️ Un error de la ZONA GRIS tampoco mata el job, y aquí se separa de P3 a propósito:
// allí el modelo es el ÚNICO productor; aquí es el TERCER escalón de una cascada cuyos
// dos primeros ya corrieron.
//
// # LA ETAPA NO TIENE ESTADO
//
// No cachea nada entre jobs —lo que prepara del catálogo para barrerlo lo prepara una
// vez POR JOB—, así que dos `Run` seguidos no se ven y los contadores del artefacto son
// los de ESE job.
//
// # LO QUE NUNCA SALE POR EL LOG
//
// Ni una palabra del cliente (ADR-0034): ni el producto, ni la evidencia, ni una
// indicación, ni la nota. De un ítem se registra su POSICIÓN (`item_pos`).

// Los `kind` de una línea del presupuesto (design §7.4), vocabulario CERRADO. Los
// valores son claves del artefacto y de la revisión: no se tocan.
const (
	// KindMatched es la línea que SÍ está en el catálogo: sku, etiqueta y precio
	// COPIADOS de él. `unit_price` puede seguir vacío si el artículo se vende por
	// variantes y no quedó determinada cuál (ver Line.VariantOptions).
	KindMatched = "matched"
	// KindUnmatched es el producto que el catálogo NO tiene. Precio VACÍO siempre: es el
	// renglón que el dueño precifica a mano. 🔴 NO es el kind de un añadido sin artículo
	// (D-041.24): ése va como `customization` de su línea.
	KindUnmatched = "unmatched"
	// KindShipping es la línea de envío, que va SIEMPRE (D-041.11).
	KindShipping = "shipping"
)

// Motivos de un Warning (antes `Motivo…`). Vocabulario cerrado que describe el CASO
// REAL, no una familia: «la cantidad no es válida» y «el ítem no traía producto» piden
// cosas distintas al dueño. Los valores viajan en el artefacto: son literales.
const (
	// WarningNoProduct (antes `MotivoSinProducto`) es el ítem sin producto. Sin
	// evidencia tampoco, no genera línea; con evidencia, sale `unmatched` con ella.
	WarningNoProduct = "sin_producto"
	// WarningInvalidQty (antes `MotivoCantidadInvalida`) es `qty <= 0`. La línea
	// SOBREVIVE con la cantidad tal como vino —no se maquilla a 1—.
	WarningInvalidQty = "cantidad_invalida"
	// WarningNoteTooLong (antes `MotivoIndicacionLarga`) es la personalización que no
	// cabe en `intakes.MaxNoteRunes`. No se TRUNCA (REQ-33e: el final es donde va el
	// alérgeno) y no se pierde en silencio: la línea sale sin indicación y con aviso.
	WarningNoteTooLong = "indicacion_larga"
	// WarningGrayZoneDown (antes `MotivoZonaGrisCaida`) es el ítem que la zona gris no
	// pudo resolver porque falló la llamada. Cae a `unmatched`, como el que no casó.
	WarningGrayZoneDown = "zona_gris_caida"
	// WarningRangeWithoutVariant (antes `MotivoRangoSinVariante`) es el artículo que SÍ
	// está en el catálogo pero cuyo rango pedido no cae en ninguna de sus variantes
	// («25-30 porciones» en un artículo que solo se hace de 10 y 12).
	WarningRangeWithoutVariant = "rango_sin_variante"
)

// OrderNote (antes `NotaDePedido`) es la indicación que vale para el PEDIDO ENTERO
// («dejarlo en portería»), no para ninguna línea. Va a `intakes.customer_note` y NUNCA
// se reparte por las líneas (REQ-17e, D-041.19).
//
// 🔴 HOY NADIE LA PRODUCE, Y ESO ES UN HUECO DEL PIPELINE, NO DE ESTA ETAPA. Es un tipo
// propio —y no un `string` suelto— para que el hueco se VEA en la línea del llamante.
// Ninguna etapa anterior emite una nota de pedido (`llm.MainIdeas` tiene `wants` y
// `delivery_hint`; `llm.ItemSpec.notes` es del ÍTEM), así que «y dejarlo en portería»
// llega hoy como un ítem más y sale línea `unmatched`. No se arregla aquí con un
// léxico: distinguirla de un producto es SEMÁNTICO, y un léxico ancho se tragaría un
// producto real sin que nadie lo viera (la regla 1). Las dos salidas —que P2 la emita,
// o que el dueño la mueva desde la bandeja— son decisiones de producto.
type OrderNote string

// NoOrderNote (antes `SinNotaDePedido`) es el valor que HOY debe pasar el llamante: no
// es un default cómodo, es la ausencia de un productor.
const NoOrderNote OrderNote = ""

// ErrMatchNotWired (antes `ErrMatchSinCablear`) es la etapa a la que le falta el log o
// el store. NO comparte texto con ErrNotWired porque no comparte piezas: el match no
// necesita selector de vía.
var ErrMatchNotWired = errors.New("stages: la etapa match necesita log y store")

// ErrNoCatalog (antes `ErrSinCatalogo`) es «llegó un job sin índice de catálogo». Es de
// infraestructura y se reintenta: sin catálogo ningún ítem puede casar.
var ErrNoCatalog = errors.New("stages: el match necesita el índice del catálogo del tenant")

// ErrNoQuantities (antes `ErrSinCantidades`) es «llegó un job sin el artefacto de P4».
// Distinto de un artefacto con CERO ítems, que sí es legítimo (design §3.2) y produce un
// borrador con la sola línea de envío.
var ErrNoQuantities = errors.New("stages: el match necesita el artefacto de P4")

// VariantOption (antes `OpcionVariante`) es una presentación concreta que el DUEÑO tiene
// que elegir, con su precio ya copiado del catálogo (design §7.4).
type VariantOption struct {
	// SKU es el sku COMPUESTO de la línea: `<sku del artículo>#<code de la variante>`
	// ("TORTA-CHOC#12", D-041.4).
	SKU string `json:"sku"`
	// Label es la etiqueta compuesta: `<artículo> — <variante>`, con raya larga U+2014
	// entre espacios ("Torta de chocolate — 12 porciones").
	Label string `json:"label"`
	// Price es el precio de ESA variante, copiado del catálogo.
	Price float64 `json:"price"`
}

// MatchProvenance (antes `ProcedenciaMatch`) es de dónde salió el match de una línea
// (design §7.4: `"match": {"strategy": "fuzzy", "confidence": 0.91}`): lo que permite
// mirar un presupuesto raro y saber si lo decidió una igualdad, una distancia de edición
// o el modelo.
type MatchProvenance struct {
	// Strategy es el escalón que lo resolvió: StrategySKU, StrategyExact,
	// StrategyVariant, StrategyTag, StrategyNGram, el que declare el comparador en su
	// `Result` ("fuzzy") o el `Name()` de la zona gris.
	Strategy string `json:"strategy"`
	// Confidence es 0..1. En los escalones por clave es 1.0; en el fuzzy, la similitud;
	// en la zona gris, la que ella declare.
	Confidence float64 `json:"confidence"`
}

// Line (antes `Linea`) es un renglón del presupuesto, tal como lo verá el dueño en la
// bandeja y tal como `draft` lo proyectará a `intake_revisions.payload` (design §7.4).
type Line struct {
	// Kind es KindMatched, KindUnmatched o KindShipping.
	Kind string `json:"kind"`
	// SKU es el del catálogo, COPIADO. Vacío en `unmatched`.
	SKU string `json:"sku,omitempty"`
	// Label, en `matched`, viene del CATÁLOGO; en `unmatched` es lo que dijo el cliente
	// (el producto sin blancos en los bordes y, si no lo hay, la evidencia recortada).
	Label string `json:"label"`
	// Qty es la cantidad de la línea, tal como vino de P4.
	Qty int `json:"qty"`
	// UnitPrice es el precio unitario COPIADO del catálogo, o nil cuando lo tiene que
	// poner el dueño. Es puntero y NO lleva `omitempty` a propósito: design §7.4 escribe
	// `"unit_price": null`, y un 0 no significa lo mismo que «sin precio».
	UnitPrice *float64 `json:"unit_price"`
	// Customization son las indicaciones del cliente para ESTA línea, saneadas y unidas.
	// No lleva precio y NO ENTRA EN NINGÚN TOTAL (D-044.14).
	Customization string `json:"customization,omitempty"`
	// Range es el rango pedido, sin colapsar («10 o 12 porciones»).
	Range *llm.Range `json:"range,omitempty"`
	// UnitKind y PackageSize vienen de P4 y viajan por si el artículo que casó no es el
	// paquete: sin ellos, «un paquete de 30» se pierde en cuanto la línea toma el nombre
	// del catálogo.
	UnitKind    string `json:"unit_kind,omitempty"`
	PackageSize int    `json:"package_size,omitempty"`
	// VariantOptions son las presentaciones entre las que el DUEÑO elige. Cuando las
	// hay, UnitPrice va nil: elegir por el cliente es inventar.
	VariantOptions []VariantOption `json:"variant_options,omitempty"`
	// Match es la procedencia. Nil en `unmatched` y en `shipping`.
	Match *MatchProvenance `json:"match,omitempty"`
	// Note es la nota de la PLATAFORMA sobre la línea, no del cliente («por confirmar
	// zona»). No confundir con Customization.
	Note string `json:"note,omitempty"`
	// Evidence es la frase literal del cliente que sostiene la línea, tal como vino.
	// Viaja en claro dentro del artefacto del job; `draft` la cifra en la revisión.
	Evidence string `json:"evidence,omitempty"`
}

// Warning (antes `Aviso`) es un ítem que no salió redondo, con su motivo. Lleva la
// POSICIÓN del ítem en el artefacto de P4 y no su texto: el texto ya está persistido una
// vez, y una segunda copia del literal puede divergir de la primera.
type Warning struct {
	// ItemPos es la posición del ítem en `artifacts.p4.items`.
	ItemPos int `json:"item_pos"`
	// Reason es uno de los `Warning…` de este fichero.
	Reason string `json:"reason"`
}

// MatchArtifact (antes `ArtefactoMatch`) es lo que la etapa persiste bajo
// `artifacts.match`: el borrador SIN la parte que aún no es suya (las media refs y la
// revisión son de `draft`).
type MatchArtifact struct {
	// Version es la del artefacto (`llm.ArtifactVersion`), que exige
	// `intake.Artifact.Validate`.
	Version int `json:"version"`
	// Lines son las líneas EN ORDEN: por cada ítem de P4 su línea y, pegadas detrás, las
	// de sus añadidos facturables; el envío SIEMPRE al final.
	Lines []Line `json:"lines"`
	// CustomerNote es la indicación del pedido entero, ya saneada.
	CustomerNote string `json:"customer_note,omitempty"`
	// Warnings son los ítems degradados (DEUDA-044.16). Vacío (nil) en el caso normal.
	Warnings []Warning `json:"warnings,omitempty"`
	// GrayZoneCalls son las llamadas a la zona gris de ESTE job: como mucho UNA por ítem
	// que los escalones deterministas no cubrieron, y CERO por personalización o por
	// añadido. La clave viaja siempre, también a 0.
	GrayZoneCalls int `json:"gray_zone_calls"`
}

// PartialTotal (antes `TotalParcial`) suma `unit_price × qty` de las líneas que YA
// tienen precio y cuenta las que siguen pendientes (`unit_price` nil), envío incluido.
// Los dos números van juntos a propósito (design §7.5: «Total parcial: $X (1 línea
// pendiente de precio)»): un total que callara las pendientes diría que el presupuesto
// está cerrado.
//
// Es una VISTA de las líneas —no se congela en el artefacto—; la `customization` NO
// entra (D-044.14); una línea de cantidad 0 con precio suma 0 y NO cuenta como
// pendiente. Sin líneas devuelve `(0, 0)`.
func (a *MatchArtifact) PartialTotal() (total float64, pending int) {
	panic(pendiente.Implementar("stages.MatchArtifact.PartialTotal"))
}

// MatchInput (antes `EntradaMatch`) es todo lo que la etapa necesita del job; cada campo
// tiene un dueño distinto:
//
//   - Quantities (antes `Cantidades`) las deja P4;
//   - Index (antes `Indice`) lo construye la caché del catálogo UNA VEZ POR JOB y lo
//     pasa el worker: esta etapa NO lee `tenant_content`;
//   - Zones (antes `Zonas`) salen de `tenant_settings.shipping_zones`, leídas también
//     una vez por job. Sin zonas, el envío sale sin precio;
//   - Note (antes `Nota`) es la del pedido entero, que hoy nadie produce (ver OrderNote).
type MatchInput struct {
	Quantities *llm.Quantities
	Index      *indice.Indice
	Zones      []intakes.ShippingZone
	Note       OrderNote
}

// Match es la etapa del CRUCE CON EL CATÁLOGO (Plan 044 · T3.2). Sus piezas: el log, el
// store donde deja el artefacto, el comparador DETERMINISTA del bucle y —opcional— la
// zona gris.
type Match struct {
	cmp      textmatch.Comparator
	grayZone textmatch.GrayZone
}

// MatchOption (antes `OpciónMatch`) configura la etapa.
//
// 🔴 ES UN TIPO APARTE DE Option, Y ESA ES LA PROMESA (R-03): Option es la de los plazos
// de las etapas LLM, y como los dos tipos no son asignables entre sí, un match «con
// plazo por llamada» —`NewMatch(log, store, WithCallTimeout(…))`— NO COMPILA. Aquí un
// plazo por llamada no significa nada: en producción la etapa no llama al modelo.
type MatchOption func(*Match)

// WithGrayZone (antes `ConZonaGris`) cablea el TERCER escalón de la cascada: el juicio
// caro para lo que ni la igualdad ni la distancia de edición resolvieron.
//
// 🔴 PRODUCCIÓN NO LO CABLEA (R-04): el tercer escalón llamaría al LLM por la misma
// plaza única del Edge que ocupan P2–P4. El exportado se conserva; cablearlo «ya que
// estamos» está prohibido por la spec de F7.
//
// 🔴 NO entra en el bucle de comparación: se consulta como mucho UNA VEZ por ítem no
// cubierto (ver MaxGrayZoneCandidates). Sin esta opción, o con `nil`, la etapa produce
// un borrador igual de correcto, con más renglones `unmatched` para el dueño.
func WithGrayZone(gz textmatch.GrayZone) MatchOption {
	panic(pendiente.Implementar("stages.WithGrayZone"))
}

// WithComparator (antes `ConComparador`) sustituye el comparador determinista del
// barrido. Existe para los tests —espiar QUÉ textos se comparan es la única forma de
// demostrar que una personalización nunca llega al matcher— y para una segunda cascada
// que traiga su propio margen (ver LengthMargin). Pasar `nil` no hace nada: la etapa no
// se queda sin comparador.
func WithComparator(cmp textmatch.Comparator) MatchOption {
	panic(pendiente.Implementar("stages.WithComparator"))
}

// NewMatch construye la etapa. Devuelve ErrMatchNotWired (y etapa nil) si `log` o
// `store` es nil. Sin opciones, el comparador es DefaultCascade() y no hay zona gris.
func NewMatch(log logger.Logger, store StageStore, opts ...MatchOption) (*Match, error) {
	panic(pendiente.Implementar("stages.NewMatch"))
}

// Run cruza los ítems de P4 con el catálogo, persiste las líneas del presupuesto bajo
// `artifacts.match` y devuelve el artefacto tal como quedó escrito. No lee de ninguna
// parte —índice y zonas llegan ya leídos— y no calcula totales (ver PartialTotal).
//
// # ERRORES (con cualquiera, el artefacto devuelto es nil y no se persiste nada)
//
//   - `in.Quantities == nil` ⇒ ErrNoQuantities; se mira ANTES que el índice.
//   - `in.Index == nil` ⇒ ErrNoCatalog.
//   - `SaveStage` falla ⇒ `match: persistir el artefacto: %w`; devuelve `(false, nil)`
//     ⇒ ErrJobNotProcessing.
//
// Nada de lo que traiga un ÍTEM es error, ni el fallo de la zona gris, ni el del
// comparador inyectado.
//
// # POR CADA ÍTEM, EN ESTE ORDEN (el orden ES la garantía)
//
//  0. **Degradaciones.** Producto (sin blancos) vacío y evidencia vacía ⇒ Warning
//     `sin_producto` y NINGUNA línea: se ignoran también sus añadidos. Producto vacío
//     con evidencia ⇒ el mismo Warning y la línea sale `unmatched` con la evidencia
//     recortada como etiqueta, sin buscar nada en el catálogo ni en la zona gris.
//     `qty <= 0` ⇒ Warning `cantidad_invalida` y la línea conserva la cantidad.
//  1. **Se APARTAN las personalizaciones** (`customizations`): no tocan el catálogo, el
//     comparador ni la zona gris, jamás —aunque el catálogo venda un artículo con ese
//     nombre—.
//  2. **Se busca el PRODUCTO, ENTERO** y nunca por trozos («torta de chocolate» no casa
//     un artículo «Chocolate»): la cascada de match_cascade.go —clave, barrido, zona
//     gris—. Lo que no casa sale KindUnmatched con la etiqueta del cliente, sin sku, sin
//     precio, sin opciones y sin procedencia. Toda línea de producto lleva `qty`,
//     `range`, `unit_kind`, `package_size` y `evidence` del ítem.
//  3. **Se resuelve la VARIANTE** sin elegir nunca por el cliente; en los cuatro casos
//     la línea es KindMatched, porque el PRODUCTO sí está en el catálogo:
//     a) el texto casó el label de UNA variante ⇒ sku y etiqueta compuestos y su precio;
//     b) el artículo no tiene variantes ⇒ su sku, su etiqueta y su precio;
//     c) tiene variantes y el rango señala EXACTAMENTE UNA ⇒ esa, como en (a);
//     d) el rango señala VARIAS, o no hay rango ⇒ sku y etiqueta del artículo,
//     `unit_price` nil y las candidatas en `variant_options` (todas si no hay rango).
//     Si el rango no señala NINGUNA ⇒ todas las variantes y Warning
//     `rango_sin_variante`.
//     Una variante es candidata cuando su label menciona algún entero ASCII dentro de
//     `[min, max]` (extremos incluidos; un rango invertido se ordena). La unidad del
//     rango NO se compara, y una variante sin números («Grande») no es candidata.
//  4. **Se buscan los AÑADIDOS** (`addon_candidates`), uno a uno; la única regla es
//     D-044.14, ¿el catálogo tiene un artículo para eso?:
//     - en blanco ⇒ se ignora;
//     - empieza por la negación «sin » (tras normalizar: «Sin salsa» también) ⇒ NUNCA
//     es facturable, sea lo que sea lo negado: va a indicación;
//     - casa por clave o por n-grama (StrategyNGram) ⇒ línea PROPIA KindMatched detrás
//     de la del ítem, con cantidad 1 —el añadido es del pedido y no se multiplica por
//     la del ítem (D-041.24)—, la evidencia del ítem y sin rango; un artículo con
//     variantes las ofrece todas, sin aviso;
//     - no casa ⇒ vuelve como indicación; nunca una línea `unmatched`. Los añadidos no
//     pasan por el barrido fuzzy ni por la zona gris.
//  5. **La indicación de la línea**: las personalizaciones, en su orden, y detrás los
//     añadidos que no fueron línea, cada una sin blancos en los bordes, unidas con
//     `", "` y saneadas con `intakes.SanitizeNote` —la MISMA regla que escribe
//     `intake_items.customization` desde el carrito—. Si no cabe: `customization` vacía
//     (NO se trunca), Warning `indicacion_larga`, la línea vive, y un `Warn`
//     `match: la indicación de la línea no cabe y se descarta SIN truncar`.
//
// Los Warning de un ítem salen en este orden: `sin_producto`, `cantidad_invalida`,
// `zona_gris_caida` o `rango_sin_variante`, `indicacion_larga`.
//
// # AL FINAL
//
//   - **El envío, SIEMPRE la última línea** (D-041.11), aunque no haya ni un ítem:
//     KindShipping, sku `intakes.ShippingSKU`, cantidad 1 y lo que dicte
//     `intakes.DesiredShippingLine(in.Zones)` —la misma función que el cierre del
//     carrito—: con UNA zona, su etiqueta y su tarifa; con ninguna o con varias, la
//     etiqueta «por confirmar», `unit_price` nil y la nota `por confirmar zona`.
//   - **La nota del pedido** (`in.Note`), si no es vacía, se sanea con
//     `intakes.SanitizeNote` y va a `CustomerNote`; nunca crea líneas ni toca una
//     `customization`. Si no cabe NO se trunca y NO tumba nada: se descarta con el
//     `Warn` `match: la nota del pedido no cabe y se descarta SIN truncar`, sin Warning.
//   - **Persiste** UNA vez por `SaveStage(job.ID, …)` un artefacto de etapa
//     `intake.StageMatch` con `version = llm.ArtifactVersion`; lo persistido y lo
//     devuelto son lo mismo.
//   - Deja el `Info` `match: catálogo cruzado y líneas construidas` con `job_id`,
//     `items`, `lineas`, `total_parcial`, `lineas_sin_precio`, `avisos`,
//     `zona_gris_llamadas` y `catalogo_articulos`.
func (s *Match) Run(ctx context.Context, job intake.ClaimedJob, in MatchInput) (*MatchArtifact, error) {
	panic(pendiente.Implementar("stages.Match.Run"))
}
