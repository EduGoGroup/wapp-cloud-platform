// Package intakeahead es EL ADELANTO DE VENTANA POR PULL (Plan 044 · Ola 1.6 ·
// T1.6-4; D-044.31, REQ-09, REQ-35, INV-10, ADR-0045).
//
// # QUÉ RESUELVE, Y QUÉ MURIÓ PARA QUE HICIERA FALTA
//
// Hasta la Ola 1.6 el Edge clasificaba el entrante y ADJUNTABA la intención al
// mensaje (`IncomingMessage.intent`, `SensitivePayload.intent`). El agregador leía
// esa etiqueta y, si era `intake_request` con confianza suficiente, adelantaba el
// cierre de la ventana. Ese push MURIÓ con D-044.31: T1.6-1 retiró
// `ClassifiedIntent` del contrato (los dos campos quedaron `reserved`), y con él se
// fue la única fuente de la señal.
//
// El Cloud ya no RECIBE la intención: la PIDE. Este paquete es quien la pide.
//
// # 🔴 LA REGLA QUE GOBIERNA TODO ESTE FICHERO: EL TURNO NO ESPERA (REQ-35)
//
// `ClassifyRequest` es SÍNCRONO y tarda SEGUNDOS: el p50 medido en campo sobre el
// VPS con qwen3:1.7b es de 8,1 s, y hay corridas de más de 30 s. El sitio desde el
// que se dispara —`IntakeAggregator.Observe`— corre EN LÍNEA con el mensaje del
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
// Es el mismo criterio que el `context.WithoutCancel` del aviso de degradación, y
// aquí se aplica por construcción en vez de por disciplina: la firma no deja
// pasarle el contexto equivocado.
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
//     ventana cierra igual por su reloj. Bloquear al productor sería meter la espera
//     en el camino del mensaje por la puerta de atrás.
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
// hilo acumulado, y eso conserva EXACTAMENTE la semántica del push que sustituye: el
// clasificador del Edge también clasificaba un mensaje suelto. Quien compone el hilo
// entero es el compositor del flush (T1.4), aguas abajo y con el sobre cifrado.
//
// # EL TEXTO DEL CLIENTE VIVE AQUÍ, EN MEMORIA, Y NO SE ESCRIBE EN NINGÚN SITIO
//
// Este paquete recibe el literal del cliente porque sin texto no hay clasificación
// que pedir. Lo que hace con él está acotado a tres cosas: guardarlo en la cola
// mientras espera worker, meterlo en el prompt y compararlo contra la evidencia. NO
// lo persiste, NO lo devuelve y —esto es lo que hay que vigilar en cada línea que se
// añada aquí— NO LO LOGUEA (INV-6). Los logs de este paquete llevan tenant, sesión,
// nombre de intención y números; nunca una frase.
package intakeahead

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/EduGoGroup/wapp-shared/intents"
	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intentcfg"
)

