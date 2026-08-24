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
// pipelines». Aquí eso se traduce en una regla que se puede comprobar leyendo el
// fichero: no hay ni una cadena de prompt, ni un parser, ni una validación. Los
// cinco métodos son la MISMA línea con distinto Build...Prompt, y el post-proceso es
// el mismo llm.ExtractJSON que usa el provider de la vía API. Si un día la vía local
// necesitara «un prompt un poco distinto porque el modelo es más chico», eso NO se
// escribe aquí: se arregla en wapp-shared/llm, donde lo heredan las dos vías, o deja
// de ser C2.
//
// La comparación es directa: este fichero y llm/api/anthropic.go tienen la misma
// forma —run(prompt) y ExtractJSON— y lo único que cambia es el transporte. Esa
// simetría ES el requisito.
//
// # Lo que este paquete NO hace
//
//   - NO aplica el umbral de confianza ni sanea los params: eso es del caller, que
//     es quien tiene el texto original y la config del tenant (contrato del puerto).
//   - NO reintenta. El reintento único por calidad (TemperatureRetry) lo decide el
//     caller, igual que en la vía API (REQ-02/REQ-03).
//   - NO decide la vía ni la mira. Quien elige entre local y api es internal/llmvia,
//     y ese es el ÚNICO sitio del repo donde se pregunta por la vía (C2).
//   - NO escribe la notificación de degradación. La escribe el decorador de llmvia,
//     que envuelve a las DOS vías con el mismo mecanismo.
package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"

	gatewaygrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/grpc"
)

// DefaultFormat es el formato que se le pide al modelo cuando no se configura otro.
// "json" a secas, no un JSON Schema: el Edge lo reenvía verbatim al proveedor sin
// parsearlo, y los artefactos versionados los valida el caller en Go.
const DefaultFormat = "json"

// ════════════════════════════════════════════════════════════════════════════
// 🔴 UN SOLO RELOJ: EL PLAZO SE HEREDA, NO SE INVENTA
// ════════════════════════════════════════════════════════════════════════════
//
// LO QUE PASÓ EN CAMPO (2026-08-23, VPS de UAT, WhatsApp real). Este adaptador tenía
// un `DefaultTimeout = 30 s` PROPIO, independiente del presupuesto de su llamante
// (40 s, intakeahead). El de 30 s cortaba primero, sin saber cuánto le quedaba de
// verdad al de arriba, y 30 s está POR DEBAJO del máximo real medido sobre ese mismo
// fierro (36,5 s; p50 8,1 s). Con un prompt de 7.786 bytes la inferencia murió por
// timeout y el borrador nunca se generó. El pipeline entero —petición, servicio por
// el socket, error nombrado, aviso de degradación— funcionó perfectamente: lo único
// roto era que había DOS RELOJES INDEPENDIENTES y el de abajo ignoraba al de arriba.
//
// LA REGLA, y es el invariante que este paquete custodia con un test:
//
//	EL ADAPTADOR NUNCA ES MÁS RESTRICTIVO QUE SU LLAMANTE.
//
// Con un ctx que trae deadline, el `Timeout` del frame es LO QUE QUEDA menos
// MargenVeredicto. Sin deadline, y solo entonces, cae a DefaultTimeout.
//
// Es la misma doctrina que el MargenSocket del Edge («el que vence primero es
// SIEMPRE el plazo de dentro, y el veredicto lo emite quien lo sabe»), aplicada un
// salto más arriba en la misma cadena.

