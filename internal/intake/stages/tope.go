package stages

import (
	"github.com/EduGoGroup/wapp-shared/llm"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake"
)

// ════════════════════════════════════════════════════════════════════════════
// 🔴 EL TOPE DE ÍTEMS POR PEDIDO (Plan 044 · T2.6 · ADR-0046 § Mecanismo 1)
// ════════════════════════════════════════════════════════════════════════════
//
// Hasta esta tarea el fan-out de P3 NO TENÍA TECHO: un pedido de N ítems eran N
// llamadas de lote sobre la PLAZA ÚNICA del Edge (un Ollama por máquina), y cada
// una de esas llamadas son **22–32 s medidos** (veredicto §1.4 del Plan 044). Un
// mensaje con 40 ítems ocupaba el negocio entero durante veinte minutos y dejaba
// detrás a todos los turnos interactivos de Nivel B.
//
// Esto es lo que lo acota, y es **un entero**. No una cola con prioridades, no un
// planificador: el ADR-0046 §Alternativas los descarta POR ESCRITO, con la misma
// doctrina del ADR-0003 («cuando un entero resuelve el problema, no se mete
// infraestructura»).
//
// # 🔴 EL NÚMERO NO CABE EN «< 5 MIN», Y ESO NO ES UN DESCUIDO
//
// Hay que decirlo con los segundos delante, porque la tentación de leer «10» como
// «lo que cabe en el plazo prometido» es enorme y sería FALSO:
//
//	10 ítems por la vía local ≈ P2 ~30 s + 10 × P3 ~25 s (250 s) + P4 ~25–37 s
//	                            + P5 ~15–43 s  ⇒  **320–360 s ≈ 5:20–6:00**
//
// y con el peor caso que `pipeline.PlazoPorLlamadaSuelo` (48 s) LE PERMITE a un
// ítem retener la plaza (48 − 7 s de MargenVeredicto = 41 s), 10 × 41 = 410 s
// **≈ 6:50**. Las dos cuentas están **POR ENCIMA de los 300 s** de la métrica
// reina del plan.
//
// ⇒ La promesa de tiempo y el tope son DOS NÚMEROS DISTINTOS, y cada uno dice lo
// suyo:
//
//   - **«< 5 min» se cumple hasta ~4 ítems** por la vía local (30 + 4×25 + ~31 +
//     ~29 ≈ 190 s ≈ 3:10). Por encima, el borrador **«llega, pero después»**.
//   - **10 es lo que el pipeline PROCESA**, no lo que promete en 5 minutos. Es el
//     número que fijó Jhoan (D5, D-044.39, 2026-08-24) y el que el ADR-0046
//     publica en su §Mecanismo 1 tras la Corrección 2.
//
// Por la vía API la cuenta de segundos es OTRA y se escribirá cuando se mida; el
// tope de ítems, en cambio, aplica igual, porque no acota segundos de plaza sino
// **cuánto trabajo se admite por pedido**.
//
// # POR QUÉ ES UNA CONSTANTE Y NO UNA PERILLA POR TENANT
//
// Porque D5 lo decidió así: **un solo número para todos los tenants**. El
// enunciado de T2.6 llegó a la mañana pidiendo `max_items_por_pedido` configurable
// y quedó TACHADO en el propio plan. Una perilla por tenant sería una columna, una
// migración, un endpoint de consola y una decisión de producto por cliente, para
// gobernar un recurso —la plaza del Edge— que ni siquiera es del tenant que la
// configuraría: es de su MÁQUINA.
//
// # DÓNDE SE APLICA, Y POR QUÉ AQUÍ Y NO EN EL WORKER
//
// El enunciado pide aplicarlo «entre P2 y P3: el único punto donde ya se sabe
// cuántos ítems hay y todavía no se ha gastado ni una llamada de P3». Ese punto es
// la ENTRADA de `P3.Run`, antes de que `fanOut` pida siquiera el provider — y no
// `pipeline.cadena`, por dos razones que no son de gusto:
//
//  1. **Quien gasta la plaza es P3.** Una guarda puesta en el worker deja el techo
//     fuera del código que hace las llamadas: el día que P3 tenga un segundo
//     llamante (un re-run manual desde la bandeja, un backfill) ese llamante nace
//     SIN tope y nadie se entera. La guarda va donde está el gasto.
//  2. **La MARCA de los sobrantes vive en el artefacto de P3.** El worker no
//     construye `ArtefactoP3`; para marcar desde allí habría que mutar el
//     artefacto DESPUÉS del fan-out y volver a persistirlo — que es exactamente la
//     mutación que los tests de esta tarea ponen en rojo.
//
// # 🔴 LO QUE PASA CON LOS SOBRANTES **NO** ES DESCARTARLOS
//
// El diseño de esta ola es CONSERVADOR y lo dice con todas las letras: «una salida
// malformada NUNCA descarta la solicitud». Perder en silencio los ítems 11 y 12 de
// un pedido sería literalmente lo que este plan existe para evitar — pedido
// perdido, y encima sin rastro.
//
// Así que el pedido se procesa **hasta el tope** y el resto queda **AISLADO CON
// MARCA** (`MotivoTope`) en el mismo sitio y con el mismo mecanismo que los ítems
// que el modelo no supo especificar: una entrada en `ArtefactoP3.Isolated` con su
// `IdeaPos`, que apunta a `artifacts.p2.wants[IdeaPos]` — la petición del cliente,
// ya persistida y cifrada. **Nada del cliente se pierde y nada se duplica.** El
// job llega a `done`, no a `failed`: no ha fallado nada, se ha atendido de menos.
//
// # 🔴 POR QUÉ **NO** SE ESCRIBE UN AVISO EN `owner_degradation_notices`, AUNQUE T2.6 LO PIDA
//
// El enunciado de T2.6 añade «y se **notifica la degradación** por el punto de
// inyección de T1.5-4 (REQ-38)». **No se hace, y es una decisión, no un olvido.**
// El ADR-0046 —que es quien manda— NO lo pide: su §Mecanismo 1 y su §Puntos
// abiertos cierran la política con «el exceso queda **marcado y visible**», y nada
// más. Y al bajar a ese canal, los TRES lados no encajan:
//
//  1. **El vocabulario de motivos es CERRADO y todos nombran un FALLO DE LA VÍA**
//     (`ollama_down`, `breaker_open`, `edge_offline`, `timeout`, `api_error`,
//     `credencial`, `lease_invalid`, `edge_sin_capacidad`). Aquí **no ha fallado
//     nada**: el modelo responde, el Edge responde, la credencial vale. El
//     docstring de `degradation.Notifier.Record` dice para qué existe su guarda —
//     «si aquí entrara un motivo SANO […] la tabla dejaría de significar “el LLM
//     se cayó” y el dueño dejaría de leerla»—, y ésta es esa clase de motivo.
//  2. **`Record` exige una `via` (local|api)** y el tope no tiene vía: es una
//     constante del Cloud que aplica a las dos. No hay valor honesto que pasar.
//  3. **El dedupe colapsa por (tenant, motivo, vía, ventana de 15 min) y `Notice`
//     NO tiene campo donde quepa un job** —a propósito, INV-6—. Dos pedidos
//     truncados en la misma ventana producirían UN aviso que dice «algo se truncó»
//     y no dice CUÁL. Un aviso sobre el que el dueño no puede actuar es la misma
//     avería que el paquete existe para evitar, por el otro lado.
//
// Lo que SÍ hay, y es lo que el dueño necesita: la marca en el artefacto (que la
// bandeja de la Ola 3/O4 lee y sabe atribuir a SU pedido) y una línea `warn` con
// las cuentas. Si un día se quiere además una notificación empujada, el sitio
// natural es el aviso del BORRADOR —que sí tiene `intake_id`—, no una novena
// entrada en un enum de fallos de infraestructura.
// ════════════════════════════════════════════════════════════════════════════

