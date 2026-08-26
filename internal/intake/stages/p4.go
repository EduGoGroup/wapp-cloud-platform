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

	"github.com/EduGoGroup/wapp-cloud-platform/internal/evidence"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake"
)

// ════════════════════════════════════════════════════════════════════════════
// 🔴 LA ZONA HORARIA: LO QUE SE INVESTIGÓ, LO QUE SE DECIDIÓ Y EL HUECO QUE QUEDA
// ════════════════════════════════════════════════════════════════════════════
//
// «El miércoles de la semana que viene» es un DÍA, y un día solo existe dentro de una
// zona horaria. `intake_jobs.message_ts` es `TIMESTAMPTZ` (migración 0072), o sea un
// INSTANTE: Postgres no guarda el desfase con el que llegó, lo normaliza y lo devuelve
// en la zona de la sesión. Dos zonas distintas dan DÍAS distintos para el mismo
// instante, y el error es silencioso y permanente.
//
// QUÉ SE BUSCÓ, Y QUÉ NO HAY. No existe ninguna zona horaria por tenant en este
// repositorio: `public.tenant_settings` tiene `page_size`, `order_ttl_seconds`,
// `shipping_zones`, `deposit_template`, `deposit_due_days`, `buyer_fields`, los dos TTL
// de evento y `conversation_ttl_seconds` — y ni una columna de zona, locale ni desfase.
// Tampoco hay un `time.LoadLocation` en todo el código de producción, ni el Plan 044 ni
// ningún ADR fijan una. **Elegir la zona del negocio es una decisión de producto que
// nadie ha tomado**, y fabricar aquí un `America/…` sería inventarme el día del cliente.
//
// LO QUE SE DECIDIÓ, Y POR QUÉ NO ES UN NÚMERO INVENTADO. La zona entra por el
// constructor y es OBLIGATORIA: `NewP4` se niega a nacer sin ella (ErrSinZonaHoraria).
// El valor que hoy debe pasarle el llamante es `ZonaPorDefecto` = **UTC**, y no por
// comodidad: es la única zona bajo la que Go y el PROMPT dicen lo mismo.
// `BuildNormalizeQuantitiesPrompt` hace `in.MessageTS.UTC()` antes de imprimir la fecha
// de referencia (`shared/wapp-shared/llm/prompt.go:205`), así que el modelo ve el día
// UTC llueva lo que llueva. Con la zona en UTC, la fecha de referencia del prompt y la
// base de Go son el MISMO día; con cualquier otra, empiezan a discrepar en los mensajes
// de la noche sin que nadie lo note.
//
// EL HUECO, DICHO CON SUS CONSECUENCIAS. Mientras la zona sea UTC, un mensaje escrito
// a las 22:00 en UTC−3 se fecha con el día SIGUIENTE. En «el miércoles de la semana que
// viene» casi nunca importa (lunes y martes caen en la misma semana ISO), pero hay dos
// casos en los que se va un día o siete:
//
//   - «mañana» escrito el lunes a las 22:00 (UTC−3) ⇒ base martes ⇒ se promete el
//     miércoles: **un día tarde**;
//   - «el lunes de la semana que viene» escrito un DOMINGO por la noche ⇒ la base ya es
//     lunes ⇒ la «semana que viene» es la siguiente: **siete días tarde**.
//
// QUÉ HAY QUE HACER EL DÍA QUE SE DECIDA, y son TRES cosas, no una: (1) una columna de
// zona en `tenant_settings` con su migración; (2) pasarla aquí en vez de
// `ZonaPorDefecto`; y (3) 🔴 **cambiar `BuildNormalizeQuantitiesPrompt`, que vive en
// `shared/wapp-shared` y fuerza UTC** — sin eso, el modelo seguiría razonando sobre otro
// día que el nuestro. Es una release de `shared` por delante, y por eso NO se hace aquí.
// ════════════════════════════════════════════════════════════════════════════

// ZonaPorDefecto es la zona que gobierna el cálculo de fechas mientras el producto no
// elija una. Es UTC A PROPÓSITO —ver el bloque de arriba—: no es la zona de ningún
// negocio, es la ausencia de decisión, y es la única que hoy coincide con la fecha de
// referencia que imprime el prompt compartido.
//
// Se exporta para que el llamante (el worker de T2.5) tenga que ESCRIBIRLA: un default
// implícito dentro del constructor haría invisible la decisión que falta.
var ZonaPorDefecto = time.UTC

