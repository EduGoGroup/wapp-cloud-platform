// Porta internal/intake/stages/p4.go @ 4cd9cfb

package stages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/evidence"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
)

// DefaultZone (antes `ZonaPorDefecto`) es la zona que gobierna el cálculo de fechas
// mientras el producto no elija una. Es UTC A PROPÓSITO: no es la zona de ningún
// negocio, es la ausencia de decisión (DEUDA-044.11: no hay zona horaria por tenant en
// ninguna parte), y es la única que hoy coincide con la fecha de referencia que imprime
// el prompt compartido, que fuerza UTC.
//
// Se exporta para que el llamante tenga que ESCRIBIRLA: un default implícito dentro del
// constructor haría invisible la decisión que falta.
//
// 🔴 EL HUECO, QUE QUEDA FIJADO: con UTC, un mensaje del lunes a las 22:00 en UTC−3 se
// fecha con el martes.
var DefaultZone = time.UTC

// ErrNoTimeZone (antes `ErrSinZonaHoraria`) es «se intentó construir P4 sin zona». No
// cae en ErrNotWired porque no es una pieza que falte por descuido: es la decisión de
// producto que aún no existe, y quien construye la etapa tiene que elegir
// explícitamente —hoy, DefaultZone— en vez de heredar un cero.
//
// El texto es el del paquete viejo, literal: nombra `stages.ZonaPorDefecto`, que hoy es
// DefaultZone.
var ErrNoTimeZone = errors.New("stages: P4 necesita la zona horaria que gobierna el cálculo de fechas (hoy, stages.ZonaPorDefecto)")

// P4 es la etapa de la NORMALIZACIÓN: coge las especificaciones que dejó P3 y las
// convierte en cantidades comparables —unidades, paquetes y rangos— y en UNA FECHA
// ABSOLUTA de entrega. Es la última etapa antes de que el match toque el catálogo.
//
// El modelo aporta EXACTAMENTE cuatro campos por ítem: `qty`, `range`, `unit_kind` y
// `package_size`. Todo lo demás —producto, añadidos candidatos, personalizaciones,
// notas— se copia de la spec de P3 sin pasar por el modelo, y la FECHA la calcula Go
// (ResolveDate): ni una suma de días la hace el modelo.
type P4 struct {
	log    logger.Logger
	sel    ProviderSelector
	store  StageStore
	zone   *time.Location
	limits callLimits
}

// NewP4 construye la etapa. Devuelve ErrNotWired si `log`, `sel` o `store` es nil y, con
// esas tres piezas puestas, ErrNoTimeZone si `zone` es nil; en los dos casos la etapa
// devuelta es nil. La zona es OBLIGATORIA y posicional —hoy el llamante pasa
// DefaultZone—; el plazo por llamada llega por opción (WithCallTimeout).
func NewP4(log logger.Logger, sel ProviderSelector, store StageStore, zone *time.Location, opts ...Option) (*P4, error) {
	if log == nil || sel == nil || store == nil {
		return nil, ErrNotWired
	}
	if zone == nil {
		return nil, ErrNoTimeZone
	}
	return &P4{log: log, sel: sel, store: store, zone: zone, limits: newCallLimits(opts)}, nil
}

