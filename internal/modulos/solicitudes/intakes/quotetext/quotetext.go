// Porta internal/intakes/quotetext/quotetext.go @ 36d5a04

package quotetext

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

// DefaultExamples es el N de D-044.11: cuántas cotizaciones aprobadas del tenant se
// le enseñan al modelo como few-shot. 5. Es una constante NOMBRADA porque es una
// decisión de producto («cinco basta para que se le pegue el tono»).
//
// Era `EjemplosPorDefecto` en el paquete viejo.
const DefaultExamples = 5

// Origen de la sugerencia. Vocabulario cerrado: viaja por la API y por el log. Eran
// `OrigenLLM` y `OrigenDeterminista` en el paquete viejo.
const (
	// SourceLLM — el modelo redactó el texto Y sus importes cuadran con las líneas.
	SourceLLM = "llm"
	// SourceDeterministic — el texto lo compuso Render. Siempre viene con Reason.
	SourceDeterministic = "deterministic"
)

// Motivos por los que se cayó al determinista que NO vienen del verificador de
// precios. Los del verificador son los Reason… de precios.go y viajan por el mismo
// campo (`fallback_reason`): para quien lee el log son la misma pregunta. Los VALORES
// no cambian. Eran los `Motivo…` del paquete viejo.
const (
	// ReasonNoExamples — el tenant no tiene ni historial aprobado ni semilla, así
	// que no hay voz que imitar. 🔴 EN ESTE CASO NO SE LLAMA AL MODELO: no es que la
	// llamada se descarte, es que no ocurre. Era `MotivoSinEjemplos`.
	ReasonNoExamples = "sin_ejemplos"
	// ReasonProviderUnavailable — no se pudo obtener el provider de la vía del
	// tenant (credencial caída, vía desconocida, entitlement). Era
	// `MotivoProveedorNoDisponible`.
	ReasonProviderUnavailable = "proveedor_no_disponible"
	// ReasonLLMFailed — el proveedor respondió con error (transporte, timeout o
	// calidad). No se reintenta aquí. Era `MotivoLLMFallo`.
	ReasonLLMFailed = "llm_fallo"
	// ReasonUnreadableOutput — respondió, pero lo que devolvió no es el artefacto
	// P5. Era `MotivoSalidaIlegible`.
	ReasonUnreadableOutput = "salida_no_es_artefacto"
)

// ErrNotWired es «se intentó construir el servicio sin una pieza obligatoria».
//
// Era `ErrSinCablear` en el paquete viejo.
var ErrNotWired = errors.New("quotetext: faltan piezas obligatorias (log, solicitudes, historial o selector)")

// ErrNoLines es «esta solicitud no tiene nada que cotizar»: ni una línea de cliente.
// Es el hermano de intakes.ErrEmptyQuote y se declara aquí porque el que sale de
// Approve arrastra su propia semántica de transición, que este camino no tiene.
//
// Era `ErrSinLineas` en el paquete viejo.
var ErrNoLines = errors.New("quotetext: la solicitud no tiene líneas que cotizar")

// ════════════════════════════════════════════════════════════════════════════
// 🔴 DEUDA DECLARADA: NO HAY FORMA DE MEDIR CUÁNTAS VECES SE CAE AL DETERMINISTA
// ════════════════════════════════════════════════════════════════════════════
//
// Esto NO es un olvido, es un hueco conocido que se deja abierto con nombre y apellido.
//
// LO QUE HAY: cuando el texto del modelo se descarta, el motivo sale por DOS sitios y
// los dos son de consumo individual — una línea de `log.Warn` con `motivo` y `detalle`,
// y el campo `fallback_reason` de la respuesta HTTP, que ve la consola de UN dueño en
// UNA pantalla.
//
// LO QUE NO HAY: ninguna serie agregable. No se emite `flow_event` ni métrica Prometheus
// alguna, así que NADIE puede responder «¿qué porcentaje de sugerencias las escribe de
// verdad el modelo?» ni «¿cuál de los nueve motivos manda?». Y esas dos preguntas son
// las que deciden si la voz de la dueña funciona o si se está sirviendo el texto sobrio
// todo el rato: exactamente el modo de fallo MUDO que ya se pagó una vez en esta tarea
// —el umbral que apagaba el generador con una galleta barata en el carrito— y que solo
// apareció porque alguien fue a buscarlo a mano.
//
// POR QUÉ NO SE CONSTRUYE AQUÍ: la telemetría de esta ola es T5.2, y su lista de
// eventos está CERRADA en design §10 (`intake_draft_created`, `intake_line_corrected`,
// `intake_approved`, `intake_info_requested`, `intake_reanalyzed`). Ninguno de los cinco
// cubre esto, y añadir un sexto es una decisión de esa tarea, no de ésta. Meterlo por mi
// cuenta dejaría un evento fuera del contrato que T5.2 tiene que emitir con payloads
// exactos.
//
// QUÉ HARÍA FALTA, para que quien lo recoja no tenga que redescubrirlo: un contador con
// las etiquetas `origen` (llm|deterministic) y `motivo` —el vocabulario ya es cerrado y
// ya viaja por este struct, así que el productor es una línea en Suggest—, o un
// `flow_event` equivalente si se prefiere la vía del outbox. Dueño natural: T5.2.
// ════════════════════════════════════════════════════════════════════════════