const (
	// DefaultWorkers es el techo de inferencias P1 simultáneas del proceso.
	//
	// Cuatro, y no uno ni cuarenta. Uno serializaría a TODOS los tenants detrás de la
	// conversación más lenta —una inferencia de 30 s dejaría a los demás esperando
	// media ventana—; cuarenta no compraría nada, porque en la vía local el cuello es
	// UN Ollama por Edge y pedirle cuatro cosas a la vez no las hace más rápidas. El
	// número es un PUNTO DE PARTIDA razonado, no medido con esta carga: quien lo mueva
	// tiene que mirar el parque de tenants y la cola del modelo a la vez.
	//
	// ⚠️ Este pool NO es el control de concurrencia del Edge y no pretende serlo. El
	// Edge tiene su propio despachador y su propio breaker (Plan 051); si un día hay
	// que limitar por Edge, se limita ahí, donde se conoce el fierro.
	DefaultWorkers = 4
	// DefaultQueue es cuántas peticiones caben esperando worker. Lleno ⇒ se DESCARTA
	// (ver la cabecera): el adelanto es best-effort y la ventana cierra por su reloj.
	DefaultQueue = 64
	// DefaultTimeout es el presupuesto de UNA petición P1 COMPLETA: la inferencia más
	// el reintento por calidad si lo hay.
	//
	// 🔴 ES EL ÚNICO RELOJ DE LA CADENA, y desde el arreglo del reloj único eso hay que
	// leerlo literal. El adaptador de la vía ya no inventa un plazo propio: DERIVA el
	// suyo de este deadline (ver el bloque «UN SOLO RELOJ» de llmvia/local), reservando
	// un margen para que el veredicto lo emita quien sabe qué pasó. Este número es, por
	// tanto, lo que de verdad decide cuánto puede tardar una inferencia del pipeline.
	//
	// CUARENTA Y CINCO, Y NO CUARENTA, y el cambio no es cosmético — el 40 estaba
	// MEDIBLEMENTE MAL. La versión anterior lo justificaba como «mayor que los 30 s del
	// adaptador y menor que la ventana de 45 s»; la primera mitad describía justamente
	// el segundo reloj que el arreglo retiró, y la segunda dejaba el presupuesto por
	// debajo del fierro: 40 s menos el margen del veredicto son 33 s para el modelo,
	// y el MÁXIMO REAL medido en campo sobre el VPS con qwen3:1.7b es de 36,5 s (p50
	// 8,1 s). Heredar el reloj sin mover este número habría dejado el fallo del 2026-08-23
	// exactamente donde estaba, solo que cortando siete segundos más tarde. Con 45 s el
	// modelo recibe 38 s, que sí cubre lo medido.
	//
	// 🔧 T1.8-1 (2026-08-25) DEJA ESTE ARGUMENTO CONSERVADOR, Y SE ANOTA SIN TOCAR EL
	// NÚMERO. Desde la ventana HÍBRIDA una ráfaga puede seguir viva hasta
	// `aggregation_max_seconds` (120 s por defecto) —el silencio se reinicia con cada
	// mensaje—, así que hoy hay respuestas que se cortan a los 45 s y que TODAVÍA
	// habrían podido adelantar el cierre. No es una avería: la ventana cierra igual por
	// su reloj y el adelanto perdido solo cuesta latencia. Subir este número es una
	// decisión con su propio coste —un worker ocupado más tiempo por petición— que
	// ninguna tarea ha pedido todavía; queda dicho aquí para que quien la tome sepa que
	// el techo de arriba ya no es el que este párrafo describía.
	//
	// EL TECHO SIGUE SIENDO LA VENTANA, y por eso no sube más: pasado el cierre, una
	// respuesta ya no adelanta nada. Que llegue tarde no rompe nada —es inocuo por
	// construcción, ver Sink— pero gastar un worker en algo que ya no puede adelantar
	// no compra nada. Igualarlo a DefaultAggregationWindow (45 s) es el máximo que ese
	// argumento admite.
	//
	// ⚠️ Y LA VENTANA ES POR TENANT, mientras que esto es una constante de PROCESO
	// (`tenant_settings.aggregation_window_seconds`, migración 0072; 45 s es solo el
	// default de plataforma). Un tenant que configure una ventana más corta tendrá un
	// worker esperando más de lo que su ventana dura. Es despilfarro acotado, no una
	// avería: lo que se pierde es un adelanto que ya no podía adelantar. Atarlo a la
	// ventana real exigiría que este pool leyera tenant_settings por petición, y esa
	// lectura no la pide ninguna tarea hoy.
	DefaultTimeout = 45 * time.Second
)

// ProviderSelector traduce un tenant en el llm.LLMProvider de SU vía. Lo satisface
// *llmvia.Selector.
//
// 🔴 ESTE PAQUETE NO SABE QUÉ VÍA LE TOCÓ AL TENANT, y esa ignorancia es el
// requisito C2 del ADR-0044 («si hay un `if via` fuera del adaptador, es defecto»).
// Pide un provider y llama a los mismos cinco métodos venga de donde venga.
type ProviderSelector interface {
	For(ctx context.Context, tenantID, originSessionID string) (llm.LLMProvider, error)
}

