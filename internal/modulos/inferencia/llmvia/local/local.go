// Porta internal/llmvia/local/local.go @ ebf4eb7

// Package local implementa el puerto llm.LLMProvider contra el Ollama del EDGE del
// tenant, hablando el frame InferenceRequest/InferenceResult de CloudLink (Plan 044
// · Ola 1.6 · T1.6-3; D-044.29, REQ-34, REQ-37, ADR-0045).
//
// # Qué hace, en una frase
//
// Arma el prompt AQUÍ (en la nube) con los Build...Prompt compartidos, lo manda por
// el cable, y devuelve el JSON aislado con el ExtractJSON compartido. Nada más.
//
// # 🔴 C2 DEL ADR-0044: ESTE PAQUETE NO TIENE UN SOLO PROMPT PROPIO
//
// «El esquema de orquestación es el mismo en las dos vías: prohibidos dos
// pipelines». Y el ADR-0045 dice de quién es ese esquema: el Cloud es el ÚNICO que
// orquesta la inferencia; el Edge es «prompt entra → JSON sale». Aquí eso se traduce
// en una regla que se puede comprobar leyendo el fichero: no hay ni una cadena de
// prompt, ni un parser, ni una validación. Los cinco métodos son la MISMA línea con
// distinto Build...Prompt, y el post-proceso es el mismo llm.ExtractJSON que usa el
// provider de la vía API. Si un día la vía local necesitara «un prompt un poco
// distinto porque el modelo es más chico», eso NO se escribe aquí: se arregla en
// wapp-shared/llm, donde lo heredan las dos vías, o deja de ser C2.
//
// La comparación es directa: este fichero y llm/api/anthropic.go tienen la misma
// forma —run(prompt) y ExtractJSON— y lo único que cambia es el transporte. Esa
// simetría ES el requisito.
//
// # Lo que este paquete NO hace
//
//   - NO aplica el umbral de confianza ni sanea los params: eso es del llamante, que
//     es quien tiene el texto original y la config del tenant (contrato del puerto).
//   - NO reintenta. El reintento único por calidad (TemperatureRetry) lo decide el
//     llamante, igual que en la vía API (REQ-02/REQ-03).
//   - NO decide la vía ni la mira. Quien elige entre local y api es llmvia, y ese es
//     el ÚNICO sitio del repo donde se pregunta por la vía (C2). Ni el prompt ni la
//     petición que salen de aquí la nombran.
//   - NO escribe la notificación de degradación. La escribe el decorador de llmvia,
//     que envuelve a las DOS vías con el mismo mecanismo.
//
// # 🔴 El presupuesto de salida lo fija el Cloud, y lo fija POR TAREA (T1.7-3)
//
// Hasta T1.7-3 el `num_predict` lo ponía el Edge y valía 256 para TODO. Las salidas
// medidas en campo de P2 y P3 son de 265–293 tokens, así que por la vía local esas
// dos etapas salían TRUNCADAS: el JSON no cerraba, el llamante reintentaba a 0,3 y el
// reintento volvía a truncar en el mismo sitio. Por eso cada etapa viaja con SU techo
// (campo 7 del frame, InferRequest.MaxOutputTokens) y con su rótulo de telemetría
// (campo 8, InferRequest.Class). La tabla es contrato de este paquete (R4.6.b):
//
//	etapa                     techo   class                      salida mayor medida/estimada
//	P1 · ClassifyRequest        192   edgegrpc.ClassInteractive    52
//	P2 · ExtractMainIdeas       512   edgegrpc.ClassBatch         267
//	P3 · ExtractItemSpecs       512   edgegrpc.ClassBatch         293
//	P4 · NormalizeQuantities   1024   edgegrpc.ClassBatch         830 (diez ítems)
//	P5 · GenerateQuoteText      768   edgegrpc.ClassBatch         570 (quince ítems)
//
// Es un TECHO, no una reserva (el modelo para en su token de fin), y el criterio es
// «≈ 2× la salida legítima más grande de esa etapa»: ni menos (truncar cuesta la
// inferencia entera más su reintento) ni mucho más (el techo es lo único que acota
// al modelo degenerado, que ocupa la PLAZA ÚNICA del Ollama del cliente). Es de
// SALIDA, no de entrada: el tamaño del prompt no entra en esta cuenta. Y NO se
// deriva de llm.Options: el puerto compartido solo lleva Temperature, y ampliarlo
// sería pedirle al llamante que sepa cuántos tokens ocupa el esquema de una P4.
//
// `class` va por etapa —y no por llamante— porque hoy cada etapa tiene UN llamante:
// P1 la pide el adelanto de ventana, que existe para que el turno de WhatsApp no
// espere; P2–P5 son el pipeline del presupuesto, que corre de fondo. Es SOLO rótulo:
// nadie decide con él (ver InferRequest.Class).
//
// # 🔴 Un solo reloj: el plazo se HEREDA, no se inventa
//
// LO QUE PASÓ EN CAMPO (2026-08-23, VPS de UAT, WhatsApp real). Este adaptador tenía
// un plazo de 30 s PROPIO, independiente del presupuesto de su llamante (40 s). El de
// 30 s cortaba primero, y 30 s está POR DEBAJO del máximo real medido sobre ese mismo
// fierro (36,5 s; p50 8,1 s): la inferencia murió por timeout y el borrador nunca se
// generó. Lo único roto era que había DOS RELOJES INDEPENDIENTES y el de abajo
// ignoraba al de arriba. La regla, que es el invariante que este paquete custodia:
//
//	EL ADAPTADOR NUNCA ES MÁS RESTRICTIVO QUE SU LLAMANTE.
//
// El `Timeout` del frame tiene tres desenlaces, y ninguno inventa un plazo (R4.6.a):
//
//  1. El ctx trae deadline D ⇒ lo que queda hasta D menos MargenVeredicto. Es el
//     caso de TODO el pipeline. Se mide con el reloj real (time.Until).
//  2. Lo que queda no supera MargenVeredicto ⇒ ErrSinPresupuesto, sin tocar el cable.
//  3. El ctx NO trae deadline ⇒ la red de seguridad: DefaultTimeout, o lo que fije
//     WithTimeout.
//
// 🔴 PROHIBIDO acotar el caso 1 con la red de seguridad: un `min(restante, timeout)`
// parecería prudente y sería exactamente el defecto de campo otra vez.
package local

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"

	edgegrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/grpc"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// DefaultFormat es el formato que se le pide al modelo cuando no se configura otro.
