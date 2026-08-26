package stages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/logger"
	"github.com/EduGoGroup/wapp-shared/textmatch"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/cart"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake/catalogo"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// match.go — LA ETAPA `match` (Plan 044 · Ola 3 · T3.2): lo que P4 dejó normalizado
// se cruza con el catálogo del tenant y se convierte en las LÍNEAS del presupuesto.
//
// Es la primera etapa del pipeline que NO habla con un modelo en su camino normal:
// las tres anteriores son llamadas al LLM con red en Go, y ésta es Go con una
// llamada al LLM SOLO en el escalón de zona gris, y solo por el ítem que los dos
// escalones deterministas no supieron resolver.
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 LAS DOS REGLAS QUE GOBIERNAN TODO LO DEMÁS
// ════════════════════════════════════════════════════════════════════════════
//
//  1. **EL MATCH NUNCA INVENTA** (REQ-17). Si el catálogo no tiene el producto, la
//     línea sale `unmatched` CON PRECIO VACÍO y el dueño decide. Jamás se elige «el
//     artículo más parecido» para que el presupuesto salga completo: un presupuesto
//     con el artículo equivocado es peor que uno con un renglón sin precio, porque
//     el segundo se ve y el primero se cobra.
//  2. **UNA PERSONALIZACIÓN NO ES UN ARTÍCULO** (D-044.14). «Sin sal» no se busca en
//     el catálogo —ni siquiera se ofrece a la cascada—, porque un fuzzy contra un
//     artículo «sal» o «salsa» crearía una línea con precio que el cliente no pidió.
//     Se separan ANTES de comparar nada y se pegan a su línea después.
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 DEUDA-044.16: UN ÍTEM MALO **NO** TIRA EL BORRADOR. LA DECISIÓN Y EL PORQUÉ
// ════════════════════════════════════════════════════════════════════════════
//
// El parseo de artefactos de la Ola 2 es TODO-O-NADA: un campo malo en un ítem tira
// el artefacto entero, y en campo eso mató 13 de 14 jobs por una clave `qty`
// ausente, llevándose por delante los ítems que estaban impecables. Esta etapa
// construye una línea POR ÍTEM, así que le toca decidir lo mismo otra vez.
//
// **Decisión: se DEGRADA el ítem y el resto vive.** Nunca al revés. Tres razones,
// y ninguna es «por prudencia»:
//
//   - **La unidad de daño es el ítem, no el pedido.** Tirar el borrador entero por
//     un ítem convierte un defecto de UNA línea en la pérdida de una solicitud REAL
//     de un cliente real. Es la misma aritmética que ya se pagó en campo.
//   - **Ya es la doctrina del pipeline, y romperla aquí sería la incoherencia.** P3
//     aísla el ítem que no pudo especificar y sigue (REQ-03/REQ-14); el worker
//     declara por escrito que «cero ideas no es fatal». Una etapa que sí matara el
//     job por un ítem haría que el mismo defecto tuviera dos desenlaces según en qué
//     etapa apareciera.
//   - **Degradar NO pierde la información: la ESCALA a un humano.** Un ítem
//     degradado sale como línea `unmatched` con su texto y su evidencia, más un
//     Aviso con el motivo. El dueño lo ve en la bandeja y lo arregla. Tirar el
//     borrador lo pierde en silencio.
//
// **Lo que SÍ hace fallar la etapa entera, y por qué no es lo mismo**: que no haya
// índice de catálogo (sin catálogo NINGÚN ítem puede casar, y un borrador con todo
// `unmatched` mentiría sobre el catálogo, no sobre el ítem) y que la persistencia
// falle (no habría artefacto que leer). Las dos son de infraestructura y las dos se
// reintentan; ninguna es un dato del cliente.
//
// ⚠️ **Un error de la ZONA GRIS tampoco mata el job**, y aquí sí se separa de P3 a
// propósito: en P3 el modelo es el ÚNICO productor y sin él no hay etapa, mientras
// que aquí es el TERCER escalón de una cascada cuyos dos primeros ya corrieron. Que
// el LLM no conteste significa «este ítem no se pudo rescatar», no «el pipeline está
// caído»: el ítem cae a `unmatched` con su Aviso y los otros nueve conservan su
// precio. Ver zonaGris en match_cascada.go.