// ErrSinZonaHoraria es «se intentó construir P4 sin zona». No cae en ErrSinCablear
// porque no es una pieza que falte por descuido: es la decisión de producto que aún no
// existe, y quien construye la etapa tiene que elegir explícitamente —hoy,
// ZonaPorDefecto— en vez de heredar un cero.
var ErrSinZonaHoraria = errors.New("stages: P4 necesita la zona horaria que gobierna el cálculo de fechas (hoy, stages.ZonaPorDefecto)")

// qtyOmitida es la cantidad de un ítem del que nadie dijo cuántos: UNO.
//
// 🔴 ESTA CONSTANTE NO ARREGLA UN `qty: 0` QUE VENGA DEL MODELO, y conviene saberlo:
// `llm.ParseQuantities` rechaza cualquier `qty < 1` como fallo de calidad (`parse.go`,
// validarNormalizedItem), así que una salida con la cantidad omitida NO llega hasta
// aquí: muere en el parser y el job se reintenta. La regla «qty omitida ⇒ 1» del plan
// la aplica el PROMPT, con el parser de red; lo único que esta constante decide es la
// cantidad del ítem que el modelo NO devolvió (ver fundir).
const qtyOmitida = 1

// basisPrefijo es el prefijo literal de `delivery_date_basis` (design §7.3:
// `"message_ts=2026-07-13"`). Deja escrito en el artefacto DESDE QUÉ fecha se calculó,
// que es lo que hace auditable que no salió del reloj del worker.
const basisPrefijo = "message_ts="

// P4 es la etapa de la NORMALIZACIÓN (Plan 044 · T2.4): coge las especificaciones que
// dejó P3 y las convierte en cantidades comparables —unidades, paquetes y rangos— y en
// UNA FECHA ABSOLUTA de entrega. Es la última etapa antes de que el match toque el
// catálogo, así que lo que salga de aquí es lo que se va a cotizar.
//
// # EL REPARTO CON EL MODELO, QUE ES LO QUE DEFINE LA ETAPA
//
// El modelo aporta EXACTAMENTE cuatro campos por ítem: `qty`, `range`, `unit_kind` y
// `package_size`. Todo lo demás —producto, añadidos candidatos, personalizaciones,
// notas— se copia de la spec de P3 sin pasar por el modelo, y la FECHA la calcula Go
// (fechas.go). Dos motivos:
//
//   - lo de P3 ya pasó el anclaje: dejar que el modelo lo reescriba solo puede perder;
//   - un modelo chico al que se le deja tocar el nombre del producto lo funde con el de
//     al lado, y el matcher del catálogo cobra otra cosa.
//
// # LAS TRES REGLAS DE CANTIDAD (design §7.3), Y DÓNDE VIVE CADA UNA
//
//  1. **«Paquete de 30» ⇒ `unit_kind:"package"`, `package_size:30`, JAMÁS `qty:30`.**
//     La pide el prompt, y aquí tiene RED EN GO: corregirPaquete deshace la confusión
//     cuando el modelo la comete. Lo custodia TestP4_PaqueteDe30_NuncaSeConvierteEnQty30.
//  2. **Los rangos NO se colapsan.** «10 o 12 porciones» viaja como
//     `{min:10,max:12,unit:"porciones"}` y nadie elige 11 ni 12: elegir es del dueño en
//     la bandeja (Ola 3), no de esta etapa y menos del modelo.
//  3. **Cantidad omitida ⇒ 1.** Ver el comentario de qtyOmitida: aquí solo se aplica al
//     ítem que el modelo no devolvió.
//
// # LO QUE NO ES DE ESTA ETAPA
//
//   - el tope de ítems (T2.6) y el aforo `K = 1` por Edge (T2.7);
//   - el bucle del worker, el backoff y la política de reintentos DEL JOB (T2.5): aquí
//     se hace UNA llamada y el error sale hacia arriba con su familia intacta, como en
//     P2. No hay reintento por ítem (eso es de P3, que hace N llamadas): P4 hace una
//     sola, así que reintentar el job no tira trabajo de nadie;
//   - el catálogo: esta etapa no busca precios ni crea líneas.
type P4 struct {
	log   logger.Logger
	sel   ProviderSelector
	store StageStore
	zona  *time.Location
}