// Suggestion es lo que devuelve el generador: un texto y la verdad sobre quién lo
// escribió.
//
// 🔴 EL ORIGEN NO ES TELEMETRÍA DECORATIVA. Es lo que le permite a la consola —y a
// quien lea un log— saber si lo que tiene delante lo redactó el modelo o es el
// respaldo sobrio, y por qué.
//
// Era `Sugerencia` en el paquete viejo.
type Suggestion struct {
	// Text es la cotización sugerida, lista para que el dueño la edite y la apruebe.
	// Era `Texto`.
	Text string
	// Source es SourceLLM o SourceDeterministic. Era `Origen`.
	Source string
	// Reason dice POR QUÉ no fue el modelo: uno de los trece Reason… del paquete.
	// Vacío cuando Source es SourceLLM. Era `Motivo`.
	Reason string
}

// IntakeReader es de dónde sale la solicitud con sus líneas y sus revisiones. Lo
// satisfacen *intakes.Service y *intakes.MemoryStore. Solo LEE.
//
// Era `LectorSolicitudes` en el paquete viejo.
type IntakeReader interface {
	Get(ctx context.Context, tenantID, intakeID string) (intakes.Detail, error)
}

// HistoryReader devuelve los textos de las últimas cotizaciones APROBADAS del
// tenant, de la más reciente a la más antigua. Lo satisface *intakes.MemoryStore (y
// el almacén Postgres de intakes). Solo LEE.
//
// Es POR TENANT y no por solicitud: la voz de la dueña se aprende de lo que escribió
// en OTROS pedidos.
//
// Era `LectorHistorial` en el paquete viejo.
type HistoryReader interface {
	ApprovedRenderedTexts(ctx context.Context, tenantID string, limit int) ([]string, error)
}

// SeedReader lee el blob de `public.tenant_content`. Lo satisface el repositorio de
// contenido del tenant por tipado estructural. Es OPCIONAL: sin él, el few-shot se
// arma solo con el historial. Solo LEE.
//
// Era `LectorSemilla` en el paquete viejo.
type SeedReader interface {
	GetTenantContent(ctx context.Context, tenantID, ref string) ([]byte, error)
}

// ProviderSelector traduce un tenant en el llm.LLMProvider de SU vía. Lo satisface
// el selector de `llmvia`.
//
// Este paquete NO sabe qué vía le tocó al tenant y no puede preguntarlo (requisito C2
// del ADR-0044): pide un provider y llama a GenerateQuoteText venga de donde venga.
type ProviderSelector interface {
	For(ctx context.Context, tenantID, originSessionID string) (llm.LLMProvider, error)
}

// Service es el generador. Sus cuatro puertos son de LECTURA: no hay ninguno que
// escriba y no hay ninguno que envíe (INV-1: ningún camino automático aprueba, y la
// forma de que siga siendo verdad es que el generador no tenga por dónde hacerlo).
//
// Era `Servicio` en el paquete viejo.
type Service struct {
	log      logger.Logger
	reader   IntakeReader
	history  HistoryReader
	seed     SeedReader
	selector ProviderSelector
	quota    int
	timeout  time.Duration
}

// Option configura el servicio al construirlo.
//
// Era `Opción` en el paquete viejo.
type Option func(*Service)

