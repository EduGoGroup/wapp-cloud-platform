// Porta internal/turnoacotado/troceado.go @ 42117b5

// troceado.go — UNA LLAMADA CHICA POR TROZO, Y UN TOPE QUE PROTEGE EL TURNO
// (Plan 044 · Ola 3.5 · T3.5-3).
//
// El módulo descompone y recompone (cart/troceo.go); aquí solo se PREGUNTA, una vez
// por trozo, con el MISMO prompt de clase opción que T3.5-2 midió a 11/12 — no hay
// un prompt nuevo, ni un esquema nuevo, ni un segundo pipeline (C2 del ADR-0044).
// Un trozo es una pregunta de elección exactamente igual que la del turno de un solo
// texto; lo único que este fichero añade es el bucle y sus dos frenos.
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 LOS DOS FRENOS SON DISTINTOS Y HACEN FALTA LOS DOS
// ════════════════════════════════════════════════════════════════════════════
//
//  1. EL TOPE (MaxCallsPerTurn) dice CUÁNTAS llamadas se admiten. Es el freno de
//     diseño, y responde a «¿cuánto trabajo cabe en un turno?».
//  2. EL PRESUPUESTO (ChunkingBudget) dice CUÁNTO TIEMPO tienen entre todas. Es
//     el freno de seguridad, y responde a «¿qué pasa si cada llamada tarda lo peor?».
//
// Sin el segundo, el primero no protege nada, y la aritmética lo enseña sin lugar a
// dudas: `Selector.Turno` espera hasta `PlazoTurno + MargenVeredicto` = 12 + 7 = **19 s
// POR LLAMADA**, así que DOS llamadas en su peor caso absoluto son 38 s y el turno
// entero de WhatsApp dura **30 s** (`Flow.IncomingTimeout`, flujos/runtime/
// runtime_engine.go:37). Pasado ese plazo el runtime mata el entrante y la clienta
// **no recibe nada**: ni las líneas que ya se habían resuelto, ni una pantalla, nada.
// Un tope solo, sin reloj, deja esa puerta abierta.
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 POR QUÉ EL TOPE ES 3, CON LOS SEGUNDOS DELANTE
// ════════════════════════════════════════════════════════════════════════════
//
//	MEDIDO (2026-08-26, qwen3:1.7b, 18–20 tokens de salida, prefijo CALIENTE — la
//	misma medición que fijó PlazoTurno, llmvia/llmvia.go):
//	  VPS (CPU, ~6 tok/s):  mediana 4.588 ms, máximo 7.932 ms
//	  Local (GPU):          mediana   502 ms, máximo   760 ms
//
//	3 × mediana(4,6 s) = 13,8 s  ⇒ el caso NORMAL en la peor máquina de la flota cabe
//	                               dentro del presupuesto con holgura.
//	3 × máximo (7,9 s) = 23,7 s  ⇒ el peor caso caliente NO cabe: el presupuesto corta
//	                               el último trozo y SE CONSERVA lo ya resuelto. Esa es
//	                               una degradación buena, y está diseñada.
//	4 × máximo (7,9 s) = 31,7 s  ⇒ por encima de los 30 s del turno ENTERO. Con cuatro,
//	                               el peor caso deja de ser «pierdo un trozo» y pasa a
//	                               ser «pierdo el turno». **Ahí está el corte, y por eso
//	                               el número es 3 y no 4.**
//
// 🔴 Y EL TOPE ES DE **LLAMADAS**, NO DE TROZOS, que es la diferencia que hace que los
// dos fixtures del criterio pasen ENTEROS. Trocear es gratis (separadores + cascada,
// microsegundos, sin red): un turno con cuatro productos cuyas etiquetas casan por
// cascada no gasta ni una llamada y entra completo, los cuatro. Lo escaso no es el
// número de productos que alguien pide: es la PLAZA ÚNICA del Ollama del cliente
// (ADR-0038 Enmienda 1 §d, K=1), que además está compartida con el pipeline de lote
// esperando detrás. Poner el tope sobre los trozos habría castigado a quien escribe
// claro para proteger un recurso que ese cliente no estaba gastando.
//
// ════════════════════════════════════════════════════════════════════════════
// QUÉ PASA CUANDO EL PRESUPUESTO SE AGOTA A MITAD
// ════════════════════════════════════════════════════════════════════════════
//
// **No se pierde lo ya resuelto.** El bucle sale, devuelve los códigos que tenga y
// deja vacíos los demás; el módulo agrega las líneas que sí se identificaron y le dice
// a la clienta cuántas no (cart/troceo.go). Lo que NO se hace es abortar el veredicto
// entero: eso tiraría llamadas que ya se pagaron con la plaza única y dejaría a la
// persona con la pantalla de «no te entendí» habiendo entendido tres cuartas partes.
//
// Y no se ARRANCA una llamada que no quepa: si el presupuesto restante no cubre ni la
// mediana medida (FloorPerCall), el bucle para en vez de empezar una inferencia que
// el reloj va a cortar. Empezarla ocuparía la plaza del Edge —y haría esperar al lote
// de detrás— para tirar el resultado.
//
// ════════════════════════════════════════════════════════════════════════════
// LO QUE ESTE FICHERO PROMETE (se observa por Resolver.ResolveQuery con Chunks)
// ════════════════════════════════════════════════════════════════════════════
//
//   - UNA llamada a Turno por trozo, en el orden de q.Chunks, con el tenant y la
//     sesión recibidos. Cada una lleva el prompt y el esquema de la clase OPCIÓN
//     —sea cual sea la clase de la consulta—, las Options de la consulta y como
//     respuesta del cliente SU trozo y ningún otro (nunca q.Text entero).
//   - Como mucho MaxCallsPerTurn llamadas: los trozos de más no se preguntan.
//   - El ctx de cada llamada vence a ChunkingBudget de haber empezado el troceado, o
//     antes si el del llamante es más corto. Antes de CADA llamada se mira lo que
//     queda: con menos de FloorPerCall no se arranca (con FloorPerCall justo, sí) y
//     el bucle termina ahí.
//   - Resultado sin fallo de la vía: (Verdict{Codes}, nil), con len(Codes) ==
//     len(q.Chunks), Code y Reason vacíos; Codes[i] es el código de la opción que el
//     modelo eligió para el trozo i, o "" si no se preguntó o no fue concluyente.
//   - Turno falla con un error que casa con llmvia.ErrViaSinTurnoAcotado ⇒
//     (Verdict{Reason: QueryReasonNoResolver}, nil), SIN Codes, hubiera o no trozos
//     ya resueltos.
//   - Turno falla con otro error: si algún trozo anterior quedó resuelto ⇒
//     (Verdict{Codes: los parciales, Reason: QueryReasonFailure}, nil); si ninguno ⇒
//     (Verdict{}, ese error INTACTO). En los dos casos no se hacen más llamadas.