// Vale exactamente "json": "json" a secas, no un JSON Schema, porque el Edge lo
// reenvía verbatim al proveedor sin parsearlo, y los artefactos versionados los valida
// el llamante en Go.
const DefaultFormat = "json"

// MargenVeredicto es lo que este adaptador RESERVA del plazo del llamante para que el
// veredicto lo emita el Edge y no un corte del cliente. Vale 7 s.
//
// LA ARITMÉTICA, que es lo único que justifica el número:
//
//	ctx del llamante                    D
//	timeout_ms que se le da al Edge     D − MargenVeredicto   (el Edge corta aquí)
//	timer del gateway (awaitInference)  D − MargenVeredicto + edgegrpc.DefaultInferGrace
//
// 🔴 Promete MargenVeredicto > edgegrpc.DefaultInferGrace (R4.6.d), y un test lo
// custodia en vez de confiarlo a este párrafo. Con esa desigualdad el timer del
// gateway vence ANTES que el ctx, así que el desenlace es determinista: o llega el
// INFERENCE_ERROR_TIMEOUT nombrado del Edge, o el gateway emite `timeout` CON motivo.
// Lo que NUNCA pasa es que gane un `ctx.Done()`, que es edgegrpc.ErrInferenceAbandoned:
// SIN motivo, SIN aviso al dueño, y mintiendo sobre la causa. Si los dos márgenes
// fueran iguales, el veredicto lo decidiría el `select` de Go, que elige al azar entre
// casos listos: el aviso al dueño saldría o no según la moneda.
//
// SIETE SEGUNDOS = DefaultInferGrace (5 s) + 2 s de colchón, que son exactamente el
// MargenSocket del Edge y están aquí por lo mismo: cubrir un viaje de vuelta sin
// depender de que nadie lo esté midiendo.
const MargenVeredicto = 7 * time.Second

// DefaultTimeout es la RED DE SEGURIDAD: el `Timeout` del frame cuando el llamante no
// trae deadline. Vale 30 s. Es el caso RARO, no el normal.
//
// El camino normal es el pipeline, que SIEMPRE llama con deadline; en ese camino esta
// constante no se lee nunca. Un ctx sin deadline llegando aquí es un test o un
// llamante nuevo que se olvidó de acotar su propia espera, y para ese quedarse sin
// techo sería peor que un techo arbitrario. Quien necesite otro lo fija con
// WithTimeout — y si lo que quiere es que las inferencias del pipeline duren más, el
// número que tiene que mover NO es este, sino el presupuesto del llamante.
const DefaultTimeout = 30 * time.Second