// WithSeed enchufa el lector de `tenant_content`. Sin él —o con un lector nil, que se
// ignora— no hay ejemplos semilla y el few-shot sale solo del historial.
//
// Era `ConSemilla` en el paquete viejo.
func WithSeed(seed SeedReader) Option {
	return func(service *Service) {
		if seed != nil {
			service.seed = seed
		}
	}
}

// WithExamples fija el N del few-shot: el cupo de ejemplos y el `limit` con el que
// se le pide el historial al HistoryReader. Un valor <= 0 se ignora y deja
// DefaultExamples: el llamante natural es una config con default, y un cero ahí
// significa «no configurado».
//
// Era `ConEjemplos` en el paquete viejo.
func WithExamples(n int) Option {
	return func(service *Service) {
		if n > 0 {
			service.quota = n
		}
	}
}

// WithTimeout acota cuánto puede durar LA llamada al modelo: el contexto que recibe
// el proveedor lleva ese plazo. Un valor <= 0 se ignora y el proveedor hereda el
// contexto del llamante tal cual, sin plazo añadido. No acota las lecturas.
//
// Era `ConPlazo` en el paquete viejo.
func WithTimeout(d time.Duration) Option {
	return func(service *Service) {
		if d > 0 {
			service.timeout = d
		}
	}
}

// NewService construye el generador con N = DefaultExamples, sin semilla y sin plazo
// propio, y le aplica las opciones en orden (una opción nil se ignora). Devuelve
// (nil, ErrNotWired) si falta el log, el lector de solicitudes, el del historial o el
// selector.
//
// Era `NewServicio` en el paquete viejo.
func NewService(log logger.Logger, intakeReader IntakeReader, history HistoryReader,
	selector ProviderSelector, opts ...Option) (*Service, error) {
	if log == nil || intakeReader == nil || history == nil || selector == nil {
		return nil, ErrNotWired
	}
	service := &Service{log: log, reader: intakeReader, history: history, selector: selector, quota: DefaultExamples}
	for _, opt := range opts {
		if opt != nil {
			opt(service)
		}
	}
	return service, nil
}

