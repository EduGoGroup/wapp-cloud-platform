package stages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake/anclaje"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// draft.go — LA ETAPA `draft` (Plan 044 · Ola 3 · T3.4): lo que el match dejó en
// líneas se convierte en la SOLICITUD que el dueño ve en la bandeja.
//
// Es la última etapa del pipeline y la primera que escribe FUERA de `intake_jobs`:
// hasta aquí todo vivía en la cola de trabajo, y a partir de aquí hay un objeto de
// negocio con su ciclo de vida propio (`intakes`, D-041.10) y su rastro de
// negociación (`intake_revisions`, ADR-0031 §3).
//
// ════════════════════════════════════════════════════════════════════════════
// LAS CUATRO ESCRITURAS Y POR QUÉ EN ESTE ORDEN
// ════════════════════════════════════════════════════════════════════════════
//
//  1. `intakes` en `pending_approval` — la cabecera. Va primera porque las otras
//     tres cuelgan de su id (la revisión por FK real, el resto por contenido).
//  2. `intake_revisions` revisión 1, `kind='interpreted'` — el borrador entero, con
//     el contrato §7.4 dentro del payload. Es LA entrega de esta etapa: la cabecera
//     sin revisión sería una solicitud vacía.
//  3. `flow_events` `intake_draft_created` — la métrica. Es BEST-EFFORT: si falla,
//     se avisa y la etapa sigue. Perder una fila de telemetría no puede costar el
//     pedido de un cliente (mismo criterio que el hilo de decisión del PersistSink).
//  4. `artifacts.draft` en el job — la marca de «esta etapa ya corrió», que es lo
//     que hace que una reanudación no vuelva a pasar por aquí (ver `reanudar` en
//     pipeline.go). Va la ÚLTIMA a propósito: es el único orden en el que la marca
//     no puede afirmar un trabajo que no se hizo.
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 LO QUE ESTA ETAPA NO HACE, Y NO ES UN OLVIDO
// ════════════════════════════════════════════════════════════════════════════
//
//   - NO CIFRA NADA, Y ESO YA NO SIGNIFICA QUE EL LITERAL VIAJE EN CLARO (🔧 T3.5,
//     2026-08-26). Esta etapa sigue construyendo el payload §7.4 ENTERO, con su
//     `source_text` y sus `evidence` dentro, y sigue sin tocar una llave: no la
//     tiene y no debe tenerla. Lo que cambió es quién hay al otro lado del puerto —
//     `intakes.Postgres.InsertRevision` SACA el literal del payload y lo sella con
//     el `FieldCipher` (KEK, Planes 011/012) antes de que toque la BD, y se lo
//     devuelve a su sitio al leer. Es una BARRERA ÚNICA en el store y no un paso más
//     de esta etapa a propósito: así ningún escritor de revisiones —ni éste ni los
//     que vengan— puede persistir literal en claro por olvido, que es exactamente el
//     fallo de MP-06 (`vars.intent_params`) que D-044.13 cita para no repetirlo.
//     El detalle vive en `internal/intakes/literal.go`.
//   - NO ESCRIBE `intake_items`. Las líneas del presupuesto viven en la REVISIÓN, no
//     en la tabla de líneas: `intake_items` es la verdad de lo VENDIDO (intakes.go)
//     y un borrador todavía no se ha vendido. Y hay una razón más dura: la mitad de
//     las líneas de un borrador NO TIENEN PRECIO —`unit_price` nil es el caso normal
//     de §7.4—, y `intake_items.unit_price` es NOT NULL, así que materializarlas hoy
//     obligaría a escribir un 0 que el dueño leería como «gratis». Es exactamente lo
//     que §7.5 prohíbe pintar. Las líneas se materializan cuando alguien les pone
//     precio: la acción Aprobar (T4.3), que además exige cero líneas sin precio.
//   - NO ESCRIBE `intakes.customer_note`. Ver notaDePedido más abajo: hoy nadie la
//     produce y el puerto de escritura de la cabecera no puede escribirla.
//   - NO PONE TOTAL. `intakes.total` es en todo el sistema la SUMA de las líneas de
//     `intake_items` (intakes/edit.go), y esta etapa no escribe ninguna: un total
//     distinto de 0 diría una cifra que sus propias líneas no respaldan. El «total
//     parcial» que el dueño ve (§7.5) es una VISTA de la revisión
//     (ArtefactoMatch.TotalParcial), no una columna.

// EventoBorradorCreado es el `flow_events.name` de la métrica de esta etapa (design
// §10). El literal se declara aquí porque aquí está su ÚNICO productor —hasta hoy la
// clave no existía en ninguna parte del código, solo en la tabla del plan— y
// `flow_events.name` es TEXT libre sin CHECK (migración 0009), así que no hay
// migración que dar de alta: lo que hay que dar de alta es la constante.
//
// Se sigue el patrón de los efectos de módulo (cart/effects.go): un nombre lógico
// declarado como constante junto a quien lo emite, nunca un literal suelto repartido
// por el código.
const EventoBorradorCreado = "intake_draft_created"

