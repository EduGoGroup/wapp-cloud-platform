// Porta internal/llmvia/llmvia.go @ ebf4eb7 (el turno acotado del Nivel B, líneas 468-656; partido de llmvia.go por E-13)

package llmvia

// ============================================================================
// EL TURNO ACOTADO: UNA PREGUNTA SUELTA, DENTRO DE UN TURNO DE WHATSAPP
// (Plan 044 · Ola 3.5 · T3.5-2, ADR-0044 §5 · Nivel B)
// ============================================================================
//
// # 🔴 ESTE FICHERO NO COMPARA POR VÍA (C2), Y NO ES ORGANIZACIÓN
//
// En el paquete viejo Turno vivía en llmvia.go porque el candado C2 exige que la
// lista de ficheros que preguntan por la vía sea EXACTAMENTE su lista de permitidos,
// y «ampliar la lista convertiría una regla de UN sitio en una de tres». El tope de
// tamaño del árbol nuevo (E-13) obliga a partir aquel fichero, pero NO autoriza a
// ampliar la lista: la pregunta «¿este tenant está en una vía que sirve un turno
// acotado?» la sigue contestando llmvia.go, con el MISMO switch de For, Warm y
// PlazaDe —mismas dos ramas, mismo default de REQ-33, mismo error para el valor
// inventado—. Aquí se arma el frame y nada más. Una comparación por vía en este
// fichero pone rojo el candado de c2_via_test.go.
//
// # POR QUÉ UN MÉTODO NUEVO EN EL PUERTO Y NO UNA LLAMADA POR local.Provider
//
// Es literalmente la doctrina del paquete: «si necesitas saber la vía fuera de la
// selección, lo que necesitas es OTRO MÉTODO EN EL PUERTO». Y además el camino de
// siempre no servía, por dos motivos independientes:
//
//  1. Los cinco métodos de llm.LLMProvider son las cinco etapas del pipeline
//     (P1–P5) y ninguna es esto. Meter el turno acotado en ClassifyRequest sería
//     estrenar un sexto significado para un método que ya tiene uno.
//  2. local.Provider DESCUENTA local.MargenVeredicto (7 s) del deadline del llamante
//     SIEMPRE. Es correcto para el pipeline —que llama con 40–45 s— y es ruinoso
//     aquí: el turno acotado dura 12 s por diseño, así que ese descuento se llevaría
//     más de la mitad del presupuesto o, con un ctx justo, devolvería
//     local.ErrSinPresupuesto sin tocar el cable. El margen se aplica al REVÉS en
//     este método: no se resta del plazo del Edge, se SUMA a lo que esperamos
//     nosotros.
//
// # QUE PASE POR EL AVISADOR, PORQUE UN FALLO DE AQUÍ ES UN FALLO DE LA VÍA
//
// Armar el frame por nuestra cuenta se salta el decorador que envuelve a For
// (notify.go), y con él el aviso al dueño del ADR-0044 §5. Sería una asimetría
// injustificable: si el Ollama del cliente está caído, su dueño tiene que enterarse
// igual lo pida el presupuesto o lo pida el carrito. Por eso este método pasa por el
// MISMO aviso —mismo mapeo de motivos, mismo dedupe, mismo log— y lo único que
// añade es decir por qué puerta entró (OrigenTurno).