// Run normaliza un job YA RECLAMADO y devuelve el artefacto tal como quedó persistido,
// que es lo que consumen el match y el borrador. `literal` es el `source_text` EN
// CLARO; `items` son las specs que P3 dejó vivas, en su orden; `delivery` es la pista de
// entrega que P2 etiquetó, o nil si el cliente no dijo cuándo.
//
// En este orden:
//
//  1. `literal == ""` ⇒ ErrNoLiteral, sin pedir el provider, sin llamar al modelo y sin
//     persistir.
//  2. **La fecha se calcula EN GO, antes de llamar al modelo y aunque no haya ítems**:
//     `ResolveDate(delivery.Text, job.MessageTS en la zona de la etapa)`. Si resuelve,
//     `delivery_date` es esa fecha (`AAAA-MM-DD`) y `delivery_date_basis` es
//     `message_ts=` más el día del mensaje EN LA ZONA de la etapa. No se lee el reloj:
//     dos pasadas del mismo job dan el mismo artefacto byte a byte. Tres desenlaces
//     dejan el artefacto SIN fecha ni base, y ninguno es un fallo: el job no trae
//     `message_ts`, no hay pista, o la pista no se reconoce. Los avisos no llevan la
//     pista, que son palabras del cliente.
//  3. **Cero ítems no es un fallo**: ni se pide el provider ni se llama al modelo, y el
//     artefacto (con su fecha, si la hay) se persiste igual.
//  4. Con ítems, pide el provider UNA vez con `(job.Key.TenantID, job.Key.SessionID)`
//     —`p4: elegir el proveedor del tenant: %w`— y hace UNA llamada
//     `NormalizeQuantities` con el literal ENTERO, las specs y `job.MessageTS`, a
//     `llm.TemperatureGreedy`. No hay reintento aquí: es del worker. Con
//     WithCallTimeout, el plazo acaba donde acaba la llamada. Si falla:
//     `p4: pedir la normalización de cantidades: %w`, con su familia intacta.
//  5. Lee la salida con `llm.ParseQuantities`. Ilegible —o con un `qty` omitido, que el
//     parser rechaza— ⇒ `p4: la salida del modelo no es un artefacto P4 legible: %w`
//     (familia `llm.ErrLLMQuality`) y no se persiste nada: P4 NO maquilla un `qty` 0
//     como 1.
//  6. **Funde POR POSICIÓN, y la cuenta la manda P3**: sale un ítem por cada spec, ni
//     uno más ni uno menos. El ítem que el modelo no devolvió sale con la normalización
//     neutra —los datos de P3 y `qty` 1, sin rango ni paquete—; los que devuelva de más
//     se descartan con un `Warn` (`los sobrantes se descartan`).
//  7. De cada ítem del modelo se copian `qty`, `range`, `unit_kind` y `package_size`;
//     el producto y lo demás son SIEMPRE los de P3. Los rangos NO se colapsan. La
//     `evidence` del modelo sustituye a la de P3 solo si aparece en el literal (regla
//     de `evidence`); si no, se conserva la de P3 y el ítem sigue vivo.
//  8. **«Paquete de N» JAMÁS es `qty` N**: si la evidencia o las notas del ítem dicen
//     «paquete(s) de N» (hasta 40 caracteres sin dígitos entre «de» y el número) y el
//     `qty` resultante es EXACTAMENTE ese N, con N > 1, se corrige a `qty` 1,
//     `unit_kind` `package` y `package_size` N —lleve o no la etiqueta de paquete—.
//  9. La fecha que proponga el modelo NO se usa: manda la de Go, y si no coinciden
//     queda un `Warn` (`no coincide con la que calculó Go`).
//  10. Persiste por `SaveStage(job.ID, …)` UN artefacto de etapa `intake.StageP4`, que
//     `llm.ParseQuantities` tiene que poder volver a leer EN TODOS los caminos (I-CP-1
//     visto desde la etapa: lo que P4 escribe nunca trae un valor que su propio
//     validador rechace). Error ⇒ `p4: persistir el artefacto: %w`; `(false, nil)` ⇒
//     ErrJobNotProcessing.
//  11. Deja el `Info` `p4: cantidades y fecha normalizadas y persistidas` con `items` y
//     `con_fecha`.
//
// Ningún aviso lleva texto del cliente: ni la pista, ni una evidencia, ni un producto.
// Con cualquier error el artefacto devuelto es nil.
func (s *P4) Run(ctx context.Context, job intake.ClaimedJob, literal string, items []llm.ItemSpec, delivery *llm.Hint) (*llm.Quantities, error) {
	if literal == "" {
		return nil, ErrNoLiteral
	}

	art := &llm.Quantities{
		Version: llm.ArtifactVersion,
		Items:   make([]llm.NormalizedItem, 0, len(items)),
	}
	s.setDate(art, job, delivery)

	if len(items) > 0 {
		if err := s.normalize(ctx, job, literal, items, art); err != nil {
			return nil, err
		}
	}

	if err := s.persist(ctx, job.ID, art); err != nil {
		return nil, err
	}
	s.log.Info("p4: cantidades y fecha normalizadas y persistidas",
		"job_id", job.ID, "stage", intake.StageP4,
		"items", len(art.Items), "con_fecha", art.DeliveryDate != "")
	return art, nil
}