// Los `kind` de una línea del presupuesto (design §7.4), vocabulario CERRADO.
const (
	// KindMatched es la línea que SÍ está en el catálogo: sku, etiqueta y precio
	// COPIADOS de él. `unit_price` puede seguir vacío si el artículo se vende por
	// variantes y el cliente no dijo cuál (ver VariantOptions).
	KindMatched = "matched"
	// KindUnmatched es el producto que el catálogo NO tiene. Precio VACÍO siempre:
	// es el renglón que el dueño precifica a mano.
	//
	// 🔴 NO es el kind de un añadido sin artículo (D-041.24): ése va como
	// `customization` de su línea. Usarlo aquí inventaría un renglón que el cliente
	// no pidió.
	KindUnmatched = "unmatched"
	// KindShipping es la línea de envío, que va SIEMPRE (D-041.11).
	KindShipping = "shipping"
)

// Motivos de un Aviso. Son vocabulario cerrado y describen el CASO REAL, no una
// familia: «la cantidad no es válida» y «el ítem no traía producto» piden cosas
// distintas al dueño y no se pueden fundir en un «ítem malo».
const (
	// MotivoSinProducto es el ítem que no trae ni producto ni evidencia: no hay nada
	// que enseñarle al dueño, así que no genera línea.
	MotivoSinProducto = "sin_producto"
	// MotivoCantidadInvalida es `qty <= 0`. La línea SOBREVIVE con la cantidad tal
	// como vino —no se maquilla a 1—, y el aviso dice que hay que mirarla.
	MotivoCantidadInvalida = "cantidad_invalida"
	// MotivoIndicacionLarga es la personalización que no cabe en MaxNoteRunes. No se
	// TRUNCA (REQ-33e: el final es donde va el alérgeno) y no se pierde en silencio:
	// la línea sale sin indicación y el aviso lo dice.
	MotivoIndicacionLarga = "indicacion_larga"
	// MotivoZonaGrisCaida es el ítem que la zona gris no pudo resolver porque falló
	// la llamada. Cae a `unmatched`, como el que no casó.
	MotivoZonaGrisCaida = "zona_gris_caida"
	// MotivoRangoSinVariante es el artículo que SÍ está en el catálogo pero cuyo
	// rango pedido no cae en ninguna de sus variantes («25-30 porciones» en un
	// artículo que solo se hace de 10 y 12).
	MotivoRangoSinVariante = "rango_sin_variante"
)

// NotaDePedido es la indicación que vale para el PEDIDO ENTERO («dejarlo en
// portería»), no para ninguna línea. Va a `intakes.customer_note` y NUNCA se
// reparte por las líneas (REQ-17e, D-041.19).
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 HOY NADIE LA PRODUCE, Y ESO ES UN HUECO DEL PIPELINE, NO DE ESTA ETAPA
// ════════════════════════════════════════════════════════════════════════════
//
// Es un tipo propio —y no un `string` suelto— para que el hueco se VEA en la línea
// del llamante, igual que ZonaPorDefecto hace visible la zona horaria que nadie ha
// elegido. Hoy el worker tiene que escribir `SinNotaDePedido`.
//
// LO QUE SE BUSCÓ Y NO HAY. Ninguna etapa anterior emite una nota de pedido:
// `llm.MainIdeas` tiene `wants` y `delivery_hint`; `llm.ItemSpec` tiene `notes`,
// que es «el detalle libre que acompaña AL ÍTEM»; `llm.Quantities` no añade
// ninguna. Los prompts de P2 y P3 tampoco la piden. Así que «y dejarlo en portería»
// llega hoy como un ítem más —P2 lo extrae como una idea, P3 la especifica— y esta
// etapa lo ve como un producto que el catálogo no tiene: sale línea `unmatched`.
//
// POR QUÉ NO SE ARREGLA AQUÍ CON UN LÉXICO. Distinguir «dejarlo en portería» de
// «torta vainilla con lluvia de colores» es SEMÁNTICO: los dos son un ítem sin
// match, sin cantidad propia y sin variante. Lo único que los separa es el
// significado. Una lista de marcadores («dejar en», «entregar en», «tocar el
// timbre»…) sería una regla de producto inventada aquí, y de las dos formas de
// fallar la mala es grave: un léxico ancho se traga un producto real y lo saca del
// presupuesto SIN QUE NADIE LO VEA. Es exactamente lo que la regla 1 prohíbe.
//
// LAS DOS SALIDAS, para quien tenga que decidirlo: (a) que P2 emita la nota de
// pedido —campo nuevo en `llm.MainIdeas` y regla nueva en su plantilla, o sea una
// release de `shared/wapp-shared/llm` por delante—; o (b) que sea el dueño quien la
// mueva desde la bandeja, que es donde ya está mirando el borrador. Las dos son
// decisiones de producto; ninguna cabe en T3.2.
type NotaDePedido string