// NewP4 construye la etapa. Devuelve ErrSinCablear si le falta log, selector o store, y
// ErrSinZonaHoraria si no se le dice en qué zona se cuentan los días.
func NewP4(log logger.Logger, sel ProviderSelector, store StageStore, zona *time.Location) (*P4, error) {
	if log == nil || sel == nil || store == nil {
		return nil, ErrSinCablear
	}
	if zona == nil {
		return nil, ErrSinZonaHoraria
	}
	return &P4{log: log, sel: sel, store: store, zona: zona}, nil
}

// Run normaliza un job YA RECLAMADO y devuelve el artefacto tal como quedó persistido,
// que es lo que consumen el match y el borrador.
//
// `literal` es el `source_text` EN CLARO (lo descifra el worker); `items` son las specs
// que P3 dejó vivas, en su orden; `entrega` es la pista de entrega que P2 etiquetó
// (`delivery_hint`), o nil si el cliente no dijo cuándo.
//
// # LA FECHA SE CALCULA AUNQUE NO HAYA ÍTEMS, Y ANTES DE LLAMAR AL MODELO
//
// Porque no depende de él: la expresión ya viene etiquetada por P2 y la aritmética es
// de Go. Ponerla antes deja además una propiedad útil: un job cuyo P3 se quedó sin
// ítems —que es legal, design §3.2— conserva la fecha que el cliente pidió, y el dueño
// la ve en la bandeja aunque tenga que escribir las líneas a mano.
func (s *P4) Run(ctx context.Context, job intake.ClaimedJob, literal string, items []llm.ItemSpec, entrega *llm.Hint) (*llm.Quantities, error) {
	if literal == "" {
		return nil, ErrSinLiteral
	}

	art := &llm.Quantities{
		Version: llm.ArtifactVersion,
		Items:   make([]llm.NormalizedItem, 0, len(items)),
	}
	s.fechar(art, job, entrega)

	if len(items) > 0 {
		if err := s.normalizar(ctx, job, literal, items, art); err != nil {
			return nil, err
		}
	}

	if err := s.persistir(ctx, job.ID, art); err != nil {
		return nil, err
	}
	s.log.Info("p4: cantidades y fecha normalizadas y persistidas",
		"job_id", job.ID, "stage", intake.StageP4,
		"items", len(art.Items), "con_fecha", art.DeliveryDate != "")
	return art, nil
}

// fechar resuelve la fecha de entrega EN GO y la escribe en el artefacto junto con la
// base desde la que se calculó. No llama al modelo y no lee el reloj.
//
// Los tres desenlaces, y ninguno es un fallo del job:
//
//   - **sin `message_ts`** (columna NULL: la 0072 la deja anulable) ⇒ no hay base y no
//     hay fecha. Es lo único honesto: la alternativa sería usar `now()`, que es
//     exactamente lo que D-044.9 prohíbe;
//   - **sin pista** (el cliente no dijo cuándo) ⇒ no hay fecha, y el dueño pregunta;
//   - **pista que no se reconoce** («cuando puedas») ⇒ no hay fecha. Ver ResolverFecha.
//
// 🔴 EL AVISO NO LLEVA LA PISTA. `Hint.Text` son las palabras del cliente y no salen
// por el log jamás (ADR-0034, INV-6, la misma regla que P2 y P3): el operador se entera
// de que hubo pista y de que no se pudo resolver, y eso basta para investigar.
func (s *P4) fechar(art *llm.Quantities, job intake.ClaimedJob, entrega *llm.Hint) {
	if job.MessageTS.IsZero() {
		if entrega != nil {
			s.log.Warn("p4: el job no trae message_ts; el presupuesto sale SIN fecha en vez de con la de hoy",
				"job_id", job.ID, "stage", intake.StageP4)
		}
		return
	}
	base := job.MessageTS.In(s.zona)
	if entrega == nil {
		return
	}
	fecha, ok := ResolverFecha(entrega.Text, base)
	if !ok {
		s.log.Warn("p4: la pista de entrega no se pudo resolver a una fecha; el presupuesto sale sin fecha",
			"job_id", job.ID, "stage", intake.StageP4)
		return
	}
	art.DeliveryDate = fecha.Format(time.DateOnly)
	art.DeliveryDateBasis = basisPrefijo + base.Format(time.DateOnly)
}

