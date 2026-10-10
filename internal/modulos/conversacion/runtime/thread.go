// Porta internal/flujos/runtime/thread.go @ e0159171

package runtime

// thread.go no exporta nada, ni en el viejo ni aquí: su contrato en rojo es este comentario.
// Las funciones no exportadas nacen en el verde (F8-04b) y los tests de la ola siguiente
// prueban estas promesas por HandleIncoming, leyendo el hilo del doble de eventos.
//
// # Qué hará este fichero
//
// Los productores de filas de TEXTO LITERAL del hilo del evento (Plan 043 · T4.5.7b,
// D-043.23; Plan 044 · T1.6, D-044.24): lo que el cliente escribió, lo que el negocio
// contestó y lo que la plataforma dijo sin que nadie preguntara.
//
// Piezas (nombres del viejo, ya en inglés): featureThreadMessages
// (= entitlements.FeatureLLMIntake), threadAllowed, persistTurnMessages, persistOpeningTurn
// y persistOutOfTurnMessage.
//
// # El gate (RT-20): uno solo, por tenant, fail-closed
//
//   - TH-1 · Se escribe si y solo si se cumplen las CUATRO: hay plano de eventos
//     (WithEventStore), el turno tiene evento (id no vacío), hay resolver de features
//     (WithEntitlements) y el tenant tiene `llm_intake`. Si falta cualquiera: CERO filas.
//   - TH-2 · Si el resolver falla: cero filas y WARN «runtime: no se pudo resolver la feature
//     llm_intake; no se escribe en el hilo». Un fallo transitorio no abre una capacidad de
//     pago.
//   - TH-3 · «Una sola condición» incluye a `api_llm`: un tenant con `llm_intake` y sin vía
//     API archiva su hilo exactamente igual.
//   - TH-4 · El turno y el saliente fuera de turno comparten gate: se persisten, o no, en
//     bloque. El gate se resuelve UNA vez por llamada, no por cadena.
//
// # El turno (EventStore.AppendMessage)
//
//   - TH-5 · Orden de la conversación: primero el texto del cliente (events.RoleClient),
//     después cada salida con texto (events.RoleBusiness), en su orden.
//   - TH-6 · Un texto vacío no deja fila: ni el del cliente (un adjunto sin caption) ni el de
//     una salida que solo lleva adjunto.
//   - TH-7 · Se persiste lo que el motor PRODUJO, después del Save y antes del envío, aunque
//     el envío falle o el limitador lo calle: el hilo cuenta el turno del motor, no la
//     entrega.
//   - TH-8 · El evento del turno es el que estaba vivo MIENTRAS se produjo: en el turno que
//     termina el flujo se captura ANTES del cierre natural, así que ese turno sí queda en el
//     hilo del evento que acaba de cerrarse.
//   - TH-9 · Un turno CORTADO por el sink durable (RT-10) no deja ni una fila.
//   - TH-10 · Best-effort: un fallo al escribir se loguea a WARN («runtime: no se pudo
//     escribir el mensaje del cliente en el hilo; el turno sigue» / «…la respuesta del negocio
//     en el hilo; el turno sigue») y el turno sigue. El hilo jamás tumba la conversación. Un
//     fallo en una fila no impide intentar las siguientes.
//
// # El turno de APERTURA (Plan 044 · T1.4)
//
//   - TH-11 · El mensaje que ABRE un evento por el camino del disparador deja su literal en
//     el hilo, seguido de las salidas del arranque (coletilla incluida).
//   - TH-12 · Solo si el arranque nació de un entrante del cliente: Start (API), el arranque
//     plano (keyword o fallback, que además va sin evento) y la re-entrada de una conmuta no
//     escriben turno de apertura.
//   - TH-13 · La CONMUTA hacia un evento que ya estaba vivo, cuando la trae el disparador,
//     escribe el literal del cliente y NO las salidas: el hilo de ese evento ya tiene su nodo
//     inicial y repetirlo lo duplicaría en silencio.
//   - TH-14 · Por un mismo entrante nunca se escribe dos veces el literal: o corre el camino
//     del disparo o el del avance, jamás los dos.
//
// 🔴 La invariante que TH-9 y TH-11 a TH-13 sostienen, en una línea: TODO MENSAJE QUE ENTRA
// EN LA VENTANA DE CAPTACIÓN (source_refs) TIENE SU LITERAL EN EL HILO. Una referencia sin
// literal produce un texto fuente incompleto sin dar error.
//
// # El saliente FUERA DE TURNO (EventStore.AppendOutOfTurnMessage)
//
//   - TH-15 · Lo que la plataforma manda sin que nazca de un turno entra MARCADO, para que
//     quien lea el hilo lo trate como contexto y no como pedido. Las cadenas vacías se saltan.
//   - TH-16 · Los cinco emisores cableados: el resumen del rescate (solo si salió), la
//     confirmación de un event_stop, el aviso de escape (solo con evento conocido), el
//     reinicio por reanudación (aviso + pantalla inicial, y su aviso de avería), y el
//     recordatorio de la seña (RT-19).
//   - TH-17 · RT-19 · El recordatorio de la seña se escribe con el candado de la conversación
//     YA suelto y después de la fila del turno. Solo si el turno avanzó sobre un evento que
//     seguía activo: sin conversación viva, o si el reloj la soltó en ese turno, no hay evento
//     y no se escribe.
//   - TH-18 · Best-effort: WARN «runtime: no se pudo escribir el saliente fuera de turno en el
//     hilo; el envío ya salió».
//
// # Lo que NO entra en el hilo, a propósito
//
// La bienvenida (welcome.go: ni rotulada), la oferta de tipos y el menú del despachador (no
// pertenecen a UN evento), el aviso de avería de un turno cortado en el avance o en el
// arranque, y todo lo que se envía fuera de este paquete: las notificaciones de estado del
// pedido (deuda portada: el dominio de solicitudes no conoce el evento) y los envíos humanos.
// Las filas `decision` del hilo son de otra puerta (el PersistSink) y no las gobierna este
// gate.