// MargenVeredicto es lo que este adaptador RESERVA del plazo del llamante para que el
// veredicto lo emita el Edge y no un corte del cliente.
//
// LA ARITMÉTICA, que es lo único que justifica el número:
//
//	ctx del llamante                    D
//	timeout_ms que se le da al Edge     D − MargenVeredicto   (el Edge corta aquí)
//	timer de awaitInference             D − MargenVeredicto + DefaultInferGrace
//
// Con MargenVeredicto > DefaultInferGrace, el timer del Gateway vence ANTES que el
// ctx, así que el desenlace es determinista: o llega el INFERENCE_ERROR_TIMEOUT
// nombrado del Edge (el caso normal, porque el Edge cortó MargenVeredicto antes del
// final), o el Gateway emite `timeout` CON motivo. Lo que NUNCA pasa es que gane un
// `ctx.Done()`, que es ErrInferenceAbandonada: SIN motivo, SIN aviso al dueño, y
// mintiendo sobre la causa —diría «el llamante se rindió» cuando el proveedor estaba
// trabajando—.
//
// Si los dos márgenes fueran iguales, el timer y el ctx vencerían a la vez y el
// veredicto lo decidiría el `select` de Go, que elige al azar entre casos listos: el
// aviso al dueño saldría o no según la moneda. Por eso hay colchón, y por eso hay un
// test que custodia la desigualdad en vez de confiarla a estos párrafos.
//
// SIETE SEGUNDOS = DefaultInferGrace (5 s, el ida y vuelta del frame más lo que el
// Edge tarda en construir su respuesta) + 2 s de colchón. Los 2 s son exactamente el
// MargenSocket del Edge y están aquí por lo mismo: cubrir un viaje de vuelta sin
// depender de que nadie lo esté midiendo.
const MargenVeredicto = 7 * time.Second

// DefaultTimeout es la RED DE SEGURIDAD: el presupuesto que se usa cuando el llamante
// no trae deadline. Es el caso RARO, no el normal.
//
// El camino normal de este paquete es el pool de adelanto (internal/intakeahead), que
// SIEMPRE llama con deadline; en ese camino esta constante no se lee nunca. Un ctx sin
// deadline llegando aquí significa una de dos cosas: un test, o un llamante nuevo que
// se olvidó de acotar su propia espera. Para el segundo, quedarse sin techo sería
// peor que un techo arbitrario —una goroutine esperando indefinidamente a un modelo
// colgado—, así que el valor se conserva.
//
// TREINTA SEGUNDOS, y el número ya no gobierna nada de lo que importa: dejó de ser el
// que corta las inferencias del pipeline el día que este adaptador empezó a heredar el
// plazo. Se mantiene porque como techo de un llamante descuidado sigue siendo
// razonable, no porque nadie lo haya vuelto a medir. Quien necesite otro lo fija con
// WithTimeout — y si lo que quiere es que las inferencias del pipeline duren más, el
// número que tiene que mover NO es este, sino el presupuesto del llamante.
const DefaultTimeout = 30 * time.Second

// ErrSinTransporte indica que el Provider se construyó sin cable. Es un fallo de
// PROGRAMACIÓN del arranque, no de una llamada, y por eso New lo devuelve al
// construir en vez de dejar que reviente en la primera inferencia.
var ErrSinTransporte = errors.New("llmvia/local: el adaptador local necesita un transporte (Frame)")

// ErrSinTenant indica que el Provider se construyó sin tenant. Sin él no hay a qué
// Edge preguntar (INV-7/INV-8: el tenant no es opcional en ningún camino).
var ErrSinTenant = errors.New("llmvia/local: el adaptador local necesita un tenant")

// ErrSinPresupuesto indica que al llamante ya no le queda plazo útil: lo que resta de
// su deadline no cubre ni MargenVeredicto, así que ninguna respuesta podría llegar a
// tiempo de servirle.
//
// 🔴 NO SE LLAMA AL EDGE, y esa es la decisión. Mandar el frame igual gastaría un
// command_id, un viaje por el stream y —lo caro— una plaza del Ollama del cliente
// para producir algo que nadie va a estar esperando cuando llegue.
//
// Es un error PELADO, sin Motivo(), y eso también es deliberado: el escritor de avisos
// de llmvia solo notifica lo que trae motivo, y aquí no hay ninguna degradación de la
// vía que contarle al dueño. Su equipo está perfectamente; lo que se acabó fue el
// presupuesto de quien preguntó. Es la misma familia que ErrInferenceAbandonada.
var ErrSinPresupuesto = errors.New("llmvia/local: al llamante no le queda plazo para una inferencia")