// normalizar hace LA llamada de la etapa y funde lo que conteste con lo que P3 ya
// sabía. Devuelve error solo cuando la llamada o la lectura fallan: en los dos casos el
// job vuelve a la cola con sus artefactos intactos y lo recoge el backoff de T2.5.
//
// El `MessageTS` viaja al prompt aunque la fecha no salga de ahí: el modelo la necesita
// para no inventarse una fecha absurda al normalizar («para el jueves» en las notas), y
// el prompt la imprime como referencia. Lo que P4 hace con `delivery_date` de vuelta es
// COMPARARLA, no creérsela.
func (s *P4) normalizar(ctx context.Context, job intake.ClaimedJob, literal string, items []llm.ItemSpec, art *llm.Quantities) error {
	prov, err := s.sel.For(ctx, job.Key.TenantID, job.Key.SessionID)
	if err != nil {
		return fmt.Errorf("p4: elegir el proveedor del tenant: %w", err)
	}

	raw, err := prov.NormalizeQuantities(ctx,
		llm.NormalizeQuantitiesInput{SourceText: literal, Items: items, MessageTS: job.MessageTS},
		llm.Options{Temperature: llm.TemperatureGreedy})
	if err != nil {
		return fmt.Errorf("p4: pedir la normalización de cantidades: %w", err)
	}

	out, err := llm.ParseQuantities(raw)
	if err != nil {
		// 🔴 El error NO cita `raw`: la salida del modelo lleva frases del cliente.
		return fmt.Errorf("p4: la salida del modelo no es un artefacto P4 legible: %w", err)
	}

	art.Items = s.fundir(job.ID, literal, items, out.Items)
	if out.DeliveryDate != art.DeliveryDate {
		s.log.Warn("p4: la fecha que propuso el modelo no coincide con la que calculó Go; manda la de Go (D-044.9)",
			"job_id", job.ID, "stage", intake.StageP4,
			"el_modelo_propuso_fecha", out.DeliveryDate != "", "go_calculo_fecha", art.DeliveryDate != "")
	}
	return nil
}

// fundir empareja POR POSICIÓN lo que devolvió el modelo con lo que P3 ya sabía, y
// devuelve un ítem normalizado por cada ítem de P3: ni uno más, ni uno menos.
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
// La regla es la de `internal/evidence`, la misma y desde el mismo sitio. La RESPUESTA
// vuelve a ser distinta que en P2 y en P3, y por la misma lógica: aquí ya no hay nada
// que descartar ni que aislar —el ítem está probado desde P3—, así que una evidencia
// que el modelo se invente simplemente NO SUSTITUYE a la que ya había. El ítem sigue
// vivo y el artefacto nunca guarda una frase que el cliente no escribió.
func (s *P4) fundir(jobID, literal string, specs []llm.ItemSpec, norm []llm.NormalizedItem) []llm.NormalizedItem {
	if len(norm) > len(specs) {
		s.log.Warn("p4: el modelo devolvió más ítems de los que P3 dejó vivos; los sobrantes se descartan",
			"job_id", jobID, "stage", intake.StageP4, "descartados", len(norm)-len(specs))
	}
	textoNorm := evidence.Normalize(literal)
	out := make([]llm.NormalizedItem, 0, len(specs))
	for i := range specs {
		it := neutro(specs[i])
		if i < len(norm) {
			s.aplicarCantidades(&it, norm[i], textoNorm, jobID, i)
		} else {
			s.log.Warn("p4: el modelo no devolvió este ítem; sale con la normalización neutra y el pedido no lo pierde",
				"job_id", jobID, "stage", intake.StageP4, "item_pos", i)
		}
		out = append(out, it)
	}
	return out
}

// neutro es la normalización de un ítem al que el modelo no aportó nada: los datos de
// P3 tal cual y UNA unidad. Sin rango y sin paquete, porque inventarlos sería peor que
// no tenerlos: el dueño ve «1× torta» y corrige, que es exactamente lo que la bandeja
// existe para permitir.
func neutro(spec llm.ItemSpec) llm.NormalizedItem {
	return llm.NormalizedItem{
		Product:         spec.Product,
		Qty:             qtyOmitida,
		AddonCandidates: spec.AddonCandidates,
		Customizations:  spec.Customizations,
		Notes:           spec.Notes,
		Evidence:        spec.Evidence,
	}
}