// FlujoCaptacion y VersionFlujoCaptacion son el `flow_id` / `flow_version` con los
// que el pipeline firma sus filas de `flow_events`.
//
// 🔴 SON SINTÉTICOS, Y HAY QUE DECIRLO. Las dos columnas son NOT NULL desde la 0009
// porque esa tabla nació como el outbox del MOTOR DE FLUJOS, donde todo efecto sale
// de un flujo con su versión. Este evento no: lo emite un worker de cola que no está
// dentro de ningún turno y cuyo job (`intake_jobs`) no guarda flujo — durante
// `aggregating`/`pending`/`processing` no hay nada que consultar. Las opciones eran
// inventar un join que no existe o firmar con un identificador propio; se firma.
//
// El prefijo `_` es el mismo espacio RESERVADO que ya usa la plataforma para lo que
// es SUYO y no del tenant (`_shipping`, intakes/shipping.go): ningún flujo de ningún
// tenant puede colisionar con él, así que una fila con este `flow_id` es SIEMPRE del
// pipeline de captación y de nada más — y eso la hace filtrable en SQL, que es todo
// lo que se le pide a la columna aquí.
//
// La versión es 1 y no 0 porque 0 significaría «versión desconocida»: este emisor sí
// tiene una, la de su contrato de payload, y sube el día que el payload cambie.
const (
	FlujoCaptacion        = "_intake_llm"
	VersionFlujoCaptacion = 1
)

// kindEventoFlujo es el `flow_events.kind` de una fila de TELEMETRÍA ("event", frente
// a "persist" que es la que además proyecta una tabla tipada). Replica el literal que
// declaran los módulos —cart/effects.go hace lo mismo con el suyo— porque el
// vocabulario es de la tabla (0009), no de ningún paquete de Go.
const kindEventoFlujo = "event"

// Los valores de `analysis.source` (design §7.4, D-044.15): de dónde salió el
// material que se interpretó.
const (
	// OrigenHiloDelEvento es el literal del cliente reconstruido del hilo del evento.
	// Es SIEMPRE el de la revisión 1: en la primera pasada no hay ninguna otra cosa.
	OrigenHiloDelEvento = "event_thread"
	// OrigenTextoPegado es material extra que pegó el DUEÑO (Plan 045, D-045.5).
	OrigenTextoPegado = "pasted_text"
	// OrigenAmbos es el hilo del evento MÁS el texto pegado.
	OrigenAmbos = "both"
)

// ErrDraftSinCablear es la etapa a la que le falta una pieza. Las cinco son
// obligatorias y no opcionales con cero-valor por la misma razón que el proyector del
// carrito exige sus cuatro: un draft sin escritor de revisiones crearía solicitudes
// vacías y nadie lo notaría hasta que un dueño abriera la bandeja.
var ErrDraftSinCablear = errors.New("stages: la etapa draft necesita log, store, escritor de solicitudes, de revisiones y de eventos")

// ErrSinMatch es «llegó un job sin el artefacto del match». Distinto de un artefacto
// con CERO líneas, que no puede ocurrir —el envío va siempre (D-041.11)— pero que si
// ocurriera sería un borrador legítimo y vacío, no un fallo.
var ErrSinMatch = errors.New("stages: el draft necesita el artefacto del match")

// ErrJobSinEvento es el job cuya ventana no declara evento conversacional.
//
// Se comprueba en Go aunque la BD ya lo impida, y el motivo es el mensaje: sin esto,
// el fallo sería un NOT NULL de `intakes.event_id` (0055) con el texto de Postgres,
// que no dice ni qué job era ni que el problema está en la CLAVE DE VENTANA. Y no
// puede ocurrir por el camino normal: `WindowKey.Valid()` exige las cuatro columnas
// antes de abrir la ventana.
var ErrJobSinEvento = errors.New("stages: el job no trae el evento conversacional del que cuelga el borrador")

// Analisis es el rastro de QUIÉN interpretó (design §7.4, D-044.15). Va EN CLARO: no
// es texto del cliente, es metadato de proceso — y sin él no se puede comparar «lo
// que sacó el local» contra «lo que sacó la API», que es media razón de existir del
// re-análisis.
//
// ⚠️ EL CAMPO SE LLAMA `provider` Y LLEVA UNA VÍA. Está así en el contrato escrito
// (§7.4: «`provider` (`local`|`api`)»), y `local`/`api` es el eje VÍA del ADR-0044,
// no un proveedor —el proveedor es Anthropic o Gemini, y sale siempre de `tenant_llm`.
// La misma clave se renombró a `via` en la telemetría de `intake_reanalyzed` el
// 2026-08-23 por esta razón exacta. Aquí se escribe como está escrito en el contrato
// y no se renombra por cuenta propia: cambiar el nombre de un campo del payload es
// una decisión del plan (y su ventana barata es AHORA, antes de que T4.1 congele los
// golden files del detalle).
type Analisis struct {
	// Provider es la VÍA por la que corrió el pipeline: `local` o `api`.
	Provider string `json:"provider"`
	// Model es el modelo concreto, tal como lo declare el adaptador.
	Model string `json:"model,omitempty"`
	// Source es uno de los Origen*: de dónde salió el material interpretado.
	Source string `json:"source"`
	// ReanalyzedFrom es el `revision_no` del que salió este re-análisis. Es puntero y
	// NO lleva `omitempty` a propósito: §7.4 escribe `"reanalyzed_from": null` en la
	// revisión 1, y «null» dice «esta es la primera lectura», que no es lo mismo que
	// no traer el campo.
	ReanalyzedFrom *int `json:"reanalyzed_from"`
}