// omittedQty (antes `qtyOmitida`) es la cantidad de un ítem del que nadie dijo cuántos:
// UNO.
//
// 🔴 ESTA CONSTANTE NO ARREGLA UN `qty: 0` QUE VENGA DEL MODELO, y conviene saberlo:
// `llm.ParseQuantities` rechaza cualquier `qty < 1` como fallo de calidad, así que una
// salida con la cantidad omitida NO llega hasta aquí: muere en el parser y el job se
// reintenta. La regla «qty omitida ⇒ 1» del plan la aplica el PROMPT, con el parser de
// red; lo único que esta constante decide es la cantidad del ítem que el modelo NO
// devolvió (ver merge).
const omittedQty = 1

// basisPrefix (antes `basisPrefijo`) es el prefijo literal de `delivery_date_basis`
// (design §7.3: `"message_ts=2026-07-13"`). Deja escrito en el artefacto DESDE QUÉ fecha
// se calculó, que es lo que hace auditable que no salió del reloj del worker.
const basisPrefix = "message_ts="

// setDate (antes `fechar`) resuelve la fecha de entrega EN GO y la escribe en el
// artefacto junto con la base desde la que se calculó. No llama al modelo y no lee el
// reloj.
//
// Se calcula aunque no haya ítems y antes de llamar al modelo porque no depende de él:
// la expresión ya viene etiquetada por P2 y la aritmética es de Go. Un job cuyo P3 se
// quedó sin ítems —que es legal, design §3.2— conserva así la fecha que el cliente
// pidió, y el dueño la ve en la bandeja aunque tenga que escribir las líneas a mano.
//
// Los tres desenlaces, y ninguno es un fallo del job:
//
//   - **sin `message_ts`** (columna NULL: la 0072 la deja anulable) ⇒ no hay base y no
//     hay fecha. Es lo único honesto: la alternativa sería usar `now()`, que es
//     exactamente lo que D-044.9 prohíbe;
//   - **sin pista** (el cliente no dijo cuándo) ⇒ no hay fecha, y el dueño pregunta;
//   - **pista que no se reconoce** («cuando puedas») ⇒ no hay fecha. Ver ResolveDate.
//
// 🔴 EL AVISO NO LLEVA LA PISTA. `Hint.Text` son las palabras del cliente y no salen
// por el log jamás (ADR-0034, INV-6, la misma regla que P2 y P3): el operador se entera
// de que hubo pista y de que no se pudo resolver, y eso basta para investigar.
func (s *P4) setDate(art *llm.Quantities, job intake.ClaimedJob, delivery *llm.Hint) {
	if job.MessageTS.IsZero() {
		if delivery != nil {
			s.log.Warn("p4: el job no trae message_ts; el presupuesto sale SIN fecha en vez de con la de hoy",
				"job_id", job.ID, "stage", intake.StageP4)
		}
		return
	}
	base := job.MessageTS.In(s.zone)
	if delivery == nil {
		return
	}
	date, ok := ResolveDate(delivery.Text, base)
	if !ok {
		s.log.Warn("p4: la pista de entrega no se pudo resolver a una fecha; el presupuesto sale sin fecha",
			"job_id", job.ID, "stage", intake.StageP4)
		return
	}
	art.DeliveryDate = date.Format(time.DateOnly)
	art.DeliveryDateBasis = basisPrefix + base.Format(time.DateOnly)
}