// Suggest devuelve el texto sugerido para la cotización de una solicitud. No escribe
// nada, no transiciona nada y no le manda nada a nadie.
//
// # EL ORDEN, QUE ES EL CONTRATO
//
//  1. La solicitud, por el IntakeReader. Su error se devuelve TAL CUAL
//     (intakes.ErrNotFound si no es del tenant: 404 opaco, INV-8), con una Suggestion
//     vacía.
//  2. Las precondiciones de contenido, que son las MISMAS que las de `Approve`: sin
//     líneas de cliente (HasCustomerLines) ⇒ ErrNoLines; con líneas sin precio en el
//     borrador vigente (intakes.PendingPriceLines) ⇒ *intakes.PendingPriceError con
//     TODAS ellas. Una sugerencia para un presupuesto que no se puede aprobar es
//     trabajo tirado.
//  3. El borrador (DraftOf) y su Render, que se calculan SIEMPRE y ANTES de tocar el
//     modelo: a partir de aquí ya hay una respuesta buena pase lo que pase, y Suggest
//     ya NO devuelve error. Si el total de la cabecera no es la suma de las líneas
//     (más de medio céntimo de diferencia), manda la suma y se avisa por log
//     (`quotetext: el total de la cabecera no es la suma de las líneas; manda la
//     suma`, con `tenant_id`, `intake_id`, `total_cabecera` y `suma_lineas`).
//  4. Si ExpectedSequence está vacía (todas las líneas por confirmar) ⇒ el
//     determinista con ReasonDraftWithoutAmounts, SIN leer ejemplos y SIN pedir
//     provider: no hay ni un importe que el modelo pudiera copiar.
//  5. El few-shot (abajo). **Sin ejemplos ⇒ el determinista con ReasonNoExamples, y
//     NO SE LLAMA AL MODELO** — ni siquiera se le pide el provider al selector, que
//     ya tocaría `tenant_llm` y podría disparar un aviso de degradación.
//  6. El provider, por el selector, con el tenant y la SESIÓN DE ORIGEN de la
//     solicitud (es lo que enruta la inferencia al Edge correcto). Su error ⇒
//     ReasonProviderUnavailable.
//  7. UNA llamada a GenerateQuoteText, sin reintento, con el JSON del borrador
//     (Draft.JSON: lleva los importes y NO los SKU), los ejemplos y temperatura
//     greedy, acotada por WithTimeout. Su error —transporte, plazo o
//     llm.ErrLLMQuality— ⇒ ReasonLLMFailed.
//  8. La salida que no es el artefacto P5 (llm.ParseQuoteText) ⇒
//     ReasonUnreadableOutput.
//  9. Verify contra el borrador. Si no pasa ⇒ el determinista con el Reason del
//     veredicto (INV-2). Si pasa ⇒ {Text: el del modelo, EXACTO; Source: SourceLLM;
//     Reason vacío}.
//
// Toda caída de los pasos 4–9 devuelve error nil y {Text: Render del borrador;
// Source: SourceDeterministic; Reason: el motivo}: el proveedor caído, el plazo, la
// salida ilegible y el precio inventado tienen todos la misma respuesta correcta —el
// texto sobrio— y convertir alguno en un 500 le quitaría al dueño una sugerencia que
// sí se podía dar. Cada caída de los pasos 6–9 deja un aviso en el log con
// `intake_id`; 🔴 ningún aviso cita el texto que redactó el modelo.
//
// 🔴 NO SE COMPRUEBA EL ESTADO DE LA SOLICITUD, Y ES UNA DECISIÓN. `Approve` exige
// `pending_approval` porque transiciona; esto no transiciona nada. Un 422 aquí le
// impediría al dueño mirar cómo habría quedado el texto de un pedido que ya cerró.
//
// # EL FEW-SHOT (D-044.11)
//
// Son las últimas N cotizaciones aprobadas del tenant MÁS los ejemplos semilla
// (SeedStyleRef, leídos con ParseSeed), saneados y acotados:
//
//   - SANEO, por fuente y conservando el orden: cada ejemplo se recorta de blancos;
//     se descartan los vacíos, los repetidos (igualdad exacta), los que no son UTF-8 y
//     los que pasan de MaxExampleRunes (ENTEROS: jamás se trunca uno).
//   - REPARTO DEL CUPO: la semilla tiene reservada la mitad del cupo y el historial
//     se queda con el resto (con N = 5: hasta 3 del historial y 2 de la semilla). Con
//     la semilla vacía, el historial usa el cupo entero. La semilla rellena hasta N
//     si el historial no llega, sin repetir un texto que ya esté.
//   - ORDEN: historial primero (más reciente antes) y semilla después, que es el de
//     prioridad decreciente.
//   - PRESUPUESTO: se recorre esa lista sumando runas y, en cuanto un ejemplo NO CABE
//     en MaxFewShotRunes, se para: se descartan él y TODOS LOS SIGUIENTES aunque
//     alguno posterior cupiera. El resultado es siempre «los K primeros». El recorte
//     se avisa por log (`quotetext: el few-shot no cabe en su presupuesto; se recorta
//     por la cola`, con `tenant_id`, `ejemplos_pedidos`, `ejemplos_usados`,
//     `runas_usadas` y `presupuesto_runas`).
//   - Ningún fallo de lectura tumba nada: si el historial falla, el few-shot va sin
//     él (y se avisa); si la semilla no existe —el caso NORMAL— o su blob es
//     ilegible, se ignora.
//
// Era `Sugerir` en el paquete viejo.
func (s *Service) Suggest(ctx context.Context, tenantID, intakeID string) (Suggestion, error) {
	detail, err := s.reader.Get(ctx, tenantID, intakeID)
	if err != nil {
		return Suggestion{}, err
	}
	if !HasCustomerLines(detail.Items) {
		return Suggestion{}, ErrNoLines
	}
	if pending := intakes.PendingPriceLines(detail.Revisions); len(pending) > 0 {
		return Suggestion{}, &intakes.PendingPriceError{Lines: pending}
	}

	draft := DraftOf(detail.Items)
	if !sameAmount(draft.Total, detail.Total) {
		// No corrige nada: manda la suma de las líneas, que es lo que el cliente puede
		// comprobar a mano. Pero se dice, porque el store promete que coinciden
		// (EnsureShippingLine cuadra la cabecera) y que dejen de hacerlo es un dato.
		s.log.Warn("quotetext: el total de la cabecera no es la suma de las líneas; manda la suma",
			"tenant_id", tenantID, "intake_id", intakeID,
			"total_cabecera", detail.Total, "suma_lineas", draft.Total)
	}
	deterministic := Render(draft)

	if len(ExpectedSequence(draft)) == 0 {
		// Todas las líneas están por confirmar: no hay ni un importe que el modelo
		// pudiera copiar ni que se le pudiera verificar. Llamarlo sería gastar una
		// inferencia para tirar su respuesta.
		return Suggestion{Text: deterministic, Source: SourceDeterministic, Reason: ReasonDraftWithoutAmounts}, nil
	}

	examples := s.examples(ctx, tenantID)
	if len(examples) == 0 {
		return Suggestion{Text: deterministic, Source: SourceDeterministic, Reason: ReasonNoExamples}, nil
	}

	text, reason := s.compose(ctx, tenantID, detail, draft, examples)
	if reason != "" {
		return Suggestion{Text: deterministic, Source: SourceDeterministic, Reason: reason}, nil
	}
	return Suggestion{Text: text, Source: SourceLLM}, nil
}

