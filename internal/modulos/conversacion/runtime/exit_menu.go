// Porta internal/flujos/runtime/exit_menu.go @ e0159171

package runtime

// exit_menu.go no exporta nada, ni en el viejo ni aquí: su contrato en rojo es este
// comentario. Las funciones no exportadas nacen en el verde (F8-04b) y los tests de la ola
// siguiente prueban estas promesas por HandleIncoming.
//
// # Qué hará este fichero
//
// Interpreta la respuesta del cliente al MENÚ DE SALIDA del reprompt acotado (Plan 043 ·
// T5.2, D-043.10). El menú lo ARMA un módulo (modules.ArmExitMenu) cuando el cliente agota
// sus intentos dentro de un evento; aquí solo se lee la respuesta. Todo numérico: cero LLM.
//
// Piezas (nombres del viejo, ya en inglés): exitMenuChoice, disarmExitMenu y resendScreen.
//
// # Dónde corre
//
// En el avance de una conversación viva, DESPUÉS del escape global y del salto por tipo (una
// palabra de navegación dicha ante el menú de salida sigue siendo navegación) y ANTES de la
// idempotencia consecutiva y del paso del engine (el número que el cliente teclea ahí no es
// una respuesta para el módulo).
//
// # Promesas
//
//   - XM-1 · Sin plano de eventos, sin Vars o sin la marca modules.ExitMenuVar: no hace
//     nada, no escribe nada y el turno sigue.
//   - XM-2 · La marca solo se interpreta DENTRO del evento sobre el que se armó: el evento
//     activo tiene que existir y coincidir con modules.ExitMenuArmedOn. Sin evento activo, o
//     con otro, se DESARMA (se borra la marca y se guarda el estado) y el turno sigue hacia
//     el módulo: un número que ya no es de ese evento no puede desactivar al que ahora manda.
//   - XM-3 · 🔴 «Sin evento activo» va primero y no es redundante: un marcador sin sello
//     (anterior a ExitMenuEventVar) da "" y casaría con una conversación sin evento. Nunca se
//     interpreta 1/2/3 sin evento activo; solo se desarma.
//   - XM-4 · La entrada se compara tras quitarle los espacios de los lados. Si no es una de
//     las tres opciones, se desarma y el turno SIGUE hacia el módulo, que la trata como una
//     entrada más (con su contador desde cero).
//   - XM-5 · En toda rama que actúa, el estado se guarda con la marca YA borrada ANTES de
//     actuar, para que ningún camino posterior resucite un menú consumido. Si ese Save falla:
//     «runtime: desarmar el menú de salida: …» y el turno se corta.
//   - XM-6 · modules.ExitMenuKeepTrying ("1", seguir intentando): re-envía la pantalla que
//     el módulo guardó al armar, tal cual. Cobra UN token (RT-8): agotado, o con la pantalla
//     vacía, el turno se consume sin enviar.
//   - XM-7 · modules.ExitMenuStop (dejarlo): es EXACTAMENTE el event_stop (events.go, EV-5).
//     El evento sigue `open`, se apaga el puntero activo, se emite event_deactivated y se
//     confirma por nombre de tipo.
//   - XM-8 · modules.ExitMenuDispatcher (ver el menú): salta al evento `menu` con el gesto
//     «ve» —no «nuevo»: no puede cerrar un vencido— (events.go, EV-3 y EV-7).
//   - XM-9 · Las tres opciones consumen el turno. Ninguna lleva el número al hilo del evento
//     ni a la ventana de captación: es la respuesta a una lista de la plataforma, no un
//     pedido.
//
// # Costura portada
//
// El marcador debería vivir exactamente un turno. Con evento activo distinto del que lo armó
// se desarma en el primer entrante (XM-2); volver al MISMO evento pasa por la entrada al
// evento, que borra el estado entero antes de que este código llegue a verlo.