// LineaRevision es una línea del presupuesto tal como se congela en el payload de la
// revisión: la línea del match MÁS los adjuntos que le ancló T3.3.
//
// 🔴 EMBEBE `Linea` EN VEZ DE COPIAR SUS CAMPOS. La forma de la línea (§7.4) está
// definida en UN sitio —match.go— y aquí solo se le añade lo que allí no podía estar:
// el reparto de media es de otra etapa. Copiar los trece campos habría creado un
// segundo contrato que diverge del primero en cuanto alguien añada un campo a uno de
// los dos, y encima habría duplicado sus comentarios (un comentario de molde hereda
// la falsedad del molde).
type LineaRevision struct {
	Linea
	// MediaRefs son los adjuntos ANCLADOS a esta línea (T3.3). Los que no se pudieron
	// anclar con certeza no están aquí: están en la cabecera del payload, que es la
	// cláusula de cierre del anclaje («sin certeza, a la cabecera»).
	MediaRefs []anclaje.MediaRef `json:"media_refs,omitempty"`
}

// PayloadRevision es el CONTRATO §7.4: lo que se guarda en
// `intake_revisions.payload` de la revisión 1 con `kind='interpreted'`.
//
// Las etiquetas JSON son el contrato mismo — cambiarlas exige subir
// `intakes.RevisionPayloadVersion`, que es el número que viaja dentro del blob para
// que un lector que no entienda la versión pueda saberlo mirando solo lo que tiene
// delante.
type PayloadRevision struct {
	// Version es `intakes.RevisionPayloadVersion`.
	Version int `json:"version"`
	// SourceText es el texto ORIGINAL del cliente, el mismo que interpretaron P2-P4.
	//
	// 🔴 ES NIVEL 2 (ADR-0034): no llega a la columna `payload`. El store lo SACA de
	// aquí y lo guarda cifrado con KEK aparte, junto con las `evidence` de las
	// líneas (T3.5, D-044.13); el resto del payload se queda en claro a propósito, y
	// eso incluye explícitamente las personalizaciones («sin sal»), que son dato de
	// negocio cuantificable. El campo se llena igual al construir el borrador: quien
	// lo cifra es `intakes`, y quien lo lea por la API lo recibe descifrado mientras
	// no venza su TTL de retención.
	// Está aquí porque el dueño necesita ver el original AL LADO de la interpretación
	// para validarla (§7.6), y esa es la mitad de la razón de ser de la revisión.
	SourceText string `json:"source_text,omitempty"`
	// MessageTS es el instante del PRIMER mensaje de la ventana (`intake_jobs.
	// message_ts`, D-044.9): la hora que la bandeja pinta junto al nombre del cliente
	// («Ambar · 13/07 09:55») y la base contra la que P4 resolvió la fecha de
	// entrega. NO es la hora de creación de la revisión, que la pone la BD.
	MessageTS time.Time `json:"message_ts"`
	// Analysis es el rastro de quién interpretó (D-044.15).
	Analysis Analisis `json:"analysis"`
	// DeliveryDate es la fecha de entrega ABSOLUTA que calculó P4 con aritmética de
	// Go. Vacía cuando la expresión no se reconoció sin ambigüedad: entonces el
	// borrador sale sin fecha y el dueño la pregunta, que es estrictamente mejor que
	// fabricar un día.
	DeliveryDate string `json:"delivery_date,omitempty"`
	// MediaRefs son los adjuntos de la CABECERA: los audios (siempre, con su etiqueta)
	// y todo aquello que el anclaje no pudo colgar de una línea con certeza.
	MediaRefs []anclaje.MediaRef `json:"media_refs,omitempty"`
	// Lines son las líneas del presupuesto EN ORDEN (el orden es contrato: cada línea
	// con sus añadidos detrás y el envío el último, §7.5).
	Lines []LineaRevision `json:"lines"`
	// SuggestedQuestions son las preguntas PREPARADAS para el cliente (§7.4). Nunca
	// salen solas: el dueño las edita y las manda (INV-1, T4.4). Lista vacía se
	// serializa `[]` y no `null` — «no hay nada que preguntar» es una respuesta, y
	// `null` no dice cuál de las dos es.
	SuggestedQuestions []string `json:"suggested_questions"`
	// Warnings son los ítems que el match degradó (DEUDA-044.16): posición y motivo,
	// sin una línea de texto del cliente.
	//
	// ⚠️ NO ESTÁ EN EL EJEMPLO DE §7.4, y se añade a propósito. La decisión de degradar
	// un ítem en vez de tirar el borrador entero se sostiene sobre que «el dueño lo ve
	// en la bandeja y lo arregla» (match.go), y la bandeja lee ESTE payload: sin los
	// avisos aquí, el motivo se quedaría en `intake_jobs.artifacts.match`, donde no
	// mira nadie. Es aditivo y `omitempty`, así que un lector que no lo conozca no se
	// entera de que existe.
	Warnings []Aviso `json:"warnings,omitempty"`
}

// ArtefactoDraft es lo que la etapa persiste bajo `artifacts.draft`. No repite el
// borrador —ése ya está en `intake_revisions`, y dos copias del mismo dato divergen—:
// guarda el RESULTADO del acto, que es lo que una reanudación necesita saber para no
// repetirlo y lo que el worker necesita para cerrar el job con su `intake_id`.
type ArtefactoDraft struct {
	// Version es la del artefacto, que exige `intake.Artifact.Validate`.
	Version int `json:"version"`
	// IntakeID es la solicitud creada. Es el valor que viaja a `Finish` y acaba en
	// `intake_jobs.intake_id`.
	IntakeID string `json:"intake_id"`
	// RevisionNo es el número que le tocó a la revisión. Es 1 en el camino normal;
	// un re-análisis (T4.6) sobre la misma solicitud escribiría 2, 3…
	RevisionNo int `json:"revision_no"`
	// Lines es cuántas líneas tiene el borrador, para poder mirar el job y saber si
	// salió vacío sin abrir la revisión.
	Lines int `json:"lines"`
	// ElapsedMS es el mismo número que se publicó en la métrica. Ver el bloque de
	// relojes en transcurrido().
	ElapsedMS int64 `json:"elapsed_ms"`
}