// SinNotaDePedido es el valor que HOY debe pasar el llamante. Ver NotaDePedido:
// no es un default cómodo, es la ausencia de un productor.
const SinNotaDePedido NotaDePedido = ""

// ErrMatchSinCablear es la etapa a la que le falta una pieza. NO comparte texto con
// ErrSinCablear porque no comparte piezas: el match no necesita selector de vía —el
// LLM entra por la zona gris, que es opcional— y sí necesita el log y el store.
var ErrMatchSinCablear = errors.New("stages: la etapa match necesita log y store")

// ErrSinCatalogo es «llegó un job sin índice de catálogo». Es de infraestructura y
// se reintenta: sin catálogo ningún ítem puede casar, y un borrador con todo
// `unmatched` afirmaría que el tenant no vende nada de lo que el cliente pidió.
var ErrSinCatalogo = errors.New("stages: el match necesita el índice del catálogo del tenant")

// ErrSinCantidades es «llegó un job sin el artefacto de P4». Distinto de un
// artefacto con CERO ítems, que sí es legítimo (design §3.2) y produce un borrador
// con la sola línea de envío.
var ErrSinCantidades = errors.New("stages: el match necesita el artefacto de P4")

// OpcionVariante es una presentación concreta que el DUEÑO tiene que elegir, con su
// precio ya copiado del catálogo (design §7.4). Aparece cuando el artículo casó
// pero la variante no quedó determinada.
type OpcionVariante struct {
	// SKU es el sku COMPUESTO de la línea ("TORTA-CHOC#V2", D-041.4).
	SKU string `json:"sku"`
	// Label es la etiqueta compuesta ("Torta de chocolate — 12 porciones").
	Label string `json:"label"`
	// Price es el precio de ESA variante, copiado del catálogo.
	Price float64 `json:"price"`
}

// ProcedenciaMatch es de dónde salió el match de una línea (design §7.4:
// `"match": {"strategy": "fuzzy_osa", "confidence": 0.91}`). No es decorativa: es
// lo que permite mirar un presupuesto raro y saber si lo decidió una igualdad, una
// distancia de edición o el modelo.
type ProcedenciaMatch struct {
	// Strategy es el escalón que lo resolvió (EstrategiaSKU, EstrategiaExacta,
	// EstrategiaFuzzy, EstrategiaVariante, EstrategiaTag o el nombre de la zona gris).
	Strategy string `json:"strategy"`
	// Confidence es 0..1. En los escalones por clave es 1.0 —una igualdad no tiene
	// grados—; en el fuzzy es la similitud; en la zona gris, la que declare.
	Confidence float64 `json:"confidence"`
}