// ConfigStore es el catálogo de intenciones del tenant. Lo satisface
// *intentcfg.PostgresStore (producción) y *intentcfg.MemoryStore (tests).
//
// Es la MISMA fila que el `PUT /api/v1/intents` publica y que el `ConfigUpdate`
// empuja al Edge: no hay un segundo catálogo para la nube. Que la config publicada
// en campo traiga `params: []` (D-044.20) es la forma CORRECTA y se usa tal cual.
type ConfigStore interface {
	Get(ctx context.Context, tenantID string) (intentcfg.Config, error)
}

// Sink recibe la clasificación cuando llega. Lo satisface *runtime.IntakeAggregator.
//
// Se le entregan el NOMBRE y la CONFIANZA en crudo, sin decidir nada: quien aplica
// la política de disparo —qué nombre adelanta y con qué umbral— es el agregador, que
// ya la tenía escrita para el push (D-044.20). Partirla en dos sitios es como se
// desincronizan.
//
// 🔴 UNA RESPUESTA QUE LLEGA TARDE ES INOCUA, Y ESO ES DEL DISEÑO DEL AGREGADOR, NO
// DE UNA GUARDA DE AQUÍ: lo único que el sink hace es anotar una PISTA en memoria, y
// el barrido solo mira las pistas de las ventanas que siguen `aggregating`. Si la
// ventana ya cerró, su pista no casa con nada y se descarta en el siguiente barrido
// sin abrir ni cerrar nada. No hace falta comprobar aquí si la ventana vive —y no se
// comprueba: sería un SELECT para no hacer nada—.
type Sink interface {
	OnClassified(key intake.WindowKey, intent string, confidence float64)
}

// SinkFunc adapta una función a Sink.
//
// Existe para UNA cosa concreta y conviene decirla: el pool y el agregador se
// necesitan MUTUAMENTE —el agregador pide por el pool, el pool responde al
// agregador— y en el cableado hay que construir uno antes que el otro. La salida es
// una clausura sobre la variable del agregador, que se resuelve al llamar y no al
// construir. La alternativa sería un setter público sobre el agregador, es decir,
// dejar el cable mutable en caliente para arreglar un problema que solo existe
// durante el arranque.
type SinkFunc func(key intake.WindowKey, intent string, confidence float64)

// OnClassified implementa Sink.
func (f SinkFunc) OnClassified(key intake.WindowKey, intent string, confidence float64) {
	f(key, intent, confidence)
}

// peticion es UNA clasificación pendiente. Vive en la cola en memoria y muere con el
// worker que la atiende.
type peticion struct {
	key intake.WindowKey
	// texto es el literal del cliente. Ver la cabecera: no se persiste y no se loguea.
	texto string
}

// Pool pide clasificaciones P1 fuera del camino del mensaje. Es seguro para uso
// concurrente: `Request` corre desde la goroutine de cada entrante.
type Pool struct {
	log  logger.Logger
	cfg  ConfigStore
	sel  ProviderSelector
	sink Sink

	workers int
	timeout time.Duration
	cola    chan peticion

	mu sync.Mutex
	// enVuelo son las ventanas con una petición viva (encolada o corriendo). Es el
	// cerrojo (1) de la cabecera y el que hace que una ráfaga no sea una tormenta.
	enVuelo map[intake.WindowKey]struct{}

	// --- Calentamiento de la caché de prefijo (T1.7-4, ver calentamiento.go) ---
	calentador Calentador
	// calentamientoOn es el interruptor de campo. Nace en true (New lo materializa):
	// ver WithCalentamiento.
	calentamientoOn bool
	warmTimeout     time.Duration
	// calMu protege calEnVuelo. Es un candado APARTE del `mu` de arriba a propósito:
	// aquel lo toman Request y atender en el camino de CADA entrante, y colgar de él
	// un mapa que solo tocan los calentamientos —uno por Edge, cada muchos minutos—
	// sería meter dos ritmos muy distintos bajo la misma cerradura sin necesidad.
	calMu sync.Mutex
	// calEnVuelo son los Edges con un calentamiento vivo. Cerrojo «uno por Edge».
	calEnVuelo map[edgeKey]struct{}
}