// EntradaDraft es todo lo que la etapa necesita, y viaja en un struct porque cada
// campo lo produce una etapa distinta y conviene que se vea de dónde sale cada uno.
type EntradaDraft struct {
	// Match es el artefacto de T3.2: las líneas, la nota del pedido y los avisos.
	Match *ArtefactoMatch
	// SourceText es el literal del cliente YA DESCIFRADO, el mismo que se le dio a
	// P2-P4. Esta etapa no sabe descifrar —igual que no sabe cifrar—: lo recibe.
	SourceText string
	// FechaEntrega es la que dejó P4 en `llm.Quantities.DeliveryDate`, calculada con
	// aritmética de Go contra `message_ts` (fechas.go). Vacía = no se reconoció la
	// expresión, y entonces el borrador sale sin fecha.
	FechaEntrega string
	// Media es el reparto de adjuntos de T3.3.
	//
	// 🔴 `Reparto.PorLinea` va indexado por la POSICIÓN DE LA LÍNEA EN `Match.Lines`,
	// y eso es un contrato entre quien construye el reparto y esta etapa: el paquete
	// `anclaje` no interpreta el índice, lo devuelve tal cual. Un índice que no
	// corresponda a ninguna línea NO se pierde: sus refs suben a la cabecera, que es
	// la cláusula de cierre del propio anclaje.
	Media anclaje.Reparto
	// Analisis es el rastro de quién interpretó (D-044.15). Lo rellena quien eligió la
	// vía, que es el worker; esta etapa no lo puede saber.
	Analisis Analisis
}

// EscritorSolicitud es lo ÚNICO que esta etapa necesita para PARIR la cabecera de la
// solicitud: escribirla. Lo satisfacen `*store.PostgresRepository` y
// `*store.MemoryRepository`.
//
// Se declara del lado del consumidor (idioma Go, mismo criterio que las cuatro del
// proyector del carrito) y con UN método: el pipeline no lista solicitudes, no las
// transiciona y no debe poder hacerlo. Lo único que le corresponde es dar de alta la
// suya.
type EscritorSolicitud interface {
	UpsertIntake(ctx context.Context, o store.Intake) error
}

// EscritorRevision es lo ÚNICO que esta etapa necesita del dominio de solicitudes:
// dejar la revisión. Lo satisfacen `*intakes.Postgres` y `*intakes.MemoryStore`.
//
// El `revision_no` lo asigna el STORE y no el llamante (ver intakes/revisions.go):
// solo él puede saber cuál es el siguiente sin carrera, y por eso esta etapa no
// escribe «1» en ninguna parte aunque su tarea se llame «revisión 1».
type EscritorRevision interface {
	InsertRevision(ctx context.Context, rev intakes.Revision) (intakes.Revision, error)
}

// EscritorEvento es lo ÚNICO que esta etapa necesita del outbox de efectos: añadir
// una fila. Lo satisfacen `*store.PostgresRepository` y `*store.MemoryRepository`.
type EscritorEvento interface {
	InsertFlowEvent(ctx context.Context, ev store.FlowEvent) error
}

// Draft es la etapa del BORRADOR (Plan 044 · T3.4).
type Draft struct {
	log         logger.Logger
	store       StageStore
	solicitudes EscritorSolicitud
	revisiones  EscritorRevision
	eventos     EscritorEvento
	ahora       func() time.Time
}

// OpciónDraft configura la etapa. Es un tipo aparte de `Opción` (la de los plazos de
// las etapas LLM) y de `OpciónMatch` por la misma razón que aquéllas son distintas
// entre sí: mezclarlas dejaría construir un draft «con plazo por llamada», que aquí
// no significa nada.
type OpciónDraft func(*Draft)

// ConReloj sustituye el reloj de la etapa. Existe para los tests: `elapsed_ms` es la
// resta de dos instantes, y con `time.Now` de verdad no se puede afirmar un número
// —solo un rango—, que es la clase de aserción que deja pasar un cero.
//
// Pasar nil no hace nada: la etapa no se queda sin reloj.
func ConReloj(ahora func() time.Time) OpciónDraft {
	return func(d *Draft) {
		if ahora != nil {
			d.ahora = ahora
		}
	}
}

// NewDraft construye la etapa. Devuelve ErrDraftSinCablear si le falta una pieza.
func NewDraft(log logger.Logger, st StageStore, solicitudes EscritorSolicitud,
	revisiones EscritorRevision, eventos EscritorEvento, opts ...OpciónDraft) (*Draft, error) {
	if log == nil || st == nil || solicitudes == nil || revisiones == nil || eventos == nil {
		return nil, ErrDraftSinCablear
	}
	d := &Draft{
		log: log, store: st, solicitudes: solicitudes,
		revisiones: revisiones, eventos: eventos,
		// 🔴 EL ÚNICO `time.Now` DE ESTE FICHERO, Y ES EL VALOR POR DEFECTO DE UNA
		// DEPENDENCIA INYECTABLE — no una lectura del reloj metida en medio de la
		// lógica. Lo custodia TestDraft_ElRelojSoloEntraPorElConstructor.
		ahora: time.Now,
	}
	for _, o := range opts {
		o(d)
	}
	return d, nil
}