// Linea es un renglón del presupuesto, tal como lo verá el dueño en la bandeja y
// tal como T3.4 lo proyectará a `intake_revisions.payload` (design §7.4).
type Linea struct {
	// Kind es KindMatched, KindUnmatched o KindShipping.
	Kind string `json:"kind"`
	// SKU es el del catálogo, COPIADO. Vacío en `unmatched`.
	SKU string `json:"sku,omitempty"`
	// Label es la etiqueta. En `matched` viene del CATÁLOGO (es el nombre con el que
	// el dueño conoce su producto); en `unmatched` es lo que dijo el cliente, porque
	// no hay otra cosa que enseñar.
	Label string `json:"label"`
	// Qty es la cantidad de la línea.
	Qty int `json:"qty"`
	// UnitPrice es el precio unitario COPIADO del catálogo, o nil cuando lo tiene
	// que poner el dueño. Es puntero y NO lleva `omitempty` a propósito: design §7.4
	// escribe `"unit_price": null` y un 0 no significa lo mismo que «sin precio».
	UnitPrice *float64 `json:"unit_price"`
	// Customization son las indicaciones del cliente para ESTA línea, saneadas y
	// unidas. No lleva precio y NO ENTRA EN NINGÚN TOTAL (D-044.14).
	Customization string `json:"customization,omitempty"`
	// Range es el rango pedido, sin colapsar («10 o 12 porciones»).
	Range *llm.Range `json:"range,omitempty"`
	// UnitKind y PackageSize vienen de P4 y viajan por si el artículo que casó no es
	// el paquete: sin ellos, «un paquete de 30» se pierde en cuanto la línea toma el
	// nombre del catálogo, y el dueño no puede saber que el cliente pidió un pack.
	UnitKind    string `json:"unit_kind,omitempty"`
	PackageSize int    `json:"package_size,omitempty"`
	// VariantOptions son las presentaciones entre las que el DUEÑO elige. Cuando hay
	// más de una, UnitPrice va nil: elegir por el cliente es inventar.
	VariantOptions []OpcionVariante `json:"variant_options,omitempty"`
	// Match es la procedencia. Nil en `unmatched` y en `shipping`.
	Match *ProcedenciaMatch `json:"match,omitempty"`
	// Note es la nota de la PLATAFORMA sobre la línea, no del cliente («por
	// confirmar zona»). No confundir con Customization.
	Note string `json:"note,omitempty"`
	// Evidence es la frase literal del cliente que sostiene la línea. Va CIFRADA en
	// la revisión (D-044.13, T3.5); aquí viaja en claro dentro del artefacto del
	// job, que ya es material del literal.
	Evidence string `json:"evidence,omitempty"`
}

// Aviso es un ítem que no salió redondo, con su motivo. Lleva la POSICIÓN del ítem
// en el artefacto de P4 y no su texto: el texto ya está persistido una vez y
// duplicarlo aquí sería una segunda copia del literal que puede divergir de la
// primera (mismo criterio que ItemAislado en P3).
type Aviso struct {
	// ItemPos es la posición del ítem en `artifacts.p4.items`.
	ItemPos int `json:"item_pos"`
	// Reason es uno de los Motivo* de este fichero.
	Reason string `json:"reason"`
}

// ArtefactoMatch es lo que la etapa persiste bajo `artifacts.match`.
//
// Es el borrador SIN la parte que aún no es suya: las media refs las ancla T3.3 y
// la revisión la escribe T3.4. Lo que sí está entero es lo que decide el dinero —qué
// se cotiza, a qué precio y qué queda pendiente— y lo que decide la producción —qué
// personalización lleva cada línea—.
type ArtefactoMatch struct {
	// Version es la del artefacto (`llm.ArtifactVersion`), que exige
	// `intake.Artifact.Validate`.
	Version int `json:"version"`
	// Lines son las líneas EN ORDEN: por cada ítem de P4 su línea y, pegadas
	// detrás, las de sus añadidos facturables; el envío SIEMPRE al final.
	Lines []Linea `json:"lines"`
	// CustomerNote es la indicación del pedido entero, ya saneada. Va a
	// `intakes.customer_note`. Ver NotaDePedido: hoy nadie la produce.
	CustomerNote string `json:"customer_note,omitempty"`
	// Warnings son los ítems degradados (DEUDA-044.16). Vacío en el caso normal.
	Warnings []Aviso `json:"warnings,omitempty"`
	// GrayZoneCalls son las llamadas a la zona gris de ESTE job. Es el contador que
	// el criterio de T3.2 exige: como mucho UNA por ítem que los escalones
	// deterministas no cubrieron, y CERO por personalización.
	GrayZoneCalls int `json:"gray_zone_calls"`
}