// aplicarCantidades copia del ítem del modelo los CUATRO campos que le tocan y la
// evidencia si se sostiene. `qty`, `range` y `package_size` llegan ya comprobados por
// `llm.ParseQuantities` (cantidad >= 1, rango en orden con unidad, paquete con al menos
// una unidad), así que aquí no se vuelven a validar: una segunda red con el mismo
// síntoma taparía a los tests de conducta de la primera.
//
// El producto se deja el de P3 a propósito (ver el docstring de la etapa). Cuando el
// modelo lo reescribe se avisa —sin decir con qué, que sería texto del cliente—, porque
// un producto renombrado es la única señal barata de que el modelo REORDENÓ los ítems y
// el emparejamiento por posición está pegando cantidades al ítem equivocado.
func (s *P4) aplicarCantidades(it *llm.NormalizedItem, del llm.NormalizedItem, textoNorm, jobID string, pos int) {
	it.Qty = del.Qty
	it.Range = del.Range
	it.UnitKind = del.UnitKind
	it.PackageSize = del.PackageSize

	if evidence.Contains(textoNorm, del.Evidence) {
		it.Evidence = del.Evidence
	} else {
		s.log.Warn("p4: la evidencia que devolvió el modelo no aparece en el literal del cliente; se conserva la de P3",
			"job_id", jobID, "stage", intake.StageP4, "item_pos", pos)
	}
	if evidence.Normalize(del.Product) != evidence.Normalize(it.Product) {
		s.log.Warn("p4: el modelo renombró el producto; se conserva el de P3 (si esto se repite, revisa si reordenó los ítems)",
			"job_id", jobID, "stage", intake.StageP4, "item_pos", pos)
	}
	if corregirPaquete(it) {
		s.log.Warn("p4: el modelo confundió el TAMAÑO del paquete con la cantidad; se corrige a un paquete (design §7.3)",
			"job_id", jobID, "stage", intake.StageP4, "item_pos", pos)
	}
}

// rePaqueteDe saca el tamaño de un «paquete de N» del texto del cliente. Los 40
// caracteres SIN DÍGITOS de en medio son el caso real del fixture de Ambar —«un paquete
// de tequeños congelados de 30»—, donde el número no va pegado a «de»: sin ese hueco la
// regla no vería el único paquete que hay en el caso. Y son SIN dígitos a propósito, de
// modo que en «2 paquetes de 30 y 4 cajas de 6» el 30 no se confunda con el 6.
var rePaqueteDe = regexp.MustCompile(`paquetes? de [^0-9]{0,40}(\d{1,4})\b`)

// corregirPaquete es LA RED EN GO de la regla «paquete de 30 ⇒ package_size 30, JAMÁS
// qty 30» (design §7.3). Devuelve true si tuvo que corregir.
//
// # POR QUÉ HACE FALTA UNA RED, SI EL PROMPT YA LO DICE
//
// Porque el prompt es una petición y esto es una garantía. La confusión —cobrar 30
// tortas donde el cliente pidió un paquete de 30 tequeños— es el error más caro que
// puede cometer esta etapa: multiplica el presupuesto por treinta. Y es EXACTAMENTE el
// error que un modelo chico comete, porque el número está pegado al producto. Dejar la
// regla solo en el prompt sería dejar sin ninguna prueba en Go lo que el enunciado
// escribe en mayúsculas.
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
func corregirPaquete(it *llm.NormalizedItem) bool {
	m := rePaqueteDe.FindStringSubmatch(evidence.Normalize(it.Evidence + " " + it.Notes))
	if m == nil {
		return false
	}
	tam, err := strconv.Atoi(m[1])
	if err != nil || tam <= 1 || it.Qty != tam {
		return false
	}
	it.Qty = qtyOmitida
	it.UnitKind = llm.UnitKindPackage
	it.PackageSize = tam
	return true
}

// persistir serializa el artefacto y lo deja en la máquina de estados.
//
// Se serializa el artefacto DEL CLOUD y no la salida cruda del modelo por el mismo
// motivo que en P2 y P3: lo que se guarda es lo que el match se va a creer, y el crudo
// llevaría dentro la fecha que el modelo propuso —descartada de boquilla, presente en
// la base— y las evidencias que no se sostienen.
//
// No se revalida el `version` aquí: la puerta es `intake.Artifact.Validate`, dentro de
// `SaveStage`.
func (s *P4) persistir(ctx context.Context, jobID string, art *llm.Quantities) error {
	payload, err := json.Marshal(art)
	if err != nil {
		return fmt.Errorf("p4: serializar el artefacto: %w", err)
	}
	guardado, err := s.store.SaveStage(ctx, jobID, intake.Artifact{
		Stage:   intake.StageP4,
		Payload: payload,
	})
	if err != nil {
		return fmt.Errorf("p4: persistir el artefacto: %w", err)
	}
	if !guardado {
		return ErrJobFueraDeProcessing
	}
	return nil
}