// Option configura el Pool al construirlo.
type Option func(*Pool)

// WithWorkers fija el techo de inferencias simultáneas. <=0 se ignora.
func WithWorkers(n int) Option {
	return func(p *Pool) {
		if n > 0 {
			p.workers = n
		}
	}
}

// WithQueueSize fija cuántas peticiones caben esperando worker. <=0 se ignora.
func WithQueueSize(n int) Option {
	return func(p *Pool) {
		if n > 0 {
			p.cola = make(chan peticion, n)
		}
	}
}

// WithTimeout fija el presupuesto de una petición completa. <=0 se ignora.
func WithTimeout(d time.Duration) Option {
	return func(p *Pool) {
		if d > 0 {
			p.timeout = d
		}
	}
}

// New construye el pool. Con `cfg`, `sel` o `sink` a nil el pool es un no-op seguro
// —`Request` descarta y `Run` vuelve—, mismo criterio nil-safe que el resto de piezas
// opcionales del pipeline: un arranque parcial no puede tumbar el turno de nadie.
func New(log logger.Logger, cfg ConfigStore, sel ProviderSelector, sink Sink, opts ...Option) *Pool {
	p := &Pool{
		log:     log,
		cfg:     cfg,
		sel:     sel,
		sink:    sink,
		workers: DefaultWorkers,
		timeout: DefaultTimeout,
		cola:    make(chan peticion, DefaultQueue),
		enVuelo: make(map[intake.WindowKey]struct{}),

		calentamientoOn: true,
		warmTimeout:     DefaultWarmTimeout,
		calEnVuelo:      make(map[edgeKey]struct{}),
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Request encola la petición P1 de un entrante. NO BLOQUEA y NO DEVUELVE ERROR:
// corre en línea con el mensaje del cliente y lo único que puede hacer aquí es
// volver rápido (REQ-35, INV-10).
//
// No acepta `ctx` a propósito: ver la cabecera. El contexto del turno se cancela
// antes de que la inferencia empiece.
func (p *Pool) Request(key intake.WindowKey, texto string) {
	if !p.usable() || texto == "" || !key.Valid() {
		return
	}
	if !p.marcar(key) {
		// Ya hay una petición viva para esta ventana: el cerrojo (1). No es un fallo y
		// no se loguea — en una ráfaga de 50 mensajes esto ocurre 49 veces y el log
		// sería el ruido que la cola evita.
		return
	}
	select {
	case p.cola <- peticion{key: key, texto: texto}:
	default:
		// Cola llena: se descarta el ADELANTO, no el mensaje. Hay que soltar el
		// cerrojo o la ventana quedaría marcada como «preguntando» para siempre.
		p.soltar(key)
		p.log.Debug("adelanto: cola de clasificación llena; la ventana cerrará por su reloj",
			"tenant_id", key.TenantID, "session_id", key.SessionID)
	}
}

// Run arranca los workers y bloquea hasta que ctx se cancele. Sin broker (ADR-0003):
// goroutines y un canal.
//
// ⚠️ SIN ESTA LLAMADA EL ADELANTO NO EXISTE Y EL FALLO ES MUDO: `Request` encolaría,
// la cola se llenaría, y a partir de ahí todo se descartaría en silencio. Es la misma
// mitad invisible que `IntakeAggregator.Run`, y por el mismo motivo se dice aquí.
func (p *Pool) Run(ctx context.Context) {
	if !p.usable() {
		return
	}
	var wg sync.WaitGroup
	for range p.workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.worker(ctx)
		}()
	}
	wg.Wait()
}

// worker atiende peticiones hasta que ctx se cancela.
func (p *Pool) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case pet := <-p.cola:
			p.atender(ctx, pet)
		}
	}
}