package turnoacotado

import "time"

// MaxCallsPerTurn (`MaxLlamadasPorTurno` en el viejo) es el tope de inferencias que
// un turno interactivo puede gastar troceando. Ver la aritmética completa en la
// cabecera: 3 es el mayor número cuyo PEOR caso medido sigue cabiendo dentro del
// turno de WhatsApp.
const MaxCallsPerTurn = 3

// ChunkingBudget (`PresupuestoTroceado` en el viejo) es el tiempo total que el
// troceado puede consumir del turno.
//
// 20 de los 30 s de `Flow.IncomingTimeout`, y los 10 restantes no son un redondeo:
// son lo que queda para cargar el flujo, parsear el catálogo, pintar la pantalla,
// persistir el estado y despachar el saliente. En el camino normal eso es
// sub-segundo, así que 10 s es holgura deliberada — el turno tiene que sobrevivir a
// una base lenta DESPUÉS de haber gastado el presupuesto entero aquí.
const ChunkingBudget = 20 * time.Second

// FloorPerCall (`SueloPorLlamada` en el viejo) es lo mínimo que tiene que quedar de
// presupuesto para arrancar una llamada más. Es la MEDIANA medida en la peor máquina
// de la flota (4.588 ms, redondeada arriba): por debajo de eso la llamada es más
// probable que muera a que conteste, y una llamada que muere igualmente ocupó la
// plaza única mientras vivía.
const FloorPerCall = 5 * time.Second