// ErrSinTransporte indica que el Provider se construyó sin cable. Es un fallo de
// PROGRAMACIÓN del arranque, no de una llamada, y por eso New lo devuelve al construir
// en vez de dejar que reviente en la primera inferencia. El texto es observable y no
// cambia.
var ErrSinTransporte = errors.New("llmvia/local: el adaptador local necesita un transporte (Frame)")

// ErrSinTenant indica que el Provider se construyó sin tenant. Sin él no hay a qué
// Edge preguntar (INV-7/INV-8: el tenant no es opcional en ningún camino). El texto
// es observable y no cambia.
var ErrSinTenant = errors.New("llmvia/local: el adaptador local necesita un tenant")

// ErrSinPresupuesto indica que al llamante ya no le queda plazo útil: lo que resta de
// su deadline no supera MargenVeredicto, así que ninguna respuesta podría llegar a
// tiempo de servirle. El texto es observable y no cambia.
//
// Quien lo devuelve lo ENVUELVE (errors.Is lo encuentra) con el detalle, en este
// formato literal: "<ErrSinPresupuesto>: quedan <resto redondeado al milisegundo>, y
// el margen del veredicto es <MargenVeredicto>".
//
// 🔴 NO SE LLAMA AL EDGE, y esa es la decisión. Mandar el frame igual gastaría un
// command_id, un viaje por el stream y —lo caro— una plaza del Ollama del cliente
// para producir algo que nadie va a estar esperando cuando llegue.
//
// Es un error PELADO, sin Motivo(), y eso también es deliberado: el escritor de avisos
// de llmvia solo notifica lo que trae motivo, y aquí no hay ninguna degradación de la
// vía que contarle al dueño. Su equipo está perfectamente; lo que se acabó fue el
// presupuesto de quien preguntó. Es la misma familia que
// edgegrpc.ErrInferenceAbandoned.
var ErrSinPresupuesto = errors.New("llmvia/local: al llamante no le queda plazo para una inferencia")

// Frame es el transporte: empuja el InferenceRequest por el stream CloudLink del
// tenant y devuelve el JSON crudo del modelo, o un error.
//
// La firma es EXACTAMENTE la de (*edgegrpc.Server).Infer, así que el gateway NUEVO la
// satisface sin adaptador (lo afirma `var _ local.Frame = (*edgegrpc.Server)(nil)` en
// el test). Eso importa por dos cosas: la vía local habla con el gateway sin ninguna
// pieza en medio, y cada inferencia que sale por aquí cuenta en Server.InFlight() —
// el arranque espera a que llegue a cero antes de cerrar el pool (D-F3-13) —.
//
// ⚠️ POR QUÉ AQUÍ SÍ SE IMPORTA EL GATEWAY. Un handler HTTP no debe acabar acoplado
// al transporte; este paquete es lo contrario: su razón de existir ES hablar ese
// frame, y esconder el tipo de la petición detrás de ocho parámetros sueltos no lo
// desacoplaría de nada, solo haría la firma ilegible. Lo que sí se conserva es la
// INTERFAZ: los tests inyectan un doble y no necesitan un Server vivo.
//
// El método PlazaDe del gateway NO forma parte de este puerto: quien lo necesita
// (el aforo de llmvia) lo busca por aserción de tipo sobre el mismo valor.
type Frame interface {
	// Infer pide UNA inferencia al Edge del tenant y devuelve la salida cruda del
	// modelo. Sus errores se documentan en (*edgegrpc.Server).Infer: con motivo
	// (*edgegrpc.InferError, método Motivo()) o pelados.
	Infer(ctx context.Context, tenantID string, req edgegrpc.InferRequest) (string, error)
}

// Provider implementa llm.LLMProvider contra el Edge del tenant. Es inmutable tras
// construirse y seguro para uso concurrente: no guarda estado de llamada, y dos
// goroutines pueden compartir el mismo valor.
//
// Solo se construye con New: el valor cero (Provider{}) no tiene cable ni tenant y
// no es utilizable.
//
// Lo que TODA petición de sus cinco métodos lleva al Frame, con el tenant de New:
//
//   - Prompt: el del Build...Prompt compartido de la etapa, byte a byte (o el
//     Build...PromptCon de la plantilla ajustada, ver ConPlantillas);
//   - Format: DefaultFormat, o el de WithFormat;
//   - Temperature: la de llm.Options del LLAMANTE, sin tocar (el reintento por
//     calidad sube a 0,3 y, si se ignorara, pediría lo mismo que la llamada fallida);
//   - Timeout: el plazo heredado (ver «Un solo reloj» en el comentario del paquete);
//   - OriginSessionID y TargetSessionID: los de WithOriginSession y
//     WithTargetSession, vacíos si no se fijaron;
//   - MaxOutputTokens y Class: los de la etapa (tabla del comentario del paquete), o
//     MaxOutputTokens 0 con WithMaxOutputTokens(false);
//   - Warmup: false, siempre. Solo Warm lo enciende.
//
// Y lo que hacen con la respuesta, las cinco igual:
//
//   - si el plazo no alcanza, devuelven ErrSinPresupuesto (envuelto) SIN llamar al
//     Frame;
//   - hacen exactamente UNA llamada al Frame: no reintentan;
//   - el error del Frame vuelve INTACTO, sin envolver: ni errors.Is ni el duck-typing
//     del motivo (`interface{ Motivo() string }`) se pierden por el camino. Quien los
//     distingue —y decide si se avisa al dueño— es el decorador de llmvia;
//   - la salida pasa por llm.ExtractJSON, el MISMO de la vía API: JSON envuelto en
//     texto o en fences se aísla; sin JSON, llm.ErrLLMQuality (calidad, no
//     infraestructura).
type Provider struct{}

