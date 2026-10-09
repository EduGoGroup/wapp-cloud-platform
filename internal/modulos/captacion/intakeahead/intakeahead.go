// Porta internal/intakeahead/intakeahead.go @ 56097aa

// Package intakeahead es EL ADELANTO DE VENTANA POR PULL (Plan 044 · Ola 1.6 ·
// T1.6-4; D-044.31, REQ-09, REQ-35, INV-10, ADR-0045).
//
// # QUÉ RESUELVE, Y QUÉ MURIÓ PARA QUE HICIERA FALTA
//
// Hasta la Ola 1.6 el Edge clasificaba el entrante y ADJUNTABA la intención al
// mensaje. El agregador leía esa etiqueta y, si era `intake_request` con confianza
// suficiente, adelantaba el cierre de la ventana. Ese push MURIÓ con D-044.31: la
// intención salió del contrato, y con ella se fue la única fuente de la señal (la
// `Signal.Intent` del agregador es SIEMPRE nil, I-CP-2).
//
// El Cloud ya no RECIBE la intención: la PIDE (R-11: el Cloud orquesta la inferencia,
// el Edge solo la sirve). Este paquete es quien la pide.
//
// # 🔴 LA REGLA QUE GOBIERNA TODO ESTE PAQUETE: EL TURNO NO ESPERA (REQ-35)
//
// `ClassifyRequest` es SÍNCRONO y tarda SEGUNDOS: el p50 medido en campo sobre el
// VPS con qwen3:1.7b es de 8,1 s, y hay corridas de más de 30 s. El sitio desde el
// que se dispara —el `Observe` del agregador— corre EN LÍNEA con el mensaje del
// cliente. Poner ahí una llamada síncrona sería añadirle ocho segundos a cada
// respuesta de WhatsApp, que es exactamente lo que INV-10 y REQ-35 prohíben.
//
// Por eso `Request` NO BLOQUEA NUNCA y NO DEVUELVE ERROR: encola y vuelve. Lo que
// pasa después —que la vía esté caída, que el modelo tarde, que la cola esté llena—
// degrada SOLO el adelanto. La ventana cierra por silencio como cierra hoy, y el
// flujo estático ni se entera.
//
// # 🔴 EL `ctx` DEL TURNO NO SIRVE AQUÍ, Y NO ES UN DETALLE
//
// El contexto del entrante se cancela en cuanto el turno termina —milisegundos—,
// mientras que la inferencia dura segundos. Si el worker heredara ese contexto,
// TODA petición moriría cancelada y el adelanto no funcionaría jamás, sin un solo
// error que lo delatara. Por eso `Request` ni siquiera ACEPTA un `ctx`: el reloj de
// la inferencia sale del ctx de `Run` (el del proceso) más el presupuesto propio.
// Se aplica por construcción en vez de por disciplina: la firma no deja pasarle el
// contexto equivocado.
//
// # LA RÁFAGA: POR QUÉ 50 MENSAJES NO SON 50 INFERENCIAS
//
// Tres cerrojos, en este orden:
//
//  1. **UNA petición EN VUELO por ventana.** Una ráfaga de 50 mensajes de UNA
//     conversación es UNA ventana, así que mientras la primera inferencia corre, las
//     49 siguientes no encolan nada. El coste de una ráfaga es una inferencia cada
//     ~8 s, no 50 a la vez.
//  2. **Un pool ACOTADO de workers** (DefaultWorkers), que es el techo de
//     inferencias simultáneas de todo el proceso, vengan de la conversación que
//     vengan.
//  3. **Una cola ACOTADA que DESCARTA cuando se llena** (DefaultQueue). Descartar es
//     la conducta correcta y no una pérdida: lo que se pierde es un ADELANTO, y la
//     ventana cierra igual por su reloj (R-12). Bloquear al productor sería meter la
//     espera en el camino del mensaje por la puerta de atrás.
//
// # SE PREGUNTA POR CADA MENSAJE, NO UNA VEZ POR VENTANA (y es a propósito)
//
// El cerrojo (1) es «una EN VUELO», no «una por ventana». Importa: el mensaje que
// abre una ráfaga suele ser un «hola» que no clasifica nada, y el que dice «quiero
// presupuesto de 200 sillas» llega el quinto. Preguntar una sola vez por ventana
// dejaría el adelanto atado al peor candidato. Con el cerrojo en vuelo, mientras la
// inferencia del «hola» corre nadie encola, y el primer mensaje que llegue con la
// vía libre vuelve a preguntar.
//
// El texto que se clasifica es el de UN mensaje —el que disparó la petición—, no el
// hilo acumulado, y eso conserva EXACTAMENTE la semántica del push que sustituye.
// Quien compone el hilo entero es el compositor del flush, aguas abajo y con el
// sobre cifrado.
//
// # EL TEXTO DEL CLIENTE VIVE AQUÍ, EN MEMORIA, Y NO SE ESCRIBE EN NINGÚN SITIO
//
// Este paquete recibe el literal del cliente porque sin texto no hay clasificación
// que pedir. Lo que hace con él está acotado a tres cosas: guardarlo en la cola
// mientras espera worker, meterlo en el prompt y compararlo contra la evidencia. NO
// lo persiste, NO lo devuelve y —esto es lo que hay que vigilar en cada línea que se
// añada aquí— NO LO LOGUEA (INV-6). Los logs de este paquete llevan tenant, sesión,
// nombre de intención y números; nunca una frase, ni la evidencia, ni un param.
//
// # LOS FALLOS DE `Run` SON MUDOS, Y NO SE ARREGLAN AQUÍ (R-13, D-11)
//
// Nadie supervisa la goroutine que corre `Run`: si no se llama, o si muere, el
// adelanto deja de existir sin un error. Es deuda con dueño (D-11); este paquete no
// añade supervisión de paso.
package intakeahead

