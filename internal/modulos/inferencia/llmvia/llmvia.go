// Porta internal/llmvia/llmvia.go @ ebf4eb7 (la selección: For, Warm y PlazaDe; el turno acotado vive en llmvia_turno.go, E-13)

// Package llmvia es EL ÚNICO SITIO DEL REPO QUE PREGUNTA POR LA VÍA (Plan 044 ·
// Ola 1.6 · T1.6-3; D-044.28, D-044.29, REQ-33, REQ-37, ADR-0044 §C2, ADR-0045;
// el aforo de plaza es del ADR-0046 y el aviso al dueño del ADR-0044 §5).
//
// # Qué resuelve
//
// Un tenant tiene UNA vía activa —`local` (su propio Edge) o `api` (un proveedor
// externo con su credencial)— y el pipeline no debe saber cuál le tocó. Este paquete
// traduce `tenant_llm.via` a un llm.LLMProvider ya armado, y a partir de ahí todo el
// mundo llama a los mismos cinco métodos.
//
// # 🔴 C2: «SI HAY UN `if via` FUERA DEL ADAPTADOR, ES DEFECTO» (REQ-37)
//
// La selección es un switch de arranque sobre la vía y NADA MÁS: no hay enrutador
// por tarea ni registro de vías (D-044.21 sigue muerta). Y eso NO se confía a la
// disciplina de nadie: el candado de c2_via_test.go recorre el AST del árbol nuevo y
// falla si aparece una comparación por vía fuera de la lista de sitios permitidos
// —lista que es corta, explícita y con el motivo de cada uno escrito al lado—.
//
// De este paquete, EL ÚNICO fichero que compara por vía es llmvia.go. Las cuatro
// entradas del selector que dependen de ella (For, Warm, PlazaDe y Turno) responden
// con el mismo vocabulario cerrado, el mismo default de REQ-33 (sin fila ⇒ local) y
// el mismo error para el valor inventado: se amplían JUNTAS o una empieza a mentir.
// Si necesitas saber la vía en otro sitio, lo que necesitas de verdad es otro método
// en el puerto — así nacieron PlazaDe, Warm y Turno.
//
// # Por qué el provider se construye POR PETICIÓN y no una vez al arrancar
//
// Porque la vía es POR TENANT y cambia sin desplegar: es una fila que el dueño edita
// con un PUT. Un provider cacheado al arranque serviría la vía de ayer, y peor aún,
// la vía de ayer con la credencial de ayer. El coste de construirlo es un SELECT (la
// fila) más, en la vía API, un descifrado del sobre; el de la vía local es cero.
// Cachear eso exigiría invalidar por escritura, que es exactamente el tipo de estado
// que este ecosistema evita cuando no ha medido que haga falta.
//
// # La credencial no pasa por aquí más de lo imprescindible
//
// El store separa Get (sin clave) de APIKey (con clave) a propósito, y esa
// separación se respeta: la clave se pide SOLO en la rama `api` de For, se le entrega
// al provider y no se guarda, no se loguea y no vuelve a nadie.
//
// # Sin estado de proceso
//
// El paquete no tiene goroutines, cachés ni candados: el Selector es inmutable tras
// NewSelector y cada llamada arma lo suyo.
package llmvia