// TotalParcial suma los precios de las líneas que YA tienen precio y cuenta las que
// siguen pendientes. Los dos números van juntos a propósito (design §7.5:
// «Total parcial: $X+$Y+$Z (1 línea pendiente de precio)»): un total suelto que
// callara las pendientes le diría al dueño que el presupuesto está cerrado cuando
// le falta la mitad.
//
// La `customization` NO entra: no tiene precio y no es una línea (D-044.14).
func (a *ArtefactoMatch) TotalParcial() (total float64, pendientes int) {
	for _, l := range a.Lines {
		if l.UnitPrice == nil {
			pendientes++
			continue
		}
		total += *l.UnitPrice * float64(l.Qty)
	}
	return total, pendientes
}

// EntradaMatch es todo lo que la etapa necesita del job, y viaja en un struct
// porque cada campo tiene un dueño distinto y conviene que se vea:
//
//   - Cantidades las deja P4;
//   - Indice lo construye la caché del catálogo UNA VEZ POR JOB y lo pasa el worker
//     (T3.7, criterio (a)): esta etapa NO lee `tenant_content`, ni podría —el
//     `*Indice` no tiene puerto de lectura—;
//   - Zonas salen de `tenant_settings.shipping_zones`, leídas también una vez por
//     job;
//   - Nota es la del pedido entero, que hoy nadie produce (ver NotaDePedido).
type EntradaMatch struct {
	Cantidades *llm.Quantities
	Indice     *catalogo.Indice
	Zonas      []intakes.ShippingZone
	Nota       NotaDePedido
}

// Match es la etapa del CRUCE CON EL CATÁLOGO (Plan 044 · T3.2).
//
// Sus piezas: el log, el store donde deja el artefacto, el comparador DETERMINISTA
// del bucle y —opcional— la zona gris. Que la zona gris sea opcional no es una
// comodidad: sin ella la etapa sigue produciendo un borrador correcto, solo que con
// más renglones `unmatched` para el dueño.
type Match struct {
	log      logger.Logger
	store    StageStore
	cmp      textmatch.Comparator
	zonaGris textmatch.GrayZone
}

// OpciónMatch configura la etapa. Es un tipo aparte de Opción (que es la de los
// plazos de las etapas LLM) porque no configura lo mismo y mezclarlas dejaría
// construir un match «con plazo por llamada», que aquí no significa nada.
type OpciónMatch func(*Match)

// ConZonaGris cablea el TERCER escalón de la cascada: el juicio caro para lo que ni
// la igualdad ni la distancia de edición resolvieron.
//
// 🔴 NO entra en el bucle de comparación. Se consulta como mucho UNA VEZ por ítem
// no cubierto, después de que los escalones deterministas hayan terminado con TODOS
// los candidatos. Ver zonaGris en match_cascada.go.
func ConZonaGris(gz textmatch.GrayZone) OpciónMatch {
	return func(m *Match) { m.zonaGris = gz }
}

// ConComparador sustituye el comparador determinista del bucle. Existe para los
// tests —espiar QUÉ textos se comparan es la única forma de demostrar que una
// personalización nunca llega al matcher— y para el día que haya una segunda
// cascada. Pasar nil no hace nada: la etapa no se queda sin comparador.
func ConComparador(cmp textmatch.Comparator) OpciónMatch {
	return func(m *Match) {
		if cmp != nil {
			m.cmp = cmp
		}
	}
}

// NewMatch construye la etapa. Devuelve ErrMatchSinCablear si le falta el log o el
// store.
//
// El comparador por defecto es CascadaPorDefecto(): `Exact → Fuzzy(0,85)`, sin
// tercer escalón. Ver ahí por qué el umbral es 0,85 y no 0,80 (D-044.45).
func NewMatch(log logger.Logger, store StageStore, opts ...OpciónMatch) (*Match, error) {
	if log == nil || store == nil {
		return nil, ErrMatchSinCablear
	}
	m := &Match{log: log, store: store, cmp: CascadaPorDefecto()}
	for _, o := range opts {
		o(m)
	}
	return m, nil
}