// espacioBorrador es el espacio de nombres del que sale el id de la solicitud. Ver
// idDeLaSolicitud: es un UUID FIJO y arbitrario, y lo único que importa de él es que
// no cambie NUNCA.
var espacioBorrador = uuid.MustParse("6f8f5b2e-3d61-5a4c-9a1e-0b7c4d2f8a13")

// idDeLaSolicitud deriva el id del borrador del EVENTO del que cuelga, en vez de
// sortear uno nuevo. Las tres razones, en orden de importancia:
//
//  1. **UN REINTENTO NO PUEDE PARIR UNA SEGUNDA SOLICITUD.** La BD ya declara que un
//     evento tiene A LO SUMO un contenido durable (`intakes_event_id_uidx`, 0054), así
//     que un id sorteado haría que el segundo intento chocara contra el único parcial:
//     violación de integridad, clase 23, que no cede reintentando — o sea un job
//     envenenado cuyo borrador YA EXISTÍA. Con el id derivado, el segundo intento
//     re-escribe la MISMA fila (el `ON CONFLICT (id) DO UPDATE` del upsert) y
//     converge.
//  2. **EL RE-ANÁLISIS ATERRIZA SOLO EN LA MISMA SOLICITUD.** T4.6 abre un job NUEVO
//     sobre el MISMO evento y tiene que escribir una revisión más sobre el borrador
//     que ya había. Derivando el id, eso sale sin ninguna consulta previa.
//  3. Es un id REPRODUCIBLE: dado el evento, se sabe cuál era su borrador sin abrir la
//     tabla.
//
// ⚠️ LA CONTRAPARTIDA, ESCRITA PARA QUE NADIE LA DESCUBRA EN CAMPO: el upsert PISA
// `status` con `pending_approval`. En el camino de esta tarea eso es lo correcto —la
// fila nace aquí—, y en un reintento también —re-escribe lo mismo—. Pero el día que
// T4.6 reuse esta etapa para un re-análisis, la solicitud PUEDE estar ya en otro
// estado (el dueño la rechazó, o la aprobó), y volver a `pending_approval` sin mirar
// de dónde viene sería saltarse la máquina de estados. Esa decisión es de T4.6 y
// necesita leer el estado actual, cosa que este puerto —de UNA sola escritura— no
// puede hacer a propósito.
func idDeLaSolicitud(eventoID string) string {
	return uuid.NewSHA1(espacioBorrador, []byte(eventoID)).String()
}

// Run crea la solicitud, le cuelga la revisión 1 y publica la métrica.
//
// # POR QUÉ LA SOLICITUD NACE EN `pending_approval` Y NO EN `open`
//
// Porque no hay ninguna transición que hacer: es un NACIMIENTO, y `CanTransition`
// gobierna a las filas que ya existen. Un borrador interpretado por el pipeline está,
// desde el primer instante, esperando a que el dueño lo apruebe; pasarlo antes por
// `open` inventaría un estado en el que nunca estuvo —`open` es «carrito en curso»,
// y aquí no hay carrito— y dejaría una ventana en la que la bandeja enseña una
// solicitud abierta sin líneas.
//
// Lo que sí se respeta es el vocabulario y el camino de salida: `pending_approval` es
// una clave del ciclo de vida (D-041.10) desde la que se sale a `confirmed`,
// `rejected`, `needs_info` o `cancelled`, que son exactamente las cuatro acciones que
// la bandeja le ofrece al dueño (T4.3/T4.4). Lo custodia
// TestDraft_ElEstadoDeNacimientoEsUnaClaveViva.
func (s *Draft) Run(ctx context.Context, job intake.ClaimedJob, in EntradaDraft) (*ArtefactoDraft, error) {
	if in.Match == nil {
		return nil, ErrSinMatch
	}
	if job.Key.EventID == "" {
		return nil, fmt.Errorf("%w: job_id=%s", ErrJobSinEvento, job.ID)
	}

	intakeID := idDeLaSolicitud(job.Key.EventID)
	if err := s.solicitudes.UpsertIntake(ctx, store.Intake{
		ID:        intakeID,
		TenantID:  job.Key.TenantID,
		ContactID: job.Key.ContactID,
		SessionID: job.Key.SessionID,
		Status:    intakes.StatusPendingApproval,
		EventID:   job.Key.EventID,
		// Total y CustomerNote quedan a cero: ver la cabecera del fichero.
	}); err != nil {
		return nil, fmt.Errorf("draft: crear la solicitud del job %s: %w", job.ID, err)
	}
	s.notaDePedidoPerdida(job, in.Match)

	revision := s.revision(job, in)
	payload, err := serializarRevision(job.ID, revision)
	if err != nil {
		return nil, err
	}
	rev, err := s.revisiones.InsertRevision(ctx, intakes.Revision{
		IntakeID: intakeID,
		Kind:     intakes.RevisionKindInterpreted,
		Payload:  payload,
		// 🔴 CreatedBy es un ROL, jamás una persona (intakes/revisions.go). La revisión
		// 1 la escribe el pipeline, así que es `system`. La del re-análisis que pide el
		// dueño será `owner` — sigue siendo el rol, no su identidad.
		CreatedBy: intakes.RevisionBySystem,
	})
	if err != nil {
		return nil, fmt.Errorf("draft: revisión interpretada de la solicitud %s: %w", intakeID, err)
	}

	transcurrido := s.transcurrido(job)
	s.publicarMetrica(ctx, job, in.Match, transcurrido)

	art := &ArtefactoDraft{
		Version:    intakes.RevisionPayloadVersion,
		IntakeID:   intakeID,
		RevisionNo: rev.RevisionNo,
		Lines:      len(in.Match.Lines),
		ElapsedMS:  transcurrido.Milliseconds(),
	}
	if err := s.persistir(ctx, job.ID, art); err != nil {
		return nil, err
	}

	casadas, sinCasar := s.recuento(in.Match)
	s.log.Info("draft: borrador creado y esperando al dueño",
		"job_id", job.ID, "stage", intake.StageDraft, "intake_id", intakeID,
		"revision_no", rev.RevisionNo, "status", intakes.StatusPendingApproval,
		"lineas", len(in.Match.Lines), "casadas", casadas, "sin_casar", sinCasar,
		"preguntas", len(revision.SuggestedQuestions), "avisos", len(revision.Warnings),
		"elapsed_ms", art.ElapsedMS)
	return art, nil
}