// atender resuelve UNA petición: pide la clasificación con su propio presupuesto y,
// si sale algo, se lo entrega al sink. Suelta el cerrojo pase lo que pase.
func (p *Pool) atender(ctx context.Context, pet peticion) {
	defer p.soltar(pet.key)

	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	c, ok := p.clasificar(ctx, pet)
	if !ok {
		return
	}
	p.sink.OnClassified(pet.key, c.Intent, c.Confidence)
}

// clasificar arma el prompt desde el catálogo del tenant, pide P1 por la vía
// configurada y devuelve la clasificación ya saneada.
//
// Devuelve (nil, false) en TODOS los caminos de fallo, y ninguno de ellos es un
// error hacia arriba: no hay nadie arriba a quien devolvérselo, y lo que se pierde
// es un adelanto (REQ-35).
func (p *Pool) clasificar(ctx context.Context, pet peticion) (*llm.Classification, bool) {
	in, ok := p.entrada(ctx, "adelanto", pet.key.TenantID, pet.texto)
	if !ok {
		return nil, false
	}
	prov, err := p.sel.For(ctx, pet.key.TenantID, pet.key.SessionID)
	if err != nil {
		// El aviso al dueño ya lo escribió el selector si el error tenía motivo
		// (T1.6-6): aquí NO se duplica el mapeo error→motivo ni se inventa uno.
		p.log.Debug("adelanto: sin proveedor LLM para el tenant; la ventana cerrará por su reloj",
			"tenant_id", pet.key.TenantID, "error", err)
		return nil, false
	}
	c, err := p.pedir(ctx, prov, in)
	if err != nil {
		p.log.Debug("adelanto: la clasificación no salió; la ventana cerrará por su reloj",
			"tenant_id", pet.key.TenantID, "session_id", pet.key.SessionID, "error", err)
		return nil, false
	}
	if !p.aceptar(c, in, pet.key) {
		return nil, false
	}
	return c, true
}

// entrada arma la ClassifyRequestInput desde el catálogo publicado del tenant.
//
// 🔴 TIENE DOS LLAMANTES Y ESO ES EL PUNTO, no una casualidad: la clasificación real
// (clasificar) y el calentamiento de la caché de prefijo (Warm, calentamiento.go). El
// calentamiento solo sirve si el prefijo que deja cacheado es EL MISMO BYTE A BYTE que
// el de la P1 real, y todo lo que forma ese prefijo —catálogo aplanado, vocabulario,
// etiqueta de desconocido— se decide aquí. Dos funciones «que hacen lo mismo» habrían
// divergido en un campo el día que alguien tocara una y no la otra, y el síntoma
// habría sido un calentamiento que calienta un prompt que nadie pide: cero error, cero
// log, y la latencia igual que antes. `quien` solo cambia el rótulo del log.
//
// Sin catálogo NO SE PREGUNTA NADA, y no es una guarda defensiva: con `Catalog`
// vacío el parser rechaza cualquier artefacto (su docstring lo dice: «un catálogo
// vacío rechaza TODO a propósito»), así que preguntar sería gastar una inferencia de
// ocho segundos para tirarla. Un tenant sin config de intents es un estado NORMAL —la
// mayoría lo está—, así que sale por Debug y sin aviso: es un motivo SANO y REQ-38
// manda que los sanos no notifiquen.
func (p *Pool) entrada(ctx context.Context, quien, tenantID, texto string) (llm.ClassifyRequestInput, bool) {
	cfg, err := p.cfg.Get(ctx, tenantID)
	if err != nil {
		if !errors.Is(err, intentcfg.ErrNotFound) {
			p.log.Warn(quien+": no se pudo leer el catálogo de intenciones del tenant",
				"tenant_id", tenantID, "error", err)
		}
		return llm.ClassifyRequestInput{}, false
	}
	cat, err := intents.ParseAndValidate(cfg.Blob)
	if err != nil {
		// El blob se validó al publicarlo (PUT /api/v1/intents), así que llegar aquí
		// significa que la fila se escribió por fuera del API o que el contrato cambió
		// bajo los pies. Es un fallo de CONFIGURACIÓN, no de la vía: se dice y no se
		// notifica como degradación.
		p.log.Warn(quien+": el catálogo publicado del tenant no valida; no se pide inferencia",
			"tenant_id", tenantID, "version", cfg.Version, "error", err)
		return llm.ClassifyRequestInput{}, false
	}
	return llm.ClassifyRequestInput{
		Text:         texto,
		Catalog:      aplanar(cat),
		UnknownLabel: intents.ReservedUnknown,
		Vocabulary:   cat.Vocabulario,
	}, true
}