// compose hace LA llamada al modelo y verifica lo que conteste. Devuelve el texto y
// un motivo VACÍO cuando el texto se puede mandar; en cualquier otro caso devuelve el
// motivo por el que no, y el llamante usa el determinista.
//
// 🔴 NO DEVUELVE ERROR, Y NO ES PEREZA: aquí no hay ningún fallo que deba tumbar la
// petición del dueño. El proveedor caído, el timeout, la salida ilegible y el precio
// inventado tienen todos la misma respuesta correcta —el texto sobrio— y convertir
// alguno en un 500 le quitaría al dueño una sugerencia que sí se podía dar.
//
// Era `redactar` en el paquete viejo.
func (s *Service) compose(ctx context.Context, tenantID string, detail intakes.Detail,
	draft Draft, examples []string) (string, string) {
	quote, err := draft.JSON()
	if err != nil {
		s.log.Warn("quotetext: no se pudo serializar el borrador para el prompt",
			"intake_id", detail.ID, "error", err)
		return "", ReasonUnreadableOutput
	}
	provider, err := s.selector.For(ctx, tenantID, detail.SessionID)
	if err != nil {
		s.log.Warn("quotetext: no hay proveedor LLM para este tenant; sale el texto determinista",
			"intake_id", detail.ID, "error", err)
		return "", ReasonProviderUnavailable
	}

	raw, err := s.request(ctx, provider, quote, examples)
	if err != nil {
		s.log.Warn("quotetext: el proveedor no redactó la cotización; sale el texto determinista",
			"intake_id", detail.ID, "calidad", errors.Is(err, llm.ErrLLMQuality), "error", err)
		return "", ReasonLLMFailed
	}
	artifact, err := llm.ParseQuoteText(raw)
	if err != nil {
		// 🔴 El error NO cita `raw`: es texto redactado, no metadato.
		s.log.Warn("quotetext: la salida del modelo no es un artefacto P5 legible; sale el texto determinista",
			"intake_id", detail.ID, "error", err)
		return "", ReasonUnreadableOutput
	}

	if verdict := Verify(draft, artifact.Text); !verdict.OK {
		s.log.Warn("quotetext: el texto del modelo NO cuadra con las líneas (INV-2); sale el texto determinista",
			"intake_id", detail.ID, "motivo", verdict.Reason, "detalle", verdict.Detail)
		return "", verdict.Reason
	}
	return artifact.Text, ""
}

// request es LA llamada, acotada por su propio plazo. Extraída por lo mismo que
// `pedirCantidades` en P4: el `defer cancel()` tiene que cerrar donde acaba la llamada.
//
// Era `pedir` en el paquete viejo.
func (s *Service) request(ctx context.Context, provider llm.LLMProvider,
	quote json.RawMessage, examples []string) (json.RawMessage, error) {
	callCtx, cancel := s.bound(ctx)
	defer cancel()
	return provider.GenerateQuoteText(callCtx,
		llm.GenerateQuoteTextInput{Quote: quote, Examples: examples},
		llm.Options{Temperature: llm.TemperatureGreedy})
}

// bound envuelve el ctx con el plazo por llamada. Devuelve SIEMPRE una cancelación
// llamable, para que el `defer cancel()` no necesite un `if` alrededor.
//
// Era `acotar` en el paquete viejo.
func (s *Service) bound(ctx context.Context) (context.Context, context.CancelFunc) {
	if s.timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, s.timeout)
}
