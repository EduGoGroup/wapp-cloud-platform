// Porta internal/flujos/runtime/exit_menu.go @ e0159171

package runtime

import (
	"context"
	"fmt"
	"strings"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
)

// exit_menu.go no exporta nada, ni en el viejo ni aquí: su contrato es este
// comentario, y los tests (exit_menu_test.go) prueban estas promesas por HandleIncoming.
//
// # Qué hace este fichero
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

// exitMenuChoice interpreta la respuesta del cliente contra el menú de salida
// ARMADO por un módulo. Devuelve true si el turno se consumió.
//
// El menú de salida solo tiene sentido DENTRO del evento sobre el que se armó (E-3):
// si ese evento se apagó entre el menú y la respuesta —una cancelación desde la app,
// un event_stop, incluido el caso EventID=="" (sin evento activo)— la rama del sello
// (más abajo) lo detecta y desarma, y el «2» vuelve a ser del módulo. La guarda de
// `st.EventID == ""` NO corta arriba del todo —eso dejaba un marcador armado sin
// evento sobreviviendo MÁS de un turno, incumpliendo la promesa de ExitMenuVar— pero
// SÍ vive dentro de la rama del sello, porque sin evento activo el marcador jamás debe
// interpretarse: ver el comentario ⚠️ de esa rama.
//
// Las CUATRO ramas, y por qué las tres primeras persisten antes de actuar: el estado
// se guarda con la marca ya borrada para que ningún camino posterior —stopEvent
// guarda su propia copia, enterEventFlow relee de la BD— resucite un menú consumido.
func (rt *Runtime) exitMenuChoice(ctx context.Context, key store.Key, sessionID string, st model.Conversation, m *cloudlinkv1.IncomingMessage) (bool, error) {
	if rt.events == nil || st.Vars == nil {
		return false, nil
	}
	if _, armed := st.Vars[modules.ExitMenuVar].(string); !armed {
		return false, nil
	}
	// ⚠️ `st.EventID == ""` va PRIMERO y no es redundante con la comparación que
	// sigue (revisión de la Ola 6): un marcador LEGACY —ExitMenuVar escrito antes de
	// que la Ola 5 añadiera ExitMenuEventVar, que es el estado de CUALQUIER flow_state
	// hoy en producción— hace que ExitMenuArmedOn devuelva "", y "" == "" casaba con
	// una conversación sin evento activo. La comparación sola bendecía ese marcador y
	// el runtime SECUESTRABA el turno: «1» re-emitía una pantalla rancia, «2» se
	// tragaba el turno en silencio absoluto (stopEvent con EventID=="" no hace ni dice
	// nada) y «3» abría el despachador que nadie pidió. Es exactamente el lado que el
	// contrato de modules.ExitMenuArmedOn declara inseguro: «al no casar con ningún
	// EventID real, el runtime lo desarma y el texto sigue su camino... el otro sería
	// secuestrar un turno ajeno». Sin evento activo NUNCA se interpreta 1/2/3; solo se
	// desarma, que es lo que E-3 quería arreglar (el marcador no sobrevive al turno).
	if st.EventID == "" || modules.ExitMenuArmedOn(st.Vars) != st.EventID {
		// E-3: el marcador es de OTRO evento —o de NINGUNO (st.EventID==""): la
		// conversación cambió de contexto por un camino que conserva las Vars
		// (saveMenuState, el menú del despachador; stopEvent) entre el momento en
		// que se armó el menú de salida (que solo se arma DENTRO de un evento,
		// modules.InEvent) y esta respuesta. Antes de esta corrección, EventID=="" se
		// cortaba en la guarda de arriba SIN pasar por aquí, así que el marcador
		// sobrevivía MÁS de un turno — rompiendo la promesa literal de ExitMenuVar
		// («vive exactamente un turno»). Deuda latente documentada, no incidente hoy:
		// volver al MISMO evento pasa por enterEventFlow, que borra el flow_state
		// entero antes de que este código llegue a verlo. Se desarma en silencio y el
		// texto sigue su camino: un «2» que ya no es del carrito no puede desactivar
		// el evento que ahora manda (o ninguno). Es la misma norma que menuChoice se
		// aplica a sí mismo (events.go).
		if _, err := rt.disarmExitMenu(ctx, st); err != nil {
			return false, err
		}
		return false, nil
	}
	input := strings.TrimSpace(m.GetText())
	if input != modules.ExitMenuKeepTrying && input != modules.ExitMenuStop && input != modules.ExitMenuDispatcher {
		// No era una elección: se DESARMA y el texto sigue su camino hacia el módulo,
		// que lo tratará como una entrada más del nodo (contador desde cero).
		if _, err := rt.disarmExitMenu(ctx, st); err != nil {
			return false, err
		}
		return false, nil
	}
	screen, err := rt.disarmExitMenu(ctx, st)
	if err != nil {
		return false, err
	}
	switch input {
	case modules.ExitMenuKeepTrying:
		// Re-emite la pantalla tal cual. El contador ya lo reinició el módulo al armar.
		return true, rt.resendScreen(ctx, key, sessionID, screen)
	case modules.ExitMenuStop:
		// Es EXACTAMENTE el event_stop de D-043.2: resumen del abandono, puntero
		// apagado, status intacto en `open`, y confirmación POR NOMBRE DE TIPO sin
		// identificador (stopNotice, events.go:614). No se duplica ni una línea.
		return true, rt.stopEvent(ctx, key, sessionID, st)
	default: // modules.ExitMenuDispatcher
		// El menú es un evento más (D-043.3). gestureGoTo y no gestureNew: «ver el
		// menú» es ir al menú, no empezar uno nuevo, y solo el gesto «nuevo» puede
		// cerrar un vencido (E-11).
		dec := trigger.Decision{Action: trigger.StartEvent, EventKind: trigger.EventKindMenu}
		// El `event_id` que beginEvent devuelve desde el Plan 044 se DESCARTA aquí: lo
		// que el cliente tecleó es una opción del menú de SALIDA, no un pedido, y el
		// evento al que se va es el `menu`. Anclar ahí una ventana de captación pondría
		// un número suelto de primera referencia y como base de fechas (D-044.9).
		// openingTurn{} por lo mismo y en el mismo acto: ese número tampoco abre el hilo
		// del `menu`, que además ni siquiera arranca flujo (Plan 044 · T1.4).
		_, done, berr := rt.beginEvent(ctx, key, sessionID, dec, gestureGoTo, st.EventID, openingTurn{})
		return done, berr
	}
}

// disarmExitMenu borra la marca del estado EN MEMORIA y lo persiste, devolviendo la
// pantalla guardada. Muta st.Vars, que es un mapa (tipo referencia): el llamante y
// quien reciba st después ven el borrado.
func (rt *Runtime) disarmExitMenu(ctx context.Context, st model.Conversation) (string, error) {
	screen := modules.DisarmExitMenu(st.Vars)
	if err := rt.store.Save(ctx, st); err != nil {
		return "", fmt.Errorf("runtime: desarmar el menú de salida: %w", err)
	}
	return screen, nil
}

// resendScreen re-emite la pantalla del nodo donde el cliente se atascó («1) Seguir
// intentando»). Respeta la red anti-loop: agotado el cupo, el turno se consume sin
// responder, igual que cualquier otra auto-respuesta (Plan 020 · T0).
func (rt *Runtime) resendScreen(ctx context.Context, key store.Key, sessionID, screen string) error {
	if screen == "" || !rt.replyAllowed(key) {
		return nil
	}
	to, err := rt.destination(ctx, key.TenantID, key.ContactID)
	if err != nil {
		return err
	}
	_, err = rt.send(ctx, sessionID, to, key, []engine.Output{{Text: screen}})
	return err
}