// MaxItemsPorPedido es el techo del fan-out de P3: cuántos ítems de un pedido se
// especifican llamando al modelo. El resto NO se descarta — se marca (MotivoTope).
//
// Es **10** por decisión D5 de Jhoan (2026-08-24, D-044.39), publicada en el
// ADR-0046 §Mecanismo 1. 🔴 **10 ítems NO caben en la métrica reina de «< 5 min»**
// (son 320–410 s): el detalle de las dos cuentas, en el bloque de cabecera de este
// fichero. Cambiar este número cambia el peor caso de ocupación de la plaza única,
// así que se cambia con una medición delante, no con una intuición.
const MaxItemsPorPedido = 10

// acotarAlTope parte la lista de ideas que dejó P2 en las que se ATIENDEN y las que
// SOBRAN. Es todo el mecanismo: un `min` y un corte de slice.
//
// 🔴 SE LLAMA ANTES DEL FAN-OUT, Y ESE ES EL PUNTO. Aplicarlo después —especificar
// los 12 y quedarse con 10— daría el mismo artefacto y habría gastado las 12
// llamadas: exactamente los 22–32 s × 2 de plaza ajena que esta tarea existe para
// no gastar. Por eso el test cuenta LAS LLAMADAS del provider y no solo las líneas
// del resultado.
//
// Devuelve `sobrantes` como CUENTA y no como slice: lo único que se necesita de
// esas ideas es cuántas son y en qué posición estaban, porque su texto ya vive
// —cifrado y sin duplicar— en el artefacto de P2. Devolver el slice invitaría a
// copiar el literal del cliente en la marca, que es justo lo que `ItemAislado`
// evita por diseño.
//
// # ⚠️ EL `if` DE ARRIBA ES UN ATAJO, NO LA FRONTERA — Y ESO ENGAÑA AL PROBARLO
//
// La frontera vive en la COTA DEL SLICE (`ideas[:MaxItemsPorPedido]`), no en el
// `<=`. Con `len(ideas) == MaxItemsPorPedido` los dos caminos dicen exactamente lo
// mismo —la lista entera y cero sobrantes—, así que cambiar el `<=` por un `<` es
// un **NO-OP**: se ejecutó como mutación y salió VERDE, con razón. Lo que sí rompe
// de verdad es tocar la cota (`[:Max-1]`) o ensanchar el atajo (`<= Max+1`), y esa
// segunda **estuvo VERDE** hasta que se escribió el test de los ONCE ítems: con 10
// no se llega a la cota y con 12 sobra margen. De ahí la regla que quedó escrita en
// el banco: un corte se prueba en sus dos lados **y en el primero del otro lado**.
func acotarAlTope(ideas []llm.Want) (atendidas []llm.Want, sobrantes int) {
	if len(ideas) <= MaxItemsPorPedido {
		return ideas, 0
	}
	return ideas[:MaxItemsPorPedido], len(ideas) - MaxItemsPorPedido
}