// aplanar traduce el catálogo de negocio del tenant a la forma POBRE que el prompt
// necesita. Es la traducción que el módulo llm NO hace a propósito: su doc.go declara
// que no importa otro módulo de wapp-shared, así que el puente lo pone el caller.
//
// ⚠️ `params: []` VIAJA TAL CUAL Y ES LA FORMA CORRECTA (D-044.20). El intent
// publicado en campo (versión db60b90651d5) declara la lista vacía a propósito: la
// salida útil de P1 es «esto es una solicitud de pedido», no la lista de productos.
// Quien descompone en ítems es P2–P4, en la nube y sobre el texto acumulado. Rellenar
// aquí unos params «que faltan» sería deshacer esa decisión.
func aplanar(cat *intents.Config) []llm.IntentSpec {
	out := make([]llm.IntentSpec, 0, len(cat.Intents))
	for _, it := range cat.Intents {
		spec := llm.IntentSpec{
			Name:        it.Name,
			Description: it.Descripcion,
			Params:      it.Params,
			Examples:    make([]llm.IntentExample, 0, len(it.Ejemplos)),
		}
		for _, ej := range it.Ejemplos {
			spec.Examples = append(spec.Examples, llm.IntentExample{Message: ej.Mensaje, Params: ej.Params})
		}
		out = append(out, spec)
	}
	return out
}

// pedir hace la llamada y, si la salida no fue interpretable, REINTENTA UNA VEZ a
// TemperatureRetry (REQ-02/REQ-03).
//
// El reintento es del CALLER por contrato —los dos adaptadores lo dicen en su
// docstring y ninguno reintenta por su cuenta—, y es UNO: el segundo fallo de calidad
// seguido no es mala suerte, es que el modelo no sabe responder esto, y una tercera
// pasada solo gasta ventana.
//
// 🔴 EL FALLO DE CALIDAD NO AVISA AL DUEÑO, y no hace falta hacer nada para eso: el
// decorador de llmvia trata ErrLLMQuality como «el proveedor funciona y el modelo
// escribió mal un JSON» y no escribe fila. Aquí solo hay que no confundirlo con un
// fallo de vía, que es justo lo que hace el errors.Is.
//
// # 🔴 EL REINTENTO SOLO ARRANCA SI LE QUEDA PLAZO PARA SER UN REINTENTO
//
// Las dos pasadas comparten UN presupuesto (el de atender), y desde que el adaptador
// hereda el plazo eso tiene una consecuencia que antes quedaba tapada por su reloj
// propio: si la primera pasada consumió casi todo, la segunda arranca con lo que
// sobre. Un reintento que empieza con dos segundos no es un reintento, es un fallo
// garantizado que además OCUPA UN WORKER y, peor, produce un `timeout` CON motivo —o
// sea, un aviso de degradación al dueño por una avería que no existe—.
//
// SE DESCARTÓ REPARTIR MITAD Y MITAD, que era la otra opción sobre la mesa: partir 45 s
// en dos mitades de 22,5 s dejaría el presupuesto de la PRIMERA pasada por debajo del
// máximo real medido en campo (36,5 s), o sea, penalizaría el caso frecuente —una
// inferencia normal que no falla de calidad— para financiar el raro. Habría cambiado un
// defecto por su espejo.
//
// El criterio es MEDIA VUELTA DEL PRESUPUESTO, comprobada sobre el reloj real y no
// sobre un número aparte: se reintenta si al ctx le queda al menos la mitad de lo que
// se le dio. Da lo mismo que el reparto en el caso bueno —una primera pasada corta deja
// sitio de sobra— sin gravar el caso en que la primera pasada necesita el presupuesto
// entero, y se auto-escala si alguien mueve el plazo con WithTimeout. Sin deadline no
// se comprueba nada: no hay presupuesto que repartir.
func (p *Pool) pedir(ctx context.Context, prov llm.LLMProvider, in llm.ClassifyRequestInput) (*llm.Classification, error) {
	c, err := intentar(ctx, prov, in, llm.TemperatureGreedy)
	if err == nil || !errors.Is(err, llm.ErrLLMQuality) {
		return c, err
	}
	if !p.cabeElReintento(ctx) {
		// Se devuelve el error de calidad ORIGINAL, no uno de plazo: lo que pasó de
		// verdad es que el modelo escribió mal el JSON, y esa sigue siendo la causa que
		// el llamante tiene que registrar. Inventar aquí un error nuevo cambiaría la
		// familia del fallo —y con ella la decisión de avisar al dueño— por un detalle
		// de nuestra política de reintento.
		return nil, err
	}
	return intentar(ctx, prov, in, llm.TemperatureRetry)
}