// normalize (antes `normalizar`) hace LA llamada de la etapa y funde lo que conteste
// con lo que P3 ya sabía. Devuelve error solo cuando la llamada o la lectura fallan: en
// los dos casos el job vuelve a la cola con sus artefactos intactos y lo recoge el
// backoff del worker.
//
// El `MessageTS` viaja al prompt aunque la fecha no salga de ahí: el modelo la necesita
// para no inventarse una fecha absurda al normalizar («para el jueves» en las notas), y
// el prompt la imprime como referencia. Lo que P4 hace con `delivery_date` de vuelta es
// COMPARARLA, no creérsela.
func (s *P4) normalize(ctx context.Context, job intake.ClaimedJob, literal string, items []llm.ItemSpec, art *llm.Quantities) error {
	prov, err := s.sel.For(ctx, job.Key.TenantID, job.Key.SessionID)
	if err != nil {
		return fmt.Errorf("p4: elegir el proveedor del tenant: %w", err)
	}

	raw, err := s.askQuantities(ctx, prov, job, literal, items)
	if err != nil {
		return err
	}

	out, err := llm.ParseQuantities(raw)
	if err != nil {
		// 🔴 El error NO cita `raw`: la salida del modelo lleva frases del cliente.
		return fmt.Errorf("p4: la salida del modelo no es un artefacto P4 legible: %w", err)
	}

	art.Items = s.merge(job.ID, literal, items, out.Items)
	if out.DeliveryDate != art.DeliveryDate {
		s.log.Warn("p4: la fecha que propuso el modelo no coincide con la que calculó Go; manda la de Go (D-044.9)",
			"job_id", job.ID, "stage", intake.StageP4,
			"el_modelo_propuso_fecha", out.DeliveryDate != "", "go_calculo_fecha", art.DeliveryDate != "")
	}
	return nil
}

// askQuantities (antes `pedirCantidades`) es LA llamada de P4, acotada por su propio
// plazo. Extraída por el mismo motivo que en P2 y P3: el `defer cancel()` tiene que
// cerrar el plazo donde acaba la llamada, no arrastrarlo hasta la escritura del
// artefacto.
func (s *P4) askQuantities(ctx context.Context, prov llm.LLMProvider, job intake.ClaimedJob,
	literal string, items []llm.ItemSpec) (json.RawMessage, error) {
	callCtx, cancel := s.limits.bound(ctx)
	defer cancel()
	raw, err := prov.NormalizeQuantities(callCtx,
		llm.NormalizeQuantitiesInput{SourceText: literal, Items: items, MessageTS: job.MessageTS},
		llm.Options{Temperature: llm.TemperatureGreedy})
	if err != nil {
		return nil, fmt.Errorf("p4: pedir la normalización de cantidades: %w", err)
	}
	return raw, nil
}