// Run cruza los ítems de P4 con el catálogo y persiste las líneas del presupuesto.
//
// # EL ORDEN DE LAS LÍNEAS ES CONTRATO
//
// Por cada ítem, su línea y detrás las de sus añadidos facturables; el envío
// SIEMPRE el último. Es el orden con el que design §7.5 pinta la bandeja, y el que
// hace que «+ decoración infantil» se lea debajo de la torta a la que acompaña.
//
// # LO QUE NO HACE
//
// No lee de ninguna parte —el índice y las zonas llegan ya leídos, una vez por
// job— y no calcula totales: el total es una VISTA de las líneas (TotalParcial) y
// congelarlo en el artefacto crearía un segundo número que se desincroniza del
// primero en cuanto el dueño toque un precio.
func (s *Match) Run(ctx context.Context, job intake.ClaimedJob, in EntradaMatch) (*ArtefactoMatch, error) {
	if in.Cantidades == nil {
		return nil, ErrSinCantidades
	}
	if in.Indice == nil {
		return nil, ErrSinCatalogo
	}

	art := &ArtefactoMatch{
		Version: llm.ArtifactVersion,
		Lines:   make([]Linea, 0, len(in.Cantidades.Items)+1),
	}
	esc := nuevoEscaner(in.Indice)

	for pos, it := range in.Cantidades.Items {
		s.lineasDeItem(ctx, art, esc, pos, it)
	}

	art.Lines = append(art.Lines, lineaDeEnvio(in.Zonas))
	s.notaDePedido(art, in.Nota, job.ID)

	if err := s.persistir(ctx, job.ID, art); err != nil {
		return nil, err
	}

	total, pendientes := art.TotalParcial()
	s.log.Info("match: catálogo cruzado y líneas construidas",
		"job_id", job.ID, "stage", intake.StageMatch,
		"items", len(in.Cantidades.Items), "lineas", len(art.Lines),
		"total_parcial", total, "lineas_sin_precio", pendientes,
		"avisos", len(art.Warnings), "zona_gris_llamadas", art.GrayZoneCalls,
		"catalogo_articulos", in.Indice.Articulos())
	return art, nil
}

// notaDePedido sanea la indicación del pedido entero y la deja en la cabecera.
//
// 🔴 REUSA cart.SanitizeNote, que está exportada EXACTAMENTE para esto: el cart
// numérico y este pipeline son los dos productores de `intakes.customer_note` y
// `intake_items.customization`, y una copia de la regla haría que la columna
// tuviera dos contratos y ninguno fuera verdad (note.go lo dice en su cabecera).
//
// Una nota demasiado larga NO se trunca y NO tumba nada: se descarta con un Warn.
// Truncar «…y sin maní» pierde justo el final, que es donde va el alérgeno
// (REQ-33e); y tirar el borrador entero por la nota sería perder el pedido por el
// margen.
func (s *Match) notaDePedido(art *ArtefactoMatch, nota NotaDePedido, jobID string) {
	if nota == "" {
		return
	}
	saneada, err := cart.SanitizeNote(string(nota))
	if err != nil {
		// El error NO cita la nota: es texto del cliente (ADR-0034).
		s.log.Warn("match: la nota del pedido no cabe y se descarta SIN truncar",
			"job_id", jobID, "stage", intake.StageMatch, "error", err.Error())
		return
	}
	art.CustomerNote = saneada
}

// persistir deja el artefacto bajo `artifacts.match`. Mismo patrón que las etapas
// LLM: si el UPDATE no toca la fila es que el job ya no está en `processing` —lo
// soltó el watchdog, o lo terminó otro— y eso NO es un error de esta etapa.
func (s *Match) persistir(ctx context.Context, jobID string, art *ArtefactoMatch) error {
	payload, err := json.Marshal(art)
	if err != nil {
		return fmt.Errorf("match: serializar el artefacto: %w", err)
	}
	guardado, err := s.store.SaveStage(ctx, jobID, intake.Artifact{
		Stage:   intake.StageMatch,
		Payload: payload,
	})
	if err != nil {
		return fmt.Errorf("match: persistir el artefacto: %w", err)
	}
	if !guardado {
		return ErrJobFueraDeProcessing
	}
	return nil
}