// cabeElReintento dice si al presupuesto le queda al menos la MITAD, que es el umbral
// razonado en el docstring de pedir. Sin deadline devuelve true: no hay plazo que
// agotar.
func (p *Pool) cabeElReintento(ctx context.Context) bool {
	dl, ok := ctx.Deadline()
	if !ok {
		return true
	}
	return time.Until(dl) >= p.timeout/2
}

// intentar es UNA pasada: pedir y parsear. Los dos pasos fallan con el MISMO
// centinela cuando el problema es la salida del modelo, que es lo que permite que el
// reintento de arriba se decida con una sola comprobación.
func intentar(ctx context.Context, prov llm.LLMProvider, in llm.ClassifyRequestInput, temp float64) (*llm.Classification, error) {
	raw, err := prov.ClassifyRequest(ctx, in, llm.Options{Temperature: temp})
	if err != nil {
		return nil, err
	}
	return llm.ParseClassification(raw, in)
}

// aceptar aplica el SANEO (ver saneo.go) y decide si la clasificación se entrega.
//
// Rechazar aquí NO es un fallo de la vía y no escribe aviso: el proveedor respondió,
// el cable funcionó, y lo que pasó es que la respuesta no se sostiene sobre el texto
// del cliente. Es exactamente la familia de ErrLLMQuality y se trata igual.
func (p *Pool) aceptar(c *llm.Classification, in llm.ClassifyRequestInput, key intake.WindowKey) bool {
	evidenciaOK, descartados := sanear(c, in)
	if descartados > 0 {
		// El NÚMERO sí va al log; los valores NO (INV-6: son texto del cliente).
		p.log.Debug("adelanto: params descartados por el allowlist",
			"tenant_id", key.TenantID, "intent", c.Intent, "descartados", descartados)
	}
	if !evidenciaOK {
		p.log.Debug("adelanto: la evidencia no aparece en el mensaje; la clasificación se descarta",
			"tenant_id", key.TenantID, "session_id", key.SessionID, "intent", c.Intent)
		return false
	}
	return true
}

// usable dice si el pool tiene con qué trabajar. Un pool a medio cablear no encola
// ni arranca workers en vez de reventar en la primera petición.
func (p *Pool) usable() bool {
	return p != nil && p.log != nil && p.cfg != nil && p.sel != nil && p.sink != nil
}

// marcar toma el cerrojo de la ventana. Devuelve false si ya había una petición viva.
func (p *Pool) marcar(k intake.WindowKey) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, vivo := p.enVuelo[k]; vivo {
		return false
	}
	p.enVuelo[k] = struct{}{}
	return true
}

// soltar libera el cerrojo de la ventana.
func (p *Pool) soltar(k intake.WindowKey) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.enVuelo, k)
}