// merge (antes `fundir`) empareja POR POSICIÓN lo que devolvió el modelo con lo que P3
// ya sabía, y devuelve un ítem normalizado por cada ítem de P3: ni uno más, ni uno
// menos.
//
// # POR QUÉ LA CUENTA LA MANDA P3 Y NO EL MODELO
//
// Porque los ítems de P3 son PETICIONES REALES del cliente: cada uno pasó el anclaje de
// su etapa. Si el modelo devuelve menos, el que falta NO se pierde —sale con la
// normalización neutra, `qty` 1 y los datos de P3— porque perderlo sería quitarle al
// cliente algo que pidió y que el sistema ya había reconocido. Si devuelve más, los
// sobrantes se descartan: la única forma de tener más ítems que peticiones es que el
// modelo haya partido uno en dos o repetido otro, y las dos cosas acaban en una línea
// de más COBRADA. Es la misma asimetría que P3 aplica dentro de una llamada («se
// conserva la primera»), y por el mismo motivo: perder una repetición es recuperable,
// duplicar una línea con precio no.
//
// # LA EVIDENCIA: LA DEL MODELO SI SE SOSTIENE, Y SI NO LA DE P3
//
// La regla es la de `evidence`, la misma y desde el mismo sitio. La RESPUESTA vuelve a
// ser distinta que en P2 y en P3, y por la misma lógica: aquí ya no hay nada que
// descartar ni que aislar —el ítem está probado desde P3—, así que una evidencia que el
// modelo se invente simplemente NO SUSTITUYE a la que ya había. El ítem sigue vivo y el
// artefacto nunca guarda una frase que el cliente no escribió.
func (s *P4) merge(jobID, literal string, specs []llm.ItemSpec, norm []llm.NormalizedItem) []llm.NormalizedItem {
	if len(norm) > len(specs) {
		s.log.Warn("p4: el modelo devolvió más ítems de los que P3 dejó vivos; los sobrantes se descartan",
			"job_id", jobID, "stage", intake.StageP4, "descartados", len(norm)-len(specs))
	}
	normText := evidence.Normalize(literal)
	out := make([]llm.NormalizedItem, 0, len(specs))
	for i := range specs {
		it := neutral(specs[i])
		if i < len(norm) {
			s.applyQuantities(&it, norm[i], normText, jobID, i)
		} else {
			s.log.Warn("p4: el modelo no devolvió este ítem; sale con la normalización neutra y el pedido no lo pierde",
				"job_id", jobID, "stage", intake.StageP4, "item_pos", i)
		}
		out = append(out, it)
	}
	return out
}

// neutral (antes `neutro`) es la normalización de un ítem al que el modelo no aportó
// nada: los datos de P3 tal cual y UNA unidad. Sin rango y sin paquete, porque
// inventarlos sería peor que no tenerlos: el dueño ve «1× torta» y corrige, que es
// exactamente lo que la bandeja existe para permitir.
func neutral(spec llm.ItemSpec) llm.NormalizedItem {
	return llm.NormalizedItem{
		Product:         spec.Product,
		Qty:             omittedQty,
		AddonCandidates: spec.AddonCandidates,
		Customizations:  spec.Customizations,
		Notes:           spec.Notes,
		Evidence:        spec.Evidence,
	}
}

// applyQuantities (antes `aplicarCantidades`) copia del ítem del modelo los CUATRO
// campos que le tocan y la evidencia si se sostiene. `qty`, `range` y `package_size`
// llegan ya comprobados por `llm.ParseQuantities` (cantidad >= 1, rango en orden con
// unidad, paquete con al menos una unidad), así que aquí no se vuelven a validar: una
// segunda red con el mismo síntoma taparía a los tests de conducta de la primera.
//
// El producto se deja el de P3 a propósito: lo de P3 ya pasó el anclaje, y un modelo
// chico al que se le deja tocar el nombre del producto lo funde con el de al lado y el
// matcher del catálogo cobra otra cosa. Cuando el modelo lo reescribe se avisa —sin
// decir con qué, que sería texto del cliente—, porque un producto renombrado es la
// única señal barata de que el modelo REORDENÓ los ítems y el emparejamiento por
// posición está pegando cantidades al ítem equivocado.
func (s *P4) applyQuantities(it *llm.NormalizedItem, fromModel llm.NormalizedItem, normText, jobID string, pos int) {
	it.Qty = fromModel.Qty
	it.Range = fromModel.Range
	it.UnitKind = fromModel.UnitKind
	it.PackageSize = fromModel.PackageSize

	if evidence.Contains(normText, fromModel.Evidence) {
		it.Evidence = fromModel.Evidence
	} else {
		s.log.Warn("p4: la evidencia que devolvió el modelo no aparece en el literal del cliente; se conserva la de P3",
			"job_id", jobID, "stage", intake.StageP4, "item_pos", pos)
	}
	if evidence.Normalize(fromModel.Product) != evidence.Normalize(it.Product) {
		s.log.Warn("p4: el modelo renombró el producto; se conserva el de P3 (si esto se repite, revisa si reordenó los ítems)",
			"job_id", jobID, "stage", intake.StageP4, "item_pos", pos)
	}
	if fixPackage(it) {
		s.log.Warn("p4: el modelo confundió el TAMAÑO del paquete con la cantidad; se corrige a un paquete (design §7.3)",
			"job_id", jobID, "stage", intake.StageP4, "item_pos", pos)
	}
}

