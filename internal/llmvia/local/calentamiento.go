package local

import (
	"context"

	"github.com/EduGoGroup/wapp-shared/llm"

	gatewaygrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/grpc"
)

// ════════════════════════════════════════════════════════════════════════════
// 🔴 EL CALENTAMIENTO LO EMITE EL CLOUD PORQUE EL EDGE NO CONOCE EL PROMPT
// (Plan 044 · Ola 1.7 · T1.7-4, D7-c, ADR-0045)
// ════════════════════════════════════════════════════════════════════════════
//
// EL PROBLEMA. Ollama cachea el PREFIJO del prompt: la primera inferencia con un
// prefijo nuevo paga prefill FRÍO (21,6 ms/token ⇒ ~50 s para un P1 de UAT) y las
// siguientes con el mismo prefijo pagan 0,07–0,55 s. O sea que el primer mensaje de
// un tenant después de conectar —o después de publicar su catálogo— es siempre el
// caro, y lo paga un cliente que está escribiendo por WhatsApp.
//
// POR QUÉ NO LO ARREGLA EL EDGE SOLO, que es lo primero que uno piensa: el Edge
// recibe el prompt VERBATIM y no sabe qué está clasificando (ADR-0045). No puede
// fabricarse un prefijo que coincida con el que le va a llegar, porque ese prefijo lo
// arma el Cloud desde `intent_configs`. Precalentar es, por construcción, del Cloud.
//
// QUÉ ES UN CALENTAMIENTO. Una inferencia de verdad —mismo frame, mismo aforo, misma
// plaza— con el prefijo REAL del tenant y un mensaje trivial al final, marcada con
// `warmup` (campo 10) y cuya salida se TIRA. Lo único que se busca es que el prefill
// quede en la caché de ESE Ollama.
//
// ⚠️ LO QUE CUESTA, dicho por su nombre: OCUPA LA PLAZA ÚNICA mientras corre. No
// puede solaparse con tráfico real porque pasa por el mismo aforo, y una ráfaga de
// ConfigUpdate es una ráfaga de prefills fríos — molesta, legítima y NO una avería.
// Lo que NO hace es contar para el breaker: el Edge lo excluye ANTES de evaluar,
// porque un calentamiento paga frío por diseño y un breaker que lo mirara abriría el
// circuito por haber trabajado bien.
//
// ✅ Y REPETIRLO ES BARATO, que es lo que hace que no haga falta ningún cooldown ni
// ninguna memoria de «a este ya lo calenté»: si la caché ya está caliente, el
// calentamiento cuesta el prefill caliente (0,07–0,55 s) más su generación acotada.
// Solo el primero de cada prefijo cuesta los ~50 s.
//
// 🔴 EL `think:false` NO SE PIDE DESDE AQUÍ, Y NO PODRÍA: es política FIJA del Edge
// (ADR-0045 §5) y no hay campo en el frame para ella. El calentamiento LA HEREDA
// porque no estrena camino: viaja en el mismo `inference_request` que una P1, así que
// el Edge lo sirve por el mismo `chat()` que aplica `think:false` a todo. Importa
// porque está medido que precargar SIN él convierte 4 s en 4 MINUTOS. Y el corolario
// de campo: `ollama ps` diciendo «Forever» NO es evidencia de calor; la evidencia es
// `prompt_eval_duration`.

// TextoDeCalentamiento es el mensaje trivial que va al FINAL del prompt de
// calentamiento.
//
// 🔴 TIENE QUE SER CORTO Y NO TIENE QUE DAR IGUAL DÓNDE VA. Lo que se cachea es el
// PREFIJO, y BuildClassifyRequestPrompt pone `Text` en la última línea: todo lo
// anterior —cabecera, catálogo, vocabulario, reglas, esquema y few-shot— depende solo
// del catálogo del tenant, así que un calentamiento con este texto deja cacheado
// EXACTAMENTE el mismo prefijo que consumirá el primer mensaje real. Cambiar el orden
// del constructor rompería esta tarea sin romper ningún test de esta tarea; lo custodia
// el test de prefijo de wapp-shared/llm (I6, ADR-0046).
const TextoDeCalentamiento = "hola"

// etapaCalentamiento es la P1 con el techo de salida bajado al mínimo útil.
//
// 🔴 TECHO 16, y es el único número del fichero que NO busca no truncar: aquí se
// quiere TRUNCAR. Del calentamiento solo interesa el prefill; la generación es
// desperdicio puro, y a 6–12 tok/s cada token de más son ~0,1 s de la plaza única.
// Dieciséis es suficiente para que el modelo arranque a escribir (y por tanto para que
// el prefill se haya consumido y quede cacheado) y ridículo como coste.
//
// `class` = lote porque nadie espera un turno detrás de esto. No es lo que lo
// distingue de una inferencia real —eso es `warmup`, y tiene que serlo: si el breaker
// excluyera por `class`, `class` estaría DECIDIENDO y el contrato lo prohíbe por
// escrito—, solo evita que el parte del Edge cuente los calentamientos como turnos
// interactivos, que es la etiqueta que el Edge pone cuando el campo llega vacío.
var etapaCalentamiento = etapa{maxOutputTokens: 16, class: gatewaygrpc.ClaseLote}

// Warm emite UN calentamiento contra el Edge de la sesión que se le fijó con
// WithTargetSession, usando el catálogo del tenant para reproducir el prefijo real.
//
// El `in.Text` que traiga el llamante SE IGNORA y se sustituye por
// TextoDeCalentamiento: así hay un solo sitio donde se decide qué mensaje trivial va
// al final, y dos llamantes no pueden calentar prefijos que difieran en su última
// línea. Lo que sí importa del `in` es todo lo demás —catálogo, vocabulario, etiqueta
// de desconocido—, que es lo que forma el prefijo.
//
// 🔴 NO DEVUELVE LA SALIDA Y NO LA MIRA: no se le pasa por ExtractJSON, no se valida y
// no se parsea. Con un techo de 16 tokens el JSON viene truncado CASI SIEMPRE, y eso
// es correcto —ver etapaCalentamiento—. Un `llm.ErrLLMQuality` aquí no significaría
// nada, y devolverlo invitaría a un reintento que solo gastaría plaza.
//
// El error que SÍ devuelve es el del transporte, tal cual, para que quien llame pueda
// loguearlo. ⚠️ Y no debe traducirse en un aviso de degradación al dueño: nadie pidió
// esto y su fallo no le quita nada al cliente. Es la misma familia que
// ErrInferenceAbandonada — ver el llamante en llmvia.
func (p *Provider) Warm(ctx context.Context, in llm.ClassifyRequestInput) error {
	plazo, err := p.plazo(ctx)
	if err != nil {
		return err
	}
	in.Text = TextoDeCalentamiento
	_, err = p.frame.Infer(ctx, p.tenantID, gatewaygrpc.InferRequest{
		Prompt: llm.BuildClassifyRequestPrompt(in),
		Format: p.format,
		// Temperature se deja en su cero, que es exactamente lo que manda una P1 real
		// (llm.TemperatureGreedy). No cambia el prefijo —solo el muestreo— pero
		// mantenerla igual evita que alguien lea aquí una diferencia que no existe.
		Timeout:         plazo,
		TargetSessionID: p.target,
		MaxOutputTokens: etapaCalentamiento.maxOutputTokens,
		Class:           etapaCalentamiento.class,
		Warmup:          true,
	})
	return err
}