// Option configura el Provider al construirlo. Las opciones se aplican en el orden
// en que se pasan a New, y la última gana.
type Option func(*Provider)

// WithFormat fija el formato que se le pide al modelo. Vacío se ignora y queda el
// que hubiera (DefaultFormat si nadie fijó otro).
func WithFormat(f string) Option {
	panic(pendiente.Implementar("local.WithFormat"))
}

// WithTimeout fija la RED DE SEGURIDAD (ver DefaultTimeout): el `Timeout` del frame
// cuando el ctx del llamante NO trae deadline. Un valor <= 0 se ignora.
//
// ⚠️ NO es un techo sobre el plazo heredado, y cambiarlo para que lo fuera sería
// reintroducir el defecto que este paquete arregló: un techo local puede quedarse por
// debajo de lo que el llamante estaba dispuesto a esperar. Con deadline en el ctx,
// esta opción no se lee.
func WithTimeout(d time.Duration) Option {
	panic(pendiente.Implementar("local.WithTimeout"))
}

// WithOriginSession fija la sesión de WhatsApp cuya conversación originó la pregunta.
// Opcional. Viaja al frame como trazabilidad (InferRequest.OriginSessionID) y, si
// está viva, es además el stream por el que sale. Vacía es un estado legítimo y viaja
// vacía: la inferencia es de alcance Edge, no de sesión.
func WithOriginSession(sessionID string) Option {
	panic(pendiente.Implementar("local.WithOriginSession"))
}

// WithMaxOutputTokens enciende o apaga el presupuesto de SALIDA que el Cloud fija por
// tarea (campo 7 del frame). Por defecto está ENCENDIDO — New lo materializa —, así
// que un Provider construido sin esta opción manda el techo de su etapa.
//
// 🔴 APAGA EL ENVÍO, NO BAJA EL NÚMERO: con `false` el frame lleva MaxOutputTokens 0
// (campo ausente), en las cinco etapas Y en el calentamiento. Lo que hay que poder
// reproducir es la conducta ANTERIOR a T1.7-3, y esa era «el Cloud no dice nada y el
// Edge aplica su 256», no «el Cloud pide 256».
//
// Existe para el control A/B de campo en la MISMA tanda, no para ajustar nada: quien
// quiera otro techo lo cambia en la tabla de etapas, que es donde está su aritmética.
func WithMaxOutputTokens(on bool) Option {
	panic(pendiente.Implementar("local.WithMaxOutputTokens"))
}

// WithTargetSession fija POR DÓNDE debe salir la petición (InferRequest.TargetSessionID),
// sin afirmar que ninguna conversación la originó. Opcional, y hoy solo lo usa el
// calentamiento: ver ese campo de edgegrpc.InferRequest para por qué origen y destino
// son campos distintos y no dos usos del mismo.
func WithTargetSession(sessionID string) Option {
	panic(pendiente.Implementar("local.WithTargetSession"))
}

// ConPlantillas inyecta los prompts ajustados que cargó el arranque, por etapa. Sin
// ella —o con un mapa nil o vacío, o para una etapa que el mapa no trae— el proveedor
// usa los compilados en el módulo llm, que es el caso normal.
//
// Con la etapa presente (P2–P5), el prompt de esa etapa es el de su
// llm.Build...PromptCon(plantilla, in): la COMPOSICIÓN la hace siempre el módulo llm,
// aquí solo se elige el texto. P1 no tiene plantilla ajustable: sale SIEMPRE del
// compilado (su texto lo gobierna el catálogo de intenciones).
//
// Es la ÚNICA puerta por la que entra un prompt de fuera, y entra ya validado: el
// cargador (el paquete prompts) pasa cada plantilla por llm.ValidarPlantilla antes de
// que el proceso llegue aquí. Este paquete NO revalida ni tiene con qué, así que un
// cambio que se salte al cargador se salta también la red.
func ConPlantillas(p map[llm.Etapa]llm.Plantilla) Option {
	panic(pendiente.Implementar("local.ConPlantillas"))
}

