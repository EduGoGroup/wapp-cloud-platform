// Porta internal/flujos/runtime/send.go @ e0159171

package runtime

// send.go no exporta nada, ni en el viejo ni aquí: su contrato en rojo es este comentario.
// Las funciones no exportadas nacen en el verde (F8-04b) y los tests de la ola siguiente
// prueban estas promesas por Start y por HandleIncoming, leyendo el doble del Sender.
//
// # Qué hará este fichero
//
// El envío: el embudo por el que sale toda auto-respuesta del motor de flujos.
//
// Piezas (nombres del viejo; los que estaban en español pasan a inglés en el verde, E-11):
// send, emit, sendSystemText, countAutoreplyStreak, sendMedia, sendReply y destino (→
// destination).
//
// # Destino
//
//   - SD-1 · El destino se resuelve del contact_id opaco a una cadena direccionable por el
//     Edge (contact.Resolver.Destino y Ref.Sendable): el envío no depende del JID con el que
//     llegó el entrante. Si falla: «runtime: resolver destino: …» o «runtime: destino no
//     direccionable: …».
//
// # Despacho (RT-4)
//
//   - SD-2 · Las salidas se empujan EN ORDEN. Una salida con Media va por Sender.SendMedia;
//     el resto, por Sender.SendText. Se devuelve el Ack de la última que salió.
//   - SD-3 · Ante el primer error se CORTA: no se intentan las siguientes y se devuelve el
//     error con el último Ack logrado. Un fallo de texto se envuelve en «runtime: enviar
//     texto: …».
//   - SD-4 · El estado ya se guardó antes de enviar: un fallo aquí no lo corrompe ni lo
//     revierte. OnIncoming lo loguea; Start lo devuelve.
//   - SD-5 · Media: el runtime PREFIRMA la clave del adjunto (Presigner.GenerateDownloadURL)
//     y pasa la URL, el nombre, el mime, el caption y el kind a SendMedia. El binario no viaja
//     por gRPC. Si la prefirma falla: «runtime: presignar media %q: …»; si falla el envío:
//     «runtime: enviar media %q: …» (las dos con la clave).
//   - SD-6 · 🔴 Sin Presigner cableado, un adjunto es un error CONTROLADO, nunca un pánico:
//     "runtime: nodo media sin PresignClient configurado (usa WithPresignClient)". No se
//     llama al Sender para esa salida.
//
// # La respuesta a un entrante y el limitador (RT-8)
//
//   - SD-7 · Sin salidas no se envía nada y NO se gasta token.
//   - SD-8 · Con salidas se cobra UN token ANTES de resolver el destino. Agotado: no se envía
//     (el estado ya avanzó y quedó guardado), se cuenta `rate_limit` y se loguea a WARN
//     «runtime: auto-respuesta limitada por rate-limit de conversación» con tenant_id,
//     session_id y contact_id y NADA más: ni el texto ni el número.
//   - SD-9 · Un token por EMISIÓN, no por mensaje: un turno que responde con varias salidas
//     cobra uno.
//
// # La racha (RT-11; Plan 049 · Opción A)
//
//   - SD-10 · Cada emisión que de verdad sacó algo suma 1 a la racha de su conversación, con
//     el reloj del runtime (WithClock). Una emisión sin salidas no suma. Una emisión que falla
//     —también a medias— tampoco.
//   - SD-11 · Suma 1 por EMISIÓN, no por mensaje ni por turno: un turno puede hacer varias
//     emisiones (el resumen del rescate y la pantalla del flujo son dos). La racha es una cota
//     superior de los turnos.
//   - SD-12 · Deja rastro a DEBUG «runtime: auto-respuesta emitida (racha del episodio)» con
//     racha, tenant_id, session_id y contact_id. Sin umbral: observa, no corta.
//
// # El texto fijo del sistema (la bienvenida)
//
//   - SD-13 · Sale por el mismo despacho, pero NO suma a la racha y NO cobra token del
//     limitador: no es una auto-respuesta conversacional y no puede entrar en bucle (su
//     emisión está sellada en el almacén de la bienvenida). Es el único saliente del paquete
//     fuera de la métrica, y cualquier otro que se añada tiene que justificarlo por escrito.
//
// # Lo que la racha NO ve (deuda portada)
//
// Las notificaciones de cambio de estado del pedido salen por el dominio de solicitudes, no
// por aquí: son auto-respuestas y no se cuentan. Los envíos humanos (API, consola) quedan
// fuera con razón.