// marcarSobreTope anota en el artefacto las ideas que quedaron por encima del tope
// y deja la línea de log. `desde` es la posición de la primera sobrante (que es
// cuántas se atendieron) y `n`, cuántas son.
//
// Las marcas se añaden DESPUÉS de las del fan-out para que `Isolated` quede en
// orden ascendente de `IdeaPos` — es presentación, no política: la política es el
// corte de `acotarAlTope`, y quien la vigila es el contador de llamadas.
//
// 🔴 EL AVISO NO LLEVA UNA PALABRA DEL CLIENTE: posiciones y cuentas. El «qué
// pidió» se lee del artefacto de P2, que es donde está cifrado (INV-6, ADR-0034).
func (s *P3) marcarSobreTope(art *ArtefactoP3, desde, n int, jobID string) {
	if n <= 0 {
		return
	}
	for i := desde; i < desde+n; i++ {
		art.Isolated = append(art.Isolated, ItemAislado{IdeaPos: i, Reason: MotivoTope})
	}
	s.log.Warn("p3: el pedido supera el tope de ítems; se especifican los primeros y el RESTO QUEDA MARCADO, no descartado",
		"job_id", jobID, "stage", intake.StageP3,
		"tope", MaxItemsPorPedido,
		"ideas", desde+n, "atendidas", desde, "sobre_tope", n,
		"reason", MotivoTope,
		"que_hacer", "el dueño ve los ítems marcados en la bandeja del borrador y decide: atenderlos a mano o pedirle al cliente que parta el pedido")
}
