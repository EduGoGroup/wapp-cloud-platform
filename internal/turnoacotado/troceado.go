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
//  1. EL TOPE (MaxLlamadasPorTurno) dice CUÁNTAS llamadas se admiten. Es el freno de
//     diseño, y responde a «¿cuánto trabajo cabe en un turno?».
//  2. EL PRESUPUESTO (PresupuestoTroceado) dice CUÁNTO TIEMPO tienen entre todas. Es
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
// mediana medida (SueloPorLlamada), el bucle para en vez de empezar una inferencia que
// el reloj va a cortar. Empezarla ocuparía la plaza del Edge —y haría esperar al lote
// de detrás— para tirar el resultado.
package turnoacotado

import (
	"context"
	"errors"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/llmvia"
)

// MaxLlamadasPorTurno es el tope de inferencias que un turno interactivo puede gastar
// troceando. Ver la aritmética completa en la cabecera: 3 es el mayor número cuyo
// PEOR caso medido sigue cabiendo dentro del turno de WhatsApp.
const MaxLlamadasPorTurno = 3

// PresupuestoTroceado es el tiempo total que el troceado puede consumir del turno.
//
// 20 de los 30 s de `Flow.IncomingTimeout`, y los 10 restantes no son un redondeo:
// son lo que queda para cargar el flujo, parsear el catálogo, pintar la pantalla,
// persistir el estado y despachar el saliente. En el camino normal eso es
// sub-segundo, así que 10 s es holgura deliberada — el turno tiene que sobrevivir a
// una base lenta DESPUÉS de haber gastado el presupuesto entero aquí.
const PresupuestoTroceado = 20 * time.Second

// SueloPorLlamada es lo mínimo que tiene que quedar de presupuesto para arrancar una
// llamada más. Es la MEDIANA medida en la peor máquina de la flota (4.588 ms,
// redondeada arriba): por debajo de eso la llamada es más probable que muera a que
// conteste, y una llamada que muere igualmente ocupó la plaza única mientras vivía.
const SueloPorLlamada = 5 * time.Second

// resolverTrozos hace UNA llamada por trozo, en orden, hasta que se acaba el tope o
// el presupuesto. Devuelve SIEMPRE un veredicto alineado por posición con c.Trozos:
// los que no se llegaron a preguntar quedan en cadena vacía, que es exactamente lo
// que el módulo espera leer.
func (r *Resolver) resolverTrozos(ctx context.Context, tenantID, sessionID string, c modules.Consulta) (modules.Veredicto, error) {
	ctx, cancel := context.WithTimeout(ctx, PresupuestoTroceado)
	defer cancel()

	codigos := make([]string, len(c.Trozos))
	for i, trozo := range c.Trozos {
		if i >= MaxLlamadasPorTurno || !cabeUnaMas(ctx) {
			break
		}
		// La sub-consulta es una consulta de ELECCIÓN normal y corriente: mismas
		// opciones, mismo nivel, y como Texto el trozo en vez del turno entero. Que sea
		// el MISMO tipo no es comodidad: hace que prompt() y veredicto() —las dos
		// funciones medidas de T3.5-2— se reusen verbatim, sin una rama «cuando viene de
		// un troceado» que sería el sitio por donde las dos formas empezarían a divergir.
		sub := modules.Consulta{Clase: modules.ClaseOpcion, Nivel: c.Nivel, Texto: trozo, Opciones: c.Opciones}
		texto, esquema := prompt(sub)
		raw, err := r.turnero.Turno(ctx, tenantID, sessionID, llmvia.TurnoRequest{Prompt: texto, Formato: esquema})
		if err != nil {
			return paradaPorError(err, codigos)
		}
		codigos[i] = veredicto(sub, raw).Codigo
	}
	return modules.Veredicto{Codigos: codigos}, nil
}

// cabeUnaMas informa si queda presupuesto para arrancar otra llamada. Sin deadline
// —que aquí no puede pasar, porque el propio resolverTrozos acaba de poner uno— se
// responde que sí: la ausencia de reloj no puede ser el motivo de no preguntar.
func cabeUnaMas(ctx context.Context) bool {
	dl, ok := ctx.Deadline()
	return !ok || time.Until(dl) >= SueloPorLlamada
}

// paradaPorError decide qué hacer cuando la vía falla a mitad del troceado.
//
// La regla es la de la cabecera: lo ya resuelto NO se tira. Si hay al menos un código,
// se devuelve el veredicto PARCIAL con Motivo=fallo y sin error, para que el engine lo
// aplique en vez de descartarlo. Si no hay ninguno, el troceado se comporta EXACTAMENTE
// como el turno de un solo texto —error hacia arriba, DesenlaceFallo, aviso al dueño—
// porque entonces no hay nada que salvar y sí una avería que contar.
//
// ⚠️ El aviso al dueño del ADR-0044 §5 no se pierde en el caso parcial: lo escribe el
// decorador que envuelve al selector (llmvia/notify.go) en el momento del fallo, no
// este return. Aquí solo se decide qué ve el módulo.
func paradaPorError(err error, codigos []string) (modules.Veredicto, error) {
	if errors.Is(err, llmvia.ErrViaSinTurnoAcotado) {
		// Tenant en vía API: para él este escalón no existe y no es una avería de nadie
		// (mismo trato que en ResolverConsulta). Se corta en la primera llamada, así que
		// aquí nunca hay nada parcial que conservar.
		return modules.Veredicto{Motivo: modules.MotivoSinResolutor}, nil
	}
	v := modules.Veredicto{Codigos: codigos, Motivo: modules.MotivoFallo}
	if !v.ResueltoAlguno() {
		return modules.Veredicto{}, err
	}
	return v, nil
}