// Frame es el transporte: empuja el InferenceRequest por el stream CloudLink del
// tenant y devuelve el JSON crudo del modelo, o un error.
//
// La firma es EXACTAMENTE la de (*gatewaygrpc.Server).Infer, así que el Gateway la
// satisface sin adaptador — el mismo criterio que intakes.MessageSender aplica sobre
// SendText.
//
// ⚠️ POR QUÉ AQUÍ SÍ SE IMPORTA EL GATEWAY, cuando send.go argumenta lo contrario.
// El argumento de allí protege a los HANDLERS HTTP, que no tienen nada que ver con
// el transporte y no deben acabar acoplados a él. Este paquete es lo contrario: su
// razón de existir ES hablar ese frame, y esconder el tipo de la petición detrás de
// siete parámetros sueltos no lo desacoplaría de nada, solo haría la firma ilegible.
// Lo que sí se conserva del criterio es la INTERFAZ: los tests inyectan un fake y no
// necesitan un Server vivo.
type Frame interface {
	Infer(ctx context.Context, tenantID string, req gatewaygrpc.InferRequest) (string, error)
}

// Provider implementa llm.LLMProvider contra el Edge del tenant. Es inmutable tras
// construirse y seguro para uso concurrente (no guarda estado de llamada).
type Provider struct {
	frame    Frame
	tenantID string
	format   string
	timeout  time.Duration
	// origin es la sesión de WhatsApp cuya conversación originó la pregunta, cuando
	// se conoce. Viaja al frame como trazabilidad y, si está viva, es además el
	// stream por el que sale. Vacía es un estado legítimo: la inferencia es de
	// alcance Edge, no de sesión.
	origin string
}

// Option configura el Provider al construirlo.
type Option func(*Provider)

// WithFormat fija el formato que se le pide al modelo. Vacío se ignora y cae a
// DefaultFormat.
func WithFormat(f string) Option {
	return func(p *Provider) {
		if f != "" {
			p.format = f
		}
	}
}

// WithTimeout fija la RED DE SEGURIDAD (ver DefaultTimeout): el presupuesto que se
// usa cuando el ctx del llamante NO trae deadline. <= 0 se ignora.
//
// ⚠️ NO es un techo sobre el plazo heredado, y cambiarlo para que lo fuera sería
// reintroducir el defecto que este paquete arregló: un techo local puede quedarse por
// debajo de lo que el llamante estaba dispuesto a esperar, y entonces el adaptador
// vuelve a ser más restrictivo que quien lo llama. Con deadline en el ctx, esta opción
// no se lee.
func WithTimeout(d time.Duration) Option {
	return func(p *Provider) {
		if d > 0 {
			p.timeout = d
		}
	}
}

// WithOriginSession fija la sesión de WhatsApp que originó la pregunta. Opcional.
func WithOriginSession(sessionID string) Option {
	return func(p *Provider) { p.origin = sessionID }
}

// New construye el adaptador local para un tenant.
//
// Falla al CONSTRUIR si le falta el cable o el tenant, con el mismo criterio que
// llm/api.New: una configuración imposible se sabe al armarla, no a mitad de un
// pipeline.
func New(frame Frame, tenantID string, opts ...Option) (*Provider, error) {
	if frame == nil {
		return nil, ErrSinTransporte
	}
	if tenantID == "" {
		return nil, ErrSinTenant
	}
	p := &Provider{frame: frame, tenantID: tenantID, format: DefaultFormat, timeout: DefaultTimeout}
	for _, opt := range opts {
		opt(p)
	}
	return p, nil
}

// ClassifyRequest es la etapa P1: elige UNA intención del catálogo del tenant.
func (p *Provider) ClassifyRequest(ctx context.Context, in llm.ClassifyRequestInput, opts llm.Options) (json.RawMessage, error) {
	return p.run(ctx, llm.BuildClassifyRequestPrompt(in), opts)
}

