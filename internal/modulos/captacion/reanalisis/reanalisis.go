// Porta internal/reanalisis/reanalisis.go @ 56097aa

// Package reanalisis es el caso de uso de `POST /api/v1/intakes/{id}/reanalyze`
// (Plan 044 · Ola 4 · T4.6; D-044.15, REQ-24b/c/d/e, contrato completo en design
// §8.1): el dueño mira un presupuesto que la máquina interpretó mal y pide que lo
// vuelva a leer DESDE EL ORIGEN.
//
// # POR QUÉ UN PAQUETE PROPIO Y NO UN MÉTODO MÁS EN `intakes`
//
// Porque esta operación cruza CINCO fronteras que ningún otro caso de uso de la
// bandeja cruza a la vez: los entitlements del tenant, su configuración LLM
// (`tenant_llm`), el hilo cifrado del evento (`conversation_event_messages`), la
// cola del pipeline (`intake_jobs`) y el compositor del literal. Meterlo en
// `intakes.Service` obligaría a ese Service —que hoy sabe de solicitudes y de nada
// más— a depender de las cinco, y a que todo test suyo las montara. Aquí las cinco
// entran por puertos estrechos declarados del lado del consumidor.
//
// # 🔴 ESTE PAQUETE NO LE HABLA AL CLIENTE, Y NO PUEDE HACERLO (INV-1 / INV-12)
//
// Mira la lista de puertos: no hay ninguno que mande un mensaje. No hay Gateway, no
// hay Notifier, no hay SendText. Un re-análisis es una operación INTERNA del dueño
// sobre su propio pedido —el cliente ni se entera, y no debe enterarse: no le ha
// pasado nada a su pedido— y esa garantía es estructural, no una promesa en un
// comentario: los seis puertos tienen EXACTAMENTE los métodos que se declaran aquí
// (lo custodia TestPorts_NoneCanReachTheCustomerNorAnOldEnvelope).
//
// # 🔴 Y NO ESPERA AL LLM
//
// El endpoint ABRE el job y devuelve. El pipeline lo reclama después, por su cuenta,
// y puede tardar minutos o morir. Eso no es una limitación: es lo que hace que el
// criterio INV-10 de T4.6 se cumpla solo. Con el proveedor caído de verdad, esta
// función ya devolvió hace rato; la revisión anterior sigue intacta porque nadie
// escribió una nueva, la solicitud no cambió de estado porque aquí no se transiciona
// nada, y el cliente no recibió nada porque no hay por dónde.
//
// # EL ORDEN DE LOS CHEQUEOS ES EL CONTRATO, Y ESTÁ RAZONADO EN Reanalyze
//
// design §8.1 lo fija: forma → gate base → gate de vía → credencial → solicitud →
// job vivo → fuente. Lo consume la cara HTTP, que traduce cada desenlace a su código.
//
// # LOS FICHEROS (E-13: el viejo, de 772 líneas, se parte por tema)
//
//   - reanalisis.go         — la petición, la respuesta, los puertos, el servicio y
//     el orden de Reanalyze.
//   - reanalisis_errors.go  — los desenlaces con nombre y sus textos.
//   - reanalisis_checks.go  — los escalones de lectura 1–8: forma, gates, vía,
//     credencial, solicitud y job vivo. Sin exportados: nace con la lógica.
//   - reanalisis_source.go  — el material: la fuente (escalón 9) y el texto pegado.
//     Sin exportados: nace con la lógica.
package reanalisis