import (
	"context"
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intentcfg"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

const (
	// DefaultWorkers es el techo de inferencias P1 simultáneas del proceso.
	//
	// Cuatro, y no uno ni cuarenta. Uno serializaría a TODOS los tenants detrás de la
	// conversación más lenta —una inferencia de 30 s dejaría a los demás esperando
	// media ventana—; cuarenta no compraría nada, porque en la vía local el cuello es
	// UN Ollama por Edge y pedirle cuatro cosas a la vez no las hace más rápidas. El
	// número es un PUNTO DE PARTIDA razonado, no medido con esta carga.
	//
	// ⚠️ Este pool NO es el control de concurrencia del Edge y no pretende serlo. El
	// Edge tiene su propio despachador y su propio breaker (Plan 051); si un día hay
	// que limitar por Edge, se limita ahí, donde se conoce el fierro.
	DefaultWorkers = 4
	// DefaultQueue es cuántas peticiones caben esperando worker. Lleno ⇒ se DESCARTA
	// (ver la cabecera): el adelanto es best-effort y la ventana cierra por su reloj.
	DefaultQueue = 64
	// DefaultTimeout es el presupuesto de UNA petición P1 COMPLETA: leer el catálogo,
	// elegir el proveedor, la inferencia y su reintento por calidad si lo hay.
	//
	// 🔴 ES EL ÚNICO RELOJ DE LA CADENA. El adaptador de la vía no inventa un plazo
	// propio: DERIVA el suyo de este deadline, reservando un margen para que el
	// veredicto lo emita quien sabe qué pasó. Este número es, por tanto, lo que de
	// verdad decide cuánto puede tardar una inferencia del adelanto.
	//
	// CUARENTA Y CINCO, Y NO CUARENTA: 40 s menos el margen del veredicto son 33 s
	// para el modelo, y el MÁXIMO REAL medido en campo sobre el VPS con qwen3:1.7b es
	// de 36,5 s (p50 8,1 s). Con 45 s el modelo recibe 38 s, que sí cubre lo medido.
	//
	// 🔧 T1.8-1 (2026-08-25) DEJA ESTE ARGUMENTO CONSERVADOR, Y SE ANOTA SIN TOCAR EL
	// NÚMERO. Desde la ventana HÍBRIDA una ráfaga puede seguir viva hasta
	// `aggregation_max_seconds` (120 s por defecto), así que hoy hay respuestas que se
	// cortan a los 45 s y que TODAVÍA habrían podido adelantar el cierre. No es una
	// avería: la ventana cierra igual por su reloj y el adelanto perdido solo cuesta
	// latencia. Subir este número cuesta un worker ocupado más tiempo por petición, y
	// ninguna tarea lo ha pedido todavía.
	//
	// ⚠️ Y LA VENTANA ES POR TENANT, mientras que esto es una constante de PROCESO
	// (`tenant_settings.aggregation_window_seconds`; 45 s es solo el default de
	// plataforma). Un tenant con una ventana más corta tendrá un worker esperando más
	// de lo que su ventana dura. Es despilfarro acotado, no una avería: lo que se
	// pierde es un adelanto que ya no podía adelantar.
	DefaultTimeout = 45 * time.Second
)

// ProviderSelector traduce un tenant en el llm.LLMProvider de SU vía. Lo satisface
// `*llmvia.Selector` de forma estructural: este paquete no lo importa.
//
// 🔴 ESTE PAQUETE NO SABE QUÉ VÍA LE TOCÓ AL TENANT, y esa ignorancia es el
// requisito C2 del ADR-0044 («si hay un `if via` fuera del adaptador, es defecto»).
// Pide un provider y lo llama igual venga de donde venga.
//
// `originSessionID` es la sesión de la ventana (`WindowKey.SessionID`): la vía local
// elige con ella el stream del Edge, y el pool la pasa SIEMPRE.
type ProviderSelector interface {
	For(ctx context.Context, tenantID, originSessionID string) (llm.LLMProvider, error)
}

// ConfigStore es el catálogo de intenciones del tenant. Lo satisfacen
// `*intentcfg.PostgresStore` (producción) y `*intentcfg.MemoryStore` (tests).
//
// Es la MISMA fila que el `PUT /api/v1/intents` publica y que el `ConfigUpdate`
// empuja al Edge: no hay un segundo catálogo para la nube. Que la config publicada
// en campo traiga `params: []` (D-044.20) es la forma CORRECTA y se usa tal cual.
type ConfigStore interface {
	Get(ctx context.Context, tenantID string) (intentcfg.Config, error)
}

// Sink recibe la clasificación cuando llega. Lo satisface el agregador de ventanas.
//
// Se le entregan la CLAVE de la ventana que preguntó, el NOMBRE de la intención y la
// CONFIANZA en crudo, sin decidir nada: quien aplica la política de disparo —qué
// nombre adelanta y con qué umbral— es el agregador (D-044.20). Partirla en dos
// sitios es como se desincronizan. Los params y la evidencia NO salen del paquete.
//
// 🔴 UNA RESPUESTA QUE LLEGA TARDE ES INOCUA, Y ESO ES DEL DISEÑO DEL AGREGADOR, NO
// DE UNA GUARDA DE AQUÍ: lo único que el sink hace es anotar una PISTA en memoria, y
// el barrido solo mira las pistas de las ventanas que siguen `aggregating`. No hace
// falta comprobar aquí si la ventana vive —y no se comprueba: sería un SELECT para no
// hacer nada—.
type Sink interface {
	OnClassified(key intake.WindowKey, intent string, confidence float64)
}

// SinkFunc adapta una función a Sink.
//
// Existe para UNA cosa concreta: el pool y el agregador se necesitan MUTUAMENTE —el
// agregador pide por el pool, el pool responde al agregador— y en el cableado hay
// que construir uno antes que el otro. La salida es una clausura sobre la variable
// del agregador, que se resuelve al llamar y no al construir. La alternativa sería
// un setter público sobre el agregador: dejar el cable mutable en caliente para
// arreglar un problema que solo existe durante el arranque.
type SinkFunc func(key intake.WindowKey, intent string, confidence float64)

// OnClassified implementa Sink: llama a la función con los tres argumentos tal cual,
// una vez por entrega.
func (f SinkFunc) OnClassified(key intake.WindowKey, intent string, confidence float64) {
	panic(pendiente.Implementar("intakeahead.SinkFunc.OnClassified"))
}

// Pool pide clasificaciones P1 fuera del camino del mensaje. Es seguro para uso
// concurrente: `Request` corre desde la goroutine de cada entrante y `Warm` desde el
// gateway. Se construye con New; un `*Pool` nil es un no-op seguro en sus tres métodos.
type Pool struct{}

// Option configura el Pool al construirlo. Las opciones se aplican en orden: si dos
// fijan lo mismo, gana la última.
type Option func(*Pool)

// WithWorkers fija el techo de inferencias simultáneas (cuántos workers arranca Run).
// Un valor <= 0 se ignora y queda el que hubiera (DefaultWorkers).
func WithWorkers(n int) Option {
	panic(pendiente.Implementar("intakeahead.WithWorkers"))
}

// WithQueueSize fija cuántas peticiones caben esperando worker; la siguiente se
// descarta (ver Request). Un valor <= 0 se ignora y queda el que hubiera (DefaultQueue).
func WithQueueSize(n int) Option {
	panic(pendiente.Implementar("intakeahead.WithQueueSize"))
}

// WithTimeout fija el presupuesto de una petición completa (ver DefaultTimeout). Un
// valor <= 0 se ignora y queda el que hubiera. El umbral del reintento por calidad
// —la mitad del presupuesto— se escala con él.
func WithTimeout(d time.Duration) Option {
	panic(pendiente.Implementar("intakeahead.WithTimeout"))
}

// New construye el pool, sin arrancar nada: los workers nacen en Run.
//
// Por defecto: DefaultWorkers workers, cola de DefaultQueue, presupuesto
// DefaultTimeout, calentamiento ENCENDIDO con DefaultWarmTimeout y sin Warmer.
//
// Con `log`, `cfg`, `sel` o `sink` a nil el pool es un no-op seguro —`Request`
// descarta y `Run` vuelve en el acto—, mismo criterio nil-safe que el resto de piezas
// opcionales del pipeline: un arranque parcial no puede tumbar el turno de nadie.
// Nunca devuelve nil.
func New(log logger.Logger, cfg ConfigStore, sel ProviderSelector, sink Sink, opts ...Option) *Pool {
	panic(pendiente.Implementar("intakeahead.New"))
}

// Request encola la petición P1 de un entrante: `key` es su ventana y `text` (antes
// `texto`) el literal de ESE mensaje. NO BLOQUEA y NO DEVUELVE ERROR: corre en línea
// con el mensaje del cliente y lo único que puede hacer aquí es volver rápido
// (REQ-35, INV-10). No acepta `ctx` a propósito: ver la cabecera.
//
// No hace NADA —ni encola, ni lee el catálogo, ni loguea— cuando:
//
//   - el pool es nil o está a medio cablear (ver New);
//   - `text` es "" (un mensaje de solo media no tiene nada que clasificar: es un
//     motivo SANO, REQ-38);
//   - `key` no es válida (`intake.WindowKey.Valid`: le falta alguna de las cuatro);
//   - la ventana `key` YA tiene una petición viva, encolada o corriendo: es el cerrojo
//     «una en vuelo por ventana». No es un fallo y no se loguea —en una ráfaga de 50
//     mensajes ocurre 49 veces—. El cerrojo es por clave COMPLETA: otra ventana no
//     espera a esta.
//
// En otro caso toma el cerrojo de la ventana y encola. **Cola llena (R-12)**: se
// descarta el ADELANTO, no el mensaje; se SUELTA el cerrojo —o la ventana quedaría
// marcada como «preguntando» para siempre— y se deja el `Debug`
// `adelanto: cola de clasificación llena; la ventana cerrará por su reloj` con
// `tenant_id` y `session_id`. No es un error.
//
// Lo que un worker de Run hace con una petición encolada, en este orden y con UN
// presupuesto para todo (WithTimeout) colgado del ctx de Run:
//
//  1. Lee el catálogo del tenant (`ConfigStore.Get`). Sin catálogo
//     (`intentcfg.ErrNotFound`) NO SE PREGUNTA NADA y no se loguea: es el estado normal
//     de un tenant, y con catálogo vacío el parser rechazaría cualquier artefacto.
//     Otro error ⇒ `Warn` `adelanto: no se pudo leer el catálogo de intenciones del
//     tenant`. Un blob que no valida (`intents.ParseAndValidate`) ⇒ `Warn`
//     `adelanto: el catálogo publicado del tenant no valida; no se pide inferencia`.
//     En los tres casos no se pide proveedor.
//  2. Arma la entrada del prompt: `Text` = el literal tal cual; `Catalog` = las
//     intenciones publicadas EN SU ORDEN, cada una con nombre, descripción, params
//     (`params: []` viaja vacío, D-044.20: no se rellena nada) y ejemplos (mensaje y
//     params); `UnknownLabel` = `intents.ReservedUnknown`, que NO va además en el
//     catálogo; `Vocabulary` = el del tenant.
//  3. Pide el proveedor con `(key.TenantID, key.SessionID)`. Si falla ⇒ `Debug`
//     `adelanto: sin proveedor LLM para el tenant; la ventana cerrará por su reloj` y
//     no hay inferencia (el aviso al dueño es del selector, no de aquí).
//  4. Llama a `ClassifyRequest` con `llm.TemperatureGreedy` y lee la salida con
//     `llm.ParseClassification`. **Reintento por CALIDAD, y solo uno** (REQ-02/03): si
//     la llamada o el parseo fallan con `llm.ErrLLMQuality`, repite UNA vez con
//     `llm.TemperatureRetry`, y solo si al presupuesto le queda AL MENOS LA MITAD (un
//     reintento con las sobras es un fallo garantizado que ocupa un worker y avisa al
//     dueño de una avería que no existe). Un fallo que no es de calidad NO se
//     reintenta. Si no sale clasificación ⇒ `Debug`
//     `adelanto: la clasificación no salió; la ventana cerrará por su reloj`, con el
//     error ORIGINAL (el de calidad, aunque lo que faltara fuera plazo).
//  5. **Saneo contra el texto del cliente** (regla de `evidence`: minúsculas y blancos
//     normalizados, acentos NO): los params que la intención elegida no declara, o cuyo
//     valor no vacío no aparece en el texto, se descartan uno a uno —`Debug`
//     `adelanto: params descartados por el allowlist` con el NÚMERO en `descartados`— y
//     la clasificación sigue viva. La EVIDENCIA que no aparece en el texto (o vacía)
//     TUMBA la clasificación entera: `Debug` `adelanto: la evidencia no aparece en el
//     mensaje; la clasificación se descarta`.
//  6. Entrega al sink `(key, intent, confidence)` tal cual, UNA vez.
//
// Pase lo que pase —entrega, descarte o fallo— el cerrojo de la ventana se suelta al
// terminar y el siguiente mensaje de esa ventana vuelve a preguntar. Ningún fallo
// sale hacia arriba ni detiene al worker: lo que se pierde es un adelanto.
func (p *Pool) Request(key intake.WindowKey, text string) {
	panic(pendiente.Implementar("intakeahead.Pool.Request"))
}

// Run arranca los workers (DefaultWorkers, o los de WithWorkers) y bloquea hasta que
// `ctx` se cancele; entonces vuelve cuando todos han terminado. Cada worker atiende
// las peticiones encoladas de una en una (ver Request), así que nunca hay más
// inferencias simultáneas que workers. Sin broker (ADR-0003): goroutines y un canal.
//
// El ctx de cada inferencia cuelga de `ctx`: cancelarlo corta también las que estén
// en vuelo. Lo que quedara en la cola sin atender se pierde (son adelantos).
//
// En un pool nil o a medio cablear (ver New) vuelve en el acto sin arrancar nada.
//
// ⚠️ SIN ESTA LLAMADA EL ADELANTO NO EXISTE Y EL FALLO ES MUDO (R-13): `Request`
// encolaría, la cola se llenaría, y a partir de ahí todo se descartaría en silencio.
// Se llama UNA vez, desde el arranque; un segundo `Run` sobre el mismo pool sumaría
// workers sin dar error.
func (p *Pool) Run(ctx context.Context) {
	panic(pendiente.Implementar("intakeahead.Pool.Run"))
}
