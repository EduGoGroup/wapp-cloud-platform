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
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"

	gatewaygrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/grpc"
)

// DefaultFormat es el formato que se le pide al modelo cuando no se configura otro.
// "json" a secas, no un JSON Schema: el Edge lo reenvía verbatim al proveedor sin
// parsearlo, y los artefactos versionados los valida el caller en Go.
const DefaultFormat = "json"

// DefaultTimeout es el presupuesto de UNA inferencia local cuando no se configura
// otro.
//
// POR QUÉ 30 s Y NO EL DefaultTimeout DE LA VÍA API (60 s): son dos fierros
// distintos con dos ventanas distintas. La medición de campo del propio plan sobre
// el VPS con qwen3:1.7b da p50 de 8,1 s y cola larga (hasta decenas de segundos), y
// el consumidor de esta vía —el adelanto de la ventana de agregación— tiene 45 s de
// ventana en los que además hay que hacer algo con la respuesta. Treinta segundos
// deja sitio para la cola sin comerse la ventana entera. No es un número medido con
// esta carga: es el punto de partida razonado, y quien lo cambie tiene que mirar las
// dos cosas a la vez (la cola del modelo y la ventana del llamante).
const DefaultTimeout = 30 * time.Second

// ErrSinTransporte indica que el Provider se construyó sin cable. Es un fallo de
// PROGRAMACIÓN del arranque, no de una llamada, y por eso New lo devuelve al
// construir en vez de dejar que reviente en la primera inferencia.
var ErrSinTransporte = errors.New("llmvia/local: el adaptador local necesita un transporte (Frame)")

// ErrSinTenant indica que el Provider se construyó sin tenant. Sin él no hay a qué
// Edge preguntar (INV-7/INV-8: el tenant no es opcional en ningún camino).
var ErrSinTenant = errors.New("llmvia/local: el adaptador local necesita un tenant")

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

// WithTimeout fija el presupuesto de cada inferencia. <= 0 se ignora y cae a
// DefaultTimeout: ninguna inferencia queda sin reloj.
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
	raw, err := p.frame.Infer(ctx, p.tenantID, gatewaygrpc.InferRequest{
		Prompt:          prompt,
		Format:          p.format,
		Temperature:     opts.Temperature,
		Timeout:         p.timeout,
		OriginSessionID: p.origin,
	})
	if err != nil {
		return nil, err
	}
	return llm.ExtractJSON(raw)
}