import (
	"context"
	"errors"
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/degradation"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/llmvia/local"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/tenantllm"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// ErrViaDesconocida indica que la fila del tenant declara una vía que este código no
// sabe servir. En la práctica solo puede llegar aquí si alguien escribió la columna
// por fuera del CHECK `tenant_llm_via_check`: se devuelve error y NO se elige una
// vía por defecto, porque adivinar la vía de un tenant es exactamente lo que REQ-33
// prohíbe («mientras un tenant tenga una vía configurada, el sistema jamás deberá
// usar la otra»).
//
// Quien lo devuelve lo ENVUELVE (errors.Is lo encuentra) nombrando la vía y el
// tenant, con este formato literal: `<ErrViaDesconocida>: "<vía>" (tenant <tenant>)`,
// con la vía en %q. El texto es observable y no cambia.
var ErrViaDesconocida = errors.New("llmvia: vía fuera del vocabulario cerrado (local|api)")

// ErrSinConfig indica que el Selector se construyó sin store. Fallo de arranque. El
// texto es observable y no cambia.
var ErrSinConfig = errors.New("llmvia: el selector necesita el store de tenant_llm")

// Store es lo que el selector necesita de la configuración LLM del tenant. Lo
// satisfacen *tenantllm.Postgres y, en los tests, tenantllmhelpertest.Memoria.
//
// Son los DOS métodos, y no uno: Get devuelve la vía sin la credencial (y es lo
// único que la rama local necesita), APIKey la descifra (y solo la rama api la
// llama). Pedir un puerto con un solo método «que lo devuelva todo» reintroduciría
// la clave en el camino de la vía local, donde no pinta nada.
type Store interface {
	// Get devuelve la configuración del tenant SIN la credencial. found false ⇒ el
	// tenant no tiene fila, que es un estado legítimo y significa la vía local
	// (REQ-33).
	Get(ctx context.Context, tenantID string) (tenantllm.Config, bool, error)
	// APIKey devuelve la credencial descifrada de un tenant en vía api, o
	// tenantllm.ErrNotConfigured si no la tiene.
	APIKey(ctx context.Context, tenantID string) (string, error)
}

// Notifier escribe el aviso de degradación al dueño. Lo satisface
// *degradation.Notifier.
//
// El instante viaja como argumento —no lo pone el escritor— porque de él depende la
// ventana de dedupe: dos fallos de la misma ventana tienen que caer en el mismo
// bucket o REQ-38 se rompe por el camino largo.
type Notifier interface {
	// Record registra que la vía `via` del tenant falló por `reason` en el instante
	// `at`. creado true ⇒ nació un aviso; false ⇒ se colapsó sobre el de su ventana.
	Record(ctx context.Context, tenantID string, reason degradation.Reason, via string, at time.Time) (bool, error)
}

// Selector traduce la vía configurada de un tenant en un llm.LLMProvider listo para
// usar, ya envuelto con la notificación de degradación, y responde a las otras tres
// preguntas que dependen de la vía (Warm, PlazaDe, Turno).
//
// Solo se construye con NewSelector. Es INMUTABLE tras construirse y seguro para uso
// concurrente: ninguna de sus entradas lo muta, y dos goroutines pueden compartir el
// mismo valor (R4.7.b: en el proceso hay UNO).
type Selector struct{}

// SelectorOption configura el Selector al construirlo. Las opciones se aplican en el
// orden en que se pasan a NewSelector.
type SelectorOption func(*Selector)

// WithFrame inyecta el transporte de la vía local (lo satisface *edgegrpc.Server,
// sin adaptador). Sin él, un tenant en vía local falla al construir su provider —
// con local.ErrSinTransporte, nunca en silencio—.
func WithFrame(f local.Frame) SelectorOption {
	panic(pendiente.Implementar("llmvia.WithFrame"))
}

// WithNotifier inyecta el escritor de avisos de degradación. Sin él el sistema
// degrada igual (la conducta de Nivel A no depende de esto) pero NADIE SE ENTERA: no
// se escribe aviso y el provider que devuelve For va SIN envoltura. Se admite nil a
// propósito —equivale a no pasar la opción— para que los tests y los arranques
// parciales no arrastren una base de datos.
func WithNotifier(n Notifier) SelectorOption {
	panic(pendiente.Implementar("llmvia.WithNotifier"))
}

// WithLocalOptions añade opciones del adaptador local (plantillas, techo de salida,
// formato, red de seguridad). Llegan a TODO local.Provider que el selector arme: el
// de For y el de Warm.
//
// 🔴 ACUMULA, NO ASIGNA — Y ES A PROPÓSITO (T-3). El arranque llama a esta función
// DOS VECES en la misma construcción del Selector: una con local.ConPlantillas(...)
// y otra, por separado, con local.WithMaxOutputTokens(...). Las opciones de TODAS las
// llamadas llegan al provider, en el orden en que se pasaron. Cuando asignaba, la
// segunda llamada pisaba en silencio a la primera, ConPlantillas nunca llegaba al
// local.Provider y la palanca WAPP_LLM_PROMPTS_DIR estuvo MUERTA sin que nada
// fallara — nadie la tenía encendida en UAT, así que el defecto no se notó en campo.
//
// La sesión de origen (For) y la de destino (Warm) van DESPUÉS de estas opciones, así
// que una local.WithOriginSession o local.WithTargetSession pasada aquí no las pisa.
func WithLocalOptions(opts ...local.Option) SelectorOption {
	panic(pendiente.Implementar("llmvia.WithLocalOptions"))
}

// WithClock inyecta el reloj con el que se sella el instante del fallo: el `at` que
// recibe Notifier.Record. Para tests. Sin esta opción —o con una función nil— es
// time.Now.
func WithClock(f func() time.Time) SelectorOption {
	panic(pendiente.Implementar("llmvia.WithClock"))
}

// NewSelector construye el selector sobre el store de tenant_llm.
//
//   - cfg nil ⇒ (nil, ErrSinConfig): fallo de arranque, no de llamada.
//   - log puede ser nil: el selector entonces no escribe ningún log y nada más cambia.
//
// No llama al store ni al transporte al construir.
//
// 🔴 UN AVISO AL ARRANCAR, NO UNO POR JOB. Si se inyectó un frame (WithFrame) que
// NO sabe decir qué Edge atendería una inferencia —no tiene el método
// `PlazaDe(tenantID, originSessionID string) (string, bool)`, el de
// *edgegrpc.Server—, NewSelector escribe UN Warn con este texto literal:
//
//	llmvia: el transporte de la vía local no sabe decir qué Edge atiende; el aforo de plaza del pipeline de lote (T2.7) quedará INERTE para este proceso
//
// Con un frame que sí sabe, o sin frame, no escribe nada. La capacidad se resuelve
// aquí UNA vez (y no en cada PlazaDe) para que un transporte que no la tenga no deje
// el aforo inerte en silencio, que es la forma cara de fallar.
func NewSelector(cfg Store, log logger.Logger, opts ...SelectorOption) (*Selector, error) {
	panic(pendiente.Implementar("llmvia.NewSelector"))
}

// For devuelve el llm.LLMProvider de la vía configurada del tenant.
//
// # Qué vía, y qué pasa en cada una
//
// Lee la fila con Store.Get (una vez) y decide:
//
//   - 🔴 SIN FILA ⇒ vía LOCAL, y no es un default defensivo: lo fija REQ-33 y lo dice
//     el contrato del store («la ausencia de fila ES una respuesta y significa una vía
//     concreta»). Tratarlo como «desconocido» dejaría sin pipeline a todo tenant que
//     no haya tocado nunca la pantalla de configuración, que hoy son todos (R4.4.b).
//   - vía `local` ⇒ el adaptador local.Provider de ese tenant sobre el frame de
//     WithFrame. NO se pide la credencial (Store.APIKey no se llama): la vía local no
//     habla con ningún tercero, y descifrar una clave para ella sería sacar del sobre
//     un secreto que nadie va a usar.
//   - vía `api` ⇒ se pide Store.APIKey AQUÍ Y SOLO AQUÍ (una vez) y se construye el
//     provider de wapp-shared/llm/api con el Provider y el Model de la fila y esa
//     clave. No se toca el frame.
//   - cualquier otro valor ⇒ (nil, ErrViaDesconocida envuelto): NO elige por ti. Ni
//     credencial, ni frame, ni aviso.
//
// # La sesión de origen
//
// originSessionID es la sesión de WhatsApp cuya conversación originó la pregunta,
// cuando el llamante la conoce. En la vía local SE AÑADE a las opciones del arranque
// (WithLocalOptions) como local.WithOriginSession y viaja en el frame
// (InferRequest.OriginSessionID): es trazabilidad y, si esa sesión está viva, es
// además el stream por el que sale, así que la pregunta la atiende el mismo Edge que
// recibió el mensaje del cliente. Vacía es un estado legítimo y VIAJA VACÍA: el
// selector no se inventa una sesión. La vía API la ignora, y esa asimetría NO es un
// `if via` encubierto: es un dato que se le pasa al constructor de un adaptador.
//
// 🔴 QUE ESTE PÁRRAFO SEA VERDAD COSTÓ UNA TAREA (T1.7-8). El parámetro se pedía y se
// TIRABA, así que el session_id del frame viajaba SIEMPRE vacío y el Edge lo elegía
// el orden alfabético: con dos Edges del mismo tenant, uno no calentaba NUNCA su
// caché de prefijos. Lo custodia un test, no esta frase.
//
// 🔴 LAS OPCIONES SE COPIAN POR PETICIÓN (T-4). For no muta el Selector: arma su
// lista de opciones en un slice PROPIO. Un append sobre el slice compartido
// escribiría en su array subyacente en cuanto tuviera capacidad de sobra, y dos
// peticiones concurrentes se pisarían la sesión de origen —un cruce de trazabilidad
// entre tenants distintos, silencioso y reproducible una de cada muchas—.
//
// # Los errores
//
//   - Store.Get falla ⇒ (nil, error) que envuelve el del store con el prefijo literal
//     "llmvia: leyendo la configuración LLM del tenant: ". Un SELECT que falla NO es
//     «este tenant está en local»: no se toca el frame ni la credencial, y no se avisa.
//   - Store.APIKey falla ⇒ (nil, error) que lo envuelve con el prefijo literal
//     "llmvia: credencial del tenant no disponible: ". 🔴 El texto NO repite la clave
//     ni su longitud: acaba en un log.
//   - el constructor del adaptador falla (local.ErrSinTransporte, local.ErrSinTenant,
//     api.ErrInvalidConfig, api.ErrUnsupportedProvider) ⇒ (nil, ese error intacto).
//   - 🔴 En todo error el provider devuelto es un nil DE INTERFAZ (T-5): nunca un
//     (*local.Provider)(nil) metido en un llm.LLMProvider, que dejaría de comparar
//     igual a nil.
//
// # El aviso al dueño (notify.go)
//
// El fallo al CONSTRUIR el adaptador (los dos últimos casos de arriba) es un fallo de
// la vía tanto como el fallo al consumirlo —para el dueño, «tu credencial ya no vale»
// y «tu proveedor devolvió 500» son el mismo problema visto en dos momentos—: pasa
// por el mismo aviso, con origen OrigenSeleccion y la vía de la fila. En el éxito, el
// provider vuelve envuelto por el decorador de notify.go, que avisa de cada fallo
// suyo con origen OrigenPipeline; si no hay notificador (WithNotifier) vuelve TAL
// CUAL, sin envoltura.
func (s *Selector) For(ctx context.Context, tenantID, originSessionID string) (llm.LLMProvider, error) {
	panic(pendiente.Implementar("llmvia.Selector.For"))
}

// ErrViaSinCalentamiento indica que el tenant no está en una vía que tenga caché de
// prefijo que calentar. NO es un fallo: es la respuesta correcta para un tenant en
// vía API, y por eso es un error nombrado y no un `nil` mudo — quien lo reciba tiene
// que poder decir «no había nada que hacer» en vez de «lo hice». El texto es
// observable y no cambia.
var ErrViaSinCalentamiento = errors.New("llmvia: la vía del tenant no tiene caché de prefijo que calentar")

// Warm emite UN calentamiento de la caché de prefijo del Edge (T1.7-4).
//
// sessionID es la sesión POR LA QUE debe salir —el Edge cuya caché se quiere llenar—,
// no la conversación que preguntó: aquí no hay ninguna. Viaja como TargetSessionID
// (local.WithTargetSession) y el frame NO lleva sesión de origen.
//
// # Qué vía (el mismo switch de For, no uno nuevo)
//
//   - Store.Get falla ⇒ error con el prefijo literal
//     "llmvia: leyendo la configuración LLM del tenant: ", sin tocar el cable.
//   - sin fila, o vía `local` ⇒ arma el local.Provider del tenant con las opciones del
//     arranque (WithLocalOptions, copiadas como en For) más la sesión de destino, y
//     devuelve lo que devuelva su Warm: el frame sale marcado Warmup. Sin frame ⇒
//     local.ErrSinTransporte.
//   - vía `api` ⇒ ErrViaSinCalentamiento SIN tocar el cable ni pedir la credencial: el
//     prefijo de un proveedor por API lo cachea —o no— el proveedor, y no hay nada en
//     nuestra mano que empujar.
//   - cualquier otro valor ⇒ ErrViaDesconocida envuelto, como For, y tampoco se toca
//     el Edge.
//
// # 🔴 POR QUÉ ESTO VIVE AQUÍ Y NO EN EL PAQUETE DEL CALENTAMIENTO
//
// Porque preguntar «¿este tenant tiene una caché de prefijo?» ES preguntar por la
// vía, y C2 dice que eso se hace en un solo sitio. La alternativa —calentar SIEMPRE,
// sin mirar— no es gratis: al tenant en vía API le gastaría ~50 s del Ollama de SU
// máquina y 250 MB de caché por un prefijo que nadie va a volver a pedir, compitiendo
// además con el clasificador que el propio Edge sí ejecuta.
//
// # Lo que NO hace, y es deliberado
//
//   - 🔴 NUNCA AVISA NI CUENTA (T-10): un calentamiento que falla no es una
//     degradación de la vía del dueño. Nadie lo pidió y su fallo no le quita nada al
//     cliente; avisarle sería mandarlo a revisar un equipo que está bien. Ni escribe
//     aviso ni llama al observador, falle con el motivo que falle. Misma familia que
//     edgegrpc.ErrInferenceAbandoned.
//   - NO bloquea al llamante por su cuenta ni se pone reloj: el ctx lo trae quien
//     llama, que es quien sabe cuánto está dispuesto a esperar por algo que nadie
//     está esperando.
func (s *Selector) Warm(ctx context.Context, tenantID, sessionID string, in llm.ClassifyRequestInput) error {
	panic(pendiente.Implementar("llmvia.Selector.Warm"))
}

// ============================================================================
// QUÉ PLAZA OCUPA UNA INFERENCIA, Y SI OCUPA ALGUNA
// (Plan 044 · Ola 2 · T2.7, ADR-0046 Mecanismo 1)
// ============================================================================
//
// # POR QUÉ ESTA PREGUNTA VIVE AQUÍ Y NO EN EL WORKER DEL PIPELINE
//
// Porque la respuesta DEPENDE DE LA VÍA, y la vía se pregunta en un solo sitio: el
// selector. El worker del pipeline recibe un `(edgeID, ok)` y no sabe —ni tiene por
// qué— por qué un tenant no tiene plaza: puede ser que esté en vía API o que no
// tenga ningún Edge conectado ahora mismo. Las dos cosas significan lo mismo para
// él: no hay plaza que tomar, adelante.
//
// # POR QUÉ LA VÍA API NO TIENE PLAZA
//
// Porque el entero del Mecanismo 1 protege UNA MÁQUINA —un Ollama por Edge—, y por
// la vía API no hay máquina del cliente en el camino: la llamada sale a un proveedor
// remoto que atiende en paralelo. Allí el tope que importa es de PRECIO, no de
// capacidad. Serializar dos cadenas de lote de un tenant en vía API sería una
// restricción inventada: cuesta throughput y no protege nada.

// PlazaDe devuelve el Edge cuya plaza ocuparía una inferencia de este tenant
// originada en esa sesión, o `ok = false` si no ocupa ninguna.
//
// En la vía local (con fila `local` o SIN fila, REQ-33) se lo pregunta al transporte
// de WithFrame, cuando sabe responder —tiene el método
// `PlazaDe(tenantID, originSessionID string) (string, bool)`, el de
// *edgegrpc.Server—, con el tenant y la sesión tal cual, y devuelve su respuesta.
//
// `ok = false` con error nil NO es un fallo y tiene tres orígenes legítimos, todos
// con la misma consecuencia para el llamante:
//
//   - el tenant está en vía API (no hay máquina del cliente que proteger): 🔴 NI
//     SIQUIERA SE LE PREGUNTA al transporte, y el edgeID es "";
//   - el transporte dice que no hay Edge (ninguna sesión viva en esta réplica; la
//     inferencia fallará por su cuenta con `edge_offline`);
//   - el transporte no sabe responder a la pregunta, o no hay transporte (ver
//     NewSelector: se avisa UNA vez al arrancar, no una vez por job); edgeID "".
//
// El error queda para lo que sí lo es, siempre con ("", false):
//
//   - Store.Get falla ⇒ error con el prefijo literal
//     "llmvia: leyendo la configuración LLM del tenant: ";
//   - vía fuera del vocabulario ⇒ ErrViaDesconocida envuelto, como For: una vía
//     inventada no se degrada a «sin plaza», porque eso escondería una fila corrupta
//     detrás de una conducta que parece normal.
//
// No pide la credencial, no avisa y no cuenta.
//
// ⚠️ CUESTA UNA LECTURA DE `tenant_llm` POR JOB DE LOTE, no por llamada al modelo:
// se resuelve una vez, antes de la cadena, y la cadena dura minutos.
func (s *Selector) PlazaDe(ctx context.Context, tenantID, originSessionID string) (string, bool, error) {
	panic(pendiente.Implementar("llmvia.Selector.PlazaDe"))
}
