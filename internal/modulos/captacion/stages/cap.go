// Porta internal/intake/stages/tope.go @ 4cd9cfb

package stages

import (
	"github.com/EduGoGroup/wapp-shared/llm"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
)

// cap.go — EL TOPE DE ÍTEMS POR PEDIDO (Plan 044 · T2.6 · ADR-0046 § Mecanismo 1).
//
// Sin techo, un pedido de N ítems son N llamadas de lote sobre la PLAZA ÚNICA del Edge
// (22–32 s medidos cada una): un mensaje con 40 ítems ocupaba el negocio entero veinte
// minutos. Lo que lo acota es **un entero**, no una cola con prioridades ni un
// planificador (ADR-0046 §Alternativas).
//
// # DÓNDE SE APLICA: A LA ENTRADA DE P3.Run, ANTES DEL FAN-OUT
//
// Es el único punto donde ya se sabe cuántos ítems hay y todavía no se ha gastado ni
// una llamada. Aplicarlo después —especificar los 12 y quedarse con 10— daría el mismo
// artefacto y habría gastado las 12 llamadas: por eso lo que se cuenta son LAS LLAMADAS
// al modelo, no solo las líneas del resultado.
//
// # 🔴 LOS SOBRANTES NO SE DESCARTAN: SE MARCAN
//
// El pedido se procesa hasta el tope y el resto queda AISLADO CON MARCA
// (ReasonOverLimit) en `P3Artifact.Isolated`, con su `IdeaPos` y sin una palabra del
// cliente. El job NO falla: no ha fallado nada, se ha atendido de menos. Las cuentas
// —y el aviso— las promete P3.Run.
//
// # 🔴 NO SE ESCRIBE UN AVISO DE DEGRADACIÓN
//
// El vocabulario de `owner_degradation_notices` nombra FALLOS DE LA VÍA y aquí no ha
// fallado nada. Lo que hay es la marca en el artefacto y una línea `Warn` con las
// cuentas.

// MaxItemsPerOrder (antes `MaxItemsPorPedido`) es el techo del fan-out de P3: cuántos
// ítems de un pedido se especifican llamando al modelo. Los que pasen de ahí NO se
// preguntan y NO se descartan: se marcan con ReasonOverLimit.
//
// Es **10** por decisión D5 de Jhoan (2026-08-24, D-044.39), publicada en el ADR-0046
// §Mecanismo 1: un solo número para todos los tenants, constante y no perilla. 🔴 10
// ítems NO caben en la métrica de «< 5 min» (son 320–410 s por la vía local): el tope
// es lo que el pipeline PROCESA, no lo que promete en cinco minutos. Cambiarlo cambia
// el peor caso de ocupación de la plaza única: se cambia con una medición delante.
//
// La frontera es exacta: con 10 ideas no se recorta ni se marca nada; con 11, la
// undécima es la primera que sobra; se atienden siempre las PRIMERAS, en su orden.
const MaxItemsPerOrder = 10

// capIdeas (antes `acotarAlTope`) parte la lista de ideas que dejó P2 en las que se
// ATIENDEN y las que SOBRAN. Es todo el mecanismo: un `min` y un corte de slice.
//
// 🔴 SE LLAMA ANTES DEL FAN-OUT, Y ESE ES EL PUNTO. Aplicarlo después —especificar los
// 12 y quedarse con 10— daría el mismo artefacto y habría gastado las 12 llamadas:
// exactamente los 22–32 s × 2 de plaza ajena que el tope existe para no gastar.
//
// Devuelve `leftOver` como CUENTA y no como slice: lo único que se necesita de esas
// ideas es cuántas son y en qué posición estaban, porque su texto ya vive —cifrado y
// sin duplicar— en el artefacto de P2. Devolver el slice invitaría a copiar el literal
// del cliente en la marca, que es justo lo que `IsolatedItem` evita por diseño.
//
// # ⚠️ EL `if` DE ARRIBA ES UN ATAJO, NO LA FRONTERA — Y ESO ENGAÑA AL PROBARLO
//
// La frontera vive en la COTA DEL SLICE (`ideas[:MaxItemsPerOrder]`), no en el `<=`.
// Con `len(ideas) == MaxItemsPerOrder` los dos caminos dicen exactamente lo mismo —la
// lista entera y cero sobrantes—, así que cambiar el `<=` por un `<` es un **NO-OP**:
// se ejecutó como mutación y salió VERDE, con razón. Lo que sí rompe de verdad es tocar
// la cota (`[:Max-1]`) o ensanchar el atajo (`<= Max+1`), y esa segunda **estuvo
// VERDE** hasta que se escribió el test de los ONCE ítems: con 10 no se llega a la cota
// y con 12 sobra margen. De ahí la regla: un corte se prueba en sus dos lados **y en el
// primero del otro lado**.
func capIdeas(ideas []llm.Want) (served []llm.Want, leftOver int) {
	if len(ideas) <= MaxItemsPerOrder {
		return ideas, 0
	}
	return ideas[:MaxItemsPerOrder], len(ideas) - MaxItemsPerOrder
}

// markOverLimit (antes `marcarSobreTope`) anota en el artefacto las ideas que quedaron
// por encima del tope y deja la línea de log. `from` es la posición de la primera
// sobrante (que es cuántas se atendieron) y `n`, cuántas son.
//
// Las marcas se añaden DESPUÉS de las del fan-out para que `Isolated` quede en orden
// ascendente de `IdeaPos` —es presentación, no política: la política es el corte de
// `capIdeas`, y quien la vigila es el contador de llamadas—.
//
// 🔴 EL AVISO NO LLEVA UNA PALABRA DEL CLIENTE: posiciones y cuentas. El «qué pidió» se
// lee del artefacto de P2, que es donde está cifrado (INV-6, ADR-0034).
func (s *P3) markOverLimit(art *P3Artifact, from, n int, jobID string) {
	if n <= 0 {
		return
	}
	for i := from; i < from+n; i++ {
		art.Isolated = append(art.Isolated, IsolatedItem{IdeaPos: i, Reason: ReasonOverLimit})
	}
	s.log.Warn("p3: el pedido supera el tope de ítems; se especifican los primeros y el RESTO QUEDA MARCADO, no descartado",
		"job_id", jobID, "stage", intake.StageP3,
		"tope", MaxItemsPerOrder,
		"ideas", from+n, "atendidas", from, "sobre_tope", n,
		"reason", ReasonOverLimit,
		"que_hacer", "el dueño ve los ítems marcados en la bandeja del borrador y decide: atenderlos a mano o pedirle al cliente que parta el pedido")
}