import (
	"context"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/tenantllm"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// ---------------------------------------------------------------------------
// EL CONTRATO (design §8.1)
// ---------------------------------------------------------------------------

// Request (antes `Solicitud`) es el cuerpo de la petición, ya con el tenant del token
// (INV-7: el tenant NUNCA sale de la URL ni del cuerpo).
type Request struct {
	TenantID string
	IntakeID string
	// Via AFIRMA por qué vía se quiere correr (`local` | `api`). VACÍA es válida y
	// es el caso normal: se usa la del tenant (`tenant_llm.via`), y sin fila en esa
	// tabla la vía efectiva es `local` (D-044.48 §4).
	//
	// 🔴 EL PROVEEDOR NO ESTÁ AQUÍ Y NO PUEDE ESTARLO (D-044.28 §a): `anthropic` |
	// `gemini` salen SIEMPRE de `tenant_llm`. Aceptarlo en este cuerpo sería dejar
	// que una llamada suelta se salte la configuración del tenant.
	Via string
	// Text es material EXTRA del dueño (Plan 045 D-045.5): la transcripción de un
	// audio, el mensaje que le llegó por otro canal. SUMA al literal del evento, no
	// lo sustituye. Vacío es el caso normal —«regenera otra vez, según el origen»—.
	Text string
}

// Result (antes `Resultado`) es el 200 del contrato §8.1.
type Result struct {
	IntakeID string
	// RevisionNo es el número que le TOCARÁ a la revisión que escriba este job: el
	// vigente más uno.
	//
	// 🔴 ES UNA PREVISIÓN Y SE PUBLICA IGUAL. El §8.1 publica `{"revision_no":3, …}`
	// de forma SÍNCRONA, y la revisión solo se escribe cuando el job TERMINA —
	// minutos después, en otro proceso, y puede no terminar nunca—. Puede no
	// cumplirse de dos formas: el job muere, o entra otra revisión entremedias (una
	// corrección del dueño por `PUT …/items`) y la del re-análisis acaba siendo la
	// siguiente. Lo que lo hace honesto es el campo de al lado: `status` sale
	// SIEMPRE como `processing`, que significa «esto todavía no ha pasado». La
	// alternativa descartada era omitirlo: rompe el contrato escrito sin ganar nada,
	// porque el consumidor lo deduciría con la misma aritmética.
	//
	// ⚠️ NO ES EL CASO DEL LITERAL `RevisionNo: 1` QUE T4.10 TUVO QUE MATAR. Aquel
	// AFIRMABA el número de una revisión ya escrita y lo afirmaba mal. Éste no
	// afirma nada sobre el pasado: quien necesite el número CIERTO lo lee de la
	// solicitud cuando el job acabe.
	RevisionNo int
	JobID      string
	// Via es la vía EFECTIVA con la que se abrió el job, ya resuelta.
	Via string
	// Status es el estado DE LA PETICIÓN visto por quien llama, no el de la fila.
	// Siempre StatusInProgress.
	Status string
}

// StatusInProgress (antes `EstadoEnCurso`) es el `status` del 200 (design §8.1:
// `"status":"processing"`).
//
// ⚠️ NO ES `intake_jobs.status`. El job nace en `pending` —ver
// `intake.ReanalysisRequest` para el porqué— y solo pasa a `processing` cuando un
// worker lo reclama, segundos o minutos después. Lo que este literal dice es «tu
// petición se aceptó y hay trabajo en marcha». Coinciden en el nombre y no en el
// sujeto; por eso está declarado aquí y no se importa de `intake`.
const StatusInProgress = "processing"

// ---------------------------------------------------------------------------
// LOS PUERTOS
// ---------------------------------------------------------------------------

// Intakes (antes `Solicitudes`) es lo ÚNICO que este caso de uso necesita del dominio
// de solicitudes: leer de qué evento cuelga una y por qué revisión iba. NO puede
// transicionar, NO puede editar líneas y NO puede escribir revisiones — y eso es el
// mecanismo por el que «el re-análisis no cambia el estado de la solicitud» (INV-10,
// R-10) se sostiene en los tipos y no en una promesa. Lo satisface
// `*intakes.Postgres` del módulo `solicitudes`.
type Intakes interface {
	ReanalysisTargetOf(ctx context.Context, tenantID, intakeID string) (intakes.ReanalysisTarget, error)
}

// Thread (antes `Hilo`) es el historial cifrado del evento, DESCIFRADO en el borde
// (REQ-10c). Lo satisface `*events.Store`, que es quien tiene el FieldCipher; por sus
// tipos (`events.ThreadEntry`) este paquete declara un puente de import a
// `internal/flujos/events`, que muere en F8.
//
// `AppendPastedMessage` añade el texto del dueño como UNA fila más del hilo, con
// `origin='owner_pasted'`, y devuelve su `seq`; `ListPastedByOwner` devuelve, ya
// descifrados, los cuerpos de las filas con ese origen.
type Thread interface {
	ListThread(ctx context.Context, eventID string, limit int) ([]events.ThreadEntry, error)
	ListPastedByOwner(ctx context.Context, eventID string) ([]string, error)
	AppendPastedMessage(ctx context.Context, eventID, body string) (int, error)
}

// Jobs es la cola del pipeline vista por esta puerta: preguntar si hay un job vivo y
// abrir el del re-análisis (`LiveJobOfEvent` y `OpenReanalysis`, antes
// `JobNoTerminalDeEvento` y `AbrirReanalisis`). Deliberadamente NO es
// `intake.JobStore` ni `intake.PipelineStore`: desde aquí no se puede reclamar, ni
// avanzar etapa, ni terminar un job, ni leer el sobre de ninguno. Lo satisface
// `*intake.Postgres`.
type Jobs interface {
	LiveJobOfEvent(ctx context.Context, tenantID, eventID string) (string, bool, error)
	OpenReanalysis(ctx context.Context, req intake.ReanalysisRequest) (string, error)
}

// Composer (antes `Compositor`) lee el hilo, lo compone, lo cifra y lo guarda en el
// sobre del job. Es el MISMO compositor que corre al cerrar una ventana del pipeline
// normal (R-08, T-5), y por eso este paquete no escribe un segundo: dos caminos que
// producen el `source_text` divergirían en el primer rótulo que cambie. La clave es el
// `intake.WindowKey` NUEVO; el adaptador que lo cose al compositor viejo es del
// arranque (F7-04).
type Composer interface {
	ComposeAtFlush(ctx context.Context, key intake.WindowKey) error
}

// Features resuelve los derechos comerciales del tenant. Lo satisface el resolver de
// `entitlements` — el MISMO resolver cacheado que gatea el resto del carril, nunca uno
// nuevo: dos resolvers serían dos cachés y dos verdades sobre el plan.
type Features interface {
	Has(ctx context.Context, tenantID, feature string) (bool, error)
}

// LLMConfig (antes `ConfigLLM`) lee la configuración LLM del tenant. Es el puerto
// RECORTADO que NO puede devolver la credencial: `tenantllm.Config` dice SI hay clave
// (`HasAPIKey`), no cuál es. Lo satisface `*tenantllm.Postgres`.
type LLMConfig interface {
	Get(ctx context.Context, tenantID string) (tenantllm.Config, bool, error)
}

// ---------------------------------------------------------------------------
// EL SERVICIO
// ---------------------------------------------------------------------------

// Service (antes `Servicio`) ejecuta el re-análisis. Sin estado propio: todo lo que
// decide sale de los puertos, del límite del hilo y del cuerpo de la petición.
type Service struct{}

// NewService (antes `NewServicio`) construye el caso de uso.
//
//   - Las SIETE piezas son obligatorias. Con cualquiera de las seis primeras o `config`
//     a nil devuelve `(nil, ErrNotWired)`: es preferible no montar la ruta a montarla y
//     que responda 500 a medio camino. Un servicio sin compositor abriría jobs con el
//     sobre vacío que el pipeline mataría uno a uno, y nadie lo notaría hasta mirar la
//     tabla.
//   - `threadLimit` es cuántas entradas del hilo se piden para decidir si hay material.
//     🔴 TIENE QUE SER EL MISMO LÍMITE CON EL QUE COMPONE EL COMPOSITOR: la pregunta
//     «¿hay material?» tiene que mirar exactamente las entradas que se van a componer,
//     o un hilo largo pasaría la comprobación y se compondría vacío. El viejo lo
//     importaba (`runtime.DefaultThreadLimit`, 200); aquí ENTRA POR CONSTRUCTOR para no
//     declarar un puente de import a `internal/flujos/runtime` ni una segunda constante
//     (dos constantes serían dos verdades). Lo pasa el arranque.
//   - Un `threadLimit` <= 0 se RECHAZA: `(nil, error)` que envuelve ErrNotWired
//     (`errors.Is`) y cuyo texto es
//     `reanalisis: el límite del hilo debe ser positivo (<n>): ` seguido del de
//     ErrNotWired. No se sustituye por un valor por defecto: `ListThread` con un límite
//     <= 0 devuelve un hilo vacío, y toda petición saldría `never_stored` en silencio.
func NewService(log logger.Logger, intakeReader Intakes, thread Thread, jobs Jobs,
	composer Composer, features Features, config LLMConfig, threadLimit int) (*Service, error) {
	panic(pendiente.Implementar("reanalisis.NewService"))
}

// Reanalyze (antes `Reanalizar`) abre el job del re-análisis y devuelve lo que el
// contrato §8.1 publica. Sobre un `*Service` nil devuelve ErrNotWired.
//
// # EL ORDEN DE OPERACIONES, Y QUÉ DEVUELVE CADA ESCALÓN
//
//  1. FORMA de `via`: presente y fuera de `local|api` ⇒ InvalidViaError{Via} con
//     `Configured` vacío. No toca ningún puerto: es vocabulario.
//  2. FORMA de `text`: se sanea con `intakes.SanitizeNote` (la MISMA puerta que la
//     indicación del cliente; tope de 280 runas, se RECHAZA en vez de truncar). Su
//     rechazo sale envuelto —`reanalisis: el texto pegado no pasa el saneo: %w`— y
//     deja leer el `intakes.NoteTooLongError` con `errors.As`. Un texto que sanea a
//     VACÍO equivale a no haber mandado `text`. Tampoco toca ningún puerto.
//  3. Gate `llm_intake` (el nivel): `Features.Has` ⇒ sin ella,
//     FeatureMissingError{"llm_intake"}. FAIL-CLOSED: un resolver que falla responde
//     lo mismo, y su error NO se propaga (un 5xx invita a reintentar hasta colarse).
//  4. Vía EFECTIVA: `LLMConfig.Get`. Si falla,
//     `reanalisis: leer la configuración LLM del tenant: %w`. La efectiva es la de la
//     fila, y `local` sin fila o con la vía de la fila vacía (D-044.48 §4). `via`
//     AFIRMA, NO CONMUTA (REQ-33, D-044.51): presente y distinta de la efectiva ⇒
//     InvalidViaError{Via, Configured: efectiva}, TAMBIÉN sin fila.
//  5. Gate `api_llm` (la vía), SOLO si la efectiva es `api`: sin ella,
//     FeatureMissingError{"api_llm"}, igual de fail-closed. 🔴 En vía `local` por esa
//     clave NO SE PREGUNTA ni una vez (ADR-0044 · D-044.28): gatea la vía, no la
//     capacidad.
//  6. Credencial, SOLO si la efectiva es `api`: sin clave (`HasAPIKey`) o sin
//     consentimiento (`ConsentedAt` cero) ⇒ CredentialsMissingError{Via: "api"}. No
//     toca ningún puerto: sale de la Config ya leída.
//  7. La SOLICITUD: `Intakes.ReanalysisTargetOf`; su error sale TAL CUAL (el
//     `intakes.ErrNotFound` de una solicitud ajena es el 404: nunca 403, confirmaría
//     que existe). Una solicitud LEGADA, sin evento, es
//     SourceUnavailableError{ReasonNeverStored} y no un 404: existe y el dueño la
//     está mirando; no se pregunta nada más.
//  8. Job vivo del evento: `Jobs.LiveJobOfEvent`; su error sale tal cual. Si lo hay ⇒
//     InProgressError{JobID}. Guarda además la carrera con la ventana viva del
//     cliente, que es `aggregating` y también cuenta.
//  9. La FUENTE: `Thread.ListThread(evento, threadLimit)`. Si falla,
//     `reanalisis: leer el hilo del evento %s: %w`. Solo cuentan las entradas
//     `message` (los `summary` y los `message_out_of_turn` son CONTEXTO: REQ-10b,
//     D-044.24), y de ellas las que conservan texto:
//     - con texto en el hilo y `text` pegado  ⇒ origen `stages.SourceBoth`;
//     - con texto en el hilo                  ⇒ `stages.SourceEventThread`;
//     - sin texto en el hilo y con `text`     ⇒ `stages.SourcePastedText`;
//     - filas `message` todas sin cuerpo      ⇒ SourceUnavailableError{ReasonPurged};
//     - ni una fila `message`                 ⇒ SourceUnavailableError{ReasonNeverStored}.
//  10. — a partir de aquí SE ESCRIBE, en este orden —
//     a. el texto pegado, si lo hay: `Thread.ListPastedByOwner` (si falla,
//     `reanalisis: leer las transcripciones ya pegadas del evento %s: %w`) y, salvo
//     que una de las ya pegadas sea el MISMO texto saneado (se compara su SHA-256),
//     `Thread.AppendPastedMessage` con el texto SANEADO (si falla,
//     `reanalisis: guardar la transcripción pegada en el hilo del evento %s: %w`).
//     Un texto repetido no es un error y no cambia el desenlace.
//     b. el job: `Jobs.OpenReanalysis` con la clave de ventana (tenant de la petición;
//     sesión, contacto y evento de la SOLICITUD, no del cuerpo), el `IntakeID` y el
//     contexto `{RequestedBy: owner, Via: efectiva, Source: origen, From: revisión
//     vigente}`. Su error sale tal cual.
//     c. el sobre: `Composer.ComposeAtFlush` con ESA MISMA clave.
//
// Devuelve `Result{IntakeID, RevisionNo: vigente+1, JobID, Via: efectiva,
// Status: StatusInProgress}`.
//
// 🔴 LA LÍNEA DEL 10 ES LA QUE IMPORTA, Y ES LA RAZÓN DEL ORDEN. Los NUEVE escalones
// anteriores son puras lecturas: una petición que acaba en error antes del 10 no ha
// tocado una sola fila (ni texto pegado, ni job, ni sobre). Si la fuente se comprobara
// DESPUÉS de abrir el job, cada 422 dejaría un `intake_job` en `pending` que ningún
// worker puede completar. Y el gate va delante de la solicitud y de la fuente porque lo
// contrario es una fuga de existencia: a quien no tiene `llm_intake` se le confirmaría
// si la solicitud es suya o si el evento tiene material.
//
// 🔴 EL ÚNICO PUNTO EN QUE ESTE ORDEN SE APARTA DE LA LISTA DEL §8.1: el contrato pone
// TODO el 400 «antes de cualquier gate»; aquí está PARTIDO. La mitad de VOCABULARIO
// (escalón 1) va antes que nada; la de COINCIDENCIA (escalón 4) va después del gate
// base a propósito, porque su cuerpo publica `configured_via` —la configuración LLM del
// tenant— y contestársela a quien no tiene `llm_intake` sería responder antes de gatear.
//
// ⚠️ CONSECUENCIA DE «`via` AFIRMA»: el escalón 6 es DEFENSA EN PROFUNDIDAD. Para llegar
// a él la efectiva tiene que ser `api`, y una fila `via='api'` la garantiza COMPLETA el
// CHECK `tenant_llm_via_api_completa_check` de la 0073. Solo lo alcanza una fila que la
// base no puede producir (un restore parcial, un `UPDATE` a mano), que tiene que salir
// por un error con nombre y no por un 500.
//
// 🔴 LOS TRES PASOS DEL 10 NO SON TRANSACCIONALES, a propósito: son tres sentencias en
// tres tablas, y envolverlas exigiría que este paquete manejara el `*sql.DB` de los
// tres stores. Lo que se paga es benigno: si falla la apertura del job, la fila del
// texto pegado queda escrita y SIRVE —el siguiente re-análisis la leerá—; si falla la
// composición, la petición NO falla (el job YA existe: un error le diría al dueño que
// no pasó nada mientras la cola tiene trabajo) y deja el `Error`
// `reanalisis: el job quedó abierto pero SIN literal; el worker lo matará al
// reclamarlo` con `tenant_id`, `intake_id`, `event_id`, `job_id` y `error`.
//
// # EL LOG LLEVA IDENTIFICADORES Y NÚMEROS, NUNCA CONTENIDO (REQ-10c, ADR-0034)
//
// Al terminar, el `Info` `reanalisis: job abierto a petición del dueño` con
// `tenant_id`, `intake_id`, `event_id`, `job_id`, `via`, `source`, `reanalyzed_from`,
// `status_intake` y `runas_pegadas`. Ni el texto pegado, ni el literal del hilo, ni un
// trozo de ninguno de los dos salen por ningún log de este paquete.
func (s *Service) Reanalyze(ctx context.Context, req Request) (Result, error) {
	panic(pendiente.Implementar("reanalisis.Service.Reanalyze"))
}