// revision arma el contrato §7.4.
func (s *Draft) revision(job intake.ClaimedJob, in EntradaDraft) PayloadRevision {
	lineas, cabecera := s.lineasConMedia(job, in)
	return PayloadRevision{
		Version:      intakes.RevisionPayloadVersion,
		SourceText:   in.SourceText,
		MessageTS:    job.MessageTS,
		Analysis:     s.analisis(job, in.Analisis),
		DeliveryDate: in.FechaEntrega,
		MediaRefs:    cabecera,
		Lines:        lineas,
		// Las preguntas se DERIVAN de las líneas; no las trae nadie de fuera. Ver
		// preguntasSugeridas.
		SuggestedQuestions: preguntasSugeridas(in.Match.Lines),
		Warnings:           in.Match.Warnings,
	}
}

// serializarRevision pasa el contrato a JSON.
func serializarRevision(jobID string, p PayloadRevision) (json.RawMessage, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		// El error NO cita el payload: dentro va el literal del cliente (ADR-0034).
		return nil, fmt.Errorf("draft: serializar el payload de la revisión del job %s: %w", jobID, err)
	}
	return raw, nil
}

// analisis completa lo que el llamante no dijo y avisa de lo que no se puede completar.
//
// `source` se rellena solo porque en la revisión 1 no hay más que una posibilidad: el
// hilo del evento. La VÍA no se puede rellenar —quien la sabe es quien eligió el
// proveedor— y su ausencia no tumba el borrador: se avisa. Un borrador sin metadato de
// proceso sigue siendo el pedido de un cliente; lo que se pierde es poder comparar
// después «lo que sacó el local» contra «lo que sacó la API» (D-044.15).
func (s *Draft) analisis(job intake.ClaimedJob, a Analisis) Analisis {
	if a.Source == "" {
		a.Source = OrigenHiloDelEvento
	}
	if a.Provider == "" {
		s.log.Warn("draft: la revisión sale SIN vía de análisis; no se podrá comparar local contra api (D-044.15)",
			"job_id", job.ID, "stage", intake.StageDraft)
	}
	return a
}

// lineasConMedia pega a cada línea los adjuntos que le ancló T3.3 y devuelve, aparte,
// los de la cabecera.
//
// Un índice del reparto que no corresponde a ninguna línea NO se descarta: sus refs
// suben a la cabecera con un aviso. Es la misma cláusula de cierre que aplica el
// propio anclaje cuando no tiene certeza («sin certeza, a la cabecera»), y el motivo
// es idéntico: perder un audio del cliente es peor que enseñarlo en el sitio genérico.
func (s *Draft) lineasConMedia(job intake.ClaimedJob, in EntradaDraft) (lineas []LineaRevision, cabecera []anclaje.MediaRef) {
	lineas = make([]LineaRevision, 0, len(in.Match.Lines))
	for i, l := range in.Match.Lines {
		lineas = append(lineas, LineaRevision{Linea: l, MediaRefs: in.Media.PorLinea[i]})
	}
	cabecera = in.Media.Solicitud
	for idx, refs := range in.Media.PorLinea {
		if idx >= 0 && idx < len(in.Match.Lines) {
			continue
		}
		s.log.Warn("draft: adjuntos anclados a una línea que no existe; suben a la cabecera",
			"job_id", job.ID, "stage", intake.StageDraft,
			"linea_idx", idx, "lineas", len(in.Match.Lines), "refs", len(refs))
		cabecera = append(cabecera, refs...)
	}
	return lineas, cabecera
}

// notaDePedidoPerdida avisa de que la indicación del PEDIDO ENTERO no se persiste.
//
// 🔴 HOY ES UN CAMINO MUERTO Y AUN ASÍ ESTÁ ESCRITO, con su porqué: nadie produce la
// nota (`stages.SinNotaDePedido`, ver NotaDePedido en match.go), así que el campo
// llega SIEMPRE vacío. Lo que este aviso defiende es el día que alguien SÍ la
// produzca: `intakes.customer_note` solo la sabe escribir `CloseIntake` —el cierre
// del carrito numérico—, y `UpsertIntake`, que es el puerto de esta etapa, «ni
// menciona la columna» (store.go). O sea que el día que la nota exista se perdería
// SIN ERROR y sin que nadie se enterase. Con esto, se entera.
func (s *Draft) notaDePedidoPerdida(job intake.ClaimedJob, art *ArtefactoMatch) {
	if art.CustomerNote == "" {
		return
	}
	// El aviso NO cita la nota: es texto del cliente (ADR-0034).
	s.log.Warn("draft: el borrador trae nota de pedido y esta etapa NO la puede persistir (intakes.customer_note solo la escribe el cierre del carrito)",
		"job_id", job.ID, "stage", intake.StageDraft, "runas", len([]rune(art.CustomerNote)))
}