// ExtractMainIdeas es la etapa P2: las ideas principales del hilo.
func (p *Provider) ExtractMainIdeas(ctx context.Context, in llm.ExtractMainIdeasInput, opts llm.Options) (json.RawMessage, error) {
	return p.run(ctx, llm.BuildExtractMainIdeasPrompt(in), opts)
}

// ExtractItemSpecs es la etapa P3: especifica UN ítem por llamada.
func (p *Provider) ExtractItemSpecs(ctx context.Context, in llm.ExtractItemSpecsInput, opts llm.Options) (json.RawMessage, error) {
	return p.run(ctx, llm.BuildExtractItemSpecsPrompt(in), opts)
}

// NormalizeQuantities es la etapa P4: cantidades, paquetes, rangos y fecha.
func (p *Provider) NormalizeQuantities(ctx context.Context, in llm.NormalizeQuantitiesInput, opts llm.Options) (json.RawMessage, error) {
	return p.run(ctx, llm.BuildNormalizeQuantitiesPrompt(in), opts)
}

// GenerateQuoteText redacta la cotización con la voz del negocio.
func (p *Provider) GenerateQuoteText(ctx context.Context, in llm.GenerateQuoteTextInput, opts llm.Options) (json.RawMessage, error) {
	return p.run(ctx, llm.BuildGenerateQuoteTextPrompt(in), opts)
}

// run es el camino común de las cinco tareas: mandar el prompt por el cable y aislar
// el JSON de lo que venga. Es la MISMA forma que anthropicProvider.run, y esa
// simetría es el requisito C2, no una casualidad.
//
// Los dos pasos fallan con vocabularios distintos, igual que en la vía API: el
// transporte devuelve un error con motivo de degradación (*gatewaygrpc.InferError) y
// el aislado devuelve llm.ErrLLMQuality. Quien los distingue —y decide si se avisa
// al dueño— es el decorador de llmvia; aquí solo se propagan sin envolver, para que
// ni errors.Is ni el duck-typing del motivo se pierdan por el camino.
func (p *Provider) run(ctx context.Context, prompt string, opts llm.Options) (json.RawMessage, error) {
	plazo, err := p.plazo(ctx)
	if err != nil {
		return nil, err
	}
	raw, err := p.frame.Infer(ctx, p.tenantID, gatewaygrpc.InferRequest{
		Prompt:          prompt,
		Format:          p.format,
		Temperature:     opts.Temperature,
		Timeout:         plazo,
		OriginSessionID: p.origin,
	})
	if err != nil {
		return nil, err
	}
	return llm.ExtractJSON(raw)
}

// plazo resuelve el `timeout_ms` que viaja en el frame HEREDÁNDOLO del deadline del
// llamante. Es el corazón de este paquete desde el arreglo del reloj único: ver el
// bloque «UN SOLO RELOJ» de arriba.
//
// Tres desenlaces, y ninguno de ellos inventa un plazo por su cuenta:
//
//  1. **El ctx trae deadline** ⇒ lo que queda menos MargenVeredicto. Ese es el caso
//     de TODO el pipeline.
//  2. **Le queda menos que el margen** ⇒ ErrSinPresupuesto, sin tocar el cable.
//  3. **El ctx NO trae deadline** ⇒ DefaultTimeout, la red de seguridad.
//
// 🔴 LO QUE NO HACE, dicho porque es la tentación evidente: NO acota el resultado con
// p.timeout. Un `min(restante, p.timeout)` parecería prudente y sería exactamente el
// defecto de campo otra vez —el adaptador cortando por debajo de lo que su llamante
// estaba dispuesto a esperar—, solo que escrito con más letras.
func (p *Provider) plazo(ctx context.Context) (time.Duration, error) {
	dl, ok := ctx.Deadline()
	if !ok {
		return p.timeout, nil
	}
	restante := time.Until(dl) - MargenVeredicto
	if restante <= 0 {
		return 0, fmt.Errorf("%w: quedan %v, y el margen del veredicto es %v",
			ErrSinPresupuesto, time.Until(dl).Round(time.Millisecond), MargenVeredicto)
	}
	return restante, nil
}