// rePackageOf (antes `rePaqueteDe`) saca el tamaño de un «paquete de N» del texto del
// cliente. Los 40 caracteres SIN DÍGITOS de en medio son el caso real del fixture de
// Ambar —«un paquete de tequeños congelados de 30»—, donde el número no va pegado a
// «de»: sin ese hueco la regla no vería el único paquete que hay en el caso. Y son SIN
// dígitos a propósito, de modo que en «2 paquetes de 30 y 4 cajas de 6» el 30 no se
// confunda con el 6.
var rePackageOf = regexp.MustCompile(`paquetes? de [^0-9]{0,40}(\d{1,4})\b`)

// fixPackage (antes `corregirPaquete`) es LA RED EN GO de la regla «paquete de 30 ⇒
// package_size 30, JAMÁS qty 30» (design §7.3). Devuelve true si tuvo que corregir.
//
// # POR QUÉ HACE FALTA UNA RED, SI EL PROMPT YA LO DICE
//
// Porque el prompt es una petición y esto es una garantía. La confusión —cobrar 30
// tortas donde el cliente pidió un paquete de 30 tequeños— es el error más caro que
// puede cometer esta etapa: multiplica el presupuesto por treinta. Y es EXACTAMENTE el
// error que un modelo chico comete, porque el número está pegado al producto.
//
// # CÓMO DECIDE, Y POR QUÉ NO SE PASA DE LISTA
//
// Dispara solo cuando el texto del cliente dice «paquete(s) de N» Y la cantidad que
// devolvió el modelo es EXACTAMENTE ese N, con N > 1. Esa coincidencia no es
// interpretable de otra forma: «2 paquetes de 30» con `qty` 2 no dispara (2 ≠ 30), y un
// «30 paquetes de 30» —que sí dispararía— es un pedido que nadie escribe. La corrección
// es la del §7.3 y es completa: una unidad, `unit_kind` de paquete y el tamaño dentro.
//
// 🔴 NO mira `unit_kind` para decidir. Un `qty:30, unit_kind:"package", package_size:30`
// pasa el parser compartido sin una queja y son NOVECIENTAS unidades: el fallo caro
// cabe con y sin la etiqueta puesta, así que la red tiene que cubrir los dos.
func fixPackage(it *llm.NormalizedItem) bool {
	m := rePackageOf.FindStringSubmatch(evidence.Normalize(it.Evidence + " " + it.Notes))
	if m == nil {
		return false
	}
	size, err := strconv.Atoi(m[1])
	if err != nil || size <= 1 || it.Qty != size {
		return false
	}
	it.Qty = omittedQty
	it.UnitKind = llm.UnitKindPackage
	it.PackageSize = size
	return true
}

// persist (antes `persistir`) serializa el artefacto y lo deja en la máquina de estados.
//
// Se serializa el artefacto DEL CLOUD y no la salida cruda del modelo por el mismo
// motivo que en P2 y P3: lo que se guarda es lo que el match se va a creer, y el crudo
// llevaría dentro la fecha que el modelo propuso —descartada de boquilla, presente en
// la base— y las evidencias que no se sostienen.
//
// No se revalida el `version` aquí: la puerta es `intake.Artifact.Validate`, dentro de
// `SaveStage`.
func (s *P4) persist(ctx context.Context, jobID string, art *llm.Quantities) error {
	payload, err := json.Marshal(art)
	if err != nil {
		return fmt.Errorf("p4: serializar el artefacto: %w", err)
	}
	saved, err := s.store.SaveStage(ctx, jobID, intake.Artifact{
		Stage:   intake.StageP4,
		Payload: payload,
	})
	if err != nil {
		return fmt.Errorf("p4: persistir el artefacto: %w", err)
	}
	if !saved {
		return ErrJobNotProcessing
	}
	return nil
}