// transcurrido es `elapsed_ms`: cuánto esperó el cliente desde que escribió hasta que
// su borrador existió. Es el KPI del plan entero («tiempo a primer borrador < 5 min»).
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 DE QUÉ RELOJ SALE CADA EXTREMO
// ════════════════════════════════════════════════════════════════════════════
//
//	FIN    → `s.ahora()`: el reloj de GO del proceso que corre el pipeline (cloud).
//	INICIO → `job.MessageTS`: NO es del reloj del cloud. Es el `ts_unix` del mensaje
//	         del CLIENTE que abrió la ventana (intake.Append.MessageTS, «no el reloj
//	         del servidor»), o sea el reloj del EDGE, guardado en
//	         `intake_jobs.message_ts` (TIMESTAMPTZ) y leído de vuelta a Go.
//
// Son DOS RELOJES, y eso se dice en vez de esconderlo. Se restan igual, por dos
// razones que sí se sostienen:
//
//  1. es lo que pide el criterio —«el evento trae `elapsed_ms` desde `message_ts`»— y
//     lo que mide el KPI: la espera que interesa es la DEL CLIENTE, y esa empieza
//     cuando él escribió, no cuando nos enteramos;
//  2. la magnitud es de MINUTOS (objetivo < 5) y la deriva entre dos relojes con NTP
//     es de milisegundos a segundos: ruido frente a lo medido.
//
// Lo que NO se hace con estos dos instantes es DECIDIR nada. Comparar «¿ocurrió antes
// o después?» entre relojes distintos sí es el defecto permanente y silencioso; medir
// una duración larga, no.
//
// Y por si la deriva fuera grande —o el Edge tuviera la hora mal—, un resultado
// NEGATIVO no se publica: se recorta a 0 y se avisa. Un `elapsed_ms` negativo en un
// panel no se lee como «relojes desajustados», se lee como un bug del pipeline.
func (s *Draft) transcurrido(job intake.ClaimedJob) time.Duration {
	if job.MessageTS.IsZero() {
		// Sin instante del cliente no hay nada que restar. Cero es el único valor
		// honesto, y el aviso dice que el número que se publica no mide la espera.
		s.log.Warn("draft: el job no trae message_ts; elapsed_ms se publica como 0 y NO mide la espera del cliente",
			"job_id", job.ID, "stage", intake.StageDraft)
		return 0
	}
	d := s.ahora().Sub(job.MessageTS)
	if d < 0 {
		s.log.Warn("draft: elapsed_ms salió NEGATIVO; el reloj del Edge y el del cloud están desalineados",
			"job_id", job.ID, "stage", intake.StageDraft, "desfase_ms", d.Milliseconds())
		return 0
	}
	return d
}

// publicarMetrica escribe la fila de `flow_events`. BEST-EFFORT: un fallo se avisa y
// la etapa sigue — el borrador ya está escrito y perder una fila de telemetría no
// puede costar el pedido de un cliente.
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 EN ESTE PAYLOAD NO ENTRA NI UNA PALABRA DEL CLIENTE
// ════════════════════════════════════════════════════════════════════════════
//
// Son cuatro CONTADORES y nada más, y es la forma exacta que fija design §10
// (`{"elapsed_ms":174000,"lines":4,"matched":2,"unmatched":1}`). Ni el texto, ni las
// evidencias, ni las etiquetas de los productos: `flow_events` es una tabla en claro
// que se lee entera desde la telemetría, y el ADR-0034 no admite ahí ni el literal ni
// nada que identifique. El `contact_id` que lleva la fila es el OPACO de la clave de
// ventana (ADR-0010/ADR-0017): nunca un número ni un JID — no podría serlo aunque
// alguien quisiera, porque `WindowKey` no transporta otra cosa.
func (s *Draft) publicarMetrica(ctx context.Context, job intake.ClaimedJob, art *ArtefactoMatch, transcurrido time.Duration) {
	casadas, sinCasar := s.recuento(art)
	err := s.eventos.InsertFlowEvent(ctx, store.FlowEvent{
		TenantID:    job.Key.TenantID,
		ContactID:   job.Key.ContactID,
		FlowID:      FlujoCaptacion,
		FlowVersion: VersionFlujoCaptacion,
		Kind:        kindEventoFlujo,
		Name:        EventoBorradorCreado,
		Payload: map[string]any{
			"elapsed_ms": transcurrido.Milliseconds(),
			"lines":      len(art.Lines),
			"matched":    casadas,
			"unmatched":  sinCasar,
		},
	})
	if err != nil {
		s.log.Warn("draft: no se pudo publicar la métrica del borrador; el borrador SÍ está creado",
			"job_id", job.ID, "stage", intake.StageDraft, "name", EventoBorradorCreado, "error", err.Error())
	}
}

// recuento cuenta las líneas por clase. El envío no es ninguna de las dos: no casa
// con el catálogo porque no sale de él —lo pone la plataforma (D-041.11)—, así que
// contarlo como `unmatched` diría que hay un producto que el tenant no vende.
func (*Draft) recuento(art *ArtefactoMatch) (casadas, sinCasar int) {
	for _, l := range art.Lines {
		switch l.Kind {
		case KindMatched:
			casadas++
		case KindUnmatched:
			sinCasar++
		}
	}
	return casadas, sinCasar
}

