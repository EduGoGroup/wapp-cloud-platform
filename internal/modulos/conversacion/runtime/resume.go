// Porta internal/flujos/runtime/resume.go @ e0159171

package runtime

import "errors"

// resume.go exporta un solo símbolo, el centinela de abajo. El resto de su contrato en rojo es
// este comentario: las funciones y constantes no exportadas nacen en el verde (F8-04b), y los
// tests de la ola siguiente prueban estas promesas por HandleIncoming y por Start.
//
// # Qué hará este fichero
//
// Dos cosas que el viejo ya tenía juntas: la REANUDACIÓN por módulo (H9) y el FAN-OUT de
// efectos con su reintento acotado (D-054.4).
//
// Piezas (nombres del viejo, ya en inglés): prepareResume, outputTexts, dispatch,
// retryDurableSink, durableRetrySleep, durableRetryAttempts = 2 y
// durableRetryBackoff = 25 * time.Millisecond.
//
// # Reanudación por módulo (Plan 027 · Ola 3 · T8, cierra H9)
//
// Corre en el avance de una conversación viva, justo antes del paso del engine, y consulta la
// política registrada con WithResumePolicy para el TIPO del nodo actual.
//
//   - RS-1 · Sin política para ese tipo, o con el nodo actual fuera de la definición: no-op
//     total. No toca las Vars y el turno sigue.
//   - RS-2 · La política dice «no reiniciar»: se garantiza un mapa de Vars no nil y se le
//     pide la siembra (Seed). El turno sigue hacia el engine con esas Vars. Un error de
//     Restart o de Seed corta el turno: «runtime: política de reanudación: …» /
//     «runtime: siembra de reanudación: …».
//   - RS-3 · La política dice «reiniciar»: se cobra UN token del limitador (RT-8). Agotado,
//     el turno se consume sin reiniciar, sin responder y sin escribir nada.
//   - RS-4 · Con token: los efectos que la política sintetizó van al fan-out (con el evento
//     activo del estado que se reanuda); después se descartan las Vars, se re-entra al flujo
//     con la MISMA versión con la que corría, se estampa last_wa_message_id, se aplica el
//     cierre natural, se GUARDA y se envían, en UNA emisión, el aviso de la política (si lo
//     trae) y la pantalla inicial fresca. El turno queda consumido.
//   - RS-5 · Tras un reinicio consumado, y solo entonces, con evento activo: el literal del
//     cliente entra al hilo como turno (sin salidas), las salidas entran MARCADAS como fuera
//     de turno, y el mensaje se ofrece a la ventana de captación. Si el cierre natural apagó
//     el evento en ese mismo turno, no se escribe ninguna de las tres.
//   - RS-6 · Si el fan-out de RS-4 corta el turno (RT-10): no se reinicia ni se guarda, se
//     envía el aviso de avería por el camino normal (con su token) y ese aviso entra al hilo
//     marcado como fuera de turno, salga o no. ERROR «runtime: reanudación cortada: el sink
//     durable no pudo materializar el efecto sintetizado tras el reintento acotado».
//
// # Fan-out de efectos (ADR-0003: en proceso, sin broker)
//
//   - FO-1 · Cada efecto pasa por cada sink, en el orden de fase que fijó New (RT-18).
//   - FO-2 · BEST-EFFORT por defecto: un sink que falla se loguea a ERROR «runtime: sink de
//     efecto falló» (con kind, name y session_id) y el fan-out SIGUE con los demás sinks y
//     efectos. El turno no se entera.
//   - FO-3 · RT-10, la excepción ACOTADA (Plan 054 · T3, D-054.4). Se sale del best-effort
//     solo si se cumplen las DOS a la vez: el efecto lo produjo un módulo de contenido durable
//     (EffectContext.Durable) y el error del sink lleva ErrMaterializationFailed. Entonces:
//     un error PERMANENTE de la base (postgres.IsPermanentFailure: una violación de
//     integridad no cede reintentando) corta de inmediato, sin gastar reintentos; cualquier
//     otro se reintenta hasta 2 veces más —3 intentos en total sobre el mismo (efecto, sink)—
//     con 25 ms de espera entre ellos, en la misma goroutine del turno.
//   - FO-4 · La espera respeta el contexto: cancelado, el reintento se abandona con el error
//     del contexto y el turno se corta. 🔴 Es un time.After de 25 ms sobre el reloj REAL: no
//     pasa por WithClock. Los tests del reintento no adelantan ningún reloj; esperan a lo sumo
//     50 ms de verdad.
//   - FO-5 · Un reintento que vuelve SIN ErrMaterializationFailed cuenta como éxito aunque
//     traiga otro error (p. ej. el hilo de decisión): se loguea a ERROR «runtime: sink de
//     efecto falló tras reintentar (best-effort, no bloquea el turno)» y el fan-out sigue.
//     Cada reintento que vuelve a fallar: ERROR «runtime: reintento del sink durable falló»,
//     con la clave "intento".
//   - FO-6 · Agotado el cupo, o ante el permanente: se CORTA. No se despachan los efectos ni
//     los sinks que quedaban y el resultado es ErrTurnCutBySinkFailure envolviendo al último
//     error.
//   - FO-7 · Se reintenta el Handle ENTERO del sink: si la fila de flow_events ya se había
//     escrito, el reintento la duplica (no hay unicidad). Compromiso asumido y portado.
//
// # Qué hace quien recibe el corte
//
// Los tres llamantes del fan-out —el arranque (start.go), el avance (incoming.go) y la
// reanudación (aquí)— lo llaman ANTES de su Save: no guardan el estado avanzado, no escriben
// el turno en el hilo, no lo ofrecen a la ventana de captación y contestan con el aviso de
// avería en vez de sus salidas. El flujo nunca alcanza su despedida.

// ErrTurnCutBySinkFailure marca que el fan-out de efectos CORTÓ el turno: el sink que
// materializa contenido durable agotó su reintento acotado, o falló con un error permanente
// (FO-6; D-054.4). Envuelve al último error del sink: errors.Is lo reconoce a él y a la
// causa.
//
// No sale del paquete por ningún método exportado: quien lo recibe corta su turno y devuelve
// el resultado de enviar el aviso de avería (RT-10). Es exportado, como en el viejo, para que
// un test o un candado lo reconozcan con errors.Is.
var ErrTurnCutBySinkFailure = errors.New("runtime: el turno se corta: un sink durable no pudo materializar el efecto")