import (
	"context"
	"errors"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// ErrViaSinTurnoAcotado indica que el tenant no está en una vía capaz de servir un
// turno acotado. NO es una avería: es la respuesta correcta para un tenant en vía
// API, y por eso es un error NOMBRADO y no un `nil` mudo — hermano de
// ErrViaSinCalentamiento y por el mismo motivo. El texto es observable y no cambia.
//
// # POR QUÉ LA VÍA API NO TIENE TURNO ACOTADO (todavía)
//
// Porque el adaptador de la vía API es wapp-shared/llm/api y expone EXACTAMENTE los
// cinco métodos del pipeline: no hay por dónde meterle un prompt suelto sin
// ampliar el puerto compartido y publicar una release de shared. Y no hace falta
// para esta ola: el turno acotado nace para que el carrito entienda «mejor dos», y
// el tenant en vía API no se queda sin carrito — se queda sin ESE escalón, o sea en
// el Nivel A de siempre (el reprompt), que es la degradación que este plan diseñó.
// Cuando alguien quiera cerrarlo, el sitio es el puerto de shared, no un `if` aquí.
var ErrViaSinTurnoAcotado = errors.New("llmvia: la vía del tenant no sabe servir un turno acotado")

// PlazoTurno es el presupuesto de UN turno acotado, el que viaja como `timeout_ms`
// en el frame. Vale 12 s. 🔴 EL NÚMERO ESTÁ RAZONADO Y NO SE TOCA SIN REHACER LA
// CUENTA (R4.6.c):
//
//		MEDIDO (2026-08-26, qwen3:1.7b, 18–20 tokens de salida, prefijo CALIENTE):
//		  VPS (CPU, ~6 tok/s):   mediana 4.588 ms, máximo 7.932 ms
//		  Local (GPU):           mediana   502 ms, máximo   760 ms
//		FRÍO (prefijo no cacheado): VPS 17.980 ms, local ~1.800 ms
//
//	 1. EL TECHO NO PUEDE ENVENENAR EL BREAKER DEL TENANT, que es COMPARTIDO con el
//	    pipeline de intakes. El Edge marca «lenta» toda respuesta que pase de 0,8 ×
//	    timeout_ms (ADR-0042). Con 12 s el umbral queda en 9.600 ms, por encima del
//	    peor caso caliente medido (7.932 ms) ⇒ las respuestas SANAS no cuentan como
//	    lentas. Con un timeout_ms de 5 s el umbral sería 4.000 ms y marcaría lentas
//	    CASI TODAS las respuestas buenas del VPS, abriendo el circuito del tenant por
//	    haber trabajado bien — y quien pagaría ese circuito abierto sería el
//	    pipeline, que no ha hecho nada.
//	 2. Y A LA VEZ TIENE QUE CORTAR EL CASO FRÍO (17.980 ms). Un turno que paga
//	    prefill frío NO CABE en un turno de WhatsApp, así que se corta y se degrada a
//	    Nivel A con aviso, que es el mecanismo del ADR-0044 §5 tal cual.
//
// 🔴 Y POR ESO NO SE CONSTRUYE NINGÚN DETECTOR DE «PREFIJO FRÍO»: el timeout YA lo
// implementa. Un detector sería una segunda verdad sobre lo mismo, con su propio
// estado y su propia forma de desincronizarse. Si te ves escribiéndolo, párate.
const PlazoTurno = 12 * time.Second

// TechoTurno es el presupuesto de SALIDA del turno acotado (campo 7 del frame). Vale
// 128. La salida real medida son 18–20 tokens —es un objeto de tres claves cortas y
// el JSON Schema forzado no deja producir más— así que 128 es ~6,5× lo observado: de
// sobra para el caso legítimo y suficientemente bajo para que un modelo degenerado
// no se coma el plazo entero generando basura. Es el mismo criterio de la tabla de
// etapas de llmvia/local, aplicado a una salida mucho más pequeña.
const TechoTurno int32 = 128

// TurnoRequest es lo que hay que saber para servir un turno acotado. El PROMPT y el
// ESQUEMA vienen armados de fuera y este paquete no los mira.
//
// 🔴 ESO ES C2, NO PEREZA: «este paquete no tiene un solo prompt propio». Quien sabe
// qué preguntar es quien conoce el dominio de la pregunta (el resolutor del carrito);
// aquí solo se elige la vía y se empuja el frame. Si un día el prompt del turno
// acabara escrito en este fichero, habría dos sitios donde vive el conocimiento del
// dominio y el segundo sería invisible.
type TurnoRequest struct {
	// Prompt es el texto YA COMPUESTO (instrucciones + few-shot + la pregunta).
	// Viaja verbatim: el Edge lo entrega al modelo en UN solo turno de usuario.
	Prompt string
	// Formato es el JSON Schema SERIALIZADO que fuerza la forma de la respuesta.
	// Viaja opaco como string —el campo del proto es un string y el Edge lo
	// distingue de la cadena "json" mirando si empieza por '{'— así que aquí no se
	// parsea ni se valida: un esquema roto tiene que llegar arriba como el 400 del
	// proveedor que es, no convertirse en otra cosa por el camino.
	Formato string
}

// Turno sirve UN turno acotado del Nivel B y devuelve el texto CRUDO del modelo.
//
// No lo parsea, no lo valida y no comprueba que sea JSON: el contrato es idéntico
// al de local.Frame.Infer y por el mismo motivo. Quien pregunta es quien sabe qué
// forma espera, y la última palabra sobre lo que el modelo dijo la tiene Go, no el
// modelo.
//
// # Qué vía (lo contesta llmvia.go; ver la cabecera de este fichero)
//
//   - Store.Get falla ⇒ ("", error) con el prefijo literal
//     "llmvia: leyendo la configuración LLM del tenant: "; sin cable y sin aviso.
//   - sin fila, o vía `local` ⇒ se sirve (REQ-33: sin fila es local).
//   - vía `api` ⇒ ("", ErrViaSinTurnoAcotado) SIN tocar el cable, sin aviso y sin
//     contar: una vía que no sirve este escalón no es una degradación del equipo del
//     dueño.
//   - cualquier otro valor ⇒ ("", ErrViaDesconocida envuelto), como For.
//   - vía local sin frame (WithFrame) ⇒ ("", local.ErrSinTransporte): un selector sin
//     cable es un fallo de ARRANQUE, y se dice con el vocabulario de siempre en vez
//     de estrenar un tercer nombre para el mismo problema.
//
// # Qué viaja en el frame (R4.6.c)
//
// UNA llamada a Frame.Infer, con el tenant y exactamente esto:
//
//   - Prompt y Format: los de t, verbatim (Format = t.Formato);
//   - Temperature: 0, y no es configurable a propósito: esto no redacta nada, elige
//     entre opciones que ya existen. El reintento a 0,3 por calidad que el pipeline
//     tiene previsto aquí NO aplica — un segundo viaje de 4–8 s dentro del mismo
//     turno de WhatsApp cuesta más de lo que rescata;
//   - Timeout: PlazoTurno, ENTERO, traiga el ctx el plazo que traiga;
//   - OriginSessionID: la sesión de la CONVERSACIÓN que preguntó (contesta el Edge
//     que tiene caliente el prefijo de este prompt);
//   - MaxOutputTokens: TechoTurno;
//   - Class: edgegrpc.ClassInteractive (solo rótulo: separa en el parte lo que
//     alguien estaba esperando de lo que corría de fondo);
//   - TargetSessionID vacío y Warmup false: un turno NO es un calentamiento.
//
// # LOS DOS RELOJES, Y POR QUÉ EL MARGEN SE SUMA EN VEZ DE RESTARSE
//
//	al Edge se le pide          PlazoTurno                              (12 s)
//	el gateway espera           PlazoTurno + edgegrpc.DefaultInferGrace (17 s)
//	nosotros esperamos hasta    PlazoTurno + local.MargenVeredicto      (19 s)
//
// El ctx que recibe el Frame vence como mucho a PlazoTurno + local.MargenVeredicto.
// Con MargenVeredicto (7 s) > DefaultInferGrace (5 s), el timer del gateway vence
// ANTES que nuestro ctx, así que el desenlace es DETERMINISTA: o llega el timeout
// nombrado del Edge —el caso normal, a los ~12 s— o el gateway emite `timeout` CON
// motivo. Lo que nunca gana es nuestro `ctx.Done()`, que sería
// edgegrpc.ErrInferenceAbandoned: sin motivo, SIN AVISO AL DUEÑO y mintiendo sobre
// la causa.
//
// ⚠️ El ctx del llamante SIGUE MANDANDO cuando es más corto: gana el que venza
// antes. Y un ctx corto NO deja el turno sin presupuesto: aquí no existe
// local.ErrSinPresupuesto, el frame sale igual con su Timeout de 12 s.
//
// # El resultado y el aviso
//
// Éxito ⇒ (el texto crudo, nil), sin aviso ni conteo. Fallo del frame ⇒ ("", ese
// error INTACTO), y pasa por el aviso de notify.go con origen OrigenTurno y la vía
// local: si tiene motivo, se cuenta (aunque no haya notificador) y se escribe el
// aviso al dueño.
func (s *Selector) Turno(ctx context.Context, tenantID, originSessionID string, t TurnoRequest) (string, error) {
	panic(pendiente.Implementar("llmvia.Selector.Turno"))
}
