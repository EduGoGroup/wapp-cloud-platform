// Porta internal/llmvia/local/calentamiento.go @ ebf4eb7

package local

import (
	"context"

	"github.com/EduGoGroup/wapp-shared/llm"

	edgegrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/grpc"
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
// POR QUÉ NO LO ARREGLA EL EDGE SOLO: recibe el prompt VERBATIM y no sabe qué está
// clasificando (ADR-0045). No puede fabricarse un prefijo que coincida con el que le
// va a llegar, porque ese prefijo lo arma el Cloud desde el catálogo del tenant.
// Precalentar es, por construcción, del Cloud.
//
// QUÉ ES UN CALENTAMIENTO. Una inferencia de verdad —mismo frame, mismo aforo, misma
// plaza— con el prefijo REAL del tenant y un mensaje trivial al final, marcada con
// `warmup` (campo 10) y cuya salida se TIRA. Lo único que se busca es que el prefill
// quede en la caché de ESE Ollama.
//
// ⚠️ LO QUE CUESTA: OCUPA LA PLAZA ÚNICA mientras corre. Lo que NO hace es contar
// para el breaker: el Edge lo excluye ANTES de evaluar, porque un calentamiento paga
// frío por diseño. Y repetirlo es barato (prefill caliente más 16 tokens), así que no
// hace falta ningún cooldown ni memoria de «a este ya lo calenté».
//
// 🔴 EL `think:false` NO SE PIDE DESDE AQUÍ, Y NO PODRÍA: es política FIJA del Edge
// (ADR-0045 §5) y no hay campo en el frame para ella. El calentamiento LA HEREDA
// porque no estrena camino: viaja en el mismo `inference_request` que una P1.

// TextoDeCalentamiento es el mensaje trivial que va al FINAL del prompt de
// calentamiento. Vale exactamente "hola".
//
// 🔴 TIENE QUE SER CORTO Y NO DA IGUAL DÓNDE VA. Lo que se cachea es el PREFIJO, y
// llm.BuildClassifyRequestPrompt pone `Text` en la última línea: todo lo anterior
// —cabecera, catálogo, vocabulario, reglas, esquema y few-shot— depende solo del
// catálogo del tenant, así que un calentamiento con este texto deja cacheado
// EXACTAMENTE el mismo prefijo que consumirá el primer mensaje real. El orden del
// constructor lo custodia el test de prefijo de wapp-shared/llm (I6, ADR-0046).
const TextoDeCalentamiento = "hola"

// warmupStage (en el paquete viejo, etapaCalentamiento) es la P1 con el techo de
// salida bajado al mínimo útil.
//
// 🔴 TECHO 16, y es el único número del paquete que NO busca no truncar: aquí se
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
var warmupStage = stage{maxOutputTokens: 16, class: edgegrpc.ClassBatch}

// Warm emite UN calentamiento contra el Edge de la sesión que se fijó con
// WithTargetSession, usando el catálogo del tenant para reproducir el prefijo real.
// Promete, sobre la ÚNICA petición que hace al Frame (con el tenant de New):
//
//   - Prompt: llm.BuildClassifyRequestPrompt(in) con in.Text SUSTITUIDO por
//     TextoDeCalentamiento. El texto que traiga el llamante se ignora y no llega al
//     modelo: así dos llamantes no pueden calentar prefijos que difieran en su última
//     línea. Todo lo demás de `in` —catálogo, vocabulario, etiqueta de desconocido—
//     viaja intacto, y por eso el prefijo (el prompt menos su última línea) es byte a
//     byte el de una ClassifyRequest real con el mismo `in`;
//   - Warmup: true. Es lo que hace que el Edge lo excluya del breaker;
//   - MaxOutputTokens: 16 — o 0 con WithMaxOutputTokens(false), igual que el resto.
//     Es el único techo del paquete que busca TRUNCAR: solo interesa el prefill, y
//     cada token generado es plaza única gastada en algo que se tira;
//   - Class: edgegrpc.ClassBatch, porque nadie espera un turno detrás de esto. NO es
//     lo que lo distingue de una inferencia real (eso es Warmup; `class` no decide);
//   - TargetSessionID: el de WithTargetSession. OriginSessionID: VACÍO siempre, aunque
//     el Provider tenga WithOriginSession: un calentamiento no lo originó ninguna
//     conversación y rellenarlo pondría un dato de trazabilidad FALSO en el cable;
//   - Format: el del Provider. Temperature: 0, lo mismo que manda una P1 real;
//   - Timeout: el plazo heredado, con las mismas tres reglas que los cinco métodos
//     (ver «Un solo reloj» en local.go).
//
// 🔴 NO DEVUELVE LA SALIDA Y NO LA MIRA: no pasa por llm.ExtractJSON, no se valida y
// no se parsea. Con un techo de 16 tokens el JSON viene truncado CASI SIEMPRE, y eso
// es correcto: una salida ilegible devuelve nil.
//
// Errores, y solo estos dos:
//
//   - ErrSinPresupuesto (envuelto), sin tocar el Frame, si el plazo no alcanza;
//   - el error del Frame, TAL CUAL, para que quien llame pueda loguearlo. ⚠️ No debe
//     traducirse en un aviso de degradación al dueño: nadie pidió esto y su fallo no
//     le quita nada al cliente (lo garantiza el llamante, en llmvia).
func (p *Provider) Warm(ctx context.Context, in llm.ClassifyRequestInput) error {
	timeout, err := p.frameTimeout(ctx)
	if err != nil {
		return err
	}
	in.Text = TextoDeCalentamiento
	_, err = p.frame.Infer(ctx, p.tenantID, edgegrpc.InferRequest{
		Prompt: llm.BuildClassifyRequestPrompt(in),
		Format: p.format,
		// Temperature se deja en su cero, que es exactamente lo que manda una P1 real
		// (llm.TemperatureGreedy). No cambia el prefijo —solo el muestreo— pero
		// mantenerla igual evita que alguien lea aquí una diferencia que no existe.
		Timeout:         timeout,
		TargetSessionID: p.target,
		MaxOutputTokens: p.outputCap(warmupStage),
		Class:           warmupStage.class,
		Warmup:          true,
	})
	// La salida se TIRA sin mirarla: ni llm.ExtractJSON ni validación. Devolver un
	// llm.ErrLLMQuality aquí invitaría a un reintento que solo gastaría plaza.
	return err
}