// New construye el adaptador local para un tenant.
//
// Falla al CONSTRUIR, con el mismo criterio que llm/api.New (una configuración
// imposible se sabe al armarla, no a mitad de un pipeline):
//
//   - frame nil ⇒ (nil, ErrSinTransporte);
//   - tenantID vacío ⇒ (nil, ErrSinTenant). Con las dos cosas ausentes gana
//     ErrSinTransporte.
//
// Materializa los defaults ANTES de aplicar las opciones: formato DefaultFormat, red
// de seguridad DefaultTimeout, techo de salida ENCENDIDO, sin sesión de origen ni de
// destino y sin plantillas. Ninguna inferencia sale sin formato ni sin reloj.
// No llama al Frame al construir.
func New(frame Frame, tenantID string, opts ...Option) (*Provider, error) {
	panic(pendiente.Implementar("local.New"))
}

// ClassifyRequest es la etapa P1: elige UNA intención del catálogo del tenant.
// Prompt: llm.BuildClassifyRequestPrompt(in), siempre el compilado. Techo 192, clase
// edgegrpc.ClassInteractive. Lo demás, lo que promete Provider.
func (p *Provider) ClassifyRequest(ctx context.Context, in llm.ClassifyRequestInput, opts llm.Options) (json.RawMessage, error) {
	panic(pendiente.Implementar("local.Provider.ClassifyRequest"))
}

// ExtractMainIdeas es la etapa P2: las ideas principales del hilo. Prompt:
// llm.BuildExtractMainIdeasPrompt(in), o llm.BuildExtractMainIdeasPromptCon con la
// plantilla de llm.EtapaP2 si ConPlantillas la trae. Techo 512, clase
// edgegrpc.ClassBatch. Lo demás, lo que promete Provider.
func (p *Provider) ExtractMainIdeas(ctx context.Context, in llm.ExtractMainIdeasInput, opts llm.Options) (json.RawMessage, error) {
	panic(pendiente.Implementar("local.Provider.ExtractMainIdeas"))
}

// ExtractItemSpecs es la etapa P3: especifica UN ítem por llamada. Prompt:
// llm.BuildExtractItemSpecsPrompt(in), o llm.BuildExtractItemSpecsPromptCon con la
// plantilla de llm.EtapaP3 si ConPlantillas la trae. Techo 512, clase
// edgegrpc.ClassBatch. Lo demás, lo que promete Provider.
func (p *Provider) ExtractItemSpecs(ctx context.Context, in llm.ExtractItemSpecsInput, opts llm.Options) (json.RawMessage, error) {
	panic(pendiente.Implementar("local.Provider.ExtractItemSpecs"))
}

// NormalizeQuantities es la etapa P4: cantidades, paquetes, rangos y fecha. Prompt:
// llm.BuildNormalizeQuantitiesPrompt(in), o llm.BuildNormalizeQuantitiesPromptCon con
// la plantilla de llm.EtapaP4 si ConPlantillas la trae. Techo 1024 —es la única etapa
// cuya salida crece con el pedido Y repite el esquema entero de cada ítem—, clase
// edgegrpc.ClassBatch. Lo demás, lo que promete Provider.
func (p *Provider) NormalizeQuantities(ctx context.Context, in llm.NormalizeQuantitiesInput, opts llm.Options) (json.RawMessage, error) {
	panic(pendiente.Implementar("local.Provider.NormalizeQuantities"))
}

// GenerateQuoteText es la etapa P5: redacta la cotización con la voz del negocio.
// Prompt: llm.BuildGenerateQuoteTextPrompt(in), o llm.BuildGenerateQuoteTextPromptCon
// con la plantilla de llm.EtapaP5 si ConPlantillas la trae. Techo 768 —su salida es
// literalmente lo que el cliente lee por WhatsApp: el peor sitio donde ahorrar
// tokens—, clase edgegrpc.ClassBatch. Lo demás, lo que promete Provider.
func (p *Provider) GenerateQuoteText(ctx context.Context, in llm.GenerateQuoteTextInput, opts llm.Options) (json.RawMessage, error) {
	panic(pendiente.Implementar("local.Provider.GenerateQuoteText"))
}