// persistir deja el artefacto bajo `artifacts.draft`. Mismo patrón que las demás
// etapas: si el UPDATE no toca la fila es que el job ya no está en `processing` —lo
// soltó el watchdog, o lo terminó otro— y eso NO es un error de esta etapa.
func (s *Draft) persistir(ctx context.Context, jobID string, art *ArtefactoDraft) error {
	payload, err := json.Marshal(art)
	if err != nil {
		return fmt.Errorf("draft: serializar el artefacto: %w", err)
	}
	guardado, err := s.store.SaveStage(ctx, jobID, intake.Artifact{
		Stage:   intake.StageDraft,
		Payload: payload,
	})
	if err != nil {
		return fmt.Errorf("draft: persistir el artefacto: %w", err)
	}
	if !guardado {
		return ErrJobFueraDeProcessing
	}
	return nil
}

// ════════════════════════════════════════════════════════════════════════════
// LAS PREGUNTAS SUGERIDAS
// ════════════════════════════════════════════════════════════════════════════

// Las preguntas que el sistema prepara. Son literales de CONTRATO —design §7.4 las
// escribe así— y no se arman en el sitio donde se usan para que no haya dos versiones
// del mismo texto.
const (
	// preguntaZonaDeEnvio es la del envío sin precio. Sale literal de §7.4.
	preguntaZonaDeEnvio = "¿Zona de entrega para calcular el envío?"
	// preguntaTamañoConRango y preguntaTamañoSinRango son las dos formas de la
	// pregunta por la variante. La primera es la de §7.4 con el rango que pidió el
	// cliente dentro; la segunda es para cuando no hay rango que citar. La unidad
	// llega con su espacio delante o vacía (ver unidadDelRango).
	preguntaTamañoConRango = "¿Confirmas el tamaño de «%s»: %d o %d%s?"
	preguntaTamañoSinRango = "¿Cuál de las presentaciones de «%s» necesitas?"
)

// preguntasSugeridas deriva de las líneas lo que hay que PREGUNTARLE AL CLIENTE.
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 SON PREGUNTAS PARA EL CLIENTE, NO TAREAS PARA EL DUEÑO
// ════════════════════════════════════════════════════════════════════════════
//
// De ahí sale toda la regla, y de ahí sale lo que NO entra. Solo hay dos huecos que
// únicamente el cliente puede rellenar:
//
//   - LA VARIANTE. El producto está en el catálogo pero no se sabe de qué tamaño lo
//     quiere («10 o 12 porciones»): la línea salió con `variant_options` y sin precio
//     porque elegir por él sería inventar (match_lineas.go).
//   - LA ZONA DE ENVÍO. El envío va siempre y sale «por confirmar» cuando el tenant
//     no tiene zonas o tiene varias: quien sabe dónde vive es el cliente.
//
// Y LO QUE NO GENERA PREGUNTA, con su porqué:
//
//   - Las líneas `unmatched`. Que el catálogo no tenga la torta de vainilla no es una
//     duda del cliente —él ya dijo lo que quiere—: es un precio que tiene que poner el
//     DUEÑO. Preguntarle a un cliente cuánto cuesta lo que pide es absurdo, y es
//     además la línea que §7.5 pinta como «¿precio?» editable en la bandeja.
//   - Los avisos del match (cantidad inválida, indicación que no cabe). Son cosas que
//     el dueño tiene que MIRAR en el original, no cosas que se pregunten.
//
// El orden es el de las líneas, que es determinista: dos ejecuciones con el mismo
// borrador dan las mismas preguntas en el mismo orden, y por eso un test las puede
// afirmar en vez de contarlas.
func preguntasSugeridas(lineas []Linea) []string {
	out := make([]string, 0, 2)
	for _, l := range lineas {
		if p, ok := preguntaDeLinea(l); ok {
			out = append(out, p)
		}
	}
	return out
}

// preguntaDeLinea devuelve la pregunta de UNA línea, si la tiene.
func preguntaDeLinea(l Linea) (string, bool) {
	if l.Kind == KindShipping {
		// Un envío YA precificado no se pregunta: la zona estaba resuelta y cobrada.
		if l.UnitPrice != nil {
			return "", false
		}
		return preguntaZonaDeEnvio, true
	}
	if len(l.VariantOptions) == 0 {
		return "", false
	}
	// La etiqueta es la del CATÁLOGO (así la construye el match para toda línea
	// `matched`), no las palabras del cliente: es el nombre con el que el dueño
	// conoce su producto, y es el que va a leer quien reciba la pregunta.
	if l.Range == nil {
		return fmt.Sprintf(preguntaTamañoSinRango, l.Label), true
	}
	return fmt.Sprintf(preguntaTamañoConRango, l.Label, l.Range.Min, l.Range.Max, unidadDelRango(l.Range.Unit)), true
}

// unidadDelRango es la unidad que citó el cliente («porciones»), pegada al número con
// su espacio. Vacía NO se sustituye por una inventada: la pregunta queda «: 10 o 12?»,
// que sigue siendo legible, y ponerle al cliente una palabra que no dijo sería
// exactamente lo que el resto de este pipeline se prohíbe.
func unidadDelRango(unidad string) string {
	if u := strings.TrimSpace(unidad); u != "" {
		return " " + u
	}
	return ""
}
