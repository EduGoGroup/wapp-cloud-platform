// Porta internal/intake/stages/tope.go @ 4cd9cfb

package stages

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
